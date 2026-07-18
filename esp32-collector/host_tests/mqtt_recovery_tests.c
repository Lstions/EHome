#include <stdbool.h>
#include <stdarg.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

typedef void *TaskHandle_t;
#include "mqtt_supervisor_notify.h"

void host_test_log_record(char level, const char *tag, const char *format, ...)
{ (void)level; (void)tag; (void)format; }

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

esp_err_t mqtt_client_request_start(void);

#define ESP_OK 0
#define ESP_FAIL (-1)
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

#define CONFIG_COLLECTOR_MQTT_BROKER_URL "mqtt://host-test"
#define CONFIG_MQTT_SUBSCRIPTION_TIMEOUT_SEC 10
#define CONFIG_MQTT_CONNECTING_TIMEOUT_SEC 30
#define portMAX_DELAY 0

static int mock_client_init_calls;
static int mock_client_destroy_calls;
static int mock_client_start_calls;
static int mock_client_stop_calls;
static int mock_client_reconnect_calls;
static int mock_register_event_calls;
static bool mock_start_should_fail;
static bool mock_init_should_fail;
static bool mock_reconnect_should_fail;
static int mock_mutex_create_calls;
static int mock_mutex_fail_call;
static int mock_batch_subscribe_calls;
static int mock_batch_result;
static int mock_next_batch_msg_id;
static int mock_batch_size;
static char mock_batch_topics[2][72];
static int mock_batch_qos[2];
static int mock_publish_calls;
static int mock_enqueue_calls;
static bool mock_disable_auto_reconnect;
static bool mock_request_start_during_stop;
static int mock_init_delta_during_stop;
static unsigned mock_notify_calls;
static TaskHandle_t mock_notified_task;

static void mock_notify(void *task)
{
    mock_notify_calls++;
    mock_notified_task = task;
}

static SemaphoreHandle_t xSemaphoreCreateMutex(void)
{
    mock_mutex_create_calls++;
    if (mock_mutex_fail_call == mock_mutex_create_calls) return NULL;
    return (void *)1;
}
static void xSemaphoreTake(SemaphoreHandle_t mutex, int ticks) { (void)mutex; (void)ticks; }
static void xSemaphoreGive(SemaphoreHandle_t mutex) { (void)mutex; }
static void vSemaphoreDelete(SemaphoreHandle_t mutex) { (void)mutex; }
static const char *esp_err_to_name(esp_err_t err) { (void)err; return "host-test"; }

static esp_mqtt_client_handle_t esp_mqtt_client_init(const esp_mqtt_client_config_t *cfg)
{
    mock_client_init_calls++;
    mock_disable_auto_reconnect = cfg->network.disable_auto_reconnect;
    if (mock_init_should_fail) return NULL;
    return (void *)(uintptr_t)(100 + mock_client_init_calls);
}

static esp_err_t esp_mqtt_client_register_event(esp_mqtt_client_handle_t client,
                                                 int event_id,
                                                 void (*handler)(void *, esp_event_base_t, int32_t, void *),
                                                 void *args)
{
    (void)client; (void)event_id; (void)handler; (void)args;
    mock_register_event_calls++;
    return ESP_OK;
}

static esp_err_t esp_mqtt_client_start(esp_mqtt_client_handle_t client)
{
    (void)client;
    mock_client_start_calls++;
    return mock_start_should_fail ? ESP_FAIL : ESP_OK;
}

static esp_err_t esp_mqtt_client_stop(esp_mqtt_client_handle_t client)
{
    (void)client;
    mock_client_stop_calls++;
    if (mock_request_start_during_stop) {
        int before = mock_client_init_calls;
        (void)mqtt_client_request_start();
        mock_init_delta_during_stop = mock_client_init_calls - before;
    }
    return ESP_OK;
}

static esp_err_t esp_mqtt_client_destroy(esp_mqtt_client_handle_t client)
{
    (void)client;
    mock_client_destroy_calls++;
    return ESP_OK;
}

static esp_err_t esp_mqtt_client_reconnect(esp_mqtt_client_handle_t client)
{
    (void)client;
    mock_client_reconnect_calls++;
    return mock_reconnect_should_fail ? ESP_FAIL : ESP_OK;
}

static int esp_mqtt_client_subscribe_multiple(esp_mqtt_client_handle_t client,
                                               const esp_mqtt_topic_t *topics, int size)
{
    (void)client;
    mock_batch_subscribe_calls++;
    mock_batch_size = size;
    for (int i = 0; i < size && i < 2; ++i) {
        (void)snprintf(mock_batch_topics[i], sizeof(mock_batch_topics[i]), "%s", topics[i].filter);
        mock_batch_qos[i] = topics[i].qos;
    }
    if (mock_batch_result < 0) return mock_batch_result;
    return mock_next_batch_msg_id++;
}

static int esp_mqtt_client_publish(esp_mqtt_client_handle_t client, const char *topic,
                                   const char *data, int len, int qos, int retain)
{
    (void)client; (void)topic; (void)data; (void)len; (void)qos; (void)retain;
    mock_publish_calls++;
    return 1;
}
static int esp_mqtt_client_enqueue(esp_mqtt_client_handle_t client, const char *topic,
                                   const char *data, int len, int qos, int retain, bool store)
{
    (void)client; (void)topic; (void)data; (void)len; (void)qos; (void)retain; (void)store;
    mock_enqueue_calls++;
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

static unsigned ready_count;
static uint32_t ready_generation;
static void record_ready(uint32_t generation, void *ctx)
{
    (void)ctx;
    ready_count++;
    ready_generation = generation;
}

static void reset_mocks(void)
{
    memset(&s_ctx, 0, sizeof(s_ctx));
    s_ctx.state = MQTT_CLIENT_DISCONNECTED;
    s_ctx.subscription_phase = MQTT_SUB_IDLE;
    s_ctx.subscription_msg_id = -1;
    s_lifecycle_mutex = NULL;
    s_init_failed = false;
    s_shutdown_requested = false;
    s_start_requested = false;
    s_stop_requested = false;
    s_client_starting = false;
    s_retire_in_progress = false;
    s_active_operations = 0;
    s_next_client_generation = 0;
    s_reconnect_attempts = 0;
    s_reconnect_deadline_us = 0;
    s_connecting_start_us = 0;
    s_connecting_active = false;
    s_subscription_retire_pending = false;
    mock_client_init_calls = 0;
    mock_client_destroy_calls = 0;
    mock_client_start_calls = 0;
    mock_client_stop_calls = 0;
    mock_client_reconnect_calls = 0;
    mock_register_event_calls = 0;
    mock_start_should_fail = false;
    mock_init_should_fail = false;
    mock_reconnect_should_fail = false;
    mock_mutex_create_calls = 0;
    mock_mutex_fail_call = 0;
    mock_batch_subscribe_calls = 0;
    mock_batch_result = 0;
    mock_next_batch_msg_id = 100;
    mock_batch_size = 0;
    memset(mock_batch_topics, 0, sizeof(mock_batch_topics));
    memset(mock_batch_qos, 0, sizeof(mock_batch_qos));
    mock_publish_calls = 0;
    mock_enqueue_calls = 0;
    mock_disable_auto_reconnect = false;
    mock_request_start_during_stop = false;
    mock_init_delta_during_stop = -1;
    mock_notify_calls = 0;
    mock_notified_task = NULL;
    mock_now_us = 0;
    ready_count = 0;
    ready_generation = 0;
}

static void initialize_and_start(void)
{
    mqtt_client_init();
    mqtt_client_register_ready_cb(record_ready, NULL);
    (void)mqtt_client_request_start();
    mqtt_client_owner_step(true);
}

static void connect_current(void)
{
    esp_mqtt_event_t event = {
        .event_id = MQTT_EVENT_CONNECTED,
        .client = s_ctx.client,
    };
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_CONNECTED, &event);
}

static void send_subscription(void)
{
    mqtt_client_owner_step(true);
}

static void ack_current(const uint8_t reasons[2], esp_mqtt_error_codes_t *error)
{
    esp_mqtt_event_t event = {
        .event_id = MQTT_EVENT_SUBSCRIBED,
        .client = s_ctx.client,
        .msg_id = s_ctx.subscription_msg_id,
        .data = (char *)reasons,
        .data_len = 2,
        .error_handle = error,
    };
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_SUBSCRIBED, &event);
}

static void test_owner_start_and_batch_ready(void)
{
    reset_mocks();
    initialize_and_start();
    CHECK(mock_client_init_calls == 1 && mock_client_start_calls == 1,
          "start request must create and start exactly one client");
    CHECK(mock_disable_auto_reconnect,
          "ESP-MQTT automatic reconnect must remain disabled");
    mqtt_client_owner_step(true);
    CHECK(mock_client_init_calls == 1,
          "repeated owner tick while CONNECTING must not duplicate client");

    connect_current();
    mock_now_us = 1000000;
    send_subscription();
    CHECK(mock_batch_subscribe_calls == 1 && mock_batch_size == 2,
          "one generation must send exactly one batch");
    CHECK(strcmp(mock_batch_topics[0], "nodes/unknown/down") == 0 && mock_batch_qos[0] == 0,
          "batch slot zero must be down QoS0");
    CHECK(strcmp(mock_batch_topics[1], "nodes/unknown/control") == 0 && mock_batch_qos[1] == 1,
          "batch slot one must be control QoS1");
    CHECK(s_ctx.subscription_phase == MQTT_SUB_WAIT_ACK &&
          s_ctx.subscription_deadline_us == 11000000,
          "successful batch API must arm one msg_id and deadline");
    CHECK(ready_count == 0, "API success alone must not publish ready");

    esp_mqtt_error_codes_t ok = { .error_type = MQTT_ERROR_TYPE_NONE };
    const uint8_t exact[] = {0x00, 0x01};
    ack_current(exact, &ok);
    mqtt_client_owner_step(true);
    CHECK(ready_count == 1 && ready_generation == 1 &&
          s_ctx.state == MQTT_CLIENT_CONNECTED,
          "strict SUBACK must publish current generation ready");
    mqtt_client_owner_step(true);
    CHECK(ready_count == 1 && mock_batch_subscribe_calls == 1,
          "ready generation must be idempotent");
}

static void test_batch_api_failure_recreates_without_retry(void)
{
    reset_mocks();
    initialize_and_start();
    connect_current();
    mock_batch_result = -1;
    void *old = s_ctx.client;
    send_subscription();
    CHECK(mock_batch_subscribe_calls == 1,
          "negative batch result must be attempted once");
    CHECK(mock_client_destroy_calls == 1 && mock_client_init_calls == 2,
          "negative batch result must retire and create fresh client immediately");
    CHECK(s_ctx.client != NULL && s_ctx.client != old &&
          s_ctx.state == MQTT_CLIENT_CONNECTING,
          "batch failure must leave a fresh connecting client");
    mqtt_client_owner_step(true);
    CHECK(mock_batch_subscribe_calls == 1,
          "failed client must never retry the batch");
}

static void test_timeout_recreates_without_pair_retry(void)
{
    reset_mocks();
    initialize_and_start();
    connect_current();
    mock_now_us = 2000000;
    send_subscription();
    int64_t deadline = s_ctx.subscription_deadline_us;
    void *old = s_ctx.client;

    mock_now_us = deadline - 1;
    mqtt_client_owner_step(true);
    CHECK(mock_batch_subscribe_calls == 1 && mock_client_destroy_calls == 0,
          "pre-deadline tick must neither retry nor retire");
    mock_now_us = deadline;
    mqtt_client_owner_step(true);
    CHECK(mock_batch_subscribe_calls == 1 && mock_client_destroy_calls == 1 &&
          mock_client_init_calls == 2 && s_ctx.client != old,
          "deadline must recreate once without same-client retry");
}

static void test_invalid_suback_recreates(void)
{
    reset_mocks();
    initialize_and_start();
    connect_current();
    send_subscription();
    esp_mqtt_error_codes_t ok = { .error_type = MQTT_ERROR_TYPE_NONE };
    const uint8_t downgraded[] = {0x00, 0x00};
    ack_current(downgraded, &ok);
    CHECK(s_ctx.subscription_phase == MQTT_SUB_FAILED,
          "QoS downgrade must commit FAILED in callback transaction");
    mqtt_client_owner_step(true);
    CHECK(mock_client_destroy_calls == 1 && mock_client_init_calls == 2,
          "FAILED phase must retire and recreate on owner tick");
}

static void test_suback_and_timeout_have_single_winner(void)
{
    esp_mqtt_error_codes_t ok = { .error_type = MQTT_ERROR_TYPE_NONE };
    const uint8_t exact[] = {0x00, 0x01};

    reset_mocks();
    initialize_and_start();
    connect_current();
    send_subscription();
    mock_now_us = s_ctx.subscription_deadline_us;
    ack_current(exact, &ok);
    mqtt_client_owner_step(true);
    CHECK(ready_count == 1 && mock_client_destroy_calls == 0,
          "SUBACK committed first must defeat simultaneous deadline");

    reset_mocks();
    initialize_and_start();
    connect_current();
    send_subscription();
    void *old = s_ctx.client;
    int old_msg_id = s_ctx.subscription_msg_id;
    mock_now_us = s_ctx.subscription_deadline_us;
    mqtt_client_owner_step(true);
    CHECK(mock_client_destroy_calls == 1 && s_ctx.client != old,
          "deadline committed first must recreate current client");
    esp_mqtt_event_t stale = {
        .event_id = MQTT_EVENT_SUBSCRIBED,
        .client = old,
        .msg_id = old_msg_id,
        .data = (char *)exact,
        .data_len = 2,
        .error_handle = &ok,
    };
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_SUBSCRIBED, &stale);
    CHECK(ready_count == 0 && s_ctx.subscription_phase == MQTT_SUB_IDLE,
          "late SUBACK from retired client must not revive failed generation");
}

static void test_retire_pending_survives_failed_drain(void)
{
    reset_mocks();
    initialize_and_start();
    connect_current();
    send_subscription();
    void *old = s_ctx.client;
    mqtt_operation_t inflight;
    CHECK(begin_operation(&inflight, false),
          "test setup must begin an operation before subscription retire");
    mock_now_us = s_ctx.subscription_deadline_us;
    mqtt_client_owner_step(true);
    CHECK(s_subscription_retire_pending && s_ctx.client == old &&
          mock_client_destroy_calls == 0 && mock_client_reconnect_calls == 0,
          "failed drain must retain client and retire_pending without reconnect");

    unsigned active_before_probe = s_active_operations;
    mqtt_operation_t blocked;
    bool began_during_retire = begin_operation(&blocked, false);
    CHECK(!began_during_retire && s_active_operations == active_before_probe,
          "retire_pending must keep the shutdown fence closed to new operations");
    if (began_during_retire) (void)finish_operation(&blocked);

    /* Public publish must be rejected by the shutdown fence even if stale
     * transport state momentarily appears connected. */
    s_ctx.state = MQTT_CLIENT_CONNECTED;
    s_ctx.transport_connected = true;
    const uint8_t frame[] = {0x01};
    CHECK(!mqtt_client_publish_impl(frame, sizeof(frame)) &&
          mock_publish_calls == 0 && mock_enqueue_calls == 0 &&
          s_active_operations == active_before_probe,
          "retire_pending must reject publish without entering ESP-MQTT");
    s_ctx.transport_connected = false;
    CHECK(finish_operation(&inflight) && s_active_operations == 0,
          "an operation admitted before retire must still be able to finish");

    mqtt_client_owner_step(true);
    CHECK(!s_subscription_retire_pending && mock_client_destroy_calls == 1 &&
          mock_client_init_calls == 2 && s_ctx.client != old,
          "next owner tick must finish pending retire and recreate once");
}

static void test_new_connection_generation_resets_subscription(void)
{
    reset_mocks();
    initialize_and_start();
    connect_current();
    send_subscription();
    int old_msg_id = s_ctx.subscription_msg_id;
    uint32_t old_generation = s_ctx.connection_generation;

    connect_current();
    CHECK(s_ctx.connection_generation == old_generation + 1 &&
          s_ctx.subscription_phase == MQTT_SUB_NEEDS_SEND &&
          s_ctx.subscription_deadline_us == 0,
          "new CONNECTED must reset one phase for a new generation");
    esp_mqtt_error_codes_t ok = { .error_type = MQTT_ERROR_TYPE_NONE };
    const uint8_t exact[] = {0x00, 0x01};
    esp_mqtt_event_t old_ack = {
        .event_id = MQTT_EVENT_SUBSCRIBED,
        .client = s_ctx.client,
        .msg_id = old_msg_id,
        .data = (char *)exact,
        .data_len = 2,
        .error_handle = &ok,
    };
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_SUBSCRIBED, &old_ack);
    CHECK(s_ctx.subscription_phase == MQTT_SUB_NEEDS_SEND,
          "old msg_id before new batch must be ignored");
    send_subscription();
    CHECK(mock_batch_subscribe_calls == 2 && s_ctx.subscription_msg_id != old_msg_id,
          "new generation must send one fresh batch");
}

static void test_disconnect_recovery_and_connecting_timeout(void)
{
    reset_mocks();
    initialize_and_start();
    connect_current();
    esp_mqtt_event_t disconnected = {
        .event_id = MQTT_EVENT_DISCONNECTED,
        .client = s_ctx.client,
    };
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_DISCONNECTED, &disconnected);
    mqtt_client_owner_step(true);
    int64_t first_deadline = s_reconnect_deadline_us;
    CHECK(mock_client_reconnect_calls == 0 &&
          first_deadline == 1000000 &&
          s_ctx.state == MQTT_CLIENT_DISCONNECTED,
          "first DISCONNECTED must arm one-second owner deadline");

    /* A callback storm may wake the owner repeatedly, but neither callbacks
     * nor pre-deadline owner steps may move the deadline or reconnect. */
    for (int i = 0; i < 100; ++i) {
        mock_now_us = i * 9000;
        mqtt_event_handler(NULL, NULL, MQTT_EVENT_DISCONNECTED, &disconnected);
        mqtt_client_owner_step(true);
    }
    CHECK(mock_client_reconnect_calls == 0 &&
          s_reconnect_deadline_us == first_deadline,
          "continuous DISCONNECTED events must not bypass or slide deadline");

    mock_now_us = first_deadline - 1;
    mqtt_client_owner_step(true);
    CHECK(mock_client_reconnect_calls == 0,
          "owner must not reconnect one microsecond before deadline");
    mock_now_us = first_deadline;
    mqtt_client_owner_step(true);
    CHECK(mock_client_reconnect_calls == 1 && s_reconnect_attempts == 1 &&
          s_ctx.state == MQTT_CLIENT_CONNECTING && s_connecting_active,
          "deadline must admit exactly one inflight reconnect");
    mqtt_client_owner_step(true);
    CHECK(mock_client_reconnect_calls == 1,
          "owner tick during inflight reconnect must not duplicate action");

    mqtt_event_handler(NULL, NULL, MQTT_EVENT_DISCONNECTED, &disconnected);
    mqtt_client_owner_step(true);
    int64_t second_deadline = s_reconnect_deadline_us;
    CHECK(second_deadline == first_deadline + 2000000 &&
          mock_client_reconnect_calls == 1,
          "failed first attempt must arm two-second exponential deadline");
    mock_now_us = second_deadline;
    mqtt_client_owner_step(true);
    CHECK(mock_client_reconnect_calls == 2 && s_reconnect_attempts == 2,
          "second deadline must admit one second reconnect action");

    /* A strict READY commit is the recovery boundary that resets both retry
     * count and deadline. */
    connect_current();
    send_subscription();
    esp_mqtt_error_codes_t ok = { .error_type = MQTT_ERROR_TYPE_NONE };
    const uint8_t exact[] = {0x00, 0x01};
    ack_current(exact, &ok);
    mqtt_client_owner_step(true);
    CHECK(ready_count == 1 && s_reconnect_attempts == 0 &&
          s_reconnect_deadline_us == 0 && !s_connecting_active,
          "READY must reset reconnect backoff state");
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_DISCONNECTED, &disconnected);
    mqtt_client_owner_step(true);
    CHECK(s_reconnect_deadline_us == mock_now_us + 1000000,
          "post-recovery disconnect must restart at base backoff");

    reset_mocks();
    initialize_and_start();
    connect_current();
    mqtt_event_handler(NULL, NULL, MQTT_EVENT_DISCONNECTED, &disconnected);
    mqtt_client_owner_step(true);
    mock_now_us = s_reconnect_deadline_us;
    mqtt_client_owner_step(true);

    s_connecting_active = true;
    s_connecting_start_us = mock_now_us - 40000000;
    int init_before = mock_client_init_calls;
    mqtt_client_owner_step(true);
    CHECK(mock_client_destroy_calls == 1 && mock_client_init_calls == init_before + 1,
          "stuck CONNECTING must retire and recreate");
}

static void test_start_failures_and_mutex_failure(void)
{
    reset_mocks();
    mqtt_client_init();
    mock_start_should_fail = true;
    (void)mqtt_client_request_start();
    mqtt_client_owner_step(true);
    CHECK(s_ctx.client == NULL && s_ctx.state == MQTT_CLIENT_FAILED &&
          mock_client_destroy_calls == 1,
          "start failure must destroy partial client and fail closed");

    reset_mocks();
    mqtt_client_init();
    mock_init_should_fail = true;
    (void)mqtt_client_request_start();
    mqtt_client_owner_step(true);
    CHECK(s_ctx.client == NULL && s_ctx.state == MQTT_CLIENT_FAILED,
          "init failure must leave no client and FAILED state");

    reset_mocks();
    mock_mutex_fail_call = 2;
    mqtt_client_init();
    mqtt_client_owner_step(true);
    CHECK(s_init_failed && mock_client_init_calls == 0 &&
          s_ctx.state == MQTT_CLIENT_FAILED,
          "mutex initialization failure must block lifecycle creation");
}

static void test_owner_stop_and_operation_drain_barrier(void)
{
    reset_mocks();
    initialize_and_start();
    void *old = s_ctx.client;
    s_active_operations = 1;
    mqtt_client_owner_step(false);
    CHECK(s_ctx.client == old && mock_client_destroy_calls == 0,
          "network-down stop must not destroy while operation is active");
    s_active_operations = 0;
    mqtt_client_owner_step(false);
    CHECK(s_ctx.client == NULL && mock_client_destroy_calls == 1,
          "owner must retire after operation drain");

    reset_mocks();
    initialize_and_start();
    mock_request_start_during_stop = true;
    mqtt_client_owner_step(false);
    CHECK(mock_init_delta_during_stop == 0 && s_ctx.client == NULL,
          "callback request during stop must not pierce retire transaction");
    mqtt_client_owner_step(true);
    CHECK(mock_client_init_calls == 2 && s_ctx.client != NULL,
          "queued start request must be consumed by a later owner step");
}

static void test_stop_clears_retire_pending(void)
{
    reset_mocks();
    initialize_and_start();
    s_subscription_retire_pending = true;
    mqtt_client_owner_step(false);
    CHECK(!s_subscription_retire_pending && s_ctx.client == NULL,
          "owner stop must clear subscription retire continuation");
}

static void test_explicit_stop_survives_failed_drain(void)
{
    reset_mocks();
    initialize_and_start();
    void *old = s_ctx.client;
    s_ctx.state = MQTT_CLIENT_CONNECTED;
    s_ctx.transport_connected = true;
    mqtt_operation_t inflight;
    CHECK(begin_operation(&inflight, false),
          "test setup must begin an operation before explicit stop");
    (void)mqtt_client_request_stop();
    mqtt_client_owner_step(true);
    CHECK(s_ctx.client == old && mock_client_destroy_calls == 0,
          "explicit stop must not destroy with an active operation");

    unsigned active_before_probe = s_active_operations;
    mqtt_operation_t blocked;
    bool began_during_stop = begin_operation(&blocked, false);
    CHECK(!began_during_stop && s_active_operations == active_before_probe,
          "failed explicit stop drain must keep new operations fenced out");
    if (began_during_stop) (void)finish_operation(&blocked);

    const uint8_t frame[] = {0x01};
    CHECK(!mqtt_client_publish_impl(frame, sizeof(frame)) &&
          mock_publish_calls == 0 && mock_enqueue_calls == 0 &&
          s_active_operations == active_before_probe,
          "failed explicit stop drain must reject publish before ESP-MQTT");
    CHECK(finish_operation(&inflight) && s_active_operations == 0,
          "pre-stop operation must still finish while shutdown remains armed");

    mqtt_client_owner_step(true);
    CHECK(s_ctx.client == NULL && mock_client_destroy_calls == 1,
          "explicit stop continuation must retry after the operation drains");

    (void)mqtt_client_request_start();
    mqtt_client_owner_step(true);
    CHECK(s_ctx.client != NULL && s_ctx.client != old &&
          mock_client_init_calls == 2 && mock_client_start_calls == 2,
          "a later explicit start must reopen shutdown and create a fresh client");
}

static void test_notification_helper_is_non_owning(void)
{
    reset_mocks();
    TaskHandle_t task = (TaskHandle_t)(uintptr_t)0x1234;
    mqtt_supervisor_notify_if_running(NULL, mock_notify);
    CHECK(mock_notify_calls == 0, "missing supervisor must not be notified");
    mqtt_supervisor_notify_if_running(task, mock_notify);
    CHECK(mock_notify_calls == 1 && mock_notified_task == task &&
          mock_client_init_calls == 0 && mock_client_stop_calls == 0,
          "notification seam must only wake an existing owner");
}

int main(void)
{
    test_notification_helper_is_non_owning();
    test_owner_start_and_batch_ready();
    test_batch_api_failure_recreates_without_retry();
    test_timeout_recreates_without_pair_retry();
    test_invalid_suback_recreates();
    test_suback_and_timeout_have_single_winner();
    test_retire_pending_survives_failed_drain();
    test_new_connection_generation_resets_subscription();
    test_disconnect_recovery_and_connecting_timeout();
    test_start_failures_and_mutex_failure();
    test_owner_stop_and_operation_drain_barrier();
    test_stop_clears_retire_pending();
    test_explicit_stop_survives_failed_drain();

    if (failures != 0) {
        fprintf(stderr, "%d MQTT recovery test(s) failed\n", failures);
        return 1;
    }
    puts("mqtt_recovery_tests: all tests passed");
    return 0;
}
