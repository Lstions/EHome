#ifndef ESP_ERR_H
#define ESP_ERR_H

typedef int esp_err_t;

#define ESP_OK                 0
#define ESP_FAIL              -1
#define ESP_ERR_NO_MEM         0x101
#define ESP_ERR_INVALID_ARG    0x102
#define ESP_ERR_INVALID_STATE  0x103
#define ESP_ERR_INVALID_SIZE   0x104
#define ESP_ERR_NOT_FOUND      0x105
#define ESP_ERR_NOT_SUPPORTED  0x106
#define ESP_ERR_TIMEOUT        0x107
#define ESP_ERR_PIN_CONFLICT   0x7101
/* ESP-IDF: ESP_ERR_NVS_BASE(0x1100) + 0x0A。桩里必须给出**真实数值**，
 * 否则"命名空间不存在"这条分支在宿主侧永远走不到，测试就成了假绿。 */
#define ESP_ERR_NVS_NOT_FOUND  0x110A

const char *esp_err_to_name(esp_err_t err);

#endif
