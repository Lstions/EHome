/**
 * @file session.c
 * @brief 3.0 会话状态机实现
 */
#include "session.h"

#include <stdlib.h>
#include <string.h>

#include "link_rx_adapt.h"

struct session {
    session_config_t cfg;
    link_t          *link;
    link_tcp_ctx_t  *tcp;        /* link_tcp 的驱动上下文 */
    link_rx_binding_t *rx_bind;  /* 适配层绑定 */
    rx_pump_t       *pump;
    session_state_t  state;
    uint64_t         next_attempt_ms;  /* BACKOFF 到期时刻 */
    /* ⭐ 【自上次应用层握手以来】的失败次数 —— 退避表按它查。
     * 为什么不用 link_tcp_reconnect_attempt()：那个计数只在
     * **socket connect 软失败**时 ++；而设计 §4.2 的口径是
     * "自上次握手以来的尝试次数"。若每次 socket 都连得上、
     * 但应用层握手始终不成功，那个计数会一直是 0
     * ⇒ 退避永远是 1 秒 ⇒ **重连风暴**（正是本节要防的故障）。
     * 这条是 host 测试抓出来的：test_connect_success_does_not_reset_backoff
     * 打印 "退避计数必须递增（0 -> 0）"。 */
    uint32_t         attempt;
    session_stats_t  stats;
};

static const char *const s_names[] = {
    [SESSION_DOWN]           = "DOWN",
    [SESSION_WAIT_HANDSHAKE] = "WAIT_HANDSHAKE",
    [SESSION_READY]          = "READY",
    [SESSION_BACKOFF]        = "BACKOFF",
    [SESSION_FATAL]          = "FATAL",
};

const char *session_state_name(session_state_t s)
{
    if ((int)s < 0 || s > (int)SESSION_FATAL) return "UNKNOWN";
    return s_names[s];
}

session_t *session_create(const session_config_t *cfg)
{
    /* 必需参数缺一不可：不构造"看起来能用"的半成品（P1） */
    if (cfg == NULL || cfg->io == NULL || cfg->rx_buf == NULL ||
        cfg->rx_buf_cap == 0 || cfg->now_ms == NULL || cfg->on_msg == NULL) {
        return NULL;
    }

    session_t *s = (session_t *)calloc(1, sizeof(*s));
    if (s == NULL) return NULL;
    s->cfg = *cfg;

    link_tcp_config_t tcfg = { .io = cfg->io, .io_ctx = cfg->io_ctx };
    s->tcp = link_tcp_new(&tcfg);
    if (s->tcp == NULL) { free(s); return NULL; }

    s->link = link_create(link_tcp_driver(), s->tcp);
    if (s->link == NULL) { link_tcp_free(s->tcp); free(s); return NULL; }

    s->rx_bind = link_rx_binding_new(s->tcp);
    if (s->rx_bind == NULL) { link_destroy(s->link); link_tcp_free(s->tcp); free(s); return NULL; }

    s->pump = rx_pump_create(cfg->max_payload, link_rx_adapt_read, s->rx_bind,
                             cfg->on_msg, cfg->on_msg_ctx,
                             cfg->rx_buf, cfg->rx_buf_cap);
    if (s->pump == NULL) {
        link_rx_binding_free(s->rx_bind);
        link_destroy(s->link);
        link_tcp_free(s->tcp);
        free(s);
        return NULL;
    }

    s->state = SESSION_DOWN;
    return s;
}

void session_destroy(session_t *s)
{
    if (s == NULL) return;
    /* 逆序拆除：pump -> 适配 -> link -> 驱动上下文 */
    rx_pump_destroy(s->pump);
    link_rx_binding_free(s->rx_bind);
    link_destroy(s->link);
    link_tcp_free(s->tcp);
    free(s);
}

session_state_t session_state(const session_t *s)
{
    return (s == NULL) ? SESSION_FATAL : s->state;
}

uint32_t session_reconnect_attempt(const session_t *s)
{
    return (s == NULL) ? 0 : s->attempt;
}

void session_get_stats(const session_t *s, session_stats_t *out)
{
    if (out == NULL) return;
    if (s == NULL) { memset(out, 0, sizeof(*out)); return; }
    *out = s->stats;
}

/** 进入退避：按退避表 + 抖动算出下次尝试时刻。 */
static void enter_backoff(session_t *s)
{
    uint32_t jitter = (s->cfg.rand_permille != NULL) ? s->cfg.rand_permille() : 500u;
    /* 用**本层**的 attempt 查退避表（口径见结构体注释） */
    uint32_t delay = link_tcp_backoff_ms(s->attempt, jitter);
    s->next_attempt_ms = s->cfg.now_ms() + (uint64_t)delay;
    s->attempt++;                    /* 这一次失败计入"自上次握手以来" */
    s->state = SESSION_BACKOFF;
    s->stats.backoffs_entered++;
}

void session_note_handshake(session_t *s)
{
    if (s == NULL) return;
    /* ⭐ 唯一的重置点（设计 §4.2）。socket connect **不算**握手。 */
    link_tcp_note_handshake(s->tcp);   /* 同步把驱动层的计数也归零 */
    s->attempt = 0;                    /* ⭐ 本层计数同样归零（唯一重置点）*/
    s->stats.handshakes++;
    s->state = SESSION_READY;
}

/** 尝试建立链路。 */
static void do_connect(session_t *s)
{
    link_result_t r = link_open(s->link);
    if (r == LINK_SENT_FULL) {
        s->state = SESSION_WAIT_HANDSHAKE;
        s->stats.connects_ok++;
        return;
    }
    /* 硬失败（证书真不对 / 配置缺失）：**不盲目重试**，交给人工 */
    if (r == LINK_FATAL) {
        s->stats.connects_hard_fail++;
        s->state = SESSION_FATAL;
        return;
    }
    /* 软失败（网络 / 时间不可信）：退避重试 */
    s->stats.connects_soft_fail++;
    enter_backoff(s);
}

session_state_t session_poll(session_t *s, uint32_t *delivered_out)
{
    if (delivered_out != NULL) *delivered_out = 0;
    if (s == NULL) return SESSION_FATAL;

    switch (s->state) {
    case SESSION_FATAL:
        /* 需人工介入：**不动**。无限重试只会把真问题淹没在日志里。 */
        return s->state;

    case SESSION_BACKOFF:
        if (s->cfg.now_ms() < s->next_attempt_ms) return s->state;   /* 未到点 */
        s->state = SESSION_DOWN;
        /* 落到下面立刻尝试 */
        /* fall through */
        /* FALLTHRU */
    case SESSION_DOWN:
        do_connect(s);
        /* ⚠ 连上后**不**落穿到泵读：一次 poll 只做一件事。
         * 落穿会让 WAIT_HANDSHAKE 只瞬时存在（连上的同一次调用里就把数据也读了），
         * 状态不可观测、测试也难表达"连上但还没读"。
         * 调用方本来就是循环调用，下一次 poll 自然去泵。 */
        return s->state;

    case SESSION_WAIT_HANDSHAKE:
    case SESSION_READY: {
        uint32_t n = 0;
        rx_pump_result_t rr = rx_pump_step(s->pump, &n);
        if (n > 0) s->stats.msgs_delivered += n;
        if (delivered_out != NULL) *delivered_out = n;

        if (rr == RX_PUMP_CLOSED || rr == RX_PUMP_ERROR) {
            /* 链路掉了：进入退避，等下一次 link_open 重连。
             *
             * ⚠ 这里**不**调用任何 close：link 层**没有** link_close()
             *   （只有 link_destroy）。这在本路径上是安全的，因为：
             *     - link_tcp 在 read/send 失败时**自己**把 handle 置 NULL
             *       （且 tcp_close 对 NULL handle 安全）；
             *     - 于是下一次 link_open -> tcp_open 看到 handle==NULL，
             *       会真正重建连接。
             *   ⇒ 重连不依赖 session 主动 close。
             *
             * 但这是个**值得记下的接口缺口**：link 没有"主动断开单条连接"
             * 的能力，将来若有"主动换服务器/降级到别的传输"的需求，
             * 就得补 link_close()。 */
            s->stats.links_dropped++;
            enter_backoff(s);
        }
        return s->state;
    }

    default:
        return s->state;
    }
}
