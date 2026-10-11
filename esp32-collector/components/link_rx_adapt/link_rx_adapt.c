/**
 * @file link_rx_adapt.c
 * @brief link_tcp_read -> rx_read_fn_t 翻译实现
 */
#include "link_rx_adapt.h"

#include <stdlib.h>
#include <string.h>

struct link_rx_binding {
    link_tcp_ctx_t *tcp;
};

static link_rx_adapt_stats_t s_stats;

void link_rx_adapt_get_stats(link_rx_adapt_stats_t *out)
{
    if (out == NULL) return;
    *out = s_stats;
}

void link_rx_adapt_reset_stats(void)
{
    memset(&s_stats, 0, sizeof(s_stats));
}

int link_rx_adapt_result(link_read_result_t r, size_t n)
{
    switch (r) {
    case LINK_READ_DATA:
        /* ⭐ **必须返回 n，而不是 (int)r**。
         * (int)r 会得到 0，而 0 在 rx_pump 那里是"暂无数据" ——
         * 读到的数据会被静默丢掉，且计数上只体现为 again++。 */
        if (n == 0) return RX_IO_ERROR;              /* 上游违约：不伪装成暂无数据 */
        if (n > 0x7FFFFFFFu) return RX_IO_ERROR;     /* 溢出保护：不静默截断 */
        return (int)n;
    case LINK_READ_AGAIN:
        return RX_IO_AGAIN;
    case LINK_READ_CLOSED:
        return RX_IO_CLOSED;
    case LINK_READ_NOT_READY:
        /* 与 CLOSED 对调用方的动作相同（重建、不算故障）。
         * 区分信息留在本层计数里（not_ready vs closed）。 */
        return RX_IO_CLOSED;
    case LINK_READ_FATAL:
    default:
        return RX_IO_ERROR;
    }
}

link_rx_binding_t *link_rx_binding_new(link_tcp_ctx_t *tcp)
{
    if (tcp == NULL) return NULL;   /* 不构造半成品对象 */
    link_rx_binding_t *b = (link_rx_binding_t *)calloc(1, sizeof(*b));
    if (b == NULL) return NULL;
    b->tcp = tcp;
    return b;
}

void link_rx_binding_free(link_rx_binding_t *b)
{
    free(b);
}

int link_rx_adapt_read(void *ctx, uint8_t *buf, size_t cap)
{
    link_rx_binding_t *b = (link_rx_binding_t *)ctx;
    if (b == NULL || b->tcp == NULL || buf == NULL || cap == 0) return RX_IO_ERROR;

    size_t n = 0;
    link_read_result_t r = link_tcp_read(b->tcp, buf, cap, &n);

    switch (r) {
    case LINK_READ_DATA:      s_stats.data++;      break;
    case LINK_READ_AGAIN:     s_stats.again++;     break;
    case LINK_READ_CLOSED:    s_stats.closed++;    break;
    case LINK_READ_NOT_READY: s_stats.not_ready++; break;
    default:                  s_stats.fatal++;     break;
    }

    return link_rx_adapt_result(r, n);
}
