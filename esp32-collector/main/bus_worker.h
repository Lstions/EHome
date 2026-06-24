/**
 * @file bus_worker.h
 * @brief Unified bus transaction worker — consumes cmd_queue, drives bus_dma_transact.
 *
 * P1-7: Protocol-agnostic frame delimiter types and stream RX state.
 */

#ifndef BUS_WORKER_H
#define BUS_WORKER_H

#include "app_state.h"
#include <stdint.h>
#include <stddef.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

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
void bus_worker_start(app_state_t *state);

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
