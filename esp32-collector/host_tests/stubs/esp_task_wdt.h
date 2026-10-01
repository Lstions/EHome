#ifndef ESP_TASK_WDT_H
#define ESP_TASK_WDT_H

#include "esp_err.h"

/* 返回类型必须与真实 IDF 一致：这三个函数都返回 esp_err_t。
 * 曾写成 void —— 与 uart_flush_input（stubs/driver/uart.h）同一类漂移。
 * 当前未致构建失败（生产侧调用均未使用返回值），但一旦有人写
 * `if (esp_task_wdt_add(NULL) != ESP_OK)`，host 构建就会报
 * "void value not ignored"，且真机与 host 行为分歧。这里一并对齐，
 * 避免同类问题再次潜伏。 */
static inline esp_err_t esp_task_wdt_add(void *handle) { (void)handle; return ESP_OK; }
static inline esp_err_t esp_task_wdt_reset(void) { return ESP_OK; }
static inline esp_err_t esp_task_wdt_delete(void *handle) { (void)handle; return ESP_OK; }

#endif
