/**
 * @file tls_guard.c
 * @brief mTLS 前置条件守卫实现（纯函数 + 计数）
 */
#include "tls_guard.h"

#include <string.h>

static const char *const s_fail_names[] = {
    [TLS_FAIL_NONE]           = "NONE",
    [TLS_FAIL_NETWORK]        = "NETWORK",
    [TLS_FAIL_CERT_UNTRUSTED] = "CERT_UNTRUSTED",
    [TLS_FAIL_CERT_EXPIRED]   = "CERT_EXPIRED",
    [TLS_FAIL_PROTOCOL]       = "PROTOCOL",
    [TLS_FAIL_CONFIG]         = "CONFIG",
};

const char *tls_failure_name(tls_failure_t f)
{
    if ((int)f < 0 || f >= TLS_FAIL_COUNT) return "UNKNOWN";
    return s_fail_names[f];
}

static const char *const s_action_names[] = {
    [TLS_ACTION_PROCEED]        = "PROCEED",
    [TLS_ACTION_SYNC_TIME_FIRST] = "SYNC_TIME_FIRST",
    [TLS_ACTION_RETRY_BACKOFF]  = "RETRY_BACKOFF",
    [TLS_ACTION_FATAL]          = "FATAL",
};

const char *tls_action_name(tls_action_t a)
{
    if ((int)a < 0 || a >= TLS_ACTION_COUNT) return "UNKNOWN";
    return s_action_names[a];
}

bool tls_guard_time_is_trusted(uint64_t now_epoch)
{
    /* 两端都判：只判下限会漏"时钟跑到很远的将来"，
     * 只判上限会漏"根本没校时"（本问题的主因）。 */
    return now_epoch >= TLS_GUARD_MIN_EPOCH && now_epoch <= TLS_GUARD_MAX_EPOCH;
}

/** 该失败是否属于"证书类"（时间不可信时会被误报的那一类）。 */
static bool is_cert_failure(tls_failure_t f)
{
    return f == TLS_FAIL_CERT_UNTRUSTED || f == TLS_FAIL_CERT_EXPIRED;
}

tls_action_t tls_guard_classify(bool time_trusted, tls_failure_t failure)
{
    if (failure == TLS_FAIL_NONE) return TLS_ACTION_PROCEED;

    /* ⭐ 时间不可信 + 证书类失败 ⇒ 先校时。
     * 绝不可报 FATAL：时间错时无法区分"证书真有问题"与"看起来有问题"，
     * 而后者只需校时即可自愈。 */
    if (!time_trusted && is_cert_failure(failure)) return TLS_ACTION_SYNC_TIME_FIRST;

    switch (failure) {
    case TLS_FAIL_NETWORK:
        return TLS_ACTION_RETRY_BACKOFF;   /* 与证书无关，退避即可 */
    case TLS_FAIL_CERT_UNTRUSTED:
    case TLS_FAIL_CERT_EXPIRED:
        /* 走到这里说明时间可信 ⇒ 证书是真的有问题，重试无用 */
        return TLS_ACTION_FATAL;
    case TLS_FAIL_PROTOCOL:
        /* 版本/套件谈不拢：重试不会改变结果，需人工介入 */
        return TLS_ACTION_FATAL;
    case TLS_FAIL_CONFIG:
        /* 本地没装证书/私钥/CA：重试无用 */
        return TLS_ACTION_FATAL;
    case TLS_FAIL_NONE:
    default:
        return TLS_ACTION_FATAL;
    }
}

static tls_guard_stats_t s_stats;

tls_action_t tls_guard_note(bool time_trusted, tls_failure_t failure)
{
    tls_action_t a = tls_guard_classify(time_trusted, failure);
    switch (a) {
    case TLS_ACTION_PROCEED:         s_stats.proceed++;         break;
    case TLS_ACTION_SYNC_TIME_FIRST: s_stats.sync_time_first++; break;
    case TLS_ACTION_RETRY_BACKOFF:   s_stats.retry_backoff++;   break;
    /* 故意不写 default：新增动作时编译器会提醒这里漏了计数（P3）。
     * 用 if 兜底以免 -Werror=switch 在别的动作上失败。 */
    default:                         s_stats.fatal++;          break;
    }
    return a;
}

void tls_guard_get_stats(tls_guard_stats_t *out)
{
    if (out == NULL) return;
    *out = s_stats;
}

void tls_guard_reset_stats(void)
{
    memset(&s_stats, 0, sizeof(s_stats));
}
