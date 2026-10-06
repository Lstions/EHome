/**
 * @file transport_registry_tests.c
 * @brief 锁住 L-02 去重判据所依赖的注册表查询语义。
 *
 * 为什么需要这个文件（2026-10-06，L-02 根因修复的配套回归）
 * =======================================================
 * L-02 的根因是"同一帧被发布两次"：
 *   msg_handler_publish_checked()
 *     -> transport_broadcast()            [第 1 次，**已包含** MQTT 适配器]
 *     -> mqtt_client_publish_impl()       [第 2 次，直接重试]
 * 实测 PF/NT = 1.996（每个失败帧 2 条 "Publish failed"）。
 *
 * 修复用 transport_registry_has_type(TRANSPORT_TYPE_MQTT) 作为去重判据：
 * 只有当"MQTT 适配器确实已在注册表里"时才跳过第二次发布。
 *
 * 这意味着一件事：**这个函数的语义错了，修复就会以"消息根本不发"或
 * "重复发布照旧"的形式失效，而两者在 100 Hz 台架之外都很难察觉。**
 * 所以在此把它钉死：
 *   1. 未初始化 -> false（不是"猜一个"）；
 *   2. 只注册 TCP -> MQTT 查询必须是 false（否则会跳过 MQTT 发布 = 静默丢消息）；
 *   3. 注册了 MQTT -> true（否则重复发布照旧）；
 *   4. 注销后 -> false（回退路径必须复活）。
 *
 * 分母/口径：本用例直接编译**真实**的 components/transport/transport.c，
 * 不使用替身，因此它验证的是生产实现本身。
 */
#include <stdio.h>
#include <string.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "transport.h"

/* 宿主侧 ESP_LOGx 落在 host_test_log_record（见 stubs/esp_log.h）。
 * transport.c 会被真实编入，所以这里必须提供该符号。 */
void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level; (void)tag; (void)format;
}

static int s_failures;
#define CHECK(cond, msg) do { \
    if (!(cond)) { s_failures++; printf("FAIL: %s\n", msg); } \
    else { printf("ok: %s\n", msg); } \
} while (0)

/* transport_broadcast() 会遍历 ops->send；这里给一个最小可用实现。 */
static bool dummy_connected(transport_t *t)
{
    (void)t;
    return true;
}

static esp_err_t dummy_send(transport_t *t, const uint8_t *data, size_t len)
{
    (void)t; (void)data; (void)len;
    return ESP_OK;
}

static const transport_ops_t dummy_ops = {
    .init = NULL,
    .start = NULL,
    .stop = NULL,
    .send = dummy_send,
    .is_connected = dummy_connected,
    .deinit = NULL,
};

int main(void)
{
    /* 1) 未初始化：必须 false。若这里为 true，去重会在启动早期生效，
     *    把本该发出去的 MQTT 消息静默吞掉。 */
    CHECK(!transport_registry_has_type(TRANSPORT_TYPE_MQTT),
          "before init: MQTT must NOT be reported present");
    CHECK(!transport_registry_has_type(TRANSPORT_TYPE_TCP),
          "before init: TCP must NOT be reported present");

    transport_manager_init();

    /* 2) 只有 TCP：MQTT 查询必须 false。这是最关键的一条 ——
     *    若误报 true，真正的 MQTT 回退会被跳过 => 静默丢消息。 */
    static transport_t tcp = { .ops = &dummy_ops, .type = TRANSPORT_TYPE_TCP, .state = TRANSPORT_CONNECTED };
    CHECK(transport_register(&tcp) == ESP_OK, "register TCP transport");
    CHECK(transport_registry_has_type(TRANSPORT_TYPE_TCP),
          "TCP registered: TCP reports present");
    CHECK(!transport_registry_has_type(TRANSPORT_TYPE_MQTT),
          "TCP registered only: MQTT must NOT report present (else MQTT fallback is skipped silently)");

    /* 3) 注册 MQTT 适配器：现在必须 true —— 这正是跳过第二次发布的依据。 */
    static transport_t mqtt = { .ops = &dummy_ops, .type = TRANSPORT_TYPE_MQTT, .state = TRANSPORT_CONNECTED };
    CHECK(transport_register(&mqtt) == ESP_OK, "register MQTT transport");
    CHECK(transport_registry_has_type(TRANSPORT_TYPE_MQTT),
          "MQTT registered: MQTT reports present (dedup predicate fires)");

    /* 4) 注销 MQTT 后必须回到 false，回退路径复活。 */
    CHECK(transport_unregister(&mqtt) == ESP_OK, "unregister MQTT transport");
    CHECK(!transport_registry_has_type(TRANSPORT_TYPE_MQTT),
          "after unregister: MQTT must NOT report present (fallback revives)");

    printf("\n%s (%d failure%s)\n", s_failures ? "FAILED" : "PASSED",
           s_failures, s_failures == 1 ? "" : "s");
    return s_failures ? 1 : 0;
}
