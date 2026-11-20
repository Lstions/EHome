#ifndef HOST_TEST_ESP_ADC_ONESHOT_H
#define HOST_TEST_ESP_ADC_ONESHOT_H

#include <stdint.h>
#include "esp_err.h"

typedef void *adc_oneshot_unit_handle_t;
typedef int adc_unit_t;
typedef int adc_channel_t;
typedef int adc_atten_t;
typedef int adc_bitwidth_t;

enum {
    ADC_UNIT_1 = 0,
    ADC_UNIT_2 = 1,
    ADC_ATTEN_DB_12 = 3,
    ADC_BITWIDTH_9 = 9,
    ADC_BITWIDTH_12 = 12,
    ADC_ULP_MODE_DISABLE = 0,
};

typedef struct {
    adc_unit_t unit_id;
    int ulp_mode;
} adc_oneshot_unit_init_cfg_t;

typedef struct {
    adc_atten_t atten;
    adc_bitwidth_t bitwidth;
} adc_oneshot_chan_cfg_t;

esp_err_t adc_oneshot_new_unit(const adc_oneshot_unit_init_cfg_t *cfg,
                               adc_oneshot_unit_handle_t *out_handle);
esp_err_t adc_oneshot_del_unit(adc_oneshot_unit_handle_t handle);
esp_err_t adc_oneshot_config_channel(adc_oneshot_unit_handle_t handle,
                                     adc_channel_t channel,
                                     const adc_oneshot_chan_cfg_t *cfg);
esp_err_t adc_oneshot_read(adc_oneshot_unit_handle_t handle,
                           adc_channel_t channel, int *out_raw);

#endif
