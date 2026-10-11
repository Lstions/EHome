/**
 * @file scheduler_health.h
 * @brief 设备健康计数的写入规则 —— 纯函数，宿主可测（D-07）
 *
 * 背景（D-07，2026-10-06）：
 *   sched_command_t.error_count 是【服务端读的唯一健康量】
 *   （handler_data.c 明确写着："cmd->error_count is THE counter the server reads"），
 *   handler_data.c 把它映射成 comm_status：>=3 -> FAULT，>0 -> TIMEOUT。
 *
 *   而 scheduler.c 在【本机 TX 队列满】时也把它 +1 ——
 *   于是"设备自己处理不过来"被上报成"现场传感器 TIMEOUT/FAULT"。
 *   100 Hz 下队列满必然发生（L-01c 实测 full=577~589），
 *   现场会看到一批【根本没坏】的传感器报故障，运维据此上门。
 *   源码注释自陈：同一不变量曾被破坏，导致【一次 7 天的现场静默】。
 *
 * 本文件把"哪些事件可以推动健康计数"变成【显式规则 + 可断言数据】，
 * 而不是散落在 scheduler.c 各处的 error_count++。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_SCHEDULER_HEALTH_H
#define EHOME_SCHEDULER_HEALTH_H

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/** 健康计数的上限（沿用既有实现：>100 截断为 100）。 */
#define SCHED_HEALTH_MAX 100u

typedef enum {
    /** 设备给出了正常响应 —— 连续错误清零。 */
    SCHED_HEALTH_DEVICE_SUCCESS = 0,
    /** 设备层面的失败（未响应 / CRC 错 / 帧非法）—— 计数 +1。 */
    SCHED_HEALTH_DEVICE_FAILURE,
    /**
     * 【本机】背压：TX 队列满、没有余量投递。
     *
     * ⚠ 这一类【不得】推动健康计数 —— 它不是设备的状态，是【我们自己的】状态。
     *    混进去就会把"设备处理不过来"报成"现场传感器故障"（D-07）。
     *    该事件本身【不是静默丢弃】：它已经计入
     *    s_queue_metrics.sample_rejected[] / sample_skipped[]，
     *    并由 handler_data.c 上报（见该文件 277-279 行）——
     *    ⇒ 可观测性没有损失，损失的只是【错误的标签】。
     */
    SCHED_HEALTH_LOCAL_BACKPRESSURE,
} sched_health_event_t;

/**
 * 计算健康计数在某个事件之后的新值。
 *
 * 规则（唯一来源，P4）：
 *   DEVICE_SUCCESS          -> 0
 *   DEVICE_FAILURE          -> min(current + 1, SCHED_HEALTH_MAX)
 *   LOCAL_BACKPRESSURE      -> current（【不变】）
 */
uint32_t sched_health_next(uint32_t current, sched_health_event_t event);

/**
 * 该事件是否属于"设备层面的结果"。
 * 只有设备层面的结果才可以推动上报给服务端的健康量。
 */
bool sched_health_event_is_device_outcome(sched_health_event_t event);

/** 事件名（日志/测试用；避免名字在多处各写一遍）。 */
const char *sched_health_event_name(sched_health_event_t event);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_SCHEDULER_HEALTH_H */
