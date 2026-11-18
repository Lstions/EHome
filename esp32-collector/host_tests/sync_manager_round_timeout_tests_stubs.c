/* sync_manager_round_timeout_tests_stubs.c —— 可**被用例驱动**的替身
 *
 * 与 sync_manager_uplink_tests_stubs.c 的关键区别：
 *   那边的 esp_timer 替身是 **no-op**（"不会让超时真的触发"，见 stubs/esp_timer.h
 *   的语义提醒）；本用例要证明的**正是"表到点之后发生了什么"** ⇒ 一个 no-op
 *   定时器会让 "超时该触发却没触发" 这类缺陷在宿主侧永远看不见（假绿）。
 *
 * 因此这里的替身把「回调 + 时长」如实存下来，由用例显式 host_test_timer_fire()
 * 触发。"时间到了" 是**用例**的主张，不是替身的假设。
 */
#include <stddef.h>
#include <stdint.h>
#include <stdbool.h>
#include "esp_timer.h"
#include "rgb_led.h"
#include "esp_log.h"

/* esp_timer stub 头声明的时间基准（sync_manager 的 get_time_sec 读它）。 */
int64_t g_test_time_us = 0;

/* ---- 定时器：观察 + 驱动 ---- */
static esp_timer_cb_t s_cb = NULL;
static void *s_cb_arg = NULL;
static bool s_armed = false;
static uint32_t s_armed_sec = 0;
static int s_start_count = 0;
static int s_stop_count = 0;
static int s_callback_count = 0;

esp_err_t esp_timer_create(const esp_timer_create_args_t *args,
                           esp_timer_handle_t *out)
{
    if (args == NULL || out == NULL) return ESP_ERR_INVALID_ARG;
    static int dummy;                 /* 句柄：调用方只判非 NULL */
    s_cb = args->callback;
    s_cb_arg = args->arg;
    *out = (esp_timer_handle_t)&dummy;
    return ESP_OK;
}

esp_err_t esp_timer_stop(esp_timer_handle_t t)
{
    (void)t;
    s_stop_count++;
    s_armed = false;
    return ESP_OK;
}

esp_err_t esp_timer_start_once(esp_timer_handle_t t, uint64_t us)
{
    (void)t;
    s_armed = true;
    s_armed_sec = (uint32_t)(us / 1000000ULL);
    s_start_count++;
    return ESP_OK;
}

bool host_test_timer_armed(void) { return s_armed; }
int host_test_timer_armed_sec(void) { return (int)s_armed_sec; }
int host_test_timer_start_count(void) { return s_start_count; }
int host_test_timer_stop_count(void) { return s_stop_count; }
int host_test_timer_callback_count(void) { return s_callback_count; }

/* 模拟 "时间到了"：一次性定时器触发即撤，然后同步调用生产回调。 */
void host_test_timer_fire(void)
{
    if (!s_armed || s_cb == NULL) return;
    s_armed = false;
    s_callback_count++;
    s_cb(s_cb_arg);
}

/* ---- LED：只记录最后一次，供 "点灯必须按 has_manifest 分支" 的断言用 ---- */
static led_state_t s_last_led = LED_STATE_BOOTING;
void rgb_led_set_state(led_state_t state) { s_last_led = state; }
led_state_t host_test_last_led(void) { return s_last_led; }

/* ---- config_mgr：由用例控制 "设备有没有可用配置" ---- */
static bool s_manifest = false;
void host_test_set_manifest(bool has) { s_manifest = has; }
bool config_mgr_has_manifest(void) { return s_manifest; }
uint64_t config_mgr_get_epoch(void) { return 5; }
const char *config_mgr_get_last_known_manifest_id(void) { return "manifest-host"; }
esp_err_t config_mgr_persist_sync_metadata(uint64_t epoch, const char *manifest_id)
{
    (void)epoch; (void)manifest_id;
    return ESP_OK;
}

/* ---- 日志/错误名：本用例不断言日志，只需不崩 ---- */
void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level; (void)tag; (void)format;
}
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_ERR"; }

/* ---- 一次性复位所有替身观察点 ---- */
void host_test_stub_reset(void)
{
    s_armed = false;
    s_armed_sec = 0;
    s_start_count = 0;
    s_stop_count = 0;
    s_callback_count = 0;
}
