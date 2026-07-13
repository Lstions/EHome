/**
 * @file log_stream.c
 * @brief ESP32 native log capture and remote stream implementation.
 *
 * Architecture: IDF v6 esp_log link wrapper -> one bounded log_capture ring ->
 * log_tx_task -> MQTT MsgLogStream (0x1D). Explicit log_stream_emit() calls use
 * the same capture ring as native ESP_LOG calls.
 */

#include "log_stream.h"
#include "log_stream_codec.h"
#include "log_capture.h"
#include "log_capture_esp.h"
#include "frame_codec.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "esp_task_wdt.h"
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/task.h"
#include <stdarg.h>
#include <stdatomic.h>

#define TAG "LOG_STREAM"

#define LOG_BATCH_MAX       4
#define LOG_TX_INTERVAL_MS  200
#define LOG_TX_STACK        1536
#define LOG_TX_PRIO         5
#define LOG_TX_BUF_SIZE     768
#define RING_CAPACITY       4
#define LOG_TASK_EXITED_BIT BIT0
#define LOG_TASK_STOP_TIMEOUT_MS 1000
#define LOG_CAPTURE_USER_TIMEOUT_MS 100

typedef enum {
    LOG_STREAM_STOPPED = 0,
    LOG_STREAM_STARTING,
    LOG_STREAM_RUNNING,
    LOG_STREAM_STOPPING,
} log_stream_state_t;

/* Capture storage is static so a wrapper that was already in flight at detach
 * can never observe freed memory. log_capture_esp_detach() also waits for such
 * readers before stop returns or a subsequent start reinitializes the ring. */
static log_capture_t s_capture;
static log_capture_entry_t s_ring[RING_CAPACITY];
static log_capture_entry_t s_tx_batch[LOG_BATCH_MAX];
static uint8_t s_tx_buf[LOG_TX_BUF_SIZE];

static _Atomic(TaskHandle_t) s_task;
static atomic_uint s_state;
static atomic_uint s_capture_users;
static uint16_t s_seq;
static _Atomic(log_stream_publish_fn_t) s_publish;
static StaticEventGroup_t s_task_events_storage;
static EventGroupHandle_t s_task_events;

/* Compile-time gate for firmware-owned known storage plus configured task stack.
 * Static ring/TX/control storage is resident even while disabled. This is only a
 * lower-bound accounting gate: dynamically allocated TCB, allocator metadata,
 * per-task TLS, and target runtime overhead require C6/S3 hardware measurement. */
#define LOG_STREAM_OWNED_RAM_BUDGET_BYTES 4096U
#define LOG_STREAM_OWNED_RAM_BYTES ( \
    sizeof(s_capture) + sizeof(s_ring) + sizeof(s_tx_batch) + sizeof(s_tx_buf) + \
    LOG_TX_STACK + sizeof(s_task) + sizeof(s_state) + sizeof(s_capture_users) + \
    sizeof(s_seq) + sizeof(s_publish) + sizeof(s_task_events_storage) + \
    sizeof(s_task_events))
_Static_assert(LOG_STREAM_OWNED_RAM_BYTES <= LOG_STREAM_OWNED_RAM_BUDGET_BYTES,
               "log_stream firmware-owned static/stack/control RAM exceeds budget");

static uint8_t bounded_level(uint8_t level)
{
    return level > LOG_LEVEL_VERBOSE ? LOG_LEVEL_VERBOSE : level;
}

static void maybe_finish_stop(void)
{
    if (atomic_load_explicit(&s_task, memory_order_seq_cst) == NULL &&
        atomic_load_explicit(&s_capture_users, memory_order_seq_cst) == 0) {
        unsigned expected = LOG_STREAM_STOPPING;
        (void)atomic_compare_exchange_strong_explicit(
            &s_state, &expected, LOG_STREAM_STOPPED,
            memory_order_seq_cst, memory_order_seq_cst);
    }
}

static bool capture_user_enter(void)
{
    atomic_fetch_add_explicit(&s_capture_users, 1, memory_order_seq_cst);
    if (atomic_load_explicit(&s_state, memory_order_seq_cst) != LOG_STREAM_RUNNING) {
        atomic_fetch_sub_explicit(&s_capture_users, 1, memory_order_seq_cst);
        maybe_finish_stop();
        return false;
    }
    return true;
}

static void capture_user_leave(void)
{
    atomic_fetch_sub_explicit(&s_capture_users, 1, memory_order_seq_cst);
    maybe_finish_stop();
}

static void log_tx_task(void *pv)
{
    (void)pv;
    /* Creation can schedule this task on the other S3 core before start has
     * published its handle/state. The creator releases this one-shot gate only
     * after both are visible. */
    (void)ulTaskNotifyTake(pdTRUE, portMAX_DELAY);
    esp_task_wdt_add(NULL);

    while (atomic_load_explicit(&s_state, memory_order_acquire) == LOG_STREAM_RUNNING) {
        esp_task_wdt_reset();

        size_t count = log_capture_drain(&s_capture, s_tx_batch, LOG_BATCH_MAX);
        if (count > 0) {
            log_stream_entry_t entries[LOG_BATCH_MAX];
            for (size_t i = 0; i < count; ++i) {
                entries[i] = (log_stream_entry_t){
                    .level = s_tx_batch[i].level,
                    .timestamp_us = s_tx_batch[i].timestamp_us,
                    .tag = s_tx_batch[i].tag,
                    .message = s_tx_batch[i].message,
                };
            }

            size_t encoded_len = 0;
            log_stream_publish_fn_t publish =
                atomic_load_explicit(&s_publish, memory_order_acquire);
            if (log_stream_encode(s_tx_buf, sizeof(s_tx_buf), &encoded_len,
                                  s_seq++, entries, count) == FRAME_OK &&
                publish != NULL) {
                /* MQTT publish paths log internally. Keep those diagnostics out
                 * of this same ring to prevent a publish -> log -> publish loop. */
                log_capture_suppress(&s_capture);
                publish(s_tx_buf, encoded_len);
                log_capture_resume(&s_capture);
            }
        }

        /* Stop wakes this wait immediately instead of waiting for the full TX
         * interval. Notifications are private to this worker task. */
        (void)ulTaskNotifyTake(pdTRUE, pdMS_TO_TICKS(LOG_TX_INTERVAL_MS));
    }

    esp_task_wdt_delete(NULL);
    atomic_store_explicit(&s_task, NULL, memory_order_seq_cst);
    xEventGroupSetBits(s_task_events, LOG_TASK_EXITED_BIT);
    maybe_finish_stop();
    vTaskDelete(NULL);
}

void log_stream_set_publish_callback(log_stream_publish_fn_t publish)
{
    atomic_store_explicit(&s_publish, publish, memory_order_release);
}

void log_stream_start(uint8_t level)
{
    level = bounded_level(level);
    unsigned expected = LOG_STREAM_STOPPED;
    if (!atomic_compare_exchange_strong_explicit(
            &s_state, &expected, LOG_STREAM_STARTING,
            memory_order_acq_rel, memory_order_acquire)) {
        if (expected == LOG_STREAM_RUNNING) {
            log_stream_set_level(level);
        }
        /* STARTING/STOPPING are short, owned lifecycle transitions. A second
         * caller must not race initialization or resurrect a stopping task. */
        return;
    }

    if (s_task_events == NULL) {
        s_task_events = xEventGroupCreateStatic(&s_task_events_storage);
    }
    if (s_task_events == NULL ||
        atomic_load_explicit(&s_task, memory_order_acquire) != NULL) {
        atomic_store_explicit(&s_state, LOG_STREAM_STOPPED, memory_order_release);
        return;
    }

    /* Complete a previous bounded detach before reinitializing static storage. */
    if (!log_capture_esp_detach()) {
        atomic_store_explicit(&s_state, LOG_STREAM_STOPPED, memory_order_release);
        return;
    }

    xEventGroupClearBits(s_task_events, LOG_TASK_EXITED_BIT);
    log_capture_init(&s_capture, s_ring, RING_CAPACITY, level);
    s_seq = 0;
    log_capture_esp_attach(&s_capture);

    TaskHandle_t task = NULL;
    BaseType_t ret = xTaskCreate(log_tx_task, "log_tx", LOG_TX_STACK,
                                 NULL, LOG_TX_PRIO, &task);
    if (ret != pdPASS) {
        (void)log_capture_esp_detach();
        atomic_store_explicit(&s_task, NULL, memory_order_release);
        atomic_store_explicit(&s_state, LOG_STREAM_STOPPED, memory_order_release);
        ESP_LOGE(TAG, "Failed to create log_tx_task");
        return;
    }
    atomic_store_explicit(&s_task, task, memory_order_release);
    atomic_store_explicit(&s_state, LOG_STREAM_RUNNING, memory_order_release);
    xTaskNotifyGive(task);

    ESP_LOGI(TAG, "Started (level=%u, ring=%u entries)", level, RING_CAPACITY);
}

void log_stream_stop(void)
{
    unsigned expected = LOG_STREAM_RUNNING;
    if (!atomic_compare_exchange_strong_explicit(
            &s_state, &expected, LOG_STREAM_STOPPING,
            memory_order_acq_rel, memory_order_acquire)) {
        return;
    }

    if (!log_capture_esp_detach()) {
        /* Pointer is already detached. Static storage remains untouched and a
         * later start retries reader quiescence before reinitializing it. */
        ESP_LOGW(TAG, "native log readers did not quiesce before timeout");
    }

    const TickType_t capture_start = xTaskGetTickCount();
    TickType_t capture_timeout = pdMS_TO_TICKS(LOG_CAPTURE_USER_TIMEOUT_MS);
    if (capture_timeout == 0) {
        capture_timeout = 1;
    }
    while (atomic_load_explicit(&s_capture_users, memory_order_seq_cst) != 0) {
        if ((TickType_t)(xTaskGetTickCount() - capture_start) >= capture_timeout) {
            ESP_LOGW(TAG, "explicit log producers did not quiesce before timeout");
            break;
        }
        taskYIELD();
        vTaskDelay(1);
    }

    TaskHandle_t task = atomic_load_explicit(&s_task, memory_order_acquire);
    if (task != NULL) {
        xTaskNotifyGive(task);
        EventBits_t bits = xEventGroupWaitBits(
            s_task_events, LOG_TASK_EXITED_BIT, pdFALSE, pdTRUE,
            pdMS_TO_TICKS(LOG_TASK_STOP_TIMEOUT_MS));
        if ((bits & LOG_TASK_EXITED_BIT) == 0) {
            /* Never force-delete: the worker may be inside publish. It owns its
             * exit and will move STOPPING -> STOPPED after publish returns. */
            ESP_LOGW(TAG, "log_tx_task stop timed out; awaiting cooperative exit");
            return;
        }
        /* The exit bit is set after s_task becomes NULL. Complete the state
         * transition here as well so stop never returns while still STOPPING. */
        maybe_finish_stop();
    } else {
        maybe_finish_stop();
    }

    ESP_LOGI(TAG, "Stopped");
}

void log_stream_set_level(uint8_t level)
{
    if (!capture_user_enter()) {
        return;
    }
    level = bounded_level(level);
    log_capture_set_level(&s_capture, level);
    capture_user_leave();
    log_stream_emit(LOG_LEVEL_INFO, TAG, "remote level set=%u", level);
}

bool log_stream_is_active(void)
{
    return atomic_load_explicit(&s_state, memory_order_acquire) == LOG_STREAM_RUNNING;
}

void log_stream_emit(uint8_t level, const char *tag, const char *fmt, ...)
{
    if (tag == NULL || fmt == NULL || !capture_user_enter()) {
        return;
    }

    va_list args;
    va_start(args, fmt);
    (void)log_capture_pushv(&s_capture, level, (uint64_t)esp_timer_get_time(),
                            tag, fmt, args);
    va_end(args);
    capture_user_leave();
}
