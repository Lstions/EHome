/**
 * @file sntp_mgr.c
 * @brief SNTP 时间同步管理器实现（尽力而为，不阻断 TLS）
 */
#include "sntp_mgr.h"

#include <stdlib.h>
#include <string.h>

/** 退避表：5s, 15s, 60s, 300s, 900s（末位为上限）。 */
static const uint32_t s_backoff_ms[SNTP_MGR_BACKOFF_STEPS] = {
    5000u, 15000u, 60000u, 300000u, 900000u,
};

struct sntp_mgr {
    sntp_mgr_config_t cfg;
    sntp_mgr_state_t  state;
    bool              net_up;
    uint64_t          started_at_ms;   /* WAITING 的起点 */
    uint64_t          next_try_ms;     /* BACKOFF 的到点时刻 */
    uint32_t          attempt;         /* 连续失败次数（用于查退避表） */
    sntp_mgr_stats_t  stats;
};

static const char *const s_names[] = {
    [SNTP_MGR_IDLE]     = "IDLE",
    [SNTP_MGR_WAITING]  = "WAITING",
    [SNTP_MGR_SYNCED]   = "SYNCED",
    [SNTP_MGR_BACKOFF]  = "BACKOFF",
    [SNTP_MGR_DISABLED] = "DISABLED",
};

const char *sntp_mgr_state_name(sntp_mgr_state_t s)
{
    if ((int)s < 0 || s > (int)SNTP_MGR_DISABLED) return "UNKNOWN";
    return s_names[s];
}

/** 服务器是否已配置（空串等同未配置）。 */
static bool has_server(const sntp_mgr_config_t *c)
{
    return c->server != NULL && c->server[0] != '\0';
}

sntp_mgr_t *sntp_mgr_create(const sntp_mgr_config_t *cfg)
{
    if (cfg == NULL || cfg->io == NULL || cfg->is_time_trusted == NULL) return NULL;

    sntp_mgr_t *m = (sntp_mgr_t *)calloc(1, sizeof(*m));
    if (m == NULL) return NULL;
    m->cfg = *cfg;
    if (m->cfg.wait_ms == 0) m->cfg.wait_ms = SNTP_MGR_DEFAULT_WAIT_MS;

    /* 未配置服务器：直接 DISABLED，**不**假装能同步。
     * 选 DISABLED 而不是 IDLE，是为了让"配置缺失"在状态里就看得出来。 */
    m->state = has_server(&m->cfg) ? SNTP_MGR_IDLE : SNTP_MGR_DISABLED;
    return m;
}

void sntp_mgr_destroy(sntp_mgr_t *m)
{
    free(m);
}

void sntp_mgr_network_up(sntp_mgr_t *m)
{
    if (m == NULL) return;
    m->net_up = true;
    if (m->state == SNTP_MGR_DISABLED) return;
    /* 网络起来了：若当前空闲，下一轮 poll 会发起 */
}

void sntp_mgr_network_down(sntp_mgr_t *m)
{
    if (m == NULL) return;
    m->net_up = false;
    if (m->state == SNTP_MGR_DISABLED) return;
    /* ⚠ 回到 IDLE，但**不重置退避计数**：
     * 断网不是"同步失败"，不该把退避重置成 0（否则网络抖动会导致
     * 每次恢复都从 5s 重来，反而更频繁地打 NTP）。 */
    m->state = SNTP_MGR_IDLE;
}

void sntp_mgr_request_now(sntp_mgr_t *m)
{
    if (m == NULL) return;
    m->stats.request_now++;
    if (m->state == SNTP_MGR_DISABLED) return;
    if (!m->net_up) return;   /* 没网时请求也没用，等网络 */
    /* 把退避**提前到点**：此时我们已知时间不对正在造成真实故障，
     * 值得立即再试一次（而不是等满 900s）。 */
    if (m->state == SNTP_MGR_BACKOFF) {
        m->next_try_ms = m->cfg.io->now_ms(m->cfg.io_ctx);
        m->state = SNTP_MGR_IDLE;
    }
}

/** 发起一次同步。 */
static void do_start(sntp_mgr_t *m)
{
    m->cfg.io->start(m->cfg.io_ctx, m->cfg.server);
    m->stats.starts++;
    m->started_at_ms = m->cfg.io->now_ms(m->cfg.io_ctx);
    m->state = SNTP_MGR_WAITING;
}

sntp_mgr_state_t sntp_mgr_poll(sntp_mgr_t *m, uint64_t *epoch_out)
{
    if (m == NULL) return SNTP_MGR_DISABLED;
    if (m->state == SNTP_MGR_DISABLED) return m->state;

    uint64_t epoch = 0;
    bool have_time = (m->cfg.io->get_time != NULL) &&
                     m->cfg.io->get_time(m->cfg.io_ctx, &epoch);
    if (have_time && epoch_out != NULL) *epoch_out = epoch;

    /* 无论处于哪一态，只要时间已经可信就进入 SYNCED。
     * 这样"由别处把时间弄对了"（例如将来加了别的校时途径）也能被识别到。 */
    if (have_time && m->cfg.is_time_trusted(epoch)) {
        if (m->state != SNTP_MGR_SYNCED) {
            m->stats.synced++;
            m->attempt = 0;          /* 成功 => 退避归零（唯一的重置点） */
        }
        m->state = SNTP_MGR_SYNCED;
        return m->state;
    }

    /* 网络未就绪：不发起（发起也没有用），等网络。 */
    if (!m->net_up) {
        if (m->state != SNTP_MGR_SYNCED) m->state = SNTP_MGR_IDLE;
        return m->state;
    }

    switch (m->state) {
    case SNTP_MGR_SYNCED:
        /* 曾经可信、现在又不可信了（时间被改回去？）—— 重新走流程 */
        m->state = SNTP_MGR_IDLE;
        /* fall through */
        /* FALLTHRU */
    case SNTP_MGR_IDLE:
        m->stats.skipped_no_net += 0;   /* 走到这里说明网络是就绪的 */
        do_start(m);
        return m->state;

    case SNTP_MGR_WAITING: {
        uint64_t elapsed = m->cfg.io->now_ms(m->cfg.io_ctx) - m->started_at_ms;
        if (elapsed >= m->cfg.wait_ms) {
            /* 超时：本轮失败，进退避。**不清零计数**，故退避逐步拉长。 */
            m->stats.timeouts++;
            uint32_t idx = (m->attempt < SNTP_MGR_BACKOFF_STEPS)
                           ? m->attempt : (SNTP_MGR_BACKOFF_STEPS - 1u);
            m->next_try_ms = m->cfg.io->now_ms(m->cfg.io_ctx) + s_backoff_ms[idx];
            m->attempt++;
            m->state = SNTP_MGR_BACKOFF;
        }
        return m->state;
    }

    case SNTP_MGR_BACKOFF:
        if (m->cfg.io->now_ms(m->cfg.io_ctx) >= m->next_try_ms) {
            do_start(m);
        }
        return m->state;

    default:
        return m->state;
    }
}

sntp_mgr_state_t sntp_mgr_state(const sntp_mgr_t *m)
{
    return (m == NULL) ? SNTP_MGR_DISABLED : m->state;
}

bool sntp_mgr_now(const sntp_mgr_t *m, uint64_t *epoch_out)
{
    if (m == NULL || epoch_out == NULL) return false;
    if (m->cfg.io == NULL || m->cfg.io->get_time == NULL) return false;
    return m->cfg.io->get_time(m->cfg.io_ctx, epoch_out);
}

void sntp_mgr_get_stats(const sntp_mgr_t *m, sntp_mgr_stats_t *out)
{
    if (out == NULL) return;
    if (m == NULL) { memset(out, 0, sizeof(*out)); return; }
    *out = m->stats;
}
