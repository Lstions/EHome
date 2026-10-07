/* uplink_gate_exhaustive_tests.c —— 上行两条门的**穷举**互斥性证明
 *
 * ## 为什么要有这个文件
 * M1' 的全部正确性都归结为一条性质：
 *     **任一时刻，两条门里至多一条为真**（否则同一帧被投递两次）
 * 并且还有一条**同样重要**的活性性质：
 *     在"至少有一条链路可用"时，**恰好一条**为真（否则上行掉进直发兜底）
 *
 * 这两条性质只依赖 4 个布尔输入 ⇒ **可以穷举**。穷举比"挑几个用例"强：
 * 它排除了"漏掉某个组合"的可能，而那种漏法在真机上表现为间歇性双发/丢帧。
 *
 * 输入：link_enabled / tsel_is_tcp / three_zero_ready / mqtt_connected 共 2^4 = 16 组。
 */
#include <stdio.h>
#include <stdbool.h>

#include "uplink_arbiter.h"

static int s_failures = 0;

int main(void)
{
    int double_delivery = 0;     /* 两条门同时为真 ⇒ 会双发 */
    int neither_when_usable = 0; /* 有可用链路却两条都关 ⇒ 会掉进直发兜底 */

    printf("link tsel_3.0 ready mqtt | tcp3  mqtt  | 判定\n");
    printf("-------------------------+-------------+------\n");

    for (int le = 0; le <= 1; le++)
    for (int tt = 0; tt <= 1; tt++)
    for (int tr = 0; tr <= 1; tr++)
    for (int mc = 0; mc <= 1; mc++) {
        uplink_facts_t f;
        f.link_enabled    = (le != 0);
        f.tsel_is_tcp     = (tt != 0);
        f.three_zero_ready = (tr != 0);
        f.mqtt_connected  = (mc != 0);

        bool g3 = uplink_gate_tcp3(&f);
        bool gm = uplink_gate_mqtt(&f);

        const char *verdict = "ok";
        if (g3 && gm) { verdict = "**双发！**"; double_delivery++; }
        else if (!g3 && !gm && (mc || (le && tr))) {
            /* 有可用链路（MQTT 已连，或 3.0 已 READY）却两条都关 */
            verdict = "**无可用出口！**"; neither_when_usable++;
        }

        printf("  %d    %d      %d       %d   |  %d    %d   | %s\n",
               le, tt, tr, mc, g3 ? 1 : 0, gm ? 1 : 0, verdict);
    }

    printf("\n");
    if (double_delivery) {
        printf("FAIL: %d 组出现两条门同时为真 ⇒ 同一帧会被投递两次\n", double_delivery);
        s_failures++;
    }
    if (neither_when_usable) {
        printf("FAIL: %d 组在有可用链路时两条门都为假 ⇒ 上行无出口\n", neither_when_usable);
        s_failures++;
    }

    /* 单独钉住两条最关键的行（便于回归时一眼看出是哪条坏了）：
     *  ① 默认构建（link_enabled=0）+ MQTT 已连 ⇒ MQTT 门开（与今天逐位相同）
     *  ② 链路启用 + tsel=TCP + 3.0 READY + MQTT 已连 ⇒ **只有 3.0 开**（双发的原始场景） */
    {
        uplink_facts_t a = { false, false, false, true };
        if (!(uplink_gate_mqtt(&a) && !uplink_gate_tcp3(&a))) {
            printf("FAIL: 默认构建 + MQTT 已连 ⇒ 应只开 MQTT 门\n"); s_failures++;
        }
        uplink_facts_t b = { true, true, true, true };
        if (!(uplink_gate_tcp3(&b) && !uplink_gate_mqtt(&b))) {
            printf("FAIL: 双栈稳态（tsel=TCP, 3.0 READY, MQTT 已连）⇒ 应**只开 3.0 门**"
                   "（这正是 §120 实测到的双发场景，必须只剩一条）\n"); s_failures++;
        }
        /* ③ 链路启用 + tsel=TCP + 3.0 **未** READY（重连退避）⇒ 只开 MQTT（活性） */
        uplink_facts_t c = { true, true, false, true };
        if (!(!uplink_gate_tcp3(&c) && uplink_gate_mqtt(&c))) {
            printf("FAIL: tsel=TCP 但 3.0 未 READY ⇒ 必须只开 MQTT 门（否则上行无出口）\n");
            s_failures++;
        }
    }

    if (s_failures) { printf("uplink_gate_exhaustive_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("uplink_gate_exhaustive_tests: 16 组全部无互斥冲突、无出口缺失（all checks passed）\n");
    return 0;
}
