/* session_transport_tests.c —— 3.0 会话→transport 适配的**纯判定**（宿主可测）
 *
 * ## 为什么这些断言值得存在
 *
 * 适配层里只有两件事有判断，而两件错了都**没有报错**：
 *
 * 1. **"能否上行"的判据**（is_connected）。若把 WAIT_HANDSHAKE 也算 true，
 *    transport_broadcast 会把帧投给一个应用层未就绪的链路 ⇒ **静默丢弃**。
 * 2. **"这次发送到底怎么了"的分类**（stx_classify_send）。最危险的是
 *    **部分写出后失败**：那一刻 TCP 流里已有半帧，对端重组器会一直等。
 *    若把它归成"可重试"，调用方重发整帧 ⇒ 前缀写第二遍 ⇒ **静默数据损坏**（D-30）。
 *
 * ⇒ 两条都必须用**后果**断言（不是"返回值等于某枚举"就算），
 *   尤其要覆盖"progress 已推进"这一族。
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>

#include "session_transport.h"

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

/* ════════ 1. "能否上行"只认 READY ════════ */
static void test_only_ready_counts_as_up(void)
{
    CHECK(session_transport_ready(SESSION_READY) == true,
          "READY 必须是可上行");

    /* 其余四个状态**全部**不可上行。逐个断言而不是一句 !=READY：
     * 逐个才能在将来新增状态时立刻发现"新状态没被归类"。 */
    CHECK(session_transport_ready(SESSION_DOWN) == false, "DOWN 不可上行");
    CHECK(session_transport_ready(SESSION_BACKOFF) == false, "BACKOFF 不可上行");
    CHECK(session_transport_ready(SESSION_FATAL) == false, "FATAL 不可上行");

    /* ⭐ 最关键的一条：WAIT_HANDSHAKE **不算**可上行。
     * 链路是通的（TLS 已连），但应用层未握手 ⇒ 投给它就是静默丢弃。
     * 这条若写成 true，症状是"帧消失且没有任何错误"。 */
    CHECK(session_transport_ready(SESSION_WAIT_HANDSHAKE) == false,
          "WAIT_HANDSHAKE 不算可上行（链路通但应用层未就绪）—— "
          "算 true 会让 transport_broadcast 把帧投给发不出去的链路，表现为静默丢弃");
}

/* ════════ 2. 整帧写出 = DONE ════════ */
static void test_full_write_is_done(void)
{
    CHECK(stx_classify_send(LINK_SENT_FULL, 100, 100) == STX_SEND_DONE,
          "progress==len 且 FULL ⇒ DONE");
    /* 契约不符（FULL 但 progress<len）⇒ 保守按"流已污染"，不当作成功。 */
    CHECK(stx_classify_send(LINK_SENT_FULL, 50, 100) == STX_SEND_STREAM_DIRTY,
          "FULL 却 progress<len（link 层违约）⇒ 必须按 STREAM_DIRTY，不能当成功");
}

/* ════════ 3. ⭐ 部分写出后失败 = STREAM_DIRTY（不是 RETRY）════════ */
static void test_partial_write_is_stream_dirty(void)
{
    /* 这是本文件最重要的一组：progress>0 且未完成 ⇒ 流里已有半帧。
     * 归成 RETRY 会让调用方重发整帧 ⇒ 重复前缀 ⇒ 静默损坏。 */
    CHECK(stx_classify_send(LINK_BACKPRESSURE, 40, 100) == STX_SEND_STREAM_DIRTY,
          "BACKPRESSURE 但已写出 40/100 ⇒ STREAM_DIRTY（重发整帧会污染流）");
    CHECK(stx_classify_send(LINK_SENT_PARTIAL, 40, 100) == STX_SEND_STREAM_DIRTY,
          "PARTIAL 且已写出 ⇒ STREAM_DIRTY");
    CHECK(stx_classify_send(LINK_FATAL, 1, 100) == STX_SEND_STREAM_DIRTY,
          "FATAL 但已写出 1 字节 ⇒ STREAM_DIRTY（不是 NOT_READY —— 流已脏）");

    /* 对照：progress==0 时"重试整帧"才是安全的。 */
    CHECK(stx_classify_send(LINK_BACKPRESSURE, 0, 100) == STX_SEND_RETRY,
          "BACKPRESSURE 且一字节没写出 ⇒ RETRY（整帧重发安全）");
    CHECK(stx_classify_send(LINK_SENT_PARTIAL, 0, 100) == STX_SEND_RETRY,
          "PARTIAL 但 progress==0（矛盾输入）⇒ 保守 RETRY");
}

/* ════════ 4. 未就绪 / 超 MTU ════════ */
static void test_not_ready_and_too_big(void)
{
    CHECK(stx_classify_send(LINK_NOT_READY, 0, 100) == STX_SEND_NOT_READY,
          "NOT_READY ⇒ NOT_READY");
    CHECK(stx_classify_send(LINK_FATAL, 0, 100) == STX_SEND_NOT_READY,
          "FATAL 且未写出 ⇒ NOT_READY");
    CHECK(stx_classify_send(LINK_PAYLOAD_TOO_BIG, 0, 20000) == STX_SEND_TOO_BIG,
          "超 MTU ⇒ TOO_BIG（重试无用，调用方必须分片或拒绝）");

    /* 未知取值必须保守（不能默认成功）。 */
    CHECK(stx_classify_send((link_result_t)999, 0, 100) == STX_SEND_NOT_READY,
          "未知 link_result ⇒ 保守 NOT_READY，绝不能默认 DONE");
}

/* ════════ 5. 映射到 esp_err：**成功只有一种，且不是 0 以外的任何值** ════════ */
static void test_esp_err_mapping(void)
{
    CHECK(stx_to_esp_err(STX_SEND_DONE) == 0, "DONE ⇒ ESP_OK(0)");

    /* 关键性质：除 DONE 外**都不得**返回 0。
     * 否则上层会把"没发出去"当成功 —— D-01 的病根就是压平错误。 */
    const stx_send_class_t bad[] = { STX_SEND_RETRY, STX_SEND_NOT_READY,
                                     STX_SEND_TOO_BIG, STX_SEND_STREAM_DIRTY };
    for (unsigned i = 0; i < sizeof(bad) / sizeof(bad[0]); i++) {
        CHECK(stx_to_esp_err(bad[i]) != 0,
              "非 DONE 的分类 %s 不得映射为 ESP_OK(0)",
              stx_send_class_name(bad[i]));
    }

    /* 可重试与不可重试必须**可区分**：否则调用方无法决定"稍后再来"还是"换路"。 */
    CHECK(stx_to_esp_err(STX_SEND_RETRY) != stx_to_esp_err(STX_SEND_NOT_READY),
          "RETRY 与 NOT_READY 必须映射到不同错误码（调用方处置不同）");
    CHECK(stx_to_esp_err(STX_SEND_TOO_BIG) != stx_to_esp_err(STX_SEND_STREAM_DIRTY),
          "TOO_BIG 与 STREAM_DIRTY 必须可区分（前者分片、后者重建链路）");
}

/* ════════ 6. 名字非空（日志/诊断会打印）════════ */
static void test_names_nonempty(void)
{
    const stx_send_class_t all[] = { STX_SEND_DONE, STX_SEND_RETRY, STX_SEND_NOT_READY,
                                     STX_SEND_TOO_BIG, STX_SEND_STREAM_DIRTY };
    for (unsigned i = 0; i < sizeof(all) / sizeof(all[0]); i++) {
        CHECK(stx_send_class_name(all[i])[0] != '\0', "分类名不应为空");
    }
    CHECK(stx_send_class_name((stx_send_class_t)999) != NULL, "未知枚举应返回非 NULL");
}


/* ════════ 7. ⭐ task-21：组合判据（语义 AND 策略）════════ */
/* 为什么这组必须存在：is_connected 是**唯一**决定"这一帧投不投给 3.0"的地方。
 * 它错了有两种相反的静默故障：
 *   过松（只看 READY，不看策略）⇒ 双栈稳态**双发**；
 *   过严（只看策略，不看 READY）⇒ 未握手就被投 ⇒ **静默丢弃**。
 * ⇒ 两个方向各一条断言，缺一不可。 */
static void test_connected_requires_both_semantics_and_policy(void)
{
    /* (a) 就绪 + 放行 ⇒ 可投（正常态） */
    CHECK(session_transport_connected(SESSION_READY, true) == true,
          "READY 且闸放行 ⇒ 可投");

    /* (b) 就绪但闸关（如 tsel 已切 MQTT）⇒ **不可投**。
     *     这一条就是"消除双发"在适配层的落点：
     *     若写成 true，transport_broadcast 会同时投给 3.0 与 MQTT ⇒ 双发。 */
    CHECK(session_transport_connected(SESSION_READY, false) == false,
          "READY 但仲裁未选中 TCP ⇒ 不得投（否则与 MQTT 双发）");

    /* (c) 闸放行但未握手 ⇒ **不可投**（静默丢弃方向）。
     *     这是 session_transport_ready 的既有语义，组合函数必须**继承**它。 */
    const session_state_t not_ready[] = { SESSION_WAIT_HANDSHAKE, SESSION_DOWN,
                                          SESSION_BACKOFF, SESSION_FATAL };
    for (unsigned i = 0; i < sizeof(not_ready) / sizeof(not_ready[0]); i++) {
        CHECK(session_transport_connected(not_ready[i], true) == false,
              "状态 %d 未就绪 ⇒ 即使闸放行也不得投（投了就是静默丢弃）",
              (int)not_ready[i]);
        CHECK(session_transport_connected(not_ready[i], false) == false,
              "状态 %d 未就绪且闸关 ⇒ 更不得投", (int)not_ready[i]);
    }

    /* (d) 与 session_transport_ready **严格一致**（闸恒开时）。
     *     下界断言：穷举全部状态，确认组合函数没有自己另立一套判据。 */
    int checked = 0;
    for (int st = 0; st <= 4; st++) {
        CHECK(session_transport_connected((session_state_t)st, true)
              == session_transport_ready((session_state_t)st),
              "闸恒开时，组合判据必须与 session_transport_ready 完全一致（st=%d）", st);
        checked++;
    }
    CHECK(checked == 5, "应穷举 5 个会话状态，实际 %d", checked);
}

int main(void)
{
    test_only_ready_counts_as_up();
    test_full_write_is_done();
    test_partial_write_is_stream_dirty();
    test_not_ready_and_too_big();
    test_esp_err_mapping();
    test_names_nonempty();
    test_connected_requires_both_semantics_and_policy();

    if (s_failures) { printf("session_transport_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("session_transport_tests: all checks passed\n");
    return 0;
}
