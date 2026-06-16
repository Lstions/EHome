/**
 * @file bus_worker.c
 * @brief Unified bus transaction worker — consumes cmd_queue, drives bus_dma_transact.
 *
 * Single high-priority task (prio=8) that processes bus commands from the
 * shared command queue.  Injects write responses and data reports back to
 * msg_handler.  Periodic performance logging every 10 seconds.
 */

#include "bus_worker.h"
#include "bus_manager.h"
#include "bus_dma.h"
#include "cmd_queue.h"
#include "msg_handler.h"
#include "scheduler.h"
#include "esp_log.h"
#include "esp_timer.h"
#include <inttypes.h>

#define TAG          "BUS_WORKER"
#define WORKER_PRIO  8
#define WORKER_STACK 4096

static void worker_task(void *pv)
{
    app_state_t *s = (app_state_t *)pv;
    bus_cmd_t cmd;
    static uint8_t rx[256]; /* static to reduce stack pressure */

    ESP_LOGI(TAG, "Started (prio=%d)", uxTaskPriorityGet(NULL));

    uint32_t txn = 0, errs = 0, no_ctx = 0;
    TickType_t last_stats = xTaskGetTickCount();

    while (1) {
        if (!xQueueReceive(s->cmd_queue, &cmd, portMAX_DELAY)) continue;

        bus_dma_ctx_t *ctx = bus_manager_find_ctx(s, cmd.channel_id);
        if (!ctx) {
            no_ctx++;
            if (cmd.type == CMD_WRITE)
                msg_handler_send_write_rsp(cmd.request_id, false, 4, "no ctx");
            scheduler_notify_channel_error(cmd.channel_id);
            continue;
        }

        size_t rl = 0;
        /* Use cmd.read_size for rx, capped at buffer size.
         * Total transaction length = max(tx_len, read_size) for full-duplex SPI. */
        size_t actual_rx_size = cmd.read_size;
        if (actual_rx_size > sizeof(rx)) actual_rx_size = sizeof(rx);
        /* For SPI: transaction length must cover both TX and RX */
        size_t total_len = (cmd.tx_len > actual_rx_size) ? cmd.tx_len : actual_rx_size;
        if (total_len == 0) total_len = 1;  /* at least 1 byte */

        esp_err_t e = bus_dma_transact(ctx, cmd.tx_data, total_len,
                                       cmd.timeout_ms ? cmd.timeout_ms : 50,
                                       rx, actual_rx_size, &rl);

        txn++;
        if (e != ESP_OK) {
            errs++;
            scheduler_notify_channel_error(cmd.channel_id);
        } else {
            scheduler_notify_channel_success(cmd.channel_id);
        }

        if (cmd.type == CMD_WRITE) {
            bool ok = (e == ESP_OK);
            msg_handler_send_write_rsp(cmd.request_id, ok,
                                       ok ? 0 : (uint32_t)e,
                                       ok ? NULL : "bus err");
            if (rl > 0) {
                uint64_t ts = esp_timer_get_time();
                msg_handler_send_data_report(cmd.channel_id, ts, 0, rx, rl, 0, cmd.request_id);
            }
        }

        if (cmd.type == CMD_SAMPLE && rl > 0) {
            uint64_t ts = esp_timer_get_time();
            msg_handler_send_data_report(cmd.channel_id, ts, 0, rx, rl, 0, 0);
        }

        /* Periodic stats (every 10s) */
        TickType_t now = xTaskGetTickCount();
        if (now - last_stats > pdMS_TO_TICKS(10000)) {
            if (txn > 0 || errs > 0 || no_ctx > 0) {
                uint32_t rate = txn > 0 ? ((txn - errs) * 100 / txn) : 0;
                ESP_LOGI(TAG, "Stats: txn=%" PRIu32 " err=%" PRIu32
                         " (%" PRIu32 "%%) no_ctx=%" PRIu32,
                         txn, errs, rate, no_ctx);
            }
            txn = 0; errs = 0; no_ctx = 0;
            last_stats = now;
        }
    }
}

void bus_worker_start(app_state_t *state)
{
    xTaskCreate(worker_task, "bus_worker", WORKER_STACK,
                (void *)state, WORKER_PRIO, NULL);
}
