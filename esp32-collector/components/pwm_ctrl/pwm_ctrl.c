/**
 * @file pwm_ctrl.c
 * @brief PWM peripheral control implementation via LEDC
 *
 * LEDC resource management:
 *   - ESP32-C6: 6 channels, 4 timers
 *   - Same-frequency channels share a timer (reference counting)
 *   - STOP decrements refcount, deinit timer at 0
 *   - Resource exhaustion returns ESP_ERR_NOT_FOUND
 *
 * Duty mapping: ledc_duty = (duty * 2^resolution) / 10000
 * Freq constraint: freq * 2^resolution <= 40MHz (APB_CLK / 2)
 */

#include "pwm_ctrl.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "driver/ledc.h"
#include "hw_tables.h"
#include <string.h>

#define TAG "PWM_CTRL"

/* ESP32-C6 LEDC constants */
#define PWM_LEDC_TIMER_NUM     SOC_LEDC_TIMER_NUM    /* 4 on C6 */
#define PWM_LEDC_CHANNEL_NUM   SOC_LEDC_CHANNEL_NUM  /* 6 on C6 */
#define PWM_LEDC_MODE          LEDC_LOW_SPEED_MODE   /* C6 only has low-speed */
#define PWM_LEDC_DUTY_MAX_BITS 20
#define PWM_APB_CLK_HALF       40000000ULL  /* APB_CLK / 2 = 40 MHz */

/* === Timer pool with reference counting === */
typedef struct {
    bool          in_use;
    uint32_t      frequency;
    ledc_timer_t  timer_id;
    uint8_t       resolution;
    int           refcount;
} pwm_timer_slot_t;

/* === Channel state === */
typedef struct {
    bool             running;
    int              pin;
    ledc_channel_t   channel;
    pwm_timer_slot_t *timer;  /* pointer to shared timer slot */
    uint32_t         frequency;
    uint16_t         duty;     /* user-space duty (0-10000) */
    uint8_t          resolution;
} pwm_channel_state_t;

static pwm_timer_slot_t  s_timers[PWM_LEDC_TIMER_NUM];
static pwm_channel_state_t s_channels[PWM_LEDC_CHANNEL_NUM];
static SemaphoreHandle_t s_mutex = NULL;
static bool s_initialized = false;

/* Reserved pins — same as gpio_ctrl */
#ifdef CONFIG_IDF_TARGET_ESP32C6
  #define PWM_RESERVED_COUNT  3
  static const int s_reserved_pins[PWM_RESERVED_COUNT] = { 12, 13, 8 };
#elif defined(CONFIG_IDF_TARGET_ESP32S3)
  #define PWM_RESERVED_COUNT  3
  static const int s_reserved_pins[PWM_RESERVED_COUNT] = { 19, 20, 48 };
#else
  #define PWM_RESERVED_COUNT  0
  static const int s_reserved_pins[1] = { -1 };
#endif

/* === Internal helpers === */

static bool is_reserved_pin(int pin)
{
    for (int i = 0; i < PWM_RESERVED_COUNT; i++) {
        if (s_reserved_pins[i] == pin) return true;
    }
    return false;
}

static bool is_valid_pin(int pin)
{
    return pin >= 0 && pin < PWM_CTRL_MAX_PINS && !is_reserved_pin(pin);
}

static bool is_pin_conflict(int pin)
{
    if (is_reserved_pin(pin)) return true;

    for (int i = 0; i < HW_UART_COUNT; i++) {
        if (hw_uarts[i].default_tx_pin == pin ||
            hw_uarts[i].default_rx_pin == pin) return true;
    }
    for (int i = 0; i < HW_I2C_COUNT; i++) {
        if (hw_i2cs[i].default_sda == pin ||
            hw_i2cs[i].default_scl == pin) return true;
    }
    for (int i = 0; i < HW_SPI_COUNT; i++) {
        if (hw_spis[i].default_mosi == pin ||
            hw_spis[i].default_miso == pin ||
            hw_spis[i].default_sclk == pin ||
            hw_spis[i].default_cs == pin) return true;
    }
    return false;
}

static bool check_freq_resolution(uint32_t freq, uint8_t resolution)
{
    if (freq == 0) return false;
    if (resolution < PWM_RES_MIN || resolution > PWM_RES_MAX) return false;
    /* freq * 2^resolution <= 40 MHz */
    uint64_t product = (uint64_t)freq * (1ULL << resolution);
    return product <= PWM_APB_CLK_HALF;
}

static uint32_t duty_to_ledc(uint16_t duty, uint8_t resolution)
{
    /* ledc_duty = (duty * 2^resolution) / 10000 */
    return (uint32_t)((uint64_t)duty * (1ULL << resolution) / PWM_DUTY_MAX);
}

static uint16_t ledc_to_duty(uint32_t ledc_duty, uint8_t resolution)
{
    if (resolution == 0) return 0;
    return (uint16_t)((uint64_t)ledc_duty * PWM_DUTY_MAX / (1ULL << resolution));
}

/**
 * @brief Find or allocate a timer for the given frequency + resolution.
 * @return pointer to timer slot, or NULL if exhausted
 */
static pwm_timer_slot_t *find_or_alloc_timer(uint32_t freq, uint8_t resolution)
{
    /* First, try to find an existing timer with same freq+resolution */
    for (int i = 0; i < PWM_LEDC_TIMER_NUM; i++) {
        if (s_timers[i].in_use &&
            s_timers[i].frequency == freq &&
            s_timers[i].resolution == resolution) {
            return &s_timers[i];
        }
    }

    /* Allocate a free timer */
    for (int i = 0; i < PWM_LEDC_TIMER_NUM; i++) {
        if (!s_timers[i].in_use) {
            ledc_timer_config_t timer_conf = {
                .speed_mode      = PWM_LEDC_MODE,
                .duty_resolution = (ledc_timer_bit_t)resolution,
                .timer_num       = (ledc_timer_t)i,
                .freq_hz         = freq,
                .clk_cfg         = LEDC_AUTO_CLK,
            };
            esp_err_t ret = ledc_timer_config(&timer_conf);
            if (ret != ESP_OK) {
                ESP_LOGE(TAG, "ledc_timer_config(t=%d, f=%u, r=%d) failed: %s",
                         i, freq, resolution, esp_err_to_name(ret));
                return NULL;
            }

            s_timers[i].in_use     = true;
            s_timers[i].frequency  = freq;
            s_timers[i].timer_id   = (ledc_timer_t)i;
            s_timers[i].resolution = resolution;
            s_timers[i].refcount   = 0;
            ESP_LOGI(TAG, "Allocated timer %d: freq=%u, res=%d", i, freq, resolution);
            return &s_timers[i];
        }
    }

    /* All timers exhausted */
    ESP_LOGW(TAG, "LEDC timer pool exhausted (need freq=%u)", freq);
    return NULL;
}

static void release_timer(pwm_timer_slot_t *slot)
{
    if (!slot || !slot->in_use) return;
    slot->refcount--;
    if (slot->refcount <= 0) {
        ledc_timer_rst(PWM_LEDC_MODE, slot->timer_id);
        ledc_timer_pause(PWM_LEDC_MODE, slot->timer_id);
        ESP_LOGI(TAG, "Released timer %d (freq=%u)", slot->timer_id, slot->frequency);
        slot->in_use = false;
        slot->refcount = 0;
        slot->frequency = 0;
        slot->resolution = 0;
    }
}

/**
 * @brief Find channel slot by pin.
 * @return index or -1 if not found
 */
static int find_channel_by_pin(int pin)
{
    for (int i = 0; i < PWM_LEDC_CHANNEL_NUM; i++) {
        if (s_channels[i].running && s_channels[i].pin == pin) {
            return i;
        }
    }
    return -1;
}

/**
 * @brief Find a free channel slot.
 * @return index or -1 if exhausted
 */
static int find_free_channel(void)
{
    for (int i = 0; i < PWM_LEDC_CHANNEL_NUM; i++) {
        if (!s_channels[i].running) return i;
    }
    return -1;
}

/* === Public API === */

static void ensure_mutex(void)
{
    if (s_mutex == NULL) {
        s_mutex = xSemaphoreCreateMutex();
    }
}

esp_err_t pwm_ctrl_init(const pwm_config_entry_t *configs, int count)
{
    ensure_mutex();
    if (s_mutex == NULL) return ESP_ERR_NO_MEM;

    xSemaphoreTake(s_mutex, portMAX_DELAY);

    /* Stop and deconfig all existing channels */
    for (int i = 0; i < PWM_LEDC_CHANNEL_NUM; i++) {
        if (s_channels[i].running) {
            ledc_stop(PWM_LEDC_MODE, s_channels[i].channel, 0);
            release_timer(s_channels[i].timer);
            s_channels[i].running = false;
            s_channels[i].timer = NULL;
        }
    }

    /* Apply new configs */
    esp_err_t result = ESP_OK;
    for (int i = 0; i < count && configs != NULL; i++) {
        if (!is_valid_pin(configs[i].pin)) {
            ESP_LOGW(TAG, "init: skipping invalid pin %d", configs[i].pin);
            continue;
        }
        if (is_pin_conflict(configs[i].pin)) {
            ESP_LOGW(TAG, "init: skipping conflicting pin %d", configs[i].pin);
            continue;
        }

        uint8_t res = configs[i].resolution;
        if (res == 0) res = PWM_RES_DEFAULT;
        if (res < PWM_RES_MIN) res = PWM_RES_MIN;
        if (res > PWM_RES_MAX) res = PWM_RES_MAX;

        if (configs[i].auto_start) {
            esp_err_t ret = pwm_ctrl_start(configs[i].pin, configs[i].frequency,
                                            configs[i].duty, res);
            if (ret != ESP_OK) {
                ESP_LOGE(TAG, "init: auto_start pin=%d failed: %s",
                         configs[i].pin, esp_err_to_name(ret));
                result = ret;
            }
        }
        /* If not auto_start, just skip — pin will be started on demand */
    }

    s_initialized = true;
    xSemaphoreGive(s_mutex);
    ESP_LOGI(TAG, "init: %d configs processed", count);
    return result;
}

esp_err_t pwm_ctrl_start(int pin, uint32_t freq, uint16_t duty, uint8_t resolution)
{
    ensure_mutex();
    if (!is_valid_pin(pin)) return ESP_ERR_INVALID_ARG;
    if (is_pin_conflict(pin)) return ESP_ERR_INVALID_STATE;
    if (duty > PWM_DUTY_MAX) return ESP_ERR_INVALID_ARG;
    if (resolution == 0) resolution = PWM_RES_DEFAULT;
    if (resolution < PWM_RES_MIN || resolution > PWM_RES_MAX) return ESP_ERR_INVALID_ARG;
    if (!check_freq_resolution(freq, resolution)) return ESP_ERR_INVALID_ARG;

    xSemaphoreTake(s_mutex, portMAX_DELAY);

    /* If already running on this pin, stop first */
    int existing = find_channel_by_pin(pin);
    if (existing >= 0) {
        ledc_stop(PWM_LEDC_MODE, s_channels[existing].channel, 0);
        release_timer(s_channels[existing].timer);
        s_channels[existing].running = false;
        s_channels[existing].timer = NULL;
    }

    /* Find free channel */
    int ch_idx = find_free_channel();
    if (ch_idx < 0) {
        xSemaphoreGive(s_mutex);
        ESP_LOGW(TAG, "start: LEDC channel pool exhausted (pin=%d)", pin);
        return ESP_ERR_NOT_FOUND;
    }

    /* Find or allocate timer */
    pwm_timer_slot_t *timer = find_or_alloc_timer(freq, resolution);
    if (timer == NULL) {
        xSemaphoreGive(s_mutex);
        return ESP_ERR_NOT_FOUND;
    }

    /* Configure LEDC channel */
    ledc_channel_config_t ch_conf = {
        .channel    = (ledc_channel_t)ch_idx,
        .duty       = duty_to_ledc(duty, resolution),
        .gpio_num   = pin,
        .speed_mode = PWM_LEDC_MODE,
        .timer_sel  = timer->timer_id,
        .intr_type  = LEDC_INTR_DISABLE,
        .flags.output_invert = 0,
    };

    esp_err_t ret = ledc_channel_config(&ch_conf);
    if (ret != ESP_OK) {
        ESP_LOGE(TAG, "ledc_channel_config(pin=%d, ch=%d) failed: %s",
                 pin, ch_idx, esp_err_to_name(ret));
        /* Don't hold timer if channel config failed and no other users */
        if (timer->refcount == 0) {
            release_timer(timer);
        }
        xSemaphoreGive(s_mutex);
        return ret;
    }

    timer->refcount++;
    s_channels[ch_idx].running    = true;
    s_channels[ch_idx].pin        = pin;
    s_channels[ch_idx].channel    = (ledc_channel_t)ch_idx;
    s_channels[ch_idx].timer      = timer;
    s_channels[ch_idx].frequency  = freq;
    s_channels[ch_idx].duty       = duty;
    s_channels[ch_idx].resolution = resolution;

    xSemaphoreGive(s_mutex);
    ESP_LOGI(TAG, "start: pin=%d, ch=%d, freq=%u, duty=%u, res=%d",
             pin, ch_idx, freq, duty, resolution);
    return ESP_OK;
}

esp_err_t pwm_ctrl_stop(int pin)
{
    ensure_mutex();
    if (!is_valid_pin(pin)) return ESP_ERR_INVALID_ARG;

    xSemaphoreTake(s_mutex, portMAX_DELAY);

    int ch_idx = find_channel_by_pin(pin);
    if (ch_idx < 0) {
        xSemaphoreGive(s_mutex);
        return ESP_ERR_INVALID_STATE;  /* not running */
    }

    ledc_stop(PWM_LEDC_MODE, s_channels[ch_idx].channel, 0);
    release_timer(s_channels[ch_idx].timer);
    s_channels[ch_idx].running = false;
    s_channels[ch_idx].timer = NULL;

    xSemaphoreGive(s_mutex);
    ESP_LOGI(TAG, "stop: pin=%d", pin);
    return ESP_OK;
}

esp_err_t pwm_ctrl_set_duty(int pin, uint16_t duty)
{
    ensure_mutex();
    if (!is_valid_pin(pin)) return ESP_ERR_INVALID_ARG;
    if (duty > PWM_DUTY_MAX) return ESP_ERR_INVALID_ARG;

    xSemaphoreTake(s_mutex, portMAX_DELAY);

    int ch_idx = find_channel_by_pin(pin);
    if (ch_idx < 0) {
        xSemaphoreGive(s_mutex);
        return ESP_ERR_INVALID_STATE;
    }

    uint32_t ledc_duty = duty_to_ledc(duty, s_channels[ch_idx].resolution);
    esp_err_t ret = ledc_set_duty(PWM_LEDC_MODE, s_channels[ch_idx].channel, ledc_duty);
    if (ret != ESP_OK) {
        xSemaphoreGive(s_mutex);
        return ret;
    }
    ledc_update_duty(PWM_LEDC_MODE, s_channels[ch_idx].channel);
    s_channels[ch_idx].duty = duty;

    xSemaphoreGive(s_mutex);
    return ESP_OK;
}

esp_err_t pwm_ctrl_set_freq(int pin, uint32_t freq, uint8_t resolution)
{
    ensure_mutex();
    if (!is_valid_pin(pin)) return ESP_ERR_INVALID_ARG;
    if (freq == 0) return ESP_ERR_INVALID_ARG;

    xSemaphoreTake(s_mutex, portMAX_DELAY);

    int ch_idx = find_channel_by_pin(pin);
    if (ch_idx < 0) {
        xSemaphoreGive(s_mutex);
        return ESP_ERR_INVALID_STATE;
    }

    uint8_t new_res = (resolution > 0) ? resolution : s_channels[ch_idx].resolution;
    if (new_res < PWM_RES_MIN || new_res > PWM_RES_MAX) {
        xSemaphoreGive(s_mutex);
        return ESP_ERR_INVALID_ARG;
    }
    if (!check_freq_resolution(freq, new_res)) {
        xSemaphoreGive(s_mutex);
        return ESP_ERR_INVALID_ARG;
    }

    /* If freq or res changed, we need to re-allocate timer and reconfig channel */
    if (s_channels[ch_idx].frequency != freq || s_channels[ch_idx].resolution != new_res) {
        uint16_t cur_duty = s_channels[ch_idx].duty;
        pwm_timer_slot_t *old_timer = s_channels[ch_idx].timer;

        /* Stop channel, release old timer */
        ledc_stop(PWM_LEDC_MODE, s_channels[ch_idx].channel, 0);
        release_timer(old_timer);
        s_channels[ch_idx].running = false;
        s_channels[ch_idx].timer = NULL;

        /* Find or alloc new timer */
        pwm_timer_slot_t *new_timer = find_or_alloc_timer(freq, new_res);
        if (new_timer == NULL) {
            xSemaphoreGive(s_mutex);
            return ESP_ERR_NOT_FOUND;
        }

        /* Reconfig channel with new timer */
        ledc_channel_config_t ch_conf = {
            .channel    = s_channels[ch_idx].channel,
            .duty       = duty_to_ledc(cur_duty, new_res),
            .gpio_num   = pin,
            .speed_mode = PWM_LEDC_MODE,
            .timer_sel  = new_timer->timer_id,
            .intr_type  = LEDC_INTR_DISABLE,
            .flags.output_invert = 0,
        };

        esp_err_t ret = ledc_channel_config(&ch_conf);
        if (ret != ESP_OK) {
            if (new_timer->refcount == 0) release_timer(new_timer);
            xSemaphoreGive(s_mutex);
            return ret;
        }

        new_timer->refcount++;
        s_channels[ch_idx].running    = true;
        s_channels[ch_idx].timer      = new_timer;
        s_channels[ch_idx].frequency  = freq;
        s_channels[ch_idx].resolution = new_res;
    }

    xSemaphoreGive(s_mutex);
    ESP_LOGI(TAG, "set_freq: pin=%d, freq=%u, res=%d", pin, freq, resolution);
    return ESP_OK;
}

esp_err_t pwm_ctrl_set_resolution(int pin, uint8_t resolution)
{
    ensure_mutex();
    if (!is_valid_pin(pin)) return ESP_ERR_INVALID_ARG;
    if (resolution < PWM_RES_MIN || resolution > PWM_RES_MAX) return ESP_ERR_INVALID_ARG;

    xSemaphoreTake(s_mutex, portMAX_DELAY);

    int ch_idx = find_channel_by_pin(pin);
    if (ch_idx < 0) {
        xSemaphoreGive(s_mutex);
        return ESP_ERR_INVALID_STATE;
    }

    /* Changing resolution requires re-configuring timer — delegate to set_freq */
    uint32_t freq = s_channels[ch_idx].frequency;
    xSemaphoreGive(s_mutex);

    return pwm_ctrl_set_freq(pin, freq, resolution);
}

uint16_t pwm_ctrl_get_duty(int pin)
{
    if (!is_valid_pin(pin)) return 0;

    ensure_mutex();
    xSemaphoreTake(s_mutex, portMAX_DELAY);

    int ch_idx = find_channel_by_pin(pin);
    if (ch_idx < 0) {
        xSemaphoreGive(s_mutex);
        return 0;
    }

    /* Read hardware duty and convert back */
    uint32_t hw_duty = ledc_get_duty(PWM_LEDC_MODE, s_channels[ch_idx].channel);
    uint16_t user_duty = ledc_to_duty(hw_duty, s_channels[ch_idx].resolution);

    /* Return cached value if hw reads 0 but we know it's running */
    if (user_duty == 0 && s_channels[ch_idx].duty > 0) {
        user_duty = s_channels[ch_idx].duty;
    }

    xSemaphoreGive(s_mutex);
    return user_duty;
}

esp_err_t pwm_ctrl_deconfig(int pin)
{
    /* Idempotent: stop if running, then ensure cleanup */
    ensure_mutex();
    if (!is_valid_pin(pin)) return ESP_ERR_INVALID_ARG;

    xSemaphoreTake(s_mutex, portMAX_DELAY);

    int ch_idx = find_channel_by_pin(pin);
    if (ch_idx >= 0) {
        ledc_stop(PWM_LEDC_MODE, s_channels[ch_idx].channel, 0);
        release_timer(s_channels[ch_idx].timer);
        s_channels[ch_idx].running = false;
        s_channels[ch_idx].timer = NULL;
    }

    xSemaphoreGive(s_mutex);
    ESP_LOGI(TAG, "deconfig: pin=%d", pin);
    return ESP_OK;
}

bool pwm_ctrl_is_running(int pin)
{
    if (!is_valid_pin(pin)) return false;
    ensure_mutex();
    xSemaphoreTake(s_mutex, portMAX_DELAY);
    bool running = (find_channel_by_pin(pin) >= 0);
    xSemaphoreGive(s_mutex);
    return running;
}
