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
 *   session        —— 真实会话状态机（内部串起下面四层）
 *   link / link_tcp—— 真实链路驱动 + 状态机（部分写/背压/错误分级）
 *   link_rx_adapt  —— 真实"枚举撞车"翻译层
 *   rx_pump        —— 真实接收泵（定界；处理半条帧）
 *   wire           —— 真实帧头编码器（12 B 大端头）
 *
 * ## ⭐ 现在走的是 session_send（task-15）
 * 本程序原先**绕开** session 自己拼 link_tcp+link+rx_pump —— 原因是当时
 * session.h **没有任何上行入口**，发不出那条 0x23。task-15 给 session 补了
 * `session_send()`，本程序随即改为**只用 session**：
 *   session_create → poll 到链路通 → session_send(0x23) → 继续 poll 收 0x22。
 *
 * ## ⚠ 为什么"等链路通"**不是**等 READY（这是本卡最容易写错的地方）
 * `SESSION_READY` **只能**由 `session_note_handshake()`（收到 HelloAck）进入，
 * 而设备必须**先发 Hello** 才可能收到 HelloAck。
 * ⇒ 若在这里等 READY 才发，就是一条**功能死锁**：永远发不出 Hello，
 *   永远进不了 READY，3.0 链路完全不可用 —— 且构建/门禁全绿、一处不报错。
 * ⇒ 本程序在 `WAIT_HANDSHAKE`（"链路已通，等应用层握手"）就发。
 *   session_send 本身也**不做** READY 门控（见 session.h 的说明）。
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

#include "device_link_handshake.h"
#include "link.h"
#include "link_tcp.h"
#include "msg_handler.h"   /* task-23: 真实 msg_handler_send_hello */
#include "frame_codec.h"   /* task-23: handler 内部用到的 MSG_HELLO 等 */
#include "config_mgr.h"    /* task-23: 桩声明 */
#include "sync_manager.h"  /* task-23: 桩声明 */
#include "rgb_led.h"       /* task-23: 桩声明 */
#include "rx_pump.h"
#include "session.h"
#include "wire.h"
/* task-31：生产成帧函数（与真机同一条路径）。不引入任何 IDF 头：
 * device_link_wiring.h 只依赖 variant.h + stdint，宿主可编。 */
#include "device_link_wiring.h"
#include "frame_codec.h"   /* MSG_HELLO / MSG_HELLO_ACK 的单一来源 */
/* ===== task-25：真实 device_op 回程所需的头 ===== */
#include "transport.h"             /* 捕获 transport（真实广播路径的落点）*/
#include "msg_handler_hooks.h"     /* msg_handler_publish_checked 的**唯一**声明处 */
#include "msg_handler_device_op.h" /* msg_handler_set_device_op_hooks / _ready */
#include "device_op.h"             /* device_op_result_t / DEVICE_OP_REBOOT */
/* FreeRTOS 桩：msg_handler.c 用互斥量保护 s_current_transport。 */
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

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

/* ==========================================================================
 * task-23：Hello 改用**真实 handler 的编码**（消除手写的第二份定义）
 *
 * ## 之前的问题
 *
 * 本程序原先自己手写了一份 Hello 编码（裸数字 1/2/3/4/5/6/8/9），与真机的
 * handler_hello.c 是**同一协议的两份独立定义**。实测危害：把 handler_hello.c
 * 的 field **编号** 4 改成 10（真实破坏 —— 后端 required 集缺 4 ⇒ 不回
 * HelloAck ⇒ 设备永远进不了 READY），跨语言对锚**仍然 rc=0 / result=PASS**，
 * 因为对锚用的是客户端那份。⇒ 对锚当时证明不了"**真机的** Hello 编码是对的"。
 *
 * ## 现在怎么做（方案 A：真正一处定义，P4）
 *
 * 直接调用**生产**的 msg_handler_send_hello()（与真机同一个函数），由它按
 * HELLO_F_* 常量与 field 7 条件发送规则编码；本程序只提供
 * msg_handler_publish() 的**捕获实现**把产出的字节接住 ——
 * 这正是 host_tests/hello_handshake_tests.c:128 已在用的手法。
 *
 * ⇒ **未改任何生产文件**（没有给 handler_hello.c 加"只编码不发布"的新入口）。
 *
 * ## 代价与桩（诚实记录）
 *
 * 要编入 handler_hello.c 及其依赖，故 target 多链接了 handler_hello.c 与
 * 若干桩。桩的取值决定 field 5/6/7：
 *   config_mgr_get_epoch()                     -> field 5
 *   config_mgr_has_manifest()                  -> field 6
 *   config_mgr_get_last_known_manifest_id()    -> field 7（非空才发）
 * 下面桩返回 0 / false / "" ⇒ 与之前手写版本发**同样那 8 个字段**，对锚行为不变。
 * ========================================================================== */

/* ---- 真实 handler 所需的最小桩 ---- */
uint64_t config_mgr_get_epoch(void) { return 0; }
bool config_mgr_has_manifest(void) { return false; }
const char *config_mgr_get_last_known_manifest_id(void) { return ""; }
void sync_manager_start_config_timeout(void) {}
void sync_manager_on_downlink_received(uint8_t msg_type) { (void)msg_type; }
void rgb_led_set_state(led_state_t state) { (void)state; }
/* handler_hello.c 引用 main/ 的握手监督器；本程序不跑它。 */
bool hello_handshake_notify_ack(uint32_t nonce) { (void)nonce; return false; }

/* ══════════════════════════════════════════════════════════════════════════
 * task-25：捕获「真实产出的上行帧」
 *
 * ## 为什么捕获点是 transport 而不是 msg_handler_publish
 *
 * task-23 时本文件自己实现了一个 msg_handler_publish() 来截字节。task-25 把
 * **真实的 msg_handler.c** 编进本程序（见 CMakeLists 的 `msg_handler.c`），
 * 而**它自己就定义** msg_handler_publish() ⇒ 再定义一次会**重复符号**。
 *
 * ⭐ 而且捕获点放在 transport 层**更真实**：真实的回程是
 *    msg_handler_publish_checked → transport_broadcast_ex → transport->ops->send()
 * 把捕获做在 send() 上，等于让**真实的广播路径完整跑一遍**（含 is_connected 判定、
 * attempted/sent/mqtt_attempted 计数），**只有最后一步**（写 socket）被替换成"存起来"。
 *
 * ⚠ 3.0 路径调的是 msg_handler_process（**非** _with_transport）
 *   ⇒ s_current_transport 恒为 NULL ⇒ ACK 必走 broadcast。
 *   **这个真实形态被刻意保留** —— 改成 _with_transport 会掩盖真实回程路径。
 * ══════════════════════════════════════════════════════════════════════════ */

#define E2E_CAPTURE_CAP 512
static uint8_t s_capture[E2E_CAPTURE_CAP];
static size_t  s_capture_len;
static bool    s_captured;
static int     s_capture_calls;

static esp_err_t cap_init(transport_t *t, const void *c) { (void)t; (void)c; return ESP_OK; }
static esp_err_t cap_start(transport_t *t) { (void)t; return ESP_OK; }
static esp_err_t cap_stop(transport_t *t) { (void)t; return ESP_OK; }

/* 只把字节接住，不真的写 socket —— 这是唯一被替换的一步，且它是 I/O 不是逻辑。 */
static esp_err_t cap_send(transport_t *t, const uint8_t *d, size_t n)
{
    (void)t;
    s_capture_calls++;
    if (n == 0 || n > sizeof(s_capture)) return ESP_FAIL;
    memcpy(s_capture, d, n);
    s_capture_len = n;
    s_captured = true;
    return ESP_OK;
}

/* 恒 true，代表「3.0 会话已 READY」⇒ 广播才会真的投给它。 */
static bool cap_connected(transport_t *t) { (void)t; return true; }
static void cap_deinit(transport_t *t) { (void)t; }

static const transport_ops_t CAP_OPS = {
    .init = cap_init, .start = cap_start, .stop = cap_stop,
    .send = cap_send, .is_connected = cap_connected, .deinit = cap_deinit,
};

static transport_t *install_capture_transport(void)
{
    static transport_t t;
    memset(&t, 0, sizeof(t));
    t.ops = &CAP_OPS;
    t.type = TRANSPORT_TYPE_TCP;   /* 代表 3.0 会话 */
    t.state = TRANSPORT_CONNECTED;
    if (transport_register(&t) != ESP_OK) return NULL;
    return &t;
}

/* ══ device_op 的**真实出口**（照 main/device_op_wiring.c:75 的写法）══
 * 真实固件用 msg_handler_publish_checked（带校验）—— 不要换成不检查返回值的
 * msg_handler_publish()：device_op 依赖「失败 ⇒ 不重启」。 */
static int client_send_frame(const uint8_t *frame, size_t len)
{
    if (frame == NULL || len == 0) return -1;
    return (msg_handler_publish_checked(frame, len) == ESP_OK) ? 0 : -1;
}

/* 宿主上没有 NVS 语义；恢复出厂那条路径本卡不测，返回成功即可。 */
static int client_erase_namespace(const char *ns) { (void)ns; return 0; }

/* ⚠ 宿主上**绝不能**真重启（真机是 esp_restart()，不返回）。
 * device_op 保证它只在 ACK 送出之后才被调用。 */
static void client_restart(void) { }

static const device_op_hooks_t CLIENT_DEVOP_HOOKS = {
    .erase_namespace = client_erase_namespace,
    .send_frame      = client_send_frame,
    .restart         = client_restart,
};

/* 用**真实 handler** 编一条 Hello，返回被真实广播路径送出的字节。 */
static const uint8_t *build_hello_via_real_handler(size_t *out_len)
{
    s_captured = false;
    s_capture_len = 0;
    s_capture_calls = 0;
    /* 参数与 task-23 逐项相同，保证对锚行为不变：
     *   node_id="v3-link-node"  fw="3.0-link"  model="esp32"
     *   channel_count=1  nonce=1（非 0）
     * field 5/6/8 由 handler 依桩与 HELLO_F_* 常量决定。 */
    msg_handler_send_hello("v3-link-node", "3.0-link", "esp32", 1u, 1u);
    if (!s_captured) {
        *out_len = 0;
        return NULL;
    }
    *out_len = s_capture_len;
    return s_capture;
}
/* ══════════════════════════════════════════════════════════════════════════
 * task-25：真实 msg_handler.c 需要的宿主桩
 *
 * 手法与 host_tests/msg_handler_publish_tests.c:19-23 完全一致 ——
 * 摊入真实生产源码，只把"与本卡无关的其它 handler"桩掉。
 *
 * ⚠ 被桩掉的只是**本卡不测的那几条消息**（config/writecmd/data-ota/channel_cmd/
 *   periph/diag）。**0x22 那条链路全程是真代码**：
 *   msg_handler_process 的真实 switch → 真实 handler_device_op_process
 *   → 真实 device_op_execute → 真实钩子 → 真实 msg_handler_publish_checked
 *   → 真实 transport_broadcast_ex。
 * ══════════════════════════════════════════════════════════════════════════ */

/* FreeRTOS / 日志 / 系统桩 */
void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level; (void)tag; (void)format;   /* 与 hello_stubs/esp_log.h 的丢弃语义一致 */
}
void esp_restart(void) { }   /* ⚠ 宿主上绝不真重启 */
SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (SemaphoreHandle_t)1; }
int xSemaphoreTake(SemaphoreHandle_t sem, uint32_t ticks) { (void)sem; (void)ticks; return 1; }
int xSemaphoreGive(SemaphoreHandle_t sem) { (void)sem; return 1; }
void vSemaphoreDelete(SemaphoreHandle_t sem) { (void)sem; }

/* MQTT 兜底（msg_handler.c 广播失败后的最后一条路）。本卡走 broadcast 成功路径，
 * 不会到这里；给它一个如实失败的桩，语义与真实"没连上"一致。 */
bool mqtt_client_publish_impl(const uint8_t *data, size_t len)
{
    (void)data; (void)len;
    return false;
}

/* msg_handler.c 引用到的**其它 handler**（本卡不测它们；0x22 那条走真实实现）。
 * ⚠ handler_hello_process_ack / _ping **不在这里桩** —— 它们来自上游已链接的
 *   真实 handler_hello.c（task-23 引入）⇒ 再桩会 multiple definition。 */
void handler_config_process_manifest(frame_decoder_t *d) { (void)d; }
void handler_config_process_query(frame_decoder_t *d) { (void)d; }
void handler_config_process_query_resources(frame_decoder_t *d) { (void)d; }
void handler_writecmd_process(frame_decoder_t *d) { (void)d; }
void handler_writecmd_process_scan(frame_decoder_t *d) { (void)d; }
void handler_writecmd_process_query(frame_decoder_t *d) { (void)d; }
void handler_data_process_ota(frame_decoder_t *d) { (void)d; }
void handler_channel_cmd_v2_process(frame_decoder_t *d) { (void)d; }
void handler_periph_process(frame_decoder_t *d) { (void)d; }
void handler_diag_process_ack(frame_decoder_t *d) { (void)d; }
void on_query_resources_received(const char *request_id) { (void)request_id; }
void on_write_cmd_received(uint32_t a, uint32_t b, const uint8_t *c, size_t d,
                           uint32_t e, uint32_t f, uint32_t g)
{ (void)a;(void)b;(void)c;(void)d;(void)e;(void)f;(void)g; }
void on_scan_req_received(const char *a, uint32_t b) { (void)a; (void)b; }
void on_modbus_scan_req_received(const char *a, uint32_t b, uint32_t c, uint32_t d)
{ (void)a;(void)b;(void)c;(void)d; }
const char *channel_cmd_v2_current_boot_id(void) { return "boot"; }
uint64_t channel_cmd_v2_current_time_ms(void) { return 0; }
bool on_channel_cmd_v2_received(const struct channel_cmd_v2 *c, uint8_t s)
{ (void)c; (void)s; return false; }
bool ehome_mem_can_start(size_t n) { (void)n; return true; }

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

/** 退避抖动 [0,1000]。本程序只连一次、且对锚讲究可复现，故固定 500（无抖动）。 */
static uint32_t rand_permille(void) { return 500u; }

/* ══════════════════ 收到的帧 ══════════════════ */

typedef struct {
    bool     got;           /* 已收到**目标**帧（0x22） */
    bool     got_hello_ack; /* 已收到过 0x12 HelloAck（累计） */
    /* ⭐ "**本轮**收到的帧类型"（供 dlhs_decide）。
     *
     * 为什么需要"本轮"这个粒度、而不是"最近一条"：一轮 poll 里 rx_pump 可能
     * 交付**多条**消息（定界器一次 feed 最多出一条，但循环会继续）。
     * 若只记"最近一条"，当 HelloAck 与 0x22 在同一轮到达时，0x12 会被 0x22
     * 覆盖掉 ⇒ 握手决策看不到 HelloAck ⇒ 永远进不了 READY。
     * 这是"半条帧/多帧同轮"这一族缺陷的又一变体。 */
    bool     round_saw_hello_ack;
    uint8_t  last_type;
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

    cap->last_type = m->type;

    /* HelloAck(0x12)：握手推进用；**不算**目标帧，继续泵（同轮可能还有 0x22）。 */
    if (m->type == MSG_HELLO_ACK) {
        cap->got_hello_ack = true;
        cap->round_saw_hello_ack = true;
        return true;
    }

    if (cap->got) return false;              /* 目标帧只要第一条 */

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

    /* ══ task-25：先把"上行捕获"装好 —— Hello 与 ACK 两次上行都要经过它 ══
     * 时机很关键：msg_handler_send_hello() 也走 msg_handler_publish_checked →
     * transport_broadcast_ex ⇒ **Hello 第一次发之前**就必须有 transport 在册，
     * 否则广播找不到落点、Hello 一个字节都发不出去（实测症状：
     * note=real_handler_produced_no_hello）。
     * device_op 的钩子同样一次装好；两者都幂等。 */
    transport_manager_init();
    if (install_capture_transport() == NULL) return fail("capture_register");
    msg_handler_set_device_op_hooks(&CLIENT_DEVOP_HOOKS);
    if (!msg_handler_device_op_ready()) return fail("devop_hooks");

    /* ── 1) 真实 session（内部就是 link_tcp + link_rx_adapt + rx_pump）── */
    static uint8_t rbuf[E2E_RX_BUF_CAP];
    rx_capture_t cap;
    memset(&cap, 0, sizeof(cap));

    session_config_t scfg;
    memset(&scfg, 0, sizeof(scfg));
    scfg.io           = &TCP_IO;
    scfg.io_ctx       = &ctx;
    scfg.max_payload  = E2E_MAX_PAYLOAD;
    scfg.rx_buf       = rbuf;
    scfg.rx_buf_cap   = sizeof(rbuf);
    scfg.now_ms       = now_ms;
    scfg.rand_permille = rand_permille;
    scfg.on_msg       = on_msg;
    scfg.on_msg_ctx   = &cap;

    session_t *sess = session_create(&scfg);
    if (sess == NULL) return fail("session_create");

    /* ── 2) poll 到"链路已通" ──
     * ⚠ 这里**不能**等 SESSION_READY（那要收到 HelloAck，而 Hello 还没发）。
     *   链路刚建成的那一态是 WAIT_HANDSHAKE —— 正是发 Hello 的时机。 */
    uint64_t conn_deadline = now_ms() + E2E_CONNECT_TIMEOUT_MS;
    session_state_t st = session_state(sess);
    while (now_ms() < conn_deadline) {
        (void)session_poll(sess, NULL);
        st = session_state(sess);
        if (st == SESSION_WAIT_HANDSHAKE || st == SESSION_READY) break;
        if (st == SESSION_FATAL) {
            say("note=session_fatal");
            session_destroy(sess);
            return fail("session_fatal");
        }
        /* DOWN / BACKOFF：继续等（BACKOFF 到点后 poll 会重连） */
    }
    if (st != SESSION_WAIT_HANDSHAKE && st != SESSION_READY) {
        say("note=session_state=%s", session_state_name(st));
        session_destroy(sess);
        return fail("connect");
    }
    say("connect=ok");

    /* ══════════ 3) 应用层握手：发 Hello(0x01) → 等 HelloAck(0x12) → READY ══════════
     *
     * ⚠ 关键：**必须在 WAIT_HANDSHAKE 就发 Hello**。
     *   READY 只能由 session_note_handshake()（收到 HelloAck）进入，而设备
     *   必须先发 Hello 才可能收到 HelloAck ⇒ 若在这里等 READY 再发，就是一条
     *   功能死锁（永远发不出 Hello、永远进不了 READY）。
     *   决策用**纯函数** dlhs_decide（与 main/device_link_wiring.c 用的是同一份，
     *   所以这里测到的规则就是固件里跑的规则）。 */
    uint8_t hello_frame[128];
    size_t  hello_len = 0;
    {
        /* ⚠ task-23：**不再手写字段号**。
         *
         * 这里调用**生产**的 msg_handler_send_hello()（与真机同一个函数），
         * 由它按 HELLO_F_* 常量与 field 7 条件发送规则编码；本文件顶部提供了
         * msg_handler_publish() 的捕获实现把字节接住。
         *
         * 为什么必须这样（实测危害）：原先手写的这份与 handler_hello.c 是同一
         * 协议的两份独立定义。把 handler_hello.c 的 field **编号** 4 改成 10 后，
         * 对锚仍然 rc=0/PASS —— 因为对锚用的是手写那份，真机那份改了它毫发无损。
         * ⇒ 对锚证明不了"真机的 Hello 编码是对的"。改成调用真实函数后，
         * handler 的任何编号漂移都会直接改变本程序发出的字节。
         *
         * 后端 parseHello 的 required 是 {1,2,3,4,5,6,8,9}；缺一个就整条被拒
         * ⇒ 不回 HelloAck ⇒ 设备永远进不了 READY。该完整性由 handler 保证，
         * 并由 host_tests/hello_handshake_tests.c 的逐字段断言守护。 */
        size_t hello_payload_len = 0;
        const uint8_t *hello_payload = build_hello_via_real_handler(&hello_payload_len);
        if (hello_payload == NULL || hello_payload_len == 0) {
            say("note=real_handler_produced_no_hello");
            session_destroy(sess);
            return fail("hello_encode");
        }
        /* ⭐⭐ task-31（§122 同型收口）：**调用生产成帧函数**，不再自己拼头。
         *
         * 缺陷回顾：本程序原先自己 memcpy + wire_encode_header ⇒ 它证明的是
         * "一个**会正确成帧的客户端**能与后端互通"，**不是**"生产固件能与后端
         * 互通"——两者是**两个不同的程序**。真机那边全程不成帧
         * （devlink_send_frame 直接把 payload 交给 session_send），
         * 后端 protoframe.DecodeHeader 一律 ErrMagic ⇒ 设备永远进不了 READY，
         * 而固件侧一处都不报错。
         *
         * 现在这里调的是**真机同一条** devlink_encode_frame
         * （main/device_link_wiring.c 的纯判定段，本 target 已编入它）。
         * ⇒ 生产成帧一旦漂移（忘了头/type 取自 payload[0]/payload_len 错），
         * 对锚**立刻**红。这正是本卡要永久钉住的那一类。
         *
         * ⚠ 别改回自己拼头：那就把本测试变回"测一个不存在的客户端"。 */
        {
            size_t out_len = 0;
            int frc = devlink_encode_frame(hello_frame, sizeof(hello_frame),
                                           hello_payload, hello_payload_len,
                                           /* seq */ 0u, &out_len);
            if (frc != DEVLINK_FRAME_OK) {
                say("note=devlink_encode_frame rc=%s", devlink_frame_err_name(frc));
                session_destroy(sess);
                return fail("hello_encode");
            }
            hello_len = out_len;
        }
    }

    /* 发送 Hello：按 link.h 的 progress 循环（PARTIAL 续写，BACKPRESSURE 整帧重试）。 */
    {
        size_t progress = 0;
        uint64_t tx_deadline = now_ms() + 5000;
        for (;;) {
            link_result_t lr = session_send(sess, hello_frame, hello_len, &progress);
            if (lr == LINK_SENT_FULL) break;
            if (lr == LINK_SENT_PARTIAL) continue;
            if (lr == LINK_BACKPRESSURE) {
                if (now_ms() > tx_deadline) { session_destroy(sess); return fail("hello_backpressure"); }
                continue;
            }
            say("note=hello_send_rc=%s", link_result_name(lr));
            session_destroy(sess);
            return fail("hello_send");
        }
    }
    say("sent type=0x01");

    /* 等 HelloAck(0x12) —— 用**同一个** dlhs_decide 驱动，证明"收到 0x12 ⇒ note"。 */
    {
        uint64_t hs_deadline = now_ms() + E2E_RX_TIMEOUT_MS;
        bool noted = false;
        while (!noted && now_ms() < hs_deadline) {
            uint32_t delivered = 0;
            cap.round_saw_hello_ack = false;   /* 清"本轮"标记 */
            session_state_t ps = session_poll(sess, &delivered);

            /* 本轮收到的类型（无则 0）—— 与 dlhs_decide 的入参语义一致 */
            uint8_t rx_type = cap.round_saw_hello_ack ? MSG_HELLO_ACK : 0;
            if (dlhs_decide(ps, true /* hello 已发 */, rx_type, true) == DLHS_NOTE_HANDSHAKE) {
                session_note_handshake(sess);
                noted = true;
                break;
            }
            if (ps == SESSION_BACKOFF || ps == SESSION_FATAL) {
                say("note=session_state=%s", session_state_name(ps));
                session_destroy(sess);
                return fail("hello_closed");
            }
        }
    }
    if (session_state(sess) != SESSION_READY) {
        say("note=state=%s", session_state_name(session_state(sess)));
        session_destroy(sess);
        return fail("no_hello_ack");
    }
    say("state=READY");

    /* ══════════════════════════════════════════════════════════════════════
     * ── 4) ⭐ task-25：**先收真实的 0x22**，再由真实回程产出 0x23，最后发出去 ──
     *
     * 为什么次序变成"先收后发"：这就是**因果序**。
     * 设备的 ACK 是对某一条命令的回执；没收到命令就产生不了回执。
     * （后端 device_e2e_firmware_test.go 已相应改序：先逐字节发 0x22，再读 0x23。）
     *
     * ⚠ 收帧走 session_poll（内部是 rx_pump_step + wire 定界），因此
     *   "后端每次 1 字节写来"的**半条帧**必须仍能被正确组装 —— 这是本程序
     *   原有能力，不得退化。
     *
     * 状态机语义：poll 在 WAIT_HANDSHAKE / READY 两态都会泵读。
     * 链路掉了会进 BACKOFF（本程序记为 rx_closed_by_peer，不假装是超时）。
     * ══════════════════════════════════════════════════════════════════════ */
    uint64_t deadline = now_ms() + E2E_RX_TIMEOUT_MS;
    int exit_code = 1;
    const char *rx_fail = "rx_timeout";   /* 默认：真的等满了 */

    while (!cap.got && now_ms() < deadline) {
        uint32_t delivered = 0;
        session_state_t ps = session_poll(sess, &delivered);
        if (ps == SESSION_BACKOFF || ps == SESSION_FATAL) {
            /* 链路不可用（对端关闭/硬错误）。如实报，不混进 rx_timeout ——
             * 那会把人引向"再等等"而不是"对端为什么关了"。 */
            say("note=session_state=%s", session_state_name(ps));
            rx_fail = "rx_closed_by_peer";
            break;
        }
        /* WAIT_HANDSHAKE / READY：继续泵（收到后 cap.got 置位，循环退出） */
    }

    if (!cap.got) {
        session_destroy(sess);
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

    /* ══════════════════════════════════════════════════════════════════════
     * ── 5) ⭐⭐ task-25 的核心：让**真实链路**产出 0x23，而不是手写字节 ──
     *
     * 此前这里是 kAckOkPayload 这个手写常量（"一个手写的正确形状 ACK 能被后端接受"）
     * ⇒ 对锚证明不了"**设备真的会产生**这条 ACK"。现在改成：
     *
     *   收到的 0x22 载荷
     *     -> msg_handler_process（**真实**分发 switch，按 payload 首字节取类型）
     *     -> handler_device_op_process（**真实**解析）
     *     -> device_op_execute（**真实**执行：宿主上 restart 是空桩，不真重启）
     *     -> 钩子 send_frame -> msg_handler_publish_checked（**真实**出口）
     *     -> transport_broadcast_ex（**真实**广播，s_current_transport 为 NULL）
     *     -> 捕获 transport 的 send()（**唯一被替换的一步：存下来而不是写 socket**）
     *
     * 然后把这份**真实产出的字节**经真实 session_send 发回后端。
     * ══════════════════════════════════════════════════════════════════════ */
    {
        if (!ok_type || !ok_len || !ok_pay) {
            /* 载荷不对就不该拿它去驱动真实执行 —— 那会把"收到的字节错"
             * 掩盖成"回执错"。失败原因上面已经报过了。 */
            say("result=FAIL reason=%s",
                !ok_type ? "type" : (!ok_len ? "payload_len" : "payload_bytes"));
            session_destroy(sess);
            return 1;
        }

        /* 准备真实回程：注册捕获 transport（广播的落点）+ 注入 device_op 三个原语。 */
        /* 回程所需的两件事已在启动时装好（见 main 开头的 setup 段）。 */
        s_captured = false;
        s_capture_len = 0;
        s_capture_calls = 0;

        /* ⭐ 真实分发。注意传的是**整个载荷**（首字节 0x22 = 类型，2.x 约定）。 */
        msg_handler_process(cap.payload, cap.payload_len);

        if (!s_captured || s_capture_len == 0) {
            /* 真实回程没产出任何帧 ⇒ 广播没投到任何 transport。
             * 这正是 task-19 报告过的"3.0 未注册 ⇒ 回执发不出去"形态。 */
            say("note=real_ack_not_produced broadcast_calls=%d", s_capture_calls);
            session_destroy(sess);
            return fail("no_real_ack");
        }

        /* 断言真实产出的字节 == 共享向量（后端能接受的那串）。
         * 这一条把"设备真的会产生这条 ACK"钉住 —— 手写常量的时代结束了。 */
        if (s_capture_len != sizeof(kAckOkPayload) ||
            memcmp(s_capture, kAckOkPayload, sizeof(kAckOkPayload)) != 0) {
            printf("E2E-CLIENT real_ack_hex=");
            for (size_t i = 0; i < s_capture_len; i++) printf("%02x", s_capture[i]);
            printf("\n");
            say("note=real_ack_mismatch len=%u", (unsigned)s_capture_len);
            session_destroy(sess);
            return fail("real_ack_bytes");
        }

        /* 发出去：**真实 wire 编码器** + **真实 session_send**。
         * 载荷用的是**真实回程产出的字节**，不是 kAckOkPayload 这个常量本身。 */
        uint8_t frame[WIRE_HEADER_BYTES + E2E_CAPTURE_CAP];
        /* ⭐ task-31：同样改走**生产成帧函数**（此前是自己 memset+wire_encode_header）。
         * 与 Hello 那条同一收口，见上面的长注释。 */
        size_t tx_len = 0;
        {
            int frc = devlink_encode_frame(frame, sizeof(frame), s_capture, s_capture_len,
                                           /* seq */ 0u, &tx_len);
            if (frc != DEVLINK_FRAME_OK) {
                say("note=devlink_encode_frame rc=%s", devlink_frame_err_name(frc));
                session_destroy(sess);
                return fail("wire_encode");
            }
        }

        /* 按 link.h 规定的续写模式推进 progress：
         *   PARTIAL（写了一部分）⇒ 继续写；BACKPRESSURE（一字节没写出）⇒ 稍后重试。
         * 绝不重发整帧 —— 已上线字节再写一遍会让接收端定界器无法自愈（D-30）。 */
        size_t progress = 0;
        uint64_t tx_deadline = now_ms() + 5000;
        for (;;) {
            link_result_t lr = session_send(sess, frame, tx_len, &progress);
            if (lr == LINK_SENT_FULL) break;
            if (lr == LINK_SENT_PARTIAL) continue;
            if (lr == LINK_BACKPRESSURE) {
                if (now_ms() > tx_deadline) {
                    session_destroy(sess);
                    return fail("tx_backpressure_timeout");
                }
                continue;
            }
            say("note=session_send_rc=%s progress=%zu", link_result_name(lr), progress);
            session_destroy(sess);
            return fail("send");
        }
        say("sent type=0x23 len=%u", (unsigned)s_capture_len);
    }

    say("result=PASS");
    exit_code = 0;

    session_destroy(sess);
    return exit_code;
}
