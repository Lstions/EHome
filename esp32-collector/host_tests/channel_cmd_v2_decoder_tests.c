#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "msg_handler_internal.h"

static unsigned callback_count;
static unsigned publish_count;
static uint8_t captured_slot;
static channel_cmd_v2_t captured_cmd;
static uint8_t published[128];
static size_t published_len;

void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level;
    (void)tag;
    (void)format;
}

void msg_handler_publish(const uint8_t *data, size_t len)
{
	publish_count++;
    published_len = len;
    if (len <= sizeof(published)) memcpy(published, data, len);
}

const char *channel_cmd_v2_current_boot_id(void) { return "boot-1"; }
uint64_t channel_cmd_v2_current_time_ms(void) { return 1699999990000ULL; }

bool on_channel_cmd_v2_received(const channel_cmd_v2_t *cmd, uint8_t slot)
{
    callback_count++;
    captured_slot = slot;
    captured_cmd = *cmd;
    return true;
}

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL: %s:%d: %s\n", __FILE__, __LINE__, message); \
        return false; \
    } \
} while (0)

static void reset_capture(void)
{
    callback_count = 0;
	publish_count = 0;
	captured_slot = CHANNEL_CMD_V2_SLOT_NONE;
    memset(&captured_cmd, 0, sizeof(captured_cmd));
    memset(published, 0, sizeof(published));
    published_len = 0;
}

static void process(const uint8_t *buf, size_t len)
{
    frame_decoder_t dec;
    if (frame_decoder_init(&dec, buf, len) == FRAME_OK) {
        handler_channel_cmd_v2_process(&dec);
    }
}

static bool response_field_equals(uint8_t message_type, uint8_t wanted_field, uint64_t wanted_value)
{
    frame_decoder_t dec;
    frame_field_t field;
    if (frame_decoder_init(&dec, published, published_len) != FRAME_OK ||
        dec.buf[0] != message_type) return false;
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == wanted_field && field.wire_type == WIRE_VARINT &&
            field.value.varint == wanted_value) return true;
    }
    return false;
}

#define ack_field_equals(field, value) response_field_equals(MSG_CHANNEL_CMD_V2_ACK, field, value)

static bool test_go_c_golden_vector(void)
{
    static const uint8_t golden[] = {
        0x15, 0x0a, 0x10, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06,
        0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x12,
        0x10, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
        0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x18, 0x01, 0x22,
        0x06, 0x62, 0x6f, 0x6f, 0x74, 0x2d, 0x31, 0x28, 0x07, 0x30,
        0x09, 0x38, 0x80, 0xd0, 0x95, 0xff, 0xbc, 0x31, 0x42, 0x08,
        0x01, 0x03, 0x00, 0x00, 0x00, 0x02, 0xc4, 0x0b, 0x48, 0x09,
        0x50, 0xe8, 0x07, 0x58, 0x64, 0x60, 0x00, 0x68, 0x00, 0x70,
        0x01,
    };
    reset_capture();
    process(golden, sizeof(golden));
    CHECK(callback_count == 1, "Go golden ChannelCmdV2 was not dispatched");
    CHECK(captured_cmd.attempt == 1 && captured_cmd.edge_device_id == 7 &&
          captured_cmd.channel_id == 9, "identity mismatch");
    CHECK(strcmp(captured_cmd.boot_id, "boot-1") == 0, "boot ID mismatch");
    CHECK(captured_cmd.tx_len == 8 && captured_cmd.tx_data[1] == 3 &&
          captured_cmd.read_size == 9 && captured_cmd.rx_timeout_ms == 1000 &&
          captured_cmd.post_tx_delay_ms == 100, "transaction fields mismatch");
    CHECK(ack_field_equals(6, 1) && ack_field_equals(7, 0), "accepted ack mismatch");
    CHECK(captured_slot != CHANNEL_CMD_V2_SLOT_NONE, "control slot was not assigned");
    unsigned publishes_before_final = publish_count;
    static const uint8_t raw[] = {0x01, 0x03, 0x02, 0x00, 0x14};
    handler_channel_cmd_v2_complete(captured_slot, true, 0, raw, sizeof(raw));
    CHECK(publish_count == publishes_before_final + 1, "final was not published");
    CHECK(response_field_equals(MSG_CHANNEL_CMD_V2_FINAL, 6, 1) &&
          response_field_equals(MSG_CHANNEL_CMD_V2_FINAL, 7, 0), "final mismatch");
    handler_channel_cmd_v2_complete(captured_slot, false, 9, NULL, 0);
    CHECK(publish_count == publishes_before_final + 1, "duplicate final was published");
	channel_cmd_v2_metrics_t metrics = {0};
	handler_channel_cmd_v2_get_metrics(&metrics);
	CHECK(metrics.accepted == 1 && metrics.rejected == 0 && metrics.completed == 1 && metrics.replayed == 0,
	      "control metrics did not reflect one accepted completed command");
    return true;
}

static bool test_malformed_and_wrong_boot_rejected(void)
{
    uint8_t malformed[] = {0x15, 0x18, 0x01, 0x18, 0x02};
    reset_capture();
    process(malformed, sizeof(malformed));
    CHECK(callback_count == 0, "malformed command was dispatched");
    CHECK(ack_field_equals(6, 0) && ack_field_equals(7, 1002), "malformed ack mismatch");

    uint8_t valid[128];
    frame_encoder_t enc;
    frame_encoder_init(&enc, valid, sizeof(valid), MSG_CHANNEL_CMD_V2);
    uint8_t id[16] = {0};
    frame_encode_bytes(&enc, 1, id, sizeof(id));
    frame_encode_bytes(&enc, 2, id, sizeof(id));
    frame_encode_varint(&enc, 3, 1);
    frame_encode_string(&enc, 4, "stale-boot");
    frame_encode_varint(&enc, 5, 1);
    frame_encode_varint(&enc, 6, 1);
    frame_encode_varint(&enc, 7, 1);
    frame_encode_bytes(&enc, 8, (const uint8_t *)"x", 1);
    frame_encode_varint(&enc, 9, 0);
    frame_encode_varint(&enc, 10, 1);
    frame_encode_varint(&enc, 11, 0);
    frame_encode_varint(&enc, 12, 0);
    frame_encode_varint(&enc, 13, 0);
    frame_encode_varint(&enc, 14, 1);
    reset_capture();
    process(valid, frame_encoder_size(&enc));
    CHECK(callback_count == 0, "stale boot command was dispatched");
    CHECK(ack_field_equals(6, 0) && ack_field_equals(7, 1001), "stale boot ack mismatch");

    frame_encoder_init(&enc, valid, sizeof(valid), MSG_CHANNEL_CMD_V2);
    frame_encode_bytes(&enc, 1, id, sizeof(id));
    frame_encode_bytes(&enc, 2, id, sizeof(id));
    frame_encode_varint(&enc, 3, 1);
    frame_encode_string(&enc, 4, "boot-1");
    frame_encode_varint(&enc, 5, 1);
    frame_encode_varint(&enc, 6, 1);
    frame_encode_varint(&enc, 7, 1);
    frame_encode_bytes(&enc, 8, (const uint8_t *)"x", 1);
    frame_encode_varint(&enc, 9, 0);
    frame_encode_varint(&enc, 10, 1);
    frame_encode_varint(&enc, 11, 0);
    frame_encode_varint(&enc, 12, 0);
    frame_encode_varint(&enc, 13, 0);
    frame_encode_varint(&enc, 14, 1);
    reset_capture();
    process(valid, frame_encoder_size(&enc));
    CHECK(callback_count == 0, "expired command was dispatched");
    CHECK(ack_field_equals(6, 0) && ack_field_equals(7, 1006), "expired ack mismatch");
    return true;
}

static void process_unique_command(uint8_t seed)
{
    uint8_t valid[128];
    uint8_t command_id[16];
    uint8_t digest[16];
    memset(command_id, seed, sizeof(command_id));
    memset(digest, (uint8_t)(0x80U + seed), sizeof(digest));
    frame_encoder_t enc;
    frame_encoder_init(&enc, valid, sizeof(valid), MSG_CHANNEL_CMD_V2);
    frame_encode_bytes(&enc, 1, command_id, sizeof(command_id));
    frame_encode_bytes(&enc, 2, digest, sizeof(digest));
    frame_encode_varint(&enc, 3, 1);
    frame_encode_string(&enc, 4, "boot-1");
    frame_encode_varint(&enc, 5, 1);
    frame_encode_varint(&enc, 6, 1);
    frame_encode_varint(&enc, 7, 1700000000000ULL);
    frame_encode_bytes(&enc, 8, (const uint8_t *)"x", 1);
    frame_encode_varint(&enc, 9, 0);
    frame_encode_varint(&enc, 10, 1);
    frame_encode_varint(&enc, 11, 0);
    frame_encode_varint(&enc, 12, 0);
    frame_encode_varint(&enc, 13, 0);
    frame_encode_varint(&enc, 14, 1);
    process(valid, frame_encoder_size(&enc));
}

static bool test_completed_slot_window_evicts_oldest_final(void)
{
    static const uint8_t golden[] = {
        0x15, 0x0a, 0x10, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06,
        0x07, 0x08, 0x09, 0x0a, 0x0b, 0x0c, 0x0d, 0x0e, 0x0f, 0x12,
        0x10, 0x10, 0x11, 0x12, 0x13, 0x14, 0x15, 0x16, 0x17, 0x18,
        0x19, 0x1a, 0x1b, 0x1c, 0x1d, 0x1e, 0x1f, 0x18, 0x01, 0x22,
        0x06, 0x62, 0x6f, 0x6f, 0x74, 0x2d, 0x31, 0x28, 0x07, 0x30,
        0x09, 0x38, 0x80, 0xd0, 0x95, 0xff, 0xbc, 0x31, 0x42, 0x08,
        0x01, 0x03, 0x00, 0x00, 0x00, 0x02, 0xc4, 0x0b, 0x48, 0x09,
        0x50, 0xe8, 0x07, 0x58, 0x64, 0x60, 0x00, 0x68, 0x00, 0x70,
        0x01,
    };
    static const uint8_t raw[] = {0x01};
    reset_capture();
    for (uint8_t seed = 1; seed <= CHANNEL_CMD_V2_SLOT_COUNT; seed++) {
        process_unique_command(seed);
        CHECK(callback_count == seed, "completed command was rejected before the replay window filled");
        CHECK(captured_slot != CHANNEL_CMD_V2_SLOT_NONE, "completed command had no slot");
        handler_channel_cmd_v2_complete(captured_slot, true, 0, raw, sizeof(raw));
    }
    process(golden, sizeof(golden));
    CHECK(callback_count == CHANNEL_CMD_V2_SLOT_COUNT + 1,
          "oldest completed command was not evicted from the replay window");
    CHECK(ack_field_equals(6, 1) && ack_field_equals(7, 0), "evicted command was not admitted");
    return true;
}

int main(void)
{
    bool ok = true;
    ok = test_go_c_golden_vector() && ok;
    ok = test_malformed_and_wrong_boot_rejected() && ok;
    ok = test_completed_slot_window_evicts_oldest_final() && ok;
    if (!ok) return 1;
    puts("channel_cmd_v2_decoder_tests: all tests passed");
    return 0;
}
