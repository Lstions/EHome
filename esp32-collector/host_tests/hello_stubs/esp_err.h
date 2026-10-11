#ifndef HELLO_HOST_ESP_ERR_H
#define HELLO_HOST_ESP_ERR_H
typedef int esp_err_t;
#define ESP_OK 0
#define ESP_FAIL (-1)

/* 以下为 2026-10-06 新增（D-09 需要宿主编译 components/transport/transport.c）。
 * 只做【加法】：不改变已有符号的取值，故对既有用例零影响。
 * 取值沿用 ESP-IDF 的真实约定（0x101/0x102/0x103/0x104/0x105...），
 * 但宿主测试不应依赖具体数值 —— 只应做相等性比较。 */
#define ESP_ERR_NO_MEM 0x101
#define ESP_ERR_INVALID_ARG 0x102
#define ESP_ERR_INVALID_STATE 0x103
#define ESP_ERR_INVALID_SIZE 0x104
#define ESP_ERR_NOT_FOUND 0x105
#define ESP_ERR_NOT_SUPPORTED 0x106
#define ESP_ERR_TIMEOUT 0x107
#endif
