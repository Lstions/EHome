/**
 * @file log_stream.h
 * @brief Safe structured ESP32 diagnostic log stream.
 *
 * Native ESP-IDF v6 esp_log calls are captured before UART formatting through a
 * link wrapper, while preserving the original ESP-IDF backend. Explicit
 * log_stream_emit() diagnostics and native logs share one bounded, non-blocking
 * capture ring. Disabled mode runs no TX task, emits no stream traffic, and
 * captures no entries; fixed ring/TX/control storage remains statically resident.
 */
#ifndef LOG_STREAM_H
#define LOG_STREAM_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

#define LOG_LEVEL_ERROR   0
#define LOG_LEVEL_WARN    1
#define LOG_LEVEL_INFO    2
#define LOG_LEVEL_DEBUG   3
#define LOG_LEVEL_VERBOSE 4

typedef void (*log_stream_publish_fn_t)(const uint8_t *data, size_t len);

void log_stream_start(uint8_t level);
void log_stream_stop(void);
void log_stream_set_level(uint8_t level);
bool log_stream_is_active(void);

/* Injected by the application layer to avoid a component dependency cycle. */
void log_stream_set_publish_callback(log_stream_publish_fn_t publish);

/**
 * Emits a remote-only structured diagnostic. Level is a verbosity threshold:
 * ERROR(0) is always eligible; WARN/INFO/DEBUG/VERBOSE require a threshold at
 * least as high as their numeric value. This does not mutate ESP-IDF log levels.
 */
void log_stream_emit(uint8_t level, const char *tag, const char *fmt, ...)
    __attribute__((format(printf, 3, 4)));

#define LOG_STREAM_E(tag, fmt, ...) log_stream_emit(LOG_LEVEL_ERROR, tag, fmt, ##__VA_ARGS__)
#define LOG_STREAM_W(tag, fmt, ...) log_stream_emit(LOG_LEVEL_WARN, tag, fmt, ##__VA_ARGS__)
#define LOG_STREAM_I(tag, fmt, ...) log_stream_emit(LOG_LEVEL_INFO, tag, fmt, ##__VA_ARGS__)
#define LOG_STREAM_D(tag, fmt, ...) log_stream_emit(LOG_LEVEL_DEBUG, tag, fmt, ##__VA_ARGS__)
#define LOG_STREAM_V(tag, fmt, ...) log_stream_emit(LOG_LEVEL_VERBOSE, tag, fmt, ##__VA_ARGS__)

#ifdef __cplusplus
}
#endif
#endif /* LOG_STREAM_H */
