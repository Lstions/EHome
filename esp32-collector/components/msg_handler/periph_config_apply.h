#ifndef PERIPH_CONFIG_APPLY_H
#define PERIPH_CONFIG_APPLY_H

#include "config_mgr.h"
#include "gpio_ctrl.h"
#include "pwm_ctrl.h"
#include "periph_owner.h"

typedef struct {
    gpio_config_entry_t gpio[MAX_GPIO_CONFIGS];
    pwm_config_entry_t pwm[MAX_PWM_CONFIGS];
    int gpio_count;
    int pwm_count;
    periph_owner_snapshot_t owners;
} periph_runtime_snapshot_t;

esp_err_t periph_config_apply(const config_manifest_t *cfg);
esp_err_t periph_config_snapshot(periph_runtime_snapshot_t *snapshot);
esp_err_t periph_config_restore(const periph_runtime_snapshot_t *snapshot);

/* Variants for callers that already hold periph_owner_transaction_begin(). */
esp_err_t periph_config_apply_locked(const config_manifest_t *cfg);
esp_err_t periph_config_snapshot_locked(periph_runtime_snapshot_t *snapshot);
esp_err_t periph_config_restore_locked(const periph_runtime_snapshot_t *snapshot);

#endif