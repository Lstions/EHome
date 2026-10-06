/* msgcodec_tests.c —— 编解码原语（原则 P2/P7） */
#include "msgcodec.h"

#include <stdio.h>
#include <string.h>

static int s_failures = 0;
#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond); s_failures++; } \
    } while (0)

/* 1) varint 往返（含边界值） */
static void test_varint_roundtrip(void)
{
    const uint64_t vals[] = { 0, 1, 127, 128, 300, 16383, 16384,
                              0xFFFFFFFFull, 0xFFFFFFFFFFFFFFFFull };
    for (size_t i = 0; i < sizeof(vals)/sizeof(vals[0]); i++) {
        uint8_t buf[16]; size_t used = 0;
        CHECK(enc_varint(buf, sizeof(buf), &used, vals[i]) == ENC_OK);
        CHECK(used > 0 && used <= 10);
        size_t back = 0; uint64_t v = 0;
        CHECK(dec_varint(buf, used, &back, &v) == DEC_OK);
        CHECK(v == vals[i]);
        CHECK(back == used);
    }
}

/* 2) 【P2】容量不足必须返回 NO_SPACE，且不写半个 varint */
static void test_varint_no_space(void)
{
    uint8_t buf[1];
    size_t used = 123;
    CHECK(enc_varint(buf, 1, &used, 300) == ENC_NO_SPACE);  /* 300 需 2 字节 */
    CHECK(used == 123);                                     /* 未改动出参 */
}

/* 3) 不完整输入 -> TRUNCATED（不是 BAD_WIRE） */
static void test_varint_truncated(void)
{
    uint8_t buf[2] = {0x80, 0x80};   /* 都带继续位 */
    size_t used = 0; uint64_t v = 0;
    CHECK(dec_varint(buf, 2, &used, &v) == DEC_TRUNCATED);
}

/* 4) 超长 varint -> BAD_WIRE（不"尽量解析"） */
static void test_varint_overlong(void)
{
    uint8_t buf[11] = {0x80,0x80,0x80,0x80,0x80,0x80,0x80,0x80,0x80,0x80,0x01};
    size_t used = 0; uint64_t v = 0;
    CHECK(dec_varint(buf, sizeof(buf), &used, &v) == DEC_BAD_WIRE);
}

/* 5) 字段往返：u64 与 bytes */
static void test_field_roundtrip(void)
{
    uint8_t buf[64]; size_t off = 0, u = 0;
    /* 注意：每个字段必须写在 buf + off（上一版测试漏了 +off，把前一个字段覆盖了） */
    CHECK(enc_field_u64(buf + off, sizeof(buf) - off, &u, 1, 12345) == ENC_OK); off += u;
    CHECK(enc_field_bytes(buf + off, sizeof(buf) - off, &u, 2, "hello", 5) == ENC_OK); off += u;
    CHECK(enc_field_u64(buf + off, sizeof(buf) - off, &u, 3, 0) == ENC_OK); off += u;

    size_t cur = 0;
    field_view_t f;
    uint64_t v = 0;

    CHECK(dec_next_field(buf, off, &cur, &f) == DEC_OK);
    CHECK(f.field_id == 1);
    CHECK(dec_field_u64(&f, &v) == DEC_OK && v == 12345);

    CHECK(dec_next_field(buf, off, &cur, &f) == DEC_OK);
    CHECK(f.field_id == 2 && f.value_len == 5 && memcmp(f.value, "hello", 5) == 0);

    CHECK(dec_next_field(buf, off, &cur, &f) == DEC_OK);
    CHECK(f.field_id == 3);
    CHECK(dec_field_u64(&f, &v) == DEC_OK && v == 0);

    /* 没有更多字段 -> TRUNCATED 且 cursor 不变 */
    size_t before = cur;
    CHECK(dec_next_field(buf, off, &cur, &f) == DEC_TRUNCATED);
    CHECK(cur == before);
}

/* 6) 【P2】字段容量不足 -> NO_SPACE，不截断
 * 注意：bytes 与 u64 两条路径【都要】覆盖 ——
 * 变异自证时曾发现只测了 bytes，u64 的容量检查无人看管。 */
static void test_field_no_space(void)
{
    uint8_t small[3]; size_t u = 0;
    /* bytes: field_id(1) + len(1) + "hello"(5) = 7 > 3 */
    CHECK(enc_field_bytes(small, sizeof(small), &u, 2, "hello", 5) == ENC_NO_SPACE);

    /* u64: field_id(1) + len(1) + varint(300)=2 = 4，cap=3 必须拒绝 */
    uint8_t u3[3]; size_t uu = 0;
    CHECK(enc_field_u64(u3, sizeof(u3), &uu, 1, 300) == ENC_NO_SPACE);
    /* 恰好放下（cap=4）允许 —— 边界 */
    uint8_t u4[4]; size_t uu4 = 0;
    CHECK(enc_field_u64(u4, sizeof(u4), &uu4, 1, 300) == ENC_OK);
    CHECK(uu4 == 4);

    /* bytes 恰好放下 */
    uint8_t b7[7]; size_t ub = 0;
    CHECK(enc_field_bytes(b7, sizeof(b7), &ub, 2, "hello", 5) == ENC_OK);
    CHECK(ub == 7);
}

/* 7) 字段号 0 非法（编码与解码对称拒绝） */
static void test_field_id_zero_rejected(void)
{
    uint8_t buf[8]; size_t u = 0;
    CHECK(enc_field_u64(buf, sizeof(buf), &u, 0, 1) == ENC_BAD_ARG);

    uint8_t bad[2] = { 0x00, 0x00 };   /* field_id=0 */
    size_t cur = 0; field_view_t f;
    CHECK(dec_next_field(bad, sizeof(bad), &cur, &f) == DEC_BAD_WIRE);
}

/* 8) u64 字段值是"多余字节" -> BAD_WIRE（编码方违约，不尽量解析） */
static void test_u64_field_with_trailing_bytes(void)
{
    uint8_t buf[8]; size_t off = 0, u = 0;
    /* 手工造一个 field_id=1、len=2、值为 [0x05,0x00] 的字段 */
    CHECK(enc_field_bytes(buf, sizeof(buf), &u, 1, "\x05\x00", 2) == ENC_OK); off = u;
    size_t cur = 0; field_view_t f; uint64_t v = 0;
    CHECK(dec_next_field(buf, off, &cur, &f) == DEC_OK);
    CHECK(dec_field_u64(&f, &v) == DEC_BAD_WIRE);
}

/* 9) 截断的字段值 -> TRUNCATED */
static void test_truncated_field(void)
{
    uint8_t buf[3] = { 0x02, 0x05, 'h' };   /* 声称 len=5，只有 1 字节 */
    size_t cur = 0; field_view_t f;
    CHECK(dec_next_field(buf, sizeof(buf), &cur, &f) == DEC_TRUNCATED);
}

int main(void)
{
    test_varint_roundtrip();
    test_varint_no_space();
    test_varint_truncated();
    test_varint_overlong();
    test_field_roundtrip();
    test_field_no_space();
    test_field_id_zero_rejected();
    test_u64_field_with_trailing_bytes();
    test_truncated_field();
    if (s_failures) { printf("msgcodec_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("msgcodec_tests: all checks passed\n");
    return 0;
}
