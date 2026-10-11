/* net_policy_tests.c —— D-03 的决策函数（宿主可测）
 *
 * 这些断言的存在理由：D-03 是【不可达代码】造成的 —— TCP 启动块写在 break 之后。
 * 在原来的结构下这类缺陷无法被测出（藏在 main/ 的 switch 里，而 main/ 不能宿主编译）。
 * 把决策抽成纯函数后，"该不该启动"就有了可断言的真相。 */
#include "net_policy.h"

#include <stdio.h>

static int s_failures = 0;
#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond); s_failures++; } \
    } while (0)

/* 1) 唯一应当启动的组合：已配置 + 未连接 + WiFi 已连 */
static void test_start_when_all_conditions_hold(void)
{
    CHECK(net_policy_should_start_tcp(true, false, true) == true);
}

/* 2) 【核心】WiFi 已连但 TCP 未启动 —— 这正是 D-03 里从未发生的动作。
 *    若把这条断言去掉，就回到了"没人检查 TCP 到底启没启"的状态。 */
static void test_tcp_must_start_on_wifi_connected(void)
{
    /* 场景：WiFi 刚连上，transport 已创建，尚未连接 */
    CHECK(net_policy_should_start_tcp(/*configured=*/true, /*connected=*/false,
                                      /*wifi=*/true) == true);
}

/* 3) 三个条件任一不成立 -> 不启动（逐条锁死，避免"少判一个"） */
static void test_each_condition_is_required(void)
{
    /* 未配置 */
    CHECK(net_policy_should_start_tcp(false, false, true) == false);
    /* 已连接（重复 start 无意义） */
    CHECK(net_policy_should_start_tcp(true, true, true) == false);
    /* WiFi 没连 */
    CHECK(net_policy_should_start_tcp(true, false, false) == false);
    /* 全否 */
    CHECK(net_policy_should_start_tcp(false, false, false) == false);
    /* 已连接且未配置（矛盾输入）-> 不启动 */
    CHECK(net_policy_should_start_tcp(false, true, true) == false);
}

/* 4) 启动期断言：应当连上却没连上 -> 逾期 */
static void test_overdue(void)
{
    const uint32_t D = NET_POLICY_TCP_START_DEADLINE_MS;
    /* 未到点：不告警 */
    CHECK(net_policy_tcp_start_overdue(true, false, 0, D) == false);
    CHECK(net_policy_tcp_start_overdue(true, false, D - 1, D) == false);
    /* 到点：告警（边界取 >=） */
    CHECK(net_policy_tcp_start_overdue(true, false, D, D) == true);
    CHECK(net_policy_tcp_start_overdue(true, false, D + 1, D) == true);
    /* 已经连上：不再告警（即使时间很久） */
    CHECK(net_policy_tcp_start_overdue(true, true, 999999, D) == false);
    /* 没配置：不告警（不是"逾期"，是"没启用"） */
    CHECK(net_policy_tcp_start_overdue(false, false, 999999, D) == false);
}

/* 5) 逾期判据必须是"配置了、没连上、超时"三者的合取 ——
 *    逐个否定，确保没有把某一条漏掉。 */
static void test_overdue_needs_all_three(void)
{
    const uint32_t D = NET_POLICY_TCP_START_DEADLINE_MS;
    CHECK(net_policy_tcp_start_overdue(false, false, D * 10, D) == false); /* 未配置 */
    CHECK(net_policy_tcp_start_overdue(true,  true,  D * 10, D) == false); /* 已连上 */
    CHECK(net_policy_tcp_start_overdue(true,  false, 0,      D) == false); /* 未超时 */
    CHECK(net_policy_tcp_start_overdue(true,  false, D,      D) == true);  /* 三者齐备 */
}

/* 6) 超时上限是"可接受时间内暴露不可达代码"的取值 —— 锁住它，改动需有理由 */
static void test_deadline_value(void)
{
    CHECK(NET_POLICY_TCP_START_DEADLINE_MS == 15000u);
}

int main(void)
{
    test_start_when_all_conditions_hold();
    test_tcp_must_start_on_wifi_connected();
    test_each_condition_is_required();
    test_overdue();
    test_overdue_needs_all_three();
    test_deadline_value();
    if (s_failures) { printf("net_policy_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("net_policy_tests: all checks passed\n");
    return 0;
}
