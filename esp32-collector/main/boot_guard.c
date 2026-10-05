/**
 * @file boot_guard.c
 * @brief 启动熔断：重启过于频繁 -> 安全模式；持续健康后自动退出。
 *
 * 设计理由见 boot_guard.h。两条实现红线：
 *   1) NVS 每次启动只写一次（计数），**绝不**周期性写 flash；
 *      周期性的状态记录落在 RTC_NOINIT SRAM 里。
 *   2) 安全模式必须能自动退出，且退出条件是"持续健康"而不是"一次握手"。
 */

#include "boot_guard.h"

#include <string.h>

#include "esp_attr.h"
#include "esp_log.h"
#include "esp_rtc_time.h"
#include "esp_timer.h"
#include "nvs.h"
#include "nvs_flash.h"

static const char *TAG = "BOOT_GUARD";

#define NVS_NS              "bootguard"
#define NVS_KEY_FAST_BOOTS  "fastboots"

/* RTC_NOINIT：跨复位保留，写入不耗 flash。掉电丢失 —— 这是特性而非缺陷：
 * 掉电重启不应该被算作"重启循环"。 */
#define BOOT_GUARD_RTC_MAGIC 0x42475432UL /* "BGT2" —— 布局版本号，改结构时递增 */

typedef struct {
    uint32_t magic;
    uint64_t last_boot_rtc_us; /* 上一次启动时刻（RTC 计数器，跨复位不归零） */
    uint32_t fast_boots;       /* 连续"紧跟前一次启动"的次数 */
} boot_guard_rtc_t;

static RTC_NOINIT_ATTR boot_guard_rtc_t s_rtc;

static bool     s_inited      = false;
static bool     s_safe_mode   = false;
static bool     s_health_seen = false; /* 已观测到服务端往返，但还在等最短驻留 */
static uint32_t s_fast_boots  = 0;
static const char *s_reason   = "";
static esp_timer_handle_t s_tick_timer = NULL;
static int64_t s_boot_us = 0;

static uint32_t load_fast_boots(void)
{
    nvs_handle_t h;
    uint32_t v = 0;
    if (nvs_open(NVS_NS, NVS_READONLY, &h) != ESP_OK) return 0;
    if (nvs_get_u32(h, NVS_KEY_FAST_BOOTS, &v) != ESP_OK) v = 0;
    nvs_close(h);
    return v;
}

static void store_fast_boots(uint32_t v)
{
    nvs_handle_t h;
    if (nvs_open(NVS_NS, NVS_READWRITE, &h) != ESP_OK) {
        ESP_LOGW(TAG, "nvs_open failed; boot-loop counter will not persist");
        return;
    }
    if (nvs_set_u32(h, NVS_KEY_FAST_BOOTS, v) == ESP_OK) {
        (void)nvs_commit(h);
    }
    nvs_close(h);
}

bool boot_guard_init(void)
{
    if (s_inited) return s_safe_mode;
    s_inited = true;
    s_boot_us = esp_timer_get_time();

    const uint64_t now_rtc_us = esp_rtc_get_time_us();
    const bool have_history = (s_rtc.magic == BOOT_GUARD_RTC_MAGIC);

    /* 判据是"**两次启动之间**隔了多久"，而不是"上一次启动活了多久"。
     *
     * 为什么不用"存活时长"：本设备 48 次崩溃的 uptime 分布是 min=13s、avg=229s、
     * max=3341s，其中 **38 次（79%）存活 >= 60 秒**。若按"上次存活 < 60s 才算短命"，
     * 绝大多数真实崩溃都会被漏掉 —— 一个每次活 200 秒就崩的循环永远不会触发熔断。
     *
     * 改用"距上次启动的间隔"后，判据与单次存活多久无关：
     *   - 事故现场是 506 秒内 11 次重启，间隔约 50 秒 => 连续 3 次很快达标；
     *   - 而"稳定运行一周后正常重启一次"的间隔是 1 周 >> 窗口 => 计数清零，
     *     不会误判。
     *
     * esp_rtc_get_time_us() 读的是 RTC 计数器，**跨 CPU 复位不归零**，所以能
     * 直接做差；掉电才归零，而掉电不算重启循环（正是我们想要的）。 */
    uint32_t fast = 0;
    if (have_history) {
        const uint64_t delta_us = (now_rtc_us > s_rtc.last_boot_rtc_us)
                                      ? (now_rtc_us - s_rtc.last_boot_rtc_us)
                                      : 0;
        const uint32_t delta_s = (uint32_t)(delta_us / 1000000ULL);
        if (delta_s < BOOT_GUARD_LOOP_WINDOW_SEC) {
            /* 紧接着上一次启动 —— 无论上次活了 13 秒还是 200 秒，都算一次快速重启。 */
            fast = s_rtc.fast_boots + 1;
            ESP_LOGW(TAG, "Fast reboot detected: %us since previous boot (<%us window), "
                          "consecutive=%u",
                     (unsigned)delta_s, (unsigned)BOOT_GUARD_LOOP_WINDOW_SEC,
                     (unsigned)fast);
        } else {
            ESP_LOGI(TAG, "Previous boot was %us ago (>= %us); resetting reboot counter",
                     (unsigned)delta_s, (unsigned)BOOT_GUARD_LOOP_WINDOW_SEC);
            fast = 1; /* 本次启动自己算第 1 次 */
        }
    } else {
        /* 冷启动/掉电：无历史可比，不计入循环。 */
        fast = 1;
    }

    /* NVS 里的计数与 RTC 里的计数取较大者：
     * NVS 跨掉电保留，RTC 跨复位保留。取大者可以避免"掉电后计数丢失"
     * 导致熔断被绕过（例如反复上电制造的循环）。 */
    const uint32_t nvs_fast = load_fast_boots();
    if (nvs_fast > fast) fast = nvs_fast;

    s_fast_boots = fast;
    if (fast >= BOOT_GUARD_LOOP_THRESHOLD) {
        s_safe_mode = true;
        s_reason = "too many rapid reboots (possible crash/reboot loop)";
        ESP_LOGE(TAG, "*** SAFE MODE: %u rapid reboots within a %us window. "
                      "Optional features disabled until health is sustained. ***",
                 (unsigned)fast, (unsigned)BOOT_GUARD_LOOP_WINDOW_SEC);
    }

    store_fast_boots(fast);

    /* 为本次启动重置 RTC 记录。 */
    s_rtc.magic = BOOT_GUARD_RTC_MAGIC;
    s_rtc.last_boot_rtc_us = now_rtc_us;
    s_rtc.fast_boots = fast;
    return s_safe_mode;
}

static int64_t uptime_ms(void)
{
    const int64_t us = esp_timer_get_time() - s_boot_us;
    return us < 0 ? 0 : us / 1000;
}

void boot_guard_tick(void)
{
    if (!s_inited) return;

    /* 本次启动已经**活过了循环窗口** —— 仅凭这一点就足以证明它不是一次"快速重启"，
     * 因此把计数清零。
     *
     * 为什么计数清零放在这里、而**不**放在 notify_server_contact()：
     *
     * 第一版是在"服务端往返成功"时清零。那是错的，而且错得很关键：设备启动后
     * 约 20 秒就会完成一次配置事务（manifest -> 应用 -> ConfigResult），
     * 于是一个"每次活 50 秒就崩"的循环会变成：
     *
     *     启动 -> 20s 配置成功（计数被清零）-> 50s 崩溃 -> 启动 -> ...
     *
     * **计数永远累积不起来，安全模式永远不会触发** —— 也就是熔断在它本该起作用的
     * 那个场景里完全失效。
     *
     * 判据必须是"这台设备活过了窗口"，而不是"它做成了某件事"。 */
    if (s_fast_boots != 0 &&
        uptime_ms() >= (int64_t)BOOT_GUARD_LOOP_WINDOW_SEC * 1000) {
        ESP_LOGI(TAG, "Uptime %lld ms exceeds the %us reboot window; clearing counter",
                 (long long)uptime_ms(), (unsigned)BOOT_GUARD_LOOP_WINDOW_SEC);
        s_fast_boots = 0;
        s_rtc.fast_boots = 0;
        store_fast_boots(0);
    }
    /* 退出判据：**本次启动已持续运行超过最短驻留时间**。
     *
     * 这里踩过一个大坑，记录下来避免再犯：
     *
     * 第一版要求"必须观测到一次服务端往返（s_health_seen）"才能退出，
     * 而 s_health_seen 只在 handle_config_applied() 里置位 —— 也就是**只在收到
     * ConfigManifest 并应用成功时**。
     *
     * 问题是：设备重启后如果配置**没有变化**，服务端的 hash 匹配成立，
     * 于是**正确地不下发 manifest**（这是对的行为，不是缺陷）。
     * 结果 handle_config_applied() 永远不会被调用 -> s_health_seen 永远为 false
     * -> **安全模式永远退不出来**。
     *
     * 也就是说：**设备越健康（配置越稳定、越不需要变更），就越退不出降级。**
     * 实测确认：三次快速复位后日志上报被成功压住（熔断生效），
     * 但等 uptime 超过 130 秒后日志**仍然**没有恢复 —— 正是这个死锁。
     *
     * 正确判据是"它活下来了"，而不是"它做成了某件事"：
     * 崩溃循环里的设备每次重启都会把 uptime 归零，**永远到不了 120 秒**；
     * 能连续活过 120 秒，本身就是"已脱离循环"的充分证据。
     *
     * 这个判据不会死锁，也不依赖任何外部事件，因此比原方案更可靠。
     * s_health_seen 保留下来仅用于**日志说明**（区分"是活着退出的"还是
     * "顺带还连上了服务端"），不再作为退出前提。 */
    if (s_safe_mode && uptime_ms() >= BOOT_GUARD_SAFE_MODE_MIN_DWELL_MS) {
        const bool saw_server = s_health_seen;
        s_safe_mode = false;
        s_reason = "";
        s_health_seen = false;

        /* **必须同时清零计数（RTC + NVS）**，否则安全模式是单向的。
         *
         * 为什么：init() 里用 `max(NVS 计数, RTC 计数)` 来防"反复上电绕过熔断"。
         * 但进入安全模式后 NVS 里存的是 >= 阈值 的值，而正常模式下才有机会清零它
         * （见 notify_server_contact）。于是下次启动算出的 max() 仍然 >= 阈值，
         * **再次进入安全模式** —— 设备被永久降级，只能靠人工恢复。
         * 那正是熔断本该避免的结局，只是换了个形式。 */
        s_fast_boots = 0;
        s_rtc.fast_boots = 0;
        store_fast_boots(0);

        ESP_LOGW(TAG, "SAFE MODE cleared after %lld ms uptime (server contact seen: %s); "
                      "reboot counter reset; optional features will be restored on the "
                      "next manifest apply.",
                 (long long)uptime_ms(), saw_server ? "yes" : "no");
    }
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
        ESP_LOGW(TAG, "tick timer unavailable; safe-mode exit needs status_task ticks");
        s_tick_timer = NULL;
        return;
    }
    (void)esp_timer_start_periodic(s_tick_timer, 1000000);
}

bool boot_guard_in_safe_mode(void)
{
    return s_inited && s_safe_mode;
}

const char *boot_guard_safe_mode_reason(void)
{
    return s_reason != NULL ? s_reason : "";
}

uint32_t boot_guard_fast_boot_count(void)
{
    return s_fast_boots;
}

void boot_guard_notify_server_contact(void)
{
    if (!s_inited) return;
    s_health_seen = true;

    if (!s_safe_mode) {
        /* 正常模式下**不**在这里清零计数。
         *
         * 清零由 boot_guard_tick() 在"本次启动活过循环窗口"时执行。
         * 在这里清零会让熔断彻底失效：设备启动约 20 秒就能完成一次配置事务，
         * 于是一个"每次活 50 秒就崩"的循环每次都会先把计数清掉，
         * 永远累积不到阈值。详见 tick() 里的说明。 */
        return;
    }

    /* 安全模式下：这里**不**立即解除，只记录"已见到健康"；
     * 由 boot_guard_tick() 在满足最短驻留后再解除。 */
    ESP_LOGW(TAG, "Health observed while in SAFE MODE; will exit after %u ms uptime "
                  "(currently %lld ms)",
             (unsigned)BOOT_GUARD_SAFE_MODE_MIN_DWELL_MS, (long long)uptime_ms());
}