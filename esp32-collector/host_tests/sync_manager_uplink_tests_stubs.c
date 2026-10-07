/* sync_manager_uplink_tests_stubs.c —— 本用例需要的最小替身
 *
 * 为什么需要它：sync_manager.c 会 esp_timer（配置接收超时）、config_mgr（有无 manifest）、
 * rgb_led（状态灯）—— 而 host_tests/stubs/ 里只提供了头文件与 esp_timer_get_time()。
 *
 * ⚠ 这里全部是**刻意的 no-op/可控替身**，且**不假装**完成任何语义：
 *    - esp_timer 三个函数建了就成、停/起都成功：本用例不测超时逻辑（那是另一条线），
 *      只需要这些调用不崩、不阻断。一个"假装在跑定时器"的替身会让"超时该触发却没触发"
 *      这类缺陷在本用例里看不见 —— 那正是本仓反复记的"替身比真实更宽容"陷阱。
 *    - config_mgr_has_manifest 返回 0：本用例只关心"要不要请求同步"，不关心已有配置。
 *    - rgb_led_set_state 空实现：状态灯与本用例无关。
 *    - g_test_time_us 定义在此（stub 头里声明为 extern）。
 */
#include <stddef.h>
#include <stdint.h>
#include <stdbool.h>
#include "esp_timer.h"
#include "rgb_led.h"
#include "esp_log.h"

/* esp_timer stub 头声明的时间基准 */
int64_t g_test_time_us = 0;

esp_err_t esp_timer_create(const esp_timer_create_args_t *args, esp_timer_handle_t *out)
{
    (void)args;
    if (out == NULL) return ESP_ERR_INVALID_ARG;
    static int dummy;
    *out = (esp_timer_handle_t)&dummy;   /* 非 NULL 即可（调用方只判非空） */
    return ESP_OK;
}
esp_err_t esp_timer_stop(esp_timer_handle_t t) { (void)t; return ESP_OK; }
esp_err_t esp_timer_start_once(esp_timer_handle_t t, uint64_t us) { (void)t; (void)us; return ESP_OK; }

/* config_mgr：本用例不关心"已有配置"，一律返回"没有"。 */
bool config_mgr_has_manifest(void) { return false; }

/* rgb_led：与本用例无关。 */
void rgb_led_set_state(led_state_t state) { (void)state; }

/* esp_log 替身（stubs/esp_log.h 把 ESP_LOG* 宏指向它）。 */
void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level; (void)tag; (void)format;
}
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_ERR"; }

/* config_mgr 的其余替身：本用例只关心"要不要请求同步"，不关心配置元数据本身。 */
uint64_t config_mgr_get_epoch(void) { return 0; }
const char *config_mgr_get_last_known_manifest_id(void) { return ""; }
esp_err_t config_mgr_persist_sync_metadata(uint64_t epoch, const char *manifest_id)
{
    (void)epoch; (void)manifest_id;
    return ESP_OK;   /* ⚠ 刻意不落任何东西：本用例不测持久化 */
}
