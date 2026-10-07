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
/* V3-2a: HelloAck features 现在是服务端能力位图，由 bus_worker 读取。 */
#include "handler_hello.h"

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
/*
 * 这两个 stub 的返回值原先写死。S0 对拍需要让 handler 产出**与共享向量完全相同的
 * 字节**（向量要求 field6=1 且**不发** field7），所以改成可注入 —— 但**默认值与原行为
 * 逐位相同**，因此本文件既有的全部用例行为不变（这是"改测试夹具"不是"改期望"）。
 */
static bool stub_has_manifest = false;
static const char *stub_last_manifest_id = "manifest-host";
bool config_mgr_has_manifest(void) { return stub_has_manifest; }
uint8_t config_mgr_get_active_channel_count(void) { return 0; }
uint64_t config_mgr_get_epoch(void) { return 7; }
const char *config_mgr_get_last_known_manifest_id(void) { return stub_last_manifest_id; }
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

static void test_ack_nonce_is_mandatory(void)
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
    CHECK(msg_handler_get_server_time() == 0 && sync_downlink_count == 0,
          "ACK without nonce must have no handshake side effects");

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

static void test_hello_codec_requires_nonce(void)
{
    reset_fixture();
    msg_handler_send_hello("node", "fw", "model", 2, 0);
    CHECK(hello_publish_count == 0 && last_publish_len == 0,
          "Hello without nonce must not reach the wire");

    msg_handler_send_hello("node", "fw", "model", 2, 77);
    CHECK(decode_hello_nonce(last_publish, last_publish_len) == 77,
          "handshake Hello must encode exact field9 nonce");

    frame_decoder_t dec;
    frame_field_t field;
    char protocol[8] = {0};
    CHECK(frame_decoder_init(&dec, last_publish, last_publish_len) == FRAME_OK,
          "nonce-correlated Hello must decode");
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == 8) {
            (void)frame_field_get_string(&field, protocol, sizeof(protocol));
        }
    }
    CHECK(strcmp(protocol, "2.6") == 0,
          "new firmware Hello must advertise protocol 2.6");
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

/* ------------------------------------------------------------------ *
 *  V3-2a: HelloAck features -> hello_get_server_caps()
 *
 *  这是 DataBatch 的**唯一开关**。两个失败模式都会造成现场事故：
 *    - 没存下来 -> 后端置了 bit0，固件仍只发 0x03（功能不生效）；
 *    - 存下来但跨连接不清 -> 新后端不支持 0x20 时固件继续发，遥测静默丢失。
 * ------------------------------------------------------------------ */
static void test_server_caps_captured_and_reset(void)
{
    reset_fixture();
    app_state_t *state = start_fixture();
    (void)state;
    connect_ready_send(11);
    uint32_t nonce = hello_handshake_debug_armed_nonce();
    CHECK(nonce != 0, "fixture must arm a nonce");

    CHECK(hello_get_server_caps() == 0,
          "caps must be 0 before any accepted HelloAck");

    /* features = CAP_DATA_BATCH_V1 -> 必须被捕获。 */
    process_ack(nonce, true, 500, (uint32_t)CAP_DATA_BATCH_V1);
    CHECK(msg_handler_is_hello_ack_received(), "ACK must be accepted");
    CHECK(hello_get_server_caps() == CAP_DATA_BATCH_V1,
          "accepted HelloAck must store features into server caps");

    /* 全 64 位都要能存下（features 在 wire 上是 varint，但固件侧按 uint64 存）。 */
    reset_fixture();
    state = start_fixture();
    (void)state;
    connect_ready_send(12);
    nonce = hello_handshake_debug_armed_nonce();
    process_ack(nonce, true, 600, 0x5);
    CHECK(hello_get_server_caps() == 0x5ULL,
          "multi-bit features must be preserved verbatim");

    /* reset 必须清空：旧连接的能力位绝不能泄漏到新连接。 */
    msg_handler_reset_hello_ack();
    CHECK(hello_get_server_caps() == 0,
          "msg_handler_reset_hello_ack() must clear server caps");

    /* 陈旧 nonce 的 ACK 不得改变能力位。 */
    reset_fixture();
    state = start_fixture();
    (void)state;
    connect_ready_send(13);
    uint32_t stale = hello_handshake_debug_armed_nonce();
    for (uint32_t i = 0; i < 20; i++) (void)hello_handshake_worker_step(0);
    process_ack(stale, true, 700, (uint32_t)CAP_DATA_BATCH_V1);
    CHECK(!msg_handler_is_hello_ack_received(),
          "stale ACK must not be accepted");
    CHECK(hello_get_server_caps() == 0,
          "stale ACK must not change server caps");
}


/* ==================================================================== *
 * S0 盲区修复 (2026-10-07)：把 handler_hello.c 的**真实输出**绑到共享向量
 *
 * ## 为什么需要（本卡存在的全部理由）
 *
 * 变异 `handler_hello.c:179` 的 field 编号 4 → 10（即 field 4 **整条消失**）后：
 *   宿主 ctest 100/100 全绿 · 14 条门禁全 RC=0 · 后端 go test rc=0 · 跨语言对锚 rc=0
 *   ⇒ **没有任何一层发现它**。
 * 而它是真实破坏：后端 required 集 = {1,2,3,4,5,6,8,9}，缺 4 ⇒ parseHello 返回
 *   ⇒ 不回 HelloAck ⇒ 设备永远进不了 READY。
 *
 * ## 根因（本卡修的那一个）
 *
 * `test_hello_codec_requires_nonce` **确实**调用了真实 `msg_handler_send_hello`
 * （即覆盖了真实编码路径），但它**只断言 field 8 与 field 9**；
 * field 4/5/6 从不检查 ⇒ 缺必填字段这条真实故障无覆盖。
 * 教训：**"有测试覆盖了那个函数" ≠ "覆盖了那个函数里会出错的部分"**。
 *
 * ## 做法
 *
 * 1) `assert_hello_wire_contract`：逐字段核对**编号 + wire type + 必填齐全**；
 * 2) `test_hello_matches_shared_vector`：用与向量**相同的参数**编一条，
 *    断言**逐字节等于**向量 `hello_from_device` 的 wire。
 *
 * 两者互补：对拍证明"当前字节正确"，逐字段证明"**为什么**错"（对拍失败时能直接
 * 指出是哪个字段漂了，而不是只说"字节不符"）。
 * ==================================================================== */

/* 后端 parseHello 的 required 集（handler_hello.go:134）。
 * 这 8 个字段**一个都不能少** —— 少任何一个后端都直接 return，不回 HelloAck。 */
static const uint8_t kHelloRequiredFields[] = {1, 2, 3, 4, 5, 6, 8, 9};
/* 固件**允许**发送的可选字段：field 7 last_manifest（仅非空时发，见 handler_hello.c:186-188）。 */
static const uint8_t kHelloOptionalFields[] = {7};

static bool field_in_list(uint8_t n, const uint8_t *list, size_t len)
{
    for (size_t i = 0; i < len; i++) {
        if (list[i] == n) return true;
    }
    return false;
}

/* 每个字段在 wire 上的**规范** wire type。
 *
 * ⚠ field 8 (proto_version) 是**字符串**不是 varint —— 依据：
 *   · 固件 handler_hello.c:189 `frame_encode_string(&enc, HELLO_F_PROTO_VERSION, "2.6")`
 *   · 后端 handler_hello.go:118-122 对 case 8 要求 WIRE_LENGTH_DELIMITED
 *   · 共享向量 `bytes 8 322e36`（字符串 "2.6"）
 * 我第一版把 8 写成 varint，被本函数自己的断言当场抓出（见报告"我自己的错"）。
 * 这正是"逐字段 wire type"值得写的原因。 */
static uint8_t expected_wire_type(uint8_t field_num)
{
    switch (field_num) {
    case 1: case 2: case 3: case 7: case 8: return WIRE_LENGTH_DELIMITED;
    case 4: case 5: case 6: case 9: return WIRE_VARINT;
    default: return 0xFF; /* 未知字段：调用方自行报错 */
    }
}

/*
 * 逐字段核对 handler_hello.c 真实产出的 Hello：
 *   · 必填 {1,2,3,4,5,6,8,9} **一个不缺**（这条正是本卡要挡的故障）；
 *   · 每个出现的字段的 **wire type 正确**；
 *   · 不出现**未知**字段号（防止编号漂移后落到别的号上而"看起来还在"）。
 *
 * 注意：本函数**不检查字段的取值**（channel_count 是 2 还是 10 都不管）——
 * 契约是关于"编号与存在性"的，取值由别的用例覆盖。
 */
static void assert_hello_wire_contract(const uint8_t *data, size_t len, const char *ctx)
{
    frame_decoder_t dec;
    frame_field_t field;
    bool seen[256] = {false};
    size_t n_seen = 0;

    if (frame_decoder_init(&dec, data, len) != FRAME_OK) {
        fprintf(stderr, "FAIL [%s]: Hello 帧无法解码\n", ctx);
        failures++;
        return;
    }
    if (len == 0 || data[0] != MSG_HELLO) {
        fprintf(stderr, "FAIL [%s]: 首字节 0x%02X != MSG_HELLO(0x%02X)\n",
                ctx, len ? data[0] : 0, MSG_HELLO);
        failures++;
        return;
    }

    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == 0) continue;
        if (seen[field.field_num]) {
            fprintf(stderr, "FAIL [%s]: 字段 %u 重复出现\n", ctx, field.field_num);
            failures++;
        }
        seen[field.field_num] = true;
        n_seen++;

        /* 已知字段必须是"必填"或"可选"之一；其余一律是编号漂移。 */
        bool known = field_in_list(field.field_num, kHelloRequiredFields,
                                   sizeof(kHelloRequiredFields)) ||
                     field_in_list(field.field_num, kHelloOptionalFields,
                                   sizeof(kHelloOptionalFields));
        /* wire type 必须与规范一致（未知字段号 => 0xFF 报错）。 */
        uint8_t want = expected_wire_type(field.field_num);
        if (!known || want == 0xFF) {
            fprintf(stderr,
                    "FAIL [%s]: 出现未知字段号 %u (wire=%u) —— Hello 编号漂移\n",
                    ctx, field.field_num, field.wire_type);
            failures++;
        } else if (field.wire_type != want) {
            fprintf(stderr,
                    "FAIL [%s]: 字段 %u wire type = %u, 期望 %u\n",
                    ctx, field.field_num, field.wire_type, want);
            failures++;
        }
    }

    /* ⭐ 必填字段一个都不能少 —— 缺任何一个后端 parseHello 都直接 return。 */
    for (size_t i = 0; i < sizeof(kHelloRequiredFields); i++) {
        uint8_t n = kHelloRequiredFields[i];
        if (!seen[n]) {
            fprintf(stderr,
                    "FAIL [%s]: 缺少**必填**字段 %u —— 后端 parseHello 会整条拒绝,"
                    " 不回 HelloAck, 设备永远进不了 READY (已见 %zu 个字段)\n",
                    ctx, n, n_seen);
            failures++;
        }
    }
}

/* 解码辅助：取某字段的 varint 值（不存在或 wire 不符返回 false）。 */
static bool hello_field_u64(const uint8_t *data, size_t len, uint8_t want_num,
                            uint64_t *out)
{
    frame_decoder_t dec;
    frame_field_t field;
    if (frame_decoder_init(&dec, data, len) != FRAME_OK) return false;
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == want_num && field.wire_type == WIRE_VARINT) {
            *out = field.value.varint;
            return true;
        }
    }
    return false;
}

/*
 * ⭐ 与共享向量**逐字节对拍**：用向量 hello_from_device 的同样参数编一条 Hello。
 *
 * 向量参数（protocol/vectors/wire_primitives.txt，case hello_from_device）：
 *   node_id="v3-link-node"  fw="3.0.0-dev"  model="EH-S3"
 *   channel_count=4  epoch=7  has_manifest=1  proto="2.6"  nonce=2712847316
 *   **不发 field 7**（向量里没有 field 7）
 *
 * 参数形状核对（这是 Lead 要求先确认的）：
 *   · handler 的 epoch / has_manifest / last_manifest 来自 config_mgr_* stub
 *     ⇒ 通过 stub 注入成向量要求的 7 / 1 / (空 ⇒ 不发 field 7)；
 *   · 其余（node_id/fw/model/channel_count/nonce）都是 handler 的入参，直接给。
 *   ⇒ **形状对得上**，可以做对拍。
 */
static void test_hello_matches_shared_vector(void)
{
    reset_fixture();
    stub_has_manifest = true;              /* 向量 u64 6 1 */
    stub_last_manifest_id = "";            /* 向量无 field 7 ⇒ 必须不发 */

    msg_handler_send_hello("v3-link-node", "3.0.0-dev", "EH-S3", 4, 2712847316U);
    stub_has_manifest = false;             /* 立刻还原默认, 避免影响后续用例 */
    stub_last_manifest_id = "manifest-host";

    CHECK(hello_publish_count == 1,
          "对拍: handler 必须真的发出一条 Hello");
    if (hello_publish_count != 1) return;

    /* 先跑逐字段契约 —— 它能在对拍失败时指出**哪个字段**漂了。 */
    assert_hello_wire_contract(last_publish, last_publish_len, "vector-parity");

    /* 向量 wire（与 protocol/vectors/wire_primitives.txt 的 hello_from_device 一致）。 */
    static const uint8_t kVectorWire[] = {
        0x01,
        0x0a, 0x0c, 'v', '3', '-', 'l', 'i', 'n', 'k', '-', 'n', 'o', 'd', 'e',
        0x12, 0x09, '3', '.', '0', '.', '0', '-', 'd', 'e', 'v',
        0x1a, 0x05, 'E', 'H', '-', 'S', '3',
        0x20, 0x04,
        0x28, 0x07,
        0x30, 0x01,
        0x42, 0x03, '2', '.', '6',
        0x48, 0xd4, 0x87, 0xcb, 0x8d, 0x0a,
    };

    if (last_publish_len != sizeof(kVectorWire)) {
        fprintf(stderr,
                "FAIL 对拍: Hello 长度 %zu != 向量 %zu\n",
                last_publish_len, sizeof(kVectorWire));
        failures++;
        return;
    }
    for (size_t i = 0; i < sizeof(kVectorWire); i++) {
        if (last_publish[i] != kVectorWire[i]) {
            fprintf(stderr,
                    "FAIL 对拍: 第 %zu 字节 0x%02X != 向量 0x%02X\n",
                    i, last_publish[i], kVectorWire[i]);
            failures++;
            return;
        }
    }

    /* 顺带把取值也钉住（编号对了但值错了同样是契约漂移）。 */
    uint64_t v = 0;
    CHECK(hello_field_u64(last_publish, last_publish_len, 4, &v) && v == 4,
          "对拍: field4 channel_count 必须 == 4");
    CHECK(hello_field_u64(last_publish, last_publish_len, 5, &v) && v == 7,
          "对拍: field5 config_epoch 必须 == 7");
    CHECK(hello_field_u64(last_publish, last_publish_len, 6, &v) && v == 1,
          "对拍: field6 has_manifest 必须 == 1");
    CHECK(hello_field_u64(last_publish, last_publish_len, 9, &v) && v == 2712847316ULL,
          "对拍: field9 nonce 必须 == 2712847316");
}

/* 把契约断言接到**既有的**真实编码用例上（它本来就调 msg_handler_send_hello）。 */
static void test_hello_codec_contract_fields(void)
{
    reset_fixture();
    msg_handler_send_hello("node", "fw", "model", 2, 77);
    CHECK(hello_publish_count == 1, "contract: Hello 必须发到 wire");
    assert_hello_wire_contract(last_publish, last_publish_len, "default-config");
}

int main(void)
{
    test_delayed_nonce1_ack_after_nonce2_armed_is_rejected();
    test_ack_nonce_is_mandatory();
    test_ack_parser_rejects_duplicate_wrong_wire_overflow_and_malformed();
    test_hello_codec_requires_nonce();
    test_hello_codec_contract_fields();
    test_hello_matches_shared_vector();
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
    test_server_caps_captured_and_reset();

    if (failures != 0) {
        fprintf(stderr, "%d test(s) FAILED\n", failures);
        return 1;
    }
    puts("hello_handshake_tests: all tests passed");
    return 0;
}
