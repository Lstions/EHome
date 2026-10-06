/**
 * @file transport_sel.c
 * @brief 传输选择策略实现（纯策略，不管怎么连）
 */
#include "transport_sel.h"

#include <stdlib.h>
#include <string.h>

struct tsel {
    tsel_config_t cfg;
    tsel_which_t  current;
    uint32_t      consecutive_fail;   /* TCP 连续失败计数 */
    uint32_t      consecutive_ok;     /* TCP 连续成功计数（兜底期间用）*/
    tsel_stats_t  stats;
};

static const char *const s_which[] = {
    [TSEL_TCP]  = "TCP",
    [TSEL_MQTT] = "MQTT",
};

const char *tsel_which_name(tsel_which_t w)
{
    if ((int)w < 0 || w > (int)TSEL_MQTT) return "UNKNOWN";
    return s_which[w];
}

static const char *const s_reason[] = {
    [TSEL_REASON_TCP_HEALTHY]  = "TCP_HEALTHY",
    [TSEL_REASON_TCP_FAILED_N] = "TCP_FAILED_N",
    [TSEL_REASON_STAY_MQTT]    = "STAY_MQTT",
    [TSEL_REASON_BACK_TO_TCP]  = "BACK_TO_TCP",
    [TSEL_REASON_NO_FALLBACK]  = "NO_FALLBACK",
};

const char *tsel_reason_name(tsel_reason_t r)
{
    if ((int)r < 0 || r > (int)TSEL_REASON_NO_FALLBACK) return "UNKNOWN";
    return s_reason[r];
}

tsel_t *tsel_create(const tsel_config_t *cfg)
{
    /* 阈值 0 无意义：那会"每次都切"，等于没有防抖。
     * **拒绝**而不是偷偷兜底成 1 —— 静默改配置会让调用方以为设了 0 生效。 */
    if (cfg == NULL || cfg->tcp_fail_threshold == 0 || cfg->tcp_recover_success == 0) {
        return NULL;
    }
    tsel_t *s = (tsel_t *)calloc(1, sizeof(*s));
    if (s == NULL) return NULL;
    s->cfg = *cfg;
    s->current = TSEL_TCP;      /* 起点永远是主通道 */
    return s;
}

void tsel_destroy(tsel_t *s)
{
    free(s);
}

tsel_which_t tsel_current(const tsel_t *s)
{
    return (s == NULL) ? TSEL_TCP : s->current;
}

tsel_which_t tsel_poll(tsel_t *s, const tsel_input_t *in, tsel_reason_t *reason_out)
{
    if (s == NULL || in == NULL) {
        if (reason_out != NULL) *reason_out = TSEL_REASON_TCP_HEALTHY;
        return TSEL_TCP;
    }
    tsel_reason_t reason = TSEL_REASON_TCP_HEALTHY;

    /* ---- 先更新"连续失败/成功"计数 ---- */
    if (in->tcp_failed) {
        s->consecutive_fail++;
        s->consecutive_ok = 0;
    } else if (in->tcp_ready || in->tcp_succeeded) {
        /* 注意：**只有真正 READY 才算成功**，不是"socket 连上"。
         * 与 session/link 的口径一致（设计 §4.2：握手才算）。 */
        s->consecutive_ok++;
        /* ⭐ 成功必须把**连续失败计数归零**。
         * 这是 host 测试抓出来的：我原来只在失败分支清 ok，
         * 成功分支忘了清 fail ⇒ "败2 → 成1 → 败1" 会累加成 3 而**误切**。
         * 后果正是本模块要防的那种：零散失败被当成"连续失败"。 */
        s->consecutive_fail = 0;
    }

    switch (s->current) {
    case TSEL_TCP:
        if (s->consecutive_ok > 0 || in->tcp_ready) {
            reason = TSEL_REASON_TCP_HEALTHY;
            break;                                  /* 健康，不动 */
        }
        if (s->consecutive_fail >= s->cfg.tcp_fail_threshold) {
            if (s->cfg.allow_mqtt_fallback) {
                s->current = TSEL_MQTT;
                s->stats.switched_to_mqtt++;
                reason = TSEL_REASON_TCP_FAILED_N;
            } else {
                /* 兜底被禁用（P3 之后）：**继续留在 TCP**，
                 * 绝不偷偷用兜底 —— 否则 P3 的目标（拆掉 MQTT）永远达不到。 */
                s->stats.no_fallback_hits++;
                reason = TSEL_REASON_NO_FALLBACK;
            }
            break;
        }
        reason = TSEL_REASON_TCP_HEALTHY;           /* 未到阈值：继续观察 */
        break;

    case TSEL_MQTT:
        if (in->tcp_ready && s->consecutive_ok >= s->cfg.tcp_recover_success) {
            s->current = TSEL_TCP;
            /* 回切后要重新攒阈值。
             *
             * ⚠ host 测试的 M114 证明：删掉本行**可观察行为不变**
             * （即"等价变异"equivalent mutant）。原因：
             *   能走到这里的前提是本轮 **in->tcp_ready**，
             *   而每次 ready/succeeded 都会在上方"成功分支"把
             *   consecutive_fail 置 0。∴ 本行永远只能把 0 再置 0。
             *
             * 为何仍保留：它把"回切后计数归零"这个**意图**
             * 写在回切处，不依赖"上方恰好也清了"这个隐式事实。
             * 但它**不是**真正负载的行 —— 读者不应把它当成功能保障。
             * （真正负载的是上方成功分支的归零，由 M111 看住。） */
            s->consecutive_fail = 0;
            s->stats.switched_back++;
            reason = TSEL_REASON_BACK_TO_TCP;
        } else {
            reason = TSEL_REASON_STAY_MQTT;
        }
        break;

    default:
        break;
    }

    if (s->current == TSEL_TCP) s->stats.tcp_ok++;
    s->stats.consecutive_fail = s->consecutive_fail;
    if (reason_out != NULL) *reason_out = reason;
    return s->current;
}

void tsel_get_stats(const tsel_t *s, tsel_stats_t *out)
{
    if (out == NULL) return;
    if (s == NULL) { memset(out, 0, sizeof(*out)); return; }
    *out = s->stats;
}
