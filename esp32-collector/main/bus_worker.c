/**
 * @file bus_worker.c
 * @brief Two independent tasks: cmd_task (TX) and rx_task (RX).
 *
 * Design:
 *   UART is full-duplex at the hardware level.  cmd_task handles TX
 *   (fire-and-forget for WriteCommand, write+delay for CMD_SAMPLE).
 *   rx_task polls all UART channels non-blocking and emits DataReport.
 *
 *   SPI and I2C are transactional — cmd_task does atomic write+read
 *   via bus_dma_transact().  rx_task skips non-UART channels.
 *
 *   Timeout is NOT handled here.  The backend decides when a
 *   WriteCommand has timed out (no DataReport with matching request_id
 *   within the expected window).
 */

#include "bus_worker.h"
#include "bus_manager.h"
#include "bus_dma.h"
#include "cmd_queue.h"
#include "scheduler.h"
#include "esp_log.h"
#include "esp_timer.h"
#include <inttypes.h>

#define TAG_CMD      "CMD_TASK"
#define TAG_RX       "RX_TASK"
#define CMD_PRIO     7
#define RX_PRIO      8
#define WORKER_STACK 4096
#define RX_POLL_MS   5     /* rx_task poll interval */

/* Injected callbacks (set before bus_worker_start) */
static write_rsp_cb_t s_write_rsp_cb = NULL;
static data_rpt_cb_t  s_data_rpt_cb  = NULL;

/* Task handles for suspend/resume during config re-apply */
static TaskHandle_t s_rx_task_h = NULL;
static TaskHandle_t s_cmd_task_h = NULL;

void bus_worker_set_callbacks(write_rsp_cb_t wr_cb, data_rpt_cb_t dr_cb)
{
    s_write_rsp_cb = wr_cb;
    s_data_rpt_cb  = dr_cb;
}

/* ==================================================================
 *  cmd_task — TX path (WriteCommand + CMD_SAMPLE)
 * ================================================================== */

static void cmd_task(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    bus_cmd_t cmd;

    ESP_LOGI(TAG_CMD, "Started (prio=%d)", uxTaskPriorityGet(NULL));

    uint32_t txn = 0, errs = 0, no_ctx = 0;
    TickType_t last_stats = xTaskGetTickCount();

    while (1) {
        if (!xQueueReceive(s->cmd_queue, &cmd, portMAX_DELAY)) continue;

        bus_dma_ctx_t *ctx = bus_manager_find_ctx(s, cmd.channel_id);
        if (!ctx) {
            no_ctx++;
            if (cmd.type == CMD_WRITE)
                if (s_write_rsp_cb) s_write_rsp_cb(cmd.request_id, false, 4, "no ctx");
            scheduler_notify_channel_error(cmd.channel_id);
            continue;
        }

        txn++;

        if (cmd.type == CMD_WRITE) {
            /*
             * WriteCommand: TX only for UART, transact for SPI/I2C.
             * WriteResponse is sent immediately after TX completes.
             * For UART, record pending request_id so rx_task can
             * attach it to the DataReport when data arrives.
             */
            if (ctx->bus_type == BUS_TYPE_UART) {
                esp_err_t e = bus_dma_write(ctx, cmd.tx_data, cmd.tx_len);
                if (e == ESP_OK) {
                    /* Record pending request_id for rx_task correlation */
                    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
                        if (s->bus_ch[i] == cmd.channel_id) {
                            s->pending_requests[i] = cmd.request_id;
                            break;
                        }
                    }
                    if (s_write_rsp_cb) s_write_rsp_cb(cmd.request_id, true, 0, NULL);
                    scheduler_notify_channel_success(cmd.channel_id);
                } else {
                    errs++;
                    if (s_write_rsp_cb) s_write_rsp_cb(cmd.request_id, false,
                                               (uint32_t)e, "bus err");
                    scheduler_notify_channel_error(cmd.channel_id);
                }
            } else {
                /* SPI / I2C: atomic transact */
                uint8_t rx[256];
                size_t rl = 0;
                esp_err_t e = bus_dma_transact(ctx, cmd.tx_data, cmd.tx_len,
                                               rx, sizeof(rx), &rl);
                if (e == ESP_OK) {
                    if (s_write_rsp_cb) s_write_rsp_cb(cmd.request_id, true, 0, NULL);
                    scheduler_notify_channel_success(cmd.channel_id);
                    if (rl > 0) {
                        uint64_t ts = esp_timer_get_time();
                        if (s_data_rpt_cb) s_data_rpt_cb(cmd.channel_id, ts, 0,
                                                     rx, rl, 0, cmd.request_id);
                    }
                } else {
                    errs++;
                    if (s_write_rsp_cb) s_write_rsp_cb(cmd.request_id, false,
                                               (uint32_t)e, "bus err");
                    scheduler_notify_channel_error(cmd.channel_id);
                }
            }
        }

        if (cmd.type == CMD_SAMPLE) {
            /*
             * CMD_SAMPLE: periodic sampling from scheduler.
             * UART: TX + delay, rx_task picks up response.
             * SPI/I2C: atomic transact.
             */
            if (ctx->bus_type == BUS_TYPE_UART) {
                esp_err_t e = bus_dma_write(ctx, cmd.tx_data, cmd.tx_len);
                if (e != ESP_OK) {
                    errs++;
                    scheduler_notify_channel_error(cmd.channel_id);
                } else {
                    scheduler_notify_channel_success(cmd.channel_id);
                }
                /* Let device process, then rx_task picks up the response */
                if (cmd.delay_ms > 0) {
                    vTaskDelay(pdMS_TO_TICKS(cmd.delay_ms));
                }
            } else {
                /* SPI / I2C */
                uint8_t rx[256];
                size_t rl = 0;
                esp_err_t e = bus_dma_transact(ctx, cmd.tx_data, cmd.tx_len,
                                               rx, sizeof(rx), &rl);
                if (e == ESP_OK) {
                    scheduler_notify_channel_success(cmd.channel_id);
                    if (rl > 0) {
                        uint64_t ts = esp_timer_get_time();
                        if (s_data_rpt_cb) s_data_rpt_cb(cmd.channel_id, ts, 0,
                                                     rx, rl, 0, 0);
                    }
                } else {
                    errs++;
                    scheduler_notify_channel_error(cmd.channel_id);
                }
            }
        }

        /* Periodic stats (every 10s) */
        TickType_t now = xTaskGetTickCount();
        if (now - last_stats > pdMS_TO_TICKS(10000)) {
            if (txn > 0 || errs > 0 || no_ctx > 0) {
                uint32_t rate = txn > 0 ? ((txn - errs) * 100 / txn) : 0;
                ESP_LOGI(TAG_CMD, "Stats: txn=%" PRIu32 " err=%" PRIu32
                         " (%" PRIu32 "%%) no_ctx=%" PRIu32,
                         txn, errs, rate, no_ctx);
            }
            txn = 0; errs = 0; no_ctx = 0;
            last_stats = now;
        }
    }
}

/* ==================================================================
 *  rx_task — RX path (UART only, non-blocking poll)
 * ================================================================== */

static void rx_task(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    uint8_t rx[256];

    ESP_LOGI(TAG_RX, "Started (prio=%d, poll=%dms)",
             uxTaskPriorityGet(NULL), RX_POLL_MS);

    uint32_t reads = 0, hits = 0;
    TickType_t last_stats = xTaskGetTickCount();

    while (1) {
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!s->bus_ctx[i].initialized) continue;
            if (s->bus_ctx[i].bus_type != BUS_TYPE_UART) continue;

            reads++;
            size_t n = bus_dma_read(&s->bus_ctx[i], rx, sizeof(rx));
            if (n > 0) {
                hits++;
                uint32_t ch = s->bus_ch[i];
                uint32_t rid = 0;

                /* Consume pending request_id if one exists */
                if (s->pending_requests[i] != 0) {
                    rid = s->pending_requests[i];
                    s->pending_requests[i] = 0;
                }

                uint64_t ts = esp_timer_get_time();
                if (s_data_rpt_cb) s_data_rpt_cb(ch, ts, 0, rx, n, 0, rid);
            }
        }

        vTaskDelay(pdMS_TO_TICKS(RX_POLL_MS));

        /* Periodic stats (every 10s) */
        TickType_t now = xTaskGetTickCount();
        if (now - last_stats > pdMS_TO_TICKS(10000)) {
            if (reads > 0) {
                ESP_LOGI(TAG_RX, "Stats: reads=%" PRIu32 " hits=%" PRIu32
                         " (%.1f%%)", reads, hits,
                         (float)hits * 100.0f / (float)reads);
            }
            reads = 0; hits = 0;
            last_stats = now;
        }
    }
}

/* ==================================================================
 *  Public API
 * ================================================================== */

void bus_worker_start(app_state_t *state)
{
    /* rx_task: highest priority — must not miss DMA data */
    xTaskCreate(rx_task, "rx_task", WORKER_STACK,
                (void *)state, RX_PRIO, &s_rx_task_h);

    /* cmd_task: second priority — TX path */
    xTaskCreate(cmd_task, "cmd_task", WORKER_STACK,
                (void *)state, CMD_PRIO, &s_cmd_task_h);
}

void bus_worker_suspend(void)
{
    if (s_rx_task_h) vTaskSuspend(s_rx_task_h);
    if (s_cmd_task_h) vTaskSuspend(s_cmd_task_h);
}

void bus_worker_resume(void)
{
    if (s_rx_task_h) vTaskResume(s_rx_task_h);
    if (s_cmd_task_h) vTaskResume(s_cmd_task_h);
}
