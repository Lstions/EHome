/**
 * @file led_indicator.h
 * @brief RGB LED 状态指示器
 * 
 * 通过板载 RGB LED (如 WS2812) 展示节点运行状态。
 * 编译时可选开启（Kconfig CONFIG_COLLECTOR_RGB_LED）。
 */

#ifndef LED_INDICATOR_H
#define LED_INDICATOR_H

#include <stdint.h>
#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === LED 状态枚举 === */
typedef enum {
    LED_STATE_BOOTING,              // 启动中 - 白色呼吸
    LED_STATE_WIFI_UNCONFIGURED,    // WiFi 未配置 - 黄色快闪
    LED_STATE_WIFI_CONNECTING,      // WiFi 连接中 - 蓝色慢闪
    LED_STATE_WIFI_FAILED,          // WiFi 连接失败 - 红色双闪
    LED_STATE_MQTT_CONNECTING,      // MQTT 连接中 - 青色慢闪
    LED_STATE_MQTT_FAILED,          // MQTT 连接失败 - 红色三闪
    LED_STATE_SERVER_OFFLINE,       // Server 未响应 - 橙色慢闪
    LED_STATE_NORMAL,               // 正常运行 - 绿色常亮
    LED_STATE_OTA,                  // OTA 升级中 - 紫色呼吸
    LED_STATE_COLLECT_ERROR,        // 采集错误 - 黄色单闪
    LED_STATE_FACTORY_RESET,        // 恢复出厂中 - 红色快闪
    LED_STATE_COUNT
} led_state_t;

/* === 初始化 === */
void led_indicator_init(void);
void led_indicator_deinit(void);

/* === 设置状态 === */
void led_indicator_set_state(led_state_t state);

/* === 当前状态查询 === */
led_state_t led_indicator_get_state(void);

/* === 任务 (需要在 FreeRTOS 中运行) === */
void led_indicator_task(void *pvParameters);

#ifdef __cplusplus
}
#endif

#endif /* LED_INDICATOR_H */
