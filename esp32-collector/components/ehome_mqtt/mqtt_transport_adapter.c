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
    mqtt_client_start();
    return ESP_OK;
}

static esp_err_t mqtt_adapter_stop(transport_t *transport)
{
    mqtt_client_stop();
    return ESP_OK;
}

static esp_err_t mqtt_adapter_send(transport_t *transport, const uint8_t *data, size_t len)
{
    bool ok = mqtt_client_publish_impl(data, len);
    return ok ? ESP_OK : ESP_FAIL;
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
