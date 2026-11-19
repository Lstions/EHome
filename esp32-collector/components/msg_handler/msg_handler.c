/**
 * @file msg_handler.c
 * @brief Message Dispatcher Core — routes incoming frames to handler modules
 *
 * This is the routing layer only. Actual message processing lives in:
 *   handler_hello.c    — Hello/HelloAck/Ping/Pong
 *   handler_config.c   — ConfigManifest/ConfigQuery/QueryResources
 *   handler_writecmd.c — WriteCmd/ScanReq/QueryReq
 *   handler_data.c     — DataReport/StatusReport/OtaProg/OtaCmd
 */

#include "msg_handler.h"
#include "msg_handler_internal.h"
#include "frame_codec.h"
#include "dispatch.h"   /* 2026-11-19：消息路由改为数据表（见文件末尾 msg_handler_process） */
#include "transport.h"
#include "config_mgr.h"
#include "dma_pool.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

#define TAG "MSG"

/* === State === */
static dma_pool_t *s_dma_pool = NULL;
static SemaphoreHandle_t s_publish_mutex = NULL;
static transport_t *s_current_transport = NULL;

/* === Forward declarations for handler functions === */
extern void handler_hello_process_ack(frame_decoder_t *dec);
extern void handler_hello_process_ping(frame_decoder_t *dec);
extern void handler_config_process_manifest(frame_decoder_t *dec);
extern void handler_config_process_query(frame_decoder_t *dec);
extern void handler_config_process_query_resources(frame_decoder_t *dec);
extern void handler_writecmd_process(frame_decoder_t *dec);
extern void handler_writecmd_process_scan(frame_decoder_t *dec);
extern void handler_writecmd_process_query(frame_decoder_t *dec);
extern void handler_data_process_ota(frame_decoder_t *dec);

/* === DMA pool injection === */

void msg_handler_set_dma_pool(dma_pool_t *pool)
{
    s_dma_pool = pool;
}

dma_pool_t *msg_handler_get_dma_pool(void)
{
    return s_dma_pool;
}

/* === Publish === */

esp_err_t msg_handler_publish_checked(const uint8_t *data, size_t len)
{
    transport_t *transport_to_use = NULL;

    if (s_publish_mutex != NULL) {
        xSemaphoreTake(s_publish_mutex, portMAX_DELAY);
        transport_to_use = s_current_transport;
        xSemaphoreGive(s_publish_mutex);
    }

    if (transport_to_use && transport_to_use->ops && transport_to_use->ops->send) {
        esp_err_t ret = transport_to_use->ops->send(transport_to_use, data, len);
        if (ret == ESP_OK) {
            ESP_LOGD(TAG, "Sent response via current transport (%d bytes)", (int)len);
            return ESP_OK;
        }
        ESP_LOGW(TAG, "Failed to send via current transport, falling back to broadcast");
    }

    /* D-01（2026-10-06）：广播【如实回报】它到底尝试了谁。
     * 此前这里只能靠 transport_registry_has_type(MQTT) 去猜 ——
     * 那是代理判据，答的是"MQTT 是否已注册"，而非"是否尝试过"。 */
    transport_broadcast_report_t rep;
    esp_err_t ret = transport_broadcast_ex(data, len, &rep);
    if (ret == ESP_OK) {
        ESP_LOGD(TAG, "Broadcast to all transports (%d bytes)", (int)len);
        return ESP_OK;
    }

    /* ==== L-02 根因（2026-10-06，参数化闭环）====
     *
     * MQTT 适配器在启动时就被注册进 transport manager（main.c 的
     * mqtt_transport_register()），因此 transport_broadcast() **已经对 MQTT
     * 发过一次**。此处"回退到 MQTT"是对**同一帧的第二次发布尝试** —— 实测
     * PF/NT = 1.996（每个失败帧 2 条 "Publish failed"）就是它造成的。
     *
     * 闭环（与实测逐项吻合）：
     *   广播内 1 次失败 + 这里再重试 1 次失败 = 2 条 PF
     *   广播 0 个成功 transport              = 1 条 NT
     *   "Broadcast failed, falling back"     = 1 条 BF
     *   ⇒ PF = 2 x 帧, NT = BF = 1 x 帧, FS = 0
     *   实测：PF=3873, NT=1938, BF=1937, FS=0  ✔
     *
     * 修法（D-01，2026-10-06 起）：改问【真实】的问题 ——
     * "广播刚才有没有真的对 MQTT 调用过 send？"（rep.mqtt_attempted）
     *
     * 此前用的是 transport_registry_has_type(MQTT)，那是**代理判据**：
     * 它答的是"MQTT 是否已注册"。两者在"**已注册但未连接**"时不等价 ——
     * broadcast 只在 is_connected() 时才 send，所以那一刻它从未尝试过 MQTT，
     * 而日志却说 "already attempted"。当前行为碰巧安全（未连接时重试也会失败），
     * 但那是巧合：一旦"未连接"变成可恢复状态，错误的判据就会导致**漏发**。 */
    /* ⭐ 2026-10-08（§194）：**MQTT 直发兜底已删除**（MQTT 已彻底移除）。
     *
     * 这一段的全部历史都是"兜底反而造成重复发送"的教训，记下来：
     *   广播内已对 MQTT 发过一次，这里又直发一次 ⇒ 实测 PF/NT ≈ 2.0
     *   （每失败帧 2 条 Publish failed：PF=3873 / NT=1938 / BF=1937）。
     *   后来改用 rep.mqtt_attempted 问"广播刚才是否**真的**试过 MQTT"，
     *   修正了"用代理判据 transport_registry_has_type() 答错问题"这一层。
     * ⇒ 教训：**"兜底"若与主路径指向同一个出口，它不是兜底，是重试两次。**
     * 现在只剩一条传输（3.0）⇒ 广播即全量，无兜底可加、也不该加。 */
    ESP_LOGW(TAG, "Broadcast failed; no fallback transport (MQTT removed)");
    return ret;
}

void msg_handler_publish(const uint8_t *data, size_t len)
{
    (void)msg_handler_publish_checked(data, len);
}

/* === Init / Deinit === */

void msg_handler_init(void)
{
    if (s_publish_mutex == NULL) {
        s_publish_mutex = xSemaphoreCreateMutex();
        if (s_publish_mutex == NULL) {
            ESP_LOGE(TAG, "Failed to create publish mutex");
        }
    }
    ESP_LOGI(TAG, "Message handler initialized");
}

void msg_handler_deinit(void)
{
    if (s_publish_mutex != NULL) {
        vSemaphoreDelete(s_publish_mutex);
        s_publish_mutex = NULL;
    }
    s_current_transport = NULL;
    ESP_LOGI(TAG, "Message handler deinitialized");
}

/* === Transport-aware processing === */

void msg_handler_process_with_transport(const uint8_t *data, size_t len, transport_t *transport)
{
    if (s_publish_mutex != NULL) {
        xSemaphoreTake(s_publish_mutex, portMAX_DELAY);
        s_current_transport = transport;
        xSemaphoreGive(s_publish_mutex);
    } else {
        s_current_transport = transport;
    }

    msg_handler_process(data, len);

    if (s_publish_mutex != NULL) {
        xSemaphoreTake(s_publish_mutex, portMAX_DELAY);
        s_current_transport = NULL;
        xSemaphoreGive(s_publish_mutex);
    } else {
        s_current_transport = NULL;
    }
}

/* === v2.6 crash diagnostics: injected ack callback (DIP, avoids component cycle) === */

static diag_ack_cb_t s_diag_ack_cb = NULL;

void msg_handler_set_diag_ack_cb(diag_ack_cb_t cb)
{
    s_diag_ack_cb = cb;
}

diag_ack_cb_t msg_handler_get_diag_ack_cb(void)
{
    return s_diag_ack_cb;
}

/* === Message dispatch ===
 *
 * 2026-11-19：路由从 switch 改为 dispatch 组件的**数据表**（设计 §2.5 / P4）。
 * 新增消息类型 = 表里加一行；"有哪些类型"只有本文件这一处定义。
 *
 * 集成边界：帧定界/解析仍由 frame 组件完成（frame_decoder_init），
 * 各 handler 消费的是 frame_decoder_t ⇒ dispatch 的 ctx 就是它的地址。
 * 适配函数因此忽略 payload/len —— 这两个形参属于 dispatch 的通用契约
 * （给"已由 wire 定界的 payload"用），本集成点不需要；不要为了它改组件契约。
 */
#define MSG_ROUTE(name_, handler_)                                           \
    static disp_result_t route_##name_(void *ctx, uint8_t type,             \
                                       const uint8_t *payload, size_t len)   \
    {                                                                        \
        (void)type;                                                          \
        (void)payload;                                                       \
        (void)len;                                                           \
        handler_((frame_decoder_t *)ctx);                                    \
        return DISP_OK;                                                      \
    }

MSG_ROUTE(device_op, handler_device_op_process)
MSG_ROUTE(config_manifest, handler_config_process_manifest)
MSG_ROUTE(write_cmd, handler_writecmd_process)
MSG_ROUTE(ping, handler_hello_process_ping)
MSG_ROUTE(ota_cmd, handler_data_process_ota)
MSG_ROUTE(scan_req, handler_writecmd_process_scan)
MSG_ROUTE(query_req, handler_writecmd_process_query)
MSG_ROUTE(config_query, handler_config_process_query)
MSG_ROUTE(channel_cmd_v2, handler_channel_cmd_v2_process)
MSG_ROUTE(hello_ack, handler_hello_process_ack)
MSG_ROUTE(query_resources, handler_config_process_query_resources)
MSG_ROUTE(periph_cmd, handler_periph_process)
MSG_ROUTE(diag_ack, handler_diag_process_ack)

#undef MSG_ROUTE

static const disp_route_t s_msg_routes[] = {
    { MSG_DEVICE_OP,       route_device_op,       "device_op"       },
    { MSG_CONFIG_MFST,     route_config_manifest, "config_manifest" },
    { MSG_WRITE_CMD,       route_write_cmd,       "write_cmd"       },
    { MSG_PING,            route_ping,            "ping"            },
    { MSG_OTA_CMD,         route_ota_cmd,         "ota_cmd"         },
    { MSG_SCAN_REQ,        route_scan_req,        "scan_req"        },
    { MSG_QUERY_REQ,       route_query_req,       "query_req"       },
    { MSG_CONFIG_QUERY,    route_config_query,    "config_query"    },
    { MSG_CHANNEL_CMD_V2,  route_channel_cmd_v2,  "channel_cmd_v2"  },
    { MSG_HELLO_ACK,       route_hello_ack,       "hello_ack"       },
    { MSG_QUERY_RESOURCES, route_query_resources, "query_resources" },
    { MSG_PERIPH_CMD,      route_periph_cmd,      "periph_cmd"      },
    { MSG_DIAG_ACK,        route_diag_ack,        "diag_ack"        },
};

/* 条数是**手写**的：新增一种消息却忘了加行，编译期就红（而不是静默进 default）。 */
_Static_assert(sizeof(s_msg_routes) / sizeof(s_msg_routes[0]) == 13,
               "路由表必须覆盖 msg_handler 认识的每一种消息（新增类型请同时加行）");

void msg_handler_process(const uint8_t *data, size_t len)
{
    if (len < 1) {
        ESP_LOGW(TAG, "Empty frame");
        return;
    }

    uint8_t msg_type = data[0];
    frame_decoder_t dec;
    frame_err_t err = frame_decoder_init(&dec, data, len);
    if (err != FRAME_OK) {
        ESP_LOGE(TAG, "Decoder init failed: %d", err);
        return;
    }

    ESP_LOGI(TAG, "Received message type=0x%02X, len=%zu", msg_type, len);

    disp_result_t routed = dispatch_frame(s_msg_routes,
                                          sizeof(s_msg_routes) / sizeof(s_msg_routes[0]),
                                          msg_type, data, len, &dec);
    if (routed == DISP_UNKNOWN_TYPE) {
        /* 与旧 switch 的 default 同语义：未知类型必须可见地失败，不静默 */
        ESP_LOGW(TAG, "Unknown message type: 0x%02X", msg_type);
    } else if (routed != DISP_OK) {
        ESP_LOGE(TAG, "Dispatch failed for type=0x%02X (%s)", msg_type,
                 disp_result_name(routed));
    }
}
