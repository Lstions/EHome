#include <stdio.h>
#include <string.h>
#include "frame_codec.h"

static int tests_passed = 0;
static int tests_failed = 0;

#define ASSERT_EQ(a, b, msg) do { \
    if ((a) != (b)) { \
        printf("FAIL: %s: expected %d, got %d\n", msg, (int)(b), (int)(a)); \
        tests_failed++; \
    } else { \
        printf("PASS: %s\n", msg); \
        tests_passed++; \
    } \
} while(0)

#define ASSERT_STR_EQ(a, b, len, msg) do { \
    if (memcmp(a, b, len) != 0) { \
        printf("FAIL: %s\n", msg); \
        tests_failed++; \
    } else { \
        printf("PASS: %s\n", msg); \
        tests_passed++; \
    } \
} while(0)

void test_hello_encode_decode(void)
{
    printf("\n=== Test: Hello Encode/Decode ===\n");
    uint8_t buf[256];
    frame_encoder_t enc;
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_HELLO);
    frame_encode_string(&enc, 1, "AABBCCDDEEFF");
    frame_encode_string(&enc, 2, "2.0.0");
    frame_encode_string(&enc, 3, "ESP32S3");
    frame_encode_varint(&enc, 4, 4);
    
    printf("Encoded %zu bytes: ", enc.pos);
    for (size_t i = 0; i < enc.pos; i++) printf("%02X", buf[i]);
    printf("\n");
    
    frame_decoder_t dec;
    frame_err_t err = frame_decoder_init(&dec, buf, enc.pos);
    ASSERT_EQ(err, FRAME_OK, "decoder init");
    
    frame_field_t field;
    
    err = frame_decoder_next(&dec, &field);
    ASSERT_EQ(err, FRAME_OK, "field 1 decode");
    ASSERT_EQ(field.field_num, 1, "field 1 num");
    ASSERT_EQ(field.wire_type, WIRE_LENGTH_DELIMITED, "field 1 wire type");
    ASSERT_EQ(field.value.bytes.len, 12, "field 1 len");
    ASSERT_STR_EQ(field.value.bytes.ptr, "AABBCCDDEEFF", 12, "field 1 value");
    
    err = frame_decoder_next(&dec, &field);
    ASSERT_EQ(err, FRAME_OK, "field 2 decode");
    ASSERT_EQ(field.field_num, 2, "field 2 num");
    ASSERT_EQ(field.value.bytes.len, 5, "field 2 len");
    
    err = frame_decoder_next(&dec, &field);
    ASSERT_EQ(err, FRAME_OK, "field 3 decode");
    ASSERT_EQ(field.field_num, 3, "field 3 num");
    
    err = frame_decoder_next(&dec, &field);
    ASSERT_EQ(err, FRAME_OK, "field 4 decode");
    ASSERT_EQ(field.field_num, 4, "field 4 num");
    ASSERT_EQ(field.value.varint, 4, "field 4 value");
    
    err = frame_decoder_next(&dec, &field);
    ASSERT_EQ(err, FRAME_DONE, "end of frame");
}

void test_data_report_encode_decode(void)
{
    printf("\n=== Test: DataReport Encode/Decode ===\n");
    uint8_t buf[256];
    frame_encoder_t enc;
    uint8_t raw[] = {0x01, 0x02, 0x03, 0x04, 0x05};
    
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_DATA_RPT);
    frame_encode_varint(&enc, 1, 1);
    frame_encode_varint(&enc, 2, 12345678);
    frame_encode_varint(&enc, 3, 42);
    frame_encode_bytes(&enc, 4, raw, sizeof(raw));
    
    printf("Encoded %zu bytes: ", enc.pos);
    for (size_t i = 0; i < enc.pos; i++) printf("%02X", buf[i]);
    printf("\n");
    
    frame_decoder_t dec;
    frame_decoder_init(&dec, buf, enc.pos);
    
    frame_field_t field;
    frame_decoder_next(&dec, &field);
    ASSERT_EQ(field.field_num, 1, "channel_id field");
    ASSERT_EQ(field.value.varint, 1, "channel_id value");
    
    frame_decoder_next(&dec, &field);
    ASSERT_EQ(field.field_num, 2, "timestamp field");
    ASSERT_EQ(field.value.varint, 12345678, "timestamp value");
    
    frame_decoder_next(&dec, &field);
    ASSERT_EQ(field.field_num, 3, "sequence field");
    ASSERT_EQ(field.value.varint, 42, "sequence value");
    
    frame_decoder_next(&dec, &field);
    ASSERT_EQ(field.field_num, 4, "raw_data field");
    ASSERT_EQ(field.value.bytes.len, 5, "raw_data len");
    ASSERT_EQ(field.value.bytes.ptr[0], 0x01, "raw_data[0]");
}

void test_varint_edge_cases(void)
{
    printf("\n=== Test: Varint Edge Cases ===\n");
    uint8_t buf[256];
    frame_encoder_t enc;
    
    struct {
        uint64_t value;
    } cases[] = {
        {0}, {1}, {127}, {128}, {255}, {0x4000}
    };
    
    for (size_t i = 0; i < sizeof(cases)/sizeof(cases[0]); i++) {
        frame_encoder_init(&enc, buf, sizeof(buf), MSG_STATUS_RPT);
        frame_encode_varint(&enc, 1, cases[i].value);
        
        frame_decoder_t dec;
        frame_decoder_init(&dec, buf, enc.pos);
        frame_field_t field;
        frame_decoder_next(&dec, &field);
        ASSERT_EQ(field.value.varint, cases[i].value, "varint roundtrip");
    }
}

int main(void)
{
    printf("========================================\n");
    printf("ESP32 Frame Codec - Unit Tests\n");
    printf("========================================\n");
    
    test_hello_encode_decode();
    test_data_report_encode_decode();
    test_varint_edge_cases();
    
    printf("\n========================================\n");
    printf("Results: %d passed, %d failed\n", tests_passed, tests_failed);
    printf("========================================\n");
    
    return tests_failed > 0 ? 1 : 0;
}
