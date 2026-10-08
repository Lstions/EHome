/* msg_handler_publish_tests.c —— D-08：L-02 的【源码级】调用点保证
 *
 * 为什么需要它：此前锁定 L-02 的用例（l02_publish_dedup_callsite_tests.c）
 * **不编译生产代码** —— 它复刻了一份决策结构并对自己的副本断言，
 * 文件头自陈"不能证明生产源码里那一行当前就是去重版本"。
 * 删掉 msg_handler.c 里的分支，它仍然全绿。
 *
 * 本用例改为 **#include 真实的 msg_handler.c**，因此：
 *   删掉/改错生产源码里的去重分支 => 本用例必红。
 *
 * 被测决策（msg_handler_publish_checked）：
 *   1. 广播【确实尝试过】MQTT 且失败  => 【不】再单独发布（否则同一帧发两次 = L-02）
 *   2. 广播【没有尝试过】MQTT 且失败  => 回退单独发布一次（保底路径仍在）
 * 两种情形的区别由 transport_broadcast_ex() 的 mqtt_attempted 给出（D-01）。
 */
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

/* 先引入宿主桩的自由度依赖，再摊入生产源码 ——
 * 否则 stubs/driver/uart.h 会在 esp_err_t 定义之前被看到。
 * （这是既有宿主测试的通行做法，见 bus_worker_rx_tests.c 的 include 顺序。） */
#include "esp_err.h"
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

/* ---- FreeRTOS / 日志桩（沿用既有多数宿主测试的内联做法）---- */
void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level; (void)tag; (void)format;
}
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_OK"; }
void esp_restart(void) { }
SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (SemaphoreHandle_t)1; }
int xSemaphoreTake(SemaphoreHandle_t sem, uint32_t ticks) { (void)sem; (void)ticks; return 1; }
int xSemaphoreGive(SemaphoreHandle_t sem) { (void)sem; return 1; }
void vSemaphoreDelete(SemaphoreHandle_t sem) { (void)sem; }

/* 真实生产源码（与 scheduler_route_tests / bus_worker_*_tests 同一手法）。 */
#include "../components/msg_handler/msg_handler.c"

static int s_failures = 0;
#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond); s_failures++; } \
    } while (0)

/* === 计数桩：这是被断言的对象 === */
static int s_mqtt_publish_calls = 0;
static bool s_mqtt_publish_result = true;

bool mqtt_client_publish_impl(const uint8_t *data, size_t len)
{
    (void)data; (void)len;
    s_mqtt_publish_calls++;
    return s_mqtt_publish_result;
}

/* === msg_handler.c 引用到的其它 handler（本用例不测它们）=== */
void handler_hello_process_ack(frame_decoder_t *d) { (void)d; }
void handler_hello_process_ping(frame_decoder_t *d) { (void)d; }
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
bool on_channel_cmd_v2_received(const struct channel_cmd_v2 *c, uint8_t s) { (void)c; (void)s; return false; }
bool ehome_mem_can_start(size_t n) { (void)n; return true; }

/* === 假的 transport === */
typedef struct { int send_calls; bool connected; esp_err_t ret; } fake_t;

static esp_err_t ft_init(transport_t *t, const void *c) { (void)t; (void)c; return ESP_OK; }
static esp_err_t ft_start(transport_t *t) { (void)t; return ESP_OK; }
static esp_err_t ft_stop(transport_t *t) { (void)t; return ESP_OK; }
static esp_err_t ft_send(transport_t *t, const uint8_t *d, size_t n)
{
    fake_t *f = (fake_t *)t->priv_data;
    (void)d; (void)n;
    f->send_calls++;
    return f->ret;
}
static bool ft_conn(transport_t *t) { return ((fake_t *)t->priv_data)->connected; }
static void ft_deinit(transport_t *t) { (void)t; }

static const transport_ops_t FT_OPS = {
    .init = ft_init, .start = ft_start, .stop = ft_stop,
    .send = ft_send, .is_connected = ft_conn, .deinit = ft_deinit,
};

static void reset_all(void)
{
    transport_manager_init();
    s_mqtt_publish_calls = 0;
    s_mqtt_publish_result = true;
    s_current_transport = NULL;   /* 强制走 broadcast 路径 */
}

/* 1) 【L-02 核心】广播确实尝试过 MQTT 且失败 => 【不】再单独发布。
 *    去掉生产源码里的 rep.mqtt_attempted 分支，本条必红。 */
static void test_no_republish_when_broadcast_attempted_mqtt(void)
{
    reset_all();
    fake_t f = { 0, true, ESP_FAIL };          /* 已连接、发送失败 */
    transport_t *t = (transport_t *)calloc(1, sizeof(*t));
    t->ops = &FT_OPS; t->type = TRANSPORT_TYPE_MQTT; t->priv_data = &f;
    CHECK(transport_register(t) == ESP_OK);

    esp_err_t r = msg_handler_publish_checked((const uint8_t *)"frame", 5);

    CHECK(f.send_calls == 1);                  /* 广播内尝试过一次 */
    CHECK(s_mqtt_publish_calls == 0);          /* ← 关键：不得再单独发一次 */
    CHECK(r != ESP_OK);                        /* 失败被如实上报 */

    (void)transport_unregister(t);
    free(t);
}

/* 2) ⭐ 2026-10-08（§194）：**MQTT 直发兜底已随 MQTT 一起删除**。
 *
 * 原用例断言"广播没试过 MQTT ⇒ 回退单独发布一次"（保底路径仍在）。
 * 该保底路径已从 msg_handler.c 删除 ⇒ 原断言测的是**已不存在的行为**。
 *
 * 但它守护的**真正**不变式仍然要守，只是换成了反面：
 *   广播没尝试过任何 transport 且失败 ⇒ **调用方必须看到失败**，
 *   且**不得**有任何第二条出口把这一帧偷偷发出去。
 * 这正是 L-02 教训的另一面：兜底若与主路径指向同一出口，它不是兜底、是重试两次；
 * 而现在**根本没有兜底** ⇒ 更要说清"失败就是失败"。
 *
 * ⚠ 它凭什么会失败：若有人重新引入一条"失败后再直发一次"的路径
 *   （例如为了让上行更"可靠"），send_calls 会变成 1、s_mqtt_publish_calls 也会 >0。 */
static void test_no_fallback_after_broadcast_failure(void)
{
    reset_all();
    fake_t f = { 0, false, ESP_FAIL };         /* 未连接 => 广播会跳过它 */
    transport_t *t = (transport_t *)calloc(1, sizeof(*t));
    t->ops = &FT_OPS; t->type = TRANSPORT_TYPE_MQTT; t->priv_data = &f;
    CHECK(transport_register(t) == ESP_OK);

    esp_err_t r = msg_handler_publish_checked((const uint8_t *)"frame", 5);

    CHECK(f.send_calls == 0);                  /* 未连接，广播没调 send */
    CHECK(s_mqtt_publish_calls == 0);          /* ← 关键：**没有**任何兜底出口 */
    CHECK(r != ESP_OK);                        /* 失败必须如实上报，不被压成 ESP_OK（D-01） */

    (void)transport_unregister(t);
    free(t);
}

/* 3) 广播成功 => 直接返回，任何情况下都不再单独发布 */
static void test_no_republish_on_success(void)
{
    reset_all();
    fake_t f = { 0, true, ESP_OK };
    transport_t *t = (transport_t *)calloc(1, sizeof(*t));
    t->ops = &FT_OPS; t->type = TRANSPORT_TYPE_MQTT; t->priv_data = &f;
    CHECK(transport_register(t) == ESP_OK);

    CHECK(msg_handler_publish_checked((const uint8_t *)"frame", 5) == ESP_OK);
    CHECK(s_mqtt_publish_calls == 0);

    (void)transport_unregister(t);
    free(t);
}

/* 4) 空注册表（既没尝试也没成功）=> 如实失败，且**没有**兜底出口。
 *
 * 原用例断言"走回退一次"（s_mqtt_publish_calls == 1）。兜底已删 ⇒ 断言反转。
 * 保留它的价值：这是"一条出口都没有"的**最坏情形**，最能暴露
 * "为了显得可靠而偷偷补发"这类回退（那会让本用例立刻红）。 */
static void test_no_transport_fails_without_fallback(void)
{
    reset_all();
    esp_err_t r = msg_handler_publish_checked((const uint8_t *)"frame", 5);
    CHECK(s_mqtt_publish_calls == 0);          /* 无兜底 */
    CHECK(r != ESP_OK);                        /* 如实失败（D-01：不得压平成 ESP_OK）*/
}

int main(void)
{
    test_no_republish_when_broadcast_attempted_mqtt();
    test_no_fallback_after_broadcast_failure();
    test_no_republish_on_success();
    test_no_transport_fails_without_fallback();
    if (s_failures) { printf("msg_handler_publish_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("msg_handler_publish_tests: all checks passed\n");
    return 0;
}
