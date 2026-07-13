#include "log_capture.h"

#include <stdio.h>
#include <string.h>

static uint8_t bounded_level(uint8_t level)
{
    return level > LOG_CAPTURE_LEVEL_VERBOSE ? LOG_CAPTURE_LEVEL_VERBOSE : level;
}

void log_capture_init(log_capture_t *capture, log_capture_entry_t *entries,
                      size_t capacity, uint8_t level)
{
    if (capture == NULL) {
        return;
    }
    *capture = (log_capture_t){
        .entries = entries,
        .capacity = capacity,
        .level = ATOMIC_VAR_INIT(bounded_level(level)),
        .suppression = ATOMIC_VAR_INIT(0),
        .dropped_oldest = ATOMIC_VAR_INIT(0),
        .dropped_contention = ATOMIC_VAR_INIT(0),
        .lock = ATOMIC_FLAG_INIT,
    };
    atomic_flag_clear_explicit(&capture->lock, memory_order_release);
}

void log_capture_set_level(log_capture_t *capture, uint8_t level)
{
    if (capture != NULL) {
        atomic_store_explicit(&capture->level, bounded_level(level), memory_order_relaxed);
    }
}

bool log_capture_push_line(log_capture_t *capture, uint8_t level, uint64_t timestamp_us,
                           const char *tag, const char *message)
{
    if (capture == NULL || capture->entries == NULL || capture->capacity == 0 ||
        tag == NULL || message == NULL || level > LOG_CAPTURE_LEVEL_VERBOSE ||
        level > atomic_load_explicit(&capture->level, memory_order_relaxed) ||
        atomic_load_explicit(&capture->suppression, memory_order_relaxed) != 0) {
        return false;
    }
    if (atomic_flag_test_and_set_explicit(&capture->lock, memory_order_acquire)) {
        atomic_fetch_add_explicit(&capture->dropped_contention, 1, memory_order_relaxed);
        return false;
    }
    if (capture->entries == NULL || capture->capacity == 0 ||
        atomic_load_explicit(&capture->suppression, memory_order_relaxed) != 0 ||
        level > atomic_load_explicit(&capture->level, memory_order_relaxed)) {
        atomic_flag_clear_explicit(&capture->lock, memory_order_release);
        return false;
    }

    if (capture->count == capture->capacity) {
        capture->tail = (capture->tail + 1) % capture->capacity;
        capture->count--;
        atomic_fetch_add_explicit(&capture->dropped_oldest, 1, memory_order_relaxed);
    }

    log_capture_entry_t *entry = &capture->entries[capture->head];
    entry->timestamp_us = timestamp_us;
    entry->level = level;
    size_t tag_len = strnlen(tag, sizeof(entry->tag) - 1);
    memcpy(entry->tag, tag, tag_len);
    entry->tag[tag_len] = '\0';
    size_t message_len = strnlen(message, sizeof(entry->message) - 1);
    memcpy(entry->message, message, message_len);
    entry->message[message_len] = '\0';

    capture->head = (capture->head + 1) % capture->capacity;
    capture->count++;
    atomic_flag_clear_explicit(&capture->lock, memory_order_release);
    return true;
}

bool log_capture_pushv(log_capture_t *capture, uint8_t level, uint64_t timestamp_us,
                       const char *tag, const char *format, va_list args)
{
    if (capture == NULL || format == NULL ||
        level > atomic_load_explicit(&capture->level, memory_order_relaxed) ||
        atomic_load_explicit(&capture->suppression, memory_order_relaxed) != 0) {
        return false;
    }
    char message[LOG_CAPTURE_MESSAGE_MAX];
    va_list copy;
    va_copy(copy, args);
    int formatted = vsnprintf(message, sizeof(message), format, copy);
    va_end(copy);
    if (formatted < 0) {
        return false;
    }
    return log_capture_push_line(capture, level, timestamp_us, tag, message);
}

size_t log_capture_drain(log_capture_t *capture, log_capture_entry_t *out, size_t max_entries)
{
    if (capture == NULL || out == NULL || max_entries == 0 ||
        atomic_flag_test_and_set_explicit(&capture->lock, memory_order_acquire)) {
        return 0;
    }

    size_t count = 0;
    while (count < max_entries && capture->count > 0) {
        out[count++] = capture->entries[capture->tail];
        capture->tail = (capture->tail + 1) % capture->capacity;
        capture->count--;
    }
    atomic_flag_clear_explicit(&capture->lock, memory_order_release);
    return count;
}

void log_capture_suppress(log_capture_t *capture)
{
    if (capture != NULL) {
        atomic_fetch_add_explicit(&capture->suppression, 1, memory_order_relaxed);
    }
}

void log_capture_resume(log_capture_t *capture)
{
    if (capture == NULL) {
        return;
    }
    unsigned current = atomic_load_explicit(&capture->suppression, memory_order_relaxed);
    while (current > 0 && !atomic_compare_exchange_weak_explicit(
               &capture->suppression, &current, current - 1,
               memory_order_relaxed, memory_order_relaxed)) {
    }
}

unsigned log_capture_dropped_oldest(const log_capture_t *capture)
{
    return capture == NULL ? 0 :
        atomic_load_explicit(&capture->dropped_oldest, memory_order_relaxed);
}

unsigned log_capture_dropped_contention(const log_capture_t *capture)
{
    return capture == NULL ? 0 :
        atomic_load_explicit(&capture->dropped_contention, memory_order_relaxed);
}
