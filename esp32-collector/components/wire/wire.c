/**
 * @file wire.c
 * @brief 帧定界实现（12 B 定长头；**无分片、无重组**）
 *
 * 零拷贝：MSG_READY 时 payload_out 指向定界器【内部】缓冲。
 * 惰性压缩：只在 feed 开头前移未消费数据 —— 见 wire_delim_compact 注释。
 */
#include "wire.h"

#include <stdlib.h>
#include <string.h>

static const char *const s_wire_names[] = {
    [WIRE_OK]            = "OK",
    [WIRE_ERR_SHORT]     = "ERR_SHORT",
    [WIRE_ERR_MAGIC]     = "ERR_MAGIC",
    [WIRE_ERR_VERSION]   = "ERR_VERSION",
    [WIRE_ERR_RANGE]     = "ERR_RANGE",
    [WIRE_ERR_STRUCTURE] = "ERR_STRUCTURE",
    [WIRE_ERR_BAD_ARG]   = "ERR_BAD_ARG",
};

const char *wire_result_name(wire_result_t r)
{
    if ((int)r < 0 || r > (int)WIRE_ERR_BAD_ARG) return "UNKNOWN";
    return s_wire_names[r];
}

static const char *const s_delim_names[] = {
    [WIRE_DELIM_NEED_MORE]      = "NEED_MORE",
    [WIRE_DELIM_MSG_READY]      = "MSG_READY",
    [WIRE_DELIM_ERR_TOO_LARGE]  = "ERR_TOO_LARGE",
    [WIRE_DELIM_ERR_MALFORMED]  = "ERR_MALFORMED",
    [WIRE_DELIM_ERR_CRC]        = "ERR_CRC",
    [WIRE_DELIM_ERR_INTERNAL]   = "ERR_INTERNAL",
};

const char *wire_delim_result_name(wire_delim_result_t r)
{
    if ((int)r < 0 || r > (int)WIRE_DELIM_ERR_INTERNAL) return "UNKNOWN";
    return s_delim_names[r];
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

/* === CRC32C（Castagnoli，反射多项式 0x82F63B78）===
 *
 * 逐位实现（无查表）：消息最大 ~16 KB ⇒ 约 13 万次迭代，对校验路径可以接受，
 * 换来的是"没有一张需要与后端对齐的表"（表的生成多项式写错会静默算错）。
 *
 * 标准校验值（由 host 测试断言）：CRC32C("123456789") == 0xE3069283。
 */
uint32_t wire_crc32c(const uint8_t *data, size_t n)
{
    uint32_t crc = 0xFFFFFFFFu;
    for (size_t i = 0; i < n; i++) {
        crc ^= data[i];
        for (int b = 0; b < 8; b++) {
            /* 反射：最低位为 1 时右移并异或多项式 */
            crc = (crc >> 1) ^ (0x82F63B78u & (uint32_t)(-(int32_t)(crc & 1u)));
        }
    }
    return crc ^ 0xFFFFFFFFu;
}

wire_result_t wire_encode_header(uint8_t *out, size_t cap, const wire_header_t *h)
{
    if (out == NULL || h == NULL) return WIRE_ERR_BAD_ARG;
    if (cap < WIRE_HEADER_BYTES) return WIRE_ERR_BAD_ARG;
    if (h->payload_len > WIRE_PAYLOAD_MAX) return WIRE_ERR_RANGE;

    put_u16(out + 0, WIRE_MAGIC);
    out[2] = h->ver;
    out[3] = h->type;
    put_u16(out + 4, h->flags);
    put_u32(out + 6, h->seq);
    put_u16(out + 10, h->payload_len);
    return WIRE_OK;
}

wire_result_t wire_decode_header(const uint8_t *in, size_t n, wire_header_t *out)
{
    if (in == NULL || out == NULL) return WIRE_ERR_BAD_ARG;
    if (n < WIRE_HEADER_BYTES) return WIRE_ERR_SHORT;
    if (get_u16(in + 0) != (uint16_t)WIRE_MAGIC) return WIRE_ERR_MAGIC;
    if (in[2] != (uint8_t)WIRE_VER) return WIRE_ERR_VERSION;

    out->ver         = in[2];
    out->type        = in[3];
    out->flags       = get_u16(in + 4);
    out->seq         = get_u32(in + 6);
    out->payload_len = get_u16(in + 10);
    if (out->payload_len > WIRE_PAYLOAD_MAX) return WIRE_ERR_RANGE;
    return WIRE_OK;
}

bool wire_header_has_crc(const wire_header_t *h)
{
    return h != NULL && (h->flags & WIRE_FLAG_CRC32C) != 0;
}

uint32_t wire_frame_wire_bytes(const wire_header_t *h)
{
    if (h == NULL) return 0;
    return (uint32_t)WIRE_HEADER_BYTES + (uint32_t)h->payload_len +
           (wire_header_has_crc(h) ? WIRE_CRC_BYTES : 0u);
}

/* === 流定界器 === */

struct wire_delim {
    uint32_t max_payload;   /* 构造参数（P5） */
    uint8_t *buf;
    size_t   cap;           /* max_payload + 头 + CRC */
    size_t   start;         /* 未消费数据的起点（惰性压缩用） */
    size_t   len;           /* 有效字节数；数据位于 buf[start, len) */
    /* task-34：缓冲是"自己 malloc 的"还是"调用方给的"。
     * 调用方给的缓冲**不得**被本模块 free —— 否则会 free 掉一块可能来自
     * PSRAM 的堆指针（heap_caps 分配的指针必须用 heap_caps_free），
     * 那是堆损坏级别的错误，且在宿主上（同一 malloc）**测不出来**。 */
    bool     owns_buf;
};

/* task-34：用调用方提供的缓冲创建定界器。
 *
 * 为什么需要它：定界器缓冲是 max_payload + 16 字节（默认 4112）的**纯数据**
 * 缓冲，只需"够大且连续"，**不需要**内部 RAM。s3p 上把它放 PSRAM 可以把
 * 内部连续块还给内存门禁（见 docs/设计/决策-3.0-s3p-内部RAM-2026-10-07.md）。
 *
 * 为什么不直接在本文件调 heap_caps_malloc：本文件按约束 C2 **不依赖 IDF**
 * （宿主测试直接编它）。把 IDF 分配器写进来会破坏宿主可测性。
 * ⇒ 用"调用方注入缓冲"的形态：IDF 侧负责选池，本层只负责用它。 */
wire_delim_t *wire_delim_create_with_buf(uint32_t max_payload, uint8_t *buf, size_t cap)
{
    const size_t need = (size_t)max_payload + WIRE_HEADER_BYTES + WIRE_CRC_BYTES;
    if (max_payload == 0 || buf == NULL || cap < need) return NULL;
    wire_delim_t *d = (wire_delim_t *)calloc(1, sizeof(*d));
    if (d == NULL) return NULL;
    d->max_payload = max_payload;
    d->cap = need;
    d->buf = buf;
    d->owns_buf = false;
    return d;
}

wire_delim_t *wire_delim_create(uint32_t max_payload)
{
    if (max_payload == 0) return NULL;   /* 0 上界无意义：拒绝而不是兜底 */
    wire_delim_t *d = (wire_delim_t *)calloc(1, sizeof(*d));
    if (d == NULL) return NULL;
    d->max_payload = max_payload;
    d->cap = (size_t)max_payload + WIRE_HEADER_BYTES + WIRE_CRC_BYTES;
    d->buf = (uint8_t *)malloc(d->cap);
    if (d->buf == NULL) { free(d); return NULL; }
    d->owns_buf = true;
    return d;
}

void wire_delim_destroy(wire_delim_t *d)
{
    if (d == NULL) return;
    if (d->owns_buf) free(d->buf);
    free(d);
}

static void wire_delim_reset(wire_delim_t *d) { d->start = 0; d->len = 0; }

/* 惰性压缩：只在 feed 开头把未消费数据前移。
 * 为什么不能"一产出消息就压缩"：payload_out 指向内部缓冲，
 * 立刻 memmove 会把【刚返回给调用方的那条消息】覆盖掉。
 * 契约是"payload_out 在下次 feed 前有效"，所以压缩只能发生在 feed 内部，
 * 且必须发生在调用方已经用完上一条消息之后 —— 即本次 feed 的开头。 */
static void wire_delim_compact(wire_delim_t *d)
{
    if (d->start == 0) return;
    size_t keep = d->len - d->start;
    if (keep > 0) memmove(d->buf, d->buf + d->start, keep);
    d->len = keep;
    d->start = 0;
}

wire_delim_result_t wire_delim_feed(wire_delim_t *d, const uint8_t *in, size_t n,
                                    const uint8_t **payload_out, size_t *len_out)
{
    if (d == NULL || payload_out == NULL || len_out == NULL) return WIRE_DELIM_ERR_INTERNAL;
    if (n > 0 && in == NULL) return WIRE_DELIM_ERR_INTERNAL;

    wire_delim_compact(d);

    /* 单次投入必须能放进缓冲。放不进说明调用方一次性给了超过一条最大消息的数据，
     * 那是调用方违约；**不静默丢弃** —— 明确报错。 */
    if (n > d->cap - d->len) {
        wire_delim_reset(d);
        return WIRE_DELIM_ERR_TOO_LARGE;
    }
    if (n > 0) {
        memcpy(d->buf + d->len, in, n);
        d->len += n;
    }

    size_t avail = d->len - d->start;
    if (avail < WIRE_HEADER_BYTES) return WIRE_DELIM_NEED_MORE;

    wire_header_t h;
    wire_result_t wr = wire_decode_header(d->buf + d->start, avail, &h);
    if (wr == WIRE_ERR_RANGE)    { wire_delim_reset(d); return WIRE_DELIM_ERR_TOO_LARGE; }
    if (wr != WIRE_OK)           { wire_delim_reset(d); return WIRE_DELIM_ERR_MALFORMED; }
    if (h.payload_len > d->max_payload) {
        /* 超本实例上界：不缓冲（禁止"按对端声明分配"） */
        wire_delim_reset(d);
        return WIRE_DELIM_ERR_TOO_LARGE;
    }

    size_t total = (size_t)WIRE_HEADER_BYTES + (size_t)h.payload_len;
    if (wire_header_has_crc(&h)) total += WIRE_CRC_BYTES;
    if (avail < total) return WIRE_DELIM_NEED_MORE;

    const uint8_t *pl = d->buf + d->start + WIRE_HEADER_BYTES;
    if (wire_header_has_crc(&h)) {
        uint32_t want = get_u32(d->buf + d->start + WIRE_HEADER_BYTES + h.payload_len);
        uint32_t got  = wire_crc32c(pl, h.payload_len);
        if (got != want) { wire_delim_reset(d); return WIRE_DELIM_ERR_CRC; }
    }

    *payload_out = pl;
    *len_out = h.payload_len;
    /* 推进 start 但【不】压缩：压缩留到下一次 feed 开头（见 wire_delim_compact）。 */
    d->start += total;
    if (d->start >= d->len) wire_delim_reset(d);
    return WIRE_DELIM_MSG_READY;
}
