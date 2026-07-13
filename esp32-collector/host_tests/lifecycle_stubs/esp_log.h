#ifndef HOST_LIFECYCLE_ESP_LOG_H
#define HOST_LIFECYCLE_ESP_LOG_H
#define ESP_LOGE(tag, format, ...) host_log_record('E', (tag), (format), ##__VA_ARGS__)
#define ESP_LOGW(tag, format, ...) host_log_record('W', (tag), (format), ##__VA_ARGS__)
#define ESP_LOGI(tag, format, ...) host_log_record('I', (tag), (format), ##__VA_ARGS__)
void host_log_record(char level, const char *tag, const char *format, ...);
#endif
