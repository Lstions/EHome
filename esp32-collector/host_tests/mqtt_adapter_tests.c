/* mqtt_adapter_tests.c —— D-24：把 MQTT 适配器的行为变成【可断言的事实】
 *
 * 为什么测它：这是 D-01 里"三态被压平"发生的那一层，也是 3.0 最关键的模块之一
 * （182 行，此前从未被宿主编译器看过一眼）。
 *
 * 本用例【不】改变其行为，而是把当前行为写成断言。价值在于：
 *   - 状态映射（MQTT_CLIENT_* -> TRANSPORT_*）此前只是"读代码才知道"，
 *     现在错了会红；
 *   - "三态被压成两态"此前只写在注释里，现在是一行**可执行的事实** ——
 *     将来 transport -> link 迁移时，若不再压平，本用例会红，
 *     那正是需要有人【有意】更新它的时刻（而不是悄悄漂移）。
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "esp_err.h"
/* 先看到真实声明，桩才能与它一致（mqtt_publish_result_t 等类型来自这里）。 */
#include "ehome_mqtt.h"

/* === 计数/可控桩：被测代码依赖的 ehome_mqtt API === */
static mqtt_publish_result_t s_publish_result = MQTT_PUBLISH_OK;
static int  s_publish_calls = 0;
static bool s_connected = false;
static int  s_start_calls = 0;
static int  s_stop_calls = 0;

bool mqtt_client_is_connected_impl(void) { return s_connected; }

mqtt_publish_result_t mqtt_client_publish_ex(const uint8_t *data, size_t len)
{
    (void)data; (void)len;
    s_publish_calls++;
    return s_publish_result;
}

esp_err_t mqtt_client_request_start(void) { s_start_calls++; return ESP_OK; }
esp_err_t mqtt_client_request_stop(void)  { s_stop_calls++;  return ESP_OK; }

void mqtt_client_register_msg_cb(mqtt_msg_cb_t cb, void *ctx)     { (void)cb; (void)ctx; }
void mqtt_client_register_state_cb(mqtt_state_cb_t cb, void *ctx) { (void)cb; (void)ctx; }

void host_test_log_record(char level, const char *tag, const char *format, ...)
{ (void)level; (void)tag; (void)format; }
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_OK"; }

/* 真实生产源码 */
#include "../components/ehome_mqtt/mqtt_transport_adapter.c"

static int s_failures = 0;
#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond); s_failures++; } \
    } while (0)

static transport_t *make_t(void)
{
    transport_t *t = (transport_t *)calloc(1, sizeof(*t));
    t->type = TRANSPORT_TYPE_MQTT;
    t->ops = &mqtt_adapter_ops;
    return t;
}

/* 1) 【D-01 核心事实】三态 -> 两态的映射，逐条钉死。
 *    这不是"应该如此"，而是"当前如此"—— 改它就等于改契约，必须有人有意为之。 */
static void test_send_flattens_three_states_to_two(void)
{
    transport_t *t = make_t();

    s_publish_result = MQTT_PUBLISH_OK;
    CHECK(mqtt_adapter_send(t, (const uint8_t *)"x", 1) == ESP_OK);

    s_publish_result = MQTT_PUBLISH_NOT_CONNECTED;
    CHECK(mqtt_adapter_send(t, (const uint8_t *)"x", 1) == ESP_FAIL);

    s_publish_result = MQTT_PUBLISH_FAILED;
    CHECK(mqtt_adapter_send(t, (const uint8_t *)"x", 1) == ESP_FAIL);

    CHECK(s_publish_calls == 3);   /* 每次都真的调了底层 */
    free(t);
}

/* 2) NOT_CONNECTED 与 FAILED 在当前契约下【不可区分】——
 *    把这件事写成断言，是为了让"压平"这个既成事实显式化：
 *    将来不再压平时，这条会红，提醒更新调用方（msg_handler 的 D-01 判据）。 */
static void test_not_connected_and_failed_are_indistinguishable(void)
{
    transport_t *t = make_t();

    s_publish_result = MQTT_PUBLISH_NOT_CONNECTED;
    esp_err_t a = mqtt_adapter_send(t, (const uint8_t *)"x", 1);
    s_publish_result = MQTT_PUBLISH_FAILED;
    esp_err_t b = mqtt_adapter_send(t, (const uint8_t *)"x", 1);

    CHECK(a == b);          /* ← 当前被有意压平 */
    CHECK(a == ESP_FAIL);
    free(t);
}

/* 3) is_connected 如实转发（不猜、不缓存） */
static void test_is_connected_forwards(void)
{
    transport_t *t = make_t();
    s_connected = false;
    CHECK(mqtt_adapter_is_connected(t) == false);
    s_connected = true;
    CHECK(mqtt_adapter_is_connected(t) == true);
    free(t);
}

/* 4) start/stop 转发到底层 */
static void test_start_stop_forward(void)
{
    transport_t *t = make_t();
    int a = s_start_calls, b = s_stop_calls;
    (void)mqtt_adapter_start(t);
    (void)mqtt_adapter_stop(t);
    CHECK(s_start_calls == a + 1);
    CHECK(s_stop_calls == b + 1);
    free(t);
}

/* 5) 状态回调映射：四个 MQTT 状态 -> 四个 transport 状态，逐条 */
static void test_state_mapping(void)
{
    transport_t *t = make_t();
    static transport_state_t seen;
    static int seen_count;
    transport_state_cb_t prev = NULL;
    (void)prev;

    seen_count = 0;
    t->state_cb = NULL;   /* 先不接回调，只验 transport->state */

    mqtt_adapter_state_cb(MQTT_CLIENT_CONNECTED, t);
    CHECK(t->state == TRANSPORT_CONNECTED);
    mqtt_adapter_state_cb(MQTT_CLIENT_DISCONNECTED, t);
    CHECK(t->state == TRANSPORT_DISCONNECTED);
    mqtt_adapter_state_cb(MQTT_CLIENT_CONNECTING, t);
    CHECK(t->state == TRANSPORT_CONNECTING);
    mqtt_adapter_state_cb(MQTT_CLIENT_FAILED, t);
    CHECK(t->state == TRANSPORT_FAILED);
    free(t);
    (void)seen; (void)seen_count;
}

/* 6) 消息回调转发：data/len 原样转给 transport->msg_cb，且用 msg_cb_ctx */
static int    g_msg_calls;
static size_t g_msg_len;
static void  *g_msg_ctx_seen;

static void counting_msg_cb(const uint8_t *data, size_t len, void *ctx)
{
    (void)data;
    g_msg_calls++;
    g_msg_len = len;
    g_msg_ctx_seen = ctx;
}

static void test_msg_cb_forwards(void)
{
    g_msg_calls = 0; g_msg_len = 0; g_msg_ctx_seen = NULL;

    transport_t *t = make_t();
    int sentinel = 42;
    t->msg_cb = counting_msg_cb;
    t->msg_cb_ctx = &sentinel;

    mqtt_adapter_msg_cb("topic", (const uint8_t *)"abc", 3, t);
    CHECK(g_msg_calls == 1);
    CHECK(g_msg_len == 3);
    CHECK(g_msg_ctx_seen == &sentinel);   /* ctx 必须原样传回 */

    /* 没装回调时不应崩 */
    t->msg_cb = NULL;
    mqtt_adapter_msg_cb("topic", (const uint8_t *)"abc", 3, t);
    CHECK(g_msg_calls == 1);

    free(t);
}

int main(void)
{
    test_send_flattens_three_states_to_two();
    test_not_connected_and_failed_are_indistinguishable();
    test_is_connected_forwards();
    test_start_stop_forward();
    test_state_mapping();
    test_msg_cb_forwards();
    if (s_failures) { printf("mqtt_adapter_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("mqtt_adapter_tests: all checks passed\n");
    return 0;
}
