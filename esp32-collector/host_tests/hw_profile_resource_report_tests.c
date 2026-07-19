#include <inttypes.h>
#include <stdbool.h>
#include <stdio.h>
#include <string.h>

#include "frame_codec.h"
#include "hw_profile.h"

static int s_failures;

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (message)); \
        s_failures++; \
        return; \
    } \
} while (0)

size_t dma_pool_serialize(dma_pool_t *pool, uint8_t *buf, size_t buf_size)
{
    (void)pool;
    (void)buf;
    (void)buf_size;
    return 0;
}

static bool decode_pwm_entry(const frame_field_t *entry, char *id, size_t id_size,
                             uint64_t *channel, uint64_t *timer_count,
                             uint64_t *max_resolution_bits)
{
    frame_decoder_t dec;
    frame_field_t field;
    if (entry->wire_type != WIRE_LENGTH_DELIMITED ||
        frame_decoder_init_sub(&dec, entry->value.bytes.ptr,
                               entry->value.bytes.len) != FRAME_OK) {
        return false;
    }

    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        switch (field.field_num) {
        case 1:
            if (frame_field_get_string(&field, id, id_size) != FRAME_OK) return false;
            break;
        case 2:
            if (frame_field_get_varint(&field, channel) != FRAME_OK) return false;
            break;
        case 3:
            if (frame_field_get_varint(&field, timer_count) != FRAME_OK) return false;
            break;
        case 4:
            if (frame_field_get_varint(&field, max_resolution_bits) != FRAME_OK) return false;
            break;
        default:
            break;
        }
    }
    return true;
}

static void test_resource_report_encodes_independent_c6_pwm_resources(void)
{
    uint8_t report[1024] = {0};
    size_t report_len = 0;
    CHECK(hw_profile_build_report(report, sizeof(report), &report_len,
                                  NULL, NULL, 0),
          "ResourceReport encode failed");
    CHECK(report[0] == MSG_RESOURCE_REPORT, "ResourceReport message type is wrong");

    frame_decoder_t report_dec;
    frame_field_t field;
    const uint8_t *buses = NULL;
    size_t buses_len = 0;
    const uint8_t *manifest_capacity = NULL;
    size_t manifest_capacity_len = 0;
    uint64_t resource_count = 0;
    CHECK(frame_decoder_init(&report_dec, report, report_len) == FRAME_OK,
          "ResourceReport decode failed");
    while (frame_decoder_next(&report_dec, &field) == FRAME_OK) {
        if (field.field_num == 2) {
            CHECK(frame_field_get_varint(&field, &resource_count) == FRAME_OK,
                  "resource_count decode failed");
        } else if (field.field_num == 3) {
            CHECK(frame_field_get_bytes(&field, &buses, &buses_len) == FRAME_OK,
                  "buses_blob decode failed");
        } else if (field.field_num == 10) {
            CHECK(frame_field_get_bytes(&field, &manifest_capacity, &manifest_capacity_len) == FRAME_OK,
                  "manifest capacity decode failed");
        }
    }
    CHECK(resource_count == HW_RESOURCE_COUNT, "resource_count must include PWM resources");
    CHECK(buses != NULL, "buses_blob is missing");
    CHECK(manifest_capacity != NULL, "manifest capacity is missing");

    frame_decoder_t capacity_dec;
    uint64_t max_templates = 0, max_channels = 0, max_template_ids = 0;
    CHECK(frame_decoder_init_sub(&capacity_dec, manifest_capacity, manifest_capacity_len) == FRAME_OK,
          "manifest capacity nested decode failed");
    while (frame_decoder_next(&capacity_dec, &field) == FRAME_OK) {
        uint64_t value = 0;
        CHECK(frame_field_get_varint(&field, &value) == FRAME_OK,
              "manifest capacity value decode failed");
        if (field.field_num == 1) max_templates = value;
        if (field.field_num == 2) max_channels = value;
        if (field.field_num == 3) max_template_ids = value;
    }
    CHECK(max_templates == MAX_TEMPLATES, "reported max templates differs from config_mgr");
    CHECK(max_channels == MAX_CHANNELS, "reported max channels differs from config_mgr");
    CHECK(max_template_ids == MAX_TEMPLATE_IDS, "reported max template ids differs from config_mgr");

    frame_decoder_t buses_dec;
    int pwm_count = 0;
    bool saw_pwm0 = false;
    bool saw_pwm5 = false;
    CHECK(frame_decoder_init_sub(&buses_dec, buses, buses_len) == FRAME_OK,
          "buses_blob nested decode failed");
    while (frame_decoder_next(&buses_dec, &field) == FRAME_OK) {
        if (field.field_num != 6) continue;

        char id[16] = {0};
        uint64_t channel = UINT64_MAX;
        uint64_t timer_count = 0;
        uint64_t max_resolution_bits = 0;
        CHECK(decode_pwm_entry(&field, id, sizeof(id), &channel,
                               &timer_count, &max_resolution_bits),
              "PWM entry decode failed");
        CHECK(timer_count == 4, "C6 PWM entry timer_count must be 4");
        CHECK(max_resolution_bits == 20,
              "C6 PWM entry max_resolution_bits must be 20");
        if (strcmp(id, "PWM0") == 0 && channel == 0) saw_pwm0 = true;
        if (strcmp(id, "PWM5") == 0 && channel == 5) saw_pwm5 = true;
        pwm_count++;
    }

    CHECK(pwm_count == 6, "C6 must report six independent PWM entries");
    CHECK(saw_pwm0, "PWM0 resource entry is missing");
    CHECK(saw_pwm5, "PWM5 resource entry is missing");
}

int main(void)
{
    test_resource_report_encodes_independent_c6_pwm_resources();
    if (s_failures != 0) {
        fprintf(stderr, "%d test(s) failed\n", s_failures);
        return 1;
    }
    printf("hw_profile_resource_report_tests: all tests passed\n");
    return 0;
}
