/**
 * @file app_state.h
 * @brief Centralized application state — replaces all main.c globals.
 *
 * Single struct passed by pointer to all modules.  Zero global variables
 * outside of the singleton in app_state.c.
 */

#ifndef APP_STATE_H
#define APP_STATE_H

#include <stdint.h>
#include <stdbool.h>
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "bus_dma.h"
#include "config_mgr.h"
#include "scheduler.h"
#include "transport.h"
#include "cmd_queue.h"

/* Forward declaration — dma_pool_t is owned by dma_pool component.
 * Full definition in dma_pool.h; app_state only needs the pointer. */
struct dma_pool_t;

#ifdef __cplusplus
extern "C" {
#endif

#define NODE_ID_MAX_LEN  32

typedef struct {
    /* ---- Identity (auto-generated from MAC) ---- */
    char        node_id[NODE_ID_MAX_LEN];

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

    /* ---- Command queue ---- */
    QueueHandle_t cmd_queue;

    /* ---- DMA resource pool (DIP: injected, not global) ---- */
    struct dma_pool_t *dma_pool;

} app_state_t;

/* ---- Lifecycle ---- */
app_state_t *app_state_init(void);

/* ---- Getters ---- */
app_state_t *app_state_get(void);
bool app_state_is_config_received(void);
void app_state_set_config_received(bool v);
uint32_t app_state_get_uptime_sec(void);

/* ---- Config-manifest lock helpers ---- */
void app_state_lock_config(void);
void app_state_unlock_config(void);

/* ---- Version ---- */
const char *get_firmware_version(void);
const char *get_model_name(void);

#ifdef __cplusplus
}
#endif

#endif /* APP_STATE_H */
