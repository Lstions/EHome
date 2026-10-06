/*
 * app_state_tests.c
 *
 * Host tests for app_state.c — multi-bus runtime adaptation.
 * Covers the P2-8 bus_runtime_t field mapping (app_state_init_bus_runtime)
 * and queue creation in app_state_init.
 *
 * Coverage:
 *   1. app_state_init_bus_runtime — all fields correctly mapped
 *   2. app_state_init — queue creation (sample + control queues)
 *   3. Control/sample queue separation — distinct handles
 */

#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <stdint.h>

/* ---- Stub headers ---- */
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include "esp_err.h"
#include "esp_log.h"
#include "esp_mac.h"
#include "esp_random.h"
#include "driver/uart.h"
#include "driver/spi_master.h"
#include "driver/i2c_master.h"

/* ---- Component headers ---- */
#include "bus_dma.h"
#include "bus_worker.h"
#include "cmd_queue.h"
#include "scheduler.h"
#include "config_mgr.h"
#include "dma_pool.h"
#include "hw_tables.h"

/* ---- app_state.h (includes transport.h) ---- */
#include "app_state.h"

/* =====================================================================
 * Test infrastructure
 * ===================================================================== */
static int g_failures = 0;

#define CHECK(cond, msg) do { \
    if (!(cond)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (msg)); \
        g_failures++; \
    } \
} while (0)

/* =====================================================================
 * ESP stubs
 * ===================================================================== */
void host_test_log_record(char level, const char *tag, const char *format, ...) {
    (void)level; (void)tag; (void)format;
}
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_ERR"; }

/* ---------------------------------------------------------------------
 * MAC 桩：必须**如实模仿真芯片语义**，否则本组用例无法复现真实缺陷。
 *
 * 真实 ESP32-C6（SOC_IEEE802154_SUPPORTED=1）上（esp-idf mac_addr.c）：
 *   esp_read_mac(ESP_MAC_WIFI_STA) → 直接返回 6 字节接口 MAC，不插 MAC_EXT；
 *   esp_efuse_mac_get_default()    → 先取 48 位 MAC_FACTORY，再执行
 *                                    insert_mac_ext_into_mac()：
 *                                    mac[3..4] = MAC_EXT(0xFFFE)，
 *                                    真 MAC 的 mac[3..5] 被挪到 mac[5..7]。
 * 只取前 6 字节时，区分两块芯片的字节（mac[3..4]）正好被丢掉。
 *
 * 2026-10-06 生产事故：ttyACM0/ttyACM2 两块不同 C6（EFUSE 分别
 * bd02f35c…fffffef0f5 / bd02dd84…fffffef0f5）都上报 node_id
 * F0F5BDFFFE02，互相顶掉 MQTT 连接（~5s 一次），后端看到同一节点反复上下线。
 * 本桩令该差异在宿主机上可复现（g_test_efuse_mac_factory 为可注入的 48 位 MAC）。
 * ------------------------------------------------------------------- */
uint8_t g_test_efuse_mac_factory[6] = {0xF0, 0xF5, 0xBD, 0x02, 0xDD, 0x84};
uint8_t g_test_mac_ext[2] = {0xFF, 0xFE};
bool g_test_read_mac_fail = false;

esp_err_t esp_read_mac(uint8_t *mac, esp_mac_type_t type) {
    (void)type;
    if (g_test_read_mac_fail) return ESP_FAIL;   /* 兜底路径用例注入 */
    /* 6 字节接口 MAC == MAC_FACTORY，无 MAC_EXT 插入 */
    memcpy(mac, g_test_efuse_mac_factory, 6);
    return ESP_OK;
}

esp_err_t esp_efuse_mac_get_default(uint8_t *mac) {
    /* 复刻 IDF 的 8 字节布局 + insert_mac_ext_into_mac() */
    uint8_t buf[8];
    memcpy(buf, g_test_efuse_mac_factory, 6);
    buf[6] = 0; buf[7] = 0;
    uint8_t mac_tmp[3];
    memcpy(mac_tmp, &buf[3], 3);
    memcpy(&buf[3], g_test_mac_ext, 2);
    memcpy(&buf[5], mac_tmp, 3);
    memcpy(mac, buf, 6);
    return ESP_OK;
}
uint32_t esp_random(void) { return 0x12345678; }

/* esp_timer 桩（stubs/esp_timer.h）背后的可控时间源。
 * app_state_uptime_sec_now() 读它，用例自己设置期望值。 */
int64_t g_test_time_us = 0;

#ifndef CONFIG_COLLECTOR_NODE_ID
#define CONFIG_COLLECTOR_NODE_ID "test-node"
#endif

/* =====================================================================
 * bus_manager stubs
 * ===================================================================== */
bus_dma_ctx_t *bus_manager_find_ctx(bus_runtime_t *rt, uint32_t channel_id) {
    (void)rt; (void)channel_id; return NULL;
}

/* =====================================================================
 * Semaphore stubs (needed by dma_pool.c)
 * ===================================================================== */
SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (SemaphoreHandle_t)1; }
int xSemaphoreTake(SemaphoreHandle_t sem, uint32_t ticks) { (void)sem; (void)ticks; return 1; }
int xSemaphoreGive(SemaphoreHandle_t sem) { (void)sem; return 1; }
void vSemaphoreDelete(SemaphoreHandle_t sem) { (void)sem; }

/* =====================================================================
 * hw_profile stubs
 * ===================================================================== */
void hw_profile_set_boot_id(const char *id) { (void)id; }

/* =====================================================================
 * Include app_state.c directly
 * ===================================================================== */
#include "../main/app_state.c"

/* =====================================================================
 * Test 1: app_state_init_bus_runtime maps all fields correctly
 * ===================================================================== */
static void test_init_bus_runtime_field_mapping(void) {
    app_state_t s;
    memset(&s, 0, sizeof(s));

    dma_pool_t fake_pool;
    memset(&fake_pool, 0, sizeof(fake_pool));

    /* For queue fields, use distinct sentinel addresses */
    QueueHandle_t q_uart0  = (QueueHandle_t)0x1001;
    QueueHandle_t q_uart1  = (QueueHandle_t)0x1002;
    QueueHandle_t q_uart2  = (QueueHandle_t)0x1003;
    QueueHandle_t q_spi    = (QueueHandle_t)0x1004;
    QueueHandle_t q_i2c    = (QueueHandle_t)0x1005;
    QueueHandle_t qc_uart0 = (QueueHandle_t)0x2001;
    QueueHandle_t qc_uart1 = (QueueHandle_t)0x2002;
    QueueHandle_t qc_uart2 = (QueueHandle_t)0x2003;
    QueueHandle_t qc_spi   = (QueueHandle_t)0x2004;
    QueueHandle_t qc_i2c   = (QueueHandle_t)0x2005;

    s.uart0_cmd_queue = q_uart0;
    s.uart1_cmd_queue = q_uart1;
    s.uart2_cmd_queue = q_uart2;
    s.spi_cmd_queue   = q_spi;
    s.i2c_cmd_queue   = q_i2c;
    s.uart0_control_queue = qc_uart0;
    s.uart1_control_queue = qc_uart1;
    s.uart2_control_queue = qc_uart2;
    s.spi_control_queue   = qc_spi;
    s.i2c_control_queue   = qc_i2c;
    s.dma_pool = &fake_pool;

    bus_runtime_t rt;
    memset(&rt, 0, sizeof(rt));
    app_state_init_bus_runtime(&s, &rt);

    /* Verify all sample queue mappings */
    CHECK(rt.uart0_cmd_queue == q_uart0, "uart0_cmd_queue should map");
    CHECK(rt.uart1_cmd_queue == q_uart1, "uart1_cmd_queue should map");
    CHECK(rt.uart2_cmd_queue == q_uart2, "uart2_cmd_queue should map");
    CHECK(rt.spi_cmd_queue   == q_spi,   "spi_cmd_queue should map");
    CHECK(rt.i2c_cmd_queue   == q_i2c,   "i2c_cmd_queue should map");

    /* Verify all control queue mappings */
    CHECK(rt.uart0_control_queue == qc_uart0, "uart0_control_queue should map");
    CHECK(rt.uart1_control_queue == qc_uart1, "uart1_control_queue should map");
    CHECK(rt.uart2_control_queue == qc_uart2, "uart2_control_queue should map");
    CHECK(rt.spi_control_queue   == qc_spi,   "spi_control_queue should map");
    CHECK(rt.i2c_control_queue   == qc_i2c,   "i2c_control_queue should map");

    /* Verify other fields */
    CHECK(rt.bus_ctx == s.bus_ctx, "bus_ctx should map");
    CHECK(rt.bus_ch == s.bus_ch, "bus_ch should map");
    CHECK(rt.dma_pool == &fake_pool, "dma_pool should map");
    CHECK(rt.find_ctx == bus_manager_find_ctx, "find_ctx should point to bus_manager_find_ctx");
}

/* =====================================================================
 * Test 2: control queues are distinct from sample queues
 * ===================================================================== */
static void test_control_sample_queue_separation(void) {
    app_state_t s;
    memset(&s, 0, sizeof(s));

    s.uart0_cmd_queue     = (QueueHandle_t)0x1001;
    s.uart0_control_queue = (QueueHandle_t)0x2001;
    s.uart1_cmd_queue     = (QueueHandle_t)0x1002;
    s.uart1_control_queue = (QueueHandle_t)0x2002;

    bus_runtime_t rt;
    memset(&rt, 0, sizeof(rt));
    app_state_init_bus_runtime(&s, &rt);

    CHECK(rt.uart0_cmd_queue != rt.uart0_control_queue,
          "uart0 sample and control queues must be distinct");
    CHECK(rt.uart1_cmd_queue != rt.uart1_control_queue,
          "uart1 sample and control queues must be distinct");
}

/* =====================================================================
 * Test 3: app_state_init creates all queues
 * ===================================================================== */
static void test_app_state_init_creates_queues(void) {
    app_state_t *s = app_state_init();
    CHECK(s != NULL, "app_state_init should return non-NULL");

    /* Sample queues */
    CHECK(s->uart0_cmd_queue != NULL, "uart0_cmd_queue should be created");
    CHECK(s->uart1_cmd_queue != NULL, "uart1_cmd_queue should be created");
    CHECK(s->uart2_cmd_queue != NULL, "uart2_cmd_queue should be created");
    CHECK(s->spi_cmd_queue   != NULL, "spi_cmd_queue should be created");
    CHECK(s->i2c_cmd_queue   != NULL, "i2c_cmd_queue should be created");

    /* Control queues */
    CHECK(s->uart0_control_queue != NULL, "uart0_control_queue should be created");
    CHECK(s->uart1_control_queue != NULL, "uart1_control_queue should be created");
    CHECK(s->uart2_control_queue != NULL, "uart2_control_queue should be created");
    CHECK(s->spi_control_queue   != NULL, "spi_control_queue should be created");
    CHECK(s->i2c_control_queue   != NULL, "i2c_control_queue should be created");

    /* All 10 queues should be distinct handles */
    QueueHandle_t all[10] = {
        s->uart0_cmd_queue, s->uart1_cmd_queue, s->uart2_cmd_queue,
        s->spi_cmd_queue, s->i2c_cmd_queue,
        s->uart0_control_queue, s->uart1_control_queue, s->uart2_control_queue,
        s->spi_control_queue, s->i2c_control_queue,
    };
    for (int i = 0; i < 10; i++) {
        for (int j = i + 1; j < 10; j++) {
            if (all[i] == all[j]) {
                CHECK(0, "queue handles must all be distinct");
                return;
            }
        }
    }
    CHECK(1, "all 10 queue handles are distinct");

    /* bus_runtime should be initialized */
    CHECK(s->bus_runtime.uart0_cmd_queue == s->uart0_cmd_queue,
          "bus_runtime should reference app_state queues");
    CHECK(s->bus_runtime.find_ctx == bus_manager_find_ctx,
          "bus_runtime.find_ctx should be set");

    /* node_id should be generated */
    CHECK(strlen(s->node_id) > 0, "node_id should be generated");
}

/* =====================================================================
 * Test 4: app_state_uptime_sec_now — 运行时长必须来自真实单调时钟
 *
 * 回归背景（2026-09-29 实机定位）：status_task 曾在 5 秒上报循环里做
 * `s->uptime_sec++`，使 StatusReport field 1（单位=秒）每 5 秒真实时间
 * 才 +1 —— 上报值 = 真实运行秒数 / 5，前端「固件在线时长」少 5 倍。
 * 实测铁证：相邻两次重启间的墙钟 15332s vs 上报 3065（×5 = 15325s）。
 *
 * 本用例锁定：uptime 只由单调时钟决定，与调用次数无关。
 * ===================================================================== */
static void test_uptime_sec_now_follows_monotonic_clock(void) {
    /* 0 → 0 秒 */
    g_test_time_us = 0;
    CHECK(app_state_uptime_sec_now() == 0, "t=0us 应为 0 秒");

    /* 整秒边界：截断（向下取整），不是四舍五入 */
    g_test_time_us = 1000000;              /* 1.000000 s */
    CHECK(app_state_uptime_sec_now() == 1, "t=1s 应为 1 秒");
    g_test_time_us = 1999999;              /* 1.999999 s */
    CHECK(app_state_uptime_sec_now() == 1, "t=1.999999s 应截断为 1 秒");

    /* 关键回归点：真实经过 5 秒，值必须 +5（旧实现只 +1） */
    g_test_time_us = 1000000;
    uint32_t a = app_state_uptime_sec_now();
    g_test_time_us = 6000000;              /* +5 秒真实时间 */
    uint32_t b = app_state_uptime_sec_now();
    CHECK(b - a == 5, "真实经过 5 秒，uptime 必须 +5（旧 bug 只 +1）");

    /* 关键回归点：与调用次数无关 —— 同一时刻连读 3 次值不变。
     * 旧实现每次 status_task 循环自增，值会随"调用次数"漂移。 */
    g_test_time_us = 100000000;            /* 100 s */
    uint32_t v1 = app_state_uptime_sec_now();
    uint32_t v2 = app_state_uptime_sec_now();
    uint32_t v3 = app_state_uptime_sec_now();
    CHECK(v1 == 100 && v2 == 100 && v3 == 100,
          "同一时刻重复读取必须恒为 100（不得随调用次数自增）");

    /* 分钟级运行：1800 s 必须原样读出（旧实现按 5s 周期缩水成 360） */
    g_test_time_us = 1800000000LL;         /* 1800 s = 30 min */
    CHECK(app_state_uptime_sec_now() == 1800, "t=1800s 应为 1800 秒");

    g_test_time_us = 0;
}

/* =====================================================================
 * Main
 * ===================================================================== */
/* =====================================================================
 * Test: node_id 必须逐芯片唯一（2026-10-06 生产事故回归）
 *
 * 事故：两块不同的 ESP32-C6（EFUSE 低 3 字节分别为 02:dd:84 / 02:f3:5c）
 * 都上报 F0F5BDFFFE02 → 同一个 node_id 变成同一个 MQTT client_id 与主题
 * 前缀 → 互踢连接（实测周期 5.1s）、后端同一节点反复上下线。
 *
 * 根因：generate_node_id 用了 esp_efuse_mac_get_default()，该 API 在有
 * IEEE 802.15.4 的芯片上会把 mac[3..4] 覆盖成 MAC_EXT(0xFFFE)，真实 MAC
 * 被挪到 mac[5..7]，而函数只格式化前 6 字节 ⇒ 两块芯片算出同一个值。
 *
 * 本用例用两块真实芯片的 EFUSE 值断言派生结果不同，并锁定修复所用的 API。
 * ===================================================================== */
static const uint8_t kChipA[6] = {0xF0, 0xF5, 0xBD, 0x02, 0xDD, 0x84}; /* ttyACM2 */
static const uint8_t kChipB[6] = {0xF0, 0xF5, 0xBD, 0x02, 0xF3, 0x5C}; /* ttyACM0 */

static void test_node_id_is_unique_per_chip(void) {
    uint8_t macA[6], macB[6];

    /* 真实芯片语义下，旧 API 对两块不同芯片返回**相同**前 6 字节 */
    memcpy(g_test_efuse_mac_factory, kChipA, 6);
    esp_efuse_mac_get_default(macA);
    memcpy(g_test_efuse_mac_factory, kChipB, 6);
    esp_efuse_mac_get_default(macB);
    CHECK(memcmp(macA, macB, 6) == 0,
          "复现前提：esp_efuse_mac_get_default 前 6 字节对两块芯片相同（正是缺陷根因）");

    /* 修复所用 API 必须给出不同 MAC —— 这是本用例真正锁定的一点 */
    memcpy(g_test_efuse_mac_factory, kChipA, 6);
    esp_read_mac(macA, ESP_MAC_WIFI_STA);
    memcpy(g_test_efuse_mac_factory, kChipB, 6);
    esp_read_mac(macB, ESP_MAC_WIFI_STA);

    CHECK(memcmp(macA, macB, 6) != 0, "esp_read_mac(WIFI_STA) 必须给出逐芯片唯一的 MAC");
    CHECK(memcmp(macA, kChipA, 6) == 0, "esp_read_mac(WIFI_STA) 必须返回真实接口 MAC（无 MAC_EXT 插入）");
    CHECK(memcmp(macB, kChipB, 6) == 0, "esp_read_mac(WIFI_STA) 必须返回真实接口 MAC（无 MAC_EXT 插入）");

    /* 端到端：app_state_init 之后两块芯片的 node_id 必须不同，且等于接口 MAC 的十六进制 */
    memcpy(g_test_efuse_mac_factory, kChipA, 6);
    app_state_t *sa = app_state_init();
    char idA[24];
    strlcpy(idA, sa->node_id, sizeof(idA));

    memcpy(g_test_efuse_mac_factory, kChipB, 6);
    app_state_t *sb = app_state_init();
    char idB[24];
    strlcpy(idB, sb->node_id, sizeof(idB));

    CHECK(strcmp(idA, idB) != 0,
          "两块不同 C6 的 node_id 必须不同（事故中二者都是 F0F5BDFFFE02）");
    CHECK(strcmp(idA, "F0F5BD02DD84") == 0, "芯片 A 的 node_id 应为 F0F5BD02DD84");
    CHECK(strcmp(idB, "F0F5BD02F35C") == 0, "芯片 B 的 node_id 应为 F0F5BD02F35C");
    CHECK(strcmp(idA, "F0F5BDFFFE02") != 0, "node_id 不得再退化为 MAC_EXT 形态（F0F5BDFFFE02）");
    CHECK(strlen(idA) == 12 && strlen(idB) == 12, "node_id 仍须是 12 位十六进制");
}

/* =====================================================================
 * Test: MAC 读取失败时回退到 Kconfig（修复不得破坏兜底路径）
 * ===================================================================== */
static void test_node_id_falls_back_to_kconfig(void) {
    g_test_read_mac_fail = true;
    app_state_t *s = app_state_init();
    g_test_read_mac_fail = false;
    CHECK(strcmp(s->node_id, "test-node") == 0,
          "MAC 读取失败时应回退到 CONFIG_COLLECTOR_NODE_ID");
}

int main(void)
{
    test_init_bus_runtime_field_mapping();
    test_control_sample_queue_separation();
    test_app_state_init_creates_queues();
    test_uptime_sec_now_follows_monotonic_clock();
    test_node_id_is_unique_per_chip();
    test_node_id_falls_back_to_kconfig();

    if (g_failures > 0) {
        fprintf(stderr, "\napp_state_tests: %d FAILURES\n", g_failures);
        return 1;
    }
    puts("app_state_tests: all tests passed");
    return 0;
}
