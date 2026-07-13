#include <pthread.h>
#include <stdarg.h>
#include <stdatomic.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "log_capture.h"
#include "log_capture_esp.h"

void __wrap_esp_log(esp_log_config_t config, const char *tag, const char *format, ...);

static int s_failures;
static atomic_bool s_block_capture_format;
static atomic_bool s_detach_returned;
static atomic_bool s_detach_result;
static atomic_uint s_ticks;
static atomic_uint s_forward_count;
static pthread_barrier_t s_format_entered;
static pthread_barrier_t s_allow_format;
static pthread_barrier_t s_detach_waiting;
static pthread_barrier_t s_allow_detach_delay;

int log_capture_esp_test_vsnprintf(char *buffer, size_t capacity,
                                   const char *format, va_list args)
{
    if (atomic_exchange_explicit(&s_block_capture_format, false, memory_order_seq_cst)) {
        (void)pthread_barrier_wait(&s_format_entered);
        (void)pthread_barrier_wait(&s_allow_format);
    }
    return vsnprintf(buffer, capacity, format, args);
}

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (message)); \
        s_failures++; \
        return; \
    } \
} while (0)

static esp_log_config_t make_config(esp_log_level_t level)
{
    esp_log_config_t config = { .data = 0 };
    config.opts.log_level = level;
    return config;
}

void esp_log_va(esp_log_config_t config, const char *tag, const char *format, va_list args)
{
    (void)config;
    (void)tag;
    char message[LOG_CAPTURE_MESSAGE_MAX];
    (void)vsnprintf(message, sizeof(message), format, args);
    atomic_fetch_add_explicit(&s_forward_count, 1, memory_order_relaxed);
}

bool esp_log_util_is_constrained(void)
{
    return false;
}

int64_t esp_timer_get_time(void)
{
    return 1234;
}

BaseType_t xPortInIsrContext(void)
{
    return 0;
}

TickType_t xTaskGetTickCount(void)
{
    return atomic_load_explicit(&s_ticks, memory_order_relaxed);
}

void host_task_yield(void)
{
}

void vTaskDelay(TickType_t ticks)
{
    (void)pthread_barrier_wait(&s_detach_waiting);
    (void)pthread_barrier_wait(&s_allow_detach_delay);
    atomic_fetch_add_explicit(&s_ticks, ticks, memory_order_relaxed);
}

static void *wrapper_thread(void *arg)
{
    (void)arg;
    __wrap_esp_log(make_config(ESP_LOG_INFO), "RACE", "reader=%d", 7);
    return NULL;
}

static void *detach_thread(void *arg)
{
    (void)arg;
    bool detached = log_capture_esp_detach();
    atomic_store_explicit(&s_detach_result, detached, memory_order_seq_cst);
    atomic_store_explicit(&s_detach_returned, true, memory_order_seq_cst);
    return NULL;
}

static void test_real_wrapper_reader_and_detach_quiesce(void)
{
    log_capture_entry_t storage[2];
    log_capture_entry_t entry;
    log_capture_t capture;
    log_capture_init(&capture, storage, 2, LOG_CAPTURE_LEVEL_VERBOSE);
    log_capture_esp_attach(&capture);

    atomic_store_explicit(&s_ticks, 0, memory_order_relaxed);
    atomic_store_explicit(&s_forward_count, 0, memory_order_relaxed);
    atomic_store_explicit(&s_detach_returned, false, memory_order_seq_cst);
    atomic_store_explicit(&s_detach_result, false, memory_order_seq_cst);
    atomic_store_explicit(&s_block_capture_format, true, memory_order_seq_cst);
    CHECK(pthread_barrier_init(&s_format_entered, NULL, 2) == 0, "format barrier init failed");
    CHECK(pthread_barrier_init(&s_allow_format, NULL, 2) == 0, "format release barrier init failed");
    CHECK(pthread_barrier_init(&s_detach_waiting, NULL, 2) == 0, "detach barrier init failed");
    CHECK(pthread_barrier_init(&s_allow_detach_delay, NULL, 2) == 0, "detach release barrier init failed");

    pthread_t reader;
    pthread_t detacher;
    CHECK(pthread_create(&reader, NULL, wrapper_thread, NULL) == 0, "reader thread create failed");
    (void)pthread_barrier_wait(&s_format_entered);
    CHECK(pthread_create(&detacher, NULL, detach_thread, NULL) == 0, "detach thread create failed");
    (void)pthread_barrier_wait(&s_detach_waiting);

    CHECK(!atomic_load_explicit(&s_detach_returned, memory_order_seq_cst),
          "detach returned while a reader held the loaded capture pointer");
    (void)pthread_barrier_wait(&s_allow_format);
    CHECK(pthread_join(reader, NULL) == 0, "reader thread join failed");
    (void)pthread_barrier_wait(&s_allow_detach_delay);
    CHECK(pthread_join(detacher, NULL) == 0, "detach thread join failed");

    CHECK(atomic_load_explicit(&s_detach_returned, memory_order_seq_cst) &&
          atomic_load_explicit(&s_detach_result, memory_order_seq_cst),
          "detach did not complete after reader quiescence");
    CHECK(atomic_load_explicit(&s_forward_count, memory_order_relaxed) == 1,
          "concurrent wrapper call was not forwarded exactly once");
    CHECK(log_capture_drain(&capture, &entry, 1) == 1,
          "reader that loaded before detach must finish its capture safely");
    CHECK(strcmp(entry.message, "reader=7") == 0, "concurrent capture was corrupted");

    __wrap_esp_log(make_config(ESP_LOG_INFO), "RACE", "after detach");
    CHECK(log_capture_drain(&capture, &entry, 1) == 0,
          "wrapper call after detach must not enter the capture");

    (void)pthread_barrier_destroy(&s_format_entered);
    (void)pthread_barrier_destroy(&s_allow_format);
    (void)pthread_barrier_destroy(&s_detach_waiting);
    (void)pthread_barrier_destroy(&s_allow_detach_delay);
}

typedef struct {
    log_capture_t *capture;
    pthread_barrier_t *start;
    atomic_uint *accepted;
    atomic_uint *drained;
} ring_thread_ctx_t;

static void *push_thread(void *arg)
{
    ring_thread_ctx_t *ctx = arg;
    (void)pthread_barrier_wait(ctx->start);
    for (unsigned i = 0; i < 20000; ++i) {
        char message[32];
        (void)snprintf(message, sizeof(message), "push-%u", i);
        if (log_capture_push_line(ctx->capture, LOG_CAPTURE_LEVEL_INFO, i, "PUSH", message)) {
            atomic_fetch_add_explicit(ctx->accepted, 1, memory_order_relaxed);
        }
    }
    return NULL;
}

static void *drain_thread(void *arg)
{
    ring_thread_ctx_t *ctx = arg;
    log_capture_entry_t entries[4];
    (void)pthread_barrier_wait(ctx->start);
    for (unsigned i = 0; i < 20000; ++i) {
        atomic_fetch_add_explicit(ctx->drained,
                                  log_capture_drain(ctx->capture, entries, 4),
                                  memory_order_relaxed);
    }
    return NULL;
}

static void test_push_and_drain_basic_contention_accounting(void)
{
    log_capture_entry_t storage[8];
    log_capture_entry_t remaining[8];
    log_capture_t capture;
    log_capture_init(&capture, storage, 8, LOG_CAPTURE_LEVEL_VERBOSE);

    pthread_barrier_t start;
    atomic_uint accepted = ATOMIC_VAR_INIT(0);
    atomic_uint drained = ATOMIC_VAR_INIT(0);
    CHECK(pthread_barrier_init(&start, NULL, 3) == 0, "ring start barrier init failed");
    ring_thread_ctx_t ctx = {
        .capture = &capture,
        .start = &start,
        .accepted = &accepted,
        .drained = &drained,
    };
    pthread_t producer;
    pthread_t consumer;
    CHECK(pthread_create(&producer, NULL, push_thread, &ctx) == 0, "producer create failed");
    CHECK(pthread_create(&consumer, NULL, drain_thread, &ctx) == 0, "consumer create failed");
    (void)pthread_barrier_wait(&start);
    CHECK(pthread_join(producer, NULL) == 0, "producer join failed");
    CHECK(pthread_join(consumer, NULL) == 0, "consumer join failed");

    size_t final_count = log_capture_drain(&capture, remaining, 8);
    unsigned accepted_count = atomic_load_explicit(&accepted, memory_order_relaxed);
    unsigned drained_count = atomic_load_explicit(&drained, memory_order_relaxed);
    unsigned overwritten = log_capture_dropped_oldest(&capture);
    CHECK(accepted_count == drained_count + final_count + overwritten,
          "accepted pushes must equal drained, remaining, and overwritten entries");
    CHECK(accepted_count > 0 && drained_count + final_count > 0,
          "push/drain contention test did not exercise useful work");
    (void)pthread_barrier_destroy(&start);
}

int main(void)
{
    test_real_wrapper_reader_and_detach_quiesce();
    test_push_and_drain_basic_contention_accounting();

    if (s_failures != 0) {
        fprintf(stderr, "%d concurrency test(s) failed\n", s_failures);
        return 1;
    }
    puts("log_capture_concurrency_tests: all tests passed");
    return 0;
}
