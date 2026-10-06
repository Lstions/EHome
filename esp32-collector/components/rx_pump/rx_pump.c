/**
 * @file rx_pump.c
 * @brief 接收泵实现：读字节流 -> 定界 -> 交付消息
 */
#include "rx_pump.h"

#include <stdlib.h>
#include <string.h>

struct rx_pump {
    wire_delim_t *delim;
    rx_read_fn_t  read_fn;
    void         *read_ctx;
    rx_msg_cb_t   cb;
    void         *cb_ctx;
    uint8_t      *read_buf;
    size_t        read_buf_cap;
    rx_pump_stats_t stats;
};

static const char *const s_names[] = {
    [RX_PUMP_IDLE]      = "IDLE",
    [RX_PUMP_DELIVERED] = "DELIVERED",
    [RX_PUMP_CLOSED]    = "CLOSED",
    [RX_PUMP_ERROR]     = "ERROR",
    [RX_PUMP_FATAL]     = "FATAL",
};

const char *rx_pump_result_name(rx_pump_result_t r)
{
    if ((int)r < 0 || r > (int)RX_PUMP_FATAL) return "UNKNOWN";
    return s_names[r];
}

rx_pump_t *rx_pump_create(uint32_t max_payload,
                          rx_read_fn_t read_fn, void *read_ctx,
                          rx_msg_cb_t cb, void *cb_ctx,
                          uint8_t *read_buf, size_t read_buf_cap)
{
    /* 参数校验：缺任何一个都**不**构造半成品对象（P1） */
    if (read_fn == NULL || cb == NULL || read_buf == NULL || read_buf_cap == 0) return NULL;

    rx_pump_t *p = (rx_pump_t *)calloc(1, sizeof(*p));
    if (p == NULL) return NULL;
    p->delim = wire_delim_create(max_payload);
    if (p->delim == NULL) { free(p); return NULL; }
    p->read_fn = read_fn;
    p->read_ctx = read_ctx;
    p->cb = cb;
    p->cb_ctx = cb_ctx;
    p->read_buf = read_buf;
    p->read_buf_cap = read_buf_cap;
    return p;
}

void rx_pump_destroy(rx_pump_t *p)
{
    if (p == NULL) return;
    wire_delim_destroy(p->delim);
    free(p);
}

void rx_pump_get_stats(const rx_pump_t *p, rx_pump_stats_t *out)
{
    if (out == NULL) return;
    if (p == NULL) { memset(out, 0, sizeof(*out)); return; }
    *out = p->stats;
}

/**
 * 从 payload 指针反推头。
 * 定界器保证 payload 前面紧邻 12 B 头（零拷贝布局），且头已合法。
 */
static bool msg_from_payload(const uint8_t *payload, size_t plen, rx_msg_t *out)
{
    wire_header_t h;
    wire_result_t wr = wire_decode_header(payload - WIRE_HEADER_BYTES,
                                          (size_t)WIRE_HEADER_BYTES + plen, &h);
    if (wr != WIRE_OK) return false;
    out->ver = h.ver;
    out->type = h.type;
    out->flags = h.flags;
    out->seq = h.seq;
    out->payload_len = h.payload_len;
    out->payload = payload;
    return true;
}

static void count_delim_error(rx_pump_stats_t *st, wire_delim_result_t dr)
{
    switch (dr) {
    case WIRE_DELIM_ERR_CRC:       st->crc_errors++; break;
    case WIRE_DELIM_ERR_MALFORMED: st->malformed++;  break;
    case WIRE_DELIM_ERR_TOO_LARGE: st->too_large++;  break;
    default:                       st->io_errors++;  break;
    }
}

/**
 * 从"当前这次定界器结果"开始，把所有**已完整**的消息交付出去。
 *
 * @param dr  本次 feed 的结果（由调用方提供，避免重复 feed 丢掉已取出的那一条）
 * @return 0 = 已取完（需要更多数据）；1 = 出错；2 = 回调要求停止
 */
static int deliver_loop(rx_pump_t *p, wire_delim_result_t dr,
                        const uint8_t *payload, size_t plen,
                        uint32_t *delivered)
{
    for (;;) {
        if (dr == WIRE_DELIM_NEED_MORE) return 0;
        if (dr != WIRE_DELIM_MSG_READY) {
            count_delim_error(&p->stats, dr);
            return 1;
        }

        rx_msg_t m;
        if (!msg_from_payload(payload, plen, &m)) {
            p->stats.malformed++;
            return 1;
        }

        (*delivered)++;
        p->stats.msgs_delivered++;
        if (!p->cb(&m, p->cb_ctx)) return 2;   /* 调用方要求停 */

        /* 同一段数据里可能还有下一条：继续向定界器要（**不新读 socket**） */
        payload = NULL; plen = 0;
        dr = wire_delim_feed(p->delim, NULL, 0, &payload, &plen);
    }
}

rx_pump_result_t rx_pump_step(rx_pump_t *p, uint32_t *delivered_out)
{
    if (delivered_out != NULL) *delivered_out = 0;
    if (p == NULL || p->delim == NULL) return RX_PUMP_FATAL;

    uint32_t delivered = 0;

    /* ==== 阶段 1：先取出**已经在缓冲里**的完整消息 ====
     *
     * 为什么这一步必须在 read 之【前】：
     * 回调可能在上一条之后要求停止，此时剩余消息已完整地躺在定界器缓冲里。
     * 若每轮都先 read，而这些消息之后又没有新数据到达，它们就**永远交不出来**
     * —— 消息被静默卡死，且计数上看不出任何异常。
     *
     * 这个缺陷是 host 测试（test_callback_may_stop）抓出来的：
     * 我原来的实现一进来就 read，脚本耗尽后读回 AGAIN，缓冲里的 2 条再也出不来。 */
    {
        const uint8_t *pl = NULL; size_t pln = 0;
        wire_delim_result_t dr = wire_delim_feed(p->delim, NULL, 0, &pl, &pln);
        int st = deliver_loop(p, dr, pl, pln, &delivered);
        if (st == 1) { if (delivered_out) *delivered_out = delivered; return RX_PUMP_ERROR; }
        if (st == 2) { if (delivered_out) *delivered_out = delivered; return RX_PUMP_DELIVERED; }
    }

    /* ==== 阶段 2：读一段字节流 ==== */
    int n = p->read_fn(p->read_ctx, p->read_buf, p->read_buf_cap);
    rx_pump_result_t tail;
    if (n == RX_IO_AGAIN) {
        p->stats.again++;
        tail = RX_PUMP_IDLE;          /* **不是错误**：只是暂时没数据 */
    } else if (n == RX_IO_CLOSED) {
        p->stats.closed++;
        tail = RX_PUMP_CLOSED;        /* 对端有意结束，**不算故障** */
    } else if (n < 0) {
        p->stats.io_errors++;
        tail = RX_PUMP_ERROR;
    } else if (n == 0) {
        /* 契约里 0 是 RX_IO_AGAIN，走到这里说明驱动违约。
         * 不静默当 IDLE —— 否则"驱动从不给数据"会被误读成"线上没流量"。 */
        p->stats.io_errors++;
        tail = RX_PUMP_ERROR;
    } else if ((size_t)n > p->read_buf_cap) {
        p->stats.io_errors++;
        tail = RX_PUMP_ERROR;
    } else {
        p->stats.bytes_read += (uint32_t)n;

        /* ==== 阶段 3：把刚读入的数据里能定界的都交付 ==== */
        const uint8_t *pl = NULL; size_t pln = 0;
        wire_delim_result_t dr = wire_delim_feed(p->delim, p->read_buf, (size_t)n,
                                                 &pl, &pln);
        int st = deliver_loop(p, dr, pl, pln, &delivered);
        if (st == 1) { if (delivered_out) *delivered_out = delivered; return RX_PUMP_ERROR; }
        if (st == 2) { if (delivered_out) *delivered_out = delivered; return RX_PUMP_DELIVERED; }
        tail = RX_PUMP_IDLE;
    }

    if (delivered_out != NULL) *delivered_out = delivered;

    /* 已交付过消息时优先报 DELIVERED：终止状态（CLOSED/ERROR）在下一次
     * 调用里仍会被观察到（read 会再次给出同样结果），信息不会丢。 */
    if (delivered > 0) return RX_PUMP_DELIVERED;
    return tail;
}
