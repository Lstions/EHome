/**
 * @file app_state.c
 * @brief Application state singleton — replaces main.c globals.
 *
 * node_id is auto-generated from the WiFi MAC address for true uniqueness
 * across devices.  Falls back to Kconfig CONFIG_COLLECTOR_NODE_ID if MAC
 * read fails.
 *
 * P2-8: bus_runtime_t initialization bridges app_state_t fields to the
 * decoupled bus_worker/bus_manager components.
 */

#include "app_state.h"
#include "bus_manager.h"  /* P2-8: bus_manager_find_ctx for find_ctx callback */
#include "dma_pool.h"
#include "hw_profile.h"
#include "esp_log.h"
#include "esp_mac.h"
#include "esp_random.h"
#include <string.h>
#include <inttypes.h>

#define TAG "APP_STATE"
/* Single source of truth: version comes from CMakeLists.txt PROJECT_VER,
 * injected via target_compile_definitions as EHOME_PROJECT_VER. */
#define FIRMWARE_VERSION EHOME_PROJECT_VER
#define MODEL_NAME       CONFIG_IDF_TARGET

/* ==== Singleton ==== */
static app_state_t s_app;
static dma_pool_t s_dma_pool;  /* Lives in app_state, not a global */

/* ---- node_id from MAC ---- */

static void generate_node_id(char *buf, size_t buflen)
{
    uint8_t mac[6];
    esp_err_t err = esp_efuse_mac_get_default(mac);
    if (err == ESP_OK) {
        /* Pure hex: 12 chars from 6-byte MAC */
        snprintf(buf, buflen, "%02X%02X%02X%02X%02X%02X",
                 mac[0], mac[1], mac[2], mac[3], mac[4], mac[5]);
        ESP_LOGI(TAG, "node_id from MAC: %s", buf);
        return;
    }
    /* Fallback to Kconfig */
    strlcpy(buf, CONFIG_COLLECTOR_NODE_ID, buflen);
    ESP_LOGW(TAG, "MAC read failed, using Kconfig node_id: %s", buf);
}

static void generate_boot_id(char *buf, size_t buflen)
{
    snprintf(buf, buflen, "%08" PRIX32 "%08" PRIX32,
             esp_random(), esp_random());
}

/* ---- P2-8: Bus runtime initialization ---- */

void app_state_init_bus_runtime(app_state_t *s, bus_runtime_t *rt)
{
    rt->bus_ctx         = s->bus_ctx;
    rt->bus_ch          = s->bus_ch;
    rt->bus_hw_id       = (char *)s->bus_hw_id;  /* flat 2D array cast */
    rt->dma_pool        = s->dma_pool;
    rt->pending_queues  = s->pending_queues;
    rt->uart0_cmd_queue = s->uart0_cmd_queue;
    rt->uart1_cmd_queue = s->uart1_cmd_queue;
    rt->spi_cmd_queue   = s->spi_cmd_queue;
    rt->i2c_cmd_queue   = s->i2c_cmd_queue;
    rt->find_ctx        = bus_manager_find_ctx;  /* P2-8: breaks circular dependency */
}

/* ---- Lifecycle ---- */

app_state_t *app_state_init(void)
{
    memset(&s_app, 0, sizeof(s_app));

    /* Unique node_id from hardware MAC */
    generate_node_id(s_app.node_id, sizeof(s_app.node_id));
    generate_boot_id(s_app.boot_id, sizeof(s_app.boot_id));
    hw_profile_set_boot_id(s_app.boot_id);

    /* Mutex for config-manifest application.
     * Using mutex instead of spinlock because we call blocking functions
     * (scheduler_stop with vTaskDelay, scheduler_start with xTaskCreate) */
    s_app.config_mutex = xSemaphoreCreateMutex();
    if (s_app.config_mutex == NULL) {
        ESP_LOGE(TAG, "Failed to create config mutex!");
    }

    /* Command queue (WriteCommand compat, transition period) */
    s_app.cmd_queue = xQueueCreate(CMD_QUEUE_DEPTH, sizeof(bus_cmd_t));

    /* Per-bus command queues (for cmd_task split) */
    s_app.uart0_cmd_queue = xQueueCreate(16, sizeof(bus_cmd_t));
    s_app.uart1_cmd_queue = xQueueCreate(16, sizeof(bus_cmd_t));
    s_app.spi_cmd_queue   = xQueueCreate(8, sizeof(bus_cmd_t));
    s_app.i2c_cmd_queue   = xQueueCreate(8, sizeof(bus_cmd_t));

    /* Zero the pool markers */
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        s_app.bus_ch[i] = 0;
    }

    /* Per-channel pending queues (depth=PENDING_QUEUE_DEPTH, replaces race-prone single-slot arrays) */
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        s_app.pending_queues[i] = xQueueCreate(PENDING_QUEUE_DEPTH, sizeof(pending_cmd_t));
        if (s_app.pending_queues[i] == NULL) {
            ESP_LOGE(TAG, "Failed to create pending queue for slot %d!", i);
        }
    }

    /* DMA pool init (from chip-specific hw_profile table) */
    dma_pool_init(&s_dma_pool, hw_dmas, HW_DMA_COUNT);
    s_app.dma_pool = &s_dma_pool;

    /* P2-8: Initialize bus_runtime_t from app_state fields */
    app_state_init_bus_runtime(&s_app, &s_app.bus_runtime);

    ESP_LOGI(TAG, "State initialized: node_id=%s boot_id=%s fw=%s model=%s",
             s_app.node_id, s_app.boot_id, FIRMWARE_VERSION, MODEL_NAME);
    return &s_app;
}

/* ---- Getters ---- */

app_state_t *app_state_get(void)
{
    return &s_app;
}

bool app_state_is_config_received(void)
{
    return s_app.config_received;
}

void app_state_set_config_received(bool v)
{
    s_app.config_received = v;
}

uint32_t app_state_get_uptime_sec(void)
{
    return s_app.uptime_sec;
}

/* ---- Config lock ---- */

void app_state_lock_config(void)
{
    if (s_app.config_mutex != NULL) {
        xSemaphoreTake(s_app.config_mutex, portMAX_DELAY);
    }
}

void app_state_unlock_config(void)
{
    if (s_app.config_mutex != NULL) {
        xSemaphoreGive(s_app.config_mutex);
    }
}

/* ---- Version ---- */

const char *get_firmware_version(void)
{
    return FIRMWARE_VERSION;
}

const char *get_model_name(void)
{
    return MODEL_NAME;
}
