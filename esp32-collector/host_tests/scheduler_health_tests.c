/* scheduler_health_tests.c —— D-07 健康计数写入规则（宿主可测）
 *
 * 这些断言的存在理由：sched_command_t.error_count 是【服务端读的唯一健康量】
 * （handler_data.c 映射成 comm_status：>=3 -> FAULT，>0 -> TIMEOUT）。
 * 旧代码在【本机 TX 队列满】时也把它 +1 ⇒ 现场会看到一批没坏的传感器报故障，
 * 而 100 Hz 下队列满必然发生（L-01c 实测 full=577~589）。
 * 本用例把"谁能改这个字段"钉死。 */
#include "scheduler_health.h"

#include <stdio.h>
#include <string.h>

static int s_failures = 0;
#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond); s_failures++; } \
    } while (0)

/* 1) 【核心】本机背压【不得】改变健康计数 —— 这是 D-07 的整条修复 */
static void test_backpressure_does_not_move_health(void)
{
    for (uint32_t cur = 0; cur <= 5; cur++) {
        CHECK(sched_health_next(cur, SCHED_HEALTH_LOCAL_BACKPRESSURE) == cur);
    }
    /* 从任意值出发都不动 */
    CHECK(sched_health_next(100, SCHED_HEALTH_LOCAL_BACKPRESSURE) == 100);
    CHECK(sched_health_next(99,  SCHED_HEALTH_LOCAL_BACKPRESSURE) == 99);
    CHECK(sched_health_next(1,   SCHED_HEALTH_LOCAL_BACKPRESSURE) == 1);
}

/* 2) 【回归防线】背压【再多也不能】把计数推到 FAULT 阈值(3)。
 *    这正是旧行为的危害：队列一挤，服务端就看到一批 FAULT。 */
static void test_many_backpressures_never_reach_fault(void)
{
    uint32_t e = 0;
    for (int i = 0; i < 1000; i++) {
        e = sched_health_next(e, SCHED_HEALTH_LOCAL_BACKPRESSURE);
    }
    CHECK(e == 0);
    CHECK(e < 3);   /* 3 是 handler_data.c 的 FAULT 阈值 */
}

/* 3) 设备失败才递增 */
static void test_device_failure_increments(void)
{
    CHECK(sched_health_next(0, SCHED_HEALTH_DEVICE_FAILURE) == 1);
    CHECK(sched_health_next(1, SCHED_HEALTH_DEVICE_FAILURE) == 2);
    CHECK(sched_health_next(2, SCHED_HEALTH_DEVICE_FAILURE) == 3);  /* 达 FAULT 阈值 */
}

/* 4) 设备成功清零（连续错误streak 语义） */
static void test_device_success_clears(void)
{
    CHECK(sched_health_next(0,   SCHED_HEALTH_DEVICE_SUCCESS) == 0);
    CHECK(sched_health_next(3,   SCHED_HEALTH_DEVICE_SUCCESS) == 0);
    CHECK(sched_health_next(100, SCHED_HEALTH_DEVICE_SUCCESS) == 0);
}

/* 5) 上限截断（沿用既有实现：>100 截为 100） */
static void test_clamp(void)
{
    CHECK(sched_health_next(SCHED_HEALTH_MAX, SCHED_HEALTH_DEVICE_FAILURE) == SCHED_HEALTH_MAX);
    CHECK(sched_health_next(SCHED_HEALTH_MAX - 1, SCHED_HEALTH_DEVICE_FAILURE) == SCHED_HEALTH_MAX);
    CHECK(sched_health_next(1000, SCHED_HEALTH_DEVICE_FAILURE) == SCHED_HEALTH_MAX);
}

/* 6) 只有设备层面的结果才算"设备结果" */
static void test_device_outcome_predicate(void)
{
    CHECK(sched_health_event_is_device_outcome(SCHED_HEALTH_DEVICE_SUCCESS) == true);
    CHECK(sched_health_event_is_device_outcome(SCHED_HEALTH_DEVICE_FAILURE) == true);
    CHECK(sched_health_event_is_device_outcome(SCHED_HEALTH_LOCAL_BACKPRESSURE) == false);
}

/* 7) 未知事件不猜测、不递增（宁可不动，也不要把无关事件算成设备故障） */
static void test_unknown_event_is_inert(void)
{
    CHECK(sched_health_next(7, (sched_health_event_t)99) == 7);
}

/* 8) 【混合序列】背压与设备结果交替时，只有设备部分影响计数 */
static void test_mixed_sequence(void)
{
    uint32_t e = 0;
    e = sched_health_next(e, SCHED_HEALTH_LOCAL_BACKPRESSURE);  /* 0 */
    e = sched_health_next(e, SCHED_HEALTH_LOCAL_BACKPRESSURE);  /* 0 */
    CHECK(e == 0);
    e = sched_health_next(e, SCHED_HEALTH_DEVICE_FAILURE);      /* 1 -> TIMEOUT */
    CHECK(e == 1);
    e = sched_health_next(e, SCHED_HEALTH_LOCAL_BACKPRESSURE);  /* 仍 1 */
    CHECK(e == 1);
    e = sched_health_next(e, SCHED_HEALTH_DEVICE_FAILURE);      /* 2 */
    e = sched_health_next(e, SCHED_HEALTH_DEVICE_FAILURE);      /* 3 -> FAULT */
    CHECK(e == 3);
    e = sched_health_next(e, SCHED_HEALTH_LOCAL_BACKPRESSURE);  /* 不改变 */
    CHECK(e == 3);
    e = sched_health_next(e, SCHED_HEALTH_DEVICE_SUCCESS);      /* 清零 */
    CHECK(e == 0);
}

/* 9) 名字唯一非空 */
static void test_names(void)
{
    CHECK(strcmp(sched_health_event_name(SCHED_HEALTH_DEVICE_SUCCESS), "DEVICE_SUCCESS") == 0);
    CHECK(strcmp(sched_health_event_name(SCHED_HEALTH_DEVICE_FAILURE), "DEVICE_FAILURE") == 0);
    CHECK(strcmp(sched_health_event_name(SCHED_HEALTH_LOCAL_BACKPRESSURE), "LOCAL_BACKPRESSURE") == 0);
    CHECK(strcmp(sched_health_event_name((sched_health_event_t)99), "UNKNOWN") == 0);
}

int main(void)
{
    test_backpressure_does_not_move_health();
    test_many_backpressures_never_reach_fault();
    test_device_failure_increments();
    test_device_success_clears();
    test_clamp();
    test_device_outcome_predicate();
    test_unknown_event_is_inert();
    test_mixed_sequence();
    test_names();
    if (s_failures) { printf("scheduler_health_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("scheduler_health_tests: all checks passed\n");
    return 0;
}
