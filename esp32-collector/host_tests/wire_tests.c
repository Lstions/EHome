/* wire_tests.c —— 定界/重组（设计文档 1.3；治 D-09）
 * 核心断言：**任意切分点**下重组结果一致 —— 这正是"一次 recv = 一条消息"
 * 这个错误前提的根治证明。 */
#include "wire.h"

#include <stdio.h>
#include <string.h>

static int s_failures = 0;
#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond); s_failures++; } \
    } while (0)

/* 1) 一帧完整投入 */
static void test_single_frame(void)
{
    reassembler_t *r = reasm_create(1024);
    uint8_t buf[64];
    size_t n = 0;
    CHECK(wire_encode_len_prefix(buf, sizeof(buf), &n, 5));
    memcpy(buf + n, "HELLO", 5);

    const uint8_t *f = NULL; size_t fl = 0;
    CHECK(reasm_push(r, buf, n + 5, &f, &fl) == REASM_FRAME_READY);
    CHECK(fl == 5);
    CHECK(f != NULL && memcmp(f, "HELLO", 5) == 0);
    reasm_destroy(r);
}

/* 2) 【关键】任意切分点重组结果一致（含每字节一次投入） */
static void test_arbitrary_splits(void)
{
    uint8_t wire[64];
    size_t hdr = 0;
    const char *msg = "ABCDEFGHIJ";
    CHECK(wire_encode_len_prefix(wire, sizeof(wire), &hdr, 10));
    memcpy(wire + hdr, msg, 10);
    size_t total = hdr + 10;

    /* 对每个可能的"每 k 字节投一次"都验一遍 */
    for (size_t k = 1; k <= total; k++) {
        reassembler_t *r = reasm_create(1024);
        const uint8_t *f = NULL; size_t fl = 0;
        int got = 0;
        for (size_t off = 0; off < total; off += k) {
            size_t chunk = (total - off < k) ? (total - off) : k;
            reasm_result_t res = reasm_push(r, wire + off, chunk, &f, &fl);
            if (res == REASM_FRAME_READY) {
                got = 1;
                CHECK(fl == 10);
                CHECK(memcmp(f, msg, 10) == 0);
            } else {
                CHECK(res == REASM_NEED_MORE);
            }
        }
        CHECK(got == 1);   /* 每个切分点都必须恰好产出一帧 */
        reasm_destroy(r);
    }
}

/* 3) 一次投入含多帧 -> 需要多次 push 消化；顺序正确 */
static void test_multiple_frames_sequential(void)
{
    reassembler_t *r = reasm_create(1024);
    uint8_t wire[64]; size_t off = 0;
    size_t u = 0;
    CHECK(wire_encode_len_prefix(wire + off, sizeof(wire) - off, &u, 3)); off += u;
    memcpy(wire + off, "AAA", 3); off += 3;
    CHECK(wire_encode_len_prefix(wire + off, sizeof(wire) - off, &u, 3)); off += u;
    memcpy(wire + off, "BBB", 3); off += 3;

    const uint8_t *f = NULL; size_t fl = 0;
    /* 全部一次性投入：第一帧就绪，第二帧留在缓冲里 */
    CHECK(reasm_push(r, wire, off, &f, &fl) == REASM_FRAME_READY);
    CHECK(fl == 3 && memcmp(f, "AAA", 3) == 0);
    /* 再 push 0 字节即可取出第二帧（不需要新数据） */
    CHECK(reasm_push(r, wire, 0, &f, &fl) == REASM_FRAME_READY);
    CHECK(fl == 3 && memcmp(f, "BBB", 3) == 0);
    reasm_destroy(r);
}

/* 4) 超限【立即拒绝】，且长度前缀一读出来就拒绝（不缓冲整个超限消息） */
static void test_too_large_rejected_early(void)
{
    reassembler_t *r = reasm_create(100);
    uint8_t wire[16]; size_t n = 0;
    CHECK(wire_encode_len_prefix(wire, sizeof(wire), &n, 101));  /* 101 > 100 */
    const uint8_t *f = NULL; size_t fl = 0;
    /* 只投长度前缀就应该报错 —— 证明"不缓冲" */
    CHECK(reasm_push(r, wire, n, &f, &fl) == REASM_ERROR_TOO_LARGE);
    /* 错误不粘住：随后一个合法帧能正常通过 */
    uint8_t ok[16]; size_t n2 = 0;
    CHECK(wire_encode_len_prefix(ok, sizeof(ok), &n2, 2));
    ok[n2] = 'H'; ok[n2 + 1] = 'I';
    CHECK(reasm_push(r, ok, n2 + 2, &f, &fl) == REASM_FRAME_READY);
    CHECK(fl == 2 && memcmp(f, "HI", 2) == 0);
    reasm_destroy(r);
}

/* 5) 长度恰好等于上界：允许（边界） */
static void test_exact_max_allowed(void)
{
    reassembler_t *r = reasm_create(8);
    uint8_t wire[16]; size_t n = 0;
    CHECK(wire_encode_len_prefix(wire, sizeof(wire), &n, 8));
    memset(wire + n, 0x5A, 8);
    const uint8_t *f = NULL; size_t fl = 0;
    CHECK(reasm_push(r, wire, n + 8, &f, &fl) == REASM_FRAME_READY);
    CHECK(fl == 8);
    reasm_destroy(r);
}

/* 6) 非法 varint（5 字节以上且未终止）-> MALFORMED，不"尽量解析" */
static void test_malformed_varint(void)
{
    reassembler_t *r = reasm_create(100);
    uint8_t bad[6] = {0x80, 0x80, 0x80, 0x80, 0x80, 0x01};
    const uint8_t *f = NULL; size_t fl = 0;
    CHECK(reasm_push(r, bad, sizeof(bad), &f, &fl) == REASM_ERROR_MALFORMED);
    reasm_destroy(r);
}

/* 7) 零长度 payload 合法 */
static void test_zero_length_frame(void)
{
    reassembler_t *r = reasm_create(10);
    uint8_t wire[4]; size_t n = 0;
    CHECK(wire_encode_len_prefix(wire, sizeof(wire), &n, 0));
    const uint8_t *f = NULL; size_t fl = 0;
    CHECK(reasm_push(r, wire, n, &f, &fl) == REASM_FRAME_READY);
    CHECK(fl == 0);
    reasm_destroy(r);
}

/* 8) 构造参数校验：0 上界拒绝（不兜底） */
static void test_create_validation(void)
{
    CHECK(reasm_create(0) == NULL);
    CHECK(reasm_create(1) != NULL);
}

/* 9) 长度前缀编码：容量不足【不截断】 */
static void test_prefix_no_truncation(void)
{
    uint8_t out[1]; size_t used = 0;
    CHECK(wire_encode_len_prefix(out, 1, &used, 200) == false);  /* 200 需要 2 字节 */
    CHECK(wire_encode_len_prefix(out, 1, &used, 5) == true);
    CHECK(used == 1 && out[0] == 5);
}

int main(void)
{
    test_single_frame();
    test_arbitrary_splits();
    test_multiple_frames_sequential();
    test_too_large_rejected_early();
    test_exact_max_allowed();
    test_malformed_varint();
    test_zero_length_frame();
    test_create_validation();
    test_prefix_no_truncation();
    if (s_failures) { printf("wire_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("wire_tests: all checks passed\n");
    return 0;
}
