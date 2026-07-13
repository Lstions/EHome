/**
 * @file mqtt_client.c
 * @brief MQTT Client using espressif__mqtt component
 */

#include "ehome_mqtt.h"
#include "esp_log.h"
#include "esp_event.h"
#include <string.h>

#define TAG "MQTT"

enum {
    MQTT_PUBLISH_QOS_NO_ACK = 0,
    MQTT_PUBLISH_QOS_RELIABLE = 1,
    /* MsgLogStream is the frame protocol message type 0x1D. Keep this local to
     * avoid adding a frame component dependency solely for QoS selection. */
    MQTT_FRAME_TYPE_LOG_STREAM = 0x1D,
};

static int mqtt_publish_qos_for_frame(const uint8_t *data, size_t len)
{
    return data != NULL && len > 0 && data[0] == MQTT_FRAME_TYPE_LOG_STREAM
               ? MQTT_PUBLISH_QOS_NO_ACK
               : MQTT_PUBLISH_QOS_RELIABLE;
}

/* === Static context instance === */
static mqtt_client_ctx_t s_ctx = {
    .client = NULL,
    .state = MQTT_CLIENT_DISCONNECTED,
    .msg_cb = NULL,
    .msg_cb_ctx = NULL,
    .state_cb = NULL,
    .state_cb_ctx = NULL,
    .mutex = NULL,
};

/* === Configuration strings (set once at init, read-only thereafter) === */
static char s_node_id[32] = {0};
static char s_up_topic[64] = {0};
static char s_down_topic[64] = {0};

/* === Internal function declarations === */
static void set_state(mqtt_client_ctx_t *ctx, mqtt_client_state_t state);
static void build_topics(void);
static void mqtt_event_handler(void *handler_args, esp_event_base_t base, int32_t event_id, void *event_data);

/* === Helper macros for mutex locking === */
#define LOCK_CTX()   do { if (s_ctx.mutex) xSemaphoreTake(s_ctx.mutex, portMAX_DELAY); } while(0)
#define UNLOCK_CTX() do { if (s_ctx.mutex) xSemaphoreGive(s_ctx.mutex); } while(0)

void mqtt_client_init(void)
{
    ESP_LOGI(TAG, "Initializing MQTT client...");
    
    /* Create mutex for context protection */
    if (s_ctx.mutex == NULL) {
        s_ctx.mutex = xSemaphoreCreateMutex();
        if (s_ctx.mutex == NULL) {
            ESP_LOGE(TAG, "Failed to create mutex");
        }
    }
    
    build_topics();
}

void mqtt_client_start(void)
{
    ESP_LOGI(TAG, "mqtt_client_start() called");
    LOCK_CTX();
    
    if (s_ctx.client != NULL) {
        ESP_LOGW(TAG, "MQTT client already started");
        UNLOCK_CTX();
        return;
    }
    
    static esp_mqtt_client_config_t mqtt_cfg;
    memset(&mqtt_cfg, 0, sizeof(mqtt_cfg));
    mqtt_cfg.broker.address.uri = CONFIG_COLLECTOR_MQTT_BROKER_URL;
    mqtt_cfg.credentials.client_id = s_node_id;
    mqtt_cfg.session.keepalive = 30;
    mqtt_cfg.session.disable_clean_session = false;
    
    s_ctx.client = esp_mqtt_client_init(&mqtt_cfg);
    if (s_ctx.client == NULL) {
        ESP_LOGE(TAG, "Failed to create MQTT client");
        set_state(&s_ctx, MQTT_CLIENT_FAILED);
        UNLOCK_CTX();
        return;
    }
    
    esp_mqtt_client_register_event(s_ctx.client, ESP_EVENT_ANY_ID, mqtt_event_handler, NULL);
    esp_err_t err = esp_mqtt_client_start(s_ctx.client);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to start MQTT client: %s", esp_err_to_name(err));
        set_state(&s_ctx, MQTT_CLIENT_FAILED);
        UNLOCK_CTX();
        return;
    }
    
    set_state(&s_ctx, MQTT_CLIENT_CONNECTING);
    UNLOCK_CTX();
}

void mqtt_client_stop(void)
{
    LOCK_CTX();
    
    if (s_ctx.client != NULL) {
        esp_mqtt_client_stop(s_ctx.client);
        esp_mqtt_client_destroy(s_ctx.client);
        s_ctx.client = NULL;
    }
    set_state(&s_ctx, MQTT_CLIENT_DISCONNECTED);
    
    UNLOCK_CTX();
}

bool mqtt_client_publish_impl(const uint8_t *data, size_t len)
{
    LOCK_CTX();
    
    if (s_ctx.client == NULL || s_ctx.state != MQTT_CLIENT_CONNECTED) {
        ESP_LOGW(TAG, "Cannot publish: not connected");
        UNLOCK_CTX();
        return false;
    }

    const int qos = mqtt_publish_qos_for_frame(data, len);
    int msg_id = esp_mqtt_client_publish(s_ctx.client, s_up_topic, (const char *)data, len, qos, 0);
    if (msg_id < 0) {
        ESP_LOGE(TAG, "Publish failed");
        UNLOCK_CTX();
        return false;
    }

    ESP_LOGD(TAG, "Published %zu bytes to %s (msg_id=%d)", len, s_up_topic, msg_id);
    UNLOCK_CTX();
    return true;
}

bool mqtt_client_subscribe_impl(const char *topic)
{
    LOCK_CTX();
    
    if (s_ctx.client == NULL) {
        UNLOCK_CTX();
        return false;
    }

    int msg_id = esp_mqtt_client_subscribe(s_ctx.client, topic, 1);
    if (msg_id < 0) {
        ESP_LOGE(TAG, "Subscribe failed for %s", topic);
        UNLOCK_CTX();
        return false;
    }

    ESP_LOGI(TAG, "Subscribed to %s", topic);
    UNLOCK_CTX();
    return true;
}

mqtt_client_state_t mqtt_client_get_state(void)
{
    LOCK_CTX();
    mqtt_client_state_t state = s_ctx.state;
    UNLOCK_CTX();
    return state;
}

bool mqtt_client_is_connected_impl(void)
{
    LOCK_CTX();
    bool connected = (s_ctx.state == MQTT_CLIENT_CONNECTED);
    UNLOCK_CTX();
    return connected;
}

void mqtt_client_register_msg_cb(mqtt_msg_cb_t cb, void *ctx)
{
    LOCK_CTX();
    s_ctx.msg_cb = cb;
    s_ctx.msg_cb_ctx = ctx;
    UNLOCK_CTX();
}

void mqtt_client_register_state_cb(mqtt_state_cb_t cb, void *ctx)
{
    LOCK_CTX();
    s_ctx.state_cb = cb;
    s_ctx.state_cb_ctx = ctx;
    UNLOCK_CTX();
}

void mqtt_client_set_node_id(const char *node_id)
{
    strlcpy(s_node_id, node_id, sizeof(s_node_id));
    build_topics();
    ESP_LOGI(TAG, "Node ID set: %s", s_node_id);
}

/* === Internal === */

static void build_topics(void)
{
    if (s_node_id[0] != '\0') {
        snprintf(s_up_topic, sizeof(s_up_topic), "nodes/%s/up", s_node_id);
        snprintf(s_down_topic, sizeof(s_down_topic), "nodes/%s/down", s_node_id);
    } else {
        strlcpy(s_up_topic, "nodes/unknown/up", sizeof(s_up_topic));
        strlcpy(s_down_topic, "nodes/unknown/down", sizeof(s_down_topic));
    }
}

static void set_state(mqtt_client_ctx_t *ctx, mqtt_client_state_t state)
{
    /* Note: caller must already hold the mutex */
    if (ctx->state != state) {
        ctx->state = state;
        if (ctx->state_cb) {
            ctx->state_cb(state, ctx->state_cb_ctx);
        }
    }
}

static void mqtt_event_handler(void *handler_args, esp_event_base_t base, int32_t event_id, void *event_data)
{
    (void)handler_args;
    (void)base;
    esp_mqtt_event_handle_t event = event_data;

    /* Don't hold mutex during event processing - MQTT events run in the MQTT task
     * and blocking here can deadlock with esp_mqtt_client API calls */
    switch (event->event_id) {
    case MQTT_EVENT_CONNECTED:
    ESP_LOGI(TAG, "MQTT connected to broker");
        /* Subscribe BEFORE notifying app, so down-topic is ready for ConfigManifest */
        if (s_down_topic[0] != '\0') {
            esp_mqtt_client_subscribe(s_ctx.client, s_down_topic, 1);
            ESP_LOGI(TAG, "Subscribed to %s", s_down_topic);
        }
        set_state(&s_ctx, MQTT_CLIENT_CONNECTED);
        break;

    case MQTT_EVENT_DISCONNECTED:
        ESP_LOGW(TAG, "MQTT disconnected");
        set_state(&s_ctx, MQTT_CLIENT_DISCONNECTED);
        break;

    case MQTT_EVENT_DATA: {
        ESP_LOGD(TAG, "MQTT data received");
        if (s_ctx.msg_cb) {
            s_ctx.msg_cb(event->topic, (const uint8_t *)event->data, event->data_len, s_ctx.msg_cb_ctx);
        }
        break;
    }

    case MQTT_EVENT_ERROR:
        ESP_LOGE(TAG, "MQTT transport error");
        break;

    case MQTT_EVENT_PUBLISHED:
        /* esp-mqtt can report this after publish() returns, including QoS 0.
         * Logging it would occur after log-stream suppression is restored and
         * turn every log frame into an asynchronous capture feedback loop. */
        break;

    default:
        break;
    }
}
