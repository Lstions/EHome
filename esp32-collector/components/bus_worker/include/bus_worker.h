/**
 * @file bus_worker.h
 * @brief Unified bus transaction worker — consumes cmd_queue, drives bus_dma_transact.
 *
 * P1-7: Protocol-agnostic frame delimiter types and stream RX state.
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
 *  P2-8: Decoupled runtime context — replaces direct app_state_t dependency.
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

struct bus_runtime_s {
    /* Bus DMA contexts (SCHED_MAX_CHANNELS slots) */
    bus_dma_ctx_t  *bus_ctx;           /* s->bus_ctx */
    uint32_t       *bus_ch;            /* s->bus_ch */
    char           *bus_hw_id;         /* s->bus_hw_id (flat array, [SCHED_MAX_CHANNELS][16]) */

    /* DMA resource pool */
    struct dma_pool_t *dma_pool;       /* s->dma_pool */

    /* Per-channel pending queues */
    QueueHandle_t  *pending_queues;    /* s->pending_queues */

    /* Per-bus command queues */
    QueueHandle_t   uart0_cmd_queue;
    QueueHandle_t   uart1_cmd_queue;
    QueueHandle_t   spi_cmd_queue;
    QueueHandle_t   i2c_cmd_queue;

    /* P2-8: Context lookup callback — breaks circular dependency with bus_manager.
     * Set by app_state_init_bus_runtime() to point to bus_manager_find_ctx(). */
    bus_find_ctx_fn find_ctx;
};

/* ==================================================================
 *  Callback types (moved from app_state.h for decoupling)
 * ================================================================== */

typedef void (*write_rsp_cb_t)(uint32_t rid, bool ok, uint32_t code, const char *msg);
typedef void (*data_rpt_cb_t)(uint32_t ch, uint64_t ts, uint32_t seq,
                               const uint8_t *data, size_t len, uint32_t code, uint32_t rid,
                               uint32_t edge_device_id, uint8_t command_index);

/* ==================================================================
 *  Pending command descriptor (moved from app_state.h for decoupling)
 * ================================================================== */

#define PENDING_QUEUE_DEPTH 10  /* per-channel pending queue depth */
#define PENDING_CMD_DATA_MAX 8  /* Store first 8 bytes of command for Modbus exception matching */

typedef struct {
    uint32_t edge_device_id;
    uint8_t  command_index;
    uint32_t request_id;       /* 0 for CMD_SAMPLE (no WriteResponse) */
    uint32_t read_size;        /* Expected RX length for CMD_WRITE+readSize; 0 = CMD_SAMPLE (matches any length) */
    /* P1-1: Store original command bytes for Modbus exception matching
     * cmd_data[0] = slave address, cmd_data[1] = function code */
    uint8_t  cmd_data[PENDING_CMD_DATA_MAX];
    uint8_t  cmd_data_len;     /* Number of valid bytes in cmd_data */
    /* P1-6: RX timeout tracking */
    int64_t  tx_timestamp;     /* Time when TX completed (esp_timer_get_time()), 0 = no tracking */
    uint32_t rx_timeout_ms;    /* RX timeout in ms (0 = no timeout tracking), default 1000 */
} pending_cmd_t;

/* ==================================================================
 *  P1-7: Frame delimiter types for protocol-agnostic RX processing
 * ================================================================== */

typedef enum {
    FRAME_DELIM_TIMEOUT,       /* 基于时间间隔 (Modbus RTU) */
    FRAME_DELIM_DELIMITER,     /* 基于分隔符 (GB3024 ASCII) */
    FRAME_DELIM_START_STOP,    /* 起止标记+长度字段 (嘉佰达 BMS) */
    FRAME_DELIM_FIXED,         /* 固定长度 */
} frame_delim_type_t;

typedef struct {
    frame_delim_type_t type;
    union {
        /* FRAME_DELIM_TIMEOUT: 帧间静默超时 */
        struct {
            uint64_t timeout_us;         /* 超时微秒 */
        } timeout;

        /* FRAME_DELIM_DELIMITER: 分隔符结尾 */
        struct {
            uint8_t  bytes[4];           /* 分隔符字节序列 (最多4字节) */
            uint8_t  len;                /* 分隔符长度 */
        } delimiter;

        /* FRAME_DELIM_START_STOP: 起止标记+长度字段 */
        struct {
            uint8_t  start_byte;         /* 帧头 (如 0xDD) */
            uint8_t  stop_byte;          /* 帧尾 (如 0x77) */
            int      length_field_offset;/* 长度字段偏移 */
            int      length_field_size;  /* 长度字段大小 (1或2) */
            bool     length_includes_header; /* 长度是否含帧头 */
            int      header_size;        /* 帧头总字节数 */
        } start_stop;

        /* FRAME_DELIM_FIXED: 固定长度 */
        struct {
            size_t   length;             /* 固定帧长度 */
        } fixed_len;
    };
} frame_delim_config_t;

/* P1-7: Per-channel stream RX state */
#define STREAM_RX_BUF_SIZE 512

typedef struct {
    uint8_t  buffer[STREAM_RX_BUF_SIZE];
    size_t   len;
    uint64_t last_rx_time;
    bool     frame_started;     /* start_stop 模式: 已检测到帧头 */
    frame_delim_config_t delim_cfg;
} stream_rx_t;

/* ==================================================================
 *  Public API
 * ================================================================== */

/** Inject msg_handler callbacks (call before bus_worker_start) */
void bus_worker_set_callbacks(write_rsp_cb_t wr_cb, data_rpt_cb_t dr_cb);

/** Start rx_task + per-bus cmd_tasks (called once at boot) */
void bus_worker_start(bus_runtime_t *rt);

/** Suspend all worker tasks — call before bus_manager_cleanup_all */
void bus_worker_suspend(void);

/** Resume all worker tasks — call after bus_manager_setup_from_manifest */
void bus_worker_resume(void);

/** Delete all worker tasks — full teardown */
void bus_worker_stop(void);

#ifdef __cplusplus
}
#endif

#endif /* BUS_WORKER_H */
