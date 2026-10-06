/**
 * @file net_policy.c
 * @brief 网络决策纯函数实现（无状态、无副作用、无 IDF 依赖）
 */
#include "net_policy.h"

bool net_policy_should_start_tcp(bool tcp_configured, bool tcp_connected, bool wifi_connected)
{
    /* 三个条件缺一不可。写成显式合取而不是多个 return，
     * 是为了让"少了哪个条件"在审查与变异自证里都一目了然。 */
    return tcp_configured && !tcp_connected && wifi_connected;
}

bool net_policy_tcp_start_overdue(bool tcp_configured, bool tcp_connected,
                                  uint32_t elapsed_ms, uint32_t deadline_ms)
{
    if (!tcp_configured) return false;   /* 没配置就无所谓逾期 */
    if (tcp_connected)   return false;   /* 连上了就不告警 */
    return elapsed_ms >= deadline_ms;
}
