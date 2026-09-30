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

esp_err_t esp_read_mac(uint8_t *mac, esp_mac_type_t type) {
    (void)type;
    mac[0] = 0xAA; mac[1] = 0xBB; mac[2] = 0xCC;
    mac[3] = 0xDD; mac[4] = 0xEE; mac[5] = 0xFF;
    return ESP_OK;
}
esp_err_t esp_efuse_mac_get_default(uint8_t *mac) {
    mac[0] = 0xAA; mac[1] = 0xBB; mac[2] = 0xCC;
    mac[3] = 0xDD; mac[4] = 0xEE; mac[5] = 0xFF;
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
int main(void)
{
    test_init_bus_runtime_field_mapping();
    test_control_sample_queue_separation();
    test_app_state_init_creates_queues();
    test_uptime_sec_now_follows_monotonic_clock();

    if (g_failures > 0) {
        fprintf(stderr, "\napp_state_tests: %d FAILURES\n", g_failures);
        return 1;
    }
    puts("app_state_tests: all tests passed");
    return 0;
}
