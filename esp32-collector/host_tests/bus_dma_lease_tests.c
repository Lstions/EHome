/*
 * Bus DMA preferred_controller conflict-check tests.
 *
 * bus_dma.c's init functions depend on 20+ ESP-IDF driver API calls
 * (uart_driver_install, spi_bus_initialize, i2c_new_master_bus, etc.)
 * that cannot be easily mocked.  Instead, this test covers the
 * lease-selection logic that bus_dma.c delegates to:
 *
 *   - bus_lease_select_controller() — the inline policy in bus_lease_policy.h
 *   - resource_index_for_controller() — bus_manager.c static helper
 *   - runtime_bus_config_matches() — bus_manager.c static helper
 *   - controller_in_use() — bus_manager.c static helper
 *
 * These are the decision functions that determine whether a preferred
 * controller is honored or rejected.  The actual driver-level conflict
 * (uart_driver_install returning ESP_ERR_INVALID_STATE) is covered by
 * the manifest preflight in bus_manager_manifest_tests.c.
 *
 * Coverage:
 *   - resource_index_for_controller: valid and invalid controller IDs
 *   - controller_in_use: detects active controller by bus_type
 *   - runtime_bus_config_matches: UART/SPI/I2C config comparison
 */

#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <stdbool.h>
#include <stdint.h>

/* Host stubs */
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/queue.h"
#include "esp_err.h"
#include "esp_log.h"
#include "driver/uart.h"
#include "driver/spi_master.h"
#include "driver/i2c_master.h"

#include "config_mgr.h"
#include "dma_pool.h"
#include "hw_tables.h"
#include "hw_profile.h"
#include "bus_dma.h"
#include "bus_worker.h"

/* Stubs for external symbols */
void host_test_log_record(char level, const char *tag, const char *format, ...) {
    (void)level;(void)tag;(void)format;
}
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_OK"; }
void esp_restart(void) {}
uint32_t esp_get_free_heap_size(void) { return 200000; }
uint32_t esp_get_minimum_free_heap_size(void) { return 150000; }

/* bus_dma stubs */
bool bus_dma_uart0_is_available(void) { return true; }
esp_err_t bus_dma_init_preferred(bus_dma_ctx_t *c, uint8_t t, bool d,
                                 const uint8_t *cfg, size_t l, int32_t cid) {
    (void)c;(void)t;(void)d;(void)cfg;(void)l;(void)cid; return ESP_OK;
}
esp_err_t bus_dma_init(bus_dma_ctx_t *c, uint8_t t, bool d,
                       const uint8_t *cfg, size_t l) { return bus_dma_init_preferred(c,t,d,cfg,l,-1); }
esp_err_t bus_dma_deinit(bus_dma_ctx_t *c) { (void)c; return ESP_OK; }
esp_err_t bus_dma_write(bus_dma_ctx_t *c, const uint8_t *d, size_t l) { (void)c;(void)d;(void)l; return ESP_OK; }
size_t bus_dma_read(bus_dma_ctx_t *c, uint8_t *b, size_t s) { (void)c;(void)b;(void)s; return 0; }
esp_err_t bus_dma_transact(bus_dma_ctx_t *c, const uint8_t *t, size_t tl,
                           uint8_t *r, size_t rs, size_t *rl) {
    (void)c;(void)t;(void)tl;(void)r;(void)rs; if(rl)*rl=0; return ESP_OK;
}
QueueHandle_t bus_dma_uart_event_queue(const bus_dma_ctx_t *c) { (void)c; return NULL; }

/* dma_pool stubs */
esp_err_t dma_pool_allocate(dma_pool_t *p, uint8_t bt, const char *hw, uint32_t *id) {
    (void)p;(void)bt;(void)hw; if(id)*id=1; return ESP_OK;
}
esp_err_t dma_pool_release_by_hw(dma_pool_t *p, const char *hw) { (void)p;(void)hw; return ESP_OK; }
void dma_pool_init(dma_pool_t *p, const hw_dma_t *t, int c) { (void)p;(void)t;(void)c; }
size_t dma_pool_serialize(dma_pool_t *p, uint8_t *b, size_t s) { (void)p;(void)b;(void)s; return 0; }

/* hw_profile stubs */
void bus_worker_set_callbacks(write_rsp_cb_t wr, data_rpt_cb_t dr) { (void)wr;(void)dr; }
void bus_worker_set_channel_cmd_v2_final_cb(channel_cmd_v2_final_cb_t cb) { (void)cb; }
void bus_worker_start(bus_runtime_t *rt) { (void)rt; }
bool bus_worker_suspend(void) { return true; }
void bus_worker_resume(void) {}
void bus_worker_stop(void) {}
void bus_worker_discard_queued(bus_runtime_t *rt) { (void)rt; }
uint32_t bus_worker_get_rx_timeout_count(int ch) { (void)ch; return 0; }
uint32_t bus_worker_get_report_drop_count(void) { return 0; }
uint32_t bus_worker_get_report_queue_high_water(void) { return 0; }
uint32_t bus_worker_get_min_stack_watermark(void) { return 0; }
void msg_handler_send_write_rsp(uint32_t r, bool s, uint32_t e, const char *m) {
    (void)r;(void)s;(void)e;(void)m;
}
void msg_handler_send_data_report(uint32_t ch, uint64_t ts, uint32_t sq,
    const uint8_t *d, size_t l, uint32_t ec, uint32_t r, uint32_t ed,
    uint32_t t, uint8_t ci) {
    (void)ch;(void)ts;(void)sq;(void)d;(void)l;(void)ec;(void)r;(void)ed;(void)t;(void)ci;
}

/* config_mgr stub */
const config_manifest_t *config_mgr_get_manifest(void) { return NULL; }

/* Include bus_manager.c to access static helpers */
#define TAG "BUS_MGR"
#include "../components/bus_manager/bus_manager.c"

/* Test framework */
static int failures = 0;
#define CHECK(cond, msg) do { \
    if (!(cond)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (msg)); \
        failures++; \
        return; \
    } \
} while (0)

/* Test runtime */
static uint32_t s_bus_ch[SCHED_MAX_CHANNELS];
static bus_dma_ctx_t s_bus_ctx[SCHED_MAX_CHANNELS];
static char s_hw_id[SCHED_MAX_CHANNELS * 16];
static QueueHandle_t s_pending[SCHED_MAX_CHANNELS];

static void init_test_runtime(bus_runtime_t *rt)
{
    memset(rt, 0, sizeof(*rt));
    memset(s_bus_ctx, 0, sizeof(s_bus_ctx));
    memset(s_bus_ch, 0, sizeof(s_bus_ch));
    rt->bus_ctx = s_bus_ctx;
    rt->bus_ch = s_bus_ch;
    rt->bus_hw_id = s_hw_id;
    rt->pending_queues = s_pending;
    rt->dma_pool = NULL;
    rt->lease_hints_valid = false;
}

/* ---- Test cases ---- */

static void test_resource_index_for_uart(void)
{
    /* C6: UART0 port=0, UART1 port=1 */
    int idx0 = resource_index_for_controller(BUS_TYPE_UART, 0);
    CHECK(idx0 == 0, "UART0 should be index 0");

    int idx1 = resource_index_for_controller(BUS_TYPE_UART, 1);
    CHECK(idx1 == 1, "UART1 should be index 1");

    int idx_bad = resource_index_for_controller(BUS_TYPE_UART, 99);
    CHECK(idx_bad == -1, "invalid UART controller should return -1");

    int idx_neg = resource_index_for_controller(BUS_TYPE_UART, -1);
    CHECK(idx_neg == -1, "negative controller should return -1");
}

static void test_resource_index_for_spi(void)
{
    /* C6: SPI2 port=2 */
    int idx = resource_index_for_controller(BUS_TYPE_SPI, 2);
    CHECK(idx == 0, "SPI2 should be index 0");

    idx = resource_index_for_controller(BUS_TYPE_SPI, 3);
    CHECK(idx == -1, "SPI3 doesn't exist on C6, should return -1");
}

static void test_resource_index_for_i2c(void)
{
    /* C6: I2C0 port=0 */
    int idx = resource_index_for_controller(BUS_TYPE_I2C, 0);
    CHECK(idx == 0, "I2C0 should be index 0");

    idx = resource_index_for_controller(BUS_TYPE_I2C, 1);
    CHECK(idx == -1, "I2C1 doesn't exist on C6, should return -1");
}

static void test_controller_in_use_detects_uart(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);

    /* No contexts initialized → nothing in use */
    CHECK(!controller_in_use(&rt, BUS_TYPE_UART, 0), "no UART should be in use initially");

    /* Initialize UART0 on ctx[0] */
    s_bus_ctx[0].initialized = true;
    s_bus_ctx[0].bus_type = BUS_TYPE_UART;
    s_bus_ctx[0].cfg.uart.port = UART_NUM_0;

    CHECK(controller_in_use(&rt, BUS_TYPE_UART, 0), "UART0 should be in use");
    CHECK(!controller_in_use(&rt, BUS_TYPE_UART, 1), "UART1 should not be in use");
    CHECK(!controller_in_use(&rt, BUS_TYPE_SPI, 2), "SPI should not be in use");
}

static void test_controller_in_use_detects_spi(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);

    s_bus_ctx[0].initialized = true;
    s_bus_ctx[0].bus_type = BUS_TYPE_SPI;
    s_bus_ctx[0].cfg.spi.host = SPI2_HOST;

    CHECK(controller_in_use(&rt, BUS_TYPE_SPI, SPI2_HOST), "SPI2 should be in use");
    CHECK(!controller_in_use(&rt, BUS_TYPE_UART, 0), "UART should not be in use");
}

static void test_runtime_bus_config_matches_uart(void)
{
    bus_dma_ctx_t ctx;
    memset(&ctx, 0, sizeof(ctx));
    ctx.initialized = true;
    ctx.bus_type = BUS_TYPE_UART;
    ctx.cfg.uart.tx_pin = 16;
    ctx.cfg.uart.rx_pin = 17;
    ctx.cfg.uart.baud = 9600;

    /* Matching config */
    uint8_t cfg[] = {16, 17, 0, 0, 0x25, 0x80}; /* tx=16, rx=17, baud=9600 BE */
    CHECK(runtime_bus_config_matches(&ctx, BUS_TYPE_UART, cfg, sizeof(cfg)),
          "matching UART config should return true");

    /* Non-matching TX pin */
    uint8_t cfg2[] = {18, 17, 0, 0, 0x25, 0x80};
    CHECK(!runtime_bus_config_matches(&ctx, BUS_TYPE_UART, cfg2, sizeof(cfg2)),
          "non-matching TX pin should return false");

    /* Wrong bus type */
    CHECK(!runtime_bus_config_matches(&ctx, BUS_TYPE_SPI, cfg, sizeof(cfg)),
          "wrong bus type should return false");

    /* NULL ctx */
    CHECK(!runtime_bus_config_matches(NULL, BUS_TYPE_UART, cfg, sizeof(cfg)),
          "NULL ctx should return false");
}

static void test_runtime_bus_config_matches_i2c(void)
{
    bus_dma_ctx_t ctx;
    memset(&ctx, 0, sizeof(ctx));
    ctx.initialized = true;
    ctx.bus_type = BUS_TYPE_I2C;
    ctx.cfg.i2c.sda_pin = 21;
    ctx.cfg.i2c.scl_pin = 22;

    uint8_t cfg[] = {21, 22, 0x48, 0, 0, 0x01, 0x86}; /* sda=21, scl=22, addr=0x48, freq=100k */
    CHECK(runtime_bus_config_matches(&ctx, BUS_TYPE_I2C, cfg, sizeof(cfg)),
          "matching I2C config should return true");

    uint8_t cfg2[] = {22, 21, 0x48, 0, 0, 0x01, 0x86}; /* swapped SDA/SCL */
    CHECK(!runtime_bus_config_matches(&ctx, BUS_TYPE_I2C, cfg2, sizeof(cfg2)),
          "swapped SDA/SCL should return false");
}

static void test_runtime_controller_id_extracts_port(void)
{
    bus_dma_ctx_t ctx;
    memset(&ctx, 0, sizeof(ctx));
    ctx.initialized = true;
    ctx.bus_type = BUS_TYPE_UART;
    ctx.cfg.uart.port = UART_NUM_1;

    CHECK(runtime_controller_id(&ctx) == 1, "UART1 controller_id should be 1");

    ctx.bus_type = BUS_TYPE_SPI;
    ctx.cfg.spi.host = SPI2_HOST;
    CHECK(runtime_controller_id(&ctx) == 2, "SPI2 controller_id should be 2");

    ctx.bus_type = BUS_TYPE_I2C;
    ctx.cfg.i2c.port = I2C_NUM_0;
    CHECK(runtime_controller_id(&ctx) == 0, "I2C0 controller_id should be 0");

    ctx.initialized = false;
    CHECK(runtime_controller_id(&ctx) == -1, "uninitialized ctx should return -1");
}

static void test_lease_select_with_valid_controller(void)
{
    /* When compatible=true and current_controller is valid (e.g., UART1 port=1),
     * keep the current lease */
    int32_t result = bus_lease_select_controller(1, true, 0);
    CHECK(result == 1, "compatible lease should keep controller 1");

    /* UART0 is a valid lease (port=0 is valid) */
    result = bus_lease_select_controller(0, true, 1);
    CHECK(result == 0, "UART0 (controller 0) is valid and should be kept");
}

static void test_lease_select_incompatible_falls_back(void)
{
    /* When compatible=false, use the fallback */
    int32_t result = bus_lease_select_controller(1, false, 0);
    CHECK(result == 0, "incompatible config should use fallback controller 0");

    /* When current is -1 (no lease), use fallback even if compatible=true */
    result = bus_lease_select_controller(-1, true, 1);
    CHECK(result == 1, "missing lease should use fallback");
}

int main(void)
{
    test_resource_index_for_uart();
    test_resource_index_for_spi();
    test_resource_index_for_i2c();
    test_controller_in_use_detects_uart();
    test_controller_in_use_detects_spi();
    test_runtime_bus_config_matches_uart();
    test_runtime_bus_config_matches_i2c();
    test_runtime_controller_id_extracts_port();
    test_lease_select_with_valid_controller();
    test_lease_select_incompatible_falls_back();

    if (failures != 0) {
        fprintf(stderr, "%d test(s) failed\n", failures);
        return 1;
    }
    puts("bus_dma_lease_tests: all tests passed");
    return 0;
}
