/**
 * @file hello_handshake.c
 * @brief Long-lived Hello v2.6 handshake supervisor.
 *
 * FreeRTOS task notifications are wakeups only. The latest MQTT generation,
 * ready state and ACK nonce live in lock-free 32-bit atomics.
 */

#include "hello_handshake.h"
#include "hello_handshake_runtime.h"
#include "hello_handshake_state.h"
#include "msg_handler.h"
#include "config_mgr.h"
#include "sync_manager.h"
#include "rgb_led.h"
#include "esp_log.h"
#include "freertos/task.h"
#include <stddef.h>

#define TAG "HELLO"
#define HELLO_POLL_INTERVAL_MS 500

static TaskHandle_t s_task_handle;
static app_state_t *s_state_ref;
static hello_runtime_t s_runtime = HELLO_RUNTIME_INITIALIZER;
static hello_sm_t s_sm;
static uint32_t s_active_nonce;

static void notify_worker(void)
{
    /* s_task_handle is published before MQTT is initialized. Callbacks only
     * read it after that one-time publication and never delete the task. */
    TaskHandle_t task = s_task_handle;
    if (task != NULL) (void)xTaskNotifyGive(task);
}

static void handle_hello_success(void)
{
    if (config_mgr_has_manifest()) {
        rgb_led_set_state(LED_STATE_RUNNING);
    } else {
        rgb_led_set_state(LED_STATE_WAITING_CONFIG);
    }
    msg_handler_send_resource_report();
    sync_manager_start_config_timeout();
}

static void clear_active_nonce(void)
{
    if (s_active_nonce != 0) {
        hello_runtime_clear_nonce(&s_runtime, s_active_nonce);
        s_active_nonce = 0;
    }
}

static bool process_action(hello_sm_action_t action)
{
    while (action != HELLO_SM_ACTION_NONE) {
        if (action == HELLO_SM_ACTION_SEND_HELLO) {
            clear_active_nonce();
            uint32_t generation = s_sm.generation;
            uint32_t nonce = 0;
            if (!hello_runtime_prepare_send(&s_runtime, generation, &nonce)) {
                if (hello_runtime_current_generation(&s_runtime) != generation) {
                    hello_sm_init(&s_sm);
                }
                return false;
            }

            /* The nonce is armed before publish so a synchronous ACK is safe.
             * Validate the sole MQTT generation again after publish returns. */
            msg_handler_send_hello(
                s_state_ref->node_id,
                get_firmware_version(),
                get_model_name(),
                config_mgr_get_active_channel_count(),
                nonce);
            if (!hello_runtime_finish_send(&s_runtime, generation, nonce)) {
                if (hello_runtime_current_generation(&s_runtime) != generation) {
                    hello_sm_init(&s_sm);
                }
                return false;
            }
            s_active_nonce = nonce;
            hello_sm_on_hello_sent(&s_sm);
            return true;
        }

        if (action == HELLO_SM_ACTION_COMPLETE) {
            clear_active_nonce();
            handle_hello_success();
            return true;
        }

        if (action == HELLO_SM_ACTION_FAILED) {
            clear_active_nonce();
            ESP_LOGE(TAG, "Hello failed after retries; continuing supervisor");
            rgb_led_set_state(LED_STATE_SERVER_OFFLINE);
            action = hello_sm_next_action(&s_sm);
            continue;
        }
    }
    return false;
}

bool hello_handshake_worker_step(uint32_t max_wait_ticks)
{
    /* The new task may run before xTaskCreate returns and the creator publishes
     * s_task_handle. s_state_ref is published before creation and is therefore
     * the worker's readiness gate; it can safely block for a notification. */
    if (s_state_ref == NULL) return false;

    TickType_t wait_ticks = (TickType_t)max_wait_ticks;
    if (hello_sm_is_running(&s_sm)) {
        TickType_t poll = pdMS_TO_TICKS(HELLO_POLL_INTERVAL_MS);
        if (wait_ticks == portMAX_DELAY || wait_ticks > poll) wait_ticks = poll;
    }
    uint32_t notified = ulTaskNotifyTake(pdTRUE, wait_ticks);

    uint32_t current = hello_runtime_current_generation(&s_runtime);
    if (s_sm.generation != 0 && current != s_sm.generation) {
        clear_active_nonce();
        hello_sm_init(&s_sm);
    }

    hello_sm_action_t action = HELLO_SM_ACTION_NONE;
    uint32_t ready = hello_runtime_ready_generation(&s_runtime);
    bool sync_requested = ready != 0 &&
        hello_runtime_take_sync_request(&s_runtime, ready);
    if (ready != 0 && (ready != s_sm.generation || sync_requested)) {
        clear_active_nonce();
        msg_handler_reset_hello_ack();
        action = hello_sm_accept_generation(&s_sm, ready);
        ESP_LOGI(TAG, sync_requested ? "Hello sync generation %lu" :
                                      "Hello generation %lu",
                 (unsigned long)ready);
    } else if (hello_sm_is_running(&s_sm)) {
        bool acked = s_active_nonce != 0 &&
            hello_runtime_consume_ack(&s_runtime, s_sm.generation,
                                      s_active_nonce);
        if (acked) (void)hello_sm_notify_ack(&s_sm);
        /* Notifications never advance retry time. Only the bounded poll
         * timeout is one state-machine tick; an ACK tick completes now. */
        if (acked || notified == 0) action = hello_sm_tick(&s_sm);
    }

    bool acted = process_action(action);
    return notified != 0 || acted;
}

static void hello_supervisor_task(void *pv)
{
    (void)pv;
    for (;;) (void)hello_handshake_worker_step(portMAX_DELAY);
}

void hello_handshake_start(app_state_t *state)
{
    if (state == NULL || s_task_handle != NULL ||
        hello_runtime_creation_failed(&s_runtime)) {
        return;
    }

    s_state_ref = state;
    hello_runtime_init(&s_runtime);
    hello_sm_init(&s_sm);
    s_active_nonce = 0;

    TaskHandle_t created = NULL;
    if (xTaskCreate(hello_supervisor_task, "hello_super", 4096, NULL, 5,
                    &created) != pdPASS || created == NULL) {
        hello_runtime_set_creation_failed(&s_runtime, true);
        state->hello_task_running = false;
        ESP_LOGE(TAG, "Failed to create long-lived Hello supervisor");
        return;
    }

    /* MQTT is initialized only after this function returns, so callback readers
     * cannot observe a partially published handle. The new task itself does
     * not read s_task_handle until entering its first finite worker step. */
    s_task_handle = created;
    state->hello_task_running = true;
}

void hello_handshake_on_transport_connected(uint32_t generation)
{
    hello_runtime_on_transport_connected(&s_runtime, generation);
    notify_worker();
}

void hello_handshake_on_ready(uint32_t generation)
{
    hello_runtime_on_ready(&s_runtime, generation);
    notify_worker();
}

bool hello_handshake_request_sync(void)
{
    if (!hello_runtime_request_sync(&s_runtime)) return false;
    notify_worker();
    return true;
}

bool hello_handshake_notify_ack(uint32_t nonce)
{
    if (!hello_runtime_notify_ack(&s_runtime, nonce)) return false;
    notify_worker();
    return true;
}

bool hello_handshake_is_running(void)
{
    return s_task_handle != NULL;
}

bool hello_handshake_has_failed(void)
{
    return hello_runtime_creation_failed(&s_runtime);
}

uint32_t hello_handshake_debug_armed_nonce(void)
{
    return hello_runtime_armed_nonce(&s_runtime);
}

uint32_t hello_handshake_debug_current_generation(void)
{
    return hello_runtime_current_generation(&s_runtime);
}

#ifdef HELLO_HANDSHAKE_HOST_TEST
void hello_handshake_test_reset(void)
{
    s_task_handle = NULL;
    s_state_ref = NULL;
    hello_runtime_init(&s_runtime);
    hello_sm_init(&s_sm);
    s_active_nonce = 0;
}
#endif
