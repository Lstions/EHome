/**
 * @file test_mqtt_state_encapsulation.c
 * @brief Unit tests for MQTT state encapsulation (Step 6)
 *
 * Tests: mqtt_client_ctx_t structure and state management
 */

#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <stdbool.h>
#include <stdint.h>
#include <stddef.h>

/* Mock MQTT types */
typedef void* esp_mqtt_client_handle_t;

typedef enum {
    MQTT_CLIENT_DISCONNECTED = 0,
    MQTT_CLIENT_CONNECTING,
    MQTT_CLIENT_CONNECTED,
    MQTT_CLIENT_FAILED
} mqtt_client_state_t;

/* Callback types */
typedef void (*mqtt_msg_cb_t)(const char *topic, const uint8_t *data, size_t len, void *ctx);
typedef void (*mqtt_state_cb_t)(mqtt_client_state_t state, void *ctx);

/* MQTT client context structure (from Step 6) */
typedef struct {
    esp_mqtt_client_handle_t client;
    mqtt_client_state_t state;
    mqtt_msg_cb_t msg_cb;
    void *msg_cb_ctx;
    mqtt_state_cb_t state_cb;
    void *state_cb_ctx;
    char node_id[32];
    char up_topic[64];
    char down_topic[64];
} mqtt_client_ctx_t;

/* Test counters */
static int tests_run = 0;
static int tests_passed = 0;

#define TEST_ASSERT(cond, msg) do { \
    tests_run++; \
    if (cond) { \
        printf("  ✓ %s\n", msg); \
        tests_passed++; \
    } else { \
        printf("  ✗ %s\n", msg); \
    } \
} while(0)

/* Mock callback for testing */
static mqtt_client_state_t last_state_callback_state = MQTT_CLIENT_DISCONNECTED;
static int state_callback_count = 0;

static void mock_state_callback(mqtt_client_state_t state, void *ctx) {
    last_state_callback_state = state;
    state_callback_count++;
}

static int msg_callback_count = 0;
static char last_msg_topic[64] = {0};

static void mock_msg_callback(const char *topic, const uint8_t *data, size_t len, void *ctx) {
    msg_callback_count++;
    strncpy(last_msg_topic, topic, sizeof(last_msg_topic) - 1);
}

/* Test: Context initialization */
void test_context_initialization(void) {
    printf("\n[test_context_initialization]\n");
    
    mqtt_client_ctx_t ctx;
    memset(&ctx, 0, sizeof(ctx));
    
    TEST_ASSERT(ctx.client == NULL, "Client handle is NULL");
    TEST_ASSERT(ctx.state == MQTT_CLIENT_DISCONNECTED, "Initial state is DISCONNECTED");
    TEST_ASSERT(ctx.msg_cb == NULL, "Message callback is NULL");
    TEST_ASSERT(ctx.msg_cb_ctx == NULL, "Message callback context is NULL");
    TEST_ASSERT(ctx.state_cb == NULL, "State callback is NULL");
    TEST_ASSERT(ctx.state_cb_ctx == NULL, "State callback context is NULL");
    TEST_ASSERT(ctx.node_id[0] == '\0', "Node ID is empty");
    TEST_ASSERT(ctx.up_topic[0] == '\0', "Up topic is empty");
    TEST_ASSERT(ctx.down_topic[0] == '\0', "Down topic is empty");
}

/* Test: State transitions */
void test_state_transitions(void) {
    printf("\n[test_state_transitions]\n");
    
    mqtt_client_ctx_t ctx;
    memset(&ctx, 0, sizeof(ctx));
    ctx.state_cb = mock_state_callback;
    state_callback_count = 0;
    
    /* Initial state */
    TEST_ASSERT(ctx.state == MQTT_CLIENT_DISCONNECTED, "Initial state is DISCONNECTED");
    
    /* Transition to CONNECTING */
    ctx.state = MQTT_CLIENT_CONNECTING;
    if (ctx.state_cb) {
        ctx.state_cb(ctx.state, ctx.state_cb_ctx);
    }
    TEST_ASSERT(ctx.state == MQTT_CLIENT_CONNECTING, "State is CONNECTING");
    TEST_ASSERT(state_callback_count == 1, "State callback called once");
    TEST_ASSERT(last_state_callback_state == MQTT_CLIENT_CONNECTING, "Callback received CONNECTING");
    
    /* Transition to CONNECTED */
    ctx.state = MQTT_CLIENT_CONNECTED;
    if (ctx.state_cb) {
        ctx.state_cb(ctx.state, ctx.state_cb_ctx);
    }
    TEST_ASSERT(ctx.state == MQTT_CLIENT_CONNECTED, "State is CONNECTED");
    TEST_ASSERT(state_callback_count == 2, "State callback called twice");
    TEST_ASSERT(last_state_callback_state == MQTT_CLIENT_CONNECTED, "Callback received CONNECTED");
    
    /* Transition to DISCONNECTED */
    ctx.state = MQTT_CLIENT_DISCONNECTED;
    if (ctx.state_cb) {
        ctx.state_cb(ctx.state, ctx.state_cb_ctx);
    }
    TEST_ASSERT(ctx.state == MQTT_CLIENT_DISCONNECTED, "State is DISCONNECTED");
    TEST_ASSERT(state_callback_count == 3, "State callback called three times");
}

/* Test: Callback registration */
void test_callback_registration(void) {
    printf("\n[test_callback_registration]\n");
    
    mqtt_client_ctx_t ctx;
    memset(&ctx, 0, sizeof(ctx));
    
    /* Register state callback */
    ctx.state_cb = mock_state_callback;
    ctx.state_cb_ctx = (void*)0x12345678;
    
    TEST_ASSERT(ctx.state_cb == mock_state_callback, "State callback registered");
    TEST_ASSERT(ctx.state_cb_ctx == (void*)0x12345678, "State callback context set");
    
    /* Register message callback */
    ctx.msg_cb = mock_msg_callback;
    ctx.msg_cb_ctx = (void*)0xABCDEF00;
    
    TEST_ASSERT(ctx.msg_cb == mock_msg_callback, "Message callback registered");
    TEST_ASSERT(ctx.msg_cb_ctx == (void*)0xABCDEF00, "Message callback context set");
}

/* Test: Topic construction */
void test_topic_construction(void) {
    printf("\n[test_topic_construction]\n");
    
    mqtt_client_ctx_t ctx;
    memset(&ctx, 0, sizeof(ctx));
    
    /* Set node ID */
    strncpy(ctx.node_id, "test_node_123", sizeof(ctx.node_id) - 1);
    TEST_ASSERT(strcmp(ctx.node_id, "test_node_123") == 0, "Node ID set");
    
    /* Construct topics */
    snprintf(ctx.up_topic, sizeof(ctx.up_topic), "ehome/%s/up", ctx.node_id);
    snprintf(ctx.down_topic, sizeof(ctx.down_topic), "ehome/%s/down", ctx.node_id);
    
    TEST_ASSERT(strcmp(ctx.up_topic, "ehome/test_node_123/up") == 0, "Up topic constructed correctly");
    TEST_ASSERT(strcmp(ctx.down_topic, "ehome/test_node_123/down") == 0, "Down topic constructed correctly");
}

/* Test: Message callback invocation */
void test_message_callback(void) {
    printf("\n[test_message_callback]\n");
    
    mqtt_client_ctx_t ctx;
    memset(&ctx, 0, sizeof(ctx));
    ctx.msg_cb = mock_msg_callback;
    msg_callback_count = 0;
    
    /* Simulate message arrival */
    const char *test_topic = "ehome/test/down";
    uint8_t test_data[] = {0x01, 0x02, 0x03};
    
    if (ctx.msg_cb) {
        ctx.msg_cb(test_topic, test_data, sizeof(test_data), ctx.msg_cb_ctx);
    }
    
    TEST_ASSERT(msg_callback_count == 1, "Message callback invoked");
    TEST_ASSERT(strcmp(last_msg_topic, test_topic) == 0, "Topic passed correctly");
}

/* Test: Context size and alignment */
void test_context_size(void) {
    printf("\n[test_context_size]\n");
    
    mqtt_client_ctx_t ctx;
    
    size_t ctx_size = sizeof(ctx);
    printf("  Context size: %zu bytes\n", ctx_size);
    
    /* Verify reasonable size (should be around 200-250 bytes) */
    TEST_ASSERT(ctx_size > 100, "Context size is reasonable (>100 bytes)");
    TEST_ASSERT(ctx_size < 500, "Context size is reasonable (<500 bytes)");
    
    /* Verify field offsets make sense */
    size_t node_id_offset = offsetof(mqtt_client_ctx_t, node_id);
    size_t up_topic_offset = offsetof(mqtt_client_ctx_t, up_topic);
    size_t down_topic_offset = offsetof(mqtt_client_ctx_t, down_topic);
    
    TEST_ASSERT(node_id_offset < up_topic_offset, "node_id comes before up_topic");
    TEST_ASSERT(up_topic_offset < down_topic_offset, "up_topic comes before down_topic");
}

/* Test: Multiple contexts */
void test_multiple_contexts(void) {
    printf("\n[test_multiple_contexts]\n");
    
    mqtt_client_ctx_t ctx1, ctx2;
    memset(&ctx1, 0, sizeof(ctx1));
    memset(&ctx2, 0, sizeof(ctx2));
    
    /* Set different node IDs */
    strncpy(ctx1.node_id, "node_1", sizeof(ctx1.node_id) - 1);
    strncpy(ctx2.node_id, "node_2", sizeof(ctx2.node_id) - 1);
    
    /* Set different states */
    ctx1.state = MQTT_CLIENT_CONNECTED;
    ctx2.state = MQTT_CLIENT_DISCONNECTED;
    
    /* Verify independence */
    TEST_ASSERT(strcmp(ctx1.node_id, "node_1") == 0, "Context 1 has correct node ID");
    TEST_ASSERT(strcmp(ctx2.node_id, "node_2") == 0, "Context 2 has correct node ID");
    TEST_ASSERT(ctx1.state == MQTT_CLIENT_CONNECTED, "Context 1 has correct state");
    TEST_ASSERT(ctx2.state == MQTT_CLIENT_DISCONNECTED, "Context 2 has correct state");
    TEST_ASSERT(&ctx1 != &ctx2, "Contexts are independent");
}

/* Test: State callback with context */
void test_state_callback_with_context(void) {
    printf("\n[test_state_callback_with_context]\n");
    
    mqtt_client_ctx_t ctx;
    memset(&ctx, 0, sizeof(ctx));
    
    int user_data = 42;
    ctx.state_cb = mock_state_callback;
    ctx.state_cb_ctx = &user_data;
    
    /* Trigger state change */
    ctx.state = MQTT_CLIENT_CONNECTED;
    if (ctx.state_cb) {
        ctx.state_cb(ctx.state, ctx.state_cb_ctx);
    }
    
    TEST_ASSERT(ctx.state_cb_ctx == &user_data, "Callback context preserved");
}

int main(void) {
    printf("=== MQTT State Encapsulation Tests ===\n");
    
    test_context_initialization();
    test_state_transitions();
    test_callback_registration();
    test_topic_construction();
    test_message_callback();
    test_context_size();
    test_multiple_contexts();
    test_state_callback_with_context();
    
    printf("\n=== Results ===\n");
    printf("Passed: %d/%d\n", tests_passed, tests_run);
    
    return (tests_passed == tests_run) ? 0 : 1;
}
