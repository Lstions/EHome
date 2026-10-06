/**
 * @file wire.h
 * @brief 定界与重组 —— 3.0 帧格式（设计 §5.1；原则 P2/P3）
 *
 * 为什么需要它：TCP 是字节流。旧代码把一次 recv() 的内容直接当【一条完整消息】
 * 回调（ehome_tcp.c:447），而 transport.h 从未声明这个前提 —— MQTT 恰好满足、
 * TCP 流不满足（D-09）。本模块把"定界"变成【显式契约】。
 *
 * ## 帧格式（设计 §5.1 原文，非本文件自定）
 *
 *     偏移  字段        宽度
 *     0     magic       2     = 0x4548 ("EH")
 *     2     ver         1     = 0x30 (3.0)
 *     3     type        1     消息类型
 *     4     flags       2     bit0 MORE / bit1 ACK_REQ / bit2 IS_ACK / bit3 IS_NAK
 *     6     seq         4     每方向单调递增（重放检测 + ACK 关联）
 *     10    frag_off    2     本片在整条消息中的字节偏移
 *     12    frag_len    2     本片载荷长度（硬上界 1024）
 *     14    total_len   2     整条消息总长（硬上界 16384）
 *     ---- 共 16 B 定长头 ---- 之后是 frag_len 字节载荷；末片可追加 4 B CRC32C
 *
 * ## 端序：设计未规定，本实现定为【大端】（网络字节序）
 * §5.1 全篇没有一处规定字节序（两份设计文档 grep 无"大端/小端/big-endian/
 * network byte order"）。多字节字段没有字节序 ⇒ 两端各自"按本机来"也能各自
 * 自测通过，却会**互相读错**。定为大端的三条理由见
 * protocol/vectors/frame_header.txt 抬头注释；该文件同时是唯一向量来源。
 *
 * **这是本实现者的决定，需设计者确认**；若改为小端，改本文件 + 两端的
 * 编解码函数即可（有 golden vector 门禁在，漏改一端会立刻红）。
 *
 * ## 与骨架初版的差异（为什么要改）
 * 骨架初版把帧格式定成 varint(payload_len) || payload —— 那是**另一种格式**：
 * 没有 magic/ver/type/flags/seq/frag_off/total_len，**不支持分片**，
 * 也没有任何与设计 §5.1 对应的字段。与 msgcodec 自创 tag 格式（D-29 同族）
 * 一样，属"骨架自作主张改了契约"。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_WIRE_H
#define EHOME_WIRE_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/** 帧头长度（定长，设计 §5.1）。 */
#define WIRE_HEADER_BYTES 16u

/** magic/ver（设计 §5.1）。 */
#define WIRE_MAGIC 0x4548u
#define WIRE_VER   0x30u

/** 载荷硬上界（设计 §5.2）：单片 1024 B / 整条 16384 B。 */
#define WIRE_FRAG_LEN_MAX   1024u
#define WIRE_TOTAL_LEN_MAX  16384u

/** flags 位（设计 §5.1）。 */
#define WIRE_FLAG_MORE     0x0001u
#define WIRE_FLAG_ACK_REQ  0x0002u
#define WIRE_FLAG_IS_ACK   0x0004u
#define WIRE_FLAG_IS_NAK   0x0008u

/** 末片可选追加的 CRC32C 长度（整条消息）。 */
#define WIRE_CRC_BYTES 4u

/** 已解码的帧头（字段已转主机序）。 */
typedef struct {
    uint8_t  ver;
    uint8_t  type;
    uint16_t flags;
    uint32_t seq;
    uint16_t frag_off;
    uint16_t frag_len;
    uint16_t total_len;
} wire_header_t;

/** 帧头编解码结果。 */
typedef enum {
    WIRE_OK = 0,
    WIRE_ERR_SHORT,      /* 字节不足 16（需要更多数据；不是错误） */
    WIRE_ERR_MAGIC,      /* magic 不符 */
    WIRE_ERR_VERSION,    /* ver 不符 */
    WIRE_ERR_RANGE,      /* 字段越界（frag_len > 1024 / total_len > 16384 等） */
    WIRE_ERR_STRUCTURE,  /* 结构性错误：off+len > total、MORE 与末片矛盾等 */
    WIRE_ERR_BAD_ARG
} wire_result_t;

const char *wire_result_name(wire_result_t r);

/**
 * 编码帧头（大端）。out 必须至少 WIRE_HEADER_BYTES。
 * @return WIRE_OK / WIRE_ERR_BAD_ARG / WIRE_ERR_RANGE
 */
wire_result_t wire_encode_header(uint8_t *out, size_t cap, const wire_header_t *h);

/**
 * 解码帧头（大端）。只读 in[0,16)。
 * **只做字段级校验**（magic/ver/范围），结构一致性用 wire_check_structure。
 */
wire_result_t wire_decode_header(const uint8_t *in, size_t n, wire_header_t *out);

/**
 * 结构一致性校验（与解码分离，便于分别测试）：
 *   - frag_len > WIRE_FRAG_LEN_MAX          -> RANGE
 *   - total_len > WIRE_TOTAL_LEN_MAX        -> RANGE
 *   - frag_off + frag_len > total_len       -> STRUCTURE
 *   - MORE=1 且 frag_off+frag_len >= total  -> STRUCTURE（声称还有后续却已到末尾）
 *   - MORE=0 且 frag_off+frag_len != total  -> STRUCTURE（末片却没到末尾）
 */
wire_result_t wire_check_structure(const wire_header_t *h);

/* === 重组（下行分片）=== */

typedef enum {
    REASM_NEED_MORE = 0,     /* 数据不足，等更多字节 */
    REASM_MSG_READY,         /* 产出一整条消息（payload_out/len_out 有效） */
    REASM_ERROR_TOO_LARGE,   /* 越界：【立即拒绝，不缓冲】 */
    REASM_ERROR_MALFORMED,   /* 头非法 / 结构性错误 */
    REASM_ERROR_OUT_OF_ORDER,/* frag_off ≠ 已收字节数（设计 §5.3：拒绝整条） */
    REASM_ERROR_CRC,         /* 末片 CRC32C 不符 */
    REASM_ERROR_INTERNAL
} reasm_result_t;

const char *reasm_result_name(reasm_result_t r);

typedef struct reassembler reassembler_t;

/**
 * 上界是【构造参数】而非散落常量（P5）。
 * 缓冲为 max_total_bytes（设计 §5.2 硬上界 16,384）+ 头部余量，一次分配。
 */
reassembler_t *reasm_create(uint32_t max_total_bytes);
void reasm_destroy(reassembler_t *r);

/**
 * 投入 n 字节，最多产出一条完整消息。
 *
 * 确定性行为（对应设计 §5.3）：
 *   - 乱序（frag_off ≠ 已收字节数）-> REASM_ERROR_OUT_OF_ORDER，**丢弃整条**；
 *   - 总长不符 / 结构错误        -> REASM_ERROR_MALFORMED，丢弃整条；
 *   - 越界                       -> REASM_ERROR_TOO_LARGE，**不分配不缓冲**；
 *   - 出错后下一次 push 先复位再处理（错误不粘住）；
 *   - *payload_out 指向重组器【内部】缓冲（零拷贝），在下一次 push 前有效。
 */
reasm_result_t reasm_push(reassembler_t *r, const uint8_t *in, size_t n,
                          const uint8_t **payload_out, size_t *len_out);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_WIRE_H */
