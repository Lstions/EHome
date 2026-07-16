#include <stdarg.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <pthread.h>
#include <stdatomic.h>

#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/task.h"
#include "log_capture.h"
#include "log_capture_esp.h"
#include "log_stream.h"
#include "log_stream_codec.h"

static int s_failures;
static bool s_create_succeeds;
static bool s_detach_succeeds;
static bool s_run_task_on_notify;
static bool s_run_task_on_next_notify;
static bool s_wait_times_out;
static unsigned s_create_calls;
static unsigned s_notify_calls;
static unsigned s_delete_calls;
static unsigned s_attach_calls;
static unsigned s_detach_calls;
static unsigned s_esp_log_count;
static TickType_t s_ticks;
static EventBits_t s_event_bits;
static TaskFunction_t s_created_task_fn;
static void *s_created_task_arg;
static log_capture_t *s_attached_capture;
static int s_task_token;
static atomic_bool s_block_level_update;
static pthread_barrier_t s_level_entered;
static pthread_barrier_t s_allow_level_update;
static pthread_barrier_t s_stop_waiting_for_user;
static pthread_barrier_t s_allow_stop_delay;

void __real_log_capture_set_level(log_capture_t *capture, uint8_t level);

void __wrap_log_capture_set_level(log_capture_t *capture, uint8_t level)
{
    if (atomic_exchange_explicit(&s_block_level_update, false, memory_order_seq_cst)) {
        (void)pthread_barrier_wait(&s_level_entered);
        (void)pthread_barrier_wait(&s_allow_level_update);
    }
    __real_log_capture_set_level(capture, level);
}

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (message)); \
        s_failures++; \
        return; \
    } \
} while (0)

static void reset_stubs(void)
{
    if (log_stream_is_active()) {
        s_run_task_on_notify = true;
        s_wait_times_out = false;
        log_stream_stop();
    }
    s_create_succeeds = true;
    s_detach_succeeds = true;
    s_run_task_on_notify = false;
    s_run_task_on_next_notify = false;
    s_wait_times_out = false;
    s_create_calls = 0;
    s_notify_calls = 0;
    s_delete_calls = 0;
    s_attach_calls = 0;
    s_detach_calls = 0;
    s_esp_log_count = 0;
    s_ticks = 0;
    s_event_bits = 0;
    s_created_task_fn = NULL;
    s_created_task_arg = NULL;
    s_attached_capture = NULL;
}

void host_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level;
    (void)tag;
    s_esp_log_count++;
    va_list args;
    va_start(args, format);
    va_end(args);
}

int64_t esp_timer_get_time(void)
{
    return 42;
}

void esp_task_wdt_add(void *task) { (void)task; }
void esp_task_wdt_reset(void) {}
void esp_task_wdt_delete(void *task) { (void)task; }

EventGroupHandle_t xEventGroupCreateStatic(StaticEventGroup_t *storage)
{
    return storage;
}

EventBits_t xEventGroupClearBits(EventGroupHandle_t group, EventBits_t bits)
{
    (void)group;
    EventBits_t before = s_event_bits;
    s_event_bits &= ~bits;
    return before;
}

EventBits_t xEventGroupSetBits(EventGroupHandle_t group, EventBits_t bits)
{
    (void)group;
    s_event_bits |= bits;
    return s_event_bits;
}

EventBits_t xEventGroupWaitBits(EventGroupHandle_t group, EventBits_t bits,
                                BaseType_t clear_on_exit, BaseType_t wait_for_all,
                                TickType_t wait_ticks)
{
    (void)group;
    (void)wait_for_all;
    (void)wait_ticks;
    EventBits_t result = s_wait_times_out ? 0 : (s_event_bits & bits);
    if (clear_on_exit) {
        s_event_bits &= ~bits;
    }
    return result;
}

BaseType_t xTaskCreate(TaskFunction_t task, const char *name, uint32_t stack_depth,
                       void *arg, unsigned priority, TaskHandle_t *out_task)
{
    (void)name;
    (void)stack_depth;
    if (priority != 2) {
        fprintf(stderr, "FAIL xTaskCreate: log_tx must remain below MQTT/control task priority\n");
        s_failures++;
        *out_task = NULL;
        return 0;
    }
    s_create_calls++;
    s_created_task_fn = task;
    s_created_task_arg = arg;
    if (!s_create_succeeds) {
        *out_task = NULL;
        return 0;
    }
    *out_task = &s_task_token;
    return pdPASS;
}

uint32_t ulTaskNotifyTake(BaseType_t clear_on_exit, TickType_t wait_ticks)
{
    (void)clear_on_exit;
    (void)wait_ticks;
    return 1;
}

void xTaskNotifyGive(TaskHandle_t task)
{
    (void)task;
    s_notify_calls++;
    if (s_run_task_on_next_notify) {
        s_run_task_on_notify = true;
        s_run_task_on_next_notify = false;
    }
    if (s_run_task_on_notify && s_created_task_fn != NULL) {
        TaskFunction_t fn = s_created_task_fn;
        s_created_task_fn = NULL;
        fn(s_created_task_arg);
    }
}

TickType_t xTaskGetTickCount(void) { return s_ticks; }
void host_task_yield(void) {}
void vTaskDelay(TickType_t ticks)
{
    if (!log_stream_is_active()) {
        (void)pthread_barrier_wait(&s_stop_waiting_for_user);
        (void)pthread_barrier_wait(&s_allow_stop_delay);
    }
    s_ticks += ticks;
}
void vTaskDelete(TaskHandle_t task) { (void)task; s_delete_calls++; }

void log_capture_esp_attach(log_capture_t *capture)
{
    s_attach_calls++;
    s_attached_capture = capture;
}

bool log_capture_esp_detach(void)
{
    s_detach_calls++;
    s_attached_capture = NULL;
    return s_detach_succeeds;
}

int log_stream_encode(uint8_t *buf, size_t capacity, size_t *out_len,
                      uint16_t sequence, const log_stream_entry_t *entries,
                      size_t entry_count)
{
    (void)sequence;
    (void)entries;
    (void)entry_count;
    if (capacity > 0) {
        buf[0] = 0;
        *out_len = 1;
    }
    return 0;
}

static void publish_probe(const uint8_t *data, size_t len)
{
    (void)data;
    (void)len;
}

static void test_start_success_and_running_start_updates_without_second_task(void)
{
    reset_stubs();
    log_stream_set_publish_callback(publish_probe);
    log_stream_start(LOG_LEVEL_INFO);

    CHECK(log_stream_is_active(), "successful start must publish RUNNING state");
    CHECK(s_create_calls == 1 && s_attach_calls == 1,
          "successful start must create one worker and attach one capture");
    CHECK(s_notify_calls == 1, "worker start gate must be released once");

    log_stream_start(LOG_LEVEL_VERBOSE);
    CHECK(s_create_calls == 1, "start while RUNNING must not create another task");

    log_stream_emit(LOG_LEVEL_VERBOSE, "TEST", "probe");
    log_capture_entry_t entry;
    CHECK(s_attached_capture != NULL &&
          log_capture_drain(s_attached_capture, &entry, 1) >= 1,
          "RUNNING start must update the existing capture level");

    s_run_task_on_next_notify = true;
    log_stream_stop();
    CHECK(!log_stream_is_active(), "cooperative worker completion must reach STOPPED");
    CHECK(s_delete_calls == 1, "only worker self-delete should be invoked");
}

static void test_start_failure_detaches_and_returns_stopped(void)
{
    reset_stubs();
    s_create_succeeds = false;
    CHECK(log_stream_start(LOG_LEVEL_INFO) == ESP_FAIL,
          "task creation failure must be reported to the caller");

    CHECK(!log_stream_is_active(), "task creation failure must restore STOPPED");
    CHECK(s_create_calls == 1, "task creation failure path was not exercised");
    CHECK(s_attach_calls == 1 && s_detach_calls == 2,
          "failed start must detach the initialized capture");
    CHECK(s_notify_calls == 0, "failed task must not be notified");
}

static void test_stop_timeout_stays_stopping_and_blocks_restart(void)
{
    reset_stubs();
    log_stream_start(LOG_LEVEL_INFO);
    CHECK(log_stream_is_active(), "precondition start failed");

    s_wait_times_out = true;
    s_run_task_on_notify = false;
    log_stream_stop();
    CHECK(!log_stream_is_active(), "STOPPING must not report active");
    CHECK(s_delete_calls == 0, "timeout must never force-delete the worker");

    unsigned creates_before = s_create_calls;
    log_stream_start(LOG_LEVEL_DEBUG);
    CHECK(s_create_calls == creates_before, "start must not resurrect a STOPPING worker");

    s_run_task_on_notify = false;
    s_run_task_on_next_notify = true;
    xTaskNotifyGive(&s_task_token);
    CHECK(!log_stream_is_active(), "late worker completion must settle at STOPPED");

    s_run_task_on_notify = false;
    s_wait_times_out = false;
    log_stream_start(LOG_LEVEL_WARN);
    CHECK(log_stream_is_active(), "restart should work only after cooperative completion");
    s_run_task_on_next_notify = true;
    log_stream_stop();
}

static void test_start_emits_only_real_esp_log(void)
{
    reset_stubs();
    log_stream_start(LOG_LEVEL_INFO);

    CHECK(s_esp_log_count == 1, "start should emit exactly one real ESP log");
    log_capture_entry_t entry;
    CHECK(s_attached_capture != NULL &&
          log_capture_drain(s_attached_capture, &entry, 1) == 0,
          "start must not enqueue the obsolete explicit remote-start duplicate");

    s_run_task_on_next_notify = true;
    log_stream_stop();
}

static void *set_level_thread(void *arg)
{
    (void)arg;
    log_stream_set_level(LOG_LEVEL_VERBOSE);
    return NULL;
}

static void *stop_thread(void *arg)
{
    (void)arg;
    log_stream_stop();
    return NULL;
}

static void test_stop_waits_for_inflight_set_level_and_blocks_restart(void)
{
    reset_stubs();
    log_stream_start(LOG_LEVEL_INFO);
    CHECK(log_stream_is_active(), "precondition start failed");
    CHECK(pthread_barrier_init(&s_level_entered, NULL, 2) == 0,
          "level-entered barrier init failed");
    CHECK(pthread_barrier_init(&s_allow_level_update, NULL, 2) == 0,
          "level-release barrier init failed");
    CHECK(pthread_barrier_init(&s_stop_waiting_for_user, NULL, 2) == 0,
          "stop-wait barrier init failed");
    CHECK(pthread_barrier_init(&s_allow_stop_delay, NULL, 2) == 0,
          "stop-release barrier init failed");
    atomic_store_explicit(&s_block_level_update, true, memory_order_seq_cst);

    pthread_t setter;
    pthread_t stopper;
    CHECK(pthread_create(&setter, NULL, set_level_thread, NULL) == 0,
          "set-level thread create failed");
    (void)pthread_barrier_wait(&s_level_entered);

    s_run_task_on_notify = true;
    CHECK(pthread_create(&stopper, NULL, stop_thread, NULL) == 0,
          "stop thread create failed");
    (void)pthread_barrier_wait(&s_stop_waiting_for_user);
    CHECK(!log_stream_is_active(), "stop must publish STOPPING before waiting for set-level");
    unsigned creates_before = s_create_calls;
    log_stream_start(LOG_LEVEL_WARN);
    CHECK(s_create_calls == creates_before,
          "start must not reinitialize storage while set-level still owns capture");

    (void)pthread_barrier_wait(&s_allow_level_update);
    CHECK(pthread_join(setter, NULL) == 0, "set-level thread join failed");
    (void)pthread_barrier_wait(&s_allow_stop_delay);
    CHECK(pthread_join(stopper, NULL) == 0, "stop thread join failed");
    CHECK(!log_stream_is_active(), "stop must settle after in-flight set-level leaves");
    CHECK(s_delete_calls == 1, "worker must exit cooperatively after set-level quiescence");

    (void)pthread_barrier_destroy(&s_level_entered);
    (void)pthread_barrier_destroy(&s_allow_level_update);
    (void)pthread_barrier_destroy(&s_stop_waiting_for_user);
    (void)pthread_barrier_destroy(&s_allow_stop_delay);
}

int main(void)
{
    test_start_success_and_running_start_updates_without_second_task();
    test_start_failure_detaches_and_returns_stopped();
    test_stop_timeout_stays_stopping_and_blocks_restart();
    test_start_emits_only_real_esp_log();
    test_stop_waits_for_inflight_set_level_and_blocks_restart();

    if (s_failures != 0) {
        fprintf(stderr, "%d lifecycle test(s) failed\n", s_failures);
        return 1;
    }
    printf("log_stream_lifecycle_tests: all tests passed\n");
    return 0;
}
