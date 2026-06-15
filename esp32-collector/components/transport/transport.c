/**
 * @file transport.c
 * @brief Transport Manager 实现
 */

#include "transport.h"
#include "esp_log.h"
#include <string.h>

static const char *TAG = "TRANSPORT";

#define MAX_TRANSPORTS 4

static transport_t *s_transports[MAX_TRANSPORTS];
static int s_transport_count = 0;
static bool s_initialized = false;

void transport_manager_init(void)
{
    if (s_initialized) {
        return;
    }
    
    memset(s_transports, 0, sizeof(s_transports));
    s_transport_count = 0;
    s_initialized = true;
    
    ESP_LOGI(TAG, "Transport manager initialized");
}

esp_err_t transport_register(transport_t *transport)
{
    if (!s_initialized) {
        ESP_LOGE(TAG, "Transport manager not initialized");
        return ESP_ERR_INVALID_STATE;
    }
    
    if (!transport || !transport->ops) {
        ESP_LOGE(TAG, "Invalid transport");
        return ESP_ERR_INVALID_ARG;
    }
    
    if (s_transport_count >= MAX_TRANSPORTS) {
        ESP_LOGE(TAG, "Transport registry full");
        return ESP_ERR_NO_MEM;
    }
    
    s_transports[s_transport_count++] = transport;
    
    ESP_LOGI(TAG, "Registered transport type=%d, count=%d", 
             transport->type, s_transport_count);
    
    return ESP_OK;
}

esp_err_t transport_unregister(transport_t *transport)
{
    if (!s_initialized || !transport) {
        return ESP_ERR_INVALID_ARG;
    }
    
    for (int i = 0; i < s_transport_count; i++) {
        if (s_transports[i] == transport) {
            // 移动后面的元素填补空缺
            for (int j = i; j < s_transport_count - 1; j++) {
                s_transports[j] = s_transports[j + 1];
            }
            s_transports[--s_transport_count] = NULL;
            
            ESP_LOGI(TAG, "Unregistered transport, count=%d", s_transport_count);
            return ESP_OK;
        }
    }
    
    return ESP_ERR_NOT_FOUND;
}

esp_err_t transport_broadcast(const uint8_t *data, size_t len)
{
    if (!s_initialized) {
        return ESP_ERR_INVALID_STATE;
    }
    
    if (!data || len == 0) {
        return ESP_ERR_INVALID_ARG;
    }
    
    int sent_count = 0;
    
    for (int i = 0; i < s_transport_count; i++) {
        transport_t *t = s_transports[i];
        
        if (t && t->ops && t->ops->send) {
            if (t->ops->is_connected(t)) {
                esp_err_t err = t->ops->send(t, data, len);
                if (err == ESP_OK) {
                    sent_count++;
                }
            }
        }
    }
    
    if (sent_count == 0) {
        ESP_LOGW(TAG, "No transport connected for broadcast");
        return ESP_ERR_INVALID_STATE;
    }
    
    ESP_LOGD(TAG, "Broadcast to %d transports, len=%d", sent_count, (int)len);
    return ESP_OK;
}

esp_err_t transport_send(transport_t *transport, const uint8_t *data, size_t len)
{
    if (!transport || !data || len == 0) {
        return ESP_ERR_INVALID_ARG;
    }
    
    if (!transport->ops || !transport->ops->send) {
        return ESP_ERR_NOT_SUPPORTED;
    }
    
    return transport->ops->send(transport, data, len);
}

transport_t *transport_get_connected(void)
{
    if (!s_initialized) {
        return NULL;
    }
    
    for (int i = 0; i < s_transport_count; i++) {
        transport_t *t = s_transports[i];
        
        if (t && t->ops && t->ops->is_connected) {
            if (t->ops->is_connected(t)) {
                return t;
            }
        }
    }
    
    return NULL;
}

bool transport_any_connected(void)
{
    return transport_get_connected() != NULL;
}
