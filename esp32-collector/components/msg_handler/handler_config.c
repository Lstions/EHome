/**
 * @file handler_config.c
 * @brief ConfigManifest/ConfigQuery/QueryResources message handler
 *
 * Receives: MSG_CONFIG_MFST (0x04), MSG_CONFIG_QUERY (0x10), MSG_QUERY_RESOURCES (0x1A)
 * Sends:    ConfigResult (0x05), ConfigReport (0x11), ResourceReport (0x19)
 */

#include "msg_handler.h"
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
    char manifest_id[64] = {0};
    uint64_t server_epoch = 0;
    frame_err_t err;
    frame_field_t field;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        if (field.field_num == 1 && field.wire_type == WIRE_LENGTH_DELIMITED) {
            if (field.value.bytes.ptr) {
                size_t copy_len = field.value.bytes.len < sizeof(manifest_id) - 1
                                ? field.value.bytes.len : sizeof(manifest_id) - 1;
                memcpy(manifest_id, field.value.bytes.ptr, copy_len);
                manifest_id[copy_len] = '\0';
            }
        }
        if (field.field_num == 2 && field.wire_type == WIRE_VARINT) {
            server_epoch = field.value.varint;
        }
    }
    ESP_LOGI(TAG, "ConfigManifest: manifest_id=%s, epoch=%llu",
             manifest_id, (unsigned long long)server_epoch);

    /* We need the raw frame data for config_mgr_apply_manifest.
     * The decoder was initialized from it, so we reconstruct from decoder state. */
    /* Actually, config_mgr_apply_manifest needs the original data+len.
     * We'll pass it through the dispatch layer instead. */
    /* NOTE: The dispatch in msg_handler_process passes raw data for this case. */

    /* This function is called AFTER config_mgr_apply_manifest in the dispatcher.
     * Here we just handle the protocol-level response and sync notifications. */
    msg_handler_send_config_result(manifest_id, true);
    sync_manager_on_config_applied(server_epoch, manifest_id);
    sync_manager_cancel_config_timeout();
    sync_manager_on_downlink_received(MSG_CONFIG_MFST);
}

/* === Receive: ConfigQuery (0x10) === */

void handler_config_process_query(frame_decoder_t *dec)
{
    char request_id[64] = {0};
    frame_err_t err;
    frame_field_t field;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        if (field.field_num == 1 && field.wire_type == WIRE_LENGTH_DELIMITED && field.value.bytes.ptr) {
            memcpy(request_id, field.value.bytes.ptr,
                   field.value.bytes.len < sizeof(request_id)-1 ? field.value.bytes.len : sizeof(request_id)-1);
        }
    }
    ESP_LOGI(TAG, "ConfigQuery: req=%s", request_id);
    msg_handler_send_config_report(request_id);
}

/* === Receive: QueryResources (0x1A) === */

/* Weak callback - implemented in main.c */
__attribute__((weak)) void on_query_resources_received(const char *request_id)
{
    (void)request_id;
}

void handler_config_process_query_resources(frame_decoder_t *dec)
{
    char request_id[64] = {0};
    frame_err_t err;
    frame_field_t field;
    while ((err = frame_decoder_next(dec, &field)) == FRAME_OK) {
        if (field.field_num == 1 && field.wire_type == WIRE_LENGTH_DELIMITED && field.value.bytes.ptr) {
            size_t copy_len = field.value.bytes.len < sizeof(request_id) - 1
                            ? field.value.bytes.len : sizeof(request_id) - 1;
            memcpy(request_id, field.value.bytes.ptr, copy_len);
        }
    }
    ESP_LOGI(TAG, "QueryResources: req=%s", request_id);
    on_query_resources_received(request_id);
    msg_handler_send_resource_report();
}

/* === Send: ConfigResult (0x05) === */

void msg_handler_send_config_result(const char *manifest_id, bool success)
{
    uint8_t buf[128];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_CONFIG_RSLT);
    frame_encode_string(&enc, 1, manifest_id);
    frame_encode_varint(&enc, 2, success ? 1 : 0);
    ESP_LOGI(TAG, "Sending ConfigResult: %s, success=%d", manifest_id, success);
    msg_handler_publish(frame_encoder_data(&enc), frame_encoder_size(&enc));
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
