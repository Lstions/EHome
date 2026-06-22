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
#include "transport.h"
#include "ehome_mqtt.h"
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

void msg_handler_publish(const uint8_t *data, size_t len)
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
            return;
        }
        ESP_LOGW(TAG, "Failed to send via current transport, falling back to broadcast");
    }

    esp_err_t ret = transport_broadcast(data, len);
    if (ret == ESP_OK) {
        ESP_LOGD(TAG, "Broadcast to all transports (%d bytes)", (int)len);
        return;
    }

    ESP_LOGW(TAG, "Broadcast failed, falling back to MQTT");
    mqtt_client_publish_impl(data, len);
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

/* === Message dispatch === */

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

    switch (msg_type) {
    case MSG_CONFIG_MFST:
        /* v2.4: config_mgr_apply_manifest moved to handle_config_applied
         * inside app_state_lock_config() — prevents TOCTOU race between
         * manifest parse (clear+rebuild) and concurrent StatusReport reads. */
        handler_config_process_manifest(&dec);
        break;

    case MSG_WRITE_CMD:
        handler_writecmd_process(&dec);
        break;

    case MSG_PING:
        handler_hello_process_ping(&dec);
        break;

    case MSG_OTA_CMD:
        handler_data_process_ota(&dec);
        break;

    case MSG_SCAN_REQ:
        handler_writecmd_process_scan(&dec);
        break;

    case MSG_QUERY_REQ:
        handler_writecmd_process_query(&dec);
        break;

    case MSG_CONFIG_QUERY:
        handler_config_process_query(&dec);
        break;

    case MSG_HELLO_ACK:
        handler_hello_process_ack(&dec);
        break;

    case MSG_QUERY_RESOURCES:
        handler_config_process_query_resources(&dec);
        break;

    default:
        ESP_LOGW(TAG, "Unknown message type: 0x%02X", msg_type);
        break;
    }
}
