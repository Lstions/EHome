/* wire_tests.c —— 3.0 帧定界（delimiting）
 *
 * 用户决策（2026-10-06）：**不需要分片与重组，需要的是分界。**
 * 旧版这一整套 reasm_* 测试（乱序 / 丢片 / 重复 / 孔洞）随分片一起删除 ——
 * 它们防的状态在 TCP 上不会发生，属"永不被生产流量检验"的代码。
 *
 * 本文件锁住的是**定界**这件事本身：字节流 → 语义完整的消息。
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
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

/* NULL 安全的载荷比较。
 * 为什么必须 NULL 安全：若 feed 没有返回 MSG_READY，out 会保持 NULL；
 * 直接 memcmp(NULL, ...) 会**段错误**，于是"测试失败"退化成"进程崩溃" ——
 * 变异自证里这会让判定从"断言抓到"变成"崩了"，既不是清晰信号，
 * 也会把真正的失败信息吞掉（stdout 缓冲随崩溃丢失）。
 * 这条是变异 M63 暴露出来的：M63 确实触发了大量断言，但先崩了。 */
static bool payload_eq(const uint8_t *got, size_t got_len,
                      const uint8_t *want, size_t want_len)
{
    if (got == NULL) return false;
    if (got_len != want_len) return false;
    return memcmp(got, want, want_len) == 0;
}

/* 构造一条完整消息（头 + 载荷 [+ CRC]） */
static size_t mkmsg(uint8_t *out, size_t cap, uint8_t type, uint16_t flags,
                    uint32_t seq, const uint8_t *payload, uint16_t plen)
{
    wire_header_t h = { .ver = WIRE_VER, .type = type, .flags = flags,
                        .seq = seq, .payload_len = plen };
    if (wire_encode_header(out, cap, &h) != WIRE_OK) return 0;
    if ((size_t)WIRE_HEADER_BYTES + plen > cap) return 0;
    if (plen > 0) memcpy(out + WIRE_HEADER_BYTES, payload, plen);
    size_t n = (size_t)WIRE_HEADER_BYTES + plen;
    if (flags & WIRE_FLAG_CRC32C) {
        uint32_t c = wire_crc32c(payload, plen);
        out[n + 0] = (uint8_t)(c >> 24); out[n + 1] = (uint8_t)(c >> 16);
        out[n + 2] = (uint8_t)(c >> 8);  out[n + 3] = (uint8_t)(c);
        n += 4;
    }
    return n;
}

/* ============ 1. CRC32C 用【业界标准校验值】锚定 ============
 * 不自己算一个值当基准 —— 那样多项式写错也会"自洽通过"。
 * CRC32C("123456789") = 0xE3069283 是公开的标准 check value。 */
static void test_crc32c_check_value(void)
{
    const uint8_t *v = (const uint8_t *)"123456789";
    uint32_t got = wire_crc32c(v, 9);
    CHECK(got == 0xE3069283u,
          "CRC32C(\"123456789\") 应为 0xE3069283，实际 0x%08X", got);
    CHECK(wire_crc32c(v, 0) == 0u, "空输入 CRC 应为 0");
}

/* ============ 2. 帧头：大端布局逐字节核对 ============
 * 设计未规定字节序，本实现定为大端。这条测试是**唯一**的字节序事实来源，
 * 后端与任何其他语言实现都必须与 protocol/vectors/frame_header.txt 对齐。 */
static void test_header_is_big_endian(void)
{
    uint8_t buf[WIRE_HEADER_BYTES];
    wire_header_t h = { .ver = WIRE_VER, .type = 0x20, .flags = 0x0003,
                        .seq = 0x12345678u, .payload_len = 0x00A5 };
    CHECK(wire_encode_header(buf, sizeof(buf), &h) == WIRE_OK, "编码应 OK");

    /* magic 大端 0x4548 => 45 48 */
    CHECK(buf[0] == 0x45 && buf[1] == 0x48, "magic 应为 45 48，实际 %02x %02x",
          buf[0], buf[1]);
    CHECK(buf[2] == 0x30, "ver 应为 30");
    CHECK(buf[3] == 0x20, "type 应为 20");
    /* flags 大端 */
    CHECK(buf[4] == 0x00 && buf[5] == 0x03, "flags 应为 00 03，实际 %02x %02x",
          buf[4], buf[5]);
    /* seq 大端：0x12345678 => 12 34 56 78（小端实现会写成 78 56 34 12） */
    CHECK(buf[6] == 0x12 && buf[7] == 0x34 && buf[8] == 0x56 && buf[9] == 0x78,
          "seq 应为大端 12 34 56 78，实际 %02x %02x %02x %02x",
          buf[6], buf[7], buf[8], buf[9]);
    CHECK(buf[10] == 0x00 && buf[11] == 0xA5, "payload_len 应为大端 00 a5");

    /* 解回来必须一致 */
    wire_header_t d;
    CHECK(wire_decode_header(buf, sizeof(buf), &d) == WIRE_OK, "解码应 OK");
    CHECK(d.type == 0x20 && d.flags == 0x0003 && d.seq == 0x12345678u &&
          d.payload_len == 0x00A5, "往返应一致");
}

/* ============ 3. 头校验：magic / ver / 上界 ============ */
static void test_header_validation(void)
{
    uint8_t buf[WIRE_HEADER_BYTES];
    wire_header_t h = { .ver = WIRE_VER, .type = 1, .flags = 0, .seq = 1, .payload_len = 0 };
    CHECK(wire_encode_header(buf, sizeof(buf), &h) == WIRE_OK, "编码应 OK");

    wire_header_t d;
    CHECK(wire_decode_header(buf, WIRE_HEADER_BYTES - 1, &d) == WIRE_ERR_SHORT,
          "字节不足应为 SHORT（不是错误，是需要更多数据）");

    uint8_t bad[WIRE_HEADER_BYTES];
    memcpy(bad, buf, sizeof(bad)); bad[0] = 0x00;
    CHECK(wire_decode_header(bad, sizeof(bad), &d) == WIRE_ERR_MAGIC, "magic 错应 ERR_MAGIC");
    memcpy(bad, buf, sizeof(bad)); bad[2] = 0x99;
    CHECK(wire_decode_header(bad, sizeof(bad), &d) == WIRE_ERR_VERSION, "ver 错应 ERR_VERSION");

    /* 上界：16369 超上界，16368 恰好合法 */
    memcpy(bad, buf, sizeof(bad)); bad[10] = 0x3F; bad[11] = 0xF1;   /* 16369 */
    CHECK(wire_decode_header(bad, sizeof(bad), &d) == WIRE_ERR_RANGE, "超上界应 ERR_RANGE");
    memcpy(bad, buf, sizeof(bad)); bad[10] = 0x3F; bad[11] = 0xF0;   /* 16368 */
    CHECK(wire_decode_header(bad, sizeof(bad), &d) == WIRE_OK, "恰为上界应 OK");
}

/* ============ 4. 定界：一条消息 + 任意切分 ============
 * 这是"字节流 → 消息"最核心的性质：**怎么切分都不影响结果**。
 * 旧的分片/重组做不到"任意切分"（它要求片边界与 frag_off 对齐）——
 * 而定界天然做到，因为边界由 payload_len 给出，与到达方式无关。 */
static void test_delim_arbitrary_splits(void)
{
    uint8_t pl[37];
    for (int i = 0; i < 37; i++) pl[i] = (uint8_t)(i * 7 + 3);
    uint8_t msg[128];
    size_t n = mkmsg(msg, sizeof(msg), 0x03, 0, 0xABCD, pl, sizeof(pl));
    CHECK(n > 0, "构造消息失败");

    int ready = 0;
    for (size_t chunk = 1; chunk <= n; chunk++) {
        wire_delim_t *d = wire_delim_create(WIRE_PAYLOAD_MAX);
        CHECK(d != NULL, "创建失败");
        const uint8_t *out = NULL; size_t olen = 0;
        wire_delim_result_t r = WIRE_DELIM_NEED_MORE;
        for (size_t i = 0; i < n; i += chunk) {
            size_t c = (n - i < chunk) ? (n - i) : chunk;
            r = wire_delim_feed(d, msg + i, c, &out, &olen);
            if (r == WIRE_DELIM_MSG_READY) break;
        }
        if (r == WIRE_DELIM_MSG_READY) {
            ready++;
            CHECK(olen == sizeof(pl), "chunk=%zu 载荷长度应为 37，实际 %zu", chunk, olen);
            CHECK(payload_eq(out, olen, pl, sizeof(pl)),
                  "chunk=%zu 载荷内容应一致", chunk);
        }
        wire_delim_destroy(d);
    }
    CHECK(ready == (int)n, "所有切分（1..%zu）都应成功，实际成功 %d 次", n, ready);
}

/* ============ 5. 定界：一次投入含多条消息 ============
 * 旧实现把"一次 recv"当"一条消息"（D-09）—— 那种假设在这里直接爆掉。 */
static void test_delim_stops_at_message_boundary(void)
{
    uint8_t a[5] = {1,2,3,4,5}, b[3] = {9,8,7};
    uint8_t buf[128];
    size_t na = mkmsg(buf, sizeof(buf), 0x03, 0, 1, a, sizeof(a));
    size_t nb = mkmsg(buf + na, sizeof(buf) - na, 0x20, 0, 2, b, sizeof(b));

    wire_delim_t *d = wire_delim_create(WIRE_PAYLOAD_MAX);
    const uint8_t *out = NULL; size_t olen = 0;

    /* 第一次：只能产出第一条，且**不越过**边界 */
    CHECK(wire_delim_feed(d, buf, na + nb, &out, &olen) == WIRE_DELIM_MSG_READY,
          "应产出第一条");
    CHECK(payload_eq(out, olen, a, sizeof(a)),
          "第一条内容应为 a（不得把 b 也吞进来）");

    /* 提一句：out 指向内部缓冲，下一次 feed 前有效 —— 先把它拷出来再 feed */
    uint8_t first[5];
    memcpy(first, out, olen);

    /* 第二次：剩余字节已缓冲，直接产出第二条 */
    CHECK(wire_delim_feed(d, NULL, 0, &out, &olen) == WIRE_DELIM_MSG_READY,
          "应产出第二条");
    CHECK(payload_eq(out, olen, b, sizeof(b)), "第二条内容应为 b");
    CHECK(memcmp(first, a, sizeof(a)) == 0, "第一条内容不应被后续 feed 破坏（已拷出）");

    CHECK(wire_delim_feed(d, NULL, 0, &out, &olen) == WIRE_DELIM_NEED_MORE,
          "第三条不存在，应 NEED_MORE");
    wire_delim_destroy(d);
}

/* ============ 6. 零拷贝契约：payload 在下一次 feed 前有效 ============ */
static void test_payload_lifetime_contract(void)
{
    uint8_t pl[8] = {0xAA,0xBB,0xCC,0xDD,0xEE,0xFF,0x11,0x22};
    uint8_t buf[64];
    size_t n = mkmsg(buf, sizeof(buf), 0x03, 0, 1, pl, sizeof(pl));

    wire_delim_t *d = wire_delim_create(WIRE_PAYLOAD_MAX);
    const uint8_t *out = NULL; size_t olen = 0;
    CHECK(wire_delim_feed(d, buf, n, &out, &olen) == WIRE_DELIM_MSG_READY, "应 READY");
    /* 契约为"下一次 feed 前有效" —— 此刻读取必须正确 */
    CHECK(payload_eq(out, olen, pl, sizeof(pl)), "刚返回时载荷必须有效（零拷贝）");
    /* 下一次 feed 之后，同一指针【不保证】仍有旧内容 —— 契约如此，不做断言 */
    (void)wire_delim_feed(d, NULL, 0, &out, &olen);
    wire_delim_destroy(d);
}

/* ============ 7. 坏头：拒绝并重新同步 ============ */
static void test_bad_header_rejected_then_resync(void)
{
    wire_delim_t *d = wire_delim_create(WIRE_PAYLOAD_MAX);
    const uint8_t *out = NULL; size_t olen = 0;

    uint8_t junk[WIRE_HEADER_BYTES] = {0};   /* magic = 0000，非法 */
    CHECK(wire_delim_feed(d, junk, sizeof(junk), &out, &olen) == WIRE_DELIM_ERR_MALFORMED,
          "坏 magic 应 ERR_MALFORMED");

    /* 错误不粘住：下一条合法消息必须能正常产出 */
    uint8_t pl[4] = {7,7,7,7};
    uint8_t good[64];
    size_t n = mkmsg(good, sizeof(good), 0x03, 0, 5, pl, sizeof(pl));
    CHECK(wire_delim_feed(d, good, n, &out, &olen) == WIRE_DELIM_MSG_READY,
          "出错后应能恢复（错误不粘住）");
    CHECK(payload_eq(out, olen, pl, sizeof(pl)), "恢复后内容应正确");
    wire_delim_destroy(d);
}

/* ============ 8. 声称长度超上界：**拒绝且不缓冲** ============
 * 这是防"按对端声明分配内存"的关键：对端说 60000，我们不能先缓冲再说。 */
static void test_declared_too_large_is_not_buffered(void)
{
    wire_delim_t *d = wire_delim_create(256);   /* 本实例上界故意远小于全局上界 */
    CHECK(d != NULL, "创建失败");
    const uint8_t *out = NULL; size_t olen = 0;

    uint8_t hdr[WIRE_HEADER_BYTES];
    wire_header_t h = { .ver = WIRE_VER, .type = 1, .flags = 0, .seq = 1, .payload_len = 900 };
    CHECK(wire_encode_header(hdr, sizeof(hdr), &h) == WIRE_OK, "编码应 OK");
    CHECK(wire_delim_feed(d, hdr, sizeof(hdr), &out, &olen) == WIRE_DELIM_ERR_TOO_LARGE,
          "超本实例上界应 ERR_TOO_LARGE");

    /* 全局上界之外（16369）也必须在【头解析】阶段就被拒 */
    h.payload_len = 16369;
    CHECK(wire_encode_header(hdr, sizeof(hdr), &h) == WIRE_ERR_RANGE,
          "编码器应拒绝超全局上界的长度");

    /* 即使伪造一个超上界的头放进来，解码也必须拒 */
    hdr[10] = 0x3F; hdr[11] = 0xF1;
    CHECK(wire_delim_feed(d, hdr, sizeof(hdr), &out, &olen) == WIRE_DELIM_ERR_TOO_LARGE,
          "超全局上界应 ERR_TOO_LARGE");
    wire_delim_destroy(d);
}

/* ============ 9. CRC：好则过、坏则拒 ============ */
static void test_crc_checked_when_flag_set(void)
{
    uint8_t pl[6] = {1,2,3,4,5,6};
    uint8_t buf[64];
    size_t n = mkmsg(buf, sizeof(buf), 0x20, WIRE_FLAG_CRC32C, 9, pl, sizeof(pl));

    wire_delim_t *d = wire_delim_create(WIRE_PAYLOAD_MAX);
    const uint8_t *out = NULL; size_t olen = 0;
    CHECK(wire_delim_feed(d, buf, n, &out, &olen) == WIRE_DELIM_MSG_READY,
          "CRC 正确应 READY");
    CHECK(olen == sizeof(pl), "载荷长度应正确");

    /* 篡改 CRC（最后一个字节） */
    buf[n - 1] ^= 0xFF;
    CHECK(wire_delim_feed(d, buf, n, &out, &olen) == WIRE_DELIM_ERR_CRC,
          "CRC 不符应 ERR_CRC");
    wire_delim_destroy(d);
}

/* ============ 10. ⭐ 上界与 TLS 记录的自洽性 ============
 * 上一版把上界写成 16384，于是"整条消息进一个 TLS 记录"差 20 B 不成立。
 * 现在上界反过来从 TLS 记录倒推 ⇒ 这个性质必须**可证**。 */
static void test_max_payload_fits_one_tls_record(void)
{
    const uint32_t TLS_IN = 16384;
    uint32_t worst = WIRE_HEADER_BYTES + WIRE_PAYLOAD_MAX + WIRE_CRC_BYTES;
    CHECK(worst <= TLS_IN,
          "最大帧 %u B 应 <= MBEDTLS_SSL_IN_CONTENT_LEN %u（现在真的成立了）",
          worst, TLS_IN);
    CHECK(worst == TLS_IN,
          "上界应【恰好】用满一个 TLS 记录（否则上界是拍脑袋定的），实际 %u vs %u",
          worst, TLS_IN);

    /* 用一条真的最大帧跑一遍定界器 */
    uint8_t *big = (uint8_t *)malloc((size_t)WIRE_PAYLOAD_MAX);
    CHECK(big != NULL, "分配失败");
    if (big != NULL) {
        for (uint32_t i = 0; i < WIRE_PAYLOAD_MAX; i++) big[i] = (uint8_t)(i & 0xFF);
        uint8_t *msg = (uint8_t *)malloc((size_t)worst + 64);
        CHECK(msg != NULL, "分配消息失败");
        if (msg != NULL) {
            size_t n = mkmsg(msg, (size_t)worst + 64, 0x20, WIRE_FLAG_CRC32C, 1,
                             big, (uint16_t)WIRE_PAYLOAD_MAX);
            CHECK(n == worst, "最大帧线上字节数应为 %u，实际 %zu", worst, n);
            wire_delim_t *d = wire_delim_create(WIRE_PAYLOAD_MAX);
            const uint8_t *out = NULL; size_t olen = 0;
            CHECK(wire_delim_feed(d, msg, n, &out, &olen) == WIRE_DELIM_MSG_READY,
                  "最大帧应能定界");
            CHECK(olen == WIRE_PAYLOAD_MAX, "载荷长度应为 %u，实际 %zu",
                  WIRE_PAYLOAD_MAX, olen);
            wire_delim_destroy(d);
            free(msg);
        }
        free(big);
    }
}

/* ============ 11. 单次投入超过缓冲：明确报错，不静默丢 ============ */
static void test_oversized_feed_is_rejected(void)
{
    wire_delim_t *d = wire_delim_create(16);     /* 缓冲 = 16 + 12 + 4 = 32 */
    CHECK(d != NULL, "创建失败");
    const uint8_t *out = NULL; size_t olen = 0;
    uint8_t junk[64] = {0};
    CHECK(wire_delim_feed(d, junk, sizeof(junk), &out, &olen) == WIRE_DELIM_ERR_TOO_LARGE,
          "单次投入超过缓冲应 ERR_TOO_LARGE（不静默丢弃）");
    wire_delim_destroy(d);
}

/* ============ 12. 创建参数：上界 0 无意义 ============ */
static void test_create_rejects_zero_bound(void)
{
    CHECK(wire_delim_create(0) == NULL, "上界 0 应拒绝（不兜底）");
    wire_delim_t *d = wire_delim_create(1);
    CHECK(d != NULL, "上界 1 应可用");
    wire_delim_destroy(d);
}

int main(void)
{
    test_crc32c_check_value();
    test_header_is_big_endian();
    test_header_validation();
    test_delim_arbitrary_splits();
    test_delim_stops_at_message_boundary();
    test_payload_lifetime_contract();
    test_bad_header_rejected_then_resync();
    test_declared_too_large_is_not_buffered();
    test_crc_checked_when_flag_set();
    test_max_payload_fits_one_tls_record();
    test_oversized_feed_is_rejected();
    test_create_rejects_zero_bound();

    if (s_failures) { printf("wire_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("wire_tests: all checks passed\n");
    return 0;
}
