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
    [TLS_FAIL_UNKNOWN]        = "UNCLASSIFIED",   /* 故意不叫 UNKNOWN：留给越界兜底 */
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
    case TLS_FAIL_UNKNOWN:
        /* ⭐ 认不出的错误按**可重试**处理，不按永久失败。
         * 理由：误判成 FATAL 会让设备再也不回来（不可逆）；
         * 误判成可重试最坏只是"一直退避重试"（可观测、可人工介入）。
         * 两害相权，取其可逆者。 */
        return TLS_ACTION_RETRY_BACKOFF;
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

/* ============================================================
 * 错误码归约
 * ============================================================ */

bool tls_failure_is_time_related(tls_failure_t f)
{
    return f == TLS_FAIL_CERT_EXPIRED;
}

tls_failure_t tls_guard_reduce_error(int type, int code, int cert_flags)
{
    (void)code;   /* 目前只按 type + flags 分类；code 留给将来细分 */

    /* --- 证书标志位类型：这是**唯一**能可靠区分"时间问题"与"信任问题"的来源 --- */
    if (type == TLS_ERGTYPE_MBEDTLS_CERT_FLAGS ||
        type == TLS_ERGTYPE_CUSTOM_STACK_CERT_FLAGS) {
        if (cert_flags == 0) {
            /* 报了 CERT_FLAGS 却没有位 —— 自相矛盾，不猜 */
            return TLS_FAIL_UNKNOWN;
        }
        /* ⭐ 时效位（EXPIRED / FUTURE）才是"可能就是没校时"的信号。
         * FUTURE 正是 now(1970) < notBefore(2026) 时置的那一位。 */
        if ((cert_flags & TLS_CERTFLAG_TIME_RELATED) != 0) return TLS_FAIL_CERT_EXPIRED;
        return TLS_FAIL_CERT_UNTRUSTED;
    }

    /* --- 本地配置类：设备自己就没装好，重试永远不会好 --- */
    if (type == TLS_ERGTYPE_ESP) {
        /* esp_err_t 的配置类取值：
         *   ESP_ERR_INVALID_ARG   0x102
         *   ESP_ERR_INVALID_STATE 0x103
         *   ESP_ERR_NOT_FOUND     0x105
         * 这些出现在 TLS 建立阶段基本都意味着"参数/状态/文件不对"。 */
        if (code == 0x102 || code == 0x103 || code == 0x105) return TLS_FAIL_CONFIG;
        return TLS_FAIL_NETWORK;   /* 其余 ESP 层（连接/内存）按网络类处理 */
    }

    /* --- 系统层（errno）与 mbedTLS 一般错误：归为网络/协议 ---
     * 注意：mbedTLS 的错误码负值里也有"证书"类（如 MBEDTLS_ERR_SSL_BAD_CERTIFICATE
     * = -0x7A00），但**它不带标志位** ⇒ 无法区分时间/信任。
     * 若把它直接判成 CERT_EXPIRED，会把"真的不受信"说成"可能没校时"；
     * 若判成 CERT_UNTRUSTED，又可能掩盖"其实没校时"。
     * ⇒ 保守归为 UNKNOWN（可重试 + 可观测），比猜错更安全。 */
    if (type == TLS_ERGTYPE_SYSTEM) return TLS_FAIL_NETWORK;
    if (type == TLS_ERGTYPE_MBEDTLS) return TLS_FAIL_UNKNOWN;

    return TLS_FAIL_UNKNOWN;
}


/* ============================================================
 * 以 esp_tls 真实输入为准的归约
 * ============================================================ */

tls_failure_t tls_guard_reduce_esp_error(unsigned last_error,
                                         int esp_tls_code,
                                         int cert_flags)
{
    /* 1) 证书标志位优先 —— 唯一能区分"时间问题 vs 信任问题"的信号 */
    if (cert_flags != 0) {
        return tls_guard_reduce_error(TLS_ERGTYPE_MBEDTLS_CERT_FLAGS,
                                      esp_tls_code, cert_flags);
    }

    /* 2) 网络码段 */
    if (last_error >= TLS_ESP_ERR_NET_FIRST && last_error <= TLS_ESP_ERR_NET_LAST) {
        /* 例外：安全元件失败属本地配置/硬件问题，退避重试无用 */
        if (last_error == TLS_ESP_ERR_SE_FAILED) return TLS_FAIL_CONFIG;
        return TLS_FAIL_NETWORK;
    }

    /* 3) mbedtls 码段（0x8010+）：只能知道"mbedtls 层出错"，
     *    无 flags 就分不出是不是证书 —— 不猜（见 tls_guard_reduce_error 的说明）。*/
    /* 4) 其它 */
    return TLS_FAIL_UNKNOWN;
}
