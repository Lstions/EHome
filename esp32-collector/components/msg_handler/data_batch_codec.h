#ifndef DATA_BATCH_CODEC_H
#define DATA_BATCH_CODEC_H

#include <stddef.h>
#include <stdint.h>
#include <stdbool.h>
#include "frame_codec.h"

/* DataBatch (0x20) 编码/解码 —— 契约 §2（冻结）。
 *
 * 这个文件是**双方唯一的 wire 参照实现**：
 *   - 固件侧：bus_worker.c 的 report_tx 用它把 ≤4 个非关键样本聚合进一帧；
 *   - 宿主测试：用它做 roundtrip + 7 条不变量的负例（改坏实现必须变红）；
 *   - 后端（v3-backend）：backend/internal/nodemgr/handler_data_batch.go
 *     按同一份契约实现，其锚点字节由本编码器产出。
 *
 * 编码器**自身也强制不变量**（fail-closed）：违反契约的输入直接返回错误，
 * 绝不产出一帧需要后端整帧拒绝的字节。 */

/* 契约 §2.1.3：count 范围 1..4。 */
#define DATA_BATCH_MAX_SAMPLES 4
/* 契约 §2.1.5：单样本 raw_data <= 1024 B。 */
#define DATA_BATCH_MAX_RAW     1024

/* DataBatch 顶层字段号（契约 §2）。 */
enum {
    DATA_BATCH_F_COUNT            = 1,
    DATA_BATCH_F_BASE_TIMESTAMP   = 2,
    DATA_BATCH_F_FIRST_SEQUENCE   = 3,
    DATA_BATCH_F_CHANNEL_ID       = 4,
    DATA_BATCH_F_SAMPLE           = 5,
    DATA_BATCH_F_EDGE_DEVICE_ID   = 6,
    DATA_BATCH_F_COMMAND_TEMPLATE = 7,
    DATA_BATCH_F_COMMAND_INDEX    = 8,
};

/* sample 子消息字段号（契约 §2）。 */
enum {
    DATA_BATCH_SAMPLE_F_DELTA    = 1,
    DATA_BATCH_SAMPLE_F_RAW_DATA = 2,
};

typedef struct {
    uint64_t       delta_us;  /* 相对 base_timestamp_us；首样本必须为 0 */
    const uint8_t *raw_data;  /* 与 0x03 的 payload 同义，1..1024 B */
    size_t         raw_len;
} data_batch_sample_t;

/**
 * @brief 编码一帧 DataBatch(0x20)。
 *
 * @param samples 同一 channel/edge/template 的样本，按时间升序，count ∈ 1..4。
 *                samples[0].delta_us 必须为 0；其余必须 > 0 且不递减。
 * @return FRAME_OK，或 FRAME_ERR_INVALID_TAG（违反契约不变量）、
 *         FRAME_ERR_OVERFLOW（capacity 不足 —— 调用方应**降 n 而不是截断**）。
 */
frame_err_t data_batch_encode(uint8_t *buf, size_t capacity, size_t *out_len,
                              uint32_t channel_id, uint64_t base_timestamp_us,
                              uint32_t first_sequence,
                              const data_batch_sample_t *samples, size_t count,
                              uint32_t edge_device_id,
                              uint32_t command_template_id,
                              uint8_t command_index);

/**
 * @brief 按契约 §2 计算编码 count 个样本所需的**确切**字节数（含类型字节）。
 *
 * 契约 §2.2 的硬上限要求"编码缓冲剩余不足时降 n，不得截断"。调用方先用本
 * 函数算出 n=4/3/2/1 各自的确切大小，再挑第一个放得下的 n，避免"试编码到
 * 一半失败"的不确定路径。
 */
size_t data_batch_encoded_size(uint32_t channel_id, uint64_t base_timestamp_us,
                               uint32_t first_sequence,
                               const data_batch_sample_t *samples, size_t count,
                               uint32_t edge_device_id,
                               uint32_t command_template_id,
                               uint8_t command_index);

typedef struct {
    uint64_t       delta_us;
    const uint8_t *raw_data;
    size_t         raw_len;
} data_batch_decoded_sample_t;

typedef struct {
    uint32_t channel_id;
    uint64_t base_timestamp_us;
    uint32_t first_sequence;
    uint32_t edge_device_id;
    uint32_t command_template_id;
    uint8_t  command_index;
    size_t   count;
    data_batch_decoded_sample_t samples[DATA_BATCH_MAX_SAMPLES];
} data_batch_decoded_t;

/**
 * @brief 严格解码一帧 DataBatch(0x20)，逐条强制契约 §2.1 不变量。
 *
 * 未知 field 号跳过（前向兼容）；已知 field 号重复、count 与实际 field5
 * 次数不符、count 越界、首样本 delta != 0、delta 非单调、raw 为空或超
 * 1024 B —— 任一违反即返回 FRAME_ERR_INVALID_TAG（整帧拒绝，fail-closed）。
 *
 * 注意：底层 frame_decoder_next() 只支持 WIRE_VARINT / WIRE_LENGTH_DELIMITED，
 * 因此"未知 field 跳过"仅对这两种 wire type 成立（本协议只使用这两种）。
 */
frame_err_t data_batch_decode(const uint8_t *buf, size_t len,
                              data_batch_decoded_t *out);

#endif /* DATA_BATCH_CODEC_H */
