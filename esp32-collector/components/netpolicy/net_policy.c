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

write_outcome_t net_policy_classify_write(size_t requested, size_t written, bool hard_error)
{
    /* 顺序很重要：硬错误优先于任何"写了多少"的判断 ——
     * 部分写出后遇到 EPIPE，结论应当是 ERROR（重试无意义），不是 PARTIAL。 */
    if (hard_error) return WRITE_ERROR;
    if (written == 0 && requested > 0) return WRITE_NOTHING;
    if (written < requested) return WRITE_PARTIAL;
    return WRITE_COMPLETE;   /* 注意：written >= requested 都算完整（written 不该超过 requested） */
}

const char *net_policy_write_outcome_name(write_outcome_t w)
{
    switch (w) {
    case WRITE_COMPLETE: return "COMPLETE";
    case WRITE_PARTIAL:  return "PARTIAL";
    case WRITE_NOTHING:  return "NOTHING";
    case WRITE_ERROR:    return "ERROR";
    default:             return "UNKNOWN";
    }
}

bool net_policy_write_is_success(write_outcome_t w)
{
    /* 【只有】WRITE_COMPLETE 算成功。
     * D-10 的病根就是在这里放宽：旧代码用 written > 0 当成功。 */
    return w == WRITE_COMPLETE;
}
