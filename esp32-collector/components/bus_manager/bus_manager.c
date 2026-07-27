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
#include "bus_lease_policy.h"
#include "legacy_write_guard.h"
#include "bus_dma.h"
#include "config_mgr.h"
#include "dma_pool.h"
#include "hw_tables.h"
#include "hw_profile.h"
#include "esp_log.h"
#include "driver/uart.h"
#include <string.h>
#include <inttypes.h>

#define TAG "BUS_MGR"

/* ---- Callback for write response (injected by main.c) ---- */
static write_rsp_cb_t s_write_rsp_cb = NULL;
static uint32_t s_runtime_generation;

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

static void format_resource_hw_id(char *buf, size_t buflen, uint8_t bus_type,
                                  const char *resource_id)
{
    const char *prefix = bus_type == BUS_TYPE_UART ? "uart" :
                         bus_type == BUS_TYPE_SPI ? "spi" :
                         bus_type == BUS_TYPE_I2C ? "i2c" : "unknown";
    if (!resource_id || resource_id[0] == '\0') {
        snprintf(buf, buflen, "%s/UNKNOWN", prefix);
        return;
    }
    snprintf(buf, buflen, "%s/%s", prefix, resource_id);
}

static bool controller_in_use(const bus_runtime_t *rt, uint8_t bus_type,
                              uint32_t controller_id)
{
    if (!rt) return false;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        const bus_dma_ctx_t *ctx = &rt->bus_ctx[i];
        if (!ctx->initialized || ctx->bus_type != bus_type) continue;
        uint32_t active = 0;
        if (bus_type == BUS_TYPE_UART) active = (uint32_t)ctx->cfg.uart.port;
        else if (bus_type == BUS_TYPE_SPI) active = (uint32_t)ctx->cfg.spi.host;
        else if (bus_type == BUS_TYPE_I2C) active = (uint32_t)ctx->cfg.i2c.port;
        if (active == controller_id) return true;
    }
    return false;
}

static uint32_t read_be32_config(const uint8_t *p);

static bool runtime_bus_config_matches(const bus_dma_ctx_t *ctx, uint8_t bus_type,
                                       const uint8_t *config, size_t config_len)
{
    if (!ctx || !ctx->initialized || ctx->bus_type != bus_type || !config) return false;
    switch (bus_type) {
    case BUS_TYPE_UART:
        return config_len >= 6 &&
               ctx->cfg.uart.tx_pin == config[0] &&
               ctx->cfg.uart.rx_pin == config[1] &&
               ctx->cfg.uart.baud == read_be32_config(&config[2]);
    case BUS_TYPE_SPI: {
        if (config_len < 6) return false;
        int mosi = config_len >= 9 ? config[6] : -1;
        int miso = config_len >= 9 ? config[7] : -1;
        int sclk = config_len >= 9 ? config[8] : -1;
        return ctx->cfg.spi.mosi_pin == mosi &&
               ctx->cfg.spi.miso_pin == miso &&
               ctx->cfg.spi.sclk_pin == sclk;
    }
    case BUS_TYPE_I2C:
        return config_len >= 7 && ctx->cfg.i2c.sda_pin == config[0] &&
               ctx->cfg.i2c.scl_pin == config[1];
    default:
        return false;
    }
}

static const char *resource_id_for_controller(uint8_t bus_type, uint32_t controller_id)
{
    if (bus_type == BUS_TYPE_UART) {
        for (int i = 0; i < HW_UART_COUNT; i++)
            if ((uint32_t)hw_uarts[i].port == controller_id) return hw_uarts[i].id;
    } else if (bus_type == BUS_TYPE_SPI) {
        for (int i = 0; i < HW_SPI_COUNT; i++)
            if ((uint32_t)hw_spis[i].port == controller_id) return hw_spis[i].id;
    } else if (bus_type == BUS_TYPE_I2C) {
        for (int i = 0; i < HW_I2C_COUNT; i++)
            if ((uint32_t)hw_i2cs[i].port == controller_id) return hw_i2cs[i].id;
    }
    return NULL;
}

/* Resolve a canonical pool binding for a custom-pin channel using the same
 * deterministic first-free controller policy as bus_dma. */
static void derive_runtime_hw_id(const bus_runtime_t *rt, char *buf,
                                 size_t buflen, uint8_t bus_type,
                                 const uint8_t *config, size_t config_len)
{
    derive_hw_id(buf, buflen, bus_type, config, config_len);
    if (!strstr(buf, "UNKNOWN")) return;

    /* A shared SPI/I2C bus may use custom pins that are not present in the
     * profile table.  Reuse the active runtime's actual controller before
     * selecting a free one; otherwise the second logical device would get a
     * different DMA binding even though bus_dma correctly reuses the same
     * hardware bus. */
    if (rt) {
        for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
            const bus_dma_ctx_t *ctx = &rt->bus_ctx[i];
            if (!runtime_bus_config_matches(ctx, bus_type, config, config_len)) continue;
            uint32_t controller_id = 0;
            if (bus_type == BUS_TYPE_UART) controller_id = (uint32_t)ctx->cfg.uart.port;
            else if (bus_type == BUS_TYPE_SPI) controller_id = (uint32_t)ctx->cfg.spi.host;
            else if (bus_type == BUS_TYPE_I2C) controller_id = (uint32_t)ctx->cfg.i2c.port;
            const char *resource_id = resource_id_for_controller(bus_type, controller_id);
            if (resource_id) {
                format_resource_hw_id(buf, buflen, bus_type, resource_id);
                return;
            }
        }
    }

    if (bus_type == BUS_TYPE_UART) {
        for (int i = 0; i < HW_UART_COUNT; i++) {
            uart_port_t port = (uart_port_t)hw_uarts[i].port;
            if (port == UART_NUM_0 && !bus_dma_uart0_is_available()) continue;
            if (!controller_in_use(rt, bus_type, (uint32_t)port)) {
                format_resource_hw_id(buf, buflen, bus_type, hw_uarts[i].id);
                return;
            }
        }
    } else if (bus_type == BUS_TYPE_SPI) {
        int mosi = config_len >= 9 ? config[6] : -1;
        int miso = config_len >= 9 ? config[7] : -1;
        int sclk = config_len >= 9 ? config[8] : -1;
        spi_host_device_t preferred = hw_derive_spi_host(mosi, miso, sclk, SPI_HOST_MAX);
        for (int i = 0; i < HW_SPI_COUNT; i++) {
            uint32_t host = hw_spis[i].port;
            if (preferred < SPI_HOST_MAX && host != (uint32_t)preferred) continue;
            if (!controller_in_use(rt, bus_type, host)) {
                format_resource_hw_id(buf, buflen, bus_type, hw_spis[i].id);
                return;
            }
        }
        for (int i = 0; i < HW_SPI_COUNT; i++) {
            if (!controller_in_use(rt, bus_type, hw_spis[i].port)) {
                format_resource_hw_id(buf, buflen, bus_type, hw_spis[i].id);
                return;
            }
        }
    } else if (bus_type == BUS_TYPE_I2C) {
        for (int i = 0; i < HW_I2C_COUNT; i++) {
            if (!controller_in_use(rt, bus_type, hw_i2cs[i].port)) {
                format_resource_hw_id(buf, buflen, bus_type, hw_i2cs[i].id);
                return;
            }
        }
    }
}

/* ==== Helper: get bus_hw_id[i] from flat array ==== */
static char *get_bus_hw_id(bus_runtime_t *rt, int idx)
{
    return rt->bus_hw_id + idx * 16;
}

/* A DMA pool entry is keyed by the physical resource, while the runtime may
 * contain multiple logical channels sharing that resource. Releasing by
 * hw_id is safe only after the last initialized channel detaches. */
static bool runtime_has_other_hw_lease(const bus_runtime_t *rt, int excluded_idx,
                                       const char *hw_id)
{
    if (!rt || !rt->bus_ctx || !rt->bus_ch || !rt->bus_hw_id ||
        !hw_id || hw_id[0] == '\0') return false;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (i == excluded_idx || rt->bus_ch[i] == 0 ||
            !rt->bus_ctx[i].initialized) continue;
        if (strcmp(rt->bus_hw_id + i * 16, hw_id) == 0) return true;
    }
    return false;
}

static uint32_t read_be32_config(const uint8_t *p)
{
    return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) |
           ((uint32_t)p[2] << 8) | (uint32_t)p[3];
}

static bool resource_pin_reserved(int pin)
{
    return pin == HW_RESERVED_USB_DN || pin == HW_RESERVED_USB_DP ||
           pin == HW_RESERVED_LED;
}

static bool claim_resource_pin(int *owners, int pin, int owner)
{
    if (pin < 0 || pin >= 256) return true;
    if (owners[pin] >= 0 && owners[pin] != owner) return false;
    owners[pin] = owner;
    return true;
}

/* A manifest plan records the concrete controller selected before the old
 * runtime is torn down.  Keeping this lease explicit is what makes dynamic
 * allocation deterministic across a rebuild: a compatible Channel keeps its
 * UART/SPI/I2C controller even when manifest order changes. */
typedef struct {
    bool valid;
    int32_t controller_id;
} bus_plan_entry_t;

static int32_t runtime_controller_id(const bus_dma_ctx_t *ctx)
{
    if (!ctx || !ctx->initialized) return -1;
    switch (ctx->bus_type) {
    case BUS_TYPE_UART: return (int32_t)ctx->cfg.uart.port;
    case BUS_TYPE_SPI:  return (int32_t)ctx->cfg.spi.host;
    case BUS_TYPE_I2C:  return (int32_t)ctx->cfg.i2c.port;
    default: return -1;
    }
}

static const bus_dma_ctx_t *find_compatible_runtime_lease(
    const bus_runtime_t *rt, uint32_t channel_id, const config_channel_t *ch)
{
    if (!rt || !ch) return NULL;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (rt->bus_ch[i] != channel_id) continue;
        if (runtime_bus_config_matches(&rt->bus_ctx[i], ch->bus_type,
                                       ch->bus_config, ch->bus_config_len))
            return &rt->bus_ctx[i];
    }
    return NULL;
}

static int32_t find_restore_lease(const bus_runtime_t *rt,
                                  const config_channel_t *ch)
{
    if (!rt || !ch || !rt->lease_hints_valid) return -1;
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        const bus_lease_hint_t *hint = &rt->lease_hints[i];
        if (hint->valid && hint->channel_id == ch->id &&
            hint->bus_type == ch->bus_type)
            return hint->controller_id;
    }
    return -1;
}

static int resource_index_for_controller(uint8_t bus_type, int32_t controller_id)
{
    if (controller_id < 0) return -1;
    if (bus_type == BUS_TYPE_UART) {
        for (int i = 0; i < HW_UART_COUNT; i++)
            if ((int32_t)hw_uarts[i].port == controller_id) return i;
    } else if (bus_type == BUS_TYPE_SPI) {
        for (int i = 0; i < HW_SPI_COUNT; i++)
            if ((int32_t)hw_spis[i].port == controller_id) return i;
    } else if (bus_type == BUS_TYPE_I2C) {
        for (int i = 0; i < HW_I2C_COUNT; i++)
            if ((int32_t)hw_i2cs[i].port == controller_id) return i;
    }
    return -1;
}

/* P1 preflight.  This function is deliberately side-effect free: it runs
 * before bus_manager_cleanup_all(), so pin/controller/DMA conflicts reject a
 * manifest while the old runtime remains usable. */
static esp_err_t validate_manifest_resources(bus_runtime_t *rt,
                                             const config_manifest_t *manifest,
                                             bus_plan_entry_t *plan)
{
    if (!rt || !manifest) return ESP_ERR_INVALID_ARG;
    if (manifest->channel_count > MAX_CHANNELS) return ESP_ERR_NO_MEM;
    if (plan) {
        for (int i = 0; i < MAX_CHANNELS; i++) {
            plan[i].valid = false;
            plan[i].controller_id = -1;
        }
    }
    bool used_uart[HW_UART_COUNT] = {false};
    bool used_spi[HW_SPI_COUNT] = {false};
    bool used_i2c[HW_I2C_COUNT] = {false};
    bool spi_dma_mode[HW_SPI_COUNT] = {false};
    bool i2c_dma_mode[HW_I2C_COUNT] = {false};
    bool spi_mode_set[HW_SPI_COUNT] = {false};
    bool i2c_mode_set[HW_I2C_COUNT] = {false};
    bool spi_pins_set[HW_SPI_COUNT] = {false};
    bool i2c_pins_set[HW_I2C_COUNT] = {false};
    int spi_bus_pins[HW_SPI_COUNT][3];
    int i2c_bus_pins[HW_I2C_COUNT][2];
    int pin_owners[256];
    bool used_spi_cs[256] = {false};
    for (size_t p = 0; p < sizeof(pin_owners) / sizeof(pin_owners[0]); p++)
        pin_owners[p] = -1;
    memset(spi_bus_pins, -1, sizeof(spi_bus_pins));
    memset(i2c_bus_pins, -1, sizeof(i2c_bus_pins));
    dma_pool_t simulated_dma;
    bool have_dma = rt->dma_pool != NULL;
    if (have_dma) memcpy(&simulated_dma, rt->dma_pool, sizeof(simulated_dma));

    for (int i = 0; i < manifest->channel_count; i++) {
        const config_channel_t *ch = &manifest->channels[i];
        if (!ch->enabled) continue;
        if (ch->id == 0 || ch->bus_config_len == 0 ||
            ch->bus_config_len > sizeof(ch->bus_config)) return ESP_ERR_INVALID_ARG;
        for (int j = 0; j < i; j++) {
            if (manifest->channels[j].enabled &&
                manifest->channels[j].id == ch->id) return ESP_ERR_INVALID_ARG;
        }

        int resource_index = -1;
        const char *resource_id = NULL;
        const hw_uart_t *uart = NULL;
        bool dma_requested = config_channel_get_dma_enabled(ch);
        const bus_dma_ctx_t *lease = find_compatible_runtime_lease(rt, ch->id, ch);
        int32_t lease_controller = bus_lease_select_controller(
            runtime_controller_id(lease), lease != NULL, -1);
        if (!lease)
            lease_controller = find_restore_lease(rt, ch);

        switch (ch->bus_type) {
        case BUS_TYPE_UART: {
            if (ch->bus_config_len < 6) return ESP_ERR_INVALID_SIZE;
            int tx = ch->bus_config[0];
            int rx = ch->bus_config[1];
            uint32_t baud = read_be32_config(&ch->bus_config[2]);
            if (tx == rx || resource_pin_reserved(tx) || resource_pin_reserved(rx) ||
                baud == 0) return ESP_ERR_INVALID_ARG;

            uart_port_t port = lease_controller >= 0
                ? (uart_port_t)lease_controller
                : hw_derive_uart_port(tx, rx, UART_NUM_MAX);
            if (lease_controller >= 0 && port >= UART_NUM_MAX)
                return ESP_ERR_NOT_SUPPORTED;
            if (port < UART_NUM_MAX) {
                for (int u = 0; u < HW_UART_COUNT; u++) {
                    if ((uart_port_t)hw_uarts[u].port == port) {
                        uart = &hw_uarts[u];
                        resource_index = u;
                        break;
                    }
                }
                if (!uart) return ESP_ERR_NOT_SUPPORTED;
                if (port == UART_NUM_0 && !bus_dma_uart0_is_available())
                    return ESP_ERR_NOT_SUPPORTED;
            } else {
                /* Custom pins are assigned deterministically to the first
                 * compatible free UART. UART0 is skipped when boot manager
                 * has reserved it for download. */
                for (int u = 0; u < HW_UART_COUNT; u++) {
                    uart_port_t candidate = (uart_port_t)hw_uarts[u].port;
                    if (candidate == UART_NUM_0 && !bus_dma_uart0_is_available()) continue;
                    if (!used_uart[u]) {
                        uart = &hw_uarts[u];
                        port = candidate;
                        resource_index = u;
                        break;
                    }
                }
            }
            if (!uart || resource_index < 0 || resource_index >= HW_UART_COUNT ||
                used_uart[resource_index]) return ESP_ERR_NOT_SUPPORTED;
            if (baud > uart->max_baud) return ESP_ERR_INVALID_ARG;
            if (dma_requested && !(uart->flags & HW_UART_FLAG_DMA))
                return ESP_ERR_NOT_SUPPORTED;
            int owner = (BUS_TYPE_UART << 8) | resource_index;
            if (!claim_resource_pin(pin_owners, tx, owner) ||
                !claim_resource_pin(pin_owners, rx, owner))
                return ESP_ERR_INVALID_ARG;
            resource_id = uart->id;
            used_uart[resource_index] = true;
            break;
        }
        case BUS_TYPE_SPI: {
            if (ch->bus_config_len < 6) return ESP_ERR_INVALID_SIZE;
            uint32_t freq = read_be32_config(&ch->bus_config[2]);
            int mosi = ch->bus_config_len >= 9 ? ch->bus_config[6] : -1;
            int miso = ch->bus_config_len >= 9 ? ch->bus_config[7] : -1;
            int sclk = ch->bus_config_len >= 9 ? ch->bus_config[8] : -1;
            if (resource_pin_reserved(ch->bus_config[0]) ||
                (mosi >= 0 && resource_pin_reserved(mosi)) ||
                (miso >= 0 && resource_pin_reserved(miso)) ||
                (sclk >= 0 && resource_pin_reserved(sclk)) || freq == 0)
                return ESP_ERR_INVALID_ARG;
            if (lease_controller >= 0) {
                resource_index = resource_index_for_controller(BUS_TYPE_SPI,
                                                                 lease_controller);
                if (resource_index >= 0) resource_id = hw_spis[resource_index].id;
            }
            /* Reuse an already planned bus by its actual pin tuple when no
             * compatible runtime lease dictated a controller. */
            for (int s = 0; resource_index < 0 && s < HW_SPI_COUNT; s++) {
                if (spi_pins_set[s] &&
                    spi_bus_pins[s][0] == mosi &&
                    spi_bus_pins[s][1] == miso &&
                    spi_bus_pins[s][2] == sclk) {
                    resource_index = s;
                    resource_id = hw_spis[s].id;
                    break;
                }
            }
            for (int s = 0; resource_index < 0 && s < HW_SPI_COUNT; s++) {
                bool match = (mosi < 0 || hw_spis[s].default_mosi == mosi) &&
                             (miso < 0 || hw_spis[s].default_miso == miso) &&
                             (sclk < 0 || hw_spis[s].default_sclk == sclk);
                if (match) {
                    resource_index = s;
                    resource_id = hw_spis[s].id;
                    break;
                }
            }
            if (resource_index < 0) {
                /* Custom pins still consume a real SPI controller.  Choose
                 * the first deterministic free profile resource instead of
                 * rejecting the channel merely because its pin tuple is not
                 * the board default. */
                for (int s = 0; s < HW_SPI_COUNT; s++) {
                    if (!used_spi[s]) {
                        resource_index = s;
                        resource_id = hw_spis[s].id;
                        break;
                    }
                }
            }
            if (resource_index < 0 || freq > hw_spis[resource_index].max_freq_hz)
                return ESP_ERR_NOT_SUPPORTED;
            if (spi_pins_set[resource_index] &&
                (spi_bus_pins[resource_index][0] != mosi ||
                 spi_bus_pins[resource_index][1] != miso ||
                 spi_bus_pins[resource_index][2] != sclk))
                return ESP_ERR_INVALID_ARG;
            if ((mosi >= 0 && miso >= 0 && mosi == miso) ||
                (mosi >= 0 && sclk >= 0 && mosi == sclk) ||
                (miso >= 0 && sclk >= 0 && miso == sclk) ||
                (mosi >= 0 && ch->bus_config[0] == mosi) ||
                (miso >= 0 && ch->bus_config[0] == miso) ||
                (sclk >= 0 && ch->bus_config[0] == sclk))
                return ESP_ERR_INVALID_ARG;
            int owner = (BUS_TYPE_SPI << 8) | resource_index;
            if (!claim_resource_pin(pin_owners, mosi, owner) ||
                !claim_resource_pin(pin_owners, miso, owner) ||
                !claim_resource_pin(pin_owners, sclk, owner) ||
                !claim_resource_pin(pin_owners, ch->bus_config[0], owner) ||
                used_spi_cs[ch->bus_config[0]])
                return ESP_ERR_INVALID_ARG;
            used_spi_cs[ch->bus_config[0]] = true;
            spi_pins_set[resource_index] = true;
            spi_bus_pins[resource_index][0] = mosi;
            spi_bus_pins[resource_index][1] = miso;
            spi_bus_pins[resource_index][2] = sclk;
            if (dma_requested && !(hw_spis[resource_index].flags & HW_UART_FLAG_DMA))
                return ESP_ERR_NOT_SUPPORTED;
            if (spi_mode_set[resource_index] &&
                spi_dma_mode[resource_index] != dma_requested)
                return ESP_ERR_INVALID_ARG;
            spi_mode_set[resource_index] = true;
            spi_dma_mode[resource_index] = dma_requested;
            used_spi[resource_index] = true;
            break;
        }
        case BUS_TYPE_I2C: {
            if (ch->bus_config_len < 7) return ESP_ERR_INVALID_SIZE;
            int sda = ch->bus_config[0];
            int scl = ch->bus_config[1];
            uint32_t freq = read_be32_config(&ch->bus_config[3]);
            if (sda == scl || resource_pin_reserved(sda) || resource_pin_reserved(scl) ||
                freq == 0) return ESP_ERR_INVALID_ARG;
            if (lease_controller >= 0) {
                resource_index = resource_index_for_controller(BUS_TYPE_I2C,
                                                                 lease_controller);
                if (resource_index >= 0) resource_id = hw_i2cs[resource_index].id;
            }
            /* Exact pin reuse is valid for custom buses; a compatible runtime
             * lease takes precedence over this first-pass lookup. */
            for (int b = 0; resource_index < 0 && b < HW_I2C_COUNT; b++) {
                if (i2c_pins_set[b] &&
                    i2c_bus_pins[b][0] == sda &&
                    i2c_bus_pins[b][1] == scl) {
                    resource_index = b;
                    resource_id = hw_i2cs[b].id;
                    break;
                }
            }
            for (int b = 0; resource_index < 0 && b < HW_I2C_COUNT; b++) {
                bool match = hw_i2cs[b].default_sda == sda &&
                             hw_i2cs[b].default_scl == scl;
                if (match) {
                    resource_index = b;
                    resource_id = hw_i2cs[b].id;
                    break;
                }
            }
            if (resource_index < 0) {
                for (int b = 0; b < HW_I2C_COUNT; b++) {
                    if (!used_i2c[b]) {
                        resource_index = b;
                        resource_id = hw_i2cs[b].id;
                        break;
                    }
                }
            }
            if (resource_index < 0 || freq > hw_i2cs[resource_index].max_freq_hz)
                return ESP_ERR_NOT_SUPPORTED;
            if (i2c_pins_set[resource_index] &&
                (i2c_bus_pins[resource_index][0] != sda ||
                 i2c_bus_pins[resource_index][1] != scl))
                return ESP_ERR_INVALID_ARG;
            int owner = (BUS_TYPE_I2C << 8) | resource_index;
            if (!claim_resource_pin(pin_owners, sda, owner) ||
                !claim_resource_pin(pin_owners, scl, owner))
                return ESP_ERR_INVALID_ARG;
            i2c_pins_set[resource_index] = true;
            i2c_bus_pins[resource_index][0] = sda;
            i2c_bus_pins[resource_index][1] = scl;
            if (dma_requested && !(hw_i2cs[resource_index].flags & HW_UART_FLAG_DMA))
                return ESP_ERR_NOT_SUPPORTED;
            if (i2c_mode_set[resource_index] &&
                i2c_dma_mode[resource_index] != dma_requested)
                return ESP_ERR_INVALID_ARG;
            i2c_mode_set[resource_index] = true;
            i2c_dma_mode[resource_index] = dma_requested;
            used_i2c[resource_index] = true;
            break;
        }
        default:
            return ESP_ERR_NOT_SUPPORTED;
        }

        if (dma_requested) {
            if (!have_dma) return ESP_ERR_INVALID_STATE;
            uint32_t dma_id = 0;
            char hw_id[16] = {0};
            /* The planner has already selected a controller.  Use that
             * canonical lease ID even for custom pins, for which the raw
             * pin matcher intentionally returns UNKNOWN. */
            format_resource_hw_id(hw_id, sizeof(hw_id), ch->bus_type, resource_id);
            if (!resource_id || dma_pool_allocate(&simulated_dma, ch->bus_type,
                                                   hw_id, &dma_id) != ESP_OK)
                return ESP_ERR_NOT_FOUND;
        }
        if (plan) {
            plan[i].valid = true;
            if (ch->bus_type == BUS_TYPE_UART)
                plan[i].controller_id = (int32_t)uart->port;
            else if (ch->bus_type == BUS_TYPE_SPI)
                plan[i].controller_id = (int32_t)hw_spis[resource_index].port;
            else if (ch->bus_type == BUS_TYPE_I2C)
                plan[i].controller_id = (int32_t)hw_i2cs[resource_index].port;
        }
    }
    return ESP_OK;
}

/* ==== Register one channel ==== */

static esp_err_t reg_bus_channel(bus_runtime_t *rt, uint32_t ch_id,
                            uint8_t bus_type,
                            const uint8_t *config, size_t config_len,
                            bool dma_requested, uint32_t generation,
                            int32_t preferred_controller)
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
            if (preferred_controller >= 0) {
                const char *resource_id = resource_id_for_controller(
                    bus_type, (uint32_t)preferred_controller);
                if (resource_id)
                    format_resource_hw_id(hw_id, sizeof(hw_id), bus_type, resource_id);
                else
                    derive_runtime_hw_id(rt, hw_id, sizeof(hw_id), bus_type,
                                         config, config_len);
            } else {
                derive_runtime_hw_id(rt, hw_id, sizeof(hw_id), bus_type,
                                     config, config_len);
            }
            
            /* Respect user DMA preference from bus_config flags */
            bool user_wants_dma = dma_requested;
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
            esp_err_t err = bus_dma_init_preferred(&rt->bus_ctx[i], bus_type, dma,
                                                   config, config_len,
                                                   preferred_controller);
            if (err == ESP_OK) {
                /* Save hw_id for cleanup (avoid manifest dependency) */
                char *hw_id_slot = get_bus_hw_id(rt, i);
                strncpy(hw_id_slot, hw_id, 15);
                hw_id_slot[15] = '\0';
                uint32_t controller_id = 0;
                switch (bus_type) {
                case BUS_TYPE_UART: controller_id = (uint32_t)rt->bus_ctx[i].cfg.uart.port; break;
                case BUS_TYPE_SPI:  controller_id = (uint32_t)rt->bus_ctx[i].cfg.spi.host; break;
                case BUS_TYPE_I2C:  controller_id = (uint32_t)rt->bus_ctx[i].cfg.i2c.port; break;
                default: break;
                }
                hw_profile_runtime_set(ch_id, bus_type, controller_id,
                                       dma_requested, dma, generation);
                ESP_LOGI(TAG, "ch=%" PRIu32 " type=%u dma=%d idx=%d SUCCESS",
                         ch_id, bus_type, dma, i);
            } else {
                ESP_LOGE(TAG, "ch=%" PRIu32 " init failed: %s",
                         ch_id, esp_err_to_name(err));
                rt->bus_ch[i] = 0;
                /* Release DMA if init failed */
                if (dma && !runtime_has_other_hw_lease(rt, i, hw_id)) {
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
    if (rt) rt->lease_hints_valid = false;
}

void bus_manager_snapshot_leases(bus_runtime_t *rt)
{
    if (!rt) return;
    memset(rt->lease_hints, 0, sizeof(rt->lease_hints));
    if (!rt->bus_ctx || !rt->bus_ch) {
        rt->lease_hints_valid = true;
        return;
    }
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        bus_dma_ctx_t *ctx = &rt->bus_ctx[i];
        if (!ctx->initialized || rt->bus_ch[i] == 0) continue;
        rt->lease_hints[i].valid = true;
        rt->lease_hints[i].channel_id = rt->bus_ch[i];
        rt->lease_hints[i].bus_type = ctx->bus_type;
        rt->lease_hints[i].controller_id = runtime_controller_id(ctx);
    }
    rt->lease_hints_valid = true;
}

esp_err_t bus_manager_cleanup_all(bus_runtime_t *rt)
{
    if (!rt) return ESP_ERR_INVALID_ARG;
    hw_profile_runtime_clear();
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
        if (rt->bus_ch[i] != 0 && rt->dma_pool && hw_id_slot[0] != '\0' &&
            !runtime_has_other_hw_lease(rt, i, hw_id_slot)) {
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
                           ch->bus_config, ch->bus_config_len,
                           config_channel_get_dma_enabled(ch),
                           ++s_runtime_generation, -1);
}

esp_err_t bus_manager_apply_manifest(bus_runtime_t *rt, const config_manifest_t *manifest)
{
    if (!rt || !manifest) return ESP_ERR_INVALID_ARG;
    if (manifest->channel_count > MAX_CHANNELS) return ESP_ERR_NO_MEM;
    /* P1 preflight: reject resource conflicts before suspending the old
     * runtime/tearing down drivers. */
    bus_plan_entry_t plan[MAX_CHANNELS];
    esp_err_t plan_err = validate_manifest_resources(rt, manifest, plan);
    if (plan_err != ESP_OK) {
        ESP_LOGE(TAG, "manifest resource plan rejected: %s", esp_err_to_name(plan_err));
        return plan_err;
    }
    uint32_t generation = ++s_runtime_generation;
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
        const config_channel_t *ch = &manifest->channels[i];
        err = reg_bus_channel(rt, ch->id, ch->bus_type,
                              ch->bus_config, ch->bus_config_len,
                              config_channel_get_dma_enabled(ch), generation,
                              plan[i].valid ? plan[i].controller_id : -1);
        if (err != ESP_OK) {
            esp_err_t cleanup_err = bus_manager_cleanup_all(rt);
            return cleanup_err == ESP_OK ? err : cleanup_err;
        }
    }
    /* A successful apply has a new authoritative lease map.  Keep hints only
     * across a failed apply so the transaction rollback can consume them. */
    rt->lease_hints_valid = false;
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
            if (rt->dma_pool && hw_id_slot[0] != '\0' &&
                !runtime_has_other_hw_lease(rt, i, hw_id_slot)) {
                err = dma_pool_release_by_hw(rt->dma_pool, hw_id_slot);
                if (err != ESP_OK) return err;
            }
            rt->bus_ch[i] = 0;
            hw_id_slot[0] = '\0';
            /* Drain stale pending entries */
            if (rt->pending_queues[i]) {
                xQueueReset(rt->pending_queues[i]);
            }
            hw_profile_runtime_remove(channel_id);
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

uart_port_t bus_manager_get_uart_port(void *runtime, uint32_t channel_id)
{
    bus_runtime_t *rt = (bus_runtime_t *)runtime;
    bus_dma_ctx_t *ctx = bus_manager_find_ctx(rt, channel_id);
    if (!ctx || ctx->bus_type != BUS_TYPE_UART) return UART_NUM_MAX;
    return ctx->cfg.uart.port;
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

    /* Dispatch control commands to the controller's reserved control queue.
     * Scheduler samples use the separate sample queue, so telemetry pressure
     * cannot reject an on-demand WriteCommand. */
    QueueHandle_t target_q;
    switch (cmd.bus_type) {
    case BUS_TYPE_UART:
        if (cmd.uart_port == UART_NUM_0) target_q = rt->uart0_control_queue;
        else if (cmd.uart_port == UART_NUM_1) target_q = rt->uart1_control_queue;
        else if (cmd.uart_port == UART_NUM_2) target_q = rt->uart2_control_queue;
        else {
            if (s_write_rsp_cb) s_write_rsp_cb(rid, false, ESP_ERR_INVALID_ARG, "unsupported uart port");
            return;
        }
        break;
    case BUS_TYPE_SPI:  target_q = rt->spi_control_queue;  break;
    case BUS_TYPE_I2C:  target_q = rt->i2c_control_queue;  break;
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
        target_q = cmd.uart_port == UART_NUM_0 ? rt->uart0_control_queue :
                   cmd.uart_port == UART_NUM_1 ? rt->uart1_control_queue :
                   cmd.uart_port == UART_NUM_2 ? rt->uart2_control_queue : NULL;
        break;
    case BUS_TYPE_SPI: target_q = rt->spi_control_queue; break;
    case BUS_TYPE_I2C: target_q = rt->i2c_control_queue; break;
    default: return false;
    }
    ESP_LOGI(TAG, "V2 queue ch=%lu bus=%u uart=%d tx=%u read=%lu timeout=%lu slot=%u",
             (unsigned long)ch, (unsigned)cmd.bus_type, (int)cmd.uart_port,
             (unsigned)cmd.tx_len, (unsigned long)cmd.read_size,
             (unsigned long)cmd.rx_timeout_ms, (unsigned)control_slot);
    return target_q && xQueueSend(target_q, &cmd, 0) == pdTRUE;
}
