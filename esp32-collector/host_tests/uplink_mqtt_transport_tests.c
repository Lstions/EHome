
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

/* task-32：捕获 MQTT 实际收到的**字节**（此前只数调用次数）。
 * 为什么必须捕获字节：MQTT 与 3.0 TCP 共用 transport_ops.send 契约，
 * 3.0 TCP 侧需要 12 B 帧头，而 MQTT 侧是"原样当报文载荷发布"。
 * 若谁把成帧放错层（放到 publish / 放到共用契约上），3.0 那边照样绿，
 * 而 MQTT 这一份会被悄悄加上包头 ⇒ 2.x 设备与兜底路径全部读不懂。
 * ⇒ 只数次数抓不到它，必须断言**字节**。 */
#include <string.h>
static uint8_t s_pub_buf[512];
static size_t  s_pub_len = 0;

static esp_err_t fake_publish(const uint8_t *d, size_t n)
{
    s_publish_calls++;
    s_pub_len = (n <= sizeof(s_pub_buf)) ? n : sizeof(s_pub_buf);
    if (d != NULL && s_pub_len > 0) memcpy(s_pub_buf, d, s_pub_len);
    return s_publish_rc;
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

/* ⭐⭐ task-32 交付 4：**MQTT 路径不得被成帧**（"放错层"的失效形态）。
 *
 * 判据（两条一起才有意义）：
 *   1. MQTT 收到的字节与传入 payload **逐字节相同**（长度也一样）；
 *   2. 且它的前两字节**不是** 3.0 帧头 magic 0x45 0x48。
 * 第 1 条能抓住"加了头"（长度会 +12、内容会整体位移）；
 * 第 2 条把意图写成可读断言 —— 单看第 1 条，读者不知道"多出来的字节"是什么。
 *
 * 载荷刻意不 bare 0x45 开头：否则"没成帧"与"恰好以 0x45 开头"无法区分。 */
static void test_mqtt_payload_is_never_framed(void)
{
    /* 一份真实的 MQTT 上行载荷形态：首字节 0x03 = DataReport 类型。 */
    const uint8_t payload[] = { 0x03, 0x08, 0xAA, 0xBB, 0xCC, 0xDD, 0xEE };
    uplink_mqtt_io_t io = { fake_publish, fake_is_connected };
    transport_t *t = uplink_mqtt_transport_ops(&io);
    if (t == NULL || t->ops == NULL || t->ops->send == NULL) return;

    s_publish_calls = 0;
    s_pub_len = 0;
    s_publish_rc = ESP_OK;
    CHECK(t->ops->send(t, payload, sizeof(payload)) == ESP_OK, "发送应成功");

    /* 1) 长度必须**原样**：加 12 B 头会让它变成 19。 */
    CHECK(s_pub_len == sizeof(payload),
          "MQTT 收到的长度必须是原载荷长度 %u（加帧头会变成 %u），实际 %u",
          (unsigned)sizeof(payload), (unsigned)(sizeof(payload) + 12u), (unsigned)s_pub_len);

    /* 2) 内容必须逐字节相同。 */
    CHECK(s_pub_len == sizeof(payload) && memcmp(s_pub_buf, payload, sizeof(payload)) == 0,
          "MQTT 必须收到**原样**载荷（逐字节相同）—— 被成帧即污染 2.x 设备与兜底路径");

    /* 3) 且不得以 3.0 帧头 magic 开头。 */
    CHECK(!(s_pub_len >= 2 && s_pub_buf[0] == 0x45 && s_pub_buf[1] == 0x48),
          "MQTT 载荷不得带 3.0 帧头 magic 0x4548（成帧是 TCP 线协议的属性，不是消息语义的）");
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
    test_mqtt_payload_is_never_framed();   /* task-32：MQTT 不得被成帧 */
    test_unbound_io_is_refused();

    if (s_failures) {
        printf("uplink_mqtt_transport_tests: %d FAILURE(S)\n", s_failures);
        return 1;
    }
    printf("uplink_mqtt_transport_tests: all checks passed\n");
    return 0;
}
