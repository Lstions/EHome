/* sync_manager_round_timeout_tests.c —— task-5：整轮同步看门狗 + 无条件复位
 *
 * ## 缺陷（现场，后端观测）
 * 节点 sync_state 钉在 1=SYNCING 达 11 h，期间只有 1 次 Hello，后端只能靠
 * 周期性重推兜底。两个缺陷指向同一个性质：**这一轮到底有没有人看着**。
 *
 *   A. 120 s 表只在 handle_hello_success()（HelloAck **被接受之后**）起表
 *      ⇒ Ack 丢失 / Ack 被 stale-nonce 拒绝 / Hello 根本没发出去，
 *        三种情形全都没人起表：state 永久 SYNCING 且**零重试**被 arm。
 *   B. 超时回调在 config_mgr_has_manifest() 为真时只点灯、**不复位状态**
 *      ⇒ 稳态设备（NVS 里本来就有 manifest）恰恰走这条分支，
 *        超时等于什么也没做 —— 缺陷 A 因此被永久藏在"全绿"后面。
 *
 * ## 修法（本用例钉住的性质）
 *   起表点挪到 request_sync（覆盖整轮）；到点**无条件**把 SYNCING 复位为
 *   ERROR 并按**有界**退避重试；has_manifest 只决定 LED 与"无配置激进重试"；
 *   stale-nonce 拒收要看门狗/通知都能让本轮立刻作废；ConfigResult/Manifest
 *   成功时收表并归零退避。
 *
 * ## 每条断言"凭什么会失败"（防假绿）
 *   ① 若起表点退回 "只在 Ack 之后"，"request_sync 后表就在" 立刻红；
 *      若超时回调退回 "有 manifest 只点灯"，"超时后不得停在 SYNCING" 红；
 *      若只复位不重试，"退避到期真的重发 Hello" 红。
 *   ② 若 Ack 之后仍重起表 / 不收表，则会在每个周期后 120 s 报一次假超时。
 *   ③ 若"无配置"也把表收掉，就再也不会有 SERVER_OFFLINE 与激进重试。
 *   ④ 若 stale-nonce 不通知，"拒收后离开 SYNCING" 红（旧代码正是静默 return）。
 *   ⑤ 若成功后不归零退避，重试间隔会一路涨到上限。
 *
 * ## 替身的诚实边界
 *   esp_timer 替身**不假装在跑**：它保存回调与时长，由用例显式
 *   host_test_timer_fire() 触发（见 *_stubs.c 头部说明）。
 *   周期任务（sync_manager_periodic_task）**不在**本用例范围内：它是死循环，
 *   宿主上跑不了；本用例只覆盖"一次性请求/超时/重试"这条链。
 */
#include <stdio.h>
#include <stdbool.h>
#include <stdint.h>

#include "sync_manager.h"
/* 消息号的唯一来源（与生产同一份头，避免在用例里重抄 0x04/0x05/0x12）。 */
#include "frame_codec.h"
/* LED 状态枚举：本用例断言 "has_manifest 只影响 LED" 这一条性质。 */
#include "rgb_led.h"

/* ==== 注入点（与生产 main.c 的接法一致）==== */
static bool s_uplink = true;
static bool uplink_stub(void) { return s_uplink; }

static int s_hello_calls = 0;
static void on_send_hello(void) { s_hello_calls++; }

/* ==== 替身观察点 / 驱动器（定义在 *_stubs.c）==== */
extern void host_test_stub_reset(void);
extern void host_test_set_manifest(bool has);
extern bool host_test_timer_armed(void);
extern int  host_test_timer_armed_sec(void);
extern int  host_test_timer_start_count(void);
extern int  host_test_timer_stop_count(void);
extern int  host_test_timer_callback_count(void);
extern void host_test_timer_fire(void);
extern led_state_t host_test_last_led(void);

static int s_failures = 0;
#define CHECK(cond, msg) do { \
    if (!(cond)) { printf("  FAIL %s\n", msg); s_failures++; } \
    else         { printf("  ok   %s\n", msg); } \
} while (0)

/* 每个用例都从 "已初始化、没有在途轮次、退避为最小" 出发。 */
static void reset_scenario(bool has_manifest)
{
    host_test_stub_reset();
    host_test_set_manifest(has_manifest);
    s_uplink = true;
    s_hello_calls = 0;
    sync_manager_test_reset();
    sync_manager_register_send_hello_cb(on_send_hello);
    sync_manager_register_uplink_available_cb(uplink_stub);
}

/* ① 核心回归：HelloAck 丢失 + 设备已有 manifest（稳态，即缺陷 B 的分支）。
 *    这正是现场症状：Ack 不来，state 钉在 SYNCING，且**零重试**。 */
static void test_lost_ack_times_out_and_retries(void)
{
    printf("[① HelloAck 丢失 + 设备已有 manifest]\n");
    reset_scenario(true);

    CHECK(!host_test_timer_armed(), "进入用例时没有在途表");

    sync_manager_request_sync(SYNC_REASON_FORCED);

    CHECK(sync_manager_get_state_enum() == SYNC_STATE_SYNCING,
          "请求发出后进入 SYNCING");
    CHECK(s_hello_calls == 1, "请求时确实发了 1 次 Hello");
    CHECK(host_test_timer_armed(),
          "★起表点必须在 request_sync（Ack 之前）就存在");
    CHECK(host_test_timer_armed_sec() == 120, "整轮表长 120 s");

    /* Ack 永远不来 —— 直接把表推到点（这是"丢包"的可观测后果）。 */
    host_test_timer_fire();

    CHECK(sync_manager_get_state_enum() != SYNC_STATE_SYNCING,
          "★整轮超时后**不得**再停在 SYNCING（缺陷 A 的核心）");
    CHECK(sync_manager_get_state_enum() == SYNC_STATE_ERROR,
          "复位为 ERROR（后端能在 StatusReport 里看见异常，而不是假的进行中）");
    CHECK(host_test_last_led() == LED_STATE_RUNNING,
          "有 manifest ⇒ LED 仍表示可运行（has_manifest 只影响 LED）");
    CHECK(host_test_timer_armed(), "★超时必须排上重试，而不是只点灯（缺陷 B）");
    CHECK(host_test_timer_armed_sec() == 30, "首次退避 30 s");
    CHECK(s_hello_calls == 1, "退避期间不额外发 Hello");

    /* 退避到期：必须真的重开一轮。 */
    host_test_timer_fire();
    CHECK(s_hello_calls == 2, "★退避到期后真的重发 Hello（发生重试）");
    CHECK(sync_manager_get_state_enum() == SYNC_STATE_SYNCING,
          "重试重新进入 SYNCING");

    /* 连续第二个失败轮：退避翻倍（有界）—— 防止"退避永远 30 s"的风暴。 */
    host_test_timer_fire();
    CHECK(host_test_timer_armed_sec() == 60,
          "连续失败后退避翻倍（30→60），且复位依旧发生");
    CHECK(sync_manager_get_state_enum() == SYNC_STATE_ERROR,
          "第二轮超时同样离开 SYNCING");
}

/* ② 稳态（有 manifest）+ Ack 被接受 ⇒ 本轮闭合，必须收表。
 *    否则每 10 min 周期同步后都会在 120 s 报一次假超时。 */
static void test_accepted_ack_with_config_completes_round(void)
{
    printf("[② 稳态 + Ack 被接受 ⇒ 本轮完成、收表]\n");
    reset_scenario(true);
    sync_manager_request_sync(SYNC_REASON_FORCED);
    CHECK(host_test_timer_armed(), "起表");

    /* handler_hello 先报 "收到下行"，supervisor 随后报 "Ack 被接受"。 */
    sync_manager_on_downlink_received(MSG_HELLO_ACK);
    sync_manager_on_hello_ack_accepted();

    CHECK(sync_manager_get_state_enum() == SYNC_STATE_IDLE, "Ack 接受后回到 IDLE");
    CHECK(!host_test_timer_armed(),
          "★有配置 ⇒ 本轮已闭合，表必须收掉（否则每周期假超时一次）");
}

/* ③ 无配置 + Ack 被接受但服务端没推配置 ⇒ 表继续在途（这一段必须仍然有界），
 *    到点点 SERVER_OFFLINE 并重试 —— 即 "无配置激进重试" 分支。 */
static void test_accepted_ack_without_config_keeps_waiting(void)
{
    printf("[③ 无配置 + Ack 已接受但配置没来 ⇒ 继续等，超时点灯并重试]\n");
    reset_scenario(false);
    sync_manager_request_sync(SYNC_REASON_FORCED);
    const int starts_after_request = host_test_timer_start_count();

    sync_manager_on_downlink_received(MSG_HELLO_ACK);
    sync_manager_on_hello_ack_accepted();

    CHECK(sync_manager_get_state_enum() == SYNC_STATE_IDLE, "Ack 接受后 IDLE");
    CHECK(host_test_timer_armed(), "★无配置 ⇒ 还要等服务端推配置，表必须还在");
    CHECK(host_test_timer_start_count() == starts_after_request,
          "★不得因为 Ack 重起表（重复 Ack 不能给本轮续命）");

    host_test_timer_fire();
    CHECK(host_test_last_led() == LED_STATE_SERVER_OFFLINE,
          "无配置超时 ⇒ SERVER_OFFLINE（has_manifest 另一分支）");
    CHECK(host_test_timer_armed(), "并排了重试");
    CHECK(host_test_timer_armed_sec() == 30, "无配置重试用最小退避");
}

/* ④ stale-nonce 拒收（缺陷 C）：必须立刻作废本轮并重试，
 *    而不是静默 return、让 state 干等 120 s 看门狗。 */
static void test_rejected_ack_leaves_syncing_immediately(void)
{
    printf("[④ stale-nonce 拒收 ⇒ 立刻离开 SYNCING 并重试]\n");
    reset_scenario(true);
    sync_manager_request_sync(SYNC_REASON_FORCED);
    CHECK(sync_manager_get_state_enum() == SYNC_STATE_SYNCING, "在途中");

    /* handler_hello.c 的 stale-nonce 分支现在会调这个入口。 */
    sync_manager_on_round_failed();

    CHECK(sync_manager_get_state_enum() != SYNC_STATE_SYNCING,
          "★拒收后不得静默停在 SYNCING（缺陷 C）");
    CHECK(sync_manager_get_state_enum() == SYNC_STATE_ERROR, "记为 ERROR");
    CHECK(host_test_timer_armed(), "排上退避重试");
    CHECK(host_test_timer_armed_sec() == 30, "立即用最小退避重试");

    /* 没有在途轮次时收到 "失败" 通知（上一代连接残留的陈旧 ACK）：
     * 不得被误判成一次失败，也不该重置已经排好的退避。 */
    const int starts_before = host_test_timer_start_count();
    sync_manager_on_round_failed();
    CHECK(host_test_timer_start_count() == starts_before,
          "无在途轮次时的失败通知被忽略（不重复排程）");
    CHECK(host_test_timer_armed_sec() == 30, "已有退避不被无意改动");
}

/* ⑤ ConfigResult 成功（要求 ④）⇒ 收表 + 退避归零。 */
static void test_config_result_success_cancels_and_resets_backoff(void)
{
    printf("[⑤ ConfigResult 成功 ⇒ 收表，退避归零]\n");
    reset_scenario(true);

    /* 先制造一次失败，把退避推到 60。 */
    sync_manager_request_sync(SYNC_REASON_FORCED);
    host_test_timer_fire();          /* 整轮超时 → ERROR，下次退避 60 */
    host_test_timer_fire();          /* 退避到期 → 重开一轮 */
    CHECK(sync_manager_get_state_enum() == SYNC_STATE_SYNCING, "重试在途中");

    /* 这一轮成功：服务端确认 ConfigResult。 */
    sync_manager_on_downlink_received(MSG_CONFIG_RSLT);
    CHECK(sync_manager_get_state_enum() == SYNC_STATE_IDLE, "成功后 IDLE");
    CHECK(!host_test_timer_armed(), "★成功必须收表");

    /* 再失败一次 ⇒ 退避应回到 30（证明成功确实归零了）。 */
    sync_manager_request_sync(SYNC_REASON_FORCED);
    host_test_timer_fire();
    CHECK(host_test_timer_armed_sec() == 30,
          "成功一轮后退避归零（若没归零这里会是 60）");

    /* ConfigManifest 到达同样收表（要求 ④ 的另一半）。 */
    reset_scenario(true);
    sync_manager_request_sync(SYNC_REASON_FORCED);
    CHECK(host_test_timer_armed(), "重新起表");
    sync_manager_on_downlink_received(MSG_CONFIG_MFST);
    CHECK(!host_test_timer_armed(), "★ConfigManifest 到达也必须收表");
}

/* ⑥ 无可用上行：本轮没开始，但不能留在 SYNCING 空等（退避重试兜住）。 */
static void test_no_uplink_schedules_retry(void)
{
    printf("[⑥ 无可用上行 ⇒ 不留在 SYNCING，且排退避重试]\n");
    reset_scenario(true);
    s_uplink = false;
    sync_manager_request_sync(SYNC_REASON_FORCED);

    CHECK(sync_manager_get_state_enum() == SYNC_STATE_ERROR, "无上行记 ERROR");
    CHECK(s_hello_calls == 0, "无上行时确实没发 Hello");
    CHECK(host_test_timer_armed(), "★仍要排重试（上行恢复后自己继续）");
    CHECK(host_test_timer_armed_sec() == 30, "用最小退避");
}

int main(void)
{
    printf("=== sync_manager 整轮看门狗 + 无条件复位（task-5 缺陷 A/B/C）===\n");
    test_lost_ack_times_out_and_retries();
    test_accepted_ack_with_config_completes_round();
    test_accepted_ack_without_config_keeps_waiting();
    test_rejected_ack_leaves_syncing_immediately();
    test_config_result_success_cancels_and_resets_backoff();
    test_no_uplink_schedules_retry();
    printf("=== %s（失败 %d）===\n", s_failures == 0 ? "PASS" : "FAIL", s_failures);
    return s_failures == 0 ? 0 : 1;
}
