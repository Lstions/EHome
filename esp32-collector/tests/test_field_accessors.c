/**
 * @file test_field_accessors.c
 * @brief Unit tests for frame_field_get_* accessor functions (Step 1)
 *
 * Tests: NULL guard, string/varint/bytes accessors, truncation, type mismatch
 */

#include <stdio.h>
#include <string.h>
#include <assert.h>
#include "frame_codec.h"

static int tests_passed = 0;
static int tests_failed = 0;

#define TEST_ASSERT(cond, msg) do { \
    if (cond) { \
        printf("  PASS: %s\n", msg); \
        tests_passed++; \
    } else { \
        printf("  FAIL: %s (line %d)\n", msg, __LINE__); \
        tests_failed++; \
    } \
} while(0)

/* === Test: frame_encode_string NULL guard === */
void test_encode_string_null_guard(void)
{
    printf("\n[test_encode_string_null_guard]\n");
    uint8_t buf[64];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_HELLO);
    frame_err_t err = frame_encode_string(&enc, 1, NULL);
    
    TEST_ASSERT(err == FRAME_OK, "NULL string should encode as empty string");
    TEST_ASSERT(frame_encoder_size(&enc) > 1, "Should have encoded at least tag byte");
    
    /* Decode and verify empty string */
    frame_decoder_t dec;
    frame_decoder_init(&dec, buf, frame_encoder_size(&enc));
    frame_field_t field;
    err = frame_decoder_next(&dec, &field);
    TEST_ASSERT(err == FRAME_OK, "Should decode successfully");
    TEST_ASSERT(field.wire_type == WIRE_LENGTH_DELIMITED, "Should be length-delimited");
    TEST_ASSERT(field.value.bytes.len == 0, "Should be empty string (len=0)");
}

/* === Test: frame_field_get_string normal === */
void test_field_get_string_normal(void)
{
    printf("\n[test_field_get_string_normal]\n");
    uint8_t buf[64];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_HELLO);
    frame_encode_string(&enc, 1, "test_value");
    
    frame_decoder_t dec;
    frame_decoder_init(&dec, buf, frame_encoder_size(&enc));
    frame_field_t field;
    frame_decoder_next(&dec, &field);
    
    char out[32] = {0};
    frame_err_t err = frame_field_get_string(&field, out, sizeof(out));
    
    TEST_ASSERT(err == FRAME_OK, "Should extract string successfully");
    TEST_ASSERT(strcmp(out, "test_value") == 0, "String should match");
}

/* === Test: frame_field_get_string truncation === */
void test_field_get_string_truncation(void)
{
    printf("\n[test_field_get_string_truncation]\n");
    uint8_t buf[64];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_HELLO);
    frame_encode_string(&enc, 1, "long_string_value");
    
    frame_decoder_t dec;
    frame_decoder_init(&dec, buf, frame_encoder_size(&enc));
    frame_field_t field;
    frame_decoder_next(&dec, &field);
    
    char out[8] = {0};  /* Small buffer */
    frame_err_t err = frame_field_get_string(&field, out, sizeof(out));
    
    TEST_ASSERT(err == FRAME_OK, "Should succeed with truncation");
    TEST_ASSERT(strlen(out) == 7, "Should truncate to 7 chars + NUL");
    TEST_ASSERT(out[7] == '\0', "Should be NUL-terminated");
}

/* === Test: frame_field_get_string NULL ptr === */
void test_field_get_string_null_ptr(void)
{
    printf("\n[test_field_get_string_null_ptr]\n");
    
    frame_field_t field = {
        .field_num = 1,
        .wire_type = WIRE_LENGTH_DELIMITED,
        .value.bytes.ptr = NULL,
        .value.bytes.len = 0
    };
    
    char out[32] = "garbage";
    frame_err_t err = frame_field_get_string(&field, out, sizeof(out));
    
    TEST_ASSERT(err == FRAME_OK, "Should handle NULL ptr gracefully");
    TEST_ASSERT(out[0] == '\0', "Should set empty string");
}

/* === Test: frame_field_get_string type mismatch === */
void test_field_get_string_type_mismatch(void)
{
    printf("\n[test_field_get_string_type_mismatch]\n");
    
    frame_field_t field = {
        .field_num = 1,
        .wire_type = WIRE_VARINT,  /* Wrong type */
        .value.varint = 42
    };
    
    char out[32];
    frame_err_t err = frame_field_get_string(&field, out, sizeof(out));
    
    TEST_ASSERT(err == FRAME_ERR_INVALID_TAG, "Should reject wrong wire type");
}

/* === Test: frame_field_get_varint normal === */
void test_field_get_varint_normal(void)
{
    printf("\n[test_field_get_varint_normal]\n");
    uint8_t buf[64];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_STATUS_RPT);
    frame_encode_varint(&enc, 1, 12345);
    
    frame_decoder_t dec;
    frame_decoder_init(&dec, buf, frame_encoder_size(&enc));
    frame_field_t field;
    frame_decoder_next(&dec, &field);
    
    uint64_t value = 0;
    frame_err_t err = frame_field_get_varint(&field, &value);
    
    TEST_ASSERT(err == FRAME_OK, "Should extract varint successfully");
    TEST_ASSERT(value == 12345, "Value should match");
}

/* === Test: frame_field_get_varint type mismatch === */
void test_field_get_varint_type_mismatch(void)
{
    printf("\n[test_field_get_varint_type_mismatch]\n");
    
    frame_field_t field = {
        .field_num = 1,
        .wire_type = WIRE_LENGTH_DELIMITED,  /* Wrong type */
        .value.bytes.ptr = NULL,
        .value.bytes.len = 0
    };
    
    uint64_t value;
    frame_err_t err = frame_field_get_varint(&field, &value);
    
    TEST_ASSERT(err == FRAME_ERR_INVALID_TAG, "Should reject wrong wire type");
}

/* === Test: frame_field_get_bytes === */
void test_field_get_bytes(void)
{
    printf("\n[test_field_get_bytes]\n");
    uint8_t buf[64];
    uint8_t raw_data[] = {0x01, 0x02, 0x03, 0x04, 0x05};
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_DATA_RPT);
    frame_encode_bytes(&enc, 4, raw_data, sizeof(raw_data));
    
    frame_decoder_t dec;
    frame_decoder_init(&dec, buf, frame_encoder_size(&enc));
    frame_field_t field;
    frame_decoder_next(&dec, &field);
    
    const uint8_t *data;
    size_t len;
    frame_err_t err = frame_field_get_bytes(&field, &data, &len);
    
    TEST_ASSERT(err == FRAME_OK, "Should extract bytes successfully");
    TEST_ASSERT(len == 5, "Length should match");
    TEST_ASSERT(memcmp(data, raw_data, len) == 0, "Data should match");
}

int main(void)
{
    printf("=== Field Accessor Unit Tests ===\n");
    
    test_encode_string_null_guard();
    test_field_get_string_normal();
    test_field_get_string_truncation();
    test_field_get_string_null_ptr();
    test_field_get_string_type_mismatch();
    test_field_get_varint_normal();
    test_field_get_varint_type_mismatch();
    test_field_get_bytes();
    
    printf("\n=== Results ===\n");
    printf("Passed: %d\n", tests_passed);
    printf("Failed: %d\n", tests_failed);
    
    return tests_failed > 0 ? 1 : 0;
}
