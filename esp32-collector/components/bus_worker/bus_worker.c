/**
 * @file bus_worker.c
 * @brief Per-bus command tasks + shared rx_task.
 *
 * Architecture:
 * - Four independent cmd_tasks (UART0, UART1, SPI, I2C) at priority 6
 * - Shared rx_task at priority 7 (higher) for non-blocking UART RX poll
 * - Each cmd_task has its own queue, eliminating bus-type contention
 *
 * UART: TX is fire-and-forget (cmd_task). RX is non-blocking poll (rx_task).
 * SPI/I2C: atomic transact(write+read) inside cmd_task, callback immediately.
 *
 * P1-8 rx_task: ESP32 is a TRANSPARENT UART BYTE PIPE. DMA bytes are
 * accumulated into per-channel stream buffers (cross-DMA-read linearization)
 * and reported to the backend as-is. No frame detection. No protocol parsing.
 * Backend must handle frame boundaries and protocol parsing.
 *
 * Reporting trigger: has pending cmd AND buffer non-empty → report all
 * buffered bytes with cmd correlation (request_id, edge_device_id).
 *
 * P1-6: RX timeout drain — if a pending cmd has rx_timeout_ms > 0 and no
 * data received within that window, rx_task emits DataReport with error_code=0x01.
 *
 * P2-8: Decoupled from app_state_t — uses bus_runtime_t for DI.
 */

#include "bus_worker.h"
#include "bus_dma.h"
#include "cmd_queue.h"
#include "scheduler.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "esp_task_wdt.h"  // Task watchdog
#include "rom/ets_sys.h"   // ets_delay_us — busy-wait without disabling interrupts
#include "freertos/semphr.h"
#include <inttypes.h>
#include <string.h>

#define TAG_U0  "CMD_U0"
#define TAG_U1  "CMD_U1"
#define TAG_SPI "CMD_SPI"
#define TAG_I2C "CMD_I2C"
#define TAG_RX  "RX_TASK"

#define CMD_PRIO      6
#define RX_PRIO       7
#define UART_STACK    4096
#define SPI_I2C_STACK 3072
#define RX_STACK      4096
#define RX_POLL_MS    5  /* rx_task poll interval */

/* Injected callbacks */
static write_rsp_cb_t s_write_rsp_cb = NULL;
static data_rpt_cb_t s_data_rpt_cb = NULL;

/* P0-3: Counting semaphore for suspend/resume */
static SemaphoreHandle_t s_suspend_sem = NULL;

static void ensure_suspend_sem(void) {
 if (!s_suspend_sem) {
  s_suspend_sem = xSemaphoreCreateCounting(1, 1);
 }
}

/* Task handles */
static TaskHandle_t s_cmd_u0_h  = NULL;
static TaskHandle_t s_cmd_u1_h  = NULL;
static TaskHandle_t s_cmd_spi_h = NULL;
static TaskHandle_t s_cmd_i2c_h = NULL;
static TaskHandle_t s_rx_task_h = NULL;

/* 8.1: Runtime counters */
static uint32_t s_rx_timeout_count[SCHED_MAX_CHANNELS];

void bus_worker_set_callbacks(write_rsp_cb_t wr_cb, data_rpt_cb_t dr_cb)
{
 s_write_rsp_cb = wr_cb;
 s_data_rpt_cb  = dr_cb;
}

/* ------------------------------------------------------------------ */
/*  P2-2: Turnaround delay helpers                                    */
/* ------------------------------------------------------------------ */

static uint32_t compute_turnaround_us(const bus_dma_ctx_t *ctx)
{
 int32_t cfg = ctx->cfg.uart.turnaround_us;
 if (cfg == -1) return 0; /* Full duplex — no turnaround */
 uint32_t us;
 if (cfg == 0) {
  uint32_t baud = ctx->cfg.uart.baud;
  if (!baud) return 0;
  us = 38500000UL / baud; /* Modbus RTU 3.5 char interval */
 } else {
  us = (uint32_t)cfg;
 }
 if (us > 100000) us = 100000; /* Cap at 100ms */
 return us;
}

static void apply_turnaround_delay(uint32_t us)
{
 if (!us) return;
 if (us > 10000) {
  vTaskDelay(pdMS_TO_TICKS((us + 999) / 1000));
 } else if (us > 1000) {
  ets_delay_us(us);
 }
}

/* ------------------------------------------------------------------ */
/*  Enqueue pending cmd for rx_task correlation                       */
/* ------------------------------------------------------------------ */

static void enqueue_pending(bus_runtime_t *rt, int ch_idx, const bus_cmd_t *cmd)
{
 pending_cmd_t pcmd = {
  .edge_device_id  = cmd->edge_device_id,
  .command_index   = cmd->command_index,
  .request_id      = (cmd->type == CMD_SAMPLE) ? 0 : cmd->request_id,
  .read_size       = cmd->read_size,
  .tx_timestamp    = esp_timer_get_time(),
  .rx_timeout_ms   = 1000,
 };
 pcmd.cmd_data_len = (cmd->tx_len < PENDING_CMD_DATA_MAX)
   ? cmd->tx_len : PENDING_CMD_DATA_MAX;
 if (pcmd.cmd_data_len > 0) {
  memcpy(pcmd.cmd_data, cmd->tx_data, pcmd.cmd_data_len);
 }
 if (!xQueueSend(rt->pending_queues[ch_idx], &pcmd, 0)) {
  ESP_LOGW(TAG_U0, "pending queue full ch%d, dropping", ch_idx);
 }
}

/* ------------------------------------------------------------------ */
/*  UART cmd loop                                                     */
/* ------------------------------------------------------------------ */

static void uart_cmd_loop(bus_runtime_t *rt, QueueHandle_t queue, const char *tag)
{
 bus_cmd_t cmd;
 ESP_LOGI(tag, "Started (prio=%d)", uxTaskPriorityGet(NULL));
 esp_task_wdt_add(NULL);

 uint32_t txn = 0, errs = 0, no_ctx = 0;
 TickType_t last_stats = xTaskGetTickCount();

 while (1) {
  esp_task_wdt_reset();
  /* P0-3: Suspend check */
  if (s_suspend_sem) {
   if (xSemaphoreTake(s_suspend_sem, 0) == pdTRUE) {
    xSemaphoreGive(s_suspend_sem);
   } else {
    vTaskDelay(pdMS_TO_TICKS(10));
    continue;
   }
  }

  if (!xQueueReceive(queue, &cmd, pdMS_TO_TICKS(5000))) continue;

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
   esp_err_t e = bus_dma_write(ctx, cmd.tx_data, cmd.tx_len);
   if (e == ESP_OK) {
    if (cmd.read_size > 0) {
     uint32_t tu = compute_turnaround_us(ctx);
     apply_turnaround_delay(tu);
     for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
      if (rt->bus_ch[i] == cmd.channel_id) {
       enqueue_pending(rt, i, &cmd);
       break;
      }
     }
    }
    if (s_write_rsp_cb) s_write_rsp_cb(cmd.request_id, true, 0, NULL);
    scheduler_notify_channel_success(cmd.channel_id);
   } else {
    errs++;
    if (s_write_rsp_cb) s_write_rsp_cb(cmd.request_id, false, (uint32_t)e, "bus err");
    scheduler_notify_channel_error(cmd.channel_id);
   }
  }

  if (cmd.type == CMD_SAMPLE) {
   esp_err_t e = bus_dma_write(ctx, cmd.tx_data, cmd.tx_len);
   if (e != ESP_OK) {
    errs++;
    scheduler_notify_channel_error(cmd.channel_id);
   } else {
    scheduler_notify_channel_success(cmd.channel_id);
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
     if (rt->bus_ch[i] == cmd.channel_id) {
      enqueue_pending(rt, i, &cmd);
      break;
     }
    }
   }
   uint32_t tu = compute_turnaround_us(ctx);
   apply_turnaround_delay(tu);
  }

  /* Periodic stats */
  TickType_t now = xTaskGetTickCount();
  if (now - last_stats > pdMS_TO_TICKS(10000)) {
   if (txn || errs || no_ctx) {
    uint32_t rate = txn ? ((txn - errs) * 100 / txn) : 0;
    ESP_LOGI(tag, "Stats: txn=%" PRIu32 " err=%" PRIu32 " (%" PRIu32 "%%) no_ctx=%" PRIu32,
     txn, errs, rate, no_ctx);
   }
   txn = 0; errs = 0; no_ctx = 0;
   last_stats = now;
  }
 }
}

/* ------------------------------------------------------------------ */
/*  SPI/I2C cmd loop                                                  */
/* ------------------------------------------------------------------ */

static void spi_i2c_cmd_loop(bus_runtime_t *rt, QueueHandle_t queue, const char *tag)
{
 bus_cmd_t cmd;
 ESP_LOGI(tag, "Started (prio=%d)", uxTaskPriorityGet(NULL));
 esp_task_wdt_add(NULL);

 uint32_t txn = 0, errs = 0, no_ctx = 0;
 TickType_t last_stats = xTaskGetTickCount();

 while (1) {
  esp_task_wdt_reset();
  if (s_suspend_sem) {
   if (xSemaphoreTake(s_suspend_sem, 0) == pdTRUE) {
    xSemaphoreGive(s_suspend_sem);
   } else {
    vTaskDelay(pdMS_TO_TICKS(10));
    continue;
   }
  }
  if (!xQueueReceive(queue, &cmd, pdMS_TO_TICKS(5000))) continue;

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
   uint8_t rx[256]; size_t rl = 0; esp_err_t e;
   if (cmd.read_size > 0) {
    size_t cap = cmd.read_size < sizeof(rx) ? cmd.read_size : sizeof(rx);
    e = bus_dma_transact(ctx, cmd.tx_data, cmd.tx_len, rx, cap, &rl);
   } else {
    e = bus_dma_transact(ctx, cmd.tx_data, cmd.tx_len, rx, sizeof(rx), &rl);
   }
   if (e == ESP_OK) {
    if (s_write_rsp_cb) s_write_rsp_cb(cmd.request_id, true, 0, NULL);
    scheduler_notify_channel_success(cmd.channel_id);
    if (cmd.read_size > 0 && rl > 0) {
     uint64_t ts = esp_timer_get_time();
     if (s_data_rpt_cb) s_data_rpt_cb(cmd.channel_id, ts, 0, rx, rl, 0,
      cmd.request_id, cmd.edge_device_id, cmd.command_index);
    }
   } else {
    errs++;
    if (s_write_rsp_cb) s_write_rsp_cb(cmd.request_id, false, (uint32_t)e, "bus err");
    scheduler_notify_channel_error(cmd.channel_id);
   }
  }

  if (cmd.type == CMD_SAMPLE) {
   uint8_t rx[256]; size_t rl = 0;
   esp_err_t e = bus_dma_transact(ctx, cmd.tx_data, cmd.tx_len, rx, sizeof(rx), &rl);
   if (e == ESP_OK) {
    scheduler_notify_channel_success(cmd.channel_id);
    if (rl > 0) {
     uint64_t ts = esp_timer_get_time();
     if (s_data_rpt_cb) s_data_rpt_cb(cmd.channel_id, ts, 0, rx, rl, 0,
      0, cmd.edge_device_id, cmd.command_index);
   }
  } else {
    errs++;
    scheduler_notify_channel_error(cmd.channel_id);
   }
  }

  TickType_t now = xTaskGetTickCount();
  if (now - last_stats > pdMS_TO_TICKS(10000)) {
   if (txn || errs || no_ctx) {
    uint32_t rate = txn ? ((txn - errs) * 100 / txn) : 0;
    ESP_LOGI(tag, "Stats: txn=%" PRIu32 " err=%" PRIu32 " (%" PRIu32 "%%) no_ctx=%" PRIu32,
     txn, errs, rate, no_ctx);
   }
   txn = 0; errs = 0; no_ctx = 0;
   last_stats = now;
  }
 }
}

/* ------------------------------------------------------------------ */
/*  Per-bus task entry points                                          */
/* ------------------------------------------------------------------ */

static void cmd_task_uart0(void *pv) {
 bus_runtime_t *rt = (bus_runtime_t *)pv;
 uart_cmd_loop(rt, rt->uart0_cmd_queue, TAG_U0);
}
static void cmd_task_uart1(void *pv) {
 bus_runtime_t *rt = (bus_runtime_t *)pv;
 uart_cmd_loop(rt, rt->uart1_cmd_queue, TAG_U1);
}
static void cmd_task_spi(void *pv) {
 bus_runtime_t *rt = (bus_runtime_t *)pv;
 spi_i2c_cmd_loop(rt, rt->spi_cmd_queue, TAG_SPI);
}
static void cmd_task_i2c(void *pv) {
 bus_runtime_t *rt = (bus_runtime_t *)pv;
 spi_i2c_cmd_loop(rt, rt->i2c_cmd_queue, TAG_I2C);
}

/* ------------------------------------------------------------------ */
/*  P1-8: rx_task — transparent UART byte pipe                        */
/*                                                                      */
/*  No frame detection. DMA bytes are linearized into per-channel       */
/*  stream buffers. Reporting is triggered by UART line idle: after     */
/*  the last byte arrives, wait UART_IDLE_THRESHOLD_US (10ms) for      */
/*  more bytes. If none arrive, the response is considered complete    */
/*  and all buffered bytes are reported with pending cmd metadata.     */
/*                                                                      */
/*  This works for ALL protocols and ALL response lengths — 5-byte     */
/*  Modbus exceptions to 100+ byte BMS frames — because every         */
/*  request-response protocol has a post-response idle gap.            */
/* ------------------------------------------------------------------ */

#define UART_IDLE_THRESHOLD_US 10000  /* 10ms = 2 poll cycles */

static stream_rx_t s_streams[SCHED_MAX_CHANNELS];
static int64_t     s_last_rx_us[SCHED_MAX_CHANNELS];

static bool has_pending_cmd(bus_runtime_t *rt, int ch_idx)
{
 return rt->pending_queues[ch_idx]
   && uxQueueMessagesWaiting(rt->pending_queues[ch_idx]) > 0;
}

static void rx_task(void *pv)
{
 bus_runtime_t *rt = (bus_runtime_t *)pv;
 uint8_t rx[256];

 ESP_LOGI(TAG_RX, "Started (prio=%d, poll=%dms)",
  uxTaskPriorityGet(NULL), RX_POLL_MS);
 esp_task_wdt_add(NULL);

 uint32_t reads = 0, hits = 0;
 TickType_t last_stats = xTaskGetTickCount();

 while (1) {
  esp_task_wdt_reset();
  if (s_suspend_sem) {
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

   stream_rx_t *s = &s_streams[i];

   if (n > 0) {
    /* Data arrived — accumulate and record timestamp */
    s_last_rx_us[i] = esp_timer_get_time();

    if (s->len + n > STREAM_RX_BUF_SIZE) {
     ESP_LOGW(TAG_RX, "ch%d rx overflow (len=%d+%d > %d), resetting",
      i, (int)s->len, (int)n, (int)STREAM_RX_BUF_SIZE);
     s->len = 0;
    }
    memcpy(s->buffer + s->len, rx, n);
    s->len += n;
    /* Don't report yet — wait for UART line to go idle */
   }

   /* UART idle detection: if no data this poll AND buffer non-empty
    * AND idle threshold exceeded → response is complete → report */
   if (s->len > 0 && n == 0) {
    int64_t now_us = esp_timer_get_time();
    if (now_us - s_last_rx_us[i] > UART_IDLE_THRESHOLD_US) {
     if (has_pending_cmd(rt, i)) {
      /* Request-response mode: report with pending cmd metadata */
      pending_cmd_t pcmd;
      if (xQueuePeek(rt->pending_queues[i], &pcmd, 0) == pdTRUE) {
       uint64_t ts = (uint64_t)now_us;
       if (s_data_rpt_cb) {
        s_data_rpt_cb(rt->bus_ch[i], ts, 0,
         s->buffer, s->len, 0,
         pcmd.request_id, pcmd.edge_device_id, pcmd.command_index);
       }
       /* Pop the pending cmd — response is complete */
       xQueueReceive(rt->pending_queues[i], &pcmd, 0);
       hits++;
      }
     } else {
      /* Passive/terminal mode: no pending cmd — report as spontaneous data
       * (request_id=0, edge_device_id=0) so terminal sees RX bytes */
      uint64_t ts = (uint64_t)now_us;
      if (s_data_rpt_cb) {
       s_data_rpt_cb(rt->bus_ch[i], ts, 0,
        s->buffer, s->len, 0,
        0, 0, 0);
      }
      hits++;
     }
     s->len = 0;
    }
   }
  }

  /* P1-6: RX timeout drain — check pending cmds that got no data */
  for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
   if (!rt->pending_queues[i]) continue;
   if (!rt->bus_ctx[i].initialized) continue;
   if (rt->bus_ctx[i].bus_type != BUS_TYPE_UART) continue;

   pending_cmd_t drained[PENDING_QUEUE_DEPTH];
   int tmo_count = 0;
   pending_cmd_t pcmd;

   while (xQueueReceive(rt->pending_queues[i], &pcmd, 0) == pdTRUE) {
    if (pcmd.rx_timeout_ms > 0 && pcmd.tx_timestamp > 0) {
     int64_t now_us = esp_timer_get_time();
     int64_t elapsed_ms = (now_us - pcmd.tx_timestamp) / 1000;
     if (elapsed_ms > (int64_t)pcmd.rx_timeout_ms) {
      s_rx_timeout_count[i]++;
      ESP_LOGW(TAG_RX, "P1-6: RX timeout reqID=%lu (%lldms)",
       (unsigned long)pcmd.request_id, (long long)elapsed_ms);
      if (s_data_rpt_cb) {
       uint64_t ts = (uint64_t)esp_timer_get_time();
       s_data_rpt_cb(rt->bus_ch[i], ts, 0, NULL, 0,
        0x01, pcmd.request_id, pcmd.edge_device_id, pcmd.command_index);
      }
      continue; /* drop — timed out */
     }
    }
    drained[tmo_count++] = pcmd;
   }
   for (int j = 0; j < tmo_count; j++) {
    xQueueSendToBack(rt->pending_queues[i], &drained[j], 0);
   }
  }

  vTaskDelay(pdMS_TO_TICKS(RX_POLL_MS));

  /* Stats */
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

/* ------------------------------------------------------------------ */
/*  Public API                                                        */
/* ------------------------------------------------------------------ */

void bus_worker_start(bus_runtime_t *rt)
{
 xTaskCreate(rx_task, "rx_task", RX_STACK,
  (void *)rt, RX_PRIO, &s_rx_task_h);
 xTaskCreate(cmd_task_uart0, "cmd_u0", UART_STACK,
  (void *)rt, CMD_PRIO, &s_cmd_u0_h);
 xTaskCreate(cmd_task_uart1, "cmd_u1", UART_STACK,
  (void *)rt, CMD_PRIO, &s_cmd_u1_h);
 xTaskCreate(cmd_task_spi, "cmd_spi", SPI_I2C_STACK,
  (void *)rt, CMD_PRIO, &s_cmd_spi_h);
 xTaskCreate(cmd_task_i2c, "cmd_i2c", SPI_I2C_STACK,
  (void *)rt, CMD_PRIO, &s_cmd_i2c_h);
}

void bus_worker_suspend(void)
{
 ensure_suspend_sem();
 ESP_LOGI("BUS_WORKER", "Suspending (config apply)");
 xSemaphoreTake(s_suspend_sem, portMAX_DELAY);
}

void bus_worker_resume(void)
{
 if (s_suspend_sem) xSemaphoreGive(s_suspend_sem);
 ESP_LOGI("BUS_WORKER", "Resumed");
 for (int i = 0; i < SCHED_MAX_CHANNELS; i++) s_streams[i].len = 0;
}

void bus_worker_stop(void)
{
 if (s_rx_task_h)  { vTaskDelete(s_rx_task_h);  s_rx_task_h  = NULL; }
 if (s_cmd_u0_h)   { vTaskDelete(s_cmd_u0_h);   s_cmd_u0_h   = NULL; }
 if (s_cmd_u1_h)   { vTaskDelete(s_cmd_u1_h);   s_cmd_u1_h   = NULL; }
 if (s_cmd_spi_h)  { vTaskDelete(s_cmd_spi_h);  s_cmd_spi_h  = NULL; }
 if (s_cmd_i2c_h)  { vTaskDelete(s_cmd_i2c_h);  s_cmd_i2c_h  = NULL; }
}

uint32_t bus_worker_get_rx_timeout_count(int channel)
{
 if (channel >= 0 && channel < SCHED_MAX_CHANNELS) return s_rx_timeout_count[channel];
 return 0;
}
