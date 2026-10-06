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

static int fake_write(void *handle, const uint8_t *data, size_t len)
{
    (void)handle; (void)data;
    s_io.write_calls++;
    if (s_io.write_idx >= s_io.write_count) {
        s_io.total_written += len;
        return (int)len;                 /* 脚本用尽：视为一次写完（避免测试挂死） */
    }
    int r = s_io.write_results[s_io.write_idx++];
    if (r > 0) {
        size_t n = (size_t)r;
        if (n > len) n = len;            /* 不许"写出"超过请求量 */
        s_io.total_written += n;
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
    CHECK(link_open(l) == LINK_SENT, "夹具应连接成功");

    uint8_t frame[10] = {0};
    s_io.write_results[0] = 4;
    s_io.write_results[1] = 3;
    s_io.write_results[2] = 3;
    s_io.write_count = 3;

    CHECK(link_send(l, frame, 10) == LINK_SENT, "部分写后应最终 SENT");
    CHECK(s_io.write_calls == 3, "应续写 3 次，实际 %d", s_io.write_calls);
    CHECK(s_io.total_written == 10, "应写完 10 字节，实际 %zu", s_io.total_written);
    drop_link(l);
}

/* ================= 2. 写不动 = 背压（可重试，不是失败）================= */
static void test_write_blocked_is_backpressure_not_failure(void)
{
    reset_io();
    link_tcp_config_t cfg = { .io = &FAKE_IO, .io_ctx = NULL };
    link_t *l = make_link(&cfg);

    CHECK(link_open(l) == LINK_SENT, "夹具应连接成功");
    uint8_t frame[10] = {0};
    s_io.write_results[0] = 4;      /* 先写一部分 */
    s_io.write_results[1] = 0;      /* 然后缓冲满 */
    s_io.write_count = 2;

    link_result_t r = link_send(l, frame, 10);
    CHECK(r == LINK_BACKPRESSURE, "缓冲满应为 BACKPRESSURE，实际 %s", link_result_name(r));
    CHECK(!link_result_is_error(r), "背压【不是】错误（调用方应退避重试）");

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

    CHECK(link_open(l) == LINK_SENT, "夹具应连接成功");
    uint8_t frame[10] = {0};
    s_io.write_results[0] = -1;     /* 连接已断 */
    s_io.write_count = 1;

    link_result_t r = link_send(l, frame, 10);
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
    CHECK(link_send(l, (const uint8_t *)"x", 1) == LINK_NOT_READY,
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
    CHECK(link_open(l) == LINK_SENT, "应连接成功");
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
    CHECK(link_open(l) == LINK_SENT, "夹具应连接成功");

    int before = s_io.write_calls;
    CHECK(link_send(l, big, sizeof(big)) == LINK_PAYLOAD_TOO_BIG,
          "超 mtu 应 PAYLOAD_TOO_BIG");
    CHECK(s_io.write_calls == before, "超 mtu 时【不应】调用 write");

    /* 恰好等于 mtu：允许 */
    CHECK(link_send(l, big, LINK_TCP_MTU_BYTES) == LINK_SENT, "恰好 mtu 应允许");
    drop_link(l);
}

/* ================= 10. close 幂等 + 计数 ================= */
static void test_close_is_idempotent(void)
{
    reset_io();
    link_tcp_config_t cfg = { .io = &FAKE_IO, .io_ctx = NULL };
    link_t *l = make_link(&cfg);
    CHECK(link_open(l) == LINK_SENT, "应连接成功");
    int n = s_io.close_calls;
    drop_link(l);
    CHECK(s_io.close_calls == n + 1, "destroy 应 close 一次，实际 %d", s_io.close_calls - n);
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

    if (s_failures) { printf("link_tcp_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("link_tcp_tests: all checks passed\n");
    return 0;
}
