/**
 * @file wire.c
 * @brief 帧定界 / 分片重组实现（16 B 定长头，设计 §5.1）
 *
 * 零拷贝：MSG_READY 时 payload_out 指向重组器【内部】缓冲。
 * 惰性压缩：只在 push 开头前移未解析数据 —— 见 reasm_compact 注释。
 */
#include "wire.h"

#include <stdlib.h>
#include <string.h>

static const char *const s_wire_names[] = {
    [WIRE_OK]              = "OK",
    [WIRE_ERR_SHORT]       = "ERR_SHORT",
    [WIRE_ERR_MAGIC]       = "ERR_MAGIC",
    [WIRE_ERR_VERSION]     = "ERR_VERSION",
    [WIRE_ERR_RANGE]       = "ERR_RANGE",
    [WIRE_ERR_STRUCTURE]   = "ERR_STRUCTURE",
    [WIRE_ERR_BAD_ARG]     = "ERR_BAD_ARG",
};

const char *wire_result_name(wire_result_t r)
{
    if ((int)r < 0 || r > (int)WIRE_ERR_BAD_ARG) return "UNKNOWN";
    return s_wire_names[r];
}

static const char *const s_reasm_names[] = {
    [REASM_NEED_MORE]           = "NEED_MORE",
    [REASM_MSG_READY]           = "MSG_READY",
    [REASM_ERROR_TOO_LARGE]     = "ERROR_TOO_LARGE",
    [REASM_ERROR_MALFORMED]     = "ERROR_MALFORMED",
    [REASM_ERROR_OUT_OF_ORDER]  = "ERROR_OUT_OF_ORDER",
    [REASM_ERROR_CRC]           = "ERROR_CRC",
    [REASM_ERROR_INTERNAL]      = "ERROR_INTERNAL",
};

const char *reasm_result_name(reasm_result_t r)
{
    if ((int)r < 0 || r > (int)REASM_ERROR_INTERNAL) return "UNKNOWN";
    return s_reasm_names[r];
}

/* === 大端读写（本实现定的字节序，见 wire.h 抬头）=== */

static void put_u16(uint8_t *p, uint16_t v)
{
    p[0] = (uint8_t)(v >> 8);
    p[1] = (uint8_t)(v & 0xFFu);
}

static void put_u32(uint8_t *p, uint32_t v)
{
    p[0] = (uint8_t)(v >> 24);
    p[1] = (uint8_t)((v >> 16) & 0xFFu);
    p[2] = (uint8_t)((v >> 8) & 0xFFu);
    p[3] = (uint8_t)(v & 0xFFu);
}

static uint16_t get_u16(const uint8_t *p)
{
    return (uint16_t)(((uint16_t)p[0] << 8) | (uint16_t)p[1]);
}

static uint32_t get_u32(const uint8_t *p)
{
    return ((uint32_t)p[0] << 24) | ((uint32_t)p[1] << 16) |
           ((uint32_t)p[2] << 8)  | (uint32_t)p[3];
}

wire_result_t wire_encode_header(uint8_t *out, size_t cap, const wire_header_t *h)
{
    if (out == NULL || h == NULL) return WIRE_ERR_BAD_ARG;
    if (cap < WIRE_HEADER_BYTES) return WIRE_ERR_BAD_ARG;
    wire_result_t s = wire_check_structure(h);
    if (s != WIRE_OK) return s;

    put_u16(out + 0, WIRE_MAGIC);
    out[2] = h->ver;
    out[3] = h->type;
    put_u16(out + 4, h->flags);
    put_u32(out + 6, h->seq);
    put_u16(out + 10, h->frag_off);
    put_u16(out + 12, h->frag_len);
    put_u16(out + 14, h->total_len);
    return WIRE_OK;
}

wire_result_t wire_decode_header(const uint8_t *in, size_t n, wire_header_t *out)
{
    if (in == NULL || out == NULL) return WIRE_ERR_BAD_ARG;
    if (n < WIRE_HEADER_BYTES) return WIRE_ERR_SHORT;
    if (get_u16(in + 0) != (uint16_t)WIRE_MAGIC) return WIRE_ERR_MAGIC;
    if (in[2] != (uint8_t)WIRE_VER) return WIRE_ERR_VERSION;

    out->ver       = in[2];
    out->type      = in[3];
    out->flags     = get_u16(in + 4);
    out->seq       = get_u32(in + 6);
    out->frag_off  = get_u16(in + 10);
    out->frag_len  = get_u16(in + 12);
    out->total_len = get_u16(in + 14);
    return wire_check_structure(out);
}

wire_result_t wire_check_structure(const wire_header_t *h)
{
    if (h == NULL) return WIRE_ERR_BAD_ARG;
    if (h->frag_len > WIRE_FRAG_LEN_MAX)  return WIRE_ERR_RANGE;
    if (h->total_len > WIRE_TOTAL_LEN_MAX) return WIRE_ERR_RANGE;

    uint32_t end = (uint32_t)h->frag_off + (uint32_t)h->frag_len;
    if (end > h->total_len) return WIRE_ERR_STRUCTURE;

    bool more = (h->flags & WIRE_FLAG_MORE) != 0;
    if (more) {
        /* 声称还有后续：本片就不能已经到末尾（否则 MORE 自相矛盾） */
        if (end >= h->total_len) return WIRE_ERR_STRUCTURE;
    } else {
        /* 末片：必须恰好到末尾 */
        if (end != h->total_len) return WIRE_ERR_STRUCTURE;
    }
    return WIRE_OK;
}

/* === 重组 === */

struct reassembler {
    uint32_t max_total;   /* 由构造参数决定（P5） */
    uint8_t *msg;         /* 组装中的整条消息 */
    size_t   msg_len;     /* 已组装字节数（也用作"期望的下一片 frag_off"）*/
    uint16_t total;       /* 期望总长；0 表示尚未收到首片 */
    uint32_t seq;         /* 当前组装的 seq */

    uint8_t *in;          /* 输入累积缓冲（头 + 未消费的载荷） */
    size_t   in_cap;
    size_t   start;       /* 未消费数据的起点（惰性压缩用） */
    size_t   len;         /* 有效字节数；数据位于 in[start, len) */
};

reassembler_t *reasm_create(uint32_t max_total_bytes)
{
    if (max_total_bytes == 0) return NULL;   /* 0 上界无意义：拒绝而不是兜底 */
    reassembler_t *r = (reassembler_t *)calloc(1, sizeof(*r));
    if (r == NULL) return NULL;
    r->max_total = max_total_bytes;
    r->msg = (uint8_t *)malloc(max_total_bytes);
    /* 输入缓冲需容纳 头 + 最大片 */
    r->in_cap = WIRE_HEADER_BYTES + WIRE_FRAG_LEN_MAX;
    r->in = (uint8_t *)malloc(r->in_cap);
    if (r->msg == NULL || r->in == NULL) {
        free(r->msg);
        free(r->in);
        free(r);
        return NULL;
    }
    return r;
}

void reasm_destroy(reassembler_t *r)
{
    if (r == NULL) return;
    free(r->msg);
    free(r->in);
    free(r);
}

static void reasm_reset(reassembler_t *r)
{
    r->len = 0;
    r->start = 0;
    r->msg_len = 0;
    r->total = 0;
}

/* 惰性压缩：只在【push 开头】把未消费数据前移。
 * 为什么不能"一产出消息就压缩"：payload_out 指向内部缓冲，
 * 立刻 memmove 会把【刚返回给调用方的那条消息】覆盖掉。
 * 契约是"payload_out 在下次 push 前有效"，所以压缩只能发生在 push 内部，
 * 且必须发生在调用方已经用完上一条消息之后 —— 即本次 push 的开头。 */
static void reasm_compact(reassembler_t *r)
{
    if (r->start == 0) return;
    size_t keep = r->len - r->start;
    if (keep > 0) memmove(r->in, r->in + r->start, keep);
    r->len = keep;
    r->start = 0;
}

reasm_result_t reasm_push(reassembler_t *r, const uint8_t *in, size_t n,
                          const uint8_t **payload_out, size_t *len_out)
{
    if (r == NULL || payload_out == NULL || len_out == NULL) return REASM_ERROR_INTERNAL;
    if (n > 0 && in == NULL) return REASM_ERROR_INTERNAL;

    /* 上一次的错误不粘住：新一次 push 先复位（契约写在 wire.h） */
    reasm_compact(r);
    if (r->len == WIRE_HEADER_BYTES + WIRE_FRAG_LEN_MAX) {
        /* 输入缓冲满但还没解析出完整片 —— 不可能发生在合法流里
         * （头一到就该能判断还需多少），属内部状态错误 */
        reasm_reset(r);
        return REASM_ERROR_INTERNAL;
    }
    if (n > r->in_cap - r->len) {
        /* 单次投入超过输入缓冲：说明调用方没按"片"喂。
         * 不静默丢弃 —— 明确报错。 */
        reasm_reset(r);
        return REASM_ERROR_TOO_LARGE;
    }
    if (n > 0) {
        memcpy(r->in + r->len, in, n);
        r->len += n;
    }

    for (;;) {
        size_t avail = r->len - r->start;
        if (avail < WIRE_HEADER_BYTES) return REASM_NEED_MORE;

        wire_header_t h;
        wire_result_t wr = wire_decode_header(r->in + r->start, avail, &h);
        if (wr == WIRE_ERR_RANGE)     { reasm_reset(r); return REASM_ERROR_TOO_LARGE; }
        if (wr != WIRE_OK)            { reasm_reset(r); return REASM_ERROR_MALFORMED; }
        if (h.total_len > r->max_total) { reasm_reset(r); return REASM_ERROR_TOO_LARGE; }

        size_t need = WIRE_HEADER_BYTES + (size_t)h.frag_len;
        if (avail < need) return REASM_NEED_MORE;

        /* 首片：初始化整条消息的期望 */
        if (r->total == 0) {
            r->total = h.total_len;
            r->seq = h.seq;
            r->msg_len = 0;
        } else if (h.seq != r->seq || h.total_len != r->total) {
            /* 片间不一致（seq 或总长变了）—— 拒绝整条 */
            reasm_reset(r);
            return REASM_ERROR_MALFORMED;
        }

        /* 设计 §5.3：乱序 -> 拒绝整条（不允许"洞"） */
        if ((size_t)h.frag_off != r->msg_len) {
            reasm_reset(r);
            return REASM_ERROR_OUT_OF_ORDER;
        }

        const uint8_t *pl = r->in + r->start + WIRE_HEADER_BYTES;
        memcpy(r->msg + r->msg_len, pl, h.frag_len);
        r->msg_len += h.frag_len;
        r->start += need;

        if (r->msg_len >= r->total) {
            /* 收齐（结构校验已保证末片恰好到末尾） */
            *payload_out = r->msg;
            *len_out = r->msg_len;
            /* 不 reset：调用方在下次 push 前要用 msg。
             * 下次 push 开头会 reasm_compact，那时再复位消息状态。 */
            r->total = 0;
            r->msg_len = 0;
            if (r->start == r->len) { r->start = 0; r->len = 0; }
            return REASM_MSG_READY;
        }
        /* 还有后续片：继续尝试解析缓冲里剩余的字节 */
    }
}
