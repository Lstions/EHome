/* link_tcp_tests.c —— 3.0 主传输（TCP+TLS）驱动的状态机
 *
 * 这是 ESP32 端 S1 的核心：设计 §4.1 规定设备作为 TCP client 主动连后端
 * 8443/mTLS。本用例用**可控假 I/O**驱动状态机，断言四类语义：
 *   1. D-10：部分写必须续写；写不动 = 背压（可重试），不是失败
 *   2. 错误分级：证书类硬错 → FATAL（别盲目重试）；网络类软错 → NOT_READY（可退避）
 *   3. 设计 §4.2：退避 1→2→4→8→16→30→60s + ±20% 抖动；**应用层握手**才重置，
 *      socket connect **不**重置（防"连上但不通"的抖振）
 *   4. P2：mtu 由驱动给出（16,384，与 MBEDTLS_SSL_IN_CONTENT_LEN 对齐）
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "esp_err.h"
#include "link.h"
#include "link_tcp.h"

/* D-30：link_send 现在需要 progress 出入参。
 * SEND1 是"发一整帧、不关心中间进度"的便捷包装（progress 从 0 起、丢弃更新）； */
static size_t g_prog;
#define SEND1(l, f, n) (g_prog = 0, link_send((l), (f), (n), &g_prog))

/* link.h 规定的续写循环 —— 调用方发一整帧的唯一正确姿势。
 * 这里把它做成辅助函数，好让每个用例都用同一种（正确的）调用方式，
 * 而不是各写各的、把"重发整帧"这种错法散落进测试。 */
static link_result_t SEND_LOOP(link_t *l, const uint8_t *f, size_t n, size_t *prog)
{
    *prog = 0;
    for (int guard = 0; guard < 16; guard++) {     /* 防御：避免测试挂死 */
        link_result_t r = link_send(l, f, n, prog);
        if (r == LINK_SENT_PARTIAL) continue;      /* 接着写 */
        return r;                                  /* FULL / BACKPRESSURE / 其它 */
    }
    return LINK_FATAL;
}

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

/* ---- 可控假 I/O ---- */
#define WRITE_SCRIPT_MAX 8

typedef struct {
    int      connect_calls;
    int      close_calls;
    void    *handle_to_return;     /* NULL = 连接失败 */
    bool     hard_fatal;           /* connect 失败时是否报告为硬错 */

    int      write_results[WRITE_SCRIPT_MAX];
    size_t   write_count;
    size_t   write_idx;
    int      write_calls;
    size_t   total_written;        /* 经假 I/O 实际写出的字节 */
    /* 【D-30 的关键】把实际落到"线上"的字节按顺序记下来。
     * 只有比对这条流，才能证明"续写"没有把已写出的字节重复写一遍。 */
    uint8_t  wire[256];
    size_t   wire_len;
} fake_io_t;

static fake_io_t s_io;

static void *fake_connect(void *io_ctx, bool *hard_fatal)
{
    (void)io_ctx;
    s_io.connect_calls++;
    if (s_io.handle_to_return == NULL) {
        if (hard_fatal) *hard_fatal = s_io.hard_fatal;
        return NULL;
    }
    if (hard_fatal) *hard_fatal = false;
    return s_io.handle_to_return;
}

/* 把 data[0..n) 追加到"线上"记录（模拟对端真正收到的字节序列） */
static void record_wire(const uint8_t *data, size_t n)
{
    if (s_io.wire_len + n > sizeof(s_io.wire)) return;
    memcpy(s_io.wire + s_io.wire_len, data, n);
    s_io.wire_len += n;
}

static int fake_write(void *handle, const uint8_t *data, size_t len)
{
    (void)handle;
    s_io.write_calls++;
    if (s_io.write_idx >= s_io.write_count) {
        s_io.total_written += len;
        record_wire(data, len);
        return (int)len;                 /* 脚本用尽：视为一次写完（避免测试挂死） */
    }
    int r = s_io.write_results[s_io.write_idx++];
    if (r > 0) {
        size_t n = (size_t)r;
        if (n > len) n = len;            /* 不许"写出"超过请求量 */
        s_io.total_written += n;
        record_wire(data, n);
        return (int)n;
    }
    return r;                            /* 0 = 背压；<0 = 硬错 */
}

static void fake_close(void *handle) { (void)handle; s_io.close_calls++; }

static const link_tcp_io_t FAKE_IO = {
    .connect = fake_connect, .write = fake_write, .close = fake_close,
};

static void reset_io(void)
{
    memset(&s_io, 0, sizeof(s_io));
    s_io.handle_to_return = (void *)0x1;   /* 默认连接成功 */
}

/* 每个测试建独立上下文 —— 无全局状态，测试之间不会互相污染。
 * （驱动本身是无状态单例；状态都在这个上下文里。） */
static link_tcp_ctx_t *s_ctx;

static link_t *make_link(const link_tcp_config_t *cfg)
{
    s_ctx = link_tcp_new(cfg);
    if (s_ctx == NULL) return NULL;
    return link_create(link_tcp_driver(), s_ctx);
}

static void drop_link(link_t *l)
{
    link_destroy(l);       /* 先关链路（内部会用 ctx） */
    link_tcp_free(s_ctx);  /* 再释放上下文 */
    s_ctx = NULL;
}

/* ================= 1. 部分写必须续写（D-10）================= */
static void test_partial_write_is_continued(void)
{
    reset_io();
    link_tcp_config_t cfg = { .io = &FAKE_IO, .io_ctx = NULL };
    link_t *l = make_link(&cfg);
    CHECK(l != NULL, "link_create 失败");
    CHECK(link_open(l) == LINK_SENT_FULL, "夹具应连接成功");

    /* frame 用可辨识的内容，便于逐字节比对"线上"是否被污染 */
    uint8_t frame[10];
    for (int i = 0; i < 10; i++) frame[i] = (uint8_t)(0xA0 + i);

    s_io.write_results[0] = 4;
    s_io.write_results[1] = 3;
    s_io.write_results[2] = 3;
    s_io.write_count = 3;

    /* ⭐ D-30 回归：按 link.h 规定的续写循环发送，而不是"重发整帧"。 */
    size_t progress = 0;
    link_result_t r = SEND_LOOP(l, frame, 10, &progress);

    CHECK(r == LINK_SENT_FULL, "续写循环后应 FULL，实际 %s", link_result_name(r));
    CHECK(progress == 10, "progress 应为 10，实际 %zu", progress);
    CHECK(s_io.write_calls == 3, "应为 3 次写（4+3+3），实际 %d", s_io.write_calls);

    /* ⭐ 决定性断言：线上收到的字节必须【恰好等于这一帧，且只出现一次】。
     * 这正是 D-30 的病灶 —— 旧实现会写成 frame[0:4] 两次。 */
    CHECK(s_io.wire_len == 10, "线上字节数应为 10（不得重复），实际 %zu", s_io.wire_len);
    CHECK(s_io.wire_len == 10 && memcmp(s_io.wire, frame, 10) == 0,
          "线上字节必须恰好是这一帧一次（无重复、无错位）");

    drop_link(l);
}

/* ================= 2. 只写出一部分 -> PARTIAL（不是失败，也不是成功）===== */
static void test_partial_write_reports_progress(void)
{
    reset_io();
    link_tcp_config_t cfg = { .io = &FAKE_IO, .io_ctx = NULL };
    link_t *l = make_link(&cfg);
    CHECK(link_open(l) == LINK_SENT_FULL, "夹具应连接成功");

    uint8_t frame[10] = {0};
    s_io.write_results[0] = 4;      /* 只吞得下 4 字节 */
    s_io.write_count = 1;

    size_t progress = 0;
    link_result_t r = link_send(l, frame, 10, &progress);
    CHECK(r == LINK_SENT_PARTIAL, "部分写应 PARTIAL，实际 %s", link_result_name(r));
    CHECK(!link_result_is_error(r), "PARTIAL【不是】错误（是进行中）");
    CHECK(progress == 4, "progress 应为 4，实际 %zu", progress);

    /* 关键：新的一次调用必须【从进度处】继续，而不是重头写。
     * 假 I/O 会如实记录它收到的是 data+4（见下一节的线上比对）。 */
    s_io.write_results[0] = 6;
    s_io.write_idx = 0; s_io.write_count = 1;
    r = link_send(l, frame, 10, &progress);
    CHECK(r == LINK_SENT_FULL, "续写后应 FULL，实际 %s", link_result_name(r));
    CHECK(progress == 10, "progress 应为 10，实际 %zu", progress);

    link_stats_t st;
    link_get_stats(l, &st);
    CHECK(st.tx_sent_partial == 1, "PARTIAL 应单独计数（P3），实际 %u", st.tx_sent_partial);
    CHECK(st.tx_sent_full == 1, "FULL 应计数，实际 %u", st.tx_sent_full);
    drop_link(l);
}

/* ================= 2b. 背压 = 一个字节都没写出（整帧重试才安全）========= */
static void test_write_blocked_is_backpressure_not_failure(void)
{
    reset_io();
    link_tcp_config_t cfg = { .io = &FAKE_IO, .io_ctx = NULL };
    link_t *l = make_link(&cfg);

    CHECK(link_open(l) == LINK_SENT_FULL, "夹具应连接成功");
    uint8_t frame[10] = {0};
    s_io.write_results[0] = 0;      /* 一个字节都没写出 */
    s_io.write_count = 1;

    size_t progress = 0;
    link_result_t r = link_send(l, frame, 10, &progress);
    CHECK(r == LINK_BACKPRESSURE, "一字节未写出应为 BACKPRESSURE，实际 %s",
          link_result_name(r));
    CHECK(!link_result_is_error(r), "背压【不是】错误（调用方应退避重试）");
    CHECK(progress == 0, "背压时 progress 必须保持 0（这样整帧重试才安全），实际 %zu",
          progress);
    CHECK(s_io.wire_len == 0, "背压时线上不应有任何字节，实际 %zu", s_io.wire_len);

    link_stats_t st;
    link_get_stats(l, &st);
    CHECK(st.tx_backpressure == 1, "背压应计数");
    CHECK(st.tx_fatal == 0, "背压不应计入 fatal");
    drop_link(l);
}

/* ================= 3. 硬错误 -> FATAL ================= */
static void test_write_error_is_fatal(void)
{
    reset_io();
    link_tcp_config_t cfg = { .io = &FAKE_IO, .io_ctx = NULL };
    link_t *l = make_link(&cfg);

    CHECK(link_open(l) == LINK_SENT_FULL, "夹具应连接成功");
    uint8_t frame[10] = {0};
    s_io.write_results[0] = -1;     /* 连接已断 */
    s_io.write_count = 1;

    link_result_t r = SEND1(l, frame, 10);
    CHECK(r == LINK_FATAL, "写硬错应为 FATAL，实际 %s", link_result_name(r));
    CHECK(link_result_is_error(r), "FATAL 是错误");
    drop_link(l);
}

/* ================= 4. 错误分级：硬错 vs 软错 ================= */
static void test_connect_error_grading(void)
{
    /* 4a. 证书/配置类硬错 -> FATAL（不该盲目退避重试） */
    reset_io();
    s_io.handle_to_return = NULL;
    s_io.hard_fatal = true;
    link_tcp_config_t cfg = { .io = &FAKE_IO, .io_ctx = NULL };
    link_t *l = make_link(&cfg);
    CHECK(link_open(l) == LINK_FATAL, "证书类硬错应 FATAL");
    drop_link(l);

    /* 4b. 网络类软错 -> NOT_READY（可退避重试） */
    reset_io();
    s_io.handle_to_return = NULL;
    s_io.hard_fatal = false;
    link_t *l2 = make_link(&cfg);
    CHECK(link_open(l2) == LINK_NOT_READY, "网络类软错应 NOT_READY");
    CHECK(!link_result_is_error(LINK_NOT_READY), "NOT_READY 不是错误");
    link_destroy(l2);
}

/* ================= 5. 未连接时发 -> NOT_READY（由驱动给出，不预检）===== */
static void test_send_before_open_is_not_ready(void)
{
    reset_io();
    s_io.handle_to_return = NULL;
    s_io.hard_fatal = false;
    link_tcp_config_t cfg = { .io = &FAKE_IO, .io_ctx = NULL };
    link_t *l = make_link(&cfg);

    (void)link_open(l);                  /* 失败：未连接 */
    int writes = s_io.write_calls;
    CHECK(SEND1(l, (const uint8_t *)"x", 1) == LINK_NOT_READY,
          "未连接发送应 NOT_READY");
    CHECK(s_io.write_calls == writes, "未连接时【不应】调用 write");
    drop_link(l);
}

/* ================= 6. 退避序列与抖动（纯函数）================= */
static void test_backoff_sequence(void)
{
    /* 中点抖动（rand_permille=500 时 factor=1000）⇒ 应等于查表值 */
    const uint32_t want[] = { 1000, 2000, 4000, 8000, 16000, 30000, 60000 };
    for (uint32_t i = 0; i < 7; i++) {
        uint32_t got = link_tcp_backoff_ms(i, 500);
        CHECK(got == want[i], "退避[%u] 应为 %u，实际 %u", i, want[i], got);
    }
    /* 超出表的次数：夹在上限，不是溢出/0 */
    CHECK(link_tcp_backoff_ms(7, 500) == 60000, "超出应夹到上限");
    CHECK(link_tcp_backoff_ms(999, 500) == 60000, "极大值应夹到上限");
}

static void test_backoff_jitter(void)
{
    /* 设计 §4.2：±20%。rand=0 -> -20%；rand=1000 -> +20%；中点 -> 原值。 */
    CHECK(link_tcp_backoff_ms(6, 0) == 48000, "-20%% 应为 48000，实际 %u",
          link_tcp_backoff_ms(6, 0));
    CHECK(link_tcp_backoff_ms(6, 1000) == 72000, "+20%% 应为 72000，实际 %u",
          link_tcp_backoff_ms(6, 1000));
    CHECK(link_tcp_backoff_ms(6, 500) == 60000, "中点应无抖动");
    /* 抖动必须落在 ±20% 区间内（对多个随机点） */
    for (uint32_t r = 0; r <= 1000; r += 125) {
        uint32_t v = link_tcp_backoff_ms(6, r);
        CHECK(v >= 48000 && v <= 72000, "抖动越界：rand=%u -> %u", r, v);
    }
    /* 坏输入不崩、不越界 */
    CHECK(link_tcp_backoff_ms(6, 99999) == 72000, "越界随机数应夹到 +20%%");
}

/* ================= 7. 【核心】退避重置条件是应用层握手 ================= */
static void test_backoff_resets_on_handshake_not_connect(void)
{
    reset_io();
    s_io.handle_to_return = NULL;
    s_io.hard_fatal = false;
    link_tcp_config_t cfg = { .io = &FAKE_IO, .io_ctx = NULL };
    link_t *l = make_link(&cfg);

    CHECK(link_tcp_reconnect_attempt(NULL) == 0, "NULL 应安全返回 0");
    CHECK(link_tcp_is_connected(NULL) == false, "NULL 应安全返回 false");

    /* 连续 3 次软失败 -> attempt = 3 */
    (void)link_open(l); (void)link_open(l); (void)link_open(l);
    CHECK(link_tcp_reconnect_attempt(s_ctx) == 3,
          "3 次软失败后 attempt 应为 3，实际 %u", link_tcp_reconnect_attempt(s_ctx));

    /* ⭐ 连上：**不应**重置（设计 §4.2：重置条件是应用层握手，不是 socket connect） */
    s_io.handle_to_return = (void *)0x1;
    CHECK(link_open(l) == LINK_SENT_FULL, "应连接成功");
    CHECK(link_tcp_is_connected(s_ctx) == true, "连上后 is_connected 应为真");
    CHECK(link_tcp_reconnect_attempt(s_ctx) == 3,
          "socket connect 成功【不应】重置退避（设计 §4.2），实际 %u",
          link_tcp_reconnect_attempt(s_ctx));

    /* 空指针安全（不应崩、不应改状态） */
    link_tcp_note_handshake(NULL);
    CHECK(link_tcp_reconnect_attempt(s_ctx) == 3, "空指针调用不应改变状态");

    /* ⭐ 应用层握手完成 -> 才重置 */
    link_tcp_note_handshake(s_ctx);
    CHECK(link_tcp_reconnect_attempt(s_ctx) == 0,
          "应用层握手完成应重置退避，实际 %u", link_tcp_reconnect_attempt(s_ctx));

    drop_link(l);
}

/* ================= 8. mtu = 设计 §5.2 的 16,384 ================= */
static void test_mtu_matches_design(void)
{
    reset_io();
    link_tcp_config_t cfg = { .io = &FAKE_IO, .io_ctx = NULL };
    const link_driver_t *drv = link_tcp_driver();
    CHECK(drv != NULL, "driver 不应为 NULL");
    CHECK(drv->mtu(NULL) == LINK_TCP_MTU_BYTES, "mtu 应为常量");
    CHECK(LINK_TCP_MTU_BYTES == 16384u,
          "mtu 应为 16384（与 MBEDTLS_SSL_IN_CONTENT_LEN 对齐，设计 §5.2）");
    CHECK(strcmp(drv->name, "tcp") == 0, "驱动名应为 tcp");
}

/* ================= 9. P2：超 mtu 在【发出前】被拒 ================= */
static void test_oversize_rejected_before_write(void)
{
    reset_io();
    link_tcp_config_t cfg = { .io = &FAKE_IO, .io_ctx = NULL };
    link_t *l = make_link(&cfg);
    static uint8_t big[LINK_TCP_MTU_BYTES + 1];
    CHECK(link_open(l) == LINK_SENT_FULL, "夹具应连接成功");

    int before = s_io.write_calls;
    CHECK(SEND1(l, big, sizeof(big)) == LINK_PAYLOAD_TOO_BIG,
          "超 mtu 应 PAYLOAD_TOO_BIG");
    CHECK(s_io.write_calls == before, "超 mtu 时【不应】调用 write");

    /* 恰好等于 mtu：允许 */
    CHECK(SEND1(l, big, LINK_TCP_MTU_BYTES) == LINK_SENT_FULL, "恰好 mtu 应允许");
    drop_link(l);
}

/* ================= 10. close 幂等 + 计数 ================= */
static void test_close_is_idempotent(void)
{
    reset_io();
    link_tcp_config_t cfg = { .io = &FAKE_IO, .io_ctx = NULL };
    link_t *l = make_link(&cfg);
    CHECK(link_open(l) == LINK_SENT_FULL, "应连接成功");
    int n = s_io.close_calls;
    drop_link(l);
    CHECK(s_io.close_calls == n + 1, "destroy 应 close 一次，实际 %d", s_io.close_calls - n);
}

/* ================= 11. 违约驱动：报出的字节数超过请求量 =================
 * 为什么值得单独测：若 link 信任驱动报的数，进度就会【越过帧尾】，
 * 下一轮续写会从帧外读到内存里的任意字节并写上线 —— 与 D-30 同族，
 * 都是"流被污染"。驱动违约必须被显式抓住，不能静默夹取。 */
static link_result_t lying_send(void *ctx, const uint8_t *data, size_t len,
                                size_t *written_out)
{
    (void)ctx; (void)data; (void)len;
    if (written_out != NULL) *written_out = 9999;   /* 谎报：远超请求量 */
    return LINK_SENT_PARTIAL;
}
static link_result_t lying_open(void *ctx) { (void)ctx; return LINK_SENT_FULL; }
static void lying_close(void *ctx) { (void)ctx; }
static uint32_t lying_mtu(void *ctx) { (void)ctx; return 1024; }
static bool lying_ready(void *ctx) { (void)ctx; return true; }

static const link_driver_t LYING_DRV = {
    .open = lying_open, .close = lying_close, .send = lying_send,
    .mtu = lying_mtu, .is_ready = lying_ready, .name = "lying",
};

static void test_lying_driver_is_rejected(void)
{
    link_t *l = link_create(&LYING_DRV, NULL);
    CHECK(l != NULL, "link_create 失败");
    uint8_t frame[8] = {0};
    size_t progress = 0;

    link_result_t r = link_send(l, frame, 8, &progress);
    CHECK(r == LINK_FATAL, "驱动谎报字节数应 FATAL，实际 %s", link_result_name(r));
    CHECK(progress == 0, "被拒时进度不得前进，实际 %zu", progress);

    link_stats_t st;
    link_get_stats(l, &st);
    CHECK(st.tx_driver_error == 1, "应计入 tx_driver_error，实际 %u", st.tx_driver_error);
    link_destroy(l);
}

int main(void)
{
    test_partial_write_is_continued();
    test_write_blocked_is_backpressure_not_failure();
    test_write_error_is_fatal();
    test_connect_error_grading();
    test_send_before_open_is_not_ready();
    test_backoff_sequence();
    test_backoff_jitter();
    test_backoff_resets_on_handshake_not_connect();
    test_mtu_matches_design();
    test_oversize_rejected_before_write();
    test_close_is_idempotent();
    test_lying_driver_is_rejected();

    if (s_failures) { printf("link_tcp_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("link_tcp_tests: all checks passed\n");
    return 0;
}
