/**
 * @file app_callbacks.c
 * @brief WiFi / MQTT / Transport state and message callbacks.
 *
 * All callbacks receive app_state_t *ctx for thread-safe access to
 * shared state.  Config-manifest application is protected by mutex.
 *
 * v2.4: Incremental config apply — only rebuild channels whose bus
 * configuration changed (bus_type, bus_config, DMA binding).  Channels
 * with only parameter changes (interval_ms, template_ids) are updated
 * in-place via scheduler without bus teardown.
 */

#include "app_state.h"
#include "app_callbacks.h"
#include "bus_manager.h"
#include "hello_handshake.h"
#include "msg_handler.h"
#include "scheduler.h"
#include "bus_worker.h"
#include "config_mgr.h"
#include "dma_pool.h"
#include "sync_manager.h"
#include "rgb_led.h"
#include "wifi_mgr.h"
#include "ehome_mqtt.h"
#include "transport.h"
#include "log_stream.h"
#include "esp_log.h"
#include "frame_codec.h"
#include <string.h>

#define TAG "CALLBACK"

/* ==== Config Manifest helper ==== */

static bool is_config_manifest(const uint8_t *data, size_t len)
{
    return len > 0 && data[0] == MSG_CONFIG_MFST;
}

/* v2.4: Incremental config apply.
 *
 * Instead of full teardown/rebuild, we compare old vs new manifest
 * and only rebuild channels whose bus-level config changed.
 *
 * "Bus-level" = bus_type, bus_config bytes, or DMA binding.
 * "Param-level" = interval_ms, template_ids, enabled flag.
 *
 * Param-only changes → update scheduler in-place, no bus rebuild.
 * Bus-level changes → unreg old bus, reg new bus.
 * Removed channels → unreg bus, remove from scheduler.
 * New channels → reg bus, add to scheduler.
 */
static void handle_config_applied(app_state_t *s, const uint8_t *data, size_t len)
{
    /* v2.4: Idempotency guard — check BEFORE apply_manifest destroys old config.
     * We peek at field 1 (manifest_id string) from raw frame bytes to compare
     * against last known NVS manifest_id, without calling apply_manifest.
     *
     * Frame format: [type_byte] [field1_tag] [field1_len] [manifest_id_bytes...]
     * Field 1 is tag=0x0A (field_num=1, wire_type=2), followed by varint length
     * then the string bytes. If it matches NVS and scheduler is running → skip. */
    if (len > 4 && data[0] == MSG_CONFIG_MFST) {
        const uint8_t *p = data + 1;
        /* Find field 1: tag byte = (field_num << 3) | wire_type = (1 << 3) | 2 = 0x0A */
        while (p < data + len - 1) {
            uint8_t tag = *p++;
            uint8_t field_num = tag >> 3;
            uint8_t wire_type = tag & 0x07;
            if (field_num == 1 && wire_type == 2) {
                /* Varint length */
                size_t slen = 0;
                int shift = 0;
                while (p < data + len && (*p & 0x80)) {
                    slen |= ((size_t)(*p++ & 0x7F)) << shift;
                    shift += 7;
                }
                if (p < data + len) slen |= ((size_t)(*p++)) << shift;
                if (p + slen <= data + len && slen < 64) {
                    char mid[64];
                    memcpy(mid, p, slen);
                    mid[slen] = '\0';
                    const char *last_id = config_mgr_get_last_known_manifest_id();
                    if (last_id && strcmp(mid, last_id) == 0 && scheduler_is_running()) {
                        ESP_LOGI(TAG, "handle_config_applied: SKIP (same manifest %s, scheduler running)", mid);
                        sync_manager_on_config_applied(0, mid);
                        sync_manager_cancel_config_timeout();
                        sync_manager_on_downlink_received(MSG_CONFIG_MFST);
                        rgb_led_set_state(LED_STATE_RUNNING);
                        return;
                    }
                }
                break;
            }
            if (wire_type == 0) { while (p < data + len && (*p++ & 0x80)); }
            else if (wire_type == 2) {
                size_t vlen = 0; int s = 0;
                while (p < data + len && (*p & 0x80)) { vlen |= ((size_t)(*p++ & 0x7F)) << s; s += 7; }
                if (p < data + len) vlen |= ((size_t)(*p++)) << s;
                p += vlen;
            } else if (wire_type == 5) { p += 4; }
        }
    }

    /* v2.4: Snapshot only channel-level fields needed for diff.
     * config_manifest_t is ~4.3KB — too large for MQTT callback stack. */
    struct {
        uint32_t id;
        bool     enabled;
        uint8_t  bus_type;
        uint8_t  bus_config[128];
        size_t   bus_config_len;
    } old_channels[MAX_CHANNELS];
    uint8_t old_count = 0;

    const config_manifest_t *old_cfg = config_mgr_get_manifest();
    bool had_old = (old_cfg && old_cfg->applied);
    if (had_old) {
        old_count = old_cfg->channel_count;
        if (old_count > MAX_CHANNELS) old_count = MAX_CHANNELS;
        for (int i = 0; i < old_count; i++) {
            old_channels[i].id = old_cfg->channels[i].id;
            old_channels[i].enabled = old_cfg->channels[i].enabled;
            old_channels[i].bus_type = old_cfg->channels[i].bus_type;
            old_channels[i].bus_config_len = old_cfg->channels[i].bus_config_len;
            if (old_cfg->channels[i].bus_config_len <= sizeof(old_channels[i].bus_config)) {
                memcpy(old_channels[i].bus_config,
                       old_cfg->channels[i].bus_config,
                       old_cfg->channels[i].bus_config_len);
            }
        }
    }

    /* Suspend rx_task/cmd_task before cleanup to prevent race */
    bus_worker_suspend();

    /* Mutex — bus teardown/rebuild may block */
    app_state_lock_config();

    /* === Phase 0: Parse new manifest (inside mutex) === */
    config_mgr_apply_manifest(data, len);
    {
        const config_manifest_t *tmp_cfg = config_mgr_get_manifest();
        ESP_LOGI(TAG, "After apply_manifest: channels=%d, ch[0].edge_device_count=%d",
                 tmp_cfg ? tmp_cfg->channel_count : 0,
                 (tmp_cfg && tmp_cfg->channel_count > 0) ? tmp_cfg->channels[0].edge_device_count : -1);
    }

    /* v2.4: Apply DmaChannelConfig to dma_pool.
     * Previously done in parse_manifest() but removed (redundant ALLOCATED→FREE→ALLOCATED).
     * Now applied here inside mutex, before bus rebuild consumes the DMA bindings. */
    if (s->dma_pool) {
        const config_manifest_t *m = config_mgr_get_manifest();
        if (m && m->applied) {
            for (int i = 0; i < m->dma_config_count; i++) {
                const config_dma_channel_t *dc = &m->dma_configs[i];
                dma_pool_apply_config(s->dma_pool, dc->dma_id, dc->enabled, dc->bind_to);
            }
        }
    }

    const config_manifest_t *new_cfg = config_mgr_get_manifest();
    const char *new_id = new_cfg ? new_cfg->manifest_id : NULL;

    /* Idempotency guard — already checked at function entry with raw frame peek.
     * If we reach here, either manifest is new or scheduler is not running. */

    ESP_LOGI(TAG, "handle_config_applied: START (manifest=%s, scheduler_running=%d, had_old=%d)",
             new_id ? new_id : "(null)", scheduler_is_running(), had_old);

    /* === Phase 1: Send ConfigResult + update sync state === */
    msg_handler_send_config_result(new_id ? new_id : "unknown", true);
    sync_manager_on_config_applied(0, new_id ? new_id : "");
    sync_manager_cancel_config_timeout();
    sync_manager_on_downlink_received(MSG_CONFIG_MFST);

    /* === Phase 2: Incremental bus rebuild === */

    if (had_old && scheduler_is_running()) {
        /* v2.4: Pause scheduler (preserves channel state) to prevent data race
         * between scheduler_task reading s_channels[] while we modify it.
         * scheduler_pause() stops the task loop but keeps s_channels[].active intact,
         * so scheduler_update_channel / scheduler_remove_channel can still find channels. */
        scheduler_pause();

        /* --- 2a: Remove channels that no longer exist or are disabled --- */
        for (int i = 0; i < old_count; i++) {
            if (!old_channels[i].enabled) continue;

            bool found = false;
            for (int j = 0; j < new_cfg->channel_count; j++) {
                if (new_cfg->channels[j].id == old_channels[i].id &&
                    new_cfg->channels[j].enabled) {
                    found = true;
                    break;
                }
            }
            if (!found) {
                ESP_LOGI(TAG, "Incremental: remove ch=%lu", (unsigned long)old_channels[i].id);
                bus_manager_unreg_channel(&s->bus_runtime, old_channels[i].id);
                scheduler_remove_channel(old_channels[i].id);
            }
        }

        /* --- 2b: Rebuild channels whose bus-level config changed --- */
        for (int j = 0; j < new_cfg->channel_count; j++) {
            const config_channel_t *new_ch = &new_cfg->channels[j];
            if (!new_ch->enabled) continue;

            /* Find matching old channel */
            int old_idx = -1;
            for (int i = 0; i < old_count; i++) {
                if (old_channels[i].id == new_ch->id && old_channels[i].enabled) {
                    old_idx = i;
                    break;
                }
            }

            if (old_idx < 0) {
                /* New channel — full register */
                ESP_LOGI(TAG, "Incremental: add new ch=%lu", (unsigned long)new_ch->id);
                bus_manager_reg_channel(&s->bus_runtime, new_ch);
                scheduler_add_channel(new_ch);
            } else if (old_channels[old_idx].bus_type != new_ch->bus_type ||
                       old_channels[old_idx].bus_config_len != new_ch->bus_config_len ||
                       memcmp(old_channels[old_idx].bus_config, new_ch->bus_config,
                              new_ch->bus_config_len) != 0) {
                /* Bus-level config changed — rebuild */
                ESP_LOGI(TAG, "Incremental: rebuild ch=%lu (bus config changed)",
                         (unsigned long)new_ch->id);
                bus_manager_unreg_channel(&s->bus_runtime, old_channels[old_idx].id);
                bus_manager_reg_channel(&s->bus_runtime, new_ch);
                scheduler_remove_channel(old_channels[old_idx].id);
                scheduler_add_channel(new_ch);
            } else {
                /* Param-only change — update scheduler in-place */
                ESP_LOGI(TAG, "Incremental: update ch=%lu (params only)",
                         (unsigned long)new_ch->id);
                scheduler_update_channel(new_ch);
            }
        }

        /* v2.4: Resume scheduler after pause — incremental modifications done */
        scheduler_resume(&(scheduler_queues_t){
            .uart0_cmd_queue = s->uart0_cmd_queue,
            .uart1_cmd_queue = s->uart1_cmd_queue,
            .spi_cmd_queue  = s->spi_cmd_queue,
            .i2c_cmd_queue  = s->i2c_cmd_queue,
        });
    } else {
        /* First-time apply or scheduler not running — full setup */
        ESP_LOGI(TAG, "Full setup (first apply or scheduler not running)");
        if (scheduler_is_running()) {
            scheduler_stop();
        }
        bus_manager_cleanup_all(&s->bus_runtime);
        bus_manager_setup_from_manifest(&s->bus_runtime);
        scheduler_start(&(scheduler_queues_t){
            .uart0_cmd_queue = s->uart0_cmd_queue,
            .uart1_cmd_queue = s->uart1_cmd_queue,
            .spi_cmd_queue  = s->spi_cmd_queue,
            .i2c_cmd_queue  = s->i2c_cmd_queue,
        });
    }

    ESP_LOGI(TAG, "Config→scheduler %d ch", scheduler_get_channel_count());

    app_state_unlock_config();

    /* Resume rx_task/cmd_task */
    bus_worker_resume();

    /* v2.5: Apply log_stream config from manifest */
    {
        bool log_en = config_mgr_get_log_stream_enabled();
        uint8_t log_lvl = config_mgr_get_log_stream_level();
        if (log_en && !log_stream_is_active()) {
            ESP_LOGI(TAG, "LogStream: starting (level=%d)", log_lvl);
            log_stream_start(log_lvl);
        } else if (!log_en && log_stream_is_active()) {
            ESP_LOGI(TAG, "LogStream: stopping");
            log_stream_stop();
        } else if (log_en && log_stream_is_active()) {
            ESP_LOGI(TAG, "LogStream: updating level=%d", log_lvl);
            log_stream_set_level(log_lvl);
        }
    }

    rgb_led_set_state(LED_STATE_RUNNING);
    ESP_LOGI(TAG, "handle_config_applied: DONE");
}

/* ==== WiFi callback ==== */

/* === Deferred MQTT start (avoid stack overflow in WiFi event loop) === */

static void mqtt_start_task(void *pv)
{
    ESP_LOGI(TAG, "mqtt_start_task: starting MQTT client...");
    mqtt_client_start();
    ESP_LOGI(TAG, "mqtt_start_task: done, deleting task");
    vTaskDelete(NULL);
}

void on_wifi_state_cb(wifi_mgr_state_t state, void *ctx)
{
    app_state_t *s = (app_state_t *)ctx;
    if (!s) return;

    switch (state) {
    case WIFI_MGR_CONNECTED:
        rgb_led_set_state(LED_STATE_MQTT_CONNECTING);
        if (s->ota_need_confirm) {
            extern void ota_confirm_valid(void);
            ota_confirm_valid();
            s->ota_need_confirm = false;
        }
        /* Defer mqtt_client_start() to a separate task to avoid stack overflow
         * in the WiFi event loop task (which has limited stack). */
        ESP_LOGI(TAG, "WiFi connected, spawning MQTT start task");
        xTaskCreate(mqtt_start_task, "mqtt_start", 8192, NULL, 5, NULL);
        break;

#ifdef CONFIG_DEBUG_TCP_ENABLED
        if (s->tcp_transport && s->tcp_transport->state != TRANSPORT_CONNECTED
            && s->tcp_transport->ops->start) {
            ESP_LOGI(TAG, "Starting TCP transport");
            s->tcp_transport->ops->start(s->tcp_transport);
        }
#endif
        break;

    case WIFI_MGR_CONNECTING:
        rgb_led_set_state(LED_STATE_WIFI_CONNECTING);
        break;

    case WIFI_MGR_FAILED:
        rgb_led_set_state(LED_STATE_WIFI_FAILED);
        break;

    default:
        break;
    }
}

/* ==== Transport message callback ==== */

void on_transport_msg_cb(const uint8_t *data, size_t len, void *ctx)
{
    app_state_t *s = (app_state_t *)ctx;
    if (!s || !data || len == 0) return;

    ESP_LOGI(TAG, "Transport msg: %d bytes", (int)len);

    bool is_cfg = is_config_manifest(data, len);
    ESP_LOGI(TAG, "is_config_manifest: %d, msg_type: 0x%02X", is_cfg, data[0]);

    if (is_cfg) s->config_received = true;

    msg_handler_process_with_transport(data, len, s->tcp_transport);

    if (is_cfg) {
        ESP_LOGI(TAG, "Calling handle_config_applied...");
        handle_config_applied(s, data, len);
    }
}

/* ==== Transport state callback ==== */

void on_transport_state_cb(transport_state_t state, void *ctx)
{
    (void)ctx;
    ESP_LOGI(TAG, "Transport state: %d", state);

    if (state == TRANSPORT_CONNECTED) {
    } else if (state == TRANSPORT_DISCONNECTED || state == TRANSPORT_FAILED) {
        rgb_led_set_state(LED_STATE_MQTT_FAILED);
    }
}

/* ==== MQTT state callback ==== */

void on_mqtt_state_cb(mqtt_client_state_t state, void *ctx)
{
    app_state_t *s = (app_state_t *)ctx;
    if (!s) return;

    if (state == MQTT_CLIENT_CONNECTED) {
        rgb_led_set_state(LED_STATE_MQTT_CONNECTING);
        if (!s->hello_task_running) {
            msg_handler_reset_hello_ack();
            hello_handshake_start(s);
        }
    } else if (state == MQTT_CLIENT_FAILED) {
        rgb_led_set_state(LED_STATE_MQTT_FAILED);
    }
}

/* ==== MQTT message callback ==== */

void on_mqtt_msg_cb(const char *topic, const uint8_t *data, size_t len, void *ctx)
{
    (void)topic;
    app_state_t *s = (app_state_t *)ctx;
    if (!s) return;

    bool is_cfg = is_config_manifest(data, len);
    if (is_cfg) s->config_received = true;

    msg_handler_process(data, len);

    if (is_cfg) handle_config_applied(s, data, len);
}
