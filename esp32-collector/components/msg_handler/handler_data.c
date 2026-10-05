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
#include "data_report_codec.h"
#include "data_batch_codec.h"
#include "config_mgr.h"
#include "sync_manager.h"
#include "scheduler.h"
#include "bus_worker.h"
#include "ota.h"
#include "wifi_mgr.h"
#include "esp_log.h"
#include "esp_system.h"
#include "esp_heap_caps.h"
#include <stdbool.h>
#include <stddef.h>
#include <string.h>
#include <stdlib.h>

#define TAG "DATA_H"

/* === WS-E: OTA start memory gate ===
 *
 * Why here and not in ota.c: ota_start() runs in a dedicated task, but the
 * decision to accept an OTA is observable at command admission.  Refusing at
 * admission gives the server a deterministic error instead of an OTA that
 * starts and dies mid-download from heap fragmentation — the 2026-10-01 field
 * failure was exactly that ("Allocation failed" after the HTTP client had
 * already been created).
 *
 * The water-level predicate lives in main/mem_guard.c (WS-G).  A component
 * cannot include a header from main/ (dependency direction: main REQUIRES
 * msg_handler, not the reverse), so this hook follows the same weak-symbol DIP
 * pattern the file already uses for msg_handler_publish_checked and
 * on_query_resources_received:
 *   - msg_handler links the weak default (allow) so host tests build;
 *   - main/app_callbacks.c provides the strong definition backed by
 *     mem_guard_can_start(), the single gate implementation for all callers.
 *
 * 4096 is OTA_TASK_STACK_BYTES (components/ota/ota.c:78; private to ota.c) and
 * 4096 covers the HTTP client's contiguous rx/tx buffers (ota.c configures
 * 1024/512) plus esp_ota state.  Keep the two numbers in sync if either moves.
 */
#define OTA_CMD_MEM_NEED_BYTES (4096u + 4096u)

__attribute__((weak)) bool ehome_mem_can_start(size_t need_bytes)
{
    /* Host tests and any build without the main/ implementation: do not gate. */
    (void)need_bytes;
    return true;
}

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
    uint32_t seen = 0;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        if (field.field_num < OTA_CMD_F_OTA_ID || field.field_num > OTA_CMD_F_SEQUENCE ||
            (seen & (1U << field.field_num))) goto reject;
        seen |= 1U << field.field_num;
        switch (field.field_num) {
        case OTA_CMD_F_OTA_ID:
            if (field.wire_type != WIRE_LENGTH_DELIMITED || field.value.bytes.len == 0 || field.value.bytes.len >= sizeof(cmd->ota_id)) goto reject;
            if (frame_field_get_string(&field, cmd->ota_id, sizeof(cmd->ota_id)) != FRAME_OK) goto reject;
            break;
        case OTA_CMD_F_URL:
            if (field.wire_type != WIRE_LENGTH_DELIMITED || field.value.bytes.len == 0 || field.value.bytes.len >= sizeof(cmd->firmware_url)) goto reject;
            if (frame_field_get_string(&field, cmd->firmware_url, sizeof(cmd->firmware_url)) != FRAME_OK) goto reject;
            break;
        case OTA_CMD_F_CHECKSUM:
            if (field.wire_type != WIRE_LENGTH_DELIMITED || field.value.bytes.len == 0 || field.value.bytes.len >= sizeof(cmd->checksum)) goto reject;
            if (frame_field_get_string(&field, cmd->checksum, sizeof(cmd->checksum)) != FRAME_OK) goto reject;
            break;
        case OTA_CMD_F_SIZE:
            if (field.wire_type != WIRE_VARINT) goto reject;
            if (frame_field_get_varint(&field, &cmd->size_bytes) != FRAME_OK) goto reject;
            break;
        case OTA_CMD_F_VERSION:
            if (field.wire_type != WIRE_LENGTH_DELIMITED || field.value.bytes.len == 0 || field.value.bytes.len >= sizeof(cmd->version)) goto reject;
            if (frame_field_get_string(&field, cmd->version, sizeof(cmd->version)) != FRAME_OK) goto reject;
            break;
        case OTA_CMD_F_SEQUENCE:
            if (field.wire_type != WIRE_VARINT || field.value.varint == 0 || field.value.varint > UINT32_MAX) goto reject;
			cmd->sequence = (uint32_t)field.value.varint;
            break;
        }
    }
    if (err != FRAME_DONE || !cmd->ota_id[0] || !cmd->firmware_url[0] ||
        !cmd->checksum[0] || cmd->size_bytes == 0 || !cmd->version[0] || cmd->sequence == 0) goto reject;
    
    ESP_LOGI(TAG, "OtaCmd: id=%s, url=%s, size=%llu", 
             cmd->ota_id, cmd->firmware_url, (unsigned long long)cmd->size_bytes);
    
    ota_cmd_class_t cmd_class = ota_classify_cmd(cmd);
    if (cmd_class == OTA_CMD_EXACT_REPLAY) {
        ESP_LOGW(TAG, "OTA duplicate replayed: %s", cmd->ota_id);
        ota_replay_last_progress(cmd->ota_id);
        free(cmd);
        return;
    }
    if (cmd_class == OTA_CMD_COLLISION || cmd_class == OTA_CMD_BUSY) {
        ESP_LOGW(TAG, "OTA command rejected: id=%s class=%d", cmd->ota_id, (int)cmd_class);
        free(cmd);
        return;
    }
    /* WS-E water gate: an OTA that cannot fit is refused at admission, with
     * an explicit log, instead of failing later mid-download.  The command is
     * released and the server sees no OtaProg (its documented retry path). */
    if (!ehome_mem_can_start(OTA_CMD_MEM_NEED_BYTES)) {
        ESP_LOGE(TAG, "[memgate] phase=admission step=ota need=%u rc=REJECT",
                 (unsigned)OTA_CMD_MEM_NEED_BYTES);
        goto reject;
    }
    char ota_id_copy[sizeof(cmd->ota_id)];
    memcpy(ota_id_copy, cmd->ota_id, sizeof(ota_id_copy));
    ota_id_copy[sizeof(ota_id_copy) - 1] = '\0';
    if (ota_start(cmd) != ESP_OK) ota_forget_duplicate(ota_id_copy);
    return;
reject:
    ESP_LOGW(TAG, "Rejecting malformed OtaCmd");
    free(cmd);
}

/* === Send: StatusReport (0x02) === */

esp_err_t msg_handler_send_status(uint32_t uptime_sec, const char *status,
                             uint8_t channel_count, const scheduler_state_t *sched)
{
    /* DataReport payloads are produced by the bus-worker fixed block pool
     * (currently 1024 bytes).  Keep wire framing headroom for routing fields
     * so a full block does not fail the encode at the old 512-byte limit. */
    uint8_t buf[1400];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_STATUS_RPT);
    frame_encode_varint(&enc, 1, uptime_sec);
    frame_encode_string(&enc, 2, status);
    frame_encode_varint(&enc, 3, channel_count);
    frame_encode_varint(&enc, 4, config_mgr_get_epoch());
    frame_encode_varint(&enc, 5, (uint64_t)sync_manager_get_state_enum());

    // v2.2: send config_hash so server can decide without waiting for Hello
    const char *config_hash = config_mgr_get_manifest_id();
    if (config_hash != NULL) {
        frame_encode_string(&enc, 6, config_hash);
    }
    const config_manifest_t *active_cfg = config_mgr_get_manifest();
    if (active_cfg && active_cfg->sync_id[0]) frame_encode_string(&enc, 8, active_cfg->sync_id);

    // v2.3: field 7 — channel_health (repeated nested) → edge_device_health
    if (sched != NULL) {
        for (int ci = 0; ci < sched->channel_count; ci++) {
            const sched_channel_t *ch = &sched->channels[ci];
            if (!ch->active || ch->edge_device_count == 0) continue;

            // Build ChannelHealth sub-frame
            uint8_t ch_buf[512];
            frame_encoder_t ch_enc;
            frame_encoder_init_sub(&ch_enc, ch_buf, sizeof(ch_buf));
            if (frame_encode_varint(&ch_enc, 1, ch->config.id) != FRAME_OK) {
                continue;
            }
            size_t unhealthy_count = 0;

            for (int ed = 0; ed < ch->edge_device_count; ed++) {
                const sched_edge_device_t *dev = &ch->edge_devices[ed];
                for (int ci2 = 0; ci2 < dev->command_count; ci2++) {
                    const sched_command_t *cmd = &dev->commands[ci2];
                    /* Disabled commands are intentionally inactive, not a
                     * communication failure. Only report enabled commands
                     * with an observed error.
                     *
                     * NOTE (2026-09-30, defect 2): cmd->error_count is THE
                     * counter the server reads.  It is written only by
                     * scheduler_notify_command_outcome() from the rx_task
                     * outcome paths; a TX enqueue must never clear it.  Keep
                     * that invariant or a silent sensor disappears from this
                     * frame again (the 7-day outage). */
                    if (!cmd->enabled || cmd->error_count == 0) continue;

                    // Build EdgeDeviceHealth sub-frame
                    uint8_t ed_buf[64];
                    frame_encoder_t ed_enc;
                    frame_encoder_init_sub(&ed_enc, ed_buf, sizeof(ed_buf));
                    if (frame_encode_varint(&ed_enc, 1, dev->edge_device_id) != FRAME_OK ||
                        frame_encode_varint(&ed_enc, 2, ci2) != FRAME_OK ||
                        frame_encode_varint(&ed_enc, 3, cmd->error_count) != FRAME_OK) {
                        continue;
                    }
                    // comm_status: 0=OK, 1=TIMEOUT, 2=CRC_ERROR, 3=FAULT
                    uint64_t comm_st = cmd->error_count >= 3 ? 3 :
                                       (cmd->error_count > 0  ? 1 : 0);
                    if (frame_encode_varint(&ed_enc, 4, comm_st) != FRAME_OK) {
                        continue;
                    }

                    if (frame_encode_bytes(&ch_enc, 2,
                                           frame_encoder_data(&ed_enc),
                                           frame_encoder_size(&ed_enc)) == FRAME_OK) {
                        unhealthy_count++;
                    }
                }
            }

            // Only emit ChannelHealth if it has content beyond channel_id
            if (unhealthy_count > 0) {
                if (frame_encode_bytes(&enc, 7,
                                       frame_encoder_data(&ch_enc),
                                       frame_encoder_size(&ch_enc)) != FRAME_OK) {
                    ESP_LOGW(TAG, "StatusReport channel health truncated");
                    break;
                }
            }
        }
    }

    /* Field 9 is a bounded, transport-only performance snapshot.  It is
     * intentionally aggregate-only: no task names, queues or configuration
     * details are exposed to the control plane. */
    scheduler_performance_t perf = {0};
    scheduler_get_performance(&perf);
    uint8_t perf_buf[192];
    frame_encoder_t perf_enc;
    frame_encoder_init_sub(&perf_enc, perf_buf, sizeof(perf_buf));

    /* 字段 1/2 是"空闲堆 / 历史最小空闲堆"。**必须取内部 RAM 口径**：
     * esp_get_free_heap_size() 是 heap_caps_get_free_size(MALLOC_CAP_DEFAULT) 的封装
     * （esp_system_chip.c:65），开了 CONFIG_SPIRAM_USE_MALLOC 后把 PSRAM 也算进去。
     * 实测 s3p-n16 上它上报 8.3 MB —— 服务端与告警看到的 s3p 节点"永远充裕"，
     * 而真正会耗尽的是内部 RAM（任务栈/DMA/OTA 缓冲必须内部）。这与
     * main/mem_guard.c 2026-10-05 修的是同一个口径缺陷（缺陷报告 §7）。
     *
     * 宿主测试仍走 esp_get_* 桩：那两份测试文件对 esp_get_* 的返回类型不统一
     * （既有 uint32_t 也有 size_t，见 host_tests/stubs/esp_heap_caps.h 的说明），
     * 而 heap_caps_* 在宿主侧没有为这两个 target 提供实现。caps 正确性由
     * host_tests/mem_guard_tests.c 的口径断言覆盖。 */
#ifdef ESP_PLATFORM
    const uint32_t perf_heap_free = heap_caps_get_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT);
    const uint32_t perf_heap_min  = heap_caps_get_minimum_free_size(MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT);
#else
    const uint32_t perf_heap_free = (uint32_t)esp_get_free_heap_size();
    const uint32_t perf_heap_min  = (uint32_t)esp_get_minimum_free_heap_size();
#endif

    bool perf_ok = frame_encode_varint(&perf_enc, 1, perf_heap_free) == FRAME_OK &&
        frame_encode_varint(&perf_enc, 2, perf_heap_min) == FRAME_OK &&
        frame_encode_varint(&perf_enc, 3, perf.stack_high_water_words) == FRAME_OK &&
        frame_encode_varint(&perf_enc, 4, bus_worker_get_min_stack_watermark()) == FRAME_OK &&
        frame_encode_varint(&perf_enc, 5, perf.min_queue_spaces) == FRAME_OK &&
        frame_encode_varint(&perf_enc, 6, bus_worker_get_report_drop_count()) == FRAME_OK &&
        frame_encode_varint(&perf_enc, 7, bus_worker_get_report_queue_high_water()) == FRAME_OK;
    scheduler_queue_metrics_t queue_metrics = {0};
    scheduler_get_queue_metrics(&queue_metrics);
    for (uint8_t i = 0; perf_ok && i < SCHED_QUEUE_METRIC_COUNT; i++) {
        perf_ok = frame_encode_varint(&perf_enc, 8 + i,
                                      queue_metrics.current_spaces[i]) == FRAME_OK &&
                  frame_encode_varint(&perf_enc, 13 + i,
                                      queue_metrics.high_water_used[i]) == FRAME_OK &&
                  frame_encode_varint(&perf_enc, 18 + i,
                                      queue_metrics.sample_skipped[i]) == FRAME_OK &&
                  frame_encode_varint(&perf_enc, 23 + i,
                                      queue_metrics.sample_rejected[i]) == FRAME_OK;
    }
    /* Field 28: WiFi RSSI.  Transport carries the absolute value of the
     * negative dBm reading (e.g. -55 dBm -> 55); the backend negates it.
     * 0 means "no WiFi data" (disconnected or query failed). */
    if (perf_ok) {
        int rssi_dbm = wifi_mgr_get_rssi_dbm();
        uint64_t rssi_abs = (rssi_dbm < 0) ? (uint64_t)(-rssi_dbm) : 0;
        perf_ok = frame_encode_varint(&perf_enc, 28, rssi_abs) == FRAME_OK;
    }
    if (perf_ok) {
        (void)frame_encode_bytes(&enc, STATUS_RPT_F_RUNTIME_PERF,
                                 frame_encoder_data(&perf_enc), frame_encoder_size(&perf_enc));
    }

    channel_cmd_v2_metrics_t control = {0};
    handler_channel_cmd_v2_get_metrics(&control);
    uint8_t control_buf[48];
    frame_encoder_t control_enc;
    frame_encoder_init_sub(&control_enc, control_buf, sizeof(control_buf));
    if (frame_encode_varint(&control_enc, 1, control.accepted) == FRAME_OK &&
        frame_encode_varint(&control_enc, 2, control.rejected) == FRAME_OK &&
        frame_encode_varint(&control_enc, 3, control.completed) == FRAME_OK &&
        frame_encode_varint(&control_enc, 4, control.replayed) == FRAME_OK) {
        (void)frame_encode_bytes(&enc, STATUS_RPT_F_CONTROL_STATS,
                                 frame_encoder_data(&control_enc), frame_encoder_size(&control_enc));
    }

    ESP_LOGD(TAG, "Sending StatusReport: %lu sec, %s, %d ch, epoch=%llu, sync_state=%d, hash=%s",
             (unsigned long)uptime_sec, status, channel_count,
             (unsigned long long)config_mgr_get_epoch(),
             sync_manager_get_state_enum(),
             config_hash ? config_hash : "(none)");
    return msg_handler_publish_checked(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

/* === Send: DataReport (0x03) === */

void msg_handler_send_data_report(uint32_t channel_id, uint64_t timestamp_us,
                                  uint32_t sequence, const uint8_t *raw_data, size_t raw_len,
                                  uint32_t error_code, uint32_t request_id,
                                  uint32_t edge_device_id, uint32_t command_template_id,
                                  uint8_t command_index)
{
    /* Keep this buffer at least as large as the event-driven payload block.
     * A 512-byte stack buffer cannot carry a full 512-byte chunk once the
     * message type, field tags, length prefix and routing metadata are
     * added, so full chunks would be silently rejected by the codec. */
    uint8_t buf[1400];
    size_t len = 0;
    frame_err_t err = data_report_encode(buf, sizeof(buf), &len,
                                         channel_id, timestamp_us, sequence,
                                         raw_data, raw_len, error_code, request_id,
                                         edge_device_id, command_template_id,
                                         command_index);
    if (err != FRAME_OK) {
        ESP_LOGE(TAG, "DataReport encode failed: %d", err);
        return;
    }
    ESP_LOGD(TAG, "Sending DataReport: ch=%lu, seq=%lu, len=%zu, edge=%lu, cmd_idx=%u",
             (unsigned long)channel_id, (unsigned long)sequence, raw_len,
             (unsigned long)edge_device_id, command_index);
    msg_handler_publish(buf, len);
}

/* === Send: DataBatch (0x20) — V3-2a（契约 §2/§3） === */

bool msg_handler_send_data_batch(uint32_t channel_id, uint32_t first_sequence,
                                 const uint64_t *timestamps_us,
                                 const uint8_t *const *raw_data,
                                 const size_t *raw_lens,
                                 size_t count,
                                 uint32_t edge_device_id,
                                 uint32_t command_template_id,
                                 uint8_t command_index)
{
    if (timestamps_us == NULL || raw_data == NULL || raw_lens == NULL) return false;
    if (count < 2 || count > DATA_BATCH_MAX_SAMPLES) return false;

    /* 契约 §2.2 的 1,400 B 编码缓冲。放在**本函数**而不是 bus_worker：
     * bus_worker 的组件门禁是 -Wframe-larger-than=1024，而 report_tx 的
     * 4096 B 栈还要同时容纳 send_data_report 的 2416 B 帧；把 1400 B 再叠
     * 进去会顶穿。这里的栈深度与 send_data_report 同量级（批路径与单样本
     * 路径互斥，不会叠加），并且复用 0x03 路径已经验证过的 1400 B 预算。 */
    uint8_t buf[1400];
    data_batch_sample_t samples[DATA_BATCH_MAX_SAMPLES];
    for (size_t i = 0; i < count; i++) {
        if (timestamps_us[i] < timestamps_us[0]) return false;
        samples[i].delta_us = timestamps_us[i] - timestamps_us[0];
        samples[i].raw_data = raw_data[i];
        samples[i].raw_len = raw_lens[i];
    }

    size_t len = 0;
    frame_err_t err = data_batch_encode(buf, sizeof(buf), &len,
                                        channel_id, timestamps_us[0], first_sequence,
                                        samples, count,
                                        edge_device_id, command_template_id,
                                        command_index);
    if (err != FRAME_OK) {
        /* 契约 §2.2：不得截断。调用方（bus_worker）据此退回逐样本 0x03。 */
        ESP_LOGW(TAG, "DataBatch encode failed: %d (n=%u)", (int)err, (unsigned)count);
        return false;
    }
    ESP_LOGD(TAG, "Sending DataBatch: ch=%lu, first_seq=%lu, n=%u, bytes=%zu",
             (unsigned long)channel_id, (unsigned long)first_sequence,
             (unsigned)count, len);
    msg_handler_publish(buf, len);
    return true;
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
