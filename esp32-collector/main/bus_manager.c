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
#include "esp_log.h"
#include <string.h>
#include <inttypes.h>

#define TAG "BUS_MGR"

/* ---- Forward declare for msg_handler callback ---- */
extern void msg_handler_send_write_rsp(uint32_t request_id, bool success,
                                       uint32_t error_code, const char *error_msg);

/* ==== Bus type → DMA flags byte offset ==== */

static bool get_dma_enabled(uint8_t bus_type, const uint8_t *cfg, size_t cfglen)
{
    static const uint8_t k_flags_offset[] = {
        [BUS_TYPE_UART - 1] = 6,
        [BUS_TYPE_I2C  - 1] = 7,
        [BUS_TYPE_SPI  - 1] = 6,
    };
    if (bus_type < 1 || bus_type > 3) return true;

    uint8_t off = k_flags_offset[bus_type - 1];
    if (cfg && cfglen > off)
        return (cfg[off] & 0x01) != 0;
    return true; /* default: DMA enabled */
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
    /* Already registered? */
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++)
        if (s->bus_ch[i] == ch_id && s->bus_ctx[i].initialized) return;

    /* Find free slot */
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s->bus_ch[i] == 0) {
            s->bus_ch[i] = ch_id;
            bool dma = get_dma_enabled(bus_type, config, config_len);
            esp_err_t err = bus_dma_init(&s->bus_ctx[i], bus_type, dma,
                                         config, config_len);
            if (err == ESP_OK) {
                ESP_LOGI(TAG, "ch=%" PRIu32 " type=%u dma=%d idx=%d",
                         ch_id, bus_type, dma, i);
            } else {
                ESP_LOGE(TAG, "ch=%" PRIu32 " init failed: %s",
                         ch_id, esp_err_to_name(err));
                s->bus_ch[i] = 0;
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
            bus_dma_deinit(&s->bus_ctx[i]);
            s->bus_ch[i] = 0;
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
