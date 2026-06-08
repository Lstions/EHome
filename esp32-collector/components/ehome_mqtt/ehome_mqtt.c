/**
 * @file mqtt_client.c
 * @brief MQTT Client using espressif__mqtt component
 */

#include "ehome_mqtt.h"
#include "mqtt_client.h"
#include "esp_log.h"
#include "esp_event.h"
#include <string.h>

#define TAG "MQTT"

static esp_mqtt_client_handle_t s_client = NULL;
static mqtt_client_state_t s_state = MQTT_CLIENT_DISCONNECTED;
static mqtt_msg_cb_t s_msg_cb = NULL;
static void *s_msg_cb_ctx = NULL;
static mqtt_state_cb_t s_state_cb = NULL;
static void *s_state_cb_ctx = NULL;
static char s_node_id[32] = {0};
static char s_up_topic[64] = {0};
static char s_down_topic[64] = {0};

static void set_state(mqtt_client_state_t state);
static void build_topics(void);
static void mqtt_event_handler(void *handler_args, esp_event_base_t base, int32_t event_id, void *event_data);

void mqtt_client_init(void)
{
    ESP_LOGI(TAG, "Initializing MQTT client...");
    build_topics();
}

void mqtt_client_start(void)
{
    if (s_client != NULL) {
        ESP_LOGW(TAG, "MQTT client already started");
        return;
    }

    esp_mqtt_client_config_t mqtt_cfg = {
        .broker.address.uri = "mqtt://192.168.20.3:1883",
        .credentials.client_id = s_node_id,
        .session.keepalive = 30,
        .session.disable_clean_session = false,  // TODO: true causes session conflict after EMQX restart
    };

    s_client = esp_mqtt_client_init(&mqtt_cfg);
    if (s_client == NULL) {
        ESP_LOGE(TAG, "Failed to create MQTT client");
        set_state(MQTT_CLIENT_FAILED);
        return;
    }

    esp_mqtt_client_register_event(s_client, ESP_EVENT_ANY_ID, mqtt_event_handler, NULL);
    esp_err_t err = esp_mqtt_client_start(s_client);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to start MQTT client: %s", esp_err_to_name(err));
        set_state(MQTT_CLIENT_FAILED);
        return;
    }

    set_state(MQTT_CLIENT_CONNECTING);
}

void mqtt_client_stop(void)
{
    if (s_client != NULL) {
        esp_mqtt_client_stop(s_client);
        esp_mqtt_client_destroy(s_client);
        s_client = NULL;
    }
    set_state(MQTT_CLIENT_DISCONNECTED);
}

bool mqtt_client_publish_impl(const uint8_t *data, size_t len)
{
    if (s_client == NULL || s_state != MQTT_CLIENT_CONNECTED) {
        ESP_LOGW(TAG, "Cannot publish: not connected");
        return false;
    }

    int msg_id = esp_mqtt_client_publish(s_client, s_up_topic, (const char *)data, len, 1, 0);
    if (msg_id < 0) {
        ESP_LOGE(TAG, "Publish failed");
        return false;
    }

    ESP_LOGD(TAG, "Published %zu bytes to %s (msg_id=%d)", len, s_up_topic, msg_id);
    return true;
}

bool mqtt_client_subscribe_impl(const char *topic)
{
    if (s_client == NULL) {
        return false;
    }

    int msg_id = esp_mqtt_client_subscribe(s_client, topic, 1);
    if (msg_id < 0) {
        ESP_LOGE(TAG, "Subscribe failed for %s", topic);
        return false;
    }

    ESP_LOGI(TAG, "Subscribed to %s", topic);
    return true;
}

mqtt_client_state_t mqtt_client_get_state(void)
{
    return s_state;
}

bool mqtt_client_is_connected_impl(void)
{
    return s_state == MQTT_CLIENT_CONNECTED;
}

void mqtt_client_register_msg_cb(mqtt_msg_cb_t cb, void *ctx)
{
    s_msg_cb = cb;
    s_msg_cb_ctx = ctx;
}

void mqtt_client_register_state_cb(mqtt_state_cb_t cb, void *ctx)
{
    s_state_cb = cb;
    s_state_cb_ctx = ctx;
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

static void set_state(mqtt_client_state_t state)
{
    if (s_state != state) {
        s_state = state;
        if (s_state_cb) {
            s_state_cb(state, s_state_cb_ctx);
        }
    }
}

static void mqtt_event_handler(void *handler_args, esp_event_base_t base, int32_t event_id, void *event_data)
{
    (void)handler_args;
    (void)base;
    esp_mqtt_event_handle_t event = event_data;

    switch (event->event_id) {
    case MQTT_EVENT_CONNECTED:
        ESP_LOGI(TAG, "MQTT connected to broker");
        /* Subscribe BEFORE notifying app, so down-topic is ready for ConfigManifest */
        if (s_down_topic[0] != '\0') {
            esp_mqtt_client_subscribe(s_client, s_down_topic, 1);
            ESP_LOGI(TAG, "Subscribed to %s", s_down_topic);
        }
        set_state(MQTT_CLIENT_CONNECTED);
        break;

    case MQTT_EVENT_DISCONNECTED:
        ESP_LOGW(TAG, "MQTT disconnected");
        set_state(MQTT_CLIENT_DISCONNECTED);
        break;

    case MQTT_EVENT_DATA: {
        ESP_LOGD(TAG, "MQTT data received");
        if (s_msg_cb) {
            s_msg_cb(event->topic, (const uint8_t *)event->data, event->data_len, s_msg_cb_ctx);
        }
        break;
    }

    default:
        break;
    }
}
