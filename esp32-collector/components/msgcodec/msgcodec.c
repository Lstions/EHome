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

enc_result_t enc_field_u64(uint8_t *out, size_t cap, size_t *used,
                           uint8_t field_id, uint64_t v)
{
    if (out == NULL || used == NULL || field_id == 0) return ENC_BAD_ARG;
    uint8_t tmp[VARINT64_MAX_BYTES];
    size_t vlen = 0;
    enc_result_t e = enc_varint(tmp, sizeof(tmp), &vlen, v);
    if (e != ENC_OK) return e;

    /* 总长 = field_id(1) + 长度前缀字节数 + 值字节数。
     * 长度前缀编码的是 vlen（0..10），因此【最多 1 字节】——
     * 早先误用 VARINT64_MAX_BYTES(10) 会让容量检查过度保守：
     * 明明放得下的调用被判成 NO_SPACE（由 msgcodec_tests 的边界用例发现）。 */
    uint8_t vlen_buf[2];
    size_t vlen_len = 0;
    if (enc_varint(vlen_buf, sizeof(vlen_buf), &vlen_len, (uint64_t)vlen) != ENC_OK) {
        return ENC_NO_SPACE;
    }
    size_t total = 1 + vlen_len + vlen;
    if (cap < total) return ENC_NO_SPACE;   /* 先算总长，绝不写一半 */

    size_t i = 0;
    out[i++] = field_id;
    size_t hdr = 0;
    e = enc_varint(out + i, cap - i, &hdr, (uint64_t)vlen);
    if (e != ENC_OK) return e;
    i += hdr;
    for (size_t k = 0; k < vlen; k++) out[i++] = tmp[k];
    *used = i;
    return ENC_OK;
}

enc_result_t enc_field_bytes(uint8_t *out, size_t cap, size_t *used,
                             uint8_t field_id, const void *p, size_t n)
{
    if (out == NULL || used == NULL || field_id == 0) return ENC_BAD_ARG;
    if (n > 0 && p == NULL) return ENC_BAD_ARG;

    uint8_t tmp[VARINT64_MAX_BYTES];
    size_t vlen = 0;
    enc_result_t e = enc_varint(tmp, sizeof(tmp), &vlen, (uint64_t)n);
    if (e != ENC_OK) return e;

    size_t total = 1 + vlen + n;
    if (cap < total) return ENC_NO_SPACE;   /* 不截断（P2） */

    size_t i = 0;
    out[i++] = field_id;
    for (size_t k = 0; k < vlen; k++) out[i++] = tmp[k];
    if (n > 0) memcpy(out + i, p, n);
    i += n;
    *used = i;
    return ENC_OK;
}

dec_result_t dec_next_field(const uint8_t *in, size_t n, size_t *cursor, field_view_t *out)
{
    if (in == NULL || cursor == NULL || out == NULL) return DEC_BAD_WIRE;
    size_t c = *cursor;
    if (c >= n) return DEC_TRUNCATED;
    uint8_t fid = in[c];
    if (fid == 0) return DEC_BAD_WIRE;      /* 字段号 0 非法（与编码侧对称） */
    c++;
    size_t hdr = 0;
    uint64_t vlen = 0;
    dec_result_t d = dec_varint(in + c, n - c, &hdr, &vlen);
    if (d != DEC_OK) return d;
    c += hdr;
    if (vlen > (uint64_t)(n - c)) return DEC_TRUNCATED;   /* 值还没收全 */
    out->field_id = fid;
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
