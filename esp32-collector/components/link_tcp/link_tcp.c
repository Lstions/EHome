/**
 * @file link_tcp.c
 * @brief TCP+TLS link 驱动实现（状态机；I/O 注入 ⇒ 宿主可测）
 */
#include "link_tcp.h"

#include <stdlib.h>
#include <string.h>

/* 2026-10-11（§258）：handle 生命周期需要一把锁 —— 见 tcp_release_handle()
 * 上方"两个任务并发判定连接不可用"的说明。host 测试经
 * host_tests/stubs/freertos/ 提供桩。 */
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

/* 设计 §4.2 的退避序列（ms）：1s→2s→4s→8s→16s→30s→60s（上限）。
 * 用查表而非 1<<n：上限是 60s 而不是 64s，且序列非纯指数（30→60 只翻一倍）。 */
static const uint32_t s_backoff_ms[] = { 1000u, 2000u, 4000u, 8000u,
                                         16000u, 30000u, 60000u };

/* 前置声明：读/写路径在文件前部就要用它释放连接（§216 的 socket 泄漏修复），
 * 而定义在 tcp_close 附近。放在这里而不是把定义上移，是为了让
 * "所有 handle 释放都经此一处"这个事实在定义处一眼可见。 */
static void tcp_release_handle(link_tcp_ctx_t *c);

struct link_tcp_ctx {
    link_tcp_config_t cfg;                   /* 复制，不持有调用方的指针 */

    /* --- handle 生命周期（2026-10-11，真机 double-free 修复）----------
     * lock 保护下面三个字段的**全部**状态转移；它**绝不**跨越
     * io->read / io->write / io->connect —— 那些调用最坏阻塞
     * EHOME_DEVICE_LINK_TIMEOUT_MS(10000 ms)，持锁跨越它们会让 report_tx
     * 等一次下行静默等满 10s（吞吐直接归零）。
     * ⇒ 用 inflight 计数代替"持锁调用"：I/O 期间握的是**租约**，不是锁。 */
    SemaphoreHandle_t lock;
    void             *handle;                /* io->connect 的返回值；NULL = 未连接 */
    void             *retiring;              /* 已认领关闭、等最后一个租约归还的连接 */
    uint32_t          inflight;              /* 正在使用 handle 的 I/O 调用数 */
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

/* 取一个 I/O 租约：连接有效时返回句柄并 ++inflight，否则返回 NULL。
 * 必须与 tcp_io_release() 成对 —— 少了它，"有人正在用句柄"这件事对
 * tcp_release_handle() 不可见，它就会把句柄从另一个任务脚下 free 掉。 */
static void *tcp_io_acquire(link_tcp_ctx_t *c)
{
    void *h = NULL;
    if (c == NULL || c->lock == NULL) return NULL;
    xSemaphoreTake(c->lock, portMAX_DELAY);
    /* 只看 handle：retiring 是**另一代**连接，tcp_release_handle 已把它摘走，
     * 不会再有新租约落到它上面。若这里还去看 retiring，就会在"旧连接等归还、
     * 新连接已连上"时把新连接也挡住（最多 10s，等于重连后写不出去）。 */
    if (c->handle != NULL) {
        c->inflight++;
        h = c->handle;
    }
    xSemaphoreGive(c->lock);
    return h;
}

/* 归还 I/O 租约。若此时连接已认领关闭、且这是最后一个租约，
 * 由本函数执行真正的 io->close()（见 tcp_release_handle() 的说明）。
 * ⚠ io->close() 必须在**锁外**调用：它内部销毁 TLS 上下文并 free，
 *   属于可能阻塞的工作，不能把锁带进去。 */
static void tcp_io_release(link_tcp_ctx_t *c)
{
    if (c == NULL || c->lock == NULL) return;
    void *h = NULL;
    xSemaphoreTake(c->lock, portMAX_DELAY);
    if (c->inflight > 0) c->inflight--;
    if (c->retiring != NULL && c->inflight == 0) {
        h = c->retiring;
        c->retiring = NULL;
    }
    xSemaphoreGive(c->lock);
    if (h != NULL && c->cfg.io != NULL && c->cfg.io->close != NULL) {
        c->cfg.io->close(h);
    }
}

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
    /* ⚠ 2026-10-11 更正：注释原先写"handle 由本模块维护，不存在查与用之间被
     * 外部改变"—— **那是错的**。handle 有两个任务在动：dev_link 走本函数，
     * report_tx 走 tcp_send()，两者都可能在**对方 I/O 进行中**判定连接不可用
     * 并释放句柄。真机后果 = 堆 double-free（tlsf_free，每 60s 崩溃 9 次）。
     * ⇒ 现在用租约：取到句柄后 tcp_release_handle() 会等我们归还再 close。 */
    if (c->cfg.io == NULL || c->cfg.io->read == NULL) return LINK_READ_FATAL;
    void *h = tcp_io_acquire(c);
    if (h == NULL) return LINK_READ_NOT_READY;

    int n = c->cfg.io->read(h, buf, cap);
    tcp_io_release(c);
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
        /* ⚠ §216：必须**释放**（close socket/TLS），不能只丢指针。 */
        tcp_release_handle(c);
        return LINK_READ_CLOSED;
    }
    c->rx_fatal++;
    /* ⚠ §216：同上 —— 原实现只置 NULL，socket 泄漏。 */
    tcp_release_handle(c);
    return LINK_READ_FATAL;
}

link_tcp_ctx_t *link_tcp_new(const link_tcp_config_t *cfg)
{
    if (cfg == NULL || cfg->io == NULL) return NULL;
    link_tcp_ctx_t *c = (link_tcp_ctx_t *)calloc(1, sizeof(*c));
    if (c == NULL) return NULL;
    c->cfg = *cfg;                           /* 复制：调用方可在之后释放 cfg */

    /* 锁必须在这里建：tcp_open/read/send/release 都用它保护 handle 的
     * 状态转移。建不出来就**不返回半成品**（P1）—— 一个 lock==NULL 的上下文
     * 会让所有租约静默退化为"永远取不到句柄"，等于链路不可用却看不出原因。 */
    c->lock = xSemaphoreCreateMutex();
    if (c->lock == NULL) {
        free(c);
        return NULL;
    }
    return c;
}

void link_tcp_free(link_tcp_ctx_t *c)
{
    if (c == NULL) return;
    /* 若调用方没先 close 就 free（session.c 的几条失败路径就是这样），
     * 这里补一次关闭 —— 否则底层 socket/TLS 泄漏（§216 的老问题）。
     * tcp_release_handle 会在锁内摘句柄、在锁外 close。 */
    tcp_release_handle(c);
    if (c->lock != NULL) vSemaphoreDelete(c->lock);
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
    /* ⚠ 2026-10-11：这里既看 handle 也看 retiring。
     * retiring 非空 = 上一条连接还有 I/O 在用、尚未 close。此时**不能**建新
     * 连接：同一时刻出现两个活着的 handle 后，inflight 是**跨代**计数，
     * 无法分辨某个租约属于哪一代 ⇒ 归还时可能关错那一个（use-after-free）。
     * 退避重试的代价是几秒；判错的代价是崩溃重启。 */
    bool connected = false, retiring = false;
    if (c->lock != NULL) {
        xSemaphoreTake(c->lock, portMAX_DELAY);
        connected = (c->handle != NULL);
        retiring  = (c->retiring != NULL);
        xSemaphoreGive(c->lock);
    } else {
        connected = (c->handle != NULL);
    }
    if (connected) return LINK_SENT_FULL;    /* 已连接：幂等 */
    if (retiring)  return LINK_NOT_READY;    /* 旧连接待回收：稍后重试 */

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

    if (c->lock != NULL) {
        xSemaphoreTake(c->lock, portMAX_DELAY);
        c->handle = h;
        xSemaphoreGive(c->lock);
    } else {
        c->handle = h;
    }
    /* 注意：此处**不**重置 reconnect_attempt —— 设计 §4.2 规定重置条件是
     * **应用层握手**（Hello/HelloAck），不是 socket connect。
     * 否则会出现"连上但不通"时退避不断归零、永远 1s 重连的抖振。 */
    c->handshaked = false;
    return LINK_SENT_FULL;
}

/* ⚠⚠ 2026-10-09（§216）：**先关句柄，再置 NULL** —— 这是唯一的正确顺序。
 *
 * 真机现象（C6，uptime 2.87h，后端重启后）：
 *   E esp-tls: Failed to create socket (family 2 socktype 1 protocol 0)
 *   tls_esp: 连接失败: last_error=0x8002 (=ESP_ERR_ESP_TLS_CANNOT_CREATE_SOCKET)
 *   ⇒ 设备再也连不上（每 60s 退避重试一次，每次都失败）；
 *     断电重启后**立即**恢复 ⇒ 是资源泄漏，不是配置/证书问题。
 *
 * 根因：read/send 探测到连接不可用时，原实现直接
 *       `c->handle = NULL;` —— 只丢掉指针，**不调用 io->close()**
 *       ⇒ 底层 socket / TLS 上下文泄漏，LWIP_MAX_SOCKETS=6 很快耗尽。
 *
 * ⚠ 这个缺陷**被一段注释"论证"成了安全的**（session.c 原文）：
 *     "link_tcp 在 read/send 失败时**自己**把 handle 置 NULL
 *      （且 tcp_close 对 NULL handle 安全）"
 *   —— 前半句是事实，后半句也是事实，但**两者拼不出"安全"**：
 *     置 NULL 不等于已释放。tcp_close 对 NULL 安全只保证"重复关不出错"，
 *     不保证"已经被关过"。
 *   ⇒ 又一次"为多传输权衡而写的判据在真实约束下失效"（见 §216 的教训）。
 *
 * 现改为统一走本函数释放：任何"连接不可用"的路径都必须经过它。
 *
 * ⚠⚠ 2026-10-11（真机 BLOCKER）：上面这条修法**不完整** —— 它只回答了
 * "谁负责释放"，没回答"**另一个任务正在用这个 handle 时怎么办**"。
 *
 * 真机现象（S3P 30EDA0A9A808，只跑正常轮询，无代码改动）：
 *   E esp-tls-mbedtls: write error :-0x6C00 / read error :-0x004C
 *   assert failed: tlsf_free tlsf.c:630 (!block_is_free(block)
 *                    && "block already marked as free")
 *   Rebooting...
 * 实测频率：链路有负载时 **每 60s 崩溃重启 9 次**（空闲/无应答/低速率 = 0 次）。
 * 崩溃点 backtrace：tlsf_free <- esp_tls_internal_event_tracker_destroy(
 *   esp_tls_error_capture.c:50) <- esp_tls_conn_destroy(esp_tls.c:188)
 *   <- tls_esp_close(tls_esp.c:253) <- tcp_release_handle(本函数)。
 *
 * 根因：handle 有**两个任务**在动 —— dev_link 走 link_tcp_read()，
 * report_tx 走 tcp_send()。两者都可能在**对方 I/O 进行中**判定"连接不可用"
 * 并调用本函数 ⇒ 同一个 handle 被 close 两次 ⇒ esp_tls_conn_destroy() 对同一个
 * error_handle 二次 free() ⇒ tlsf 断言。
 * （那次 backtrace 是栈扫描，两帧不构成调用链，所以"并发"的依据是下述代码事实，
 *   不是那段栈：c->handle 无锁 + 两条路径都能走到本函数。）
 *
 * 修法：**延迟到最后一个租约归还时再关闭**。本函数只做"认领"：
 *   · 在锁内把 handle 摘到 retiring，handle 立即置 NULL（新 I/O 不再取到它）；
 *   · 若无 I/O 在飞（inflight == 0）⇒ 当场在锁外 close；
 *   · 否则**不 close** —— 由最后一个 tcp_io_release() 负责。
 * 关键不变量：**一个句柄只会被 close 一次**，且**只在没有使用者时**被 close。
 * ⇒ 这既消除 double-free，也消除"free 掉别人正在用的句柄"（use-after-free）。 */
static void tcp_release_handle(link_tcp_ctx_t *c)
{
    if (c == NULL || c->lock == NULL) return;

    void *to_close = NULL;
    xSemaphoreTake(c->lock, portMAX_DELAY);
    c->handshaked = false;
    if (c->handle != NULL) {
        void *claimed = c->handle;
        c->handle = NULL;         /* 新 I/O 立刻取不到它（tcp_io_acquire 只认 handle） */
        if (c->inflight == 0) {
            /* 没有使用者在飞 ⇒ 当场关闭（close 在锁外执行） */
            to_close = claimed;
        } else {
            /* 有 I/O 在飞 ⇒ 挂到 retiring，交给最后一个归还者关闭。
             * ⚠ 这里**不能**顺手把旧的 retiring 也关掉：此刻 inflight 是
             * 一个**跨两代**的计数，无法区分"在用旧的"还是"在用新的"，
             * 关旧的就会 free 掉别人正在用的句柄（use-after-free）。
             * ⇒ 代际并发由 tcp_open() 的不变量排除：只要 retiring 非空，
             *   就不允许建立新连接（返回 NOT_READY 稍后重试）。
             *   因此同一时刻**至多只有一个**未关闭的 handle，
             *   inflight 这个单一计数才是可靠的。 */
            c->retiring = claimed;
        }
    }
    xSemaphoreGive(c->lock);

    if (to_close != NULL && c->cfg.io != NULL && c->cfg.io->close != NULL) {
        c->cfg.io->close(to_close);
    }
}

static void tcp_close(void *ctx)
{
    tcp_release_handle((link_tcp_ctx_t *)ctx);
}

static link_result_t tcp_send(void *ctx, const uint8_t *data, size_t len,
                            size_t *written_out)
{
    link_tcp_ctx_t *c = (link_tcp_ctx_t *)ctx;
    if (c == NULL || c->cfg.io == NULL ||
        c->cfg.io->write == NULL) {
        return LINK_FATAL;
    }
    /* 未连接：由【驱动】给出 NOT_READY（调用方不预检 —— 见 link.h"为什么不预检"）。
     * ⚠ 2026-10-11：这里原先直接读 c->handle 再交给 io->write —— 与
     * link_tcp_read() 是同一个 TOCTOU。改为租约（见 tcp_io_acquire）。 */
    void *h = tcp_io_acquire(c);
    if (h == NULL) {
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

    int n = c->cfg.io->write(h, data, len);
    tcp_io_release(c);
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
    /* ⚠ §216：原实现只置 NULL（"避免后续误用"），但**没释放** —— 写入失败
     * 是 socket 泄漏最频繁的路径（每次 TLS 断开都会走到这里）。
     * 释放后 handle 同样是 NULL，原意图（防误用）不变，且不再泄漏。 */
    tcp_release_handle(c);
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
    if (c == NULL) return false;
    /* 这是**只读观测**，不进决策（见 link.h"为什么不预检"）——
     * 但读 handle 仍要走锁，否则可能读到被释放瞬间的中间态。 */
    if (c->lock == NULL) return c->handle != NULL;
    xSemaphoreTake(c->lock, portMAX_DELAY);
    bool ready = (c->handle != NULL);
    xSemaphoreGive(c->lock);
    return ready;
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
    if (c == NULL) return false;
    if (c->lock == NULL) return c->handle != NULL;
    xSemaphoreTake(c->lock, portMAX_DELAY);
    bool connected = (c->handle != NULL);
    xSemaphoreGive(c->lock);
    return connected;
}
