/*
 * firmware_tcp_e2e_client.c —— 固件侧 C 客户端：与后端 Go 走**真实 socket** 的跨语言对锚
 *
 * ## 为什么存在
 * 本项目 40+ 轮一直登记着一条：固件 C 与后端 Go **从未在真实链路上对话过**。
 * 两端的协议向量文件自己写着"各自独立实现了同一份字段约定，而两边从未在真实
 * 链路上对话过；两端各自单测全绿、合起来却不通"。
 * 本程序是这条的第一半：让**真实固件栈**通过**真实 TCP socket** 把**真实字节**
 * 发出去，并校验对端发回的字节。
 *
 * ## 用的是哪些**真实**组件（未改一行生产代码）
 *   wire           —— 真实帧头编码器（12 B 大端头）
 *   link_tcp       —— 真实链路驱动 + 状态机（部分写/背压/错误分级）
 *   link_rx_adapt  —— 真实"枚举撞车"翻译层
 *   rx_pump        —— 真实接收泵（定界；处理半条帧）
 *
 * ## ⚠ 为什么**没有**用 session_create（与任务卡的偏离，已如实报告）
 * 任务卡要求"用真实 session_create"。实测核对后确认：**session 没有任何上行
 * 入口**（session.h 只有 create/poll/state/note_handshake/stats，唯一的上行
 * `link_send` 被私有的 `struct session` 持有，外部拿不到）。
 * 而本程序**必须**能发一条 0x23 帧 ⇒ 用 session_create 无法完成。
 * 不改生产代码的加法只有一种：**自己按 session.c 内部同样的方式组装同一批层**
 * （session.c:58-66 就是 link_tcp_new + link_create + link_rx_binding_new +
 * rx_pump_create）。本程序用的正是这四个真实组件，只少了 session 那层
 * 退避包装 —— 而单发一帧的对锚不需要退避。
 *
 * ## ⭐ link_tcp 读返回值的四态（本程序最容易写错的地方）
 * link_tcp.h 明确定义（link_tcp.c:55-77 逐条实现）：
 *
 *   | 返回值 | 含义 | 常量 |
 *   |---|---|---|
 *   | > 0 | 读到的**字节数** | — |
 *   | 0 | **暂无数据（超时/EAGAIN）** | LINK_TCP_IO_AGAIN |
 *   | -1 | 对端**正常关闭**（EOF） | LINK_TCP_IO_CLOSED |
 *   | -2 | 硬错误 | LINK_TCP_IO_ERROR |
 *
 * ### ⚠ 陷阱：**recv() 返回 0 是 EOF，必须翻成 -1，不能翻成 0**
 * POSIX `recv()==0` 表示对端关闭，而本接口的 `0` 表示"暂无数据"。
 * 若把 EOF 也返回 0，调用方（rx_pump）会把它当"超时，继续读"⇒
 * **永远转下去，直到 8 秒超时**，且日志上看不出对端已经关了。
 * 这是一次典型的**假绿**：程序最终可能仍打印 PASS（如果之前已收到帧），
 * 但对"对端关闭"这条路径的判定是错的。
 * ⇒ 本实现显式区分二者（见 tcp_read）。
 *
 * ### ⚠ 第二个陷阱：`link_rx_adapt` 的枚举值撞车
 * `LINK_READ_DATA == 0` 而 `RX_IO_AGAIN == 0`，**含义相反**。
 * 所以绝不能写 `return (int)link_tcp_read(...)` —— 那会把**读到的数据当成
 * 暂无数据丢掉**。本程序用真实的 `link_rx_adapt_read()`（它内部逐值 switch），
 * 不自己透传。
 *
 * ## 用法
 *   firmware_tcp_e2e_client <host> <port>
 *
 * ## 退出码
 *   0 = PASS；1 = FAIL（最后一行 E2E-CLIENT result=FAIL reason=...）
 */
#include <arpa/inet.h>
#include <errno.h>
#include <netinet/in.h>
#include <netinet/tcp.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/time.h>
#include <unistd.h>

#include "link.h"
#include "link_rx_adapt.h"
#include "link_tcp.h"
#include "rx_pump.h"
#include "wire.h"

/* ── 对锚用的精确字节（任务卡给定，已由协议向量核对）── */

/** 我们要发的：0x23 ACK OK，payload 15 B。 */
static const uint8_t kAckOkPayload[15] = {
    0x23, 0x08, 0x00, 0x12, 0x0a,
    'o', 'p', '-', 'n', 'o', 'd', 'e', '1', '-', '1',
};

/** 期望收到的：0x22 reboot，payload 15 B。 */
static const uint8_t kRebootPayload[15] = {
    0x22, 0x08, 0x01, 0x12, 0x0a,
    'o', 'p', '-', 'n', 'o', 'd', 'e', '1', '-', '1',
};

#define E2E_CONNECT_TIMEOUT_MS 5000
#define E2E_RX_TIMEOUT_MS      8000
#define E2E_MAX_PAYLOAD        4096
#define E2E_RX_BUF_CAP         2048

/* ── 结果输出（机器可读；Lead 的脚本按前缀 grep）── */

static void say(const char *fmt, ...)
{
    va_list ap;
    va_start(ap, fmt);
    fputs("E2E-CLIENT ", stdout);
    vprintf(fmt, ap);
    va_end(ap);
    fputc('\n', stdout);
    fflush(stdout);
}

static int fail(const char *reason)
{
    say("result=FAIL reason=%s", reason);
    return 1;
}

/* ══════════════════ link_tcp_io_t over 真 socket ══════════════════ */

typedef struct {
    const char *host;
    int         port;
    int         fd;          /* -1 = 未连接 */
    int         rx_timeout_ms;
} tcp_ctx_t;

/**
 * io->connect：真实阻塞式 TCP 连接 + 读超时。
 *
 * 返回 socket fd（**注意不能返回 0**，0 在"句柄"语境下是合法 fd，
 * 但 link_tcp 用 NULL 判"未连接"，故这里把 fd 包成指针语义：
 * 我们把 fd 存进 ctx，句柄返回 ctx 本身）。
 */
static void *tcp_connect(void *io_ctx, bool *hard_fatal)
{
    tcp_ctx_t *c = (tcp_ctx_t *)io_ctx;
    if (hard_fatal) *hard_fatal = false;

    int fd = socket(AF_INET, SOCK_STREAM, 0);
    if (fd < 0) {
        if (hard_fatal) *hard_fatal = true;   /* 建 socket 都失败：配置/资源类，重试无意义 */
        return NULL;
    }

    /* 连接超时用阻塞 connect + 内核默认；本程序只在本地/内网对锚，够用。
     * 连接本身设了 SO_RCVTIMEO 之前先连 —— 否则会影响 connect 的等待行为。 */
    struct sockaddr_in sa;
    memset(&sa, 0, sizeof(sa));
    sa.sin_family = AF_INET;
    sa.sin_port = htons((uint16_t)c->port);
    if (inet_pton(AF_INET, c->host, &sa.sin_addr) != 1) {
        /* 不是点分 IP：本程序刻意只支持 IP —— 解析名要走 getaddrinfo，
         * 而"连不上"与"名字解析不了"在日志上必须可分。 */
        close(fd);
        if (hard_fatal) *hard_fatal = true;
        say("note=host_not_dotted_quad host=%s", c->host);
        return NULL;
    }

    if (connect(fd, (struct sockaddr *)&sa, sizeof(sa)) != 0) {
        close(fd);
        return NULL;    /* 软失败：网络不可达/拒绝 ⇒ 可重试 */
    }

    /* ⭐ 读超时：这就是"暂无数据"（LINK_TCP_IO_AGAIN）的来源。
     * 没有它，recv 会永久阻塞 ⇒ 8 秒超时兜底形同虚设。 */
    struct timeval tv;
    tv.tv_sec  = c->rx_timeout_ms / 1000;
    tv.tv_usec = (c->rx_timeout_ms % 1000) * 1000;
    (void)setsockopt(fd, SOL_SOCKET, SO_RCVTIMEO, &tv, sizeof(tv));

    int one = 1;
    (void)setsockopt(fd, IPPROTO_TCP, TCP_NODELAY, &one, sizeof(one));

    c->fd = fd;
    return (void *)c;
}

/**
 * ⭐⭐ 本文件最关键的 10 行：读返回值四态。
 *
 * POSIX recv()          →  本接口必须返回
 *   > 0                  →  > 0（字节数）
 *   0（对端关闭 EOF）     →  LINK_TCP_IO_CLOSED (-1)   **不是 0！**
 *   -1 且 EAGAIN/EWOULDBLOCK（读超时） →  LINK_TCP_IO_AGAIN (0)
 *   其它 -1              →  LINK_TCP_IO_ERROR (-2)
 *   EINTR                →  LINK_TCP_IO_AGAIN (0)：被信号打断不是错误，
 *                            当作"暂无数据"让上层下一轮再来（避免把 Ctrl-C
 *                            之类误报成链路故障）。
 */
static int tcp_read(void *handle, uint8_t *buf, size_t cap)
{
    tcp_ctx_t *c = (tcp_ctx_t *)handle;
    if (c == NULL || c->fd < 0 || buf == NULL || cap == 0) return LINK_TCP_IO_ERROR;

    ssize_t n = recv(c->fd, buf, cap, 0);
    if (n > 0) return (int)n;

    if (n == 0) {
        /* 对端正常关闭。⚠ 必须返回 -1 而不是 0：0 的含义是"暂无数据"。 */
        return LINK_TCP_IO_CLOSED;
    }

    if (errno == EAGAIN || errno == EWOULDBLOCK) {
        /* 读超时：正常状态 */
        return LINK_TCP_IO_AGAIN;
    }
    if (errno == EINTR) {
        /* 被信号打断：不是错误，下一轮再来 */
        return LINK_TCP_IO_AGAIN;
    }
    return LINK_TCP_IO_ERROR;
}

/** io->write：如实报出写了多少（link_tcp 自己处理部分写/背压）。 */
static int tcp_write(void *handle, const uint8_t *data, size_t len)
{
    tcp_ctx_t *c = (tcp_ctx_t *)handle;
    if (c == NULL || c->fd < 0 || data == NULL || len == 0) return LINK_TCP_IO_ERROR;

    ssize_t n = send(c->fd, data, len, 0);
    if (n > 0) return (int)n;
    if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) return 0;  /* 背压 */
    if (n < 0 && errno == EINTR) return 0;
    return LINK_TCP_IO_ERROR;
}

static void tcp_close(void *handle)
{
    tcp_ctx_t *c = (tcp_ctx_t *)handle;
    if (c == NULL) return;
    if (c->fd >= 0) { close(c->fd); c->fd = -1; }
}

static const link_tcp_io_t TCP_IO = {
    .connect = tcp_connect,
    .write   = tcp_write,
    .read    = tcp_read,
    .close   = tcp_close,
};

/* ══════════════════ 时钟 / 随机（link_tcp 不回退，但 session 需要）══════════ */

static uint64_t now_ms(void)
{
    struct timeval tv;
    gettimeofday(&tv, NULL);
    return (uint64_t)tv.tv_sec * 1000u + (uint64_t)(tv.tv_usec / 1000);
}

/* ══════════════════ 收到的帧 ══════════════════ */

typedef struct {
    bool     got;
    uint8_t  ver;
    uint8_t  type;
    uint16_t flags;
    uint32_t seq;
    uint16_t payload_len;
    uint8_t  payload[E2E_MAX_PAYLOAD];
} rx_capture_t;

static bool on_msg(const rx_msg_t *m, void *ctx)
{
    rx_capture_t *cap = (rx_capture_t *)ctx;
    if (cap->got) return false;              /* 只要第一条 */

    cap->got  = true;
    cap->ver  = m->ver;
    cap->type = m->type;
    cap->flags = m->flags;
    cap->seq  = m->seq;
    cap->payload_len = m->payload_len;

    size_t n = m->payload_len;
    if (n > sizeof(cap->payload)) n = sizeof(cap->payload);
    if (n > 0 && m->payload != NULL) memcpy(cap->payload, m->payload, n);
    return false;                             /* 已拿到，停止本轮泵送 */
}

/* ══════════════════ main ══════════════════ */

int main(int argc, char **argv)
{
    if (argc != 3) {
        fprintf(stderr, "用法: %s <host> <port>\n", argv[0]);
        return 1;
    }

    tcp_ctx_t ctx;
    memset(&ctx, 0, sizeof(ctx));
    ctx.host = argv[1];
    ctx.port = atoi(argv[2]);
    ctx.fd   = -1;
    ctx.rx_timeout_ms = 200;   /* recv 超时 ⇒ LINK_TCP_IO_AGAIN ⇒ 上层可判总超时 */

    if (ctx.port <= 0 || ctx.port > 65535) return fail("bad_port");

    /* ── 1) 真实 link_tcp（含真实状态机）── */
    link_tcp_config_t lcfg = { .io = &TCP_IO, .io_ctx = &ctx };
    link_tcp_ctx_t *tcp = link_tcp_new(&lcfg);
    if (tcp == NULL) return fail("link_tcp_new");

    link_t *link = link_create(link_tcp_driver(), tcp);
    if (link == NULL) { link_tcp_free(tcp); return fail("link_create"); }

    /* ── 2) 真实 link_rx_adapt + rx_pump（定界 ⇒ 能处理半条帧）── */
    link_rx_binding_t *bind = link_rx_binding_new(tcp);
    if (bind == NULL) { link_destroy(link); link_tcp_free(tcp); return fail("rx_binding"); }

    static uint8_t rbuf[E2E_RX_BUF_CAP];
    rx_capture_t cap;
    memset(&cap, 0, sizeof(cap));

    rx_pump_t *pump = rx_pump_create(E2E_MAX_PAYLOAD, link_rx_adapt_read, bind,
                                     on_msg, &cap, rbuf, sizeof(rbuf));
    if (pump == NULL) {
        link_rx_binding_free(bind); link_destroy(link); link_tcp_free(tcp);
        return fail("rx_pump_create");
    }

    /* ── 3) 连接 ── */
    link_result_t lr = link_open(link);
    if (lr != LINK_SENT_FULL) {
        rx_pump_destroy(pump); link_rx_binding_free(bind);
        link_destroy(link); link_tcp_free(tcp);
        say("note=link_open_rc=%s", link_result_name(lr));
        return fail("connect");
    }
    say("connect=ok");

    /* ── 4) 用**真实 wire 编码器**构造一条 0x23 帧 ── */
    wire_header_t h;
    memset(&h, 0, sizeof(h));
    h.ver         = (uint8_t)WIRE_VER;
    h.type        = 0x23;
    h.flags       = 0;                      /* ⚠ 不置 CRC 位 */
    h.seq         = 1;
    h.payload_len = (uint16_t)sizeof(kAckOkPayload);

    uint8_t frame[WIRE_HEADER_BYTES + sizeof(kAckOkPayload)];
    wire_result_t wr = wire_encode_header(frame, sizeof(frame), &h);
    if (wr != WIRE_OK) {
        rx_pump_destroy(pump); link_rx_binding_free(bind);
        link_destroy(link); link_tcp_free(tcp);
        say("note=wire_encode_header=%s", wire_result_name(wr));
        return fail("wire_encode");
    }
    memcpy(frame + WIRE_HEADER_BYTES, kAckOkPayload, sizeof(kAckOkPayload));

    /* ── 5) 发送（真实 link_send；按 link.h 的续写模式处理部分写）── */
    size_t len = sizeof(frame);
    size_t progress = 0;
    uint64_t tx_deadline = now_ms() + 5000;
    for (;;) {
        lr = link_send(link, frame, len, &progress);
        if (lr == LINK_SENT_FULL) break;
        if (lr == LINK_SENT_PARTIAL) continue;            /* 接着写（progress 已推进） */
        if (lr == LINK_BACKPRESSURE) {                    /* 一字节没写出：整帧稍后重试 */
            if (now_ms() > tx_deadline) {
                rx_pump_destroy(pump); link_rx_binding_free(bind);
                link_destroy(link); link_tcp_free(tcp);
                return fail("tx_backpressure_timeout");
            }
            continue;
        }
        /* 其它结果：交给上层决策（本程序直接判失败并如实报出） */
        say("note=link_send_rc=%s progress=%zu", link_result_name(lr), progress);
        rx_pump_destroy(pump); link_rx_binding_free(bind);
        link_destroy(link); link_tcp_free(tcp);
        return fail("send");
    }
    say("sent type=0x23 len=%u", (unsigned)sizeof(kAckOkPayload));

    /* ── 6) 反复 poll 直到收到一条帧或超时 ── */
    uint64_t deadline = now_ms() + E2E_RX_TIMEOUT_MS;
    int exit_code = 1;
    const char *rx_fail = "rx_timeout";   /* 默认：真的等满了 */

    while (!cap.got && now_ms() < deadline) {
        uint32_t delivered = 0;
        rx_pump_result_t pr = rx_pump_step(pump, &delivered);
        /* 每一档都要区分（P1）—— 尤其 CLOSED 不能与 AGAIN 混：
         * 前者说明对端关了，后者只是"还没数据"。
         * ⚠ 失败原因必须**如实**：对端关闭却报 rx_timeout，会把人引向
         * "再等等"而不是"对端为什么关了"。 */
        if (pr == RX_PUMP_CLOSED) { say("note=rx_closed_by_peer"); rx_fail = "rx_closed_by_peer"; break; }
        if (pr == RX_PUMP_ERROR)  { say("note=rx_pump_rc=ERROR");  rx_fail = "rx_pump_error"; break; }
        if (pr == RX_PUMP_FATAL)  { say("note=rx_pump_rc=FATAL");  rx_fail = "rx_pump_fatal"; break; }
        /* IDLE / DELIVERED：继续（DELIVERED 时 cap.got 已置位，循环条件会退出） */
    }

    if (!cap.got) {
        rx_pump_destroy(pump); link_rx_binding_free(bind);
        link_destroy(link); link_tcp_free(tcp);
        return fail(rx_fail);
    }

    /* ── 7) 校验 ── */
    say("rx type=0x%02X plen=%u", (unsigned)cap.type, (unsigned)cap.payload_len);

    bool ok_type = (cap.type == 0x22);
    bool ok_len  = (cap.payload_len == sizeof(kRebootPayload));
    bool ok_pay  = ok_len && (memcmp(cap.payload, kRebootPayload, sizeof(kRebootPayload)) == 0);

    if (!ok_type) say("note=type_mismatch want=0x22 got=0x%02X", (unsigned)cap.type);
    if (!ok_len)  say("note=plen_mismatch want=%u got=%u",
                      (unsigned)sizeof(kRebootPayload), (unsigned)cap.payload_len);
    if (ok_len && !ok_pay) {
        /* 把实际字节打出来 —— 对锚失败时"差在哪一个字节"比"不匹配"有用得多。 */
        printf("E2E-CLIENT payload_hex=");
        for (size_t i = 0; i < cap.payload_len; i++) printf("%02x", cap.payload[i]);
        printf("\n");
    }

    say("payload_match=%s", ok_pay ? "yes" : "no");

    if (ok_type && ok_len && ok_pay) {
        say("result=PASS");
        exit_code = 0;
    } else {
        say("result=FAIL reason=%s",
            !ok_type ? "type" : (!ok_len ? "payload_len" : "payload_bytes"));
    }

    rx_pump_destroy(pump);
    link_rx_binding_free(bind);
    link_destroy(link);
    link_tcp_free(tcp);
    return exit_code;
}
