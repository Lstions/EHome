/* golden_vectors_tests.c —— S0：ESP32 端消费【共享】golden vector
 *
 * 为什么需要它：ESP32（components/msgcodec，C）与后端（pkg/frame，Go）是两套
 * 独立实现，各自带着各自的黄金向量 ⇒ 两端可能【同时自洽却互相不通】。
 * 设计文档 §3 的 S0 阶段要求"冻结契约"，判据是"两端各有一套对锚测试"。
 *
 * 本用例**直接读** protocol/vectors/wire_primitives.txt（唯一向量来源），
 * 用真实的 msgcodec 逐条断言：
 *   1. 按用例字段编码 == wire
 *   2. wire 解回 == 用例字段（顺序扫描，无尾随）
 *   3. 编码长度 == wire 长度
 * 向量文件改了而 msgcodec 没跟上（或反之），这里立刻红。
 *
 * 解析刻意写得朴素：文件语法极简（case/type/u64/bytes/wire/end），
 * 复杂解析器本身就是新的错误面。
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdint.h>
#include <stdbool.h>

#include "msgcodec.h"

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

#define MAX_FIELDS 16
#define MAX_BYTES  512

typedef struct {
    char     name[64];
    int      line;
    bool     is_sub;                 /* sub => 无类型前缀 */
    uint8_t  type;                   /* is_sub 时忽略 */
    int      nfields;
    enum { F_U64, F_BYTES } kind[MAX_FIELDS];
    uint8_t  field_id[MAX_FIELDS];
    uint64_t u64v[MAX_FIELDS];
    uint8_t  bytesv[MAX_FIELDS][MAX_BYTES];
    size_t   bytes_len[MAX_FIELDS];
    uint8_t  wire[MAX_BYTES + 64];
    size_t   wire_len;
} vec_case_t;

static int hex_val(char c)
{
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    if (c >= 'A' && c <= 'F') return c - 'A' + 10;
    return -1;
}

/* 解析 hex 串；返回字节数，-1 表示非法 */
static long parse_hex(const char *s, uint8_t *out, size_t cap)
{
    size_t n = strlen(s);
    if (n == 1 && s[0] == '-') return 0;
    if (n % 2 != 0) return -1;
    if (n / 2 > cap) return -1;
    for (size_t i = 0; i < n / 2; i++) {
        int hi = hex_val(s[2 * i]), lo = hex_val(s[2 * i + 1]);
        if (hi < 0 || lo < 0) return -1;
        out[i] = (uint8_t)((hi << 4) | lo);
    }
    return (long)(n / 2);
}

/* 逐条跑一个用例 */
static void run_case(const vec_case_t *c)
{
    uint8_t buf[MAX_BYTES + 128];
    size_t used = 0;

    /* --- 1. 编码 ---
     * 契约：enc_field_* 的 *used 是【本字段写入的字节数】，不是累计偏移。
     * 调用方自己维护 off 并写 buf+off（第一次我传了累计偏移当 used，
     * 导致每个字段都从头覆盖 —— 12 条里错 11 条，全部差"少一个类型字节"）。 */
    if (!c->is_sub) {
        buf[0] = c->type;
        used = 1;
    } else {
        used = 0;
    }
    size_t off = used;
    enc_result_t er = ENC_OK;
    for (int i = 0; i < c->nfields && er == ENC_OK; i++) {
        size_t wrote = 0;
        if (c->kind[i] == F_U64) {
            er = enc_field_u64(buf + off, sizeof(buf) - off, &wrote,
                               c->field_id[i], c->u64v[i]);
        } else {
            er = enc_field_bytes(buf + off, sizeof(buf) - off, &wrote,
                                 c->field_id[i], c->bytesv[i], c->bytes_len[i]);
        }
        off += wrote;
    }
    used = off;
    CHECK(er == ENC_OK, "%s: 编码返回 %s", c->name, enc_result_name(er));
    if (er != ENC_OK) return;

    CHECK(used == c->wire_len, "%s: 编码长度 %zu != 向量 %zu",
          c->name, used, c->wire_len);
    if (used == c->wire_len) {
        CHECK(memcmp(buf, c->wire, used) == 0, "%s: 编码字节不符", c->name);
    }

    /* --- 2. 解码（顺序扫描 + 无尾随）--- */
    size_t cursor = c->is_sub ? 0 : 1;
    for (int i = 0; i < c->nfields; i++) {
        field_view_t fv;
        dec_result_t dr = dec_next_field(c->wire, c->wire_len, &cursor, &fv);
        CHECK(dr == DEC_OK, "%s: 第 %d 个字段解码返回 %s",
              c->name, i, dec_result_name(dr));
        if (dr != DEC_OK) return;
        CHECK(fv.field_id == c->field_id[i],
              "%s: 第 %d 个字段号 %u != %u", c->name, i, fv.field_id, c->field_id[i]);
        if (c->kind[i] == F_U64) {
            uint64_t v = 0;
            dec_result_t d2 = dec_field_u64(&fv, &v);
            CHECK(d2 == DEC_OK, "%s: 第 %d 个 u64 解码失败", c->name, i);
            CHECK(v == c->u64v[i], "%s: 第 %d 个 u64 值 %llu != %llu",
                  c->name, i, (unsigned long long)v, (unsigned long long)c->u64v[i]);
        } else {
            CHECK(fv.value_len == c->bytes_len[i],
                  "%s: 第 %d 个 bytes 长度 %zu != %zu",
                  c->name, i, fv.value_len, c->bytes_len[i]);
            CHECK(fv.value_len == c->bytes_len[i] &&
                  memcmp(fv.value, c->bytesv[i], fv.value_len) == 0,
                  "%s: 第 %d 个 bytes 内容不符", c->name, i);
        }
    }
    CHECK(cursor == c->wire_len, "%s: 解析未恰好结束（剩 %zu 字节）",
          c->name, c->wire_len - cursor);
}

int main(int argc, char **argv)
{
    const char *path = (argc > 1) ? argv[1]
        : "../protocol/vectors/wire_primitives.txt";
    FILE *fp = fopen(path, "r");
    if (fp == NULL) {
        printf("FAIL 打不开向量文件 %s\n", path);
        return 2;
    }

    static vec_case_t cur;
    char line[4096];
    int lineno = 0;
    bool in_case = false;

    while (fgets(line, sizeof(line), fp) != NULL) {
        lineno++;
        char *p = line;
        while (*p == ' ' || *p == '\t') p++;
        if (*p == '#' || *p == '\n' || *p == '\0') continue;

        char kw[32] = {0};
        if (sscanf(p, "%31s", kw) != 1) continue;

        if (strcmp(kw, "case") == 0) {
            memset(&cur, 0, sizeof(cur));
            cur.line = lineno;
            sscanf(p, "case %63s", cur.name);
            in_case = true;
        } else if (!in_case) {
            continue;
        } else if (strcmp(kw, "sub") == 0) {
            cur.is_sub = true;
        } else if (strcmp(kw, "type") == 0) {
            unsigned t = 0;
            sscanf(p, "type %u", &t);
            cur.type = (uint8_t)t;
        } else if (strcmp(kw, "u64") == 0) {
            unsigned fid = 0;
            unsigned long long v = 0;
            sscanf(p, "u64 %u %llu", &fid, &v);
            int i = cur.nfields++;
            cur.kind[i] = F_U64;
            cur.field_id[i] = (uint8_t)fid;
            cur.u64v[i] = (uint64_t)v;
        } else if (strcmp(kw, "bytes") == 0) {
            unsigned fid = 0;
            char hex[2048] = {0};
            sscanf(p, "bytes %u %2047s", &fid, hex);
            int i = cur.nfields++;
            cur.kind[i] = F_BYTES;
            cur.field_id[i] = (uint8_t)fid;
            long n = parse_hex(hex, cur.bytesv[i], MAX_BYTES);
            if (n < 0) {
                printf("FAIL 第 %d 行：bytes 的 hex 非法\n", lineno);
                fclose(fp);
                return 2;
            }
            cur.bytes_len[i] = (size_t)n;
        } else if (strcmp(kw, "wire") == 0) {
            char hex[4096] = {0};
            sscanf(p, "wire %4095s", hex);
            long n = parse_hex(hex, cur.wire, sizeof(cur.wire));
            if (n < 0) {
                printf("FAIL 第 %d 行：wire 的 hex 非法\n", lineno);
                fclose(fp);
                return 2;
            }
            cur.wire_len = (size_t)n;
        } else if (strcmp(kw, "end") == 0) {
            s_cases++;
            run_case(&cur);
            in_case = false;
        }
    }
    fclose(fp);

    printf("对锚 %d 条共享向量，%d 条不一致\n", s_cases, s_failures);
    if (s_failures) {
        printf("golden_vectors_tests: FAILED —— 本端实现与 S0 契约漂移\n");
        return 1;
    }
    printf("golden_vectors_tests: all checks passed\n");
    return 0;
}
