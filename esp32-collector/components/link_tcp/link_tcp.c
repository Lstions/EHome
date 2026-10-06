/**
 * @file link_tcp.c
 * @brief TCP+TLS link 驱动实现（状态机；I/O 注入 ⇒ 宿主可测）
 */
#include "link_tcp.h"

#include <stdlib.h>
#include <string.h>

/* 设计 §4.2 的退避序列（ms）：1s→2s→4s→8s→16s→30s→60s（上限）。
 * 用查表而非 1<<n：上限是 60s 而不是 64s，且序列非纯指数（30→60 只翻一倍）。 */
static const uint32_t s_backoff_ms[] = { 1000u, 2000u, 4000u, 8000u,
                                         16000u, 30000u, 60000u };

struct link_tcp_ctx {
    link_tcp_config_t cfg;                   /* 复制，不持有调用方的指针 */
    void             *handle;                /* io->connect 的返回值；NULL = 未连接 */
    bool              handshaked;            /* 应用层握手是否完成 */
    uint32_t          reconnect_attempt;
    uint32_t          tx_sent;               /* 诊断计数 */
    uint32_t          tx_backpressure;
    uint32_t          tx_fatal;
};

link_tcp_ctx_t *link_tcp_new(const link_tcp_config_t *cfg)
{
    if (cfg == NULL || cfg->io == NULL) return NULL;
    link_tcp_ctx_t *c = (link_tcp_ctx_t *)calloc(1, sizeof(*c));
    if (c == NULL) return NULL;
    c->cfg = *cfg;                           /* 复制：调用方可在之后释放 cfg */
    return c;
}

void link_tcp_free(link_tcp_ctx_t *c)
{
    free(c);
}

uint32_t link_tcp_backoff_ms(uint32_t attempt, uint32_t rand_permille)
{
    size_t n = sizeof(s_backoff_ms) / sizeof(s_backoff_ms[0]);
    uint32_t base = (attempt < n) ? s_backoff_ms[attempt] : s_backoff_ms[n - 1];

    /* 抖动 ±20%：把 [0,1000] 映射到 [800,1200] 千分比。
     * rand_permille >= 1000 时夹到 1000，避免越界（调用方给坏值也不出错）。 */
    if (rand_permille > 1000u) rand_permille = 1000u;
    uint32_t span = 2u * LINK_TCP_JITTER_PERMILLE;        /* 400 */
    uint32_t factor = (1000u - LINK_TCP_JITTER_PERMILLE) + /* 800 */
                      (span * rand_permille) / 1000u;      /* +0..400 */
    return (uint32_t)(((uint64_t)base * factor) / 1000u);
}

static link_result_t tcp_open(void *ctx)
{
    link_tcp_ctx_t *c = (link_tcp_ctx_t *)ctx;
    if (c == NULL || c->cfg.io == NULL ||
        c->cfg.io->connect == NULL) {
        return LINK_FATAL;   /* 配置缺失是硬错，重试无意义 */
    }
    if (c->handle != NULL) {
        return LINK_SENT;    /* 已连接：幂等 */
    }

    bool hard_fatal = false;
    void *h = c->cfg.io->connect(c->cfg.io_ctx, &hard_fatal);
    if (h == NULL) {
        /* 【错误分级】证书/配置类失败重试无意义 —— 报 FATAL 让上层别盲目退避；
         * 网络类失败报 NOT_READY（可重试）。分级由 I/O 层给出（它才知道原因）。 */
        if (hard_fatal) {
            return LINK_FATAL;
        }
        c->reconnect_attempt++;
        return LINK_NOT_READY;
    }

    c->handle = h;
    /* 注意：此处**不**重置 reconnect_attempt —— 设计 §4.2 规定重置条件是
     * **应用层握手**（Hello/HelloAck），不是 socket connect。
     * 否则会出现"连上但不通"时退避不断归零、永远 1s 重连的抖振。 */
    c->handshaked = false;
    return LINK_SENT;
}

static void tcp_close(void *ctx)
{
    link_tcp_ctx_t *c = (link_tcp_ctx_t *)ctx;
    if (c == NULL || c->handle == NULL) return;
    if (c->cfg.io->close != NULL) {
        c->cfg.io->close(c->handle);
    }
    c->handle = NULL;
    c->handshaked = false;
}

static link_result_t tcp_send(void *ctx, const uint8_t *data, size_t len)
{
    link_tcp_ctx_t *c = (link_tcp_ctx_t *)ctx;
    if (c == NULL || c->cfg.io == NULL ||
        c->cfg.io->write == NULL) {
        return LINK_FATAL;
    }
    /* 未连接：由【驱动】给出 NOT_READY（调用方不预检 —— 见 link.h"为什么不预检"）。 */
    if (c->handle == NULL) {
        c->tx_fatal++;   /* 未连接却来发：属调用方/上层时序问题，计数可见 */
        return LINK_NOT_READY;
    }

    /* D-10：**必须续写**。部分写不是成功，也不是失败，是"还没写完"。 */
    size_t written = 0;
    for (;;) {
        int n = c->cfg.io->write(c->handle, data + written, len - written);
        if (n > 0) {
            written += (size_t)n;
            if (written >= len) {
                c->tx_sent++;
                return LINK_SENT;
            }
            continue;                 /* 部分写：继续 */
        }
        if (n == 0) {
            /* 【背压】：发送缓冲满。不是错误 —— 调用方应退避重试。
             * 这是 3.0 相对 MQTT 的净收益之一：背压从"隐式丢"变"显式可重试"。 */
            c->tx_backpressure++;
            return LINK_BACKPRESSURE;
        }
        /* n < 0：硬错误（连接已断） */
        c->handle = NULL;             /* 连接已不可用，避免后续误用 */
        c->tx_fatal++;
        return LINK_FATAL;
    }
}

static uint32_t tcp_mtu(void *ctx)
{
    (void)ctx;
    return LINK_TCP_MTU_BYTES;
}

static bool tcp_is_ready(void *ctx)
{
    const link_tcp_ctx_t *c = (const link_tcp_ctx_t *)ctx;
    return c != NULL && c->handle != NULL;
}

static const link_driver_t s_tcp_driver = {
    .open = tcp_open,
    .close = tcp_close,
    .send = tcp_send,
    .mtu = tcp_mtu,
    .is_ready = tcp_is_ready,
    .name = "tcp",
};

const link_driver_t *link_tcp_driver(void)
{
    /* 驱动实例是【无状态】单例 —— 状态全在 link_tcp_ctx_t 里。
     * 第一版把状态放静态变量并把 cfg 存进去，结果 link_create(drv, NULL)
     * 让所有回调收到 NULL 而全面失效（测试当场抓出 13 条失败）。
     * 无状态单例 + 显式上下文 ⇒ 可同时存在多个实例（也便于测试）。 */
    return &s_tcp_driver;
}

void link_tcp_note_handshake(link_tcp_ctx_t *c)
{
    if (c == NULL) return;
    c->handshaked = true;
    c->reconnect_attempt = 0;   /* 应用层握手完成 ⇒ 退避归零（设计 §4.2） */
}

uint32_t link_tcp_reconnect_attempt(const link_tcp_ctx_t *c)
{
    return (c == NULL) ? 0u : c->reconnect_attempt;
}

bool link_tcp_is_connected(const link_tcp_ctx_t *c)
{
    return c != NULL && c->handle != NULL;
}
