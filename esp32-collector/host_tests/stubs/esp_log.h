#ifndef HOST_TEST_ESP_LOG_H
#define HOST_TEST_ESP_LOG_H

void host_test_log_record(char level, const char *tag, const char *format, ...);

#define ESP_LOGE(tag, format, ...) host_test_log_record('E', tag, format, ##__VA_ARGS__)
#define ESP_LOGW(tag, format, ...) host_test_log_record('W', tag, format, ##__VA_ARGS__)
#define ESP_LOGI(tag, format, ...) host_test_log_record('I', tag, format, ##__VA_ARGS__)
#define ESP_LOGD(tag, format, ...) host_test_log_record('D', tag, format, ##__VA_ARGS__)
#endif