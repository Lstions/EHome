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
 *   P1-6: RX timeout — if a pending command has rx_timeout_ms > 0 and
 *   no response arrives within that window, rx_task emits a DataReport
 *   with error_code=0x01 (sensor RX timeout).  This replaces the old
 *   backend-only timeout for UART read operations.
 *
 *   P1-7: Protocol-agnostic frame delimiter — rx_task accumulates UART
 *   bytes into per-channel stream_rx_t buffers and uses frame_delim_config_t
 *   to detect complete frames.  Four modes: timeout (Modbus RTU),
 *   delimiter (GB3024 ASCII), start_stop (嘉佰达 BMS), fixed length.
 *   Default is FRAME_DELIM_TIMEOUT with auto-calculated interval from
 *   baud rate — fully backward-compatible with existing Modbus RTU.
 *
 *   Pending command tracking: Each UART channel has a FreeRTOS Queue
 *   (pending_queues[i], depth=PENDING_QUEUE_DEPTH) of pending_cmd_t entries.  UART
 *   cmd_tasks enqueue before/after TX; rx_task dequeues one entry per
 *   RX read.  This replaces the old per-channel single-slot arrays
 *   that caused data misattribution when multiple edge_devices share
 *   a channel.
 *
 *   P2-8: Decoupled from app_state_t — uses bus_runtime_t for dependency
 *   injection.  All s->field accesses replaced with rt->field.
 */

#include "bus_worker.h"
#include "bus_dma.h"
#include "cmd_queue.h"
#include "scheduler.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "esp_task_wdt.h"   /* 8.3: Task watchdog — feed in cmd_task loops */
#include "rom/ets_sys.h"   /* ets_delay_us — busy-wait without disabling interrupts */
#include "freertos/semphr.h"
#include <inttypes.h>
#include <string.h>

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

/* P0-3: Counting semaphore for suspend/resume — replaces vTaskSuspend/Resume
 * maxCount=1, initialCount=1 (available = not suspended)
 * suspend: Take semaphore → blocks cmd_tasks
 * resume: Give semaphore → unblocks cmd_tasks
 * cmd_task: probe with Take(0)+Give() — if Take fails, we're suspended */
static SemaphoreHandle_t s_suspend_sem = NULL;

static void ensure_suspend_sem(void) {
    if (s_suspend_sem == NULL) {
        s_suspend_sem = xSemaphoreCreateCounting(1, 1);
    }
}

/* Task handles for suspend/resume/stop */
static TaskHandle_t s_rx_task_h   = NULL;
static TaskHandle_t s_cmd_u0_h    = NULL;
static TaskHandle_t s_cmd_u1_h    = NULL;
static TaskHandle_t s_cmd_spi_h   = NULL;
static TaskHandle_t s_cmd_i2c_h   = NULL;

/* 8.1: Runtime counters for observability */
static uint32_t s_rx_timeout_count[SCHED_MAX_CHANNELS];   /* P1-6: RX timeout count per channel */
static uint32_t s_rx_match_count[SCHED_MAX_CHANNELS];     /* Frame match count per channel */

void bus_worker_set_callbacks(write_rsp_cb_t wr_cb, data_rpt_cb_t dr_cb)
{
    s_write_rsp_cb = wr_cb;
    s_data_rpt_cb  = dr_cb;
}

/* ==================================================================
 *  P2-2: Configurable turnaround delay helpers
 * ================================================================== */

/* P2-2: Compute turnaround delay from configuration
 * turnaround_us: 0 = auto (Modbus RTU 3.5 character interval)
 *               -1 = none (full duplex, no delay)
 *               >0 = manual value in microseconds
 * Returns the delay in us, or 0 if no delay needed.
 */
static uint32_t compute_turnaround_us(const bus_dma_ctx_t *ctx) {
    int32_t cfg_turnaround = ctx->cfg.uart.turnaround_us;

    if (cfg_turnaround == -1) {
        /* Full duplex — no turnaround */
        return 0;
    }

    uint32_t turnaround_us;
    if (cfg_turnaround == 0) {
        /* Auto: Modbus RTU 3.5 character interval = 38.5 bits */
        uint32_t baud = ctx->cfg.uart.baud;
        if (baud == 0) return 0;
        turnaround_us = 38500000UL / baud;
    } else {
        turnaround_us = (uint32_t)cfg_turnaround;
    }

    /* Bounds check: cap at 100ms */
    if (turnaround_us > 100000) {
        ESP_LOGW("BUS_WORKER", "turnaround_us=%lu > 100ms, capping", (unsigned long)turnaround_us);
        turnaround_us = 100000;
    }

    return turnaround_us;
}

/* P2-2: Apply turnaround delay */
static void apply_turnaround_delay(uint32_t turnaround_us) {
    if (turnaround_us == 0) return;

    if (turnaround_us > 10000) {
        /* >10ms: use vTaskDelay (yields CPU) */
        vTaskDelay(pdMS_TO_TICKS((turnaround_us + 999) / 1000));
    } else if (turnaround_us > 1000) {
        /* 1-10ms: use ets_delay_us (busy-wait, does NOT disable interrupts) */
        ets_delay_us(turnaround_us);
    }
    /* <1ms (@115200+): no extra wait needed */
}

/* ==================================================================
 *  UART cmd worker — shared logic for UART0 and UART1 tasks
 * ================================================================== */

static void uart_cmd_loop(bus_runtime_t *rt, QueueHandle_t queue, const char *tag)
{
    bus_cmd_t cmd;

    ESP_LOGI(tag, "Started (prio=%d)", uxTaskPriorityGet(NULL));

    /* 8.3: Subscribe this task to the watchdog */
    esp_task_wdt_add(NULL);

    uint32_t txn = 0, errs = 0, no_ctx = 0;
    TickType_t last_stats = xTaskGetTickCount();

    while (1) {
        /* 8.3: Feed watchdog to prevent hardware hang causing task death */
        esp_task_wdt_reset();
        /* P0-3: Suspend check — if config apply is in progress, wait */
        if (s_suspend_sem != NULL) {
            if (xSemaphoreTake(s_suspend_sem, 0) == pdTRUE) {
                /* Semaphore available = not suspended, give it back */
                xSemaphoreGive(s_suspend_sem);
            } else {
                /* Semaphore taken = suspended, wait and retry */
                vTaskDelay(pdMS_TO_TICKS(10));
                continue;
            }
        }

        if (!xQueueReceive(queue, &cmd, portMAX_DELAY)) continue;

        bus_dma_ctx_t *ctx = rt->find_ctx(rt, cmd.channel_id);
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
                    /* P2-2: Configurable turnaround delay before RX window */
                    uint32_t turnaround_us = compute_turnaround_us(ctx);
                    apply_turnaround_delay(turnaround_us);
                    /* Enqueue pending command for rx_task correlation */
                    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
                        if (rt->bus_ch[i] == cmd.channel_id) {
                            pending_cmd_t pcmd = {
                                .edge_device_id = cmd.edge_device_id,
                                .command_index  = cmd.command_index,
                                .request_id     = cmd.request_id,
                                .read_size      = cmd.read_size,
                                .tx_timestamp   = esp_timer_get_time(),  /* P1-6: Record TX time */
                                .rx_timeout_ms  = 1000,                  /* P1-6: Default 1000ms timeout */
                            };
                            /* P1-1: Copy command bytes for Modbus exception matching */
                            pcmd.cmd_data_len = cmd.tx_len < PENDING_CMD_DATA_MAX ? cmd.tx_len : PENDING_CMD_DATA_MAX;
                            if (pcmd.cmd_data_len > 0) {
                                memcpy(pcmd.cmd_data, cmd.tx_data, pcmd.cmd_data_len);
                            }
                            if (!xQueueSend(rt->pending_queues[i], &pcmd, 0)) {
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
                    if (rt->bus_ch[i] == cmd.channel_id) {
                        pending_cmd_t pcmd = {
                            .edge_device_id = cmd.edge_device_id,
                            .command_index  = cmd.command_index,
                            .request_id     = 0,
                            .read_size      = 0,
                        };
                        if (!xQueueSend(rt->pending_queues[i], &pcmd, 0)) {
                            ESP_LOGW(tag, "pending queue full for slot %d (sample), dropping pending", i);
                        }
                        break;
                    }
                }
            }
            /* P2-2: Configurable turnaround delay */
            {
                uint32_t turnaround_us = compute_turnaround_us(ctx);
                apply_turnaround_delay(turnaround_us);
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

static void spi_i2c_cmd_loop(bus_runtime_t *rt, QueueHandle_t queue, const char *tag)
{
    bus_cmd_t cmd;

    ESP_LOGI(tag, "Started (prio=%d)", uxTaskPriorityGet(NULL));

    /* 8.3: Subscribe this task to the watchdog */
    esp_task_wdt_add(NULL);

    uint32_t txn = 0, errs = 0, no_ctx = 0;
    TickType_t last_stats = xTaskGetTickCount();

    while (1) {
        /* 8.3: Feed watchdog to prevent hardware hang causing task death */
        esp_task_wdt_reset();
        /* P0-3: Suspend check — if config apply is in progress, wait */
        if (s_suspend_sem != NULL) {
            if (xSemaphoreTake(s_suspend_sem, 0) == pdTRUE) {
                /* Semaphore available = not suspended, give it back */
                xSemaphoreGive(s_suspend_sem);
            } else {
                /* Semaphore taken = suspended, wait and retry */
                vTaskDelay(pdMS_TO_TICKS(10));
                continue;
            }
        }

        if (!xQueueReceive(queue, &cmd, portMAX_DELAY)) continue;

        bus_dma_ctx_t *ctx = rt->find_ctx(rt, cmd.channel_id);
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
    bus_runtime_t *rt = (bus_runtime_t *)pv;
    uart_cmd_loop(rt, rt->uart0_cmd_queue, TAG_U0);
}

static void cmd_task_uart1(void *pv)
{
    bus_runtime_t *rt = (bus_runtime_t *)pv;
    uart_cmd_loop(rt, rt->uart1_cmd_queue, TAG_U1);
}

static void cmd_task_spi(void *pv)
{
    bus_runtime_t *rt = (bus_runtime_t *)pv;
    spi_i2c_cmd_loop(rt, rt->spi_cmd_queue, TAG_SPI);
}

static void cmd_task_i2c(void *pv)
{
    bus_runtime_t *rt = (bus_runtime_t *)pv;
    spi_i2c_cmd_loop(rt, rt->i2c_cmd_queue, TAG_I2C);
}

/* ==================================================================
 *  rx_task — RX path (UART only, non-blocking poll)
 *
 *  P1-7: Protocol-agnostic frame delimiter — stream RX processing.
 *  Bytes from bus_dma_read are accumulated into per-channel stream
 *  buffers.  Frame completeness is determined by the channel's
 *  frame_delim_config_t (timeout / delimiter / start_stop / fixed).
 *  Only complete frames are dispatched to the pending-queue match
 *  and DataReport callback.
 * ================================================================== */

/* P1-7: Per-channel stream state */
static stream_rx_t s_streams[SCHED_MAX_CHANNELS];

/* 检查 buffer 末尾是否匹配分隔符 */
static bool ends_with_delimiter(const uint8_t *buf, size_t len,
                                 const uint8_t *delim, uint8_t delim_len) {
    if (len < delim_len) return false;
    return memcmp(buf + len - delim_len, delim, delim_len) == 0;
}

/* 从 start_stop 帧中读取长度字段 */
static int32_t read_length_field(const uint8_t *buf, size_t buf_len,
                                  const frame_delim_config_t *cfg) {
    int offset = cfg->start_stop.length_field_offset;
    int size   = cfg->start_stop.length_field_size;
    if ((int)buf_len < offset + size) return -1;
    if (size == 1) return buf[offset];
    if (size == 2) return (buf[offset] << 8) | buf[offset + 1];
    return -1;
}

/* P1-7: Check if a complete frame has been received */
static bool is_frame_complete(stream_rx_t *stream) {
    switch (stream->delim_cfg.type) {

    case FRAME_DELIM_TIMEOUT: {
        /* Modbus RTU: 帧间静默超过阈值 */
        if (stream->len == 0) return false;
        uint64_t now = esp_timer_get_time();
        return (now - stream->last_rx_time) > stream->delim_cfg.timeout.timeout_us;
    }

    case FRAME_DELIM_DELIMITER: {
        /* GB3024 ASCII: 检查末尾是否为分隔符 */
        return ends_with_delimiter(stream->buffer, stream->len,
                                   stream->delim_cfg.delimiter.bytes,
                                   stream->delim_cfg.delimiter.len);
    }

    case FRAME_DELIM_START_STOP: {
        /* 嘉佰达 BMS: start + len + data + stop */
        if (stream->len < 1) return false;
        if (!stream->frame_started) {
            if (stream->buffer[0] == stream->delim_cfg.start_stop.start_byte) {
                stream->frame_started = true;
            } else {
                stream->len = 0;  /* 丢弃非帧头字节 */
                return false;
            }
        }
        int32_t payload_len = read_length_field(stream->buffer, stream->len, &stream->delim_cfg);
        if (payload_len < 0) return false;
        int header = stream->delim_cfg.start_stop.header_size;
        int expected = header + payload_len + 2 + 1;  /* data + checksum(2) + stop(1) */
        if (stream->delim_cfg.start_stop.length_includes_header) {
            expected = payload_len + 2 + 1;
        }
        if (stream->len >= (size_t)expected) {
            if (stream->buffer[expected - 1] == stream->delim_cfg.start_stop.stop_byte) {
                return true;
            }
            ESP_LOGW(TAG_RX, "stop byte mismatch: expected 0x%02X, got 0x%02X",
                     stream->delim_cfg.start_stop.stop_byte,
                     stream->buffer[expected - 1]);
            stream->len = 0;
            stream->frame_started = false;
            return false;
        }
        return false;
    }

    case FRAME_DELIM_FIXED: {
        return stream->len >= stream->delim_cfg.fixed_len.length;
    }

    default:
        return false;
    }
}

/* P1-7: Initialize default frame delimiter config for a channel
 * Modbus RTU is the default — timeout based on baud rate */
static void init_default_delim_config(stream_rx_t *stream, uint32_t baud) {
    stream->delim_cfg.type = FRAME_DELIM_TIMEOUT;
    if (baud > 0) {
        stream->delim_cfg.timeout.timeout_us = 38500000UL / baud;
    } else {
        stream->delim_cfg.timeout.timeout_us = 4000;  /* 默认 4ms @9600 */
    }
    stream->len = 0;
    stream->frame_started = false;
}

static void rx_task(void *pv)
{
    bus_runtime_t *rt = (bus_runtime_t *)pv;
    uint8_t rx[256];

    ESP_LOGI(TAG_RX, "Started (prio=%d, poll=%dms)",
             uxTaskPriorityGet(NULL), RX_POLL_MS);

    /* 8.3: Subscribe this task to the watchdog */
    esp_task_wdt_add(NULL);

    uint32_t reads = 0, hits = 0;
    TickType_t last_stats = xTaskGetTickCount();

    while (1) {
        /* 8.3: Feed watchdog to prevent hardware hang causing task death */
        esp_task_wdt_reset();
        /* P0-3: Suspend check for rx_task too */
        if (s_suspend_sem != NULL) {
            if (xSemaphoreTake(s_suspend_sem, 0) == pdTRUE) {
                xSemaphoreGive(s_suspend_sem);
            } else {
                vTaskDelay(pdMS_TO_TICKS(10));
                continue;
            }
        }

        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!rt->bus_ctx[i].initialized) continue;
            if (rt->bus_ctx[i].bus_type != BUS_TYPE_UART) continue;

            reads++;
            size_t n = bus_dma_read(&rt->bus_ctx[i], rx, sizeof(rx));
            if (n > 0) {
                stream_rx_t *stream = &s_streams[i];

                /* P1-7: Initialize stream on first RX if not yet configured */
                if (stream->delim_cfg.type == FRAME_DELIM_TIMEOUT &&
                    stream->delim_cfg.timeout.timeout_us == 0) {
                    init_default_delim_config(stream, rt->bus_ctx[i].cfg.uart.baud);
                }

                /* Append to stream buffer */
                if (stream->len + n <= sizeof(stream->buffer)) {
                    memcpy(stream->buffer + stream->len, rx, n);
                    stream->len += n;
                } else {
                    ESP_LOGW(TAG_RX, "ch%d buffer overflow (%d+%d > %d), resetting",
                             i, (int)stream->len, (int)n, (int)sizeof(stream->buffer));
                    stream->len = 0;
                    stream->frame_started = false;
                    continue;
                }
                stream->last_rx_time = esp_timer_get_time();
            }

            /* P1-7: Check if a complete frame has been received */
            stream_rx_t *stream = &s_streams[i];
            if (stream->len > 0 && is_frame_complete(stream)) {
                hits++;
                s_rx_match_count[i]++;   /* 8.1: Runtime counter — frame match per channel */
                uint32_t ch = rt->bus_ch[i];
                uint32_t rid = 0;
                uint32_t eid = 0;
                uint8_t  cidx = 0;

                /* Smart pending queue match (existing logic, but using stream->buffer instead of rx) */
                pending_cmd_t best_match;
                bool found = false;
                pending_cmd_t drained[PENDING_QUEUE_DEPTH];
                int drained_count = 0;
                pending_cmd_t pcmd;
                size_t frame_len = stream->len;

                /* Drain the entire queue */
                while (xQueueReceive(rt->pending_queues[i], &pcmd, 0) == pdTRUE) {
                    if (!found && pcmd.read_size > 0 && pcmd.read_size == frame_len) {
                        best_match = pcmd;
                        found = true;
                    } else {
                        drained[drained_count++] = pcmd;
                    }
                }

                if (found) {
                    rid  = best_match.request_id;
                    eid  = best_match.edge_device_id;
                    cidx = best_match.command_index;
                    for (int j = 0; j < drained_count; j++) {
                        if (!xQueueSendToBack(rt->pending_queues[i], &drained[j], 0)) {
                            ESP_LOGW(TAG_RX, "failed to re-enqueue pending entry");
                        }
                    }
                } else if (!found && frame_len >= 5 && (stream->buffer[1] & 0x80)) {
                    /* P1-1: Modbus exception response detection */
                    uint8_t resp_addr = stream->buffer[0];
                    uint8_t resp_func = stream->buffer[1] & 0x7F;

                    int best_idx = -1;
                    for (int j = 0; j < drained_count; j++) {
                        if (drained[j].read_size > 0 &&
                            drained[j].cmd_data_len >= 2 &&
                            drained[j].cmd_data[0] == resp_addr &&
                            drained[j].cmd_data[1] == resp_func) {
                            best_idx = j;
                            break;
                        }
                    }
                    if (best_idx < 0) {
                        for (int j = 0; j < drained_count; j++) {
                            if (drained[j].read_size > 0) {
                                best_idx = j;
                                break;
                            }
                        }
                    }
                    if (best_idx >= 0) {
                        rid  = drained[best_idx].request_id;
                        eid  = drained[best_idx].edge_device_id;
                        cidx = drained[best_idx].command_index;
                        for (int k = best_idx; k < drained_count - 1; k++) {
                            drained[k] = drained[k + 1];
                        }
                        drained_count--;
                        found = true;
                        ESP_LOGI(TAG_RX, "P1-1: matched Modbus exception (addr=0x%02X func=0x%02X) to reqID=%lu",
                                 resp_addr, resp_func, (unsigned long)rid);
                    }
                    for (int j = 0; j < drained_count; j++) {
                        if (!xQueueSendToBack(rt->pending_queues[i], &drained[j], 0)) {
                            ESP_LOGW(TAG_RX, "failed to re-enqueue pending entry");
                        }
                    }
                } else if (drained_count > 0) {
                    rid  = drained[0].request_id;
                    eid  = drained[0].edge_device_id;
                    cidx = drained[0].command_index;
                    for (int j = 1; j < drained_count; j++) {
                        if (!xQueueSendToBack(rt->pending_queues[i], &drained[j], 0)) {
                            ESP_LOGW(TAG_RX, "failed to re-enqueue pending entry");
                        }
                    }
                }

                uint64_t ts = esp_timer_get_time();
                if (s_data_rpt_cb) s_data_rpt_cb(ch, ts, 0, stream->buffer, stream->len, 0, rid, eid, cidx);

                /* Reset stream state */
                stream->len = 0;
                stream->frame_started = false;
            }
        }

        /* P1-6: Check for RX timeouts on pending commands */
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            if (!rt->pending_queues[i]) continue;
            if (!rt->bus_ctx[i].initialized) continue;
            if (rt->bus_ctx[i].bus_type != BUS_TYPE_UART) continue;

            /* Drain and check timeouts */
            pending_cmd_t drained_tmo[PENDING_QUEUE_DEPTH];
            int tmo_count = 0;
            pending_cmd_t pcmd;

            while (xQueueReceive(rt->pending_queues[i], &pcmd, 0) == pdTRUE) {
                if (pcmd.rx_timeout_ms > 0 && pcmd.tx_timestamp > 0) {
                    int64_t now_us = esp_timer_get_time();
                    int64_t elapsed_ms = (now_us - pcmd.tx_timestamp) / 1000;

                    if (elapsed_ms > (int64_t)pcmd.rx_timeout_ms) {
                        /* RX timeout — send DataReport with error_code=0x01 */
                        s_rx_timeout_count[i]++;   /* 8.1: Runtime counter — RX timeout per channel */
                        ESP_LOGW(TAG_RX, "P1-6: RX timeout for reqID=%lu (waited %lldms, limit=%lums)",
                                 (unsigned long)pcmd.request_id,
                                 (long long)elapsed_ms, (unsigned long)pcmd.rx_timeout_ms);

                        if (s_data_rpt_cb) {
                            uint32_t ch = rt->bus_ch[i];
                            uint64_t ts = (uint64_t)esp_timer_get_time();
                            /* error_code = 0x01 means RX timeout (sensor no response) */
                            s_data_rpt_cb(ch, ts, 0, NULL, 0, 0x01, pcmd.request_id,
                                         pcmd.edge_device_id, pcmd.command_index);
                        }
                        /* Don't re-enqueue — timed out */
                        continue;
                    }
                }
                /* Not timed out — re-enqueue */
                drained_tmo[tmo_count++] = pcmd;
            }

            /* Re-enqueue non-expired entries */
            for (int j = 0; j < tmo_count; j++) {
                xQueueSendToBack(rt->pending_queues[i], &drained_tmo[j], 0);
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

void bus_worker_start(bus_runtime_t *rt)
{
    /* rx_task: highest priority — must not miss DMA data */
    xTaskCreate(rx_task, "rx_task", RX_STACK,
                (void *)rt, RX_PRIO, &s_rx_task_h);

    /* Per-bus cmd_tasks: same priority (6), fair scheduling */
    xTaskCreate(cmd_task_uart0, "cmd_u0", UART_STACK,
                (void *)rt, CMD_PRIO, &s_cmd_u0_h);
    xTaskCreate(cmd_task_uart1, "cmd_u1", UART_STACK,
                (void *)rt, CMD_PRIO, &s_cmd_u1_h);
    xTaskCreate(cmd_task_spi,   "cmd_spi", SPI_I2C_STACK,
                (void *)rt, CMD_PRIO, &s_cmd_spi_h);
    xTaskCreate(cmd_task_i2c,   "cmd_i2c", SPI_I2C_STACK,
                (void *)rt, CMD_PRIO, &s_cmd_i2c_h);
}

void bus_worker_suspend(void)
{
    ensure_suspend_sem();
    ESP_LOGI("BUS_WORKER", "Suspending bus tasks (config apply)");
    xSemaphoreTake(s_suspend_sem, portMAX_DELAY);  // Take = suspend
}

void bus_worker_resume(void)
{
    if (s_suspend_sem != NULL) {
        xSemaphoreGive(s_suspend_sem);  // Give = resume
        ESP_LOGI("BUS_WORKER", "Resumed bus tasks");
    }
}

void bus_worker_stop(void)
{
    if (s_rx_task_h)  { vTaskDelete(s_rx_task_h);  s_rx_task_h  = NULL; }
    if (s_cmd_u0_h)   { vTaskDelete(s_cmd_u0_h);   s_cmd_u0_h   = NULL; }
    if (s_cmd_u1_h)   { vTaskDelete(s_cmd_u1_h);   s_cmd_u1_h   = NULL; }
    if (s_cmd_spi_h)  { vTaskDelete(s_cmd_spi_h);  s_cmd_spi_h  = NULL; }
    if (s_cmd_i2c_h)  { vTaskDelete(s_cmd_i2c_h);  s_cmd_i2c_h  = NULL; }
}

/* 8.1: Get runtime counters (for debug/status queries) */
uint32_t bus_worker_get_rx_timeout_count(int channel)
{
    if (channel < 0 || channel >= SCHED_MAX_CHANNELS) return 0;
    return s_rx_timeout_count[channel];
}

uint32_t bus_worker_get_rx_match_count(int channel)
{
    if (channel < 0 || channel >= SCHED_MAX_CHANNELS) return 0;
    return s_rx_match_count[channel];
}
