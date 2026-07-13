#include "log_capture_esp.h"

#include <stdio.h>

#include "sdkconfig.h"
#include "esp_attr.h"
#include "esp_log.h"
#include "esp_private/log_util.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#ifdef LOG_CAPTURE_ESP_HOST_TEST
#include "log_capture_esp_test.h"
#define LOG_CAPTURE_VSNPRINTF log_capture_esp_test_vsnprintf
#else
#define LOG_CAPTURE_VSNPRINTF vsnprintf
#endif

#if !defined(CONFIG_LOG_VERSION_2) || CONFIG_LOG_VERSION != 2
#error "log_stream native capture requires ESP-IDF Log V2"
#endif

_Static_assert(CONFIG_LOG_MAXIMUM_LEVEL >= ESP_LOG_VERBOSE,
               "log_stream native capture requires maximum log level VERBOSE");

#define LOG_CAPTURE_DETACH_TIMEOUT_MS 100

static _Atomic(log_capture_t *) s_capture;
static atomic_uint s_wrapper_readers;
static _Thread_local bool s_in_wrapper;

static uint8_t remote_level(esp_log_level_t level)
{
    return level > ESP_LOG_NONE ? (uint8_t)(level - 1) : LOG_CAPTURE_LEVEL_VERBOSE + 1;
}

void log_capture_esp_attach(log_capture_t *capture)
{
    /* Callers first complete detach and initialize the static capture storage.
     * A wrapper which starts now may safely observe either NULL or this fully
     * initialized capture, so attaching need not wait for transient readers. */
    atomic_store_explicit(&s_capture, capture, memory_order_seq_cst);
}

bool log_capture_esp_detach(void)
{
    if (xPortInIsrContext()) {
        return false;
    }

    atomic_store_explicit(&s_capture, NULL, memory_order_seq_cst);
    /* A wrapper may have loaded the old pointer immediately before detach.
     * Sequential consistency closes the increment/store/load race across the
     * two atomics. Yield and delay so a preempted reader can run on single-core
     * ESP32-C6; never monopolize the CPU with an unbounded spin. */
    const TickType_t start = xTaskGetTickCount();
    TickType_t timeout = pdMS_TO_TICKS(LOG_CAPTURE_DETACH_TIMEOUT_MS);
    if (timeout == 0) {
        timeout = 1;
    }
    while (atomic_load_explicit(&s_wrapper_readers, memory_order_seq_cst) != 0) {
        if ((TickType_t)(xTaskGetTickCount() - start) >= timeout) {
            return false;
        }
        taskYIELD();
        vTaskDelay(1);
    }
    return true;
}

void IRAM_ATTR __wrap_esp_log(esp_log_config_t config, const char *tag, const char *format, ...)
{
    va_list args;
    va_start(args, format);

    /* IDF's configured constrained bit is already authoritative. In that path,
     * do not call the runtime helper or touch capture state/TLS at all. */
    if (config.opts.constrained_env) {
        esp_log_va(config, tag, format, args);
        va_end(args);
        return;
    }
    /* This check must precede reader, capture, and TLS access. The original IDF
     * backend remains responsible for constrained-context forwarding. */
    if (esp_log_util_is_constrained()) {
        esp_log_va(config, tag, format, args);
        va_end(args);
        return;
    }

    atomic_fetch_add_explicit(&s_wrapper_readers, 1, memory_order_seq_cst);
    log_capture_t *capture = atomic_load_explicit(&s_capture, memory_order_seq_cst);
    bool owns_recursion_guard = false;
    if (capture != NULL && !s_in_wrapper && !config.opts.binary_mode) {
        uint8_t level = remote_level(config.opts.log_level);
        if (level <= LOG_CAPTURE_LEVEL_VERBOSE &&
            level <= atomic_load_explicit(&capture->level, memory_order_relaxed) &&
            atomic_load_explicit(&capture->suppression, memory_order_relaxed) == 0) {
            s_in_wrapper = true;
            owns_recursion_guard = true;
            char message[LOG_CAPTURE_MESSAGE_MAX];
            va_list capture_args;
            va_copy(capture_args, args);
            int formatted = LOG_CAPTURE_VSNPRINTF(message, sizeof(message), format, capture_args);
            va_end(capture_args);
            if (formatted >= 0) {
                log_capture_push_line(capture, level, (uint64_t)esp_timer_get_time(), tag, message);
            }
        }
    }
    atomic_fetch_sub_explicit(&s_wrapper_readers, 1, memory_order_seq_cst);

    /* Keep IDF's original filtering, formatting, lock and UART backend exactly intact. */
    esp_log_va(config, tag, format, args);
    if (owns_recursion_guard) {
        s_in_wrapper = false;
    }
    va_end(args);
}
