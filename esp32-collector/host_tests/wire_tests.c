/* wire_tests.c —— 16 B 定长头 + 分片重组（设计 §5.1/§5.3）
 *
 * 为什么重写：骨架初版把帧格式定成 varint(len)||payload（**另一种格式**、
 * 且不支持分片）。改为设计 §5.1 的 16 B 头后，旧用例的语义已不适用。
 *
 * 覆盖设计 §5.3 的判定表里可由单侧实现验证的项：
 *   乱序 -> 拒绝整条；总长不符 -> 拒绝整条；越界 -> 不缓冲；
 *   任意字节切分下重组结果一致；错误不粘住。
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "wire.h"

static int s_failures = 0;
#define CHECK(cond, ...)                                                     \
    do {                                                                     \
        if (!(cond)) {                                                       \
            printf("FAIL %s:%d  ", __FILE__, __LINE__);                      \
            printf(__VA_ARGS__);                                             \
            printf("\n");                                                    \
            s_failures++;                                                    \
        }                                                                    \
    } while (0)

/* 造一个帧（头 + 载荷），大端 */
static size_t mkframe(uint8_t *out, uint8_t type, uint16_t flags, uint32_t seq,
                      uint16_t off, uint16_t flen, uint16_t total,
                      const uint8_t *pl)
{
    wire_header_t h = { .ver = WIRE_VER, .type = type, .flags = flags, .seq = seq,
                        .frag_off = off, .frag_len = flen, .total_len = total };
    if (wire_encode_header(out, WIRE_HEADER_BYTES, &h) != WIRE_OK) return 0;
    if (flen > 0 && pl != NULL) memcpy(out + WIRE_HEADER_BYTES, pl, flen);
    return WIRE_HEADER_BYTES + (size_t)flen;
}

/* 1) 单帧 -> 直接产出整条消息 */
static void test_single_frame(void)
{
    uint8_t f[64];
    const uint8_t pl[4] = { 0xde, 0xad, 0xbe, 0xef };
    size_t n = mkframe(f, 0x08, 0, 1, 0, 4, 4, pl);
    CHECK(n == 20, "帧长应为 20，实际 %zu", n);

    reassembler_t *r = reasm_create(WIRE_TOTAL_LEN_MAX);
    CHECK(r != NULL, "reasm_create 失败");
    const uint8_t *out = NULL; size_t olen = 0;
    reasm_result_t res = reasm_push(r, f, n, &out, &olen);
    CHECK(res == REASM_MSG_READY, "应 MSG_READY，实际 %s", reasm_result_name(res));
    CHECK(olen == 4, "长度应为 4，实际 %zu", olen);
    CHECK(out != NULL && memcmp(out, pl, 4) == 0, "载荷不符");
    reasm_destroy(r);
}

/* 2) 两片按序 -> 拼成一条 */
static void test_two_fragments(void)
{
    uint8_t f1[64], f2[64];
    const uint8_t p1[4] = { 1, 2, 3, 4 };
    const uint8_t p2[4] = { 5, 6, 7, 8 };
    size_t n1 = mkframe(f1, 0x08, WIRE_FLAG_MORE, 2, 0, 4, 8, p1);
    size_t n2 = mkframe(f2, 0x08, 0,              2, 4, 4, 8, p2);

    reassembler_t *r = reasm_create(WIRE_TOTAL_LEN_MAX);
    const uint8_t *out = NULL; size_t olen = 0;

    CHECK(reasm_push(r, f1, n1, &out, &olen) == REASM_NEED_MORE, "首片应 NEED_MORE");
    reasm_result_t res = reasm_push(r, f2, n2, &out, &olen);
    CHECK(res == REASM_MSG_READY, "末片应 MSG_READY，实际 %s", reasm_result_name(res));
    CHECK(olen == 8, "长度应为 8，实际 %zu", olen);
    const uint8_t want[8] = { 1, 2, 3, 4, 5, 6, 7, 8 };
    CHECK(out != NULL && memcmp(out, want, 8) == 0, "拼接结果不符");
    reasm_destroy(r);
}

/* 3) 【关键】任意字节切分下重组结果一致（含每字节一次投入） */
static void test_arbitrary_splits(void)
{
    uint8_t f[64];
    const uint8_t pl[4] = { 0xaa, 0xbb, 0xcc, 0xdd };
    size_t n = mkframe(f, 0x08, 0, 7, 0, 4, 4, pl);

    for (size_t k = 1; k <= n; k++) {
        reassembler_t *r = reasm_create(WIRE_TOTAL_LEN_MAX);
        const uint8_t *out = NULL; size_t olen = 0;
        reasm_result_t res = REASM_NEED_MORE;
        for (size_t i = 0; i < n; i += k) {
            size_t chunk = (n - i < k) ? (n - i) : k;
            res = reasm_push(r, f + i, chunk, &out, &olen);
            if (res != REASM_NEED_MORE) break;
        }
        CHECK(res == REASM_MSG_READY, "切分 %zu：应 MSG_READY，实际 %s",
              k, reasm_result_name(res));
        CHECK(olen == 4 && out != NULL && memcmp(out, pl, 4) == 0,
              "切分 %zu：载荷不符", k);
        reasm_destroy(r);
    }
}

/* 4) 乱序 -> 拒绝整条（设计 §5.3：不允许"洞"） */
static void test_out_of_order_rejected(void)
{
    uint8_t f0[64], f4[64];
    const uint8_t p[4] = { 1, 2, 3, 4 };
    size_t n0 = mkframe(f0, 0x08, WIRE_FLAG_MORE, 9, 0, 4, 8, p);
    /* 第二片是【末片】，必须 MORE=0 —— 写成 MORE=1 会被结构校验判为矛盾
     * （end==total 却声称还有后续），mkframe 直接返回 0（第一次就这样写错了）。 */
    size_t n4 = mkframe(f4, 0x08, 0,              9, 4, 4, 8, p);
    CHECK(n0 == 20 && n4 == 20, "构造的帧长应各为 20（n0=%zu n4=%zu）", n0, n4);

    reassembler_t *r = reasm_create(WIRE_TOTAL_LEN_MAX);
    const uint8_t *out = NULL; size_t olen = 0;
    CHECK(reasm_push(r, f0, n0, &out, &olen) == REASM_NEED_MORE, "首片应 NEED_MORE");
    reasm_result_t res = reasm_push(r, f4, n4, &out, &olen);
    /* off=4 但已收 4 字节 ⇒ 这一片其实是"下一片"，属正常顺序。
     * 真正的乱序是：首片就声明 off != 0。 */
    CHECK(res == REASM_MSG_READY, "off=4 在已收 4 字节后应被接受，实际 %s",
          reasm_result_name(res));
    reasm_destroy(r);

    /* 真正的乱序：第一条消息就是 off=8（跳过前缀） */
    reassembler_t *r2 = reasm_create(WIRE_TOTAL_LEN_MAX);
    uint8_t skip[64];
    size_t ns = mkframe(skip, 0x08, WIRE_FLAG_MORE, 9, 8, 4, 16, p);
    res = reasm_push(r2, skip, ns, &out, &olen);
    CHECK(res == REASM_ERROR_OUT_OF_ORDER, "首片 off!=0 应 OUT_OF_ORDER，实际 %s",
          reasm_result_name(res));
    reasm_destroy(r2);
}

/* 5) 越界 -> 不缓冲（P2） */
static void test_too_large_rejected(void)
{
    /* total_len 超构造上界 */
    reassembler_t *r = reasm_create(256);
    uint8_t f[64];
    const uint8_t p[4] = { 1, 2, 3, 4 };
    size_t n = mkframe(f, 0x08, WIRE_FLAG_MORE, 1, 0, 4, 1000, p);
    const uint8_t *out = NULL; size_t olen = 0;
    CHECK(reasm_push(r, f, n, &out, &olen) == REASM_ERROR_TOO_LARGE,
          "超构造上界应 TOO_LARGE");
    reasm_destroy(r);

    /* frag_len > 1024（头级越界）—— 构造一个非法帧 */
    uint8_t bad[WIRE_HEADER_BYTES];
    bad[0] = 0x45; bad[1] = 0x48; bad[2] = WIRE_VER; bad[3] = 0x20;
    bad[4] = 0; bad[5] = 0;                       /* flags */
    memset(bad + 6, 0, 4);                        /* seq */
    bad[10] = 0; bad[11] = 0;                     /* off */
    bad[12] = 0x04; bad[13] = 0x01;               /* frag_len = 0x0401 > 1024 */
    bad[14] = 0x04; bad[15] = 0x01;               /* total */
    reassembler_t *r2 = reasm_create(WIRE_TOTAL_LEN_MAX);
    CHECK(reasm_push(r2, bad, sizeof(bad), &out, &olen) == REASM_ERROR_TOO_LARGE,
          "frag_len>1024 应 TOO_LARGE");
    reasm_destroy(r2);
}

/* 6) 错误不粘住：出错后下一次 push 能正常产出 */
static void test_error_is_not_sticky(void)
{
    reassembler_t *r = reasm_create(WIRE_TOTAL_LEN_MAX);
    const uint8_t *out = NULL; size_t olen = 0;

    uint8_t bad[WIRE_HEADER_BYTES];
    memset(bad, 0, sizeof(bad));
    bad[0] = 0x00; bad[1] = 0x00;   /* magic 错 */
    (void)reasm_push(r, bad, sizeof(bad), &out, &olen);

    uint8_t f[64];
    const uint8_t pl[4] = { 0x11, 0x22, 0x33, 0x44 };
    size_t n = mkframe(f, 0x08, 0, 1, 0, 4, 4, pl);
    CHECK(reasm_push(r, f, n, &out, &olen) == REASM_MSG_READY,
          "错误后应能恢复");
    CHECK(olen == 4 && memcmp(out, pl, 4) == 0, "恢复后载荷不符");
    reasm_destroy(r);
}

/* 7) 头级校验：magic/ver/结构 */
static void test_header_validation(void)
{
    wire_header_t h = { .ver = WIRE_VER, .type = 1, .flags = 0, .seq = 1,
                        .frag_off = 0, .frag_len = 4, .total_len = 4 };
    CHECK(wire_check_structure(&h) == WIRE_OK, "合法头应 OK");

    h.frag_len = WIRE_FRAG_LEN_MAX + 1;
    CHECK(wire_check_structure(&h) == WIRE_ERR_RANGE, "frag_len 超限应 RANGE");

    h.frag_len = 4; h.total_len = WIRE_TOTAL_LEN_MAX + 1;
    CHECK(wire_check_structure(&h) == WIRE_ERR_RANGE, "total_len 超限应 RANGE");

    /* 末片没到末尾 */
    h.total_len = 8; h.frag_len = 4; h.frag_off = 0;
    CHECK(wire_check_structure(&h) == WIRE_ERR_STRUCTURE, "末片未到末尾应 STRUCTURE");

    /* MORE=1 却已到末尾：end(8) >= total(8) ⇒ 自相矛盾 */
    h.total_len = 8; h.frag_len = 8; h.frag_off = 0; h.flags = WIRE_FLAG_MORE;
    CHECK(wire_check_structure(&h) == WIRE_ERR_STRUCTURE, "MORE 矛盾应 STRUCTURE");
    /* 对照：MORE=1 且 end < total 是合法的 */
    h.total_len = 16; h.frag_len = 8; h.frag_off = 0;
    CHECK(wire_check_structure(&h) == WIRE_OK, "MORE=1 且未到末尾应 OK");

    uint8_t buf[WIRE_HEADER_BYTES];
    h.flags = 0; h.frag_off = 0; h.frag_len = 4; h.total_len = 4;
    CHECK(wire_encode_header(buf, sizeof(buf), &h) == WIRE_OK, "编码应 OK");
    CHECK(buf[0] == 0x45 && buf[1] == 0x48, "magic 应为 4548");
    CHECK(buf[2] == WIRE_VER, "ver 应为 0x30");

    wire_header_t g = {0};
    CHECK(wire_decode_header(buf, sizeof(buf), &g) == WIRE_OK, "解码应 OK");
    CHECK(g.type == 1 && g.seq == 1 && g.total_len == 4, "往返字段不符");
}

int main(void)
{
    test_single_frame();
    test_two_fragments();
    test_arbitrary_splits();
    test_out_of_order_rejected();
    test_too_large_rejected();
    test_error_is_not_sticky();
    test_header_validation();
    if (s_failures) { printf("wire_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("wire_tests: all checks passed\n");
    return 0;
}
