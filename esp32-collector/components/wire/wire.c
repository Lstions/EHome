/**
 * @file wire.c
 * @brief 帧定界/重组实现（零拷贝：frame_out 指向内部缓冲）
 */
#include "wire.h"

#include <stdlib.h>
#include <string.h>

#define VARINT32_MAX_BYTES 5u

struct reassembler {
    uint32_t max_msg_bytes;
    size_t   cap;    /* max_msg_bytes + VARINT32_MAX_BYTES */
    size_t   start;  /* 未解析数据的起点（惰性压缩：避免破坏刚返回的帧） */
    size_t   len;    /* 有效字节数；数据位于 buf[start, len) */
    uint8_t *buf;
};

static const char *const s_names[] = {
    [REASM_NEED_MORE]          = "NEED_MORE",
    [REASM_FRAME_READY]        = "FRAME_READY",
    [REASM_ERROR_TOO_LARGE]    = "ERROR_TOO_LARGE",
    [REASM_ERROR_MALFORMED]    = "ERROR_MALFORMED",
    [REASM_ERROR_INTERNAL]     = "ERROR_INTERNAL",
};

const char *reasm_result_name(reasm_result_t r)
{
    if ((int)r < 0 || r > (int)REASM_ERROR_INTERNAL) return "UNKNOWN";
    return s_names[r];
}

bool wire_encode_len_prefix(uint8_t *out, size_t cap, size_t *used, uint32_t payload_len)
{
    if (out == NULL || used == NULL) return false;
    size_t i = 0;
    uint32_t v = payload_len;
    do {
        if (i >= cap) return false;            /* 不截断 */
        uint8_t b = (uint8_t)(v & 0x7Fu);
        v >>= 7;
        if (v != 0) b |= 0x80u;
        out[i++] = b;
    } while (v != 0);
    *used = i;
    return true;
}

/* 解析 LEB128 varint（最多 5 字节，32 位）。
 * 返回：0=完整(消耗 *used)；1=需要更多；-1=非法 */
static int parse_varint32(const uint8_t *in, size_t n, size_t *used, uint32_t *v)
{
    uint32_t result = 0;
    for (size_t i = 0; i < VARINT32_MAX_BYTES; i++) {
        if (i >= n) return 1;                       /* 需要更多 */
        uint8_t b = in[i];
        if (i == VARINT32_MAX_BYTES - 1 && (b & 0x80u) != 0) return -1; /* 溢出 */
        if (i == VARINT32_MAX_BYTES - 1 && (b & 0xF0u) != 0) return -1; /* 32 位放不下 */
        result |= (uint32_t)(b & 0x7Fu) << (7 * i);
        if ((b & 0x80u) == 0) {
            *used = i + 1;
            *v = result;
            return 0;
        }
    }
    return -1;
}

reassembler_t *reasm_create(uint32_t max_msg_bytes)
{
    if (max_msg_bytes == 0) return NULL;   /* 0 上界无意义：拒绝而不是兜底 */
    reassembler_t *r = (reassembler_t *)calloc(1, sizeof(*r));
    if (r == NULL) return NULL;
    r->max_msg_bytes = max_msg_bytes;
    r->cap = (size_t)max_msg_bytes + VARINT32_MAX_BYTES;
    r->buf = (uint8_t *)malloc(r->cap);
    if (r->buf == NULL) { free(r); return NULL; }
    r->len = 0;
    return r;
}

void reasm_destroy(reassembler_t *r)
{
    if (r == NULL) return;
    free(r->buf);
    free(r);
}

static void reasm_reset(reassembler_t *r) { r->len = 0; r->start = 0; }

/* 惰性压缩：只在【需要空间】时把未解析数据前移。
 * 为什么不能"一产出帧就压缩"：frame_out 指向内部缓冲，
 * 立刻 memmove 会把【刚返回给调用方的那一帧】覆盖掉。
 * 契约是"frame_out 在下次 push 前有效"，所以压缩只能发生在 push 内部，
 * 且必须发生在调用方已经用完上一帧之后 —— 即本次 push 的开头。 */
static void reasm_compact(reassembler_t *r)
{
    if (r->start == 0) return;
    size_t pending = r->len - r->start;
    if (pending > 0) memmove(r->buf, r->buf + r->start, pending);
    r->len = pending;
    r->start = 0;
}

reasm_result_t reasm_push(reassembler_t *r, const uint8_t *in, size_t n,
                          const uint8_t **frame_out, size_t *frame_len_out)
{
    if (frame_out != NULL) *frame_out = NULL;
    if (frame_len_out != NULL) *frame_len_out = 0;
    if (r == NULL) return REASM_ERROR_INTERNAL;

    /* 上一帧已经被调用方用完了（契约：frame_out 只在下一次 push 前有效），
     * 现在可以安全地把未解析数据前移。 */
    reasm_compact(r);

    if (in != NULL && n > 0) {
        /* 缓冲装不下 -> 拒绝，不静默丢弃、不部分追加。
         * 正常路径不会走到：合法帧的长度前缀一定 <= max_msg_bytes。 */
        if (r->len + n > r->cap) {
            reasm_reset(r);
            return REASM_ERROR_TOO_LARGE;
        }
        memcpy(r->buf + r->len, in, n);
        r->len += n;
    }
    /* 注意：n == 0 是【合法】调用 —— 用于把缓冲里已经完整的后续帧吐出来
     * （一次 push 最多产出一帧，所以多帧需要用 n==0 继续取）。 */

    size_t hdr = 0;
    uint32_t payload_len = 0;
    int pr = parse_varint32(r->buf + r->start, r->len - r->start, &hdr, &payload_len);

    if (pr == 1) return REASM_NEED_MORE;           /* 长度前缀还没收全 */
    if (pr < 0) { reasm_reset(r); return REASM_ERROR_MALFORMED; }

    /* 已知长度：超限立刻拒绝并复位，不缓冲整个超限消息。 */
    if (payload_len > r->max_msg_bytes) {
        reasm_reset(r);
        return REASM_ERROR_TOO_LARGE;
    }

    if (r->len - r->start < hdr + (size_t)payload_len) return REASM_NEED_MORE;

    /* 不 memmove：把帧留在原地，只推进 start（惰性压缩见上）。 */
    if (frame_out != NULL) *frame_out = r->buf + r->start + hdr;
    if (frame_len_out != NULL) *frame_len_out = payload_len;
    r->start += hdr + (size_t)payload_len;

    return REASM_FRAME_READY;
}
