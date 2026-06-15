/**
 * @file ehome_tcp.c
 * @brief TCP Transport 实现
 */

#include "ehome_tcp.h"
#include "esp_log.h"
#include "esp_system.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/semphr.h"
#include "lwip/sockets.h"
#include "lwip/netdb.h"
#include <string.h>
#include <unistd.h>
#include <fcntl.h>

static const char *TAG = "TCP_TRANSPORT";

#define TCP_RECV_BUF_SIZE 2048
#define TCP_SEND_BUF_SIZE 2048
#define TCP_CLIENT_STACK_SIZE 4096
#define TCP_CLIENT_TASK_PRIORITY 5
#define MAX_CLIENTS_DEFAULT 4

/* === TCP 客户端连接 === */
typedef struct tcp_client {
    int socket;
    struct sockaddr_in addr;
    TaskHandle_t task_handle;
    bool active;
    transport_t *transport;  /* Add transport reference */
} tcp_client_t;

/* === TCP Transport 私有数据 === */
typedef struct tcp_transport_priv {
    tcp_transport_config_t config;
    int server_socket;
    TaskHandle_t accept_task;
    tcp_client_t *clients;
    int client_count;
    SemaphoreHandle_t clients_mutex;
    
    // 统计
    int total_connections;
    uint64_t bytes_sent;
    uint64_t bytes_received;
} tcp_transport_priv_t;

/* === 前向声明 === */
static esp_err_t tcp_init(transport_t *transport, const void *config);
static esp_err_t tcp_start(transport_t *transport);
static esp_err_t tcp_stop(transport_t *transport);
static esp_err_t tcp_send(transport_t *transport, const uint8_t *data, size_t len);
static bool tcp_is_connected(transport_t *transport);
static void tcp_deinit(transport_t *transport);

static void tcp_accept_task(void *arg);
static void tcp_client_task(void *arg);

/* === Transport 操作接口 === */
static const transport_ops_t tcp_ops = {
    .init = tcp_init,
    .start = tcp_start,
    .stop = tcp_stop,
    .send = tcp_send,
    .is_connected = tcp_is_connected,
    .deinit = tcp_deinit,
};

/* === 实现 === */

transport_t *tcp_transport_create(const tcp_transport_config_t *config)
{
    if (!config) {
        ESP_LOGE(TAG, "Invalid config");
        return NULL;
    }
    
    transport_t *transport = calloc(1, sizeof(transport_t));
    if (!transport) {
        ESP_LOGE(TAG, "Failed to allocate transport");
        return NULL;
    }
    
    transport->ops = &tcp_ops;
    transport->type = TRANSPORT_TYPE_TCP;
    transport->state = TRANSPORT_DISCONNECTED;
    transport->priv_data = calloc(1, sizeof(tcp_transport_priv_t));
    
    if (!transport->priv_data) {
        ESP_LOGE(TAG, "Failed to allocate priv data");
        free(transport);
        return NULL;
    }
    
    tcp_transport_priv_t *priv = transport->priv_data;
    memcpy(&priv->config, config, sizeof(tcp_transport_config_t));
    
    if (priv->config.max_clients <= 0) {
        priv->config.max_clients = MAX_CLIENTS_DEFAULT;
    }
    
    priv->clients = calloc(priv->config.max_clients, sizeof(tcp_client_t));
    if (!priv->clients) {
        ESP_LOGE(TAG, "Failed to allocate clients");
        free(transport->priv_data);
        free(transport);
        return NULL;
    }
    
    priv->clients_mutex = xSemaphoreCreateMutex();
    if (!priv->clients_mutex) {
        ESP_LOGE(TAG, "Failed to create mutex");
        free(priv->clients);
        free(transport->priv_data);
        free(transport);
        return NULL;
    }
    
    ESP_LOGI(TAG, "TCP transport created (port=%d, max_clients=%d)",
             priv->config.port, priv->config.max_clients);
    
    return transport;
}

void tcp_transport_destroy(transport_t *transport)
{
    if (!transport) {
        return;
    }
    
    if (transport->ops && transport->ops->deinit) {
        transport->ops->deinit(transport);
    }
    
    tcp_transport_priv_t *priv = transport->priv_data;
    if (priv) {
        if (priv->clients_mutex) {
            vSemaphoreDelete(priv->clients_mutex);
        }
        if (priv->clients) {
            free(priv->clients);
        }
        free(priv);
    }
    
    free(transport);
    
    ESP_LOGI(TAG, "TCP transport destroyed");
}

static esp_err_t tcp_init(transport_t *transport, const void *config)
{
    if (!transport) {
        return ESP_ERR_INVALID_ARG;
    }
    
    // 配置已在 create 时设置
    return ESP_OK;
}

static esp_err_t tcp_start(transport_t *transport)
{
    if (!transport) {
        return ESP_ERR_INVALID_ARG;
    }
    
    tcp_transport_priv_t *priv = transport->priv_data;
    
    // 创建服务器 socket
    priv->server_socket = socket(AF_INET, SOCK_STREAM, IPPROTO_TCP);
    if (priv->server_socket < 0) {
        ESP_LOGE(TAG, "Failed to create socket: errno %d", errno);
        transport->state = TRANSPORT_FAILED;
        return ESP_FAIL;
    }
    
    // 设置 socket 选项
    int opt = 1;
    setsockopt(priv->server_socket, SOL_SOCKET, SO_REUSEADDR, &opt, sizeof(opt));
    
    // 绑定地址
    struct sockaddr_in server_addr = {
        .sin_family = AF_INET,
        .sin_port = htons(priv->config.port),
        .sin_addr.s_addr = htonl(INADDR_ANY),
    };
    
    if (bind(priv->server_socket, (struct sockaddr *)&server_addr, sizeof(server_addr)) < 0) {
        ESP_LOGE(TAG, "Failed to bind: errno %d", errno);
        close(priv->server_socket);
        transport->state = TRANSPORT_FAILED;
        return ESP_FAIL;
    }
    
    // 监听
    if (listen(priv->server_socket, priv->config.max_clients) < 0) {
        ESP_LOGE(TAG, "Failed to listen: errno %d", errno);
        close(priv->server_socket);
        transport->state = TRANSPORT_FAILED;
        return ESP_FAIL;
    }
    
    transport->state = TRANSPORT_CONNECTED;
    
    // 创建 accept 任务
    BaseType_t ret = xTaskCreate(
        tcp_accept_task,
        "tcp_accept",
        4096,
        transport,
        5,
        &priv->accept_task
    );
    
    if (ret != pdPASS) {
        ESP_LOGE(TAG, "Failed to create accept task");
        close(priv->server_socket);
        transport->state = TRANSPORT_FAILED;
        return ESP_FAIL;
    }
    
    ESP_LOGI(TAG, "TCP server started on port %d", priv->config.port);
    return ESP_OK;
}

static esp_err_t tcp_stop(transport_t *transport)
{
    if (!transport) {
        return ESP_ERR_INVALID_ARG;
    }
    
    tcp_transport_priv_t *priv = transport->priv_data;
    
    // 停止 accept 任务
    if (priv->accept_task) {
        vTaskDelete(priv->accept_task);
        priv->accept_task = NULL;
    }
    
    // 关闭服务器 socket
    if (priv->server_socket >= 0) {
        close(priv->server_socket);
        priv->server_socket = -1;
    }
    
    // 关闭所有客户端
    if (xSemaphoreTake(priv->clients_mutex, pdMS_TO_TICKS(1000)) == pdTRUE) {
        for (int i = 0; i < priv->config.max_clients; i++) {
            if (priv->clients[i].active) {
                /* Check if task is still running before deleting */
                if (priv->clients[i].task_handle != NULL) {
                    vTaskDelete(priv->clients[i].task_handle);
                    priv->clients[i].task_handle = NULL;
                }
                /* Check if socket is still open before closing */
                if (priv->clients[i].socket >= 0) {
                    close(priv->clients[i].socket);
                    priv->clients[i].socket = -1;
                }
                priv->clients[i].active = false;
            }
        }
        priv->client_count = 0;
        xSemaphoreGive(priv->clients_mutex);
    }
    
    transport->state = TRANSPORT_DISCONNECTED;
    
    ESP_LOGI(TAG, "TCP server stopped");
    return ESP_OK;
}

static esp_err_t tcp_send(transport_t *transport, const uint8_t *data, size_t len)
{
    if (!transport || !data || len == 0) {
        return ESP_ERR_INVALID_ARG;
    }
    
    tcp_transport_priv_t *priv = transport->priv_data;
    
    if (!priv->clients || priv->client_count == 0) {
        return ESP_ERR_INVALID_STATE;
    }
    
    // 向所有活跃客户端发送
    int sent_count = 0;
    
    if (xSemaphoreTake(priv->clients_mutex, pdMS_TO_TICKS(100)) == pdTRUE) {
        for (int i = 0; i < priv->config.max_clients; i++) {
            if (priv->clients[i].active && priv->clients[i].socket >= 0) {
                ssize_t sent = send(priv->clients[i].socket, data, len, 0);
                if (sent > 0) {
                    sent_count++;
                    priv->bytes_sent += sent;
                } else {
                    ESP_LOGW(TAG, "Failed to send to client %d", i);
                }
            }
        }
        xSemaphoreGive(priv->clients_mutex);
    }
    
    if (sent_count == 0) {
        return ESP_FAIL;
    }
    
    ESP_LOGD(TAG, "Sent %d bytes to %d clients", (int)len, sent_count);
    return ESP_OK;
}

static bool tcp_is_connected(transport_t *transport)
{
    if (!transport) {
        return false;
    }
    
    tcp_transport_priv_t *priv = transport->priv_data;
    return (transport->state == TRANSPORT_CONNECTED && priv->client_count > 0);
}

static void tcp_deinit(transport_t *transport)
{
    tcp_stop(transport);
}

/* === Accept 任务 === */

static void tcp_accept_task(void *arg)
{
    transport_t *transport = arg;
    tcp_transport_priv_t *priv = transport->priv_data;
    
    ESP_LOGI(TAG, "Accept task started");
    
    while (1) {
        struct sockaddr_in client_addr;
        socklen_t addr_len = sizeof(client_addr);
        
        int client_socket = accept(priv->server_socket, 
                                   (struct sockaddr *)&client_addr, 
                                   &addr_len);
        
        if (client_socket < 0) {
            ESP_LOGE(TAG, "Accept failed: errno %d", errno);
            vTaskDelay(pdMS_TO_TICKS(100));
            continue;
        }
        
        ESP_LOGI(TAG, "Client connected from %s:%d",
                 inet_ntoa(client_addr.sin_addr),
                 ntohs(client_addr.sin_port));
        
        // 查找空闲客户端槽位
        if (xSemaphoreTake(priv->clients_mutex, pdMS_TO_TICKS(1000)) == pdTRUE) {
            int slot = -1;
            for (int i = 0; i < priv->config.max_clients; i++) {
                if (!priv->clients[i].active) {
                    slot = i;
                    break;
                }
            }
            
            if (slot < 0) {
                ESP_LOGW(TAG, "No free client slots, rejecting connection");
                close(client_socket);
            } else {
                priv->clients[slot].socket = client_socket;
                priv->clients[slot].addr = client_addr;
                priv->clients[slot].active = true;
                priv->clients[slot].transport = transport;  /* Store transport reference */
                priv->client_count++;
                priv->total_connections++;
                
                // 创建客户端接收任务
                BaseType_t ret = xTaskCreate(
                    tcp_client_task,
                    "tcp_client",
                    TCP_CLIENT_STACK_SIZE,
                    &priv->clients[slot],
                    TCP_CLIENT_TASK_PRIORITY,
                    &priv->clients[slot].task_handle
                );
                
                if (ret != pdPASS) {
                    ESP_LOGE(TAG, "Failed to create client task");
                    close(client_socket);
                    priv->clients[slot].active = false;
                    priv->client_count--;
                }
            }
            
            xSemaphoreGive(priv->clients_mutex);
        }
    }
    
    vTaskDelete(NULL);
}

/* === 客户端接收任务 === */

static void tcp_client_task(void *arg)
{
    tcp_client_t *client = arg;
    transport_t *transport = client->transport;  /* Use stored transport reference */
    
    if (!transport || !transport->msg_cb) {
        ESP_LOGE(TAG, "No transport or callback registered");
        close(client->socket);
        client->active = false;
        vTaskDelete(NULL);
        return;
    }
    
    tcp_transport_priv_t *priv = transport->priv_data;
    
    uint8_t *recv_buf = malloc(TCP_RECV_BUF_SIZE);
    if (!recv_buf) {
        ESP_LOGE(TAG, "Failed to allocate recv buffer");
        close(client->socket);
        client->active = false;
        vTaskDelete(NULL);
        return;
    }
    
    ESP_LOGI(TAG, "Client task started for %s:%d",
             inet_ntoa(client->addr.sin_addr),
             ntohs(client->addr.sin_port));
    
    while (client->active) {
        ssize_t received = recv(client->socket, recv_buf, TCP_RECV_BUF_SIZE, 0);
        
        if (received < 0) {
            ESP_LOGE(TAG, "Recv error: errno %d", errno);
            break;
        } else if (received == 0) {
            ESP_LOGI(TAG, "Client disconnected");
            break;
        }
        
        priv->bytes_received += received;
        
        ESP_LOGD(TAG, "Received %d bytes", (int)received);
        
        // 调用消息回调
        if (transport->msg_cb) {
            transport->msg_cb(recv_buf, received, transport->msg_cb_ctx);
        }
    }
    
    // 清理
    close(client->socket);
    client->socket = -1;  /* Mark socket as closed */
    client->active = false;
    client->task_handle = NULL;  /* Mark task as deleted before vTaskDelete */
    
    if (xSemaphoreTake(priv->clients_mutex, pdMS_TO_TICKS(1000)) == pdTRUE) {
        priv->client_count--;
        xSemaphoreGive(priv->clients_mutex);
    }
    
    free(recv_buf);
    
    ESP_LOGI(TAG, "Client task terminated");
    vTaskDelete(NULL);
}

void tcp_transport_get_stats(transport_t *transport, 
                             int *connected_clients,
                             int *total_connections,
                             uint64_t *bytes_sent,
                             uint64_t *bytes_received)
{
    if (!transport) {
        return;
    }
    
    tcp_transport_priv_t *priv = transport->priv_data;
    
    if (connected_clients) *connected_clients = priv->client_count;
    if (total_connections) *total_connections = priv->total_connections;
    if (bytes_sent) *bytes_sent = priv->bytes_sent;
    if (bytes_received) *bytes_received = priv->bytes_received;
}
