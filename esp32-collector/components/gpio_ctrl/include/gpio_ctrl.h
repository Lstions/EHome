/**
 * @file gpio_ctrl.h
 * @brief GPIO peripheral control — direct pin control outside the channel system.
 *
 * GPIO and PWM are not "channels" — they are MCU peripheral controls.
 * This module provides direct GPIO set/read/config/deconfig/toggle.
 *
 * Concurrency:
 *   SET/READ/TOGGLE — no mutex (gpio_set_level/gpio_get_level are thread-safe)
 *   CONFIG/DECONFIG — mutex protected (gpio_config/gpio_reset_pin not thread-safe)
 *   gpio_ctrl_init() sets s_reconfiguring flag — SET/READ reject during reconfig
 */

#ifndef GPIO_CTRL_H
#define GPIO_CTRL_H

#include <stdint.h>
#include <stdbool.h>
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === Limits === */
#define GPIO_CTRL_MAX_PINS  30  /* ESP32-C6 has GPIO 0-30 */

/* === Direction encoding (matches protocol) === */
typedef enum {
    GPIO_DIR_INPUT         = 0,
    GPIO_DIR_OUTPUT        = 1,
    GPIO_DIR_INPUT_PULLUP  = 2,
    GPIO_DIR_INPUT_PULLDOWN = 3,
} gpio_dir_t;

/* === Action encoding (matches protocol) === */
typedef enum {
    GPIO_ACTION_SET_LOW   = 0,
    GPIO_ACTION_SET_HIGH  = 1,
    GPIO_ACTION_READ      = 2,
    GPIO_ACTION_CONFIG    = 3,
    GPIO_ACTION_DECONFIG  = 4,
    GPIO_ACTION_TOGGLE    = 5,
} gpio_action_t;

/* === Config entry (for batch init from ConfigManifest) === */
typedef struct {
    uint8_t pin;
    uint8_t direction;
    uint8_t initial_level;
} gpio_config_entry_t;

/* === API === */

/**
 * @brief Initialize GPIO controller with batch configs from ConfigManifest.
 *        Sets s_reconfiguring flag during init, clears afterwards.
 * @param configs  Array of GPIO config entries (may be NULL if count=0)
 * @param count    Number of entries
 * @return ESP_OK on success
 */
esp_err_t gpio_ctrl_init(const gpio_config_entry_t *configs, int count);
esp_err_t gpio_ctrl_preflight(const gpio_config_entry_t *configs, int count);
int gpio_ctrl_snapshot(gpio_config_entry_t *configs, int capacity);

/**
 * @brief Set GPIO pin output level. No mutex (thread-safe HW op).
 * @return ESP_OK, ESP_ERR_NOT_CONFIGURED, ESP_ERR_INVALID_ARG
 */
esp_err_t gpio_ctrl_set(int pin, int level);

/**
 * @brief Read GPIO pin level. No mutex (thread-safe HW op).
 * @return level (0/1), or -1 on error
 */
int gpio_ctrl_read(int pin);

/**
 * @brief Configure GPIO pin direction. Mutex protected.
 * @param pin           GPIO pin number
 * @param direction     gpio_dir_t value
 * @param initial_level For OUTPUT: initial level (0 or 1)
 * @return ESP_OK, ESP_ERR_INVALID_ARG, ESP_ERR_INVALID_STATE (pin conflict)
 */
esp_err_t gpio_ctrl_config(int pin, int direction, int initial_level);

/**
 * @brief Deconfigure GPIO pin (reset to default state). Mutex protected. Idempotent.
 * @return ESP_OK, ESP_ERR_INVALID_ARG
 */
esp_err_t gpio_ctrl_deconfig(int pin);

/**
 * @brief Toggle GPIO pin level. No mutex (atomic read+set).
 * @return ESP_OK, ESP_ERR_NOT_CONFIGURED, ESP_ERR_INVALID_ARG
 */
esp_err_t gpio_ctrl_toggle(int pin);

/**
 * @brief Check if a pin is currently configured by gpio_ctrl.
 */
bool gpio_ctrl_is_configured(int pin);

#ifdef __cplusplus
}
#endif

#endif /* GPIO_CTRL_H */
