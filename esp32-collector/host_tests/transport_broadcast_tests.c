/* transport_broadcast_tests.c —— D-01：广播必须【如实回报】它尝试了谁
 *
 * 这些断言的存在理由：msg_handler 原先用 transport_registry_has_type(MQTT)
 * 来回答"广播有没有尝试过 MQTT" —— 那是**代理判据**，答的是"是否已注册"。
 * 两者在"**已注册但未连接**"时不等价：
 *   transport_broadcast 只在 is_connected() 才调用 send，
 *   所以那一刻它从未尝试过 MQTT，而日志却说 "already attempted"。
 *
 * 本用例把这两个场景【分开】断言 —— 代理判据在这一对场景上必然给出同一个答案，
 * 因此这一对断言正是它无法通过的。 */
#include "transport.h"

#include <stdio.h>
#include <stdlib.h>
#include <string.h>

static int s_failures = 0;
#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond); s_failures++; } \
    } while (0)

typedef struct {
    int  send_calls;
    bool connected;
    esp_err_t send_ret;
} fake_t;

static esp_err_t f_init(transport_t *t, const void *cfg) { (void)t; (void)cfg; return ESP_OK; }
static esp_err_t f_start(transport_t *t) { (void)t; return ESP_OK; }
static esp_err_t f_stop(transport_t *t) { (void)t; return ESP_OK; }
static esp_err_t f_send(transport_t *t, const uint8_t *d, size_t n)
{
    fake_t *f = (fake_t *)t->priv_data;
    (void)d; (void)n;
    f->send_calls++;
    return f->send_ret;
}
static bool f_conn(transport_t *t) { return ((fake_t *)t->priv_data)->connected; }
static void f_deinit(transport_t *t) { (void)t; }

static const transport_ops_t FAKE_OPS = {
    .init = f_init, .start = f_start, .stop = f_stop,
    .send = f_send, .is_connected = f_conn, .deinit = f_deinit,
};

static transport_t *make(transport_type_t type, fake_t *f)
{
    transport_t *t = (transport_t *)calloc(1, sizeof(*t));
    t->ops = &FAKE_OPS;
    t->type = type;
    t->priv_data = f;
    return t;
}

static void reset_registry(void)
{
    transport_manager_init();
}

/* 1) 【关键对照 A】MQTT 已注册但【未连接】：
 *    broadcast 从未调用 send ⇒ mqtt_attempted 必须为 false。
 *    这正是代理判据答错的那个场景（它只看"注册了没有"）。 */
static void test_registered_but_not_connected_is_not_attempted(void)
{
    reset_registry();
    fake_t f = { 0, false, ESP_OK };
    transport_t *t = make(TRANSPORT_TYPE_MQTT, &f);
    CHECK(transport_register(t) == ESP_OK);

    /* 注册表里明明有 MQTT —— 代理判据到这里就会说"尝试过了" */
    CHECK(transport_registry_has_type(TRANSPORT_TYPE_MQTT) == true);

    transport_broadcast_report_t rep;
    esp_err_t r = transport_broadcast_ex((const uint8_t *)"x", 1, &rep);

    CHECK(f.send_calls == 0);
    CHECK(rep.attempted == 0);
    CHECK(rep.connected == 0);
    CHECK(rep.mqtt_attempted == false);   /* ← 代理判据在此必然给 true（错） */
    CHECK(r == ESP_ERR_INVALID_STATE);

    (void)transport_unregister(t);
    free(t);
}

/* 2) 【关键对照 B】MQTT 已注册【且已连接】、send 失败：
 *    确实调用过 send ⇒ mqtt_attempted 必须为 true。
 *    与用例 1 构成"代理判据无法区分、真实判据必须区分"的一对。 */
static void test_registered_and_connected_is_attempted(void)
{
    reset_registry();
    fake_t f = { 0, true, ESP_FAIL };
    transport_t *t = make(TRANSPORT_TYPE_MQTT, &f);
    CHECK(transport_register(t) == ESP_OK);

    transport_broadcast_report_t rep;
    esp_err_t r = transport_broadcast_ex((const uint8_t *)"x", 1, &rep);

    CHECK(f.send_calls == 1);
    CHECK(rep.attempted == 1);
    CHECK(rep.connected == 1);
    CHECK(rep.mqtt_attempted == true);
    CHECK(rep.sent == 0);                     /* 失败了，没送出去 */
    CHECK(r == ESP_ERR_INVALID_STATE);        /* 无成功 transport */

    (void)transport_unregister(t);
    free(t);
}

/* 3) 成功路径：sent 计数正确、返回 OK */
static void test_success_reports_sent(void)
{
    reset_registry();
    fake_t f = { 0, true, ESP_OK };
    transport_t *t = make(TRANSPORT_TYPE_MQTT, &f);
    CHECK(transport_register(t) == ESP_OK);

    transport_broadcast_report_t rep;
    esp_err_t r = transport_broadcast_ex((const uint8_t *)"x", 1, &rep);
    CHECK(r == ESP_OK);
    CHECK(rep.attempted == 1 && rep.sent == 1);
    CHECK(rep.mqtt_attempted == true && rep.tcp_attempted == false);

    (void)transport_unregister(t);
    free(t);
}

/* 4) 两种 transport：TCP 未连接、MQTT 已连接 ⇒ 只算 MQTT 被尝试过 */
static void test_per_type_flags_are_independent(void)
{
    reset_registry();
    fake_t mqtt_f = { 0, true,  ESP_OK };
    fake_t tcp_f  = { 0, false, ESP_OK };
    transport_t *m = make(TRANSPORT_TYPE_MQTT, &mqtt_f);
    transport_t *c = make(TRANSPORT_TYPE_TCP,  &tcp_f);
    CHECK(transport_register(m) == ESP_OK);
    CHECK(transport_register(c) == ESP_OK);

    transport_broadcast_report_t rep;
    (void)transport_broadcast_ex((const uint8_t *)"x", 1, &rep);
    CHECK(rep.mqtt_attempted == true);
    CHECK(rep.tcp_attempted == false);   /* TCP 没连，不算尝试 */
    CHECK(tcp_f.send_calls == 0);
    CHECK(rep.connected == 1);

    (void)transport_unregister(m); (void)transport_unregister(c);
    free(m); free(c);
}

/* 5) 空注册表：全 0，不崩 */
static void test_empty_registry(void)
{
    reset_registry();
    transport_broadcast_report_t rep;
    esp_err_t r = transport_broadcast_ex((const uint8_t *)"x", 1, &rep);
    CHECK(r == ESP_ERR_INVALID_STATE);
    CHECK(rep.attempted == 0 && rep.sent == 0 && rep.connected == 0);
    CHECK(rep.mqtt_attempted == false && rep.tcp_attempted == false);
}

/* 6) 参数错：报告被清零（不残留上一次的内容） */
static void test_bad_args_clears_report(void)
{
    reset_registry();
    transport_broadcast_report_t rep;
    memset(&rep, 0xAA, sizeof(rep));   /* 先塞垃圾 */
    esp_err_t r = transport_broadcast_ex(NULL, 0, &rep);
    CHECK(r == ESP_ERR_INVALID_ARG);
    CHECK(rep.attempted == 0 && rep.sent == 0);
    CHECK(rep.mqtt_attempted == false);
}

/* 7) out == NULL 时不应崩（旧签名路径） */
static void test_null_out_is_safe(void)
{
    reset_registry();
    fake_t f = { 0, true, ESP_OK };
    transport_t *t = make(TRANSPORT_TYPE_MQTT, &f);
    CHECK(transport_register(t) == ESP_OK);
    CHECK(transport_broadcast((const uint8_t *)"x", 1) == ESP_OK);
    CHECK(f.send_calls == 1);
    (void)transport_unregister(t);
    free(t);
}

int main(void)
{
    test_registered_but_not_connected_is_not_attempted();
    test_registered_and_connected_is_attempted();
    test_success_reports_sent();
    test_per_type_flags_are_independent();
    test_empty_registry();
    test_bad_args_clears_report();
    test_null_out_is_safe();
    if (s_failures) { printf("transport_broadcast_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("transport_broadcast_tests: all checks passed\n");
    return 0;
}
