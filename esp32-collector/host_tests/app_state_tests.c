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
 * Main
 * ===================================================================== */
int main(void)
{
    test_init_bus_runtime_field_mapping();
    test_control_sample_queue_separation();
    test_app_state_init_creates_queues();

    if (g_failures > 0) {
        fprintf(stderr, "\napp_state_tests: %d FAILURES\n", g_failures);
        return 1;
    }
    puts("app_state_tests: all tests passed");
    return 0;
}
