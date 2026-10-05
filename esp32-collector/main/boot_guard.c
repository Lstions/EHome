/**
 * @file boot_guard.c
 * @brief 启动熔断：连续短命启动 -> 安全模式；证明健康后自动退出。
 *
 * 设计理由见 boot_guard.h。这里只强调两条实现红线：
 *   1) NVS 每次启动只写一次（计数），**绝不**周期性写 flash；
 *      周期性的"存活多久"记录落在 RTC_NOINIT SRAM 里。
 *   2) 安全模式必须能自动退出，且退出条件是"服务端接触过"而不是"等够时间"。
 */

#include "boot_guard.h"

#include <string.h>

#include "esp_attr.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "nvs.h"
#include "nvs_flash.h"

static const char *TAG = "BOOT_GUARD";

#define NVS_NS              "bootguard"
#define NVS_KEY_SHORT_BOOTS "shortboots"

/* RTC_NOINIT：跨复位保留，写入不耗 flash。掉电丢失 —— 这是特性而非缺陷：
 * 掉电重启不应该被算作"重启循环"。 */
#define BOOT_GUARD_RTC_MAGIC 0x42475452UL /* "BGTR" */

typedef struct {
    uint32_t magic;
    uint32_t uptime_sec; /* 上次刷新时本次启动已存活秒数 */
} boot_guard_rtc_t;

static RTC_NOINIT_ATTR boot_guard_rtc_t s_rtc;

static bool     s_inited       = false;
static bool     s_safe_mode    = false;
static uint32_t s_short_boots  = 0;
static const char *s_reason    = "";
static esp_timer_handle_t s_tick_timer = NULL;

/* 本次启动的起始时刻。tick 用它换算 uptime，避免依赖与系统时钟的耦合。
 * 注意不能用 esp_timer_get_time() 作为 uptime 基准的唯一来源 —— 它本身是可靠的，
 * 但我们在 tick 里只需要"距启动过了多久"，用 esp_timer_get_time() 减去启动时
 * 记录的值即可，无需额外状态。 */
static int64_t s_boot_us = 0;

static uint32_t load_short_boots(void)
{
    nvs_handle_t h;
    uint32_t v = 0;
    if (nvs_open(NVS_NS, NVS_READONLY, &h) != ESP_OK) return 0;
    if (nvs_get_u32(h, NVS_KEY_SHORT_BOOTS, &v) != ESP_OK) v = 0;
    nvs_close(h);
    return v;
}

static void store_short_boots(uint32_t v)
{
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READWRITE, &h) != ESP_OK) {
        ESP_LOGW(TAG, "nvs_open failed; boot-loop counter will not persist");
        return;
    }
    if (nvs_set_u32(h, NVS_KEY_SHORT_BOOTS, v) == ESP_OK) {
        (void)nvs_commit(h);
    }
    nvs_close(h);
}

bool boot_guard_init(void)
{
    if (s_inited) return s_safe_mode;
    s_inited = true;
    s_boot_us = esp_timer_get_time();

    /* 读取**上一次启动存活了多久**。首次上电或掉电后 magic 不匹配 -> 视为"没有历史"。 */
    uint32_t prev_uptime = 0;
    bool have_history = (s_rtc.magic == BOOT_GUARD_RTC_MAGIC);
    if (have_history) prev_uptime = s_rtc.uptime_sec;

    uint32_t count = load_short_boots();

    if (!have_history) {
        /* 冷启动/掉电：无历史可比，不计入循环。 */
        count = 0;
    } else if (prev_uptime < BOOT_GUARD_SHORT_BOOT_SEC) {
        count++;
        ESP_LOGW(TAG, "Short-lived boot detected: previous uptime=%us (<%us), "
                      "consecutive=%u",
                 (unsigned)prev_uptime, (unsigned)BOOT_GUARD_SHORT_BOOT_SEC,
                 (unsigned)count);
    } else {
        /* 上次活过了阈值 -> 这是一次正常重启，计数清零。 */
        if (count != 0) {
            ESP_LOGI(TAG, "Previous boot was healthy (%us); resetting loop counter",
                     (unsigned)prev_uptime);
        }
        count = 0;
    }

    s_short_boots = count;
    if (count >= BOOT_GUARD_LOOP_THRESHOLD) {
        s_safe_mode = true;
        s_reason = "consecutive short-lived boots (possible crash/reboot loop)";
        ESP_LOGE(TAG, "*** SAFE MODE: %u consecutive short-lived boots (<%us each). "
                      "Optional features disabled until a server sync proves health. ***",
                 (unsigned)count, (unsigned)BOOT_GUARD_SHORT_BOOT_SEC);
    }

    store_short_boots(count);

    /* 为本次启动重置 RTC 记录：uptime 从 0 开始，magic 标记"有历史"。 */
    s_rtc.magic = BOOT_GUARD_RTC_MAGIC;
    s_rtc.uptime_sec = 0;
    return s_safe_mode;
}

void boot_guard_tick(void)
{
    if (!s_inited) return;
    int64_t elapsed_us = esp_timer_get_time() - s_boot_us;
    if (elapsed_us < 0) return;
    /* 只写 RTC_NOINIT：零 flash 代价，可高频调用。 */
    s_rtc.magic = BOOT_GUARD_RTC_MAGIC;
    s_rtc.uptime_sec = (uint32_t)(elapsed_us / 1000000LL);
}

static void tick_cb(void *arg)
{
    (void)arg;
    boot_guard_tick();
}

void boot_guard_start_watchdog(void)
{
    if (s_tick_timer != NULL) return;
    const esp_timer_create_args_t args = {
        .callback = tick_cb,
        .arg = NULL,
        .name = "boot_guard",
    };
    if (esp_timer_create(&args, &s_tick_timer) != ESP_OK) {
        ESP_LOGW(TAG, "tick timer unavailable; short-boot detection degraded");
        s_tick_timer = NULL;
        return;
    }
    /* 2s 一次：足够精确（阈值是 60s），且只是几次 SRAM 写入。 */
    (void)esp_timer_start_periodic(s_tick_timer, 2000000);
}

bool boot_guard_in_safe_mode(void)
{
    return s_inited && s_safe_mode;
}

const char *boot_guard_safe_mode_reason(void)
{
    return s_reason != NULL ? s_reason : "";
}

uint32_t boot_guard_short_boot_count(void)
{
    return s_short_boots;
}

void boot_guard_notify_server_contact(void)
{
    if (!s_inited) return;
    if (s_short_boots != 0) {
        s_short_boots = 0;
        store_short_boots(0);
    }
    if (s_safe_mode) {
        s_safe_mode = false;
        s_reason = "";
        ESP_LOGW(TAG, "Health proven (server sync); leaving SAFE MODE. "
                      "Optional features will be restored on the next manifest apply.");
    }
}
