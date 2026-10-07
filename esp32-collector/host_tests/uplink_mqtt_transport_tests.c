
/* ⭐ 门控版 MQTT 上行出口的**纯逻辑**（main/uplink_mqtt_transport.c）。
 *
 * 为什么这组断言必须存在（本卡历史上真的踩过）：
 * 该对象替代 mqtt_transport_register() 成为上行出口，它错了有两种静默故障：
 *   1. is_connected 不问仲裁层 ⇒ 双栈稳态**双发**（本卡要消除的那个）；
 *   2. send 把失败压成 ESP_OK ⇒ 上层以为发出去了（D-01 病根）。
 * 另外还要钉住"本对象不参与下行"（msg_cb/state_cb 恒 NULL）——
 * 那是"不重蹈 unregister 覆盖 app 回调"的结构性保证。
 */
#include "uplink_mqtt_transport.h"

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

static int s_publish_calls = 0;
static esp_err_t s_publish_rc = ESP_OK;
static bool s_connected = true;

static esp_err_t fake_publish(const uint8_t *d, size_t n)
{
    (void)d; (void)n; s_publish_calls++; return s_publish_rc;
}
static bool fake_is_connected(void) { return s_connected; }

static void test_ops_table_and_no_downlink(void)
{
    uplink_mqtt_io_t io = { fake_publish, fake_is_connected };
    transport_t *t = uplink_mqtt_transport_ops(&io);
    CHECK(t != NULL, "绑定 IO 后应返回 transport");
    if (t == NULL) return;

    CHECK(t->type == TRANSPORT_TYPE_MQTT,
          "type 必须是 MQTT，否则 rep.mqtt_attempted 失真（实际 %d）", (int)t->type);
    CHECK(t->ops != NULL, "ops 必须已绑定");
    if (t->ops == NULL) return;

    /* ⭐ 结构性保证：本对象不参与下行 —— 两个回调槽恒 NULL。 */
    CHECK(t->msg_cb == NULL,
          "msg_cb 必须为 NULL：本对象只做上行出口，下行仍归 main.c 注册的 app 回调");
    CHECK(t->state_cb == NULL,
          "state_cb 必须为 NULL：状态回调同样归 app（不参与 = 不会覆盖）");

    /* 必需钩子齐全（缺 is_connected 会让广播把它当"永远可投"）。 */
    CHECK(t->ops->send != NULL, "send 必需");
    CHECK(t->ops->is_connected != NULL, "is_connected 必需（缺它会被无条件投递）");
}

/* ⭐ 双发的另一半：is_connected 必须**转发**到仲裁门（不是自己拍脑袋）。 */
static void test_is_connected_follows_arbiter_gate(void)
{
    uplink_mqtt_io_t io = { fake_publish, fake_is_connected };
    transport_t *t = uplink_mqtt_transport_ops(&io);
    if (t == NULL || t->ops == NULL) return;

    s_connected = true;
    CHECK(t->ops->is_connected(t) == true, "门开 ⇒ is_connected 应为真");
    s_connected = false;
    CHECK(t->ops->is_connected(t) == false,
          "门关 ⇒ is_connected 必须为假（这就是消除双发的落点）");
    s_connected = true;
}

/* D-01：send 必须**如实回报**，绝不把失败压成 ESP_OK。 */
static void test_send_reports_truthfully(void)
{
    uplink_mqtt_io_t io = { fake_publish, fake_is_connected };
    transport_t *t = uplink_mqtt_transport_ops(&io);
    if (t == NULL || t->ops == NULL) return;

    s_publish_calls = 0;
    s_publish_rc = ESP_OK;
    CHECK(t->ops->send(t, (const uint8_t *)"X", 1) == ESP_OK, "成功应返回 ESP_OK");
    CHECK(s_publish_calls == 1, "成功路径应调用 publish 1 次，实际 %d", s_publish_calls);

    s_publish_rc = ESP_FAIL;
    esp_err_t rc = t->ops->send(t, (const uint8_t *)"X", 1);
    CHECK(rc != ESP_OK, "失败绝不能压成 ESP_OK（D-01 病根）");
}

/* 未绑定 IO 时必须**拒绝**，不能假装成功。 */
static void test_unbound_io_is_refused(void)
{
    transport_t *t = uplink_mqtt_transport_ops(NULL);
    CHECK(t == NULL, "io 为 NULL 应返回 NULL（不注册半成品）");

    /* 绑定一个 send=NULL 的 IO：send 必须报错而不是 ESP_OK。 */
    uplink_mqtt_io_t empty = { NULL, NULL };
    transport_t *t2 = uplink_mqtt_transport_ops(&empty);
    CHECK(t2 != NULL, "绑定空 IO 应返回对象（由 send/is_connected 拒绝）");
    if (t2 != NULL && t2->ops != NULL) {
        CHECK(t2->ops->send(t2, (const uint8_t *)"X", 1) != ESP_OK,
              "未绑定 publish ⇒ send 必须报错（绝不假装成功）");
        CHECK(t2->ops->is_connected(t2) == false,
              "未绑定 is_connected ⇒ 必须为假（绝不假装可投）");
    }
}

int main(void)
{
    test_ops_table_and_no_downlink();
    test_is_connected_follows_arbiter_gate();
    test_send_reports_truthfully();
    test_unbound_io_is_refused();

    if (s_failures) {
        printf("uplink_mqtt_transport_tests: %d FAILURE(S)\n", s_failures);
        return 1;
    }
    printf("uplink_mqtt_transport_tests: all checks passed\n");
    return 0;
}
