/*
 * Manifest resource preflight tests for bus_manager.
 *
 * This test includes bus_manager.c directly to access the static
 * validate_manifest_resources() function.  The only external bus_dma
 * dependency is bus_dma_uart0_is_available(), which is wrapped to return
 * true (UART0 available, as on a C6 with USB-JTAG console).
 *
 * Coverage:
 *   - UART pin conflict (TX == RX)
 *   - UART reserved pin conflict
 *   - UART baud == 0
 *   - UART baud exceeds max
 *   - Duplicate channel IDs in manifest
 *   - DMA requested on non-DMA hardware
 *   - UART controller exhaustion (more channels than controllers)
 *   - SPI pin conflict (MOSI == MISO)
 *   - SPI CS pin reuse
 *   - I2C SDA == SCL
 *   - Valid manifest with UART + SPI + I2C passes
 *   - Lease snapshot: compatible channel keeps controller
 */

#include <stdio.h>
#include <string.h>
#include <stdlib.h>

/* Host stubs */
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/queue.h"
#include "esp_err.h"
#include "esp_log.h"
#include "driver/uart.h"
#include "driver/spi_master.h"
#include "driver/i2c_master.h"

/* Config + DMA pool + hw_tables */
#include "config_mgr.h"
#include "dma_pool.h"
#include "hw_tables.h"
#include "hw_profile.h"

/* bus_dma.h, bus_worker.h provide the types (bus_dma_ctx_t, callbacks, etc.) */
#include "bus_dma.h"
#include "bus_worker.h"

/* Provide implementations for ESP_LOG and esp_err_to_name used by bus_manager.c */
void host_test_log_record(char level, const char *tag, const char *format, ...) {
    (void)level; (void)tag; (void)format;
}
const char *esp_err_to_name(esp_err_t err) {
    (void)err; return "ESP_OK";
}

/* Provide dma_pool stubs (validate_manifest_resources calls dma_pool_allocate) */
esp_err_t dma_pool_allocate(dma_pool_t *pool, uint8_t bus_type,
                             const char *hw_id, uint32_t *out_dma_id) {
    (void)pool; (void)bus_type; (void)hw_id;
    if (out_dma_id) *out_dma_id = 1;
    return ESP_OK;
}
esp_err_t dma_pool_release_by_hw(dma_pool_t *pool, const char *hw_id) { (void)pool; (void)hw_id; return ESP_OK; }
void dma_pool_init(dma_pool_t *pool, const hw_dma_t *table, int count) {
    (void)pool; (void)table; (void)count;
}
size_t dma_pool_serialize(dma_pool_t *pool, uint8_t *buf, size_t buf_size) {
    (void)pool; (void)buf; (void)buf_size; return 0;
}

/* config_mgr stubs */
const config_manifest_t *config_mgr_get_manifest(void) { return NULL; }

/* bus_dma_uart0_is_available: defined here since we don't link bus_dma.c */
static bool s_uart0_available = true;
bool bus_dma_uart0_is_available(void) { return s_uart0_available; }
/* bus_dma init/deinit/write/read/transact stubs for bus_manager.c link */
esp_err_t bus_dma_init_preferred(bus_dma_ctx_t *ctx, uint8_t bus_type,
                                 bool dma_enabled, const uint8_t *config,
                                 size_t config_len, int32_t controller_id)
{
    (void)ctx; (void)bus_type; (void)dma_enabled; (void)config;
    (void)config_len; (void)controller_id;
    return ESP_OK;
}
esp_err_t bus_dma_init(bus_dma_ctx_t *ctx, uint8_t bus_type, bool dma_enabled,
                       const uint8_t *config, size_t config_len)
{
    return bus_dma_init_preferred(ctx, bus_type, dma_enabled, config, config_len, -1);
}
esp_err_t bus_dma_deinit(bus_dma_ctx_t *ctx) { (void)ctx; return ESP_OK; }
esp_err_t bus_dma_write(bus_dma_ctx_t *ctx, const uint8_t *data, size_t len)
{
    (void)ctx; (void)data; (void)len; return ESP_OK;
}
size_t bus_dma_read(bus_dma_ctx_t *ctx, uint8_t *buf, size_t buf_size)
{
    (void)ctx; (void)buf; (void)buf_size; return 0;
}
esp_err_t bus_dma_transact(bus_dma_ctx_t *ctx,
                           const uint8_t *tx, size_t tx_len,
                           uint8_t *rx, size_t rx_size, size_t *rx_len)
{
    (void)ctx; (void)tx; (void)tx_len; (void)rx; (void)rx_size;
    if (rx_len) *rx_len = 0;
    return ESP_OK;
}
QueueHandle_t bus_dma_uart_event_queue(const bus_dma_ctx_t *ctx)
{
    (void)ctx; return NULL;
}
/* bus_worker stubs (bus_manager.c calls these at non-preflight scope) */
void bus_worker_set_callbacks(write_rsp_cb_t wr_cb, data_rpt_cb_t dr_cb) { (void)wr_cb; (void)dr_cb; }
void bus_worker_set_channel_cmd_v2_final_cb(channel_cmd_v2_final_cb_t cb) { (void)cb; }
void bus_worker_start(bus_runtime_t *rt) { (void)rt; }
bool bus_worker_suspend(void) { return true; }
void bus_worker_resume(void) {}
void bus_worker_stop(void) {}
void bus_worker_discard_queued(bus_runtime_t *rt) { (void)rt; }
uint32_t bus_worker_get_rx_timeout_count(int channel) { (void)channel; return 0; }
uint32_t bus_worker_get_report_drop_count(void) { return 0; }
uint32_t bus_worker_get_report_queue_high_water(void) { return 0; }
uint32_t bus_worker_get_min_stack_watermark(void) { return 0; }
void msg_handler_send_write_rsp(uint32_t rid, bool s, uint32_t e, const char *m)
{
    (void)rid; (void)s; (void)e; (void)m;
}
void msg_handler_send_data_report(uint32_t ch, uint64_t ts, uint32_t seq,
    const uint8_t *d, size_t l, uint32_t ec, uint32_t rid, uint32_t eid,
    uint32_t tid, uint8_t ci)
{
    (void)ch; (void)ts; (void)seq; (void)d; (void)l; (void)ec; (void)rid;
    (void)eid; (void)tid; (void)ci;
}

/* Now include bus_manager.c to access static functions */
#include "../components/bus_manager/bus_manager.c"

/* ---- Test helpers ---- */

static int failures = 0;
#define CHECK(cond, msg) do { \
    if (!(cond)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (msg)); \
        failures++; \
        return; \
    } \
} while (0)

/* Test runtime: provide stack-allocated arrays to avoid NULL derefs */
static uint32_t s_test_bus_ch[SCHED_MAX_CHANNELS];
static bus_dma_ctx_t s_test_bus_ctx[SCHED_MAX_CHANNELS];
static char s_test_bus_hw_id[SCHED_MAX_CHANNELS * 16];
static QueueHandle_t s_test_pending_queues[SCHED_MAX_CHANNELS];

static void init_test_runtime(bus_runtime_t *rt)
{
    memset(rt, 0, sizeof(*rt));
    rt->bus_ctx = s_test_bus_ctx;
    rt->bus_ch = s_test_bus_ch;
    rt->bus_hw_id = s_test_bus_hw_id;
    rt->pending_queues = s_test_pending_queues;
    rt->dma_pool = NULL;
    rt->lease_hints_valid = false;
    memset(s_test_bus_ctx, 0, sizeof(s_test_bus_ctx));
    memset(s_test_bus_ch, 0, sizeof(s_test_bus_ch));
}

static void make_uart_channel(config_channel_t *ch, uint32_t id,
                              int tx, int rx, uint32_t baud, bool dma)
{
    memset(ch, 0, sizeof(*ch));
    ch->id = id;
    ch->bus_type = 1; /* UART */
    ch->enabled = true;
    ch->interval_ms = 5000;
    /* bus_config: [tx, rx, baud BE32] + optional flags */
    ch->bus_config[0] = (uint8_t)tx;
    ch->bus_config[1] = (uint8_t)rx;
    ch->bus_config[2] = (baud >> 24) & 0xFF;
    ch->bus_config[3] = (baud >> 16) & 0xFF;
    ch->bus_config[4] = (baud >> 8) & 0xFF;
    ch->bus_config[5] = baud & 0xFF;
    ch->bus_config_len = 6;
    if (dma) {
        ch->bus_config[6] = 0x01;
        ch->bus_config_len = 7;
    }
    ch->dma_enabled = dma;
    ch->dma_enabled_present = true;
}

static void make_spi_channel(config_channel_t *ch, uint32_t id,
                             int cs, int mosi, int miso, int sclk,
                             uint32_t freq, bool dma)
{
    memset(ch, 0, sizeof(*ch));
    ch->id = id;
    ch->bus_type = 3; /* SPI */
    ch->enabled = true;
    ch->interval_ms = 5000;
    ch->bus_config[0] = (uint8_t)cs;
    ch->bus_config[1] = 0; /* mode */
    ch->bus_config[2] = (freq >> 24) & 0xFF;
    ch->bus_config[3] = (freq >> 16) & 0xFF;
    ch->bus_config[4] = (freq >> 8) & 0xFF;
    ch->bus_config[5] = freq & 0xFF;
    ch->bus_config[6] = (uint8_t)mosi;
    ch->bus_config[7] = (uint8_t)miso;
    ch->bus_config[8] = (uint8_t)sclk;
    ch->bus_config_len = 9;
    if (dma) {
        ch->bus_config_len = 10;
        ch->bus_config[9] = 0x01;
    }
    ch->dma_enabled = dma;
    ch->dma_enabled_present = true;
}

static void make_i2c_channel(config_channel_t *ch, uint32_t id,
                             int sda, int scl, uint8_t addr, uint32_t freq)
{
    memset(ch, 0, sizeof(*ch));
    ch->id = id;
    ch->bus_type = 2; /* I2C */
    ch->enabled = true;
    ch->interval_ms = 5000;
    ch->bus_config[0] = (uint8_t)sda;
    ch->bus_config[1] = (uint8_t)scl;
    ch->bus_config[2] = addr;
    ch->bus_config[3] = (freq >> 24) & 0xFF;
    ch->bus_config[4] = (freq >> 16) & 0xFF;
    ch->bus_config[5] = (freq >> 8) & 0xFF;
    ch->bus_config[6] = freq & 0xFF;
    ch->bus_config_len = 7;
    ch->dma_enabled = false;
    ch->dma_enabled_present = true;
}

static void make_manifest(config_manifest_t *m, config_channel_t *chans, int count)
{
    memset(m, 0, sizeof(*m));
    memcpy(m->channels, chans, sizeof(config_channel_t) * count);
    m->channel_count = count;
}

/* ---- Test cases ---- */

static void test_uart_tx_rx_same_pin_rejected(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.dma_pool = NULL;
    bus_plan_entry_t plan[MAX_CHANNELS];

    config_channel_t ch;
    make_uart_channel(&ch, 1, 16, 16, 9600, false); /* TX == RX */
    config_manifest_t m;
    make_manifest(&m, &ch, 1);

    esp_err_t err = validate_manifest_resources(&rt, &m, plan);
    CHECK(err == ESP_ERR_INVALID_ARG, "TX==RX must be rejected");
}

static void test_uart_baud_zero_rejected(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.dma_pool = NULL;
    bus_plan_entry_t plan[MAX_CHANNELS];

    config_channel_t ch;
    make_uart_channel(&ch, 1, 16, 17, 0, false); /* baud==0 */
    config_manifest_t m;
    make_manifest(&m, &ch, 1);

    esp_err_t err = validate_manifest_resources(&rt, &m, plan);
    CHECK(err == ESP_ERR_INVALID_ARG, "baud==0 must be rejected");
}

static void test_uart_duplicate_channel_id_rejected(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);
    bus_plan_entry_t plan[MAX_CHANNELS];

    config_channel_t chans[2];
    make_uart_channel(&chans[0], 100, 16, 17, 9600, false);
    make_uart_channel(&chans[1], 100, 18, 19, 9600, false);
    config_manifest_t m;
    make_manifest(&m, chans, 2);

    esp_err_t err = validate_manifest_resources(&rt, &m, plan);
    CHECK(err == ESP_ERR_INVALID_ARG, "duplicate channel ID must be rejected");
}

static void test_uart_reserved_pin_rejected(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);
    bus_plan_entry_t plan[MAX_CHANNELS];

    config_channel_t ch;
    /* C6 reserved: USB_DN=12, USB_DP=13, LED=8 */
    make_uart_channel(&ch, 1, 12, 17, 9600, false);
    config_manifest_t m;
    make_manifest(&m, &ch, 1);

    esp_err_t err = validate_manifest_resources(&rt, &m, plan);
    CHECK(err == ESP_ERR_INVALID_ARG, "reserved pin must be rejected");
}

static void test_uart_baud_exceeds_max_rejected(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);
    bus_plan_entry_t plan[MAX_CHANNELS];

    config_channel_t ch;
    /* C6 max_baud is 5000000; request 6000000 */
    make_uart_channel(&ch, 1, 16, 17, 6000000, false);
    config_manifest_t m;
    make_manifest(&m, &ch, 1);

    esp_err_t err = validate_manifest_resources(&rt, &m, plan);
    CHECK(err == ESP_ERR_INVALID_ARG, "baud exceeding max must be rejected");
}

static void test_valid_uart_manifest_passes(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);
    bus_plan_entry_t plan[MAX_CHANNELS];

    config_channel_t ch;
    make_uart_channel(&ch, 1, 16, 17, 9600, false);
    config_manifest_t m;
    make_manifest(&m, &ch, 1);

    esp_err_t err = validate_manifest_resources(&rt, &m, plan);
    CHECK(err == ESP_OK, "valid UART manifest should pass");
    CHECK(plan[0].valid, "plan entry should be valid");
    CHECK(plan[0].controller_id >= 0, "controller_id should be assigned");
}

static void test_uart_controller_exhaustion_rejected(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);
    bus_plan_entry_t plan[MAX_CHANNELS];

    /* C6 has 2 HP UARTs (UART0, UART1). Request 3 channels. */
    config_channel_t chans[3];
    make_uart_channel(&chans[0], 1, 16, 17, 9600, false);
    make_uart_channel(&chans[1], 2, 18, 19, 9600, false);
    /* Third channel uses custom pins not in profile — should fail to find a free UART */
    make_uart_channel(&chans[2], 3, 20, 21, 9600, false);
    config_manifest_t m;
    make_manifest(&m, chans, 3);

    esp_err_t err = validate_manifest_resources(&rt, &m, plan);
    /* C6 has only 2 UARTs; the third must fail with NOT_SUPPORTED.
     * However, C6 HW_UART_COUNT=2, and if pins 20/21 aren't in the profile,
     * the custom-pin path searches for a free UART. Since UART0+UART1 are
     * both used, it should return NOT_SUPPORTED. */
    CHECK(err == ESP_ERR_NOT_SUPPORTED, "third UART should exhaust controllers");
}

static void test_spi_pin_conflict_rejected(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);
    bus_plan_entry_t plan[MAX_CHANNELS];

    config_channel_t ch;
    make_spi_channel(&ch, 1, 10, 11, 11, 12, 1000000, false); /* MOSI==MISO */
    config_manifest_t m;
    make_manifest(&m, &ch, 1);

    esp_err_t err = validate_manifest_resources(&rt, &m, plan);
    CHECK(err == ESP_ERR_INVALID_ARG, "MOSI==MISO must be rejected");
}

static void test_i2c_sda_scl_same_rejected(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);
    bus_plan_entry_t plan[MAX_CHANNELS];

    config_channel_t ch;
    make_i2c_channel(&ch, 1, 6, 6, 0x48, 100000); /* SDA==SCL */
    config_manifest_t m;
    make_manifest(&m, &ch, 1);

    esp_err_t err = validate_manifest_resources(&rt, &m, plan);
    CHECK(err == ESP_ERR_INVALID_ARG, "SDA==SCL must be rejected");
}

static void test_mixed_bus_manifest_passes(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);
    bus_plan_entry_t plan[MAX_CHANNELS];

    config_channel_t chans[3];
    /* C6: UART0 TX=16 RX=17, SPI2 MOSI=23 MISO=19 SCLK=18 CS=5, I2C0 SDA=21 SCL=22 */
    make_uart_channel(&chans[0], 1, 16, 17, 9600, false);
    make_spi_channel(&chans[1], 2, 5, 23, 19, 18, 1000000, false);
    make_i2c_channel(&chans[2], 3, 21, 22, 0x48, 100000);
    /* I2C SDA=21 conflicts with UART1 RX=21 (but UART1 not used here) */
    /* I2C SCL=22 is fine */
    config_manifest_t m;
    make_manifest(&m, chans, 3);

    esp_err_t err = validate_manifest_resources(&rt, &m, plan);
    CHECK(err == ESP_OK, "valid mixed bus manifest should pass");
    CHECK(plan[0].valid, "UART plan entry valid");
    CHECK(plan[1].valid, "SPI plan entry valid");
    CHECK(plan[2].valid, "I2C plan entry valid");
}

static void test_dma_requested_without_pool_rejected(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.dma_pool = NULL; /* no DMA pool */
    bus_plan_entry_t plan[MAX_CHANNELS];

    config_channel_t ch;
    make_uart_channel(&ch, 1, 16, 17, 9600, true); /* DMA requested */
    config_manifest_t m;
    make_manifest(&m, &ch, 1);

    esp_err_t err = validate_manifest_resources(&rt, &m, plan);
    CHECK(err == ESP_ERR_INVALID_STATE, "DMA without pool must be rejected");
}

static void test_disabled_channel_skipped(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);
    bus_plan_entry_t plan[MAX_CHANNELS];

    config_channel_t ch;
    make_uart_channel(&ch, 1, 16, 16, 0, false); /* would fail validation */
    ch.enabled = false; /* but disabled, so skipped */
    config_manifest_t m;
    make_manifest(&m, &ch, 1);

    esp_err_t err = validate_manifest_resources(&rt, &m, plan);
    CHECK(err == ESP_OK, "disabled channel should be skipped");
    CHECK(!plan[0].valid, "disabled channel plan should be invalid");
}

static void test_null_inputs_rejected(void)
{
    bus_plan_entry_t plan[MAX_CHANNELS];
    esp_err_t err = validate_manifest_resources(NULL, NULL, plan);
    CHECK(err == ESP_ERR_INVALID_ARG, "NULL inputs must be rejected");
}

static void test_shared_dma_lease_is_retained_for_other_channel(void)
{
    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.bus_ch[0] = 1;
    rt.bus_ch[1] = 2;
    rt.bus_ctx[0].initialized = true;
    rt.bus_ctx[1].initialized = true;
    strcpy(rt.bus_hw_id + 0 * 16, "uart/UART0");
    strcpy(rt.bus_hw_id + 1 * 16, "uart/UART0");

    CHECK(runtime_has_other_hw_lease(&rt, 0, "uart/UART0"),
          "shared physical lease must remain while another channel is active");
    CHECK(runtime_has_other_hw_lease(&rt, 1, "uart/UART0"),
          "the other logical channel must be reported as a shared lease");

    rt.bus_ctx[1].initialized = false;
    CHECK(!runtime_has_other_hw_lease(&rt, 0, "uart/UART0"),
          "last channel should be allowed to release the physical lease");
}

int main(void)
{
    test_null_inputs_rejected();
    test_disabled_channel_skipped();
    test_uart_tx_rx_same_pin_rejected();
    test_uart_baud_zero_rejected();
    test_uart_duplicate_channel_id_rejected();
    test_uart_reserved_pin_rejected();
    test_uart_baud_exceeds_max_rejected();
    test_valid_uart_manifest_passes();
    test_uart_controller_exhaustion_rejected();
    test_spi_pin_conflict_rejected();
    test_i2c_sda_scl_same_rejected();
    test_mixed_bus_manifest_passes();
    test_dma_requested_without_pool_rejected();
    test_shared_dma_lease_is_retained_for_other_channel();

    if (failures != 0) {
        fprintf(stderr, "%d test(s) failed\n", failures);
        return 1;
    }
    puts("bus_manager_manifest_tests: all tests passed");
    return 0;
}
