/**
 * @file bus_worker.h
 * @brief Unified bus transaction workers — consume per-controller queues and
 *        drive bus_dma transactions through the active dynamic lease.
 *
 * P1-8: ESP32 is a transparent UART byte pipe. DMA bytes are linearized into
 * per-channel stream buffers and reported to backend as-is. Frame detection
 * and protocol parsing are the backend's responsibility.
 * P2-8: Decoupled from app_state_t — uses bus_runtime_t for dependency injection.
 */

#ifndef BUS_WORKER_H
#define BUS_WORKER_H

#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "bus_dma.h"
#include "cmd_queue.h"
#include "scheduler.h"

#ifdef __cplusplus
extern "C" {
#endif

/* ==================================================================
 * P2-8: Decoupled runtime context — replaces direct app_state_t dependency.
 *
 * Populated by main/ at startup, passed to bus_worker/bus_manager functions.
 * This allows bus_worker/bus_manager to live in components/ without
 * depending on the monolithic app_state_t.
 * ================================================================== */

/* Forward declaration — dma_pool_t is owned by dma_pool component.
 * Full definition in dma_pool.h; bus_runtime_t only needs the pointer. */
struct dma_pool_t;

/* P2-8: Forward declaration for bus_find_ctx_fn typedef */
typedef struct bus_runtime_s bus_runtime_t;

/* P2-8: Context lookup callback — breaks circular dependency with bus_manager.
 * Set by app_state_init_bus_runtime() to point to bus_manager_find_ctx(). */
typedef bus_dma_ctx_t *(*bus_find_ctx_fn)(bus_runtime_t *rt, uint32_t channel_id);

typedef struct {
 bool valid;
 uint32_t channel_id;
 uint8_t bus_type;
 int32_t controller_id;
} bus_lease_hint_t;

struct bus_runtime_s {
 /* Bus DMA contexts (SCHED_MAX_CHANNELS slots) */
 bus_dma_ctx_t *bus_ctx; /* s->bus_ctx */
 uint32_t *bus_ch; /* s->bus_ch */
 char *bus_hw_id; /* s->bus_hw_id (flat array, [SCHED_MAX_CHANNELS][16]) */

 /* DMA resource pool */
 struct dma_pool_t *dma_pool; /* s->dma_pool */

 /* Per-channel pending queues */
 QueueHandle_t *pending_queues; /* s->pending_queues */

 /* Per-controller sample queues (scheduler-owned). */
 QueueHandle_t uart0_cmd_queue;
 QueueHandle_t uart1_cmd_queue;
 QueueHandle_t uart2_cmd_queue;
 QueueHandle_t spi_cmd_queue;
 QueueHandle_t i2c_cmd_queue;

 /* Per-controller control queues (WriteCommand/ChannelCmdV2-owned).  They
  * are deliberately separate from sample queues so telemetry backpressure
  * cannot consume control capacity. */
 QueueHandle_t uart0_control_queue;
 QueueHandle_t uart1_control_queue;
 QueueHandle_t uart2_control_queue;
 QueueHandle_t spi_control_queue;
 QueueHandle_t i2c_control_queue;

 /* P2-8: Context lookup callback — breaks circular dependency with bus_manager.
  * Set by app_state_init_bus_runtime() to point to bus_manager_find_ctx(). */
 bus_find_ctx_fn find_ctx;

 /* Snapshot of the concrete leases held before a manifest transaction.  It
  * survives bus cleanup so a failed apply can restore a custom-pin Channel
  * to its original UART/SPI/I2C controller instead of silently reassigning
  * the first free controller. */
 bus_lease_hint_t lease_hints[SCHED_MAX_CHANNELS];
 bool lease_hints_valid;
};

/* ==================================================================
 * Callback types (moved from app_state.h for decoupling)
 * ================================================================== */

typedef void (*write_rsp_cb_t)(uint32_t rid, bool ok, uint32_t code, const char *msg);
typedef void (*data_rpt_cb_t)(uint32_t ch, uint64_t ts, uint32_t seq,
 const uint8_t *data, size_t len, uint32_t code, uint32_t rid,
 uint32_t edge_device_id, uint32_t command_template_id, uint8_t command_index);
typedef void (*channel_cmd_v2_final_cb_t)(uint8_t slot, bool success, uint32_t error_code,
                                          const uint8_t *raw_response, size_t raw_len);

/* V3-2a: 非关键遥测批量上报回调。
 *
 * bus_worker 只负责**决定**把哪几个样本合成一批（契约 §3 的聚合策略），
 * 帧的编码（契约 §2）由 msg_handler 在它自己的 1400 B 编码预算内完成。
 *
 * 为什么编码不放 bus_worker：bus_worker 的组件级门禁是
 * -Wframe-larger-than=1024，而一帧 DataBatch 需要 1400 B 的编码缓冲。若在
 * bus_worker 里放这个缓冲，report_tx 的栈帧会变成 ~1472 B，且它会**叠加**
 * 在 send_data_report() 已有的 2416 B 栈帧之上（只有批路径才调用 publish，
 * 但同一任务栈仍要容纳两者），实测峰值会顶到 4096 B 任务栈的上限。契约
 * §2.2 所说的"report_tx 的 1,400 B 编码缓冲"指的正是 msg_handler 上报路径
 * 里那个既有的 buf[1400]；批量编码复用它，既不新增静态 RAM（契约要求），
 * 也不把两个大缓冲叠在同一个任务栈上。
 *
 * 参数用并行数组而非结构体：让这个回调契约不依赖任何额外头文件，从而不
 * 把 msg_handler 的头链拖进 bus_worker 的每个宿主测试。
 *   timestamps_us[i] —— 第 i 个样本的绝对微秒时间戳；[0] 即 base。
 *   raw_data[i] / raw_lens[i] —— 与 0x03 payload 同义的样本字节。
 * 返回 false 表示编码失败（调用方据此退回逐样本 0x03，绝不丢样本）。 */
typedef bool (*data_batch_cb_t)(uint32_t channel_id,
                                uint32_t first_sequence,
                                const uint64_t *timestamps_us,
                                const uint8_t *const *raw_data,
                                const size_t *raw_lens,
                                size_t count,
                                uint32_t edge_device_id,
                                uint32_t command_template_id,
                                uint8_t command_index);


/* ==================================================================
 * Pending command descriptor (moved from app_state.h for decoupling)
 * ================================================================== */

#define PENDING_QUEUE_DEPTH 10 /* per-channel pending queue depth */
#define PENDING_CMD_DATA_MAX 8 /* Store first 8 bytes of command for Modbus exception matching */

typedef struct {
 uint32_t edge_device_id;
 uint32_t command_template_id;
 uint8_t command_index;
 uint32_t request_id; /* 0 for CMD_SAMPLE (no WriteResponse) */
 uint32_t read_size; /* Expected RX length for CMD_WRITE+readSize; 0 = CMD_SAMPLE (matches any length) */
 /* P1-1: Store original command bytes for Modbus exception matching
  * cmd_data[0] = slave address, cmd_data[1] = function code */
 uint8_t cmd_data[PENDING_CMD_DATA_MAX];
 uint8_t cmd_data_len; /* Number of valid bytes in cmd_data */
 /* P1-6: RX timeout tracking */
 int64_t tx_timestamp; /* Time when TX completed (esp_timer_get_time()), 0 = no tracking */
 uint32_t rx_timeout_ms; /* RX timeout in ms (0 = no timeout tracking), default 1000 */
 bool channel_cmd_v2;
 uint8_t control_slot; /* ChannelCmdV2 slot, or CONTROL_SLOT_NONE */
} pending_cmd_t;

/* ==================================================================
 * P3: Per-channel stream RX buffer. Boundary decisions are automatic
 * (read_size, fixed report block, then idle completion); no user-selectable
 * receive mode is exposed.
 * ================================================================== */
/* WS-C: 1024 -> 512.  The UART hardware FIFO is 128 B and the driver ring is
 * drained in 256 B reads (bus_dma_read()), so the stream buffer only has to
 * hold one automatic boundary at a time: read_size <= 256, fixed block = 512
 * (BUS_RX_FIXED_BLOCK_SIZE).  Anything larger was slack, and it is multiplied
 * by SCHED_MAX_CHANNELS (5 on S3).  Boundary semantics are unchanged: the
 * overflow path still flushes and then re-assesses against this capacity. */
#define STREAM_RX_BUF_SIZE 512

typedef struct {
 uint8_t buffer[STREAM_RX_BUF_SIZE];
 size_t len;
 /* A UART FIFO/buffer overflow invalidates the current response.  Keep the
  * marker until the pending descriptor is completed so overflow cannot be
  * reported as a successful short or idle frame. */
 bool overflow;
} stream_rx_t;

/* ==================================================================
 * Public API
 * ================================================================== */

/** Inject msg_handler callbacks (call before bus_worker_start) */
void bus_worker_set_callbacks(write_rsp_cb_t wr_cb, data_rpt_cb_t dr_cb);
void bus_worker_set_channel_cmd_v2_final_cb(channel_cmd_v2_final_cb_t cb);
/** Inject the V3-2a DataBatch encoder callback (call before bus_worker_start).
 *  Not injected => DataBatch stays disabled regardless of the capability bit. */
void bus_worker_set_data_batch_cb(data_batch_cb_t cb);

/** Start rx_task + per-bus cmd_tasks (called once at boot) */
void bus_worker_start(bus_runtime_t *rt);

/**
 * Suspend all worker tasks — call before bus_manager_cleanup_all.
 *
 * Returns false when every worker did not acknowledge within the bounded
 * lifecycle deadline.  A failed suspend leaves the runtime intact and must
 * abort the configuration transaction rather than deleting live queues.
 */
bool bus_worker_suspend(void);

/** Resume all worker tasks — call after bus_manager_setup_from_manifest */
void bus_worker_resume(void);
void bus_worker_discard_queued(bus_runtime_t *rt);

/** Delete all worker tasks — full teardown */
void bus_worker_stop(void);

/* P1-6: Get RX timeout count (for debug/status queries) */
uint32_t bus_worker_get_rx_timeout_count(int channel);

/* Asynchronous report path counters.  A non-zero telemetry drop count means
 * the network side could not keep up; critical reports use a reserved pool
 * and are never evicted by telemetry. */
uint32_t bus_worker_get_report_drop_count(void);
uint32_t bus_worker_get_report_queue_high_water(void);

/* Minimum remaining stack watermark across active bus worker tasks, in
 * FreeRTOS stack words.  Zero means workers are not running yet. */
uint32_t bus_worker_get_min_stack_watermark(void);

#ifdef __cplusplus
}
#endif

#endif /* BUS_WORKER_H */
