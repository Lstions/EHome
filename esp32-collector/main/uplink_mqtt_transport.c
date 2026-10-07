/**
 * @file uplink_mqtt_transport.c
 * @brief 门控版 MQTT 上行出口（纯逻辑：宿主可编可测，不含任何 IDF 头）。
 *
 * ## 为什么不直接用 mqtt_transport_register()（task-21 实测的致命缺陷）
 *
 * 我们要让 MQTT 的 is_connected 受仲裁层门控，但不能用
 * mqtt_transport_unregister() + mqtt_transport_register() 反复切换 —— 实测：
 *
 *  1. mqtt_transport_adapter.c:150-151 注册时把 mqtt_adapter_msg_cb /
 *     mqtt_adapter_state_cb 装进 MQTT 回调槽；而 main.c:557/553 随后用
 *     on_mqtt_msg_cb / on_mqtt_state_cb 覆盖同一槽（setter 是单槽赋值：
 *     ehome_mqtt.c:652）。
 *  2. mqtt_transport_unregister()（adapter.c:166-175）只 transport_unregister()
 *     + free()，不还原回调；re-register 会把 adapter 回调再装回去，覆盖 app 回调。
 *  3. 而 adapter 的 transport->msg_cb 恒为 NULL（全仓只有 main.c:647 给
 *     tcp_transport 赋过 ->msg_cb）⇒ 转发是空操作。
 *  ⇒ 一旦移出再移回，MQTT 下行与状态回调被静默切断（正是本卡要防的那一族）。
 *
 * ## 本文件的做法
 *
 * 自己实现 ops（只贡献一个 transport 对象），绝不触碰任何 mqtt_client_register_*。
 *
 * ## 为什么 IO 是注入的（而不是直接调 mqtt_client_*）
 *
 * 直接调会把 ehome_mqtt.h 拉进来，而它 include mqtt_client.h / freertos/semphr.h
 * ⇒ 本文件无法进宿主测试 ⇒ 会被 check_host_coverage.py 判为未覆盖
 * （阈值 15，本卡禁止加豁免/调阈值）。
 * ⇒ 把发布与是否连上做成注入的两个函数指针：本文件保持纯 C
 * （只依赖 transport.h + esp_err.h，两者都有宿主 stub），
 * 真实绑定放在 uplink_arbiter.c 的 IDF 段（那里本来就有 IDF 依赖）。
 *
 * 这也让不触碰回调槽成为结构性事实：本文件里根本没有 mqtt_client_register_*
 * ⇒ 不是靠约定，而是没有那个符号（由宿主测试断言 ops 表来钉住）。
 */
#include "uplink_mqtt_transport.h"

#include <stddef.h>

/* 两个回调槽有意为 NULL：本对象只做上行出口，不参与下行。
 * 下行与状态回调仍由 main.c:553/557 注册的 app 回调持有 —— 本文件不碰它们。 */
static transport_t s_mqtt_tx = {
    .ops      = NULL,                   /* 由 uplink_mqtt_transport_ops() 绑定 */
    .type     = TRANSPORT_TYPE_MQTT,    /* 必须，否则 rep.mqtt_attempted 失真 */
    .state    = TRANSPORT_DISCONNECTED,
    .msg_cb   = NULL,
    .state_cb = NULL,
};

/* 注入的 IO。 */
static uplink_mqtt_io_t s_io = { NULL, NULL };

static esp_err_t mqtt_tx_init(transport_t *t, const void *cfg)
{
    (void)t; (void)cfg;
    return ESP_OK;   /* MQTT 客户端由 main.c 初始化，本层不重复初始化 */
}

static esp_err_t mqtt_tx_start(transport_t *t) { (void)t; return ESP_OK; }
static esp_err_t mqtt_tx_stop(transport_t *t)  { (void)t; return ESP_OK; }
static void      mqtt_tx_deinit(transport_t *t){ (void)t; }

static esp_err_t mqtt_tx_send(transport_t *t, const uint8_t *data, size_t len)
{
    (void)t;
    if (s_io.publish == NULL) {
        /* 未绑定 IO ⇒ 如实报错，绝不压成 ESP_OK（那是 D-01 的病根）。 */
        return ESP_FAIL;
    }
    /* 三态映射与 mqtt_transport_adapter.c:60-67 逐条一致：
     *   PUBLISH_OK                     -> ESP_OK
     *   PUBLISH_NOT_CONNECTED / FAILED -> ESP_FAIL
     * 适配层把两者都映为 ESP_FAIL，因为 transport_ops.send 的返回类型
     * 没有未连接这一档（理由见 mqtt_transport_adapter.c:48-59）。
     * 三态映射由注入的适配函数负责（本文件不认识 mqtt_publish_result_t）。 */
    return s_io.publish(data, len);
}

/* 门控：由仲裁层决定现在该不该走 MQTT —— 消除双发的一半。
 * 另一半是 3.0 侧的闸（session_transport.c 的 session_transport_connected）。 */
static bool mqtt_tx_is_connected(transport_t *t)
{
    (void)t;
    if (s_io.is_connected == NULL) return false;
    return s_io.is_connected();
}

static const transport_ops_t s_mqtt_tx_ops = {
    .init         = mqtt_tx_init,
    .start        = mqtt_tx_start,
    .stop         = mqtt_tx_stop,
    .send         = mqtt_tx_send,
    .is_connected = mqtt_tx_is_connected,
    .deinit       = mqtt_tx_deinit,
};

transport_t *uplink_mqtt_transport_ops(const uplink_mqtt_io_t *io)
{
    if (io == NULL) return NULL;
    s_io = *io;
    s_mqtt_tx.ops = &s_mqtt_tx_ops;
    return &s_mqtt_tx;
}
