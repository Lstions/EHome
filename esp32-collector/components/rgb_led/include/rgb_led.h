/**
 * @file rgb_led.h
 * @brief RGB LED 状态指示驱动 (WS2812 on GPIO8)
 *
 * 功能：
 * 1. WS2812 驱动 (RMT)
 * 11 种状态模式 (常亮/快闪/慢闪/呼吸等)
 * Kconfig 可选编译 (CONFIG_COLLECTOR_RGB_LED)
 */

#pragma once

#include "esp_err.h"
#include <stdint.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

/**
 * @brief LED 状态定义
 */
typedef enum {
    LED_STATE_BOOTING = 0,      // 白色呼吸 - 启动中
    LED_STATE_WIFI_NO_CREDS,    // 黄色快闪 - WiFi未配置
    LED_STATE_WIFI_CONNECTING,  // 蓝色慢闪 - WiFi连接中
    LED_STATE_WIFI_FAILED,      // 红色双闪 - WiFi连接失败
    LED_STATE_MQTT_CONNECTING,  // 青色慢闪 - MQTT连接中
    LED_STATE_MQTT_FAILED,      // 红色三闪 - MQTT连接失败
    LED_STATE_SERVER_OFFLINE,   // 橙色慢闪 - Server未响应
    LED_STATE_RUNNING,          // 绿色常亮 - 正常运行
    LED_STATE_OTA,              // 紫色呼吸 - OTA升级中
    LED_STATE_COLLECT_ERROR,    // 黄色单闪 - 采集错误
    LED_STATE_FACTORY_RESET,    // 红色快闪 - 恢复出厂中
    LED_STATE_MAX
} led_state_t;

/**
 * @brief LED 颜色定义 (GRB for WS2812)
 */
typedef struct {
    uint8_t g;
    uint8_t r;
    uint8_t b;
} led_color_t;

/**
 * @brief 初始化 RGB LED
 * @param gpio_pin GPIO 引脚号 (默认 8 = ESP32-S3 板载 LED)
 * @return ESP_OK on success
 */
esp_err_t rgb_led_init(int gpio_pin);

/**
 * @brief 设置 LED 状态
 * @param state LED 状态
 */
void rgb_led_set_state(led_state_t state);

/**
 * @brief 获取当前 LED 状态
 * @return 当前状态
 */
led_state_t rgb_led_get_state(void);

/**
 * @brief 设置 LED 状态
 * @param state LED 状态
 * @note 无优先级阻断，由调用方控制流程顺序
 */
void rgb_led_set_state(led_state_t state);

/**
 * @brief 强制设置 LED 状态 (等同于 set_state，保留兼容)
 * @param state LED 状态
 */
void rgb_led_set_state_force(led_state_t state);

/**
 * @brief 设置 LED 颜色 (直接控制, 不使用状态机)
 * @param r 红色 (0-255)
 * @param g 绿色 (0-255)
 * @param b 蓝色 (0-255)
 */
void rgb_led_set_color(uint8_t r, uint8_t g, uint8_t b);

/**
 * @brief 关闭 LED
 */
void rgb_led_off(void);

/**
 * @brief 启动 LED 状态机任务
 * @return ESP_OK on success
 */
esp_err_t rgb_led_start(void);

/**
 * @brief 停止 LED 状态机任务
 */
void rgb_led_stop(void);

#ifdef __cplusplus
}
#endif
