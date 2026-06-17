/**
 * @file hello_handshake.c
 * @brief Event-driven Hello handshake — replaces polling busy-wait with EventGroup.
 *
 * Flow:
 *   1. MQTT connected → hello_handshake_start()
 *   2. Task sends Hello, waits on EventGroup with short poll interval
 *   3. Each poll tick checks msg_handler_is_hello_ack_received()
 *   4. On success: send ResourceReport, apply persisted config, start scheduler
 *
 * No modification to msg_handler.c required — polls the existing flag.
 */

#include "hello_handshake.h"
#include "msg_handler.h"
#include "config_mgr.h"
#include "scheduler.h"
#include "bus_manager.h"
#include "rgb_led.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "freertos/task.h"
#include "freertos/event_groups.h"

#define TAG "HELLO"

#define HELLO_MAX_RETRIES         3
#define HELLO_TIMEOUT_MS          10000
#define HELLO_POLL_INTERVAL_MS    500

/* Event bits */
#define EVT_HELLO_ACK     (1 << 0)
#define EVT_TIMEOUT       (1 << 1)
#define EVT_STOP          (1 << 2)

static EventGroupHandle_t s_hello_evt;
static app_state_t       *s_state_ref;

/* ---- Task body ---- */

static void hello_task(void *pv)
{
    (void)pv;
    app_state_t *s = s_state_ref;
    bool ok = false;

    /* First hello immediately */
    msg_handler_send_hello(s->node_id, get_firmware_version(), get_model_name(),
                           config_mgr_get_active_channel_count());

    for (int attempt = 0; attempt < HELLO_MAX_RETRIES && !ok; attempt++) {
        uint32_t elapsed = 0;

        while (elapsed < HELLO_TIMEOUT_MS) {
            /* Poll the msg_handler flag */
            if (msg_handler_is_hello_ack_received()) {
                ok = true;
                break;
            }

            /* Wait with short timeout for event signal */
            EventBits_t bits = xEventGroupWaitBits(
                s_hello_evt, EVT_STOP,
                pdTRUE, pdFALSE,
                pdMS_TO_TICKS(HELLO_POLL_INTERVAL_MS));

            if (bits & EVT_STOP) break;

            elapsed += HELLO_POLL_INTERVAL_MS;
        }

        if (ok || (elapsed >= HELLO_TIMEOUT_MS && attempt + 1 >= HELLO_MAX_RETRIES)) {
            break;
        }

        /* Timeout — retry */
        ESP_LOGW(TAG, "Hello timeout (attempt %d/%d)", attempt + 1, HELLO_MAX_RETRIES);
        rgb_led_set_state(LED_STATE_MQTT_CONNECTING);

        if (attempt + 1 < HELLO_MAX_RETRIES) {
            msg_handler_send_hello(s->node_id, get_firmware_version(),
                                   get_model_name(),
                                   config_mgr_get_active_channel_count());
        }
    }

    if (!ok) {
        ESP_LOGE(TAG, "Hello failed after %d retries", HELLO_MAX_RETRIES);
        rgb_led_set_state(LED_STATE_MQTT_FAILED);
    } else {
        rgb_led_set_state(LED_STATE_RUNNING);

        /* v2.4: Send ResourceReport after successful handshake */
        msg_handler_send_resource_report();

        /* Apply persisted config if needed */
        if (!scheduler_is_running() && config_mgr_has_manifest()) {
            config_mgr_load_from_nvs();
            bus_manager_setup_from_manifest(s);
            scheduler_start(s->cmd_queue);
        }
    }

    s->hello_task_running = false;
    vEventGroupDelete(s_hello_evt);
    s_hello_evt = NULL;
    s_state_ref = NULL;
    vTaskDelete(NULL);
}

/* ---- Public API ---- */

void hello_handshake_start(app_state_t *state)
{
    if (state->hello_task_running) return;

    s_state_ref = state;
    s_hello_evt = xEventGroupCreate();

    state->hello_task_running = true;
    xTaskCreate(hello_task, "hello", 4096, NULL, 5, NULL);
}

void hello_handshake_notify_ack(void)
{
    /* Kept for future direct integration with msg_handler.
     * Currently unused — hello_task polls msg_handler_is_hello_ack_received(). */
}

bool hello_handshake_is_running(void)
{
    return s_state_ref && s_state_ref->hello_task_running;
}
