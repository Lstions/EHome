/* device_op_shared_vectors_tests.c —— 让【固件真实实现】消费共享向量
 *
 * # 为什么需要它（一个真实缺口）
 *
 * 共享向量文件 protocol/vectors/wire_primitives.txt 的既有消费者
 * （golden_vectors_tests.c、后端 Go 的 TestGoldenVectorsFromSharedContract、
 * Python 门禁）都只用**通用** frame_encoder/frame_decoder 去编解向量。
 *
 * 那证明了"通用编解码器能处理这串字节"，但**没有**证明
 * handler_device_op.c 用的是 field 1/2/3。换句话说：
 * **字段号写错，通用向量照样全绿** —— 本仓反复出现的"绿了但没测到那个东西"。
 *
 * 本用例把固件的**真实**下行解析（handler_device_op_process）与
 * **真实**上行编码（adapt_flush 走的 encode_and_send_ack）接到同一份向量上。
 *
 * 与后端 Go 侧 device_op_vectors_test.go 用**同一串字节**做对锚：
 * 两端任一处字段号漂移，两边各自会红。
 */
#include <stdbool.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "device_op.h"
#include "frame_codec.h"
#include "msg_handler_device_op.h"

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

void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level; (void)tag; (void)format;
}

/* ── 假原语：记录下行解析结果与上行字节 ── */
static int  s_restart_calls;
static int  s_send_calls;
static uint8_t s_sent[512];
static size_t  s_sent_len;

static int h_erase(const char *ns) { (void)ns; return 0; }
static int h_send(const uint8_t *frame, size_t len)
{
    s_send_calls++;
    s_sent_len = (len <= sizeof(s_sent)) ? len : sizeof(s_sent);
    memcpy(s_sent, frame, s_sent_len);
    return 0;
}
static void h_restart(void) { s_restart_calls++; }

static const device_op_hooks_t HOOKS = {
    .erase_namespace = h_erase,
    .send_frame = h_send,
    .restart = h_restart,
};

/* ── 极简向量解析（只取本用例需要的字段） ── */
#define MAX_BYTES 256
typedef struct {
    char     name[64];
    bool     in_case;
    bool     is_sub;
    unsigned type;
    /* field 1 (varint) 与 field 2/3 (bytes) */
    bool     have_f1; unsigned long long f1;
    bool     have_f2; uint8_t f2[MAX_BYTES]; size_t f2_len;
    bool     have_f3; uint8_t f3[MAX_BYTES]; size_t f3_len;
    uint8_t  wire[MAX_BYTES]; size_t wire_len;
} vec_t;

static long parse_hex(const char *hex, uint8_t *out, size_t cap)
{
    size_t n = strlen(hex);
    if (n % 2 != 0) return -1;
    if (n / 2 > cap) return -1;
    for (size_t i = 0; i < n / 2; i++) {
        unsigned v = 0;
        if (sscanf(hex + i * 2, "%2x", &v) != 1) return -1;
        out[i] = (uint8_t)v;
    }
    return (long)(n / 2);
}

/* 把一个 0x22 向量喂进真实 handler，返回它回上去的字节。 */
static bool feed_and_capture(const uint8_t *wire, size_t len,
                             uint8_t *out, size_t *out_len, bool *restarted)
{
    s_send_calls = 0; s_sent_len = 0; s_restart_calls = 0;
    memset(s_sent, 0, sizeof(s_sent));
    msg_handler_set_device_op_hooks(&HOOKS);
    /* device_op 是单飞的：一次操作以重启收尾，真机上状态随重启清零。
     * 宿主测试里假 restart 直接返回，状态会留着 ⇒ 第二条用例会被判 BUSY。
     * 必须显式复位（与 device_op_tests.c 的做法一致）。 */
    device_op_reset_state();

    frame_decoder_t dec;
    if (frame_decoder_init(&dec, wire, len) != FRAME_OK) return false;
    handler_device_op_process(&dec);

    if (s_send_calls == 0) return false;
    memcpy(out, s_sent, s_sent_len);
    *out_len = s_sent_len;
    *restarted = (s_restart_calls > 0);
    return true;
}

static void run_vector(const vec_t *v)
{
    if (v->is_sub || v->wire_len == 0) return;

    if (v->type == MSG_DEVICE_OP) {
        /* 下行：真实解析器必须接受这串字节，并回出向量里的 0x23。
         * 但向量里的下行用例没有配对的 ack 向量，所以这里只断言
         * "解析器接受 + 回了 ACK + 结果码正确 + request_id 原样回显"。 */
        uint8_t out[MAX_BYTES]; size_t out_len = 0; bool restarted = false;
        bool got = feed_and_capture(v->wire, v->wire_len, out, &out_len, &restarted);
        CHECK(got, "%s: 真实下行解析器没有回 ACK", v->name);
        if (!got) return;

        CHECK(out[0] == MSG_DEVICE_OP_ACK, "%s: 回的不是 0x23（实际 0x%02X）",
              v->name, out[0]);

        /* 解出回上去的 result / request_id */
        frame_decoder_t dec;
        if (frame_decoder_init(&dec, out, out_len) != FRAME_OK) {
            CHECK(false, "%s: 回上去的帧解不开", v->name);
            return;
        }
        bool have_result = false; unsigned long long result = 0;
        char rid[128] = {0};
        frame_field_t f;
        while (frame_decoder_next(&dec, &f) == FRAME_OK) {
            if (f.field_num == 1) { result = f.value.varint; have_result = true; }
            else if (f.field_num == 2) {
                size_t n = f.value.bytes.len;
                if (n >= sizeof(rid)) n = sizeof(rid) - 1;
                memcpy(rid, f.value.bytes.ptr, n);
                rid[n] = '\0';
            }
        }
        CHECK(have_result, "%s: 回的 ACK 没有 result 字段", v->name);
        CHECK(result == DEVOP_OK, "%s: 结果码应为 OK，实际 %llu", v->name, result);
        CHECK(v->have_f2 && strcmp(rid, (const char *)v->f2) == 0,
              "%s: **request_id 没有原样回显**（got [%s], want [%s]）—— "
              "服务端靠它把 ACK 配到具体请求上", v->name, rid,
              v->have_f2 ? (const char *)v->f2 : "(none)");
        return;
    }

    if (v->type == MSG_DEVICE_OP_ACK) {
        /* 上行：真实解码必须与向量一致。
         * 固件侧没有"解 0x23"的生产代码（设备只发不收），
         * 所以这里用**后端定义的口径**校验：字段号 1/2/3 与向量一致，
         * 并且未知结果码原样保留数字。 */
        CHECK(v->have_f1, "%s: 向量缺少 field 1 (result_code)", v->name);
        /* 直接按字段号解，验证 1/2/3 的语义与 Go 侧一致 */
        frame_decoder_t dec;
        if (frame_decoder_init(&dec, v->wire, v->wire_len) != FRAME_OK) {
            CHECK(false, "%s: 向量解不开", v->name);
            return;
        }
        bool seen1 = false, seen2 = false;
        unsigned long long r1 = 0;
        char rid2[128] = {0};
        frame_field_t f;
        while (frame_decoder_next(&dec, &f) == FRAME_OK) {
            if (f.field_num == 1) { r1 = f.value.varint; seen1 = true; }
            else if (f.field_num == 2) {
                size_t n = f.value.bytes.len;
                if (n >= sizeof(rid2)) n = sizeof(rid2) - 1;
                memcpy(rid2, f.value.bytes.ptr, n);
                rid2[n] = '\0';
                seen2 = true;
            }
        }
        CHECK(seen1, "%s: 没有解析到 result_code(field 1)", v->name);
        CHECK(seen2, "%s: 没有解析到 request_id(field 2)", v->name);
        CHECK(seen1 && r1 == v->f1,
              "%s: result_code 不一致（got %llu want %llu）—— "
              "未知结果码必须原样保留数字，折叠成已知值会把"
              "'设备比服务端新'误报成'操作失败'", v->name, r1, v->f1);
        CHECK(seen2 && v->have_f2 && strcmp(rid2, (const char *)v->f2) == 0,
              "%s: request_id 不一致（got [%s] want [%s]）", v->name, rid2,
              v->have_f2 ? (const char *)v->f2 : "(none)");
    }
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

    static vec_t cur;
    char line[4096];
    int lineno = 0;
    int device_op_cases = 0;

    while (fgets(line, sizeof(line), fp) != NULL) {
        lineno++;
        char *p = line;
        while (*p == ' ' || *p == '\t') p++;
        if (*p == '#' || *p == '\n' || *p == '\0') continue;

        char kw[32] = {0};
        if (sscanf(p, "%31s", kw) != 1) continue;

        if (strcmp(kw, "case") == 0) {
            memset(&cur, 0, sizeof(cur));
            sscanf(p, "case %63s", cur.name);
            cur.in_case = true;
        } else if (!cur.in_case) {
            continue;
        } else if (strcmp(kw, "sub") == 0) {
            cur.is_sub = true;
        } else if (strcmp(kw, "type") == 0) {
            unsigned t = 0;
            sscanf(p, "type %u", &t);
            cur.type = t;
        } else if (strcmp(kw, "u64") == 0) {
            unsigned fid = 0; unsigned long long v = 0;
            sscanf(p, "u64 %u %llu", &fid, &v);
            if (fid == 1) { cur.have_f1 = true; cur.f1 = v; }
        } else if (strcmp(kw, "bytes") == 0) {
            unsigned fid = 0; char hex[2048] = {0};
            int matched = sscanf(p, "bytes %u %2047s", &fid, hex);
            /* 空 bytes 在向量里写成 "-"（见文件头部语法说明）。
             * 只取到字段号时说明就是空值，不是解析失败。 */
            long n = 0;
            if (matched == 2 && strcmp(hex, "-") != 0) {
                n = parse_hex(hex, (fid == 3) ? cur.f3 : cur.f2, MAX_BYTES);
                if (n < 0) { printf("FAIL 第 %d 行 hex 非法\n", lineno); fclose(fp); return 2; }
            } else if (matched < 1) {
                printf("FAIL 第 %d 行 bytes 语法非法\n", lineno); fclose(fp); return 2;
            }
            if (fid == 2) { cur.have_f2 = true; cur.f2_len = (size_t)n; }
            else if (fid == 3) { cur.have_f3 = true; cur.f3_len = (size_t)n; }
        } else if (strcmp(kw, "wire") == 0) {
            char hex[4096] = {0};
            sscanf(p, "wire %4095s", hex);
            long n = parse_hex(hex, cur.wire, sizeof(cur.wire));
            if (n < 0) { printf("FAIL 第 %d 行 wire hex 非法\n", lineno); fclose(fp); return 2; }
            cur.wire_len = (size_t)n;
        } else if (strcmp(kw, "end") == 0) {
            if (strncmp(cur.name, "device_op_", 10) == 0) {
                device_op_cases++;
                run_vector(&cur);
            }
            cur.in_case = false;
        }
    }
    fclose(fp);

    CHECK(device_op_cases > 0,
          "共享向量里找不到 device_op_* 用例 —— 向量被删了，"
          "两端的 0x22/0x23 字段约定就失去了唯一的对锚");
    printf("  (检查了 %d 条 device_op 共享向量)\n", device_op_cases);

    if (s_failures) { printf("device_op_shared_vectors_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("device_op_shared_vectors_tests: all checks passed\n");
    return 0;
}
