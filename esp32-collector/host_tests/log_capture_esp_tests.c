#include <stdarg.h>
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "log_capture.h"
#include "log_capture_esp.h"

void __wrap_esp_log(esp_log_config_t config, const char *tag, const char *format, ...);

static int s_failures;
static unsigned s_forward_count;
static unsigned s_format_side_effects;
static unsigned s_vsnprintf_count;
static char s_forwarded_message[LOG_CAPTURE_MESSAGE_MAX];
static const char *s_forwarded_tag;
static esp_log_config_t s_forwarded_config;
static int64_t s_timer_us;
static bool s_runtime_constrained;
static unsigned s_runtime_constraint_checks;
static bool s_detach_during_constraint_check;
static bool s_constraint_check_detach_result;
static bool s_isr_context;
static TickType_t s_ticks;
static unsigned s_yields;
static unsigned s_delays;
static log_capture_t *s_reentrant_capture;

int log_capture_esp_test_vsnprintf(char *buffer, size_t capacity,
                                   const char *format, va_list args)
{
    s_vsnprintf_count++;
    return vsnprintf(buffer, capacity, format, args);
}

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (message)); \
        s_failures++; \
        return; \
    } \
} while (0)

static esp_log_config_t make_config(esp_log_level_t level)
{
    esp_log_config_t config = { .data = 0 };
    config.opts.log_level = level;
    return config;
}

static void reset_stubs(void)
{
    s_forward_count = 0;
    s_format_side_effects = 0;
    s_vsnprintf_count = 0;
    s_forwarded_message[0] = '\0';
    s_forwarded_tag = NULL;
    s_forwarded_config.data = 0;
    s_timer_us = 123456789;
    s_runtime_constrained = false;
    s_runtime_constraint_checks = 0;
    s_detach_during_constraint_check = false;
    s_constraint_check_detach_result = false;
    s_isr_context = false;
    s_ticks = 0;
    s_yields = 0;
    s_delays = 0;
    s_reentrant_capture = NULL;
    CHECK(log_capture_esp_detach(), "test reset detach failed");
}

void esp_log_va(esp_log_config_t config, const char *tag, const char *format, va_list args)
{
    s_forward_count++;
    s_forwarded_config = config;
    s_forwarded_tag = tag;
    (void)vsnprintf(s_forwarded_message, sizeof(s_forwarded_message), format, args);
    if (s_reentrant_capture != NULL && s_forward_count == 1) {
        __wrap_esp_log(make_config(ESP_LOG_WARN), "INNER", "inner=%d", 9);
    }
}

bool esp_log_util_is_constrained(void)
{
    s_runtime_constraint_checks++;
    if (s_detach_during_constraint_check) {
        s_detach_during_constraint_check = false;
        s_constraint_check_detach_result = log_capture_esp_detach();
    }
    return s_runtime_constrained;
}

int64_t esp_timer_get_time(void)
{
    return s_timer_us;
}

BaseType_t xPortInIsrContext(void)
{
    return s_isr_context ? 1 : 0;
}

TickType_t xTaskGetTickCount(void)
{
    return s_ticks;
}

void host_task_yield(void)
{
    s_yields++;
}

void vTaskDelay(TickType_t ticks)
{
    s_delays++;
    s_ticks += ticks;
}

static int formatting_probe(void)
{
    s_format_side_effects++;
    return 77;
}

static size_t drain_all(log_capture_t *capture, log_capture_entry_t *entries, size_t capacity)
{
    return log_capture_drain(capture, entries, capacity);
}

static void test_maps_all_levels_and_captures_plain_body(void)
{
    reset_stubs();
    log_capture_entry_t storage[5];
    log_capture_entry_t entries[5];
    log_capture_t capture;
    log_capture_init(&capture, storage, 5, LOG_CAPTURE_LEVEL_VERBOSE);
    log_capture_esp_attach(&capture);

    const esp_log_level_t esp_levels[] = {
        ESP_LOG_ERROR, ESP_LOG_WARN, ESP_LOG_INFO, ESP_LOG_DEBUG, ESP_LOG_VERBOSE,
    };
    for (size_t i = 0; i < 5; ++i) {
        s_timer_us = (int64_t)(1000 + i);
        __wrap_esp_log(make_config(esp_levels[i]), "MAP", "body-%u", (unsigned)i);
    }

    CHECK(s_forward_count == 5, "every wrapped call must retain original forwarding");
    CHECK(s_vsnprintf_count == 5,
          "each eligible call must format exactly once through the capture test seam");
    CHECK(drain_all(&capture, entries, 5) == 5, "all E/W/I/D/V entries must be captured");
    for (size_t i = 0; i < 5; ++i) {
        char expected[16];
        (void)snprintf(expected, sizeof(expected), "body-%u", (unsigned)i);
        CHECK(entries[i].level == i, "ESP level mapping is wrong");
        CHECK(entries[i].timestamp_us == 1000 + i, "timer stub value was not captured");
        CHECK(strcmp(entries[i].tag, "MAP") == 0, "tag was not captured");
        CHECK(strcmp(entries[i].message, expected) == 0,
              "capture must contain only formatted body text");
    }
    CHECK(log_capture_esp_detach(), "attached capture failed to detach");
}

static void test_unattached_forwards_without_capture_formatting(void)
{
    reset_stubs();
    __wrap_esp_log(make_config(ESP_LOG_INFO), "UART", "value=%d", formatting_probe());

    CHECK(s_forward_count == 1, "unattached call must still forward exactly once");
    CHECK(s_vsnprintf_count == 0,
          "unattached wrapper must not invoke capture formatting");
    CHECK(s_format_side_effects == 1,
          "unattached wrapper must not evaluate or format arguments a second time");
    CHECK(strcmp(s_forwarded_message, "value=77") == 0, "forwarded arguments were corrupted");
}

static void test_constrained_binary_and_suppressed_calls_skip_capture(void)
{
    reset_stubs();
    log_capture_entry_t storage[4];
    log_capture_entry_t entry;
    log_capture_t capture;
    log_capture_init(&capture, storage, 4, LOG_CAPTURE_LEVEL_VERBOSE);
    log_capture_esp_attach(&capture);

    esp_log_config_t config = make_config(ESP_LOG_INFO);
    config.opts.constrained_env = 1;
    __wrap_esp_log(config, "SAFE", "config constrained");

    config = make_config(ESP_LOG_INFO);
    s_runtime_constrained = true;
    __wrap_esp_log(config, "SAFE", "runtime constrained");
    s_runtime_constrained = false;

    config.opts.binary_mode = 1;
    __wrap_esp_log(config, "SAFE", "binary");

    config = make_config(ESP_LOG_INFO);
    log_capture_suppress(&capture);
    __wrap_esp_log(config, "SAFE", "suppressed");
    log_capture_resume(&capture);

    CHECK(s_forward_count == 4, "all skipped capture paths must still forward");
    CHECK(s_vsnprintf_count == 0,
          "skipped capture paths must not enter capture formatting");
    CHECK(drain_all(&capture, &entry, 1) == 0,
          "constrained, binary, and suppressed calls must not be captured");
    CHECK(log_capture_esp_detach(), "capture failed to detach after skip tests");
}

static void test_constrained_check_precedes_reader_and_config_path_skips_helper(void)
{
    reset_stubs();
    log_capture_entry_t storage[2];
    log_capture_t capture;
    log_capture_init(&capture, storage, 2, LOG_CAPTURE_LEVEL_VERBOSE);
    log_capture_esp_attach(&capture);

    s_detach_during_constraint_check = true;
    s_runtime_constrained = true;
    __wrap_esp_log(make_config(ESP_LOG_INFO), "SAFE", "runtime constrained");
    CHECK(s_runtime_constraint_checks == 1, "runtime constrained helper was not called once");
    CHECK(s_constraint_check_detach_result,
          "runtime constrained helper must run before acquiring a wrapper reader");
    CHECK(s_forward_count == 1, "runtime constrained path must preserve forwarding");

    log_capture_esp_attach(&capture);
    esp_log_config_t config = make_config(ESP_LOG_INFO);
    config.opts.constrained_env = 1;
    s_runtime_constraint_checks = 0;
    __wrap_esp_log(config, "SAFE", "configured constrained");
    CHECK(s_runtime_constraint_checks == 0,
          "configured constrained path must not call the runtime helper");
    CHECK(s_forward_count == 2, "configured constrained path must preserve forwarding");
    CHECK(log_capture_esp_detach(), "configured constrained cleanup detach failed");
}

static void test_remote_threshold_and_non_log_levels_skip_capture(void)
{
    reset_stubs();
    log_capture_entry_t storage[2];
    log_capture_entry_t entry;
    log_capture_t capture;
    log_capture_init(&capture, storage, 2, LOG_CAPTURE_LEVEL_WARN);
    log_capture_esp_attach(&capture);

    __wrap_esp_log(make_config(ESP_LOG_INFO), "FILTER", "info");
    __wrap_esp_log(make_config(ESP_LOG_NONE), "FILTER", "none");
    __wrap_esp_log(make_config(ESP_LOG_MAX), "FILTER", "max");

    CHECK(s_forward_count == 3, "filtered levels must retain UART forwarding");
    CHECK(drain_all(&capture, &entry, 1) == 0, "filtered levels reached capture ring");
    CHECK(log_capture_esp_detach(), "capture failed to detach after level test");
}

static void test_attach_detach_lifecycle(void)
{
    reset_stubs();
    log_capture_entry_t storage[2];
    log_capture_entry_t entry;
    log_capture_t capture;
    log_capture_init(&capture, storage, 2, LOG_CAPTURE_LEVEL_VERBOSE);

    log_capture_esp_attach(&capture);
    __wrap_esp_log(make_config(ESP_LOG_ERROR), "LIFE", "attached");
    CHECK(log_capture_esp_detach(), "task-context detach should complete");
    __wrap_esp_log(make_config(ESP_LOG_ERROR), "LIFE", "detached");

    CHECK(s_forward_count == 2, "attach/detach must not affect forwarding");
    CHECK(drain_all(&capture, &entry, 1) == 1, "exactly the attached call should be captured");
    CHECK(strcmp(entry.message, "attached") == 0, "detached call reached capture");

    log_capture_esp_attach(&capture);
    s_isr_context = true;
    CHECK(!log_capture_esp_detach(), "detach must reject ISR context");
    s_isr_context = false;
    __wrap_esp_log(make_config(ESP_LOG_WARN), "LIFE", "still attached");
    CHECK(log_capture_esp_detach(), "cleanup detach failed");
    CHECK(drain_all(&capture, &entry, 1) == 1,
          "failed ISR detach must leave the capture attached");
}

static void test_recursion_guard_blocks_nested_capture_but_forwards_it(void)
{
    reset_stubs();
    log_capture_entry_t storage[3];
    log_capture_entry_t entries[3];
    log_capture_t capture;
    log_capture_init(&capture, storage, 3, LOG_CAPTURE_LEVEL_VERBOSE);
    log_capture_esp_attach(&capture);
    s_reentrant_capture = &capture;

    __wrap_esp_log(make_config(ESP_LOG_INFO), "OUTER", "outer=%d", 5);

    CHECK(s_forward_count == 2, "recursive log must still be forwarded to the original backend");
    CHECK(drain_all(&capture, entries, 3) == 1,
          "thread-local recursion guard must block nested capture");
    CHECK(strcmp(entries[0].tag, "OUTER") == 0 &&
          strcmp(entries[0].message, "outer=5") == 0,
          "outer capture was corrupted by recursive forwarding");
    CHECK(log_capture_esp_detach(), "capture failed to detach after recursion test");
}

int main(void)
{
    test_maps_all_levels_and_captures_plain_body();
    test_unattached_forwards_without_capture_formatting();
    test_constrained_binary_and_suppressed_calls_skip_capture();
    test_constrained_check_precedes_reader_and_config_path_skips_helper();
    test_remote_threshold_and_non_log_levels_skip_capture();
    test_attach_detach_lifecycle();
    test_recursion_guard_blocks_nested_capture_but_forwards_it();

    if (s_failures != 0) {
        fprintf(stderr, "%d wrapper test(s) failed\n", s_failures);
        return 1;
    }
    printf("log_capture_esp_tests: all tests passed\n");
    return 0;
}
