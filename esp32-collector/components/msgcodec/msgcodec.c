/**
 * @file msgcodec.c
 * @brief 编解码原语实现（纯函数；任何容量不足都【不截断】）
 */
#include "msgcodec.h"

#include <string.h>

#define VARINT64_MAX_BYTES 10u

static const char *const s_enc_names[] = {
    [ENC_OK] = "OK", [ENC_NO_SPACE] = "NO_SPACE", [ENC_BAD_ARG] = "BAD_ARG",
};
static const char *const s_dec_names[] = {
    [DEC_OK] = "OK", [DEC_TRUNCATED] = "TRUNCATED", [DEC_BAD_WIRE] = "BAD_WIRE",
};

const char *enc_result_name(enc_result_t r)
{
    if ((int)r < 0 || r > (int)ENC_BAD_ARG) return "UNKNOWN";
    return s_enc_names[r];
}
const char *dec_result_name(dec_result_t r)
{
    if ((int)r < 0 || r > (int)DEC_BAD_WIRE) return "UNKNOWN";
    return s_dec_names[r];
}

enc_result_t enc_varint(uint8_t *out, size_t cap, size_t *used, uint64_t v)
{
    if (out == NULL || used == NULL) return ENC_BAD_ARG;
    size_t i = 0;
    do {
        if (i >= cap) return ENC_NO_SPACE;    /* 不写半个 varint */
        uint8_t b = (uint8_t)(v & 0x7Fu);
        v >>= 7;
        if (v != 0) b |= 0x80u;
        out[i++] = b;
    } while (v != 0);
    *used = i;
    return ENC_OK;
}

dec_result_t dec_varint(const uint8_t *in, size_t n, size_t *used, uint64_t *v)
{
    if (in == NULL || used == NULL || v == NULL) return DEC_BAD_WIRE;
    uint64_t result = 0;
    for (size_t i = 0; i < VARINT64_MAX_BYTES; i++) {
        if (i >= n) return DEC_TRUNCATED;
        uint8_t b = in[i];
        /* 第 10 字节只能贡献低 1 位，否则溢出 64 位 */
        if (i == VARINT64_MAX_BYTES - 1 && (b & 0x7Eu) != 0) return DEC_BAD_WIRE;
        result |= (uint64_t)(b & 0x7Fu) << (7 * i);
        if ((b & 0x80u) == 0) {
            *used = i + 1;
            *v = result;
            return DEC_OK;
        }
    }
    return DEC_BAD_WIRE;   /* 超长 varint */
}

/* tag = (field_id << 3) | wire_type —— 与生产 frame_codec.c:38 和后端
 * frame.go:253 完全一致。字段号 0 非法（tag 会退化成 0，无法与"无字段"区分）。 */
static enc_result_t enc_tag(uint8_t *out, size_t cap, size_t *used,
                            uint8_t field_id, uint8_t wire_type)
{
    uint64_t tag = ((uint64_t)field_id << 3) | (uint64_t)(wire_type & 0x07u);
    return enc_varint(out, cap, used, tag);
}

enc_result_t enc_field_u64(uint8_t *out, size_t cap, size_t *used,
                           uint8_t field_id, uint64_t v)
{
    if (out == NULL || used == NULL || field_id == 0) return ENC_BAD_ARG;

    uint8_t tmp[VARINT64_MAX_BYTES];
    size_t vlen = 0;
    enc_result_t e = enc_varint(tmp, sizeof(tmp), &vlen, v);
    if (e != ENC_OK) return e;

    /* 总长 = tag 字节数 + 值 varint 字节数。
     * tag 最大 2 字节（字段号 ≤ 31 时；更大则更多）—— 用 6 字节上限够所有 u8 字段号。
     * 注意：这里【不】写"长度前缀"—— 那是本骨架初版的错误格式（见头文件说明）。 */
    uint8_t tag_buf[6];
    size_t tag_len = 0;
    e = enc_tag(tag_buf, sizeof(tag_buf), &tag_len, field_id, MSGCODEC_WIRE_VARINT);
    if (e != ENC_OK) return e;

    size_t total = tag_len + vlen;
    if (cap < total) return ENC_NO_SPACE;   /* 先算总长，绝不写一半 */

    memcpy(out, tag_buf, tag_len);
    memcpy(out + tag_len, tmp, vlen);
    *used = total;
    return ENC_OK;
}

enc_result_t enc_field_bytes(uint8_t *out, size_t cap, size_t *used,
                             uint8_t field_id, const void *p, size_t n)
{
    if (out == NULL || used == NULL || field_id == 0) return ENC_BAD_ARG;
    if (n > 0 && p == NULL) return ENC_BAD_ARG;

    uint8_t tag_buf[6];
    size_t tag_len = 0;
    enc_result_t e = enc_tag(tag_buf, sizeof(tag_buf), &tag_len, field_id,
                             MSGCODEC_WIRE_BYTES);
    if (e != ENC_OK) return e;

    uint8_t len_buf[VARINT64_MAX_BYTES];
    size_t len_len = 0;
    e = enc_varint(len_buf, sizeof(len_buf), &len_len, (uint64_t)n);
    if (e != ENC_OK) return e;

    size_t total = tag_len + len_len + n;
    if (cap < total) return ENC_NO_SPACE;   /* 不截断（P2） */

    size_t i = 0;
    memcpy(out + i, tag_buf, tag_len); i += tag_len;
    memcpy(out + i, len_buf, len_len); i += len_len;
    if (n > 0) { memcpy(out + i, p, n); i += n; }
    *used = i;
    return ENC_OK;
}

dec_result_t dec_next_field(const uint8_t *in, size_t n, size_t *cursor, field_view_t *out)
{
    if (in == NULL || cursor == NULL || out == NULL) return DEC_BAD_WIRE;
    size_t c = *cursor;
    if (c >= n) return DEC_TRUNCATED;

    /* tag = (field_id << 3) | wire_type —— protobuf 风格，与生产/后端一致。 */
    uint64_t tag = 0;
    size_t tag_len = 0;
    dec_result_t d = dec_varint(in + c, n - c, &tag_len, &tag);
    if (d != DEC_OK) return d;
    c += tag_len;

    uint8_t fid = (uint8_t)(tag >> 3);
    uint8_t wtype = (uint8_t)(tag & 0x07u);
    if (fid == 0) return DEC_BAD_WIRE;      /* 字段号 0 非法（与编码侧对称） */
    if (wtype != MSGCODEC_WIRE_VARINT && wtype != MSGCODEC_WIRE_BYTES) {
        return DEC_BAD_WIRE;                /* 不支持的 wire type：不"尽量解析" */
    }

    if (wtype == MSGCODEC_WIRE_VARINT) {
        /* 负载是 varint 本身；先解出来确认收全，再回填 */
        uint64_t v = 0;
        size_t vlen = 0;
        d = dec_varint(in + c, n - c, &vlen, &v);
        if (d != DEC_OK) return d;
        out->field_id = fid;
        out->wire_type = wtype;
        out->value = in + c;
        out->value_len = vlen;
        *cursor = c + vlen;
        return DEC_OK;
    }

    /* length-delimited */
    uint64_t vlen = 0;
    size_t hdr = 0;
    d = dec_varint(in + c, n - c, &hdr, &vlen);
    if (d != DEC_OK) return d;
    c += hdr;
    if (vlen > (uint64_t)(n - c)) return DEC_TRUNCATED;   /* 值还没收全 */
    out->field_id = fid;
    out->wire_type = wtype;
    out->value = in + c;
    out->value_len = (size_t)vlen;
    *cursor = c + (size_t)vlen;
    return DEC_OK;
}

dec_result_t dec_field_u64(const field_view_t *f, uint64_t *v)
{
    if (f == NULL || v == NULL) return DEC_BAD_WIRE;
    size_t used = 0;
    dec_result_t d = dec_varint(f->value, f->value_len, &used, v);
    if (d != DEC_OK) return d;
    /* 值必须【恰好】是一个 varint：多余字节说明编码方违约，不"尽量解析" */
    if (used != f->value_len) return DEC_BAD_WIRE;
    return DEC_OK;
}
