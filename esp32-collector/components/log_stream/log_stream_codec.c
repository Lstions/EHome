#include "log_stream_codec.h"

frame_err_t log_stream_encode(uint8_t *buf, size_t capacity, size_t *out_len,
                              uint16_t sequence, const log_stream_entry_t *entries,
                              size_t entry_count)
{
    if (buf == NULL || out_len == NULL || (entries == NULL && entry_count != 0)) {
        return FRAME_ERR_INVALID_TAG;
    }

    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, capacity, MSG_LOG_STREAM);

    frame_err_t err;
    if ((err = frame_encode_varint(&enc, 1, entry_count)) != FRAME_OK ||
        (err = frame_encode_varint(&enc, 2, sequence)) != FRAME_OK) {
        return err;
    }

    for (size_t i = 0; i < entry_count; ++i) {
        if (entries[i].tag == NULL || entries[i].message == NULL) {
            return FRAME_ERR_INVALID_TAG;
        }

        uint8_t sub_buf[224];
        frame_encoder_t sub = {
            .buf = sub_buf,
            .pos = 0,
            .capacity = sizeof(sub_buf),
        };
        if ((err = frame_encode_varint(&sub, 1, entries[i].level)) != FRAME_OK ||
            (err = frame_encode_varint(&sub, 2, entries[i].timestamp_us)) != FRAME_OK ||
            (err = frame_encode_string(&sub, 3, entries[i].tag)) != FRAME_OK ||
            (err = frame_encode_string(&sub, 4, entries[i].message)) != FRAME_OK ||
            (err = frame_encode_bytes(&enc, 3, sub_buf, frame_encoder_size(&sub))) != FRAME_OK) {
            return err;
        }
    }

    *out_len = frame_encoder_size(&enc);
    return FRAME_OK;
}
