/**
 * @file link.c
 * @brief 上行链路实现 —— 状态只在本文件内迁移（P4）
 */
#include "link.h"

#include <stdlib.h>
#include <string.h>

struct link {
    const link_driver_t *drv;
    void                *ctx;
    link_stats_t         stats;
    /* `opened` 与 stats.ready 是两回事：
     *   - opened  = 生命周期事实（open 成功过 ⇒ destroy 要调 close），
     *              由本文件自己维护，**不被任何观测刷新**；
     *   - stats.ready = 诊断快照，随时可能变。
     * 原先 link_destroy 用 stats.ready 决定要不要 close() —— 那会让
     * "一次诊断刷新"顺手改变生命周期行为（把观测量当控制量用）。
     * 这是 P1/P4 的同类病：一个量只该有一个语义。 */
    bool                 opened;
};

static const char *const s_result_names[LINK_RESULT_COUNT] = {
    [LINK_SENT_FULL]       = "SENT_FULL",
    [LINK_SENT_PARTIAL]    = "SENT_PARTIAL",
    [LINK_NOT_READY]       = "NOT_READY",
    [LINK_BACKPRESSURE]    = "BACKPRESSURE",
    [LINK_PAYLOAD_TOO_BIG] = "PAYLOAD_TOO_BIG",
    [LINK_FATAL]           = "FATAL",
};

const char *link_result_name(link_result_t r)
{
    if ((int)r < 0 || r >= LINK_RESULT_COUNT) return "UNKNOWN";
    return s_result_names[r];
}

bool link_result_is_error(link_result_t r)
{
    switch (r) {
    case LINK_SENT_FULL:
    case LINK_SENT_PARTIAL:
    case LINK_BACKPRESSURE:
    case LINK_NOT_READY:
        /* 都不是错误：成功 / 进行中 / 稍后重试 / 等待就绪。
         * PARTIAL 尤其不是错误 —— 它是"还要接着写"；当成错误会让调用方
         * 丢弃已上线的字节，或重发而污染 TCP 流（D-30）。 */
        return false;
    case LINK_PAYLOAD_TOO_BIG:
    case LINK_FATAL:
    default:
        return true;
    }
}

link_t *link_create(const link_driver_t *drv, void *drv_ctx)
{
    if (drv == NULL) return NULL;
    /* 五个必需函数缺一不可 —— 宁可构造失败，也不要运行期静默降级（D-06 的教训）。 */
    if (drv->open == NULL || drv->close == NULL || drv->send == NULL ||
        drv->mtu == NULL || drv->is_ready == NULL) {
        return NULL;
    }
    link_t *l = (link_t *)calloc(1, sizeof(*l));
    if (l == NULL) return NULL;
    l->drv = drv;
    l->ctx = drv_ctx;
    l->stats.ready = false;
    l->stats.mtu = 0;
    return l;
}

void link_destroy(link_t *l)
{
    if (l == NULL) return;
    if (l->opened) {
        l->drv->close(l->ctx);
    }
    free(l);
}

/** 打开链路（骨架阶段的最小实现：调用驱动 open 并把结果映射进 stats）。 */
link_result_t link_open(link_t *l)
{
    if (l == NULL) return LINK_FATAL;
    link_result_t r = l->drv->open(l->ctx);
    if (r == LINK_SENT_FULL) {
        l->opened = true;
        l->stats.ready = true;
        l->stats.mtu = l->drv->mtu(l->ctx);
    }
    return r;
}

link_result_t link_send(link_t *l, const uint8_t *frame, size_t len,
                        size_t *progress)
{
    /* 规则 1：参数错 -> FATAL（不是背压；调用方不该退避重试） */
    if (l == NULL || l->drv == NULL || frame == NULL || len == 0 ||
        progress == NULL) {
        if (l != NULL) l->stats.tx_fatal++;
        return LINK_FATAL;
    }
    /* 进度越界 = 调用方违约（把别的帧的进度传进来了）。不猜、不夹取。 */
    if (*progress > len) {
        l->stats.tx_fatal++;
        return LINK_FATAL;
    }
    /* 已经写完了：幂等返回，不再向线上写任何字节。
     * 这条让调用方的重试循环天然安全（重试一个已完成的帧不会重复写）。 */
    if (*progress == len) {
        return LINK_SENT_FULL;
    }

    /* 规则 2：发出【前】校验契约（P2）—— 这是 R1 类缺陷的根治点。
     * 关键：mtu 不足时【绝不调用】drv->send，避免"发出去了=成功了"的假象。 */
    const uint32_t mtu = l->drv->mtu(l->ctx);
    if (mtu == 0 || len > (size_t)mtu) {
        l->stats.tx_too_big++;
        return LINK_PAYLOAD_TOO_BIG;
    }

    /* 规则 3（2026-10-06 修正）：**不**做 is_ready 预检。
     *
     * 原实现先 !is_ready() 判断再 send —— 那正是设计文档 §1.2 判为
     * "缺陷①：调用方须'先查再发' = TOCTOU" 的形态，也与本文件
     * "P1：不做'先查再发'，结果即决策依据" 自相矛盾。
     *
     * 为什么预检是错的（不只是风格问题）：
     *   - 查与发之间链路可以变化 ⇒ 预检通过不代表 send 会成功，
     *     预检失败也不代表 send 会失败 —— 它**不能**替代结果；
     *   - 它把"未就绪"变成 link 层的判断，而"未就绪"的权威来源
     *     是驱动（它知道自己为什么没就绪）⇒ 语义被复制到两处（P4）；
     *   - 省下的那次 send 调用没有价值：驱动本来就在未就绪时
     *     立刻返回 NOT_READY（mqtt 驱动就是这么做的）。
     *
     * 现在"未就绪"只有一个来源：驱动 send 的返回值（规则 4）。
     * `stats.ready` 退回它本来的角色 —— **只读观测，不参与决策**。
     * 为避免"指标看不到就绪态"，仍在发送后刷新一次快照（规则 5）。 */
    const size_t want_from = *progress;       /* 本次从这一字节开始写 */
    size_t wrote = 0;
    link_result_t r = l->drv->send(l->ctx, frame + want_from, len - want_from, &wrote);

    /* 驱动契约校验：写出的字节数不能超过本次请求量（超出即驱动违约）。
     * 不静默夹取 —— 夹取会掩盖驱动缺陷，而这类缺陷会污染流。 */
    if (wrote > len - want_from) {
        l->stats.tx_driver_error++;
        return LINK_FATAL;
    }
    /* 进度只前进，不后退（重试同一个帧时 progress 保持不变是正确的） */
    *progress = want_from + wrote;

    /* 规则 5：每条路径都计数（P3），并刷新只读快照 */
    l->stats.ready = l->drv->is_ready(l->ctx);
    switch (r) {
    case LINK_SENT_FULL:       l->stats.tx_sent_full++;    break;
    case LINK_SENT_PARTIAL:    l->stats.tx_sent_partial++; break;
    case LINK_BACKPRESSURE:    l->stats.tx_backpressure++; break;
    case LINK_NOT_READY:       l->stats.tx_not_ready++;    break;
    case LINK_PAYLOAD_TOO_BIG: l->stats.tx_too_big++;      break;
    case LINK_FATAL:           l->stats.tx_fatal++;        break;
    default:                   l->stats.tx_driver_error++; break; /* 驱动违约，可见 */
    }
    return r;
}

void link_get_stats(const link_t *l, link_stats_t *out)
{
    if (l == NULL || out == NULL) return;
    *out = l->stats;   /* 结构体一次拷贝 = 一致快照 */
}
