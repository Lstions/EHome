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
    uint32_t          tx_sent;               /* 整帧写出次数（诊断） */
    uint32_t          tx_partial;            /* 部分写出次数（P3：这条路径必须可见） */
    uint32_t          tx_backpressure;
    uint32_t          tx_fatal;
    /* --- 接收侧诊断（P3：每条路径都要可见）--- */
    uint32_t          rx_bytes;   /* 累计读到的字节数（**字节流**口径，不是消息数） */
    uint32_t          rx_again;   /* 暂无数据次数（正常状态） */
    uint32_t          rx_closed;  /* 对端正常关闭次数（EOF，非错误） */
    uint32_t          rx_fatal;   /* 读硬错误次数 */
};

static const char *const s_read_names[] = {
    [LINK_READ_DATA]      = "DATA",
    [LINK_READ_AGAIN]     = "AGAIN",
    [LINK_READ_CLOSED]    = "CLOSED",
    [LINK_READ_NOT_READY] = "NOT_READY",
    [LINK_READ_FATAL]     = "FATAL",
};

const char *link_read_result_name(link_read_result_t r)
{
    if ((int)r < 0 || r > (int)LINK_READ_FATAL) return "UNKNOWN";
    return s_read_names[r];
}

link_read_result_t link_tcp_read(link_tcp_ctx_t *c, uint8_t *buf, size_t cap,
                                size_t *n_out)
{
    if (n_out != NULL) *n_out = 0;
    if (c == NULL || buf == NULL || cap == 0 || n_out == NULL) return LINK_READ_FATAL;
    /* 连接未建立时没有可发起的读 —— handle 由本模块维护，
     * 不存在"查与用之间被外部改变"的窗口（与 link_send 的 TOCTOU 情形不同）。 */
    if (c->handle == NULL) return LINK_READ_NOT_READY;
    if (c->cfg.io == NULL || c->cfg.io->read == NULL) return LINK_READ_FATAL;

    int n = c->cfg.io->read(c->handle, buf, cap);
    if (n > 0) {
        if ((size_t)n > cap) { c->rx_fatal++; return LINK_READ_FATAL; }  /* 驱动违约 */
        c->rx_bytes += (uint32_t)n;
        *n_out = (size_t)n;
        return LINK_READ_DATA;
    }
    if (n == LINK_TCP_IO_AGAIN) {
        /* 超时：正常状态。单独一档，**不并进 FATAL** */
        c->rx_again++;
        return LINK_READ_AGAIN;
    }
    if (n == LINK_TCP_IO_CLOSED) {
        /* 对端正常关闭：连接不可再用，但**不是故障**
         * （可能是服务端有意重启/滚动更新）。 */
        c->rx_closed++;
        c->handle = NULL;
        return LINK_READ_CLOSED;
    }
    c->rx_fatal++;
    c->handle = NULL;
    return LINK_READ_FATAL;
}

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
        return LINK_SENT_FULL;    /* 已连接：幂等 */
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
    return LINK_SENT_FULL;
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

static link_result_t tcp_send(void *ctx, const uint8_t *data, size_t len,
                            size_t *written_out)
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

    /* D-10 + D-30：**如实报出写了多少**，而不是自行"续写到写完"。
     *
     * 为什么不再自行续写（骨架首版的做法）：
     *   首版在这里循环续写，写不动时返回 BACKPRESSURE；但调用方按"稍后重试"
     *   会**重发整帧** ⇒ 已上线的字节再写一遍 ⇒ 线上出现重复片段 ⇒
     *   接收端重组器无法自愈（静默流污染）。
     *   根因是接口丢掉了"已写出多少"。
     *   ⇒ 现在：写多少报多少，续写由调用方按 link.h 的循环模式负责。
     *
     * 语义（与 link_driver_t.send 契约一致）：
     *   返回 FULL      => *written_out == len（本次请求的全部）
     *   返回 PARTIAL   => 0 < *written_out < len
     *   返回 BACKPRESSURE => *written_out == 0（一字节未写出）
     *   其它           => *written_out == 0
     */
    if (written_out != NULL) *written_out = 0;

    int n = c->cfg.io->write(c->handle, data, len);
    if (n > 0) {
        size_t got = ((size_t)n > len) ? len : (size_t)n;  /* 驱动不变量：不得超过请求量 */
        if (written_out != NULL) *written_out = got;
        if (got == len) {
            c->tx_sent++;
            return LINK_SENT_FULL;
        }
        /* 写出了一部分：不是成功也不是失败 —— 是【进行中】。
         * 调用方必须从这里续写，绝不能重发整帧。 */
        c->tx_partial++;
        return LINK_SENT_PARTIAL;
    }
    if (n == 0) {
        /* 【背压】：一个字节都没写出 ⇒ 整帧稍后重试是安全的。
         * 这是 3.0 相对 MQTT 的净收益：背压从"隐式丢"变"显式可重试"。 */
        c->tx_backpressure++;
        return LINK_BACKPRESSURE;
    }
    /* n < 0：硬错误（连接已断） */
    c->handle = NULL;             /* 连接已不可用，避免后续误用 */
    c->tx_fatal++;
    return LINK_FATAL;
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
