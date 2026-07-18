/**
 * @file frame_codec.h
 * @brief Binary Frame Protocol - ESP32 Encoder/Decoder
 * 
 * ~300 lines, zero-allocation, stack-based
 */

#ifndef FRAME_CODEC_H
#define FRAME_CODEC_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include <string.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === Error codes === */
typedef enum {
    FRAME_OK = 0,
    FRAME_DONE = 1,
    FRAME_ERR_OVERFLOW = -1,
    FRAME_ERR_UNDERFLOW = -2,
    FRAME_ERR_INVALID_TAG = -3,
    FRAME_ERR_INCOMPLETE = -4,
} frame_err_t;

/* === Wire types (protobuf compatible) === */
#define WIRE_VARINT           0
#define WIRE_FIXED64          1
#define WIRE_LENGTH_DELIMITED 2
#define WIRE_START_GROUP      3
#define WIRE_END_GROUP        4
#define WIRE_FIXED32          5

/* === Message types === */
#define MSG_HELLO       0x01
#define MSG_STATUS_RPT  0x02
#define MSG_DATA_RPT    0x03
#define MSG_CONFIG_MFST 0x04
#define MSG_CONFIG_RSLT 0x05
#define MSG_WRITE_CMD   0x06
#define MSG_WRITE_RSP   0x07
#define MSG_PING        0x08
#define MSG_PONG        0x09
#define MSG_OTA_CMD     0x0A
#define MSG_OTA_PROG    0x0B
#define MSG_SCAN_RPT    0x0C
#define MSG_SCAN_REQ    0x0D
#define MSG_QUERY_REQ   0x0E
#define MSG_QUERY_RSP   0x0F
#define MSG_CONFIG_QUERY  0x10
#define MSG_CONFIG_REPORT 0x11
#define MSG_HELLO_ACK     0x12
/* v2.1 sync messages */
#define MSG_CONFIG_SYNC_REQ  0x13
#define MSG_CONFIG_SYNC_RSP  0x14
/* v2.4 resource reporting */
#define MSG_RESOURCE_REPORT  0x19
#define MSG_QUERY_RESOURCES  0x1A
/* v3.0 peripheral control (GPIO + PWM) */
#define MSG_PERIPH_CMD       0x1B
#define MSG_PERIPH_RSP       0x1C
/* v2.5 log streaming */
#define MSG_LOG_STREAM       0x1D

/* === Encoder === */
typedef struct {
    uint8_t *buf;
    size_t   pos;
    size_t   capacity;
} frame_encoder_t;

void frame_encoder_init(frame_encoder_t *enc, uint8_t *buf, size_t cap, uint8_t msg_type);
/* Initialize an embedded message. Unlike frame_encoder_init(), this does not
 * prepend a top-level message-type byte. */
void frame_encoder_init_sub(frame_encoder_t *enc, uint8_t *buf, size_t cap);
frame_err_t frame_encode_varint(frame_encoder_t *enc, uint8_t field_num, uint64_t value);
frame_err_t frame_encode_string(frame_encoder_t *enc, uint8_t field_num, const char *str);
frame_err_t frame_encode_bytes(frame_encoder_t *enc, uint8_t field_num, const uint8_t *data, size_t len);
/**
 * @brief Append raw bytes directly to encoder (no field tag/length prefix).
 *
 * Use for pre-encoded sub-message payloads that already contain their own
 * field tags. Avoids direct enc->pos manipulation by callers.
 */
frame_err_t frame_encoder_append_raw(frame_encoder_t *enc, const uint8_t *data, size_t len);
/* Bool is encoded as varint (0 or 1), protobuf-compatible */
static inline frame_err_t frame_encode_bool(frame_encoder_t *enc, uint8_t field_num, bool value) {
    return frame_encode_varint(enc, field_num, value ? 1 : 0);
}
static inline size_t frame_encoder_size(const frame_encoder_t *enc) { return enc->pos; }
static inline uint8_t *frame_encoder_data(frame_encoder_t *enc) { return enc->buf; }

/* === Decoder === */
typedef struct {
    const uint8_t *buf;
    size_t         pos;
    size_t         len;
} frame_decoder_t;

typedef struct {
    uint8_t  field_num;
    uint8_t  wire_type;
    union {
        uint64_t varint;
        struct { const uint8_t *ptr; size_t len; } bytes;
    } value;
} frame_field_t;

frame_err_t frame_decoder_init(frame_decoder_t *dec, const uint8_t *buf, size_t len);
frame_err_t frame_decoder_init_sub(frame_decoder_t *dec, const uint8_t *buf, size_t len);
frame_err_t frame_decoder_next(frame_decoder_t *dec, frame_field_t *field);

/* === Varint helpers === */
static inline size_t frame_varint_size(uint64_t value) {
    size_t size = 1;
    while (value > 0x7F) { size++; value >>= 7; }
    return size;
}

static inline size_t frame_encode_varint_to_buf(uint8_t *buf, uint64_t value) {
    size_t i = 0;
    while (value > 0x7F) {
        buf[i++] = (value & 0x7F) | 0x80;
        value >>= 7;
    }
    buf[i++] = value & 0x7F;
    return i;
}

/* === Field accessors (safe extraction from decoded fields) === */

static inline frame_err_t frame_field_get_string(const frame_field_t *f, char *buf, size_t sz) {
    if (!f || !buf || sz == 0) return FRAME_ERR_INVALID_TAG;
    if (f->wire_type != WIRE_LENGTH_DELIMITED) return FRAME_ERR_INVALID_TAG;
    if (!f->value.bytes.ptr) { buf[0] = '\0'; return FRAME_OK; }
    size_t n = f->value.bytes.len < sz - 1 ? f->value.bytes.len : sz - 1;
    memcpy(buf, f->value.bytes.ptr, n);
    buf[n] = '\0';
    return FRAME_OK;
}

static inline frame_err_t frame_field_get_varint(const frame_field_t *f, uint64_t *v) {
    if (!f || !v) return FRAME_ERR_INVALID_TAG;
    if (f->wire_type != WIRE_VARINT) return FRAME_ERR_INVALID_TAG;
    *v = f->value.varint;
    return FRAME_OK;
}

static inline frame_err_t frame_field_get_bytes(const frame_field_t *f,
                                                  const uint8_t **data, size_t *len) {
    if (!f || !data || !len) return FRAME_ERR_INVALID_TAG;
    if (f->wire_type != WIRE_LENGTH_DELIMITED) return FRAME_ERR_INVALID_TAG;
    *data = f->value.bytes.ptr;
    *len = f->value.bytes.len;
    return FRAME_OK;
}

#ifdef __cplusplus
}
#endif

#endif /* FRAME_CODEC_H */
