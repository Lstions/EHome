#include "data_report_codec.h"

frame_err_t data_report_encode(uint8_t *buf, size_t capacity, size_t *out_len,
                               uint32_t channel_id, uint64_t timestamp_us,
                               uint32_t sequence, const uint8_t *raw_data, size_t raw_len,
                               uint32_t error_code, uint32_t request_id,
                               uint32_t edge_device_id, uint32_t command_template_id,
                               uint8_t command_index)
{
    if (buf == NULL || out_len == NULL || (raw_data == NULL && raw_len != 0)) {
        return FRAME_ERR_INVALID_TAG;
    }

    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, capacity, MSG_DATA_RPT);

    frame_err_t err;
    if ((err = frame_encode_varint(&enc, 1, channel_id)) != FRAME_OK ||
        (err = frame_encode_varint(&enc, 2, timestamp_us)) != FRAME_OK ||
        (err = frame_encode_varint(&enc, 3, sequence)) != FRAME_OK) {
        return err;
    }
    if (raw_len > 0 && (err = frame_encode_bytes(&enc, 4, raw_data, raw_len)) != FRAME_OK) {
        return err;
    }
    if (error_code != 0 && (err = frame_encode_varint(&enc, 5, error_code)) != FRAME_OK) {
        return err;
    }
    if (request_id != 0 && (err = frame_encode_varint(&enc, 6, request_id)) != FRAME_OK) {
        return err;
    }
    if (edge_device_id != 0 && (err = frame_encode_varint(&enc, 7, edge_device_id)) != FRAME_OK) {
        return err;
    }
    if ((command_index > 0 || edge_device_id != 0) &&
        (err = frame_encode_varint(&enc, 8, command_index)) != FRAME_OK) {
        return err;
    }
    if (command_template_id != 0 &&
        (err = frame_encode_varint(&enc, 9, command_template_id)) != FRAME_OK) {
        return err;
    }

    *out_len = frame_encoder_size(&enc);
    return FRAME_OK;
}
