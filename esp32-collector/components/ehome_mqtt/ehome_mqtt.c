/**
 * @file mqtt_client.c
 * @brief MQTT Client using espressif__mqtt component
 */

#include "ehome_mqtt.h"
#ifndef EHOME_MQTT_HOST_TEST
#include "esp_timer.h"
#else
int64_t esp_timer_get_time(void);
#endif
#include "esp_log.h"
#include "esp_event.h"
#ifndef EHOME_MQTT_HOST_TEST
#include "freertos/task.h"
#else
void vTaskDelay(uint32_t ticks);
#endif
#include <string.h>

#define TAG "MQTT"

enum {
    MQTT_PUBLISH_QOS_NO_ACK = 0,
    MQTT_PUBLISH_QOS_RELIABLE = 1,
    MQTT_FRAME_TYPE_LOG_STREAM = 0x1D,
};

#ifndef CONFIG_MQTT_RECONNECT_MAX
#define CONFIG_MQTT_RECONNECT_MAX 3
#endif

#ifndef CONFIG_MQTT_CONNECTING_TIMEOUT_SEC
#define CONFIG_MQTT_CONNECTING_TIMEOUT_SEC 30
#endif

#ifndef CONFIG_MQTT_SUBSCRIPTION_TIMEOUT_SEC
#define CONFIG_MQTT_SUBSCRIPTION_TIMEOUT_SEC CONFIG_MQTT_CONNECTING_TIMEOUT_SEC
#endif

static int mqtt_publish_qos_for_frame(const uint8_t *data, size_t len)
{
    return data != NULL && len > 0 && data[0] == MQTT_FRAME_TYPE_LOG_STREAM
               ? MQTT_PUBLISH_QOS_NO_ACK
               : MQTT_PUBLISH_QOS_RELIABLE;
}

static mqtt_client_ctx_t s_ctx = {
    .client = NULL,
    .state = MQTT_CLIENT_DISCONNECTED,
    .msg_cb = NULL,
    .msg_cb_ctx = NULL,
    .state_cb = NULL,
    .state_cb_ctx = NULL,
    .ready_cb = NULL,
    .ready_cb_ctx = NULL,
    .transport_cb = NULL,
    .transport_cb_ctx = NULL,
    .owner_wake_cb = NULL,
    .owner_wake_cb_ctx = NULL,
    .mutex = NULL,
    .transport_connected = false,
    .disconnected_event_pending = false,
    .connection_generation = 0,
    .subscription_generation = 0,
    .client_generation = 0,
    .subscription_phase = MQTT_SUB_IDLE,
    .subscription_msg_id = -1,
    .subscription_deadline_us = 0,
};

static SemaphoreHandle_t s_lifecycle_mutex = NULL;
static bool s_init_failed = false;

/* Lifecycle-owner state. These are owned by the single recovery supervisor
 * through mqtt_client_owner_step and helpers it invokes on its status tick. The
 * MQTT event callback (mqtt_event_handler) must NOT touch these fields; it
 * only updates s_ctx state under s_ctx.mutex and lets the owner act on it.
 * All fields are protected by s_lifecycle_mutex to guarantee safe access
 * even if mqtt_start_task and the status task race. */
static bool s_shutdown_requested = false;   /* lifecycle: blocks new operations during retire */
static bool s_start_requested = false;    /* adapter request; supervisor is sole consumer */
static bool s_stop_requested = false;     /* adapter request; supervisor is sole consumer */
static bool s_client_starting = false;       /* lifecycle: create_and_start_client in flight */
static bool s_retire_in_progress = false;    /* lifecycle: includes unlocked ESP stop/destroy */
static unsigned s_active_operations = 0;     /* lifecycle: inflight ESP-MQTT API calls */
static uint32_t s_next_client_generation = 0;/* lifecycle: monotonic client id */
static int s_reconnect_attempts = 0;         /* owner: reconnect tries before recreate */
static int64_t s_reconnect_deadline_us = 0;  /* owner: earliest next recovery action */
static int64_t s_connecting_start_us = 0;    /* owner: CONNECTING deadline origin */
static bool s_connecting_active = false;     /* owner: a CONNECTING wait is in progress */
/* Owner-only continuation after subscription timeout exhaustion. It remains
 * armed when bounded retire cannot drain operations, preventing the next tick
 * from falling through to ordinary reconnect with a known-dead generation. */
static bool s_subscription_retire_pending = false;

static char s_node_id[32] = {0};
static char s_up_topic[64] = {0};
static char s_down_topic[64] = {0};
static char s_control_topic[72] = {0};

#define LOCK_CTX() xSemaphoreTake(s_ctx.mutex, portMAX_DELAY)
#define UNLOCK_CTX() xSemaphoreGive(s_ctx.mutex)
#define LOCK_LIFECYCLE() xSemaphoreTake(s_lifecycle_mutex, portMAX_DELAY)
#define UNLOCK_LIFECYCLE() xSemaphoreGive(s_lifecycle_mutex)

typedef struct {
    esp_mqtt_client_handle_t client;
    uint32_t client_generation;
    uint32_t connection_generation;
} mqtt_operation_t;

static void set_state(mqtt_client_state_t state);
mqtt_client_state_t mqtt_client_get_state(void);
static void build_topics(void);
static void mqtt_event_handler(void *handler_args, esp_event_base_t base,
                               int32_t event_id, void *event_data);
static bool create_and_start_client(void);
static bool retire_client(esp_mqtt_client_handle_t *out_client);
static void allow_recovery_create(void);
static void owner_start(void);
static bool owner_stop(void);
static void owner_attempt_recovery(void);
static bool owner_recreate_after_subscription_failure(void);
static bool begin_operation(mqtt_operation_t *op, bool require_transport);
static bool finish_operation(const mqtt_operation_t *op);
static bool finish_subscribe_operation(const mqtt_operation_t *op, int msg_id);
static bool claim_subscription_failure(void);
static void reset_subscription_state_locked(void);
static bool handle_current_event_locked(esp_mqtt_event_handle_t event,
                                        mqtt_transport_cb_t *transport_cb,
                                        void **transport_ctx,
                                        uint32_t *transport_generation,
                                        mqtt_msg_cb_t *msg_cb,
                                        void **msg_ctx,
                                        mqtt_owner_wake_cb_t *owner_wake_cb,
                                        void **owner_wake_ctx);
static bool try_publish_ready(void);

/* === Lifecycle-owner state accessors (all protected by s_lifecycle_mutex) === */
static void owner_set_connecting(int64_t start_us, bool active)
{
    LOCK_LIFECYCLE();
    s_connecting_start_us = start_us;
    s_connecting_active = active;
    UNLOCK_LIFECYCLE();
}

static bool owner_get_connecting_active(void)
{
    LOCK_LIFECYCLE();
    bool v = s_connecting_active;
    UNLOCK_LIFECYCLE();
    return v;
}

static int64_t owner_get_connecting_start_us(void)
{
    LOCK_LIFECYCLE();
    int64_t v = s_connecting_start_us;
    UNLOCK_LIFECYCLE();
    return v;
}

static int owner_begin_reconnect_attempt(void)
{
    LOCK_LIFECYCLE();
    s_reconnect_attempts++;
    int attempts = s_reconnect_attempts;
    UNLOCK_LIFECYCLE();
    return attempts;
}

static int owner_get_reconnect_attempts(void)
{
    LOCK_LIFECYCLE();
    int v = s_reconnect_attempts;
    UNLOCK_LIFECYCLE();
    return v;
}

static void owner_reset_reconnect_attempts(void)
{
    LOCK_LIFECYCLE();
    s_reconnect_attempts = 0;
    s_reconnect_deadline_us = 0;
    UNLOCK_LIFECYCLE();
}

static int64_t owner_reconnect_backoff_us_locked(void)
{
    int attempts = s_reconnect_attempts;
    int64_t delay_us = 1000000LL;
    const int64_t max_delay_us = 30000000LL;
    while (attempts-- > 0 && delay_us < max_delay_us) {
        delay_us *= 2;
        if (delay_us > max_delay_us) delay_us = max_delay_us;
    }
    return delay_us;
}

/* Arm without moving an existing deadline. Repeated DISCONNECTED callbacks
 * may wake the owner, but can never pull the next recovery action forward. */
static int64_t owner_arm_reconnect_deadline(int64_t now_us)
{
    LOCK_LIFECYCLE();
    if (s_reconnect_deadline_us == 0) {
        s_reconnect_deadline_us = now_us + owner_reconnect_backoff_us_locked();
    }
    int64_t deadline = s_reconnect_deadline_us;
    UNLOCK_LIFECYCLE();
    return deadline;
}

/* Claim exactly one due recovery action. A missing deadline is first armed,
 * never treated as immediately due. */
static bool owner_claim_reconnect_deadline(int64_t now_us)
{
    LOCK_LIFECYCLE();
    if (s_reconnect_deadline_us == 0) {
        s_reconnect_deadline_us = now_us + owner_reconnect_backoff_us_locked();
        UNLOCK_LIFECYCLE();
        return false;
    }
    if (now_us < s_reconnect_deadline_us) {
        UNLOCK_LIFECYCLE();
        return false;
    }
    s_reconnect_deadline_us = 0;
    UNLOCK_LIFECYCLE();
    return true;
}

static void owner_clear_reconnect_deadline(void)
{
    LOCK_LIFECYCLE();
    s_reconnect_deadline_us = 0;
    UNLOCK_LIFECYCLE();
}

/* s_subscription_retire_pending is read and written by owner recovery/stop
 * transactions. Every access goes through these lifecycle-locked accessors to
 * avoid torn reads and lost updates across tasks. */
static bool owner_get_subscription_retire_pending(void)
{
    LOCK_LIFECYCLE();
    bool v = s_subscription_retire_pending;
    UNLOCK_LIFECYCLE();
    return v;
}

static void owner_set_subscription_retire_pending(bool v)
{
    LOCK_LIFECYCLE();
    s_subscription_retire_pending = v;
    UNLOCK_LIFECYCLE();
}

static bool owner_recreate_after_subscription_failure(void)
{
    owner_set_subscription_retire_pending(true);
    esp_mqtt_client_handle_t old;
    if (!retire_client(&old)) {
        ESP_LOGE(TAG, "subscription retire timed out; retrying on next owner tick");
        return false;
    }
    owner_set_subscription_retire_pending(false);
    allow_recovery_create();
    owner_reset_reconnect_attempts();
    owner_set_connecting(0, false);
    if (create_and_start_client()) {
        owner_set_connecting(esp_timer_get_time(), true);
        set_state(MQTT_CLIENT_CONNECTING);
    }
    return true;
}

void mqtt_client_init(void)
{
    ESP_LOGI(TAG, "Initializing MQTT client...");
    if (s_init_failed) {
        ESP_LOGE(TAG, "MQTT initialization is permanently failed");
        return;
    }
    if (s_ctx.mutex == NULL) {
        s_ctx.mutex = xSemaphoreCreateMutex();
        if (s_ctx.mutex == NULL) {
            ESP_LOGE(TAG, "Failed to create mutex");
            s_init_failed = true;
            s_ctx.state = MQTT_CLIENT_FAILED;
            return;
        }
    }
    if (s_lifecycle_mutex == NULL) {
        s_lifecycle_mutex = xSemaphoreCreateMutex();
        if (s_lifecycle_mutex == NULL) {
            ESP_LOGE(TAG, "Failed to create MQTT lifecycle mutex");
            vSemaphoreDelete(s_ctx.mutex);
            s_ctx.mutex = NULL;
            s_init_failed = true;
            s_ctx.state = MQTT_CLIENT_FAILED;
            return;
        }
    }
    build_topics();
}

esp_err_t mqtt_client_request_start(void)
{
    if (s_init_failed || s_lifecycle_mutex == NULL) return -1;
    LOCK_LIFECYCLE();
    s_start_requested = true;
    UNLOCK_LIFECYCLE();
    return ESP_OK;
}

esp_err_t mqtt_client_request_stop(void)
{
    if (s_init_failed || s_lifecycle_mutex == NULL) return -1;
    LOCK_LIFECYCLE();
    s_stop_requested = true;
    UNLOCK_LIFECYCLE();
    return ESP_OK;
}

static void owner_start(void)
{
    if (s_init_failed || s_ctx.mutex == NULL || s_lifecycle_mutex == NULL) {
        ESP_LOGE(TAG, "Cannot start MQTT: initialization failed");
        return;
    }
    LOCK_LIFECYCLE();
    if (s_ctx.client != NULL || s_client_starting || s_retire_in_progress) {
        UNLOCK_LIFECYCLE();
        ESP_LOGW(TAG, "MQTT client already started");
        return;
    }
    s_shutdown_requested = false;
    UNLOCK_LIFECYCLE();
    if (create_and_start_client() && mqtt_client_get_state() != MQTT_CLIENT_CONNECTED) {
        owner_set_connecting(esp_timer_get_time(), true);
        set_state(MQTT_CLIENT_CONNECTING);
    }
}

static bool owner_stop(void)
{
    if (s_init_failed || s_ctx.mutex == NULL || s_lifecycle_mutex == NULL) return false;
    esp_mqtt_client_handle_t old;
    if (!retire_client(&old)) {
        ESP_LOGE(TAG, "retire_client timed out; retry stop later");
        /* request_stop is level-triggered. Preserve it when the operation
         * drain barrier cannot be crossed so a network-up owner tick cannot
         * accidentally resume normal recovery on the client being stopped. */
        LOCK_LIFECYCLE();
        s_stop_requested = true;
        UNLOCK_LIFECYCLE();
        return false;
    }
    owner_reset_reconnect_attempts();
    owner_set_connecting(0, false);
    owner_set_subscription_retire_pending(false);
    LOCK_LIFECYCLE();
    s_stop_requested = false;
    UNLOCK_LIFECYCLE();
    set_state(MQTT_CLIENT_DISCONNECTED);
    return true;
}

void mqtt_client_owner_step(bool network_available)
{
    if (s_init_failed || s_ctx.mutex == NULL || s_lifecycle_mutex == NULL) return;
    if (!network_available) {
        (void)owner_stop();
        return;
    }
    owner_attempt_recovery();
}

static void owner_attempt_recovery(void)
{
    if (s_init_failed || s_ctx.mutex == NULL || s_lifecycle_mutex == NULL) return;

    bool start_requested, stop_requested;
    LOCK_LIFECYCLE();
    start_requested = s_start_requested;
    stop_requested = s_stop_requested;
    s_start_requested = false;
    s_stop_requested = false;
    UNLOCK_LIFECYCLE();
    if (stop_requested) {
        (void)owner_stop();
        return;
    }
    if (start_requested) {
        owner_start();
        return;
    }

    if (owner_get_subscription_retire_pending()) {
        (void)owner_recreate_after_subscription_failure();
        return;
    }

    bool transport_connected;
    bool disconnected_event_pending;
    LOCK_CTX();
    transport_connected = s_ctx.transport_connected;
    disconnected_event_pending = s_ctx.disconnected_event_pending;
    s_ctx.disconnected_event_pending = false;
    UNLOCK_CTX();

    if (disconnected_event_pending) {
        owner_set_connecting(0, false);
        (void)owner_arm_reconnect_deadline(esp_timer_get_time());
        set_state(MQTT_CLIENT_DISCONNECTED);
    }

    if (transport_connected) {
        owner_clear_reconnect_deadline();
        bool send_subscription = false;
        int64_t now_us = esp_timer_get_time();

        /* SUBACK and deadline compete in this single CTX transaction. Once
         * either commits SUCCEEDED or FAILED, the other path cannot reverse
         * the result for this connection generation. */
        LOCK_CTX();
        bool current_generation = s_ctx.transport_connected &&
                                  s_ctx.subscription_generation ==
                                      s_ctx.connection_generation;
        if (current_generation && s_ctx.subscription_phase == MQTT_SUB_WAIT_ACK &&
            s_ctx.subscription_deadline_us > 0 &&
            now_us >= s_ctx.subscription_deadline_us) {
            s_ctx.subscription_phase = MQTT_SUB_FAILED;
            s_ctx.subscription_deadline_us = 0;
        }
        if (current_generation && s_ctx.subscription_phase == MQTT_SUB_NEEDS_SEND) {
            send_subscription = true;
        }
        UNLOCK_CTX();

        if (claim_subscription_failure()) {
            ESP_LOGW(TAG, "subscription phase failed; recreating MQTT client");
            (void)owner_recreate_after_subscription_failure();
            return;
        }

        if (send_subscription) {
            set_state(MQTT_CLIENT_CONNECTING);
            mqtt_operation_t op;
            if (begin_operation(&op, true)) {
                const esp_mqtt_topic_t topics[] = {
                    { .filter = s_down_topic, .qos = 0 },
                    { .filter = s_control_topic, .qos = 1 },
                };
                int msg_id = esp_mqtt_client_subscribe_multiple(op.client, topics, 2);
                (void)finish_subscribe_operation(&op, msg_id);
            }
            if (claim_subscription_failure()) {
                ESP_LOGW(TAG, "batched subscribe API failed; recreating MQTT client");
                (void)owner_recreate_after_subscription_failure();
                return;
            }
        }
        (void)try_publish_ready();
        return;
    }

    if (mqtt_client_get_state() == MQTT_CLIENT_CONNECTED) set_state(MQTT_CLIENT_DISCONNECTED);

    if (mqtt_client_get_state() == MQTT_CLIENT_CONNECTING && owner_get_connecting_active()) {
        int64_t elapsed_us = esp_timer_get_time() - owner_get_connecting_start_us();
        if (elapsed_us < (int64_t)CONFIG_MQTT_CONNECTING_TIMEOUT_SEC * 1000000) return;
        ESP_LOGW(TAG, "CONNECTING stuck, escalating to recreate");
        esp_mqtt_client_handle_t old;
        if (!retire_client(&old)) {
            ESP_LOGE(TAG, "retire timed out during CONNECTING escalation, retry next tick");
            return;
        }
        allow_recovery_create();
        owner_reset_reconnect_attempts();
        owner_set_connecting(0, false);
        if (create_and_start_client()) {
            owner_set_connecting(esp_timer_get_time(), true);
            set_state(MQTT_CLIENT_CONNECTING);
        }
        return;
    }

    LOCK_CTX();
    bool have_client = s_ctx.client != NULL;
    UNLOCK_CTX();
    if (!have_client) {
        if (create_and_start_client()) {
            owner_reset_reconnect_attempts();
            owner_set_connecting(esp_timer_get_time(), true);
            set_state(MQTT_CLIENT_CONNECTING);
        }
        return;
    }

    int64_t now_us = esp_timer_get_time();
    if (!owner_claim_reconnect_deadline(now_us)) return;

    if (owner_get_reconnect_attempts() < CONFIG_MQTT_RECONNECT_MAX) {
        mqtt_operation_t op;
        if (!begin_operation(&op, false)) return;
        int attempts = owner_begin_reconnect_attempt();
        esp_err_t err = esp_mqtt_client_reconnect(op.client);
        if (!finish_operation(&op)) return;
        if (err == ESP_OK) {
            owner_set_connecting(now_us, true);
            set_state(MQTT_CLIENT_CONNECTING);
        } else {
            ESP_LOGW(TAG, "Reconnect request failed (%d/%d)", attempts,
                     CONFIG_MQTT_RECONNECT_MAX);
            (void)owner_arm_reconnect_deadline(now_us);
            set_state(MQTT_CLIENT_DISCONNECTED);
        }
        return;
    }

    ESP_LOGW(TAG, "Reconnect exhausted, escalating to destroy+recreate");
    esp_mqtt_client_handle_t old;
    if (!retire_client(&old)) {
        ESP_LOGE(TAG, "retire timed out during escalation, retry next tick");
        return;
    }
    allow_recovery_create();
    owner_reset_reconnect_attempts();
    if (create_and_start_client()) {
        owner_set_connecting(esp_timer_get_time(), true);
        set_state(MQTT_CLIENT_CONNECTING);
    }
}

bool mqtt_client_publish_impl(const uint8_t *data, size_t len)
{
    mqtt_operation_t op;
    if (s_init_failed || s_ctx.mutex == NULL || s_lifecycle_mutex == NULL ||
        !begin_operation(&op, false)) return false;

    LOCK_CTX();
    bool connected = s_ctx.state == MQTT_CLIENT_CONNECTED && s_ctx.transport_connected;
    UNLOCK_CTX();
    if (!connected) {
        (void)finish_operation(&op);
        return false;
    }

    const int qos = mqtt_publish_qos_for_frame(data, len);
    int msg_id = qos == MQTT_PUBLISH_QOS_NO_ACK
        ? esp_mqtt_client_enqueue(op.client, s_up_topic, (const char *)data, len,
                                  qos, 0, true)
        : esp_mqtt_client_publish(op.client, s_up_topic, (const char *)data, len,
                                  qos, 0);
    bool valid = finish_operation(&op);
    if (msg_id < 0 || !valid) {
        ESP_LOGE(TAG, "Publish failed");
        return false;
    }
    ESP_LOGD(TAG, "Published %zu bytes to %s (msg_id=%d)", len, s_up_topic, msg_id);
    return true;
}

mqtt_client_state_t mqtt_client_get_state(void)
{
    if (s_init_failed || s_ctx.mutex == NULL) return MQTT_CLIENT_FAILED;
    LOCK_CTX();
    mqtt_client_state_t state = s_ctx.state;
    UNLOCK_CTX();
    return state;
}

bool mqtt_client_is_connected_impl(void)
{
    if (s_init_failed || s_ctx.mutex == NULL) return false;
    LOCK_CTX();
    bool connected = s_ctx.state == MQTT_CLIENT_CONNECTED && s_ctx.transport_connected;
    UNLOCK_CTX();
    return connected;
}

void mqtt_client_register_msg_cb(mqtt_msg_cb_t cb, void *ctx)
{
    if (s_init_failed || s_ctx.mutex == NULL) return;
    LOCK_CTX(); s_ctx.msg_cb = cb; s_ctx.msg_cb_ctx = ctx; UNLOCK_CTX();
}

void mqtt_client_register_state_cb(mqtt_state_cb_t cb, void *ctx)
{
    if (s_init_failed || s_ctx.mutex == NULL) return;
    LOCK_CTX(); s_ctx.state_cb = cb; s_ctx.state_cb_ctx = ctx; UNLOCK_CTX();
}

void mqtt_client_register_ready_cb(mqtt_ready_cb_t cb, void *ctx)
{
    if (s_init_failed || s_ctx.mutex == NULL) return;
    LOCK_CTX(); s_ctx.ready_cb = cb; s_ctx.ready_cb_ctx = ctx; UNLOCK_CTX();
}

void mqtt_client_register_transport_cb(mqtt_transport_cb_t cb, void *ctx)
{
    if (s_init_failed || s_ctx.mutex == NULL) return;
    LOCK_CTX(); s_ctx.transport_cb = cb; s_ctx.transport_cb_ctx = ctx; UNLOCK_CTX();
}

void mqtt_client_register_owner_wake_cb(mqtt_owner_wake_cb_t cb, void *ctx)
{
    if (s_init_failed || s_ctx.mutex == NULL) return;
    LOCK_CTX(); s_ctx.owner_wake_cb = cb; s_ctx.owner_wake_cb_ctx = ctx; UNLOCK_CTX();
}

void mqtt_client_set_node_id(const char *node_id)
{
    if (node_id == NULL) return;
    strlcpy(s_node_id, node_id, sizeof(s_node_id));
    build_topics();
    ESP_LOGI(TAG, "Node ID set: %s", s_node_id);
}

static void build_topics(void)
{
    if (s_node_id[0] != '\0') {
        snprintf(s_up_topic, sizeof(s_up_topic), "nodes/%s/up", s_node_id);
        snprintf(s_down_topic, sizeof(s_down_topic), "nodes/%s/down", s_node_id);
        snprintf(s_control_topic, sizeof(s_control_topic), "nodes/%s/control", s_node_id);
    } else {
        strlcpy(s_up_topic, "nodes/unknown/up", sizeof(s_up_topic));
        strlcpy(s_down_topic, "nodes/unknown/down", sizeof(s_down_topic));
        strlcpy(s_control_topic, "nodes/unknown/control", sizeof(s_control_topic));
    }
}

static void reset_subscription_state_locked(void)
{
    s_ctx.subscription_generation = 0;
    s_ctx.subscription_phase = MQTT_SUB_IDLE;
    s_ctx.subscription_msg_id = -1;
    s_ctx.subscription_deadline_us = 0;
}

static void set_state(mqtt_client_state_t state)
{
    mqtt_state_cb_t cb = NULL;
    void *cb_ctx = NULL;
    LOCK_CTX();
    if (s_ctx.state != state) {
        s_ctx.state = state;
        cb = s_ctx.state_cb;
        cb_ctx = s_ctx.state_cb_ctx;
    }
    UNLOCK_CTX();
    if (cb != NULL) cb(state, cb_ctx);
}

/* Caller holds s_ctx.mutex. The current-client validation and every event
 * effect on s_ctx (including callback/context snapshots) are one atomic CTX
 * transaction. The caller invokes any snapshotted callback after unlocking. */
static bool handle_current_event_locked(esp_mqtt_event_handle_t event,
                                        mqtt_transport_cb_t *transport_cb,
                                        void **transport_ctx,
                                        uint32_t *transport_generation,
                                        mqtt_msg_cb_t *msg_cb,
                                        void **msg_ctx,
                                        mqtt_owner_wake_cb_t *owner_wake_cb,
                                        void **owner_wake_ctx)
{
    if (transport_cb != NULL) *transport_cb = NULL;
    if (transport_ctx != NULL) *transport_ctx = NULL;
    if (transport_generation != NULL) *transport_generation = 0;
    if (msg_cb != NULL) *msg_cb = NULL;
    if (msg_ctx != NULL) *msg_ctx = NULL;
    if (owner_wake_cb != NULL) *owner_wake_cb = NULL;
    if (owner_wake_ctx != NULL) *owner_wake_ctx = NULL;
    if (event == NULL || event->client != s_ctx.client) return false;

    switch (event->event_id) {
    case MQTT_EVENT_CONNECTED:
        s_ctx.transport_connected = true;
        s_ctx.connection_generation++;
        if (s_ctx.connection_generation == 0) s_ctx.connection_generation = 1;
        s_ctx.subscription_generation = s_ctx.connection_generation;
        s_ctx.subscription_phase = MQTT_SUB_NEEDS_SEND;
        s_ctx.subscription_msg_id = -1;
        s_ctx.subscription_deadline_us = 0;
        s_ctx.disconnected_event_pending = false;
        if (transport_cb != NULL) *transport_cb = s_ctx.transport_cb;
        if (transport_ctx != NULL) *transport_ctx = s_ctx.transport_cb_ctx;
        if (transport_generation != NULL) *transport_generation = s_ctx.connection_generation;
        if (owner_wake_cb != NULL) *owner_wake_cb = s_ctx.owner_wake_cb;
        if (owner_wake_ctx != NULL) *owner_wake_ctx = s_ctx.owner_wake_cb_ctx;
        break;
    case MQTT_EVENT_DISCONNECTED:
        s_ctx.transport_connected = false;
        reset_subscription_state_locked();
        bool first_disconnect = !s_ctx.disconnected_event_pending;
        s_ctx.disconnected_event_pending = true;
        if (first_disconnect) {
            if (owner_wake_cb != NULL) *owner_wake_cb = s_ctx.owner_wake_cb;
            if (owner_wake_ctx != NULL) *owner_wake_ctx = s_ctx.owner_wake_cb_ctx;
        }
        break;
    case MQTT_EVENT_SUBSCRIBED:
        if (s_ctx.transport_connected &&
            s_ctx.subscription_generation == s_ctx.connection_generation &&
            s_ctx.subscription_phase == MQTT_SUB_WAIT_ACK &&
            event->msg_id == s_ctx.subscription_msg_id) {
            bool exact_success = event->error_handle != NULL &&
                                 event->error_handle->error_type == MQTT_ERROR_TYPE_NONE &&
                                 event->data != NULL && event->data_len == 2 &&
                                 (uint8_t)event->data[0] == 0x00 &&
                                 (uint8_t)event->data[1] == 0x01;
            s_ctx.subscription_phase = exact_success
                ? MQTT_SUB_SUCCEEDED : MQTT_SUB_FAILED;
            s_ctx.subscription_deadline_us = 0;
            if (owner_wake_cb != NULL) *owner_wake_cb = s_ctx.owner_wake_cb;
            if (owner_wake_ctx != NULL) *owner_wake_ctx = s_ctx.owner_wake_cb_ctx;
        }
        break;
    case MQTT_EVENT_DATA:
        if (msg_cb != NULL) *msg_cb = s_ctx.msg_cb;
        if (msg_ctx != NULL) *msg_ctx = s_ctx.msg_cb_ctx;
        break;
    default:
        break;
    }
    return true;
}

static bool begin_operation(mqtt_operation_t *op, bool require_transport)
{
    if (op == NULL) return false;
    LOCK_LIFECYCLE();
    if (s_shutdown_requested || s_client_starting) {
        UNLOCK_LIFECYCLE();
        return false;
    }
    LOCK_CTX();
    bool usable = s_ctx.client != NULL &&
                  (!require_transport || s_ctx.transport_connected);
    if (usable) {
        op->client = s_ctx.client;
        op->client_generation = s_ctx.client_generation;
        op->connection_generation = s_ctx.connection_generation;
        s_active_operations++;
    }
    UNLOCK_CTX();
    UNLOCK_LIFECYCLE();
    return usable;
}

static bool finish_operation(const mqtt_operation_t *op)
{
    LOCK_LIFECYCLE();
    LOCK_CTX();
    bool valid = op != NULL && s_ctx.client == op->client &&
                 s_ctx.client_generation == op->client_generation;
    if (s_active_operations > 0) s_active_operations--;
    UNLOCK_CTX();
    UNLOCK_LIFECYCLE();
    return valid;
}

static bool finish_subscribe_operation(const mqtt_operation_t *op, int msg_id)
{
    LOCK_LIFECYCLE();
    LOCK_CTX();
    bool generation_ok = op != NULL &&
                 s_ctx.client == op->client &&
                 s_ctx.client_generation == op->client_generation &&
                 s_ctx.transport_connected &&
                 s_ctx.connection_generation == op->connection_generation &&
                 s_ctx.subscription_generation == op->connection_generation &&
                 s_ctx.subscription_phase == MQTT_SUB_NEEDS_SEND;
    bool valid = generation_ok && msg_id >= 0;
    if (generation_ok) {
        s_ctx.subscription_msg_id = valid ? msg_id : -1;
        s_ctx.subscription_phase = valid ? MQTT_SUB_WAIT_ACK : MQTT_SUB_FAILED;
        s_ctx.subscription_deadline_us = valid
            ? esp_timer_get_time() +
                  (int64_t)CONFIG_MQTT_SUBSCRIPTION_TIMEOUT_SEC * 1000000LL
            : 0;
    }
    if (s_active_operations > 0) s_active_operations--;
    UNLOCK_CTX();
    UNLOCK_LIFECYCLE();
    return valid;
}

/* Claim a failed subscription generation before owner-side teardown. This
 * transaction shares s_ctx.mutex with SUBACK processing and timeout, so no
 * late event can revive a generation after failure wins. */
static bool claim_subscription_failure(void)
{
    LOCK_CTX();
    bool claimed = s_ctx.transport_connected &&
                   s_ctx.subscription_generation == s_ctx.connection_generation &&
                   s_ctx.subscription_phase == MQTT_SUB_FAILED;
    if (claimed) {
        s_ctx.transport_connected = false;
        reset_subscription_state_locked();
    }
    UNLOCK_CTX();
    return claimed;
}

/* Bounded deadline for waiting on inflight ESP-MQTT operations during retire.
 * In host tests the clock is frozen, so use 0 for immediate timeout. */
#ifndef EHOME_MQTT_HOST_TEST
#define RETIRE_WAIT_TIMEOUT_US (12 * 1000000LL)
#else
#define RETIRE_WAIT_TIMEOUT_US 0
#endif

static bool retire_client(esp_mqtt_client_handle_t *out_client)
{
    if (out_client != NULL) *out_client = NULL;
    LOCK_LIFECYCLE();
    if (s_retire_in_progress) {
        UNLOCK_LIFECYCLE();
        return false;
    }
    s_retire_in_progress = true;
    s_shutdown_requested = true;
    int64_t deadline_us = esp_timer_get_time() + RETIRE_WAIT_TIMEOUT_US;
    while (s_active_operations != 0) {
        if (esp_timer_get_time() >= deadline_us) {
            /* Fail closed: do NOT destroy the client while operations are
             * still inflight (UAF risk), and keep shutdown armed so no new
             * operation can enter before the owner retries this drain. An
             * already-admitted operation can still finish and decrement the
             * count; only a later explicit start or successful recovery
             * recreate may reopen the operation gate. */
            s_retire_in_progress = false;
            UNLOCK_LIFECYCLE();
            ESP_LOGE(TAG, "retire_client: inflight ops did not drain within %lld us",
                     (long long)RETIRE_WAIT_TIMEOUT_US);
            return false;
        }
        UNLOCK_LIFECYCLE();
        vTaskDelay(1);
        LOCK_LIFECYCLE();
    }
    LOCK_CTX();
    esp_mqtt_client_handle_t client = s_ctx.client;
    s_ctx.client = NULL;
    s_ctx.client_generation = 0;
    s_client_starting = false;
    s_ctx.transport_connected = false;
    reset_subscription_state_locked();
    s_ctx.disconnected_event_pending = false;
    UNLOCK_CTX();
    UNLOCK_LIFECYCLE();
    if (client != NULL) {
        (void)esp_mqtt_client_stop(client);
        (void)esp_mqtt_client_destroy(client);
    }
    LOCK_LIFECYCLE();
    s_retire_in_progress = false;
    UNLOCK_LIFECYCLE();
    if (out_client != NULL) *out_client = client;
    return true;
}

static void allow_recovery_create(void)
{
    LOCK_LIFECYCLE();
    s_shutdown_requested = false;
    UNLOCK_LIFECYCLE();
}

static bool create_and_start_client(void)
{
    LOCK_LIFECYCLE();
    if (s_shutdown_requested || s_ctx.client != NULL || s_client_starting ||
        s_retire_in_progress) {
        UNLOCK_LIFECYCLE();
        return false;
    }
    s_client_starting = true;
    s_active_operations++;
    uint32_t client_generation = ++s_next_client_generation;
    if (client_generation == 0) client_generation = ++s_next_client_generation;
    UNLOCK_LIFECYCLE();

    esp_mqtt_client_config_t mqtt_cfg;
    memset(&mqtt_cfg, 0, sizeof(mqtt_cfg));
    mqtt_cfg.broker.address.uri = CONFIG_COLLECTOR_MQTT_BROKER_URL;
    mqtt_cfg.credentials.client_id = s_node_id;
    mqtt_cfg.session.keepalive = 30;
    mqtt_cfg.session.disable_clean_session = false;
    mqtt_cfg.network.disable_auto_reconnect = true;

    esp_mqtt_client_handle_t client = esp_mqtt_client_init(&mqtt_cfg);
    if (client == NULL) {
        LOCK_LIFECYCLE();
        s_client_starting = false;
        if (s_active_operations > 0) s_active_operations--;
        UNLOCK_LIFECYCLE();
        set_state(MQTT_CLIENT_FAILED);
        return false;
    }

    LOCK_LIFECYCLE();
    bool cancelled = s_shutdown_requested;
    if (!cancelled) {
        LOCK_CTX();
        s_ctx.client = client;
        s_ctx.client_generation = client_generation;
        UNLOCK_CTX();
    }
    UNLOCK_LIFECYCLE();
    if (cancelled) {
        (void)esp_mqtt_client_destroy(client);
        LOCK_LIFECYCLE();
        s_client_starting = false;
        if (s_active_operations > 0) s_active_operations--;
        UNLOCK_LIFECYCLE();
        return false;
    }

    esp_err_t err = esp_mqtt_client_register_event(client, ESP_EVENT_ANY_ID,
                                                     mqtt_event_handler, NULL);
    if (err == ESP_OK) err = esp_mqtt_client_start(client);

    LOCK_LIFECYCLE();
    cancelled = s_shutdown_requested;
    LOCK_CTX();
    if (err != ESP_OK || cancelled) {
        if (s_ctx.client == client) {
            s_ctx.client = NULL;
            s_ctx.client_generation = 0;
        }
    }
    s_client_starting = false;
    if (s_active_operations > 0) s_active_operations--;
    UNLOCK_CTX();
    UNLOCK_LIFECYCLE();

    if (err != ESP_OK || cancelled) {
        if (err != ESP_OK) {
            ESP_LOGE(TAG, "Failed to start MQTT client: %s", esp_err_to_name(err));
            set_state(MQTT_CLIENT_FAILED);
        }
        (void)esp_mqtt_client_destroy(client);
        return false;
    }
    return true;
}

static bool try_publish_ready(void)
{
    mqtt_state_cb_t state_cb = NULL;
    void *state_ctx = NULL;
    mqtt_ready_cb_t ready_cb = NULL;
    void *ready_ctx = NULL;
    uint32_t generation = 0;
    LOCK_CTX();
    bool ready = s_ctx.transport_connected &&
                 s_ctx.subscription_generation == s_ctx.connection_generation &&
                 s_ctx.subscription_phase == MQTT_SUB_SUCCEEDED;
    if (ready) {
        s_ctx.subscription_phase = MQTT_SUB_IDLE;
        s_ctx.subscription_msg_id = -1;
        s_ctx.subscription_deadline_us = 0;
        s_ctx.state = MQTT_CLIENT_CONNECTED;
        state_cb = s_ctx.state_cb;
        state_ctx = s_ctx.state_cb_ctx;
        ready_cb = s_ctx.ready_cb;
        ready_ctx = s_ctx.ready_cb_ctx;
        generation = s_ctx.connection_generation;
    }
    UNLOCK_CTX();
    if (!ready) return false;
    if (state_cb != NULL) state_cb(MQTT_CLIENT_CONNECTED, state_ctx);
    if (ready_cb != NULL) ready_cb(generation, ready_ctx);
    owner_reset_reconnect_attempts();
    owner_set_connecting(0, false);
    return true;
}

static void mqtt_event_handler(void *handler_args, esp_event_base_t base,
                               int32_t event_id, void *event_data)
{
    (void)handler_args;
    (void)base;
    (void)event_id;
    esp_mqtt_event_handle_t event = event_data;
    if (event == NULL) return;

    mqtt_transport_cb_t transport_cb = NULL;
    void *transport_ctx = NULL;
    uint32_t transport_generation = 0;
    mqtt_msg_cb_t msg_cb = NULL;
    void *msg_ctx = NULL;
    mqtt_owner_wake_cb_t owner_wake_cb = NULL;
    void *owner_wake_ctx = NULL;
    bool guarded_event = event->event_id == MQTT_EVENT_CONNECTED ||
                         event->event_id == MQTT_EVENT_DISCONNECTED ||
                         event->event_id == MQTT_EVENT_SUBSCRIBED ||
                         event->event_id == MQTT_EVENT_DATA;
    if (guarded_event) {
        LOCK_CTX();
        bool current_client = handle_current_event_locked(
            event, &transport_cb, &transport_ctx, &transport_generation,
            &msg_cb, &msg_ctx,
            &owner_wake_cb, &owner_wake_ctx);
        UNLOCK_CTX();
        if (!current_client) {
            if (event->event_id == MQTT_EVENT_CONNECTED ||
                event->event_id == MQTT_EVENT_DISCONNECTED) {
                ESP_LOGW(TAG, "stale MQTT event %d from retired client, ignoring",
                         event->event_id);
            }
            return;
        }
    }

    switch (event->event_id) {
    case MQTT_EVENT_CONNECTED:
        ESP_LOGI(TAG, "MQTT connected to broker");
        if (transport_cb != NULL) transport_cb(transport_generation, transport_ctx);
        if (owner_wake_cb != NULL) owner_wake_cb(owner_wake_ctx);
        break;
    case MQTT_EVENT_DISCONNECTED:
        if (owner_wake_cb != NULL) {
            ESP_LOGW(TAG, "MQTT disconnected");
            owner_wake_cb(owner_wake_ctx);
        }
        break;
    case MQTT_EVENT_SUBSCRIBED:
        if (owner_wake_cb != NULL) owner_wake_cb(owner_wake_ctx);
        break;
    case MQTT_EVENT_DATA: {
        if (msg_cb != NULL) msg_cb(event->topic, (const uint8_t *)event->data,
                                   event->data_len, msg_ctx);
        break;
    }
    case MQTT_EVENT_ERROR:
        ESP_LOGE(TAG, "MQTT transport error");
        break;
    case MQTT_EVENT_PUBLISHED:
        break;
    default:
        break;
    }
}
