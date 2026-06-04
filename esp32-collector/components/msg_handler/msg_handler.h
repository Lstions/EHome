/**
 * @file msg_handler.h
 * @brief Message Dispatcher - handles all protocol message types
 */

#ifndef MSG_HANDLER_H
#define MSG_HANDLER_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === Init === */
void msg_handler_init(void);

/* === HelloAck state === */
bool msg_handler_is_hello_ack_received(void);
uint64_t msg_handler_get_server_time(void);
void msg_handler_reset_hello_ack(void);

/* === Process incoming frame === */
void msg_handler_process(const uint8_t *data, size_t len);

/* === Send outgoing messages === */
void msg_handler_send_hello(const char *node_id, const char *fw_version,
                            const char *model, uint8_t channel_count);
void msg_handler_send_status(uint32_t uptime_sec, const char *status, uint8_t channel_count);
void msg_handler_send_data_report(uint32_t channel_id, uint64_t timestamp_us,
                                  uint32_t sequence, const uint8_t *raw_data, size_t raw_len,
                                  uint32_t error_code, uint32_t request_id);
void msg_handler_send_config_result(const char *manifest_id, bool success);
void msg_handler_send_write_rsp(uint32_t request_id, bool success,
                                uint32_t error_code, const char *error_msg);
void msg_handler_send_pong(uint64_t timestamp_us);
void msg_handler_send_ota_prog(const char *ota_id, uint8_t status,
                               uint8_t progress_pct, const char *error_msg);
void msg_handler_send_scan_rpt(const char *request_id, uint32_t hardware_id,
                               bool success, const uint32_t *addresses, uint8_t addr_count);
void msg_handler_send_query_rsp(const char *request_id, bool success, const char *error_msg);
void msg_handler_send_config_report(const char *request_id);

/* === Factory reset - implemented in main.c === */
void factory_reset(void);

#ifdef __cplusplus
}
#endif

#endif /* MSG_HANDLER_H */
