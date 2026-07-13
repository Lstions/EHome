#include <inttypes.h>
#include <stdio.h>
#include <string.h>

#include "frame_codec.h"
#include "data_report_codec.h"
#include "log_capture.h"
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

static bool capture_printf(log_capture_t *capture, uint8_t level, uint64_t uptime_us,
                           const char *tag, const char *format, ...)
{
    va_list args;
    va_start(args, format);
    bool captured = log_capture_pushv(capture, level, uptime_us, tag, format, args);
    va_end(args);
    return captured;
}

static void test_native_log_capture_preserves_metadata_and_encodes_protocol(void)
{
    log_capture_entry_t storage[4];
    log_capture_t capture;
    log_capture_init(&capture, storage, 4, LOG_CAPTURE_LEVEL_VERBOSE);

    CHECK(capture_printf(&capture, LOG_CAPTURE_LEVEL_WARN, 987654321ULL,
                         "MQTT", "event code: %d", 5),
          "native ESP_LOG metadata must be captured");

    log_capture_entry_t entry;
    CHECK(log_capture_drain(&capture, &entry, 1) == 1, "captured entry missing");
    CHECK(entry.level == LOG_CAPTURE_LEVEL_WARN, "native level was not preserved");
    CHECK(entry.timestamp_us == 987654321ULL, "uptime was not preserved");
    CHECK(strcmp(entry.tag, "MQTT") == 0, "native tag was not preserved");
    CHECK(strcmp(entry.message, "event code: 5") == 0, "native arguments were not formatted");

    const log_stream_entry_t protocol_entry = {
        .level = entry.level,
        .timestamp_us = entry.timestamp_us,
        .tag = entry.tag,
        .message = entry.message,
    };
    uint8_t frame[256];
    size_t frame_len = 0;
    CHECK(log_stream_encode(frame, sizeof(frame), &frame_len, 17,
                            &protocol_entry, 1) == FRAME_OK,
          "captured native entry must encode as MsgLogStream");
    CHECK(frame_len > 1 && frame[0] == MSG_LOG_STREAM,
          "captured native entry encoded with wrong message type");
}

static void test_native_log_capture_is_bounded_and_drops_oldest(void)
{
    log_capture_entry_t storage[2];
    log_capture_t capture;
    log_capture_init(&capture, storage, 2, LOG_CAPTURE_LEVEL_INFO);

    CHECK(capture_printf(&capture, LOG_CAPTURE_LEVEL_ERROR, 1, "A", "first"),
          "first entry rejected");
    CHECK(capture_printf(&capture, LOG_CAPTURE_LEVEL_WARN, 2, "B", "second"),
          "second entry rejected");
    CHECK(capture_printf(&capture, LOG_CAPTURE_LEVEL_INFO, 3, "C", "%0200d", 7),
          "newest entry rejected when ring was full");

    log_capture_entry_t entries[2];
    CHECK(log_capture_drain(&capture, entries, 2) == 2, "bounded ring count is wrong");
    CHECK(strcmp(entries[0].message, "second") == 0, "full ring must drop oldest");
    CHECK(entries[1].timestamp_us == 3, "newest entry must be retained");
    CHECK(strlen(entries[1].message) == LOG_CAPTURE_MESSAGE_MAX - 1,
          "formatted native message must be truncated to fixed upper bound");
    CHECK(log_capture_dropped_oldest(&capture) == 1, "overflow metric is wrong");
}

static void test_native_log_capture_filters_contention_and_feedback(void)
{
    log_capture_entry_t storage[2];
    log_capture_t capture;
    log_capture_init(&capture, storage, 2, LOG_CAPTURE_LEVEL_WARN);

    CHECK(!capture_printf(&capture, LOG_CAPTURE_LEVEL_INFO, 1, "FILTER", "info"),
          "entries above remote threshold must be rejected");

    log_capture_set_level(&capture, LOG_CAPTURE_LEVEL_VERBOSE);
    CHECK(capture_printf(&capture, LOG_CAPTURE_LEVEL_DEBUG, 2, "LEVEL", "updated"),
          "updated remote threshold must be applied by the capture ring");
    CHECK(log_capture_drain(&capture, storage, 2) == 1,
          "entry accepted after threshold update must reach the queue");

    log_capture_suppress(&capture);
    CHECK(!capture_printf(&capture, LOG_CAPTURE_LEVEL_ERROR, 3, "MQTT", "publish feedback"),
          "publish feedback must be structurally suppressed");
    log_capture_resume(&capture);

    CHECK(atomic_flag_test_and_set_explicit(&capture.lock, memory_order_acquire) == false,
          "test failed to hold capture lock");
    CHECK(!capture_printf(&capture, LOG_CAPTURE_LEVEL_ERROR, 4, "RACE", "contended"),
          "capture path must never wait for a contended ring");
    atomic_flag_clear_explicit(&capture.lock, memory_order_release);

    CHECK(log_capture_drain(&capture, storage, 2) == 0,
          "suppressed/contended entries must not reach queue");
    CHECK(log_capture_dropped_contention(&capture) == 1, "contention metric is wrong");
}

static void test_native_log_capture_resource_budget(void)
{
    enum {
        stream_ring_capacity = 4,
        tx_batch_capacity = 4,
        tx_buffer_bytes = 768,
        configured_task_stack_bytes = 1536,
        static_event_group_min_bytes = sizeof(uint32_t),
        task_handle_bytes = sizeof(void *),
        event_group_handle_bytes = sizeof(void *),
        state_atomic_bytes = sizeof(atomic_uint),
        capture_users_atomic_bytes = sizeof(atomic_uint),
        sequence_bytes = sizeof(uint16_t),
        publish_callback_bytes = sizeof(void (*)(const uint8_t *, size_t)),
        owned_ram_budget_bytes = 4096,
    };
    const size_t ring_bytes = stream_ring_capacity * sizeof(log_capture_entry_t);
    const size_t tx_batch_bytes = tx_batch_capacity * sizeof(log_capture_entry_t);
    const size_t static_capture_transport_bytes = sizeof(log_capture_t) + ring_bytes +
                                                  tx_batch_bytes + tx_buffer_bytes;
    const size_t known_control_bytes = static_event_group_min_bytes +
                                       task_handle_bytes + event_group_handle_bytes +
                                       state_atomic_bytes + capture_users_atomic_bytes +
                                       sequence_bytes + publish_callback_bytes;
    const size_t owned_ram_bytes = static_capture_transport_bytes +
                                   configured_task_stack_bytes + known_control_bytes;

    printf("log_capture owned RAM: entry=%zu capture_transport=%zu stack=%d "
           "known_control_min=%zu owned_min_total=%zu budget=%d; target build "
           "asserts the exact static event-group object; excludes dynamic TCB, "
           "allocator metadata, and TLS\n",
           sizeof(log_capture_entry_t), static_capture_transport_bytes,
           configured_task_stack_bytes, known_control_bytes, owned_ram_bytes,
           owned_ram_budget_bytes);
    CHECK(sizeof(log_capture_entry_t) <= 160,
          "each capture entry exceeds the per-entry RAM budget");
    CHECK(owned_ram_bytes <= owned_ram_budget_bytes,
          "known static buffers, configured stack, and control objects exceed RAM budget");
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
    test_native_log_capture_preserves_metadata_and_encodes_protocol();
    test_native_log_capture_is_bounded_and_drops_oldest();
    test_native_log_capture_filters_contention_and_feedback();
    test_native_log_capture_resource_budget();
    test_scheduler_queue_guard();

    if (s_failures != 0) {
        fprintf(stderr, "%d test(s) failed\n", s_failures);
        return 1;
    }
    printf("protocol_log_tests: all tests passed\n");
    return 0;
}
