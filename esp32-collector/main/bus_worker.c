/**
 * @file bus_worker.c
 * @brief Per-bus command tasks + shared rx_task.
 *
 * Design:
 *   Four independent cmd_tasks, one per bus (UART0, UART1, SPI, I2C).
 *   Each consumes from its own command queue, eliminating bus-type
 *   contention and allowing per-bus priority/stack tuning.
 *
 *   UART cmd_tasks: TX via bus_dma_write, enqueue pending_cmd_t for
 *   rx_task correlation, optional delay for CMD_SAMPLE.
 *
 *   SPI/I2C cmd_tasks: atomic bus_dma_transact (write+read), callback
 *   directly from the task context.
 *
 *   rx_task: polls all UART channels non-blocking, dequeues pending_cmd_t
 *   for DataReport attribution. Priority 7 (above cmd_tasks at 6).
 *
 *   Timeout is NOT handled here.  The backend decides when a
 *   WriteCommand has timed out (no DataReport with matching request_id
 *   within the expected window).
 *
 *   Pending command tracking: Each UART channel has a FreeRTOS Queue
 *   (pending_queues[i], depth=4) of pending_cmd_t entries.  UART
 *   cmd_tasks enqueue before/after TX; rx_task dequeues one entry per
 *   RX read.  This replaces the old per-channel single-slot arrays
 *   that caused data misattribution when multiple edge_devices share
 *   a channel.
 */

#include "bus_worker.h"
#include "bus_manager.h"
#include "bus_dma.h"
#include "cmd_queue.h"
#include "scheduler.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "rom/ets_sys.h"   /* ets_delay_us — busy-wait without disabling interrupts */
#include <inttypes.h>

#define TAG_U0       "CMD_U0"
#define TAG_U1       "CMD_U1"
#define TAG_SPI      "CMD_SPI"
#define TAG_I2C      "CMD_I2C"
#define TAG_RX       "RX_TASK"

#define CMD_PRIO     6
#define RX_PRIO      7
#define UART_STACK   4096
#define SPI_I2C_STACK 3072
#define RX_STACK     4096
#define RX_POLL_MS   5     /* rx_task poll interval */

/* Injected callbacks (set before bus_worker_start) */
static write_rsp_cb_t s_write_rsp_cb = NULL;
static data_rpt_cb_t  s_data_rpt_cb  = NULL;

/* Task handles for suspend/resume/stop */
static TaskHandle_t s_rx_task_h   = NULL;
static TaskHandle_t s_cmd_u0_h    = NULL;
static TaskHandle_t s_cmd_u1_h    = NULL;
static TaskHandle_t s_cmd_spi_h   = NULL;
static TaskHandle_t s_cmd_i2c_h   = NULL;

void bus_worker_set_callbacks(write_rsp_cb_t wr_cb, data_rpt_cb_t dr_cb)
{
    s_write_rsp_cb = wr_cb;
    s_data_rpt_cb  = dr_cb;
}

/* ==================================================================
 *  UART cmd worker — shared logic for UART0 and UART1 tasks
 * ================================================================== */

static void uart_cmd_loop(app_state_t *s, QueueHandle_t queue, const char *tag)
{
    bus_cmd_t cmd;

    ESP_LOGI(tag, "Started (prio=%d)", uxTaskPriorityGet(NULL));

    uint32_t txn = 0, errs = 0, no_ctx = 0;
    TickType_t last_stats = xTaskGetTickCount();

    while (1) {
        if (!xQueueReceive(queue, &cmd, portMAX_DELAY)) continue;

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
             * WriteCommand on UART:
             *   read_size == 0: TX only, fire-and-forget, WriteRsp(success).
             *   read_size > 0: TX + turnaround + enqueue pending_cmd_t so
             *                   rx_task can correlate the response DataReport.
             *                   WriteRsp still sent immediately (TX confirmed).
             */
            esp_err_t e = bus_dma_write(ctx, cmd.tx_data, cmd.tx_len);
            if (e == ESP_OK) {
                if (cmd.read_size > 0) {
                    /* Turnaround delay (same as CMD_SAMPLE) before RX window */
                    uint32_t baud = ctx->cfg.uart.baud;
                    if (baud > 0) {
                        uint32_t turnaround_us = 38500000UL / baud;
                        if (turnaround_us > 10000) {
                            vTaskDelay(pdMS_TO_TICKS((turnaround_us + 999) / 1000));
                        } else if (turnaround_us > 1000) {
                            ets_delay_us(turnaround_us);
                        }
                    }
                    /* Enqueue pending command for rx_task correlation */
                    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
                        if (s->bus_ch[i] == cmd.channel_id) {
                            pending_cmd_t pcmd = {
                                .edge_device_id = cmd.edge_device_id,
                                .command_index  = cmd.command_index,
                                .request_id     = cmd.request_id,
                            };
                            if (!xQueueSend(s->pending_queues[i], &pcmd, 0)) {
                                ESP_LOGW(tag, "pending queue full for slot %d (write+read), dropping pending", i);
                            }
                            break;
                        }
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
        }

        if (cmd.type == CMD_SAMPLE) {
            /*
             * CMD_SAMPLE on UART: TX + delay, rx_task picks up response.
             * Enqueue pending_cmd_t for rx_task correlation.
             * request_id=0 signals CMD_SAMPLE (no WriteResponse expected).
             */
            esp_err_t e = bus_dma_write(ctx, cmd.tx_data, cmd.tx_len);
            if (e != ESP_OK) {
                errs++;
                scheduler_notify_channel_error(cmd.channel_id);
            } else {
                scheduler_notify_channel_success(cmd.channel_id);
                for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
                    if (s->bus_ch[i] == cmd.channel_id) {
                        pending_cmd_t pcmd = {
                            .edge_device_id = cmd.edge_device_id,
                            .command_index  = cmd.command_index,
                            .request_id     = 0,
                        };
                        if (!xQueueSend(s->pending_queues[i], &pcmd, 0)) {
                            ESP_LOGW(tag, "pending queue full for slot %d (sample), dropping pending", i);
                        }
                        break;
                    }
                }
            }
            /* Modbus RTU turn-around: replace delay_ms with precise 3.5-char wait.
             * 3.5 char = 38.5 bits → turnaround_us = 38500000 / baud
             * New firmware auto-calculates from baud rate; ConfigTemplate.delay_ms
             * field is kept for backward compat but ignored. */
            {
                uint32_t baud = ctx->cfg.uart.baud;
                if (baud > 0) {
                    uint32_t turnaround_us = 38500000UL / baud;
                    if (turnaround_us > 10000) {
                        /* >10ms: use vTaskDelay (yields CPU) */
                        vTaskDelay(pdMS_TO_TICKS((turnaround_us + 999) / 1000));
                    } else if (turnaround_us > 1000) {
                        /* 1-10ms: use ets_delay_us (busy-wait, does NOT disable interrupts) */
                        ets_delay_us(turnaround_us);
                    }
                    /* <1ms (@115200+): no extra wait needed */
                }
            }
        }

        /* Periodic stats (every 10s) */
        TickType_t now = xTaskGetTickCount();
        if (now - last_stats > pdMS_TO_TICKS(10000)) {
            if (txn > 0 || errs > 0 || no_ctx > 0) {
                uint32_t rate = txn > 0 ? ((txn - errs) * 100 / txn) : 0;
                ESP_LOGI(tag, "Stats: txn=%" PRIu32 " err=%" PRIu32
                         " (%" PRIu32 "%%) no_ctx=%" PRIu32,
                         txn, errs, rate, no_ctx);
            }
            txn = 0; errs = 0; no_ctx = 0;
            last_stats = now;
        }
    }
}

/* ==================================================================
 *  SPI/I2C cmd worker — shared logic for SPI and I2C tasks
 * ================================================================== */

static void spi_i2c_cmd_loop(app_state_t *s, QueueHandle_t queue, const char *tag)
{
    bus_cmd_t cmd;

    ESP_LOGI(tag, "Started (prio=%d)", uxTaskPriorityGet(NULL));

    uint32_t txn = 0, errs = 0, no_ctx = 0;
    TickType_t last_stats = xTaskGetTickCount();

    while (1) {
        if (!xQueueReceive(queue, &cmd, portMAX_DELAY)) continue;

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
            /* SPI / I2C WriteCommand:
             *   read_size == 0: TX only, WriteRsp(success), no DataReport.
             *   read_size > 0: atomic transact(TX+RX), WriteRsp(success),
             *                   plus DataReport with read-back data.
             */
            uint8_t rx[256];
            size_t rl = 0;
            esp_err_t e;
            if (cmd.read_size > 0) {
                size_t read_cap = cmd.read_size < sizeof(rx) ? cmd.read_size : sizeof(rx);
                e = bus_dma_transact(ctx, cmd.tx_data, cmd.tx_len,
                                     rx, read_cap, &rl);
            } else {
                e = bus_dma_transact(ctx, cmd.tx_data, cmd.tx_len,
                                     rx, sizeof(rx), &rl);
            }
            if (e == ESP_OK) {
                if (s_write_rsp_cb) s_write_rsp_cb(cmd.request_id, true, 0, NULL);
                scheduler_notify_channel_success(cmd.channel_id);
                if (cmd.read_size > 0 && rl > 0) {
                    uint64_t ts = esp_timer_get_time();
                    if (s_data_rpt_cb) s_data_rpt_cb(cmd.channel_id, ts, 0,
                                                 rx, rl, 0, cmd.request_id,
                                                 cmd.edge_device_id, cmd.command_index);
                }
            } else {
                errs++;
                if (s_write_rsp_cb) s_write_rsp_cb(cmd.request_id, false,
                                           (uint32_t)e, "bus err");
                scheduler_notify_channel_error(cmd.channel_id);
            }
        }

        if (cmd.type == CMD_SAMPLE) {
            /* SPI / I2C: atomic transact */
            uint8_t rx[256];
            size_t rl = 0;
            esp_err_t e = bus_dma_transact(ctx, cmd.tx_data, cmd.tx_len,
                                           rx, sizeof(rx), &rl);
            if (e == ESP_OK) {
                scheduler_notify_channel_success(cmd.channel_id);
                if (rl > 0) {
                    uint64_t ts = esp_timer_get_time();
                    if (s_data_rpt_cb) s_data_rpt_cb(cmd.channel_id, ts, 0,
                                                 rx, rl, 0, 0,
                                                 cmd.edge_device_id, cmd.command_index);
                }
            } else {
                errs++;
                scheduler_notify_channel_error(cmd.channel_id);
            }
        }

        /* Periodic stats (every 10s) */
        TickType_t now = xTaskGetTickCount();
        if (now - last_stats > pdMS_TO_TICKS(10000)) {
            if (txn > 0 || errs > 0 || no_ctx > 0) {
                uint32_t rate = txn > 0 ? ((txn - errs) * 100 / txn) : 0;
                ESP_LOGI(tag, "Stats: txn=%" PRIu32 " err=%" PRIu32
                         " (%" PRIu32 "%%) no_ctx=%" PRIu32,
                         txn, errs, rate, no_ctx);
            }
            txn = 0; errs = 0; no_ctx = 0;
            last_stats = now;
        }
    }
}

/* ==================================================================
 *  Per-bus task entry points
 * ================================================================== */

static void cmd_task_uart0(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    uart_cmd_loop(s, s->uart0_cmd_queue, TAG_U0);
}

static void cmd_task_uart1(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    uart_cmd_loop(s, s->uart1_cmd_queue, TAG_U1);
}

static void cmd_task_spi(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    spi_i2c_cmd_loop(s, s->spi_cmd_queue, TAG_SPI);
}

static void cmd_task_i2c(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    spi_i2c_cmd_loop(s, s->i2c_cmd_queue, TAG_I2C);
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
                uint32_t eid = 0;
                uint8_t  cidx = 0;

                /* Dequeue one pending entry per RX read (FIFO match).
                 * Each TX command enqueues one pending_cmd_t; each RX
                 * read consumes one, preserving TX/RX ordering. */
                pending_cmd_t pcmd;
                if (xQueueReceive(s->pending_queues[i], &pcmd, 0) == pdTRUE) {
                    rid  = pcmd.request_id;
                    eid  = pcmd.edge_device_id;
                    cidx = pcmd.command_index;
                }
                /* If no pending entry, rid/eid/cidx stay 0 — data
                 * arrives without command context (e.g. unsolicited). */

                uint64_t ts = esp_timer_get_time();
                if (s_data_rpt_cb) s_data_rpt_cb(ch, ts, 0, rx, n, 0, rid, eid, cidx);
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
    xTaskCreate(rx_task, "rx_task", RX_STACK,
                (void *)state, RX_PRIO, &s_rx_task_h);

    /* Per-bus cmd_tasks: same priority (6), fair scheduling */
    xTaskCreate(cmd_task_uart0, "cmd_u0", UART_STACK,
                (void *)state, CMD_PRIO, &s_cmd_u0_h);
    xTaskCreate(cmd_task_uart1, "cmd_u1", UART_STACK,
                (void *)state, CMD_PRIO, &s_cmd_u1_h);
    xTaskCreate(cmd_task_spi,   "cmd_spi", SPI_I2C_STACK,
                (void *)state, CMD_PRIO, &s_cmd_spi_h);
    xTaskCreate(cmd_task_i2c,   "cmd_i2c", SPI_I2C_STACK,
                (void *)state, CMD_PRIO, &s_cmd_i2c_h);
}

void bus_worker_suspend(void)
{
    if (s_rx_task_h)  vTaskSuspend(s_rx_task_h);
    if (s_cmd_u0_h)   vTaskSuspend(s_cmd_u0_h);
    if (s_cmd_u1_h)   vTaskSuspend(s_cmd_u1_h);
    if (s_cmd_spi_h)  vTaskSuspend(s_cmd_spi_h);
    if (s_cmd_i2c_h)  vTaskSuspend(s_cmd_i2c_h);
}

void bus_worker_resume(void)
{
    if (s_rx_task_h)  vTaskResume(s_rx_task_h);
    if (s_cmd_u0_h)   vTaskResume(s_cmd_u0_h);
    if (s_cmd_u1_h)   vTaskResume(s_cmd_u1_h);
    if (s_cmd_spi_h)  vTaskResume(s_cmd_spi_h);
    if (s_cmd_i2c_h)  vTaskResume(s_cmd_i2c_h);
}

void bus_worker_stop(void)
{
    if (s_rx_task_h)  { vTaskDelete(s_rx_task_h);  s_rx_task_h  = NULL; }
    if (s_cmd_u0_h)   { vTaskDelete(s_cmd_u0_h);   s_cmd_u0_h   = NULL; }
    if (s_cmd_u1_h)   { vTaskDelete(s_cmd_u1_h);   s_cmd_u1_h   = NULL; }
    if (s_cmd_spi_h)  { vTaskDelete(s_cmd_spi_h);  s_cmd_spi_h  = NULL; }
    if (s_cmd_i2c_h)  { vTaskDelete(s_cmd_i2c_h);  s_cmd_i2c_h  = NULL; }
}
