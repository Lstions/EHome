/* session_tests.c —— 3.0 会话状态机
 *
 * 本用例重点是设计 §4.2 那条最容易写错的规则：
 *   **退避计数只由应用层握手重置，不由 socket connect 重置。**
 *
 * 写错的后果是一种很难查的故障：链路能连上但业务不通（证书/协议/后端未就绪）
 * ⇒ 每次 connect 成功就把退避归零 ⇒ **每 1 秒重连一次（重连风暴）**，
 * 而日志上只看到"不断重连"，看不出"其实一直连上了"。
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "session.h"
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

/* ================= 可注入的假世界 ================= */
static uint64_t s_now_ms;
static uint32_t s_rand = 500;          /* 500/1000 => 无抖动，便于断言精确值 */
static uint64_t now_ms(void) { return s_now_ms; }
static uint32_t rand_pm(void) { return s_rand; }

static int s_connect_results[16];   /* 1=成功 0=软失败 -1=硬失败 */
static int s_connect_n, s_connect_i;
static int s_read_results[32];      /* >0 字节数 0=暂无 -1=关闭 -2=错误 */
static int s_read_n, s_read_i;
static int s_connect_calls, s_read_calls;
static uint8_t s_pending[256];
static size_t  s_pending_len;

static void reset_world(void)
{
    s_now_ms = 0; s_rand = 500;
    s_connect_n = s_connect_i = 0;
    s_read_n = s_read_i = 0;
    s_connect_calls = s_read_calls = 0;
    s_pending_len = 0;
}

static void *fake_connect(void *io_ctx, bool *hard_fatal)
{
    (void)io_ctx;
    s_connect_calls++;
    int r = (s_connect_i < s_connect_n) ? s_connect_results[s_connect_i++] : 0;
    if (r == 1) { if (hard_fatal) *hard_fatal = false; return (void *)1; }
    if (hard_fatal) *hard_fatal = (r < 0);
    return NULL;
}
static void fake_close(void *h) { (void)h; }
static int fake_write(void *h, const uint8_t *d, size_t n) { (void)h; (void)d; return (int)n; }
static int fake_read(void *h, uint8_t *buf, size_t cap)
{
    (void)h;
    s_read_calls++;
    int r = (s_read_i < s_read_n) ? s_read_results[s_read_i++] : 0;
    if (r <= 0) return r;
    size_t k = (size_t)r < cap ? (size_t)r : cap;
    if (k > s_pending_len) k = s_pending_len;
    memcpy(buf, s_pending, k);
    return (int)k;
}
static const link_tcp_io_t FAKE_IO = {
    .connect = fake_connect, .write = fake_write,
    .read = fake_read, .close = fake_close,
};

static int s_msgs;
static size_t s_last_len;
static bool on_msg(const rx_msg_t *m, void *ctx)
{
    (void)ctx; s_msgs++; s_last_len = m->payload_len; return true;
}

static uint8_t s_rbuf[256];
static session_t *mk(void)
{
    session_config_t cfg = {
        .io = &FAKE_IO, .io_ctx = NULL,
        .max_payload = 512,
        .rx_buf = s_rbuf, .rx_buf_cap = sizeof(s_rbuf),
        .now_ms = now_ms, .rand_permille = rand_pm,
        .on_msg = on_msg, .on_msg_ctx = NULL,
    };
    return session_create(&cfg);
}

/* ============ 1. socket 连上**不等于**握手：退避不得归零 ============ */
static void test_connect_success_does_not_reset_backoff(void)
{
    reset_world();
    session_t *s = mk();
    CHECK(s != NULL, "创建失败");

    uint32_t seen[5];
    for (int i = 0; i < 5; i++) {
        s_connect_results[s_connect_n++] = 1;    /* connect 成功 */
        s_read_results[s_read_n++] = -1;         /* 随后链路关闭 */

        /* ⚠ 时间必须推进过退避窗口，否则会一直停在 BACKOFF 而不重试
         * （我第一版漏了这一步：时钟不动，循环空转）。退避上限 60s，
         * 故每次推进 70s 足以跨过任何一档。 */
        s_now_ms += 70000;

        session_poll(s, NULL);
        CHECK(session_state(s) == SESSION_WAIT_HANDSHAKE,
              "第 %d 次连接后应处于 WAIT_HANDSHAKE，实际 %s",
              i, session_state_name(session_state(s)));

        /* 关键：**没有**调用 session_note_handshake()，退避计数必须继续增长 */
        session_poll(s, NULL);
        CHECK(session_state(s) == SESSION_BACKOFF,
              "第 %d 次链路掉后应 BACKOFF，实际 %s",
              i, session_state_name(session_state(s)));
        seen[i] = session_reconnect_attempt(s);
    }

    CHECK(seen[4] > seen[0],
          "**退避计数必须递增**（%u -> %u）—— 若 connect 成功就归零，会形成重连风暴",
          seen[0], seen[4]);

    session_stats_t st;
    session_get_stats(s, &st);
    CHECK(st.connects_ok == 5, "应记录 5 次 connect 成功，实际 %u", st.connects_ok);
    CHECK(st.handshakes == 0, "**从未握手**（handshakes 应为 0），实际 %u", st.handshakes);
    CHECK(st.backoffs_entered == 5, "应 5 次进入退避，实际 %u", st.backoffs_entered);

    session_destroy(s);
}

/* ============ 2. 握手完成才归零，并进入 READY ============ */
static void test_handshake_resets_backoff(void)
{
    reset_world();
    session_t *s = mk();

    s_connect_results[0] = 0; s_connect_results[1] = 0; s_connect_n = 2;
    session_poll(s, NULL);
    s_now_ms += 10000;
    session_poll(s, NULL);
    uint32_t before = session_reconnect_attempt(s);
    CHECK(before > 0, "应有累积的失败次数，实际 %u", before);

    s_connect_results[s_connect_n++] = 1;
    s_now_ms += 120000;
    session_poll(s, NULL);
    CHECK(session_state(s) == SESSION_WAIT_HANDSHAKE, "应连上");

    session_note_handshake(s);
    CHECK(session_state(s) == SESSION_READY, "握手后应 READY");
    CHECK(session_reconnect_attempt(s) == 0,
          "**握手后退避计数必须归零**，实际 %u", session_reconnect_attempt(s));

    session_stats_t st;
    session_get_stats(s, &st);
    CHECK(st.handshakes == 1, "应记录 1 次握手，实际 %u", st.handshakes);
    session_destroy(s);
}

/* ============ 3. 硬失败 -> FATAL，不盲目重试 ============ */
static void test_hard_failure_goes_fatal(void)
{
    reset_world();
    session_t *s = mk();
    s_connect_results[0] = -1;  s_connect_n = 1;

    session_poll(s, NULL);
    CHECK(session_state(s) == SESSION_FATAL,
          "硬失败应 FATAL，实际 %s", session_state_name(session_state(s)));

    int calls_before = s_connect_calls;
    for (int i = 0; i < 10; i++) { s_now_ms += 100000; session_poll(s, NULL); }
    CHECK(s_connect_calls == calls_before,
          "FATAL 状态**不得再尝试连接**（%d -> %d）", calls_before, s_connect_calls);

    session_stats_t st;
    session_get_stats(s, &st);
    CHECK(st.connects_hard_fail == 1, "应记录 1 次硬失败，实际 %u", st.connects_hard_fail);
    CHECK(st.connects_soft_fail == 0, "硬失败**不应**计入软失败，实际 %u", st.connects_soft_fail);
    session_destroy(s);
}

/* ============ 4. 软失败 -> BACKOFF，到点才重试 ============ */
static void test_soft_failure_backs_off_then_retries(void)
{
    reset_world();
    session_t *s = mk();
    s_connect_results[0] = 0; s_connect_results[1] = 1; s_connect_n = 2;

    session_poll(s, NULL);
    CHECK(session_state(s) == SESSION_BACKOFF, "软失败应 BACKOFF");
    CHECK(s_connect_calls == 1, "只应尝试 1 次，实际 %d", s_connect_calls);

    s_now_ms += 1;
    session_poll(s, NULL);
    CHECK(s_connect_calls == 1, "未到点不应重试，实际 %d", s_connect_calls);

    s_now_ms += 2000;
    session_poll(s, NULL);
    CHECK(s_connect_calls == 2, "到点后应重试，实际 %d", s_connect_calls);
    CHECK(session_state(s) == SESSION_WAIT_HANDSHAKE, "第二次成功应进入 WAIT_HANDSHAKE");
    session_destroy(s);
}

/* ============ 5. 退避时间**递增** ============ */
static void test_backoff_grows(void)
{
    reset_world();
    session_t *s = mk();
    uint64_t delays[3];
    for (int i = 0; i < 3; i++) {
        uint64_t t0 = s_now_ms;
        s_connect_results[s_connect_n++] = 0;
        session_poll(s, NULL);

        uint64_t d = 0;
        int before = s_connect_calls;
        for (uint64_t step = 50; step <= 65000; step += 50) {
            s_now_ms = t0 + step;
            if (s_connect_calls > before) break;
            int c0 = s_connect_calls;
            session_poll(s, NULL);
            if (s_connect_calls > c0) { d = step; break; }
        }
        delays[i] = d;
    }
    CHECK(delays[0] > 0, "首次退避应 > 0，实际 %llu", (unsigned long long)delays[0]);
    CHECK(delays[1] > delays[0],
          "退避应递增：%llu -> %llu", (unsigned long long)delays[0],
          (unsigned long long)delays[1]);
    CHECK(delays[2] >= delays[1],
          "退避不应回退：%llu -> %llu", (unsigned long long)delays[1],
          (unsigned long long)delays[2]);
    session_destroy(s);
}

/* ============ 5b. ⭐ 退避**恰好到期**那一刻必须重试（边界） ============
 * 为什么单独测这一刻：其余用例都以 50ms 为步长推进时间，**永远踩不到"恰好等于
 * 到期时刻"**。于是 "now < deadline" 写成 "now <= deadline" 这种边界错误
 * 不会被抓到 —— 而那会让设备**永远等不到重试**（每轮都差一个刻度）。
 * 这是变异 M103 没被抓住后补的用例。 */
static void test_backoff_exact_deadline_retries(void)
{
    reset_world();
    session_t *s = mk();
    s_connect_results[0] = 0;   /* 软失败 */
    s_connect_results[1] = 1;   /* 第二次成功 */
    s_connect_n = 2;

    session_poll(s, NULL);
    CHECK(session_state(s) == SESSION_BACKOFF, "应进入 BACKOFF");
    CHECK(s_connect_calls == 1, "只尝试过 1 次，实际 %d", s_connect_calls);

    /* 第 0 次退避 = 1000ms（抖动取 500/1000 => ±0%） */
    /* 到期的**前一毫秒**：不应重试 */
    s_now_ms = 1000 - 1;
    session_poll(s, NULL);
    CHECK(s_connect_calls == 1, "差 1ms 不应重试，实际 %d", s_connect_calls);

    /* ⭐ **恰好到期**：必须重试 */
    s_now_ms = 1000;
    session_poll(s, NULL);
    CHECK(s_connect_calls == 2,
          "**恰好到期必须重试**（差一个刻度就永远等不到），实际 %d 次", s_connect_calls);
    CHECK(session_state(s) == SESSION_WAIT_HANDSHAKE, "到点重试成功应进入 WAIT_HANDSHAKE");
    session_destroy(s);
}

/* ============ 6. 交付消息 ============ */
static void test_delivers_messages(void)
{
    reset_world();
    s_msgs = 0;
    session_t *s = mk();

    const uint8_t pl[3] = {1,2,3};
    wire_header_t h = { .ver = 0x30, .type = 0x03, .flags = 0, .seq = 1, .payload_len = 3 };
    wire_encode_header(s_pending, sizeof(s_pending), &h);
    memcpy(s_pending + WIRE_HEADER_BYTES, pl, 3);
    s_pending_len = WIRE_HEADER_BYTES + 3;

    s_connect_results[s_connect_n++] = 1;
    s_read_results[s_read_n++] = (int)s_pending_len;
    s_read_results[s_read_n++] = 0;

    session_poll(s, NULL);
    CHECK(session_state(s) == SESSION_WAIT_HANDSHAKE, "应连上");
    session_note_handshake(s);

    uint32_t d = 0;
    session_poll(s, &d);
    CHECK(s_msgs == 1, "应交付 1 条消息，实际 %d", s_msgs);
    CHECK(s_last_len == 3, "载荷长度应为 3，实际 %zu", s_last_len);

    session_stats_t st;
    session_get_stats(s, &st);
    CHECK(st.msgs_delivered == 1, "应记录 1 条，实际 %u", st.msgs_delivered);
    session_destroy(s);
}

/* ============ 7. 参数校验 ============ */
static void test_create_validation(void)
{
    session_config_t base = {
        .io = &FAKE_IO, .max_payload = 512,
        .rx_buf = s_rbuf, .rx_buf_cap = sizeof(s_rbuf),
        .now_ms = now_ms, .on_msg = on_msg,
    };
    session_config_t c;

    c = base; c.io = NULL;        CHECK(session_create(&c) == NULL, "缺 io 应拒绝");
    c = base; c.rx_buf = NULL;    CHECK(session_create(&c) == NULL, "缺 rx_buf 应拒绝");
    c = base; c.rx_buf_cap = 0;   CHECK(session_create(&c) == NULL, "rx_buf_cap=0 应拒绝");
    c = base; c.now_ms = NULL;    CHECK(session_create(&c) == NULL, "缺时钟应拒绝");
    c = base; c.on_msg = NULL;    CHECK(session_create(&c) == NULL, "缺回调应拒绝");
    CHECK(session_create(NULL) == NULL, "NULL cfg 应拒绝");
}

int main(void)
{
    test_connect_success_does_not_reset_backoff();
    test_handshake_resets_backoff();
    test_hard_failure_goes_fatal();
    test_soft_failure_backs_off_then_retries();
    test_backoff_grows();
    test_backoff_exact_deadline_retries();
    test_delivers_messages();
    test_create_validation();

    if (s_failures) { printf("session_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("session_tests: all checks passed\n");
    return 0;
}
