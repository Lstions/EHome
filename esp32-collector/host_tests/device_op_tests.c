/* device_op_tests.c —— 前端可远程"重启 / 恢复出厂（保留 wifi）"的设备侧策略
 *
 * 需求原文（2026-10-06）：
 *   "要能前端操作节点设备恢复出厂（不重置 wifi 连接信息）、重启"
 *
 * 本用例锁住三件最容易做错的事：
 *   1. **wifi_cfg 绝不能被远程擦除**（擦了设备就再也连不回来）—— 直接断言；
 *   2. **顺序必须是 (擦除) → 刷新 ACK → 重启**（先重启则前端永远拿不到结果）；
 *   3. 失败路径**不重启**（否则操作员以为成功、设备却停在半状态）。
 *
 * 用注入的假 I/O 记录【调用顺序与命名空间】，因此这些行为都能在宿主机断言。
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "device_op.h"

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

/* ---- 假 I/O：记录调用顺序与参数 ---- */
enum { STEP_ERASE = 1, STEP_FLUSH = 2, STEP_RESTART = 3, ORDER_MAX = 32 };

static int  s_order[ORDER_MAX];
static int  s_order_n;
static char s_erased[8][32];
static int  s_erased_n;
static int  s_flush_calls;
static int  s_restart_calls;
static int  s_last_ack_len;
static int  s_erase_fail_on_call;   /* >0: 第 N 次 erase 返回失败 */
static int  s_flush_fail;           /* 1: flush_ack 返回失败 */

static void note(int step) { if (s_order_n < ORDER_MAX) s_order[s_order_n++] = step; }

static void reset_fake(void)
{
    s_order_n = 0; s_erased_n = 0; s_flush_calls = 0; s_restart_calls = 0;
    s_last_ack_len = 0; s_erase_fail_on_call = 0; s_flush_fail = 0;
}

static int fake_erase(void *ctx, const char *ns)
{
    (void)ctx;
    note(STEP_ERASE);
    if (s_erased_n < 8) snprintf(s_erased[s_erased_n++], 32, "%s", ns);
    if (s_erase_fail_on_call > 0 && s_erased_n == s_erase_fail_on_call) return -1;
    return 0;
}

static int fake_flush(void *ctx, const uint8_t *ack, size_t len)
{
    (void)ctx; (void)ack;
    note(STEP_FLUSH);
    s_flush_calls++;
    s_last_ack_len = (int)len;
    return s_flush_fail ? -1 : 0;
}

static void fake_restart(void *ctx)
{
    (void)ctx;
    note(STEP_RESTART);
    s_restart_calls++;
}

static const device_op_io_t FAKE_IO = {
    .erase_namespace = fake_erase,
    .flush_ack = fake_flush,
    .restart = fake_restart,
};

static bool erased_contains(const char *ns)
{
    for (int i = 0; i < s_erased_n; i++) {
        if (strcmp(s_erased[i], ns) == 0) return true;
    }
    return false;
}

static uint8_t ACK[8] = { 0x45, 0x48, 0x30, 0x23, 0, 0, 0, 0 };

/* ============ 1. 【需求核心】wifi_cfg 绝不能被远程擦除 ============ */
static void test_wifi_is_never_erased(void)
{
    reset_fake();
    device_op_reset_state();
    bool restarted = false;

    device_op_result_t r = device_op_execute(&FAKE_IO, NULL,
                                             DEVICE_OP_FACTORY_RESET_KEEP_CONN,
                                             ACK, sizeof(ACK), &restarted);
    CHECK(r == DEVOP_OK, "恢复出厂应成功，实际 %s", device_op_result_name(r));
    CHECK(restarted, "应重启");

    /* 直接断言需求：wifi 连接信息不被重置 */
    CHECK(!erased_contains("wifi_cfg"),
          "**wifi_cfg 被远程擦除了** —— 这是需求明确禁止的（擦了设备永久失联）");
    /* 顺带断言 ota 也不在远程路径（固件状态不是"配置"） */
    CHECK(!erased_contains("ota"),
          "ota 命名空间不应在远程恢复出厂路径里（可能触发非预期回滚）");
    /* 该擦的确实擦了 */
    CHECK(erased_contains("config"),
          "config 应被擦除以迫使重新拉取配置");

    printf("  (远程恢复出厂实际擦除：%d 个命名空间)\n", s_erased_n);
}

/* ============ 2. 擦除清单是单一来源，且不含连通性项 ============ */
static void test_namespace_list_is_single_source(void)
{
    size_t n = 0;
    const char *const *ns = device_op_factory_namespaces(&n);
    CHECK(ns != NULL && n > 0, "擦除清单不应为空");

    for (size_t i = 0; i < n; i++) {
        CHECK(strcmp(ns[i], "wifi_cfg") != 0,
              "擦除清单里出现了 wifi_cfg（需求禁止）");
        CHECK(strcmp(ns[i], "ota") != 0,
              "擦除清单里出现了 ota（固件状态，不应作为配置擦除）");
    }
    /* 清单必须**恰好**是 {config}：加东西要显式改设计，不能顺手加 */
    CHECK(n == 1 && strcmp(ns[0], "config") == 0,
          "远程擦除清单应恰为 {config}，实际 %zu 项", n);
}

/* ============ 3. 【顺序】擦除 -> 刷新 ACK -> 重启 ============ */
static void test_order_erase_flush_restart(void)
{
    reset_fake();
    device_op_reset_state();
    bool restarted = false;

    (void)device_op_execute(&FAKE_IO, NULL, DEVICE_OP_FACTORY_RESET_KEEP_CONN,
                            ACK, sizeof(ACK), &restarted);

    CHECK(s_order_n == 3, "应有 3 步，实际 %d", s_order_n);
    CHECK(s_order[0] == STEP_ERASE,   "第 1 步应为擦除，实际 %d", s_order[0]);
    CHECK(s_order[1] == STEP_FLUSH,   "第 2 步应为刷新 ACK，实际 %d", s_order[1]);
    CHECK(s_order[2] == STEP_RESTART, "第 3 步应为重启，实际 %d", s_order[2]);
    /* 顺序反了（先重启）前端就永远收不到 ACK —— 这是本条要拦的 */
    CHECK(s_last_ack_len == (int)sizeof(ACK), "ACK 长度应原样传递");
}

/* ============ 4. 重启操作：不擦任何东西，但同样先 ACK 后重启 ============ */
static void test_reboot_does_not_erase(void)
{
    reset_fake();
    device_op_reset_state();
    bool restarted = false;

    device_op_result_t r = device_op_execute(&FAKE_IO, NULL, DEVICE_OP_REBOOT,
                                             ACK, sizeof(ACK), &restarted);
    CHECK(r == DEVOP_OK, "重启应成功，实际 %s", device_op_result_name(r));
    CHECK(restarted, "应重启");
    CHECK(s_erased_n == 0, "重启【不应】擦除任何命名空间，实际擦了 %d 个", s_erased_n);
    CHECK(s_order_n == 2 && s_order[0] == STEP_FLUSH && s_order[1] == STEP_RESTART,
          "重启顺序应为 刷新ACK -> 重启");
}

/* ============ 5. 未知操作码：什么都不做 ============ */
static void test_unknown_op_does_nothing(void)
{
    reset_fake();
    device_op_reset_state();
    bool restarted = false;

    device_op_result_t r = device_op_execute(&FAKE_IO, NULL, (device_op_t)99,
                                             ACK, sizeof(ACK), &restarted);
    CHECK(r == DEVOP_ERR_UNKNOWN_OP, "应 UNKNOWN_OP，实际 %s", device_op_result_name(r));
    CHECK(!restarted, "未知操作不应重启");
    CHECK(s_order_n == 0, "未知操作不应有任何动作，实际 %d 步", s_order_n);
    CHECK(!device_op_in_progress(), "未知操作不应把状态置为进行中");
}

/* ============ 6. 单飞：重复触发被拒 ============ */
static void test_second_op_is_busy(void)
{
    reset_fake();
    device_op_reset_state();
    bool restarted = false;

    CHECK(device_op_execute(&FAKE_IO, NULL, DEVICE_OP_REBOOT, ACK, sizeof(ACK),
                            &restarted) == DEVOP_OK, "第一次应成功");
    CHECK(device_op_in_progress(), "成功后应处于进行中（重启前不允许再来一条）");

    reset_fake();   /* 只重置假 IO，不重置模块状态 */
    bool restarted2 = false;
    device_op_result_t r2 = device_op_execute(&FAKE_IO, NULL,
                                              DEVICE_OP_FACTORY_RESET_KEEP_CONN,
                                              ACK, sizeof(ACK), &restarted2);
    CHECK(r2 == DEVOP_ERR_BUSY, "第二次应 BUSY，实际 %s", device_op_result_name(r2));
    CHECK(s_order_n == 0, "被拒的操作不应有任何动作");
    CHECK(!restarted2, "被拒的操作不应重启");
}

/* ============ 7. 擦除失败：ACK 报告失败，但【不重启】 ============ */
static void test_erase_failure_reports_but_does_not_restart(void)
{
    reset_fake();
    device_op_reset_state();
    s_erase_fail_on_call = 1;
    bool restarted = false;

    device_op_result_t r = device_op_execute(&FAKE_IO, NULL,
                                             DEVICE_OP_FACTORY_RESET_KEEP_CONN,
                                             ACK, sizeof(ACK), &restarted);
    CHECK(r == DEVOP_ERR_ERASE_FAILED, "应 ERASE_FAILED，实际 %s", device_op_result_name(r));
    CHECK(!restarted, "擦除失败**不应**重启（否则操作员以为成功、设备停在半状态）");
    CHECK(s_restart_calls == 0, "restart 不应被调用");
    CHECK(s_flush_calls == 1, "失败也必须把 ACK 送出，让前端知道，实际 %d 次", s_flush_calls);
    /* 失败后可重试：单飞状态必须被解除（否则重试拿到 BUSY，只能重启设备再试） */
    CHECK(!device_op_in_progress(), "擦除失败后应解除单飞，允许重试");
    reset_fake();
    bool restarted2 = false;
    CHECK(device_op_execute(&FAKE_IO, NULL, DEVICE_OP_FACTORY_RESET_KEEP_CONN,
                            ACK, sizeof(ACK), &restarted2) == DEVOP_OK,
          "失败后应能重试成功");
    CHECK(restarted2, "重试成功应重启");
}

/* ============ 8. ACK 送不出去：不重启 ============ */
static void test_ack_flush_failure_does_not_restart(void)
{
    reset_fake();
    device_op_reset_state();
    s_flush_fail = 1;
    bool restarted = false;

    device_op_result_t r = device_op_execute(&FAKE_IO, NULL, DEVICE_OP_REBOOT,
                                             ACK, sizeof(ACK), &restarted);
    CHECK(r == DEVOP_ERR_ACK_FLUSH_FAILED, "应 ACK_FLUSH_FAILED，实际 %s",
          device_op_result_name(r));
    CHECK(!restarted, "ACK 送不出去时不应重启（否则操作员看到的是'点了没反应'）");
    CHECK(!device_op_in_progress(), "ACK 刷新失败后应解除单飞，允许重试");
    CHECK(s_restart_calls == 0, "restart 不应被调用");
}

/* ============ 9. 参数校验 ============ */
static void test_bad_args(void)
{
    reset_fake();
    device_op_reset_state();
    bool restarted = false;

    CHECK(device_op_execute(NULL, NULL, DEVICE_OP_REBOOT, ACK, sizeof(ACK), &restarted)
              == DEVOP_ERR_BAD_ARG, "io 为 NULL 应 BAD_ARG");
    CHECK(device_op_execute(&FAKE_IO, NULL, DEVICE_OP_REBOOT, NULL, 0, &restarted)
              == DEVOP_ERR_BAD_ARG, "ack 为空应 BAD_ARG");
    CHECK(s_order_n == 0, "参数错不应有任何动作");
    CHECK(!restarted, "参数错不应重启");
}

int main(void)
{
    test_wifi_is_never_erased();
    test_namespace_list_is_single_source();
    test_order_erase_flush_restart();
    test_reboot_does_not_erase();
    test_unknown_op_does_nothing();
    test_second_op_is_busy();
    test_erase_failure_reports_but_does_not_restart();
    test_ack_flush_failure_does_not_restart();
    test_bad_args();

    if (s_failures) { printf("device_op_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("device_op_tests: all checks passed\n");
    return 0;
}
