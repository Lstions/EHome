/**
 * @file handler_diag.c
 * @brief MSG_DIAG_ACK (0x1F) 处理：服务端确认后释放设备端 NVS 占用
 *
 * 契约（与 main/crash_diag.c 一一对应）：
 *   field 1 = record_id (varint)
 *   field 2 = accepted  (varint bool)
 *
 * accepted=true  -> 删除对应 NVS 记录，释放占用
 * accepted=false -> **保留**记录，等下次上线重试（服务端明说没存下）
 *
 * 为什么用注入回调而不是直接调 crash_diag_on_ack()
 * -------------------------------------------------
 * 本文件属于 msg_handler 组件，而 crash_diag 实现在 main 组件里；msg_handler
 * 反过来被 main 依赖，直接 include 会形成组件环。沿用本仓库既有的 DIP 模式
 * （见 bus_manager_set_write_rsp_cb / ota_set_progress_callback 的用法）：
 * main 在启动时注入 crash_diag_on_ack，这里只调用函数指针。
 * 回调未注入时仅记警告，不崩溃、不误删。
 */

#include <stdint.h>

#include "esp_log.h"

#include "frame_codec.h"
#include "msg_handler.h"
#include "msg_handler_internal.h"

static const char *TAG = "DIAG_ACK";

void handler_diag_process_ack(frame_decoder_t *dec)
{
    uint64_t record_id = 0;
    bool accepted = false;
    bool have_id = false;
    bool have_accepted = false;

    frame_field_t field;
    while (frame_decoder_next(dec, &field) == FRAME_OK) {
        switch (field.field_num) {
        case 1:
            record_id = field.value.varint;
            have_id = true;
            break;
        case 2:
            accepted = (field.value.varint != 0);
            have_accepted = true;
            break;
        default:
            break;
        }
    }

    /* 畸形帧：宁可不释放，也不能把证据误删。 */
    if (!have_id || !have_accepted) {
        ESP_LOGW(TAG, "malformed MSG_DIAG_ACK (have_id=%d have_accepted=%d) — ignoring",
                 (int)have_id, (int)have_accepted);
        return;
    }
    if (record_id > 0xFFFFFFFFULL) {
        ESP_LOGW(TAG, "MSG_DIAG_ACK record_id out of range: %llu",
                 (unsigned long long)record_id);
        return;
    }

    ESP_LOGI(TAG, "server ack: record_id=%08X accepted=%d",
             (unsigned)record_id, (int)accepted);

    diag_ack_cb_t cb = msg_handler_get_diag_ack_cb();
    if (cb == NULL) {
        ESP_LOGW(TAG, "no diag ack callback registered — record not released");
        return;
    }
    cb((uint32_t)record_id, accepted);
}
