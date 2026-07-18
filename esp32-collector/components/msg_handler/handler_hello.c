/**
 * @file handler_hello.c
 * @brief Hello/HelloAck message handler
 *
 * Receives: MSG_HELLO_ACK (0x12)
 * Sends:    Hello (0x01), Pong (0x09)
 */

#include "msg_handler.h"
#include "msg_handler_internal.h"
#include "frame_codec.h"
#include "config_mgr.h"
#include "sync_manager.h"
#include "esp_log.h"
#include <string.h>

#define TAG "HELLO_H"

/* The main component owns the connection-generation handshake supervisor. */
extern bool hello_handshake_notify_ack(uint32_t nonce);

/* HelloAck state */
static volatile bool s_hello_ack_received = false;
static volatile uint64_t s_server_time_ms = 0;

/* === HelloAck state accessors === */

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

/* === Receive: HelloAck (0x12) === */

void handler_hello_process_ack(frame_decoder_t *dec)
{
    uint64_t server_time = 0;
    uint32_t features = 0;
    uint32_t handshake_nonce = 0;
    bool seen_server_time = false;
    bool seen_features = false;
    bool seen_nonce = false;
    frame_err_t err;
    frame_field_t field;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        switch (field.field_num) {
        case HELLO_ACK_F_SERVER_TIME:
            if (seen_server_time || field.wire_type != WIRE_VARINT) {
                ESP_LOGW(TAG, "Rejecting HelloAck: invalid/duplicate server_time");
                return;
            }
            seen_server_time = true;
            server_time = field.value.varint;
            break;
        case HELLO_ACK_F_FEATURES:
            if (seen_features || field.wire_type != WIRE_VARINT ||
                field.value.varint > UINT32_MAX) {
                ESP_LOGW(TAG, "Rejecting HelloAck: invalid/duplicate features");
                return;
            }
            seen_features = true;
            features = (uint32_t)field.value.varint;
            break;
        case HELLO_ACK_F_HANDSHAKE_NONCE:
            if (seen_nonce || field.wire_type != WIRE_VARINT ||
                field.value.varint > UINT32_MAX) {
                ESP_LOGW(TAG, "Rejecting HelloAck: invalid/duplicate handshake_nonce");
                return;
            }
            seen_nonce = true;
            handshake_nonce = (uint32_t)field.value.varint;
            break;
        default:
            ESP_LOGW(TAG, "Rejecting HelloAck: unknown field %u",
                     (unsigned)field.field_num);
            return;
        }
    }
    if (err != FRAME_DONE) {
        ESP_LOGW(TAG, "Rejecting malformed HelloAck: frame_err=%d", (int)err);
        return;
    }

    if (!seen_server_time || !seen_features || !seen_nonce || handshake_nonce == 0) {
        ESP_LOGW(TAG, "Rejecting HelloAck: required fields or nonce missing");
        return;
    }
    bool accepted = hello_handshake_notify_ack(handshake_nonce);
    if (!accepted) {
        ESP_LOGW(TAG, "Rejecting HelloAck: stale nonce=%u", (unsigned)handshake_nonce);
        return;
    }
    s_server_time_ms = server_time;
    s_hello_ack_received = accepted;
    (void)features;
    ESP_LOGI(TAG, "HelloAck: server_time=%llu features=%u nonce=%u accepted=%d",
             (unsigned long long)server_time, (unsigned)features,
             (unsigned)handshake_nonce, accepted);
    sync_manager_on_downlink_received(MSG_HELLO_ACK);
}

/* === Receive: Ping (0x08) === */

void handler_hello_process_ping(frame_decoder_t *dec)
{
    uint64_t timestamp_us = 0;
    frame_err_t err;
    frame_field_t field;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        if (field.field_num == 1) {
            timestamp_us = field.value.varint;
        }
    }
    ESP_LOGI(TAG, "Ping: ts=%llu", (unsigned long long)timestamp_us);
    msg_handler_send_pong(timestamp_us);
}

/* === Send: Hello (0x01) === */

void msg_handler_send_hello(const char *node_id, const char *fw_version,
                            const char *model, uint8_t channel_count,
                            uint32_t handshake_nonce)
{
    if (handshake_nonce == 0) {
        ESP_LOGE(TAG, "Refusing to send Hello without handshake nonce");
        return;
    }
    uint8_t buf[384];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_HELLO);

    frame_encode_string(&enc, 1, node_id);
    frame_encode_string(&enc, 2, fw_version);
    frame_encode_string(&enc, 3, model);
    frame_encode_varint(&enc, 4, channel_count);
    frame_encode_varint(&enc, 5, config_mgr_get_epoch());
    /* field 6: in-memory applied state, NOT NVS last_known.
     * After reboot, in-memory is empty even if NVS has old manifest_id.
     * Reporting 1 when in-memory is empty causes backend to skip push. */
    frame_encode_varint(&enc, 6, config_mgr_has_manifest() ? 1 : 0);
    const char *mid = config_mgr_get_last_known_manifest_id();
    if (mid && mid[0] != '\0') {
        frame_encode_string(&enc, 7, mid);   /* field 7: last_manifest (string) */
    }
    frame_encode_string(&enc, HELLO_F_PROTO_VERSION, "2.6");
    frame_encode_varint(&enc, HELLO_F_HANDSHAKE_NONCE, handshake_nonce);

    /* v2.4: log the SAME value that field 6 encodes (in-memory, not NVS) */
    ESP_LOGI(TAG, "Sending Hello: %s, %s, %s, %d ch, epoch=%llu, nvs_has=%d, last_manifest=%s, proto_ver=2.6 nonce=%u",
             node_id, fw_version, model, channel_count,
             (unsigned long long)config_mgr_get_epoch(),
             config_mgr_has_manifest(),
             (mid && mid[0] != '\0') ? mid : "(none)",
             (unsigned)handshake_nonce);
    msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

/* === Send: Pong (0x09) === */

void msg_handler_send_pong(uint64_t timestamp_us)
{
    uint8_t buf[32];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_PONG);
    frame_encode_varint(&enc, 1, timestamp_us);
    ESP_LOGI(TAG, "Sending Pong: ts=%llu", (unsigned long long)timestamp_us);
    msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
}
