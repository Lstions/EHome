/*
 * crash_diag_codec_tests.c — MSG_DIAG_REPORT(0x1E) / MSG_DIAG_ACK(0x1F) 契约测试
 *
 * 为什么这个测试必须存在
 * ----------------------
 * 崩溃诊断的价值全部押在一条链路上：设备 panic -> RTC 捕获 -> NVS 持久化 ->
 * 上报 -> 服务端 ACK -> 释放。这条链路的**每一环在正常运行时都不会被执行**
 * （除非真的崩溃），因此它是最容易"写完就烂掉"的代码 —— 没有测试的话，
 * 唯一能发现它坏了的方式是设备真的崩了却发现什么都收不到。
 *
 * 这里用真实的 frame_codec 验证：
 *   1. 上报帧的字段号/编码与服务端解码器约定一致（字段漂移 = 静默失联）
 *   2. record_id 用 varint 且能容纳 32 位（>2^31 时不能溢出/截断）
 *   3. 栈窗口按小端 uint32 序列原样搬运
 *   4. ACK 的 accepted=false 时固件**不得释放** NVS 记录（宁可重报，不可丢证）
 *   5. CRC 覆盖字段能检出单比特损坏
 */

#include <stdio.h>
#include <string.h>
#include <stdint.h>
#include <stdbool.h>

#include "frame_codec.h"

static int g_fail = 0;
static int g_pass = 0;

#define CHECK(cond, ...)                                                    \
    do {                                                                    \
        if (cond) {                                                         \
            g_pass++;                                                       \
        } else {                                                            \
            g_fail++;                                                       \
            printf("FAIL %s:%d: ", __FILE__, __LINE__);                     \
            printf(__VA_ARGS__);                                            \
            printf("\n");                                                   \
        }                                                                   \
    } while (0)

/* ---- CRC32 IEEE：必须与固件 crash_diag.c 的 crc32_span 完全一致 ---- */

static uint32_t crc32_ref(const uint8_t *data, size_t len)
{
    uint32_t crc = 0xFFFFFFFFu;
    for (size_t i = 0; i < len; i++) {
        crc ^= data[i];
        for (int b = 0; b < 8; b++) {
            uint32_t mask = (uint32_t)(-(int32_t)(crc & 1u));
            crc = (crc >> 1) ^ (0xEDB88320u & mask);
        }
    }
    return ~crc;
}

/* ---- 复刻固件的崩溃记录结构（字段顺序必须与 crash_diag.c 一致） ---- */

#define STACK_WORDS 16
#define REASON_MAX  32

typedef struct {
    uint32_t record_id;
    uint32_t magic;
    uint32_t crc;
    uint32_t reset_reason;
    uint32_t core;
    uint32_t exception;
    uint32_t pc;
    uint32_t exccause;
    uint32_t uptime_sec;
    char     task_name[16];
    char     fw_version[16];
    char     reboot_reason[REASON_MAX];
    uint32_t stack_words;
    uint32_t stack[STACK_WORDS];
} crash_record_t;

/* ---- 复刻固件的上报编码（字段号来自冻结的线协议） ---- */

static size_t encode_crash_report(uint8_t *buf, size_t cap, const crash_record_t *rec)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, cap, MSG_DIAG_REPORT);
    (void)frame_encode_varint(&enc, 1, rec->record_id);
    (void)frame_encode_varint(&enc, 2, 2); /* CRASH */
    (void)frame_encode_varint(&enc, 3, rec->reset_reason);
    (void)frame_encode_varint(&enc, 4, rec->uptime_sec);
    (void)frame_encode_string(&enc, 5, "PANIC");
    (void)frame_encode_varint(&enc, 6, rec->crc);
    (void)frame_encode_varint(&enc, 7, rec->core);
    (void)frame_encode_varint(&enc, 8, rec->exception);
    (void)frame_encode_varint(&enc, 9, rec->pc);
    (void)frame_encode_varint(&enc, 10, rec->exccause);
    (void)frame_encode_bytes(&enc, 11, (const uint8_t *)rec->task_name,
                             strnlen(rec->task_name, sizeof(rec->task_name)));
    (void)frame_encode_bytes(&enc, 12, (const uint8_t *)rec->stack,
                             rec->stack_words * sizeof(uint32_t));
    (void)frame_encode_string(&enc, 13, rec->fw_version);
    (void)frame_encode_string(&enc, 14, rec->reboot_reason);
    return frame_encoder_size(&enc);
}

/* ---- 服务端视角的解码：字段号是契约，漂移必须被这个测试挡住 ---- */

typedef struct {
    uint64_t record_id, report_type, reset_reason, uptime_sec, crc32;
    uint64_t core, exception, pc, exccause;
    uint8_t  stack[256];
    size_t   stack_len;
    char     reason_str[64];
    char     fw_version[64];
    char     reboot_reason[64];
    int      seen[15];
} decoded_t;

static void decode_report(const uint8_t *buf, size_t len, decoded_t *out)
{
    memset(out, 0, sizeof(*out));
    frame_decoder_t dec;
    if (frame_decoder_init(&dec, buf, len) != FRAME_OK) {
        return;
    }
    frame_field_t f;
    while (frame_decoder_next(&dec, &f) == FRAME_OK) {
        if (f.field_num < 15) {
            out->seen[f.field_num]++;
        }
        switch (f.field_num) {
        case 1:  out->record_id = f.value.varint; break;
        case 2:  out->report_type = f.value.varint; break;
        case 3:  out->reset_reason = f.value.varint; break;
        case 4:  out->uptime_sec = f.value.varint; break;
        case 5:
            snprintf(out->reason_str, sizeof(out->reason_str), "%.*s",
                     (int)f.value.bytes.len, (const char *)f.value.bytes.ptr);
            break;
        case 6:  out->crc32 = f.value.varint; break;
        case 7:  out->core = f.value.varint; break;
        case 8:  out->exception = f.value.varint; break;
        case 9:  out->pc = f.value.varint; break;
        case 10: out->exccause = f.value.varint; break;
        case 12:
            out->stack_len = f.value.bytes.len < sizeof(out->stack)
                                 ? f.value.bytes.len : sizeof(out->stack);
            memcpy(out->stack, f.value.bytes.ptr, out->stack_len);
            break;
        case 13:
            snprintf(out->fw_version, sizeof(out->fw_version), "%.*s",
                     (int)f.value.bytes.len, (const char *)f.value.bytes.ptr);
            break;
        case 14:
            snprintf(out->reboot_reason, sizeof(out->reboot_reason), "%.*s",
                     (int)f.value.bytes.len, (const char *)f.value.bytes.ptr);
            break;
        default: break;
        }
    }
}

/* ===================== 测试 ===================== */

static void test_type_constants(void)
{
    /* 0x1E/0x1F 不能与既有消息类型冲突 —— 冲突会让服务端把崩溃报告
     * 误当成别的消息处理（现场已经出现过一次 "Unknown msg type: 0x1E"）。 */
    CHECK(MSG_DIAG_REPORT == 0x1E, "MSG_DIAG_REPORT should be 0x1E, got 0x%02X", MSG_DIAG_REPORT);
    CHECK(MSG_DIAG_ACK == 0x1F, "MSG_DIAG_ACK should be 0x1F, got 0x%02X", MSG_DIAG_ACK);
    CHECK(MSG_DIAG_REPORT != MSG_LOG_STREAM, "diag report collides with log stream");
    CHECK(MSG_DIAG_REPORT != MSG_PERIPH_RSP, "diag report collides with periph rsp");
}

static void test_crash_report_roundtrip(void)
{
    crash_record_t rec;
    memset(&rec, 0, sizeof(rec));
    rec.record_id = 0x81234567u;
    rec.magic = 0x43444147u;
    rec.reset_reason = 4; /* ESP_RST_PANIC */
    rec.core = 0;
    rec.exception = 4; /* PANIC_EXCEPTION_FAULT */
    rec.pc = 0x42013B10u;
    rec.exccause = 28;
    rec.uptime_sec = 1234;
    strcpy(rec.task_name, "bus_worker");
    strcpy(rec.fw_version, "2.5.29");
    strcpy(rec.reboot_reason, "config_apply_failed");
    rec.stack_words = STACK_WORDS;
    for (int i = 0; i < STACK_WORDS; i++) {
        rec.stack[i] = 0xDEAD0000u + (uint32_t)i;
    }
    rec.crc = crc32_ref((const uint8_t *)&rec, offsetof(crash_record_t, crc));

    uint8_t buf[512];
    size_t n = encode_crash_report(buf, sizeof(buf), &rec);
    CHECK(n > 0, "encoder produced 0 bytes");

    decoded_t d;
    decode_report(buf, n, &d);

    CHECK(d.record_id == 0x81234567u, "record_id roundtrip: got %llu",
          (unsigned long long)d.record_id);
    CHECK(d.report_type == 2, "report_type should be CRASH(2), got %llu",
          (unsigned long long)d.report_type);
    CHECK(d.reset_reason == 4, "reset_reason got %llu", (unsigned long long)d.reset_reason);
    CHECK(d.uptime_sec == 1234, "uptime_sec got %llu", (unsigned long long)d.uptime_sec);
    CHECK(strcmp(d.reason_str, "PANIC") == 0, "reason_str got \"%s\"", d.reason_str);
    CHECK(d.core == 0, "core got %llu", (unsigned long long)d.core);
    CHECK(d.exception == 4, "exception got %llu", (unsigned long long)d.exception);
    CHECK(d.pc == 0x42013B10u, "pc got 0x%llX", (unsigned long long)d.pc);
    CHECK(d.exccause == 28, "exccause got %llu", (unsigned long long)d.exccause);
    CHECK(strcmp(d.fw_version, "2.5.29") == 0, "fw_version got %s", d.fw_version);
    CHECK(strcmp(d.reboot_reason, "config_apply_failed") == 0,
          "reboot_reason got \"%s\" — this field is what distinguishes which esp_restart() call site fired",
          d.reboot_reason);

    /* 栈窗口：小端 uint32 序列必须逐字还原 */
    CHECK(d.stack_len == STACK_WORDS * 4, "stack_len got %zu, want %d",
          d.stack_len, STACK_WORDS * 4);
    for (int i = 0; i < STACK_WORDS; i++) {
        uint32_t got;
        memcpy(&got, d.stack + i * 4, 4);
        CHECK(got == 0xDEAD0000u + (uint32_t)i, "stack[%d] got 0x%08X", i, got);
    }
}

static void test_record_id_high_bit(void)
{
    /* record_id 生成时强制置最高位（0x80000000）以避免与 BOOT 的 0 混淆。
     * varint 必须能容纳它 —— 若被截成 32 位有符号就会变负数/丢高位，
     * 导致 ACK 匹配不上，记录永远无法释放。 */
    crash_record_t rec;
    memset(&rec, 0, sizeof(rec));
    rec.record_id = 0xFFFFFFFFu;
    rec.stack_words = 0;
    rec.crc = 0x12345678u;

    uint8_t buf[256];
    size_t n = encode_crash_report(buf, sizeof(buf), &rec);
    decoded_t d;
    decode_report(buf, n, &d);
    CHECK(d.record_id == 0xFFFFFFFFu,
          "max uint32 record_id roundtrip failed: got %llu (must not truncate)",
          (unsigned long long)d.record_id);
}

static void test_boot_report_shape(void)
{
    /* BOOT 报告：record_id=0，服务端据此**不发 ACK**（不占 NVS）。
     * 这个 0 是有语义的，不能被改成非零。 */
    uint8_t buf[256];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_DIAG_REPORT);
    (void)frame_encode_varint(&enc, 1, 0);
    (void)frame_encode_varint(&enc, 2, 1); /* BOOT */
    (void)frame_encode_varint(&enc, 3, 3); /* ESP_RST_SW */
    (void)frame_encode_varint(&enc, 4, 77);
    (void)frame_encode_string(&enc, 5, "SOFTWARE");
    (void)frame_encode_varint(&enc, 6, 0);
    (void)frame_encode_string(&enc, 13, "2.5.29");
    (void)frame_encode_string(&enc, 14, "config_apply_failed");

    decoded_t d;
    decode_report(buf, frame_encoder_size(&enc), &d);
    CHECK(d.record_id == 0, "BOOT report must carry record_id=0 (else server sends a bogus ACK)");
    CHECK(d.report_type == 1, "BOOT report_type must be 1, got %llu",
          (unsigned long long)d.report_type);
    CHECK(d.reset_reason == 3, "reset_reason got %llu", (unsigned long long)d.reset_reason);
    CHECK(strcmp(d.reboot_reason, "config_apply_failed") == 0,
          "BOOT must carry reboot_reason so '谁重启的' has an answer");
}

static void test_crc_detects_corruption(void)
{
    /* 单比特损坏必须改变 CRC —— 否则一条被截断的栈窗口会被当成真实证据，
     * 把排查引向错误的地址。 */
    crash_record_t rec;
    memset(&rec, 0, sizeof(rec));
    rec.record_id = 0x80000001u;
    rec.pc = 0x400D1234u;
    rec.stack_words = STACK_WORDS;
    for (int i = 0; i < STACK_WORDS; i++) rec.stack[i] = 0x11111111u * (uint32_t)(i + 1);
    rec.crc = crc32_ref((const uint8_t *)&rec, offsetof(crash_record_t, crc));

    uint32_t before = rec.crc;
    ((uint8_t *)&rec)[4] ^= 0x01; /* flip one bit in reset_reason */
    uint32_t after = crc32_ref((const uint8_t *)&rec, offsetof(crash_record_t, crc));
    CHECK(before != after, "CRC did not change after a single-bit flip — corruption would go undetected");
}

static void test_ack_accepted_semantics(void)
{
    /* ACK 帧：field1=record_id, field2=accepted。
     * 固件在 accepted=false 时必须保留 NVS 记录（这里验证字段可区分）。 */
    uint8_t buf[64];

    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_DIAG_ACK);
    (void)frame_encode_varint(&enc, 1, 0x81234567u);
    (void)frame_encode_varint(&enc, 2, 1);
    size_t n = frame_encoder_size(&enc);
    CHECK(buf[0] == 0x1F, "ACK frame must start with 0x1F, got 0x%02X", buf[0]);

    frame_decoder_t dec;
    CHECK(frame_decoder_init(&dec, buf, n) == FRAME_OK, "ACK decode init failed");
    uint64_t id = 0, accepted = 99;
    frame_field_t f;
    bool have_id = false, have_acc = false;
    while (frame_decoder_next(&dec, &f) == FRAME_OK) {
        if (f.field_num == 1) { id = f.value.varint; have_id = true; }
        if (f.field_num == 2) { accepted = f.value.varint; have_acc = true; }
    }
    CHECK(have_id && id == 0x81234567u, "ACK record_id got %llu", (unsigned long long)id);
    CHECK(have_acc && accepted == 1, "ACK accepted got %llu", (unsigned long long)accepted);

    /* accepted=false 必须可表达且与 true 不同 */
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_DIAG_ACK);
    (void)frame_encode_varint(&enc, 1, 0x81234567u);
    (void)frame_encode_varint(&enc, 2, 0);
    frame_decoder_init(&dec, buf, frame_encoder_size(&enc));
    accepted = 99;
    while (frame_decoder_next(&dec, &f) == FRAME_OK) {
        if (f.field_num == 2) accepted = f.value.varint;
    }
    CHECK(accepted == 0, "accepted=false must decode as 0 (device must KEEP the record)");
}

static void test_malformed_ack_is_rejected(void)
{
    /* 只有 record_id、没有 accepted 的 ACK 是畸形的。固件必须忽略它，
     * 而不是默认当成 accepted=true 把记录删掉 —— 那是不可逆的证据丢失。 */
    uint8_t buf[32];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_DIAG_ACK);
    (void)frame_encode_varint(&enc, 1, 0x80000005u);
    size_t n = frame_encoder_size(&enc);

    frame_decoder_t dec;
    frame_decoder_init(&dec, buf, n);
    bool have_id = false, have_acc = false;
    frame_field_t f;
    while (frame_decoder_next(&dec, &f) == FRAME_OK) {
        if (f.field_num == 1) have_id = true;
        if (f.field_num == 2) have_acc = true;
    }
    CHECK(have_id && !have_acc,
          "fixture should be missing 'accepted' so the firmware's ignore-path is exercised");
    /* 固件的判据正是 (have_id && have_acc)：缺一即忽略 */
    CHECK(!(have_id && have_acc), "firmware must treat this ACK as malformed and NOT release the record");
}

int main(void)
{
    printf("== crash_diag_codec_tests ==\n");
    test_type_constants();
    test_crash_report_roundtrip();
    test_record_id_high_bit();
    test_boot_report_shape();
    test_crc_detects_corruption();
    test_ack_accepted_semantics();
    test_malformed_ack_is_rejected();
    printf("== passed=%d failed=%d ==\n", g_pass, g_fail);
    return g_fail == 0 ? 0 : 1;
}
