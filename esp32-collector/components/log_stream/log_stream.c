/**
 * @file log_stream.c
 * @brief ESP32 System Log Stream implementation
 *
 * Architecture:
 *   vprintf hook → ring buffer → log_tx_task → MQTT MsgLogStream (0x1D)
 *
 * The hook intercepts ALL ESP_LOGx output. It calls the original vprintf
 * (to keep UART output for local debugging) and also copies the formatted
 * line into a ring buffer. log_tx_task wakes every 100ms, drains the ring
 * buffer, and publishes batched MsgLogStream frames via msg_handler_publish.
 */

#include "log_stream.h"
#include "frame_codec.h"
#include "msg_handler.h"
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
#define LOG_LINE_MAX        128     // single line max length
#define LOG_BATCH_MAX       8       // max lines per batch
#define LOG_TX_INTERVAL_MS  200     // send interval (less frequent = less MQTT load)
#define LOG_TX_STACK        3072    // task stack
#define LOG_TX_PRIO         5       /* below rx_task(7) and cmd_task(6) */

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
#define RING_CAPACITY 8                  /* max queued entries (8*~320B = ~2.5KB) */

static TaskHandle_t s_task = NULL;
static bool s_active = false;
static uint8_t s_level = LOG_LEVEL_INFO;
static uint16_t s_seq = 0;

/* Reentrancy guard: prevents log feedback loop.
 * When log_tx_task publishes via MQTT, ESP_LOGI calls inside that path
 * would trigger the hook again, creating a recursive loop. */
static volatile bool s_in_hook = false;

/* Original vprintf function pointer */
static vprintf_like_t s_orig_vprintf = NULL;

/* === Ring buffer operations (mutex-protected) === */

static void ring_push(uint8_t level, uint64_t ts, const char *tag, const char *msg, size_t msg_len)
{
    xSemaphoreTake(s_mutex, portMAX_DELAY);

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

/* === vprintf hook === */

/**
 * The hook receives the same args as the standard vprintf.
 * ESP-IDF log format: "[LEVEL] tag: message\n"
 * We parse the level and tag from the format string, then forward.
 */
static int log_stream_vprintf(const char *fmt, va_list args)
{
    /* Reentrancy guard */
    if (s_in_hook) {
        if (s_orig_vprintf) {
            va_list args_copy;
            va_copy(args_copy, args);
            s_orig_vprintf(fmt, args_copy);
            va_end(args_copy);
        }
        return 0;
    }
    s_in_hook = true;

    /* Call original vprintf first (keep UART output) */
    if (s_orig_vprintf) {
        va_list args_copy;
        va_copy(args_copy, args);
        int ret = s_orig_vprintf(fmt, args_copy);
        va_end(args_copy);
        s_in_hook = false;
        return ret;
    }

    s_in_hook = false;
    return 0;
}

/* === log_tx_task: batch publish === */

static void log_tx_task(void *pv)
{
    /* Don't use ESP_LOGI here — it would trigger the vprintf hook */
    esp_task_wdt_add(NULL);

    log_entry_t batch[LOG_BATCH_MAX];

    while (s_active) {
        esp_task_wdt_reset();

        int n = ring_drain(batch, LOG_BATCH_MAX);
        if (n > 0) {
            /* Encode MsgLogStream frame */
            uint8_t buf[4096];
            frame_encoder_t enc;
            frame_encoder_init(&enc, buf, sizeof(buf), MSG_LOG_STREAM);

            /* Field 1: count (varint) */
            frame_encode_varint(&enc, 1, (uint64_t)n);

            /* Field 2: seq (varint) */
            frame_encode_varint(&enc, 2, (uint64_t)s_seq++);

            /* Encode each log entry as a sub-frame in field 3 (repeated) */
            for (int i = 0; i < n; i++) {
                frame_encoder_t sub;
                uint8_t subbuf[LOG_LINE_MAX + 128];
                frame_encoder_init(&sub, subbuf, sizeof(subbuf), 0);

                /* Sub-field 1: level */
                frame_encode_varint(&sub, 1, batch[i].level);
                /* Sub-field 2: ts (fixed64) */
                /* Encode ts as varint for simplicity (protobuf compatible) */
                frame_encode_varint(&sub, 2, batch[i].ts_us);
                /* Sub-field 3: tag (string) */
                frame_encode_string(&sub, 3, batch[i].tag);
                /* Sub-field 4: message (string) */
                frame_encode_string(&sub, 4, batch[i].msg);

                /* Append as length-delimited field 3 in main frame */
                frame_encoder_append_raw(&enc, subbuf, sub.pos);
            }

            /* Publish via msg_handler (MQTT) */
            msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
        }

        vTaskDelay(pdMS_TO_TICKS(LOG_TX_INTERVAL_MS));
    }

    esp_task_wdt_delete(NULL);
    ESP_LOGI(TAG, "log_tx_task exiting");
    s_task = NULL;
    vTaskDelete(NULL);
}

/* === Public API === */

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
    s_level = level;
    s_seq = 0;

    /* Install vprintf hook */
    s_orig_vprintf = esp_log_set_vprintf(log_stream_vprintf);

    /* Set global log level */
    esp_log_level_set("*", (esp_log_level_t)level);

    s_active = true;

    /* Create tx task */
    BaseType_t ret = xTaskCreate(log_tx_task, "log_tx", LOG_TX_STACK,
                                 NULL, LOG_TX_PRIO, &s_task);
    if (ret != pdPASS) {
        ESP_LOGE(TAG, "Failed to create log_tx_task");
        esp_log_set_vprintf(s_orig_vprintf);
        s_orig_vprintf = NULL;
        vSemaphoreDelete(s_mutex);
        s_mutex = NULL;
        free(s_ring);
        s_ring = NULL;
        s_active = false;
        return;
    }

    ESP_LOGI(TAG, "Started (level=%d, ring=%d entries)", level, RING_CAPACITY);
}

void log_stream_stop(void)
{
    if (!s_active) return;

    s_active = false;

    /* Wait for task to exit */
    if (s_task) {
        for (int i = 0; i < 100 && s_task; i++) {
            vTaskDelay(pdMS_TO_TICKS(10));
        }
    }

    /* Restore original vprintf */
    if (s_orig_vprintf) {
        esp_log_set_vprintf(s_orig_vprintf);
        s_orig_vprintf = NULL;
    }

    /* Restore default log level */
    esp_log_level_set("*", ESP_LOG_INFO);

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
    s_level = level;
    esp_log_level_set("*", (esp_log_level_t)level);
    ESP_LOGI(TAG, "Level set to %d", level);
}

bool log_stream_is_active(void)
{
    return s_active;
}
