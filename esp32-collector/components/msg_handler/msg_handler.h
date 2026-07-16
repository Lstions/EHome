/**
 * @file msg_handler.h
 * @brief Message Dispatcher - handles all protocol message types
 */

#ifndef MSG_HANDLER_H
#define MSG_HANDLER_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "scheduler.h"
#include "frame_codec.h"
#include "config_mgr.h"
#include "esp_err.h"

/* Forward declaration */
typedef struct transport transport_t;

#ifdef __cplusplus
extern "C" {
#endif

/* === Init === */
void msg_handler_init(void);
void msg_handler_deinit(void);

/* === HelloAck state === */
bool msg_handler_is_hello_ack_received(void);
uint64_t msg_handler_get_server_time(void);
void msg_handler_reset_hello_ack(void);

/* === Message processing === */
void msg_handler_process_with_transport(const uint8_t *data, size_t len, transport_t *transport);
void msg_handler_process(const uint8_t *data, size_t len);

/* === Send outgoing messages === */
void msg_handler_send_hello(const char *node_id, const char *fw_version,
                            const char *model, uint8_t channel_count);
esp_err_t msg_handler_send_status(uint32_t uptime_sec, const char *status,
                             uint8_t channel_count, const scheduler_state_t *sched);
void msg_handler_send_data_report(uint32_t channel_id, uint64_t timestamp_us,
                                  uint32_t sequence, const uint8_t *raw_data, size_t raw_len,
                                  uint32_t error_code, uint32_t request_id,
                                  uint32_t edge_device_id, uint32_t command_template_id,
                                  uint8_t command_index);
esp_err_t msg_handler_send_config_result(const char *manifest_id, const char *sync_id, bool success);
void msg_handler_send_write_rsp(uint32_t request_id, bool success,
                                uint32_t error_code, const char *error_msg);
void msg_handler_send_pong(uint64_t timestamp_us);
void msg_handler_send_ota_prog(const char *ota_id, uint8_t status,
                               uint8_t progress_pct, const char *error_msg);
void msg_handler_send_scan_rpt(const char *request_id, uint32_t hardware_id,
                               bool success, const uint32_t *addresses, uint8_t addr_count);
void msg_handler_send_query_rsp(const char *request_id, bool success, const char *error_msg);
void msg_handler_send_config_report(const char *request_id);
void msg_handler_send_resource_report(void);

/* === Publish raw frame via current transport (MQTT/TCP) === */
void msg_handler_publish(const uint8_t *data, size_t len);

/* DIP: inject dma_pool for ResourceReport encoding */
struct dma_pool_t;
void msg_handler_set_dma_pool(struct dma_pool_t *pool);

/* === v3.0: Peripheral control (GPIO/PWM) === */
esp_err_t handler_periph_init(void);
void handler_periph_process(frame_decoder_t *dec);
esp_err_t handler_periph_apply_configs(const config_manifest_t *cfg);
esp_err_t handler_periph_apply_configs_locked(const config_manifest_t *cfg);

/* === Weak callbacks - implemented in main.c, declared in handler modules === */
void on_modbus_scan_req_received(const char *request_id,
    uint32_t start_addr, uint32_t end_addr, uint32_t timeout_ms);

#ifdef __cplusplus
}
#endif

#endif /* MSG_HANDLER_H */
