/**
 * @file msg_handler.c
 * @brief Message Dispatcher Implementation
 */

#include "msg_handler.h"
#include "frame_codec.h"
#include "ehome_mqtt.h"
#include "config_mgr.h"
#include "ota.h"
#include "factory_reset.h"
#include "sync_manager.h"
#include "esp_log.h"
#include <string.h>

#define TAG "MSG"

/* HelloAck state */
static volatile bool s_hello_ack_received = false;
static volatile uint64_t s_server_time_ms = 0;

/* Weak callbacks - can be overridden by main.c */
__attribute__((weak)) void on_write_cmd_received(uint32_t request_id, uint32_t channel_id,
                                                   const uint8_t *data, size_t len, uint32_t read_size)
{
    (void)request_id;
    (void)channel_id;
    (void)data;
    (void)len;
    (void)read_size;
}

__attribute__((weak)) void on_scan_req_received(const char *request_id, uint32_t hardware_id)
{
    (void)request_id;
    (void)hardware_id;
}

/* === Init === */
void msg_handler_init(void)
{
    s_hello_ack_received = false;
    s_server_time_ms = 0;
    ESP_LOGI(TAG, "Message handler initialized");
}

/* === HelloAck state === */
bool msg_handler_is_hello_ack_received(void)
{
    return s_hello_ack_received;
}

uint64_t msg_handler_get_server_time(void)
{
    return s_server_time_ms;
}

void msg_handler_reset_hello_ack(void)
{
    s_hello_ack_received = false;
    s_server_time_ms = 0;
}

/* === Process incoming frame === */
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
    case MSG_CONFIG_MFST: {
        char manifest_id[64] = {0};
        uint64_t server_epoch = 0;
        frame_field_t field;
        while ((err = frame_decoder_next(&dec, &field)) == FRAME_OK) {
            if (field.field_num == 1 && field.wire_type == WIRE_LENGTH_DELIMITED) {
                size_t copy_len = field.value.bytes.len < sizeof(manifest_id) - 1 
                                ? field.value.bytes.len : sizeof(manifest_id) - 1;
                memcpy(manifest_id, field.value.bytes.ptr, copy_len);
                manifest_id[copy_len] = '\0';
            }
            /* v2.1: field 2 = config_epoch */
            if (field.field_num == 2 && field.wire_type == WIRE_VARINT) {
                server_epoch = field.value.varint;
            }
        }
        ESP_LOGI(TAG, "ConfigManifest: manifest_id=%s, epoch=%llu", 
                 manifest_id, (unsigned long long)server_epoch);
        config_mgr_apply_manifest(data, len);
        msg_handler_send_config_result(manifest_id, true);
        
        /* v2.1: notify sync_manager of applied config */
        sync_manager_on_config_applied(server_epoch, manifest_id);
        
        /* v2.1: notify downlink received */
        sync_manager_on_downlink_received(MSG_CONFIG_MFST);
        break;
    }

    case MSG_WRITE_CMD: {
        uint32_t request_id = 0, channel_id = 0, read_size = 0;
        const uint8_t *cmd_data = NULL;
        size_t cmd_len = 0;
        frame_field_t field;
        while ((err = frame_decoder_next(&dec, &field)) == FRAME_OK) {
            switch (field.field_num) {
            case 1: request_id = (uint32_t)field.value.varint; break;
            case 2: channel_id = (uint32_t)field.value.varint; break;
            case 3:
                cmd_data = field.value.bytes.ptr;
                cmd_len = field.value.bytes.len;
                break;
            case 4: read_size = (uint32_t)field.value.varint; break;
            }
        }
        ESP_LOGI(TAG, "WriteCmd: req=%lu, ch=%lu, len=%zu", 
                 (unsigned long)request_id, (unsigned long)channel_id, cmd_len);
        
        /* Check for factory reset command: channel_id=0, data=[0xFC, 0x00] */
        if (channel_id == 0 && cmd_len == 2 && cmd_data[0] == 0xFC && cmd_data[1] == 0x00) {
            ESP_LOGI(TAG, "Factory reset command received");
            msg_handler_send_write_rsp(request_id, true, 0, NULL);
            /* Perform factory reset - defined in main.c */
            factory_reset_trigger();
        } else {
            on_write_cmd_received(request_id, channel_id, cmd_data, cmd_len, read_size);
        }
        break;
    }

    case MSG_PING: {
        uint64_t timestamp_us = 0;
        frame_field_t field;
        while ((err = frame_decoder_next(&dec, &field)) == FRAME_OK) {
            if (field.field_num == 1) {
                timestamp_us = field.value.varint;
            }
        }
        ESP_LOGI(TAG, "Ping: ts=%llu", (unsigned long long)timestamp_us);
        msg_handler_send_pong(timestamp_us);
        break;
    }

    case MSG_OTA_CMD: {
        char ota_id[64] = {0};
        char firmware_url[256] = {0};
        char checksum[128] = {0};
        char version[32] = {0};
        uint64_t size_bytes = 0;
        frame_field_t field;
        while ((err = frame_decoder_next(&dec, &field)) == FRAME_OK) {
            if (field.wire_type != WIRE_LENGTH_DELIMITED && field.wire_type != WIRE_VARINT) continue;
            switch (field.field_num) {
            case 1:
                memcpy(ota_id, field.value.bytes.ptr, 
                       field.value.bytes.len < sizeof(ota_id)-1 ? field.value.bytes.len : sizeof(ota_id)-1);
                break;
            case 2:
                memcpy(firmware_url, field.value.bytes.ptr,
                       field.value.bytes.len < sizeof(firmware_url)-1 ? field.value.bytes.len : sizeof(firmware_url)-1);
                break;
            case 3:
                memcpy(checksum, field.value.bytes.ptr,
                       field.value.bytes.len < sizeof(checksum)-1 ? field.value.bytes.len : sizeof(checksum)-1);
                break;
            case 4: size_bytes = field.value.varint; break;
            case 5:
                memcpy(version, field.value.bytes.ptr,
                       field.value.bytes.len < sizeof(version)-1 ? field.value.bytes.len : sizeof(version)-1);
                break;
            }
        }
        ESP_LOGI(TAG, "OtaCmd: id=%s, url=%s, size=%llu", ota_id, firmware_url, (unsigned long long)size_bytes);
        ota_start(ota_id, firmware_url, checksum, size_bytes, version);
        break;
    }

    case MSG_SCAN_REQ: {
        char request_id[64] = {0};
        uint32_t hardware_id = 0;
        frame_field_t field;
        while ((err = frame_decoder_next(&dec, &field)) == FRAME_OK) {
            switch (field.field_num) {
            case 1:
                if (field.wire_type == WIRE_LENGTH_DELIMITED) {
                    memcpy(request_id, field.value.bytes.ptr,
                           field.value.bytes.len < sizeof(request_id)-1 ? field.value.bytes.len : sizeof(request_id)-1);
                }
                break;
            case 2:
                if (field.wire_type == WIRE_VARINT) {
                    hardware_id = (uint32_t)field.value.varint;
                }
                break;
            }
        }
        ESP_LOGI(TAG, "ScanReq: req=%s, hw=%lu", request_id, (unsigned long)hardware_id);
        on_scan_req_received(request_id, hardware_id);
        break;
    }

    case MSG_QUERY_REQ: {
        char request_id[64] = {0};
        uint32_t query_type = 0;
        frame_field_t field;
        while ((err = frame_decoder_next(&dec, &field)) == FRAME_OK) {
            switch (field.field_num) {
            case 1:
                if (field.wire_type == WIRE_LENGTH_DELIMITED) {
                    memcpy(request_id, field.value.bytes.ptr,
                           field.value.bytes.len < sizeof(request_id)-1 ? field.value.bytes.len : sizeof(request_id)-1);
                }
                break;
            case 2:
                if (field.wire_type == WIRE_VARINT) {
                    query_type = (uint32_t)field.value.varint;
                }
                break;
            }
        }
        ESP_LOGI(TAG, "QueryReq: req=%s, type=%lu", request_id, (unsigned long)query_type);
        /* Respond with basic hardware info */
        msg_handler_send_query_rsp(request_id, true, NULL);
        break;
    }

    case MSG_CONFIG_QUERY: {
        char request_id[64] = {0};
        frame_field_t field;
        while ((err = frame_decoder_next(&dec, &field)) == FRAME_OK) {
            if (field.field_num == 1 && field.wire_type == WIRE_LENGTH_DELIMITED) {
                memcpy(request_id, field.value.bytes.ptr,
                       field.value.bytes.len < sizeof(request_id)-1 ? field.value.bytes.len : sizeof(request_id)-1);
            }
        }
        ESP_LOGI(TAG, "ConfigQuery: req=%s", request_id);
        /* Respond with current config state */
        msg_handler_send_config_report(request_id);
        break;
    }

    case MSG_HELLO_ACK: {
        uint64_t server_time = 0;
        uint32_t features = 0;
        frame_field_t field;
        while ((err = frame_decoder_next(&dec, &field)) == FRAME_OK) {
            switch (field.field_num) {
            case 1: server_time = field.value.varint; break;
            case 2: features = (uint32_t)field.value.varint; break;
            }
        }
        s_hello_ack_received = true;
        s_server_time_ms = server_time;
        ESP_LOGI(TAG, "HelloAck: server_time=%llu features=%u",
                 (unsigned long long)server_time, (unsigned)features);
        
        /* v2.1: notify sync_manager */
        sync_manager_on_downlink_received(MSG_HELLO_ACK);
        break;
    }

    default:
        ESP_LOGW(TAG, "Unknown message type: 0x%02X", msg_type);
        break;
    }
}

/* === Send outgoing messages === */

void msg_handler_send_hello(const char *device_id, const char *fw_version,
                            const char *model, uint8_t channel_count)
{
    uint8_t buf[384];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_HELLO);
    
    /* v2.0 fields (1-4) */
    frame_encode_string(&enc, 1, device_id);
    frame_encode_string(&enc, 2, fw_version);
    frame_encode_string(&enc, 3, model);
    frame_encode_varint(&enc, 4, channel_count);
    
    /* v2.1 new fields (5-8) */
    frame_encode_varint(&enc, 5, config_mgr_get_epoch());
    frame_encode_varint(&enc, 6, config_mgr_has_manifest() ? 1 : 0);  /* bool as varint */
    const char *mid = config_mgr_get_manifest_id();
    if (mid && mid[0] != '\0') {
        frame_encode_string(&enc, 7, mid);
    }
    frame_encode_string(&enc, 8, "2.1");  /* protocol version */
    
    ESP_LOGI(TAG, "Sending Hello: %s, %s, %s, %d ch, epoch=%llu, nvs_has=%d, proto=2.1",
             device_id, fw_version, model, channel_count,
             (unsigned long long)config_mgr_get_epoch(),
             config_mgr_has_manifest());
    mqtt_client_publish_impl(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

void msg_handler_send_status(uint32_t uptime_sec, const char *status, uint8_t channel_count)
{
    uint8_t buf[128];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_STATUS_RPT);
    
    /* v2.0 fields (1-3) */
    frame_encode_varint(&enc, 1, uptime_sec);
    frame_encode_string(&enc, 2, status);
    frame_encode_varint(&enc, 3, channel_count);
    
    /* v2.1 new fields (4-5) */
    frame_encode_varint(&enc, 4, config_mgr_get_epoch());
    frame_encode_varint(&enc, 5, (uint64_t)sync_manager_get_state_enum());  /* idle/syncing/error */
    
    ESP_LOGD(TAG, "Sending StatusReport: %lu sec, %s, %d ch, epoch=%llu, sync_state=%d",
             (unsigned long)uptime_sec, status, channel_count,
             (unsigned long long)config_mgr_get_epoch(),
             sync_manager_get_state_enum());
    mqtt_client_publish_impl(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

void msg_handler_send_data_report(uint32_t channel_id, uint64_t timestamp_us,
                                  uint32_t sequence, const uint8_t *raw_data, size_t raw_len,
                                  uint32_t error_code, uint32_t request_id)
{
    uint8_t buf[512];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_DATA_RPT);
    frame_encode_varint(&enc, 1, channel_id);
    frame_encode_varint(&enc, 2, timestamp_us);
    frame_encode_varint(&enc, 3, sequence);
    if (raw_data && raw_len > 0) {
        frame_encode_bytes(&enc, 4, raw_data, raw_len);
    }
    if (error_code != 0) {
        frame_encode_varint(&enc, 5, error_code);
    }
    if (request_id != 0) {
        frame_encode_varint(&enc, 6, request_id);
    }
    
    ESP_LOGD(TAG, "Sending DataReport: ch=%lu, seq=%lu, len=%zu", 
             (unsigned long)channel_id, (unsigned long)sequence, raw_len);
    mqtt_client_publish_impl(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

void msg_handler_send_config_result(const char *manifest_id, bool success)
{
    uint8_t buf[128];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_CONFIG_RSLT);
    frame_encode_string(&enc, 1, manifest_id);
    frame_encode_varint(&enc, 2, success ? 1 : 0);
    
    ESP_LOGI(TAG, "Sending ConfigResult: %s, success=%d", manifest_id, success);
    mqtt_client_publish_impl(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

void msg_handler_send_write_rsp(uint32_t request_id, bool success,
                                uint32_t error_code, const char *error_msg)
{
    uint8_t buf[256];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_WRITE_RSP);
    frame_encode_varint(&enc, 1, request_id);
    frame_encode_varint(&enc, 2, success ? 1 : 0);
    if (error_code != 0) {
        frame_encode_varint(&enc, 3, error_code);
    }
    if (error_msg && error_msg[0] != '\0') {
        frame_encode_string(&enc, 4, error_msg);
    }
    
    ESP_LOGI(TAG, "Sending WriteRsp: req=%lu, success=%d", (unsigned long)request_id, success);
    mqtt_client_publish_impl(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

void msg_handler_send_pong(uint64_t timestamp_us)
{
    uint8_t buf[32];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_PONG);
    frame_encode_varint(&enc, 1, timestamp_us);
    
    ESP_LOGI(TAG, "Sending Pong: ts=%llu", (unsigned long long)timestamp_us);
    mqtt_client_publish_impl(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

void msg_handler_send_ota_prog(const char *ota_id, uint8_t status,
                               uint8_t progress_pct, const char *error_msg)
{
    uint8_t buf[256];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_OTA_PROG);
    frame_encode_string(&enc, 1, ota_id);
    frame_encode_varint(&enc, 2, status);
    frame_encode_varint(&enc, 3, progress_pct);
    if (error_msg && error_msg[0] != '\0') {
        frame_encode_string(&enc, 4, error_msg);
    }
    
    ESP_LOGI(TAG, "Sending OtaProg: %s, status=%d, progress=%d%%", ota_id, status, progress_pct);
    mqtt_client_publish_impl(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

void msg_handler_send_scan_rpt(const char *request_id, uint32_t hardware_id,
                               bool success, const uint32_t *addresses, uint8_t addr_count)
{
    uint8_t buf[256];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_SCAN_RPT);
    frame_encode_string(&enc, 1, request_id);
    frame_encode_varint(&enc, 2, hardware_id);
    frame_encode_varint(&enc, 3, success ? 1 : 0);
    
    if (addresses && addr_count > 0) {
        for (uint8_t i = 0; i < addr_count; i++) {
            frame_encode_varint(&enc, 4, addresses[i]);
        }
    }
    
    ESP_LOGI(TAG, "Sending ScanRpt: req=%s, hw=%lu, success=%d, addrs=%d",
             request_id, (unsigned long)hardware_id, success, addr_count);
    mqtt_client_publish_impl(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

void msg_handler_send_query_rsp(const char *request_id, bool success, const char *error_msg)
{
    uint8_t buf[256];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_QUERY_RSP);
    frame_encode_string(&enc, 1, request_id);
    frame_encode_varint(&enc, 2, success ? 1 : 0);
    if (error_msg && error_msg[0] != '\0') {
        frame_encode_string(&enc, 3, error_msg);
    }
    
    ESP_LOGI(TAG, "Sending QueryRsp: req=%s, success=%d", request_id, success);
    mqtt_client_publish_impl(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

void msg_handler_send_config_report(const char *request_id)
{
    uint8_t buf[256];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_CONFIG_REPORT);
    frame_encode_string(&enc, 1, request_id);
    
    /* Get current config from config_mgr */
    const config_manifest_t *cfg = config_mgr_get_manifest();
    if (cfg && cfg->manifest_id[0] != '\0') {
        frame_encode_string(&enc, 2, cfg->manifest_id);
        frame_encode_varint(&enc, 3, cfg->template_count);
        frame_encode_varint(&enc, 4, cfg->channel_count);
    } else {
        frame_encode_varint(&enc, 3, 0);
        frame_encode_varint(&enc, 4, 0);
    }
    
    ESP_LOGI(TAG, "Sending ConfigReport: req=%s, manifest=%s, tmpl=%d, ch=%d",
             request_id, cfg ? cfg->manifest_id : "none", 
             cfg ? cfg->template_count : 0, 
             cfg ? cfg->channel_count : 0);
    mqtt_client_publish_impl(frame_encoder_data(&enc), frame_encoder_size(&enc));
}
