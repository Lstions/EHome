/**
 * @file pwm_ctrl.h
 * @brief PWM peripheral control via LEDC — direct control outside the channel system.
 *
 * ESP32-C6 LEDC: 6 channels, 4 timers (SOC_LEDC_TIMER_NUM=4).
 * Same-frequency channels share a timer via reference counting.
 * Resource exhaustion returns ESP_ERR_NOT_FOUND.
 *
 * Duty precision: default 14-bit (16384 levels > 10000 duty levels).
 * Mapping: ledc_duty = (duty * 2^resolution) / 10000
 *
 * Frequency-resolution constraint: freq * 2^resolution <= APB_CLK / 2 (40 MHz)
 */

#ifndef PWM_CTRL_H
#define PWM_CTRL_H

#include <stdint.h>
#include <stdbool.h>
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === Limits === */
#define PWM_DUTY_MIN       0
#define PWM_DUTY_MAX       10000
#define PWM_RES_DEFAULT    14
#define PWM_RES_MIN        4
#define PWM_RES_MAX        20
#define PWM_CTRL_MAX_PINS  30

/* === Action encoding (matches protocol) === */
typedef enum {
    PWM_ACTION_SET_DUTY       = 0,
    PWM_ACTION_SET_FREQ       = 1,
    PWM_ACTION_START          = 2,
    PWM_ACTION_STOP           = 3,
    PWM_ACTION_READ           = 4,
    PWM_ACTION_SET_RESOLUTION = 5,
} pwm_action_t;

/* === Config entry (for batch init from ConfigManifest) === */
typedef struct {
    uint8_t  pin;
    uint32_t frequency;
    uint16_t duty;
    uint8_t  resolution;
    bool     auto_start;
} pwm_config_entry_t;

/* === API === */

/**
 * @brief Initialize PWM controller with batch configs from ConfigManifest.
 *        Applies all configs, auto-starting those with auto_start=true.
 * @param configs  Array of PWM config entries (may be NULL if count=0)
 * @param count    Number of entries
 * @return ESP_OK on success
 */
esp_err_t pwm_ctrl_init(const pwm_config_entry_t *configs, int count);

/**
 * @brief Start PWM output on a pin.
 * @param pin          GPIO pin number
 * @param freq         Frequency in Hz
 * @param duty         Duty cycle (0-10000 = 0.00%-100.00%)
 * @param resolution   Resolution bits (4-20, default 14)
 * @return ESP_OK, ESP_ERR_NOT_FOUND (resources exhausted), ESP_ERR_INVALID_ARG
 */
esp_err_t pwm_ctrl_start(int pin, uint32_t freq, uint16_t duty, uint8_t resolution);

/**
 * @brief Stop PWM output on a pin. Decrements timer refcount, deinit timer at 0.
 * @return ESP_OK, ESP_ERR_INVALID_ARG, ESP_ERR_INVALID_STATE
 */
esp_err_t pwm_ctrl_stop(int pin);

/**
 * @brief Set duty cycle for a running PWM channel.
 * @param duty  Duty (0-10000)
 * @return ESP_OK, ESP_ERR_INVALID_ARG, ESP_ERR_INVALID_STATE
 */
esp_err_t pwm_ctrl_set_duty(int pin, uint16_t duty);

/**
 * @brief Set frequency (and optionally resolution) for a running PWM channel.
 * @param freq        Frequency in Hz
 * @param resolution  Resolution bits (0 = keep current)
 * @return ESP_OK, ESP_ERR_INVALID_ARG, ESP_ERR_INVALID_STATE
 */
esp_err_t pwm_ctrl_set_freq(int pin, uint32_t freq, uint8_t resolution);

/**
 * @brief Set resolution for a running PWM channel.
 * @return ESP_OK, ESP_ERR_INVALID_ARG, ESP_ERR_INVALID_STATE
 */
esp_err_t pwm_ctrl_set_resolution(int pin, uint8_t resolution);

/**
 * @brief Get current duty value for a pin.
 * @return duty (0-10000), or 0 on error
 */
uint16_t pwm_ctrl_get_duty(int pin);

/**
 * @brief Deconfigure a PWM pin (stop + release resources). Idempotent.
 * @return ESP_OK, ESP_ERR_INVALID_ARG
 */
esp_err_t pwm_ctrl_deconfig(int pin);

/**
 * @brief Check if a pin is currently running PWM.
 */
bool pwm_ctrl_is_running(int pin);

#ifdef __cplusplus
}
#endif

#endif /* PWM_CTRL_H */
