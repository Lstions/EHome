/**
 * @file frame_codec.h
 * @brief Binary Frame Protocol - ESP32 Encoder/Decoder
 * 
 * ~300 lines, zero-allocation, stack-based
 */

#ifndef FRAME_CODEC_H
#define FRAME_CODEC_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include <string.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === Error codes === */
typedef enum {
    FRAME_OK = 0,
    FRAME_DONE = 1,
    FRAME_ERR_OVERFLOW = -1,
    FRAME_ERR_UNDERFLOW = -2,
    FRAME_ERR_INVALID_TAG = -3,
    FRAME_ERR_INCOMPLETE = -4,
} frame_err_t;

/* === Wire types (protobuf compatible) === */
#define WIRE_VARINT           0
#define WIRE_FIXED64          1
#define WIRE_LENGTH_DELIMITED 2
#define WIRE_START_GROUP      3
#define WIRE_END_GROUP        4
#define WIRE_FIXED32          5

/* === Message types === */
#define MSG_HELLO       0x01
#define MSG_STATUS_RPT  0x02
#define MSG_DATA_RPT    0x03
#define MSG_CONFIG_MFST 0x04
#define MSG_CONFIG_RSLT 0x05
#define MSG_WRITE_CMD   0x06
#define MSG_WRITE_RSP   0x07
#define MSG_PING        0x08
#define MSG_PONG        0x09
#define MSG_OTA_CMD     0x0A
#define MSG_OTA_PROG    0x0B
#define MSG_SCAN_RPT    0x0C
#define MSG_SCAN_REQ    0x0D
#define MSG_QUERY_REQ   0x0E
#define MSG_QUERY_RSP   0x0F
#define MSG_CONFIG_QUERY  0x10
#define MSG_CONFIG_REPORT 0x11
#define MSG_HELLO_ACK     0x12
/* v2.1 sync messages */
#define MSG_CONFIG_SYNC_REQ  0x13
#define MSG_CONFIG_SYNC_RSP  0x14
/* Phase 2: versioned single-step Channel control */
#define MSG_CHANNEL_CMD_V2       0x15
#define MSG_CHANNEL_CMD_V2_ACK   0x16
#define MSG_CHANNEL_CMD_V2_FINAL 0x17
/* v2.4 resource reporting */
#define MSG_RESOURCE_REPORT  0x19
#define MSG_QUERY_RESOURCES  0x1A
/* v3.0 peripheral control (GPIO + PWM) */
#define MSG_PERIPH_CMD       0x1B
#define MSG_PERIPH_RSP       0x1C
/* v2.5 log streaming */
#define MSG_LOG_STREAM       0x1D
/* v2.6 crash diagnostics (ESP -> SVR: report; SVR -> ESP: ack)
 *
 * 引入背景：远程 S3 节点反复重启而设备端不留证据（见 main/crash_diag.h）。
 * 该通道让"复位原因 + 异常 PC + 栈窗口"能在重启后上传，且设备端**只有
 * 在收到 ACK 后才释放**对应的 NVS 记录占用。 */
#define MSG_DIAG_REPORT      0x1E
#define MSG_DIAG_ACK         0x1F
/* v2.8 memory health (ESP -> SVR only).
 *
 * 低频内存水位上报：free/largest/min_ever/min_task_stack_high_water/floor，
 * 全部 varint、单位字节。独立于 StatusReport (0x02)，原因：老后端对
 * StatusReport 顶层 field_num > 10 是**整条丢弃**（handler_status.go:181），
 * 新增 field 11 会让旧后端连心跳一起掉。而未知消息类型在管理器里走
 * default 分支只记一条 Warn（manager.go:429），不拒连接、不影响其他消息，
 * 所以固件先行是安全的，后端解析可后续接入。
 *
 * 触发：启动后一次、每 60 s 一次、低内存事件时一次。
 *
 * ⚠ 类型号 0x20 → 0x21（2026-10-05，V3-2a 裁决）。0x20 被 V3 的 DataBatch
 * 同时占用，而 DataBatch 已在设计文档/收益表/迁移表里通篇使用 0x20。
 * MSG_MEM_RPT 尚未合入 main、后端尚未解析（后端对它只会打 Unknown msg type
 * 警告），因此改号零成本。详见
 * docs/设计/V3-2a-DataBatch-落地契约-2026-10-05.md §0.1。 */
#define MSG_MEM_RPT          0x21

/* V3-2a DataBatch (ESP -> SVR) —— 只承载【非关键】周期遥测。
 *
 * 为什么需要：100 Hz x N 通道时每个样本一帧 0x03 会产生 N*100 个 PUBACK/s。
 * 把同一 channel/edge/template 的 ≤4 个非关键样本聚合进一帧，把每样本开销
 * 降到 ~1/4。**能力位协商，而非版本号协商**：只有 HelloAck 的 features
 * bit0（CAP_DATA_BATCH_V1）为 1 时固件才发 0x20；否则代码路径与现状逐字节
 * 一致（只发 0x03）。后端即使自己没置位也必须能解析 0x20（防御性 + 灰度）。
 *
 * 帧布局与不变量见契约 §2（冻结）：count 必须等于 field 5 实际次数、
 * count∈1..4、首样本 delta_us==0 且 delta 单调、单样本 raw_data ∈ [1,1024]、
 * 不携带 error_code（关键样本永远单独走 0x03）、未知 field 跳过 / 已知 field
 * 重复即整帧拒绝。QoS0（同 LogStream）。 */
#define MSG_DATA_BATCH       0x20

/* DataBatch 能力位（HelloAck field 2 features 的 bit0）。 */
#define CAP_DATA_BATCH_V1    ((uint64_t)1 << 0)

/* === Encoder === */
typedef struct {
    uint8_t *buf;
    size_t   pos;
    size_t   capacity;
} frame_encoder_t;

void frame_encoder_init(frame_encoder_t *enc, uint8_t *buf, size_t cap, uint8_t msg_type);
/* Initialize an embedded message. Unlike frame_encoder_init(), this does not
 * prepend a top-level message-type byte. */
void frame_encoder_init_sub(frame_encoder_t *enc, uint8_t *buf, size_t cap);
frame_err_t frame_encode_varint(frame_encoder_t *enc, uint8_t field_num, uint64_t value);
frame_err_t frame_encode_string(frame_encoder_t *enc, uint8_t field_num, const char *str);
frame_err_t frame_encode_bytes(frame_encoder_t *enc, uint8_t field_num, const uint8_t *data, size_t len);
/**
 * @brief Append raw bytes directly to encoder (no field tag/length prefix).
 *
 * Use for pre-encoded sub-message payloads that already contain their own
 * field tags. Avoids direct enc->pos manipulation by callers.
 */
frame_err_t frame_encoder_append_raw(frame_encoder_t *enc, const uint8_t *data, size_t len);
/* Bool is encoded as varint (0 or 1), protobuf-compatible */
static inline frame_err_t frame_encode_bool(frame_encoder_t *enc, uint8_t field_num, bool value) {
    return frame_encode_varint(enc, field_num, value ? 1 : 0);
}
static inline size_t frame_encoder_size(const frame_encoder_t *enc) { return enc->pos; }
static inline uint8_t *frame_encoder_data(frame_encoder_t *enc) { return enc->buf; }

/* === Decoder === */
typedef struct {
    const uint8_t *buf;
    size_t         pos;
    size_t         len;
} frame_decoder_t;

typedef struct {
    uint8_t  field_num;
    uint8_t  wire_type;
    union {
        uint64_t varint;
        struct { const uint8_t *ptr; size_t len; } bytes;
    } value;
} frame_field_t;

frame_err_t frame_decoder_init(frame_decoder_t *dec, const uint8_t *buf, size_t len);
frame_err_t frame_decoder_init_sub(frame_decoder_t *dec, const uint8_t *buf, size_t len);
frame_err_t frame_decoder_next(frame_decoder_t *dec, frame_field_t *field);

/* === Varint helpers === */
static inline size_t frame_varint_size(uint64_t value) {
    size_t size = 1;
    while (value > 0x7F) { size++; value >>= 7; }
    return size;
}

static inline size_t frame_encode_varint_to_buf(uint8_t *buf, uint64_t value) {
    size_t i = 0;
    while (value > 0x7F) {
        buf[i++] = (value & 0x7F) | 0x80;
        value >>= 7;
    }
    buf[i++] = value & 0x7F;
    return i;
}

/* === Field accessors (safe extraction from decoded fields) === */

static inline frame_err_t frame_field_get_string(const frame_field_t *f, char *buf, size_t sz) {
    if (!f || !buf || sz == 0) return FRAME_ERR_INVALID_TAG;
    if (f->wire_type != WIRE_LENGTH_DELIMITED) return FRAME_ERR_INVALID_TAG;
    if (!f->value.bytes.ptr) { buf[0] = '\0'; return FRAME_OK; }
    size_t n = f->value.bytes.len < sz - 1 ? f->value.bytes.len : sz - 1;
    memcpy(buf, f->value.bytes.ptr, n);
    buf[n] = '\0';
    return FRAME_OK;
}

static inline frame_err_t frame_field_get_varint(const frame_field_t *f, uint64_t *v) {
    if (!f || !v) return FRAME_ERR_INVALID_TAG;
    if (f->wire_type != WIRE_VARINT) return FRAME_ERR_INVALID_TAG;
    *v = f->value.varint;
    return FRAME_OK;
}

static inline frame_err_t frame_field_get_bytes(const frame_field_t *f,
                                                  const uint8_t **data, size_t *len) {
    if (!f || !data || !len) return FRAME_ERR_INVALID_TAG;
    if (f->wire_type != WIRE_LENGTH_DELIMITED) return FRAME_ERR_INVALID_TAG;
    *data = f->value.bytes.ptr;
    *len = f->value.bytes.len;
    return FRAME_OK;
}

#ifdef __cplusplus
}
#endif

#endif /* FRAME_CODEC_H */
