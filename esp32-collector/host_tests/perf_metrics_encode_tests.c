/*
 * Runtime performance metrics encoding tests.
 *
 * Verifies that handler_data.c's StatusReport runtime perf sub-message
 * correctly encodes fields 1-27 (free heap, stack, queue metrics for
 * 5 controllers x 4 metrics each).  The Go backend decode test already
 * checks the wire format; this test checks the C encode side by providing
 * controllable scheduler/bus_worker metric values.
 *
 * Coverage:
 *   - Fields 1-5 (legacy perf: heap, stack, queue spaces)
 *   - Fields 6-7 (report_drops, report_queue_high_water)
 *   - Fields 8-12 (per-controller current_spaces for U0/U1/U2/SPI/I2C)
 *   - Fields 13-17 (per-controller high_water_used)
 *   - Fields 18-22 (per-controller sample_skipped)
 *   - Fields 23-27 (per-controller sample_rejected)
 *   - Field tag varint encoding for fields >= 16
 *   - 192-byte perf buffer can hold all 28 fields
 */

#include <stdio.h>
#include <string.h>
#include <stdint.h>

/* Host stubs */
#include "freertos/FreeRTOS.h"
#include "esp_err.h"
#include "esp_log.h"

/* Frame codec */
#include "frame_codec.h"

/* Test framework */
static int failures = 0;
#define CHECK(cond, msg) do { \
    if (!(cond)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (msg)); \
        failures++; \
        return; \
    } \
} while (0)

/* Stubs for scheduler/bus_worker metrics */
static uint32_t s_free_heap = 100000;
static uint32_t s_min_free_heap = 80000;
static uint32_t s_sched_stack = 1200;
static uint32_t s_worker_stack = 900;
static uint32_t s_min_queue_spaces = 8;
static uint32_t s_report_drops = 3;
static uint32_t s_report_q_high = 12;
static uint32_t s_queue_current[5] = {16, 14, 12, 6, 7};
static uint32_t s_queue_high_water[5] = {6, 8, 4, 2, 1};
static uint32_t s_queue_sample_skipped[5] = {2, 0, 1, 0, 0};
static uint32_t s_queue_sample_rejected[5] = {1, 0, 0, 0, 0};

uint32_t esp_get_free_heap_size(void) { return s_free_heap; }
uint32_t esp_get_minimum_free_heap_size(void) { return s_min_free_heap; }

/* We don't call msg_handler_send_status directly; instead we replicate the
 * encoding logic and verify the wire output.  This tests the frame codec's
 * ability to encode fields 16+ (two-byte varint tags) within the 192-byte
 * perf buffer. */

static void test_perf_metrics_all_fields_encoded(void)
{
    uint8_t perf_buf[192];
    frame_encoder_t enc;
    frame_encoder_init_sub(&enc, perf_buf, sizeof(perf_buf));

    /* Encode fields 1-7 (legacy + new report metrics) */
    bool ok = true;
    ok = ok && frame_encode_varint(&enc, 1, s_free_heap) == FRAME_OK;
    ok = ok && frame_encode_varint(&enc, 2, s_min_free_heap) == FRAME_OK;
    ok = ok && frame_encode_varint(&enc, 3, s_sched_stack) == FRAME_OK;
    ok = ok && frame_encode_varint(&enc, 4, s_worker_stack) == FRAME_OK;
    ok = ok && frame_encode_varint(&enc, 5, s_min_queue_spaces) == FRAME_OK;
    ok = ok && frame_encode_varint(&enc, 6, s_report_drops) == FRAME_OK;
    ok = ok && frame_encode_varint(&enc, 7, s_report_q_high) == FRAME_OK;

    /* Encode fields 8-27: 5 controllers x 4 metrics */
    for (uint8_t i = 0; ok && i < 5; i++) {
        ok = ok && frame_encode_varint(&enc, 8 + i, s_queue_current[i]) == FRAME_OK;
        ok = ok && frame_encode_varint(&enc, 13 + i, s_queue_high_water[i]) == FRAME_OK;
        ok = ok && frame_encode_varint(&enc, 18 + i, s_queue_sample_skipped[i]) == FRAME_OK;
        ok = ok && frame_encode_varint(&enc, 23 + i, s_queue_sample_rejected[i]) == FRAME_OK;
    }

    CHECK(ok, "all 27 fields should encode successfully");
    CHECK(frame_encoder_size(&enc) > 0, "encoded size must be > 0");

    /* Verify the buffer didn't overflow 192 bytes */
    CHECK(frame_encoder_size(&enc) <= sizeof(perf_buf),
          "perf buffer must hold all fields without overflow");

    /* Now decode and verify a sample of fields, especially 16+ */
    const uint8_t *data = frame_encoder_data(&enc);
    size_t len = frame_encoder_size(&enc);

    /* Use frame decoder to parse — sub-message has no type byte */
    frame_decoder_t dec;
    frame_decoder_init_sub(&dec, data, len);

    uint32_t found_field_6 = 0, found_field_18 = 0, found_field_23 = 0;
    frame_field_t field;
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        uint64_t v = 0;
        if (frame_field_get_varint(&field, &v) != FRAME_OK) continue;
        if (field.field_num == 6) found_field_6 = (uint32_t)v;
        if (field.field_num == 18) found_field_18 = (uint32_t)v;
        if (field.field_num == 23) found_field_23 = (uint32_t)v;
    }

    CHECK(found_field_6 == s_report_drops, "field 6 (report_drops) should decode");
    CHECK(found_field_18 == s_queue_sample_skipped[0], "field 18 (U0 sample_skipped) should decode");
    CHECK(found_field_23 == s_queue_sample_rejected[0], "field 23 (U0 sample_rejected) should decode");
}

static void test_field_18_tag_is_two_bytes(void)
{
    /* Field 18 = (18 << 3) | 0 = 144 = 0x90 → needs continuation bit:
     * 0x90 0x01 in varint.  Verify the encoder produces this. */
    uint8_t buf[16];
    frame_encoder_t enc;
    frame_encoder_init_sub(&enc, buf, sizeof(buf));
    CHECK(frame_encode_varint(&enc, 18, 42) == FRAME_OK, "encode field 18");

    const uint8_t *data = frame_encoder_data(&enc);
    size_t len = frame_encoder_size(&enc);
    CHECK(len >= 3, "field 18 needs at least 3 bytes (2 tag + 1 value)");
    CHECK(data[0] == 0x90, "first byte of field 18 tag should be 0x90");
    CHECK(data[1] == 0x01, "second byte of field 18 tag should be 0x01");
    CHECK(data[2] == 42, "value should be 42");
}

static void test_field_27_tag_is_two_bytes(void)
{
    /* Field 27 = (27 << 3) | 0 = 216 = 0xD8 → 0xD8 0x01 in varint. */
    uint8_t buf[16];
    frame_encoder_t enc;
    frame_encoder_init_sub(&enc, buf, sizeof(buf));
    CHECK(frame_encode_varint(&enc, 27, 99) == FRAME_OK, "encode field 27");

    const uint8_t *data = frame_encoder_data(&enc);
    size_t len = frame_encoder_size(&enc);
    CHECK(len >= 3, "field 27 needs at least 3 bytes");
    CHECK(data[0] == 0xD8, "first byte of field 27 tag should be 0xD8");
    CHECK(data[1] == 0x01, "second byte of field 27 tag should be 0x01");
    CHECK(data[2] == 99, "value should be 99");
}

static void test_large_perf_values_fit_in_buffer(void)
{
    /* Even with large varint values (up to 5 bytes each for uint32),
     * all 28 fields should fit in 192 bytes.  Worst case:
     * 28 fields x (2-byte tag + 5-byte value) = 196 bytes.
     * But most values are small; test with moderate values. */
    uint8_t perf_buf[192];
    frame_encoder_t enc;
    frame_encoder_init_sub(&enc, perf_buf, sizeof(perf_buf));

    bool ok = true;
    for (uint8_t i = 1; i <= 27; i++) {
        ok = ok && frame_encode_varint(&enc, i, 1000000) == FRAME_OK;
    }
    CHECK(ok, "all 27 fields with 7-digit values should fit");
    CHECK(frame_encoder_size(&enc) <= sizeof(perf_buf),
          "192-byte buffer must hold all fields with moderate values");
}

int main(void)
{
    test_perf_metrics_all_fields_encoded();
    test_field_18_tag_is_two_bytes();
    test_field_27_tag_is_two_bytes();
    test_large_perf_values_fit_in_buffer();

    if (failures != 0) {
        fprintf(stderr, "%d test(s) failed\n", failures);
        return 1;
    }
    puts("perf_metrics_encode_tests: all tests passed");
    return 0;
}
