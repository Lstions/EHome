#pragma once

/* ============================================================================
 * 启动熔断（boot guard）— 2026-10-04
 * ============================================================================
 *
 * 为什么需要它
 * ------------
 * 2026-10-04 实测：满资源占用下打开日志上报，S3 节点在 506 秒内 PANIC 重启 **11 次**，
 * 崩溃时 uptime 只有 **23~112 秒**（根因是 report_tx 在同步写 socket 时无法喂
 * TWDT，详见 components/ehome_mqtt/ehome_mqtt.c 的说明）。
 *
 * 真正危险的不是"崩了一次"，而是**自持**：日志开关的配置随 ConfigManifest 持久化，
 * 设备每次重启后都会**重新应用同一个配置**，于是再次崩溃 —— 形成无限重启循环。
 * 现有代码里没有任何熔断：
 *   - CONFIG_BOOTLOADER_APP_ROLLBACK_ENABLE=n（不做 OTA 回滚）；
 *   - 无连续启动计数、无安全模式、无降级启动。
 * 也就是说：**一个坏配置就能让远程部署的节点永久躺平，且只能靠人工接触设备恢复。**
 *
 * 设计要点
 * --------
 * 1) 判据是"短命启动次数"，不是"启动次数"。
 *
 *    只看"启动了几次"是错的：一台稳定运行一周后正常重启一次的设备也会被误判。
 *    所以每次启动时读取**上一次启动存活了多久**：
 *      - 上次 uptime < BOOT_GUARD_SHORT_BOOT_SEC → 记为一次"短命启动"，计数 +1；
 *      - 上次 uptime >= 阈值                      → 计数归 1（这是一次正常重启）。
 *    连续 BOOT_GUARD_LOOP_THRESHOLD 次短命启动 → 进入安全模式。
 *
 * 2) "上次存活多久"用 RTC_NOINIT 记录，不写 flash。
 *
 *    需要周期性地把 uptime 落盘才能跨重启比较，但**每几秒写一次 NVS 会磨损 flash**。
 *    RTC_NOINIT SRAM 跨复位保留、写入免费，正好合适（crash_diag 也用同一手法）。
 *    因此：
 *      - 周期任务只更新 RTC_NOINIT 里的 uptime（零 flash 代价）；
 *      - NVS 只在**每次启动写一次**（记录计数）。
 *
 * 3) 安全模式必须能自动退出，否则等于永久降级。
 *
 *    退出条件是"已证明健康"，而不是"等够时间"：设备必须真的连上服务端并完成一次
 *    正常同步（boot_guard_notify_server_contact()）。这样即使安全模式下仍有问题，
 *    它也不会自己把降级解除、再次冲进崩溃循环。
 *
 * 4) 安全模式**只关可选特性，不动核心能力**。
 *
 *    设备在安全模式下仍然：联网、上报心跳、接受配置、执行命令。
 *    只关闭"会把同一条连接流量放大数倍"的可选特性（当前是日志上报）。
 *    这样运维仍然能通过网络诊断和恢复设备，而不是只能去现场。
 */

#include <stdbool.h>
#include <stdint.h>

/* 上一次启动的存活时间低于此值，即视为"短命启动"（计入重启循环）。
 * 取 60s：实测崩溃循环的 uptime 都在 23~112s；而正常的配置事务、OTA 收尾
 * 等启动过程都在数秒内完成，60s 足以区分"没跑起来"与"跑起来后重启"。 */
#define BOOT_GUARD_SHORT_BOOT_SEC 60U

/* 连续多少次短命启动后进入安全模式。取 3：
 * 2 次可能只是偶发（例如恰好赶上网络抖动），3 次已足以说明是"每次启动都撞同一堵墙"。 */
#define BOOT_GUARD_LOOP_THRESHOLD 3U

/**
 * 初始化：读上次存活时长 -> 判定是否短命 -> 更新 NVS 计数 -> 决定是否进入安全模式。
 *
 * 必须在 nvs_flash_init() 之后、启动各子系统之前调用（建议紧随 crash_diag_init()）。
 * 幂等。
 *
 * @return 进入安全模式返回 true，否则 false。
 */
bool boot_guard_init(void);

/** 当前是否处于安全模式。未初始化时返回 false。 */
bool boot_guard_in_safe_mode(void);

/** 进入安全模式的原因（可读字符串，永不返回 NULL；未进入时返回 ""）。 */
const char *boot_guard_safe_mode_reason(void);

/**
 * 周期性刷新"本次启动已存活多久"。由定时器/周期任务调用，只写 RTC_NOINIT，
 * 无 flash 代价。掉电即失效，这正是我们想要的（掉电不算重启循环）。
 */
void boot_guard_tick(void);

/**
 * 启动周期刷新定时器（内部使用 esp_timer，不新建任务）。
 * 幂等；在 boot_guard_init() 之后调用一次即可。
 */
void boot_guard_start_watchdog(void);

/**
 * 由服务端接触回调：设备已证明健康（已连上并完成一次同步）。
 * 清除重启计数并**退出安全模式**（本函数之后 boot_guard_in_safe_mode() 返回 false）。
 */
void boot_guard_notify_server_contact(void);

/** 连续短命启动计数（用于诊断上报）。 */
uint32_t boot_guard_short_boot_count(void);
