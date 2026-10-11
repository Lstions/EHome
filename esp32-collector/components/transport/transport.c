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

esp_err_t transport_broadcast_ex(const uint8_t *data, size_t len,
                                 transport_broadcast_report_t *out)
{
    if (out != NULL) {
        out->attempted = 0; out->sent = 0; out->connected = 0;
        out->mqtt_attempted = false; out->tcp_attempted = false;
    }

    if (!s_initialized) {
        return ESP_ERR_INVALID_STATE;
    }

    if (!data || len == 0) {
        return ESP_ERR_INVALID_ARG;
    }

    int sent_count = 0;
    int attempted = 0;
    int connected = 0;

    for (int i = 0; i < s_transport_count; i++) {
        transport_t *t = s_transports[i];

        if (t && t->ops && t->ops->send) {
            /* D-01：只有【真的调用过 send】才算"尝试过"。
             * 未连接时这里直接跳过 —— 调用方必须能看见这个区别，
             * 而不是靠"它注册了没有"去猜。 */
            if (t->ops->is_connected(t)) {
                connected++;
                esp_err_t err = t->ops->send(t, data, len);
                attempted++;
                if (out != NULL) {
                    if (t->type == TRANSPORT_TYPE_MQTT) out->mqtt_attempted = true;
                    if (t->type == TRANSPORT_TYPE_TCP)  out->tcp_attempted = true;
                }
                if (err == ESP_OK) {
                    sent_count++;
                }
            }
        }
    }

    if (out != NULL) {
        out->attempted = attempted;
        out->sent = sent_count;
        out->connected = connected;
    }

    if (sent_count == 0) {
        ESP_LOGW(TAG, "No transport connected for broadcast");
        return ESP_ERR_INVALID_STATE;
    }

    ESP_LOGD(TAG, "Broadcast to %d transports, len=%d", sent_count, (int)len);
    return ESP_OK;
}

esp_err_t transport_broadcast(const uint8_t *data, size_t len)
{
    /* 保持原签名与语义；不需要报告时用这个。 */
    return transport_broadcast_ex(data, len, NULL);
}

/* === D-09：定帧契约的唯一来源 ===
 *
 * 这张表是"每种传输交付什么"的【单一事实】。
 *
 * 诚实说明其强制力：transport_type_t 目前【没有 COUNT 哨兵】，
 * 所以新增类型时编译器【不会】因为表里少一项而报错 ——
 * 未表态的类型会走到下面的 UNKNOWN 分支（失败可见，但不是编译期失败）。
 * 想升级为编译期强制，需要给枚举加哨兵并同步所有 switch；
 * 那是一次独立改动，此处不顺手做（避免把 B0 的"小修"变成枚举重构）。 */
static const transport_framing_t s_framing[TRANSPORT_TYPE_TCP + 1] = {
    [TRANSPORT_TYPE_MQTT] = TRANSPORT_DELIVERS_MESSAGES,   /* MQTT 自带分帧 */
    [TRANSPORT_TYPE_TCP]  = TRANSPORT_DELIVERS_STREAM,     /* TCP 是字节流，必须上层定界 */
};

transport_framing_t transport_framing_of(transport_type_t type)
{
    /* 越界或未表态 => UNKNOWN，【不】默认成"完整消息"。
     * 默认成完整消息正是 D-09 的成因：一个未经声明的乐观假设。 */
    if ((int)type < 0 || (int)type >= (int)(sizeof(s_framing) / sizeof(s_framing[0]))) {
        return TRANSPORT_FRAMING_UNKNOWN;
    }
    transport_framing_t f = s_framing[type];
    if (f != TRANSPORT_DELIVERS_MESSAGES && f != TRANSPORT_DELIVERS_STREAM) {
        return TRANSPORT_FRAMING_UNKNOWN;
    }
    return f;
}

const char *transport_framing_name(transport_framing_t f)
{
    switch (f) {
    case TRANSPORT_DELIVERS_MESSAGES: return "MESSAGES";
    case TRANSPORT_DELIVERS_STREAM:   return "STREAM";
    default:                          return "UNKNOWN";
    }
}

bool transport_registry_has_type(transport_type_t type)
{
    if (!s_initialized) {
        return false;
    }
    for (int i = 0; i < s_transport_count; i++) {
        transport_t *t = s_transports[i];
        if (t != NULL && t->type == type) {
            return true;
        }
    }
    return false;
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

void transport_audit_ops(const transport_ops_t *ops, transport_ops_audit_t *out)
{
    if (out == NULL) return;
    const bool has = (ops != NULL);
    out->has_init         = has && ops->init != NULL;
    out->has_deinit       = has && ops->deinit != NULL;
    out->has_start        = has && ops->start != NULL;
    out->has_stop         = has && ops->stop != NULL;
    out->has_send         = has && ops->send != NULL;
    out->has_is_connected = has && ops->is_connected != NULL;
    /* 必需：没有它们传输根本无法工作 —— 缺失必须为 0 */
    out->required_missing = (out->has_start ? 0u : 1u) + (out->has_stop ? 0u : 1u)
                          + (out->has_send ? 0u : 1u) + (out->has_is_connected ? 0u : 1u);
    /* 可选：init/deinit 允许缺席（表示"无需运行时初始化/清理"），但要看得见 */
    out->optional_missing = (out->has_init ? 0u : 1u) + (out->has_deinit ? 0u : 1u);
}
