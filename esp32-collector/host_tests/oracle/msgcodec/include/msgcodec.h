/**
 * @file msgcodec.h
 * @brief 消息编解码原语 —— 纯函数、无全局、无硬件（设计文档 1.3；原则 P7）
 *
 * 为什么重做：现状各消息的字段解析分散在 msg_handler/ 的 8 个 handler_*.c
 * （合计 3,521 行），每个文件自己拆字段、自己判 wire type，导致同一字段的
 * 宽度/顺序在多处重复定义（D-04，违反 P4）。这里把编解码收成【纯函数】，
 * 宿主可 100% 覆盖（治 D-24：29% 源码从未被宿主测试编译）。
 *
 * 字段格式（显式 TLV，不是 protobuf）：
 *     field := field_id:u8 || value_len:varint || value[value_len]
 *   - u64 的 value 就是该数的 LEB128 字节；
 *   - bytes 的 value 就是原始字节。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_MSGCODEC_H
#define EHOME_MSGCODEC_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

typedef enum {
    ENC_OK = 0,
    ENC_NO_SPACE,   /* 容量不足 —— 【不截断】，让调用方决定分片或拒绝（P2） */
    ENC_BAD_ARG
} enc_result_t;

typedef enum {
    DEC_OK = 0,
    DEC_TRUNCATED,  /* 数据不完整（需要更多字节） */
    DEC_BAD_WIRE    /* 编码非法（不许"尽量解析"） */
} dec_result_t;

const char *enc_result_name(enc_result_t r);
const char *dec_result_name(dec_result_t r);

/* ---- varint 原语 ---- */
enc_result_t enc_varint(uint8_t *out, size_t cap, size_t *used, uint64_t v);
dec_result_t dec_varint(const uint8_t *in, size_t n, size_t *used, uint64_t *v);

/* ---- 字段写入 ---- */
enc_result_t enc_field_u64(uint8_t *out, size_t cap, size_t *used,
                           uint8_t field_id, uint64_t v);
enc_result_t enc_field_bytes(uint8_t *out, size_t cap, size_t *used,
                             uint8_t field_id, const void *p, size_t n);

/* ---- 字段读取（顺序扫描）----
 * 读出一个字段并把 *cursor 推到下一个字段；value 指向【输入缓冲内部】（零拷贝）。
 * 没有更多字段时返回 DEC_TRUNCATED 且 cursor 不变。 */
/* 线路格式（**唯一权威 = 生产代码 + 后端**，不是本骨架自己定的）：
 *   帧 = [类型字节 u8] 然后若干字段
 *   字段 = [tag varint] [负载]
 *   tag  = (field_id << 3) | wire_type         <-- protobuf 风格
 *   wire_type: 0 = varint（负载就是 varint 值）
 *              2 = length-delimited（负载 = varint 长度 + 数据）
 *
 * ⚠ 2026-10-06 修正：本骨架初版把字段编成
 *     [field_id 1 字节][varint 长度][值]
 *   —— 那是**另一种格式**，与生产 frame_codec.c:38 的
 *   `tag = (field_num << 3) | WIRE_VARINT` 以及后端
 *   backend/pkg/frame/frame.go:253 的 appendTag **都不兼容**。
 *   由 S0 的共享 golden vector（protocol/vectors/wire_primitives.txt）
 *   当场发现：12 条向量里 5 条对不上。
 *   ⇒ 定义了"另一个格式"的骨架，比没有骨架更危险：它看起来能用。
 */
#define MSGCODEC_WIRE_VARINT 0u
#define MSGCODEC_WIRE_BYTES  2u

typedef struct {
    uint8_t        field_id;
    uint8_t        wire_type;   /* 0 = varint；2 = length-delimited */
    const uint8_t *value;       /* 负载（varint 字段：其 varint 字节；bytes 字段：数据本身） */
    size_t         value_len;
} field_view_t;

dec_result_t dec_next_field(const uint8_t *in, size_t n, size_t *cursor, field_view_t *out);

/** 从字段值里解出 u64（value 必须是 varint 编码）。 */
dec_result_t dec_field_u64(const field_view_t *f, uint64_t *v);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_MSGCODEC_H */
