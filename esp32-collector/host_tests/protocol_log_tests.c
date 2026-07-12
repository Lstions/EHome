#include <inttypes.h>
#include <stdio.h>
#include <string.h>

#include "frame_codec.h"
#include "data_report_codec.h"
#include "log_stream_codec.h"
#include "scheduler_queue_guard.h"

static int s_failures;

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (message)); \
        s_failures++; \
        return; \
    } \
} while (0)

static bool find_varint_field(const uint8_t *buf, size_t len, uint8_t number,
                              uint64_t *value, int *count)
{
    frame_decoder_t dec;
    frame_field_t field;
    if (frame_decoder_init(&dec, buf, len) != FRAME_OK) return false;

    *count = 0;
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == number) {
            (*count)++;
            if (field.wire_type != WIRE_VARINT) return false;
            *value = field.value.varint;
        }
    }
    return true;
}

static void test_data_report_encodes_template_id_as_field_9(void)
{
    uint8_t buf[256];
    size_t len = 0;
    const uint8_t raw[] = {0x01, 0x03, 0x02};

    CHECK(data_report_encode(buf, sizeof(buf), &len,
                             42, 123456789ULL, 7, raw, sizeof(raw),
                             0, 99, 314, 65537, 0) == FRAME_OK,
          "DataReport encode failed");
    CHECK(buf[0] == MSG_DATA_RPT, "DataReport message type is wrong");

    uint64_t value = 0;
    int count = 0;
    CHECK(find_varint_field(buf, len, 9, &value, &count), "field 9 decode failed");
    CHECK(count == 1, "field 9 must be encoded exactly once");
    CHECK(value == 65537, "field 9 must contain ConfigTemplate.ID");

    CHECK(find_varint_field(buf, len, 8, &value, &count), "field 8 decode failed");
    CHECK(count == 1 && value == 0, "field 8 must preserve command index zero with edge device");
}

static void test_data_report_omits_optional_routing_fields_when_zero(void)
{
    uint8_t buf[128];
    size_t len = 0;

    CHECK(data_report_encode(buf, sizeof(buf), &len,
                             1, 2, 3, NULL, 0,
                             0, 0, 0, 0, 0) == FRAME_OK,
          "minimal DataReport encode failed");

    uint64_t value = 0;
    int count = 0;
    CHECK(find_varint_field(buf, len, 7, &value, &count) && count == 0,
          "edge_device_id must be omitted when zero");
    CHECK(find_varint_field(buf, len, 8, &value, &count) && count == 0,
          "command_index must be omitted without edge device");
    CHECK(find_varint_field(buf, len, 9, &value, &count) && count == 0,
          "command_template_id must be omitted when zero");
}

static void test_log_stream_entry_is_a_raw_subframe(void)
{
    uint8_t buf[256];
    size_t len = 0;
    const log_stream_entry_t entry = {
        .level = 1,
        .timestamp_us = 123456789ULL,
        .tag = "RX_TASK",
        .message = "timeout channel=42",
    };

    CHECK(log_stream_encode(buf, sizeof(buf), &len, 9, &entry, 1) == FRAME_OK,
          "log stream encode failed");
    CHECK(buf[0] == MSG_LOG_STREAM, "log stream message type is wrong");

    frame_decoder_t dec;
    frame_field_t field;
    CHECK(frame_decoder_init(&dec, buf, len) == FRAME_OK, "top-level log decode failed");

    int entry_count = 0;
    const uint8_t *sub = NULL;
    size_t sub_len = 0;
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == 3) {
            entry_count++;
            sub = field.value.bytes.ptr;
            sub_len = field.value.bytes.len;
        }
    }
    CHECK(entry_count == 1 && sub != NULL, "exactly one log entry is required");
    CHECK(sub[0] == 0x08, "subframe must begin with field-1 tag, not a message type");

    frame_decoder_t sub_dec;
    CHECK(frame_decoder_init_sub(&sub_dec, sub, sub_len) == FRAME_OK,
          "subframe decoder must accept raw nested fields");
    CHECK(frame_decoder_next(&sub_dec, &field) == FRAME_OK,
          "first subframe field missing");
    CHECK(field.field_num == 1 && field.value.varint == 1,
          "subframe level field is wrong");
}

static void test_log_stream_rejects_invalid_inputs_and_capacity(void)
{
    uint8_t buf[32];
    size_t len = 0;
    log_stream_entry_t entry = { .level = 2, .timestamp_us = 1, .tag = "T", .message = "M" };

    CHECK(log_stream_encode(NULL, sizeof(buf), &len, 0, &entry, 1) == FRAME_ERR_INVALID_TAG,
          "NULL buffer must be rejected");
    CHECK(log_stream_encode(buf, sizeof(buf), &len, 0, NULL, 1) == FRAME_ERR_INVALID_TAG,
          "NULL entries must be rejected");
    CHECK(log_stream_encode(buf, 2, &len, 0, &entry, 1) == FRAME_ERR_OVERFLOW,
          "undersized buffer must report overflow");
}

static void test_scheduler_queue_guard(void)
{
    CHECK(!scheduler_queue_is_present(NULL), "NULL queue must be rejected before FreeRTOS API calls");
    CHECK(scheduler_queue_is_present((const void *)0x1), "non-NULL queue must be accepted");
}

int main(void)
{
    test_data_report_encodes_template_id_as_field_9();
    test_data_report_omits_optional_routing_fields_when_zero();
    test_log_stream_entry_is_a_raw_subframe();
    test_log_stream_rejects_invalid_inputs_and_capacity();
    test_scheduler_queue_guard();

    if (s_failures != 0) {
        fprintf(stderr, "%d test(s) failed\n", s_failures);
        return 1;
    }
    printf("protocol_log_tests: all tests passed\n");
    return 0;
}
