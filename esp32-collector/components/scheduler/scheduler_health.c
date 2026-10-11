/**
 * @file scheduler_health.c
 * @brief 健康计数规则实现（纯函数：无状态、无副作用、无 IDF 依赖）
 */
#include "scheduler_health.h"

uint32_t sched_health_next(uint32_t current, sched_health_event_t event)
{
    switch (event) {
    case SCHED_HEALTH_DEVICE_SUCCESS:
        return 0u;
    case SCHED_HEALTH_DEVICE_FAILURE:
        return (current >= SCHED_HEALTH_MAX) ? SCHED_HEALTH_MAX : (current + 1u);
    case SCHED_HEALTH_LOCAL_BACKPRESSURE:
        /* D-07 的核心：本机背压【不改变】设备健康计数。
         * 写成显式分支（而非 default 吞掉），是为了让"这一类被有意排除"
         * 在审查与变异自证里都一目了然。 */
        return current;
    default:
        /* 未知事件：不猜测、不递增 —— 宁可不动，也不要把无关事件算成设备故障。 */
        return current;
    }
}

bool sched_health_event_is_device_outcome(sched_health_event_t event)
{
    return event == SCHED_HEALTH_DEVICE_SUCCESS || event == SCHED_HEALTH_DEVICE_FAILURE;
}

const char *sched_health_event_name(sched_health_event_t event)
{
    switch (event) {
    case SCHED_HEALTH_DEVICE_SUCCESS:       return "DEVICE_SUCCESS";
    case SCHED_HEALTH_DEVICE_FAILURE:       return "DEVICE_FAILURE";
    case SCHED_HEALTH_LOCAL_BACKPRESSURE:   return "LOCAL_BACKPRESSURE";
    default:                                return "UNKNOWN";
    }
}
