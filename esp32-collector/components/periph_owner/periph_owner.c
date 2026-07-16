#include "periph_owner.h"

#include <limits.h>
#include <string.h>
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

#define CONFIG_BUS_UART 1
#define CONFIG_BUS_I2C  2
#define CONFIG_BUS_SPI  3

typedef struct {
    uint8_t owner;
    int8_t owner_id;
} pin_claim_t;

static pin_claim_t s_claims[PERIPH_OWNER_PIN_COUNT];
static bool s_bus_pins[PERIPH_OWNER_PIN_COUNT];
static SemaphoreHandle_t s_mutex;
static SemaphoreHandle_t s_transaction_mutex;

esp_err_t periph_owner_transaction_begin(void)
{
    if (!s_transaction_mutex) s_transaction_mutex = xSemaphoreCreateMutex();
    if (!s_transaction_mutex) return ESP_ERR_NO_MEM;
    return xSemaphoreTake(s_transaction_mutex, portMAX_DELAY) ? ESP_OK : ESP_FAIL;
}

void periph_owner_transaction_end(void)
{
    if (s_transaction_mutex) (void)xSemaphoreGive(s_transaction_mutex);
}

static bool channel_uses_pin(const config_channel_t *channel, int pin)
{
    if (!channel || !channel->enabled) return false;
    const uint8_t *cfg = channel->bus_config;
    size_t len = channel->bus_config_len;

    switch (channel->bus_type) {
    case CONFIG_BUS_UART:
    case CONFIG_BUS_I2C:
        return len >= 2 && (cfg[0] == pin || cfg[1] == pin);
    case CONFIG_BUS_SPI:
        if (len >= 6 && cfg[0] == pin) return true;
        return len >= 9 && (cfg[6] == pin || cfg[7] == pin || cfg[8] == pin);
    default:
        return false;
    }
}

bool periph_owner_manifest_bus_uses_pin(const config_manifest_t *manifest, int pin)
{
    if (!manifest) return false;
    for (int i = 0; i < manifest->channel_count; i++) {
        if (channel_uses_pin(&manifest->channels[i], pin)) return true;
    }
    return false;
}

static void ensure_mutex(void)
{
    if (!s_mutex) s_mutex = xSemaphoreCreateMutex();
}

static bool valid_claim_args(int pin, periph_pin_owner_t owner, int owner_id)
{
    return pin >= 0 && pin < PERIPH_OWNER_PIN_COUNT &&
           (owner == PERIPH_PIN_OWNER_GPIO || owner == PERIPH_PIN_OWNER_PWM) &&
           owner_id >= 0 && owner_id <= INT8_MAX;
}

esp_err_t periph_owner_claim(int pin, periph_pin_owner_t owner, int owner_id)
{
    if (!valid_claim_args(pin, owner, owner_id)) return ESP_ERR_INVALID_ARG;
    ensure_mutex();
    if (!s_mutex) return ESP_ERR_NO_MEM;

    xSemaphoreTake(s_mutex, portMAX_DELAY);
    pin_claim_t *claim = &s_claims[pin];
    esp_err_t result = ESP_OK;
    if (s_bus_pins[pin]) {
        result = ESP_ERR_PIN_CONFLICT;
    } else if (claim->owner != 0 &&
        (claim->owner != (uint8_t)owner || claim->owner_id != owner_id)) {
        result = ESP_ERR_PIN_CONFLICT;
    } else {
        claim->owner = (uint8_t)owner;
        claim->owner_id = (int8_t)owner_id;
    }
    xSemaphoreGive(s_mutex);
    return result;
}

void periph_owner_release(int pin, periph_pin_owner_t owner, int owner_id)
{
    if (!valid_claim_args(pin, owner, owner_id) || !s_mutex) return;
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    pin_claim_t *claim = &s_claims[pin];
    if (claim->owner == (uint8_t)owner && claim->owner_id == owner_id) {
        memset(claim, 0, sizeof(*claim));
    }
    xSemaphoreGive(s_mutex);
}

bool periph_owner_is_claimed_by(int pin, periph_pin_owner_t owner, int owner_id)
{
    if (!valid_claim_args(pin, owner, owner_id) || !s_mutex) return false;
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    bool claimed = s_claims[pin].owner == (uint8_t)owner &&
                   s_claims[pin].owner_id == owner_id;
    xSemaphoreGive(s_mutex);
    return claimed;
}

esp_err_t periph_owner_validate_manifest(const config_manifest_t *manifest)
{
    if (!manifest) return ESP_ERR_INVALID_ARG;

    for (int i = 0; i < manifest->gpio_config_count; i++) {
        int pin = manifest->gpio_configs[i].pin;
        if (periph_owner_manifest_bus_uses_pin(manifest, pin)) return ESP_ERR_PIN_CONFLICT;
        for (int j = i + 1; j < manifest->gpio_config_count; j++) {
            if (manifest->gpio_configs[j].pin == pin) return ESP_ERR_PIN_CONFLICT;
        }
        for (int j = 0; j < manifest->pwm_config_count; j++) {
            if (manifest->pwm_configs[j].pin == pin) {
                return ESP_ERR_PIN_CONFLICT;
            }
        }
    }

    for (int i = 0; i < manifest->pwm_config_count; i++) {
        int pin = manifest->pwm_configs[i].pin;
        if (periph_owner_manifest_bus_uses_pin(manifest, pin)) return ESP_ERR_PIN_CONFLICT;
        for (int j = i + 1; j < manifest->pwm_config_count; j++) {
            if (manifest->pwm_configs[j].pin == pin) {
                return ESP_ERR_PIN_CONFLICT;
            }
        }
    }
    return ESP_OK;
}

esp_err_t periph_owner_set_bus_manifest(const config_manifest_t *manifest)
{
    if (!manifest) return ESP_ERR_INVALID_ARG;
    bool next[PERIPH_OWNER_PIN_COUNT] = {0};
    for (int pin = 0; pin < PERIPH_OWNER_PIN_COUNT; pin++) {
        next[pin] = periph_owner_manifest_bus_uses_pin(manifest, pin);
    }
    ensure_mutex();
    if (!s_mutex) return ESP_ERR_NO_MEM;
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    for (int pin = 0; pin < PERIPH_OWNER_PIN_COUNT; pin++) {
        if (next[pin] && s_claims[pin].owner != 0) {
            xSemaphoreGive(s_mutex);
            return ESP_ERR_PIN_CONFLICT;
        }
    }
    memcpy(s_bus_pins, next, sizeof(s_bus_pins));
    xSemaphoreGive(s_mutex);
    return ESP_OK;
}

esp_err_t periph_owner_snapshot(periph_owner_snapshot_t *snapshot)
{
    if (!snapshot) return ESP_ERR_INVALID_ARG;
    ensure_mutex();
    if (!s_mutex) return ESP_ERR_NO_MEM;
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    for (int pin = 0; pin < PERIPH_OWNER_PIN_COUNT; pin++) {
        snapshot->owner[pin] = s_claims[pin].owner;
        snapshot->owner_id[pin] = s_claims[pin].owner_id;
        snapshot->bus_pins[pin] = s_bus_pins[pin];
    }
    xSemaphoreGive(s_mutex);
    return ESP_OK;
}

esp_err_t periph_owner_restore(const periph_owner_snapshot_t *snapshot)
{
    if (!snapshot) return ESP_ERR_INVALID_ARG;
    ensure_mutex();
    if (!s_mutex) return ESP_ERR_NO_MEM;
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    for (int pin = 0; pin < PERIPH_OWNER_PIN_COUNT; pin++) {
        s_claims[pin].owner = snapshot->owner[pin];
        s_claims[pin].owner_id = snapshot->owner_id[pin];
        s_bus_pins[pin] = snapshot->bus_pins[pin];
    }
    xSemaphoreGive(s_mutex);
    return ESP_OK;
}

esp_err_t periph_owner_replace_manifest(const config_manifest_t *manifest)
{
    if (!manifest) return ESP_ERR_INVALID_ARG;
    esp_err_t validation = periph_owner_validate_manifest(manifest);
    if (validation != ESP_OK) return validation;

    pin_claim_t next_claims[PERIPH_OWNER_PIN_COUNT] = {0};
    bool next_bus[PERIPH_OWNER_PIN_COUNT] = {0};
    for (int pin = 0; pin < PERIPH_OWNER_PIN_COUNT; pin++) {
        next_bus[pin] = periph_owner_manifest_bus_uses_pin(manifest, pin);
    }
    for (int i = 0; i < manifest->gpio_config_count; i++) {
        int pin = manifest->gpio_configs[i].pin;
        if (!valid_claim_args(pin, PERIPH_PIN_OWNER_GPIO, pin)) return ESP_ERR_INVALID_ARG;
        next_claims[pin] = (pin_claim_t){PERIPH_PIN_OWNER_GPIO, (int8_t)pin};
    }
    for (int i = 0; i < manifest->pwm_config_count; i++) {
        int pin = manifest->pwm_configs[i].pin;
        int channel = manifest->pwm_configs[i].channel;
        if (!valid_claim_args(pin, PERIPH_PIN_OWNER_PWM, channel)) return ESP_ERR_INVALID_ARG;
        next_claims[pin] = (pin_claim_t){PERIPH_PIN_OWNER_PWM, (int8_t)channel};
    }

    ensure_mutex();
    if (!s_mutex) return ESP_ERR_NO_MEM;
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    memcpy(s_claims, next_claims, sizeof(s_claims));
    memcpy(s_bus_pins, next_bus, sizeof(s_bus_pins));
    xSemaphoreGive(s_mutex);
    return ESP_OK;
}