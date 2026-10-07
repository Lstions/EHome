/* uplink_arbiter_tests.c —— task-21：两条门互斥（消除双栈双发）的宿主实证
 *
 * ## 这个文件防的是什么
 *
 * transport_broadcast 对【每一个】is_connected() 为真的 transport 都发。
 * 双栈稳态（MQTT connected + 3.0 READY）下 ⇒ 同一帧投递两次。
 * 本文件断言：只要两条门互斥，任一时刻【恰好一条】被投递，且
 * 【两条都不可投递】的窗口不存在（那会让上行掉进直发兜底）。
 *
 * ## 为什么用穷举
 *
 * 门的输入空间只有 4 个布尔（16 种组合）⇒ 穷举全部，不存在漏掉的组合。
 * 这比挑几个典型场景强：本卡历史上的坑都出在【没想到的那个组合】
 * （例如 tsel=TCP + 3.0 重连退避中）。
 *
 * ## 与 transport.c 的关系
 *
 * 本文件只测 uplink_arbiter.c 的纯判定（无 IDF 依赖）。真实 transport.c 的
 * fan-out 语义由 transport_dualstack_tests.c 实证；那里保留的
 * test_dualstack_broadcast_sends_twice 记录的是【为什么需要仲裁层】。
 */
#include "uplink_arbiter.h"

#include <stdio.h>

static int s_failures = 0;
#define CHECK(cond, ...)                                                     \
    do {                                                                     \
        if (!(cond)) {                                                       \
            printf("FAIL %s:%d  ", __FILE__, __LINE__);                     \
            printf(__VA_ARGS__);                                             \
            printf("\n");                                                   \
            s_failures++;                                                    \
        }                                                                    \
    } while (0)

static uplink_facts_t facts(bool link, bool tcp, bool ready, bool mqtt)
{
    uplink_facts_t f;
    f.link_enabled = link;
    f.tsel_is_tcp = tcp;
    f.three_zero_ready = ready;
    f.mqtt_connected = mqtt;
    return f;
}

/* ⭐ 核心 1：穷举 16 种组合，断言【永不两条都为真】。
 *
 * 这一条就是【双发已消除】的充分条件：broadcast 只会投给 is_connected() 为真的
 * transport，两条门互斥 ⇒ 最多一条被投。 */
static void test_gates_never_overlap_exhaustive(void)
{
    int checked = 0;
    for (int link = 0; link <= 1; link++)
    for (int tcp = 0; tcp <= 1; tcp++)
    for (int ready = 0; ready <= 1; ready++)
    for (int mqtt = 0; mqtt <= 1; mqtt++) {
        uplink_facts_t f = facts(link != 0, tcp != 0, ready != 0, mqtt != 0);
        CHECK(!uplink_gates_overlap(&f),
              "双发窗口! link=%d tsel_tcp=%d 3.0ready=%d mqtt=%d ⇒ 两条门同时为真",
              link, tcp, ready, mqtt);
        checked++;
    }
    /* 下界断言：穷举数必须是 16 —— 若循环被改坏成 0 次，本文件会假绿。 */
    CHECK(checked == 16, "应穷举 16 种组合，实际 %d（循环写错了？）", checked);
}

/* ⭐ 核心 2：任一时刻【恰好一条】（在链路可用时）。
 *
 * 注意区分两种情况：
 *   - 链路启用时：必须**恰好一条**（0 条会让上行掉进直发兜底，那是硬约束 2 禁止的）；
 *   - 链路未启用时：MQTT 门 == mqtt_connected（与今天逐位相同），
 *     此时允许 0 条（MQTT 没连上就是没连上，今天的语义如此）。 */
static void test_exactly_one_delivery(void)
{
    /* (1) tsel=TCP 且 3.0 READY ⇒ 恰好 3.0 一条 */
    uplink_facts_t a = facts(true, true, true, true);
    CHECK(uplink_gate_tcp3(&a), "tsel=TCP+READY ⇒ 3.0 门应为真");
    CHECK(!uplink_gate_mqtt(&a), "tsel=TCP+READY ⇒ MQTT 门应为假（否则双发）");

    /* (2) tsel=TCP 且 3.0 【重连退避中】（未 READY）⇒ 恰好 MQTT 一条。
     * ⭐ 这是 Lead 专门要求钉住的状态，也是 H1 那条缺陷最容易露头的场景。
     *    它同时证明 MQTT 门里那一项 !three_zero_ready 的作用：
     *    若去掉它，这里会变成【两条都假】⇒ 上行掉进直发兜底。 */
    uplink_facts_t b = facts(true, true, false, true);
    CHECK(!uplink_gate_tcp3(&b), "3.0 未 READY ⇒ 3.0 门必须为假（投了就是静默丢弃）");
    CHECK(uplink_gate_mqtt(&b),
          "tsel=TCP + 3.0 重连退避中 ⇒ MQTT 门必须为真（否则出现【两条都不可投递】）");

    /* (3) tsel=MQTT（TCP 连续失败达阈值）⇒ 恰好 MQTT 一条 */
    uplink_facts_t c = facts(true, false, false, true);
    CHECK(!uplink_gate_tcp3(&c), "tsel=MQTT ⇒ 3.0 门必须为假");
    CHECK(uplink_gate_mqtt(&c), "tsel=MQTT ⇒ MQTT 门应为真");

    /* (4) tsel=MQTT 但 TCP 其实已 READY（刚恢复、tsel 尚未 poll）⇒
     *     仍只走 MQTT。理由：tsel 是**唯一**策略来源，门不能自己猜。 */
    uplink_facts_t d = facts(true, false, true, true);
    CHECK(!uplink_gate_tcp3(&d), "tsel=MQTT ⇒ 3.0 门必须为假（策略未切回）");
    CHECK(uplink_gate_mqtt(&d), "tsel=MQTT ⇒ MQTT 门应为真");

    /* (5) 两条链路都不可用 ⇒ 0 条（本就无可用链路，不是我们的缺陷） */
    uplink_facts_t e = facts(true, true, false, false);
    CHECK(!uplink_gate_tcp3(&e) && !uplink_gate_mqtt(&e),
          "MQTT 未连 + 3.0 未 READY ⇒ 两条都假是正确的（确实没有可用上行）");
}

/* 硬约束 1：**默认构建（链路未启用）与今天逐位相同**。
 *
 * 未启用时：3.0 门恒假（没有 3.0 链路），MQTT 门 == mqtt_connected。
 * 这是可复核的证据，不是口头保证 —— 下面穷举未启用时的全部 8 种组合。 */
static void test_default_build_unchanged(void)
{
    int checked = 0;
    for (int tcp = 0; tcp <= 1; tcp++)
    for (int ready = 0; ready <= 1; ready++)
    for (int mqtt = 0; mqtt <= 1; mqtt++) {
        uplink_facts_t f = facts(false /* link_enabled */, tcp != 0, ready != 0, mqtt != 0);
        CHECK(!uplink_gate_tcp3(&f),
              "链路未启用 ⇒ 3.0 门必须恒假（默认构建不得出现 3.0 上行）");
        CHECK(uplink_gate_mqtt(&f) == (mqtt != 0),
              "链路未启用 ⇒ MQTT 门必须【等于 mqtt_connected】"
              "（tcp=%d ready=%d mqtt=%d），这才与今天逐位相同",
              tcp, ready, mqtt);
        checked++;
    }
    CHECK(checked == 8, "应穷举 8 种组合，实际 %d", checked);
}

/* 反向对照：门【不该】把 MQTT 关掉当 MQTT 未连接时 —— 防止门写得过严
 * 导致【上行全哑】（那是比双发更严重的静默故障）。 */
static void test_mqtt_gate_not_overly_strict(void)
{
    /* 只要 MQTT 连着、且 3.0 不能承载（未启用/未 READY/未选中 TCP），MQTT 门就该开。 */
    uplink_facts_t cases[4] = {
        facts(false, true,  false, true),   /* 未启用 */
        facts(true,  false, false, true),   /* tsel=MQTT */
        facts(true,  true,  false, true),   /* tsel=TCP 但未 READY */
        facts(true,  false, true,  true),   /* tsel=MQTT 且 READY */
    };
    for (int i = 0; i < 4; i++) {
        CHECK(uplink_gate_mqtt(&cases[i]),
              "MQTT 已连接且 3.0 不能承载时，MQTT 门必须开（case %d）—— "
              "否则上行全哑", i);
    }
    /* 反向：MQTT 真的没连时必须关（不能谎报可投）。 */
    uplink_facts_t down = facts(true, false, false, false);
    CHECK(!uplink_gate_mqtt(&down), "MQTT 未连接 ⇒ 门必须关（否则谎报可投）");
}

int main(void)
{
    test_gates_never_overlap_exhaustive();
    test_exactly_one_delivery();
    test_default_build_unchanged();
    test_mqtt_gate_not_overly_strict();

    if (s_failures) {
        printf("uplink_arbiter_tests: %d FAILURE(S)\n", s_failures);
        return 1;
    }
    printf("uplink_arbiter_tests: all checks passed\n");
    return 0;
}
