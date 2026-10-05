#include "data_batch_codec.h"
#include <string.h>

/* ------------------------------------------------------------------ *
 *  内部：精确尺寸计算 + 直写式编码
 *
 *  刻意**不用** frame_encode_varint()/frame_encode_bytes() 来写入或算尺寸：
 *  frame_encode_varint() 为了容纳最大 10 字节 varint，会保守地要求
 *  tag+10 字节的空闲空间，即使实际只写 1 字节。若调用方的 capacity 恰好
 *  等于精确帧长（契约 §2.2 的预算检查就是这么算的），最后一次编码会返回
 *  假的 FRAME_ERR_OVERFLOW。本文件所有写入都走"先算精确长度、再用
 *  frame_encode_varint_to_buf 直写"的路径，使
 *  "capacity >= data_batch_encoded_size(...)" 成为**充分条件**。
 *
 *  另一个刻意之处：sample 子消息**不**先编进临时缓冲再拷贝，而是把 field5
 *  的 tag/length 先写出、随后把 delta 与 raw_data 直接追加进主缓冲。
 *  raw_data 上限 1024 B，任何固定大小的栈上子缓冲都会在 n=4 时溢出
 *  （4 x 1024 = 4 KiB）—— 而 report_tx 的栈只有 4096 B。直写法零额外拷贝、
 *  零额外栈。
 * ------------------------------------------------------------------ */

static size_t tag_size(uint8_t field_num, uint8_t wire_type)
{
    return frame_varint_size(((uint64_t)field_num << 3) | wire_type);
}

static size_t varint_field_size(uint8_t field_num, uint64_t value)
{
    return tag_size(field_num, WIRE_VARINT) + frame_varint_size(value);
}

static size_t bytes_field_size(uint8_t field_num, size_t len)
{
    return tag_size(field_num, WIRE_LENGTH_DELIMITED) +
           frame_varint_size((uint64_t)len) + len;
}

/* 一个 sample 子消息的确切长度（不含外层 field 5 的 tag/length）。 */
static size_t sample_body_size(const data_batch_sample_t *s)
{
    return varint_field_size(DATA_BATCH_SAMPLE_F_DELTA, s->delta_us) +
           bytes_field_size(DATA_BATCH_SAMPLE_F_RAW_DATA, s->raw_len);
}

/* 追加 tag + varint 值。 */
static bool append_varint(frame_encoder_t *enc, uint8_t field_num, uint64_t value)
{
    size_t need = varint_field_size(field_num, value);
    if (enc->pos + need > enc->capacity) return false;
    enc->pos += frame_encode_varint_to_buf(&enc->buf[enc->pos],
                                           ((uint64_t)field_num << 3) | WIRE_VARINT);
    enc->pos += frame_encode_varint_to_buf(&enc->buf[enc->pos], value);
    return true;
}

/* 追加 tag + length 前缀（不含数据本身）。 */
static bool append_bytes_header(frame_encoder_t *enc, uint8_t field_num, size_t len)
{
    size_t need = tag_size(field_num, WIRE_LENGTH_DELIMITED) +
                  frame_varint_size((uint64_t)len);
    if (enc->pos + need > enc->capacity) return false;
    enc->pos += frame_encode_varint_to_buf(&enc->buf[enc->pos],
                                           ((uint64_t)field_num << 3) | WIRE_LENGTH_DELIMITED);
    enc->pos += frame_encode_varint_to_buf(&enc->buf[enc->pos], (uint64_t)len);
    return true;
}

static bool append_bytes(frame_encoder_t *enc, uint8_t field_num,
                         const uint8_t *data, size_t len)
{
    if (!append_bytes_header(enc, field_num, len)) return false;
    if (enc->pos + len > enc->capacity) return false;
    if (len > 0) {
        memcpy(&enc->buf[enc->pos], data, len);
        enc->pos += len;
    }
    return true;
}

size_t data_batch_encoded_size(uint32_t channel_id, uint64_t base_timestamp_us,
                               uint32_t first_sequence,
                               const data_batch_sample_t *samples, size_t count,
                               uint32_t edge_device_id,
                               uint32_t command_template_id,
                               uint8_t command_index)
{
    if (samples == NULL || count == 0 || count > DATA_BATCH_MAX_SAMPLES) return 0;

    size_t total = 1; /* 顶层类型字节 MSG_DATA_BATCH */
    total += varint_field_size(DATA_BATCH_F_COUNT, count);
    total += varint_field_size(DATA_BATCH_F_BASE_TIMESTAMP, base_timestamp_us);
    total += varint_field_size(DATA_BATCH_F_FIRST_SEQUENCE, first_sequence);
    total += varint_field_size(DATA_BATCH_F_CHANNEL_ID, channel_id);
    for (size_t i = 0; i < count; i++) {
        total += bytes_field_size(DATA_BATCH_F_SAMPLE, sample_body_size(&samples[i]));
    }
    /* 可选字段与 0x03 同款条件：0 即省略，让 n=1 的最小帧真正最小。 */
    if (edge_device_id != 0) {
        total += varint_field_size(DATA_BATCH_F_EDGE_DEVICE_ID, edge_device_id);
    }
    if (command_template_id != 0) {
        total += varint_field_size(DATA_BATCH_F_COMMAND_TEMPLATE, command_template_id);
    }
    if (command_index > 0 || edge_device_id != 0) {
        total += varint_field_size(DATA_BATCH_F_COMMAND_INDEX, command_index);
    }
    return total;
}

/* 契约 §2.1.1/2.1.3/2.1.4/2.1.5：编码前把不变量查一遍，违反就**不产出字节**。 */
static frame_err_t validate_samples(const data_batch_sample_t *samples, size_t count)
{
    if (samples == NULL || count == 0 || count > DATA_BATCH_MAX_SAMPLES) {
        return FRAME_ERR_INVALID_TAG;
    }
    uint64_t prev_delta = 0;
    for (size_t i = 0; i < count; i++) {
        const data_batch_sample_t *s = &samples[i];
        /* 空 raw_data 拒绝；超 1024 B 拒绝（契约 §2.1.5）。 */
        if (s->raw_data == NULL || s->raw_len == 0 || s->raw_len > DATA_BATCH_MAX_RAW) {
            return FRAME_ERR_INVALID_TAG;
        }
        /* 首样本 delta 必须为 0；其余必须 > 0 且累计不递减（契约 §2.1.4）。 */
        if (i == 0) {
            if (s->delta_us != 0) return FRAME_ERR_INVALID_TAG;
        } else if (s->delta_us == 0 || s->delta_us < prev_delta) {
            return FRAME_ERR_INVALID_TAG;
        }
        prev_delta = s->delta_us;
    }
    return FRAME_OK;
}

frame_err_t data_batch_encode(uint8_t *buf, size_t capacity, size_t *out_len,
                              uint32_t channel_id, uint64_t base_timestamp_us,
                              uint32_t first_sequence,
                              const data_batch_sample_t *samples, size_t count,
                              uint32_t edge_device_id,
                              uint32_t command_template_id,
                              uint8_t command_index)
{
    if (buf == NULL || out_len == NULL) return FRAME_ERR_INVALID_TAG;

    frame_err_t verr = validate_samples(samples, count);
    if (verr != FRAME_OK) return verr;

    size_t need = data_batch_encoded_size(channel_id, base_timestamp_us, first_sequence,
                                          samples, count, edge_device_id,
                                          command_template_id, command_index);
    if (need == 0) return FRAME_ERR_INVALID_TAG;
    if (capacity < need) return FRAME_ERR_OVERFLOW;

    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, capacity, MSG_DATA_BATCH);

    bool ok = append_varint(&enc, DATA_BATCH_F_COUNT, count) &&
              append_varint(&enc, DATA_BATCH_F_BASE_TIMESTAMP, base_timestamp_us) &&
              append_varint(&enc, DATA_BATCH_F_FIRST_SEQUENCE, first_sequence) &&
              append_varint(&enc, DATA_BATCH_F_CHANNEL_ID, channel_id);

    for (size_t i = 0; ok && i < count; i++) {
        size_t body_len = sample_body_size(&samples[i]);
        ok = append_bytes_header(&enc, DATA_BATCH_F_SAMPLE, body_len) &&
             append_varint(&enc, DATA_BATCH_SAMPLE_F_DELTA, samples[i].delta_us) &&
             append_bytes(&enc, DATA_BATCH_SAMPLE_F_RAW_DATA,
                          samples[i].raw_data, samples[i].raw_len);
    }
    if (ok && edge_device_id != 0) {
        ok = append_varint(&enc, DATA_BATCH_F_EDGE_DEVICE_ID, edge_device_id);
    }
    if (ok && command_template_id != 0) {
        ok = append_varint(&enc, DATA_BATCH_F_COMMAND_TEMPLATE, command_template_id);
    }
    if (ok && (command_index > 0 || edge_device_id != 0)) {
        ok = append_varint(&enc, DATA_BATCH_F_COMMAND_INDEX, command_index);
    }
    if (!ok) return FRAME_ERR_OVERFLOW;
    if (enc.pos != need) return FRAME_ERR_OVERFLOW; /* 尺寸预言与实写必须一致 */

    *out_len = enc.pos;
    return FRAME_OK;
}

/* ------------------------------------------------------------------ *
 *  严格解码：逐条强制契约 §2.1 不变量。
 *
 *  后端 handler_data_batch.go 与宿主测试共用同一组判据，保证"固件发的
 *  每一帧，这份解码器都能原样读回"，也保证负例（count 不符 / 首样本非 0 /
 *  delta 非单调 / 空 raw / 超 1024B / 重复字段）确实被拒。
 * ------------------------------------------------------------------ */

typedef struct {
    bool     seen;
    uint64_t value;
} seen_varint_t;

frame_err_t data_batch_decode(const uint8_t *buf, size_t len,
                              data_batch_decoded_t *out)
{
    if (buf == NULL || out == NULL) return FRAME_ERR_INVALID_TAG;
    if (len < 1 || buf[0] != MSG_DATA_BATCH) return FRAME_ERR_INVALID_TAG;
    memset(out, 0, sizeof(*out));

    frame_decoder_t dec;
    frame_err_t err = frame_decoder_init(&dec, buf, len);
    if (err != FRAME_OK) return err;

    seen_varint_t count = {false, 0}, base_ts = {false, 0};
    seen_varint_t first_seq = {false, 0}, channel = {false, 0};
    seen_varint_t edge = {false, 0}, tmpl = {false, 0}, cmd_idx = {false, 0};
    size_t samples_seen = 0;

    frame_field_t field;
    while ((err = frame_decoder_next(&dec, &field)) == FRAME_OK) {
        switch (field.field_num) {
        case DATA_BATCH_F_COUNT:
            if (count.seen || field.wire_type != WIRE_VARINT) return FRAME_ERR_INVALID_TAG;
            count.seen = true;
            count.value = field.value.varint;
            break;
        case DATA_BATCH_F_BASE_TIMESTAMP:
            if (base_ts.seen || field.wire_type != WIRE_VARINT) return FRAME_ERR_INVALID_TAG;
            base_ts.seen = true;
            base_ts.value = field.value.varint;
            break;
        case DATA_BATCH_F_FIRST_SEQUENCE:
            if (first_seq.seen || field.wire_type != WIRE_VARINT) return FRAME_ERR_INVALID_TAG;
            first_seq.seen = true;
            first_seq.value = field.value.varint;
            break;
        case DATA_BATCH_F_CHANNEL_ID:
            if (channel.seen || field.wire_type != WIRE_VARINT) return FRAME_ERR_INVALID_TAG;
            channel.seen = true;
            channel.value = field.value.varint;
            break;
        case DATA_BATCH_F_EDGE_DEVICE_ID:
            if (edge.seen || field.wire_type != WIRE_VARINT) return FRAME_ERR_INVALID_TAG;
            edge.seen = true;
            edge.value = field.value.varint;
            break;
        case DATA_BATCH_F_COMMAND_TEMPLATE:
            if (tmpl.seen || field.wire_type != WIRE_VARINT) return FRAME_ERR_INVALID_TAG;
            tmpl.seen = true;
            tmpl.value = field.value.varint;
            break;
        case DATA_BATCH_F_COMMAND_INDEX:
            if (cmd_idx.seen || field.wire_type != WIRE_VARINT) return FRAME_ERR_INVALID_TAG;
            cmd_idx.seen = true;
            cmd_idx.value = field.value.varint;
            break;
        case DATA_BATCH_F_SAMPLE: {
            if (field.wire_type != WIRE_LENGTH_DELIMITED) return FRAME_ERR_INVALID_TAG;
            if (samples_seen >= DATA_BATCH_MAX_SAMPLES) return FRAME_ERR_INVALID_TAG;

            frame_decoder_t sub;
            if (frame_decoder_init_sub(&sub, field.value.bytes.ptr,
                                       field.value.bytes.len) != FRAME_OK) {
                return FRAME_ERR_INVALID_TAG;
            }
            data_batch_decoded_sample_t *dst = &out->samples[samples_seen];
            seen_varint_t delta = {false, 0};
            bool have_raw = false;
            frame_field_t sf;
            frame_err_t serr;
            while ((serr = frame_decoder_next(&sub, &sf)) == FRAME_OK) {
                if (sf.field_num == DATA_BATCH_SAMPLE_F_DELTA) {
                    if (delta.seen || sf.wire_type != WIRE_VARINT) return FRAME_ERR_INVALID_TAG;
                    delta.seen = true;
                    delta.value = sf.value.varint;
                } else if (sf.field_num == DATA_BATCH_SAMPLE_F_RAW_DATA) {
                    if (have_raw || sf.wire_type != WIRE_LENGTH_DELIMITED) {
                        return FRAME_ERR_INVALID_TAG;
                    }
                    /* 契约 §2.1.5：空 raw_data 拒绝、>1024 B 拒绝。 */
                    if (sf.value.bytes.len == 0 ||
                        sf.value.bytes.len > DATA_BATCH_MAX_RAW) {
                        return FRAME_ERR_INVALID_TAG;
                    }
                    have_raw = true;
                    dst->raw_data = sf.value.bytes.ptr;
                    dst->raw_len = sf.value.bytes.len;
                }
                /* 其它字段号：跳过（前向兼容，契约 §2.1.7）。 */
            }
            if (serr != FRAME_DONE) return FRAME_ERR_INVALID_TAG;
            if (!delta.seen || !have_raw) return FRAME_ERR_INVALID_TAG;

            /* 契约 §2.1.4：首样本 delta==0，其余 >0 且单调不递减。 */
            if (samples_seen == 0) {
                if (delta.value != 0) return FRAME_ERR_INVALID_TAG;
            } else {
                if (delta.value == 0) return FRAME_ERR_INVALID_TAG;
                if (delta.value < out->samples[samples_seen - 1].delta_us) {
                    return FRAME_ERR_INVALID_TAG;
                }
            }
            dst->delta_us = delta.value;
            samples_seen++;
            break;
        }
        default:
            /* 未知 field 号：跳过（前向兼容，契约 §2.1.7）。 */
            break;
        }
    }
    if (err != FRAME_DONE) return FRAME_ERR_INVALID_TAG;

    /* 契约 §2.1.2/2.1.3：必填字段齐全、count ∈ 1..4、且等于 field5 实际次数。 */
    if (!count.seen || !base_ts.seen || !first_seq.seen || !channel.seen) {
        return FRAME_ERR_INVALID_TAG;
    }
    if (count.value < 1 || count.value > DATA_BATCH_MAX_SAMPLES) {
        return FRAME_ERR_INVALID_TAG;
    }
    if (samples_seen != (size_t)count.value) return FRAME_ERR_INVALID_TAG;

    out->count = samples_seen;
    out->base_timestamp_us = base_ts.value;
    out->first_sequence = (uint32_t)first_seq.value;
    out->channel_id = (uint32_t)channel.value;
    out->edge_device_id = edge.seen ? (uint32_t)edge.value : 0;
    out->command_template_id = tmpl.seen ? (uint32_t)tmpl.value : 0;
    out->command_index = cmd_idx.seen ? (uint8_t)cmd_idx.value : 0;
    return FRAME_OK;
}
