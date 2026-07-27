/**
 * @file handler_writecmd.c
 * @brief WriteCommand/ScanReq/QueryReq message handler
 *
 * Receives: MSG_WRITE_CMD (0x06), MSG_SCAN_REQ (0x0D), MSG_QUERY_REQ (0x0E)
 * Sends:    WriteRsp (0x07), ScanRpt (0x0C), QueryRsp (0x0F)
 */

#include "msg_handler_internal.h"
#include "frame_codec.h"
#include "esp_log.h"
#include <limits.h>
#include <string.h>

#define TAG "WCMD_H"
#define LEGACY_WRITE_CMD_DATA_MAX 128U

void msg_handler_send_query_rsp(const char *request_id, bool success,
                                const char *error_msg);

/* Weak callbacks - implemented in main.c */
__attribute__((weak)) void on_write_cmd_received(uint32_t request_id, uint32_t channel_id,
                                                   const uint8_t *data, size_t len, uint32_t read_size,
                                                   uint32_t edge_device_id, uint32_t rx_timeout_ms)
{
    (void)request_id; (void)channel_id; (void)data; (void)len; (void)read_size;
    (void)edge_device_id; (void)rx_timeout_ms;
}

__attribute__((weak)) void on_scan_req_received(const char *request_id, uint32_t hardware_id)
{
    (void)request_id; (void)hardware_id;
}

__attribute__((weak)) void on_modbus_scan_req_received(const char *request_id,
    uint32_t start_addr, uint32_t end_addr, uint32_t timeout_ms)
{
    (void)request_id; (void)start_addr; (void)end_addr; (void)timeout_ms;
}

/* === Receive: WriteCmd (0x06) === */

void handler_writecmd_process(frame_decoder_t *dec)
{
    uint32_t request_id = 0, channel_id = 0, read_size = 0, edge_device_id = 0;
    uint32_t rx_timeout_ms = 1000;
    const uint8_t *cmd_data = NULL;
    size_t cmd_len = 0;
    uint32_t seen = 0;
    frame_err_t err;
    frame_field_t field;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        if (field.field_num < WRITE_CMD_F_REQUEST_ID || field.field_num > WRITE_CMD_F_RX_TIMEOUT_MS) {
            ESP_LOGW(TAG, "Rejecting WriteCmd unknown field=%u", field.field_num);
            return;
        }
        uint32_t bit = 1U << field.field_num;
        if ((seen & bit) != 0) {
            ESP_LOGW(TAG, "Rejecting WriteCmd duplicate field=%u", field.field_num);
            return;
        }
        seen |= bit;
        switch (field.field_num) {
        case WRITE_CMD_F_REQUEST_ID:
            if (field.wire_type != WIRE_VARINT || field.value.varint == 0 || field.value.varint > UINT32_MAX) return;
            request_id = (uint32_t)field.value.varint;
            break;
        case WRITE_CMD_F_CHANNEL_ID:
            if (field.wire_type != WIRE_VARINT || field.value.varint == 0 || field.value.varint > UINT32_MAX) return;
            channel_id = (uint32_t)field.value.varint;
            break;
        case WRITE_CMD_F_DATA:
            if (field.wire_type != WIRE_LENGTH_DELIMITED) return;
            if (field.value.bytes.len > LEGACY_WRITE_CMD_DATA_MAX) return;
            cmd_data = field.value.bytes.ptr;
            cmd_len = field.value.bytes.len;
            break;
        case WRITE_CMD_F_READ_SIZE:
            if (field.wire_type != WIRE_VARINT || field.value.varint > 256) return;
            read_size = (uint32_t)field.value.varint;
            break;
        case WRITE_CMD_F_EDGE_DEVICE_ID:
            if (field.wire_type != WIRE_VARINT || field.value.varint > UINT32_MAX) return;
            edge_device_id = (uint32_t)field.value.varint;
            break;
        case WRITE_CMD_F_RX_TIMEOUT_MS:
            if (field.wire_type != WIRE_VARINT || field.value.varint == 0 || field.value.varint > 30000) return;
            rx_timeout_ms = (uint32_t)field.value.varint;
            break;
        }
    }
    if (err != FRAME_DONE) {
        ESP_LOGW(TAG, "Rejecting malformed WriteCmd: decode error %d", err);
        return;
    }
    const uint32_t required = (1U << WRITE_CMD_F_REQUEST_ID) |
                              (1U << WRITE_CMD_F_CHANNEL_ID) |
                              (1U << WRITE_CMD_F_DATA);
    if ((seen & required) != required) {
        ESP_LOGW(TAG, "Rejecting WriteCmd missing required fields");
        return;
    }
    ESP_LOGI(TAG, "WriteCmd: req=%lu, ch=%lu, len=%zu",
             (unsigned long)request_id, (unsigned long)channel_id, cmd_len);

    on_write_cmd_received(request_id, channel_id, cmd_data, cmd_len, read_size,
                          edge_device_id, rx_timeout_ms);
}

/* === Receive: ScanReq (0x07) === */

void handler_writecmd_process_scan(frame_decoder_t *dec)
{
    char request_id[64] = {0};
    uint32_t hardware_id = 0;
    uint32_t scan_type = 1;     /* 1=I2C(default), 2=MODBUS */
    uint32_t start_addr = 0;
    uint32_t end_addr = 0;
    uint32_t timeout_ms = 200;  /* default 200ms */
    frame_err_t err;
    frame_field_t field;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        uint64_t v;
        switch (field.field_num) {
        case 1: frame_field_get_string(&field, request_id, sizeof(request_id)); break;
        case 2: if (frame_field_get_varint(&field, &v) == FRAME_OK) hardware_id = (uint32_t)v; break;
        case 3: if (frame_field_get_varint(&field, &v) == FRAME_OK) scan_type = (uint32_t)v; break;
        case 4: if (frame_field_get_varint(&field, &v) == FRAME_OK) start_addr = (uint32_t)v; break;
        case 5: if (frame_field_get_varint(&field, &v) == FRAME_OK) end_addr = (uint32_t)v; break;
        case 6: if (frame_field_get_varint(&field, &v) == FRAME_OK) timeout_ms = (uint32_t)v; break;
        }
    }

    if (scan_type == 2) {
        /* Modbus scan */
        ESP_LOGI(TAG, "ScanReq MODBUS: req=%s start=%lu end=%lu timeout=%lu",
                 request_id, (unsigned long)start_addr, (unsigned long)end_addr, (unsigned long)timeout_ms);
        on_modbus_scan_req_received(request_id, start_addr, end_addr, timeout_ms);
    } else {
        /* I2C scan (original logic) */
        ESP_LOGI(TAG, "ScanReq I2C: req=%s hw=%lu", request_id, (unsigned long)hardware_id);
        on_scan_req_received(request_id, hardware_id);
    }
}

/* === Receive: QueryReq (0x0E) === */

void handler_writecmd_process_query(frame_decoder_t *dec)
{
    char request_id[64] = {0};
    uint32_t query_type = 0;
    frame_err_t err;
    frame_field_t field;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        uint64_t v;
        switch (field.field_num) {
        case 1: frame_field_get_string(&field, request_id, sizeof(request_id)); break;
        case 2: if (frame_field_get_varint(&field, &v) == FRAME_OK) query_type = (uint32_t)v; break;
        }
    }
    ESP_LOGI(TAG, "QueryReq: req=%s, type=%lu", request_id, (unsigned long)query_type);
    msg_handler_send_query_rsp(request_id, true, NULL);
}

/* === Send: WriteRsp (0x07) === */

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
    msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

/* === Send: ScanRpt (0x0B) === */

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
    msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

/* === Send: QueryRsp (0x0F) === */

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
    msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
}
