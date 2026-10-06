/**
 * @file mqtt_transport_adapter.c
 * @brief MQTT Transport 适配器 - 将 ehome_mqtt 包装为 transport 接口
 */

#include "transport.h"
#include "ehome_mqtt.h"
#include "esp_log.h"
#include <string.h>

static const char *TAG = "MQTT_ADAPTER";

typedef struct {
    transport_t transport;
} mqtt_adapter_t;

static mqtt_adapter_t *s_adapter = NULL;

/* === Transport 操作实现 === */

static esp_err_t mqtt_adapter_init(transport_t *transport, const void *config)
{
    // ehome_mqtt 已经在 main.c 中初始化
    return ESP_OK;
}

static esp_err_t mqtt_adapter_start(transport_t *transport)
{
    (void)transport;
    return mqtt_client_request_start();
}

static esp_err_t mqtt_adapter_stop(transport_t *transport)
{
    (void)transport;
    return mqtt_client_request_stop();
}

static esp_err_t mqtt_adapter_send(transport_t *transport, const uint8_t *data, size_t len)
{
    (void)transport;
    /* 三态解析 + 与修复前一致的对外语义。
     *
     * 为什么要解析三态：调用方需要能区分"本地没连上"与"对端/队列失败"，
     * 否则无法避免对**同一帧**重复发布（见 mqtt_client_publish_ex 注释与
     * msg_handler_publish_checked 里 L-02 的闭环推导）。
     *
     * 为什么这里仍然把两者都映射为 ESP_FAIL —— **更正一处错误理由**：
     * 原注释写"改动它会连带改变 sent_count 判定"。**实测不成立**：
     * transport_broadcast_ex() 只在 `err == ESP_OK` 时 sent_count++，
     * 而 NOT_CONNECTED 与 FAILED **都**映射为 ESP_FAIL（都是失败）
     * ⇒ 无论是否压平，sent_count 都不受影响。
     *
     * 真正的原因更简单也更诚实：`transport_ops.send` 的返回类型是 esp_err_t，
     * **没有**"未连接"这一档。要表达三态就得先扩展接口 ——
     * 那是 3.0 `link_result_t`（components/link）要做的事，不在本处最小修复范围。
     *
     * 而调用方的去重【已经不再依赖这个返回码】：见 msg_handler.c 改用
     * transport_broadcast_ex() 的 mqtt_attempted（D-01）。 */
    switch (mqtt_client_publish_ex(data, len)) {
    case MQTT_PUBLISH_OK:
        return ESP_OK;
    case MQTT_PUBLISH_NOT_CONNECTED:
    case MQTT_PUBLISH_FAILED:
    default:
        return ESP_FAIL;
    }
}

static bool mqtt_adapter_is_connected(transport_t *transport)
{
    return mqtt_client_is_connected_impl();
}

static void mqtt_adapter_deinit(transport_t *transport)
{
    // ehome_mqtt 没有 deinit API
}

static const transport_ops_t mqtt_adapter_ops = {
    .init = mqtt_adapter_init,
    .start = mqtt_adapter_start,
    .stop = mqtt_adapter_stop,
    .send = mqtt_adapter_send,
    .is_connected = mqtt_adapter_is_connected,
    .deinit = mqtt_adapter_deinit,
};

/* === 消息回调转发 === */

static void mqtt_adapter_msg_cb(const char *topic, const uint8_t *data, size_t len, void *ctx)
{
    transport_t *transport = ctx;
    
    if (transport && transport->msg_cb) {
        transport->msg_cb(data, len, transport->msg_cb_ctx);
    }
}

static void mqtt_adapter_state_cb(mqtt_client_state_t state, void *ctx)
{
    transport_t *transport = ctx;
    
    // 映射状态
    transport_state_t t_state;
    switch (state) {
        case MQTT_CLIENT_CONNECTED:
            t_state = TRANSPORT_CONNECTED;
            break;
        case MQTT_CLIENT_DISCONNECTED:
            t_state = TRANSPORT_DISCONNECTED;
            break;
        case MQTT_CLIENT_CONNECTING:
            t_state = TRANSPORT_CONNECTING;
            break;
        case MQTT_CLIENT_FAILED:
        default:
            t_state = TRANSPORT_FAILED;
            break;
    }
    
    transport->state = t_state;
    
    if (transport->state_cb) {
        transport->state_cb(t_state, transport->state_cb_ctx);
    }
}

/* === 公共 API === */

esp_err_t mqtt_transport_register(void)
{
    if (s_adapter) {
        ESP_LOGW(TAG, "MQTT adapter already registered");
        return ESP_OK;
    }
    
    s_adapter = calloc(1, sizeof(mqtt_adapter_t));
    if (!s_adapter) {
        ESP_LOGE(TAG, "Failed to allocate adapter");
        return ESP_ERR_NO_MEM;
    }
    
    // 初始化 transport 结构
    s_adapter->transport.ops = &mqtt_adapter_ops;
    s_adapter->transport.type = TRANSPORT_TYPE_MQTT;
    s_adapter->transport.state = TRANSPORT_DISCONNECTED;
    
    // 注册回调（将 MQTT 回调转发到 transport 回调）
    mqtt_client_register_msg_cb(mqtt_adapter_msg_cb, &s_adapter->transport);
    mqtt_client_register_state_cb(mqtt_adapter_state_cb, &s_adapter->transport);
    
    // 注册到 transport manager
    esp_err_t ret = transport_register(&s_adapter->transport);
    if (ret != ESP_OK) {
        ESP_LOGE(TAG, "Failed to register transport");
        free(s_adapter);
        s_adapter = NULL;
        return ret;
    }
    
    ESP_LOGI(TAG, "MQTT transport adapter registered");
    return ESP_OK;
}

void mqtt_transport_unregister(void)
{
    if (!s_adapter) {
        return;
    }
    
    transport_unregister(&s_adapter->transport);
    free(s_adapter);
    s_adapter = NULL;
    
    ESP_LOGI(TAG, "MQTT transport adapter unregistered");
}

transport_t *mqtt_transport_get(void)
{
    return s_adapter ? &s_adapter->transport : NULL;
}
