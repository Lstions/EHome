#include <stdarg.h>
#include <stdbool.h>
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

#include "gpio_ctrl.h"
#include "pwm_ctrl.h"
#include "driver/gpio.h"
#include "driver/ledc.h"
#include "freertos/semphr.h"
#include "config_mgr.h"
#include "periph_owner.h"

const config_manifest_t *config_mgr_get_manifest(void) { return NULL; }
void esp_restart(void) { abort(); }

static ledc_channel_config_t s_last_channel_config;
static uint32_t s_duty[SOC_LEDC_CHANNEL_NUM];
static unsigned s_channel_config_calls;
static unsigned s_stop_calls;
static esp_err_t s_next_stop_result = ESP_OK;
static esp_err_t s_next_timer_reset_result = ESP_OK;
static int s_gpio_config_calls;
static esp_err_t s_next_gpio_config_result = ESP_OK;
static esp_err_t s_next_gpio_set_result = ESP_OK;
static esp_err_t s_next_gpio_reset_result = ESP_OK;
static unsigned s_gpio_reset_calls;

void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level; (void)tag; (void)format;
}

const char *esp_err_to_name(esp_err_t err)
{
    (void)err;
    return "host";
}

SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (SemaphoreHandle_t)1; }
int xSemaphoreTake(SemaphoreHandle_t semaphore, uint32_t ticks)
{
    (void)semaphore; (void)ticks; return 1;
}
int xSemaphoreGive(SemaphoreHandle_t semaphore) { (void)semaphore; return 1; }

esp_err_t ledc_timer_config(const ledc_timer_config_t *config) { (void)config; return ESP_OK; }
esp_err_t ledc_channel_config(const ledc_channel_config_t *config)
{
    s_last_channel_config = *config;
    s_duty[config->channel] = config->duty;
    s_channel_config_calls++;
    return ESP_OK;
}
esp_err_t ledc_stop(ledc_mode_t mode, ledc_channel_t channel, uint32_t idle)
{
    (void)mode; (void)channel; (void)idle; s_stop_calls++;
    esp_err_t result = s_next_stop_result;
    s_next_stop_result = ESP_OK;
    return result;
}
esp_err_t ledc_set_duty(ledc_mode_t mode, ledc_channel_t channel, uint32_t duty)
{ (void)mode; s_duty[channel] = duty; return ESP_OK; }
esp_err_t ledc_update_duty(ledc_mode_t mode, ledc_channel_t channel)
{ (void)mode; (void)channel; return ESP_OK; }
uint32_t ledc_get_duty(ledc_mode_t mode, ledc_channel_t channel)
{ (void)mode; return s_duty[channel]; }
esp_err_t ledc_timer_rst(ledc_mode_t mode, ledc_timer_t timer)
{
    (void)mode; (void)timer;
    esp_err_t result = s_next_timer_reset_result;
    s_next_timer_reset_result = ESP_OK;
    return result;
}
esp_err_t ledc_timer_pause(ledc_mode_t mode, ledc_timer_t timer)
{ (void)mode; (void)timer; return ESP_OK; }

esp_err_t gpio_config(const gpio_config_t *config)
{
    (void)config;
    s_gpio_config_calls++;
    esp_err_t result = s_next_gpio_config_result;
    s_next_gpio_config_result = ESP_OK;
    return result;
}
esp_err_t gpio_set_level(int pin, int level)
{
    (void)pin; (void)level;
    esp_err_t result = s_next_gpio_set_result;
    s_next_gpio_set_result = ESP_OK;
    return result;
}
int gpio_get_level(int pin) { (void)pin; return 0; }
esp_err_t gpio_reset_pin(int pin)
{
    (void)pin; s_gpio_reset_calls++;
    esp_err_t result = s_next_gpio_reset_result;
    s_next_gpio_reset_result = ESP_OK;
    return result;
}

static void test_pwm_uses_reported_channel_and_routed_pin(void)
{
    memset(&s_last_channel_config, 0, sizeof(s_last_channel_config));
    s_channel_config_calls = 0;

    CHECK(pwm_ctrl_start(5, 6, 1000, 5000, 14) == ESP_OK);
    CHECK(s_channel_config_calls == 1);
    CHECK(s_last_channel_config.channel == 5);
    CHECK(s_last_channel_config.gpio_num == 6);
    CHECK(pwm_ctrl_is_running(5));
    CHECK(pwm_ctrl_set_duty(5, 2500) == ESP_OK);
    uint16_t duty = 0;
    CHECK(pwm_ctrl_get_duty(5, &duty) == ESP_OK);
    CHECK(duty == 2500);
    CHECK(pwm_ctrl_stop(5) == ESP_OK);
    CHECK(pwm_ctrl_deconfig(5) == ESP_OK);
}

static void test_pwm_rejects_unreported_channel(void)
{
    s_channel_config_calls = 0;
    CHECK(pwm_ctrl_start(6, 6, 1000, 5000, 14) == ESP_ERR_INVALID_ARG);
    CHECK(s_channel_config_calls == 0);
}

static void test_pwm_rejects_pin_not_in_reported_gpio_table(void)
{
    s_channel_config_calls = 0;
    CHECK(pwm_ctrl_start(0, 9, 1000, 5000, 14) == ESP_ERR_INVALID_ARG);
    CHECK(s_channel_config_calls == 0);
}

static void test_gpio_rejects_pin_not_in_reported_gpio_table(void)
{
    s_gpio_config_calls = 0;
    CHECK(gpio_ctrl_config(9, GPIO_DIR_OUTPUT, 0) == ESP_ERR_INVALID_ARG);
    CHECK(s_gpio_config_calls == 0);
}

static void test_manifest_batch_keeps_channel_separate_from_pin(void)
{
    const pwm_config_entry_t config = {
        .channel = 4,
        .pin = 6,
        .frequency = 2000,
        .duty = 4000,
        .resolution = 14,
        .auto_start = true,
    };
    s_channel_config_calls = 0;
    CHECK(pwm_ctrl_init(&config, 1) == ESP_OK);
    CHECK(s_channel_config_calls == 1);
    CHECK(s_last_channel_config.channel == 4);
    CHECK(s_last_channel_config.gpio_num == 6);
    CHECK(pwm_ctrl_stop(4) == ESP_OK);
    CHECK(pwm_ctrl_deconfig(4) == ESP_OK);
}

static void test_failed_pwm_replacement_preserves_running_output(void)
{
    CHECK(pwm_ctrl_start(1, 7, 1000, 2000, 14) == ESP_OK);
    CHECK(pwm_ctrl_start(0, 6, 1000, 3000, 14) == ESP_OK);
    s_stop_calls = 0;

    CHECK(pwm_ctrl_start(0, 7, 1000, 3000, 14) == ESP_ERR_PIN_CONFLICT);
    CHECK(s_stop_calls == 0);
    CHECK(pwm_ctrl_is_running(0));
    uint16_t duty = 0;
    CHECK(pwm_ctrl_get_duty(0, &duty) == ESP_OK);
    CHECK(duty >= 2999 && duty <= 3000);
    CHECK(pwm_ctrl_stop(0) == ESP_OK);
    CHECK(pwm_ctrl_stop(1) == ESP_OK);
    CHECK(pwm_ctrl_deconfig(0) == ESP_OK);
    CHECK(pwm_ctrl_deconfig(1) == ESP_OK);
}

static void test_gpio_and_pwm_routes_are_mutually_exclusive(void)
{
    CHECK(gpio_ctrl_config(6, GPIO_DIR_OUTPUT, 0) == ESP_OK);
    CHECK(pwm_ctrl_start(0, 6, 1000, 3000, 14) == ESP_ERR_PIN_CONFLICT);
    CHECK(gpio_ctrl_deconfig(6) == ESP_OK);

    CHECK(pwm_ctrl_start(0, 6, 1000, 3000, 14) == ESP_OK);
    CHECK(gpio_ctrl_config(6, GPIO_DIR_OUTPUT, 0) == ESP_ERR_PIN_CONFLICT);
    CHECK(pwm_ctrl_stop(0) == ESP_OK);
    CHECK(pwm_ctrl_deconfig(0) == ESP_OK);
}

static void test_pwm_read_rejects_non_running_channel(void)
{
    uint16_t duty = 1234;
    CHECK(pwm_ctrl_get_duty(0, &duty) == ESP_ERR_INVALID_STATE);
    CHECK(duty == 1234);
}

static void test_pwm_stop_propagates_teardown_failures(void)
{
    CHECK(pwm_ctrl_start(0, 6, 1000, 3000, 14) == ESP_OK);
    s_next_stop_result = ESP_FAIL;
    CHECK(pwm_ctrl_stop(0) == ESP_FAIL);
    CHECK(pwm_ctrl_is_running(0));
    s_next_timer_reset_result = ESP_FAIL;
    CHECK(pwm_ctrl_stop(0) == ESP_FAIL);
    CHECK(pwm_ctrl_is_running(0));
    CHECK(pwm_ctrl_stop(0) == ESP_OK);
    CHECK(pwm_ctrl_deconfig(0) == ESP_OK);
}

static void test_failed_gpio_replacement_preserves_old_configuration(void)
{
    CHECK(gpio_ctrl_config(6, GPIO_DIR_OUTPUT, 1) == ESP_OK);
    CHECK(gpio_ctrl_is_configured(6));
    s_next_gpio_config_result = ESP_FAIL;
    CHECK(gpio_ctrl_config(6, GPIO_DIR_INPUT, 0) == ESP_FAIL);
    CHECK(gpio_ctrl_is_configured(6));
    CHECK(gpio_ctrl_set(6, 0) == ESP_OK);
    CHECK(gpio_ctrl_deconfig(6) == ESP_OK);
}

static void test_failed_initial_level_cleans_up_partial_gpio_configuration(void)
{
    s_gpio_reset_calls = 0;
    s_next_gpio_set_result = ESP_FAIL;
    CHECK(gpio_ctrl_config(6, GPIO_DIR_OUTPUT, 1) == ESP_FAIL);
    CHECK(s_gpio_reset_calls == 1);
    CHECK(!gpio_ctrl_is_configured(6));
    CHECK(!periph_owner_is_claimed_by(6, PERIPH_PIN_OWNER_GPIO, 6));
}

static void test_gpio_init_reports_reset_failure(void)
{
    const gpio_config_entry_t old_cfg = {.pin = 6, .direction = GPIO_DIR_OUTPUT};
    CHECK(gpio_ctrl_init(&old_cfg, 1) == ESP_OK);
    s_next_gpio_reset_result = ESP_FAIL;
    CHECK(gpio_ctrl_init(NULL, 0) == ESP_FAIL);
    CHECK(gpio_ctrl_is_configured(6));
    CHECK(gpio_ctrl_init(NULL, 0) == ESP_OK);
}

static void test_bus_manifest_reconciliation_rejects_live_peripheral_claim(void)
{
    config_manifest_t bus_manifest = {0};
    bus_manifest.channel_count = 1;
    bus_manifest.channels[0].enabled = true;
    bus_manifest.channels[0].bus_type = 2;
    bus_manifest.channels[0].bus_config_len = 2;
    bus_manifest.channels[0].bus_config[0] = 6;
    bus_manifest.channels[0].bus_config[1] = 7;

    CHECK(gpio_ctrl_config(6, GPIO_DIR_OUTPUT, 0) == ESP_OK);
    CHECK(periph_owner_set_bus_manifest(&bus_manifest) == ESP_ERR_PIN_CONFLICT);
    CHECK(gpio_ctrl_is_configured(6));
    CHECK(gpio_ctrl_deconfig(6) == ESP_OK);
    CHECK(periph_owner_set_bus_manifest(&bus_manifest) == ESP_OK);
    CHECK(gpio_ctrl_config(6, GPIO_DIR_OUTPUT, 0) == ESP_ERR_PIN_CONFLICT);
    config_manifest_t empty = {0};
    CHECK(periph_owner_set_bus_manifest(&empty) == ESP_OK);
}

static void test_stopped_pwm_route_is_reserved_by_manifest(void)
{
    config_manifest_t manifest = {0};
    manifest.pwm_config_count = 1;
    manifest.pwm_configs[0] = (config_pwm_t){
        .channel = 0, .pin = 6, .frequency = 1000,
        .resolution = 14, .auto_start = false,
    };

    CHECK(periph_owner_replace_manifest(&manifest) == ESP_OK);
    CHECK(periph_owner_is_claimed_by(6, PERIPH_PIN_OWNER_PWM, 0));
    CHECK(gpio_ctrl_config(6, GPIO_DIR_OUTPUT, 0) == ESP_ERR_PIN_CONFLICT);

    config_manifest_t empty = {0};
    CHECK(periph_owner_replace_manifest(&empty) == ESP_OK);
}

static void test_runtime_stop_keeps_configured_pwm_route_reserved(void)
{
    const pwm_config_entry_t config = {
        .channel = 0, .pin = 6, .frequency = 1000, .duty = 3000,
        .resolution = 14, .auto_start = true,
    };

    CHECK(pwm_ctrl_init(&config, 1) == ESP_OK);
    CHECK(pwm_ctrl_stop(0) == ESP_OK);
    CHECK(!pwm_ctrl_is_running(0));
    CHECK(periph_owner_is_claimed_by(6, PERIPH_PIN_OWNER_PWM, 0));
    CHECK(gpio_ctrl_config(6, GPIO_DIR_OUTPUT, 0) == ESP_ERR_PIN_CONFLICT);
    CHECK(pwm_ctrl_start(0, 6, 1000, 3000, 14) == ESP_OK);
    CHECK(pwm_ctrl_stop(0) == ESP_OK);
    CHECK(pwm_ctrl_deconfig(0) == ESP_OK);
    CHECK(gpio_ctrl_config(6, GPIO_DIR_OUTPUT, 0) == ESP_OK);
    CHECK(gpio_ctrl_deconfig(6) == ESP_OK);
}

static void test_owner_snapshot_restore_replaces_transient_claims_atomically(void)
{
    config_manifest_t old = {0};
    old.gpio_config_count = 1;
    old.gpio_configs[0].pin = 6;
    CHECK(periph_owner_replace_manifest(&old) == ESP_OK);

    periph_owner_snapshot_t snapshot;
    CHECK(periph_owner_snapshot(&snapshot) == ESP_OK);

    config_manifest_t candidate = {0};
    candidate.pwm_config_count = 1;
    candidate.pwm_configs[0] = (config_pwm_t){
        .channel = 1, .pin = 7, .frequency = 1000,
        .resolution = 14, .auto_start = false,
    };
    CHECK(periph_owner_replace_manifest(&candidate) == ESP_OK);
    CHECK(periph_owner_restore(&snapshot) == ESP_OK);
    CHECK(periph_owner_is_claimed_by(6, PERIPH_PIN_OWNER_GPIO, 6));
    CHECK(!periph_owner_is_claimed_by(7, PERIPH_PIN_OWNER_PWM, 1));

    config_manifest_t empty = {0};
    CHECK(periph_owner_replace_manifest(&empty) == ESP_OK);
}

int main(void)
{
    test_pwm_uses_reported_channel_and_routed_pin();
    test_pwm_rejects_unreported_channel();
    test_pwm_rejects_pin_not_in_reported_gpio_table();
    test_gpio_rejects_pin_not_in_reported_gpio_table();
    test_manifest_batch_keeps_channel_separate_from_pin();
    test_failed_pwm_replacement_preserves_running_output();
    test_gpio_and_pwm_routes_are_mutually_exclusive();
    test_pwm_read_rejects_non_running_channel();
    test_pwm_stop_propagates_teardown_failures();
    test_failed_gpio_replacement_preserves_old_configuration();
    test_failed_initial_level_cleans_up_partial_gpio_configuration();
    test_gpio_init_reports_reset_failure();
    test_bus_manifest_reconciliation_rejects_live_peripheral_claim();
    test_stopped_pwm_route_is_reserved_by_manifest();
    test_runtime_stop_keeps_configured_pwm_route_reserved();
    test_owner_snapshot_restore_replaces_transient_claims_atomically();
    puts("peripheral identity tests passed");
    return 0;
}
