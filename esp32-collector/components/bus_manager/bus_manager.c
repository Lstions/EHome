/**
 * @file bus_manager.c
 * @brief Bus DMA context pool — register, find, cleanup channels.
 *
 * Owns the shared pool of bus_dma_ctx_t instances in bus_runtime_t.
 * All bus channel lifecycle operations go through this module.
 *
 * WriteCommand handling: constructs a bus_cmd_t and posts it to the
 * command queue. Legacy read timeouts are validated and bounded before queueing.
 *
 * P2-8: Decoupled from app_state_t — uses bus_runtime_t for dependency
 * injection.  All s->field accesses replaced with rt->field.
 */

#include "bus_manager.h"
#include "legacy_write_guard.h"
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

/* ==== Helper: get bus_hw_id[i] from flat array ==== */
static char *get_bus_hw_id(bus_runtime_t *rt, int idx)
{
    return rt->bus_hw_id + idx * 16;
}

/* ==== Register one channel ==== */

static esp_err_t reg_bus_channel(bus_runtime_t *rt, uint32_t ch_id,
                            uint8_t bus_type,
                            const uint8_t *config, size_t config_len)
{
    ESP_LOGI(TAG, "reg_bus_channel: ch=%" PRIu32 " type=%u cfg_len=%zu", 
             ch_id, bus_type, config_len);
    
    /* Already registered? */
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++)
        if (rt->bus_ch[i] == ch_id && rt->bus_ctx[i].initialized) return ESP_OK;

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
            if (user_wants_dma) {
                if (!rt->dma_pool) {
                    ESP_LOGE(TAG, "ch=%" PRIu32 " DMA requested without a pool", ch_id);
                    rt->bus_ch[i] = 0;
                    return ESP_ERR_INVALID_STATE;
                }
                uint32_t dma_id = 0;
                esp_err_t dma_err = dma_pool_allocate(rt->dma_pool, bus_type,
                                                        hw_id, &dma_id);
                if (dma_err == ESP_OK) {
                    dma = true;
                    ESP_LOGI(TAG, "ch=%" PRIu32 " DMA allocated (id=%" PRIu32 ")",
                             ch_id, dma_id);
                } else {
                    ESP_LOGE(TAG, "ch=%" PRIu32 " DMA requested but allocation failed: %s",
                             ch_id, esp_err_to_name(dma_err));
                    rt->bus_ch[i] = 0;
                    return dma_err;
                }
            } else {
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
                    esp_err_t release_err = dma_pool_release_by_hw(rt->dma_pool, hw_id);
                    if (release_err != ESP_OK) return release_err;
                }
            }
            return err;
        }
    }
    ESP_LOGE(TAG, "Bus slots full (max=%d)", SCHED_MAX_CHANNELS);
    return ESP_ERR_NO_MEM;
}

/* ==== Public API ==== */

void bus_manager_init(bus_runtime_t *rt)
{
    (void)rt; /* pool already zeroed by app_state_init */
}

esp_err_t bus_manager_cleanup_all(bus_runtime_t *rt)
{
    if (!rt) return ESP_ERR_INVALID_ARG;
    esp_err_t cleanup_err = ESP_OK;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        char *hw_id_slot = get_bus_hw_id(rt, i);
        if (rt->bus_ctx[i].initialized) {
            esp_err_t err = bus_dma_deinit(&rt->bus_ctx[i]);
            if (err != ESP_OK) {
                cleanup_err = err;
                continue;
            }
        }
        /* Release DMA only after the driver is confirmed detached. If DMA
         * release fails, retain channel metadata so a later cleanup retries. */
        if (rt->bus_ch[i] != 0 && rt->dma_pool && hw_id_slot[0] != '\0') {
            esp_err_t err = dma_pool_release_by_hw(rt->dma_pool, hw_id_slot);
            if (err != ESP_OK) {
                cleanup_err = err;
                continue;
            }
        }
        if (rt->bus_ch[i] != 0) {
            rt->bus_ch[i] = 0;
            hw_id_slot[0] = '\0';
        }
        /* Drain any stale pending entries from the queue */
        if (rt->pending_queues[i]) {
            xQueueReset(rt->pending_queues[i]);
        }
    }
    return cleanup_err;
}

esp_err_t bus_manager_setup_from_manifest(bus_runtime_t *rt)
{
    const config_manifest_t *m = config_mgr_get_manifest();
    if (!m || !m->applied) return ESP_ERR_INVALID_STATE;
    return bus_manager_apply_manifest(rt, m);
}

/* v2.4: Incremental single-channel register */
esp_err_t bus_manager_reg_channel(bus_runtime_t *rt, const config_channel_t *ch)
{
    if (!rt || !ch || !ch->enabled || ch->id == 0 || ch->bus_config_len == 0) {
        return ESP_ERR_INVALID_ARG;
    }
    return reg_bus_channel(rt, ch->id, ch->bus_type,
                           ch->bus_config, ch->bus_config_len);
}

esp_err_t bus_manager_apply_manifest(bus_runtime_t *rt, const config_manifest_t *manifest)
{
    if (!rt || !manifest) return ESP_ERR_INVALID_ARG;
    uint8_t enabled_count = 0;
    for (int i = 0; i < manifest->channel_count; i++) {
        if (!manifest->channels[i].enabled) continue;
        if (manifest->channels[i].id == 0 ||
            manifest->channels[i].bus_config_len == 0 ||
            manifest->channels[i].bus_config_len > sizeof(manifest->channels[i].bus_config)) {
            return ESP_ERR_INVALID_ARG;
        }
        for (int j = 0; j < i; j++) {
            if (manifest->channels[j].enabled &&
                manifest->channels[j].id == manifest->channels[i].id) {
                return ESP_ERR_INVALID_ARG;
            }
        }
        if (++enabled_count > SCHED_MAX_CHANNELS) return ESP_ERR_NO_MEM;
    }

    esp_err_t err = bus_manager_cleanup_all(rt);
    if (err != ESP_OK) return err;
    for (int i = 0; i < manifest->channel_count; i++) {
        if (!manifest->channels[i].enabled) continue;
        err = bus_manager_reg_channel(rt, &manifest->channels[i]);
        if (err != ESP_OK) {
            esp_err_t cleanup_err = bus_manager_cleanup_all(rt);
            return cleanup_err == ESP_OK ? err : cleanup_err;
        }
    }
    return ESP_OK;
}

/* v2.4: Incremental single-channel unregister */
esp_err_t bus_manager_unreg_channel(bus_runtime_t *rt, uint32_t channel_id)
{
    if (!rt || channel_id == 0) return ESP_ERR_INVALID_ARG;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (rt->bus_ch[i] == channel_id && rt->bus_ctx[i].initialized) {
            char *hw_id_slot = get_bus_hw_id(rt, i);
            esp_err_t err = bus_dma_deinit(&rt->bus_ctx[i]);
            if (err != ESP_OK) return err;
            if (rt->dma_pool && hw_id_slot[0] != '\0') {
                err = dma_pool_release_by_hw(rt->dma_pool, hw_id_slot);
                if (err != ESP_OK) return err;
            }
            rt->bus_ch[i] = 0;
            hw_id_slot[0] = '\0';
            /* Drain stale pending entries */
            if (rt->pending_queues[i]) {
                xQueueReset(rt->pending_queues[i]);
            }
            ESP_LOGI(TAG, "Unregistered ch=%lu", (unsigned long)channel_id);
            return ESP_OK;
        }
    }
    ESP_LOGW(TAG, "Unregister ch=%lu: not found", (unsigned long)channel_id);
    return ESP_ERR_NOT_FOUND;
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
 * Legacy read timeouts are validated and bounded before queueing; the worker
 * applies the value while waiting for the physical response.
 */

void bus_manager_on_write_cmd(bus_runtime_t *rt, uint32_t rid, uint32_t ch,
                               const uint8_t *d, size_t l, uint32_t rs,
                               uint32_t edge_device_id, uint32_t rx_timeout_ms)
{
    if (!rt || !legacy_write_args_valid(ch, d, l, rs, rx_timeout_ms, CMD_TX_MAX)) {
        if (s_write_rsp_cb) s_write_rsp_cb(rid, false, ESP_ERR_INVALID_ARG, "invalid write command");
        return;
    }

    bus_dma_ctx_t *bctx = bus_manager_find_ctx(rt, ch);
    if (!bctx) {
        if (s_write_rsp_cb) s_write_rsp_cb(rid, false, ESP_ERR_NOT_FOUND, "channel not found");
        return;
    }

    bus_cmd_t cmd = {
        .request_id = rid,
        .channel_id = ch,
        .bus_type   = bctx->bus_type,
        .tx_len     = l,
        .delay_ms   = 0,    /* WriteCommand: no fixed delay */
        .read_size  = rs,   /* v2.5: expected RX bytes (0 = TX only) */
        .rx_timeout_ms = rx_timeout_ms,
        .edge_device_id = edge_device_id, /* 0 when sent by old firmware/backend */
        .type       = CMD_WRITE,
    };
    if (cmd.bus_type == BUS_TYPE_UART) cmd.uart_port = bctx->cfg.uart.port;
    if (!legacy_write_route_valid(cmd.bus_type, (int)cmd.uart_port)) {
        if (s_write_rsp_cb) s_write_rsp_cb(rid, false, ESP_ERR_INVALID_ARG, "unsupported bus route");
        return;
    }
    if (l > 0 && d) memcpy(cmd.tx_data, d, cmd.tx_len);

    /* Dispatch to per-bus queue */
    QueueHandle_t target_q;
    switch (cmd.bus_type) {
    case BUS_TYPE_UART:
        if (cmd.uart_port == UART_NUM_0) target_q = rt->uart0_cmd_queue;
        else if (cmd.uart_port == UART_NUM_1) target_q = rt->uart1_cmd_queue;
        else {
            if (s_write_rsp_cb) s_write_rsp_cb(rid, false, ESP_ERR_INVALID_ARG, "unsupported uart port");
            return;
        }
        break;
    case BUS_TYPE_SPI:  target_q = rt->spi_cmd_queue;  break;
    case BUS_TYPE_I2C:  target_q = rt->i2c_cmd_queue;  break;
    default:
        if (s_write_rsp_cb) s_write_rsp_cb(rid, false, ESP_ERR_INVALID_ARG, "unsupported bus type");
        return;
    }
    if (!target_q) {
        if (s_write_rsp_cb) s_write_rsp_cb(rid, false, ESP_ERR_INVALID_STATE, "bus queue unavailable");
        return;
    }
    if (!xQueueSend(target_q, &cmd, 0))
        if (s_write_rsp_cb) s_write_rsp_cb(rid, false, 0xFFFF, "queue full");
}

bool bus_manager_on_channel_cmd_v2(bus_runtime_t *rt, uint32_t ch,
                                   const uint8_t *data, size_t len, uint32_t read_size,
                                   uint32_t rx_timeout_ms, uint32_t post_tx_delay_ms,
                                   const uint8_t *plan_data, size_t plan_len,
                                   uint8_t plan_step_count,
                                   uint8_t control_slot)
{
    if (!rt || control_slot == CONTROL_SLOT_NONE ||
        !legacy_write_args_valid(ch, data, len, read_size, rx_timeout_ms, CMD_TX_MAX) ||
        plan_len > CMD_PLAN_MAX || plan_step_count > CMD_BATCH_MAX_STEPS ||
        (plan_step_count > 0 && (!plan_data || plan_step_count < 2))) return false;
    bus_dma_ctx_t *bctx = bus_manager_find_ctx(rt, ch);
    if (!bctx) return false;
    if (plan_step_count > 0 && bctx->bus_type != BUS_TYPE_UART) return false;
    bus_cmd_t cmd = { .channel_id = ch, .bus_type = bctx->bus_type, .tx_len = len,
        .delay_ms = post_tx_delay_ms, .read_size = read_size, .rx_timeout_ms = rx_timeout_ms,
        .channel_cmd_v2 = true, .control_slot = control_slot, .plan_len = plan_len,
        .plan_step_count = plan_step_count, .type = CMD_WRITE };
    if (cmd.bus_type == BUS_TYPE_UART) cmd.uart_port = bctx->cfg.uart.port;
    if (!legacy_write_route_valid(cmd.bus_type, (int)cmd.uart_port)) return false;
    /* Only UART has an explicit TX→RX turnaround phase in the shared worker.
     * SPI/I2C transactions are atomic and therefore reject a requested delay. */
    if (cmd.bus_type != BUS_TYPE_UART && post_tx_delay_ms != 0) return false;
    memcpy(cmd.tx_data, data, len);
    if (plan_len > 0) memcpy(cmd.plan_data, plan_data, plan_len);
    QueueHandle_t target_q = NULL;
    switch (cmd.bus_type) {
    case BUS_TYPE_UART:
        target_q = cmd.uart_port == UART_NUM_0 ? rt->uart0_cmd_queue :
                   cmd.uart_port == UART_NUM_1 ? rt->uart1_cmd_queue : NULL;
        break;
    case BUS_TYPE_SPI: target_q = rt->spi_cmd_queue; break;
    case BUS_TYPE_I2C: target_q = rt->i2c_cmd_queue; break;
    default: return false;
    }
    ESP_LOGI(TAG, "V2 queue ch=%lu bus=%u uart=%d tx=%u read=%lu timeout=%lu slot=%u",
             (unsigned long)ch, (unsigned)cmd.bus_type, (int)cmd.uart_port,
             (unsigned)cmd.tx_len, (unsigned long)cmd.read_size,
             (unsigned long)cmd.rx_timeout_ms, (unsigned)control_slot);
    return target_q && xQueueSend(target_q, &cmd, 0) == pdTRUE;
}
