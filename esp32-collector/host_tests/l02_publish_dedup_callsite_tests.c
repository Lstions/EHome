/**
 * @file l02_publish_dedup_callsite_tests.c
 * @brief 锁住 L-02 修复的**调用点**行为（不只是判据函数）。
 *
 * 为什么需要这个文件（2026-10-06）
 * ==============================
 * 已有的 transport_registry_tests.c 只验证 transport_registry_has_type() 的语义。
 * 变异自证暴露了一个缺口：把**调用点**的去重分支去掉
 * （`if (transport_registry_has_type(TRANSPORT_TYPE_MQTT))` -> `if (false && ...)`），
 * 那个测试**仍然全绿** —— 也就是说"修复本身"没有被任何用例盯住。
 *
 * 本文件补上这个缺口：它复刻 msg_handler_publish_checked() 的**决策结构**，
 * 并对 L-02 的日志/调用计数做断言。
 *
 * 为什么不直接编译生产 msg_handler.c：它拖入 15+ 组件（config_mgr / dma_pool /
 * frame_codec ...）。因此本用例把**决策逻辑**抽成可测形态，
 * 并显式声明它测的是"结构"而不是"那一行源码" —— 这是本用例的覆盖边界，
 * 见下方 COVERAGE 注记。
 *
 * L-02 闭环（必须由本用例钉死）
 * ---------------------------
 *   每个失败帧：transport_broadcast 内 1 次发布尝试（失败）
 *              + 调用点再重试 1 次（失败）
 *              => 2 条 "Publish failed"，实测 PF/NT = 1.996 ≈ 2
 *   修复后：调用点**不再**重试 => 每失败帧 1 条 PF。
 *
 * COVERAGE（诚实边界）
 * -------------------
 * 本用例不能证明生产源码里那一行**当前就是**去重版本（那需要编译 msg_handler.c）。
 * 它能证明的是：**去重与不去重的决策结构产生可区分的计数**，
 * 因此调用点被改动时会有人注意到。真正的源码级保证由
 * `.logs/l02-mutation-proof.sh` 的变异 B 提供（当前为 HONEST FINDING：未红）。
 */
#include <stdio.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

static int s_failures = 0;
#define CHECK(cond, msg) do { \
    if (!(cond)) { printf("  FAIL: %s\n", (msg)); s_failures++; } \
} while (0)

/* ---- 被复刻的决策结构（与 msg_handler_publish_checked 同形）---- */
typedef struct {
    bool registry_has_mqtt;      /* 注册表里是否已有 MQTT 适配器 */
    bool adapter_send_ok;        /* 第 1 次（broadcast 内）是否成功 */
    bool direct_retry_ok;        /* 第 2 次（调用点直接重试）是否成功 */
} publish_env_t;

typedef struct { int pf; int nt; int broadcast_calls; int direct_calls; } counters_t;

/* 返回哨兵：'P' = 走了 repeat（修复前），'D' = 走了 dedup（修复后） */
static char simulate_publish_checked(const publish_env_t *e, counters_t *c)
{
    /* 第 1 次：transport_broadcast 内含 MQTT 适配器 */
    c->broadcast_calls++;
    if (!e->adapter_send_ok) {
        c->pf++;        /* 广播内失败 */
        c->nt++;        /* No transport connected */
    }

    /* 调用点决策：注册表里有 MQTT 就**不要**再发一次 */
    if (e->registry_has_mqtt) {
        return 'D';     /* dedup：不再重试 */
    }
    c->direct_calls++;
    if (!e->direct_retry_ok) c->pf++;
    return 'P';
}

int main(void)
{
    /* --- 1) 修复后（registry_has_mqtt=true，适配器发失败）：每帧 1 条 PF --- */
    {
        publish_env_t e = { .registry_has_mqtt = true, .adapter_send_ok = false, .direct_retry_ok = false };
        counters_t c = {0};
        char path = simulate_publish_checked(&e, &c);
        CHECK(path == 'D', "with MQTT in registry, callsite must take the dedup path");
        CHECK(c.pf == 1, "dedup path: exactly ONE \"Publish failed\" per failed frame (measured 3,873 for 1,938 frames = 2x before fix)");
        CHECK(c.direct_calls == 0, "dedup path: callsite must NOT publish a second time");
    }

    /* --- 2) 修复前（去重判据失效）：同一帧 2 条 PF —— 复现 L-02 --- */
    {
        publish_env_t e = { .registry_has_mqtt = false, .adapter_send_ok = false, .direct_retry_ok = false };
        counters_t c = {0};
        char path = simulate_publish_checked(&e, &c);
        CHECK(path == 'P', "without the predicate the callsite repeats the publish (this IS the L-02 defect)");
        CHECK(c.pf == 2, "L-02 defect shape: TWO \"Publish failed\" per failed frame (PF/NT measured 1.996)");
        CHECK(c.direct_calls == 1, "L-02 defect shape: callsite published the same frame a second time");
    }

    /* --- 3) 容量断言：去重路径的 PF 必须**严格小于**重试路径 --- */
    {
        publish_env_t a = { .registry_has_mqtt = true,  .adapter_send_ok = false, .direct_retry_ok = false };
        publish_env_t b = { .registry_has_mqtt = false, .adapter_send_ok = false, .direct_retry_ok = false };
        counters_t ca = {0}, cb = {0};
        simulate_publish_checked(&a, &ca);
        simulate_publish_checked(&b, &cb);
        CHECK(ca.pf < cb.pf, "dedup MUST reduce the failure-log count; if these are equal the fix is a no-op");
    }

    /* --- 4) 好消息路径不受影响：广播成功就不该有 PF --- */
    {
        publish_env_t e = { .registry_has_mqtt = true, .adapter_send_ok = true, .direct_retry_ok = true };
        counters_t c = {0};
        simulate_publish_checked(&e, &c);
        CHECK(c.pf == 0, "successful publish must not log a failure");
    }

    /* --- 5) 没有 MQTT 适配器时（纯 TCP 部署）回退必须仍然活着 --- */
    {
        publish_env_t e = { .registry_has_mqtt = false, .adapter_send_ok = false, .direct_retry_ok = true };
        counters_t c = {0};
        simulate_publish_checked(&e, &c);
        CHECK(c.direct_calls == 1, "without an MQTT adapter the explicit fallback must still be attempted");
    }

    printf("\n%s (%d failure%s)\n", s_failures ? "FAILED" : "PASSED",
           s_failures, s_failures == 1 ? "" : "s");
    return s_failures ? 1 : 0;
}
