/* transport_sel_tests.c —— TCP 优先 / MQTT 兜底（设计 §7.3 的 P2 阶段）
 *
 * 这条策略决定"现场设备在 3.0 过渡期会不会失联"，所以两边的错都要拦：
 *   - 切得太早：TCP 一次抖动就切到 MQTT ⇒ 两条通道反复横跳；
 *   - 切不回：TCP 恢复了还赖在 MQTT 上 ⇒ P3 永远关不掉 MQTT（功能一直受限）。
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>

#include "transport_sel.h"

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

static tsel_t *mk(uint32_t thresh, bool fallback, uint32_t recover)
{
    tsel_config_t c = { .tcp_fail_threshold = thresh,
                        .allow_mqtt_fallback = fallback,
                        .tcp_recover_success = recover };
    return tsel_create(&c);
}

static tsel_which_t poll(tsel_t *s, bool ready, bool failed, bool ok,
                         tsel_reason_t *r)
{
    tsel_input_t in = { .tcp_ready = ready, .tcp_failed = failed,
                        .tcp_succeeded = ok };
    return tsel_poll(s, &in, r);
}

/* ============ 1. 起点是 TCP；未达阈值**不切**（防抖） ============ */
static void test_does_not_switch_before_threshold(void)
{
    tsel_t *s = mk(3, true, 1);
    CHECK(s != NULL, "创建失败");
    tsel_reason_t r;

    CHECK(poll(s, false, false, false, &r) == TSEL_TCP, "起点应是 TCP");

    /* 失败 1 次、2 次：仍在 TCP（阈值是 3）*/
    CHECK(poll(s, false, true, false, &r) == TSEL_TCP,
          "失败 1 次不应切（否则会横跳）");
    CHECK(poll(s, false, true, false, &r) == TSEL_TCP,
          "失败 2 次不应切（阈值 3）");

    /* 第 3 次失败：达阈值 -> 切兜底 */
    CHECK(poll(s, false, true, false, &r) == TSEL_MQTT,
          "连续失败达阈值应切到 MQTT 兜底");
    CHECK(r == TSEL_REASON_TCP_FAILED_N, "理由应为 TCP_FAILED_N，实际 %s",
          tsel_reason_name(r));

    tsel_stats_t st;
    tsel_get_stats(s, &st);
    CHECK(st.switched_to_mqtt == 1, "应记 1 次切换，实际 %u", st.switched_to_mqtt);
    tsel_destroy(s);
}

/* ============ 2. ⭐ 中间成功一次会**重置**连续失败计数 ============ */
static void test_success_resets_fail_counter(void)
{
    tsel_t *s = mk(3, true, 1);
    tsel_reason_t r;

    poll(s, false, true, false, &r);        /* 败 1 */
    poll(s, false, true, false, &r);        /* 败 2 */
    poll(s, false, false, true, &r);        /* 成功（重置）*/
    CHECK(tsel_current(s) == TSEL_TCP, "成功一次后仍在 TCP");

    /* 再败两次：仍不到阈值（因为刚被重置）*/
    poll(s, false, true, false, &r);
    poll(s, false, true, false, &r);
    CHECK(tsel_current(s) == TSEL_TCP,
          "**成功一次必须重置连续失败计数**（否则零散失败会累加成误切）");

    /* 第三次才切 */
    CHECK(poll(s, false, true, false, &r) == TSEL_MQTT, "第三次连续失败应切");
    tsel_destroy(s);
}

/* ============ 3. ⭐ 兜底期间 TCP 恢复 -> **立即**切回 ============ */
static void test_immediately_switches_back(void)
{
    tsel_t *s = mk(2, true, 1);
    tsel_reason_t r;

    poll(s, false, true, false, &r);
    CHECK(poll(s, false, true, false, &r) == TSEL_MQTT, "应已在兜底");

    /* TCP 恢复（READY）—— 应当**立刻**切回，不等任何观察期 */
    CHECK(poll(s, true, false, true, &r) == TSEL_TCP,
          "**TCP 恢复应立即切回主通道**（留在 MQTT 会一直吃 2011B 限制）");
    CHECK(r == TSEL_REASON_BACK_TO_TCP, "理由应为 BACK_TO_TCP，实际 %s",
          tsel_reason_name(r));

    tsel_stats_t st;
    tsel_get_stats(s, &st);
    CHECK(st.switched_back == 1, "应记 1 次切回，实际 %u", st.switched_back);
    CHECK(st.consecutive_fail == 0, "切回后失败计数应归零，实际 %u",
          st.consecutive_fail);
    tsel_destroy(s);
}

/* ============ 4. ⭐ 切回后要**重新攒够阈值**才会再切 ============ */
static void test_threshold_reset_after_switch_back(void)
{
    tsel_t *s = mk(3, true, 1);
    tsel_reason_t r;

    for (int i = 0; i < 3; i++) poll(s, false, true, false, &r);
    CHECK(tsel_current(s) == TSEL_MQTT, "应已切兜底");
    poll(s, true, false, true, &r);
    CHECK(tsel_current(s) == TSEL_TCP, "应已切回");

    /* 再败 2 次（不到阈值）不应切 */
    poll(s, false, true, false, &r);
    poll(s, false, true, false, &r);
    CHECK(tsel_current(s) == TSEL_TCP,
          "切回后必须**重新攒够阈值**（否则会来回横跳）");
    tsel_destroy(s);
}

/* ============ 5. ⭐ 兜底被禁用 -> 永不返回 MQTT ============ */
static void test_no_fallback_never_returns_mqtt(void)
{
    tsel_t *s = mk(2, false, 1);     /* P3 之后的配置 */
    tsel_reason_t r;

    for (int i = 0; i < 20; i++) {
        tsel_which_t w = poll(s, false, true, false, &r);
        CHECK(w == TSEL_TCP,
              "**兜底禁用时绝不能返回 MQTT**（否则 P3 目标永远达不到）");
    }
    CHECK(r == TSEL_REASON_NO_FALLBACK, "理由应为 NO_FALLBACK，实际 %s",
          tsel_reason_name(r));

    tsel_stats_t st;
    tsel_get_stats(s, &st);
    /* 20 次里只有后 19 次真的"想切但被禁"：
     * 第一轮 consecutive_fail=1 < 阈值 2，走的是 TCP_HEALTHY 分支。
     * （这条期望我一开始写成 20 —— 是**测试的错**，不是实现的。） */
    CHECK(st.no_fallback_hits == 19, "应记 19 次 no_fallback，实际 %u",
          st.no_fallback_hits);
    CHECK(st.switched_to_mqtt == 0, "不得有任何切换，实际 %u", st.switched_to_mqtt);
    tsel_destroy(s);
}

/* ============ 6. 兜底期间 TCP 没恢复就继续待着 ============ */
static void test_stay_in_mqtt_when_tcp_not_ready(void)
{
    tsel_t *s = mk(1, true, 1);
    tsel_reason_t r;

    CHECK(poll(s, false, true, false, &r) == TSEL_MQTT, "阈值 1 应立刻切");
    for (int i = 0; i < 5; i++) {
        tsel_which_t w = poll(s, false, false, false, &r);
        CHECK(w == TSEL_MQTT, "TCP 未恢复时应留在兜底");
        CHECK(r == TSEL_REASON_STAY_MQTT, "理由应为 STAY_MQTT，实际 %s",
              tsel_reason_name(r));
    }
    tsel_destroy(s);
}

/* ============ 7. ⭐ tcp_ready 但本轮报 failed 时不得误判为成功 ============ */
static void test_ready_and_failed_together(void)
{
    tsel_t *s = mk(2, true, 1);
    tsel_reason_t r;

    poll(s, false, true, false, &r);
    poll(s, false, true, false, &r);
    CHECK(tsel_current(s) == TSEL_MQTT, "应已切兜底");

    /* 矛盾输入：tcp_ready=true 但 tcp_failed=true。
     * 实现口径：failed 优先计失败；而 MQTT 态下要求 (ready && 连续成功>=阈值)
     * 才切回。这里 failed 把 consecutive_ok 清零，故**不切回**。
     * 记录该口径，避免"矛盾输入下行为未定义"。 */
    tsel_which_t w = poll(s, true, true, false, &r);
    CHECK(w == TSEL_MQTT,
          "矛盾输入（ready 且 failed）应保守留在兜底，实际 %s",
          tsel_which_name(w));
    tsel_destroy(s);
}

/* ============ 8. 参数校验：阈值 0 拒绝（不静默兜底） ============ */
static void test_create_validation(void)
{
    tsel_config_t c = { .tcp_fail_threshold = 3, .allow_mqtt_fallback = true,
                        .tcp_recover_success = 1 };
    CHECK(tsel_create(NULL) == NULL, "NULL 应拒绝");

    tsel_config_t c2 = c; c2.tcp_fail_threshold = 0;
    CHECK(tsel_create(&c2) == NULL,
          "阈值 0 应**拒绝**（等于没防抖），而不是偷偷改成 1");

    tsel_config_t c3 = c; c3.tcp_recover_success = 0;
    CHECK(tsel_create(&c3) == NULL, "恢复阈值 0 应拒绝");
}

/* ============ 9. 名字表可用（日志要用） ============ */
static void test_names(void)
{
    CHECK(tsel_which_name(TSEL_TCP)[0] == 'T', "TCP 名字");
    CHECK(tsel_which_name(TSEL_MQTT)[0] == 'M', "MQTT 名字");
    CHECK(tsel_which_name((tsel_which_t)99)[0] == 'U', "越界应兜底");
    CHECK(tsel_reason_name(TSEL_REASON_BACK_TO_TCP)[0] != 0, "理由名非空");
    CHECK(tsel_reason_name((tsel_reason_t)99)[0] == 'U', "越界理由应兜底");
}

int main(void)
{
    test_does_not_switch_before_threshold();
    test_success_resets_fail_counter();
    test_immediately_switches_back();
    test_threshold_reset_after_switch_back();
    test_no_fallback_never_returns_mqtt();
    test_stay_in_mqtt_when_tcp_not_ready();
    test_ready_and_failed_together();
    test_create_validation();
    test_names();

    if (s_failures) { printf("transport_sel_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("transport_sel_tests: all checks passed\n");
    return 0;
}
