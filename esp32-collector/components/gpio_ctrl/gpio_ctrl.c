/**
 * @file gpio_ctrl.c
 * @brief GPIO peripheral control implementation
 *
 * Direct GPIO control outside the bus/channel system.
 * Concurrency: SET/READ/TOGGLE lockless, CONFIG/DECONFIG mutex-protected.
 */

#include "gpio_ctrl.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "driver/gpio.h"
#include "hw_tables.h"
#include "periph_owner.h"
#include <string.h>

#define TAG "GPIO_CTRL"


/* === Pin state tracking === */
typedef struct {
    bool     configured;
    uint8_t  direction;
    uint8_t  initial_level;
} gpio_pin_state_t;

static gpio_pin_state_t s_pin_state[GPIO_CTRL_MAX_PINS];
static uint8_t s_output_level[GPIO_CTRL_MAX_PINS];
static SemaphoreHandle_t s_config_mutex = NULL;
static volatile bool s_reconfiguring = false;

/* === Internal helpers === */

static bool is_valid_pin(int pin)
{
    for (int i = 0; i < HW_GPIO_COUNT; i++) {
        if (hw_gpios[i].pin == pin) return true;
    }
    return false;
}

/**
 * @brief Check if pin is used by UART/I2C/SPI bus channels.
 * This is a conservative check — only checks reserved hardware pins,
 * not dynamically assigned bus pins (those are checked by bus_manager).
 */
static esp_err_t apply_config(int pin, int direction, int initial_level)
{
    esp_err_t claim_err = periph_owner_claim(pin, PERIPH_PIN_OWNER_GPIO, pin);
    if (claim_err != ESP_OK) return claim_err;
    gpio_config_t io_conf = {0};

    switch (direction) {
    case GPIO_DIR_INPUT:
        io_conf.mode = GPIO_MODE_INPUT;
        io_conf.pull_up_en = GPIO_PULLUP_DISABLE;
        io_conf.pull_down_en = GPIO_PULLDOWN_DISABLE;
        break;
    case GPIO_DIR_OUTPUT:
        io_conf.mode = GPIO_MODE_OUTPUT;
        io_conf.pull_up_en = GPIO_PULLUP_DISABLE;
        io_conf.pull_down_en = GPIO_PULLDOWN_DISABLE;
        break;
    case GPIO_DIR_INPUT_PULLUP:
        io_conf.mode = GPIO_MODE_INPUT;
        io_conf.pull_up_en = GPIO_PULLUP_ENABLE;
        io_conf.pull_down_en = GPIO_PULLDOWN_DISABLE;
        break;
    case GPIO_DIR_INPUT_PULLDOWN:
        io_conf.mode = GPIO_MODE_INPUT;
        io_conf.pull_up_en = GPIO_PULLUP_DISABLE;
        io_conf.pull_down_en = GPIO_PULLDOWN_ENABLE;
        break;
    default:
        periph_owner_release(pin, PERIPH_PIN_OWNER_GPIO, pin);
        return ESP_ERR_INVALID_ARG;
    }

    io_conf.pin_bit_mask = (1ULL << pin);
    io_conf.intr_type = GPIO_INTR_DISABLE;

    esp_err_t ret = gpio_config(&io_conf);
    if (ret != ESP_OK) {
        periph_owner_release(pin, PERIPH_PIN_OWNER_GPIO, pin);
        ESP_LOGE(TAG, "gpio_config(pin=%d) failed: %s", pin, esp_err_to_name(ret));
        return ret;
    }

    /* Set initial level for output */
    if (direction == GPIO_DIR_OUTPUT) {
        ret = gpio_set_level(pin, initial_level ? 1 : 0);
        if (ret != ESP_OK) {
			esp_err_t reset_err = gpio_reset_pin(pin);
			if (reset_err != ESP_OK) {
				ESP_LOGE(TAG, "gpio_reset_pin(pin=%d) cleanup failed: %s", pin,
				         esp_err_to_name(reset_err));
				/* Hardware may still be live: retain ownership fail-closed. */
				return reset_err;
			}
			periph_owner_release(pin, PERIPH_PIN_OWNER_GPIO, pin);
            ESP_LOGE(TAG, "gpio_set_level(pin=%d, init) failed: %s", pin, esp_err_to_name(ret));
			return ret;
        }
        s_output_level[pin] = initial_level ? 1 : 0;
    }

    s_pin_state[pin].configured = true;
    s_pin_state[pin].direction = (uint8_t)direction;
    s_pin_state[pin].initial_level = initial_level ? 1 : 0;

    return ESP_OK;
}

/* === Public API === */

esp_err_t gpio_ctrl_preflight(const gpio_config_entry_t *configs, int count)
{
    if (count < 0 || count > GPIO_CTRL_MAX_PINS || (count > 0 && !configs)) {
        return ESP_ERR_INVALID_ARG;
    }
    for (int i = 0; i < count; i++) {
        if (!is_valid_pin(configs[i].pin) ||
            configs[i].direction > GPIO_DIR_INPUT_PULLDOWN ||
            configs[i].initial_level > 1) return ESP_ERR_INVALID_ARG;
        for (int j = i + 1; j < count; j++) {
            if (configs[i].pin == configs[j].pin) return ESP_ERR_PIN_CONFLICT;
        }
    }
    return ESP_OK;
}

int gpio_ctrl_snapshot(gpio_config_entry_t *configs, int capacity)
{
    if (!configs || capacity < 0) return ESP_ERR_INVALID_ARG;
    int count = 0;
    for (int pin = 0; pin < GPIO_CTRL_MAX_PINS && count < capacity; pin++) {
        if (!s_pin_state[pin].configured) continue;
        configs[count++] = (gpio_config_entry_t){
            .pin = pin, .direction = s_pin_state[pin].direction,
            .initial_level = s_pin_state[pin].initial_level,
        };
    }
    return count;
}

esp_err_t gpio_ctrl_init(const gpio_config_entry_t *configs, int count)
{
    esp_err_t preflight = gpio_ctrl_preflight(configs, count);
    if (preflight != ESP_OK) return preflight;
    if (s_config_mutex == NULL) {
        s_config_mutex = xSemaphoreCreateMutex();
        if (s_config_mutex == NULL) {
            ESP_LOGE(TAG, "Failed to create config mutex");
            return ESP_ERR_NO_MEM;
        }
    }

    /* Mark reconfiguring — SET/READ will reject during this window */
    s_reconfiguring = true;

    /* Reset all existing configs */
    for (int i = 0; i < GPIO_CTRL_MAX_PINS; i++) {
        if (s_pin_state[i].configured) {
			esp_err_t reset_err = gpio_reset_pin(i);
			if (reset_err != ESP_OK) {
				ESP_LOGE(TAG, "reset pin %d failed: %s", i, esp_err_to_name(reset_err));
				s_reconfiguring = false;
				return reset_err;
			}
            periph_owner_release(i, PERIPH_PIN_OWNER_GPIO, i);
            s_pin_state[i].configured = false;
            s_pin_state[i].direction = 0;
        }
    }

    /* Apply new configs */
    esp_err_t result = ESP_OK;
    for (int i = 0; i < count && configs != NULL; i++) {
        if (!is_valid_pin(configs[i].pin)) {
            ESP_LOGW(TAG, "init: skipping invalid pin %d", configs[i].pin);
            continue;
        }
        esp_err_t ret = apply_config(configs[i].pin, configs[i].direction,
                                      configs[i].initial_level);
        if (ret != ESP_OK) {
            ESP_LOGE(TAG, "init: failed to config pin %d: %s", configs[i].pin,
                     esp_err_to_name(ret));
            result = ret;  /* Continue with other pins, report last error */
        }
    }

    s_reconfiguring = false;
    ESP_LOGI(TAG, "init: %d configs applied (count=%d)", count, count);
    return result;
}

esp_err_t gpio_ctrl_set(int pin, int level)
{
    if (s_reconfiguring) return ESP_ERR_INVALID_STATE;
    if (!is_valid_pin(pin)) return ESP_ERR_INVALID_ARG;
    if (!s_pin_state[pin].configured) return ESP_ERR_INVALID_STATE;
    if (s_pin_state[pin].direction != GPIO_DIR_OUTPUT) return ESP_ERR_INVALID_STATE;

    esp_err_t ret = gpio_set_level(pin, level ? 1 : 0);
    if (ret == ESP_OK) s_output_level[pin] = level ? 1 : 0;
    if (ret != ESP_OK) {
        ESP_LOGE(TAG, "set: pin=%d failed: %s", pin, esp_err_to_name(ret));
    }
    return ret;
}

int gpio_ctrl_read(int pin)
{
    if (s_reconfiguring) return -1;
    if (!is_valid_pin(pin)) return -1;
    if (!s_pin_state[pin].configured) return -1;

    if (s_pin_state[pin].direction == GPIO_DIR_OUTPUT) return s_output_level[pin];
    return gpio_get_level(pin);
}

esp_err_t gpio_ctrl_config(int pin, int direction, int initial_level)
{
    if (s_config_mutex == NULL) {
        s_config_mutex = xSemaphoreCreateMutex();
        if (s_config_mutex == NULL) return ESP_ERR_NO_MEM;
    }

    if (!is_valid_pin(pin)) return ESP_ERR_INVALID_ARG;
    if (direction < GPIO_DIR_INPUT || direction > GPIO_DIR_INPUT_PULLDOWN) {
        return ESP_ERR_INVALID_ARG;
    }
    xSemaphoreTake(s_config_mutex, portMAX_DELAY);

    gpio_pin_state_t previous = s_pin_state[pin];
    esp_err_t ret = apply_config(pin, direction, initial_level);
    if (ret != ESP_OK && previous.configured) {
		esp_err_t restore_err = apply_config(pin, previous.direction, previous.initial_level);
		if (restore_err != ESP_OK) {
			ESP_LOGE(TAG, "config rollback failed for pin %d: %s", pin,
			         esp_err_to_name(restore_err));
			ret = restore_err;
		}
    }

    xSemaphoreGive(s_config_mutex);

    if (ret == ESP_OK) {
        ESP_LOGI(TAG, "config: pin=%d dir=%d init=%d", pin, direction, initial_level);
    }
    return ret;
}

esp_err_t gpio_ctrl_deconfig(int pin)
{
    if (s_config_mutex == NULL) {
        s_config_mutex = xSemaphoreCreateMutex();
        if (s_config_mutex == NULL) return ESP_ERR_NO_MEM;
    }

    if (!is_valid_pin(pin)) return ESP_ERR_INVALID_ARG;

    xSemaphoreTake(s_config_mutex, portMAX_DELAY);

    /* Idempotent: if not configured, just return OK */
    if (s_pin_state[pin].configured) {
		esp_err_t reset_err = gpio_reset_pin(pin);
		if (reset_err != ESP_OK) {
			xSemaphoreGive(s_config_mutex);
			return reset_err;
		}
        periph_owner_release(pin, PERIPH_PIN_OWNER_GPIO, pin);
        s_pin_state[pin].configured = false;
        s_pin_state[pin].direction = 0;
        ESP_LOGI(TAG, "deconfig: pin=%d", pin);
    }

    xSemaphoreGive(s_config_mutex);
    return ESP_OK;
}

esp_err_t gpio_ctrl_toggle(int pin)
{
    if (s_reconfiguring) return ESP_ERR_INVALID_STATE;
    if (!is_valid_pin(pin)) return ESP_ERR_INVALID_ARG;
    if (!s_pin_state[pin].configured) return ESP_ERR_INVALID_STATE;
    if (s_pin_state[pin].direction != GPIO_DIR_OUTPUT) return ESP_ERR_INVALID_STATE;

    /* Atomic toggle: read then set inverse */
    int level = s_output_level[pin];
    esp_err_t ret = gpio_set_level(pin, level ? 0 : 1);
    if (ret == ESP_OK) s_output_level[pin] = level ? 0 : 1;
    if (ret != ESP_OK) {
        ESP_LOGE(TAG, "toggle: pin=%d failed: %s", pin, esp_err_to_name(ret));
    }
    return ret;
}

bool gpio_ctrl_is_configured(int pin)
{
    if (!is_valid_pin(pin)) return false;
    return s_pin_state[pin].configured;
}
