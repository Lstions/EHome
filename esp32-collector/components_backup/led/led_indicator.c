/**
 * @file led_indicator.c
 * @brief RGB LED 状态指示器实现
 * 
 * 支持 WS2812 或简单 RGB LED。
 * 通过 FreeRTOS 任务驱动 LED 模式。
 */

#include "led_indicator.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "esp_log.h"

#define TAG "LED"

/* === 颜色定义 (RGB) === */
typedef struct {
    uint8_t r;
    uint8_t g;
    uint8_t b;
} rgb_color_t;

static const rgb_color_t COLOR_WHITE   = {255, 255, 255};
static const rgb_color_t COLOR_YELLOW  = {255, 255, 0};
static const rgb_color_t COLOR_BLUE    = {0, 0, 255};
static const rgb_color_t COLOR_RED     = {255, 0, 0};
static const rgb_color_t COLOR_CYAN    = {0, 255, 255};
static const rgb_color_t COLOR_ORANGE  = {255, 165, 0};
static const rgb_color_t COLOR_GREEN   = {0, 255, 0};
static const rgb_color_t COLOR_PURPLE  = {128, 0, 128};
static const rgb_color_t COLOR_BLACK   = {0, 0, 0};

/* === 状态配置 === */
typedef struct {
    rgb_color_t color;
    uint8_t     mode;       // 0=常亮, 1=呼吸, 2=慢闪, 3=快闪, 4=双闪, 5=三闪, 6=单闪
    uint16_t    period_ms;  // 周期
} led_state_config_t;

static const led_state_config_t s_state_configs[LED_STATE_COUNT] = {
    [LED_STATE_BOOTING]           = {COLOR_WHITE,  1, 2000},  // 白色呼吸
    [LED_STATE_WIFI_UNCONFIGURED] = {COLOR_YELLOW, 3, 500},   // 黄色快闪
    [LED_STATE_WIFI_CONNECTING]   = {COLOR_BLUE,   2, 1000},  // 蓝色慢闪
    [LED_STATE_WIFI_FAILED]       = {COLOR_RED,    4, 1000},  // 红色双闪
    [LED_STATE_MQTT_CONNECTING]   = {COLOR_CYAN,   2, 1000},  // 青色慢闪
    [LED_STATE_MQTT_FAILED]       = {COLOR_RED,    5, 1500},  // 红色三闪
    [LED_STATE_SERVER_OFFLINE]    = {COLOR_ORANGE, 2, 1000},  // 橙色慢闪
    [LED_STATE_NORMAL]            = {COLOR_GREEN,  0, 0},     // 绿色常亮
    [LED_STATE_OTA]               = {COLOR_PURPLE, 1, 2000},   // 紫色呼吸
    [LED_STATE_COLLECT_ERROR]     = {COLOR_YELLOW, 6, 1000},  // 黄色单闪
    [LED_STATE_FACTORY_RESET]     = {COLOR_RED,    3, 300},   // 红色快闪
};

/* === 全局状态 === */
static volatile led_state_t s_current_state = LED_STATE_BOOTING;
static volatile bool s_running = false;

/* === 内部函数 === */

static void set_led_color(const rgb_color_t *color)
{
    // TODO: 实际硬件驱动 (WS2812 或 GPIO PWM)
    // 这里仅打印日志，实际实现需要根据硬件连接
    ESP_LOGD(TAG, "LED color: R=%d G=%d B=%d", color->r, color->g, color->b);
}

static void led_breathe(const rgb_color_t *color, uint16_t period_ms)
{
    // 简化呼吸效果: 亮度从 0→100%→0%
    static uint32_t last_update = 0;
    uint32_t now = xTaskGetTickCount() * portTICK_PERIOD_MS;
    
    if (now - last_update < period_ms / 20) {
        return;
    }
    last_update = now;
    
    // 计算当前亮度 (正弦波)
    uint32_t phase = (now % period_ms) * 360 / period_ms;
    float brightness = (sinf(phase * 3.14159f / 180.0f) + 1.0f) / 2.0f;
    
    rgb_color_t c = {
        .r = (uint8_t)(color->r * brightness),
        .g = (uint8_t)(color->g * brightness),
        .b = (uint8_t)(color->b * brightness),
    };
    set_led_color(&c);
}

static void led_flash(const rgb_color_t *color, uint16_t period_ms, uint8_t flash_count)
{
    uint32_t now = xTaskGetTickCount() * portTICK_PERIOD_MS;
    uint32_t phase = now % period_ms;
    uint32_t flash_duration = period_ms / (flash_count * 2);
    
    bool on = false;
    for (uint8_t i = 0; i < flash_count; i++) {
        uint32_t flash_start = i * 2 * flash_duration;
        if (phase >= flash_start && phase < flash_start + flash_duration) {
            on = true;
            break;
        }
    }
    
    if (on) {
        set_led_color(color);
    } else {
        set_led_color(&COLOR_BLACK);
    }
}

/* === 公共函数 === */

void led_indicator_init(void)
{
    s_current_state = LED_STATE_BOOTING;
    s_running = true;
    ESP_LOGI(TAG, "LED indicator initialized");
}

void led_indicator_deinit(void)
{
    s_running = false;
    set_led_color(&COLOR_BLACK);
}

void led_indicator_set_state(led_state_t state)
{
    if (state < LED_STATE_COUNT && state != s_current_state) {
        s_current_state = state;
        ESP_LOGI(TAG, "LED state changed to %d", state);
    }
}

led_state_t led_indicator_get_state(void)
{
    return s_current_state;
}

void led_indicator_task(void *pvParameters)
{
    (void)pvParameters;
    
    led_indicator_init();
    
    while (s_running) {
        led_state_t state = s_current_state;
        const led_state_config_t *config = &s_state_configs[state];
        
        switch (config->mode) {
        case 0: // 常亮
            set_led_color(&config->color);
            break;
        case 1: // 呼吸
            led_breathe(&config->color, config->period_ms);
            break;
        case 2: // 慢闪
            led_flash(&config->color, config->period_ms, 1);
            break;
        case 3: // 快闪
            led_flash(&config->color, config->period_ms, 2);
            break;
        case 4: // 双闪
            led_flash(&config->color, config->period_ms, 2);
            break;
        case 5: // 三闪
            led_flash(&config->color, config->period_ms, 3);
            break;
        case 6: // 单闪
            led_flash(&config->color, config->period_ms, 1);
            break;
        default:
            set_led_color(&COLOR_BLACK);
            break;
        }
        
        vTaskDelay(pdMS_TO_TICKS(50));
    }
    
    vTaskDelete(NULL);
}
