/**
 * @file mem_guard.h
 * @brief 运行期内存水位门禁：只认 largest，不认 free。
 *
 * 背景（docs::EHomeSystem 调试验证方法论 §4.1）：
 *   - `free` 小 = 总量不够；`free` 大但 `largest` 小 = 碎片。修法相反。
 *   - 凡是需要**一整块连续内存**的启动动作（xTaskCreate、uart_driver_install、
 *     HTTP/TLS 上下文、配置事务），判决依据必须是 `heap_caps_get_largest_free_block`
 *     而不是 `heap_caps_get_free_size`。
 *
 * 本模块是 WS-E（配置事务确定性）/ OTA / log_stream 的统一接口点：
 *   - `mem_guard_can_start(need)` 是纯函数谓词：largest >= max(need, floor)。
 *   - `mem_guard_register_low_cb()` 注册水位跌破回调；`mem_guard_poll()` 由
 *     低频任务（status_task，1 Hz）调用，触发一次低内存事件。
 *   - `floor` 是“低于此值绝不启动重操作”的硬地板，按型号/PSRAM 能力选取：
 *       **s3p ≥ 8 KiB**、s3 ≥ 8 KiB、c6 ≥ 12 KiB。
 *     ⚠ 2026-10-08（§194）：原文写"s3p ≥ 16 KiB"——**已过期**。
 *       §13（决策-3.0-s3p-内部RAM-2026-10-07.md）把 s3p 从 16 KiB 降到 **8 KiB**，
 *       因为 16 KiB **不可达**（稳态 largest 实测 12288~15360 < 16384）
 *       ⇒ 那是**功能缺陷**而非保守：配置事务恒被拒（§172 实测 memgate 14~21 次、success=0）。
 *     ⚠⚠ 这里曾与 mem_guard.c 的 #define **各写一份**同一个事实，而 c 那份先改了、
 *       h 这份没跟上 ⇒ 两处互相矛盾（正是 P4"一处定义"要消除的形态）。
 *       权威来源是 `mem_guard.c` 的 `MEM_GUARD_FLOOR_BYTES` 与
 *       `docs/设计/配置事务确定性设计-2026-10-05.md`（后者由
 *       `tools/check_mem_guard_floor.py` 与代码对拍，改一处必须改另一处）。
 *     该 floor 的**语义是内部 RAM 水位**（见 mem_guard_largest 的口径说明）。
 *
 * 注意：low callback 运行在调用 `mem_guard_poll()` 的任务上下文，**不得**在
 * 回调里做发布/阻塞动作。典型实现只置一个 volatile 标志，由 status_task
 * 在上行可用时补发一次 MemReport(0x21)。
 * ⚠ 2026-10-08 更正两处：① 类型号是 0x21（不是 0x20，见 frame_codec.h:95）；
 *   ② "在 MQTT 已连接时"这个条件已过期 —— 3.0 上线后上行不再只有 MQTT，
 *      而 main.c 的补发判据曾写死 mqtt_client_is_connected_impl()，
 *      详见 §192.3（P5 死角登记）。
 */
#ifndef MEM_GUARD_H
#define MEM_GUARD_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/** 低内存事件回调：收到触发时刻的 free/largest（字节）。 */
typedef void (*mem_guard_low_cb_t)(size_t free_bytes, size_t largest_bytes);

/** 当前**内部 RAM** 8-bit 堆 total free（字节）。
 *
 * 口径说明（2026-10-05 缺陷修复）：本模块统一使用
 * `MALLOC_CAP_INTERNAL | MALLOC_CAP_8BIT`，**不含 PSRAM**。
 * 此前用裸 `MALLOC_CAP_8BIT`，在开了 CONFIG_SPIRAM_USE_MALLOC 的 s3p 上会跨
 * 内部堆与 PSRAM 堆取合计/最大值，导致门禁恒放行（见 mem_guard.c 顶部注释）。 */
size_t mem_guard_free(void);

/** 当前**内部 RAM** 最大连续块（字节）—— 所有 can_start 判决的核心。
 *
 * 为什么必须是内部 RAM：配置事务 / UART 驱动 install / OTA / log_stream /
 * 任务栈要的都是内部 RAM 的连续块（任务栈与 DMA/ISR 缓冲必须内部；flash 写
 * 期间 cache 关闭、访问 PSRAM 会崩）。PSRAM 上的 8 MB 连续块不能替代。 */
size_t mem_guard_largest(void);

/** 历史最小内部 RAM 空闲（字节，启动至今）。 */
size_t mem_guard_min_ever(void);

/** 本 profile 的硬地板（字节）。 */
size_t mem_guard_floor_bytes(void);

/**
 * 能否启动一个需要 need_bytes 连续内存的重操作。
 * 语义：largest >= max(need_bytes, floor)。
 *
 * **纯谓词，无副作用**：不触发回调、不改锁存、不分配、不打印。
 * 因此可在任意任务/日志路径/事务每一步前重复调用——配置事务的
 * Phase A（begin 前，floor 判定）与 Phase B（每步前复检）都依赖这一点：
 * 判定为 false 时由调用方决定"拒绝并返回 UNCHANGED"或"回滚"，本模块
 * 不替调用方执行任何动作。
 */
bool mem_guard_can_start(size_t need_bytes);

/**
 * 注册低内存回调（后注册覆盖先注册；传 NULL 注销）。
 * 回调只在 `mem_guard_poll()` 里触发，同一水位事件只触发一次；
 * 回弹到 floor + 2 KiB 以上后才允许下一次触发（迟滞）。
 */
void mem_guard_register_low_cb(mem_guard_low_cb_t cb);

/** 低频调用（建议 1 Hz，在 status_task 里）。检测水位并按需触发回调。 */
void mem_guard_poll(void);

/** 清除低水位锁存（测试/低内存事件人工解除用；正常路径不需要）。 */
void mem_guard_reset_latch(void);

/* ---- 任务栈 high-water 与报告编码（MSG_MEM_RPT = 0x20）---- */

/**
 * 记录本轮低频采样到的"最小任务栈剩余字节"（历史最小值，只降不升）。
 * 由 status_task 的 60 s 采样调用；单位统一为字节。
 * 传入 0 视为"尚未采样"的哨兵输入，是 no-op，防止测试/初始化污染指标。
 */
void mem_guard_set_min_stack_high_water(size_t bytes);

/** 读取已记录的最小任务栈剩余字节（未采样过时为 0）。 */
size_t mem_guard_min_stack_high_water(void);

/**
 * 编码 MemReport 到 buf，返回帧长；cap 不足或参数非法返回 0。
 * 字段：1=free_bytes 2=largest_bytes 3=min_ever_bytes
 *       4=min_task_stack_high_water_bytes 5=reserve_floor_bytes（全部 varint，单位字节）。
 *
 * **口径（2026-10-05 起）**：字段 1/2/3 全部是**内部 RAM** 水位，不含 PSRAM。
 * 修复前它们是 `MALLOC_CAP_8BIT`（s3p 上 = 内部 + PSRAM），运维在 s3p 上看到
 * 的是 8 MB 量级的“永远充裕”，看不到内部 RAM 的真实紧张程度。PSRAM 容量不在
 * 本报告内：它不参与任何门禁判决，且静态占用可从构建产物读出。
 *
 * 消息类型号：MSG_MEM_RPT 由 task-1(V3-2a) 从 0x20 顺延到 **0x21**（0x20 让给
 * DataBatch）；本模块只引用宏，不硬编码。
 *
 * 只编码不发送：发送由 main.c（持有 transport 依赖的一侧）完成。
 */
size_t mem_guard_encode_report(uint8_t *buf, size_t cap);

#ifdef __cplusplus
}
#endif

#endif /* MEM_GUARD_H */
