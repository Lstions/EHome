#ifndef DATA_REPORT_CODEC_H
#define DATA_REPORT_CODEC_H

#include <stddef.h>
#include <stdint.h>
#include "frame_codec.h"

frame_err_t data_report_encode(uint8_t *buf, size_t capacity, size_t *out_len,
                               uint32_t channel_id, uint64_t timestamp_us,
                               uint32_t sequence, const uint8_t *raw_data, size_t raw_len,
                               uint32_t error_code, uint32_t request_id,
                               uint32_t edge_device_id, uint32_t command_template_id,
                               uint8_t command_index);

#endif /* DATA_REPORT_CODEC_H */
