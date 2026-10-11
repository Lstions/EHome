/* ehome_tcp_send_tests.c —— D-10：短写必须被续写；只有全部写出才算成功
 *
 * 为什么需要它：D-10 的修复（循环续写 + 只在 WRITE_COMPLETE 时计入成功）
 * 此前只经过"编译通过"的检验 —— ehome_tcp.c 从未被宿主编译器看过一眼。
 * 本用例直接编译【真实生产源码】，并用可控的假 send() 逼出各种写结果，
 * 把"短写会不会静默上线半帧"变成可断言的事实。
 *
 * 拦截方式：提供 lwip/sockets.h 桩（只声明不定义），
 * 由本 TU 定义 send() —— 于是生产代码里的 send() 调用落到这里。
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <errno.h>

#include "esp_err.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "freertos/semphr.h"
/* 先看到 socket 类型（struct sockaddr_in / socklen_t 等），
 * 否则本文件里的 socket 桩定义会因类型未知而失败。 */
#include "lwip/sockets.h"

/* ---- 假 send：按脚本返回，记录调用次数与累计字节 ---- */
#define SEND_SCRIPT_MAX 8
typedef struct {
    ssize_t  results[SEND_SCRIPT_MAX];  /* 依次返回的值；<0 表示错误 */
    size_t   count;
    size_t   idx;
    int      calls;                     /* 实际被调用次数 */
    size_t   total_bytes;               /* 实际被"写出"的字节总数 */
    int      last_errno;
} send_script_t;

static send_script_t s_script;

ssize_t send(int s, const void *data, size_t size, int flags)
{
    (void)s; (void)data; (void)flags;
    s_script.calls++;
    if (s_script.idx >= s_script.count) {
        /* 脚本用尽：视为可重试（避免测试挂死），但也不推进 */
        errno = EAGAIN;
        return -1;
    }
    ssize_t r = s_script.results[s_script.idx++];
    if (r < 0) {
        errno = s_script.last_errno;
        return -1;
    }
    if ((size_t)r > size) r = (ssize_t)size;   /* 不许"写出"超过请求量 */
    s_script.total_bytes += (size_t)r;
    return r;
}

/* ---- 其余 socket API：本用例不触发，给最小实现以便链接 ---- */
int socket(int d, int t, int p) { (void)d; (void)t; (void)p; return -1; }
int bind(int s, const struct sockaddr *n, socklen_t l) { (void)s; (void)n; (void)l; return -1; }
int listen(int s, int b) { (void)s; (void)b; return -1; }
int accept(int s, struct sockaddr *a, socklen_t *l) { (void)s; (void)a; (void)l; return -1; }
int setsockopt(int s, int lv, int o, const void *v, socklen_t l) { (void)s;(void)lv;(void)o;(void)v;(void)l; return 0; }
ssize_t recv(int s, void *m, size_t l, int f) { (void)s;(void)m;(void)l;(void)f; return -1; }
uint16_t htons(uint16_t v) { return v; }

/* ---- FreeRTOS / 日志 / esp_system 桩 ---- */
void host_test_log_record(char level, const char *tag, const char *format, ...)
{ (void)level; (void)tag; (void)format; }
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_OK"; }
void esp_restart(void) { }
SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (SemaphoreHandle_t)1; }
int xSemaphoreTake(SemaphoreHandle_t s, uint32_t t) { (void)s; (void)t; return 1; }
int xSemaphoreGive(SemaphoreHandle_t s) { (void)s; return 1; }
void vSemaphoreDelete(SemaphoreHandle_t s) { (void)s; }
/* xTaskCreate / vTaskDelay / vTaskDelete 由 stubs/freertos/task.h 提供
 * （static inline）—— 这里重复定义会冲突。 */

/* 真实生产源码 */
#include "../components/ehome_tcp/ehome_tcp.c"

static int s_failures = 0;
#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond); s_failures++; } \
    } while (0)

/* 组装一个"已连接一个客户端"的 transport（直接用真实私有结构）。 */
static tcp_client_t      s_clients[4];
static tcp_transport_priv_t s_priv;
static transport_t       s_transport;

static void fixture(int n_clients)
{
    memset(&s_clients, 0, sizeof(s_clients));
    memset(&s_priv, 0, sizeof(s_priv));
    memset(&s_transport, 0, sizeof(s_transport));

    s_priv.config.max_clients = 4;
    s_priv.clients = s_clients;
    s_priv.clients_mutex = (SemaphoreHandle_t)1;
    /* tcp_send() 的入口条件是 `!priv->clients || priv->client_count == 0`
     * ⇒ client_count 必须一并设置，否则会提前返回 INVALID_STATE，
     * 根本走不到发送循环（这个坑第一次就踩到了）。 */
    s_priv.client_count = n_clients;
    for (int i = 0; i < n_clients; i++) {
        s_clients[i].active = true;
        s_clients[i].socket = 100 + i;
    }
    s_transport.priv_data = &s_priv;
    s_transport.type = TRANSPORT_TYPE_TCP;
    s_transport.ops = &tcp_ops;

    memset(&s_script, 0, sizeof(s_script));
}

static void script(ssize_t a, ssize_t b, ssize_t c)
{
    s_script.results[0] = a; s_script.results[1] = b; s_script.results[2] = c;
    s_script.count = 3;
}

/* 1) 一次写完 => 成功，且字节账目正确 */
static void test_full_write_in_one_call(void)
{
    fixture(1);
    script(10, 0, 0);
    CHECK(tcp_send(&s_transport, (const uint8_t *)"0123456789", 10) == ESP_OK);
    CHECK(s_script.calls == 1);
    CHECK(s_script.total_bytes == 10);
}

/* 2) 【D-10 核心】部分写 => 必须【继续写】，直到写完才算成功。
 *    旧实现把第一次 send>0 当成功 ⇒ 对端只收到半帧。 */
static void test_partial_write_is_continued(void)
{
    fixture(1);
    s_script.results[0] = 4;
    s_script.results[1] = 3;
    s_script.results[2] = 3;
    s_script.count = 3;

    CHECK(tcp_send(&s_transport, (const uint8_t *)"0123456789", 10) == ESP_OK);
    CHECK(s_script.calls == 3);            /* ← 续写了两次 */
    CHECK(s_script.total_bytes == 10);     /* ← 全部写完 */
}

/* 3) 部分写后遇硬错误 => 【不得】算作成功（否则上层以为送达） */
static void test_partial_then_hard_error_is_not_success(void)
{
    fixture(1);
    s_script.results[0] = 4;
    s_script.results[1] = -1;
    s_script.count = 2;
    s_script.last_errno = ECONNRESET;      /* 不可重试的硬错误 */

    CHECK(tcp_send(&s_transport, (const uint8_t *)"0123456789", 10) != ESP_OK);
    CHECK(s_script.total_bytes == 4);
}

/* 4) 一个字节都没写出（硬错误）=> 同样不算成功 */
static void test_immediate_hard_error_is_not_success(void)
{
    fixture(1);
    s_script.results[0] = -1;
    s_script.count = 1;
    s_script.last_errno = EPIPE;

    CHECK(tcp_send(&s_transport, (const uint8_t *)"0123456789", 10) != ESP_OK);
    CHECK(s_script.calls == 1);
}

/* 5a) 连接数为 0 => ESP_ERR_INVALID_STATE（入口校验），且【不调用】send */
static void test_no_clients_at_all(void)
{
    fixture(0);
    CHECK(tcp_send(&s_transport, (const uint8_t *)"x", 1) == ESP_ERR_INVALID_STATE);
    CHECK(s_script.calls == 0);
}

/* 5b) 有连接记录但都【不活跃】=> 走完循环、一个都没发 => ESP_FAIL，且不调用 send */
static void test_clients_present_but_inactive(void)
{
    fixture(1);
    s_clients[0].active = false;      /* 记录在，但不活跃 */
    CHECK(tcp_send(&s_transport, (const uint8_t *)"x", 1) == ESP_FAIL);
    CHECK(s_script.calls == 0);
}

/* 6) 参数校验 */
static void test_bad_args(void)
{
    fixture(1);
    CHECK(tcp_send(NULL, (const uint8_t *)"x", 1) == ESP_ERR_INVALID_ARG);
    CHECK(tcp_send(&s_transport, NULL, 1) == ESP_ERR_INVALID_ARG);
    CHECK(tcp_send(&s_transport, (const uint8_t *)"x", 0) == ESP_ERR_INVALID_ARG);
    CHECK(s_script.calls == 0);
}

int main(void)
{
    test_full_write_in_one_call();
    test_partial_write_is_continued();
    test_partial_then_hard_error_is_not_success();
    test_immediate_hard_error_is_not_success();
    test_no_clients_at_all();
    test_clients_present_but_inactive();
    test_bad_args();
    if (s_failures) { printf("ehome_tcp_send_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("ehome_tcp_send_tests: all checks passed\n");
    return 0;
}
