/**
 * frame_codec_boundary_tests.c
 *
 * Host-side tests for the boundary hardening added to frame_decoder_next():
 *   - field_num == 0 rejected (FRAME_ERR_INVALID_TAG)
 *   - field_num > UINT8_MAX rejected (FRAME_ERR_INVALID_TAG)
 *   - length-delimited len > SIZE_MAX rejected (FRAME_ERR_OVERFLOW)
 *
 * Also verifies that valid boundary values (field_num=1, field_num=255)
 * still decode correctly.
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include "frame_codec.h"

static int tests_run = 0;
static int tests_passed = 0;

#define CHECK(cond, msg) do { \
    tests_run++; \
    if (cond) { tests_passed++; } \
    else { printf("FAIL [%d]: %s\n", tests_run, msg); } \
} while (0)

/* Encode a varint into buf, return bytes written */
static size_t put_varint(uint8_t *buf, uint64_t value) {
    size_t i = 0;
    while (value > 0x7F) {
        buf[i++] = (uint8_t)((value & 0x7F) | 0x80);
        value >>= 7;
    }
    buf[i++] = (uint8_t)(value & 0x7F);
    return i;
}

/* --- field_num == 0 must be rejected --- */
static void test_field_num_zero(void) {
    /* tag = (0 << 3) | WIRE_VARINT = 0x00, followed by varint value 42 */
    uint8_t buf[4];
    size_t pos = 0;
    buf[pos++] = 0x00; /* field_num=0, wire_type=0 */
    pos += put_varint(&buf[pos], 42);

    frame_decoder_t dec;
    frame_decoder_init_sub(&dec, buf, pos);
    frame_field_t field;
    frame_err_t err = frame_decoder_next(&dec, &field);
    CHECK(err == FRAME_ERR_INVALID_TAG, "field_num=0 varint should return FRAME_ERR_INVALID_TAG");
}

static void test_field_num_zero_length_delimited(void) {
    /* tag = (0 << 3) | WIRE_LENGTH_DELIMITED = 0x02 */
    uint8_t buf[4];
    size_t pos = 0;
    buf[pos++] = 0x02; /* field_num=0, wire_type=2 */
    buf[pos++] = 0x01; /* length=1 */
    buf[pos++] = 0xAA; /* payload */

    frame_decoder_t dec;
    frame_decoder_init_sub(&dec, buf, pos);
    frame_field_t field;
    frame_err_t err = frame_decoder_next(&dec, &field);
    CHECK(err == FRAME_ERR_INVALID_TAG, "field_num=0 length-delimited should return FRAME_ERR_INVALID_TAG");
}

/* --- field_num > UINT8_MAX (255) must be rejected --- */
static void test_field_num_too_large(void) {
    /* field_num=256 → tag = (256 << 3) | 0 = 2048 = 0x800 */
    /* varint encoding of 2048: 0x80, 0x10 */
    uint8_t buf[8];
    size_t pos = 0;
    pos += put_varint(&buf[pos], (uint64_t)256 << 3); /* field_num=256, wire_type=0 */
    pos += put_varint(&buf[pos], 1); /* varint value */

    frame_decoder_t dec;
    frame_decoder_init_sub(&dec, buf, pos);
    frame_field_t field;
    frame_err_t err = frame_decoder_next(&dec, &field);
    CHECK(err == FRAME_ERR_INVALID_TAG, "field_num=256 should return FRAME_ERR_INVALID_TAG");
}

static void test_field_num_way_too_large(void) {
    /* field_num=100000 → tag = 800000 */
    uint8_t buf[16];
    size_t pos = 0;
    pos += put_varint(&buf[pos], (uint64_t)100000 << 3);
    pos += put_varint(&buf[pos], 1);

    frame_decoder_t dec;
    frame_decoder_init_sub(&dec, buf, pos);
    frame_field_t field;
    frame_err_t err = frame_decoder_next(&dec, &field);
    CHECK(err == FRAME_ERR_INVALID_TAG, "field_num=100000 should return FRAME_ERR_INVALID_TAG");
}

/* --- valid boundary: field_num=1 (minimum valid) --- */
static void test_field_num_one(void) {
    uint8_t buf[8];
    size_t pos = 0;
    buf[pos++] = (1 << 3) | WIRE_VARINT; /* field_num=1, wire_type=0 */
    pos += put_varint(&buf[pos], 99);

    frame_decoder_t dec;
    frame_decoder_init_sub(&dec, buf, pos);
    frame_field_t field;
    frame_err_t err = frame_decoder_next(&dec, &field);
    CHECK(err == FRAME_OK, "field_num=1 should decode OK");
    CHECK(field.field_num == 1, "field_num should be 1");
    CHECK(field.value.varint == 99, "varint value should be 99");
}

/* --- valid boundary: field_num=255 (maximum valid) --- */
static void test_field_num_255(void) {
    uint8_t buf[16];
    size_t pos = 0;
    /* tag = (255 << 3) | WIRE_VARINT = 2040 | 0 = 2040 */
    pos += put_varint(&buf[pos], (uint64_t)255 << 3);
    pos += put_varint(&buf[pos], 7);

    frame_decoder_t dec;
    frame_decoder_init_sub(&dec, buf, pos);
    frame_field_t field;
    frame_err_t err = frame_decoder_next(&dec, &field);
    CHECK(err == FRAME_OK, "field_num=255 should decode OK");
    CHECK(field.field_num == 255, "field_num should be 255");
    CHECK(field.value.varint == 7, "varint value should be 7");
}

/* --- valid length-delimited round-trip --- */
static void test_length_delimited_valid(void) {
    uint8_t buf[32];
    frame_encoder_t enc;
    frame_encoder_init_sub(&enc, buf, sizeof(buf));
    const uint8_t payload[] = {0xDE, 0xAD, 0xBE, 0xEF};
    frame_encode_bytes(&enc, 3, payload, 4);

    frame_decoder_t dec;
    frame_decoder_init_sub(&dec, buf, frame_encoder_size(&enc));
    frame_field_t field;
    frame_err_t err = frame_decoder_next(&dec, &field);
    CHECK(err == FRAME_OK, "length-delimited decode should be OK");
    CHECK(field.field_num == 3, "field_num should be 3");
    CHECK(field.wire_type == WIRE_LENGTH_DELIMITED, "wire_type should be LENGTH_DELIMITED");
    CHECK(field.value.bytes.len == 4, "bytes len should be 4");
    CHECK(memcmp(field.value.bytes.ptr, payload, 4) == 0, "bytes payload should match");
}

/* --- truncated length-delimited (len > remaining buffer) --- */
static void test_length_delimited_truncated(void) {
    /* tag: field=1, wire=2 → 0x0A; length=100 but only 2 bytes follow */
    uint8_t buf[4];
    buf[0] = 0x0A;
    buf[1] = 100; /* claims 100 bytes */
    buf[2] = 0x01;
    buf[3] = 0x02;

    frame_decoder_t dec;
    frame_decoder_init_sub(&dec, buf, 4);
    frame_field_t field;
    frame_err_t err = frame_decoder_next(&dec, &field);
    CHECK(err == FRAME_ERR_UNDERFLOW, "truncated length-delimited should return FRAME_ERR_UNDERFLOW");
}

/* --- empty message → FRAME_DONE --- */
static void test_empty_message(void) {
    frame_decoder_t dec;
    frame_decoder_init_sub(&dec, NULL, 0);
    frame_field_t field;
    frame_err_t err = frame_decoder_next(&dec, &field);
    CHECK(err == FRAME_DONE, "empty message should return FRAME_DONE");
}

/* --- multiple fields decode sequentially --- */
static void test_multiple_fields(void) {
    uint8_t buf[64];
    frame_encoder_t enc;
    frame_encoder_init_sub(&enc, buf, sizeof(buf));
    frame_encode_varint(&enc, 1, 100);
    frame_encode_varint(&enc, 2, 200);
    frame_encode_varint(&enc, 3, 300);

    frame_decoder_t dec;
    frame_decoder_init_sub(&dec, buf, frame_encoder_size(&enc));
    frame_field_t field;

    frame_err_t err = frame_decoder_next(&dec, &field);
    CHECK(err == FRAME_OK && field.field_num == 1 && field.value.varint == 100, "field 1");

    err = frame_decoder_next(&dec, &field);
    CHECK(err == FRAME_OK && field.field_num == 2 && field.value.varint == 200, "field 2");

    err = frame_decoder_next(&dec, &field);
    CHECK(err == FRAME_OK && field.field_num == 3 && field.value.varint == 300, "field 3");

    err = frame_decoder_next(&dec, &field);
    CHECK(err == FRAME_DONE, "after last field should be FRAME_DONE");
}

int main(void) {
    printf("=== frame_codec boundary tests ===\n");

    test_field_num_zero();
    test_field_num_zero_length_delimited();
    test_field_num_too_large();
    test_field_num_way_too_large();
    test_field_num_one();
    test_field_num_255();
    test_length_delimited_valid();
    test_length_delimited_truncated();
    test_empty_message();
    test_multiple_fields();

    printf("\n%d/%d tests passed\n", tests_passed, tests_run);
    return (tests_passed == tests_run) ? 0 : 1;
}
