#ifndef HOST_TEST_LOG_CAPTURE_ESP_TEST_H
#define HOST_TEST_LOG_CAPTURE_ESP_TEST_H

#include <stdarg.h>
#include <stddef.h>

int log_capture_esp_test_vsnprintf(char *buffer, size_t capacity,
                                   const char *format, va_list args);

#endif