/*
 * data_batch_codec_tests.c — V3-2a DataBatch(0x20) 的宿主契约测试。
 *
 * 为什么必须有这组测试：
 *   DataBatch 是 V3 二期唯一新增的 wire 类型，而它的启用是**能力位驱动**
 *   的。两类错误都很贵：
 *     (a) 固件编出一帧后端按契约必须整帧拒绝的字节（count 不符 / 首样本
 *         delta≠0 / delta 非单调 / 空 raw / 超 1024B / 重复字段）—— 现场
 *         表现为"某通道数据整段消失"，而不是一条清晰的错误；
 *     (b) 兼容性红线破了：能力位为 0 时仍发 0x20 —— 旧后端不认识它，
 *         周期遥测静默丢失。
 *   本文件把契约 §2.1 的 7 条不变量逐条钉死，并用**真实字节锚点**锁住
 *   帧布局（后端 handler_data_batch.go 用同一组字节做解码锚点）。
 *
 * 覆盖：
 *   T1  最小 n=1 帧的逐字节锚点 + roundtrip
 *   T2  n=2 帧（含 6/7/8 可选字段）的逐字节锚点 + roundtrip
 *   T3  n=4 满批 + 1024B 边界样本 roundtrip
 *   T4  不变量 2：count 与实际 field5 次数不符 -> 拒绝
 *   T5  不变量 3：count 越界（0 / 5）-> 拒绝
 *   T6  不变量 4：首样本 delta≠0 -> 拒绝；delta 非单调 -> 拒绝
 *   T7  不变量 5：空 raw_data -> 拒绝；超 1024B -> 拒绝
 *   T8  不变量 7：未知字段跳过；已知字段重复 -> 拒绝
 *   T9  不变量 1/6：编码器自身拒绝跨源/关键样本（validate_samples）
 *   T10 编码缓冲不足 -> 降 n（encoded_size 预言 + 不截断）
 *   T11 能力位为 0 时不发 0x20（兼容性红线，见 bus_worker_data_batch_tests）
 */

#include <stdio.h>
#include <string.h>
#include <stdint.h>
#include <stdbool.h>

#include "frame_codec.h"
#include "data_batch_codec.h"

static int g_failures = 0;
static int g_checks = 0;

#define CHECK(cond, ...)                                                     \
    do {                                                                     \
        g_checks++;                                                          \
        if (!(cond)) {                                                       \
            g_failures++;                                                    \
            fprintf(stderr, "FAIL %s:%d: ", __func__, __LINE__);             \
            fprintf(stderr, __VA_ARGS__);                                    \
            fprintf(stderr, "\n");                                           \
        }                                                                    \
    } while (0)

static void hex_of(const uint8_t *buf, size_t len, char *out, size_t out_sz)
{
    size_t p = 0;
    for (size_t i = 0; i < len && p + 3 < out_sz; i++) {
        p += (size_t)snprintf(out + p, out_sz - p, "%02x", buf[i]);
    }
    out[p] = '\0';
}

/* 与 v3-backend 约定的锚点向量（双方独立算出，必须逐字节一致）。 */
static const uint8_t kRawA[] = { 0x01, 0x03, 0x02, 0x00, 0x00, 0xb8, 0x44 };
static const uint8_t kRawB[] = { 0x02, 0x04, 0xaa, 0xbb, 0xcc, 0xdd };

/* ------------------------------------------------------------------ *
 *  T1: 最小 n=1 帧
 * ------------------------------------------------------------------ */
static void test_minimal_n1_anchor(void)
{
    data_batch_sample_t s[1] = { { 0, kRawA, sizeof(kRawA) } };
    uint8_t buf[64];
    size_t len = 0;
    frame_err_t err = data_batch_encode(buf, sizeof(buf), &len,
                                        3, 1700000000000ULL, 4096,
                                        s, 1, 0, 0, 0);
    CHECK(err == FRAME_OK, "n=1 encode should succeed, got %d", (int)err);

    const char *expect = "2008011080d095ffbc3118802020032a0b080012070103020000b844";
    char got[256];
    hex_of(buf, len, got, sizeof(got));
    CHECK(len == 28, "n=1 frame must be 28 bytes, got %zu", len);
    CHECK(strcmp(got, expect) == 0,
          "n=1 anchor mismatch\n  expect=%s\n  got   =%s", expect, got);

    data_batch_decoded_t d;
    CHECK(data_batch_decode(buf, len, &d) == FRAME_OK, "n=1 roundtrip must decode");
    CHECK(d.count == 1, "decoded count must be 1, got %zu", d.count);
    CHECK(d.channel_id == 3, "decoded channel must be 3, got %u", d.channel_id);
    CHECK(d.base_timestamp_us == 1700000000000ULL, "decoded base_ts wrong");
    CHECK(d.first_sequence == 4096, "decoded first_seq wrong");
    CHECK(d.edge_device_id == 0 && d.command_template_id == 0 && d.command_index == 0,
          "n=1 frame must omit optional fields 6/7/8");
    CHECK(d.samples[0].delta_us == 0, "first delta must be 0");
    CHECK(d.samples[0].raw_len == sizeof(kRawA) &&
          memcmp(d.samples[0].raw_data, kRawA, sizeof(kRawA)) == 0,
          "n=1 raw_data must roundtrip");
}

/* ------------------------------------------------------------------ *
 *  T2: n=2 帧（含 6/7/8）
 * ------------------------------------------------------------------ */
static void test_n2_anchor(void)
{
    data_batch_sample_t s[2] = {
        { 0, kRawA, sizeof(kRawA) },
        { 10000, kRawB, sizeof(kRawB) },
    };
    uint8_t buf[128];
    size_t len = 0;
    frame_err_t err = data_batch_encode(buf, sizeof(buf), &len,
                                        3, 1700000000000ULL, 4096,
                                        s, 2, 42, 7, 1);
    CHECK(err == FRAME_OK, "n=2 encode should succeed, got %d", (int)err);

    const char *expect = "2008021080d095ffbc3118802020032a0b080012070103020000b844"
                         "2a0b08904e12060204aabbccdd302a38074001";
    char got[256];
    hex_of(buf, len, got, sizeof(got));
    CHECK(len == 47, "n=2 frame must be 47 bytes, got %zu", len);
    CHECK(strcmp(got, expect) == 0,
          "n=2 anchor mismatch\n  expect=%s\n  got   =%s", expect, got);

    data_batch_decoded_t d;
    CHECK(data_batch_decode(buf, len, &d) == FRAME_OK, "n=2 roundtrip must decode");
    CHECK(d.count == 2, "decoded count must be 2, got %zu", d.count);
    CHECK(d.edge_device_id == 42, "edge_device_id must roundtrip");
    CHECK(d.command_template_id == 7, "command_template_id must roundtrip");
    CHECK(d.command_index == 1, "command_index must roundtrip");
    CHECK(d.samples[0].delta_us == 0, "sample0 delta must be 0");
    CHECK(d.samples[1].delta_us == 10000, "sample1 delta must be 10000");
    CHECK(d.samples[1].raw_len == sizeof(kRawB) &&
          memcmp(d.samples[1].raw_data, kRawB, sizeof(kRawB)) == 0,
          "sample1 raw must roundtrip");
}

/* ------------------------------------------------------------------ *
 *  T3: n=4 满批 + 1024B 上限样本
 * ------------------------------------------------------------------ */
static void test_n4_and_max_raw(void)
{
    static uint8_t big[DATA_BATCH_MAX_RAW];
    for (size_t i = 0; i < sizeof(big); i++) big[i] = (uint8_t)(i * 7 + 3);

    data_batch_sample_t s[4] = {
        { 0, kRawA, sizeof(kRawA) },
        { 10, kRawA, sizeof(kRawA) },
        { 20, kRawB, sizeof(kRawB) },
        { 30, big, sizeof(big) },
    };
    uint8_t buf[4096];
    size_t len = 0;
    frame_err_t err = data_batch_encode(buf, sizeof(buf), &len,
                                        9, 5000, 100, s, 4, 0, 0, 0);
    CHECK(err == FRAME_OK, "n=4 encode should succeed, got %d", (int)err);
    CHECK(len == data_batch_encoded_size(9, 5000, 100, s, 4, 0, 0, 0),
          "encoded_size must predict the exact n=4 length");

    data_batch_decoded_t d;
    CHECK(data_batch_decode(buf, len, &d) == FRAME_OK, "n=4 roundtrip must decode");
    CHECK(d.count == 4, "decoded count must be 4, got %zu", d.count);
    CHECK(d.samples[3].raw_len == DATA_BATCH_MAX_RAW, "1024B raw must roundtrip");
    CHECK(memcmp(d.samples[3].raw_data, big, sizeof(big)) == 0, "1024B raw content");
    /* 契约 §2.2：n=4、300B 样本 ≈1248B；1024B 样本则必然超 1400B 预算，
     * 由 T10 证明会降 n 而不是截断。 */
    CHECK(len > 1024, "n=4 with a 1024B sample must exceed 1024 B (got %zu)", len);
}

/* ------------------------------------------------------------------ *
 *  T4: count 与实际 field5 次数不符 -> 整帧拒绝
 * ------------------------------------------------------------------ */
static void test_count_mismatch_rejected(void)
{
    data_batch_sample_t s[2] = { { 0, kRawA, sizeof(kRawA) },
                                 { 10, kRawB, sizeof(kRawB) } };
    uint8_t buf[128];
    size_t len = 0;
    CHECK(data_batch_encode(buf, sizeof(buf), &len, 3, 1000, 7, s, 2, 0, 0, 0) == FRAME_OK,
          "fixture must encode");

    data_batch_decoded_t d;
    /* count 说 2，实际只有 1 个 sample：把第二个 field5 的 tag 抹掉，
     * 用"删掉最后一个 sample 的整段"来构造。先找到它的起始偏移。 */
    size_t cut = 0;
    {
        /* 重新编一个 n=1 帧，其长度就是 n=2 帧里第一个 sample 结束的位置。 */
        size_t len1 = 0;
        uint8_t tmp[128];
        CHECK(data_batch_encode(tmp, sizeof(tmp), &len1, 3, 1000, 7, s, 1, 0, 0, 0) == FRAME_OK,
              "n=1 fixture must encode");
        cut = len1;
    }
    /* 只保留前 cut 字节，但 count 仍是 2 -> 不一致。 */
    CHECK(data_batch_decode(buf, cut, &d) == FRAME_ERR_INVALID_TAG,
          "count=2 with only 1 sample must be rejected (fail-closed)");

    /* 反向：count 说 1，实际给 2 个 sample。 */
    uint8_t bad[128];
    memcpy(bad, buf, len);
    bad[2] = 0x01; /* field1 (count) 的值为 1；0x08 01 */
    CHECK(data_batch_decode(bad, len, &d) == FRAME_ERR_INVALID_TAG,
          "count=1 with 2 samples must be rejected (fail-closed)");
}

/* ------------------------------------------------------------------ *
 *  T5: count 越界（0 / 5）
 * ------------------------------------------------------------------ */
static void test_count_out_of_range_rejected(void)
{
    data_batch_sample_t s[1] = { { 0, kRawA, sizeof(kRawA) } };
    uint8_t buf[64];
    size_t len = 0;
    CHECK(data_batch_encode(buf, sizeof(buf), &len, 3, 1000, 7, s, 1, 0, 0, 0) == FRAME_OK,
          "fixture must encode");

    data_batch_decoded_t d;
    uint8_t zero[64];
    memcpy(zero, buf, len);
    zero[2] = 0x00;
    CHECK(data_batch_decode(zero, len, &d) == FRAME_ERR_INVALID_TAG,
          "count=0 must be rejected");

    uint8_t five[64];
    memcpy(five, buf, len);
    five[2] = 0x05;
    CHECK(data_batch_decode(five, len, &d) == FRAME_ERR_INVALID_TAG,
          "count=5 must be rejected (max 4)");

    /* 编码器同样拒绝越界 count。 */
    CHECK(data_batch_encode(buf, sizeof(buf), &len, 3, 1000, 7, s, 5, 0, 0, 0) != FRAME_OK,
          "encoder must reject count=5");
    CHECK(data_batch_encode(buf, sizeof(buf), &len, 3, 1000, 7, s, 0, 0, 0, 0) != FRAME_OK,
          "encoder must reject count=0");
}

/* ------------------------------------------------------------------ *
 *  T6: 首样本 delta≠0；delta 非单调
 * ------------------------------------------------------------------ */
static void test_delta_invariants(void)
{
    /* 编码器侧：首样本 delta 非 0 直接拒绝，不产出字节。 */
    data_batch_sample_t bad_first[1] = { { 5, kRawA, sizeof(kRawA) } };
    uint8_t buf[64];
    size_t len = 0;
    CHECK(data_batch_encode(buf, sizeof(buf), &len, 3, 1000, 7, bad_first, 1, 0, 0, 0)
              == FRAME_ERR_INVALID_TAG,
          "encoder must reject first delta != 0");

    data_batch_sample_t bad_mono[3] = {
        { 0, kRawA, sizeof(kRawA) },
        { 100, kRawA, sizeof(kRawA) },
        { 50, kRawA, sizeof(kRawA) },
    };
    CHECK(data_batch_encode(buf, sizeof(buf), &len, 3, 1000, 7, bad_mono, 3, 0, 0, 0)
              == FRAME_ERR_INVALID_TAG,
          "encoder must reject non-monotonic deltas");
    data_batch_sample_t bad_zero[2] = {
        { 0, kRawA, sizeof(kRawA) },
        { 0, kRawA, sizeof(kRawA) },
    };
    CHECK(data_batch_encode(buf, sizeof(buf), &len, 3, 1000, 7, bad_zero, 2, 0, 0, 0)
              == FRAME_ERR_INVALID_TAG,
          "encoder must reject a zero delta on a non-first sample");

    /* 解码器侧：手工构造首样本 delta=5 的帧 -> 必须拒绝。 */
    const uint8_t bad_frame[] = {
        MSG_DATA_BATCH,
        0x08, 0x01,                    /* field1 count=1 */
        0x10, 0xe8, 0x07,              /* field2 base_ts=1000 */
        0x18, 0x07,                    /* field3 first_seq=7 */
        0x20, 0x03,                    /* field4 channel=3 */
        0x2a, 0x07,                    /* field5 len=7 */
        0x08, 0x05,                    /*   sample field1 delta=5  (非法：首样本必须 0) */
        0x12, 0x03, 0x01, 0x02, 0x03,  /*   sample field2 raw=010203 */
    };
    data_batch_decoded_t d;
    CHECK(data_batch_decode(bad_frame, sizeof(bad_frame), &d) == FRAME_ERR_INVALID_TAG,
          "decoder must reject first delta != 0");

    /* 解码器侧：delta 非单调。
     *
     * 注意首样本 delta 被钉死为 0，所以"非单调"至少要 3 个样本才能构造
     * （deltas 0,100,200 -> 把第三个改成 50）。2 个样本时 0 之后的任何
     * 正数都满足单调，这个用例会退化成假绿。 */
    /* 正向对照：deltas = 0,100,200 必须能编能解。 */
    data_batch_sample_t ok_mono[3] = { { 0, kRawA, sizeof(kRawA) },
                                       { 100, kRawB, sizeof(kRawB) },
                                       { 200, kRawB, sizeof(kRawB) } };
    uint8_t frame[128];
    size_t flen = 0;
    CHECK(data_batch_encode(frame, sizeof(frame), &flen, 3, 1000, 7, ok_mono, 3, 0, 0, 0)
              == FRAME_OK, "monotonic 3-sample fixture must encode");
    CHECK(data_batch_decode(frame, flen, &d) == FRAME_OK,
          "monotonic 3-sample frame must decode");

    /* 负向：手工拼一帧 deltas = 0,100,50（第三个回退）。 */
    {
        uint8_t manual[64];
        frame_encoder_t enc;
        frame_encoder_init(&enc, manual, sizeof(manual), MSG_DATA_BATCH);
        frame_encode_varint(&enc, 1, 3);
        frame_encode_varint(&enc, 2, 1000);
        frame_encode_varint(&enc, 3, 7);
        frame_encode_varint(&enc, 4, 3);
        uint8_t body[32];
        frame_encoder_t sub;
        const uint64_t deltas[3] = { 0, 100, 50 };
        for (size_t k = 0; k < 3; k++) {
            frame_encoder_init_sub(&sub, body, sizeof(body));
            frame_encode_varint(&sub, 1, deltas[k]);
            frame_encode_bytes(&sub, 2, kRawA, sizeof(kRawA));
            CHECK(frame_encode_bytes(&enc, 5, frame_encoder_data(&sub),
                                     frame_encoder_size(&sub)) == FRAME_OK,
                  "manual non-monotonic fixture must assemble");
        }
        CHECK(data_batch_decode(manual, frame_encoder_size(&enc), &d)
                  == FRAME_ERR_INVALID_TAG,
              "decoder must reject non-monotonic deltas (0,100,50)");
    }
}

/* ------------------------------------------------------------------ *
 *  T7: 空 raw_data；超 1024B
 * ------------------------------------------------------------------ */
static void test_raw_size_invariants(void)
{
    uint8_t buf[2048];
    size_t len = 0;
    /* 编码器：空 raw 拒绝。 */
    data_batch_sample_t empty[1] = { { 0, kRawA, 0 } };
    CHECK(data_batch_encode(buf, sizeof(buf), &len, 3, 1000, 7, empty, 1, 0, 0, 0)
              == FRAME_ERR_INVALID_TAG,
          "encoder must reject empty raw_data");
    /* 编码器：1025B 拒绝。 */
    static uint8_t over[DATA_BATCH_MAX_RAW + 1];
    data_batch_sample_t too_big[1] = { { 0, over, sizeof(over) } };
    CHECK(data_batch_encode(buf, sizeof(buf), &len, 3, 1000, 7, too_big, 1, 0, 0, 0)
              == FRAME_ERR_INVALID_TAG,
          "encoder must reject raw_data > 1024 B");

    /* 解码器：手工构造空 raw 的帧（sample 子消息只有 delta）。 */
    const uint8_t empty_raw[] = {
        MSG_DATA_BATCH,
        0x08, 0x01,
        0x10, 0xe8, 0x07,
        0x18, 0x07,
        0x20, 0x03,
        0x2a, 0x02, 0x08, 0x00,   /* sample: 只有 field1 delta=0 */
    };
    data_batch_decoded_t d;
    CHECK(data_batch_decode(empty_raw, sizeof(empty_raw), &d) == FRAME_ERR_INVALID_TAG,
          "decoder must reject a sample with no raw_data");

    /* 解码器：raw 长度 0（显式 field2 len=0）。 */
    const uint8_t zero_len_raw[] = {
        MSG_DATA_BATCH,
        0x08, 0x01,
        0x10, 0xe8, 0x07,
        0x18, 0x07,
        0x20, 0x03,
        0x2a, 0x04, 0x08, 0x00, 0x12, 0x00,
    };
    CHECK(data_batch_decode(zero_len_raw, sizeof(zero_len_raw), &d) == FRAME_ERR_INVALID_TAG,
          "decoder must reject raw_data with length 0");

    /* 解码器：raw 长度 1025 -> 拒绝。这里用 frame_codec 的基础原语手工拼帧
     * （不走被测的 data_batch_encode，否则就变成"自己验自己"）。 */
    static uint8_t over_raw[DATA_BATCH_MAX_RAW + 1];
    memset(over_raw, 0x5a, sizeof(over_raw));
    uint8_t big_frame[2048];
    frame_encoder_t enc;
    frame_encoder_init(&enc, big_frame, sizeof(big_frame), MSG_DATA_BATCH);
    frame_encode_varint(&enc, 1, 1);
    frame_encode_varint(&enc, 2, 1000);
    frame_encode_varint(&enc, 3, 7);
    frame_encode_varint(&enc, 4, 3);
    uint8_t body[64];
    frame_encoder_t sub;
    frame_encoder_init_sub(&sub, body, sizeof(body));
    frame_encode_varint(&sub, 1, 0);
    frame_encode_bytes(&sub, 2, over_raw, sizeof(over_raw));
    CHECK(frame_encode_bytes(&enc, 5, frame_encoder_data(&sub),
                             frame_encoder_size(&sub)) == FRAME_OK,
          "fixture assembly must succeed");
    CHECK(data_batch_decode(big_frame, frame_encoder_size(&enc), &d)
              == FRAME_ERR_INVALID_TAG,
          "decoder must reject raw_data > 1024 B");
}

/* ------------------------------------------------------------------ *
 *  T8: 未知字段跳过；已知字段重复 -> 拒绝
 * ------------------------------------------------------------------ */
static void test_unknown_and_duplicate_fields(void)
{
    /* 未知字段（field 9, varint）+ 未知 field（field 10, bytes）插在中间。 */
    const uint8_t with_unknown[] = {
        MSG_DATA_BATCH,
        0x08, 0x01,
        0x48, 0x2a,               /* field 9 varint=42：未知 -> 跳过 */
        0x52, 0x02, 0xde, 0xad,   /* field 10 bytes：未知 -> 跳过 */
        0x10, 0xe8, 0x07,
        0x18, 0x07,
        0x20, 0x03,
        0x2a, 0x0b,
        0x08, 0x00,
        0x12, 0x07, 0x01, 0x03, 0x02, 0x00, 0x00, 0xb8, 0x44,
    };
    data_batch_decoded_t d;
    CHECK(data_batch_decode(with_unknown, sizeof(with_unknown), &d) == FRAME_OK,
          "unknown fields must be skipped (forward compatibility)");
    CHECK(d.count == 1 && d.channel_id == 3, "unknown-field frame must still decode");

    /* 已知字段重复：两个 field1 (count)。 */
    const uint8_t dup_count[] = {
        MSG_DATA_BATCH,
        0x08, 0x01,
        0x08, 0x01,               /* 重复的 count -> 整帧拒绝 */
        0x10, 0xe8, 0x07,
        0x18, 0x07,
        0x20, 0x03,
        0x2a, 0x0b,
        0x08, 0x00,
        0x12, 0x07, 0x01, 0x03, 0x02, 0x00, 0x00, 0xb8, 0x44,
    };
    CHECK(data_batch_decode(dup_count, sizeof(dup_count), &d) == FRAME_ERR_INVALID_TAG,
          "duplicate known top-level field must reject the frame");

    /* 已知子字段重复：sample 内两个 field1 (delta)。 */
    const uint8_t dup_delta[] = {
        MSG_DATA_BATCH,
        0x08, 0x01,
        0x10, 0xe8, 0x07,
        0x18, 0x07,
        0x20, 0x03,
        0x2a, 0x09,
        0x08, 0x00,
        0x08, 0x00,               /* 重复的 delta -> 整帧拒绝 */
        0x12, 0x03, 0x01, 0x02, 0x03,
    };
    CHECK(data_batch_decode(dup_delta, sizeof(dup_delta), &d) == FRAME_ERR_INVALID_TAG,
          "duplicate sample field must reject the frame");

    /* 缺少必填字段（没有 count）-> 拒绝。 */
    const uint8_t missing_count[] = {
        MSG_DATA_BATCH,
        0x10, 0xe8, 0x07,
        0x18, 0x07,
        0x20, 0x03,
        0x2a, 0x0b,
        0x08, 0x00,
        0x12, 0x07, 0x01, 0x03, 0x02, 0x00, 0x00, 0xb8, 0x44,
    };
    CHECK(data_batch_decode(missing_count, sizeof(missing_count), &d) == FRAME_ERR_INVALID_TAG,
          "missing required count must reject the frame");
}

/* ------------------------------------------------------------------ *
 *  T9: 编码器自身拒绝空样本集 / NULL raw
 * ------------------------------------------------------------------ */
static void test_encoder_rejects_invalid_input(void)
{
    uint8_t buf[64];
    size_t len = 0;
    data_batch_sample_t s[1] = { { 0, kRawA, sizeof(kRawA) } };
    CHECK(data_batch_encode(NULL, sizeof(buf), &len, 3, 1, 1, s, 1, 0, 0, 0)
              == FRAME_ERR_INVALID_TAG, "NULL buf must be rejected");
    CHECK(data_batch_encode(buf, sizeof(buf), NULL, 3, 1, 1, s, 1, 0, 0, 0)
              == FRAME_ERR_INVALID_TAG, "NULL out_len must be rejected");
    CHECK(data_batch_encode(buf, sizeof(buf), &len, 3, 1, 1, NULL, 1, 0, 0, 0)
              == FRAME_ERR_INVALID_TAG, "NULL samples must be rejected");
    data_batch_sample_t null_raw[1] = { { 0, NULL, 4 } };
    CHECK(data_batch_encode(buf, sizeof(buf), &len, 3, 1, 1, null_raw, 1, 0, 0, 0)
              == FRAME_ERR_INVALID_TAG, "NULL raw_data with len>0 must be rejected");
}

/* ------------------------------------------------------------------ *
 *  T10: 缓冲不足 -> 降 n（encoded_size 是充分预言，绝不截断）
 * ------------------------------------------------------------------ */
static void test_capacity_prediction_and_downgrade(void)
{
    /* 契约 §2.2：n=4、300B 样本 ≈1248B，落在 report_tx 的 1400B 编码缓冲内。 */
    static uint8_t raw300[300];
    memset(raw300, 0x33, sizeof(raw300));
    data_batch_sample_t s4[4] = {
        { 0, raw300, sizeof(raw300) },
        { 10000, raw300, sizeof(raw300) },
        { 20000, raw300, sizeof(raw300) },
        { 30000, raw300, sizeof(raw300) },
    };
    size_t need4 = data_batch_encoded_size(1, 1000, 1, s4, 4, 0, 0, 0);
    CHECK(need4 > 1200 && need4 < 1400,
          "n=4 x 300B should fit the 1400 B buffer (got %zu)", need4);

    uint8_t buf[1400];
    size_t len = 0;
    CHECK(data_batch_encode(buf, sizeof(buf), &len, 1, 1000, 1, s4, 4, 0, 0, 0)
              == FRAME_OK, "n=4 x 300B must encode into 1400 B");
    CHECK(len == need4, "encoded_size must equal the real frame length (%zu vs %zu)",
          need4, len);

    /* 契约 §2.2：缓冲剩余不足必须**报错**（调用方据此降 n），不得截断。
     * 用 need4-1 的容量编 n=4 必须 OVERFLOW，且不碰缓冲区、不改 out_len。 */
    uint8_t small[1400];
    memset(small, 0xA5, sizeof(small));
    size_t small_len = 12345;
    CHECK(data_batch_encode(small, need4 - 1, &small_len, 1, 1000, 1, s4, 4, 0, 0, 0)
              == FRAME_ERR_OVERFLOW,
          "insufficient capacity must report OVERFLOW, not truncate");
    CHECK(small_len == 12345, "out_len must be untouched when encoding fails");
    CHECK(small[0] == 0xA5 && small[need4 - 2] == 0xA5,
          "encoder must not touch the buffer when it cannot fit");

    /* 降 n 的正确用法：用 encoded_size 预测，挑第一个放得下的 n。 */
    size_t need2 = data_batch_encoded_size(1, 1000, 1, s4, 2, 0, 0, 0);
    size_t need3 = data_batch_encoded_size(1, 1000, 1, s4, 3, 0, 0, 0);
    CHECK(need2 < need3 && need3 < need4, "encoded_size must grow with n");
    CHECK(data_batch_encode(small, need2, &small_len, 1, 1000, 1, s4, 2, 0, 0, 0)
              == FRAME_OK, "downgraded n=2 must encode into its exact size");
    CHECK(small_len == need2, "downgraded frame length must match the prediction");

    /* 单样本 1024B 时 n=4 必然超 1400B：encoded_size 让调用方**提前**知道要
     * 降 n（这正是 bus_worker 在 peek 阶段做的事），而不是编到一半失败。 */
    static uint8_t big[DATA_BATCH_MAX_RAW];
    memset(big, 0x77, sizeof(big));
    data_batch_sample_t sbig[4] = {
        { 0, big, sizeof(big) }, { 10, big, sizeof(big) },
        { 20, big, sizeof(big) }, { 30, big, sizeof(big) },
    };
    size_t need_big = data_batch_encoded_size(1, 1000, 1, sbig, 4, 0, 0, 0);
    CHECK(need_big > 1400,
          "4 x 1024B must exceed the 1400 B report_tx buffer (got %zu)", need_big);
    size_t need_big1 = data_batch_encoded_size(1, 1000, 1, sbig, 1, 0, 0, 0);
    CHECK(need_big1 < 1400, "a single 1024B sample must still fit (got %zu)", need_big1);
}

/* ------------------------------------------------------------------ *
 *  T11: 尺寸预言在**变长路径**上的完备性。
 *
 *  为什么单列这条：T1/T2/T3 的 pred==actual 只覆盖了"n=4、delta 全 1 字节
 *  档"这一个角落。下面三条是评审指出的、当时**证据未覆盖**的变长路径 ——
 *  不补上就只能说"已测范围内精确"，不能说"没有隐藏的变长路径"：
 *    1. count=1..3（此前 n 只测过 1/2/4，缺 3）；
 *    2. delta ∈ [128, 16383]（2 字节 varint）× raw_len > 127（2 字节长度
 *       前缀）的**组合** —— 此前两条向量的 delta 全是 0/10/20/30，全 1 字节；
 *    3. 可选字段的**中间分支**：edge!=0 但 command_index==0（写出 40 00），
 *       此前只测了"全 0 省略"与"全非 0 写出"两端。
 *  三条都断言 data_batch_encoded_size() == 实编长度，且 enc.pos != need 的
 *  防御分支一次都不触发。 */
static void test_size_prediction_variable_length_paths(void)
{
    static uint8_t mid[300];
    static uint8_t short_raw[130];
    memset(mid, 0x33, sizeof(mid));
    memset(short_raw, 0x44, sizeof(short_raw));
    uint8_t buf[4096];

    /* 1) count = 1..3 各自 pred == actual。 */
    for (size_t n = 1; n <= 3; n++) {
        data_batch_sample_t s[DATA_BATCH_MAX_SAMPLES];
        for (size_t i = 0; i < n; i++) {
            s[i].delta_us = (uint64_t)i * 10;
            s[i].raw_data = mid;
            s[i].raw_len = sizeof(mid);
        }
        size_t pred = data_batch_encoded_size(1, 1000, 1, s, n, 0, 0, 0);
        size_t len = 0;
        CHECK(data_batch_encode(buf, sizeof(buf), &len, 1, 1000, 1, s, n, 0, 0, 0) == FRAME_OK,
              "count=%zu must encode", n);
        CHECK(len == pred, "count=%zu: pred(%zu) must equal actual(%zu)", n, pred, len);
        data_batch_decoded_t d;
        CHECK(data_batch_decode(buf, len, &d) == FRAME_OK && d.count == n,
              "count=%zu must roundtrip", n);
    }

    /* 2) delta 2 字节 varint [128,16383] × raw_len 2 字节长度前缀 (>127)。 */
    static const uint64_t deltas[] = { 128, 5000, 16383 };
    for (size_t k = 0; k < sizeof(deltas) / sizeof(deltas[0]); k++) {
        data_batch_sample_t s[2] = {
            { 0, mid, sizeof(mid) },
            { deltas[k], mid, sizeof(mid) },
        };
        CHECK(frame_varint_size(deltas[k]) == 2,
              "delta %llu must occupy exactly 2 varint bytes",
              (unsigned long long)deltas[k]);
        size_t pred = data_batch_encoded_size(1, 1000, 1, s, 2, 0, 0, 0);
        size_t len = 0;
        CHECK(data_batch_encode(buf, sizeof(buf), &len, 1, 1000, 1, s, 2, 0, 0, 0) == FRAME_OK,
              "delta=%llu must encode", (unsigned long long)deltas[k]);
        CHECK(len == pred,
              "delta=%llu x 300B: pred(%zu) must equal actual(%zu)",
              (unsigned long long)deltas[k], pred, len);
        data_batch_decoded_t d;
        CHECK(data_batch_decode(buf, len, &d) == FRAME_OK &&
                  d.samples[1].delta_us == deltas[k],
              "delta=%llu must roundtrip", (unsigned long long)deltas[k]);
    }

    /* 2b) 2 字节长度前缀（raw_len=130）× 2 字节 delta 的组合路径。 */
    {
        data_batch_sample_t s[2] = {
            { 0, short_raw, sizeof(short_raw) },
            { 16383, short_raw, sizeof(short_raw) },
        };
        size_t pred = data_batch_encoded_size(1, 1000, 1, s, 2, 0, 0, 0);
        size_t len = 0;
        CHECK(data_batch_encode(buf, sizeof(buf), &len, 1, 1000, 1, s, 2, 0, 0, 0) == FRAME_OK,
              "raw_len=130 x delta=16383 must encode");
        CHECK(len == pred,
              "raw_len=130 x delta=16383: pred(%zu) must equal actual(%zu)", pred, len);
        data_batch_decoded_t d;
        CHECK(data_batch_decode(buf, len, &d) == FRAME_OK && d.samples[0].raw_len == 130,
              "raw_len=130 must roundtrip");
    }

    /* 3) 可选字段中间分支：edge!=0 而 command_index==0 —— 契约 §2 与
     *    data_report_codec.c 同源的规则会写出 field8=0（字节 40 00）。 */
    {
        data_batch_sample_t s[1] = { { 0, mid, sizeof(mid) } };
        size_t len = 0;
        CHECK(data_batch_encode(buf, sizeof(buf), &len, 1, 1000, 1, s, 1, 42, 0, 0) == FRAME_OK,
              "edge!=0 idx=0 must encode");
        CHECK(len >= 2 && buf[len - 2] == 0x40 && buf[len - 1] == 0x00,
              "edge!=0 & idx==0 must emit field8=0 (40 00), got tail %02x %02x",
              len >= 2 ? buf[len - 2] : 0, len >= 2 ? buf[len - 1] : 0);
        data_batch_decoded_t d;
        CHECK(data_batch_decode(buf, len, &d) == FRAME_OK &&
                  d.edge_device_id == 42 && d.command_index == 0,
              "edge!=0 idx=0 must roundtrip (index present but zero)");

        /* 对照：edge==0 时 field6 与 field8 都不写，恰好短 4 字节
         * （field6 的 tag+varint 2B + field8 的 tag+varint 2B）。 */
        size_t len0 = 0;
        CHECK(data_batch_encode(buf, sizeof(buf), &len0, 1, 1000, 1, s, 1, 0, 0, 0) == FRAME_OK,
              "edge=0 idx=0 must encode");
        CHECK(len0 == len - 4,
              "edge=0 must omit field6+field8 (4B): got %zu vs %zu", len0, len);
    }
}

int main(void)
{
    test_minimal_n1_anchor();
    test_n2_anchor();
    test_n4_and_max_raw();
    test_count_mismatch_rejected();
    test_count_out_of_range_rejected();
    test_delta_invariants();
    test_raw_size_invariants();
    test_unknown_and_duplicate_fields();
    test_encoder_rejects_invalid_input();
    test_capacity_prediction_and_downgrade();
    test_size_prediction_variable_length_paths();

    if (g_failures != 0) {
        fprintf(stderr, "%d/%d checks failed\n", g_failures, g_checks);
        return 1;
    }
    printf("data_batch_codec_tests: all %d checks passed\n", g_checks);
    return 0;
}
