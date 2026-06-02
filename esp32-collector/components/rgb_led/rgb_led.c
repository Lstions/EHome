/**
 * @file rgb_led.c
 * @brief RGB LED 状态指示驱动实现 (WS2812 on GPIO8)
 */

#include <string.h>
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "driver/rmt_tx.h"
#include "esp_log.h"
#include "esp_check.h"
#include "rgb_led.h"

static const char *TAG = "rgb_led";

// WS2812 timing (in nanoseconds, converted to RMT ticks at 10MHz)
#define WS2812_T0H_NS   350   // 0 code high time
#define WS2812_T0L_NS   800   // 0 code low time
#define WS2812_T1H_NS   700   // 1 code high time
#define WS2812_T1L_NS   600   // 1 code low time
#define WS2812_RESET_NS 280000 // reset time

// RMT resolution (100ns per tick)
#define RMT_RESOLUTION_HZ  10000000  // 10MHz = 100ns per tick

// Static variables
static rmt_channel_handle_t s_rmt_chan = NULL;
static rmt_encoder_handle_t s_encoder = NULL;
static led_state_t s_current_state = LED_STATE_BOOTING;
static TaskHandle_t s_task_handle = NULL;
static int s_gpio_pin = 8;  // Default: ESP32-S3 built-in RGB LED

// LED state colors (GRB format for WS2812)
static const led_color_t s_state_colors[LED_STATE_MAX] = {
    [LED_STATE_BOOTING]         = {255, 255, 255},  // White
    [LED_STATE_WIFI_NO_CREDS]   = {255, 255, 0},    // Yellow
    [LED_STATE_WIFI_CONNECTING]  = {0, 0, 255},      // Blue
    [LED_STATE_WIFI_FAILED]      = {0, 255, 0},      // Red
    [LED_STATE_MQTT_CONNECTING]  = {255, 0, 255},    // Cyan
    [LED_STATE_MQTT_FAILED]      = {0, 255, 0},      // Red
    [LED_STATE_SERVER_OFFLINE]   = {165, 255, 0},    // Orange
    [LED_STATE_RUNNING]          = {255, 0, 0},      // Green
    [LED_STATE_OTA]              = {0, 255, 255},    // Purple
    [LED_STATE_COLLECT_ERROR]    = {255, 255, 0},    // Yellow
    [LED_STATE_FACTORY_RESET]    = {0, 255, 0},      // Red
};

// LED state modes
typedef enum {
    MODE_OFF = 0,
    MODE_SOLID,         // 常亮
    MODE_SLOW_BLINK,    // 慢闪 (1s on, 1s off)
    MODE_FAST_BLINK,    // 快闪 (200ms on, 200ms off)
    MODE_DOUBLE_BLINK,  // 双闪 (200ms on, 100ms off, 200ms on, 500ms off)
    MODE_TRIPLE_BLINK,  // 三闪 (200ms on, 100ms off, 200ms on, 100ms off, 200ms on, 500ms off)
    MODE_BREATHING,     // 呼吸
} led_mode_t;

// 状态分组:
//   系统状态: BOOTING, FACTORY_RESET, OTA — 始终接受
//   连接状态: WIFI_*, MQTT_* — 始终接受 (流程推进)
//   运行状态: RUNNING, COLLECT_ERROR, SERVER_OFFLINE — 始终接受
// 不做优先级阻断 — 全部由调用方控制流程顺序

static const led_mode_t s_state_modes[LED_STATE_MAX] = {
    [LED_STATE_BOOTING]         = MODE_BREATHING,
    [LED_STATE_WIFI_NO_CREDS]   = MODE_FAST_BLINK,
    [LED_STATE_WIFI_CONNECTING]  = MODE_SLOW_BLINK,
    [LED_STATE_WIFI_FAILED]      = MODE_DOUBLE_BLINK,
    [LED_STATE_MQTT_CONNECTING]  = MODE_SLOW_BLINK,
    [LED_STATE_MQTT_FAILED]      = MODE_TRIPLE_BLINK,
    [LED_STATE_SERVER_OFFLINE]   = MODE_SLOW_BLINK,
    [LED_STATE_RUNNING]          = MODE_SOLID,
    [LED_STATE_OTA]              = MODE_BREATHING,
    [LED_STATE_COLLECT_ERROR]    = MODE_FAST_BLINK,
    [LED_STATE_FACTORY_RESET]    = MODE_FAST_BLINK,
};

/**
 * @brief WS2812 encoder (simple bit-bang approach using RMT)
 */
static size_t ws2812_encode(rmt_encoder_t *encoder, rmt_channel_handle_t channel,
                            const void *data, size_t data_size,
                            rmt_encode_state_t *ret_state)
{
    // Simple approach: use the built-in RMT encoder
    // For now, we'll use a direct GPIO approach instead
    *ret_state = RMT_ENCODING_COMPLETE;
    return 0;
}

/**
 * @brief Set LED color using RMT
 */
static void set_led_color_rmt(uint8_t r, uint8_t g, uint8_t b)
{
    if (!s_rmt_chan || !s_encoder) return;

    // WS2812 expects GRB order
    uint8_t grb[3] = {g, r, b};

    // Transmit using bytes encoder
    rmt_transmit_config_t tx_config = {
        .loop_count = 0,
    };
    esp_err_t err = rmt_transmit(s_rmt_chan, s_encoder, grb, sizeof(grb), &tx_config);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "RMT transmit failed: %s", esp_err_to_name(err));
    }

    // Wait for transmission to complete
    rmt_tx_wait_all_done(s_rmt_chan, 100);
}

/**
 * @brief Set LED color using simple GPIO (fallback)
 */
static void set_led_color_gpio(uint8_t r, uint8_t g, uint8_t b)
{
    // For WS2812, we need precise timing
    // Use RMT if available, otherwise skip
    if (s_rmt_chan) {
        set_led_color_rmt(r, g, b);
    }
}

esp_err_t rgb_led_init(int gpio_pin)
{
    if (gpio_pin >= 0) {
        s_gpio_pin = gpio_pin;
    }

    ESP_LOGI(TAG, "Initializing RGB LED on GPIO%d", s_gpio_pin);

    // Create RMT TX channel
    rmt_tx_channel_config_t tx_chan_config = {
        .gpio_num = s_gpio_pin,
        .clk_src = RMT_CLK_SRC_DEFAULT,
        .resolution_hz = RMT_RESOLUTION_HZ,
        .mem_block_symbols = 64,
        .trans_queue_depth = 4,
    };
    esp_err_t err = rmt_new_tx_channel(&tx_chan_config, &s_rmt_chan);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to create RMT TX channel: %s", esp_err_to_name(err));
        return err;
    }

    // Create bytes encoder
    rmt_bytes_encoder_config_t enc_config = {
        .bit0 = {
            .level0 = 1,
            .duration0 = 4,   // 350ns / 100ns
            .level1 = 0,
            .duration1 = 8,   // 800ns / 100ns
        },
        .bit1 = {
            .level0 = 1,
            .duration0 = 8,   // 700ns / 100ns
            .level1 = 0,
            .duration1 = 4,   // 600ns / 100ns
        },
        .flags.msb_first = 1,
    };
    err = rmt_new_bytes_encoder(&enc_config, &s_encoder);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to create bytes encoder: %s", esp_err_to_name(err));
        return err;
    }

    // Enable the channel
    err = rmt_enable(s_rmt_chan);
    if (err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to enable RMT channel: %s", esp_err_to_name(err));
        return err;
    }

    ESP_LOGI(TAG, "RGB LED initialized on GPIO%d", s_gpio_pin);
    return ESP_OK;
}

void rgb_led_set_state(led_state_t state)
{
    if (state >= LED_STATE_MAX) {
        ESP_LOGE(TAG, "Invalid LED state: %d", state);
        return;
    }

    if (state == s_current_state) {
        return;  // 相同状态，不重复日志
    }

    ESP_LOGI(TAG, "LED state: %d -> %d", s_current_state, state);
    s_current_state = state;
}

void rgb_led_set_state_force(led_state_t state)
{
    // 与 set_state 相同 (优先级已移除)
    rgb_led_set_state(state);
}

led_state_t rgb_led_get_state(void)
{
    return s_current_state;
}

void rgb_led_set_color(uint8_t r, uint8_t g, uint8_t b)
{
    set_led_color_gpio(r, g, b);
}

void rgb_led_off(void)
{
    set_led_color_gpio(0, 0, 0);
}

/**
 * @brief LED state machine task
 */
static void rgb_led_task(void *arg)
{
    uint32_t tick = 0;
    uint8_t brightness = 0;
    int8_t breath_dir = 1;

    ESP_LOGI(TAG, "LED task started");

    while (1) {
        led_state_t state = s_current_state;
        led_mode_t mode = s_state_modes[state];
        led_color_t color = s_state_colors[state];

        switch (mode) {
        case MODE_OFF:
            rgb_led_off();
            vTaskDelay(pdMS_TO_TICKS(100));
            break;

        case MODE_SOLID:
            rgb_led_set_color(color.r, color.g, color.b);
            vTaskDelay(pdMS_TO_TICKS(100));
            break;

        case MODE_SLOW_BLINK:
            if (tick % 20 < 10) {
                rgb_led_set_color(color.r, color.g, color.b);
            } else {
                rgb_led_off();
            }
            vTaskDelay(pdMS_TO_TICKS(100));
            break;

        case MODE_FAST_BLINK:
            if (tick % 4 < 2) {
                rgb_led_set_color(color.r, color.g, color.b);
            } else {
                rgb_led_off();
            }
            vTaskDelay(pdMS_TO_TICKS(100));
            break;

        case MODE_DOUBLE_BLINK:
            {
                uint32_t phase = tick % 12;
                if (phase < 2 || (phase >= 3 && phase < 5)) {
                    rgb_led_set_color(color.r, color.g, color.b);
                } else {
                    rgb_led_off();
                }
            }
            vTaskDelay(pdMS_TO_TICKS(100));
            break;

        case MODE_TRIPLE_BLINK:
            {
                uint32_t phase = tick % 16;
                if (phase < 2 || (phase >= 3 && phase < 5) || (phase >= 6 && phase < 8)) {
                    rgb_led_set_color(color.r, color.g, color.b);
                } else {
                    rgb_led_off();
                }
            }
            vTaskDelay(pdMS_TO_TICKS(100));
            break;

        case MODE_BREATHING:
            brightness += breath_dir * 5;
            if (brightness >= 250) breath_dir = -1;
            if (brightness <= 5) breath_dir = 1;
            rgb_led_set_color(
                (color.r * brightness) / 255,
                (color.g * brightness) / 255,
                (color.b * brightness) / 255
            );
            vTaskDelay(pdMS_TO_TICKS(20));
            break;

        default:
            rgb_led_off();
            vTaskDelay(pdMS_TO_TICKS(100));
            break;
        }

        tick++;
    }
}

esp_err_t rgb_led_start(void)
{
    if (s_task_handle != NULL) {
        ESP_LOGW(TAG, "LED task already running");
        return ESP_ERR_INVALID_STATE;
    }

    BaseType_t ret = xTaskCreate(
        rgb_led_task,
        "rgb_led",
        2048,
        NULL,
        3,  // Low priority
        &s_task_handle
    );

    if (ret != pdPASS) {
        ESP_LOGE(TAG, "Failed to create LED task");
        return ESP_ERR_NO_MEM;
    }

    ESP_LOGI(TAG, "LED state machine started");
    return ESP_OK;
}

void rgb_led_stop(void)
{
    if (s_task_handle != NULL) {
        vTaskDelete(s_task_handle);
        s_task_handle = NULL;
        rgb_led_off();
        ESP_LOGI(TAG, "LED task stopped");
    }
}
