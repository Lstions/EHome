#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>
#include <stdatomic.h>

#include "hello_handshake.h"
#include "hello_handshake_runtime.h"
#include "hello_handshake_state.h"
#include "frame_codec.h"
#include "msg_handler.h"
#include "freertos/task.h"
#include "rgb_led.h"

extern void handler_hello_process_ack(frame_decoder_t *dec);

static int failures;
static bool task_create_fails;
static uint32_t task_notifications;
static uint32_t pending_notifications;
static uint32_t hello_publish_count;
static uint32_t resource_report_count;
static uint32_t sync_downlink_count;
static uint8_t last_publish[512];
static size_t last_publish_len;
static bool ack_during_publish;
static bool reset_during_publish;
static bool exercise_worker_before_handle_publish;
static app_state_t fixture_state;

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL: %s\n", message); \
        failures++; \
    } \
} while (0)

BaseType_t xTaskCreate(TaskFunction_t task, const char *name, uint32_t stack_depth,
                       void *arg, UBaseType_t priority, TaskHandle_t *handle)
{
    (void)task;
    (void)name;
    (void)stack_depth;
    (void)arg;
    (void)priority;
    if (task_create_fails) {
        *handle = NULL;
        return pdFALSE;
    }
    if (exercise_worker_before_handle_publish) {
        CHECK(!hello_handshake_worker_step(0),
              "worker may run safely before creator publishes task handle");
    }
    *handle = (TaskHandle_t)(uintptr_t)1;
    return pdPASS;
}

BaseType_t xTaskNotifyGive(TaskHandle_t task)
{
    if (task == NULL) return pdFALSE;
    task_notifications++;
    pending_notifications++;
    return pdTRUE;
}

uint32_t ulTaskNotifyTake(BaseType_t clear_on_exit, TickType_t wait)
{
    (void)wait;
    uint32_t value = pending_notifications;
    if (value == 0) return 0;
    if (clear_on_exit) pending_notifications = 0;
    else pending_notifications--;
    return value;
}

void vTaskDelete(TaskHandle_t task) { (void)task; }

const char *get_firmware_version(void) { return "host"; }
const char *get_model_name(void) { return "ESP32-HOST"; }
bool config_mgr_has_manifest(void) { return false; }
uint8_t config_mgr_get_active_channel_count(void) { return 0; }
uint64_t config_mgr_get_epoch(void) { return 7; }
const char *config_mgr_get_last_known_manifest_id(void) { return "manifest-host"; }
void msg_handler_send_resource_report(void) { resource_report_count++; }
void sync_manager_start_config_timeout(void) {}
void sync_manager_on_downlink_received(uint8_t msg_type)
{
    if (msg_type == MSG_HELLO_ACK) sync_downlink_count++;
}
void rgb_led_set_state(led_state_t state) { (void)state; }

static uint32_t decode_hello_nonce(const uint8_t *data, size_t len)
{
    frame_decoder_t dec;
    frame_field_t field;
    if (frame_decoder_init(&dec, data, len) != FRAME_OK) return 0;
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == 9 && field.wire_type == WIRE_VARINT &&
            field.value.varint <= UINT32_MAX) {
            return (uint32_t)field.value.varint;
        }
    }
    return 0;
}

static void process_ack_bytes(const uint8_t *data, size_t len)
{
    frame_decoder_t dec;
    CHECK(frame_decoder_init(&dec, data, len) == FRAME_OK,
          "ACK fixture decoder must initialize");
    handler_hello_process_ack(&dec);
}

static void process_ack(uint32_t nonce, bool include_nonce,
                        uint64_t server_time, uint32_t features)
{
    uint8_t buf[64];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_HELLO_ACK);
    (void)frame_encode_varint(&enc, 1, server_time);
    (void)frame_encode_varint(&enc, 2, features);
    if (include_nonce) (void)frame_encode_varint(&enc, 3, nonce);
    process_ack_bytes(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

void msg_handler_publish(const uint8_t *data, size_t len)
{
    CHECK(len <= sizeof(last_publish), "published Hello must fit host capture");
    if (len > sizeof(last_publish)) return;
    memcpy(last_publish, data, len);
    last_publish_len = len;
    if (len == 0 || data[0] != MSG_HELLO) return;
    hello_publish_count++;

    uint32_t nonce = decode_hello_nonce(data, len);
    if (reset_during_publish) {
        reset_during_publish = false;
        hello_handshake_on_transport_connected(2);
    }
    if (ack_during_publish) {
        ack_during_publish = false;
        process_ack(nonce, true, 1234, 5);
    }
}

static void reset_fixture(void)
{
    hello_handshake_test_reset();
    task_create_fails = false;
    task_notifications = 0;
    pending_notifications = 0;
    hello_publish_count = 0;
    resource_report_count = 0;
    sync_downlink_count = 0;
    last_publish_len = 0;
    ack_during_publish = false;
    reset_during_publish = false;
    exercise_worker_before_handle_publish = false;
    msg_handler_reset_hello_ack();
}

static app_state_t *start_fixture(void)
{
    memset(&fixture_state, 0, sizeof(fixture_state));
    strcpy(fixture_state.node_id, "host-node");
    hello_handshake_start(&fixture_state);
    CHECK(hello_handshake_is_running(), "Hello supervisor task must be created");
    return &fixture_state;
}

static void connect_ready_send(uint32_t generation)
{
    hello_handshake_on_transport_connected(generation);
    hello_handshake_on_ready(generation);
    (void)hello_handshake_worker_step(0);
}

static void test_delayed_nonce1_ack_after_nonce2_armed_is_rejected(void)
{
    reset_fixture();
    app_state_t *state = start_fixture();
    (void)state;
    connect_ready_send(1);
    uint32_t nonce1 = hello_handshake_debug_armed_nonce();
    CHECK(nonce1 != 0, "first handshake send must arm non-zero nonce1");

    for (uint32_t i = 0; i < 20; i++) {
        (void)hello_handshake_worker_step(0);
    }
    uint32_t nonce2 = hello_handshake_debug_armed_nonce();
    CHECK(nonce2 != 0 && nonce2 != nonce1,
          "retry must replace nonce1 with a fresh non-zero nonce2");

    process_ack(nonce1, true, 100, 0);
    CHECK(!msg_handler_is_hello_ack_received(),
          "delayed nonce1 ACK must not complete nonce2 attempt");
    CHECK(hello_handshake_debug_armed_nonce() == nonce2,
          "stale ACK must not clear current nonce2");
    CHECK(resource_report_count == 0,
          "stale ACK must not run Hello success side effects");

    process_ack(nonce2, true, 200, 3);
    CHECK(msg_handler_is_hello_ack_received(),
          "exact nonce2 ACK must be accepted");
    (void)hello_handshake_worker_step(0);
    CHECK(resource_report_count == 1,
          "exact nonce2 ACK must complete handshake once");
    CHECK(msg_handler_get_server_time() == 200,
          "valid ACK must preserve server_time semantics");
}

static void test_ack_nonce_validation_and_legacy_sync_semantics(void)
{
    reset_fixture();
    app_state_t *state = start_fixture();
    (void)state;
    connect_ready_send(7);
    uint32_t armed = hello_handshake_debug_armed_nonce();
    uint32_t notify_before = task_notifications;

    process_ack(0, false, 11, 1);
    CHECK(!msg_handler_is_hello_ack_received(),
          "ACK without nonce must not complete new-firmware handshake");
    CHECK(task_notifications == notify_before,
          "ACK without nonce must not notify handshake worker");
    CHECK(msg_handler_get_server_time() == 11 && sync_downlink_count == 1,
          "legacy/sync ACK must retain server-time and sync downlink semantics");

    process_ack(0, true, 12, 1);
    process_ack(armed - 1U, true, 13, 1);
    process_ack(armed + 1U, true, 14, 1);
    CHECK(!msg_handler_is_hello_ack_received() &&
          task_notifications == notify_before,
          "zero, stale, and future nonce ACKs must not complete or notify");
}

static void assert_invalid_ack_does_not_notify(const uint8_t *data, size_t len,
                                                const char *message)
{
    uint32_t notify_before = task_notifications;
    uint32_t sync_before = sync_downlink_count;
    msg_handler_reset_hello_ack();
    process_ack_bytes(data, len);
    CHECK(!msg_handler_is_hello_ack_received() &&
          task_notifications == notify_before &&
          sync_downlink_count == sync_before, message);
}

static void test_ack_parser_rejects_duplicate_wrong_wire_overflow_and_malformed(void)
{
    reset_fixture();
    app_state_t *state = start_fixture();
    (void)state;
    connect_ready_send(9);
    uint32_t armed = hello_handshake_debug_armed_nonce();

    uint8_t duplicate[64];
    frame_encoder_t enc;
    frame_encoder_init(&enc, duplicate, sizeof(duplicate), MSG_HELLO_ACK);
    (void)frame_encode_varint(&enc, 3, armed);
    (void)frame_encode_varint(&enc, 3, armed);
    assert_invalid_ack_does_not_notify(duplicate, frame_encoder_size(&enc),
                                       "duplicate nonce must be rejected");

    uint8_t wrong_wire[64];
    frame_encoder_init(&enc, wrong_wire, sizeof(wrong_wire), MSG_HELLO_ACK);
    (void)frame_encode_string(&enc, 3, "nonce");
    assert_invalid_ack_does_not_notify(wrong_wire, frame_encoder_size(&enc),
                                       "wrong nonce wire type must be rejected");

    uint8_t overflow[64];
    frame_encoder_init(&enc, overflow, sizeof(overflow), MSG_HELLO_ACK);
    (void)frame_encode_varint(&enc, 3, (uint64_t)UINT32_MAX + 1ULL);
    assert_invalid_ack_does_not_notify(overflow, frame_encoder_size(&enc),
                                       "uint32 nonce overflow must be rejected");

    const uint8_t truncated[] = {MSG_HELLO_ACK, 0x18, 0x80};
    assert_invalid_ack_does_not_notify(truncated, sizeof(truncated),
                                       "truncated nonce varint must be rejected");

    const uint8_t uint64_overflow[] = {
        MSG_HELLO_ACK, 0x18,
        0x80, 0x80, 0x80, 0x80, 0x80,
        0x80, 0x80, 0x80, 0x80, 0x02,
    };
    assert_invalid_ack_does_not_notify(uint64_overflow, sizeof(uint64_overflow),
                                       "overflowing uint64 nonce must be rejected");

    const uint8_t noncanonical_zero[] = {MSG_HELLO_ACK, 0x18, 0x80, 0x00};
    assert_invalid_ack_does_not_notify(noncanonical_zero,
                                       sizeof(noncanonical_zero),
                                       "non-canonical zero nonce must be rejected");
}

static void test_hello_codec_nonce_and_legacy_omission(void)
{
    reset_fixture();
    msg_handler_send_hello("node", "fw", "model", 2, 0);
    CHECK(decode_hello_nonce(last_publish, last_publish_len) == 0,
          "ordinary sync Hello nonce zero must omit field9");

    frame_decoder_t dec;
    frame_field_t field;
    char protocol[8] = {0};
    CHECK(frame_decoder_init(&dec, last_publish, last_publish_len) == FRAME_OK,
          "legacy Hello must decode");
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == 8) {
            (void)frame_field_get_string(&field, protocol, sizeof(protocol));
        }
    }
    CHECK(strcmp(protocol, "2.6") == 0,
          "new firmware Hello must advertise protocol 2.6");

    msg_handler_send_hello("node", "fw", "model", 2, 77);
    CHECK(decode_hello_nonce(last_publish, last_publish_len) == 77,
          "handshake Hello must encode exact field9 nonce");
}

static void test_notify_before_wait_and_latest_state_coalescing(void)
{
    reset_fixture();
    app_state_t *state = start_fixture();
    (void)state;
    hello_handshake_on_transport_connected(10);
    for (uint32_t i = 0; i < 100; i++) hello_handshake_on_ready(10);
    CHECK(pending_notifications > 0,
          "notifications produced before wait must remain pending");
    (void)hello_handshake_worker_step(0);
    CHECK(hello_publish_count == 1 &&
          hello_handshake_debug_current_generation() == 10,
          "100 READY notifications must coalesce to one latest generation send");

    hello_handshake_on_transport_connected(11);
    for (uint32_t i = 0; i < 100; i++) hello_handshake_on_ready(11);
    (void)hello_handshake_worker_step(0);
    CHECK(hello_publish_count == 2 &&
          hello_handshake_debug_current_generation() == 11,
          "latest generation must replace old state without queue ordering");
}

static void test_periodic_sync_requests_coalesce_into_correlated_handshake(void)
{
    reset_fixture();
    app_state_t *state = start_fixture();
    (void)state;
    connect_ready_send(20);
    uint32_t initial_nonce = hello_handshake_debug_armed_nonce();
    CHECK(initial_nonce != 0 && hello_publish_count == 1,
          "initial generation must publish one correlated Hello");

    process_ack(initial_nonce, true, 100, 0);
    (void)hello_handshake_worker_step(0);
    CHECK(resource_report_count == 1,
          "initial correlated Hello must complete before periodic sync");

    for (uint32_t i = 0; i < 100; i++) {
        CHECK(hello_handshake_request_sync(),
              "READY generation must accept periodic sync request");
    }
    (void)hello_handshake_worker_step(0);
    uint32_t sync_nonce = hello_handshake_debug_armed_nonce();
    CHECK(hello_publish_count == 2,
          "100 coalesced sync requests must publish exactly one new Hello");
    CHECK(sync_nonce != 0 && sync_nonce != initial_nonce &&
          decode_hello_nonce(last_publish, last_publish_len) == sync_nonce,
          "periodic sync must use a fresh non-zero wire nonce");
    CHECK(hello_handshake_debug_current_generation() == 20,
          "periodic sync must remain in the current READY generation");

    (void)hello_handshake_worker_step(0);
    CHECK(hello_publish_count == 2,
          "consumed periodic request must not restart twice");
}

static void test_periodic_sync_request_is_generation_scoped(void)
{
    reset_fixture();
    app_state_t *state = start_fixture();
    (void)state;
    CHECK(!hello_handshake_request_sync(),
          "sync request before transport READY must fail closed");

    connect_ready_send(30);
    uint32_t old_nonce = hello_handshake_debug_armed_nonce();
    CHECK(hello_handshake_request_sync(),
          "current READY generation must accept sync request");
    hello_handshake_on_transport_connected(31);
    (void)hello_handshake_worker_step(0);
    CHECK(hello_publish_count == 1 &&
          hello_handshake_debug_armed_nonce() == 0,
          "transport reset must discard pending old-generation sync request");
    CHECK(!hello_handshake_request_sync(),
          "connected but not READY generation must reject sync request");

    hello_handshake_on_ready(31);
    (void)hello_handshake_worker_step(0);
    uint32_t new_nonce = hello_handshake_debug_armed_nonce();
    CHECK(hello_publish_count == 2 && new_nonce != 0 &&
          new_nonce != old_nonce,
          "new generation READY must start only its own correlated handshake");
}

static void test_periodic_sync_replaces_active_nonce_and_rejects_old_ack(void)
{
    reset_fixture();
    app_state_t *state = start_fixture();
    (void)state;
    connect_ready_send(40);
    uint32_t old_nonce = hello_handshake_debug_armed_nonce();
    CHECK(old_nonce != 0, "active handshake must arm old nonce");

    CHECK(hello_handshake_request_sync(),
          "active READY handshake must accept periodic sync request");
    (void)hello_handshake_worker_step(0);
    uint32_t new_nonce = hello_handshake_debug_armed_nonce();
    CHECK(hello_publish_count == 2 && new_nonce != 0 &&
          new_nonce != old_nonce,
          "periodic sync must replace active nonce with a fresh nonce");

    process_ack(old_nonce, true, 401, 0);
    CHECK(!msg_handler_is_hello_ack_received() &&
          hello_handshake_debug_armed_nonce() == new_nonce &&
          resource_report_count == 0,
          "ACK for replaced nonce must not complete periodic handshake");
    process_ack(new_nonce, true, 402, 0);
    (void)hello_handshake_worker_step(0);
    CHECK(resource_report_count == 1 &&
          msg_handler_get_server_time() == 402,
          "ACK for replacement nonce must complete periodic handshake");
}

static void test_ack_and_reset_during_publish_interleavings(void)
{
    reset_fixture();
    app_state_t *state = start_fixture();
    (void)state;
    ack_during_publish = true;
    connect_ready_send(1);
    CHECK(pending_notifications > 0,
          "ACK during publish must notify before worker waits again");
    (void)hello_handshake_worker_step(0);
    CHECK(resource_report_count == 1,
          "ACK armed before publish must complete on the next worker step");

    reset_fixture();
    state = start_fixture();
    (void)state;
    reset_during_publish = true;
    connect_ready_send(1);
    CHECK(hello_handshake_debug_current_generation() == 2 &&
          hello_handshake_debug_armed_nonce() == 0,
          "reset during publish must invalidate old generation and nonce");
    hello_handshake_on_ready(2);
    (void)hello_handshake_worker_step(0);
    CHECK(hello_publish_count == 2 &&
          hello_handshake_debug_armed_nonce() != 0,
          "generation2 must send only after its own READY");
}

static void test_runtime_generation_and_nonce_wrap(void)
{
    hello_runtime_t runtime;
    hello_runtime_init_with_seed(&runtime, 100U);
    hello_runtime_on_transport_connected(&runtime, 1);
    hello_runtime_on_ready(&runtime, 1);
    atomic_store_explicit(&runtime.next_nonce, UINT32_MAX - 1U,
                          memory_order_release);
    uint32_t nonce = 0;
    CHECK(hello_runtime_prepare_send(&runtime, 1, &nonce) && nonce == UINT32_MAX,
          "nonce allocator must emit UINT32_MAX before wrap");
    hello_runtime_clear_nonce(&runtime, nonce);
    CHECK(hello_runtime_prepare_send(&runtime, 1, &nonce) && nonce == 1,
          "nonce allocator wrap must skip reserved zero");

    hello_runtime_on_transport_connected(&runtime, 2);
    hello_runtime_on_ready(&runtime, 1);
    CHECK(hello_runtime_ready_generation(&runtime) == 0,
          "stale READY must not become current after reset");
    CHECK(!hello_runtime_finish_send(&runtime, 1, nonce),
          "old publish completion must fail after generation reset");
    hello_runtime_on_ready(&runtime, 2);
    CHECK(hello_runtime_ready_generation(&runtime) == 2,
          "current READY must expose the sole MQTT generation");
}

static void test_runtime_startup_nonce_seed(void)
{
    hello_runtime_t first;
    hello_runtime_t second;
    hello_runtime_init_with_seed(&first, 100U);
    hello_runtime_init_with_seed(&second, 200U);
    CHECK(atomic_load_explicit(&first.next_nonce, memory_order_acquire) == 100U &&
          atomic_load_explicit(&second.next_nonce, memory_order_acquire) == 200U,
          "startup nonce cursor must retain a non-zero injected seed");

    hello_runtime_on_transport_connected(&first, 1);
    hello_runtime_on_ready(&first, 1);
    uint32_t nonce = 0;
    CHECK(hello_runtime_prepare_send(&first, 1, &nonce) && nonce == 101U,
          "first nonce must advance deterministically from injected seed");
}

static void test_runtime_sync_request_mailbox(void)
{
    hello_runtime_t runtime;
    hello_runtime_init_with_seed(&runtime, 300U);
    CHECK(!hello_runtime_request_sync(&runtime),
          "runtime without READY generation must reject sync request");

    hello_runtime_on_transport_connected(&runtime, 1);
    hello_runtime_on_ready(&runtime, 1);
    for (uint32_t i = 0; i < 100; i++) {
        CHECK(hello_runtime_request_sync(&runtime),
              "runtime must accept current READY sync request");
    }
    CHECK(hello_runtime_take_sync_request(&runtime, 1),
          "coalesced runtime requests must be consumed once");
    CHECK(!hello_runtime_take_sync_request(&runtime, 1),
          "runtime request mailbox must be empty after consume");

    CHECK(hello_runtime_request_sync(&runtime),
          "runtime must accept another same-generation request");
    hello_runtime_on_transport_connected(&runtime, 2);
    CHECK(!hello_runtime_take_sync_request(&runtime, 1),
          "transport reset must invalidate old-generation request");
    hello_runtime_on_ready(&runtime, 2);
    CHECK(!hello_runtime_take_sync_request(&runtime, 2),
          "old request must not be relabeled as new generation");
}

static void test_state_machine_retry_policy_is_unchanged(void)
{
    hello_sm_t sm;
    hello_sm_init(&sm);
    CHECK(hello_sm_accept_generation(&sm, 1) == HELLO_SM_ACTION_SEND_HELLO,
          "new generation must send immediately");
    for (uint32_t attempt = 1; attempt <= 3; attempt++) {
        hello_sm_on_hello_sent(&sm);
        hello_sm_action_t action = HELLO_SM_ACTION_NONE;
        for (uint32_t tick = 0; tick < 20; tick++) action = hello_sm_tick(&sm);
        hello_sm_action_t expected = attempt < 3
            ? HELLO_SM_ACTION_SEND_HELLO : HELLO_SM_ACTION_FAILED;
        CHECK(action == expected,
              "retry count and 20-tick timeout policy must remain unchanged");
    }
}

static void test_task_creation_failure_is_observable(void)
{
    reset_fixture();
    task_create_fails = true;
    app_state_t state = {0};
    strcpy(state.node_id, "host-node");
    hello_handshake_start(&state);
    CHECK(!hello_handshake_is_running() && hello_handshake_has_failed() &&
          !state.hello_task_running,
          "task creation failure must remain observable and fail closed");
}

static void test_worker_can_start_before_task_handle_publication(void)
{
    reset_fixture();
    exercise_worker_before_handle_publish = true;
    app_state_t state = {0};
    strcpy(state.node_id, "host-node");
    hello_handshake_start(&state);
    CHECK(hello_handshake_is_running() && state.hello_task_running,
          "creator must publish handle after an early worker safely blocks");
}

int main(void)
{
    test_delayed_nonce1_ack_after_nonce2_armed_is_rejected();
    test_ack_nonce_validation_and_legacy_sync_semantics();
    test_ack_parser_rejects_duplicate_wrong_wire_overflow_and_malformed();
    test_hello_codec_nonce_and_legacy_omission();
    test_notify_before_wait_and_latest_state_coalescing();
    test_periodic_sync_requests_coalesce_into_correlated_handshake();
    test_periodic_sync_request_is_generation_scoped();
    test_periodic_sync_replaces_active_nonce_and_rejects_old_ack();
    test_ack_and_reset_during_publish_interleavings();
    test_runtime_generation_and_nonce_wrap();
    test_runtime_startup_nonce_seed();
    test_runtime_sync_request_mailbox();
    test_state_machine_retry_policy_is_unchanged();
    test_task_creation_failure_is_observable();
    test_worker_can_start_before_task_handle_publication();

    if (failures != 0) {
        fprintf(stderr, "%d test(s) FAILED\n", failures);
        return 1;
    }
    puts("hello_handshake_tests: all tests passed");
    return 0;
}
