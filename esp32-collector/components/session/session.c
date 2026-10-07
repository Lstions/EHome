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

link_result_t session_send(session_t *s, const uint8_t *frame, size_t len,
                           size_t *progress)
{
    /* 参数守卫只保留一条：对象在不在。
     *
     * 其余守卫（frame==NULL / len==0 / progress==NULL / *progress > len）
     * **刻意不在这里重复** —— link_send 已经逐条判过，且判法一致（都返回
     * LINK_FATAL）。本层再抄一遍就是 P4 说的"同一语义两处定义"：将来 link
     * 改了判法，这里会静默地与它不一致。
     *
     * 为什么单独判 s == NULL：我们连 s->link 都取不到，无法委托；
     * 且 progress 也不能被写回（不知道它是谁的）。如实返回 FATAL，
     * 而不是假装推进了进度。 */
    if (s == NULL) return LINK_FATAL;

    /* ⚠⚠ 这里**没有** state 门控 —— 这是本函数最重要的一行"不存在"。
     *
     * 加一行 "state 不是 READY 就返回 NOT_READY" 看起来更安全，实则会造出
     * 一条**功能死锁**：设备的第一条 Hello 就是在 WAIT_HANDSHAKE 期间发的
     * （"链路已通，等应用层握手"），而 READY **只能**由
     * session_note_handshake()（收到 HelloAck）进入 —— Hello 发不出，就永远
     * 收不到 HelloAck。⇒ 3.0 链路永远停在 WAIT_HANDSHAKE、完全不可用，
     * 而三个 profile 构建与可达性门禁全都是绿的（本项目最怕的形态）。
     * host_tests/session_send_tests.c 有专门用例钉住这条：把门控加回去即变红。
     *
     * 另外"能不能写出去"的权威来源是**驱动**而非本层（P4）；
     * link.h 的"为什么不预检 is_ready"（TOCTOU、结果即决策依据）同理。
     * 未连接时驱动会如实返回 LINK_NOT_READY，不需要本层替它下结论。 */
    return link_send(s->link, frame, len, progress);
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
