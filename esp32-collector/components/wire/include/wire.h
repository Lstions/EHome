/**
 * @file wire.h
 * @brief 定界与重组 —— 3.0 帧格式（设计文档 1.3；原则 P2/P3）
 *
 * 为什么需要它：TCP 是字节流。旧代码把一次 recv() 的内容直接当【一条完整消息】
 * 回调（ehome_tcp.c:447），而 transport.h 从未声明这个前提 —— MQTT 恰好满足、
 * TCP 流不满足（D-09）。本模块把"定界"变成【显式契约】。
 *
 * 帧格式（定稿）：
 *     frame := varint(payload_len) || payload[payload_len]
 *   - payload_len 为 LEB128 无符号 varint，只计 payload，不含自身；
 *   - 任意字节切分下都能正确重组。
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

typedef enum {
    REASM_NEED_MORE = 0,     /* 数据不足，等更多字节 */
    REASM_FRAME_READY,       /* 产出一帧（frame_out/frame_len_out 有效） */
    REASM_ERROR_TOO_LARGE,   /* 声明长度 > max_msg_bytes：【立即拒绝，不缓冲】 */
    REASM_ERROR_MALFORMED,   /* varint 非法（超长/溢出） */
    REASM_ERROR_INTERNAL     /* 缓冲无法容纳（防御性；正常路径不可达） */
} reasm_result_t;

typedef struct reassembler reassembler_t;

/** 上界是【构造参数】而非散落常量（P5）。创建后可查询实际容量。 */
reassembler_t *reasm_create(uint32_t max_msg_bytes);
void reasm_destroy(reassembler_t *r);

/**
 * 投入 n 字节，最多产出一帧。
 * 确定性行为：
 *   - 超限 -> REASM_ERROR_TOO_LARGE，且【不分配、不缓冲】（对比旧行为"静默保留尾部"）；
 *   - 出错后调用一次即可恢复：下一次 push 先复位再处理（错误不会粘住）；
 *   - *frame_out 指向重组器【内部】缓冲（零拷贝），在下一次 push 前有效。
 */
reasm_result_t reasm_push(reassembler_t *r, const uint8_t *in, size_t n,
                          const uint8_t **frame_out, size_t *frame_len_out);

/** 编码一帧的长度前缀（供发送侧使用，保证收发用同一份规则 —— P4）。 */
bool wire_encode_len_prefix(uint8_t *out, size_t cap, size_t *used, uint32_t payload_len);

const char *reasm_result_name(reasm_result_t r);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_WIRE_H */
