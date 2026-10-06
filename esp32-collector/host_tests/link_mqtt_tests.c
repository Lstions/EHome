/* link_mqtt_tests.c —— 3.0 link 迁移：四态必须【原样】到达调用方
 *
 * 这是 D-01 那条链的终点：
 *   mqtt_client_publish_ex（四态）
 *     -> 旧路径 transport_ops.send 压平成 esp_err_t（两态）   ← D-01 的病灶
 *     -> 新路径 link_mqtt_driver 原样映射成 link_result_t（五态）
 *
 * 本用例编译【真实生产源码】link_mqtt.c，逐个断言四种底层结果
 * 对应的 link_result_t —— 任何"压平"都会让某一条变红。
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "esp_err.h"
#include "ehome_mqtt.h"

/* ---- 可控的底层桩 ---- */
static mqtt_publish_result_t s_publish_result = MQTT_PUBLISH_OK;
static int  s_publish_calls = 0;
static bool s_connected = false;
static size_t s_last_len = 0;

mqtt_publish_result_t mqtt_client_publish_ex(const uint8_t *data, size_t len)
{
    (void)data;
    s_publish_calls++;
    s_last_len = len;
    return s_publish_result;
}

bool mqtt_client_is_connected_impl(void) { return s_connected; }

void host_test_log_record(char level, const char *tag, const char *format, ...)
{ (void)level; (void)tag; (void)format; }
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_OK"; }

/* 真实生产源码由 CMake 编译并链接（本用例只用它的【公开 API】，
 * 不需要访问 static 函数 —— 与 bus_worker_*_tests 那种"摊入 TU"的
 * 做法不同：能走公开接口就不要 #include .c，否则会重复定义）。 */
#include "link_mqtt.h"

static int s_failures = 0;
#define CHECK(cond)                                                          \
    do {                                                                     \
        if (!(cond)) { printf("FAIL %s:%d  %s\n", __FILE__, __LINE__, #cond); s_failures++; } \
    } while (0)

/* 默认把底层置为"已连接" —— 否则 link_send 的规则 3（is_ready 检查）
 * 会在调用驱动【之前】就返回 LINK_NOT_READY，测不到驱动的映射。
 * （第一次就踩到了：失败信息看起来像"映射错了"，实际是夹具没连上。） */
static link_t *make_link(void)
{
    s_connected = true;
    return link_create(link_mqtt_driver(), NULL);
}

/* 1) 【核心】四种底层结果 -> 四种 link_result_t，逐条映射，不许压平。
 *    注意 NOT_READY 与 BACKPRESSURE 与 FATAL 三者【互不相同】——
 *    这正是旧 esp_err_t 做不到的。 */
static void test_all_four_states_survive(void)
{
    link_t *l = make_link();
    CHECK(l != NULL);

    uint8_t frame[8] = {0};

    s_publish_result = MQTT_PUBLISH_OK;
    CHECK(link_send(l, frame, 8) == LINK_SENT);

    s_publish_result = MQTT_PUBLISH_NOT_CONNECTED;
    CHECK(link_send(l, frame, 8) == LINK_NOT_READY);

    s_publish_result = MQTT_PUBLISH_BACKPRESSURE;
    CHECK(link_send(l, frame, 8) == LINK_BACKPRESSURE);

    s_publish_result = MQTT_PUBLISH_FAILED;
    CHECK(link_send(l, frame, 8) == LINK_FATAL);

    link_destroy(l);
}

/* 2) 【D-01 的直接对照】背压与真失败必须【可区分】。
 *    旧路径两者都是 ESP_FAIL —— 调用方无法决定"该退避"还是"该报错"。 */
static void test_backpressure_is_distinct_from_failure(void)
{
    link_t *l = make_link();
    uint8_t frame[8] = {0};

    s_publish_result = MQTT_PUBLISH_BACKPRESSURE;
    link_result_t bp = link_send(l, frame, 8);
    s_publish_result = MQTT_PUBLISH_FAILED;
    link_result_t ft = link_send(l, frame, 8);

    CHECK(bp != ft);                    /* ← 旧实现里这两个是同一个值 */
    CHECK(bp == LINK_BACKPRESSURE);
    CHECK(ft == LINK_FATAL);
    CHECK(!link_result_is_error(bp));   /* 背压不是错误 */
    CHECK(link_result_is_error(ft));

    link_destroy(l);
}

/* 3) 【P2】MTU 在【发出前】生效：超长帧不得调用底层 publish */
static void test_mtu_is_checked_before_send(void)
{
    link_t *l = make_link();
    static uint8_t big[LINK_MQTT_MTU_BYTES + 1];

    int before = s_publish_calls;
    CHECK(link_send(l, big, sizeof(big)) == LINK_PAYLOAD_TOO_BIG);
    CHECK(s_publish_calls == before);      /* ← 底层【没有】被调用 */

    /* 恰好等于 MTU：允许 */
    s_publish_result = MQTT_PUBLISH_OK;
    CHECK(link_send(l, big, LINK_MQTT_MTU_BYTES) == LINK_SENT);
    CHECK(s_publish_calls == before + 1);

    link_destroy(l);
}

/* 4) 未就绪由【驱动结果】表达（不再由 link 层预检 —— 那是 TOCTOU）。
 *    驱动在未连接时自己返回 NOT_CONNECTED ⇒ 映射成 NOT_READY。 */
static void test_not_ready_comes_from_driver(void)
{
    link_t *l = make_link();

    s_connected = false;
    s_publish_result = MQTT_PUBLISH_NOT_CONNECTED;   /* 驱动自己说没连上 */
    int before = s_publish_calls;
    CHECK(link_send(l, (const uint8_t *)"x", 1) == LINK_NOT_READY);
    CHECK(s_publish_calls == before + 1);            /* ← 驱动被调用了 */

    s_connected = true;
    s_publish_result = MQTT_PUBLISH_OK;
    CHECK(link_send(l, (const uint8_t *)"x", 1) == LINK_SENT);
    link_destroy(l);
}

/* 5) 统计：每条路径都计数（P3） */
static void test_stats_count_each_path(void)
{
    link_t *l = make_link();
    uint8_t frame[4] = {0};
    s_connected = true;

    s_publish_result = MQTT_PUBLISH_OK;
    link_send(l, frame, 4);
    s_publish_result = MQTT_PUBLISH_BACKPRESSURE;
    link_send(l, frame, 4);
    s_publish_result = MQTT_PUBLISH_FAILED;
    link_send(l, frame, 4);

    link_stats_t st;
    link_get_stats(l, &st);
    CHECK(st.tx_sent == 1);
    CHECK(st.tx_backpressure == 1);
    CHECK(st.tx_fatal == 1);
    link_destroy(l);
}

/* 6) MTU 值本身：超长会被 link_send 拦下，故驱动报出的上限必须与 R1 实测一致 */
static void test_mtu_value(void)
{
    CHECK(link_mqtt_driver()->mtu(NULL) == LINK_MQTT_MTU_BYTES);
    CHECK(LINK_MQTT_MTU_BYTES == 2011u);
    CHECK(strcmp(link_mqtt_driver()->name, "mqtt") == 0);
}

int main(void)
{
    test_all_four_states_survive();
    test_backpressure_is_distinct_from_failure();
    test_mtu_is_checked_before_send();
    test_not_ready_comes_from_driver();
    test_stats_count_each_path();
    test_mtu_value();
    if (s_failures) { printf("link_mqtt_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("link_mqtt_tests: all checks passed\n");
    return 0;
}
