/**
 * @file report_stats.h
 * @brief 上报路径的统计量 —— 中立组件（D-14：打破 msg_handler <-> bus_worker 依赖环）
 *
 * ## 为什么存在这个组件
 * 原先 msg_handler 为了在 PerformanceReport 里填三个字段，
 * 直接调用 bus_worker 的 getter（handler_data.c:263-266）：
 *     bus_worker_get_min_stack_watermark() / _get_report_drop_count() / _get_report_queue_high_water()
 * 而 bus_worker 又 REQUIRES msg_handler（需要 data_batch_codec 与 hello_get_server_caps）
 * ⇒ **唯一的组件依赖环**。它此前靠 ESP-IDF 按字典序重复解析静态库来容忍
 * （build.cmake:399），属于"能跑但结构错了"。
 *
 * ## 破环方式：把"被读取的统计量"放进双方都能依赖的中立处
 *     msg_handler ──> report_stats <── bus_worker
 * 数据由【生产者】写入（bus_worker），由【消费者】读取（msg_handler 的 handler_data.c），
 * 两者互不认识。这与 scheduler/get_queue_metrics() 的既有形态一致。
 *
 * ## 三个量的来源差异（决定了接口为何长这样）
 *   - drop / high_water：bus_worker 内部累加的计数器 ⇒ 直接搬过来；
 *   - min_stack_watermark：需要 bus_worker 的【任务句柄】按需计算
 *     （uxTaskGetStackHighWaterMark）⇒ 不能搬，改为由 bus_worker
 *     **注册一个 provider**，本组件只转发。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_REPORT_STATS_H
#define EHOME_REPORT_STATS_H

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/** 记录当前上报队列深度（本组件自行维护高水位）。由 producer 调用。 */
void report_stats_note_queue_depth(uint32_t queued);

/** 记录一次上报丢弃。 */
void report_stats_note_drop(void);

/** 清空所有统计（producer 在重开上报路径时调用）。 */
void report_stats_reset(void);

uint32_t report_stats_get_queue_high_water(void);
uint32_t report_stats_get_drop_count(void);

/**
 * 注册"最小栈余量"的提供者（bus_worker 在启动时注册）。
 * @param fn 返回最小栈余量（单位：word）；传 NULL 表示撤销注册。
 */
void report_stats_set_stack_watermark_provider(uint32_t (*fn)(void));

/**
 * 取最小栈余量。
 *
 * **未注册 provider 时返回 UINT32_MAX**，语义是"没有观测到任何低水位"——
 * 刻意**不返回 0**：0 在这个字段里读作"栈已耗尽"，会制造**假的栈告警**。
 * （同一判断在 D-07 出现过：本机状态混进设备健康量会产生假告警。）
 */
uint32_t report_stats_get_min_stack_watermark(void);

/** provider 是否已注册（供启动门禁断言 —— 别让"没接上"变成看不见的状态）。 */
bool report_stats_has_stack_watermark_provider(void);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_REPORT_STATS_H */
