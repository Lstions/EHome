/**
 * v3_manifest_view.h — PROTOTYPE (WS-F, phase 2, host only)
 *
 * Demonstrates the v3 storage model from 方案 §7.2 without touching any
 * production component: a ConfigManifest frame is retained as raw bytes and
 * accessed through a small, fixed-size offset index instead of being
 * materialised into config_manifest_t. Reference sizes on the 32-bit device
 * ABI: HEAD 5,172 B; after the phase-1 diet S3 3,536 B / C6 3,172 B; this
 * index measures 460 B generic / 388 B S3 / 380 B C6.
 *
 * This header/file live under host_tests/ only. They are not compiled into
 * firmware and do not define the production wire format.
 */
#ifndef V3_MANIFEST_VIEW_H
#define V3_MANIFEST_VIEW_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "frame_codec.h"

#ifdef __cplusplus
extern "C" {
#endif

/* Bounds mirror config_mgr.h / sender.go. Kept explicit so the prototype can
 * report its own static footprint and reject over-limit frames fail-closed. */
#define V3MV_MAX_TEMPLATES     16
#define V3MV_MAX_CHANNELS       8
#define V3MV_MAX_TEMPLATE_IDS   8
#define V3MV_MAX_DMA_CONFIGS    8
#define V3MV_MAX_GPIO_CONFIGS  12
#define V3MV_MAX_PWM_CONFIGS    8
#define V3MV_MAX_EDGES          5
#define V3MV_MAX_COMMANDS       3

typedef enum {
    V3MV_OK = 0,
    V3MV_ERR_NULL = -1,
    V3MV_ERR_TYPE = -2,        /* frame does not start with MSG_CONFIG_MFST */
    V3MV_ERR_TRUNCATED = -3,   /* declared length exceeds the retained buffer */
    V3MV_ERR_TOO_MANY = -4,    /* repeated field exceeds a compile-time bound */
    V3MV_ERR_DUPLICATE = -5,   /* singular field repeated */
    V3MV_ERR_WIRE = -6,        /* wrong wire type / malformed varint */
    V3MV_ERR_REQUIRED = -7,    /* manifest_id or a channel id missing */
} v3mv_err_t;

/* Top-level index. One slot per repeated field is 4 bytes (offset) + 2 (len). */
typedef struct {
    uint32_t off;
    uint16_t len;
} v3mv_span_t;

typedef struct {
    const uint8_t *raw;        /* retained frame bytes (caller-owned) */
    size_t         raw_len;
    v3mv_span_t    manifest_id;
    v3mv_span_t    sync_id;
    v3mv_span_t    templates[V3MV_MAX_TEMPLATES];
    uint8_t        template_count;
    v3mv_span_t    channels[V3MV_MAX_CHANNELS];
    uint8_t        channel_count;
    v3mv_span_t    dma_configs[V3MV_MAX_DMA_CONFIGS];
    uint8_t        dma_config_count;
    v3mv_span_t    gpio_configs[V3MV_MAX_GPIO_CONFIGS];
    uint8_t        gpio_config_count;
    v3mv_span_t    pwm_configs[V3MV_MAX_PWM_CONFIGS];
    uint8_t        pwm_config_count;
} v3mv_t;

/* Parsed channel view: scalars + offsets into the retained raw bytes. */
typedef struct {
    uint32_t id;
    uint32_t hardware_id;
    uint32_t interval_ms;
    bool     enabled;
    bool     dma_enabled_present;
    bool     dma_enabled;
    uint8_t  bus_type;
    v3mv_span_t bus_config;
    v3mv_span_t template_ids[V3MV_MAX_TEMPLATE_IDS];
    uint8_t  template_count;
    uint8_t  edge_count;
    v3mv_span_t edge_groups[V3MV_MAX_EDGES];
} v3mv_channel_t;

/* Parsed edge/command view (iterated lazily, no fixed worst-case array). */
typedef struct {
    uint32_t edge_device_id;
    uint32_t hardware_id;
    uint8_t  command_count;
} v3mv_edge_t;

typedef struct {
    uint32_t template_id;
    uint32_t interval_ms;
    bool     enabled;
} v3mv_command_t;

v3mv_err_t v3mv_init(v3mv_t *view, const uint8_t *frame, size_t len);

/* Raw-frame strings are NOT NUL-terminated on the wire. Callers must copy;
 * returning a `const char *` would read past the span. This is the main
 * ergonomic cost of the raw-bytes model and is called out in the design doc. */
v3mv_err_t v3mv_manifest_id(const v3mv_t *view, char *out, size_t out_sz);
v3mv_err_t v3mv_sync_id(const v3mv_t *view, char *out, size_t out_sz);

/* Accessors parse on demand; `out` is caller-owned scratch (never stored). */
v3mv_err_t v3mv_get_channel(const v3mv_t *view, uint8_t index, v3mv_channel_t *out);
v3mv_err_t v3mv_get_edge(const v3mv_t *view, const v3mv_channel_t *channel,
                         uint8_t index, v3mv_edge_t *out);
v3mv_err_t v3mv_get_command(const v3mv_t *view, const v3mv_channel_t *channel,
                            uint8_t edge_index, uint8_t command_index,
                            v3mv_command_t *out);

/* Template lookup by id: scans template index, returns write_data span. */
v3mv_err_t v3mv_find_template(const v3mv_t *view, uint32_t id,
                              v3mv_span_t *write_data, uint32_t *read_length);

size_t v3mv_index_bytes(void);

#ifdef __cplusplus
}
#endif

#endif /* V3_MANIFEST_VIEW_H */
