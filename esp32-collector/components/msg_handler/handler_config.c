/**
 * @file handler_config.c
 * @brief ConfigManifest/ConfigQuery/QueryResources message handler
 *
 * Receives: MSG_CONFIG_MFST (0x04), MSG_CONFIG_QUERY (0x10), MSG_QUERY_RESOURCES (0x1A)
 * Sends:    ConfigResult (0x05), ConfigReport (0x11), ResourceReport (0x19)
 */

#include "msg_handler.h"
#include "msg_handler_hooks.h"   /* B2：钩子的唯一声明处（禁止弱符号）*/
#include "msg_handler_internal.h"
#include "frame_codec.h"
#include "config_mgr.h"
#include "sync_manager.h"
#include "hw_profile.h"
#include "dma_pool.h"
#include "esp_log.h"
#include "esp_heap_caps.h"
#include <string.h>
#include <stdlib.h>

#define TAG "CFG_H"

/* dma_pool injected by msg_handler core */
extern dma_pool_t *msg_handler_get_dma_pool(void);

/* === Receive: ConfigManifest (0x04) === */

void handler_config_process_manifest(frame_decoder_t *dec)
{
    /* v2.4: ConfigManifest application moved to handle_config_applied
     * (app_callbacks.c) inside app_state_lock_config().
     * This function is now a no-op — msg_handler_process still dispatches
     * MSG_CONFIG_MFST here for protocol routing, but the actual manifest
     * parse, ConfigResult, and bus rebuild all happen in handle_config_applied. */
    (void)dec;
}

/* === Receive: ConfigQuery (0x10) === */

void handler_config_process_query(frame_decoder_t *dec)
{
    char request_id[64] = {0};
    frame_err_t err;
    frame_field_t field;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        if (field.field_num == 1) frame_field_get_string(&field, request_id, sizeof(request_id));
    }
    ESP_LOGI(TAG, "ConfigQuery: req=%s", request_id);
    msg_handler_send_config_report(request_id);
}

/* === Receive: QueryResources (0x1A) === */

/* B2（2026-10-06）：原为 weak 空实现。已删除 —— 声明见
 * msg_handler_hooks.h，强实现在 main.c。 */

void handler_config_process_query_resources(frame_decoder_t *dec)
{
    char request_id[64] = {0};
    frame_err_t err;
    frame_field_t field;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        if (field.field_num == 1) frame_field_get_string(&field, request_id, sizeof(request_id));
    }
    ESP_LOGI(TAG, "QueryResources: req=%s", request_id);
    on_query_resources_received(request_id);
    msg_handler_send_resource_report();
}

/* === Send: ConfigResult (0x05) === */

esp_err_t msg_handler_send_config_result(const char *manifest_id, const char *sync_id, bool success)
{
    /* ⚠ 128 B 原够用；新增 field 5（降级通道，最多 MAX_CHANNELS 个 varint）
     * 后按最坏情形（8 通道 × 2 B + 头部）放宽到 192 B。
     * ⚠ 本帧走 3.0 TCP 链路，不受 MQTT 单事件上限约束。 */
    uint8_t buf[192];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_CONFIG_RSLT);
    frame_encode_string(&enc, 1, manifest_id);
    frame_encode_varint(&enc, 2, success ? 1 : 0);
	frame_encode_string(&enc, 4, sync_id ? sync_id : "");

    /* ⚠ 2026-10-09（用户要求："DMA 分不到降级为提示"）：
     * field 5 = 因 DMA 不可用而降级为 polled 的通道 id（repeated varint）。
     *
     * 为什么走 ConfigResult 而不是 ResourceReport：
     *   降级是**这一次配置应用**的结果，与 manifest 一一对应；
     *   ConfigResult 已经携带 manifest_id + sync_id，后端能精确关联到
     *   本次同步并把提示显示在"这次配置"上，不会与历史状态混淆。
     *
     * ⚠ 只在**成功**时上报：失败路径后端已经会看到 success=false 与失败原因，
     *   再叠一条"降级"提示会误导（降级本身不是失败）。
     * ⚠ 未降级时不编这个字段（而不是编空数组）：后端按"字段缺失=无降级"处理，
     *   省字节也避免"空数组"与"字段缺失"两种表示同一语义。 */
    int degraded_count = 0;
    if (success) {
        /* ⚠ 走钩子而不是直接调 bus_manager：msg_handler REQUIRES bus_manager
         * 会成环（bus_manager -> bus_worker -> msg_handler）。
         * 实现见 main/app_callbacks.c，声明见 msg_handler_hooks.h。 */
        degraded_count = dma_degraded_channels(NULL, 0);
        if (degraded_count > 0) {
            uint32_t ids[MAX_CHANNELS];
            int n = dma_degraded_channels(ids, (int)MAX_CHANNELS);
            for (int i = 0; i < n; i++) {
                frame_encode_varint(&enc, 5, ids[i]);
            }
        }
    }

    ESP_LOGI(TAG, "Sending ConfigResult: %s, success=%d, dma_degraded=%d",
             manifest_id, success, degraded_count);
    return msg_handler_publish_checked(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

/* === Send: ConfigReport (0x11) === */

void msg_handler_send_config_report(const char *request_id)
{
    uint8_t buf[256];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_CONFIG_REPORT);
    frame_encode_string(&enc, 1, request_id);

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
    msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

/* === Send: ResourceReport (0x19) === */

void msg_handler_send_resource_report(void)
{
    uint8_t *buf = heap_caps_malloc(1024, MALLOC_CAP_DEFAULT);
    if (!buf) {
        ESP_LOGE(TAG, "Failed to allocate ResourceReport buffer");
        return;
    }
    size_t len = 0;

    dma_pool_t *pool = msg_handler_get_dma_pool();
    if (!pool) {
        ESP_LOGW(TAG, "dma_pool not set, sending report without DMA info");
    }

    const config_manifest_t *cfg = config_mgr_get_manifest();
    uint8_t ch_count = cfg ? cfg->channel_count : 0;
    const config_channel_t *ch_ptr = cfg ? cfg->channels : NULL;

    if (!hw_profile_build_report(buf, 1024, &len, pool, ch_ptr, ch_count)) {
        ESP_LOGE(TAG, "Failed to build ResourceReport");
        free(buf);
        return;
    }

    ESP_LOGI(TAG, "Sending ResourceReport: %zu bytes", len);
    msg_handler_publish(buf, len);
    free(buf);
}
