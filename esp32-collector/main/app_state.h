/**
 * @file app_state.h
 * @brief Centralized application state — replaces all main.c globals.
 *
 * Single struct passed by pointer to all modules.  Zero global variables
 * outside of the singleton in app_state.c.
 *
 * P2-8: Callback types and pending_cmd_t moved to bus_worker.h for
 * decoupling.  This header includes bus_worker.h for those types.
 */

#ifndef APP_STATE_H
#define APP_STATE_H

#include <stdint.h>
#include <stdbool.h>
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "bus_dma.h"
#include "bus_worker.h"    /* P2-8: bus_runtime_t, write_rsp_cb_t, data_rpt_cb_t, pending_cmd_t */
#include "config_mgr.h"
#include "scheduler.h"
#include "transport.h"
#include "cmd_queue.h"

#ifdef __cplusplus
extern "C" {
#endif

#define NODE_ID_MAX_LEN  32
#define BOOT_ID_MAX_LEN  17

typedef struct {
    /* ---- Identity (auto-generated from MAC) ---- */
    char        node_id[NODE_ID_MAX_LEN];
    char        boot_id[BOOT_ID_MAX_LEN];

    /* ---- Bus DMA pool ---- */
    bus_dma_ctx_t bus_ctx[SCHED_MAX_CHANNELS];
    uint32_t      bus_ch[SCHED_MAX_CHANNELS];
    char          bus_hw_id[SCHED_MAX_CHANNELS][16]; /* hw_id per slot (for cleanup) */

    /* ---- Runtime state ---- */
    uint32_t    uptime_sec;
    bool        config_received;
    bool        ota_need_confirm;
    bool        hello_task_running;

    /* ---- Config-manifest application lock ---- */
    SemaphoreHandle_t config_mutex;

    /* ---- Transports ---- */
    transport_t *tcp_transport;

    /* ---- Per-controller command queues ----
     * Sample queues are scheduler-owned; control queues are reserved for
     * WriteCommand/ChannelCmdV2.  Workers consume both with the same
     * UART0/UART1/SPI/I2C implementation. */
    QueueHandle_t uart0_cmd_queue;
    QueueHandle_t uart1_cmd_queue;
    QueueHandle_t uart2_cmd_queue;
    QueueHandle_t spi_cmd_queue;
    QueueHandle_t i2c_cmd_queue;
    QueueHandle_t uart0_control_queue;
    QueueHandle_t uart1_control_queue;
    QueueHandle_t uart2_control_queue;
    QueueHandle_t spi_control_queue;
    QueueHandle_t i2c_control_queue;

    /* ---- Pending command state (per channel, FreeRTOS Queue) ---- */
    /* Each UART channel has a queue of pending_cmd_t entries so that
     * cmd_task (TX) and rx_task (RX) never race on a single shared slot.
     * Queue depth=PENDING_QUEUE_DEPTH (10), see app_state.c. */
    QueueHandle_t pending_queues[SCHED_MAX_CHANNELS];

    /* ---- DMA resource pool (DIP: injected, not global) ---- */
    struct dma_pool_t *dma_pool;

    /* ---- P2-8: Bus runtime for dependency injection ---- */
    bus_runtime_t bus_runtime;
} app_state_t;

/* ---- Lifecycle ---- */
app_state_t *app_state_init(void);

/* ---- Getters ---- */
app_state_t *app_state_get(void);
bool app_state_is_config_received(void);
void app_state_set_config_received(bool v);
uint32_t app_state_get_uptime_sec(void);

/**
 * 本次上电以来的运行秒数，取自单调时钟 esp_timer_get_time()。
 *
 * status_task 必须用它给 app_state_t.uptime_sec 赋值，**不能**每轮自增：
 * 上报周期是 1 秒（2026-10-03 由 5s 收紧，见 main.c 的 STATUS_REPORT_PERIOD_MS），
 * 自增会让"秒"字段每 1 个周期才 +1；一旦周期再变，上报值就成了
 * 「真实运行秒数 / 周期秒数」，前端「固件在线时长」会成比例失真。
 * 取本函数则与周期无关。
 */
uint32_t app_state_uptime_sec_now(void);

/* ---- Config-manifest lock helpers ---- */
void app_state_lock_config(void);
void app_state_unlock_config(void);

/* ---- P2-8: Bus runtime initialization ---- */
void app_state_init_bus_runtime(app_state_t *s, bus_runtime_t *rt);

/* ---- Version ---- */
const char *get_firmware_version(void);
const char *get_model_name(void);

#ifdef __cplusplus
}
#endif

#endif /* APP_STATE_H */
