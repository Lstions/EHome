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

/* 远程运维（0x22）的注入契约拆到单独头文件：见 msg_handler_device_op.h
 * 的说明（本头 include 了整棵组件树，而那份契约只需要三个函数指针）。 */
#include "msg_handler_device_op.h"

/* === Init === */
void msg_handler_init(void);
void msg_handler_deinit(void);

/* === HelloAck state === */
bool msg_handler_is_hello_ack_received(void);
uint64_t msg_handler_get_server_time(void);
uint64_t msg_handler_get_current_server_time_ms(void);
void msg_handler_reset_hello_ack(void);

/* === Message processing === */
void msg_handler_process_with_transport(const uint8_t *data, size_t len, transport_t *transport);
void msg_handler_process(const uint8_t *data, size_t len);

/* === Send outgoing messages === */
void msg_handler_send_hello(const char *node_id, const char *fw_version,
                            const char *model, uint8_t channel_count,
                            uint32_t handshake_nonce);
esp_err_t msg_handler_send_status(uint32_t uptime_sec, const char *status,
                             uint8_t channel_count, const scheduler_state_t *sched);
void msg_handler_send_data_report(uint32_t channel_id, uint64_t timestamp_us,
                                  uint32_t sequence, const uint8_t *raw_data, size_t raw_len,
                                  uint32_t error_code, uint32_t request_id,
                                  uint32_t edge_device_id, uint32_t command_template_id,
                                  uint8_t command_index);
/* V3-2a DataBatch (0x20)：把一批非关键样本编码成单帧并发布。
 * 返回 false 表示编码失败（调用方退回逐样本 0x03）。签名与
 * bus_worker.h 的 data_batch_cb_t 一致，以便直接注入。 */
bool msg_handler_send_data_batch(uint32_t channel_id, uint32_t first_sequence,
                                 const uint64_t *timestamps_us,
                                 const uint8_t *const *raw_data,
                                 const size_t *raw_lens,
                                 size_t count,
                                 uint32_t edge_device_id,
                                 uint32_t command_template_id,
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

/* === v2.6 crash diagnostics === */

/**
 * 服务端确认回调签名：record_id + accepted。
 * 实现方（main/crash_diag.c）在 accepted=true 时删除对应 NVS 记录。
 */
typedef void (*diag_ack_cb_t)(uint32_t record_id, bool accepted);

/** 注入崩溃记录确认回调（DIP：避免 msg_handler -> main 的组件环）。 */
void msg_handler_set_diag_ack_cb(diag_ack_cb_t cb);

/** 读取已注入的回调；未注入时返回 NULL。 */
diag_ack_cb_t msg_handler_get_diag_ack_cb(void);

void handler_diag_process_ack(frame_decoder_t *dec);

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
