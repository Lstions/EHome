#ifndef LOG_STREAM_CODEC_H
#define LOG_STREAM_CODEC_H

#include <stddef.h>
#include <stdint.h>
#include "frame_codec.h"

typedef struct {
    uint8_t level;
    uint64_t timestamp_us;
    const char *tag;
    const char *message;
} log_stream_entry_t;

frame_err_t log_stream_encode(uint8_t *buf, size_t capacity, size_t *out_len,
                              uint16_t sequence, const log_stream_entry_t *entries,
                              size_t entry_count);

#endif /* LOG_STREAM_CODEC_H */
