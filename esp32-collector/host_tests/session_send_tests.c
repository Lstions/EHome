/* session_send_tests.c —— 3.0 会话的**上行入口** session_send
 *
 * ## 为什么单开一个文件
 * session_tests.c 锁的是"退避计数只由应用层握手重置"（设计 §4.2）。
 * 本文件锁的是另一件独立性更强、错了后果更重的事：
 * **session 到底能不能把第一帧发出去**。
 *
 * ## 这一组用例防的是什么（一个真实存在的死路）
 * 补 session_send 之前，session.h 没有任何上行入口；而 SESSION_READY 只能由
 * session_note_handshake()（收到 HelloAck）进入。但设备必须**先发 Hello**
 * 才可能收到 HelloAck ⇒ 接好线的 3.0 链路永远停在 WAIT_HANDSHAKE。
 * 这条死路**没有任何一处会报错**：构建 rc=0、可达性门禁绿、组件单测全绿。
 *
 * 因此最值钱的用例是 test_send_allowed_in_wait_handshake：它证明"链路刚建成
 * 的那一态就能发"—— 若有人日后为了安全给 session_send 加一行
 * "state 必须是 READY"，**这一条会立刻变红**。
 *
 * ## 覆盖
 *   1. WAIT_HANDSHAKE 下允许发，且**真的走到驱动的 write**（不是被本层挡掉）；
 *   2. READY 下同样能发；
 *   3. 未连接时如实报 NOT_READY（驱动给的答案，不是本层编造）；
 *   4. BACKPRESSURE / FATAL / PAYLOAD_TOO_BIG **原样透传**，不被压平；
 *   5. progress 出入参：部分写出后按 progress 续写**不重发已写字节**（D-30）；
 *   6. 参数守卫：s / frame / progress 为 NULL、len == 0 ⇒ LINK_FATAL 且不崩。
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
static uint32_t s_rand = 500;          /* 500/1000 => 无抖动，断言精确值 */
static uint64_t now_ms(void) { return s_now_ms; }
static uint32_t rand_pm(void) { return s_rand; }

static int s_connect_results[16];
static int s_connect_n, s_connect_i;
static int s_read_results[32];
static int s_read_n, s_read_i;

/* ── 写侧：可脚本化的返回值 + **记录每次真的被写出去的字节** ──
 *
 * write 的脚本项语义（与 link_tcp_io_t.write 契约一致）：
 *   > 0  写出这么多字节
 *   0    背压（一字节没写出）
 *   < 0  硬错误
 */
static int s_write_results[16];
static int s_write_n, s_write_i;

#define TX_CAPTURE_MAX 256
static uint8_t s_tx_capture[TX_CAPTURE_MAX];
static size_t  s_tx_capture_len;       /* 线上**按序累积**的字节 */
static int     s_write_calls;          /* 驱动 write 被调用次数 */

static void reset_world(void)
{
    s_now_ms = 0; s_rand = 500;
    s_connect_n = s_connect_i = 0;
    s_read_n = s_read_i = 0;
    s_write_n = s_write_i = 0;
    s_tx_capture_len = 0;
    s_write_calls = 0;
    memset(s_tx_capture, 0, sizeof(s_tx_capture));
}

static void *fake_connect(void *io_ctx, bool *hard_fatal)
{
    (void)io_ctx;
    int r = (s_connect_i < s_connect_n) ? s_connect_results[s_connect_i++] : 0;
    if (r == 1) { if (hard_fatal) *hard_fatal = false; return (void *)1; }
    if (hard_fatal) *hard_fatal = (r < 0);
    return NULL;
}
static void fake_close(void *h) { (void)h; }

/* 假 write：按脚本返回，并**如实记录**写出的字节（用于抓重发）。 */
static int fake_write(void *h, const uint8_t *data, size_t len)
{
    (void)h;
    s_write_calls++;
    int r = (s_write_i < s_write_n) ? s_write_results[s_write_i++] : (int)len;
    if (r <= 0) return r;
    size_t k = ((size_t)r < len) ? (size_t)r : len;
    /* 关键：把每次写出的字节**追加**到线上累积缓冲。
     * 若调用方（或本层）错误地重发整帧，这里会出现重复片段 —— 断言能看见。 */
    for (size_t i = 0; i < k; i++) {
        if (s_tx_capture_len < TX_CAPTURE_MAX) {
            s_tx_capture[s_tx_capture_len++] = data[i];
        }
    }
    return (int)k;
}

static int fake_read(void *h, uint8_t *buf, size_t cap)
{
    (void)h; (void)buf; (void)cap;
    return (s_read_i < s_read_n) ? s_read_results[s_read_i++] : 0;
}

static const link_tcp_io_t FAKE_IO = {
    .connect = fake_connect, .write = fake_write,
    .read = fake_read, .close = fake_close,
};

static bool on_msg(const rx_msg_t *m, void *ctx) { (void)m; (void)ctx; return true; }

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

/* 连上并使会话进入 WAIT_HANDSHAKE（真实路径：poll 一次 do_connect）。 */
static session_t *mk_connected(void)
{
    s_connect_results[s_connect_n++] = 1;   /* connect 成功 */
    session_t *s = mk();
    CHECK(s != NULL, "session_create 失败");
    if (s == NULL) return NULL;
    (void)session_poll(s, NULL);
    CHECK(session_state(s) == SESSION_WAIT_HANDSHAKE,
          "poll 一次后应处于 WAIT_HANDSHAKE，实际 %s",
          session_state_name(session_state(s)));
    return s;
}

/* ══════════════ 1. 最值钱的一条：WAIT_HANDSHAKE 就能发 ══════════════
 *
 * 这就是那条死路的守门用例：若 session_send 被加上 "state 必须是 READY"
 * 的门控，本用例立刻变红。 */
static void test_send_allowed_in_wait_handshake(void)
{
    reset_world();
    session_t *s = mk_connected();
    if (s == NULL) return;

    /* 前置事实：此刻**不是** READY（只有收到 HelloAck 才会 READY）。 */
    CHECK(session_state(s) != SESSION_READY,
          "本用例前提：连上后还不是 READY（实际 %s）",
          session_state_name(session_state(s)));

    /* 一条 Hello 形状的帧（内容不重要，本层不做语义）。 */
    static const uint8_t hello[] = { 0x45, 0x48, 0x30, 0x01, 0x00, 0x00,
                                     0x00, 0x00, 0x00, 0x01, 0x00, 0x02,
                                     0xAA, 0xBB };
    size_t progress = 0;
    link_result_t r = session_send(s, hello, sizeof(hello), &progress);

    CHECK(r == LINK_SENT_FULL,
          "WAIT_HANDSHAKE 下必须真的发出去（期望 SENT_FULL，实际 %s）；"
          "若是 NOT_READY 说明加了 READY 门控，链路永远发不出 Hello",
          link_result_name(r));
    CHECK(progress == sizeof(hello),
          "整帧写出后 progress 应等于 len（期望 %zu，实际 %zu）",
          sizeof(hello), progress);
    CHECK(s_write_calls == 1, "必须真的走到驱动的 write（调用 %d 次）", s_write_calls);
    CHECK(s_tx_capture_len == sizeof(hello),
          "线上应恰好出现 %zu 字节（实际 %zu）", sizeof(hello), s_tx_capture_len);
    CHECK(memcmp(s_tx_capture, hello, sizeof(hello)) == 0,
          "线上字节必须与传入帧逐字节相同");

    /* 发完之后状态**不该被 send 改变**（send 不改状态机）。 */
    CHECK(session_state(s) == SESSION_WAIT_HANDSHAKE,
          "send 不得改变会话状态（实际 %s）",
          session_state_name(session_state(s)));

    session_destroy(s);
}

/* ══════════════ 2. READY 下同样能发 ══════════════ */
static void test_send_allowed_in_ready(void)
{
    reset_world();
    session_t *s = mk_connected();
    if (s == NULL) return;

    session_note_handshake(s);          /* 真实入口：收到 HelloAck */
    CHECK(session_state(s) == SESSION_READY, "note_handshake 后应 READY");

    static const uint8_t frame[4] = { 1, 2, 3, 4 };
    size_t progress = 0;
    link_result_t r = session_send(s, frame, sizeof(frame), &progress);
    CHECK(r == LINK_SENT_FULL, "READY 下应发得出去（实际 %s）", link_result_name(r));
    CHECK(progress == sizeof(frame), "progress 应完整推进");
    CHECK(session_state(s) == SESSION_READY, "READY 不得被 send 改掉");

    session_destroy(s);
}

/* ══════════════ 3. 未连接时如实报 NOT_READY ══════════════ */
static void test_send_before_connect_is_not_ready(void)
{
    reset_world();
    session_t *s = mk();                /* 还没 poll ⇒ DOWN，未连接 */
    if (s == NULL) return;
    CHECK(session_state(s) == SESSION_DOWN, "新建会话应为 DOWN");

    static const uint8_t frame[4] = { 1, 2, 3, 4 };
    size_t progress = 0;
    link_result_t r = session_send(s, frame, sizeof(frame), &progress);
    CHECK(r == LINK_NOT_READY,
          "未连接时必须如实报 NOT_READY（实际 %s）—— 这是驱动给的答案，"
          "不是本层拒绝", link_result_name(r));
    CHECK(s_write_calls == 0, "未连接时不得调用驱动的 write（调用 %d 次）", s_write_calls);
    CHECK(s_tx_capture_len == 0, "未连接时线上不得出现字节");

    session_destroy(s);
}

/* ══════════════ 4. 结果原样透传，不被压平 ══════════════ */
static void test_result_passthrough_not_flattened(void)
{
    static const uint8_t f1[8] = { 1, 2, 3, 4, 5, 6, 7, 8 };
    session_t *s;
    size_t p;

    /* 4a. 背压：驱动返回 0 ⇒ BACKPRESSURE，**不是** FATAL */
    reset_world();
    s = mk_connected();
    if (s == NULL) return;
    s_write_results[s_write_n++] = 0;   /* 一字节没写出 = 背压 */
    p = 0;
    link_result_t r1 = session_send(s, f1, sizeof(f1), &p);
    CHECK(r1 == LINK_BACKPRESSURE,
          "驱动背压必须原样透传成 BACKPRESSURE（实际 %s）", link_result_name(r1));
    CHECK(p == 0, "背压时 progress 不得前进（实际 %zu）", p);
    CHECK(s_tx_capture_len == 0, "背压时线上不得出现字节");
    session_destroy(s);

    /* 4b. 驱动硬错误（<0）⇒ LINK_FATAL，不得被压成 BACKPRESSURE */
    reset_world();
    s = mk_connected();
    if (s == NULL) return;
    s_write_results[s_write_n++] = LINK_TCP_IO_ERROR;   /* -2 */
    p = 0;
    link_result_t r2 = session_send(s, f1, sizeof(f1), &p);
    CHECK(r2 == LINK_FATAL, "驱动硬错误应透传成 FATAL（实际 %s）", link_result_name(r2));
    session_destroy(s);

    /* 4c. 超 MTU ⇒ PAYLOAD_TOO_BIG，且**不得调用**驱动的 write（P2） */
    reset_world();
    s = mk_connected();
    if (s == NULL) return;
    static uint8_t huge[LINK_TCP_MTU_BYTES + 1];
    p = 0;
    link_result_t r3 = session_send(s, huge, sizeof(huge), &p);
    CHECK(r3 == LINK_PAYLOAD_TOO_BIG,
          "超 MTU 应报 PAYLOAD_TOO_BIG（实际 %s）", link_result_name(r3));
    CHECK(s_write_calls == 0,
          "超 MTU 时必须在**发出前**拦下，不得调用驱动 write（调用 %d 次）",
          s_write_calls);
    session_destroy(s);
}

/* ══════════════ 5. progress 续写：绝不重发已写字节（D-30）══════════════
 *
 * 第二条"写错也没人报错"的用例：若在部分写出后重发整帧，线上会出现
 * frame[0:k] **两次**，接收端定界器无法自愈（静默流污染）。
 * 断言方式：把每次 write 的字节**按序累积**，要求它与原帧逐字节相等且
 * 长度恰好相等 —— 任何重发都会让长度变大、内容错位。 */
static void test_partial_write_resumes_without_resending(void)
{
    reset_world();
    session_t *s = mk_connected();
    if (s == NULL) return;

    static const uint8_t frame[10] = { 0xA0, 0xA1, 0xA2, 0xA3, 0xA4,
                                       0xA5, 0xA6, 0xA7, 0xA8, 0xA9 };
    /* 脚本：先写 3 B，再写 4 B，最后写剩下 3 B（模拟 TCP 短写）。 */
    s_write_results[s_write_n++] = 3;
    s_write_results[s_write_n++] = 4;
    s_write_results[s_write_n++] = 3;

    size_t progress = 0;
    int guard = 0;
    link_result_t r = LINK_SENT_PARTIAL;
    /* 按 link.h 规定的续写模式推进（这正是调用方该写的循环）。 */
    while (r == LINK_SENT_PARTIAL && guard++ < 10) {
        r = session_send(s, frame, sizeof(frame), &progress);
    }

    CHECK(r == LINK_SENT_FULL, "循环结束应是 SENT_FULL（实际 %s）", link_result_name(r));
    CHECK(progress == sizeof(frame), "progress 应累计到 len（实际 %zu）", progress);

    /* 核心断言：线上字节 == 原帧，不多不少不重。 */
    CHECK(s_tx_capture_len == sizeof(frame),
          "线上总字节数必须恰好等于帧长（期望 %zu，实际 %zu）；"
          "多了说明重发了已写字节（D-30）", sizeof(frame), s_tx_capture_len);
    CHECK(memcmp(s_tx_capture, frame, sizeof(frame)) == 0,
          "线上字节序列必须与原帧逐字节相同（重发会造成重复片段）");
    CHECK(s_write_calls == 3,
          "驱动 write 应被调用 3 次（每次只写剩余部分），实际 %d 次", s_write_calls);

    session_destroy(s);
}

/* ══════════════ 6. 参数守卫（不得崩）══════════════ */
static void test_argument_guards(void)
{
    reset_world();
    session_t *s = mk_connected();
    if (s == NULL) return;

    static const uint8_t frame[4] = { 1, 2, 3, 4 };
    size_t progress = 0;

    CHECK(session_send(NULL, frame, sizeof(frame), &progress) == LINK_FATAL,
          "s==NULL 应返回 FATAL");
    CHECK(session_send(s, NULL, sizeof(frame), &progress) == LINK_FATAL,
          "frame==NULL 应返回 FATAL");
    CHECK(session_send(s, frame, 0, &progress) == LINK_FATAL,
          "len==0 应返回 FATAL");
    CHECK(session_send(s, frame, sizeof(frame), NULL) == LINK_FATAL,
          "progress==NULL 应返回 FATAL（不能假装写完）");
    CHECK(s_write_calls == 0, "守卫路径不得调用驱动 write（调用 %d 次）", s_write_calls);

    /* progress 越界（把别的帧的进度传进来了）⇒ FATAL，不猜不夹取 */
    size_t bad = sizeof(frame) + 1;
    CHECK(session_send(s, frame, sizeof(frame), &bad) == LINK_FATAL,
          "progress > len 应返回 FATAL（调用方违约，不得静默夹取）");
    CHECK(bad == sizeof(frame) + 1, "越界时不得夹取/改写调用方的 progress");

    /* progress == len（已写完）：幂等返回 FULL，且不再写线上任何字节 */
    size_t done = sizeof(frame);
    s_write_calls = 0;
    CHECK(session_send(s, frame, sizeof(frame), &done) == LINK_SENT_FULL,
          "progress==len 应幂等返回 SENT_FULL");
    CHECK(s_write_calls == 0, "已写完不得再写线上字节（调用 %d 次）", s_write_calls);

    session_destroy(s);

    /* s == NULL 时同样不能崩，且不允许把进度假装推进 */
    size_t p2 = 0;
    CHECK(session_send(NULL, frame, sizeof(frame), &p2) == LINK_FATAL, "NULL 会话");
    CHECK(p2 == 0, "NULL 会话时不得改动 progress（实际 %zu）", p2);
}

int main(void)
{
    test_send_allowed_in_wait_handshake();
    test_send_allowed_in_ready();
    test_send_before_connect_is_not_ready();
    test_result_passthrough_not_flattened();
    test_partial_write_resumes_without_resending();
    test_argument_guards();

    if (s_failures) { printf("session_send_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("session_send_tests: all checks passed\n");
    return 0;
}
