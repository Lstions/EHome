/**
 * @file ehome_tcp.h
 * @brief TCP Transport 实现
 * 
 * 与 MQTT 平行的 TCP 通信协议层
 */

#ifndef EHOME_TCP_H
#define EHOME_TCP_H

#include "transport.h"
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === TCP Transport 配置 === */
typedef struct {
    uint16_t port;              // 监听端口 (服务器模式)
    const char *server_addr;    // 服务器地址 (客户端模式, NULL 表示服务器模式)
    uint16_t server_port;       // 服务器端口 (客户端模式)
    int max_clients;            // 最大客户端数 (服务器模式)
    int recv_timeout_ms;        // 接收超时
    int send_timeout_ms;        // 发送超时
} tcp_transport_config_t;

/**
 * @brief 创建 TCP transport 实例
 * 
 * @param config TCP 配置
 * @return transport_t* 成功返回 transport 实例，失败返回 NULL
 */
transport_t *tcp_transport_create(const tcp_transport_config_t *config);

/**
 * @brief 销毁 TCP transport 实例
 * 
 * @param transport transport 实例
 */
void tcp_transport_destroy(transport_t *transport);

/**
 * @brief 获取 TCP transport 统计信息
 * 
 * @param transport transport 实例
 * @param connected_clients 已连接客户端数 (输出)
 * @param total_connections 总连接数 (输出)
 * @param bytes_sent 发送字节数 (输出)
 * @param bytes_received 接收字节数 (输出)
 */
void tcp_transport_get_stats(transport_t *transport, 
                             int *connected_clients,
                             int *total_connections,
                             uint64_t *bytes_sent,
                             uint64_t *bytes_received);

#ifdef __cplusplus
}
#endif

#endif /* EHOME_TCP_H */
