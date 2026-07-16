/**
 * @file pwm_ctrl.c
 * @brief PWM control addressed by reported LEDC channel with GPIO routing.
 */

#include "pwm_ctrl.h"
#include "esp_log.h"
#include "esp_system.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "driver/ledc.h"
#include "hw_tables.h"
#include "periph_owner.h"
#include <string.h>

#define TAG "PWM_CTRL"
#define PWM_LEDC_TIMER_NUM     SOC_LEDC_TIMER_NUM
#define PWM_LEDC_CHANNEL_NUM   SOC_LEDC_CHANNEL_NUM
#define PWM_LEDC_MODE          LEDC_LOW_SPEED_MODE
#define PWM_APB_CLK_HALF       40000000ULL

_Static_assert(HW_PWM_COUNT <= SOC_LEDC_CHANNEL_NUM,
               "reported PWM resources exceed SoC LEDC channels");

typedef struct {
    bool in_use;
    uint32_t frequency;
    ledc_timer_t timer_id;
    uint8_t resolution;
    int refcount;
} pwm_timer_slot_t;

typedef struct {
    bool configured;
    bool running;
    int pin;
    ledc_channel_t channel;
    pwm_timer_slot_t *timer;
    uint32_t frequency;
    uint16_t duty;
    uint8_t resolution;
} pwm_channel_state_t;

static pwm_timer_slot_t s_timers[PWM_LEDC_TIMER_NUM];
static pwm_channel_state_t s_channels[PWM_LEDC_CHANNEL_NUM];
static pwm_timer_slot_t *s_orphan_timers[PWM_LEDC_TIMER_NUM];
static SemaphoreHandle_t s_mutex;
static bool s_initialized;

static bool is_valid_pin(int pin)
{
    for (int i = 0; i < HW_GPIO_COUNT; i++) {
        if (hw_gpios[i].pin == pin) return true;
    }
    return false;
}

static int channel_index(int channel)
{
    for (int i = 0; i < HW_PWM_COUNT; i++) {
        if (hw_pwms[i].channel == channel && channel < PWM_LEDC_CHANNEL_NUM) {
            return channel;
        }
    }
    return -1;
}

static uint8_t channel_max_resolution(int channel)
{
    for (int i = 0; i < HW_PWM_COUNT; i++) {
        if (hw_pwms[i].channel == channel) return hw_pwms[i].max_resolution_bits;
    }
    return 0;
}

static bool is_pin_conflict(int pin)
{
    (void)pin;
    return false; /* periph_owner_claim serializes against the current bus pin set */
}

static bool check_freq_resolution(uint32_t freq, uint8_t resolution)
{
    if (freq == 0 || resolution < PWM_RES_MIN || resolution > PWM_RES_MAX) return false;
    return (uint64_t)freq * (1ULL << resolution) <= PWM_APB_CLK_HALF;
}

static uint32_t duty_to_ledc(uint16_t duty, uint8_t resolution)
{
    return (uint32_t)((uint64_t)duty * (1ULL << resolution) / PWM_DUTY_MAX);
}

static uint16_t ledc_to_duty(uint32_t duty, uint8_t resolution)
{
    return resolution == 0 ? 0 :
           (uint16_t)((uint64_t)duty * PWM_DUTY_MAX / (1ULL << resolution));
}

static pwm_timer_slot_t *find_or_alloc_timer(uint32_t freq, uint8_t resolution)
{
    for (int i = 0; i < PWM_LEDC_TIMER_NUM; i++) {
        if (s_timers[i].in_use && s_timers[i].frequency == freq &&
            s_timers[i].resolution == resolution) return &s_timers[i];
    }
    for (int i = 0; i < PWM_LEDC_TIMER_NUM; i++) {
        if (s_timers[i].in_use) continue;
        ledc_timer_config_t config = {
            .speed_mode = PWM_LEDC_MODE,
            .duty_resolution = (ledc_timer_bit_t)resolution,
            .timer_num = (ledc_timer_t)i,
            .freq_hz = freq,
            .clk_cfg = LEDC_AUTO_CLK,
        };
        if (ledc_timer_config(&config) != ESP_OK) return NULL;
        s_timers[i] = (pwm_timer_slot_t){
            .in_use = true,
            .frequency = freq,
            .timer_id = (ledc_timer_t)i,
            .resolution = resolution,
        };
        return &s_timers[i];
    }
    return NULL;
}

static esp_err_t release_timer(pwm_timer_slot_t *slot)
{
	if (!slot || !slot->in_use) return ESP_OK;
	if (slot->refcount <= 1) {
		esp_err_t err = ledc_timer_pause(PWM_LEDC_MODE, slot->timer_id);
		if (err != ESP_OK) return err;
		err = ledc_timer_rst(PWM_LEDC_MODE, slot->timer_id);
		if (err != ESP_OK) {
			ledc_timer_config_t restore = {
				.speed_mode = PWM_LEDC_MODE,
				.duty_resolution = (ledc_timer_bit_t)slot->resolution,
				.timer_num = slot->timer_id,
				.freq_hz = slot->frequency,
				.clk_cfg = LEDC_AUTO_CLK,
			};
			if (ledc_timer_config(&restore) != ESP_OK) esp_restart();
			return err;
		}
        memset(slot, 0, sizeof(*slot));
		return ESP_OK;
    }
	slot->refcount--;
	return ESP_OK;
}

static void remember_orphan_timer(pwm_timer_slot_t *slot)
{
	if (!slot || !slot->in_use) return;
	for (int i = 0; i < PWM_LEDC_TIMER_NUM; i++) {
		if (s_orphan_timers[i] == slot) return;
		if (!s_orphan_timers[i]) { s_orphan_timers[i] = slot; return; }
	}
}

static esp_err_t retry_orphan_timers(void)
{
	for (int i = 0; i < PWM_LEDC_TIMER_NUM; i++) {
		if (!s_orphan_timers[i]) continue;
		esp_err_t err = release_timer(s_orphan_timers[i]);
		if (err != ESP_OK) return err;
		s_orphan_timers[i] = NULL;
	}
	return ESP_OK;
}

static esp_err_t restore_channel_hardware(const pwm_channel_state_t *state)
{
	if (!state || !state->timer) return ESP_ERR_INVALID_ARG;
	ledc_channel_config_t config = {
		.channel = state->channel,
		.duty = duty_to_ledc(state->duty, state->resolution),
		.gpio_num = state->pin,
		.speed_mode = PWM_LEDC_MODE,
		.timer_sel = state->timer->timer_id,
		.intr_type = LEDC_INTR_DISABLE,
		.flags.output_invert = 0,
	};
	return ledc_channel_config(&config);
}

static void ensure_mutex(void)
{
    if (!s_mutex) s_mutex = xSemaphoreCreateMutex();
}

esp_err_t pwm_ctrl_preflight(const pwm_config_entry_t *configs, int count)
{
    if (count < 0 || count > HW_PWM_COUNT || (count > 0 && !configs)) return ESP_ERR_INVALID_ARG;
    uint32_t frequencies[PWM_LEDC_TIMER_NUM] = {0};
    uint8_t resolutions[PWM_LEDC_TIMER_NUM] = {0};
    int timer_count = 0;
    for (int i = 0; i < count; i++) {
        const pwm_config_entry_t *cfg = &configs[i];
        uint8_t resolution = cfg->resolution ? cfg->resolution : PWM_RES_DEFAULT;
        if (channel_index(cfg->channel) < 0 || !is_valid_pin(cfg->pin) ||
            cfg->duty > PWM_DUTY_MAX || resolution > channel_max_resolution(cfg->channel) ||
            !check_freq_resolution(cfg->frequency, resolution)) return ESP_ERR_INVALID_ARG;
        for (int j = i + 1; j < count; j++) {
            if (cfg->channel == configs[j].channel || cfg->pin == configs[j].pin) {
                return ESP_ERR_PIN_CONFLICT;
            }
        }
        if (!cfg->auto_start) continue;
        bool found = false;
        for (int timer = 0; timer < timer_count; timer++) {
            if (frequencies[timer] == cfg->frequency && resolutions[timer] == resolution) found = true;
        }
        if (!found) {
            if (timer_count >= PWM_LEDC_TIMER_NUM) return ESP_ERR_NOT_FOUND;
            frequencies[timer_count] = cfg->frequency;
            resolutions[timer_count++] = resolution;
        }
    }
    return ESP_OK;
}

int pwm_ctrl_snapshot(pwm_config_entry_t *configs, int capacity)
{
    if (!configs || capacity < 0) return ESP_ERR_INVALID_ARG;
    int count = 0;
    for (int i = 0; i < PWM_LEDC_CHANNEL_NUM && count < capacity; i++) {
        if (!s_channels[i].configured) continue;
        configs[count++] = (pwm_config_entry_t){
            .channel = s_channels[i].channel, .pin = s_channels[i].pin,
            .frequency = s_channels[i].frequency, .duty = s_channels[i].duty,
            .resolution = s_channels[i].resolution,
            .auto_start = s_channels[i].running,
        };
    }
    return count;
}

static esp_err_t stop_runtime_locked(int index)
{
	pwm_channel_state_t previous = s_channels[index];
	esp_err_t err = ledc_stop(PWM_LEDC_MODE, s_channels[index].channel, 0);
	if (err != ESP_OK) return err;
	err = release_timer(s_channels[index].timer);
	if (err != ESP_OK) {
		if (restore_channel_hardware(&previous) != ESP_OK) esp_restart();
		return err;
	}
    s_channels[index].running = false;
    s_channels[index].timer = NULL;
	return ESP_OK;
}

static esp_err_t deconfig_locked(int index)
{
    if (!s_channels[index].configured) return ESP_OK;
    int pin = s_channels[index].pin;
    int channel = s_channels[index].channel;
	if (s_channels[index].running) {
		esp_err_t err = stop_runtime_locked(index);
		if (err != ESP_OK) return err;
	}
    memset(&s_channels[index], 0, sizeof(s_channels[index]));
    periph_owner_release(pin, PERIPH_PIN_OWNER_PWM, channel);
	return ESP_OK;
}

esp_err_t pwm_ctrl_init(const pwm_config_entry_t *configs, int count)
{
    esp_err_t preflight = pwm_ctrl_preflight(configs, count);
    if (preflight != ESP_OK) return preflight;
    ensure_mutex();
    if (!s_mutex) return ESP_ERR_NO_MEM;
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    for (int i = 0; i < PWM_LEDC_CHANNEL_NUM; i++) {
        if (s_channels[i].configured) {
			esp_err_t err = deconfig_locked(i);
			if (err != ESP_OK) {
				xSemaphoreGive(s_mutex);
				return err;
			}
		}
    }
    xSemaphoreGive(s_mutex);

    esp_err_t result = ESP_OK;
    for (int i = 0; configs && i < count; i++) {
        if (channel_index(configs[i].channel) < 0 || !is_valid_pin(configs[i].pin)) {
            result = ESP_ERR_INVALID_ARG;
            continue;
        }
        if (is_pin_conflict(configs[i].pin)) {
            result = ESP_ERR_PIN_CONFLICT;
            continue;
        }
        esp_err_t claim_err = periph_owner_claim(configs[i].pin,
                                                 PERIPH_PIN_OWNER_PWM,
                                                 configs[i].channel);
        if (claim_err != ESP_OK) {
            result = claim_err;
            continue;
        }
        int index = channel_index(configs[i].channel);
        uint8_t resolution = configs[i].resolution ? configs[i].resolution : PWM_RES_DEFAULT;
        s_channels[index] = (pwm_channel_state_t){
            .configured = true,
            .pin = configs[i].pin,
            .channel = (ledc_channel_t)configs[i].channel,
            .frequency = configs[i].frequency,
            .duty = configs[i].duty,
            .resolution = resolution,
        };
        if (configs[i].auto_start) {
            esp_err_t err = pwm_ctrl_start(configs[i].channel, configs[i].pin,
                                           configs[i].frequency, configs[i].duty,
                                           resolution);
            if (err != ESP_OK) result = err;
        }
    }
    s_initialized = true;
    ESP_LOGI(TAG, "init: %d configs processed", count);
    return result;
}

esp_err_t pwm_ctrl_start(int channel, int pin, uint32_t freq, uint16_t duty,
                         uint8_t resolution)
{
    ensure_mutex();
    int index = channel_index(channel);
    if (index < 0 || !is_valid_pin(pin)) return ESP_ERR_INVALID_ARG;
    if (is_pin_conflict(pin)) return ESP_ERR_PIN_CONFLICT;
    if (duty > PWM_DUTY_MAX) return ESP_ERR_INVALID_ARG;
    if (resolution == 0) resolution = PWM_RES_DEFAULT;
    if (resolution > channel_max_resolution(channel) ||
        !check_freq_resolution(freq, resolution)) return ESP_ERR_INVALID_ARG;

    xSemaphoreTake(s_mutex, portMAX_DELAY);
    if (retry_orphan_timers() != ESP_OK) {
        xSemaphoreGive(s_mutex);
        return ESP_FAIL;
    }

    for (int i = 0; i < PWM_LEDC_CHANNEL_NUM; i++) {
        if (i != index && s_channels[i].configured && s_channels[i].pin == pin) {
            xSemaphoreGive(s_mutex);
            return ESP_ERR_PIN_CONFLICT;
        }
    }

    pwm_channel_state_t previous = s_channels[index];
    bool replacing = previous.configured;
    if (replacing && previous.pin != pin) {
        esp_err_t claim_err = periph_owner_claim(pin, PERIPH_PIN_OWNER_PWM, channel);
        if (claim_err != ESP_OK) {
            xSemaphoreGive(s_mutex);
            return claim_err;
        }
    } else if (!replacing) {
        esp_err_t claim_err = periph_owner_claim(pin, PERIPH_PIN_OWNER_PWM, channel);
        if (claim_err != ESP_OK) {
            xSemaphoreGive(s_mutex);
            return claim_err;
        }
    }

    pwm_timer_slot_t *timer = find_or_alloc_timer(freq, resolution);
    if (!timer) {
        if (!replacing || previous.pin != pin) {
            periph_owner_release(pin, PERIPH_PIN_OWNER_PWM, channel);
        }
        xSemaphoreGive(s_mutex);
        return ESP_ERR_NOT_FOUND;
    }
    bool timer_was_unused = timer->refcount == 0;
    ledc_channel_config_t config = {
        .channel = (ledc_channel_t)channel,
        .duty = duty_to_ledc(duty, resolution),
        .gpio_num = pin,
        .speed_mode = PWM_LEDC_MODE,
        .timer_sel = timer->timer_id,
        .intr_type = LEDC_INTR_DISABLE,
        .flags.output_invert = 0,
    };
    esp_err_t err = ledc_channel_config(&config);
    if (err != ESP_OK) {
        if (timer_was_unused) {
			esp_err_t cleanup_err = release_timer(timer);
			if (cleanup_err != ESP_OK) err = cleanup_err;
        }
        if (!replacing || previous.pin != pin) {
            periph_owner_release(pin, PERIPH_PIN_OWNER_PWM, channel);
        }
        xSemaphoreGive(s_mutex);
        return err;
    }
    timer->refcount++;
    if (replacing) {
		if (previous.timer != timer) {
			esp_err_t release_err = release_timer(previous.timer);
			if (release_err != ESP_OK) {
				esp_err_t restore_err = restore_channel_hardware(&previous);
				if (restore_err == ESP_OK) {
					esp_err_t cleanup_err = release_timer(timer);
					if (cleanup_err != ESP_OK) remember_orphan_timer(timer);
					if (previous.pin != pin) periph_owner_release(pin, PERIPH_PIN_OWNER_PWM, channel);
					s_channels[index] = previous;
				} else {
					if (previous.pin != pin) periph_owner_release(previous.pin, PERIPH_PIN_OWNER_PWM, channel);
					s_channels[index] = (pwm_channel_state_t){
						.configured = true, .running = true, .pin = pin,
						.channel = (ledc_channel_t)channel, .timer = timer,
						.frequency = freq, .duty = duty, .resolution = resolution,
					};
					remember_orphan_timer(previous.timer);
					/* Neither teardown nor rollback produced a trustworthy result.
					 * Restart rather than return a false failure over live new state. */
					esp_restart();
				}
				xSemaphoreGive(s_mutex);
				return release_err;
			}
		} else {
			/* Replacement keeps the same timer reference. */
			timer->refcount--;
		}
        if (previous.pin != pin) {
            periph_owner_release(previous.pin, PERIPH_PIN_OWNER_PWM, channel);
        }
    }
    s_channels[index] = (pwm_channel_state_t){
        .configured = true,
        .running = true,
        .pin = pin,
        .channel = (ledc_channel_t)channel,
        .timer = timer,
        .frequency = freq,
        .duty = duty,
        .resolution = resolution,
    };
    xSemaphoreGive(s_mutex);
    ESP_LOGI(TAG, "start: channel=%d pin=%d freq=%u duty=%u res=%u",
             channel, pin, (unsigned)freq, duty, resolution);
    return ESP_OK;
}

esp_err_t pwm_ctrl_stop(int channel)
{
    ensure_mutex();
    int index = channel_index(channel);
    if (index < 0) return ESP_ERR_INVALID_ARG;
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    if (!s_channels[index].running) {
        xSemaphoreGive(s_mutex);
        return ESP_OK;
    }
	esp_err_t err = stop_runtime_locked(index);
    xSemaphoreGive(s_mutex);
	return err;
}

esp_err_t pwm_ctrl_set_duty(int channel, uint16_t duty)
{
    ensure_mutex();
    int index = channel_index(channel);
    if (index < 0 || duty > PWM_DUTY_MAX) return ESP_ERR_INVALID_ARG;
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    if (!s_channels[index].running) {
        xSemaphoreGive(s_mutex);
        return ESP_ERR_INVALID_STATE;
    }
    uint32_t hw_duty = duty_to_ledc(duty, s_channels[index].resolution);
    esp_err_t err = ledc_set_duty(PWM_LEDC_MODE, s_channels[index].channel, hw_duty);
    if (err == ESP_OK) err = ledc_update_duty(PWM_LEDC_MODE, s_channels[index].channel);
    if (err == ESP_OK) s_channels[index].duty = duty;
    xSemaphoreGive(s_mutex);
    return err;
}

esp_err_t pwm_ctrl_set_freq(int channel, uint32_t freq, uint8_t resolution)
{
    ensure_mutex();
    int index = channel_index(channel);
    if (index < 0 || freq == 0) return ESP_ERR_INVALID_ARG;
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    if (retry_orphan_timers() != ESP_OK) {
        xSemaphoreGive(s_mutex);
        return ESP_FAIL;
    }
    if (!s_channels[index].running) {
        xSemaphoreGive(s_mutex);
        return ESP_ERR_INVALID_STATE;
    }
    uint8_t new_resolution = resolution ? resolution : s_channels[index].resolution;
    if (new_resolution > channel_max_resolution(channel) ||
        !check_freq_resolution(freq, new_resolution)) {
        xSemaphoreGive(s_mutex);
        return ESP_ERR_INVALID_ARG;
    }
    if (s_channels[index].frequency == freq && s_channels[index].resolution == new_resolution) {
        xSemaphoreGive(s_mutex);
        return ESP_OK;
    }

    pwm_timer_slot_t *new_timer = find_or_alloc_timer(freq, new_resolution);
    if (!new_timer) {
        xSemaphoreGive(s_mutex);
        return ESP_ERR_NOT_FOUND;
    }
    bool new_timer_was_unused = new_timer->refcount == 0;
    ledc_channel_config_t config = {
        .channel = s_channels[index].channel,
        .duty = duty_to_ledc(s_channels[index].duty, new_resolution),
        .gpio_num = s_channels[index].pin,
        .speed_mode = PWM_LEDC_MODE,
        .timer_sel = new_timer->timer_id,
        .intr_type = LEDC_INTR_DISABLE,
        .flags.output_invert = 0,
    };
    esp_err_t err = ledc_channel_config(&config);
    if (err != ESP_OK) {
        if (new_timer_was_unused) {
			esp_err_t cleanup_err = release_timer(new_timer);
			if (cleanup_err != ESP_OK) err = cleanup_err;
        }
        xSemaphoreGive(s_mutex);
        return err;
    }
	new_timer->refcount++;
    pwm_timer_slot_t *old_timer = s_channels[index].timer;
    esp_err_t release_err = release_timer(old_timer);
	if (release_err != ESP_OK) {
		pwm_channel_state_t previous = s_channels[index];
		esp_err_t restore_err = restore_channel_hardware(&previous);
		if (restore_err == ESP_OK) {
			esp_err_t cleanup_err = release_timer(new_timer);
			if (cleanup_err != ESP_OK) remember_orphan_timer(new_timer);
		} else {
			s_channels[index].timer = new_timer;
			s_channels[index].frequency = freq;
			s_channels[index].resolution = new_resolution;
			remember_orphan_timer(old_timer);
			esp_restart();
		}
		xSemaphoreGive(s_mutex);
		return release_err;
	}
    s_channels[index].timer = new_timer;
    s_channels[index].frequency = freq;
    s_channels[index].resolution = new_resolution;
    xSemaphoreGive(s_mutex);
    return ESP_OK;
}

esp_err_t pwm_ctrl_set_resolution(int channel, uint8_t resolution)
{
    ensure_mutex();
    int index = channel_index(channel);
    if (index < 0 || resolution < PWM_RES_MIN ||
        resolution > channel_max_resolution(channel)) {
        return ESP_ERR_INVALID_ARG;
    }
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    if (!s_channels[index].running) {
        xSemaphoreGive(s_mutex);
        return ESP_ERR_INVALID_STATE;
    }
    uint32_t frequency = s_channels[index].frequency;
    xSemaphoreGive(s_mutex);
    return pwm_ctrl_set_freq(channel, frequency, resolution);
}

esp_err_t pwm_ctrl_get_duty(int channel, uint16_t *duty)
{
    int index = channel_index(channel);
    if (index < 0 || !duty) return ESP_ERR_INVALID_ARG;
    ensure_mutex();
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    if (!s_channels[index].running) {
        xSemaphoreGive(s_mutex);
        return ESP_ERR_INVALID_STATE;
    }
    *duty = ledc_to_duty(ledc_get_duty(PWM_LEDC_MODE, s_channels[index].channel),
                         s_channels[index].resolution);
    if (*duty == 0 && s_channels[index].duty > 0) *duty = s_channels[index].duty;
    xSemaphoreGive(s_mutex);
    return ESP_OK;
}

esp_err_t pwm_ctrl_deconfig(int channel)
{
    ensure_mutex();
    int index = channel_index(channel);
    if (index < 0) return ESP_ERR_INVALID_ARG;
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    esp_err_t err = deconfig_locked(index);
    xSemaphoreGive(s_mutex);
    return err;
}

bool pwm_ctrl_is_running(int channel)
{
    int index = channel_index(channel);
    if (index < 0) return false;
    ensure_mutex();
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    bool running = s_channels[index].running;
    xSemaphoreGive(s_mutex);
    return running;
}
