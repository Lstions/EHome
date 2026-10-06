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
};

static const char *const s_result_names[LINK_RESULT_COUNT] = {
    [LINK_SENT]            = "SENT",
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
    if (l->stats.ready && l->drv->close != NULL) {
        l->drv->close(l->ctx);
    }
    free(l);
}

/** 打开链路（骨架阶段的最小实现：调用驱动 open 并把结果映射进 stats）。 */
link_result_t link_open(link_t *l)
{
    if (l == NULL) return LINK_FATAL;
    link_result_t r = l->drv->open(l->ctx);
    if (r == LINK_SENT) {
        l->stats.ready = true;
        l->stats.mtu = l->drv->mtu(l->ctx);
    }
    return r;
}

link_result_t link_send(link_t *l, const uint8_t *frame, size_t len)
{
    /* 规则 1：参数错 -> FATAL（不是背压；调用方不该退避重试） */
    if (l == NULL || l->drv == NULL || frame == NULL || len == 0) {
        if (l != NULL) l->stats.tx_fatal++;
        return LINK_FATAL;
    }

    /* 规则 2：发出【前】校验契约（P2）—— 这是 R1 类缺陷的根治点。
     * 关键：mtu 不足时【绝不调用】drv->send，避免"发出去了=成功了"的假象。 */
    const uint32_t mtu = l->drv->mtu(l->ctx);
    if (mtu == 0 || len > (size_t)mtu) {
        l->stats.tx_too_big++;
        return LINK_PAYLOAD_TOO_BIG;
    }

    /* 规则 3：未就绪 -> NOT_READY（同样不调用 send） */
    if (!l->drv->is_ready(l->ctx)) {
        l->stats.ready = false;
        l->stats.tx_not_ready++;
        return LINK_NOT_READY;
    }

    /* 规则 4：原样转发，【不压平】结果（D-01 的病根） */
    link_result_t r = l->drv->send(l->ctx, frame, len);

    /* 规则 5：每条路径都计数（P3） */
    switch (r) {
    case LINK_SENT:            l->stats.tx_sent++;         break;
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
