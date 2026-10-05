/**
 * v3_manifest_view.c — PROTOTYPE (WS-F, phase 2, host only)
 * Mirrors the offset scheme of docs/设计/协议v3-私有变长二进制-评估与设计.md §4.
 * No production code links against this; it exists to prove the model and to
 * make the RAM claim in the design review mechanically checkable.
 */
#include "v3_manifest_view.h"
#include <string.h>

/* Field numbers are the authoritative ones from docs/协议/二进制帧协议.md and
 * esp32-collector/components/config_mgr/config_mgr.c (not invented here). */
enum {
    MF_F_MANIFEST_ID = 1,
    MF_F_TEMPLATES   = 3,
    MF_F_CHANNELS    = 4,
    MF_F_DMA_CONFIGS = 5,
    MF_F_SYNC_ID     = 8,
    MF_F_LOG_STREAM  = 10,
    MF_F_GPIO_CONFIGS = 11,
    MF_F_PWM_CONFIGS = 12,
};

enum {
    CH_F_ID          = 1,
    CH_F_HARDWARE_ID = 2,
    CH_F_TEMPLATE_IDS = 3,
    CH_F_INTERVAL_MS = 4,
    CH_F_ENABLED     = 5,
    CH_F_BUS_TYPE    = 6,
    CH_F_BUS_CONFIG  = 7,
    CH_F_DMA_ENABLED = 8,
    CH_F_EDGE_GROUPS = 9,
};

enum {
    EDGE_F_ID       = 1,
    EDGE_F_HARDWARE = 2,
    EDGE_F_COMMANDS = 3,
};

enum {
    CMD_F_TEMPLATE_ID = 1,
    CMD_F_INTERVAL_MS = 2,
    CMD_F_ENABLED     = 3,
};

enum {
    TPL_F_ID          = 1,
    TPL_F_WRITE_DATA  = 2,
    TPL_F_READ_LENGTH = 3,
};

/* ---- helpers ------------------------------------------------------------ */

static bool buf_span_ok(const v3mv_t *view, size_t off, size_t len)
{
    return off <= view->raw_len && len <= view->raw_len - off;
}

/* Build a span from a decoded length-delimited field. Rejects fields longer
 * than the uint16 length slot instead of silently truncating them. */
static int make_span(const v3mv_t *view, const frame_field_t *f, v3mv_span_t *out)
{
    if (f->wire_type != WIRE_LENGTH_DELIMITED || !f->value.bytes.ptr) return V3MV_ERR_WIRE;
    if (f->value.bytes.len > UINT16_MAX) return V3MV_ERR_TOO_MANY;
    if (f->value.bytes.ptr < view->raw) return V3MV_ERR_TRUNCATED;
    size_t off = (size_t)(f->value.bytes.ptr - view->raw);
    if (!buf_span_ok(view, off, f->value.bytes.len)) return V3MV_ERR_TRUNCATED;
    out->off = (uint32_t)off;
    out->len = (uint16_t)f->value.bytes.len;
    return V3MV_OK;
}

/* Map a frame_codec terminal error onto the view's error domain. Keeping the
 * distinction matters for operators: UNDERFLOW means the retained frame is
 * shorter than its own declared lengths (corruption / truncation), while
 * INVALID_TAG/OVERFLOW means a malformed tag. Both are fail-closed. */
static int map_codec_err(frame_err_t err)
{
    switch (err) {
    case FRAME_ERR_UNDERFLOW:
    case FRAME_ERR_INCOMPLETE:
        return V3MV_ERR_TRUNCATED;
    case FRAME_ERR_OVERFLOW:
    case FRAME_ERR_INVALID_TAG:
    default:
        return V3MV_ERR_WIRE;
    }
}

static int read_varint_field(const frame_field_t *f, uint32_t *out)
{
    if (f->wire_type != WIRE_VARINT || f->value.varint > UINT32_MAX) return V3MV_ERR_WIRE;
    *out = (uint32_t)f->value.varint;
    return V3MV_OK;
}

/* Scan one channel sub-message, filling scalars + spans. */
static int parse_channel(const v3mv_t *view, v3mv_span_t span, v3mv_channel_t *out)
{
    if (!buf_span_ok(view, span.off, span.len)) return V3MV_ERR_TRUNCATED;
    frame_decoder_t dec;
    frame_err_t init_err = frame_decoder_init_sub(&dec, view->raw + span.off, span.len);
    if (init_err != FRAME_OK) return map_codec_err(init_err);

    bool seen_id = false;
    frame_field_t f;
    frame_err_t err;
    while ((err = frame_decoder_next(&dec, &f)) == FRAME_OK) {
        switch (f.field_num) {
        case CH_F_ID:
            if (read_varint_field(&f, &out->id) != V3MV_OK) return V3MV_ERR_WIRE;
            seen_id = true;
            break;
        case CH_F_HARDWARE_ID:
            if (read_varint_field(&f, &out->hardware_id) != V3MV_OK) return V3MV_ERR_WIRE;
            break;
        case CH_F_TEMPLATE_IDS: {
            uint32_t tid;
            if (read_varint_field(&f, &tid) != V3MV_OK) return V3MV_ERR_WIRE;
            /* Bound BEFORE writing the slot, as config_mgr does. An index-based
             * accessor that bounds after the write is a one-element overflow. */
            if (out->template_count >= V3MV_MAX_TEMPLATE_IDS) return V3MV_ERR_TOO_MANY;
            out->template_ids[out->template_count].off = tid; /* scalar id, not a span */
            out->template_count++;
            break;
        }
        case CH_F_INTERVAL_MS:
            if (read_varint_field(&f, &out->interval_ms) != V3MV_OK) return V3MV_ERR_WIRE;
            break;
        case CH_F_ENABLED:
            out->enabled = f.wire_type == WIRE_VARINT && f.value.varint != 0;
            if (f.wire_type != WIRE_VARINT) return V3MV_ERR_WIRE;
            break;
        case CH_F_BUS_TYPE: {
            uint32_t v;
            if (read_varint_field(&f, &v) != V3MV_OK) return V3MV_ERR_WIRE;
            out->bus_type = (uint8_t)v;
            break;
        }
        case CH_F_BUS_CONFIG: {
            int rc = make_span(view, &f, &out->bus_config);
            if (rc != V3MV_OK) return rc;
            break;
        }
        case CH_F_DMA_ENABLED:
            if (f.wire_type != WIRE_VARINT) return V3MV_ERR_WIRE;
            out->dma_enabled = f.value.varint != 0;
            out->dma_enabled_present = true;
            break;
        case CH_F_EDGE_GROUPS: {
            v3mv_span_t span;
            int rc = make_span(view, &f, &span);
            if (rc != V3MV_OK) return rc;
            if (out->edge_count >= V3MV_MAX_EDGES) return V3MV_ERR_TOO_MANY;
            out->edge_groups[out->edge_count] = span;
            out->edge_count++;
            break;
        }
        default:
            break; /* unknown fields are skipped, matching the open TLV model */
        }
    }
    if (err != FRAME_DONE) return map_codec_err(err);
    if (!seen_id) return V3MV_ERR_REQUIRED;
    return V3MV_OK;
}

static int record_repeated(v3mv_span_t *slots, uint8_t *count, uint8_t limit,
                           const v3mv_t *view, const frame_field_t *f, int *rc)
{
    if (*count >= limit) { *rc = V3MV_ERR_TOO_MANY; return -1; }
    int e = make_span(view, f, &slots[*count]);
    if (e != V3MV_OK) { *rc = e; return -1; }
    (*count)++;
    return 0;
}

/* ---- public API --------------------------------------------------------- */

v3mv_err_t v3mv_init(v3mv_t *view, const uint8_t *frame, size_t len)
{
    if (!view || !frame || len < 2) return V3MV_ERR_NULL;
    if (len > UINT32_MAX) return V3MV_ERR_TRUNCATED;
    if (frame[0] != MSG_CONFIG_MFST) return V3MV_ERR_TYPE;

    *view = (v3mv_t){ .raw = frame, .raw_len = len };

    frame_decoder_t dec;
    frame_err_t ierr = frame_decoder_init(&dec, frame, len);
    if (ierr != FRAME_OK) return (v3mv_err_t)map_codec_err(ierr);

    frame_field_t f;
    frame_err_t err;
    int rc = V3MV_OK;
    while ((err = frame_decoder_next(&dec, &f)) == FRAME_OK) {
        if (f.wire_type == WIRE_LENGTH_DELIMITED && !f.value.bytes.ptr) return V3MV_ERR_WIRE;
        switch (f.field_num) {
        case MF_F_MANIFEST_ID:
            rc = make_span(view, &f, &view->manifest_id);
            if (rc != V3MV_OK) return (v3mv_err_t)rc;
            break;
        case MF_F_SYNC_ID:
            rc = make_span(view, &f, &view->sync_id);
            if (rc != V3MV_OK) return (v3mv_err_t)rc;
            break;
        case MF_F_TEMPLATES:
            if (f.wire_type != WIRE_LENGTH_DELIMITED) return V3MV_ERR_WIRE;
            if (record_repeated(view->templates, &view->template_count,
                                V3MV_MAX_TEMPLATES, view, &f, &rc)) return (v3mv_err_t)rc;
            break;
        case MF_F_CHANNELS:
            if (f.wire_type != WIRE_LENGTH_DELIMITED) return V3MV_ERR_WIRE;
            if (record_repeated(view->channels, &view->channel_count,
                                V3MV_MAX_CHANNELS, view, &f, &rc)) return (v3mv_err_t)rc;
            break;
        case MF_F_DMA_CONFIGS:
            if (f.wire_type != WIRE_LENGTH_DELIMITED) return V3MV_ERR_WIRE;
            if (record_repeated(view->dma_configs, &view->dma_config_count,
                                V3MV_MAX_DMA_CONFIGS, view, &f, &rc)) return (v3mv_err_t)rc;
            break;
        case MF_F_GPIO_CONFIGS:
            if (f.wire_type != WIRE_LENGTH_DELIMITED) return V3MV_ERR_WIRE;
            if (record_repeated(view->gpio_configs, &view->gpio_config_count,
                                V3MV_MAX_GPIO_CONFIGS, view, &f, &rc)) return (v3mv_err_t)rc;
            break;
        case MF_F_PWM_CONFIGS:
            if (f.wire_type != WIRE_LENGTH_DELIMITED) return V3MV_ERR_WIRE;
            if (record_repeated(view->pwm_configs, &view->pwm_config_count,
                                V3MV_MAX_PWM_CONFIGS, view, &f, &rc)) return (v3mv_err_t)rc;
            break;
        default:
            break;
        }
    }
    if (err != FRAME_DONE) return (v3mv_err_t)map_codec_err(err);

    if (view->manifest_id.len == 0) {
        /* config_mgr rejects a manifest without a non-empty manifest_id. */
        return V3MV_ERR_REQUIRED;
    }
    return V3MV_OK;
}

static v3mv_err_t copy_span(const v3mv_t *view, v3mv_span_t s, char *out, size_t out_sz)
{
    if (!view || !out || out_sz == 0) return V3MV_ERR_NULL;
    if (!buf_span_ok(view, s.off, s.len)) return V3MV_ERR_TRUNCATED;
    if (s.len >= out_sz) return V3MV_ERR_TOO_MANY; /* fail closed, never clip silently */
    memcpy(out, view->raw + s.off, s.len);
    out[s.len] = '\0';
    return V3MV_OK;
}

v3mv_err_t v3mv_manifest_id(const v3mv_t *view, char *out, size_t out_sz)
{
    return copy_span(view, view ? view->manifest_id : (v3mv_span_t){0}, out, out_sz);
}

v3mv_err_t v3mv_sync_id(const v3mv_t *view, char *out, size_t out_sz)
{
    return copy_span(view, view ? view->sync_id : (v3mv_span_t){0}, out, out_sz);
}

v3mv_err_t v3mv_get_channel(const v3mv_t *view, uint8_t index, v3mv_channel_t *out)
{
    if (!view || !out) return V3MV_ERR_NULL;
    if (index >= view->channel_count) return V3MV_ERR_TRUNCATED;
    *out = (v3mv_channel_t){0};
    return (v3mv_err_t)parse_channel(view, view->channels[index], out);
}

v3mv_err_t v3mv_get_edge(const v3mv_t *view, const v3mv_channel_t *channel,
                            uint8_t index, v3mv_edge_t *out);
v3mv_err_t v3mv_get_command(const v3mv_t *view, const v3mv_channel_t *channel,
                            uint8_t edge_index, uint8_t command_index,
                            v3mv_command_t *out);

v3mv_err_t v3mv_get_edge(const v3mv_t *view, const v3mv_channel_t *channel,
                            uint8_t index, v3mv_edge_t *out)
{
    if (!view || !channel || !out) return V3MV_ERR_NULL;
    if (index >= channel->edge_count) return V3MV_ERR_TRUNCATED;
    const v3mv_span_t s = channel->edge_groups[index];
    if (!buf_span_ok(view, s.off, s.len)) return V3MV_ERR_TRUNCATED;

    *out = (v3mv_edge_t){0};
    frame_decoder_t dec;
    if (frame_decoder_init_sub(&dec, view->raw + s.off, s.len) != FRAME_OK)
        return V3MV_ERR_WIRE;
    frame_field_t f;
    frame_err_t err;
    while ((err = frame_decoder_next(&dec, &f)) == FRAME_OK) {
        switch (f.field_num) {
        case EDGE_F_ID:
            if (read_varint_field(&f, &out->edge_device_id) != V3MV_OK) return V3MV_ERR_WIRE;
            break;
        case EDGE_F_HARDWARE:
            if (read_varint_field(&f, &out->hardware_id) != V3MV_OK) return V3MV_ERR_WIRE;
            break;
        case EDGE_F_COMMANDS:
            if (f.wire_type != WIRE_LENGTH_DELIMITED) return V3MV_ERR_WIRE;
            if (out->command_count >= V3MV_MAX_COMMANDS) return V3MV_ERR_TOO_MANY;
            out->command_count++;
            break;
        default:
            break;
        }
    }
    return err == FRAME_DONE ? V3MV_OK : V3MV_ERR_WIRE;
}

v3mv_err_t v3mv_get_command(const v3mv_t *view, const v3mv_channel_t *channel,
                            uint8_t edge_index, uint8_t command_index,
                            v3mv_command_t *out)
{
    if (!view || !channel || !out) return V3MV_ERR_NULL;
    if (edge_index >= channel->edge_count) return V3MV_ERR_TRUNCATED;
    const v3mv_span_t g = channel->edge_groups[edge_index];
    if (!buf_span_ok(view, g.off, g.len)) return V3MV_ERR_TRUNCATED;

    frame_decoder_t dec;
    if (frame_decoder_init_sub(&dec, view->raw + g.off, g.len) != FRAME_OK)
        return V3MV_ERR_WIRE;
    frame_field_t f;
    frame_err_t err;
    uint8_t seen = 0;
    while ((err = frame_decoder_next(&dec, &f)) == FRAME_OK) {
        if (f.field_num != EDGE_F_COMMANDS) continue;
        if (f.wire_type != WIRE_LENGTH_DELIMITED) return V3MV_ERR_WIRE;
        if (seen++ != command_index) continue;
        if (!buf_span_ok(view, (size_t)(f.value.bytes.ptr - view->raw), f.value.bytes.len))
            return V3MV_ERR_TRUNCATED;
        frame_decoder_t cdec;
        if (frame_decoder_init_sub(&cdec, f.value.bytes.ptr, f.value.bytes.len) != FRAME_OK)
            return V3MV_ERR_WIRE;
        frame_field_t cf;
        frame_err_t cerr;
        while ((cerr = frame_decoder_next(&cdec, &cf)) == FRAME_OK) {
            switch (cf.field_num) {
            case CMD_F_TEMPLATE_ID:
                if (read_varint_field(&cf, &out->template_id) != V3MV_OK) return V3MV_ERR_WIRE;
                break;
            case CMD_F_INTERVAL_MS:
                if (read_varint_field(&cf, &out->interval_ms) != V3MV_OK) return V3MV_ERR_WIRE;
                break;
            case CMD_F_ENABLED:
                if (cf.wire_type != WIRE_VARINT) return V3MV_ERR_WIRE;
                out->enabled = cf.value.varint != 0;
                break;
            default:
                break;
            }
        }
        return cerr == FRAME_DONE ? V3MV_OK : V3MV_ERR_WIRE;
    }
    if (err != FRAME_DONE) return V3MV_ERR_WIRE;
    return V3MV_ERR_TRUNCATED;
}

v3mv_err_t v3mv_find_template(const v3mv_t *view, uint32_t id,
                              v3mv_span_t *write_data, uint32_t *read_length)
{
    if (!view || !write_data || !read_length) return V3MV_ERR_NULL;
    for (uint8_t i = 0; i < view->template_count; i++) {
        const v3mv_span_t s = view->templates[i];
        if (!buf_span_ok(view, s.off, s.len)) return V3MV_ERR_TRUNCATED;
        frame_decoder_t dec;
        if (frame_decoder_init_sub(&dec, view->raw + s.off, s.len) != FRAME_OK)
            return V3MV_ERR_WIRE;
        uint32_t tid = 0;
        v3mv_span_t wd = {0};
        uint32_t rl = 0;
        frame_field_t f;
        frame_err_t err;
        while ((err = frame_decoder_next(&dec, &f)) == FRAME_OK) {
            switch (f.field_num) {
            case TPL_F_ID:
                if (read_varint_field(&f, &tid) != V3MV_OK) return V3MV_ERR_WIRE;
                break;
            case TPL_F_WRITE_DATA:
                if (f.wire_type != WIRE_LENGTH_DELIMITED) return V3MV_ERR_WIRE;
                wd.off = (uint32_t)(f.value.bytes.ptr - view->raw);
                wd.len = (uint16_t)f.value.bytes.len;
                break;
            case TPL_F_READ_LENGTH:
                if (read_varint_field(&f, &rl) != V3MV_OK) return V3MV_ERR_WIRE;
                break;
            default:
                break;
            }
        }
        if (err != FRAME_DONE) return V3MV_ERR_WIRE;
        if (tid == id) { *write_data = wd; *read_length = rl; return V3MV_OK; }
    }
    return V3MV_ERR_REQUIRED;
}

size_t v3mv_index_bytes(void) { return sizeof(v3mv_t); }
