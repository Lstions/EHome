#include "periph_config_apply.h"

#include <stdlib.h>
#include "esp_log.h"
#include "periph_owner.h"

#define TAG "PERIPH_CFG"

typedef struct {
    gpio_config_entry_t gpio_entries[MAX_GPIO_CONFIGS];
    pwm_config_entry_t pwm_entries[MAX_PWM_CONFIGS];
    periph_runtime_snapshot_t old;
} periph_config_workspace_t;

static esp_err_t enter_safe_state(void)
{
    static const config_manifest_t empty_manifest;
    esp_err_t pwm_err = pwm_ctrl_init(NULL, 0);
    esp_err_t gpio_err = gpio_ctrl_init(NULL, 0);
    esp_err_t owner_err = periph_owner_replace_manifest(&empty_manifest);
    if (pwm_err != ESP_OK || gpio_err != ESP_OK || owner_err != ESP_OK) {
        ESP_LOGE(TAG, "safe-state failure: pwm=%s gpio=%s owner=%s",
                 esp_err_to_name(pwm_err), esp_err_to_name(gpio_err),
                 esp_err_to_name(owner_err));
        return ESP_FAIL;
    }
    return ESP_OK;
}

static esp_err_t rollback(periph_config_workspace_t *workspace,
                          int old_gpio_count, int old_pwm_count)
{
    esp_err_t pwm_clear = pwm_ctrl_init(NULL, 0);
    esp_err_t gpio_clear = gpio_ctrl_init(NULL, 0);
    esp_err_t owner_restore = periph_owner_restore(&workspace->old.owners);
    if (pwm_clear != ESP_OK || gpio_clear != ESP_OK || owner_restore != ESP_OK) {
        ESP_LOGE(TAG, "rollback clear/owner failure");
        (void)enter_safe_state();
        return ESP_FAIL;
    }

    /* Ownership-safe order: old PWM routes before old GPIO routes. */
    esp_err_t pwm_restore = pwm_ctrl_init(old_pwm_count ? workspace->old.pwm : NULL,
                                          old_pwm_count);
    esp_err_t gpio_restore = gpio_ctrl_init(old_gpio_count ? workspace->old.gpio : NULL,
                                            old_gpio_count);
    if (pwm_restore != ESP_OK || gpio_restore != ESP_OK) {
        ESP_LOGE(TAG, "rollback controller restore failure: pwm=%s gpio=%s",
                 esp_err_to_name(pwm_restore), esp_err_to_name(gpio_restore));
        (void)enter_safe_state();
        return ESP_FAIL;
    }
    return ESP_OK;
}

esp_err_t periph_config_snapshot_locked(periph_runtime_snapshot_t *snapshot)
{
    if (!snapshot) return ESP_ERR_INVALID_ARG;
    snapshot->gpio_count = gpio_ctrl_snapshot(snapshot->gpio, MAX_GPIO_CONFIGS);
    snapshot->pwm_count = pwm_ctrl_snapshot(snapshot->pwm, MAX_PWM_CONFIGS);
    esp_err_t owner_err = periph_owner_snapshot(&snapshot->owners);
    if (snapshot->gpio_count < 0 || snapshot->pwm_count < 0 || owner_err != ESP_OK) {
        return owner_err != ESP_OK ? owner_err : ESP_FAIL;
    }
    return ESP_OK;
}

esp_err_t periph_config_snapshot(periph_runtime_snapshot_t *snapshot)
{
    esp_err_t err = periph_owner_transaction_begin();
    if (err != ESP_OK) return err;
    err = periph_config_snapshot_locked(snapshot);
    periph_owner_transaction_end();
    return err;
}

esp_err_t periph_config_restore_locked(const periph_runtime_snapshot_t *snapshot)
{
    if (!snapshot) return ESP_ERR_INVALID_ARG;
    periph_config_workspace_t *workspace = calloc(1, sizeof(*workspace));
    if (!workspace) return ESP_ERR_NO_MEM;
    workspace->old = *snapshot;
    esp_err_t result = rollback(workspace, snapshot->gpio_count, snapshot->pwm_count);
    free(workspace);
    return result;
}

esp_err_t periph_config_restore(const periph_runtime_snapshot_t *snapshot)
{
    esp_err_t err = periph_owner_transaction_begin();
    if (err != ESP_OK) return err;
    err = periph_config_restore_locked(snapshot);
    periph_owner_transaction_end();
    return err;
}

esp_err_t periph_config_apply_locked(const config_manifest_t *cfg)
{
    if (!cfg) return ESP_ERR_INVALID_ARG;

    esp_err_t validation_err = periph_owner_validate_manifest(cfg);
    if (validation_err != ESP_OK) return validation_err;

    periph_config_workspace_t *workspace = calloc(1, sizeof(*workspace));
    if (!workspace) return ESP_ERR_NO_MEM;

    for (int i = 0; i < cfg->gpio_config_count; i++) {
        workspace->gpio_entries[i] = (gpio_config_entry_t){
            .pin = cfg->gpio_configs[i].pin,
            .direction = cfg->gpio_configs[i].direction,
            .initial_level = cfg->gpio_configs[i].initial_level,
        };
    }
    for (int i = 0; i < cfg->pwm_config_count; i++) {
        workspace->pwm_entries[i] = (pwm_config_entry_t){
            .channel = cfg->pwm_configs[i].channel,
            .pin = cfg->pwm_configs[i].pin,
            .frequency = cfg->pwm_configs[i].frequency,
            .duty = cfg->pwm_configs[i].duty,
            .resolution = cfg->pwm_configs[i].resolution,
            .auto_start = cfg->pwm_configs[i].auto_start,
        };
    }

    esp_err_t gpio_err = gpio_ctrl_preflight(cfg->gpio_config_count ? workspace->gpio_entries : NULL,
                                             cfg->gpio_config_count);
    if (gpio_err != ESP_OK) {
        free(workspace);
        return gpio_err;
    }
    esp_err_t pwm_err = pwm_ctrl_preflight(cfg->pwm_config_count ? workspace->pwm_entries : NULL,
                                           cfg->pwm_config_count);
    if (pwm_err != ESP_OK) {
        free(workspace);
        return pwm_err;
    }

    esp_err_t snapshot_err = periph_config_snapshot_locked(&workspace->old);
    int old_gpio_count = workspace->old.gpio_count;
    int old_pwm_count = workspace->old.pwm_count;
    if (snapshot_err != ESP_OK) {
        free(workspace);
        return snapshot_err;
    }

    pwm_err = pwm_ctrl_init(NULL, 0);
    gpio_err = gpio_ctrl_init(NULL, 0);
    if (pwm_err != ESP_OK || gpio_err != ESP_OK) {
        (void)rollback(workspace, old_gpio_count, old_pwm_count);
        free(workspace);
        return ESP_FAIL;
    }

    esp_err_t owner_err = periph_owner_replace_manifest(cfg);
    if (owner_err != ESP_OK) {
        (void)rollback(workspace, old_gpio_count, old_pwm_count);
        free(workspace);
        return owner_err;
    }

    gpio_err = gpio_ctrl_init(cfg->gpio_config_count ? workspace->gpio_entries : NULL,
                              cfg->gpio_config_count);
    if (gpio_err == ESP_OK) {
        pwm_err = pwm_ctrl_init(cfg->pwm_config_count ? workspace->pwm_entries : NULL,
                                cfg->pwm_config_count);
    }
    if (gpio_err != ESP_OK || pwm_err != ESP_OK) {
        esp_err_t apply_err = gpio_err != ESP_OK ? gpio_err : pwm_err;
        if (rollback(workspace, old_gpio_count, old_pwm_count) != ESP_OK) {
            apply_err = ESP_FAIL;
        }
        free(workspace);
        return apply_err;
    }

    free(workspace);
    return ESP_OK;
}

esp_err_t periph_config_apply(const config_manifest_t *cfg)
{
    esp_err_t err = periph_owner_transaction_begin();
    if (err != ESP_OK) return err;
    err = periph_config_apply_locked(cfg);
    periph_owner_transaction_end();
    return err;
}
