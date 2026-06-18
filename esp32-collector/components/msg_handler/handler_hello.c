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
    frame_err_t err;
    frame_field_t field;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        switch (field.field_num) {
        case 1: server_time = field.value.varint; break;
        case 2: features = (uint32_t)field.value.varint; break;
        }
    }
    s_hello_ack_received = true;
    s_server_time_ms = server_time;
    ESP_LOGI(TAG, "HelloAck: server_time=%llu features=%u",
             (unsigned long long)server_time, (unsigned)features);
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
                            const char *model, uint8_t channel_count)
{
    uint8_t buf[384];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_HELLO);

    frame_encode_string(&enc, 1, node_id);
    frame_encode_string(&enc, 2, fw_version);
    frame_encode_string(&enc, 3, model);
    frame_encode_varint(&enc, 4, channel_count);
    frame_encode_varint(&enc, 5, config_mgr_get_epoch());
    frame_encode_varint(&enc, 6, config_mgr_has_last_known_manifest() ? 1 : 0);
    const char *mid = config_mgr_get_last_known_manifest_id();
    if (mid && mid[0] != '\0') {
        frame_encode_string(&enc, 7, mid);
    }
    frame_encode_string(&enc, 8, "2.1");

    ESP_LOGI(TAG, "Sending Hello: %s, %s, %s, %d ch, epoch=%llu, nvs_has=%d, proto=2.1",
             node_id, fw_version, model, channel_count,
             (unsigned long long)config_mgr_get_epoch(),
             config_mgr_has_last_known_manifest());
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
