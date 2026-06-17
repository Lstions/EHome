/**
 * @file bus_manager.c
 * @brief Bus DMA context pool — register, find, cleanup channels.
 *
 * Owns the shared pool of bus_dma_ctx_t instances in app_state.
 * All bus channel lifecycle operations go through this module.
 */

#include "bus_manager.h"
#include "bus_dma.h"
#include "config_mgr.h"
#include "msg_handler.h"
#include "dma_pool.h"
#include "esp_log.h"
#include <string.h>
#include <inttypes.h>

#define TAG "BUS_MGR"

/* ---- Forward declare for msg_handler callback ---- */
extern void msg_handler_send_write_rsp(uint32_t request_id, bool success,
                                       uint32_t error_code, const char *error_msg);

/* ==== hw_id derivation (LoD: name reflects actual resource) ==== */

static void derive_hw_id(char *buf, size_t buflen, uint8_t bus_type, uint32_t ch_id)
{
    const char *bus_name = "UNKNOWN";
    switch (bus_type) {
        case BUS_TYPE_UART: bus_name = "UART"; break;
        case BUS_TYPE_SPI:  bus_name = "SPI";  break;
        case BUS_TYPE_I2C:  bus_name = "I2C";  break;
    }
    snprintf(buf, buflen, "%s_CH%" PRIu32, bus_name, ch_id);
}

/* ==== Look up bus_type from config manifest ==== */

static uint8_t find_bus_type(const config_manifest_t *m, uint32_t ch)
{
    if (!m) return 0;
    for (int i = 0; i < m->channel_count; i++)
        if (m->channels[i].id == ch) return m->channels[i].bus_type;
    return 0;
}

/* ==== Register one channel ==== */

static void reg_bus_channel(app_state_t *s, uint32_t ch_id,
                            uint8_t bus_type,
                            const uint8_t *config, size_t config_len)
{
    ESP_LOGI(TAG, "reg_bus_channel: ch=%" PRIu32 " type=%u cfg_len=%zu", 
             ch_id, bus_type, config_len);
    
    /* Already registered? */
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++)
        if (s->bus_ch[i] == ch_id && s->bus_ctx[i].initialized) return;

    /* Find free slot */
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s->bus_ch[i] == 0) {
            s->bus_ch[i] = ch_id;
            
            /* DMA allocation: check user preference first, then try pool */
            bool dma = false;
            char hw_id[16];
            derive_hw_id(hw_id, sizeof(hw_id), bus_type, ch_id);
            
            /* Respect user DMA preference from bus_config flags */
            bool user_wants_dma = bus_config_get_dma_enabled(bus_type, config, config_len);
            if (user_wants_dma && s->dma_pool) {
                uint32_t dma_id = 0;
                esp_err_t dma_err = dma_pool_allocate(s->dma_pool, bus_type,
                                                        hw_id, &dma_id);
                if (dma_err == ESP_OK) {
                    dma = true;
                    ESP_LOGI(TAG, "ch=%" PRIu32 " DMA allocated (id=%" PRIu32 ")",
                             ch_id, dma_id);
                } else {
                    ESP_LOGW(TAG, "ch=%" PRIu32 " DMA requested but unavailable, polled",
                             ch_id);
                }
            } else if (!user_wants_dma) {
                ESP_LOGI(TAG, "ch=%" PRIu32 " DMA disabled by user config", ch_id);
            }
            
            ESP_LOGI(TAG, "bus_dma_init: ch=%" PRIu32 " type=%u dma=%d idx=%d",
                     ch_id, bus_type, dma, i);
            esp_err_t err = bus_dma_init(&s->bus_ctx[i], bus_type, dma,
                                         config, config_len);
            if (err == ESP_OK) {
                /* Save hw_id for cleanup (avoid manifest dependency) */
                strncpy(s->bus_hw_id[i], hw_id, sizeof(s->bus_hw_id[i]) - 1);
                s->bus_hw_id[i][sizeof(s->bus_hw_id[i]) - 1] = '\0';
                ESP_LOGI(TAG, "ch=%" PRIu32 " type=%u dma=%d idx=%d SUCCESS",
                         ch_id, bus_type, dma, i);
            } else {
                ESP_LOGE(TAG, "ch=%" PRIu32 " init failed: %s",
                         ch_id, esp_err_to_name(err));
                s->bus_ch[i] = 0;
                /* Release DMA if init failed */
                if (dma) {
                    dma_pool_release_by_hw(s->dma_pool, hw_id);
                }
            }
            return;
        }
    }
    ESP_LOGE(TAG, "Bus slots full (max=%d)", SCHED_MAX_CHANNELS);
}

/* ==== Public API ==== */

void bus_manager_init(app_state_t *state)
{
    (void)state; /* pool already zeroed by app_state_init */
}

void bus_manager_cleanup_all(app_state_t *s)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s->bus_ctx[i].initialized) {
            /* Release DMA using saved hw_id (no manifest dependency) */
            if (s->dma_pool && s->bus_hw_id[i][0] != '\0') {
                dma_pool_release_by_hw(s->dma_pool, s->bus_hw_id[i]);
            }
            bus_dma_deinit(&s->bus_ctx[i]);
            s->bus_ch[i] = 0;
            s->bus_hw_id[i][0] = '\0';
        }
    }
}

void bus_manager_setup_from_manifest(app_state_t *s)
{
    const config_manifest_t *m = config_mgr_get_manifest();
    if (!m || !m->applied) return;
    for (int i = 0; i < m->channel_count; i++) {
        if (!m->channels[i].enabled) continue;
        reg_bus_channel(s, m->channels[i].id, m->channels[i].bus_type,
                        m->channels[i].bus_config, m->channels[i].bus_config_len);
    }
}

bus_dma_ctx_t *bus_manager_find_ctx(app_state_t *s, uint32_t channel_id)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++)
        if (s->bus_ch[i] == channel_id && s->bus_ctx[i].initialized)
            return &s->bus_ctx[i];
    return NULL;
}

/* ==== WriteCommand handler ==== */

void bus_manager_on_write_cmd(app_state_t *s, uint32_t rid, uint32_t ch,
                               const uint8_t *d, size_t l, uint32_t rs)
{
    const config_manifest_t *m = config_mgr_get_manifest();
    bus_cmd_t cmd = {
        .request_id = rid,
        .channel_id = ch,
        .bus_type   = find_bus_type(m, ch),
        .tx_len     = l < CMD_TX_MAX ? l : CMD_TX_MAX,
        .read_size  = rs,
        .timeout_ms = 50,
        .type       = CMD_WRITE,
    };
    if (l > 0 && d) memcpy(cmd.tx_data, d, cmd.tx_len);

    if (!xQueueSend(s->cmd_queue, &cmd, 0))
        msg_handler_send_write_rsp(rid, false, 0xFFFF, "queue full");
}
