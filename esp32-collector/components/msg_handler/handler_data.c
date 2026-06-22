/**
 * @file handler_data.c
 * @brief DataReport/StatusReport/OtaProg message handler
 *
 * Receives: MSG_OTA_CMD (0x0C)
 * Sends:    DataReport (0x03), StatusReport (0x02), OtaProg (0x0D)
 */

#include "msg_handler.h"
#include "msg_handler_internal.h"
#include "frame_codec.h"
#include "config_mgr.h"
#include "sync_manager.h"
#include "scheduler.h"
#include "ota.h"
#include "esp_log.h"
#include <string.h>
#include <stdlib.h>

#define TAG "DATA_H"

/* === Receive: OtaCmd (0x0C) === */

void handler_data_process_ota(frame_decoder_t *dec)
{
    /* Heap-allocate to avoid static buffer concurrency issues */
    ota_cmd_t *cmd = calloc(1, sizeof(ota_cmd_t));
    if (!cmd) {
        ESP_LOGE(TAG, "Failed to allocate ota_cmd_t");
        return;
    }
    
    frame_err_t err;
    frame_field_t field;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        switch (field.field_num) {
        case OTA_CMD_F_OTA_ID:
            frame_field_get_string(&field, cmd->ota_id, sizeof(cmd->ota_id));
            break;
        case OTA_CMD_F_URL:
            frame_field_get_string(&field, cmd->firmware_url, sizeof(cmd->firmware_url));
            break;
        case OTA_CMD_F_CHECKSUM:
            frame_field_get_string(&field, cmd->checksum, sizeof(cmd->checksum));
            break;
        case OTA_CMD_F_SIZE:
            frame_field_get_varint(&field, &cmd->size_bytes);
            break;
        case OTA_CMD_F_VERSION:
            frame_field_get_string(&field, cmd->version, sizeof(cmd->version));
            break;
        }
    }
    
    ESP_LOGI(TAG, "OtaCmd: id=%s, url=%s, size=%llu", 
             cmd->ota_id, cmd->firmware_url, (unsigned long long)cmd->size_bytes);
    
    if (ota_is_duplicate(cmd->ota_id)) {
        ESP_LOGW(TAG, "OTA duplicate ignored: %s", cmd->ota_id);
        free(cmd);
        return;
    }
    ota_start(cmd);  /* ota_start takes ownership of cmd */
}

/* === Send: StatusReport (0x02) === */

void msg_handler_send_status(uint32_t uptime_sec, const char *status,
                             uint8_t channel_count, const scheduler_state_t *sched)
{
    uint8_t buf[512];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_STATUS_RPT);
    frame_encode_varint(&enc, 1, uptime_sec);
    frame_encode_string(&enc, 2, status);
    frame_encode_varint(&enc, 3, channel_count);
    frame_encode_varint(&enc, 4, config_mgr_get_epoch());
    frame_encode_varint(&enc, 5, (uint64_t)sync_manager_get_state_enum());

    // v2.2: send config_hash so server can decide without waiting for Hello
    const char *config_hash = config_mgr_get_last_known_manifest_id();
    if (config_hash != NULL) {
        frame_encode_string(&enc, 6, config_hash);
    }

    // v2.3: field 7 — channel_health (repeated nested) → edge_device_health
    if (sched != NULL) {
        for (int ci = 0; ci < sched->channel_count; ci++) {
            const sched_channel_t *ch = &sched->channels[ci];
            if (!ch->active || ch->edge_device_count == 0) continue;

            // Build ChannelHealth sub-frame
            uint8_t ch_buf[512];
            frame_encoder_t ch_enc;
            frame_encoder_init(&ch_enc, ch_buf, sizeof(ch_buf), 0);
            frame_encode_varint(&ch_enc, 1, ch->config.id); // channel_id

            for (int ed = 0; ed < ch->edge_device_count; ed++) {
                const sched_edge_device_t *dev = &ch->edge_devices[ed];
                for (int ci2 = 0; ci2 < dev->command_count; ci2++) {
                    const sched_command_t *cmd = &dev->commands[ci2];
                    if (cmd->error_count == 0 && cmd->enabled) continue; // healthy → skip to save bandwidth

                    // Build EdgeDeviceHealth sub-frame
                    uint8_t ed_buf[64];
                    frame_encoder_t ed_enc;
                    frame_encoder_init(&ed_enc, ed_buf, sizeof(ed_buf), 0);
                    frame_encode_varint(&ed_enc, 1, dev->edge_device_id);
                    frame_encode_varint(&ed_enc, 2, ci2);            // command_index
                    frame_encode_varint(&ed_enc, 3, cmd->error_count);
                    // comm_status: 0=OK, 1=TIMEOUT, 2=CRC_ERROR, 3=FAULT
                    uint64_t comm_st = cmd->error_count >= 3 ? 3 :
                                       (cmd->error_count > 0  ? 1 : 0);
                    frame_encode_varint(&ed_enc, 4, comm_st);

                    frame_encode_bytes(&ch_enc, 2,
                                       frame_encoder_data(&ed_enc),
                                       frame_encoder_size(&ed_enc));
                }
            }

            // Only emit ChannelHealth if it has content beyond channel_id
            size_t ch_content = frame_encoder_size(&ch_enc);
            if (ch_content > 0) {
                frame_encode_bytes(&enc, 7,
                                   frame_encoder_data(&ch_enc),
                                   frame_encoder_size(&ch_enc));
            }
        }
    }

    ESP_LOGD(TAG, "Sending StatusReport: %lu sec, %s, %d ch, epoch=%llu, sync_state=%d, hash=%s",
             (unsigned long)uptime_sec, status, channel_count,
             (unsigned long long)config_mgr_get_epoch(),
             sync_manager_get_state_enum(),
             config_hash ? config_hash : "(none)");
    msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

/* === Send: DataReport (0x03) === */

void msg_handler_send_data_report(uint32_t channel_id, uint64_t timestamp_us,
                                  uint32_t sequence, const uint8_t *raw_data, size_t raw_len,
                                  uint32_t error_code, uint32_t request_id,
                                  uint32_t edge_device_id, uint8_t command_index)
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
    /* v2.3: edge_device_id + command_index for multi-command routing */
    if (edge_device_id != 0) {
        frame_encode_varint(&enc, 7, edge_device_id);
    }
    if (command_index > 0 || edge_device_id != 0) {
        /* command_index 为 0 时也编码（只要有 edge_device_id） */
        frame_encode_varint(&enc, 8, command_index);
    }
    ESP_LOGD(TAG, "Sending DataReport: ch=%lu, seq=%lu, len=%zu, edge=%lu, cmd_idx=%u",
             (unsigned long)channel_id, (unsigned long)sequence, raw_len,
             (unsigned long)edge_device_id, command_index);
    msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

/* === Send: OtaProg (0x0D) === */

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
    msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
}
