#ifndef LOG_CAPTURE_H
#define LOG_CAPTURE_H

#include <stdarg.h>
#include <stdatomic.h>
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

enum {
    LOG_CAPTURE_LEVEL_ERROR = 0,
    LOG_CAPTURE_LEVEL_WARN = 1,
    LOG_CAPTURE_LEVEL_INFO = 2,
    LOG_CAPTURE_LEVEL_DEBUG = 3,
    LOG_CAPTURE_LEVEL_VERBOSE = 4,
};

#define LOG_CAPTURE_TAG_MAX 24
#define LOG_CAPTURE_MESSAGE_MAX 96

typedef struct {
    uint64_t timestamp_us;
    uint8_t level;
    char tag[LOG_CAPTURE_TAG_MAX];
    char message[LOG_CAPTURE_MESSAGE_MAX];
} log_capture_entry_t;

typedef struct {
    log_capture_entry_t *entries;
    size_t capacity;
    size_t head;
    size_t tail;
    size_t count;
    atomic_uint level;
    atomic_uint suppression;
    atomic_uint dropped_oldest;
    atomic_uint dropped_contention;
    atomic_flag lock;
} log_capture_t;

void log_capture_init(log_capture_t *capture, log_capture_entry_t *entries,
                      size_t capacity, uint8_t level);
void log_capture_set_level(log_capture_t *capture, uint8_t level);
bool log_capture_push_line(log_capture_t *capture, uint8_t level, uint64_t timestamp_us,
                           const char *tag, const char *message);
bool log_capture_pushv(log_capture_t *capture, uint8_t level, uint64_t timestamp_us,
                       const char *tag, const char *format, va_list args);
size_t log_capture_drain(log_capture_t *capture, log_capture_entry_t *out, size_t max_entries);
void log_capture_suppress(log_capture_t *capture);
void log_capture_resume(log_capture_t *capture);
unsigned log_capture_dropped_oldest(const log_capture_t *capture);
unsigned log_capture_dropped_contention(const log_capture_t *capture);

#ifdef __cplusplus
}
#endif
#endif
