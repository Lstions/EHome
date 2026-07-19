/**
 * @file hw_profile.c
 * @brief Hardware Profile — report encoder
 *
 * Static hardware tables are in hw_tables.c.
 * This file contains only the encoding functions and hw_profile_build_report().
 */

#include "hw_profile.h"
#include "frame_codec.h"
#include <string.h>
#include <stdlib.h>
#include <stdio.h>

#define COMMAND_ENGINE_REVISION 2U
#define COMMAND_ENGINE_BOOT_ID_MAX 17
#define COMMAND_ENGINE_MAX_TX 128U /* must track cmd_queue.h CMD_TX_MAX */
#define COMMAND_ENGINE_RAM_DEDUP_ENTRIES 4U /* RAM replay slots; final identity is also persisted in NVS */

static char s_boot_id[COMMAND_ENGINE_BOOT_ID_MAX] = "unknown";

void hw_profile_set_boot_id(const char *boot_id)
{
    if (!boot_id || boot_id[0] == '\0') return;
    snprintf(s_boot_id, sizeof(s_boot_id), "%s", boot_id);
}

static bool encode_command_engine(frame_encoder_t *enc)
{
    uint8_t sub[96];
    frame_encoder_t nested;
    frame_encoder_init_sub(&nested, sub, sizeof(sub));
    if (frame_encode_varint(&nested, 1, COMMAND_ENGINE_REVISION) != FRAME_OK ||
        frame_encode_string(&nested, 2, s_boot_id) != FRAME_OK ||
        frame_encode_bool(&nested, 3, true) != FRAME_OK ||
        frame_encode_bool(&nested, 4, true) != FRAME_OK ||
        frame_encode_bool(&nested, 5, true) != FRAME_OK ||
        frame_encode_varint(&nested, 6, 8) != FRAME_OK ||
        frame_encode_varint(&nested, 7, COMMAND_ENGINE_MAX_TX) != FRAME_OK ||
        frame_encode_varint(&nested, 8, 256) != FRAME_OK ||
        frame_encode_varint(&nested, 9, 30000) != FRAME_OK ||
        frame_encode_varint(&nested, 10, COMMAND_ENGINE_RAM_DEDUP_ENTRIES) != FRAME_OK) return false;
    return frame_encode_bytes(enc, 9, sub, frame_encoder_size(&nested)) == FRAME_OK;
}

/* ConfigManifest storage is fixed by config_mgr.  Publish these bounds as a
 * device fact so the server can reject an oversized manifest before MQTT
 * delivery rather than waiting for the collector to reject it. */
static bool encode_manifest_capacity(frame_encoder_t *enc)
{
    uint8_t sub[32];
    frame_encoder_t nested;
    frame_encoder_init_sub(&nested, sub, sizeof(sub));
    if (frame_encode_varint(&nested, 1, MAX_TEMPLATES) != FRAME_OK ||
        frame_encode_varint(&nested, 2, MAX_CHANNELS) != FRAME_OK ||
        frame_encode_varint(&nested, 3, MAX_TEMPLATE_IDS) != FRAME_OK) return false;
    return frame_encode_bytes(enc, 10, sub, frame_encoder_size(&nested)) == FRAME_OK;
}

/* ================================================================
 *  Sub-message encoding helpers
 *
 *  frame_encoder_init writes a 1-byte message type prefix.
 *  For sub-messages (embedded as bytes fields) we encode into a
 *  temp buffer with a dummy type, then skip the first byte when
 *  embedding via frame_encode_bytes.
 * ================================================================ */

/* Temporary buffer sizes for sub-message encoding */
/* Local copy of bus_config_get_dma_enabled (from bus_dma.h, avoids dependency) */
static inline bool local_bus_config_get_dma_enabled(uint8_t bus_type,
                                                      const uint8_t *bus_config,
                                                      size_t bus_config_len)
{
    size_t flags_offset = 0;
    size_t min_len = 0;
    switch (bus_type) {
    case 1: flags_offset = 6; min_len = 7; break;  /* UART */
    case 2: flags_offset = 7; min_len = 8; break;  /* I2C */
    case 3: flags_offset = 6; min_len = 7; break;  /* SPI */
    default: return false;
    }
    if (bus_config && bus_config_len >= min_len) {
        return (bus_config[flags_offset] & 0x01) != 0;
    }
    return false;
}

#define SUB_ENTRY_BUF   128
#define BUSES_BLOB_BUF  512
#define CHAN_BLOB_BUF   2048

/* ----------------------------------------------------------------
 *  Encode a single uart_entry sub-message
 * ---------------------------------------------------------------- */
static bool encode_uart_entry(uint8_t *out, size_t cap, size_t *out_len,
                              const hw_uart_t *u)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, out, cap, 0);  /* type 0 = sub-message */

    if (frame_encode_string(&enc, 1, u->id) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 2, u->port) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 3, u->default_tx_pin) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 4, u->default_rx_pin) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 5, u->max_baud) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 6, u->flags) != FRAME_OK) return false;

    *out_len = frame_encoder_size(&enc);
    return true;
}

/* ----------------------------------------------------------------
 *  Encode a single i2c_entry sub-message
 * ---------------------------------------------------------------- */
static bool encode_i2c_entry(uint8_t *out, size_t cap, size_t *out_len,
                             const hw_i2c_t *i)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, out, cap, 0);

    if (frame_encode_string(&enc, 1, i->id) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 2, i->port) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 3, i->default_sda) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 4, i->default_scl) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 5, i->max_freq_hz) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 6, i->flags) != FRAME_OK) return false;

    *out_len = frame_encoder_size(&enc);
    return true;
}

/* ----------------------------------------------------------------
 *  Encode a single spi_entry sub-message
 * ---------------------------------------------------------------- */
static bool encode_spi_entry(uint8_t *out, size_t cap, size_t *out_len,
                             const hw_spi_t *s)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, out, cap, 0);

    if (frame_encode_string(&enc, 1, s->id) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 2, s->port) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 3, s->default_mosi) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 4, s->default_miso) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 5, s->default_sclk) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 6, s->default_cs) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 7, s->max_freq_hz) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 8, s->flags) != FRAME_OK) return false;

    *out_len = frame_encoder_size(&enc);
    return true;
}

/* ----------------------------------------------------------------
 *  Encode a single gpio_entry sub-message
 * ---------------------------------------------------------------- */
static bool encode_gpio_entry(uint8_t *out, size_t cap, size_t *out_len,
                              const hw_gpio_t *g)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, out, cap, 0);

    if (frame_encode_string(&enc, 1, g->id) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 2, g->pin) != FRAME_OK) return false;

    *out_len = frame_encoder_size(&enc);
    return true;
}

/* ----------------------------------------------------------------
 *  Encode a single adc_entry sub-message
 * ---------------------------------------------------------------- */
static bool encode_adc_entry(uint8_t *out, size_t cap, size_t *out_len,
                             const hw_adc_t *a)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, out, cap, 0);

    if (frame_encode_string(&enc, 1, a->id) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 2, a->unit) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 3, a->channel) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 4, a->pin) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 5, a->max_bits) != FRAME_OK) return false;

    *out_len = frame_encoder_size(&enc);
    return true;
}

/* ----------------------------------------------------------------
 *  Encode a single pwm_entry sub-message
 * ---------------------------------------------------------------- */
static bool encode_pwm_entry(uint8_t *out, size_t cap, size_t *out_len,
                             const hw_pwm_t *p)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, out, cap, 0);

    if (frame_encode_string(&enc, 1, p->id) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 2, p->channel) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 3, p->timer_count) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 4, p->max_resolution_bits) != FRAME_OK) return false;

    *out_len = frame_encoder_size(&enc);
    return true;
}

/* ----------------------------------------------------------------
 *  Encode a single channel_entry sub-message
 * ---------------------------------------------------------------- */
static bool encode_channel_entry(uint8_t *out, size_t cap, size_t *out_len,
                                 const config_channel_t *ch)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, out, cap, 0);

    if (frame_encode_varint(&enc, 1, ch->id) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 2, ch->bus_type) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 3, ch->hardware_id) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 4, ch->interval_ms) != FRAME_OK) return false;
    if (frame_encode_varint(&enc, 5, ch->enabled ? 1 : 0) != FRAME_OK) return false;

    /* field6: raw bus_config bytes */
    if (ch->bus_config_len > 0) {
        if (frame_encode_bytes(&enc, 6, ch->bus_config,
                               ch->bus_config_len) != FRAME_OK)
            return false;
    }

    /* field7: template_ids repeated varint */
    for (uint8_t i = 0; i < ch->template_count; i++) {
        if (frame_encode_varint(&enc, 7, ch->template_ids[i]) != FRAME_OK)
            return false;
    }

    /* field8: dma_enabled extracted from bus_config flags */
    bool dma = local_bus_config_get_dma_enabled(ch->bus_type, ch->bus_config,
                                          ch->bus_config_len);
    if (frame_encode_varint(&enc, 8, dma ? 1 : 0) != FRAME_OK) return false;

    *out_len = frame_encoder_size(&enc);
    return true;
}

/* ================================================================
 *  Build the buses_blob sub-message
 * ================================================================ */
static bool build_buses_blob(uint8_t *blob, size_t cap, size_t *out_len)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, blob, cap, 0);

    uint8_t entry_buf[SUB_ENTRY_BUF];
    size_t  entry_len;

    /* field1: uart_entry (repeated) */
    for (int i = 0; i < HW_UART_COUNT; i++) {
        if (!encode_uart_entry(entry_buf, sizeof(entry_buf), &entry_len,
                               &hw_uarts[i]))
            return false;
        /* embed sub-message: skip the type byte at offset 0 */
        if (frame_encode_bytes(&enc, 1, entry_buf + 1,
                               entry_len - 1) != FRAME_OK)
            return false;
    }

    /* field2: i2c_entry (repeated) */
    for (int i = 0; i < HW_I2C_COUNT; i++) {
        if (!encode_i2c_entry(entry_buf, sizeof(entry_buf), &entry_len,
                              &hw_i2cs[i]))
            return false;
        if (frame_encode_bytes(&enc, 2, entry_buf + 1,
                               entry_len - 1) != FRAME_OK)
            return false;
    }

    /* field3: spi_entry (repeated) */
    for (int i = 0; i < HW_SPI_COUNT; i++) {
        if (!encode_spi_entry(entry_buf, sizeof(entry_buf), &entry_len,
                              &hw_spis[i]))
            return false;
        if (frame_encode_bytes(&enc, 3, entry_buf + 1,
                               entry_len - 1) != FRAME_OK)
            return false;
    }

    /* field4: gpio_entry (repeated) */
    for (int i = 0; i < HW_GPIO_COUNT; i++) {
        if (!encode_gpio_entry(entry_buf, sizeof(entry_buf), &entry_len,
                               &hw_gpios[i]))
            return false;
        if (frame_encode_bytes(&enc, 4, entry_buf + 1,
                               entry_len - 1) != FRAME_OK)
            return false;
    }

    /* field5: adc_entry (repeated) */
    for (int i = 0; i < HW_ADC_COUNT; i++) {
        if (!encode_adc_entry(entry_buf, sizeof(entry_buf), &entry_len,
                              &hw_adcs[i]))
            return false;
        if (frame_encode_bytes(&enc, 5, entry_buf + 1,
                               entry_len - 1) != FRAME_OK)
            return false;
    }

    /* field6: pwm_entry (repeated) */
    for (int i = 0; i < HW_PWM_COUNT; i++) {
        if (!encode_pwm_entry(entry_buf, sizeof(entry_buf), &entry_len,
                              &hw_pwms[i]))
            return false;
        if (frame_encode_bytes(&enc, 6, entry_buf + 1,
                               entry_len - 1) != FRAME_OK)
            return false;
    }

    *out_len = frame_encoder_size(&enc);
    return true;
}

/* ================================================================
 *  Build the channels_blob sub-message
 * ================================================================ */
static bool build_channels_blob(uint8_t *blob, size_t cap, size_t *out_len,
                                  const config_channel_t *channels, uint8_t channel_count)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, blob, cap, 0);

    if (!channels || channel_count == 0) {
        /* No channels configured — empty blob */
        *out_len = frame_encoder_size(&enc);
        return true;
    }

    uint8_t entry_buf[256];
    size_t  entry_len;

    /* field1: channel_entry (repeated) — only enabled channels */
    for (uint8_t i = 0; i < channel_count; i++) {
        const config_channel_t *ch = &channels[i];
        if (!ch->enabled) continue;

        if (!encode_channel_entry(entry_buf, sizeof(entry_buf), &entry_len, ch))
            return false;
        if (frame_encode_bytes(&enc, 1, entry_buf + 1,
                               entry_len - 1) != FRAME_OK)
            return false;
    }

    *out_len = frame_encoder_size(&enc);
    return true;
}

/* ================================================================
 *  Public API: Build the full ResourceReport frame
 * ================================================================ */
bool hw_profile_build_report(uint8_t *buf, size_t sz, size_t *out_len,
                              dma_pool_t *dma_pool,
                              const config_channel_t *channels, uint8_t channel_count)
{
    if (!buf || !out_len || sz == 0) return false;

    /* Count enabled channels for resource_count */
    uint8_t enabled_channels = 0;
    if (channels) {
        for (uint8_t i = 0; i < channel_count; i++) {
            if (channels[i].enabled) enabled_channels++;
        }
    }
    uint32_t resource_count = HW_RESOURCE_COUNT + enabled_channels;

    /* Build sub-blobs into heap buffers to avoid stack overflow */
    uint8_t *buses_buf = calloc(1, BUSES_BLOB_BUF);
    if (!buses_buf) return false;
    size_t  buses_len = 0;
    bool ok = build_buses_blob(buses_buf, BUSES_BLOB_BUF, &buses_len);
    if (!ok) { free(buses_buf); return false; }

    uint8_t *channels_buf = calloc(1, CHAN_BLOB_BUF);
    if (!channels_buf) { free(buses_buf); return false; }
    size_t  channels_len = 0;
    ok = build_channels_blob(channels_buf, CHAN_BLOB_BUF, &channels_len,
                           channels, channel_count);
    if (!ok) { free(buses_buf); free(channels_buf); return false; }

    /* Encode top-level ResourceReport frame */
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sz, MSG_RESOURCE_REPORT);

    /* field1: platform string */
    if (frame_encode_string(&enc, 1, HW_PLATFORM_STRING) != FRAME_OK)
        goto cleanup_fail;

    /* field2: resource_count varint */
    if (frame_encode_varint(&enc, 2, resource_count) != FRAME_OK)
        goto cleanup_fail;

    /* field3: buses_blob bytes (sub-message, skip type byte) */
    if (buses_len > 1) {
        if (frame_encode_bytes(&enc, 3, buses_buf + 1,
                               buses_len - 1) != FRAME_OK)
            goto cleanup_fail;
    }

    /* field4: channels_blob bytes (sub-message, skip type byte) */
    if (channels_len > 1) {
        if (frame_encode_bytes(&enc, 4, channels_buf + 1,
                               channels_len - 1) != FRAME_OK)
            goto cleanup_fail;
    }

    /* field8: dma_channels (repeated DmaChannel sub-messages) */
    if (dma_pool) {
        uint8_t *dma_buf = calloc(1, 512);
        if (dma_buf) {
            size_t dma_len = dma_pool_serialize(dma_pool, dma_buf, 512);
            if (dma_len > 0) {
                frame_encoder_append_raw(&enc, dma_buf, dma_len);
            }
            free(dma_buf);
        }
    }

    /* field9: bounded ChannelCmdV2 admission/final capability. */
    if (!encode_command_engine(&enc)) goto cleanup_fail;

    /* field10: ConfigManifest storage bounds. */
    if (!encode_manifest_capacity(&enc)) goto cleanup_fail;

    free(buses_buf);
    free(channels_buf);
    *out_len = frame_encoder_size(&enc);
    return true;

cleanup_fail:
    free(buses_buf);
    free(channels_buf);
    return false;
}
