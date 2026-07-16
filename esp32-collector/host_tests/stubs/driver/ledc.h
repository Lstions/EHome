#ifndef HOST_TEST_DRIVER_LEDC_H
#define HOST_TEST_DRIVER_LEDC_H

#include <stdint.h>
#include "esp_err.h"

#define SOC_LEDC_TIMER_NUM 4
#define SOC_LEDC_CHANNEL_NUM 6

typedef int ledc_mode_t;
typedef int ledc_timer_t;
typedef int ledc_channel_t;
typedef int ledc_timer_bit_t;

enum { LEDC_LOW_SPEED_MODE = 0, LEDC_AUTO_CLK = 0, LEDC_INTR_DISABLE = 0 };

typedef struct {
    ledc_mode_t speed_mode;
    ledc_timer_bit_t duty_resolution;
    ledc_timer_t timer_num;
    uint32_t freq_hz;
    int clk_cfg;
} ledc_timer_config_t;

typedef struct {
    ledc_channel_t channel;
    uint32_t duty;
    int gpio_num;
    ledc_mode_t speed_mode;
    ledc_timer_t timer_sel;
    int intr_type;
    struct { unsigned output_invert : 1; } flags;
} ledc_channel_config_t;

esp_err_t ledc_timer_config(const ledc_timer_config_t *config);
esp_err_t ledc_channel_config(const ledc_channel_config_t *config);
esp_err_t ledc_stop(ledc_mode_t speed_mode, ledc_channel_t channel, uint32_t idle_level);
esp_err_t ledc_set_duty(ledc_mode_t speed_mode, ledc_channel_t channel, uint32_t duty);
esp_err_t ledc_update_duty(ledc_mode_t speed_mode, ledc_channel_t channel);
uint32_t ledc_get_duty(ledc_mode_t speed_mode, ledc_channel_t channel);
esp_err_t ledc_timer_rst(ledc_mode_t speed_mode, ledc_timer_t timer);
esp_err_t ledc_timer_pause(ledc_mode_t speed_mode, ledc_timer_t timer);

#endif
