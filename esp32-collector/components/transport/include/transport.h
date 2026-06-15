/**
 * @file transport.h
 * @brief 统一的传输层抽象接口
 * 
 * MQTT 和 TCP 都实现这个接口，上层业务逻辑使用统一接口
 */

#ifndef TRANSPORT_H
#define TRANSPORT_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === Transport 状态 === */
typedef enum {
    TRANSPORT_DISCONNECTED,
    TRANSPORT_CONNECTING,
    TRANSPORT_CONNECTED,
    TRANSPORT_FAILED,
} transport_state_t;

/* === Transport 类型 === */
typedef enum {
    TRANSPORT_TYPE_MQTT,
    TRANSPORT_TYPE_TCP,
} transport_type_t;

/* === 消息回调 === */
typedef void (*transport_msg_cb_t)(const uint8_t *data, size_t len, void *ctx);
typedef void (*transport_state_cb_t)(transport_state_t state, void *ctx);

/* === Transport 接口 === */
typedef struct transport_ops transport_ops_t;

typedef struct transport {
    const transport_ops_t *ops;
    transport_type_t type;
    transport_state_t state;
    transport_msg_cb_t msg_cb;
    void *msg_cb_ctx;
    transport_state_cb_t state_cb;
    void *state_cb_ctx;
    void *priv_data;  // 各 transport 的私有数据
} transport_t;

/* === Transport 操作接口 === */
struct transport_ops {
    esp_err_t (*init)(transport_t *transport, const void *config);
    esp_err_t (*start)(transport_t *transport);
    esp_err_t (*stop)(transport_t *transport);
    esp_err_t (*send)(transport_t *transport, const uint8_t *data, size_t len);
    bool (*is_connected)(transport_t *transport);
    void (*deinit)(transport_t *transport);
};

/* === Transport Manager API === */

/**
 * @brief 初始化 Transport Manager
 */
void transport_manager_init(void);

/**
 * @brief 注册一个 transport
 */
esp_err_t transport_register(transport_t *transport);

/**
 * @brief 注销一个 transport
 */
esp_err_t transport_unregister(transport_t *transport);

/**
 * @brief 向所有已连接的 transport 发送消息
 */
esp_err_t transport_broadcast(const uint8_t *data, size_t len);

/**
 * @brief 向指定的 transport 发送消息
 */
esp_err_t transport_send(transport_t *transport, const uint8_t *data, size_t len);

/**
 * @brief 获取第一个已连接的 transport
 */
transport_t *transport_get_connected(void);

/**
 * @brief 检查是否有任何 transport 已连接
 */
bool transport_any_connected(void);

#ifdef __cplusplus
}
#endif

#endif /* TRANSPORT_H */
