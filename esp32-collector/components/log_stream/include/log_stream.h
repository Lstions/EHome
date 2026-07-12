/**
 * @file log_stream.h
 * @brief ESP32 System Log Stream — vprintf hook + ring buffer + MQTT batch publish
 *
 * Default: OFF, zero overhead (no task, no buffer, no hook).
 * When enabled via config manifest (log_stream.enabled=true):
 *   - Installs vprintf hook to capture all ESP_LOGx output
 *   - Ring buffer accumulates log lines (4KB, mutex-protected)
 *   - log_tx_task batches lines into MsgLogStream (0x1D) frames every 100ms
 *
 * ESP32 is a pure producer — it does not know about downstream consumers.
 */

#ifndef LOG_STREAM_H
#define LOG_STREAM_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* Log levels (matching esp_log_level_t) */
#define LOG_LEVEL_ERROR   0
#define LOG_LEVEL_WARN    1
#define LOG_LEVEL_INFO    2
#define LOG_LEVEL_DEBUG   3
#define LOG_LEVEL_VERBOSE 4

/**
 * @brief Start log stream capture.
 * @param level  Minimum log level (0=ERROR ... 4=VERBOSE)
 *
 * Allocates ring buffer, installs vprintf hook, creates log_tx_task.
 * Idempotent: calling when already started just updates level.
 */
void log_stream_start(uint8_t level);

/**
 * @brief Stop log stream capture.
 *
 * Restores original vprintf, deletes task, frees buffer.
 * Idempotent: calling when already stopped is a no-op.
 */
void log_stream_stop(void);

/**
 * @brief Update log level at runtime (no restart).
 * @param level  New minimum log level
 */
void log_stream_set_level(uint8_t level);

/**
 * @brief Check if log stream is currently active.
 */
bool log_stream_is_active(void);

/**
 * @brief Emit a structured system log entry to the remote stream.
 * Safe to call from normal task context; non-blocking and drops on contention.
 * @param level  LOG_LEVEL_ERROR..LOG_LEVEL_VERBOSE
 * @param tag    short component tag
 * @param fmt    printf-style message format
 */
void log_stream_emit(uint8_t level, const char *tag, const char *fmt, ...) __attribute__((format(printf, 3, 4)));

#ifdef __cplusplus
}
#endif

#endif /* LOG_STREAM_H */
