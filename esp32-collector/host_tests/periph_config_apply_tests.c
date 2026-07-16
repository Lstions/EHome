#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#define CHECK(expr) do { \
    if (!(expr)) { \
        fprintf(stderr, "CHECK failed at %s:%d: %s\n", __FILE__, __LINE__, #expr); \
        abort(); \
    } \
} while (0)

#include "periph_config_apply.h"
#include "freertos/semphr.h"

static config_manifest_t s_old_manifest;
static bool s_have_old_manifest;
const config_manifest_t *config_mgr_get_manifest(void)
{ return s_have_old_manifest ? &s_old_manifest : NULL; }
SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (SemaphoreHandle_t)1; }
int xSemaphoreTake(SemaphoreHandle_t semaphore, uint32_t ticks)
{ (void)semaphore; (void)ticks; return 1; }
int xSemaphoreGive(SemaphoreHandle_t semaphore)
{ (void)semaphore; return 1; }

static int s_gpio_init_calls;
static int s_gpio_init_count = -1;
static int s_pwm_init_calls;
static int s_pwm_init_count = -1;
static esp_err_t s_gpio_init_result = ESP_OK;
static esp_err_t s_pwm_init_result = ESP_OK;
static int s_gpio_fail_on_call = -1;
static int s_pwm_fail_on_call = -1;
static gpio_config_entry_t s_gpio_snapshot[MAX_GPIO_CONFIGS];
static int s_gpio_snapshot_count;
static pwm_config_entry_t s_pwm_snapshot[MAX_PWM_CONFIGS];
static int s_pwm_snapshot_count;
static char s_init_trace[64];
static size_t s_init_trace_len;
static bool s_fail_calloc;
static int s_calloc_calls;
static int s_free_calls;

void *__real_calloc(size_t count, size_t size);
void __real_free(void *ptr);

void *__wrap_calloc(size_t count, size_t size)
{
    s_calloc_calls++;
    return s_fail_calloc ? NULL : __real_calloc(count, size);
}

void __wrap_free(void *ptr)
{
    if (ptr) s_free_calls++;
    __real_free(ptr);
}

esp_err_t gpio_ctrl_init(const gpio_config_entry_t *configs, int count)
{
    (void)configs;
    s_gpio_init_calls++;
    s_init_trace[s_init_trace_len++] = 'G';
    s_gpio_init_count = count;
    if (s_gpio_init_calls == s_gpio_fail_on_call) return ESP_FAIL;
    return s_gpio_init_result;
}

esp_err_t pwm_ctrl_init(const pwm_config_entry_t *configs, int count)
{
    (void)configs;
    s_pwm_init_calls++;
    s_init_trace[s_init_trace_len++] = 'P';
    s_pwm_init_count = count;
    if (s_pwm_init_calls == s_pwm_fail_on_call) return ESP_FAIL;
    return s_pwm_init_result;
}

esp_err_t gpio_ctrl_preflight(const gpio_config_entry_t *configs, int count)
{
    (void)configs; (void)count;
    return s_gpio_init_result;
}

esp_err_t pwm_ctrl_preflight(const pwm_config_entry_t *configs, int count)
{
    (void)configs; (void)count;
    return s_pwm_init_result;
}

int gpio_ctrl_snapshot(gpio_config_entry_t *configs, int capacity)
{
    CHECK(capacity >= s_gpio_snapshot_count);
    memcpy(configs, s_gpio_snapshot,
           (size_t)s_gpio_snapshot_count * sizeof(*configs));
    return s_gpio_snapshot_count;
}

int pwm_ctrl_snapshot(pwm_config_entry_t *configs, int capacity)
{
    CHECK(capacity >= s_pwm_snapshot_count);
    memcpy(configs, s_pwm_snapshot,
           (size_t)s_pwm_snapshot_count * sizeof(*configs));
    return s_pwm_snapshot_count;
}

const char *esp_err_to_name(esp_err_t err)
{
    (void)err;
    return "host";
}

void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level;
    (void)tag;
    (void)format;
}

static void test_workspace_allocation_failure_is_fail_closed(void)
{
    config_manifest_t manifest = {0};
    int gpio_calls = s_gpio_init_calls;
    int pwm_calls = s_pwm_init_calls;
    int calloc_calls = s_calloc_calls;
    int free_calls = s_free_calls;
    s_fail_calloc = true;

    CHECK(periph_config_apply(&manifest) == ESP_ERR_NO_MEM);
    CHECK(s_gpio_init_calls == gpio_calls);
    CHECK(s_pwm_init_calls == pwm_calls);
    CHECK(s_calloc_calls == calloc_calls + 1);
    CHECK(s_free_calls == free_calls);
    s_fail_calloc = false;
}

static void test_empty_manifest_clears_both_controllers(void)
{
    config_manifest_t manifest = {0};

    CHECK(periph_config_apply(&manifest) == ESP_OK);
    CHECK(s_gpio_init_calls == 2);
    CHECK(s_gpio_init_count == 0);
    CHECK(s_pwm_init_calls == 2);
    CHECK(s_pwm_init_count == 0);
}

static void test_conflicting_manifest_is_rejected_before_clearing(void)
{
    config_manifest_t manifest = {0};
    manifest.gpio_config_count = 1;
    manifest.gpio_configs[0].pin = 6;
    manifest.pwm_config_count = 1;
    manifest.pwm_configs[0].pin = 6;
    manifest.pwm_configs[0].auto_start = true;
    int gpio_calls = s_gpio_init_calls;
    int pwm_calls = s_pwm_init_calls;

    CHECK(periph_config_apply(&manifest) == ESP_ERR_PIN_CONFLICT);
    CHECK(s_gpio_init_calls == gpio_calls);
    CHECK(s_pwm_init_calls == pwm_calls);
}

static void test_gpio_failure_does_not_touch_pwm(void)
{
    config_manifest_t manifest = {0};
    manifest.gpio_config_count = 1;
    manifest.gpio_configs[0].pin = 6;
    manifest.gpio_configs[0].direction = GPIO_DIR_OUTPUT;
    int pwm_calls = s_pwm_init_calls;
    s_gpio_init_result = ESP_ERR_INVALID_ARG;

    CHECK(periph_config_apply(&manifest) == ESP_ERR_INVALID_ARG);
    CHECK(s_pwm_init_calls == pwm_calls);
    s_gpio_init_result = ESP_OK;
}

static void test_pwm_failure_does_not_touch_gpio(void)
{
    config_manifest_t manifest = {0};
    manifest.pwm_config_count = 1;
    manifest.pwm_configs[0].channel = 0;
    manifest.pwm_configs[0].pin = 6;
    manifest.pwm_configs[0].frequency = 1000;
    manifest.pwm_configs[0].duty = 1000;
    manifest.pwm_configs[0].resolution = 14;
    manifest.pwm_configs[0].auto_start = true;
    int gpio_calls = s_gpio_init_calls;
    s_pwm_init_result = ESP_ERR_NOT_FOUND;

    CHECK(periph_config_apply(&manifest) == ESP_ERR_NOT_FOUND);
    CHECK(s_gpio_init_calls == gpio_calls);
    s_pwm_init_result = ESP_OK;
}

static void test_gpio_apply_failure_restores_both_old_controllers(void)
{
    config_manifest_t manifest = {0};
    manifest.gpio_config_count = 1;
    manifest.gpio_configs[0] = (config_gpio_t){
        .pin = 6, .direction = GPIO_DIR_OUTPUT, .initial_level = 1,
    };
    manifest.pwm_config_count = 1;
    manifest.pwm_configs[0] = (config_pwm_t){
        .channel = 0, .pin = 7, .frequency = 1000, .duty = 5000,
        .resolution = 14, .auto_start = true,
    };
    s_have_old_manifest = true;
    s_gpio_snapshot_count = 1;
    s_gpio_snapshot[0] = (gpio_config_entry_t){
        .pin = 4, .direction = GPIO_DIR_OUTPUT, .initial_level = 0,
    };
    s_pwm_snapshot_count = 1;
    s_pwm_snapshot[0] = (pwm_config_entry_t){
        .channel = 1, .pin = 5, .frequency = 500, .duty = 2500,
        .resolution = 14, .auto_start = true,
    };
    int gpio_calls = s_gpio_init_calls;
    int pwm_calls = s_pwm_init_calls;
    int calloc_calls = s_calloc_calls;
    int free_calls = s_free_calls;
    size_t trace_start = s_init_trace_len;
    s_gpio_fail_on_call = gpio_calls + 2; /* clear old, then reject new */

    CHECK(periph_config_apply(&manifest) == ESP_FAIL);
    CHECK(s_gpio_init_calls == gpio_calls + 4); /* clear, new, rollback clear, restore */
    CHECK(s_pwm_init_calls == pwm_calls + 3);   /* clear, rollback clear, restore */
    CHECK(s_gpio_init_count == 1);
    CHECK(s_pwm_init_count == 1);
    CHECK(s_calloc_calls == calloc_calls + 1);
    CHECK(s_free_calls == free_calls + 1);
    CHECK(s_init_trace_len - trace_start == 7);
    CHECK(memcmp(s_init_trace + trace_start, "PGGPGPG", 7) == 0);

    s_gpio_fail_on_call = -1;
    s_gpio_snapshot_count = 0;
    s_pwm_snapshot_count = 0;
    s_have_old_manifest = false;
}

int main(void)
{
    test_workspace_allocation_failure_is_fail_closed();
    test_empty_manifest_clears_both_controllers();
    test_conflicting_manifest_is_rejected_before_clearing();
    test_gpio_failure_does_not_touch_pwm();
    test_pwm_failure_does_not_touch_gpio();
    test_gpio_apply_failure_restores_both_old_controllers();
    puts("peripheral config apply tests passed");
    return 0;
}