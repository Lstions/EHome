#include <stdarg.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define CHECK(expr) do { \
    if (!(expr)) { \
        fprintf(stderr, "CHECK failed at %s:%d: %s\n", __FILE__, __LINE__, #expr); \
        abort(); \
    } \
} while (0)

#include "config_mgr.h"
#include "frame_codec.h"
#include "nvs_flash.h"
#include "freertos/semphr.h"

void host_test_log_record(char level, const char *tag, const char *format, ...)
{ (void)level; (void)tag; (void)format; }
const char *esp_err_to_name(esp_err_t err) { (void)err; return "host"; }
SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (SemaphoreHandle_t)1; }
int xSemaphoreTake(SemaphoreHandle_t semaphore, uint32_t ticks)
{ (void)semaphore; (void)ticks; return 1; }
int xSemaphoreGive(SemaphoreHandle_t semaphore) { (void)semaphore; return 1; }
esp_err_t nvs_open(const char *name, int mode, nvs_handle_t *handle)
{ (void)name; (void)mode; (void)handle; return ESP_FAIL; }
esp_err_t nvs_get_u64(nvs_handle_t h, const char *k, uint64_t *v)
{ (void)h; (void)k; (void)v; return ESP_FAIL; }
esp_err_t nvs_get_str(nvs_handle_t h, const char *k, char *v, size_t *l)
{ (void)h; (void)k; (void)v; (void)l; return ESP_FAIL; }
esp_err_t nvs_set_u64(nvs_handle_t h, const char *k, uint64_t v)
{ (void)h; (void)k; (void)v; return ESP_FAIL; }
esp_err_t nvs_set_str(nvs_handle_t h, const char *k, const char *v)
{ (void)h; (void)k; (void)v; return ESP_FAIL; }
esp_err_t nvs_erase_key(nvs_handle_t h, const char *k)
{ (void)h; (void)k; return ESP_FAIL; }
esp_err_t nvs_commit(nvs_handle_t h) { (void)h; return ESP_FAIL; }
void nvs_close(nvs_handle_t h) { (void)h; }

static size_t build_manifest(uint8_t *out, size_t capacity)
{
    uint8_t pwm[64];
    frame_encoder_t sub;
    frame_encoder_init(&sub, pwm, sizeof(pwm), 0);
    CHECK(frame_encode_varint(&sub, 1, 5) == FRAME_OK);
    CHECK(frame_encode_varint(&sub, 2, 6) == FRAME_OK);
    CHECK(frame_encode_varint(&sub, 3, 1234) == FRAME_OK);
    CHECK(frame_encode_varint(&sub, 4, 4321) == FRAME_OK);
    CHECK(frame_encode_varint(&sub, 5, 14) == FRAME_OK);
    CHECK(frame_encode_varint(&sub, 6, 1) == FRAME_OK);

    frame_encoder_t manifest;
    frame_encoder_init(&manifest, out, capacity, MSG_CONFIG_MFST);
    CHECK(frame_encode_string(&manifest, 1, "pwm-layout") == FRAME_OK);
    CHECK(frame_encode_string(&manifest, 8, "sync-test") == FRAME_OK);
    CHECK(frame_encode_bytes(&manifest, 12, pwm + 1,
                              frame_encoder_size(&sub) - 1) == FRAME_OK);
    return frame_encoder_size(&manifest);
}

static size_t build_conflicting_manifest(uint8_t *out, size_t capacity)
{
    uint8_t channel[32];
    frame_encoder_t channel_enc;
    frame_encoder_init(&channel_enc, channel, sizeof(channel), 0);
    CHECK(frame_encode_varint(&channel_enc, 1, 1) == FRAME_OK);
    CHECK(frame_encode_varint(&channel_enc, 5, 1) == FRAME_OK);
    CHECK(frame_encode_varint(&channel_enc, 6, 1) == FRAME_OK);
    const uint8_t uart_config[] = {6, 7, 0, 1, 0xC2, 0};
    CHECK(frame_encode_bytes(&channel_enc, 7, uart_config, sizeof(uart_config)) == FRAME_OK);

    uint8_t gpio[16];
    frame_encoder_t gpio_enc;
    frame_encoder_init(&gpio_enc, gpio, sizeof(gpio), 0);
    CHECK(frame_encode_varint(&gpio_enc, 1, 6) == FRAME_OK);
    CHECK(frame_encode_varint(&gpio_enc, 2, 1) == FRAME_OK);

    frame_encoder_t manifest;
    frame_encoder_init(&manifest, out, capacity, MSG_CONFIG_MFST);
    CHECK(frame_encode_string(&manifest, 1, "conflict") == FRAME_OK);
    CHECK(frame_encode_bytes(&manifest, 4, channel + 1,
                              frame_encoder_size(&channel_enc) - 1) == FRAME_OK);
    CHECK(frame_encode_bytes(&manifest, 11, gpio + 1,
                              frame_encoder_size(&gpio_enc) - 1) == FRAME_OK);
    return frame_encoder_size(&manifest);
}

static size_t build_stopped_pwm_gpio_conflict(uint8_t *out, size_t capacity)
{
    uint8_t gpio[16], pwm[32];
    frame_encoder_t gpio_enc, pwm_enc, manifest;
    frame_encoder_init(&gpio_enc, gpio, sizeof(gpio), 0);
    CHECK(frame_encode_varint(&gpio_enc, 1, 6) == FRAME_OK);
    CHECK(frame_encode_varint(&gpio_enc, 2, 1) == FRAME_OK);
    frame_encoder_init(&pwm_enc, pwm, sizeof(pwm), 0);
    CHECK(frame_encode_varint(&pwm_enc, 1, 0) == FRAME_OK);
    CHECK(frame_encode_varint(&pwm_enc, 2, 6) == FRAME_OK);
    CHECK(frame_encode_varint(&pwm_enc, 3, 1000) == FRAME_OK);
    CHECK(frame_encode_varint(&pwm_enc, 5, 14) == FRAME_OK);
    CHECK(frame_encode_varint(&pwm_enc, 6, 0) == FRAME_OK);
    frame_encoder_init(&manifest, out, capacity, MSG_CONFIG_MFST);
    CHECK(frame_encode_string(&manifest, 1, "stopped-conflict") == FRAME_OK);
    CHECK(frame_encode_bytes(&manifest, 11, gpio + 1, frame_encoder_size(&gpio_enc) - 1) == FRAME_OK);
    CHECK(frame_encode_bytes(&manifest, 12, pwm + 1, frame_encoder_size(&pwm_enc) - 1) == FRAME_OK);
    return frame_encoder_size(&manifest);
}

static size_t build_repeated_empty(uint8_t *out, size_t capacity,
                                   uint8_t field_num, size_t count)
{
    frame_encoder_t manifest;
    frame_encoder_init(&manifest, out, capacity, MSG_CONFIG_MFST);
    CHECK(frame_encode_string(&manifest, 1, "overflow") == FRAME_OK);
    for (size_t i = 0; i < count; i++) {
        CHECK(frame_encode_bytes(&manifest, field_num, NULL, 0) == FRAME_OK);
    }
    return frame_encoder_size(&manifest);
}

static void assert_rejected_without_active_change(const uint8_t *frame, size_t length)
{
    config_manifest_t before;
    CHECK(config_mgr_snapshot_active(&before));
    CHECK(!config_mgr_stage_manifest(frame, length));
    CHECK(config_mgr_get_staged_manifest() == NULL);
    CHECK(memcmp(config_mgr_get_manifest(), &before, sizeof(before)) == 0);
}

static size_t build_manifest_with_nested(uint8_t *out, size_t capacity,
                                         uint8_t field_num,
                                         const uint8_t *nested, size_t nested_len)
{
    frame_encoder_t manifest;
    frame_encoder_init(&manifest, out, capacity, MSG_CONFIG_MFST);
    CHECK(frame_encode_string(&manifest, 1, "rejected") == FRAME_OK);
    CHECK(frame_encode_bytes(&manifest, field_num, nested, nested_len) == FRAME_OK);
    return frame_encoder_size(&manifest);
}

static size_t build_nested_repeated_empty(uint8_t *out, size_t capacity,
                                          uint8_t nested_field_num, size_t count)
{
    frame_encoder_t nested;
    frame_encoder_init(&nested, out, capacity, 0);
    for (size_t i = 0; i < count; i++) {
        CHECK(frame_encode_bytes(&nested, nested_field_num, NULL, 0) == FRAME_OK);
    }
    return frame_encoder_size(&nested) - 1;
}

static void test_repeated_manifest_arrays_reject_overflow(void)
{
    uint8_t frame[512];
    const struct { uint8_t field; size_t limit; } cases[] = {
        {3, MAX_TEMPLATES}, {4, MAX_CHANNELS}, {5, MAX_DMA_CONFIGS},
        {11, MAX_GPIO_CONFIGS}, {12, MAX_PWM_CONFIGS},
    };
    for (size_t i = 0; i < sizeof(cases) / sizeof(cases[0]); i++) {
        size_t length = build_repeated_empty(frame, sizeof(frame), cases[i].field,
                                             cases[i].limit + 1);
        assert_rejected_without_active_change(frame, length);
    }
}

static void test_malformed_decoder_termination_is_rejected(void)
{
    uint8_t malformed_top[] = {MSG_CONFIG_MFST, 0x0A, 0x02, 'o'};
    assert_rejected_without_active_change(malformed_top, sizeof(malformed_top));

    const uint8_t malformed_nested[] = {0x80};
    const uint8_t nested_fields[] = {3, 4, 5, 10, 11, 12};
    uint8_t frame[64];
    for (size_t i = 0; i < sizeof(nested_fields); i++) {
        size_t length = build_manifest_with_nested(frame, sizeof(frame), nested_fields[i],
                                                   malformed_nested, sizeof(malformed_nested));
        assert_rejected_without_active_change(frame, length);
    }
}

static void test_nested_repeated_arrays_reject_overflow(void)
{
    uint8_t frame[512];
    uint8_t nested[256];

    frame_encoder_t channel;
    frame_encoder_init(&channel, nested, sizeof(nested), 0);
    for (size_t i = 0; i < MAX_TEMPLATE_IDS + 1; i++) {
        CHECK(frame_encode_varint(&channel, 3, i) == FRAME_OK);
    }
    size_t length = build_manifest_with_nested(frame, sizeof(frame), 4, nested + 1,
                                               frame_encoder_size(&channel) - 1);
    assert_rejected_without_active_change(frame, length);

    size_t nested_len = build_nested_repeated_empty(nested, sizeof(nested), 9,
                                                    MAX_EDGE_DEVICES_PER_CH + 1);
    length = build_manifest_with_nested(frame, sizeof(frame), 4, nested + 1, nested_len);
    assert_rejected_without_active_change(frame, length);

    uint8_t edge[128];
    size_t edge_len = build_nested_repeated_empty(edge, sizeof(edge), 3,
                                                  MAX_COMMANDS_PER_DEVICE + 1);
    frame_encoder_init(&channel, nested, sizeof(nested), 0);
    CHECK(frame_encode_bytes(&channel, 9, edge + 1, edge_len) == FRAME_OK);
    length = build_manifest_with_nested(frame, sizeof(frame), 4, nested + 1,
                                        frame_encoder_size(&channel) - 1);
    assert_rejected_without_active_change(frame, length);
}

static void test_oversized_runtime_fields_are_rejected(void)
{
    uint8_t frame[512];
    uint8_t nested[256];
    uint8_t oversized[129] = {0};
    frame_encoder_t sub;

    frame_encoder_init(&sub, nested, sizeof(nested), 0);
    CHECK(frame_encode_bytes(&sub, 2, oversized, 65) == FRAME_OK);
    size_t length = build_manifest_with_nested(frame, sizeof(frame), 3, nested + 1,
                                               frame_encoder_size(&sub) - 1);
    assert_rejected_without_active_change(frame, length);

    frame_encoder_init(&sub, nested, sizeof(nested), 0);
    CHECK(frame_encode_bytes(&sub, 7, oversized, sizeof(oversized)) == FRAME_OK);
    length = build_manifest_with_nested(frame, sizeof(frame), 4, nested + 1,
                                        frame_encoder_size(&sub) - 1);
    assert_rejected_without_active_change(frame, length);

    frame_encoder_init(&sub, nested, sizeof(nested), 0);
    CHECK(frame_encode_bytes(&sub, 3, oversized,
                              sizeof(((config_dma_channel_t *)0)->bind_to)) == FRAME_OK);
    length = build_manifest_with_nested(frame, sizeof(frame), 5, nested + 1,
                                        frame_encoder_size(&sub) - 1);
    assert_rejected_without_active_change(frame, length);
}

static void test_unknown_fields_are_rejected(void)
{
    uint8_t frame[128];
    uint8_t gpio[32];
    frame_encoder_t gpio_enc;
    frame_encoder_init(&gpio_enc, gpio, sizeof(gpio), 0);
    CHECK(frame_encode_varint(&gpio_enc, 1, 8) == FRAME_OK);
    CHECK(frame_encode_varint(&gpio_enc, 7, 123) == FRAME_OK);

    frame_encoder_t manifest;
    frame_encoder_init(&manifest, frame, sizeof(frame), MSG_CONFIG_MFST);
    CHECK(frame_encode_string(&manifest, 1, "unknown-ok") == FRAME_OK);
    CHECK(frame_encode_string(&manifest, 8, "sync-test") == FRAME_OK);
    CHECK(frame_encode_varint(&manifest, 15, 456) == FRAME_OK);
    CHECK(frame_encode_bytes(&manifest, 11, gpio + 1,
                              frame_encoder_size(&gpio_enc) - 1) == FRAME_OK);
    CHECK(!config_mgr_stage_manifest(frame, frame_encoder_size(&manifest)));
    CHECK(config_mgr_get_staged_manifest() == NULL);
}

int main(void)
{
    uint8_t frame[128];
    size_t length = build_manifest(frame, sizeof(frame));
    config_mgr_init();
    CHECK(config_mgr_apply_manifest(frame, length));
    const config_manifest_t *manifest = config_mgr_get_manifest();
    CHECK(manifest != NULL);
    CHECK(manifest->pwm_config_count == 1);
    CHECK(manifest->pwm_configs[0].channel == 5);
    CHECK(manifest->pwm_configs[0].pin == 6);
    CHECK(manifest->pwm_configs[0].frequency == 1234);
    CHECK(manifest->pwm_configs[0].duty == 4321);
    CHECK(manifest->pwm_configs[0].resolution == 14);
    CHECK(manifest->pwm_configs[0].auto_start);

    length = build_conflicting_manifest(frame, sizeof(frame));
    CHECK(!config_mgr_apply_manifest(frame, length));
    manifest = config_mgr_get_manifest();
    CHECK(strcmp(manifest->manifest_id, "pwm-layout") == 0);
    length = build_stopped_pwm_gpio_conflict(frame, sizeof(frame));
    CHECK(!config_mgr_apply_manifest(frame, length));
    CHECK(strcmp(config_mgr_get_manifest()->manifest_id, "pwm-layout") == 0);
    test_repeated_manifest_arrays_reject_overflow();
    test_malformed_decoder_termination_is_rejected();
    test_nested_repeated_arrays_reject_overflow();
    test_oversized_runtime_fields_are_rejected();
    test_unknown_fields_are_rejected();
    puts("config manifest PWM layout tests passed");
    return 0;
}
