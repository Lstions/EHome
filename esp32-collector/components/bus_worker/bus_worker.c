/**
 * @file bus_worker.c
 * @brief Per-bus command tasks + shared rx_task.
 *
 * Architecture:
 * - Four independent cmd_tasks (UART0, UART1, SPI, I2C) at priority 6
 * - Shared rx_task at priority 7 (higher) waiting on UART driver event queues
 * - Each cmd_task has its own queue, eliminating bus-type contention
 *
 * UART: TX is fire-and-forget (cmd_task). RX is event-driven (rx_task).
 * SPI/I2C: atomic transact(write+read) inside cmd_task, then enqueue the
 * response/report for the publisher task.
 *
 * P3 rx_task: ESP32 remains a transparent UART byte pipe, but uses one
 * automatic boundary policy: explicit read_size, fixed report blocks for
 * continuous input, and idle completion for the final partial block. No user
 * selectable receive mode or protocol-specific parser is introduced.
 *
 * P1-6: RX timeout drain — if a pending cmd has rx_timeout_ms > 0 and no
 * data received within that window, rx_task emits DataReport with error_code=0x01.
 *
 * P2-8: Decoupled from app_state_t — uses bus_runtime_t for DI.
 */

#include "bus_worker.h"
#include "bus_queue_policy.h"
#include "bus_rx_boundary.h"
#include "bus_dma.h"
#include "cmd_queue.h"
#include "scheduler.h"
#include "frame_codec.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "esp_task_wdt.h"  // Task watchdog
#include "rom/ets_sys.h"   // ets_delay_us — busy-wait without disabling interrupts
#include "freertos/FreeRTOS.h"
#include "freertos/event_groups.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include <limits.h>
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
#define UART_EVENT_QUEUE_DEPTH 32
#define UART_EVENT_SET_CAPACITY (SCHED_MAX_CHANNELS * UART_EVENT_QUEUE_DEPTH)
#define RX_WAKE_PERIOD_MS 250  /* lifecycle/timeout wake, not RX polling */
#define UART_RESPONSE_WAIT_MS 5 /* command fence only; RX uses driver events */
#define CMD_QUEUE_WAIT_MS 25 /* bounded wait so lifecycle suspend is observable */
#define WORKER_SUSPEND_TIMEOUT_MS 2000

/* P3: bus workers never publish MQTT from an RX/cmd task.  A descriptor owns
 * one fixed payload block until the report task has encoded and published it.
 * Separate pools make telemetry pressure unable to consume the critical
 * response reserve. */
#define REPORT_PAYLOAD_BLOCK_SIZE 1024
#define REPORT_CRITICAL_BLOCKS 4
#define REPORT_CRITICAL_EMERGENCY_BLOCKS 1
#define REPORT_TELEMETRY_BLOCKS 12
#define REPORT_CRITICAL_QUEUE_DEPTH REPORT_CRITICAL_BLOCKS
#define REPORT_TELEMETRY_QUEUE_DEPTH REPORT_TELEMETRY_BLOCKS
#define CONTROL_FINAL_QUEUE_DEPTH 8
#define WRITE_RSP_QUEUE_DEPTH 8
#define WRITE_RSP_MSG_MAX 64
#define CONTROL_FINAL_RAW_MAX 256
#define REPORT_TASK_STACK 4096
#define REPORT_TASK_PRIO 5

typedef struct {
 uint32_t channel_id;
 uint64_t timestamp_us;
 uint32_t sequence;
 uint32_t error_code;
 uint32_t request_id;
 uint32_t edge_device_id;
 uint32_t command_template_id;
 uint8_t command_index;
 uint8_t block_index;
 uint16_t len;
 bool critical;
 bool emergency;
} report_desc_t;

typedef struct {
 uint8_t slot;
 bool success;
 uint32_t error_code;
 uint16_t raw_len;
 uint8_t raw[CONTROL_FINAL_RAW_MAX];
} control_final_desc_t;

typedef struct {
 uint32_t request_id;
 bool success;
 uint32_t error_code;
 char error_msg[WRITE_RSP_MSG_MAX];
} write_rsp_desc_t;

static uint8_t s_critical_payload[REPORT_CRITICAL_BLOCKS][REPORT_PAYLOAD_BLOCK_SIZE];
static uint8_t s_critical_emergency_payload[REPORT_CRITICAL_EMERGENCY_BLOCKS][REPORT_PAYLOAD_BLOCK_SIZE];
static uint8_t s_telemetry_payload[REPORT_TELEMETRY_BLOCKS][REPORT_PAYLOAD_BLOCK_SIZE];
static QueueHandle_t s_report_critical_free;
static QueueHandle_t s_report_critical_emergency_free;
static QueueHandle_t s_report_telemetry_free;
static QueueHandle_t s_report_critical_q;
static QueueHandle_t s_report_critical_emergency_q;
static QueueHandle_t s_report_telemetry_q;
static QueueHandle_t s_control_final_q;
static QueueHandle_t s_write_rsp_q;
static QueueSetHandle_t s_report_ready_set;
static TaskHandle_t s_report_task_h;
static volatile uint32_t s_report_telemetry_drops;
static volatile uint32_t s_report_queue_high_water;
static bool s_report_path_started;

static bool report_is_critical(uint32_t error_code, uint32_t request_id,
                               uint32_t edge_device_id, uint32_t command_template_id)
{
 /* Routing metadata (edge/template IDs) is present on ordinary scheduled
  * samples too.  Only an error or an explicit request/response correlation is
  * critical and receives the reserved pool. */
 (void)edge_device_id;
 (void)command_template_id;
 return error_code != 0 || request_id != 0;
}

static void report_path_init(void);
static void report_path_deinit(void);
static uint32_t next_report_sequence(bus_runtime_t *rt, uint32_t channel_id);
static void queue_control_final(uint8_t slot, bool success, uint32_t error_code,
                                const uint8_t *raw, size_t raw_len);
static void queue_write_rsp(uint32_t request_id, bool success, uint32_t error_code,
                            const char *error_msg);
static void report_enqueue(uint32_t channel_id, uint64_t timestamp_us,
                           uint32_t sequence, const uint8_t *data, size_t len,
                           uint32_t error_code, uint32_t request_id,
                           uint32_t edge_device_id, uint32_t command_template_id,
                           uint8_t command_index);

/* Injected callbacks */
static write_rsp_cb_t s_write_rsp_cb = NULL;
static data_rpt_cb_t s_data_rpt_cb = NULL;
static channel_cmd_v2_final_cb_t s_channel_cmd_v2_final_cb = NULL;

#define SUSPEND_RX_BIT   BIT0
#define SUSPEND_U0_BIT   BIT1
#define SUSPEND_U1_BIT   BIT2
#define SUSPEND_SPI_BIT  BIT3
#define SUSPEND_I2C_BIT  BIT4
#define SUSPEND_U2_BIT   BIT5
#define SUSPEND_ALL_BITS (SUSPEND_RX_BIT | SUSPEND_U0_BIT | SUSPEND_U1_BIT | SUSPEND_SPI_BIT | SUSPEND_I2C_BIT | SUSPEND_U2_BIT)
static EventGroupHandle_t s_suspend_events;
static bool s_suspend_requested;
static bus_runtime_t *s_runtime;

static bool ensure_suspend_events(void) {
 if (!s_suspend_events) s_suspend_events = xEventGroupCreate();
 return s_suspend_events != NULL;
}

static void wait_if_suspended(EventBits_t bit) {
 if (!__atomic_load_n(&s_suspend_requested, __ATOMIC_ACQUIRE)) return;
 if (!ensure_suspend_events()) return;
 xEventGroupSetBits(s_suspend_events, bit);
 while (__atomic_load_n(&s_suspend_requested, __ATOMIC_ACQUIRE)) vTaskDelay(pdMS_TO_TICKS(1));
}

/* Task handles */
static TaskHandle_t s_cmd_u0_h  = NULL;
static TaskHandle_t s_cmd_u1_h  = NULL;
static TaskHandle_t s_cmd_u2_h  = NULL;
static TaskHandle_t s_cmd_spi_h = NULL;
static TaskHandle_t s_cmd_i2c_h = NULL;
static TaskHandle_t s_rx_task_h = NULL;
static QueueSetHandle_t s_cmd_u0_set = NULL;
static QueueSetHandle_t s_cmd_u1_set = NULL;
static QueueSetHandle_t s_cmd_u2_set = NULL;
static QueueSetHandle_t s_cmd_spi_set = NULL;
static QueueSetHandle_t s_cmd_i2c_set = NULL;
static QueueSetHandle_t s_uart_event_set = NULL;
static QueueHandle_t s_uart_event_members[SCHED_MAX_CHANNELS];
static size_t s_uart_event_member_count;

static void destroy_uart_event_set(void)
{
 if (!s_uart_event_set) return;
 for (size_t i = 0; i < s_uart_event_member_count; i++) {
  if (s_uart_event_members[i]) {
   /* All workers have acknowledged suspend before this function is called.
    * Drop stale driver notifications before removing a non-empty queue from
    * the QueueSet; bytes are discarded with the old driver during the same
    * configuration transaction and must not leak into the new lease. */
   (void)xQueueReset(s_uart_event_members[i]);
   (void)xQueueRemoveFromSet(s_uart_event_members[i], s_uart_event_set);
  }
 }
 vQueueDelete(s_uart_event_set);
 s_uart_event_set = NULL;
 memset(s_uart_event_members, 0, sizeof(s_uart_event_members));
 s_uart_event_member_count = 0;
}

static void destroy_cmd_queue_sets(void)
{
 if (s_cmd_u0_set) { vQueueDelete(s_cmd_u0_set); s_cmd_u0_set = NULL; }
 if (s_cmd_u1_set) { vQueueDelete(s_cmd_u1_set); s_cmd_u1_set = NULL; }
 if (s_cmd_u2_set) { vQueueDelete(s_cmd_u2_set); s_cmd_u2_set = NULL; }
 if (s_cmd_spi_set) { vQueueDelete(s_cmd_spi_set); s_cmd_spi_set = NULL; }
 if (s_cmd_i2c_set) { vQueueDelete(s_cmd_i2c_set); s_cmd_i2c_set = NULL; }
}

static QueueSetHandle_t create_cmd_queue_set(QueueHandle_t sample,
                                              QueueHandle_t control,
                                              const char *tag)
{
 if (!sample || !control) {
  ESP_LOGE(TAG_RX, "%s command queue pair unavailable", tag);
  return NULL;
 }
 QueueSetHandle_t set = xQueueCreateSet(24);
 BaseType_t sample_added = set ? xQueueAddToSet(sample, set) : pdFAIL;
 BaseType_t control_added = (sample_added == pdPASS)
   ? xQueueAddToSet(control, set) : pdFAIL;
 if (!set || sample_added != pdPASS || control_added != pdPASS) {
  ESP_LOGE(TAG_RX, "%s command queue set creation failed", tag);
  if (set) {
   if (control_added == pdPASS) (void)xQueueRemoveFromSet(control, set);
   if (sample_added == pdPASS) (void)xQueueRemoveFromSet(sample, set);
   vQueueDelete(set);
  }
  return NULL;
 }
 return set;
}

static void rebuild_cmd_queue_sets(bus_runtime_t *rt)
{
 destroy_cmd_queue_sets();
 if (!rt) return;
 s_cmd_u0_set = create_cmd_queue_set(rt->uart0_cmd_queue,
                                     rt->uart0_control_queue, "UART0");
 s_cmd_u1_set = create_cmd_queue_set(rt->uart1_cmd_queue,
                                     rt->uart1_control_queue, "UART1");
 s_cmd_u2_set = create_cmd_queue_set(rt->uart2_cmd_queue,
                                     rt->uart2_control_queue, "UART2");
 s_cmd_spi_set = create_cmd_queue_set(rt->spi_cmd_queue,
                                      rt->spi_control_queue, "SPI");
 s_cmd_i2c_set = create_cmd_queue_set(rt->i2c_cmd_queue,
                                      rt->i2c_control_queue, "I2C");
}

/* Consume control commands with priority, but after a bounded burst give a
 * ready sample one turn.  This preserves control latency without allowing a
 * continuous control producer to starve scheduled sampling forever. */
static bool receive_prioritized_command(QueueSetHandle_t set,
                                         QueueHandle_t sample,
                                         QueueHandle_t control,
                                         bus_cmd_t *cmd,
                                         uint8_t *control_burst)
{
 if (!cmd || !control_burst) return false;
 bool control_ready = control && uxQueueMessagesWaiting(control) > 0;
 bool sample_ready = sample && uxQueueMessagesWaiting(sample) > 0;
 bus_queue_decision_t decision = bus_queue_choose(control_ready, sample_ready,
                                                   control_burst);
 if (decision == BUS_QUEUE_DECISION_CONTROL &&
     xQueueReceive(control, cmd, 0) == pdTRUE) return true;
 if (decision == BUS_QUEUE_DECISION_SAMPLE &&
     xQueueReceive(sample, cmd, 0) == pdTRUE) return true;
 if (!set) {
  if (control && xQueueReceive(control, cmd, pdMS_TO_TICKS(CMD_QUEUE_WAIT_MS)) == pdTRUE) {
   if (*control_burst < BUS_CONTROL_BURST_MAX) (*control_burst)++;
   return true;
  }
  return sample && xQueueReceive(sample, cmd, pdMS_TO_TICKS(CMD_QUEUE_WAIT_MS)) == pdTRUE;
 }
 QueueSetMemberHandle_t member = xQueueSelectFromSet(set, pdMS_TO_TICKS(CMD_QUEUE_WAIT_MS));
 if (!member) return false;
 (void)member;
 /* Re-evaluate after the wakeup: another producer may have changed which
  * queue should win according to the shared fairness policy. */
 control_ready = control && uxQueueMessagesWaiting(control) > 0;
 sample_ready = sample && uxQueueMessagesWaiting(sample) > 0;
 decision = bus_queue_choose(control_ready, sample_ready, control_burst);
 if (decision == BUS_QUEUE_DECISION_CONTROL &&
     xQueueReceive(control, cmd, 0) == pdTRUE) return true;
 if (decision == BUS_QUEUE_DECISION_SAMPLE &&
     xQueueReceive(sample, cmd, 0) == pdTRUE) return true;
 return false;
}

static void report_free_block(bool critical, uint8_t index)
{
 QueueHandle_t free_q = critical ? s_report_critical_free : s_report_telemetry_free;
 if (free_q) (void)xQueueSend(free_q, &index, 0);
}

static void report_free_emergency_block(uint8_t index)
{
 if (s_report_critical_emergency_free)
  (void)xQueueSend(s_report_critical_emergency_free, &index, 0);
}

static void report_task(void *pv)
{
 (void)pv;
 report_desc_t desc;
 control_final_desc_t final;
 write_rsp_desc_t write_rsp;
 ESP_LOGI(TAG_RX, "Report task started (prio=%d)", uxTaskPriorityGet(NULL));
 esp_task_wdt_add(NULL);
 for (;;) {
  esp_task_wdt_reset();
  if (s_control_final_q && xQueueReceive(s_control_final_q, &final, 0) == pdTRUE) {
   if (s_channel_cmd_v2_final_cb)
   s_channel_cmd_v2_final_cb(final.slot, final.success, final.error_code,
                              final.raw_len ? final.raw : NULL, final.raw_len);
   esp_task_wdt_reset();
   continue;
  }
  if (s_write_rsp_q && xQueueReceive(s_write_rsp_q, &write_rsp, 0) == pdTRUE) {
   if (s_write_rsp_cb)
    s_write_rsp_cb(write_rsp.request_id, write_rsp.success, write_rsp.error_code,
                   write_rsp.error_msg[0] ? write_rsp.error_msg : NULL);
   esp_task_wdt_reset();
   continue;
  }
  /* report_tx participates in the task watchdog.  A permanent queue-set
   * wait would therefore look like a hung task during idle periods; use a
   * bounded wait so the loop can reset the watchdog even when no report is
   * pending.
   *
   * F8.2: the explicit critical/emergency pre-scan was removed per the
   * approved plan (边缘设备修复优化方案-v2.md F8.2).  Selection among the
   * ready-set members is FreeRTOS arrival-order FIFO: each member queue that
   * transitions empty→non-empty posts its handle to the set's internal event
   * queue, and xQueueSelectFromSet pops that FIFO.  Therefore when telemetry
   * becomes ready before a critical report, telemetry is serviced first —
   * critical/emergency priority is best-effort, not strict, under this
   * approved tradeoff (the plan's "add-to-set order" mitigation does not
   * change arrival order).  control_final/write_rsp keep strict priority via
   * the pre-scans above. */
  bool got_desc = false;
  {
   QueueSetMemberHandle_t member = s_report_ready_set
    ? xQueueSelectFromSet(s_report_ready_set, pdMS_TO_TICKS(1000)) : NULL;
   if (!member) continue;
   if (member == s_control_final_q) {
    if (xQueueReceive(member, &final, 0) == pdTRUE && s_channel_cmd_v2_final_cb)
     s_channel_cmd_v2_final_cb(final.slot, final.success, final.error_code,
                               final.raw_len ? final.raw : NULL, final.raw_len);
    continue;
   }
   if (member == s_write_rsp_q) {
    if (xQueueReceive(member, &write_rsp, 0) == pdTRUE && s_write_rsp_cb)
     s_write_rsp_cb(write_rsp.request_id, write_rsp.success, write_rsp.error_code,
                    write_rsp.error_msg[0] ? write_rsp.error_msg : NULL);
    continue;
   }
   got_desc = xQueueReceive(member, &desc, 0) == pdTRUE;
   if (!got_desc) continue;
  }

  const uint8_t *payload = desc.emergency
    ? s_critical_emergency_payload[desc.block_index]
    : (desc.critical ? s_critical_payload[desc.block_index]
                     : s_telemetry_payload[desc.block_index]);
  if (s_data_rpt_cb) {
   s_data_rpt_cb(desc.channel_id, desc.timestamp_us, desc.sequence,
                 payload, desc.len, desc.error_code, desc.request_id,
                 desc.edge_device_id, desc.command_template_id,
                 desc.command_index);
  }
  esp_task_wdt_reset();
  if (desc.emergency) report_free_emergency_block(desc.block_index);
  else report_free_block(desc.critical, desc.block_index);
 }
}

static void queue_control_final(uint8_t slot, bool success, uint32_t error_code,
                                const uint8_t *raw, size_t raw_len)
{
 if (raw_len > CONTROL_FINAL_RAW_MAX) {
  success = false;
  error_code = 0x1005U; /* bounded final payload */
  raw_len = 0;
 }
 control_final_desc_t final = {
  .slot = slot, .success = success, .error_code = error_code,
  .raw_len = (uint16_t)raw_len,
 };
 if (raw_len && raw) memcpy(final.raw, raw, raw_len);
 if (s_control_final_q && xQueueSend(s_control_final_q, &final, 0) == pdTRUE) return;
 /* The callback publishes a transport message.  Never invoke it from a bus
  * worker when the bounded hand-off is unavailable; dropping here preserves
  * the worker/transport isolation contract and is observable in the log. */
 ESP_LOGE(TAG_RX, "control final queue unavailable/full; dropping final");
}

static void queue_write_rsp(uint32_t request_id, bool success, uint32_t error_code,
                            const char *error_msg)
{
 write_rsp_desc_t rsp = {
  .request_id = request_id,
  .success = success,
  .error_code = error_code,
 };
 if (error_msg) {
  strncpy(rsp.error_msg, error_msg, sizeof(rsp.error_msg) - 1);
  rsp.error_msg[sizeof(rsp.error_msg) - 1] = '\0';
 }
 if (s_write_rsp_q && xQueueSend(s_write_rsp_q, &rsp, 0) == pdTRUE) return;
 /* WriteRsp is a transport-bearing response; do not call the callback from a
  * bus worker when the bounded hand-off is unavailable. */
 ESP_LOGE(TAG_RX, "write response queue unavailable/full; dropping request=%" PRIu32,
          request_id);
}

static bool report_alloc_block(bool critical, uint8_t *index)
{
 QueueHandle_t free_q = critical ? s_report_critical_free : s_report_telemetry_free;
 if (!free_q || !index) return false;
 return xQueueReceive(free_q, index, 0) == pdTRUE;
}

static bool report_alloc_emergency_block(uint8_t *index)
{
 if (!s_report_critical_emergency_free || !index) return false;
 return xQueueReceive(s_report_critical_emergency_free, index, 0) == pdTRUE;
}

static void report_enqueue(uint32_t channel_id, uint64_t timestamp_us,
                           uint32_t sequence, const uint8_t *data, size_t len,
                           uint32_t error_code, uint32_t request_id,
                           uint32_t edge_device_id, uint32_t command_template_id,
                           uint8_t command_index)
{
 bool critical = report_is_critical(error_code, request_id, edge_device_id,
                                    command_template_id);
 if (len > REPORT_PAYLOAD_BLOCK_SIZE || (!data && len != 0)) {
  ESP_LOGW(TAG_RX, "DataReport payload too large (%u > %u)",
           (unsigned)len, (unsigned)REPORT_PAYLOAD_BLOCK_SIZE);
  if (!critical) {
   __atomic_add_fetch(&s_report_telemetry_drops, 1, __ATOMIC_RELAXED);
   return;
  }
  /* Keep error reporting on the publisher task even for malformed oversized
   * critical responses; never call the transport callback from RX/cmd code. */
  error_code = 0x02;
  data = NULL;
  len = 0;
 }

 uint8_t index = 0;
 bool emergency = false;
 if (!report_alloc_block(critical, &index)) {
  if (!critical) {
   __atomic_add_fetch(&s_report_telemetry_drops, 1, __ATOMIC_RELAXED);
   return;
  }
  emergency = report_alloc_emergency_block(&index);
  if (!emergency) {
   /* This is only possible when the reserved normal and emergency slots are
    * both occupied.  Count it explicitly; transport remains off this task. */
   ESP_LOGE(TAG_RX, "critical report pools exhausted");
   __atomic_add_fetch(&s_report_telemetry_drops, 1, __ATOMIC_RELAXED);
   return;
  }
 }

 uint8_t *dst = emergency ? s_critical_emergency_payload[index]
                          : (critical ? s_critical_payload[index]
                                      : s_telemetry_payload[index]);
 if (len) memcpy(dst, data, len);
 report_desc_t desc = {
  .channel_id = channel_id, .timestamp_us = timestamp_us, .sequence = sequence,
  .error_code = error_code, .request_id = request_id,
  .edge_device_id = edge_device_id, .command_template_id = command_template_id,
  .command_index = command_index, .block_index = index, .len = (uint16_t)len,
  .critical = critical, .emergency = emergency,
 };
 QueueHandle_t q = emergency ? s_report_critical_emergency_q
                             : (critical ? s_report_critical_q : s_report_telemetry_q);
 if (!q || xQueueSend(q, &desc, 0) != pdTRUE) {
  if (emergency) report_free_emergency_block(index);
  else report_free_block(critical, index);
  __atomic_add_fetch(&s_report_telemetry_drops, 1, __ATOMIC_RELAXED);
  return;
 }
 UBaseType_t queued = uxQueueMessagesWaiting(s_report_critical_q) +
                      uxQueueMessagesWaiting(s_report_critical_emergency_q) +
                      uxQueueMessagesWaiting(s_report_telemetry_q);
 uint32_t old = __atomic_load_n(&s_report_queue_high_water, __ATOMIC_RELAXED);
 while (queued > old && !__atomic_compare_exchange_n(&s_report_queue_high_water, &old,
                                                      queued, false,
                                                      __ATOMIC_RELAXED, __ATOMIC_RELAXED)) { }
}

static void report_path_init(void)
{
 if (s_report_path_started) return;
 s_report_critical_free = xQueueCreate(REPORT_CRITICAL_BLOCKS, sizeof(uint8_t));
 s_report_critical_emergency_free = xQueueCreate(REPORT_CRITICAL_EMERGENCY_BLOCKS, sizeof(uint8_t));
 s_report_telemetry_free = xQueueCreate(REPORT_TELEMETRY_BLOCKS, sizeof(uint8_t));
 s_report_critical_q = xQueueCreate(REPORT_CRITICAL_QUEUE_DEPTH, sizeof(report_desc_t));
 s_report_critical_emergency_q = xQueueCreate(REPORT_CRITICAL_EMERGENCY_BLOCKS, sizeof(report_desc_t));
 s_report_telemetry_q = xQueueCreate(REPORT_TELEMETRY_QUEUE_DEPTH, sizeof(report_desc_t));
 s_control_final_q = xQueueCreate(CONTROL_FINAL_QUEUE_DEPTH, sizeof(control_final_desc_t));
 s_write_rsp_q = xQueueCreate(WRITE_RSP_QUEUE_DEPTH, sizeof(write_rsp_desc_t));
 s_report_ready_set = xQueueCreateSet(CONTROL_FINAL_QUEUE_DEPTH + WRITE_RSP_QUEUE_DEPTH +
                                      REPORT_CRITICAL_QUEUE_DEPTH +
                                      REPORT_CRITICAL_EMERGENCY_BLOCKS + REPORT_TELEMETRY_QUEUE_DEPTH);
 if (!s_report_critical_free || !s_report_telemetry_free || !s_report_critical_q ||
     !s_report_critical_emergency_free || !s_report_critical_emergency_q ||
     !s_report_telemetry_q || !s_control_final_q || !s_write_rsp_q || !s_report_ready_set ||
     xQueueAddToSet(s_control_final_q, s_report_ready_set) != pdPASS ||
     xQueueAddToSet(s_write_rsp_q, s_report_ready_set) != pdPASS ||
     xQueueAddToSet(s_report_critical_q, s_report_ready_set) != pdPASS ||
     xQueueAddToSet(s_report_critical_emergency_q, s_report_ready_set) != pdPASS ||
     xQueueAddToSet(s_report_telemetry_q, s_report_ready_set) != pdPASS) {
  ESP_LOGE(TAG_RX, "report path initialization failed");
  report_path_deinit();
  return;
 }
 for (uint8_t i = 0; i < REPORT_CRITICAL_BLOCKS; i++) (void)xQueueSend(s_report_critical_free, &i, 0);
 for (uint8_t i = 0; i < REPORT_CRITICAL_EMERGENCY_BLOCKS; i++) (void)xQueueSend(s_report_critical_emergency_free, &i, 0);
 for (uint8_t i = 0; i < REPORT_TELEMETRY_BLOCKS; i++) (void)xQueueSend(s_report_telemetry_free, &i, 0);
 s_report_telemetry_drops = 0;
 s_report_queue_high_water = 0;
 s_report_path_started = true;
 if (xTaskCreate(report_task, "report_tx", REPORT_TASK_STACK, NULL,
                 REPORT_TASK_PRIO, &s_report_task_h) != pdPASS) {
  ESP_LOGE(TAG_RX, "report task creation failed");
  s_report_path_started = false;
  report_path_deinit();
 }
}

static void report_path_deinit(void)
{
 if (s_report_task_h) { vTaskDelete(s_report_task_h); s_report_task_h = NULL; }
 if (s_report_ready_set) {
 if (s_control_final_q) (void)xQueueRemoveFromSet(s_control_final_q, s_report_ready_set);
 if (s_write_rsp_q) (void)xQueueRemoveFromSet(s_write_rsp_q, s_report_ready_set);
 if (s_report_critical_q) (void)xQueueRemoveFromSet(s_report_critical_q, s_report_ready_set);
  if (s_report_critical_emergency_q) (void)xQueueRemoveFromSet(s_report_critical_emergency_q, s_report_ready_set);
 if (s_report_telemetry_q) (void)xQueueRemoveFromSet(s_report_telemetry_q, s_report_ready_set);
  vQueueDelete(s_report_ready_set);
  s_report_ready_set = NULL;
 }
 if (s_report_critical_q) { vQueueDelete(s_report_critical_q); s_report_critical_q = NULL; }
 if (s_report_critical_emergency_q) { vQueueDelete(s_report_critical_emergency_q); s_report_critical_emergency_q = NULL; }
 if (s_report_telemetry_q) { vQueueDelete(s_report_telemetry_q); s_report_telemetry_q = NULL; }
 if (s_control_final_q) { vQueueDelete(s_control_final_q); s_control_final_q = NULL; }
 if (s_write_rsp_q) { vQueueDelete(s_write_rsp_q); s_write_rsp_q = NULL; }
 if (s_report_critical_free) { vQueueDelete(s_report_critical_free); s_report_critical_free = NULL; }
 if (s_report_critical_emergency_free) { vQueueDelete(s_report_critical_emergency_free); s_report_critical_emergency_free = NULL; }
 if (s_report_telemetry_free) { vQueueDelete(s_report_telemetry_free); s_report_telemetry_free = NULL; }
 s_report_path_started = false;
}

/* 8.1: Runtime counters */
static uint32_t s_rx_timeout_count[SCHED_MAX_CHANNELS];
static volatile bool s_plan_active[SCHED_MAX_CHANNELS];

/* ChannelCmdV2 batches still need one synchronous final result, but their RX
 * bytes must come from the same UART event owner as ordinary traffic.  The RX
 * task fills this bounded hand-off while the command worker waits on a task
 * notification; it no longer polls the UART driver ring behind rx_task's
 * back. */
typedef struct {
 uint8_t data[CONTROL_FINAL_RAW_MAX];
 volatile size_t len;
 volatile int64_t last_rx_us;
 volatile bool error;
 TaskHandle_t waiter;
} batch_rx_state_t;

static batch_rx_state_t s_batch_rx[SCHED_MAX_CHANNELS];

static void notify_batch_waiter(batch_rx_state_t *state)
{
 if (!state) return;
 TaskHandle_t waiter = state->waiter;
 if (waiter) xTaskNotifyGive(waiter);
}

static bool has_pending_cmd(bus_runtime_t *rt, int ch_idx);
static bool wait_for_uart_response_slot(bus_runtime_t *rt, int ch_idx,
                                        const char *tag, const bus_cmd_t *cmd);

/* Queue-set membership is rebuilt only while all workers are suspended.  A
 * manifest may move a logical Channel between UART0 and UART1, so the RX
 * path must discover event queues from the active runtime rather than keeping
 * a fixed port-to-device table. */
static void rebuild_uart_event_set(bus_runtime_t *rt)
{
 destroy_uart_event_set();
 if (!rt) return;

 QueueSetHandle_t set = xQueueCreateSet(UART_EVENT_SET_CAPACITY);
 if (!set) {
  ESP_LOGE(TAG_RX, "failed to create UART event queue set");
  return;
 }

 for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
  if (!rt->bus_ctx[i].initialized || rt->bus_ctx[i].bus_type != BUS_TYPE_UART)
   continue;
  QueueHandle_t event_queue = bus_dma_uart_event_queue(&rt->bus_ctx[i]);
  if (!event_queue) {
   ESP_LOGE(TAG_RX, "UART channel slot %d has no event queue", i);
   continue;
  }
  bool already_added = false;
  for (int j = 0; j < i; j++) {
   if (rt->bus_ctx[j].initialized &&
       bus_dma_uart_event_queue(&rt->bus_ctx[j]) == event_queue) {
    already_added = true;
    break;
   }
  }
  if (!already_added) {
   if (xQueueAddToSet(event_queue, set) != pdPASS) {
    ESP_LOGE(TAG_RX, "failed to add UART channel slot %d event queue", i);
   } else if (s_uart_event_member_count < SCHED_MAX_CHANNELS) {
    s_uart_event_members[s_uart_event_member_count++] = event_queue;
   }
  }
 }
 s_uart_event_set = set;
}

void bus_worker_set_callbacks(write_rsp_cb_t wr_cb, data_rpt_cb_t dr_cb)
{
 s_write_rsp_cb = wr_cb;
 s_data_rpt_cb  = dr_cb;
}

void bus_worker_set_channel_cmd_v2_final_cb(channel_cmd_v2_final_cb_t cb)
{
 s_channel_cmd_v2_final_cb = cb;
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

static bool enqueue_pending(bus_runtime_t *rt, int ch_idx, const bus_cmd_t *cmd)
{
 pending_cmd_t pcmd = {
  .edge_device_id  = cmd->edge_device_id,
  .command_template_id = cmd->command_template_id,
  .command_index   = cmd->command_index,
  .request_id      = (cmd->type == CMD_SAMPLE) ? 0 : cmd->request_id,
  .read_size       = cmd->read_size,
  .tx_timestamp    = esp_timer_get_time(),
  .rx_timeout_ms   = (cmd->type == CMD_WRITE) ? cmd->rx_timeout_ms : 1000,
  .channel_cmd_v2  = cmd->channel_cmd_v2,
  .control_slot    = cmd->control_slot,
 };
 pcmd.cmd_data_len = (cmd->tx_len < PENDING_CMD_DATA_MAX)
   ? cmd->tx_len : PENDING_CMD_DATA_MAX;
 if (pcmd.cmd_data_len > 0) {
  memcpy(pcmd.cmd_data, cmd->tx_data, pcmd.cmd_data_len);
 }
 if (!xQueueSend(rt->pending_queues[ch_idx], &pcmd, 0)) {
        ESP_LOGW(TAG_U0, "pending queue full ch%d, dropping", ch_idx);
        return false;
 }
 return true;
}

static void complete_control(const bus_cmd_t *cmd, bool success, uint32_t code,
                             const uint8_t *raw, size_t raw_len)
{
 if (cmd->channel_cmd_v2)
  queue_control_final(cmd->control_slot, success, code, raw, raw_len);
}

typedef struct {
 uint32_t kind;
 uint8_t tx[CMD_TX_MAX];
 size_t tx_len;
 uint32_t read_size;
 uint32_t rx_timeout_ms;
 uint32_t post_tx_delay_ms;
} batch_step_t;

static bool decode_batch_step(const uint8_t *data, size_t len, batch_step_t *step)
{
 if (!data || !step) return false;
 frame_decoder_t dec;
 frame_field_t field;
 bool seen[6] = {false};
 memset(step, 0, sizeof(*step));
 if (frame_decoder_init_sub(&dec, data, len) != FRAME_OK) return false;
 for (;;) {
  frame_err_t err = frame_decoder_next(&dec, &field);
  if (err == FRAME_DONE) break;
  if (err != FRAME_OK || field.field_num == 0 || field.field_num > 5 || seen[field.field_num]) return false;
  seen[field.field_num] = true;
  if (field.field_num == 2) {
   if (field.wire_type != WIRE_LENGTH_DELIMITED || field.value.bytes.len == 0 || field.value.bytes.len > CMD_TX_MAX) return false;
   memcpy(step->tx, field.value.bytes.ptr, field.value.bytes.len);
   step->tx_len = field.value.bytes.len;
   continue;
  }
  if (field.wire_type != WIRE_VARINT || field.value.varint > UINT32_MAX) return false;
  switch (field.field_num) {
  case 1: step->kind = (uint32_t)field.value.varint; break;
  case 3: step->read_size = (uint32_t)field.value.varint; break;
  case 4: step->rx_timeout_ms = (uint32_t)field.value.varint; break;
  case 5: step->post_tx_delay_ms = (uint32_t)field.value.varint; break;
  default: break;
  }
 }
 return seen[1] && seen[2] && seen[3] && seen[4] && seen[5] && step->kind <= 3 &&
        step->tx_len > 0 && step->read_size <= 256 && step->rx_timeout_ms > 0 &&
        step->rx_timeout_ms <= 30000 && step->post_tx_delay_ms <= 30000;
}

static bool uart_collect_response(int ch_idx, uint8_t *out, size_t cap,
                                  uint32_t expected, uint32_t timeout_ms, size_t *out_len)
{
 if (ch_idx < 0 || ch_idx >= SCHED_MAX_CHANNELS || !out || !out_len ||
     cap > CONTROL_FINAL_RAW_MAX) return false;
 batch_rx_state_t *state = &s_batch_rx[ch_idx];
 int64_t start = esp_timer_get_time();
 for (;;) {
  if (__atomic_load_n(&s_suspend_requested, __ATOMIC_ACQUIRE)) return false;
  size_t len = state->len;
  if (state->error || len > cap) return false;
  if (len > 0 && state->last_rx_us > 0 &&
      esp_timer_get_time() - state->last_rx_us >= 10000) {
   memcpy(out, state->data, len);
   *out_len = len;
   return expected == 0 || len >= expected;
  }
  int64_t elapsed_ms = (esp_timer_get_time() - start) / 1000;
  if (elapsed_ms > (int64_t)timeout_ms) return false;
  TickType_t wait_ticks = pdMS_TO_TICKS((uint32_t)(timeout_ms - elapsed_ms));
  if (wait_ticks == 0) wait_ticks = 1;
  (void)ulTaskNotifyTake(pdTRUE, wait_ticks);
  esp_task_wdt_reset();
 }
}

static bool execute_uart_batch(int ch_idx, bus_dma_ctx_t *ctx,
                               const bus_cmd_t *cmd, uint8_t *raw, size_t *raw_len,
                               uint32_t *error_code)
{
 if (ch_idx < 0 || ch_idx >= SCHED_MAX_CHANNELS || !ctx || !cmd || !raw ||
     !raw_len || !error_code || cmd->plan_step_count < 2 ||
     cmd->plan_step_count > CMD_BATCH_MAX_STEPS || cmd->plan_len == 0) return false;
 /* Drain bytes left by a previous request before claiming the sequence. */
 uint8_t drain[128];
 while (bus_dma_read(ctx, drain, sizeof(drain)) > 0) { }
 (void)ulTaskNotifyTake(pdTRUE, 0);
 s_batch_rx[ch_idx].waiter = xTaskGetCurrentTaskHandle();
 s_batch_rx[ch_idx].len = 0;
 s_batch_rx[ch_idx].last_rx_us = 0;
 s_batch_rx[ch_idx].error = false;
 s_plan_active[ch_idx] = true;
 size_t cursor = 0, encoded = 1;
 raw[0] = cmd->plan_step_count;
 for (uint8_t i = 0; i < cmd->plan_step_count; i++) {
  if (cursor + 2 > cmd->plan_len) { *error_code = 0x1100U + i; goto fail; }
  uint16_t step_len = (uint16_t)cmd->plan_data[cursor] | ((uint16_t)cmd->plan_data[cursor + 1] << 8);
  cursor += 2;
  if (step_len == 0 || cursor + step_len > cmd->plan_len) { *error_code = 0x1100U + i; goto fail; }
  batch_step_t step;
  if (!decode_batch_step(cmd->plan_data + cursor, step_len, &step)) { *error_code = 0x1100U + i; goto fail; }
  cursor += step_len;
  /* Reset the event-driven hand-off before TX so a fast peripheral response
   * cannot race a post-write reset. */
  s_batch_rx[ch_idx].len = 0;
  s_batch_rx[ch_idx].last_rx_us = 0;
  s_batch_rx[ch_idx].error = false;
  if (bus_dma_write(ctx, step.tx, step.tx_len) != ESP_OK) { *error_code = 0x1200U + i; goto fail; }
  apply_turnaround_delay(compute_turnaround_us(ctx));
  if (step.post_tx_delay_ms > 0) vTaskDelay(pdMS_TO_TICKS(step.post_tx_delay_ms));
  if (encoded + 3 > 256) { *error_code = 0x1300U + i; goto fail; }
  raw[encoded++] = (uint8_t)step.kind;
  raw[encoded] = 0;
  raw[encoded + 1] = 0;
  size_t response_len = 0;
  if (!uart_collect_response(ch_idx, raw + encoded + 2, 256 - encoded - 2,
                             step.read_size, step.rx_timeout_ms, &response_len)) {
   *error_code = 0x1400U + i;
   goto fail;
  }
  raw[encoded] = (uint8_t)(response_len & 0xffU);
  raw[encoded + 1] = (uint8_t)(response_len >> 8);
  encoded += 2 + response_len;
 }
 *raw_len = encoded;
 s_batch_rx[ch_idx].waiter = NULL;
 s_plan_active[ch_idx] = false;
 return true;
fail:
 s_batch_rx[ch_idx].waiter = NULL;
 s_plan_active[ch_idx] = false;
 *raw_len = 0;
 return false;
}

/* ------------------------------------------------------------------ */
/*  UART cmd loop                                                     */
/* ------------------------------------------------------------------ */

static void uart_cmd_loop(bus_runtime_t *rt, QueueHandle_t sample_queue,
                          QueueHandle_t control_queue, QueueSetHandle_t queue_set,
                          const char *tag, EventBits_t suspend_bit)
{
 bus_cmd_t cmd;
 uint8_t control_burst = 0;
 ESP_LOGI(tag, "Started (prio=%d)", uxTaskPriorityGet(NULL));
 esp_task_wdt_add(NULL);

 uint32_t txn = 0, errs = 0, no_ctx = 0;
 TickType_t last_stats = xTaskGetTickCount();

 while (1) {
  esp_task_wdt_reset();
  wait_if_suspended(suspend_bit);

  if (!receive_prioritized_command(queue_set, sample_queue, control_queue,
                                   &cmd, &control_burst)) continue;

  if (cmd.channel_cmd_v2) {
   ESP_LOGI(tag, "V2 execute ch=%lu tx=%u read=%lu timeout=%lu slot=%u",
    (unsigned long)cmd.channel_id, (unsigned)cmd.tx_len,
    (unsigned long)cmd.read_size, (unsigned long)cmd.rx_timeout_ms,
    (unsigned)cmd.control_slot);
  }

  bus_dma_ctx_t *ctx = rt->find_ctx(rt, cmd.channel_id);
  if (!ctx) {
   no_ctx++;
   if (cmd.channel_cmd_v2) complete_control(&cmd, false, 4, NULL, 0);
   else if (cmd.type == CMD_WRITE)
    queue_write_rsp(cmd.request_id, false, 4, "no ctx");
   scheduler_notify_channel_error(cmd.channel_id);
   continue;
  }
  txn++;

  int ch_idx = -1;
  for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
   if (rt->bus_ch[i] == cmd.channel_id) { ch_idx = i; break; }
  }
  /* UART responses carry no request ID.  Never transmit another
   * request/response command before rx_task has consumed the prior response. */
  if (cmd.bus_type == BUS_TYPE_UART && (cmd.type == CMD_SAMPLE || cmd.read_size > 0)) {
   if (!wait_for_uart_response_slot(rt, ch_idx, tag, &cmd)) {
    if (cmd.channel_cmd_v2) complete_control(&cmd, false, 1007, NULL, 0);
    else if (cmd.type == CMD_WRITE) queue_write_rsp(cmd.request_id, false, 1007, "configuration changed before dispatch");
    scheduler_notify_channel_error(cmd.channel_id);
    continue;
   }
  }

  if (cmd.channel_cmd_v2 && cmd.plan_step_count > 0) {
   uint8_t raw[256];
   size_t raw_len = 0;
   uint32_t code = 0x1000;
   bool ok = cmd.bus_type == BUS_TYPE_UART && execute_uart_batch(ch_idx, ctx, &cmd, raw, &raw_len, &code);
   if (!ok && __atomic_load_n(&s_suspend_requested, __ATOMIC_ACQUIRE)) code = 1007;
   complete_control(&cmd, ok, ok ? 0 : code, ok ? raw : NULL, ok ? raw_len : 0);
   if (ok) scheduler_notify_channel_success(cmd.channel_id);
   else scheduler_notify_channel_error(cmd.channel_id);
   continue;
  }

  if (cmd.type == CMD_WRITE) {
   esp_err_t e = bus_dma_write(ctx, cmd.tx_data, cmd.tx_len);
   if (e == ESP_OK) {
    if (cmd.read_size > 0) {
     /* Register correlation immediately after TX.  A fast Modbus slave can
      * start replying before the configured post-TX delay expires; delaying
      * this enqueue would turn that valid reply into uncorrelated telemetry. */
     bool pending_enqueued = false;
     for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
      if (rt->bus_ch[i] == cmd.channel_id) {
       pending_enqueued = enqueue_pending(rt, i, &cmd);
       if (!pending_enqueued) complete_control(&cmd, false, 0xFFFF, NULL, 0);
       break;
      }
     }
     uint32_t tu = compute_turnaround_us(ctx);
     apply_turnaround_delay(tu);
     if (cmd.channel_cmd_v2 && cmd.delay_ms > 0) {
      vTaskDelay(pdMS_TO_TICKS(cmd.delay_ms));
     }
    } else if (cmd.channel_cmd_v2) {
     complete_control(&cmd, true, 0, NULL, 0);
    }
    if (!cmd.channel_cmd_v2) queue_write_rsp(cmd.request_id, true, 0, NULL);
    scheduler_notify_channel_success(cmd.channel_id);
   } else {
    errs++;
    if (cmd.channel_cmd_v2) complete_control(&cmd, false, (uint32_t)e, NULL, 0);
    else queue_write_rsp(cmd.request_id, false, (uint32_t)e, "bus err");
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
       (void)enqueue_pending(rt, i, &cmd);
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

static void spi_i2c_cmd_loop(bus_runtime_t *rt, QueueHandle_t sample_queue,
                             QueueHandle_t control_queue, QueueSetHandle_t queue_set,
                             const char *tag, EventBits_t suspend_bit)
{
 bus_cmd_t cmd;
 uint8_t control_burst = 0;
 ESP_LOGI(tag, "Started (prio=%d)", uxTaskPriorityGet(NULL));
 esp_task_wdt_add(NULL);

 uint32_t txn = 0, errs = 0, no_ctx = 0;
 TickType_t last_stats = xTaskGetTickCount();

 while (1) {
  esp_task_wdt_reset();
  wait_if_suspended(suspend_bit);
  if (!receive_prioritized_command(queue_set, sample_queue, control_queue,
                                   &cmd, &control_burst)) continue;

  bus_dma_ctx_t *ctx = rt->find_ctx(rt, cmd.channel_id);
  if (!ctx) {
   no_ctx++;
   if (cmd.channel_cmd_v2) complete_control(&cmd, false, 4, NULL, 0);
   else if (cmd.type == CMD_WRITE)
    queue_write_rsp(cmd.request_id, false, 4, "no ctx");
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
    if (cmd.channel_cmd_v2) complete_control(&cmd, true, 0, rx, rl);
    else queue_write_rsp(cmd.request_id, true, 0, NULL);
    scheduler_notify_channel_success(cmd.channel_id);
    if (cmd.read_size > 0 && rl > 0) {
     uint64_t ts = esp_timer_get_time();
     report_enqueue(cmd.channel_id, ts, next_report_sequence(rt, cmd.channel_id), rx, rl, 0,
      cmd.request_id, cmd.edge_device_id, cmd.command_template_id, cmd.command_index);
    }
   } else {
    errs++;
    if (cmd.channel_cmd_v2) complete_control(&cmd, false, (uint32_t)e, NULL, 0);
    else queue_write_rsp(cmd.request_id, false, (uint32_t)e, "bus err");
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
     report_enqueue(cmd.channel_id, ts, next_report_sequence(rt, cmd.channel_id), rx, rl, 0,
      0, cmd.edge_device_id, cmd.command_template_id, cmd.command_index);
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
 uart_cmd_loop(rt, rt->uart0_cmd_queue, rt->uart0_control_queue,
               s_cmd_u0_set, TAG_U0, SUSPEND_U0_BIT);
}
static void cmd_task_uart1(void *pv) {
 bus_runtime_t *rt = (bus_runtime_t *)pv;
 uart_cmd_loop(rt, rt->uart1_cmd_queue, rt->uart1_control_queue,
               s_cmd_u1_set, TAG_U1, SUSPEND_U1_BIT);
}
static void cmd_task_uart2(void *pv) {
 bus_runtime_t *rt = (bus_runtime_t *)pv;
 uart_cmd_loop(rt, rt->uart2_cmd_queue, rt->uart2_control_queue,
               s_cmd_u2_set, "CMD_U2", SUSPEND_U2_BIT);
}
static void cmd_task_spi(void *pv) {
 bus_runtime_t *rt = (bus_runtime_t *)pv;
 spi_i2c_cmd_loop(rt, rt->spi_cmd_queue, rt->spi_control_queue,
                  s_cmd_spi_set, TAG_SPI, SUSPEND_SPI_BIT);
}
static void cmd_task_i2c(void *pv) {
 bus_runtime_t *rt = (bus_runtime_t *)pv;
 spi_i2c_cmd_loop(rt, rt->i2c_cmd_queue, rt->i2c_control_queue,
                  s_cmd_i2c_set, TAG_I2C, SUSPEND_I2C_BIT);
}

/* ------------------------------------------------------------------ */
/*  P3: rx_task — transparent UART byte pipe with automatic boundary   */
/*  Explicit lengths complete immediately; length-less input is emitted */
/*  in fixed blocks and closed by the common 10ms idle timeout.         */
/* ------------------------------------------------------------------ */

#define UART_IDLE_THRESHOLD_US 10000  /* protocol-neutral idle completion deadline */

static stream_rx_t s_streams[SCHED_MAX_CHANNELS];
static int64_t     s_last_rx_us[SCHED_MAX_CHANNELS];
static uint32_t    s_rx_sequence[SCHED_MAX_CHANNELS];
static bool        s_stream_chunked[SCHED_MAX_CHANNELS];
static uint32_t    s_rx_overflow_count[SCHED_MAX_CHANNELS];
static uint32_t    s_rx_error_count[SCHED_MAX_CHANNELS];

static bool has_pending_cmd(bus_runtime_t *rt, int ch_idx)
{
 return rt->pending_queues[ch_idx]
   && uxQueueMessagesWaiting(rt->pending_queues[ch_idx]) > 0;
}

/* Emit complete fixed-size chunks as soon as they are available.  A command
 * with an explicit read_size is emitted exactly at that length; a generic
 * command with no length is chunked at the common boundary and remains tied
 * to the pending descriptor until the final idle gap. */
static void emit_ready_stream_chunks(bus_runtime_t *rt, int idx, int64_t now_us)
{
 stream_rx_t *s = &s_streams[idx];
 while (s->len > 0) {
  pending_cmd_t pcmd;
  bool pending = rt->pending_queues[idx] &&
                 xQueuePeek(rt->pending_queues[idx], &pcmd, 0) == pdTRUE;
  size_t target = bus_rx_boundary_length(s->len, pending,
                                         pending && pcmd.channel_cmd_v2,
                                         pending ? pcmd.read_size : 0);
  if (target == 0) return;
  bool consume_pending = false;
  if (pending && pcmd.read_size > 0) {
   consume_pending = true;
  }

  if (pending && pcmd.channel_cmd_v2) {
   queue_control_final(pcmd.control_slot, true, 0, s->buffer, target);
  } else {
   report_enqueue(rt->bus_ch[idx], (uint64_t)now_us, ++s_rx_sequence[idx],
    s->buffer, target, 0, pending ? pcmd.request_id : 0,
    pending ? pcmd.edge_device_id : 0,
    pending ? pcmd.command_template_id : 0,
    pending ? pcmd.command_index : 0);
  }
  if (pending && consume_pending) (void)xQueueReceive(rt->pending_queues[idx], &pcmd, 0);
  if (pending && !consume_pending) s_stream_chunked[idx] = true;
  s->len -= target;
  if (s->len > 0) memmove(s->buffer, s->buffer + target, s->len);
  if (consume_pending && s->len == 0) s_last_rx_us[idx] = 0;
 }
}

static uint32_t next_report_sequence(bus_runtime_t *rt, uint32_t channel_id)
{
 if (rt) {
  for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
   if (rt->bus_ch[i] == channel_id) return ++s_rx_sequence[i];
  }
 }
 static uint32_t fallback_sequence;
 return ++fallback_sequence;
}

/* A UART response has no inherent request ID.  Sending a second read before
 * rx_task has consumed the first response makes the FIFO head own the next
 * bytes, associating a control reply with a periodic sample (or vice versa).
 * rx_task releases this bounded fence after a complete response or timeout. */
static bool wait_for_uart_response_slot(bus_runtime_t *rt, int ch_idx,
                                        const char *tag, const bus_cmd_t *cmd)
{
 if (__atomic_load_n(&s_suspend_requested, __ATOMIC_ACQUIRE)) return false;
 if (ch_idx < 0 || !has_pending_cmd(rt, ch_idx)) return true;
 ESP_LOGI(tag, "waiting for pending UART response ch=%lu before %s",
          (unsigned long)cmd->channel_id,
          cmd->channel_cmd_v2 ? "V2 command" : "next command");
 while (has_pending_cmd(rt, ch_idx)) {
  if (__atomic_load_n(&s_suspend_requested, __ATOMIC_ACQUIRE)) return false;
  vTaskDelay(pdMS_TO_TICKS(UART_RESPONSE_WAIT_MS));
 }
 return !__atomic_load_n(&s_suspend_requested, __ATOMIC_ACQUIRE);
}

static int uart_slot_from_event_queue(bus_runtime_t *rt,
                                      QueueSetMemberHandle_t member)
{
 if (!rt || !member) return -1;
 for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
  if (!rt->bus_ctx[i].initialized || rt->bus_ctx[i].bus_type != BUS_TYPE_UART)
   continue;
  if (bus_dma_uart_event_queue(&rt->bus_ctx[i]) == member) return i;
 }
 return -1;
}

static void rx_append_from_event(bus_runtime_t *rt, int idx, uint8_t *rx,
                                 size_t rx_cap)
{
 stream_rx_t *s = &s_streams[idx];
 for (;;) {
  size_t n = bus_dma_read(&rt->bus_ctx[idx], rx, rx_cap);
  if (n == 0) break;
  s_last_rx_us[idx] = esp_timer_get_time();
  if (s->overflow) {
   /* Continue draining the controller ring, but do not append bytes to an
    * already invalid frame.  The idle path emits one explicit failure. */
   continue;
  }
  if (s->len + n > STREAM_RX_BUF_SIZE) {
   s_rx_overflow_count[idx]++;
   ESP_LOGW(TAG_RX, "ch%d rx overflow (len=%d+%d > %d), flushing boundary",
    idx, (int)s->len, (int)n, (int)STREAM_RX_BUF_SIZE);
   emit_ready_stream_chunks(rt, idx, s_last_rx_us[idx]);
   if (s->len + n > STREAM_RX_BUF_SIZE) {
    /* The current response is no longer frameable.  Do not clear the
     * condition into a successful idle/short response. */
    s->overflow = true;
    s->len = 0;
    continue;
   }
  }
  if (n > STREAM_RX_BUF_SIZE) {
   s_rx_overflow_count[idx]++;
   s->overflow = true;
   s->len = 0;
   continue;
  }
  memcpy(s->buffer + s->len, rx, n);
  s->len += n;
  emit_ready_stream_chunks(rt, idx, s_last_rx_us[idx]);
  if (n < rx_cap) break;
 }
}

static bool complete_idle_response(bus_runtime_t *rt, int idx, int64_t now_us)
{
 stream_rx_t *s = &s_streams[idx];
 if (s_last_rx_us[idx] == 0 ||
     now_us - s_last_rx_us[idx] < UART_IDLE_THRESHOLD_US)
  return false;

 if (s->overflow) {
  pending_cmd_t pcmd;
  bool pending = rt->pending_queues[idx] &&
                 xQueueReceive(rt->pending_queues[idx], &pcmd, 0) == pdTRUE;
  if (pending && pcmd.channel_cmd_v2) {
   queue_control_final(pcmd.control_slot, false, 0x02, NULL, 0);
  } else if (pending) {
   report_enqueue(rt->bus_ch[idx], (uint64_t)now_us, ++s_rx_sequence[idx],
    NULL, 0, 0x02, pcmd.request_id, pcmd.edge_device_id,
    pcmd.command_template_id, pcmd.command_index);
  } else {
   report_enqueue(rt->bus_ch[idx], (uint64_t)now_us, ++s_rx_sequence[idx],
    NULL, 0, 0x02, 0, 0, 0, 0);
  }
  s->len = 0;
  s->overflow = false;
  s_stream_chunked[idx] = false;
  s_last_rx_us[idx] = 0;
  return true;
 }

 /* A length-less pending response may have emitted one or more full chunks.
  * The idle gap closes that descriptor even when the final chunk ended
  * exactly on a boundary and there are no residual bytes. */
 if (s->len == 0 && s_stream_chunked[idx] && has_pending_cmd(rt, idx)) {
  pending_cmd_t pcmd;
  if (xQueueReceive(rt->pending_queues[idx], &pcmd, 0) == pdTRUE) {
   s_stream_chunked[idx] = false;
   s_last_rx_us[idx] = 0;
   return true;
  }
 }
 if (s->len == 0) return false;

 if (has_pending_cmd(rt, idx)) {
  pending_cmd_t pcmd;
  if (xQueuePeek(rt->pending_queues[idx], &pcmd, 0) != pdTRUE) return false;
  if (pcmd.read_size > 0 && s->len < pcmd.read_size) {
   (void)xQueueReceive(rt->pending_queues[idx], &pcmd, 0);
   if (pcmd.channel_cmd_v2) {
    queue_control_final(pcmd.control_slot, false, 0x03, NULL, 0);
   } else {
    report_enqueue(rt->bus_ch[idx], (uint64_t)now_us, ++s_rx_sequence[idx],
     NULL, 0, 0x03, pcmd.request_id, pcmd.edge_device_id,
     pcmd.command_template_id, pcmd.command_index);
   }
   s->len = 0;
   s_stream_chunked[idx] = false;
   s_last_rx_us[idx] = 0;
   return true;
  }
  if (pcmd.channel_cmd_v2) {
   queue_control_final(pcmd.control_slot, true, 0, s->buffer, s->len);
  } else {
   report_enqueue(rt->bus_ch[idx], (uint64_t)now_us, ++s_rx_sequence[idx],
    s->buffer, s->len, 0, pcmd.request_id, pcmd.edge_device_id,
    pcmd.command_template_id, pcmd.command_index);
  }
  (void)xQueueReceive(rt->pending_queues[idx], &pcmd, 0);
 } else {
  /* Passive/terminal data follows the same automatic boundary path. */
  report_enqueue(rt->bus_ch[idx], (uint64_t)now_us, ++s_rx_sequence[idx],
   s->buffer, s->len, 0, 0, 0, 0, 0);
 }
 s->len = 0;
 s_stream_chunked[idx] = false;
 s_last_rx_us[idx] = 0;
 return true;
}

static void handle_uart_event(bus_runtime_t *rt, int idx,
                              const uart_event_t *event, uint8_t *rx,
                              size_t rx_cap)
{
 if (!rt || idx < 0 || idx >= SCHED_MAX_CHANNELS || !event || !rx || rx_cap == 0)
  return;

 /* ChannelCmdV2 batch responses share the same UART event owner as ordinary
  * traffic.  While a batch is active, hand bytes to its bounded response
  * buffer and wake the command task; never let this path fall back to a
  * second reader or to the transparent stream accumulator. */
 if (s_plan_active[idx]) {
  batch_rx_state_t *state = &s_batch_rx[idx];
  switch (event->type) {
  case UART_DATA:
  case UART_PATTERN_DET:
   for (;;) {
    size_t n = bus_dma_read(&rt->bus_ctx[idx], rx, rx_cap);
    if (n == 0) break;
    if (state->len + n > sizeof(state->data)) {
     state->error = true;
     (void)uart_flush_input(rt->bus_ctx[idx].cfg.uart.port);
     break;
    }
    memcpy(state->data + state->len, rx, n);
    state->len += n;
    state->last_rx_us = esp_timer_get_time();
    if (n < rx_cap) break;
   }
   notify_batch_waiter(state);
   break;
  case UART_FIFO_OVF:
  case UART_BUFFER_FULL:
  case UART_BREAK:
  case UART_PARITY_ERR:
  case UART_FRAME_ERR:
   state->error = true;
   s_rx_error_count[idx]++;
   (void)uart_flush_input(rt->bus_ctx[idx].cfg.uart.port);
   notify_batch_waiter(state);
   break;
  default:
   notify_batch_waiter(state);
   break;
  }
  return;
 }

 switch (event->type) {
 case UART_DATA:
 case UART_PATTERN_DET:
  rx_append_from_event(rt, idx, rx, rx_cap);
  break;
 case UART_FIFO_OVF:
 case UART_BUFFER_FULL:
  s_rx_error_count[idx]++;
  s_rx_overflow_count[idx]++;
  s_streams[idx].len = 0;
  s_streams[idx].overflow = true;
  s_last_rx_us[idx] = esp_timer_get_time();
  (void)uart_flush_input(rt->bus_ctx[idx].cfg.uart.port);
  ESP_LOGW(TAG_RX, "UART%d event=%d size=%" PRIu32 "; input reset",
   (int)rt->bus_ctx[idx].cfg.uart.port, (int)event->type,
   (uint32_t)event->size);
  break;
 case UART_BREAK:
 case UART_PARITY_ERR:
 case UART_FRAME_ERR:
  /* USB-UART bridges can leave a line-status event queued while valid bytes
   * arrive immediately afterwards.  Flushing here would discard those
   * bytes before their UART_DATA event is consumed.  Drain any bytes already
   * buffered and preserve the stream boundary; only FIFO/buffer overflow
   * above unconditionally resets the input ring. */
  s_rx_error_count[idx]++;
  rx_append_from_event(rt, idx, rx, rx_cap);
  ESP_LOGW(TAG_RX, "UART%d event=%d size=%" PRIu32 "; retained buffered input",
   (int)rt->bus_ctx[idx].cfg.uart.port, (int)event->type,
   (uint32_t)event->size);
  break;
 default:
  /* Wakeup and other target-specific events do not carry payload, but are
   * intentionally consumed so they cannot starve DATA events. */
  break;
 }
}

static uint32_t expire_uart_state(bus_runtime_t *rt)
{
 int64_t now_us = esp_timer_get_time();
 uint32_t completions = 0;
 for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
  if (!rt->pending_queues[i] || !rt->bus_ctx[i].initialized ||
      rt->bus_ctx[i].bus_type != BUS_TYPE_UART) continue;

  if (complete_idle_response(rt, i, now_us)) completions++;

  pending_cmd_t pcmd;
  if (xQueuePeek(rt->pending_queues[i], &pcmd, 0) != pdTRUE ||
      pcmd.rx_timeout_ms == 0 || pcmd.tx_timestamp == 0) continue;
  int64_t elapsed_ms = (now_us - pcmd.tx_timestamp) / 1000;
  if (elapsed_ms <= (int64_t)pcmd.rx_timeout_ms) continue;
  if (xQueueReceive(rt->pending_queues[i], &pcmd, 0) != pdTRUE) continue;
  completions++;
  s_rx_timeout_count[i]++;
  ESP_LOGW(TAG_RX, "UART RX timeout reqID=%lu (%lldms)",
   (unsigned long)pcmd.request_id, (long long)elapsed_ms);
  if (pcmd.channel_cmd_v2) {
   queue_control_final(pcmd.control_slot, false, 1, NULL, 0);
  } else {
   report_enqueue(rt->bus_ch[i], (uint64_t)now_us, ++s_rx_sequence[i], NULL, 0, 0x01,
    pcmd.request_id, pcmd.edge_device_id, pcmd.command_template_id,
    pcmd.command_index);
  }
  /* The timeout already completed the pending descriptor.  Discard every
   * residual stream fragment, not only an overflow marker: a short response
   * that reaches the hard timeout before the idle boundary must never be
   * reclassified as passive telemetry on a later tick. */
  s_streams[i].len = 0;
  s_streams[i].overflow = false;
  s_stream_chunked[i] = false;
  s_last_rx_us[i] = 0;
 }
 return completions;
}

static TickType_t rx_wait_ticks(bus_runtime_t *rt)
{
 int64_t now_us = esp_timer_get_time();
 int64_t next_us = now_us + (int64_t)RX_WAKE_PERIOD_MS * 1000;
 for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
  if (!rt->bus_ctx[i].initialized || rt->bus_ctx[i].bus_type != BUS_TYPE_UART)
   continue;
  if ((s_streams[i].len > 0 || s_streams[i].overflow) && s_last_rx_us[i] > 0) {
   int64_t deadline = s_last_rx_us[i] + UART_IDLE_THRESHOLD_US;
   if (deadline < next_us) next_us = deadline;
  }
  pending_cmd_t pcmd;
  if (rt->pending_queues[i] && xQueuePeek(rt->pending_queues[i], &pcmd, 0) == pdTRUE &&
      pcmd.rx_timeout_ms > 0 && pcmd.tx_timestamp > 0) {
   int64_t deadline = pcmd.tx_timestamp + (int64_t)pcmd.rx_timeout_ms * 1000;
   if (deadline < next_us) next_us = deadline;
  }
 }
 int64_t delta_us = next_us - now_us;
 if (delta_us <= 0) return 0;
 TickType_t ticks = pdMS_TO_TICKS((uint32_t)((delta_us + 999) / 1000));
 return ticks == 0 ? 1 : ticks;
}

static void rx_task(void *pv)
{
 bus_runtime_t *rt = (bus_runtime_t *)pv;
 uint8_t rx[256];

 ESP_LOGI(TAG_RX, "Started (prio=%d, event-driven)", uxTaskPriorityGet(NULL));
 esp_task_wdt_add(NULL);

 uint32_t events = 0, completions = 0;
 TickType_t last_stats = xTaskGetTickCount();

 while (1) {
  esp_task_wdt_reset();
  wait_if_suspended(SUSPEND_RX_BIT);

  QueueSetMemberHandle_t member = NULL;
  if (s_uart_event_set)
   member = xQueueSelectFromSet(s_uart_event_set, rx_wait_ticks(rt));
  if (member) {
   uart_event_t event;
   while (xQueueReceive(member, &event, 0) == pdTRUE) {
    int idx = uart_slot_from_event_queue(rt, member);
    if (idx >= 0) {
     handle_uart_event(rt, idx, &event, rx, sizeof(rx));
     events++;
    }
   }
  }

  completions += expire_uart_state(rt);

  TickType_t now = xTaskGetTickCount();
  if (now - last_stats > pdMS_TO_TICKS(10000)) {
   ESP_LOGI(TAG_RX, "Stats: events=%" PRIu32 " completions=%" PRIu32
    " report_drop=%" PRIu32 " report_q_high=%" PRIu32,
    events, completions, bus_worker_get_report_drop_count(),
    bus_worker_get_report_queue_high_water());
   events = 0;
   completions = 0;
   last_stats = now;
  }
 }
}

/* ------------------------------------------------------------------ */
/*  Public API                                                        */
/* ------------------------------------------------------------------ */

void bus_worker_start(bus_runtime_t *rt)
{
 ensure_suspend_events();
 s_runtime = rt;
 report_path_init();
 rebuild_cmd_queue_sets(rt);
 rebuild_uart_event_set(rt);
 xTaskCreate(rx_task, "rx_task", RX_STACK,
  (void *)rt, RX_PRIO, &s_rx_task_h);
 xTaskCreate(cmd_task_uart0, "cmd_u0", UART_STACK,
  (void *)rt, CMD_PRIO, &s_cmd_u0_h);
 xTaskCreate(cmd_task_uart1, "cmd_u1", UART_STACK,
  (void *)rt, CMD_PRIO, &s_cmd_u1_h);
 xTaskCreate(cmd_task_uart2, "cmd_u2", UART_STACK,
  (void *)rt, CMD_PRIO, &s_cmd_u2_h);
 xTaskCreate(cmd_task_spi, "cmd_spi", SPI_I2C_STACK,
  (void *)rt, CMD_PRIO, &s_cmd_spi_h);
 xTaskCreate(cmd_task_i2c, "cmd_i2c", SPI_I2C_STACK,
  (void *)rt, CMD_PRIO, &s_cmd_i2c_h);
}

bool bus_worker_suspend(void)
{
 if (!ensure_suspend_events()) {
  ESP_LOGE("BUS_WORKER", "Cannot suspend: event group unavailable");
  return false;
 }
 ESP_LOGI("BUS_WORKER", "Suspending and waiting for in-flight transactions");
 xEventGroupClearBits(s_suspend_events, SUSPEND_ALL_BITS);
 __atomic_store_n(&s_suspend_requested, true, __ATOMIC_RELEASE);
 for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
  if (s_plan_active[i]) notify_batch_waiter(&s_batch_rx[i]);
 }
 EventBits_t active = 0;
 if (s_rx_task_h) active |= SUSPEND_RX_BIT;
 if (s_cmd_u0_h) active |= SUSPEND_U0_BIT;
 if (s_cmd_u1_h) active |= SUSPEND_U1_BIT;
 if (s_cmd_u2_h) active |= SUSPEND_U2_BIT;
 if (s_cmd_spi_h) active |= SUSPEND_SPI_BIT;
 if (s_cmd_i2c_h) active |= SUSPEND_I2C_BIT;
 if (active) {
  EventBits_t observed = xEventGroupWaitBits(
      s_suspend_events, active, pdFALSE, pdTRUE,
      pdMS_TO_TICKS(WORKER_SUSPEND_TIMEOUT_MS));
  if ((observed & active) != active) {
   ESP_LOGE("BUS_WORKER", "Suspend timeout: active=0x%02x observed=0x%02x",
            (unsigned)active, (unsigned)observed);
   __atomic_store_n(&s_suspend_requested, false, __ATOMIC_RELEASE);
   xEventGroupClearBits(s_suspend_events, SUSPEND_ALL_BITS);
   return false;
  }
 }
 /* Detach before bus teardown can delete UART driver event queues.  The set
  * is rebuilt after the new dynamic channel/controller assignment is live. */
 destroy_uart_event_set();
 return true;
}

void bus_worker_resume(void)
{
 for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
  s_streams[i].len = 0;
  s_streams[i].overflow = false;
  s_stream_chunked[i] = false;
  s_last_rx_us[i] = 0;
 }
 rebuild_uart_event_set(s_runtime);
 __atomic_store_n(&s_suspend_requested, false, __ATOMIC_RELEASE);
 if (s_suspend_events) xEventGroupClearBits(s_suspend_events, SUSPEND_ALL_BITS);
 ESP_LOGI("BUS_WORKER", "Resumed");
}

static void discard_command_queue(QueueHandle_t queue)
{
 if (!queue) return;
 bus_cmd_t cmd;
 while (xQueueReceive(queue, &cmd, 0) == pdTRUE) {
  if (cmd.channel_cmd_v2) complete_control(&cmd, false, 1007, NULL, 0);
  else if (cmd.type == CMD_WRITE)
   queue_write_rsp(cmd.request_id, false, 1007, "configuration changed before dispatch");
 }
}

void bus_worker_discard_queued(bus_runtime_t *rt)
{
 if (!rt || rt != s_runtime || !__atomic_load_n(&s_suspend_requested, __ATOMIC_ACQUIRE)) return;
 discard_command_queue(rt->uart0_cmd_queue);
 discard_command_queue(rt->uart1_cmd_queue);
 discard_command_queue(rt->uart2_cmd_queue);
 discard_command_queue(rt->spi_cmd_queue);
 discard_command_queue(rt->i2c_cmd_queue);
 discard_command_queue(rt->uart0_control_queue);
 discard_command_queue(rt->uart1_control_queue);
 discard_command_queue(rt->uart2_control_queue);
 discard_command_queue(rt->spi_control_queue);
 discard_command_queue(rt->i2c_control_queue);
 for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
  pending_cmd_t pending;
  while (rt->pending_queues[i] && xQueueReceive(rt->pending_queues[i], &pending, 0) == pdTRUE) {
   if (pending.channel_cmd_v2)
    queue_control_final(pending.control_slot, false, 1007, NULL, 0);
   else if (pending.request_id)
    queue_write_rsp(pending.request_id, false, 1007, "configuration changed before response");
  }
 }
}

void bus_worker_stop(void)
{
 if (s_rx_task_h)  { vTaskDelete(s_rx_task_h);  s_rx_task_h  = NULL; }
 if (s_cmd_u0_h)   { vTaskDelete(s_cmd_u0_h);   s_cmd_u0_h   = NULL; }
 if (s_cmd_u1_h)   { vTaskDelete(s_cmd_u1_h);   s_cmd_u1_h   = NULL; }
 if (s_cmd_u2_h)   { vTaskDelete(s_cmd_u2_h);   s_cmd_u2_h   = NULL; }
 if (s_cmd_spi_h)  { vTaskDelete(s_cmd_spi_h);  s_cmd_spi_h  = NULL; }
 if (s_cmd_i2c_h)  { vTaskDelete(s_cmd_i2c_h);  s_cmd_i2c_h  = NULL; }
 destroy_cmd_queue_sets();
 destroy_uart_event_set();
 report_path_deinit();
}

uint32_t bus_worker_get_rx_timeout_count(int channel)
{
 if (channel >= 0 && channel < SCHED_MAX_CHANNELS) return s_rx_timeout_count[channel];
 return 0;
}

uint32_t bus_worker_get_report_drop_count(void)
{
 return __atomic_load_n(&s_report_telemetry_drops, __ATOMIC_RELAXED);
}

uint32_t bus_worker_get_report_queue_high_water(void)
{
 return __atomic_load_n(&s_report_queue_high_water, __ATOMIC_RELAXED);
}

uint32_t bus_worker_get_min_stack_watermark(void)
{
 TaskHandle_t handles[] = {s_cmd_u0_h, s_cmd_u1_h, s_cmd_u2_h, s_cmd_spi_h, s_cmd_i2c_h, s_rx_task_h};
 uint32_t minimum = UINT32_MAX;
 for (size_t i = 0; i < sizeof(handles) / sizeof(handles[0]); i++) {
  if (!handles[i]) continue;
  uint32_t value = (uint32_t)uxTaskGetStackHighWaterMark(handles[i]);
  if (value < minimum) minimum = value;
 }
 return minimum == UINT32_MAX ? 0 : minimum;
}
