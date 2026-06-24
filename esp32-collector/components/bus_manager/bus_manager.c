/**
 * @file bus_manager.c
 * @brief Bus DMA context pool — register, find, cleanup channels.
 *
 * Owns the shared pool of bus_dma_ctx_t instances in bus_runtime_t.
 * All bus channel lifecycle operations go through this module.
 *
 * WriteCommand handling: constructs a bus_cmd_t and posts it to the
 * command queue.  No timeout derivation — the ESP32 does not understand
 * timeouts; that is the backend's responsibility.
 *
 * P2-8: Decoupled from app_state_t — uses bus_runtime_t for dependency
 * injection.  All s->field accesses replaced with rt->field.
 */

#include "bus_manager.h"
#include "bus_dma.h"
#include "config_mgr.h"
#include "dma_pool.h"
#include "hw_tables.h"
#include "esp_log.h"
#include "driver/uart.h"
#include <string.h>
#include <inttypes.h>

#define TAG "BUS_MGR"

/* ---- Callback for write response (injected by main.c) ---- */
static write_rsp_cb_t s_write_rsp_cb = NULL;

void bus_manager_set_write_rsp_cb(write_rsp_cb_t cb)
{
    s_write_rsp_cb = cb;
}

/* ==== hw_id derivation: produces canonical name matching dma_pool config ====
 * Crawls the matching hw resource table by bus_type and bus_config pins
 * to get "UART0", "SPI2", etc. — the same identifier used in the "bind_to"
 * field by dma_pool_apply_config (e.g. "uart/UART0").
 *
 * Falls back to a unique "bus/UNKNOWN_XX_YY" form when lookup fails,
 * encoding bus_type and first two config bytes to avoid collision
 * in dma_pool_release_by_hw(). */

static void derive_hw_id(char *buf, size_t buflen, uint8_t bus_type,
                          const uint8_t *bus_config, size_t bus_config_len)
{
    const char *id = NULL;
    const char *bus_name = "unknown";

    switch (bus_type) {
    case BUS_TYPE_UART:
        bus_name = "uart";
        if (bus_config && bus_config_len >= 2)
            for (int i = 0; i < HW_UART_COUNT; i++)
                if (hw_uarts[i].default_tx_pin == bus_config[0] &&
                    hw_uarts[i].default_rx_pin == bus_config[1]) {
                    id = hw_uarts[i].id; break;
                }
        break;
    case BUS_TYPE_SPI:
        bus_name = "spi";
        /* SPI bus is identified by MOSI/MISO/SCLK (bus-level), not CS
         * (device-level).  Match on all three when available (len >= 9),
         * otherwise fall back to MOSI/MISO/SCLK defaults from hw table. */
        if (bus_config && bus_config_len >= 6) {
            int mosi = (bus_config_len >= 9) ? bus_config[6] : -1;
            int miso = (bus_config_len >= 9) ? bus_config[7] : -1;
            int sclk = (bus_config_len >= 9) ? bus_config[8] : -1;
            for (int i = 0; i < HW_SPI_COUNT; i++) {
                bool mosi_ok = (mosi < 0) || (hw_spis[i].default_mosi == mosi);
                bool miso_ok = (miso < 0) || (hw_spis[i].default_miso == miso);
                bool sclk_ok = (sclk < 0) || (hw_spis[i].default_sclk == sclk);
                if (mosi_ok && miso_ok && sclk_ok) {
                    id = hw_spis[i].id; break;
                }
            }
        }
        break;
    case BUS_TYPE_I2C:
        bus_name = "i2c";
        if (bus_config && bus_config_len >= 2)
            for (int i = 0; i < HW_I2C_COUNT; i++)
                if (hw_i2cs[i].default_sda == bus_config[0] &&
                    hw_i2cs[i].default_scl == bus_config[1]) {
                    id = hw_i2cs[i].id; break;
                }
        break;
    }

    if (id) {
        snprintf(buf, buflen, "%s/%s", bus_name, id);
    } else {
        /* Unique fallback: encode bus_type + first 2 config bytes */
        uint8_t b0 = (bus_config && bus_config_len >= 1) ? bus_config[0] : 0;
        uint8_t b1 = (bus_config && bus_config_len >= 2) ? bus_config[1] : 0;
        snprintf(buf, buflen, "%s/UNKNOWN_%02X_%02X", bus_name, b0, b1);
    }
}

/* ==== Look up bus_type from config manifest ====
 * P2-9: This is now a fallback — prefer reading bus_type from bus_dma_ctx_t
 * which is set during bus_dma_init(). Used only when ctx is not yet available. */

static uint8_t find_bus_type(const config_manifest_t *m, uint32_t ch)
{
    if (!m) return 0;
    for (int i = 0; i < m->channel_count; i++)
        if (m->channels[i].id == ch) return m->channels[i].bus_type;
    return 0;
}

/* ==== Derive uart_port from channel bus_config via hw_tables ====
 * P3-7: Replaced static derive_uart_port_for_channel with shared hw_derive_uart_port */

static uart_port_t derive_uart_port_for_channel(const config_manifest_t *m, uint32_t ch)
{
    if (!m) return UART_NUM_0;
    for (int i = 0; i < m->channel_count; i++) {
        if (m->channels[i].id == ch && m->channels[i].bus_type == BUS_TYPE_UART) {
            const uint8_t *bc = m->channels[i].bus_config;
            size_t bclen = m->channels[i].bus_config_len;
            if (bc && bclen >= 2) {
                return hw_derive_uart_port(bc[0], bc[1], UART_NUM_0);
            }
        }
    }
    return UART_NUM_0;  /* default */
}

/* ==== Helper: get bus_hw_id[i] from flat array ==== */
static char *get_bus_hw_id(bus_runtime_t *rt, int idx)
{
    return rt->bus_hw_id + idx * 16;
}

/* ==== Register one channel ==== */

static void reg_bus_channel(bus_runtime_t *rt, uint32_t ch_id,
                            uint8_t bus_type,
                            const uint8_t *config, size_t config_len)
{
    ESP_LOGI(TAG, "reg_bus_channel: ch=%" PRIu32 " type=%u cfg_len=%zu", 
             ch_id, bus_type, config_len);
    
    /* Already registered? */
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++)
        if (rt->bus_ch[i] == ch_id && rt->bus_ctx[i].initialized) return;

    /* Find free slot */
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (rt->bus_ch[i] == 0) {
            rt->bus_ch[i] = ch_id;
            
            /* DMA allocation: check user preference first, then try pool */
            bool dma = false;
            char hw_id[16];
            derive_hw_id(hw_id, sizeof(hw_id), bus_type, config, config_len);
            
            /* Respect user DMA preference from bus_config flags */
            bool user_wants_dma = bus_config_get_dma_enabled(bus_type, config, config_len);
            if (user_wants_dma && rt->dma_pool) {
                uint32_t dma_id = 0;
                esp_err_t dma_err = dma_pool_allocate(rt->dma_pool, bus_type,
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
            esp_err_t err = bus_dma_init(&rt->bus_ctx[i], bus_type, dma,
                                         config, config_len);
            if (err == ESP_OK) {
                /* Save hw_id for cleanup (avoid manifest dependency) */
                char *hw_id_slot = get_bus_hw_id(rt, i);
                strncpy(hw_id_slot, hw_id, 15);
                hw_id_slot[15] = '\0';
                ESP_LOGI(TAG, "ch=%" PRIu32 " type=%u dma=%d idx=%d SUCCESS",
                         ch_id, bus_type, dma, i);
            } else {
                ESP_LOGE(TAG, "ch=%" PRIu32 " init failed: %s",
                         ch_id, esp_err_to_name(err));
                rt->bus_ch[i] = 0;
                /* Release DMA if init failed */
                if (dma) {
                    dma_pool_release_by_hw(rt->dma_pool, hw_id);
                }
            }
            return;
        }
    }
    ESP_LOGE(TAG, "Bus slots full (max=%d)", SCHED_MAX_CHANNELS);
}

/* ==== Public API ==== */

void bus_manager_init(bus_runtime_t *rt)
{
    (void)rt; /* pool already zeroed by app_state_init */
}

void bus_manager_cleanup_all(bus_runtime_t *rt)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (rt->bus_ctx[i].initialized) {
            /* Release DMA using saved hw_id (no manifest dependency) */
            char *hw_id_slot = get_bus_hw_id(rt, i);
            if (rt->dma_pool && hw_id_slot[0] != '\0') {
                dma_pool_release_by_hw(rt->dma_pool, hw_id_slot);
            }
            bus_dma_deinit(&rt->bus_ctx[i]);
            rt->bus_ch[i] = 0;
            hw_id_slot[0] = '\0';
        }
        /* Drain any stale pending entries from the queue */
        if (rt->pending_queues[i]) {
            xQueueReset(rt->pending_queues[i]);
        }
    }
}

void bus_manager_setup_from_manifest(bus_runtime_t *rt)
{
    const config_manifest_t *m = config_mgr_get_manifest();
    if (!m || !m->applied) return;
    for (int i = 0; i < m->channel_count; i++) {
        if (!m->channels[i].enabled) continue;
        bus_manager_reg_channel(rt, &m->channels[i]);
    }
}

/* v2.4: Incremental single-channel register */
void bus_manager_reg_channel(bus_runtime_t *rt, const config_channel_t *ch)
{
    reg_bus_channel(rt, ch->id, ch->bus_type,
                    ch->bus_config, ch->bus_config_len);
}

/* v2.4: Incremental single-channel unregister */
void bus_manager_unreg_channel(bus_runtime_t *rt, uint32_t channel_id)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (rt->bus_ch[i] == channel_id && rt->bus_ctx[i].initialized) {
            /* Release DMA using saved hw_id */
            char *hw_id_slot = get_bus_hw_id(rt, i);
            if (rt->dma_pool && hw_id_slot[0] != '\0') {
                dma_pool_release_by_hw(rt->dma_pool, hw_id_slot);
            }
            bus_dma_deinit(&rt->bus_ctx[i]);
            rt->bus_ch[i] = 0;
            hw_id_slot[0] = '\0';
            /* Drain stale pending entries */
            if (rt->pending_queues[i]) {
                xQueueReset(rt->pending_queues[i]);
            }
            ESP_LOGI(TAG, "Unregistered ch=%lu", (unsigned long)channel_id);
            return;
        }
    }
    ESP_LOGW(TAG, "Unregister ch=%lu: not found", (unsigned long)channel_id);
}

bus_dma_ctx_t *bus_manager_find_ctx(bus_runtime_t *rt, uint32_t channel_id)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++)
        if (rt->bus_ch[i] == channel_id && rt->bus_ctx[i].initialized)
            return &rt->bus_ctx[i];
    return NULL;
}

/* ==== WriteCommand handler ====
 *
 * Constructs a bus_cmd_t and posts it to the command queue.
 * No timeout derivation — the ESP32 does TX only for UART WriteCommands.
 * The backend handles timeout by watching for DataReport with matching
 * request_id.
 */

void bus_manager_on_write_cmd(bus_runtime_t *rt, uint32_t rid, uint32_t ch,
                               const uint8_t *d, size_t l, uint32_t rs)
{
    const config_manifest_t *m = config_mgr_get_manifest();

    /* Validate read_size upper bound */
    if (rs > 256) {
        if (s_write_rsp_cb) s_write_rsp_cb(rid, false, ESP_ERR_INVALID_ARG, "read_size too large");
        return;
    }

    /* P2-9: Prefer bus_type from ctx (set during bus_dma_init) over manifest lookup */
    bus_dma_ctx_t *bctx = bus_manager_find_ctx(rt, ch);

    bus_cmd_t cmd = {
        .request_id = rid,
        .channel_id = ch,
        .bus_type   = bctx ? bctx->bus_type : find_bus_type(m, ch),
        .tx_len     = l < CMD_TX_MAX ? l : CMD_TX_MAX,
        .delay_ms   = 0,    /* WriteCommand: no fixed delay */
        .read_size  = rs,   /* v2.5: expected RX bytes (0 = TX only) */
        .type       = CMD_WRITE,
        .uart_port  = derive_uart_port_for_channel(m, ch),
    };
    if (l > 0 && d) memcpy(cmd.tx_data, d, cmd.tx_len);

    /* Dispatch to per-bus queue */
    QueueHandle_t target_q;
    switch (cmd.bus_type) {
    case BUS_TYPE_UART:
        target_q = (cmd.uart_port == UART_NUM_0) ? rt->uart0_cmd_queue : rt->uart1_cmd_queue;
        break;
    case BUS_TYPE_SPI:  target_q = rt->spi_cmd_queue;  break;
    case BUS_TYPE_I2C:  target_q = rt->i2c_cmd_queue;  break;
    default:            target_q = rt->uart0_cmd_queue; break;
    }
    if (!xQueueSend(target_q, &cmd, 0))
        if (s_write_rsp_cb) s_write_rsp_cb(rid, false, 0xFFFF, "queue full");
}
