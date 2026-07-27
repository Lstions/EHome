#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "frame_codec.h"

void handler_writecmd_process(frame_decoder_t *dec);

static unsigned callback_count;
static uint32_t captured_request_id;
static uint32_t captured_channel_id;
static uint32_t captured_read_size;
static uint32_t captured_edge_device_id;
static uint32_t captured_rx_timeout_ms;
static uint8_t captured_data[256];
static size_t captured_data_len;

void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level;
    (void)tag;
    (void)format;
}

void msg_handler_publish(const uint8_t *data, size_t len)
{
    (void)data;
    (void)len;
}

void on_write_cmd_received(uint32_t request_id, uint32_t channel_id,
                           const uint8_t *data, size_t len, uint32_t read_size,
                           uint32_t edge_device_id, uint32_t rx_timeout_ms)
{
    callback_count++;
    captured_request_id = request_id;
    captured_channel_id = channel_id;
    captured_read_size = read_size;
    captured_edge_device_id = edge_device_id;
    captured_rx_timeout_ms = rx_timeout_ms;
    captured_data_len = len;
    if (len > 0 && data) memcpy(captured_data, data, len);
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
    captured_request_id = 0;
    captured_channel_id = 0;
    captured_read_size = 0;
    captured_edge_device_id = 0;
    captured_rx_timeout_ms = 0;
    captured_data_len = 0;
    memset(captured_data, 0, sizeof(captured_data));
}

static void process(const uint8_t *buf, size_t len)
{
    frame_decoder_t dec;
    if (frame_decoder_init(&dec, buf, len) == FRAME_OK) {
        handler_writecmd_process(&dec);
    }
}

static size_t valid_frame(uint8_t *buf, size_t capacity, bool explicit_timeout)
{
    static const uint8_t command[] = {0x01, 0x03, 0x00, 0x00};
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, capacity, MSG_WRITE_CMD);
    frame_encode_varint(&enc, 1, 17);
    frame_encode_varint(&enc, 2, 9);
    frame_encode_bytes(&enc, 3, command, sizeof(command));
    frame_encode_varint(&enc, 4, 8);
    frame_encode_varint(&enc, 5, 33);
    if (explicit_timeout) frame_encode_varint(&enc, 6, 2500);
    return frame_encoder_size(&enc);
}

static bool test_valid_and_default_timeout(void)
{
    uint8_t buf[128];
    size_t len = valid_frame(buf, sizeof(buf), false);
    reset_capture();
    process(buf, len);
    CHECK(callback_count == 1, "valid command was not dispatched");
    CHECK(captured_request_id == 17 && captured_channel_id == 9, "identity mismatch");
    CHECK(captured_read_size == 8 && captured_edge_device_id == 33, "optional field mismatch");
    CHECK(captured_rx_timeout_ms == 1000, "default timeout mismatch");
    CHECK(captured_data_len == 4 && captured_data[1] == 0x03, "payload mismatch");
    return true;
}

static bool test_explicit_timeout(void)
{
    uint8_t buf[128];
    size_t len = valid_frame(buf, sizeof(buf), true);
    static const uint8_t golden[] = {
        0x06, 0x08, 0x11, 0x10, 0x09, 0x1a, 0x04, 0x01, 0x03,
        0x00, 0x00, 0x20, 0x08, 0x28, 0x21, 0x30, 0xc4, 0x13,
    };
    CHECK(len == sizeof(golden) && memcmp(buf, golden, sizeof(golden)) == 0,
          "Go/C WriteCmd golden vector mismatch");
    reset_capture();
    process(buf, len);
    CHECK(callback_count == 1, "command with explicit timeout was not dispatched");
    CHECK(captured_rx_timeout_ms == 2500, "explicit timeout mismatch");
    return true;
}

static bool test_missing_and_zero_channel_rejected(void)
{
    uint8_t buf[64];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_WRITE_CMD);
    frame_encode_varint(&enc, 1, 1);
    frame_encode_varint(&enc, 2, 0);
    frame_encode_bytes(&enc, 3, NULL, 0);
    reset_capture();
    process(buf, frame_encoder_size(&enc));
    CHECK(callback_count == 0, "channel zero legacy reset path was accepted");

    frame_encoder_init(&enc, buf, sizeof(buf), MSG_WRITE_CMD);
    frame_encode_varint(&enc, 1, 1);
    frame_encode_varint(&enc, 2, 2);
    reset_capture();
    process(buf, frame_encoder_size(&enc));
    CHECK(callback_count == 0, "missing payload was accepted");
    return true;
}

static bool test_duplicate_unknown_and_wrong_wire_rejected(void)
{
    uint8_t buf[128];
    size_t len = valid_frame(buf, sizeof(buf), false);
    frame_encoder_t extra;
    extra.buf = buf;
    extra.pos = len;
    extra.capacity = sizeof(buf);
    frame_encode_varint(&extra, 2, 10);
    reset_capture();
    process(buf, frame_encoder_size(&extra));
    CHECK(callback_count == 0, "duplicate channel was accepted");

    len = valid_frame(buf, sizeof(buf), false);
    extra.pos = len;
    frame_encode_varint(&extra, 7, 1);
    reset_capture();
    process(buf, frame_encoder_size(&extra));
    CHECK(callback_count == 0, "unknown field was accepted");

    frame_encoder_init(&extra, buf, sizeof(buf), MSG_WRITE_CMD);
    frame_encode_varint(&extra, 1, 1);
    frame_encode_bytes(&extra, 2, (const uint8_t *)"x", 1);
    frame_encode_bytes(&extra, 3, NULL, 0);
    reset_capture();
    process(buf, frame_encoder_size(&extra));
    CHECK(callback_count == 0, "wrong channel wire type was accepted");
    return true;
}

static bool test_limits_and_truncation_rejected(void)
{
    uint8_t buf[256];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_WRITE_CMD);
    frame_encode_varint(&enc, 1, 1);
    frame_encode_varint(&enc, 2, 2);
    frame_encode_bytes(&enc, 3, NULL, 0);
    frame_encode_varint(&enc, 4, 257);
    reset_capture();
    process(buf, frame_encoder_size(&enc));
    CHECK(callback_count == 0, "oversized read was accepted");

    uint8_t oversized[129] = {0};
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_WRITE_CMD);
    frame_encode_varint(&enc, 1, 1);
    frame_encode_varint(&enc, 2, 2);
    frame_encode_bytes(&enc, 3, oversized, sizeof(oversized));
    reset_capture();
    process(buf, frame_encoder_size(&enc));
    CHECK(callback_count == 0, "oversized TX was accepted");

    size_t len = valid_frame(buf, sizeof(buf), true);
    reset_capture();
    process(buf, len - 1);
    CHECK(callback_count == 0, "truncated frame was accepted");
    return true;
}

int main(void)
{
    bool ok = true;
    ok = test_valid_and_default_timeout() && ok;
    ok = test_explicit_timeout() && ok;
    ok = test_missing_and_zero_channel_rejected() && ok;
    ok = test_duplicate_unknown_and_wrong_wire_rejected() && ok;
    ok = test_limits_and_truncation_rejected() && ok;
    if (!ok) return 1;
    puts("writecmd_decoder_tests: all tests passed");
    return 0;
}
