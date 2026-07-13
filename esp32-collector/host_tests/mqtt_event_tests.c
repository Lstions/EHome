#include <stdbool.h>
#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

/* Skip the ESP-IDF public header; this host test supplies the minimal API model. */
#define EHOME_MQTT_H

typedef void *esp_mqtt_client_handle_t;
typedef void *SemaphoreHandle_t;
typedef const char *esp_event_base_t;
typedef int esp_err_t;

typedef enum {
    MQTT_CLIENT_DISCONNECTED,
    MQTT_CLIENT_CONNECTING,
    MQTT_CLIENT_CONNECTED,
    MQTT_CLIENT_FAILED,
} mqtt_client_state_t;

typedef void (*mqtt_msg_cb_t)(const char *, const uint8_t *, size_t, void *);
typedef void (*mqtt_state_cb_t)(mqtt_client_state_t, void *);

typedef struct {
    esp_mqtt_client_handle_t client;
    mqtt_client_state_t state;
    mqtt_msg_cb_t msg_cb;
    void *msg_cb_ctx;
    mqtt_state_cb_t state_cb;
    void *state_cb_ctx;
    SemaphoreHandle_t mutex;
} mqtt_client_ctx_t;

typedef struct {
    struct { struct { const char *uri; } address; } broker;
    struct { const char *client_id; } credentials;
    struct { int keepalive; bool disable_clean_session; } session;
} esp_mqtt_client_config_t;

typedef struct {
    int event_id;
    const char *topic;
    const char *data;
    int data_len;
} esp_mqtt_event_t;
typedef esp_mqtt_event_t *esp_mqtt_event_handle_t;

#define ESP_OK 0
#define ESP_EVENT_ANY_ID (-1)
typedef enum {
    MQTT_EVENT_ERROR = 0,
    MQTT_EVENT_CONNECTED = 1,
    MQTT_EVENT_DISCONNECTED = 2,
    MQTT_EVENT_SUBSCRIBED = 3,
    MQTT_EVENT_UNSUBSCRIBED = 4,
    MQTT_EVENT_PUBLISHED = 5,
    MQTT_EVENT_DATA = 6,
    MQTT_EVENT_BEFORE_CONNECT = 7,
    MQTT_EVENT_DELETED = 8,
    MQTT_USER_EVENT = 9,
} esp_mqtt_event_id_t;

_Static_assert(MQTT_EVENT_ERROR == 0, "esp-mqtt ERROR event ID drifted");
_Static_assert(MQTT_EVENT_DATA == 6, "esp-mqtt DATA event ID drifted");
_Static_assert(MQTT_USER_EVENT == 9, "esp-mqtt USER event ID drifted");
#define CONFIG_COLLECTOR_MQTT_BROKER_URL "mqtt://host-test"
#define portMAX_DELAY 0

static int published_qos = -1;
static int publish_calls;
static bool capture_suppressed;
static unsigned captured_log_count;
static unsigned info_log_count;
static unsigned warn_log_count;
static unsigned error_log_count;
static char captured_message[96];
static unsigned msg_cb_count;
static char msg_cb_topic[64];
static uint8_t msg_cb_data[64];
static size_t msg_cb_len;
static void *msg_cb_context;

void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)tag;
    if (capture_suppressed) {
        return;
    }
    va_list args;
    va_start(args, format);
    (void)vsnprintf(captured_message, sizeof(captured_message), format, args);
    va_end(args);
    captured_log_count++;
    info_log_count += level == 'I';
    warn_log_count += level == 'W';
    error_log_count += level == 'E';
}

static inline SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (void *)1; }
static inline void xSemaphoreTake(SemaphoreHandle_t mutex, int ticks) { (void)mutex; (void)ticks; }
static inline void xSemaphoreGive(SemaphoreHandle_t mutex) { (void)mutex; }
static inline const char *esp_err_to_name(esp_err_t err) { (void)err; return "host-test"; }
static inline esp_mqtt_client_handle_t esp_mqtt_client_init(const esp_mqtt_client_config_t *cfg)
{ (void)cfg; return (void *)1; }
static inline esp_err_t esp_mqtt_client_register_event(esp_mqtt_client_handle_t client,
                                                        int event_id, void (*handler)(void *, esp_event_base_t, int32_t, void *),
                                                        void *args)
{ (void)client; (void)event_id; (void)handler; (void)args; return ESP_OK; }
static inline esp_err_t esp_mqtt_client_start(esp_mqtt_client_handle_t client)
{ (void)client; return ESP_OK; }
static inline esp_err_t esp_mqtt_client_stop(esp_mqtt_client_handle_t client)
{ (void)client; return ESP_OK; }
static inline esp_err_t esp_mqtt_client_destroy(esp_mqtt_client_handle_t client)
{ (void)client; return ESP_OK; }
static inline int esp_mqtt_client_publish(esp_mqtt_client_handle_t client, const char *topic,
                                          const char *data, int len, int qos, int retain)
{
    (void)client; (void)topic; (void)data; (void)len; (void)retain;
    published_qos = qos;
    publish_calls++;
    return 1;
}
static inline int esp_mqtt_client_subscribe(esp_mqtt_client_handle_t client, const char *topic, int qos)
{ (void)client; (void)topic; (void)qos; return 1; }
#include "../components/ehome_mqtt/ehome_mqtt.c"

static int failures;

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL: %s\n", message); \
        failures++; \
    } \
} while (0)

static void record_message(const char *topic, const uint8_t *data, size_t len, void *ctx)
{
    msg_cb_count++;
    (void)snprintf(msg_cb_topic, sizeof(msg_cb_topic), "%s", topic);
    msg_cb_len = len < sizeof(msg_cb_data) ? len : sizeof(msg_cb_data);
    memcpy(msg_cb_data, data, msg_cb_len);
    msg_cb_context = ctx;
}

int main(void)
{
    const uint8_t log_stream_frame[] = {0x1D, 0x08, 0x01};
    const uint8_t hello_frame[] = {0x01};
    const uint8_t data_report_frame[] = {0x18, 0x08, 0x01};

    CHECK(mqtt_publish_qos_for_frame(log_stream_frame, sizeof(log_stream_frame)) == 0,
          "MsgLogStream must use QoS 0 to avoid publish acknowledgements");
    CHECK(mqtt_publish_qos_for_frame(hello_frame, sizeof(hello_frame)) == 1,
          "Hello must keep QoS 1");
    CHECK(mqtt_publish_qos_for_frame(data_report_frame, sizeof(data_report_frame)) == 1,
          "DataReport must keep QoS 1");
    CHECK(mqtt_publish_qos_for_frame(NULL, 0) == 1,
          "empty input must default to reliable QoS 1");

    s_ctx.client = (void *)1;
    s_ctx.state = MQTT_CLIENT_CONNECTED;
    capture_suppressed = true;
    CHECK(mqtt_client_publish_impl(log_stream_frame, sizeof(log_stream_frame)),
          "MsgLogStream publish failed");
    capture_suppressed = false;
    CHECK(publish_calls == 1 && published_qos == 0,
          "publish path must pass QoS 0 for MsgLogStream");

    /* esp-mqtt can deliver PUBLISHED after publish() returns, including QoS 0.
     * This event runs after log-stream suppression has been restored, so any
     * handler log here is captured and can become publish -> event -> publish. */
    esp_mqtt_event_t published_event = { .event_id = MQTT_EVENT_PUBLISHED };
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_PUBLISHED, &published_event);
    CHECK(captured_log_count == 0,
          "asynchronous MQTT_EVENT_PUBLISHED must not create a new remote capture");

    esp_mqtt_event_t connected_event = { .event_id = MQTT_EVENT_CONNECTED };
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_CONNECTED, &connected_event);
    CHECK(info_log_count >= 1,
          "CONNECTED must retain a broker diagnostic");

    esp_mqtt_event_t disconnected_event = { .event_id = MQTT_EVENT_DISCONNECTED };
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_DISCONNECTED, &disconnected_event);
    CHECK(warn_log_count == 1, "DISCONNECTED must retain a warning diagnostic");

    esp_mqtt_event_t error_event = { .event_id = MQTT_EVENT_ERROR };
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_ERROR, &error_event);
    CHECK(error_log_count == 1, "ERROR must retain an error diagnostic");

    int callback_context = 42;
    mqtt_client_register_msg_cb(record_message, &callback_context);
    const char data_topic[] = "nodes/test/down";
    const char data_payload[] = {0x01, 0x00, 0x7f, (char)0xff};
    esp_mqtt_event_t data_event = {
        .event_id = MQTT_EVENT_DATA,
        .topic = data_topic,
        .data = data_payload,
        .data_len = (int)sizeof(data_payload),
    };
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_DATA, &data_event);
    CHECK(msg_cb_count == 1, "real MQTT_EVENT_DATA ID must dispatch msg_cb exactly once");
    CHECK(strcmp(msg_cb_topic, data_topic) == 0, "DATA callback topic mismatch");
    CHECK(msg_cb_len == sizeof(data_payload), "DATA callback length mismatch");
    CHECK(memcmp(msg_cb_data, data_payload, sizeof(data_payload)) == 0,
          "DATA callback payload mismatch");
    CHECK(msg_cb_context == &callback_context, "DATA callback context mismatch");

    s_ctx.state = MQTT_CLIENT_CONNECTED;
    CHECK(mqtt_client_publish_impl(hello_frame, sizeof(hello_frame)),
          "Hello publish failed");
    CHECK(publish_calls == 2 && published_qos == 1,
          "publish path must pass QoS 1 for non-log frames");

    if (failures != 0) {
        return 1;
    }
    puts("mqtt_event_tests: all tests passed");
    return 0;
}