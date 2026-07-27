/**
 * @file frame_codec.c
 * @brief Binary Frame Protocol - ESP32 Encoder/Decoder Implementation
 */

#include "frame_codec.h"
#include <string.h>

/* === Encoder === */

void frame_encoder_init(frame_encoder_t *enc, uint8_t *buf, size_t cap, uint8_t msg_type)
{
    enc->buf = buf;
    enc->pos = 0;
    enc->capacity = cap;
    if (cap > 0) {
        enc->buf[enc->pos++] = msg_type;
    }
}

void frame_encoder_init_sub(frame_encoder_t *enc, uint8_t *buf, size_t cap)
{
    enc->buf = buf;
    enc->pos = 0;
    enc->capacity = cap;
}

static frame_err_t encoder_ensure_space(frame_encoder_t *enc, size_t needed)
{
    if (enc->pos + needed > enc->capacity) {
        return FRAME_ERR_OVERFLOW;
    }
    return FRAME_OK;
}

frame_err_t frame_encode_varint(frame_encoder_t *enc, uint8_t field_num, uint64_t value)
{
    uint64_t tag = ((uint64_t)field_num << 3) | WIRE_VARINT;
    size_t tag_size = frame_varint_size(tag);
    frame_err_t err = encoder_ensure_space(enc, tag_size + 10); /* tag + max varint */
    if (err != FRAME_OK) return err;

    enc->pos += frame_encode_varint_to_buf(&enc->buf[enc->pos], tag);

    /* varint value */
    while (value > 0x7F) {
        enc->buf[enc->pos++] = (value & 0x7F) | 0x80;
        value >>= 7;
    }
    enc->buf[enc->pos++] = value & 0x7F;

    return FRAME_OK;
}

frame_err_t frame_encode_string(frame_encoder_t *enc, uint8_t field_num, const char *str)
{
    if (str == NULL) str = "";  /* NULL guard: encode as empty string */
    size_t len = strlen(str);
    uint64_t tag = ((uint64_t)field_num << 3) | WIRE_LENGTH_DELIMITED;
    size_t tag_size = frame_varint_size(tag);
    frame_err_t err = encoder_ensure_space(enc, tag_size + 5 + len); /* tag + varint(len) + data */
    if (err != FRAME_OK) return err;

    enc->pos += frame_encode_varint_to_buf(&enc->buf[enc->pos], tag);

    /* length as varint */
    size_t len_size = frame_encode_varint_to_buf(&enc->buf[enc->pos], len);
    enc->pos += len_size;

    /* data */
    memcpy(&enc->buf[enc->pos], str, len);
    enc->pos += len;

    return FRAME_OK;
}

frame_err_t frame_encode_bytes(frame_encoder_t *enc, uint8_t field_num, const uint8_t *data, size_t len)
{
    uint64_t tag = ((uint64_t)field_num << 3) | WIRE_LENGTH_DELIMITED;
    size_t tag_size = frame_varint_size(tag);
    frame_err_t err = encoder_ensure_space(enc, tag_size + 5 + len);
    if (err != FRAME_OK) return err;

    enc->pos += frame_encode_varint_to_buf(&enc->buf[enc->pos], tag);

    size_t len_size = frame_encode_varint_to_buf(&enc->buf[enc->pos], len);
    enc->pos += len_size;

    memcpy(&enc->buf[enc->pos], data, len);
    enc->pos += len;

    return FRAME_OK;
}

frame_err_t frame_encoder_append_raw(frame_encoder_t *enc, const uint8_t *data, size_t len)
{
    frame_err_t err = encoder_ensure_space(enc, len);
    if (err != FRAME_OK) return err;
    memcpy(&enc->buf[enc->pos], data, len);
    enc->pos += len;
    return FRAME_OK;
}

/* === Decoder === */

frame_err_t frame_decoder_init(frame_decoder_t *dec, const uint8_t *buf, size_t len)
{
    if (len < 1) return FRAME_ERR_UNDERFLOW;
    dec->buf = buf;
    dec->pos = 1; /* skip type byte for top-level messages */
    dec->len = len;
    return FRAME_OK;
}

frame_err_t frame_decoder_init_sub(frame_decoder_t *dec, const uint8_t *buf, size_t len)
{
    dec->buf = buf;
    dec->pos = 0; /* sub-messages have no type byte */
    dec->len = len;
    return FRAME_OK;
}

static frame_err_t decoder_ensure_bytes(frame_decoder_t *dec, size_t needed)
{
    if (dec->pos + needed > dec->len) {
        return FRAME_ERR_UNDERFLOW;
    }
    return FRAME_OK;
}

static frame_err_t decoder_read_varint(frame_decoder_t *dec, uint64_t *out)
{
    uint64_t result = 0;
    int shift = 0;
    size_t start = dec->pos;

    while (1) {
        frame_err_t err = decoder_ensure_bytes(dec, 1);
        if (err != FRAME_OK) return err;

        uint8_t byte = dec->buf[dec->pos++];
        if (shift == 63 && byte > 1) {
            return FRAME_ERR_INVALID_TAG;
        }
        result |= (uint64_t)(byte & 0x7F) << shift;

        if (!(byte & 0x80)) {
            if (dec->pos - start > 1 && byte == 0) {
                return FRAME_ERR_INVALID_TAG;
            }
            break;
        }

        shift += 7;
        if (shift >= 64) {
            return FRAME_ERR_INVALID_TAG;
        }
    }

    *out = result;
    return FRAME_OK;
}

frame_err_t frame_decoder_next(frame_decoder_t *dec, frame_field_t *field)
{
    if (dec->pos >= dec->len) {
        return FRAME_DONE;
    }

    /* read tag */
    uint64_t tag;
    frame_err_t err = decoder_read_varint(dec, &tag);
    if (err != FRAME_OK) return err;

    uint64_t raw_field_num = tag >> 3;
    if (raw_field_num == 0 || raw_field_num > UINT8_MAX) return FRAME_ERR_INVALID_TAG;
    field->field_num = (uint8_t)raw_field_num;
    field->wire_type = (uint8_t)(tag & 0x07);

    switch (field->wire_type) {
        case WIRE_VARINT: {
            uint64_t value;
            err = decoder_read_varint(dec, &value);
            if (err != FRAME_OK) return err;
            field->value.varint = value;
            break;
        }
        case WIRE_LENGTH_DELIMITED: {
            uint64_t len;
            err = decoder_read_varint(dec, &len);
            if (err != FRAME_OK) return err;

            if (len > SIZE_MAX) return FRAME_ERR_OVERFLOW;

            err = decoder_ensure_bytes(dec, (size_t)len);
            if (err != FRAME_OK) return err;

            field->value.bytes.ptr = &dec->buf[dec->pos];
            field->value.bytes.len = (size_t)len;
            dec->pos += (size_t)len;
            break;
        }
        default:
            return FRAME_ERR_INVALID_TAG;
    }

    return FRAME_OK;
}
