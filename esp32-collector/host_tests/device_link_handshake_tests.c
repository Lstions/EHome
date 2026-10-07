/* device_link_handshake_tests.c —— 3.0 链路应用层握手的**决策**（纯函数）
 *
 * ## 为什么这组用例值钱
 * 在补它之前，`main/` 里**没有任何地方**调用 session_send 或
 * session_note_handshake ⇒ 即使打开开关，链路也永远停在 WAIT_HANDSHAKE：
 * 发不出 Hello ⇒ 收不到 HelloAck ⇒ 进不了 READY。
 * 这条死路**构建绿、可达性门禁绿、组件单测全绿**，一处都不报错。
 *
 * 三条最值钱的用例（写错都"看不出来"）：
 *   1. test_hello_sent_only_once          —— 防止每轮 poll 都发 Hello（打爆对端）
 *   2. test_hello_ack_is_the_only_way_in  —— 防止"连上就 note"（退避永不重置/风暴）
 *   3. test_reconnect_allows_resending_hello —— ⭐ 最容易漏：重连后必须能重发，
 *      否则表现为"第一次没连上就再也连不上"
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>

#include "device_link_handshake.h"
#include "frame_codec.h"

/* 本地状态名 —— 刻意**不**链接 session.c：本用例只测**纯决策**，
 * 不该把整个会话状态机（及其 link/rx_pump/wire 依赖链）拖进这个 target。
 * 名字只用于失败信息。 */
static const char *st_name(session_state_t s)
{
    switch (s) {
    case SESSION_DOWN:           return "DOWN";
    case SESSION_WAIT_HANDSHAKE: return "WAIT_HANDSHAKE";
    case SESSION_READY:          return "READY";
    case SESSION_BACKOFF:        return "BACKOFF";
    case SESSION_FATAL:          return "FATAL";
    default:                     return "?";
    }
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

/* ══════════════ 1. 决策表逐格覆盖 ══════════════ */

static void test_wait_handshake_sends_hello_once(void)
{
    /* 尚未发过 ⇒ 发 */
    CHECK(dlhs_decide(SESSION_WAIT_HANDSHAKE, false, 0) == DLHS_SEND_HELLO,
          "WAIT_HANDSHAKE 且未发过 ⇒ 应 SEND_HELLO");
    /* ⭐ 已发过 ⇒ 什么都不做（**不是**再发一次） */
    CHECK(dlhs_decide(SESSION_WAIT_HANDSHAKE, true, 0) == DLHS_IDLE,
          "WAIT_HANDSHAKE 且已发过 ⇒ 应 IDLE（只发一次）");

    /* 连续多轮问：只有第一轮给 SEND_HELLO，其余都是 IDLE。
     * 这一条模拟调用方的循环 —— 若实现是"WAIT_HANDSHAKE 就发"，
     * 会在第 2..N 轮全部变红。 */
    bool hello_sent = false;
    int sends = 0;
    for (int i = 0; i < 20; i++) {
        dlhs_action_t a = dlhs_decide(SESSION_WAIT_HANDSHAKE, hello_sent, 0);
        if (a == DLHS_SEND_HELLO) { sends++; hello_sent = true; }  /* 调用方推进 */
    }
    CHECK(sends == 1, "20 轮 poll 里只应发 1 次 Hello（实际 %d 次）—— "
                      "每轮都发会以 100 Hz 打爆对端", sends);
}

static void test_hello_ack_leads_to_note(void)
{
    /* 在 WAIT_HANDSHAKE 收到 0x12 ⇒ NOTE（这是真实时序！） */
    CHECK(dlhs_decide(SESSION_WAIT_HANDSHAKE, true, MSG_HELLO_ACK) == DLHS_NOTE_HANDSHAKE,
          "WAIT_HANDSHAKE 收到 0x12 ⇒ 应 NOTE_HANDSHAKE");
    /* 还没发过 Hello 就收到 0x12（异常但对端可能主动发）⇒ 仍然 note，
     * 不因为 hello_sent=false 而丢掉这个 HelloAck。 */
    CHECK(dlhs_decide(SESSION_WAIT_HANDSHAKE, false, MSG_HELLO_ACK) == DLHS_NOTE_HANDSHAKE,
          "收到 0x12 即应 NOTE（不依赖 hello_sent）");
    /* READY 之后再收到 0x12 ⇒ 不再重复 note（幂等） */
    CHECK(dlhs_decide(SESSION_READY, true, MSG_HELLO_ACK) == DLHS_DONE,
          "READY 后收到 0x12 ⇒ 应 DONE（不重复 note，避免退避被反复清零）");
}

static void test_ready_is_quiet(void)
{
    CHECK(dlhs_decide(SESSION_READY, true, 0) == DLHS_DONE,
          "READY 无消息 ⇒ DONE");
    CHECK(dlhs_decide(SESSION_READY, true, 0x22) == DLHS_DONE,
          "READY 收到非 HelloAck ⇒ DONE（不打扰）");
    CHECK(dlhs_decide(SESSION_READY, false, 0) == DLHS_DONE,
          "READY ⇒ DONE（不因 hello_sent=false 而重发 Hello）");
}

static void test_not_connected_is_idle(void)
{
    const session_state_t down_states[] = { SESSION_DOWN, SESSION_BACKOFF, SESSION_FATAL };
    for (size_t i = 0; i < sizeof(down_states) / sizeof(down_states[0]); i++) {
        CHECK(dlhs_decide(down_states[i], false, 0) == DLHS_IDLE,
              "%s 未连上 ⇒ IDLE（发了也白发）", st_name(down_states[i]));
        /* 未连上却收到 HelloAck（不该发生，但不得崩、也不得静默丢弃） */
        CHECK(dlhs_decide(down_states[i], false, MSG_HELLO_ACK) == DLHS_NOTE_HANDSHAKE,
              "%s 收到 0x12 ⇒ 仍应 NOTE（如实响应，不吞）",
              st_name(down_states[i]));
    }
}

/* ══════════════ 2. ⭐ 重连后允许重发 Hello（最容易漏的一条）══════════════ */

static void test_reconnect_allows_resending_hello(void)
{
    /* 场景：第一次连上 → 发 Hello → 断了 → 重新连上 → **必须能再发**。
     *
     * 若 hello_sent 只在启动时清零，第二段就永远发不出 Hello，
     * 表现为"第一次没连上就再也连不上"。 */

    /* 链路代际判据：DOWN -> WAIT_HANDSHAKE 是"新连上" */
    CHECK(dlhs_link_generation_changed(SESSION_DOWN, SESSION_WAIT_HANDSHAKE),
          "DOWN -> WAIT_HANDSHAKE 应判为链路重建");
    CHECK(dlhs_link_generation_changed(SESSION_BACKOFF, SESSION_WAIT_HANDSHAKE),
          "BACKOFF -> WAIT_HANDSHAKE 应判为链路重建"
          "（session_poll 在退避到点时会 BACKOFF->DOWN->WAIT 一气呵成）");
    CHECK(dlhs_link_generation_changed(SESSION_READY, SESSION_WAIT_HANDSHAKE),
          "READY -> WAIT_HANDSHAKE 应判为链路重建（掉线后重连）");
    CHECK(dlhs_link_generation_changed(SESSION_FATAL, SESSION_WAIT_HANDSHAKE),
          "FATAL -> WAIT_HANDSHAKE 应判为链路重建");

    /* ⚠ 反例：WAIT_HANDSHAKE -> WAIT_HANDSHAKE **不是**重建 ——
     * 否则会把 hello_sent 反复清零，退化成"每轮都发 Hello"。 */
    CHECK(!dlhs_link_generation_changed(SESSION_WAIT_HANDSHAKE, SESSION_WAIT_HANDSHAKE),
          "WAIT_HANDSHAKE 持续**不得**判为重建（否则退化成每轮都发）");
    CHECK(!dlhs_link_generation_changed(SESSION_WAIT_HANDSHAKE, SESSION_READY),
          "WAIT -> READY 不是重建");
    CHECK(!dlhs_link_generation_changed(SESSION_READY, SESSION_READY),
          "READY 持续不是重建");

    /* 端到端模拟调用方的循环：两段连接，各自应各发 1 次 Hello。 */
    session_state_t seq[] = {
        SESSION_DOWN, SESSION_DOWN, SESSION_WAIT_HANDSHAKE, SESSION_WAIT_HANDSHAKE,
        SESSION_WAIT_HANDSHAKE, SESSION_BACKOFF, SESSION_WAIT_HANDSHAKE,
        SESSION_WAIT_HANDSHAKE, SESSION_WAIT_HANDSHAKE,
    };
    bool hello_sent = false;
    int sends = 0;
    session_state_t prev = SESSION_DOWN;
    for (size_t i = 0; i < sizeof(seq) / sizeof(seq[0]); i++) {
        session_state_t now = seq[i];
        if (dlhs_link_generation_changed(prev, now)) hello_sent = false;  /* 调用方清零 */
        dlhs_action_t a = dlhs_decide(now, hello_sent, 0);
        if (a == DLHS_SEND_HELLO) { sends++; hello_sent = true; }
        prev = now;
    }
    CHECK(sends == 2, "两段连接应各发 1 次 Hello（实际 %d 次）—— "
                      "若为 1 说明重连后没有重发（第一次没连上就再也连不上）", sends);
}

/* ══════════════ 3. 名字（日志/诊断用）══════════════ */
static void test_action_names_nonempty(void)
{
    const dlhs_action_t all[] = { DLHS_IDLE, DLHS_SEND_HELLO,
                                  DLHS_NOTE_HANDSHAKE, DLHS_DONE };
    for (size_t i = 0; i < sizeof(all) / sizeof(all[0]); i++) {
        CHECK(dlhs_action_name(all[i])[0] != '\0', "动作名不应为空");
    }
    CHECK(dlhs_action_name((dlhs_action_t)99) != NULL, "未知枚举应返回非 NULL");
    /* 0x12 这个常量必须来自 frame_codec.h 的单一来源（P4） */
    CHECK(MSG_HELLO_ACK == 0x12, "MSG_HELLO_ACK 应为 0x12（实际 0x%02X）", MSG_HELLO_ACK);
}

int main(void)
{
    test_wait_handshake_sends_hello_once();
    test_hello_ack_leads_to_note();
    test_ready_is_quiet();
    test_not_connected_is_idle();
    test_reconnect_allows_resending_hello();
    test_action_names_nonempty();

    if (s_failures) { printf("device_link_handshake_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("device_link_handshake_tests: all checks passed\n");
    return 0;
}
