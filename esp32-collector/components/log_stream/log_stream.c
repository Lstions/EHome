/**
 * @file log_stream.c
 * @brief ESP32 System Log Stream implementation
 *
 * Architecture:
 *   vprintf hook → ring buffer → log_tx_task → MQTT MsgLogStream (0x1D)
 *
 * ESP-IDF v6.0 does not safely support replacing the global vprintf handler
 * in this collector runtime, so system modules emit structured entries via
 * log_stream_emit(). The stream keeps UART logging untouched.
 */

#include "log_stream.h"
#include "log_stream_codec.h"
#include "frame_codec.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "esp_task_wdt.h"
#include "esp_heap_caps.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#include <string.h>
#include <stdarg.h>
#include <stdio.h>

#define TAG "LOG_STREAM"

/* === Configuration === */
#define LOG_RING_BUF_SIZE   4096
#define LOG_LINE_MAX        96      // bounded structured message payload
#define LOG_BATCH_MAX       4       // bounded batch limits peak RAM and MQTT work
#define LOG_TX_INTERVAL_MS  200     // send interval (less frequent = less MQTT load)
#define LOG_TX_STACK        1536    // static buffers keep task stack small
#define LOG_TX_PRIO         5       /* below rx_task(7) and cmd_task(6) */
#define LOG_TX_BUF_SIZE     768     // 4 compact entries fit within this bound

/* === Message type === */
#define MSG_LOG_STREAM      0x1D

/* === Ring buffer entry === */
typedef struct {
    uint8_t  level;
    uint64_t ts_us;
    uint8_t  tag_len;
    char     tag[32];
    uint16_t msg_len;
    char     msg[LOG_LINE_MAX];
} log_entry_t;

/* === State === */
static SemaphoreHandle_t s_mutex = NULL;
static log_entry_t *s_ring = NULL;       /* array of LOG_BATCH_MAX*... entries */
static int s_ring_head = 0;              /* write index */
static int s_ring_tail = 0;              /* read index */
static int s_ring_count = 0;             /* entries in buffer */
#define RING_CAPACITY 4                  /* 4 * 152B = 608B heap */

static TaskHandle_t s_task = NULL;
static bool s_active = false;
static uint8_t s_level = LOG_LEVEL_INFO;
static uint16_t s_seq = 0;
static log_stream_publish_fn_t s_publish = NULL;

/* TX buffers are static to avoid task-stack overflow. */
static log_entry_t s_tx_batch[LOG_BATCH_MAX];
static uint8_t s_tx_buf[LOG_TX_BUF_SIZE];

/* === Ring buffer operations (mutex-protected) === */

static void ring_push(uint8_t level, uint64_t ts, const char *tag, const char *msg, size_t msg_len)
{
    /* vprintf hook can run inside arbitrary IDF task/critical contexts.
     * It must never block: drop a log line if the drain task owns the mutex. */
    if (s_mutex == NULL || xPortInIsrContext()) {
        return;
    }
    if (xSemaphoreTake(s_mutex, 0) != pdTRUE) {
        return;
    }

    if (s_ring_count >= RING_CAPACITY) {
        /* Overflow: drop oldest by advancing tail */
        s_ring_tail = (s_ring_tail + 1) % RING_CAPACITY;
        s_ring_count--;
    }

    log_entry_t *e = &s_ring[s_ring_head];
    e->level = level;
    e->ts_us = ts;
    e->tag_len = (uint8_t)(strlen(tag) < sizeof(e->tag) ? strlen(tag) : sizeof(e->tag) - 1);
    memcpy(e->tag, tag, e->tag_len);
    e->tag[e->tag_len] = '\0';
    if (msg_len > LOG_LINE_MAX - 1) msg_len = LOG_LINE_MAX - 1;
    e->msg_len = (uint16_t)msg_len;
    memcpy(e->msg, msg, msg_len);
    e->msg[msg_len] = '\0';

    s_ring_head = (s_ring_head + 1) % RING_CAPACITY;
    s_ring_count++;

    xSemaphoreGive(s_mutex);
}

static int ring_drain(log_entry_t *out, int max)
{
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    int n = 0;
    while (s_ring_count > 0 && n < max) {
        out[n] = s_ring[s_ring_tail];
        s_ring_tail = (s_ring_tail + 1) % RING_CAPACITY;
        s_ring_count--;
        n++;
    }
    xSemaphoreGive(s_mutex);
    return n;
}

/* === log_tx_task: batch publish === */

static void log_tx_task(void *pv)
{
    /* Don't use ESP_LOGI here — it would trigger the vprintf hook */
    esp_task_wdt_add(NULL);

    /* Static buffers: never put multi-KB payloads on this task's stack. */
    log_entry_t *batch = s_tx_batch;

    while (s_active) {
        esp_task_wdt_reset();

        int n = ring_drain(batch, LOG_BATCH_MAX);
        if (n > 0) {
            log_stream_entry_t entries[LOG_BATCH_MAX];
            for (int i = 0; i < n; ++i) {
                entries[i] = (log_stream_entry_t){
                    .level = batch[i].level,
                    .timestamp_us = batch[i].ts_us,
                    .tag = batch[i].tag,
                    .message = batch[i].msg,
                };
            }
            size_t encoded_len = 0;
            if (log_stream_encode(s_tx_buf, sizeof(s_tx_buf), &encoded_len,
                                  s_seq++, entries, (size_t)n) == FRAME_OK &&
                s_publish != NULL) {
                s_publish(s_tx_buf, encoded_len);
            }
        }

        vTaskDelay(pdMS_TO_TICKS(LOG_TX_INTERVAL_MS));
    }

    esp_task_wdt_delete(NULL);
    ESP_LOGI(TAG, "log_tx_task exiting");
    s_task = NULL;
    vTaskDelete(NULL);
}

/* === Public API === */

void log_stream_set_publish_callback(log_stream_publish_fn_t publish)
{
    s_publish = publish;
}

void log_stream_start(uint8_t level)
{
    if (s_active) {
        /* Already running — just update level */
        log_stream_set_level(level);
        return;
    }

    /* Allocate ring buffer array */
    s_ring = heap_caps_calloc(RING_CAPACITY, sizeof(log_entry_t), MALLOC_CAP_DEFAULT);
    if (!s_ring) {
        ESP_LOGE(TAG, "Failed to allocate ring buffer (%d bytes)", (int)(RING_CAPACITY * sizeof(log_entry_t)));
        return;
    }

    s_mutex = xSemaphoreCreateMutex();
    if (!s_mutex) {
        free(s_ring);
        s_ring = NULL;
        ESP_LOGE(TAG, "Failed to create mutex");
        return;
    }

    s_ring_head = 0;
    s_ring_tail = 0;
    s_ring_count = 0;
    if (level > LOG_LEVEL_VERBOSE) {
        level = LOG_LEVEL_VERBOSE;
    }
    s_level = level;
    s_seq = 0;

    /* Global esp_log_set_vprintf is deliberately never installed: IDF v6's
     * process-wide callback is unsafe here. Module wrappers provide explicit,
     * bounded remote diagnostics without changing UART log behavior. */
    s_active = true;
    /* Create tx task */
    BaseType_t ret = xTaskCreate(log_tx_task, "log_tx", LOG_TX_STACK,
                                 NULL, LOG_TX_PRIO, &s_task);
    if (ret != pdPASS) {
        ESP_LOGE(TAG, "Failed to create log_tx_task");
        vSemaphoreDelete(s_mutex);
        s_mutex = NULL;
        free(s_ring);
        s_ring = NULL;
        s_active = false;
        return;
    }

    log_stream_emit(LOG_LEVEL_INFO, TAG, "remote stream started level=%u", level);
    ESP_LOGI(TAG, "Started (level=%d, ring=%d entries)", level, RING_CAPACITY);
}

void log_stream_stop(void)
{
    if (!s_active) return;

    s_active = false;

    /* Wait for task to exit. Force-delete only after the bounded wait so the
     * ring/mutex cannot be freed while log_tx_task still references them. */
    if (s_task) {
        for (int i = 0; i < 100 && s_task; i++) {
            vTaskDelay(pdMS_TO_TICKS(10));
        }
        if (s_task) {
            ESP_LOGW(TAG, "log_tx_task did not exit; deleting it before cleanup");
            vTaskDelete(s_task);
            s_task = NULL;
        }
    }

    /* Free resources */
    if (s_mutex) {
        vSemaphoreDelete(s_mutex);
        s_mutex = NULL;
    }
    if (s_ring) {
        free(s_ring);
        s_ring = NULL;
    }

    s_ring_head = 0;
    s_ring_tail = 0;
    s_ring_count = 0;

    ESP_LOGI(TAG, "Stopped");
}

void log_stream_set_level(uint8_t level)
{
    if (!s_active) return;
    s_level = level > LOG_LEVEL_VERBOSE ? LOG_LEVEL_VERBOSE : level;
    /* Remote filtering only: never change global ESP-IDF log filtering. */
    log_stream_emit(LOG_LEVEL_INFO, TAG, "remote level set=%u", s_level);
}

bool log_stream_is_active(void)
{
    return s_active;
}

void log_stream_emit(uint8_t level, const char *tag, const char *fmt, ...)
{
    if (!s_active || level > s_level || tag == NULL || fmt == NULL) {
        return;
    }

    char msg[LOG_LINE_MAX];
    va_list args;
    va_start(args, fmt);
    int len = vsnprintf(msg, sizeof(msg), fmt, args);
    va_end(args);
    if (len <= 0) {
        return;
    }
    size_t msg_len = (size_t)len;
    if (msg_len >= sizeof(msg)) {
        msg_len = sizeof(msg) - 1;
    }
    ring_push(level, esp_timer_get_time(), tag, msg, msg_len);
}
