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
#include "report_stats.h"   /* D-14：上报统计量已搬到中立组件，打破依赖环 */
#include "data_batch_codec.h"   /* D-18：帧长算术的唯一定义处 */
#include "bus_queue_policy.h"
#include "bus_rx_boundary.h"
#include "bus_dma.h"
#include "cmd_queue.h"
#include "scheduler.h"
#include "collector_mem.h"
#include "frame_codec.h"
/* V3-2a：能力位读取（契约 §1）。只取这个轻量头，刻意**不**引入
 * msg_handler.h —— 它会把 scheduler/config_mgr/esp_err 整条头链拖进
 * bus_worker。DataBatch 的**编码**不在这里：见 bus_worker.h 的
 * data_batch_cb_t 注释（组件门禁 1024 B，而一帧需要 1400 B 缓冲）。 */
#include "handler_hello.h"
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
/* 3072 was measured against spi_transact()'s ORIGINAL frame, which held no
 * scratch.  The full-duplex write-then-read fix in bus_dma.c added a
 * (tx_len + rx_size)-byte frame pair (SPI_FD_FRAME_MAX each) to that frame, so
 * the deepest SPI path is now roughly spi_i2c_cmd_loop (1072) +
 * bus_dma_transact (32) + spi_transact (864) + the IDF driver chain.
 *
 * -fstack-usage on the S3 build reports spi_transact = 864 bytes static; at
 * 3072 the remaining margin was under 30%, which is the same shape as the
 * 2026-10-04 stack overflows (log_tx, status_task) that this repo already paid
 * for.  4096 matches UART_STACK/RX_STACK and restores a ~45% margin. */
#define SPI_I2C_STACK 4096
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
/* WS-C: 12 -> 8, per Lead 2026-10-05.  Backpressure model at full population
 * (S3: 5 x 100 Hz = 500 samples/s): a telemetry block is held from enqueue
 * until report_tx calls msg_handler_send_data_report() and MQTT enqueue
 * returns -- ~1-5 ms, NOT until the network ACK.  Mean in-flight is therefore
 * ~500/s x 5 ms = 2.5 blocks; 8 gives ~3x margin.  Judgement variable is
 * bus_worker_get_report_drop_count()==0 under the task-4 stress run; if drops
 * appear, raise this back to 12 rather than relaxing the criterion.  The
 * critical/emergency reserves stay internal RAM and are deliberately NOT
 * reduced (they carry errors and V2 control finals). */
#define REPORT_TELEMETRY_BLOCKS 8
#define REPORT_CRITICAL_QUEUE_DEPTH REPORT_CRITICAL_BLOCKS
#define REPORT_TELEMETRY_QUEUE_DEPTH REPORT_TELEMETRY_BLOCKS
#define CONTROL_FINAL_QUEUE_DEPTH 8
#define WRITE_RSP_QUEUE_DEPTH 8
#define WRITE_RSP_MSG_MAX 64
#define CONTROL_FINAL_RAW_MAX 256
/* report_tx 的栈。4096 在 2026-10-04 的排查中被判定**不安全**：本任务通过
 *   bus_worker_set_callbacks(..., msg_handler_send_data_report)
 * 直接调用 msg_handler_send_data_report，而该函数有 2416 字节的栈帧
 * （components/msg_handler/handler_data.c:254，其中 uint8_t buf[1400] 是
 * "至少 1024 字节数据块 + 报头开销"所必需的，注释里写明了不能缩小），
 * 之后还要走 msg_handler_publish → MQTT → lwIP 这条 1KB 以上的链。
 * 2416 + 1000+ 已超过 4096 —— 与 status_task 那次栈溢出同形，只是触发它需要
 * 特定的数据块尺寸，所以此前没有被观测到。
 *
 * 取 6144：给 2416 帧 + 发布链留约 2 倍余量。这是**栈空间换确定性**的选择：
 * 栈在任务创建时一次性预留，不像堆分配那样有失败路径与碎片风险。
 * 同类修复见 components/log_stream/log_stream.c（LOG_TX_STACK 1536→4096）。
 *
 * ⚠ 根因未消除：send_data_report 仍把 2416 字节放在栈上。新加的组件级
 * -Wframe-larger-than 门禁会盯着它，若将来再叠大缓冲，构建即失败。 */
/* 报告任务栈。**由 6144 降到 4096（2026-10-05，实测依据）**。
 *
 * 依据：uxTaskGetStackHighWaterMark() 实测该任务峰值只用 3464 字节
 * （未用余量 2680）。取 4096 保留约 630 字节余量，相对峰值约 1.18 倍。
 *
 * 为什么现在敢按实测值收紧：S3 的 14 个任务栈全部走堆分配，标称合计
 * 约 64KB，而配置事务后只剩 844 字节可用堆，导致 MQTT 上报时 lwIP 分配
 * pbuf 失败、整个连接被判定为致命错误而断开。
 * 且 MQTT 未来要启用 TLS（TLS 上下文通常 16~40KB），当前余量根本不够，
 * 必须先把无谓的栈预留压下去。
 *
 * 注意上面那段警告仍然成立：send_data_report 把 2416 字节放在栈上，
 * 组件级 -Wframe-larger-than 门禁会继续盯着它。4096 相对 2416 的帧
 * 有 1680 字节余量，仍高于该门禁要求。若将来再叠大缓冲，构建会失败，
 * 那时应提高此值而不是放宽门禁。 */
#define REPORT_TASK_STACK 4096
#define REPORT_TASK_PRIO 5

/* ------------------------------------------------------------------ *
 *  V3-2a DataBatch(0x20) 聚合参数（契约 §3）
 * ------------------------------------------------------------------ */

/* 聚合窗口。100 Hz 每通道间隔 10 ms，20 ms 通常能攒 2 个，且远低于契约
 * 提到的 50 ms 上限。 */
#define DATA_BATCH_WINDOW_MS 20
/* 契约 §2.1.3：一帧最多 4 个样本。 */
#define DATA_BATCH_MAX_SAMPLES 4
/* 编码缓冲预算。真正的 buf[1400] 在 msg_handler 的批量编码器里（契约
 * §2.2 所指），bus_worker 只用这个常量做"降 n"的**尺寸预言**，自身不分配
 * 任何编码缓冲 —— 见 bus_worker.h 的 data_batch_cb_t 注释。 */
#define DATA_BATCH_BUF_SIZE 1400

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

/* Critical and emergency payload reserves are small and directly tied to the
 * error/control path: they stay INTERNAL RAM unconditionally.  The telemetry
 * pool (REPORT_TELEMETRY_BLOCKS x 1 KiB) is pure CPU-accessed sample data and
 * moves to PSRAM on PSRAM models, allocated in report_path_init(); the
 * internal-only models keep the static array so their layout and host-test
 * behaviour are unchanged. */
static uint8_t s_critical_payload[REPORT_CRITICAL_BLOCKS][REPORT_PAYLOAD_BLOCK_SIZE];
static uint8_t s_critical_emergency_payload[REPORT_CRITICAL_EMERGENCY_BLOCKS][REPORT_PAYLOAD_BLOCK_SIZE];
#if COLLECTOR_MEM_PSRAM_ENABLED
static uint8_t (*s_telemetry_payload)[REPORT_PAYLOAD_BLOCK_SIZE];
#else
static uint8_t s_telemetry_payload[REPORT_TELEMETRY_BLOCKS][REPORT_PAYLOAD_BLOCK_SIZE];
#endif

/* s_streams is rx_task's CPU-side linearisation buffer (no DMA, no ISR): on
 * PSRAM models it is allocated here in report_path_init() alongside the
 * telemetry pool; internal-only models keep the static array.  The storage is
 * declared BEFORE report_path_init()/deinit() so the PSRAM branch can see it.
 * See collector_mem.h for the placement policy. */
#if COLLECTOR_MEM_PSRAM_ENABLED
static stream_rx_t *s_streams;
#else
static stream_rx_t s_streams[SCHED_MAX_CHANNELS];
#endif

/* Branch-local readiness: comparing an array address to NULL trips
 * -Werror=address in the non-PSRAM branch, so the test lives in one place. */
static bool worker_buffers_ready(void)
{
#if COLLECTOR_MEM_PSRAM_ENABLED
    return s_streams != NULL && s_telemetry_payload != NULL;
#else
    return true;
#endif
}
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
/* D-14：这两个计数器已搬到 components/report_stats ——
 * msg_handler 要在上报帧里读它们，而它不该为了两个计数器就
 * REQUIRES 整个 bus_worker（那正是唯一的组件依赖环）。 */
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
/* V3-2a DataBatch 编码回调（由 main 注入 msg_handler 的实现）。为 NULL 时
 * DataBatch 完全不启用，即使能力位为 1。 */
static data_batch_cb_t s_data_batch_cb = NULL;
/* D-06：编码器未注入（配置错误）—— 供启动门禁/诊断查询，避免"静默降级"。 */
static bool s_data_batch_missing = false;
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

/* Serialises "drain the driver event queue, then attach it to the set".
 * Without it an RX ISR could enqueue between the drain and xQueueAddToSet,
 * reintroducing exactly the non-empty rejection this fix exists to prevent. */
static portMUX_TYPE s_uart_set_mux = portMUX_INITIALIZER_UNLOCKED;

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

/* 2026-10-04 现场事故（S3 30EDA0A9A808，约 50 次/小时复位）：
 *   assert failed: prvNotifyQueueSetContainer queue.c:3362
 *   (pxQueueSetContainer->uxMessagesWaiting < pxQueueSetContainer->uxLength)
 * 复位回溯落在 schedule_v2_channel -> xQueueSend -> prvNotifyQueueSetContainer。
 *
 * 根因：下面这段"先直接读成员队列"的快速路径，在队列已经加入 queue set
 * 时依然会执行。FreeRTOS 用一个内部队列记录"已入队、但尚未被
 * xQueueSelectFromSet 取走的句柄数"：
 *   - 向成员队列 send       -> prvNotifyQueueSetContainer 让 set 计数 +1
 *   - xQueueSelectFromSet   取走一个句柄 -> set 计数 -1
 *   - xQueueReceive(成员队列) 完全不动 set 计数
 * 于是每走一次快速路径直接取走一条命令，set 里就永久多留一个句柄；
 * 计数只增不减，涨到 uxLength(24) 后，下一帧 send 直接命中断言 -> abort 重启。
 *
 * 修法：只要存在 set，就只走 set 路径。set 路径每次"弹 1 个句柄 + 读 1 条
 * 命令"，两边严格配平；公平策略仍由 bus_queue_choose 在弹出后重新评估，
 * 控制命令突发优先的语义不变。 */
if (!set) {
 bool control_ready = control && uxQueueMessagesWaiting(control) > 0;
 bool sample_ready = sample && uxQueueMessagesWaiting(sample) > 0;
 bus_queue_decision_t decision = bus_queue_choose(control_ready, sample_ready,
                                                  control_burst);
 if (decision == BUS_QUEUE_DECISION_CONTROL &&
  xQueueReceive(control, cmd, 0) == pdTRUE) return true;
 if (decision == BUS_QUEUE_DECISION_SAMPLE &&
  xQueueReceive(sample, cmd, 0) == pdTRUE) return true;
 if (control && xQueueReceive(control, cmd, pdMS_TO_TICKS(CMD_QUEUE_WAIT_MS)) == pdTRUE) {
  if (*control_burst < BUS_CONTROL_BURST_MAX) (*control_burst)++;
  return true;
 }
 return sample && xQueueReceive(sample, cmd, pdMS_TO_TICKS(CMD_QUEUE_WAIT_MS)) == pdTRUE;
}

QueueSetMemberHandle_t member = xQueueSelectFromSet(set, pdMS_TO_TICKS(CMD_QUEUE_WAIT_MS));
if (!member) return false;
(void)member;
/* 弹出后重新评估：另一个生产者可能已经改变了谁该赢。
 * 上面恰好弹出了 1 个句柄，所以这里必须恰好消费 1 条命令，
 * 否则 set 计数会向反方向漂移（句柄被消耗、而队列里没有命令可读）。 */
bool control_ready = control && uxQueueMessagesWaiting(control) > 0;
bool sample_ready = sample && uxQueueMessagesWaiting(sample) > 0;
bus_queue_decision_t decision = bus_queue_choose(control_ready, sample_ready,
                                                 control_burst);
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

/* ------------------------------------------------------------------ *
 *  V3-2a DataBatch(0x20) 聚合（契约 §3）
 *
 *  为什么把聚合与"编码进帧"分成两步：
 *    report_tx 主循环拿到第一个 telemetry desc 后，可能还想并入队列里
 *    紧随其后的 1..3 个同源非关键样本。但契约 §2.2 要求"缓冲剩余不足时
 *    降 n，不得截断"，而"够不够"只有在算出确切帧长之后才知道。因此：
 *      1) data_batch_collect() 只做**非破坏性**的窥视（xQueuePeek），
 *         把"若按当前候选集发包会占多少字节"报给调用方；
 *      2) 调用方挑出第一个放得下的 n，再真正 xQueueReceive 取出这 n 个
 *         并编码。
 *    这样"放回队列"的代价为零（peek 不移动 head），不丢样本，也不会有
 *    "取出来又放回去"的次序扰动。
 *
 *  为什么不存在临时数组里：DESC_BATCH_MAX(4) 个 report_desc_t 只有约
 *  4x40=160 B 栈，属于 report_tx 4096 B 预算内的可接受开销（同函数内
 *  handler 链的 2416 B 栈帧才是大头，且那部分本函数不进入）。
 * ------------------------------------------------------------------ */

/* 从 telemetry 队列取出 desc 引用的 payload。与 report_task 主循环里的
 * 同一套选择逻辑，避免"两处漂移"。 */
static const uint8_t *report_payload_of(const report_desc_t *desc)
{
 return desc->emergency ? s_critical_emergency_payload[desc->block_index]
                        : (desc->critical ? s_critical_payload[desc->block_index]
                                          : s_telemetry_payload[desc->block_index]);
}

/* 判断 desc 能否并入以 base 为起点的同一批（契约 §2.1.1 的路由元数据
 * 同源要求 + §3 的 critical==false 要求）。 */
static bool data_batch_compatible(const report_desc_t *base, const report_desc_t *cand)
{
 if (!base || !cand) return false;
 /* 关键样本（error_code != 0 或 request_id != 0）永不入批：契约 §2.1.6。
  * 队列层面本不该出现（关键样本走 s_report_critical_q），这里是第二道锁。 */
 if (cand->critical || cand->emergency) return false;
 if (base->critical || base->emergency) return false;
 if (cand->channel_id != base->channel_id) return false;
 if (cand->edge_device_id != base->edge_device_id) return false;
 if (cand->command_template_id != base->command_template_id) return false;
 if (cand->command_index != base->command_index) return false;
 /* 契约 §2.1.4 要求 delta 非递减；时间戳倒退的样本不能并入同一批。 */
 if (cand->timestamp_us < base->timestamp_us) return false;
 /* 契约 §2.1.5：空 raw_data 拒绝。0 长样本留给 0x03 路径，不入批。 */
 if (cand->len == 0) return false;
 return true;
}

/* D-18 修复（2026-10-06）：此处原先【复刻】了编码器的字节算术
 * （db_varint_size / db_field_varint_size / db_field_bytes_size /
 *   data_batch_frame_size，约 50 行），注释自陈"与 data_batch_codec.c 的
 * data_batch_encoded_size() 同式"，靠一句"两侧若漂移测试会红"约束。
 *
 * 为什么要删掉复刻：
 *   1. 帧布局若有两处定义，漂移时的症状是【静默】的 —— 尺寸算小了会写出
 *      超长帧或提前降 n，算大了会白白少聚合；两者都不会让编译失败；
 *   2. 复刻【没有换来任何解耦】：bus_worker 本就 REQUIRES msg_handler
 *      （其 CMakeLists 注释明确写着就是为了 data_batch_encode()），
 *      宿主测试也早已链接 data_batch_codec.c ⇒ 只是多了一份会漂移的副本；
 *   3. 这正是 P4（语义只有一处定义）。
 *
 * 现在唯一来源是 data_batch_encoded_size()：bus_worker 只做
 * report_desc_t -> data_batch_sample_t 的**字段搬运**，不含任何布局知识。
 * 字段号/宽度/省略规则一旦改动，两侧不可能再各自漂移 —— 因为只剩一侧。 */
static size_t data_batch_frame_size(uint32_t channel_id, uint64_t base_ts,
                                    uint32_t first_seq,
                                    const report_desc_t *cands, size_t n,
                                    uint32_t edge, uint32_t tmpl, uint8_t cmd_idx)
{
    if (cands == NULL || n == 0 || n > DATA_BATCH_MAX_SAMPLES) return 0;

    data_batch_sample_t samples[DATA_BATCH_MAX_SAMPLES];
    for (size_t i = 0; i < n; i++) {
        /* 只搬字段，不做算术 —— 布局知识全部留在编码器里。 */
        samples[i].delta_us = cands[i].timestamp_us - base_ts;
        samples[i].raw_data = report_payload_of(&cands[i]);
        samples[i].raw_len  = cands[i].len;
    }
    return data_batch_encoded_size(channel_id, base_ts, first_seq,
                                   samples, n, edge, tmpl, cmd_idx);
}

/* 时间戳是否落在聚合窗口内（契约 §3：ts - start_us < WINDOW_MS）。 */
static bool data_batch_in_window(uint64_t ts, uint64_t start_us)
{
 return ts >= start_us && (ts - start_us) < (uint64_t)DATA_BATCH_WINDOW_MS * 1000ULL;
}

/* 把已选中的 n 个样本交给注入的 DataBatch 编码回调（msg_handler 在它的
 * 1400 B 编码预算内完成编码与发布）。返回 false 表示编码失败 —— 按契约
 * 不得截断，调用方据此退回逐样本 0x03。
 *
 * 本函数刻意**不**在 bus_worker 里放编码缓冲：见 bus_worker.h 中
 * data_batch_cb_t 的注释（组件门禁 1024 B vs 一帧需要 1400 B）。 */
static bool data_batch_publish(const report_desc_t *descs, size_t n)
{
 /* 未注入编码器 = 配置错误（【不是】"这一批装不下"）——
  * 两者都返回 false，但前者在 start() 时已经 ERROR 过，这里不再重复刷屏。 */
 if (!s_data_batch_cb) return false;
 if (!descs || n < 2 || n > DATA_BATCH_MAX_SAMPLES) return false;

 const uint8_t *raw[DATA_BATCH_MAX_SAMPLES];
 size_t raw_lens[DATA_BATCH_MAX_SAMPLES];
 uint64_t timestamps[DATA_BATCH_MAX_SAMPLES];
 for (size_t i = 0; i < n; i++) {
  timestamps[i] = descs[i].timestamp_us;
  raw[i] = report_payload_of(&descs[i]);
  raw_lens[i] = descs[i].len;
 }
 bool ok = s_data_batch_cb(descs[0].channel_id, descs[0].sequence,
                           timestamps, raw, raw_lens, n,
                           descs[0].edge_device_id,
                           descs[0].command_template_id,
                           descs[0].command_index);
 if (!ok) {
  ESP_LOGW(TAG_RX, "DataBatch encode failed for n=%u; falling back to 0x03",
           (unsigned)n);
 }
 return ok;
}

/* 尝试把 desc（来自 member 队列）与其后续同源非关键样本聚合成一帧
 * DataBatch(0x20)。返回 true 表示 desc 已被本函数消费（调用方不得再按
 * 0x03 路径上报它）；false 表示走了现状路径。
 *
 * 抽成独立函数而不是内联在 report_task 里，是为了让宿主测试能**不经
 * FreeRTOS 任务**直接驱动这段逻辑（report_tx 的循环体依赖真实任务调度）。
 * 这也让"能力位为 0 时逐字节一致"这条红线有一个可执行的断言点。 */
static bool report_try_data_batch(QueueSetMemberHandle_t member, report_desc_t *desc)
{
 /* 兼容性红线：能力位为 0 时本函数立即返回，report_tx 的代码路径与
  * V3-2a 之前**逐字节一致**（只发 0x03）。所有窥视/编码动作都在
  * CAP_DATA_BATCH_V1 为真之后才可能发生。
  *
  * 只对 telemetry 队列聚合：关键/紧急样本走各自队列，既有告警路径不受
  * 影响（契约 §2.1.6）。 */
 if ((hello_get_server_caps() & CAP_DATA_BATCH_V1) == 0) return false;
 if (member != s_report_telemetry_q || !desc) return false;
 if (desc->critical || desc->emergency || desc->len == 0) return false;

 report_desc_t cands[DATA_BATCH_MAX_SAMPLES];
 cands[0] = *desc;
 size_t n = 1;
 /* 非阻塞地窥视后续 desc，只并入同源、非关键、窗口内的样本。
  * xQueuePeek 不移动队列 head —— "不满足条件即放回队列"因此是零代价的，
  * 不丢样本、也不扰动次序。 */
 while (n < DATA_BATCH_MAX_SAMPLES) {
  report_desc_t peek;
  if (xQueuePeek(s_report_telemetry_q, &peek, 0) != pdTRUE) break;
  if (!data_batch_compatible(desc, &peek)) break;
  if (!data_batch_in_window(peek.timestamp_us, desc->timestamp_us)) break;
  /* 契约 §2.2：先算"若并入这个样本"的确切帧长，超出编码缓冲就停在这里
   * （降 n），绝不截断。 */
  report_desc_t probe[DATA_BATCH_MAX_SAMPLES];
  for (size_t i = 0; i < n; i++) probe[i] = cands[i];
  probe[n] = peek;
  size_t projected = data_batch_frame_size(desc->channel_id, desc->timestamp_us,
                                           desc->sequence, probe, n + 1,
                                           desc->edge_device_id,
                                           desc->command_template_id,
                                           desc->command_index);
  if (projected > DATA_BATCH_BUF_SIZE) break;
  /* report_tx 是本队列唯一消费者，peek 到的元素必然仍是同一个，可以安全
   * 地取出。 */
  if (xQueueReceive(s_report_telemetry_q, &peek, 0) != pdTRUE) break;
  cands[n++] = peek;
 }

 if (n < 2) return false;

 if (data_batch_publish(cands, n)) {
  /* 编码完成后才归还遥测池：编码期间这些 block 必须保持有效。 */
  for (size_t i = 0; i < n; i++) report_free_block(false, cands[i].block_index);
  return true;
 }

 /* 编码失败：把已取出的样本**放回队列头**，绝不丢样本。逆序回填以保持
  * 原顺序；刚刚取走过 n 个，容量必然够，失败分支只是防御。放回后返回
  * true —— desc 仍是"已被本函数接管"，由这里负责归位，调用方不得重复
  * 归还。 */
 for (size_t i = n; i-- > 0; ) {
  if (xQueueSendToFront(s_report_telemetry_q, &cands[i], 0) != pdTRUE) {
   report_free_block(false, cands[i].block_index);
   report_stats_note_drop();
  }
 }
 return true;
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
  QueueSetMemberHandle_t member = NULL;
  {
   member = s_report_ready_set
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

  /* ---- V3-2a DataBatch(0x20) 聚合（契约 §3） ---- */
  if (report_try_data_batch(member, &desc)) {
   esp_task_wdt_reset();
   continue;
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
   report_stats_note_drop();
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
   report_stats_note_drop();
   return;
  }
  emergency = report_alloc_emergency_block(&index);
  if (!emergency) {
   /* This is only possible when the reserved normal and emergency slots are
    * both occupied.  Count it explicitly; transport remains off this task. */
   ESP_LOGE(TAG_RX, "critical report pools exhausted");
   report_stats_note_drop();
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
  report_stats_note_drop();
  return;
 }
 UBaseType_t queued = uxQueueMessagesWaiting(s_report_critical_q) +
                      uxQueueMessagesWaiting(s_report_critical_emergency_q) +
                      uxQueueMessagesWaiting(s_report_telemetry_q);
 /* D-14：高水位由中立组件维护（CAS 逻辑一并搬过去）。 */
 report_stats_note_queue_depth((uint32_t)queued);
}

static void report_path_init(void)
{
 if (s_report_path_started) return;
#if COLLECTOR_MEM_PSRAM_ENABLED
 /* Prefer PSRAM, fall back to internal RAM.  Fail closed: a NULL pool would
  * turn the first sample into a load from address 0, so the whole report path
  * (and therefore bus_worker_start) refuses to come up instead. */
 if (s_telemetry_payload == NULL) {
  s_telemetry_payload = collector_mem_alloc_pref_psram(
      (size_t)REPORT_TELEMETRY_BLOCKS * REPORT_PAYLOAD_BLOCK_SIZE);
 }
 if (s_streams == NULL) {
  s_streams = collector_mem_alloc_pref_psram(
      (size_t)SCHED_MAX_CHANNELS * sizeof(stream_rx_t));
  if (s_streams) memset(s_streams, 0, (size_t)SCHED_MAX_CHANNELS * sizeof(stream_rx_t));
 }
 if (!worker_buffers_ready()) {
  ESP_LOGE(TAG_RX, "report/stream buffer allocation failed; report path not started");
  collector_mem_free(s_telemetry_payload); s_telemetry_payload = NULL;
  collector_mem_free(s_streams); s_streams = NULL;
  return;
 }
#endif
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
 report_stats_reset();
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
#if COLLECTOR_MEM_PSRAM_ENABLED
 /* Free only after the queues and the report task are gone, so no descriptor
  * can still reference a block.  Restart re-allocates in report_path_init(). */
 collector_mem_free(s_telemetry_payload); s_telemetry_payload = NULL;
 collector_mem_free(s_streams); s_streams = NULL;
#endif
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
  /* STREAM buses: UART and USB both deliver uart_event_t notifications, so both
   * join the same queue set and share rx_task's single wait path.  USB is the
   * native C6 USB endpoint (no baud, no pins); it is NOT a UART controller. */
  if (!rt->bus_ctx[i].initialized ||
      (rt->bus_ctx[i].bus_type != BUS_TYPE_UART &&
       rt->bus_ctx[i].bus_type != BUS_TYPE_USB))
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
  if (already_added) continue;

  /* FreeRTOS xQueueAddToSet REFUSES a member whose queue is not empty
   * (FreeRTOS-Kernel/queue.c: "Cannot add a queue/semaphore to a queue set
   * if there are already items in the queue/semaphore" -> pdFAIL).
   *
   * This is not theoretical: a UART driver starts delivering RX events from
   * the instant uart_driver_install() registers its ISR, so any byte arriving
   * between driver install and this rebuild leaves the event queue non-empty
   * and makes the attach FAIL.  The failure is SELF-LOCKING: a queue that
   * never enters the set is never selected by rx_task, so it is never drained,
   * so it stays non-empty and every later rebuild fails too.  The old code
   * only logged, and the queue was never recorded in s_uart_event_members[],
   * so destroy_uart_event_set() could not even reset it.  Field impact
   * (2026-09-22): a real rain gauge stayed unreadable for 7 hours with
   * RX_TASK events=0, while the health path reported the channel as OK.
   *
   * Draining first satisfies the kernel precondition and breaks the lock.
   * No payload is lost: these entries are "there is data" notifications;
   * rx_append_from_event() keeps reading the driver ring via bus_dma_read()
   * until it is empty, so the bytes are picked up on the next wake-up. */
  taskENTER_CRITICAL(&s_uart_set_mux);
  uart_event_t stale;
  while (xQueueReceive(event_queue, &stale, 0) == pdTRUE) { /* drain */ }
  BaseType_t added = xQueueAddToSet(event_queue, set);
  taskEXIT_CRITICAL(&s_uart_set_mux);

  if (added != pdPASS) {
   /* Not survivable: without membership the channel is invisible to rx_task.
    * Record it anyway so the next destroy_uart_event_set() can reset it, and
    * keep the failure loud instead of leaving a silent, permanently
    * unreadable channel behind. */
   ESP_LOGE(TAG_RX, "failed to add UART channel slot %d event queue (attach refused; channel will be unreadable)", i);
  }
  if (s_uart_event_member_count < SCHED_MAX_CHANNELS) {
   s_uart_event_members[s_uart_event_member_count++] = event_queue;
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

void bus_worker_set_data_batch_cb(data_batch_cb_t cb)
{
 s_data_batch_cb = cb;
}
/* D-06：编码器是否已注入。
 * 存在的理由：未注入时 DataBatch 会静默退回逐样本 0x03 ——
 * 该状态此前【无法从外部观测】（弱符号让它看起来总是"已注入"）。
 * 启动门禁与诊断据此判断，而不是靠人猜。 */
bool bus_worker_data_batch_encoder_present(void)
{
    return s_data_batch_cb != NULL;
}


/* ------------------------------------------------------------------ */
/*  P2-2: Turnaround delay helpers                                    */
/* ------------------------------------------------------------------ */

static uint32_t compute_turnaround_us(const bus_dma_ctx_t *ctx)
{
 /* The USB transport is a full-duplex virtual serial port with no wire-level
  * turnaround, and cfg is a union -- reading cfg.uart here would alias the USB
  * member and produce a garbage delay. */
 if (ctx->bus_type == BUS_TYPE_USB) return 0;
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
  /* 快速路径（2026-10-06，task-5）：已知期望长度（read_size>0）时，收到
   * 至少 expected 字节即可判定本步响应完整，立即返回，不再等 10ms 静默。
   *
   * 语义依据（不是猜测）：
   *   - 批处理步骤的 read_size 来自后端 ControlAction 的 SingleStep.ReadSize，
   *     由厂商协议的真实帧长编译而来（如 JBD read_basic_info 的 Modbus 0x03
   *     响应恰好 60 B，见 jiabaida_control.go:485 / 二进制帧协议.md §WriteCmd
   *     field 4 的"期望长度"定义）。
   *   - 判据是 >=（不是 ==）：收满才返回，绝不截断。多余字节（同一 RX 里
   *     紧跟在后的下一事务/额外上行）**不并入本步响应**：本步结束时
   *     s_batch_rx[ch].len 会被重置（execute_uart_batch 每步 TX 前重置），
   *     而 execute_uart_batch 进入时已用 bus_dma_read 循环 drain 过驱动环，
   *     因此它们既不会进本步载荷，也不会被当作下一步的响应。
   *   - expected == 0（变长协议，如 GB3024 一行 ASCII）没有可用长度，
   *     保持 10ms 静默兜底，行为不变。
   *
   * 旧顺序是"先满足 10ms 静默、再检查 expected"，对 115200 的 60 B 帧要
   * 多付 10ms 尾等待，理论上限只有约 63 Hz。 */
  if (expected > 0 && len >= expected) {
   /* 只交付声明的长度，多余字节不并入本步响应：它们属于下一个事务
    * （或从机的额外上行），本步结束后的 len 重置会丢弃它们，因此既不会
    * 污染下一步，也不会被当成本步的载荷上报给服务端 verifier。 */
   memcpy(out, state->data, (size_t)expected);
   *out_len = (size_t)expected;
   return true;
  }
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
  /* Request/response buses: UART and the native USB endpoint both need the
   * "one outstanding response" rule, because neither carries a request id. */
  if ((cmd.bus_type == BUS_TYPE_UART || cmd.bus_type == BUS_TYPE_USB) &&
      (cmd.type == CMD_SAMPLE || cmd.read_size > 0)) {
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
   bool ok = (cmd.bus_type == BUS_TYPE_UART || cmd.bus_type == BUS_TYPE_USB) &&
             execute_uart_batch(ch_idx, ctx, &cmd, raw, &raw_len, &code);
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
    /* Do NOT report success here (2026-09-22 field fix).
     *
     * bus_dma_write() only proves the bytes reached the driver.  Reporting
     * success at TX-accept time made scheduler_notify_channel_success()
     * DECREMENT scheduler error_count even when the slave never answered --
     * and handler_data.c skips the whole EdgeDeviceHealth sub-frame when
     * error_count == 0.  Net effect: a channel that could not read its sensor
     * at all (every RX timing out) was reported to the server as HEALTHY, and
     * the resulting outage stayed invisible for 7 hours.
     *
     * Success/failure for a sampled channel is now owned solely by rx_task,
     * which is the only place that can observe whether a response actually
     * arrived: handle_uart_event() reports success on a complete response and
     * expire_uart_state() reports the error on timeout.  One owner also keeps
     * the health counters from being updated twice for a single transaction. */
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

/* s_streams storage is declared next to s_telemetry_payload above (the PSRAM
 * branch must be visible to report_path_init before this point). */
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
  if (!rt->bus_ctx[i].initialized ||
      (rt->bus_ctx[i].bus_type != BUS_TYPE_UART &&
       rt->bus_ctx[i].bus_type != BUS_TYPE_USB))
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
  /* Short-read rule.  For the legacy write-response path a reply shorter than
   * read_size means the slave answered incompletely, and that must stay an
   * error.  ChannelCmdV2 is different (2026-10-03): read_size there is the
   * node's RX *window* (<=256, the size of the control-final buffer), not a
   * promise about the vendor frame length.  A single-step V2 command always
   * reaches the node as CMD_WRITE (bus_manager.c:1064), and V2 carries no
   * per-action length: an ASCII sensor reply is one line of ~15-40 bytes
   * whose exact length is not knowable to the node.  Erroring on it made
   * every variable-length read permanently fail with 0x03 (a Techfine
   * "HBAT\r" -> "(...)\r" read could never satisfy a fixed length), while
   * declaring read_size == 0 instead completed the control with NO data at
   * all (:985).  So for V2 the line-idle gap is the authoritative frame
   * boundary: deliver what arrived and let the server, which owns the
   * per-action verifier (VerifyControlAction), decide whether it is valid.
   * A genuinely silent sensor still fails, via expire_uart_state() timeout. */
  if (pcmd.read_size > 0 && s->len < pcmd.read_size && !pcmd.channel_cmd_v2) {
   (void)xQueueReceive(rt->pending_queues[idx], &pcmd, 0);
   /* Short read = the sensor answered incompletely: a command error.
    * 2026-09-30: move the counter the server reads (sched_command_t), not
    * only the channel-level backoff counter. */
   (void)scheduler_notify_command_outcome(rt->bus_ch[idx], pcmd.edge_device_id,
                                          pcmd.command_template_id,
                                         pcmd.command_index, false);
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
  /* A complete response for a pending descriptor is the ONE place a sampled
   * channel is genuinely healthy, so this is where success is reported
   * (2026-09-22 field fix).  CMD_SAMPLE no longer reports success at
   * TX-accept time.  It is also the only place that clears the per-command
   * error streak, so the reported counter can fall back to 0 on recovery. */
  (void)scheduler_notify_command_outcome(rt->bus_ch[idx], pcmd.edge_device_id,
                                          pcmd.command_template_id,
                                         pcmd.command_index, true);
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

/* 行状态事件（BREAK/PARITY/FRAME）的日志限流。
 *
 * 2026-10-04 实机观察：某台 S3 上 slot1 的 UART_BREAK（type=1）以约 50 次/秒
 * 持续触发，55 秒内打出 2826 行同样的 WARN。后果不是"日志多"这么轻：
 *
 *   1) 串口控制台被灌满，出现大量交错/截断的半行（实测 70 秒内 32 行损坏），
 *      而**真正要看的错误日志恰好被挤掉** —— 我因此连续多轮拿不到配置事务的
 *      拒绝原因，只能靠猜，浪费了大量时间；
 *   2) 该 WARN 会被 log_capture 收进日志环（它是 WARN，manifest level>=1 即捕获），
 *      一旦日志上传链路打开，就是每秒约 50 条 MQTT 帧的洪水。
 *
 * 只对**重复的同一类行状态事件**限流：窗口内前几条立即打印，之后打一条聚合摘要
 * （含被抑制条数），再安静到下一个周期。
 *
 * 重要：限的是**日志**，不是**计量**。s_rx_error_count 等计数器照常累加，
 * 任何依赖计数的健康上报都不受影响。
 */
#define RX_LINESTATUS_LOG_PERIOD_MS 5000
#define RX_LINESTATUS_LOG_BURST 3

typedef struct {
    int64_t window_start_us;
    uint32_t in_window;
    uint32_t suppressed;
} rx_linestatus_log_t;

static rx_linestatus_log_t s_rx_linestatus_log[SCHED_MAX_CHANNELS];

/* 返回 true 表示调用方应当打印；false 表示已被限流（并已在本窗口补过摘要）。 */
static bool rx_linestatus_log_should_emit(int idx)
{
    if (idx < 0 || idx >= SCHED_MAX_CHANNELS) return true;
    rx_linestatus_log_t *st = &s_rx_linestatus_log[idx];
    const int64_t now = esp_timer_get_time();
    const int64_t period = (int64_t)RX_LINESTATUS_LOG_PERIOD_MS * 1000;
    if (st->window_start_us == 0 || now - st->window_start_us >= period) {
        st->window_start_us = now;
        st->in_window = 0;
        st->suppressed = 0;
    }
    st->in_window++;
    if (st->in_window <= RX_LINESTATUS_LOG_BURST) return true;
    st->suppressed++;
    /* 抑制开始后补一条摘要，之后安静到下一个窗口。 */
    return st->suppressed == 1;
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
     (void)bus_dma_flush_input(&rt->bus_ctx[idx]);
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
   (void)bus_dma_flush_input(&rt->bus_ctx[idx]);
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
  (void)bus_dma_flush_input(&rt->bus_ctx[idx]);
  /* Do not print cfg.uart.port here: cfg is a union and this branch also runs for
   * USB contexts, where it would read the USB member as a port number. */
  ESP_LOGW(TAG_RX, "slot%d type=%d event=%d size=%" PRIu32 "; input reset",
   idx, (int)rt->bus_ctx[idx].bus_type, (int)event->type,
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
  /* 计数照常（上一行），只有日志受限流 —— 见 rx_linestatus_log_should_emit 的说明。 */
  if (rx_linestatus_log_should_emit(idx)) {
   rx_linestatus_log_t *st = &s_rx_linestatus_log[idx];
   if (st->suppressed > 0) {
    ESP_LOGW(TAG_RX, "slot%d type=%d event=%d size=%" PRIu32
     "; retained buffered input (%" PRIu32 " more like this suppressed in the last %d ms)",
     idx, (int)rt->bus_ctx[idx].bus_type, (int)event->type,
     (uint32_t)event->size, st->suppressed, RX_LINESTATUS_LOG_PERIOD_MS);
   } else {
    ESP_LOGW(TAG_RX, "slot%d type=%d event=%d size=%" PRIu32 "; retained buffered input",
     idx, (int)rt->bus_ctx[idx].bus_type, (int)event->type,
     (uint32_t)event->size);
   }
  }
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
      (rt->bus_ctx[i].bus_type != BUS_TYPE_UART &&
       rt->bus_ctx[i].bus_type != BUS_TYPE_USB)) continue;

  if (complete_idle_response(rt, i, now_us)) completions++;

  pending_cmd_t pcmd;
  if (xQueuePeek(rt->pending_queues[i], &pcmd, 0) != pdTRUE ||
      pcmd.rx_timeout_ms == 0 || pcmd.tx_timestamp == 0) continue;
  int64_t elapsed_ms = (now_us - pcmd.tx_timestamp) / 1000;
  if (elapsed_ms <= (int64_t)pcmd.rx_timeout_ms) continue;
  if (xQueueReceive(rt->pending_queues[i], &pcmd, 0) != pdTRUE) continue;
  completions++;
  s_rx_timeout_count[i]++;
  /* This loop covers BUS_TYPE_UART *and* BUS_TYPE_USB (the native USB CDC
   * endpoint speaks the same request/response protocol), so neither the
   * message nor the channel identity may be hardcoded.  Report slot/type
   * like the other rx_task diagnostics in this file (cf. the FIFO-overflow
   * and line-status branches, which print slot%d type=%d). */
  ESP_LOGW(TAG_RX, "RX timeout slot%d type=%d reqID=%lu (%lldms)",
   i, (int)rt->bus_ctx[i].bus_type,
   (unsigned long)pcmd.request_id, (long long)elapsed_ms);
  /* Channel health is owned by rx_task (2026-09-22 field fix): a request that
   * got no answer is a channel error, and only this path can observe that.
   * Previously CMD_SAMPLE reported success at TX-accept time, so a completely
   * unresponsive sensor DECREMENTED error_count and handler_data.c then
   * omitted the EdgeDeviceHealth sub-frame entirely -- the server saw a
   * healthy channel throughout a 7-hour outage.
   *
   * 2026-09-30 (defect 2, statistics-ownership migration): the counter the
   * server actually reads is sched_command_t.error_count, not the channel
   * counter that scheduler_notify_channel_error() bumps.  The channel counter
   * only fed the legacy v1 backoff, so a silent sensor still reported
   * error_code=0.  Address the exact command that was outstanding -- the
   * peeked descriptor carries the (template_id, command_index) pair the
   * scheduler used to build it.  The helper also updates the channel-level
   * counter, so the legacy v1 backoff keeps working unchanged. */
  (void)scheduler_notify_command_outcome(rt->bus_ch[i], pcmd.edge_device_id,
                                          pcmd.command_template_id,
                                         pcmd.command_index, false);
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
  if (!rt->bus_ctx[i].initialized ||
      (rt->bus_ctx[i].bus_type != BUS_TYPE_UART &&
       rt->bus_ctx[i].bus_type != BUS_TYPE_USB))
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

/* D-06 修复（2026-10-06）：此处原有一个 __attribute__((weak)) 的
 * msg_handler_send_data_batch 默认实现 + "若未注入则用它"。
 *
 * 为什么删掉它：
 *   1. 它【违反本组件自己的契约】—— bus_worker.h 里 data_batch_cb_t 的注释
 *      明确写着"Not injected => DataBatch stays disabled"，
 *      弱符号却偷偷给了一个默认实现；
 *   2. 生效与否取决于【链接顺序】（msg_handler 的强符号是否被拉进镜像），
 *      而宿主测试走的是另一套符号表 ⇒ **弱定义一旦生效，DataBatch 静默
 *      退化为逐样本 0x03，且【没有测试会红】**；
 *   3. 它与仓库里既有的做法【不一致】—— main.c 已经对另外两个回调
 *      (bus_worker_set_callbacks / bus_worker_set_channel_cmd_v2_final_cb)
 *      做显式注入，只有这一个用弱符号走捷径。
 *
 * 现在：依赖方向保持单向（main -> bus_worker / main -> msg_handler），
 * 由 main.c 显式注入；未注入时【可见地】失败，而不是静默降级。 */

void bus_worker_start(bus_runtime_t *rt)
{
 ensure_suspend_events();
 s_runtime = rt;
 /* 默认注入 DataBatch(0x20) 编码器（msg_handler 的强定义；main/ 若另行
  * 注入会覆盖此默认值）。放在 start() 而不是 report_path_init()：编码器
  * 只被 report_tx 使用，而 report_tx 由 report_path_init() 创建。 */
 /* D-06：不再兜底 —— 未注入就是配置错误，必须【看得见】。
  * 静默降级（悄悄退回逐样本 0x03）正是弱符号时代最难查的故障形态。 */
 if (s_data_batch_cb == NULL) {
  ESP_LOGE(TAG_RX, "DataBatch encoder NOT injected: aggregation disabled "
                   "(capability bit may be advertised but 0x20 will never be sent). "
                   "main.c must call bus_worker_set_data_batch_cb().");
  s_data_batch_missing = true;
 }
 report_path_init();
 if (!s_report_path_started) {
  /* report_path_init() failed (or was never able to build its pool).  Starting
   * the RX/cmd tasks here would let rx_task write into a NULL stream buffer. */
  ESP_LOGE("BUS_WORKER", "report path unavailable; not starting bus workers");
  return;
 }
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

/* D-14：这两个 getter 保留为【兼容转发】—— 仍可能有调用方（如诊断打印）。
 * 真正的数据源已在中立组件 report_stats；msg_handler 不再经由这里读取，
 * 因此不再产生依赖环。 */
uint32_t bus_worker_get_report_drop_count(void)
{
 return report_stats_get_drop_count();
}

uint32_t bus_worker_get_report_queue_high_water(void)
{
 return report_stats_get_queue_high_water();
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
