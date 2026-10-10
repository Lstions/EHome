/**
 * @file cmd_queue.h
 * @brief Shared command queue types for bus_dma scheduler and worker tasks
 *
 * Used by scheduler.c (producer), cmd_task (consumer), and rx_task (UART listener).
 */

#ifndef CMD_QUEUE_H
#define CMD_QUEUE_H

#include <stdint.h>
#include <stddef.h>
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "driver/uart.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === Queue sizing === */
#define CMD_QUEUE_DEPTH  16
#define CMD_TX_MAX       128
#define CMD_PLAN_MAX     512
#define CMD_BATCH_MAX_STEPS 8
#define CONTROL_SLOT_NONE UINT8_MAX

/* === Command types === */
typedef enum {
    CMD_WRITE = 0,    /* WriteCommand from backend (TX only if read_size==0, TX+RX if read_size>0) */
    CMD_SAMPLE = 1,   /* Periodic sample from scheduler (TX + optional delay) */
} cmd_type_t;

/* === Bus command descriptor ===
 *
 * UART channels:  cmd_task does TX; rx_task handles RX independently.
 *   CMD_WRITE + read_size==0: TX only, WriteRsp immediate.
 *   CMD_WRITE + read_size>0: TX + turnaround delay + enqueue pending_cmd_t
 *     for rx_task correlation → DataReport with request_id.
 *   CMD_SAMPLE: TX + turnaround + enqueue pending_cmd_t (request_id=0).
 *
 * SPI/I2C channels: cmd_task does full transact (TX+RX atomic).
 *   CMD_WRITE + read_size==0: transact, WriteRsp only (no DataReport).
 *   CMD_WRITE + read_size>0: transact with read_cap=read_size,
 *     WriteRsp + DataReport with request_id.
 *   CMD_SAMPLE: transact, DataReport (request_id=0).
 *
 * delay_ms: for CMD_SAMPLE, milliseconds to wait between TX and letting
 *           rx_task pick up the response.  0 = no delay.
 *           Not used for CMD_WRITE (turnaround auto-calculated from baud).
 * read_size: for CMD_WRITE, expected RX byte count.  0 = TX only.
 *
 * ── Two element types sharing one prefix (P2, 2026-10-10) ──────────────
 *
 * Sample queues and control queues have very different payload needs:
 *   · A sample command is built by the scheduler and never carries a batch
 *     plan (bus_manager rejects plans on anything but UART/USB, and the
 *     scheduler has no ChannelCmdV2 path at all).
 *   · A control command may carry a bounded batch plan (CMD_PLAN_MAX bytes).
 *
 * Historically both used one 700-byte bus_cmd_t, so 64 sample slots wasted
 * 520 bytes each on a plan buffer they can never fill.  The queues are
 * separate, so they may carry different element sizes: FreeRTOS copies
 * item_size bytes per queue, and xQueueAddToSet does not inspect item_size
 * (item_size is a per-queue property; the set only tracks which queue an
 * item came from).
 *
 * sample_cmd_t is therefore an EXACT PREFIX of bus_cmd_t, and the static
 * assertion below enforces that structurally.  Two consequences matter:
 *
 *  1. `type` must live at the END OF THE PREFIX, not at the end of the
 *     struct.  A consumer that receives into a full bus_cmd_t from a sample
 *     queue only has the first sizeof(sample_cmd_t) bytes written; anything
 *     beyond that keeps the previous iteration's contents.  With `type` at
 *     offset 696 it would be read as a stale value (bus_worker dispatches on
 *     cmd.type in 11 places), so it must be inside the prefix.
 *  2. The plan members stay AFTER the prefix, so a sample consumer can never
 *     observe them: reading plan_data/plan_len/plan_step_count is gated by
 *     channel_cmd_v2 (false for every sample command) and those offsets are
 *     outside the received bytes anyway.
 *
 * The assertion is written as offsetof(plan_data) == offsetof(type)+size
 * because that form holds on both the xtensa target (176+4 == 180) and the
 * x86_64 host (184+4 == 188) even though sizeof(sample_cmd_t) differs
 * (180 vs 192) due to different tail padding.
 */
#define BUS_CMD_COMMON \
    uint32_t   request_id;                    /* Correlates WriteResponse/DataReport */ \
    uint32_t   channel_id; \
    uint8_t    bus_type;                      /* 1=UART, 2=I2C, 3=SPI; legacy 4=GPIO rejected */ \
    uint8_t    tx_data[CMD_TX_MAX]; \
    size_t     tx_len; \
    uint32_t   delay_ms;                      /* TX→RX delay (sample only) */ \
    uint32_t   read_size;                     /* v2.5: expected RX bytes for CMD_WRITE (0=TX only) */ \
    uint32_t   rx_timeout_ms;                  /* bounded UART response timeout for CMD_WRITE */ \
    uint32_t   edge_device_id;                /* v2.3: edge device for DataReport routing */ \
    uint32_t command_template_id;            /* ConfigTemplate.ID for command-aware parsing */ \
    uint8_t command_index;                 /* v2.3: command index within edge_device */ \
    uart_port_t uart_port;                    /* UART port (UART_NUM_0/1/2), per-port dispatch */ \
    bool channel_cmd_v2;                      /* true only for control-slot commands */ \
    uint8_t control_slot;                     /* ChannelCmdV2 sidecar slot, or CONTROL_SLOT_NONE */ \
    cmd_type_t type;                          /* MUST stay last in the common prefix */

/* Slim element for the per-bus SAMPLE queues (scheduler → cmd_task). */
typedef struct {
    BUS_CMD_COMMON
} sample_cmd_t;

/* Element for the CONTROL queues (WriteCommand / ChannelCmdV2 → cmd_task).
 * Also used as the receive buffer for both queues, and as the scratch type in
 * producers that build a command before choosing a queue.
 *
 * ⭐ 方案 D（2026-10-10）：**队列元素不再内联 plan buffer**。
 *
 * 曾经这里有一个 uint8_t plan_data[CMD_PLAN_MAX]（512 B）+ plan_len +
 * plan_step_count，把元素撑到 700 B —— 违反本项目的「一条命令 ≤ 256 B」接口
 * 约束，且占掉 3 个 control 队列共 16.8 KiB 堆。
 *
 * 为什么以前要内联：一次用户操作可能是「写-等-读-再写-再读」的**多步序列**
 * （如 SN-3001 雨量计清零：read_before → write_clear → readback）。
 * 序列必须被**一个命令原子地**交给 worker，否则中途会被别的命令插入。
 *
 * 为什么现在可以去掉：原子性的真正来源是 **worker 在一次函数调用里跑完所有
 * 步骤**（bus_worker.c 的 execute_uart_batch），而不是「plan 存在队列元素里」。
 * 而 plan 数据**本来就有一份**在 msg_handler 的 control 槽位里
 * （channel_cmd_v2_t.plan_data，见 msg_handler_internal.h），且：
 *   · 槽位在 QUEUED 期间**独占**（handler_channel_cmd_v2.c 的 reserve_slot
 *     只复用 FREE / 最旧 FINAL 的槽）；
 *   · QUEUED **跨越整个执行期**（worker 完成回调后才置 FINAL）。
 * ⇒ 槽位里的 plan 在命令执行期间天然有效，队列元素不需要第二份副本。
 *
 * worker 通过 msg_handler 的访问器借用 plan：
 *     channel_cmd_v2_borrow_plan(cmd->control_slot, &plan, &len, &steps)
 * 该访问器**校验槽位状态必须是 QUEUED**，不满足就打印错误并拒绝执行 ——
 * 把「槽位不可提前释放」这条隐式不变量变成**显式运行时强制**，而不是把一根
 * 裸指针放进队列元素里靠顺序偶然成立。
 *
 * 因此 bus_cmd_t 与 sample_cmd_t **结构完全相同**（都是 180 B target /
 * 192 B host）。保留两个类型名是为了表达语义差别（control vs sample），
 * 并让下面的断言把「两者可互换接收」这件事钉死。 */
typedef struct {
    BUS_CMD_COMMON
} bus_cmd_t;

/* The two element types must be structurally identical: a consumer receives
 * into a bus_cmd_t from either queue, and producers build one before choosing
 * a queue.  If someone re-adds a member to bus_cmd_t only, this fails. */
_Static_assert(offsetof(bus_cmd_t, type) == offsetof(sample_cmd_t, type),
               "type must sit at the same offset in both element types");
_Static_assert(sizeof(bus_cmd_t) == sizeof(sample_cmd_t),
               "bus_cmd_t and sample_cmd_t must have identical size");

#ifdef __cplusplus
}
#endif

#endif /* CMD_QUEUE_H */
