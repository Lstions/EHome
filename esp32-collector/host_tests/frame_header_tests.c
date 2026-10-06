/* frame_header_tests.c —— S0：帧头编解码对锚共享 golden vector
 *
 * 为什么需要它：骨架初版把帧格式定成 varint(len)||payload —— 与设计 §5.1 的
 * 16 B 定长头（magic/ver/type/flags/seq/frag_off/frag_len/total_len）**是两种格式**，
 * 且初版**不支持分片**。这与 msgcodec 自创 tag 格式同族（骨架自作主张改契约）。
 *
 * 本用例直接读 protocol/vectors/frame_header.txt（唯一向量来源），
 * 逐条断言：编码 == wire，解码 == 各字段，且往返一致。
 * 端序漂移（大端/小端）会被"endian_seq_high_bytes"这条直接抓到。
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "wire.h"

static int s_failures = 0;
static int s_cases = 0;

#define CHECK(cond, ...)                                                     \
    do {                                                                     \
        if (!(cond)) {                                                       \
            printf("FAIL %s:%d  ", __FILE__, __LINE__);                      \
            printf(__VA_ARGS__);                                             \
            printf("\n");                                                    \
            s_failures++;                                                    \
        }                                                                    \
    } while (0)

#define MAX_PAYLOAD 2048

typedef struct {
    char     name[64];
    int      line;
    bool     have[8];   /* magic ver type flags seq off len total */
    uint16_t magic; uint8_t ver, type; uint16_t flags;
    uint32_t seq; uint16_t off, flen, total;
    uint8_t  payload[MAX_PAYLOAD];
    size_t   payload_len;
    uint8_t  wire[WIRE_HEADER_BYTES + MAX_PAYLOAD];
    size_t   wire_len;
    int      expect;   /* 0=正向；否则期望的 wire_result_t 值 */
} vec_t;

static int hexv(char c)
{
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

static long phex(const char *s, uint8_t *out, size_t cap)
{
    size_t n = strlen(s);
    if (n == 1 && s[0] == '-') return 0;
    if (n % 2) return -1;
    if (n / 2 > cap) return -1;
    for (size_t i = 0; i < n / 2; i++) {
        int hi = hexv(s[2*i]), lo = hexv(s[2*i+1]);
        if (hi < 0 || lo < 0) return -1;
        out[i] = (uint8_t)((hi << 4) | lo);
    }
    return (long)(n / 2);
}

static void run_case(const vec_t *c)
{
    uint8_t buf[WIRE_HEADER_BYTES];
    wire_header_t h = {0};

    /* 负例：解码必须【拒绝】，且原因可区分 */
    if (c->expect != 0) {
        int before = s_failures;
        wire_header_t g2 = {0};
        wire_result_t got = wire_decode_header(c->wire, WIRE_HEADER_BYTES, &g2);
        CHECK(got == (wire_result_t)c->expect,
              "%s: 期望解码返回 %s，实际 %s", c->name,
              wire_result_name((wire_result_t)c->expect), wire_result_name(got));
        if (s_failures == before) {
            printf("  (负例 %s 被正确拒绝：%s)\n", c->name,
                   wire_result_name(got));
        }
        return;
    }

    /* --- 1. 字段级编码 --- */
    h.ver = c->ver; h.type = c->type; h.flags = c->flags; h.seq = c->seq;
    h.frag_off = c->off; h.frag_len = c->flen; h.total_len = c->total;

    wire_result_t er = wire_encode_header(buf, sizeof(buf), &h);
    CHECK(er == WIRE_OK, "%s: 编码头返回 %s", c->name, wire_result_name(er));
    if (er != WIRE_OK) return;

    CHECK(memcmp(buf, c->wire, WIRE_HEADER_BYTES) == 0,
          "%s: 头部字节不符（前 4 字节 %02x %02x %02x %02x vs %02x %02x %02x %02x）",
          c->name, buf[0], buf[1], buf[2], buf[3],
          c->wire[0], c->wire[1], c->wire[2], c->wire[3]);
    /* 载荷由调用方拼接；这里核对向量自身的 wire 长度 == 16 + payload */
    CHECK(c->wire_len == WIRE_HEADER_BYTES + c->payload_len,
          "%s: 向量 wire 长度 %zu != 16 + %zu", c->name, c->wire_len, c->payload_len);
    if (c->payload_len > 0) {
        CHECK(memcmp(c->wire + WIRE_HEADER_BYTES, c->payload, c->payload_len) == 0,
              "%s: 向量载荷与 payload 字段不符", c->name);
    }

    /* --- 2. 解码 --- */
    wire_header_t g = {0};
    wire_result_t dr = wire_decode_header(c->wire, WIRE_HEADER_BYTES, &g);
    CHECK(dr == WIRE_OK, "%s: 解码头返回 %s", c->name, wire_result_name(dr));
    if (dr != WIRE_OK) return;
    CHECK(g.ver == c->ver,   "%s: ver %u != %u", c->name, g.ver, c->ver);
    CHECK(g.type == c->type, "%s: type %u != %u", c->name, g.type, c->type);
    CHECK(g.flags == c->flags, "%s: flags 0x%04x != 0x%04x", c->name, g.flags, c->flags);
    CHECK(g.seq == c->seq,   "%s: seq 0x%08x != 0x%08x", c->name, g.seq, c->seq);
    CHECK(g.frag_off == c->off,   "%s: frag_off %u != %u", c->name, g.frag_off, c->off);
    CHECK(g.frag_len == c->flen,  "%s: frag_len %u != %u", c->name, g.frag_len, c->flen);
    CHECK(g.total_len == c->total,"%s: total_len %u != %u", c->name, g.total_len, c->total);
}

int main(int argc, char **argv)
{
    const char *path = (argc > 1) ? argv[1]
        : "../protocol/vectors/frame_header.txt";
    FILE *fp = fopen(path, "r");
    if (fp == NULL) { printf("FAIL 打不开 %s\n", path); return 2; }

    static vec_t cur;
    char line[16384];
    int lineno = 0;
    bool in_case = false;

    while (fgets(line, sizeof(line), fp) != NULL) {
        lineno++;
        char *p = line;
        while (*p == ' ' || *p == '\t') p++;
        if (*p == '#' || *p == '\n' || *p == '\0') continue;

        char kw[32] = {0}, arg[16384] = {0};
        if (sscanf(p, "%31s %16383s", kw, arg) < 1) continue;

        if (strcmp(kw, "case") == 0) {
            memset(&cur, 0, sizeof(cur));
            cur.line = lineno;
            /* 显式截断：用例名不会那么长，但编译器看不到这个前提
             * （arg 是 16 KB 缓冲）—— 用精度限制长度，而不是关掉警告。 */
            snprintf(cur.name, sizeof(cur.name), "%.*s", (int)(sizeof(cur.name) - 1), arg);
            in_case = true;
        } else if (!in_case) {
            continue;
        } else if (strcmp(kw, "wire") == 0) {
            long n = phex(arg, cur.wire, sizeof(cur.wire));
            if (n < 0) { printf("FAIL 第 %d 行 wire hex 非法\n", lineno); fclose(fp); return 2; }
            cur.wire_len = (size_t)n;
        } else if (strcmp(kw, "payload") == 0) {
            long n = phex(arg, cur.payload, sizeof(cur.payload));
            if (n < 0) { printf("FAIL 第 %d 行 payload hex 非法\n", lineno); fclose(fp); return 2; }
            cur.payload_len = (size_t)n;
        } else if (strcmp(kw, "expect_range") == 0) {
            cur.expect = WIRE_ERR_RANGE;
        } else if (strcmp(kw, "expect_structure") == 0) {
            cur.expect = WIRE_ERR_STRUCTURE;
        } else if (strcmp(kw, "expect_magic") == 0) {
            cur.expect = WIRE_ERR_MAGIC;
        } else if (strcmp(kw, "expect_version") == 0) {
            cur.expect = WIRE_ERR_VERSION;
        } else if (strcmp(kw, "end") == 0) {
            s_cases++;
            run_case(&cur);
            in_case = false;
        } else {
            /* 字段行：<名> <hex> */
            uint8_t tmp[8] = {0};
            long n = phex(arg, tmp, sizeof(tmp));
            if (n < 0) { printf("FAIL 第 %d 行 %s hex 非法\n", lineno, kw); fclose(fp); return 2; }
            if      (strcmp(kw, "magic") == 0)    cur.magic = (uint16_t)((tmp[0] << 8) | tmp[1]);
            else if (strcmp(kw, "ver") == 0)      cur.ver = tmp[0];
            else if (strcmp(kw, "type") == 0)     cur.type = tmp[0];
            else if (strcmp(kw, "flags") == 0)    cur.flags = (uint16_t)((tmp[0] << 8) | tmp[1]);
            else if (strcmp(kw, "seq") == 0)      cur.seq = ((uint32_t)tmp[0] << 24) | ((uint32_t)tmp[1] << 16) | ((uint32_t)tmp[2] << 8) | tmp[3];
            else if (strcmp(kw, "frag_off") == 0) cur.off = (uint16_t)((tmp[0] << 8) | tmp[1]);
            else if (strcmp(kw, "frag_len") == 0) cur.flen = (uint16_t)((tmp[0] << 8) | tmp[1]);
            else if (strcmp(kw, "total_len") == 0) cur.total = (uint16_t)((tmp[0] << 8) | tmp[1]);
        }
    }
    fclose(fp);

    printf("对锚 %d 条帧头向量，%d 条不一致\n", s_cases, s_failures);
    if (s_failures) { printf("frame_header_tests: FAILED\n"); return 1; }
    printf("frame_header_tests: all checks passed\n");
    return 0;
}
