#include <stdbool.h>
#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

/* Minimal ESP-IDF MQTT model. The SUT is included below so static state and
 * transition helpers can be exercised without adding production test hooks. */
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

typedef enum {
    MQTT_SUB_IDLE,
    MQTT_SUB_NEEDS_SEND,
    MQTT_SUB_WAIT_ACK,
    MQTT_SUB_SUCCEEDED,
    MQTT_SUB_FAILED,
} mqtt_subscription_phase_t;

typedef enum {
    MQTT_ERROR_TYPE_NONE = 0,
    MQTT_ERROR_TYPE_TCP_TRANSPORT,
    MQTT_ERROR_TYPE_CONNECTION_REFUSED,
    MQTT_ERROR_TYPE_SUBSCRIBE_FAILED,
} esp_mqtt_error_type_t;

typedef struct {
    esp_mqtt_error_type_t error_type;
} esp_mqtt_error_codes_t;

typedef struct {
    const char *filter;
    int qos;
} esp_mqtt_topic_t;

typedef void (*mqtt_msg_cb_t)(const char *, const uint8_t *, size_t, void *);
typedef void (*mqtt_state_cb_t)(mqtt_client_state_t, void *);
typedef void (*mqtt_ready_cb_t)(uint32_t, void *);
typedef void (*mqtt_transport_cb_t)(uint32_t, void *);
typedef void (*mqtt_owner_wake_cb_t)(void *);

typedef struct {
    esp_mqtt_client_handle_t client;
    mqtt_client_state_t state;
    mqtt_msg_cb_t msg_cb;
    void *msg_cb_ctx;
    mqtt_state_cb_t state_cb;
    void *state_cb_ctx;
    mqtt_ready_cb_t ready_cb;
    void *ready_cb_ctx;
    mqtt_transport_cb_t transport_cb;
    void *transport_cb_ctx;
    mqtt_owner_wake_cb_t owner_wake_cb;
    void *owner_wake_cb_ctx;
    SemaphoreHandle_t mutex;
    bool transport_connected;
    bool disconnected_event_pending;
    uint32_t connection_generation;
    uint32_t subscription_generation;
    uint32_t client_generation;
    mqtt_subscription_phase_t subscription_phase;
    int subscription_msg_id;
    int64_t subscription_deadline_us;
} mqtt_client_ctx_t;

typedef struct {
    struct { struct { const char *uri; } address; } broker;
    struct { const char *client_id; } credentials;
    struct { int keepalive; bool disable_clean_session; } session;
    struct { bool disable_auto_reconnect; } network;
} esp_mqtt_client_config_t;

typedef struct {
    int event_id;
    int msg_id;
    esp_mqtt_client_handle_t client;
    char *topic;
    char *data;
    int data_len;
    esp_mqtt_error_codes_t *error_handle;
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
#define CONFIG_COLLECTOR_MQTT_BROKER_URL "mqtt://host-test"
#define CONFIG_MQTT_SUBSCRIPTION_TIMEOUT_SEC 10
#define portMAX_DELAY 0

static int published_qos;
static int enqueued_qos;
static int publish_calls;
static int enqueue_calls;
static bool enqueued_store;
static int batch_subscribe_calls;
static int next_batch_msg_id = 100;
static char batch_topics[2][72];
static int batch_qos[2];
static int batch_size;
static unsigned captured_log_count;
static unsigned info_log_count;
static unsigned warn_log_count;
static unsigned error_log_count;
static unsigned msg_cb_count;
static char msg_cb_topic[64];
static uint8_t msg_cb_data[64];
static size_t msg_cb_len;
static unsigned ready_cb_count;
static uint32_t ready_generations[4];
static unsigned transport_cb_count;
static uint32_t transport_generation;
static unsigned owner_wake_cb_count;

void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)tag;
    (void)format;
    captured_log_count++;
    info_log_count += level == 'I';
    warn_log_count += level == 'W';
    error_log_count += level == 'E';
}

static inline SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (void *)1; }
static inline void xSemaphoreTake(SemaphoreHandle_t mutex, int ticks) { (void)mutex; (void)ticks; }
static inline void xSemaphoreGive(SemaphoreHandle_t mutex) { (void)mutex; }
static inline void vSemaphoreDelete(SemaphoreHandle_t mutex) { (void)mutex; }
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
static inline esp_err_t esp_mqtt_client_reconnect(esp_mqtt_client_handle_t client)
{ (void)client; return ESP_OK; }
static inline int esp_mqtt_client_subscribe_multiple(esp_mqtt_client_handle_t client,
                                                      const esp_mqtt_topic_t *topics, int size)
{
    (void)client;
    batch_subscribe_calls++;
    batch_size = size;
    for (int i = 0; i < size && i < 2; ++i) {
        (void)snprintf(batch_topics[i], sizeof(batch_topics[i]), "%s", topics[i].filter);
        batch_qos[i] = topics[i].qos;
    }
    return next_batch_msg_id++;
}
static inline int esp_mqtt_client_publish(esp_mqtt_client_handle_t client, const char *topic,
                                          const char *data, int len, int qos, int retain)
{
    (void)client; (void)topic; (void)data; (void)len; (void)retain;
    published_qos = qos;
    publish_calls++;
    return 1;
}
static inline int esp_mqtt_client_enqueue(esp_mqtt_client_handle_t client, const char *topic,
                                          const char *data, int len, int qos, int retain, bool store)
{
    (void)client; (void)topic; (void)data; (void)len; (void)retain;
    enqueued_qos = qos;
    enqueued_store = store;
    enqueue_calls++;
    return 1;
}

#define EHOME_MQTT_HOST_TEST
static int64_t mock_now_us;
int64_t esp_timer_get_time(void) { return mock_now_us; }

#include "../components/ehome_mqtt/ehome_mqtt.c"

void vTaskDelay(uint32_t ticks) { (void)ticks; }

static int failures;
#define CHECK(condition, message) do { \
    if (!(condition)) { fprintf(stderr, "FAIL: %s\n", message); failures++; } \
} while (0)

static void record_message(const char *topic, const uint8_t *data, size_t len, void *ctx)
{
    (void)ctx;
    msg_cb_count++;
    (void)snprintf(msg_cb_topic, sizeof(msg_cb_topic), "%s", topic);
    msg_cb_len = len < sizeof(msg_cb_data) ? len : sizeof(msg_cb_data);
    memcpy(msg_cb_data, data, msg_cb_len);
}

static void record_ready(uint32_t generation, void *ctx)
{
    (void)ctx;
    if (ready_cb_count < 4) ready_generations[ready_cb_count] = generation;
    ready_cb_count++;
}

static void record_transport(uint32_t generation, void *ctx)
{
    (void)ctx;
    transport_cb_count++;
    transport_generation = generation;
}

static void record_owner_wake(void *ctx)
{
    (void)ctx;
    owner_wake_cb_count++;
}

static esp_mqtt_event_t make_suback(void *client, int msg_id, uint8_t *reasons,
                                     int reason_count, esp_mqtt_error_codes_t *error)
{
    return (esp_mqtt_event_t){
        .event_id = MQTT_EVENT_SUBSCRIBED,
        .client = client,
        .msg_id = msg_id,
        .data = (char *)reasons,
        .data_len = reason_count,
        .error_handle = error,
    };
}

static void arm_subscription_wait(int msg_id)
{
    s_ctx.transport_connected = true;
    s_ctx.connection_generation = 9;
    s_ctx.subscription_generation = 9;
    s_ctx.subscription_phase = MQTT_SUB_WAIT_ACK;
    s_ctx.subscription_msg_id = msg_id;
    s_ctx.subscription_deadline_us = 1000000;
}

static void test_strict_suback_validation(void)
{
    esp_mqtt_error_codes_t ok = { .error_type = MQTT_ERROR_TYPE_NONE };
    esp_mqtt_error_codes_t rejected = { .error_type = MQTT_ERROR_TYPE_SUBSCRIBE_FAILED };
    uint8_t exact[] = {0x00, 0x01};
    uint8_t downgraded[] = {0x00, 0x00};
    uint8_t broker_reject[] = {0x00, 0x80};

    arm_subscription_wait(77);
    esp_mqtt_event_t event = make_suback(s_ctx.client, 77, exact, 2, NULL);
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_SUBSCRIBED, &event);
    CHECK(s_ctx.subscription_phase == MQTT_SUB_FAILED,
          "matching SUBACK without error_handle must fail closed");

    arm_subscription_wait(77);
    event = make_suback(s_ctx.client, 77, exact, 2, &rejected);
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_SUBSCRIBED, &event);
    CHECK(s_ctx.subscription_phase == MQTT_SUB_FAILED,
          "SUBACK error_type must fail the phase");

    arm_subscription_wait(77);
    event = make_suback(s_ctx.client, 77, NULL, 2, &ok);
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_SUBSCRIBED, &event);
    CHECK(s_ctx.subscription_phase == MQTT_SUB_FAILED,
          "SUBACK without reason data must fail closed");

    arm_subscription_wait(77);
    event = make_suback(s_ctx.client, 77, exact, 1, &ok);
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_SUBSCRIBED, &event);
    CHECK(s_ctx.subscription_phase == MQTT_SUB_FAILED,
          "short reason array must fail closed");

    arm_subscription_wait(77);
    event = make_suback(s_ctx.client, 77, broker_reject, 2, &rejected);
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_SUBSCRIBED, &event);
    CHECK(s_ctx.subscription_phase == MQTT_SUB_FAILED,
          "broker rejection must fail closed");

    arm_subscription_wait(77);
    event = make_suback(s_ctx.client, 77, downgraded, 2, &ok);
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_SUBSCRIBED, &event);
    CHECK(s_ctx.subscription_phase == MQTT_SUB_FAILED,
          "control QoS downgrade must fail closed");

    arm_subscription_wait(77);
    event = make_suback(s_ctx.client, 78, exact, 2, &ok);
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_SUBSCRIBED, &event);
    CHECK(s_ctx.subscription_phase == MQTT_SUB_WAIT_ACK,
          "wrong msg_id must be ignored");

    event = make_suback((void *)0x55, 77, exact, 2, &ok);
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_SUBSCRIBED, &event);
    CHECK(s_ctx.subscription_phase == MQTT_SUB_WAIT_ACK,
          "stale client SUBACK must be ignored");

    event = make_suback(s_ctx.client, 77, exact, 2, &ok);
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_SUBSCRIBED, &event);
    CHECK(s_ctx.subscription_phase == MQTT_SUB_SUCCEEDED,
          "exact QoS0/QoS1 SUBACK must succeed");
}

int main(void)
{
    const uint8_t log_stream_frame[] = {0x1D, 0x08, 0x01};
    const uint8_t hello_frame[] = {0x01};
    esp_mqtt_error_codes_t ok = { .error_type = MQTT_ERROR_TYPE_NONE };
    uint8_t exact[] = {0x00, 0x01};

    mqtt_client_init();
    s_ctx.client = (void *)1;
    s_ctx.client_generation = 1;
    s_ctx.state = MQTT_CLIENT_CONNECTING;
    strlcpy(s_down_topic, "nodes/test/down", sizeof(s_down_topic));
    strlcpy(s_control_topic, "nodes/test/control", sizeof(s_control_topic));
    mqtt_client_register_ready_cb(record_ready, NULL);
    mqtt_client_register_transport_cb(record_transport, NULL);
    mqtt_client_register_owner_wake_cb(record_owner_wake, NULL);

    CHECK(mqtt_publish_qos_for_frame(log_stream_frame, sizeof(log_stream_frame)) == 0,
          "MsgLogStream must use QoS0");
    CHECK(mqtt_publish_qos_for_frame(hello_frame, sizeof(hello_frame)) == 1,
          "Hello must use QoS1");

    esp_mqtt_event_t connected = {
        .event_id = MQTT_EVENT_CONNECTED,
        .client = s_ctx.client,
    };
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_CONNECTED, &connected);
    CHECK(transport_cb_count == 1 && transport_generation == 1 &&
          s_ctx.subscription_phase == MQTT_SUB_NEEDS_SEND,
          "CONNECTED must arm subscriptions and notify transport with generation");
    CHECK(owner_wake_cb_count == 1,
          "CONNECTED must non-blockingly wake the lifecycle owner");
    mqtt_client_owner_step(true);
    CHECK(batch_subscribe_calls == 1 && batch_size == 2,
          "owner must send one batched SUBSCRIBE");
    CHECK(strcmp(batch_topics[0], "nodes/test/down") == 0 && batch_qos[0] == 0,
          "batch slot 0 must be down QoS0");
    CHECK(strcmp(batch_topics[1], "nodes/test/control") == 0 && batch_qos[1] == 1,
          "batch slot 1 must be control QoS1");
    CHECK(s_ctx.subscription_phase == MQTT_SUB_WAIT_ACK && ready_cb_count == 0,
          "API msg_id must not publish ready before SUBACK");

    esp_mqtt_event_t suback = make_suback(s_ctx.client, s_ctx.subscription_msg_id,
                                           exact, 2, &ok);
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_SUBSCRIBED, &suback);
    CHECK(owner_wake_cb_count == 2,
          "matching SUBACK must wake the lifecycle owner without doing owner work");
    mqtt_client_owner_step(true);
    CHECK(ready_cb_count == 1 && ready_generations[0] == 1,
          "strict matching SUBACK must publish generation ready once");
    CHECK(s_ctx.state == MQTT_CLIENT_CONNECTED,
          "strict SUBACK must publish CONNECTED state");

    /* Every CONNECTED event snapshots the newly incremented transport
     * generation while holding s_ctx.mutex.  This guards the callback ABI:
     * the first argument must be generation, never the callback context. */
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_CONNECTED, &connected);
    CHECK(transport_cb_count == 2 && transport_generation == 2 &&
          s_ctx.connection_generation == 2,
          "second CONNECTED must deliver generation 2 to transport callback");
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_CONNECTED, &connected);
    CHECK(transport_cb_count == 3 && transport_generation == 3 &&
          s_ctx.connection_generation == 3,
          "third CONNECTED must deliver generation 3 to transport callback");

    test_strict_suback_validation();

    s_ctx.state = MQTT_CLIENT_CONNECTED;
    s_ctx.transport_connected = true;
    CHECK(mqtt_client_publish_impl(log_stream_frame, sizeof(log_stream_frame)),
          "log stream publish failed");
    CHECK(enqueue_calls == 1 && enqueued_qos == 0 && enqueued_store && publish_calls == 0,
          "log stream must use stored QoS0 enqueue");
    CHECK(mqtt_client_publish_impl(hello_frame, sizeof(hello_frame)),
          "Hello publish failed");
    CHECK(publish_calls == 1 && published_qos == 1,
          "non-log frame must use reliable publish");

    mqtt_client_register_msg_cb(record_message, NULL);
    char topic[] = "nodes/test/down";
    char payload[] = {0x01, 0x00, 0x7f};
    esp_mqtt_event_t data = {
        .event_id = MQTT_EVENT_DATA,
        .client = s_ctx.client,
        .topic = topic,
        .data = payload,
        .data_len = (int)sizeof(payload),
    };
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_DATA, &data);
    CHECK(msg_cb_count == 1 && strcmp(msg_cb_topic, topic) == 0 &&
          msg_cb_len == sizeof(payload) && memcmp(msg_cb_data, payload, sizeof(payload)) == 0,
          "current DATA event must dispatch exact payload");
    data.client = (void *)0x55;
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_DATA, &data);
    CHECK(msg_cb_count == 1, "stale DATA event must be ignored");

    unsigned wakes_before_disconnect = owner_wake_cb_count;
    esp_mqtt_event_t disconnected = {
        .event_id = MQTT_EVENT_DISCONNECTED,
        .client = s_ctx.client,
    };
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_DISCONNECTED, &disconnected);
    CHECK(!s_ctx.transport_connected && s_ctx.subscription_phase == MQTT_SUB_IDLE,
          "DISCONNECTED must clear subscription phase");
    CHECK(owner_wake_cb_count == wakes_before_disconnect + 1,
          "DISCONNECTED must wake the lifecycle owner");
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_DISCONNECTED, &disconnected);
    CHECK(owner_wake_cb_count == wakes_before_disconnect + 1,
          "duplicate pending DISCONNECTED must coalesce owner wake");
    CHECK(info_log_count > 0 && warn_log_count > 0 && captured_log_count > 0,
          "transport diagnostics must remain visible");

    if (failures != 0) return 1;
    puts("mqtt_event_tests: all tests passed");
    return 0;
}
