#ifndef HOST_TEST_ESP_LOG_H
#define HOST_TEST_ESP_LOG_H

#include <stdarg.h>
#include <stdint.h>

typedef enum {
    ESP_LOG_NONE = 0,
    ESP_LOG_ERROR = 1,
    ESP_LOG_WARN = 2,
    ESP_LOG_INFO = 3,
    ESP_LOG_DEBUG = 4,
    ESP_LOG_VERBOSE = 5,
    ESP_LOG_MAX = 6,
} esp_log_level_t;

#define ESP_LOG_LEVEL_LEN 3

typedef struct {
    union {
        struct {
            esp_log_level_t log_level : ESP_LOG_LEVEL_LEN;
            uint32_t constrained_env : 1;
            uint32_t require_formatting : 1;
            uint32_t dis_color : 1;
            uint32_t dis_timestamp : 1;
            uint32_t binary_mode : 1;
            uint32_t reserved : 24;
        } opts;
        uint32_t data;
    };
} esp_log_config_t;

void esp_log_va(esp_log_config_t config, const char *tag, const char *format, va_list args);

#endif
