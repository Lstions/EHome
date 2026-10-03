#pragma once

/* ============================================================================
 * 崩溃诊断 (crash diagnostics) — 2026-10-03
 * ============================================================================
 *
 * 为什么需要它
 * ------------
 * ESP32-S3 节点 (30EDA0A9A808) 以 ~4.7 次/小时 的频率复位（对照：C6 稳定
 * 23.5 小时无复位）。排查时最大的障碍是**设备端不留任何证据**：
 * node_logs 里没有 panic / abort / backtrace / Guru 任何一条，因为固件此前
 * 既不读 esp_reset_reason()，也不捕获异常帧。"复位与 ConfigManifest 下发
 * 相隔 1.0 秒"这条相关性因此永远只能停在强相关。
 *
 * 为什么不用 ESP-IDF 的 coredump 分区
 * -----------------------------------
 * IDF 的 espcoredump 组件要求分区表新增 coredump 类型分区，而**分区表无法
 * 通过 OTA 下发**（必须物理接触设备刷机）。本节点是远程部署的，物理接触不
 * 可行。故改为"复位原因 + 异常类型 + 指令地址 + 任务名 + 受控栈窗口 + 崩溃
 * 时 uptime"，这些已足够在 ELF 上定位崩溃点。
 *
 * 两条数据通道
 * ------------
 *  A) 崩溃通道（需 ACK 才释放）
 *     panic/异常 -> crash_diag_capture_from_panic()  只写 RTC_NOINIT SRAM
 *       -> 复位 -> crash_diag_init()  搬进 NVS（CRC 校验）
 *       -> crash_diag_report_pending()  发 MSG_DIAG_REPORT(0x1E)
 *       -> 服务端 MSG_DIAG_ACK(0x1F, record_id)
 *       -> crash_diag_on_ack()  删除该 NVS 记录，释放占用
 *     若始终无 ACK，记录留驻 NVS（最多 8 条，满了覆盖最旧），不会"上报丢了
 *     但设备已经忘掉"。
 *
 *  B) 启动通道（每次启动都发，不占 NVS）
 *     crash_diag_report_pending() 在无待确认崩溃时发一条 report_type=BOOT 的
 *     报告，携带 esp_reset_reason() 与"谁发起的主动重启"。这条回答"设备为
 *     什么重启"，且不产生任何 NVS 噪声。
 *
 * 主动重启标记
 * ------------
 * 固件里有十余处 esp_restart()。复位原因只能告诉我们"是软件重启"，说不出
 * 是哪一处。crash_diag_mark_reboot_reason() 在重启前把调用点名字写进
 * RTC_NOINIT，下次启动随 BOOT 报告上传 —— 一行代码换一个确定的答案。
 */

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include "esp_err.h"

/* 报告类型 */
#define CRASH_DIAG_TYPE_BOOT  1
#define CRASH_DIAG_TYPE_CRASH 2

/* RTC 记录里保存的栈窗口字数（每字 4 字节）。16 字足以覆盖返回地址链。 */
#define CRASH_DIAG_STACK_WORDS 16

/* 主动重启原因串最大长度（含结尾 NUL） */
#define CRASH_DIAG_REASON_MAX 32

/**
 * 初始化：读取本次复位原因；若 RTC 里有上次 panic 留下的有效记录，则持久化
 * 到 NVS 并清空 RTC 槽。必须在 nvs_flash_init() 之后、联网之前调用。幂等。
 *
 * @return 有崩溃记录被持久化时返回 ESP_OK；无记录返回 ESP_ERR_NOT_FOUND。
 */
esp_err_t crash_diag_init(void);

/** 本次启动的复位原因（esp_reset_reason_t 数值）。未初始化时返回 0。 */
int crash_diag_reset_reason(void);

/** 复位原因的可读字符串（用于日志与上报）。永不返回 NULL。 */
const char *crash_diag_reset_reason_str(int reason);

/** 上次主动重启前标记的原因串；无标记时返回 ""。 */
const char *crash_diag_last_reboot_reason(void);

/** NVS 中尚未被服务端确认的崩溃记录条数。 */
int crash_diag_pending_count(void);

/**
 * 上报一条诊断记录。
 *
 * 有未确认崩溃 -> 上报最旧的一条（report_type=CRASH，带 record_id/PC/栈窗口）。
 * 无未确认崩溃 -> 上报本次启动信息（report_type=BOOT，record_id=0）。
 *
 * 返回 ESP_OK 表示已交给传输层（不代表对端已收到；确认由 ACK 表达）。
 */
esp_err_t crash_diag_report_pending(void);

/**
 * 服务端确认回调：删除 record_id 对应的 NVS 记录以释放占用。
 * accepted=false 时**不删除**（服务端明确说没存下，设备必须留着重试）。
 * record_id 为 0（BOOT 报告）不占 NVS，只记日志。
 */
void crash_diag_on_ack(uint32_t record_id, bool accepted);

/**
 * 在主动重启（esp_restart）前标记原因。写 RTC_NOINIT，跨复位存活。
 * reason 建议用调用点标识，例如 "cfg_apply_failed"、"ota_complete"。
 */
void crash_diag_mark_reboot_reason(const char *reason);

/**
 * panic 早期由 wrap 调用，记录崩溃时的 uptime。
 *
 * 为什么不让 capture 自己取：esp_timer_get_time() 依赖 flash 中的定时器
 * 状态，在 panic 上下文（flash cache 可能已关）不安全。由 wrap 在进入
 * capture 前取一次，capture 只读这个变量。
 */
void crash_diag_note_panic_uptime(uint32_t uptime_sec);

/**
 * panic 捕获钩子 —— 由 -Wl,--wrap=esp_panic_handler 注入。
 *
 * **只在 panic 上下文运行**：此时 flash cache 可能已关闭。本函数只做
 * RTC SRAM 写入，绝不调用 ESP_LOG / nvs_* / malloc。
 */
void crash_diag_capture_from_panic(int core, int exception, uintptr_t pc,
                                   const void *frame);
