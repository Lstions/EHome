/*
 * bus_dma_tests.c
 *
 * Host tests for bus_dma.c — the unified bus DMA engine.
 * Includes bus_dma.c directly to test internal logic (port registry,
 * lease binding, event queue lifecycle, ref_count sharing).
 *
 * Coverage (multi-bus P1/P2 core):
 *   1. UART init — basic, preferred_controller lease, hw-pin derivation
 *   2. UART port sharing — same pin/baud reuses port, ref_count, shared queue
 *   3. UART lease conflict — different preferred controller for same pins
 *   4. UART pin validation — reserved pins, out-of-range, config too short
 *   5. UART DMA vs polled — buffer size selection
 *   6. UART event queue — created on init, shared on reuse, NULL after deinit
 *   7. UART deinit — ref_count decrement, last deinit deletes driver
 *   8. SPI init — preferred_controller, bus sharing, conflict
 *   9. I2C init — preferred_controller, bus sharing, conflict, SDA==SCL
 *  10. bus_config_get_dma_enabled — inline helper
 *  11. Public API guards — wrong bus type, uninitialized, NULL
 */

#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <stdint.h>

/* ---- Include all stub headers FIRST for type definitions ---- */
#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/queue.h"
#include "freertos/task.h"
#include "esp_err.h"
#include "esp_log.h"
#include "esp_system.h"
#include "driver/uart.h"
#include "driver/spi_master.h"
#include "driver/i2c_master.h"
#include "driver/gpio.h"
#include "nvs_flash.h"
#include "rgb_led.h"

/* ---- Include component headers for types ---- */
#include "bus_dma.h"
#include "hw_tables.h"

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
 * ESP-IDF / FreeRTOS stubs with state tracking
 * ===================================================================== */

/* ---- Logging ---- */
void host_test_log_record(char level, const char *tag, const char *format, ...) {
    (void)level; (void)tag; (void)format;
}
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_ERR"; }
void esp_restart(void) { /* no-op */ }

/* ---- Semaphore stubs ---- */
static int g_mutex_count = 0;
SemaphoreHandle_t xSemaphoreCreateMutex(void) { g_mutex_count++; return (SemaphoreHandle_t)(uintptr_t)g_mutex_count; }
int xSemaphoreTake(SemaphoreHandle_t sem, uint32_t ticks) { (void)sem; (void)ticks; return 1; }
int xSemaphoreGive(SemaphoreHandle_t sem) { (void)sem; return 1; }
void vSemaphoreDelete(SemaphoreHandle_t sem) { (void)sem; }

/* ---- GPIO stubs ---- */
esp_err_t gpio_config(const gpio_config_t *c) { (void)c; return ESP_OK; }
esp_err_t gpio_set_level(int pin, int level) { (void)pin; (void)level; return ESP_OK; }
int gpio_get_level(int pin) { (void)pin; return 1; }
esp_err_t gpio_reset_pin(int pin) { (void)pin; return ESP_OK; }
esp_err_t gpio_set_direction(int pin, int mode) { (void)pin; (void)mode; return ESP_OK; }
esp_err_t gpio_hold_en(int pin) { (void)pin; return ESP_OK; }

/* ---- NVS stubs ---- */
static uint8_t g_nvs_dl_flag = 0;
esp_err_t nvs_open(const char *name, int mode, nvs_handle_t *h) {
    (void)name; (void)mode; *h = 1; return ESP_OK;
}
esp_err_t nvs_get_u8(nvs_handle_t h, const char *key, uint8_t *v) {
    (void)h; (void)key; *v = g_nvs_dl_flag; return ESP_OK;
}
esp_err_t nvs_set_u8(nvs_handle_t h, const char *key, uint8_t v) {
    (void)h; (void)key; g_nvs_dl_flag = v; return ESP_OK;
}
esp_err_t nvs_get_u64(nvs_handle_t h, const char *key, uint64_t *v) {
    (void)h; (void)key; *v = 0; return ESP_OK;
}
esp_err_t nvs_get_str(nvs_handle_t h, const char *key, char *v, size_t *l) {
    (void)h; (void)key; (void)v; *l = 0; return ESP_OK;
}
esp_err_t nvs_set_u64(nvs_handle_t h, const char *key, uint64_t v) {
    (void)h; (void)key; (void)v; return ESP_OK;
}
esp_err_t nvs_set_str(nvs_handle_t h, const char *key, const char *v) {
    (void)h; (void)key; (void)v; return ESP_OK;
}
esp_err_t nvs_erase_key(nvs_handle_t h, const char *key) {
    (void)h; (void)key; return ESP_OK;
}
esp_err_t nvs_commit(nvs_handle_t h) { (void)h; return ESP_OK; }
void nvs_close(nvs_handle_t h) { (void)h; }

/* ---- RGB LED stub ---- */
void rgb_led_set_state(led_state_t s) { (void)s; }

/* ---- UART driver stubs with state tracking ---- */
static bool g_uart_installed[UART_NUM_MAX];
static size_t g_uart_rx_buf[UART_NUM_MAX];
static size_t g_uart_tx_buf[UART_NUM_MAX];
static int g_fake_uart_queues[UART_NUM_MAX];  /* distinct addresses */
static int g_uart_delete_count[UART_NUM_MAX];

esp_err_t uart_param_config(uart_port_t port, const uart_config_t *cfg) {
    (void)cfg;
    if (port < 0 || port >= UART_NUM_MAX) return ESP_ERR_INVALID_ARG;
    return ESP_OK;
}
esp_err_t uart_set_pin(uart_port_t port, int tx, int rx, int rts, int cts) {
    (void)tx; (void)rx; (void)rts; (void)cts;
    if (port < 0 || port >= UART_NUM_MAX) return ESP_ERR_INVALID_ARG;
    return ESP_OK;
}
esp_err_t uart_driver_install(uart_port_t port, int rx_buf, int tx_buf,
                              int queue_size, QueueHandle_t *queue, int flags) {
    (void)queue_size; (void)flags;
    if (port < 0 || port >= UART_NUM_MAX) return ESP_ERR_INVALID_ARG;
    if (g_uart_installed[port]) return ESP_ERR_INVALID_STATE;
    g_uart_installed[port] = true;
    g_uart_rx_buf[port] = (size_t)rx_buf;
    g_uart_tx_buf[port] = (size_t)tx_buf;
    if (queue) *queue = (QueueHandle_t)&g_fake_uart_queues[port];
    return ESP_OK;
}
esp_err_t uart_driver_delete(uart_port_t port) {
    if (port < 0 || port >= UART_NUM_MAX) return ESP_ERR_INVALID_ARG;
    if (!g_uart_installed[port]) return ESP_ERR_INVALID_STATE;
    g_uart_installed[port] = false;
    g_uart_delete_count[port]++;
    return ESP_OK;
}
int uart_write_bytes(uart_port_t port, const char *data, size_t len) {
    (void)data;
    if (port < 0 || port >= UART_NUM_MAX) return -1;
    return (int)len;
}
int uart_read_bytes(uart_port_t port, void *buf, uint32_t length, TickType_t ticks) {
    (void)buf; (void)length; (void)ticks;
    if (port < 0 || port >= UART_NUM_MAX) return -1;
    return 0;  /* no data */
}
esp_err_t uart_wait_tx_done(uart_port_t port, TickType_t ticks) {
    (void)port; (void)ticks; return ESP_OK;
}
esp_err_t uart_set_rx_timeout(uart_port_t port, uint8_t tout) {
    (void)port; (void)tout; return ESP_OK;
}

/* ---- SPI driver stubs with state tracking ---- */
static bool g_spi_initialized[SPI_HOST_MAX];
static int g_spi_dma_mode[SPI_HOST_MAX];
static int g_spi_dev_count;

/* Full-duplex contract tracking.
 *
 * The previous stub was a bare `return ESP_OK` and therefore could not fail
 * for the one frame shape that broke the S3 node: a full-duplex transaction
 * with rxlength > length.  IDF rejects exactly that in check_trans_valid()
 * (esp_driver_spi/src/gpspi/spi_master.c:1119), so the stub now models the
 * rule and records what the driver was asked to do. */
static int g_spi_reject_fd_calls;   /* calls IDF would reject: rxlength > length */
static int g_spi_transmit_calls;    /* accepted calls */
static bool g_spi_last_valid;
static spi_transaction_t g_spi_last;
static uint8_t g_spi_last_tx[512];  /* copy of the MOSI bytes */
static size_t  g_spi_last_tx_len;
static uint8_t g_spi_last_rx[512];  /* bytes the modelled slave drives on MISO */
static size_t  g_spi_slave_resp_len;   /* how many response bytes it will drive */
static size_t  g_spi_slave_cmd_len;    /* how many command bytes it consumes first */

esp_err_t spi_bus_initialize(spi_host_device_t host, const spi_bus_config_t *cfg, int dma) {
    (void)cfg;
    if (host < 0 || host >= SPI_HOST_MAX) return ESP_ERR_INVALID_ARG;
    if (g_spi_initialized[host]) return ESP_ERR_INVALID_STATE;
    g_spi_initialized[host] = true;
    g_spi_dma_mode[host] = dma;
    return ESP_OK;
}
esp_err_t spi_bus_free(spi_host_device_t host) {
    if (host < 0 || host >= SPI_HOST_MAX) return ESP_ERR_INVALID_ARG;
    if (!g_spi_initialized[host]) return ESP_ERR_INVALID_STATE;
    g_spi_initialized[host] = false;
    return ESP_OK;
}
esp_err_t spi_bus_add_device(spi_host_device_t host,
                             const spi_device_interface_config_t *cfg,
                             spi_device_handle_t *handle) {
    (void)cfg;
    if (!g_spi_initialized[host]) return ESP_ERR_INVALID_STATE;
    g_spi_dev_count++;
    *handle = (spi_device_handle_t)(uintptr_t)(0x100 + g_spi_dev_count);
    return ESP_OK;
}
esp_err_t spi_bus_remove_device(spi_device_handle_t handle) {
    (void)handle; g_spi_dev_count--; return ESP_OK;
}
/* Model IDF's full-duplex length rule and the peripheral's RX capture.
 *
 * IDF check_trans_valid() (spi_master.c:1119):
 *     SPI_CHECK(is_half_duplex || rxlength <= length,
 *               "rx length > tx length in full duplex mode", ESP_ERR_INVALID_ARG);
 * and spi_hal_fetch_result() copies rx_bitlen bits out of the work registers
 * starting at bit 0 of the frame.  A stub that returns ESP_OK unconditionally
 * is blind to the defect this test exists for, so both behaviours are modelled
 * here.  It also asserts the MOSI pad contract so the test cannot pass with a
 * widened frame that forgot to zero its dummy tail. */
esp_err_t spi_device_transmit(spi_device_handle_t handle, spi_transaction_t *t) {
    (void)handle;
    if (t == NULL) return ESP_ERR_INVALID_ARG;

    g_spi_transmit_calls++;

    if (t->rxlength > t->length) {
        g_spi_reject_fd_calls++;
        g_spi_last_valid = false;
        return ESP_ERR_INVALID_ARG;
    }
    g_spi_last_valid = true;

    size_t tx_len = t->length / 8;
    if (tx_len > sizeof(g_spi_last_tx)) tx_len = sizeof(g_spi_last_tx);
    g_spi_last_tx_len = tx_len;
    if (t->tx_buffer != NULL) memcpy(g_spi_last_tx, t->tx_buffer, tx_len);
    else memset(g_spi_last_tx, 0, tx_len);

    g_spi_last = *t;

    /* Model a real SPI slave.
     *
     * A slave cannot answer a command while that command is still being
     * shifted in, so its response bytes appear on MISO only during the clocks
     * AFTER the command.  The modelled slave consumes g_spi_slave_cmd_len
     * leading bytes (driving 0x00 while it reads the command) and then drives
     * g_spi_slave_resp_len response bytes.  A frame that is too short to hold
     * the response cannot return it, which is exactly the failure the test
     * exists to catch. */
    size_t frame_len = t->rxlength / 8;
    if (frame_len > sizeof(g_spi_last_rx)) frame_len = sizeof(g_spi_last_rx);

    uint8_t miso[512];
    memset(miso, 0, sizeof(miso));
    size_t off = g_spi_slave_cmd_len;
    if (off < sizeof(miso)) {
        size_t n = g_spi_slave_resp_len;
        if (n > sizeof(miso) - off) n = sizeof(miso) - off;
        memcpy(miso + off, g_spi_last_rx, n);
    }

    if (t->rx_buffer != NULL) memcpy(t->rx_buffer, miso, frame_len);
    return ESP_OK;
}

/* Program the MISO bytes the modelled slave will shift out, and how many
 * command bytes it must consume before it starts driving them. */
static void spi_slave_set_miso(const uint8_t *bytes, size_t len, size_t cmd_len)
{
    if (len > sizeof(g_spi_last_rx)) len = sizeof(g_spi_last_rx);
    memset(g_spi_last_rx, 0, sizeof(g_spi_last_rx));
    if (bytes && len) memcpy(g_spi_last_rx, bytes, len);
    g_spi_slave_resp_len = bytes ? len : 0;
    g_spi_slave_cmd_len = cmd_len;
}

/* ---- I2C driver stubs with state tracking ---- */
static int g_i2c_bus_count;
static int g_i2c_dev_count;
static int g_fake_i2c_buses[8];

esp_err_t i2c_new_master_bus(const i2c_master_bus_config_t *cfg,
                             i2c_master_bus_handle_t *handle) {
    (void)cfg;
    *handle = (i2c_master_bus_handle_t)&g_fake_i2c_buses[g_i2c_bus_count++];
    return ESP_OK;
}
esp_err_t i2c_del_master_bus(i2c_master_bus_handle_t handle) {
    (void)handle; g_i2c_bus_count--; return ESP_OK;
}
esp_err_t i2c_master_bus_add_device(i2c_master_bus_handle_t bus,
                                    const i2c_device_config_t *cfg,
                                    i2c_master_dev_handle_t *handle) {
    (void)bus; (void)cfg;
    g_i2c_dev_count++;
    *handle = (i2c_master_dev_handle_t)(uintptr_t)(0x200 + g_i2c_dev_count);
    return ESP_OK;
}
esp_err_t i2c_master_bus_rm_device(i2c_master_dev_handle_t handle) {
    (void)handle; g_i2c_dev_count--; return ESP_OK;
}
esp_err_t i2c_master_transmit_receive(i2c_master_dev_handle_t dev,
                                      const uint8_t *tx, size_t tx_len,
                                      uint8_t *rx, size_t rx_len, int timeout) {
    (void)dev; (void)tx; (void)tx_len; (void)rx; (void)rx_len; (void)timeout;
    return ESP_OK;
}
esp_err_t i2c_master_transmit(i2c_master_dev_handle_t dev,
                              const uint8_t *tx, size_t tx_len, int timeout) {
    (void)dev; (void)tx; (void)tx_len; (void)timeout; return ESP_OK;
}
esp_err_t i2c_master_receive(i2c_master_dev_handle_t dev,
                             uint8_t *rx, size_t rx_len, int timeout) {
    (void)dev; (void)rx; (void)rx_len; (void)timeout; return ESP_OK;
}

/* =====================================================================
 * Include bus_dma.c directly to access static state
 * ===================================================================== */
#include "../components/bus_dma/bus_dma.c"

/* =====================================================================
 * Test helpers — reset all static state between tests
 * ===================================================================== */
static void reset_all_state(void)
{
    /* Reset bus_dma.c internal registries */
    memset(s_uart_ports, 0, sizeof(s_uart_ports));
    for (int i = 0; i < MAX_UART_PORTS; i++)
        s_uart_ports[i].port = UART_NUM_MAX;
    s_uart_registry_initialized = true;

    memset(s_spi_buses, 0, sizeof(s_spi_buses));
    for (int i = 0; i < MAX_SPI_BUSES; i++)
        s_spi_buses[i].host = SPI_HOST_MAX;
    s_spi_registry_initialized = true;

    memset(s_i2c_buses, 0, sizeof(s_i2c_buses));
    s_i2c_registry_initialized = true;

    s_uart0_available = true;

    /* Reset driver stub state */
    memset(g_uart_installed, 0, sizeof(g_uart_installed));
    memset(g_uart_rx_buf, 0, sizeof(g_uart_rx_buf));
    memset(g_uart_tx_buf, 0, sizeof(g_uart_tx_buf));
    memset(g_uart_delete_count, 0, sizeof(g_uart_delete_count));
    memset(g_spi_initialized, 0, sizeof(g_spi_initialized));
    memset(g_spi_dma_mode, 0, sizeof(g_spi_dma_mode));
    g_spi_dev_count = 0;
    g_spi_reject_fd_calls = 0;
    g_spi_transmit_calls = 0;
    g_spi_last_valid = false;
    memset(&g_spi_last, 0, sizeof(g_spi_last));
    memset(g_spi_last_tx, 0, sizeof(g_spi_last_tx));
    g_spi_last_tx_len = 0;
    memset(g_spi_last_rx, 0, sizeof(g_spi_last_rx));
    g_spi_slave_resp_len = 0;
    g_spi_slave_cmd_len = 0;
    g_i2c_bus_count = 0;
    g_i2c_dev_count = 0;
    g_mutex_count = 0;
}

/* Build a UART config blob: [tx, rx, baud×4 BE] */
static void make_uart_cfg(uint8_t *buf, int tx, int rx, uint32_t baud)
{
    buf[0] = (uint8_t)tx;
    buf[1] = (uint8_t)rx;
    buf[2] = (uint8_t)(baud >> 24);
    buf[3] = (uint8_t)(baud >> 16);
    buf[4] = (uint8_t)(baud >> 8);
    buf[5] = (uint8_t)(baud);
}

/* Build an SPI config blob: [cs, mode, freq×4 BE, mosi, miso, sclk] */
static void make_spi_cfg(uint8_t *buf, int cs, uint8_t mode, uint32_t freq,
                         int mosi, int miso, int sclk)
{
    buf[0] = (uint8_t)cs;
    buf[1] = mode;
    buf[2] = (uint8_t)(freq >> 24);
    buf[3] = (uint8_t)(freq >> 16);
    buf[4] = (uint8_t)(freq >> 8);
    buf[5] = (uint8_t)(freq);
    buf[6] = (uint8_t)mosi;
    buf[7] = (uint8_t)miso;
    buf[8] = (uint8_t)sclk;
}

/* Build an I2C config blob: [sda, scl, addr, freq×4 BE] */
static void make_i2c_cfg(uint8_t *buf, int sda, int scl, uint8_t addr, uint32_t freq)
{
    buf[0] = (uint8_t)sda;
    buf[1] = (uint8_t)scl;
    buf[2] = addr;
    buf[3] = (uint8_t)(freq >> 24);
    buf[4] = (uint8_t)(freq >> 16);
    buf[5] = (uint8_t)(freq >> 8);
    buf[6] = (uint8_t)(freq);
}

/* =====================================================================
 * Test cases
 * ===================================================================== */

/* --- UART: basic init with hw-pin derivation (C6: TX16/RX17 → UART0) --- */
static void test_uart_init_hw_derive(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[6];
    make_uart_cfg(cfg, 16, 17, 115200);  /* C6 UART0 default pins */

    esp_err_t r = bus_dma_init(&ctx, BUS_TYPE_UART, false, cfg, sizeof(cfg));
    CHECK(r == ESP_OK, "uart init should succeed");
    CHECK(ctx.initialized, "ctx should be initialized");
    CHECK(ctx.cfg.uart.port == UART_NUM_0, "TX16/RX17 should derive to UART0");
    CHECK(ctx.cfg.uart.baud == 115200, "baud should match");
    CHECK(ctx.cfg.uart.tx_pin == 16, "tx_pin should match");
    CHECK(ctx.cfg.uart.rx_pin == 17, "rx_pin should match");
    CHECK(ctx.uart_event_queue != NULL, "event queue should be created");
    CHECK(g_uart_installed[UART_NUM_0], "UART0 driver should be installed");
    /* Non-DMA: 256-byte buffers */
    CHECK(g_uart_rx_buf[UART_NUM_0] == 256, "non-DMA rx buffer should be 256");
    CHECK(g_uart_tx_buf[UART_NUM_0] == 256, "non-DMA tx buffer should be 256");

    bus_dma_deinit(&ctx);
    CHECK(!ctx.initialized, "ctx should be deinitialized");
}

/* --- UART: DMA init uses 1024-byte buffers --- */
static void test_uart_dma_buffer_sizes(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[6];
    make_uart_cfg(cfg, 20, 21, 9600);  /* C6 UART1 default pins */

    esp_err_t r = bus_dma_init(&ctx, BUS_TYPE_UART, true, cfg, sizeof(cfg));
    CHECK(r == ESP_OK, "uart DMA init should succeed");
    CHECK(ctx.cfg.uart.port == UART_NUM_1, "TX20/RX21 should derive to UART1");
    CHECK(ctx.dma_enabled, "DMA should be enabled");
    CHECK(g_uart_rx_buf[UART_NUM_1] == 1024, "DMA rx buffer should be 1024");
    CHECK(g_uart_tx_buf[UART_NUM_1] == 1024, "DMA tx buffer should be 1024");

    bus_dma_deinit(&ctx);
}

/* --- UART: preferred_controller lease binding --- */
static void test_uart_preferred_controller(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[6];
    /* Custom pins (not in hw table) with preferred controller = UART1 */
    make_uart_cfg(cfg, 3, 4, 115200);

    esp_err_t r = bus_dma_init_preferred(&ctx, BUS_TYPE_UART, false, cfg, sizeof(cfg), 1);
    CHECK(r == ESP_OK, "preferred UART1 init should succeed");
    CHECK(ctx.cfg.uart.port == UART_NUM_1, "should bind to preferred UART1");
    CHECK(ctx.preferred_controller == 1, "preferred_controller should be 1");

    bus_dma_deinit(&ctx);
}

/* --- UART: preferred_controller = -1 uses dynamic allocation --- */
static void test_uart_no_preferred_dynamic(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[6];
    /* Custom pins, no preference → dynamic allocation from UART0_START_INDEX */
    make_uart_cfg(cfg, 3, 4, 115200);

    esp_err_t r = bus_dma_init_preferred(&ctx, BUS_TYPE_UART, false, cfg, sizeof(cfg), -1);
    CHECK(r == ESP_OK, "dynamic alloc should succeed");
    /* With USB console (UART0_START_INDEX=0), first free port is UART0 */
    CHECK(ctx.cfg.uart.port == UART_NUM_0, "dynamic should allocate UART0 first");

    bus_dma_deinit(&ctx);
}

/* --- UART: port sharing — same pin/baud reuses port, ref_count=2 --- */
static void test_uart_port_sharing(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx1, ctx2;
    uint8_t cfg[6];
    make_uart_cfg(cfg, 16, 17, 115200);

    esp_err_t r1 = bus_dma_init(&ctx1, BUS_TYPE_UART, false, cfg, sizeof(cfg));
    CHECK(r1 == ESP_OK, "first init should succeed");
    QueueHandle_t q1 = ctx1.uart_event_queue;

    esp_err_t r2 = bus_dma_init(&ctx2, BUS_TYPE_UART, false, cfg, sizeof(cfg));
    CHECK(r2 == ESP_OK, "second init (same pins) should succeed via sharing");
    CHECK(ctx2.cfg.uart.port == ctx1.cfg.uart.port, "should share same port");
    CHECK(ctx2.uart_event_queue == q1, "should share same event queue");

    /* Verify ref_count via registry */
    uart_port_entry_t *entry = uart_find_port(16, 17, 115200);
    CHECK(entry != NULL, "port entry should exist");
    CHECK(entry->ref_count == 2, "ref_count should be 2");

    /* First deinit: ref_count drops to 1, driver stays */
    bus_dma_deinit(&ctx1);
    entry = uart_find_port(16, 17, 115200);
    CHECK(entry != NULL, "port entry should still exist after first deinit");
    CHECK(entry->ref_count == 1, "ref_count should be 1 after first deinit");
    CHECK(g_uart_installed[UART_NUM_0], "driver should still be installed");

    /* Second deinit: ref_count drops to 0, driver deleted */
    bus_dma_deinit(&ctx2);
    entry = uart_find_port(16, 17, 115200);
    CHECK(entry == NULL || entry->port == UART_NUM_MAX,
          "port entry should be freed after last deinit");
    CHECK(!g_uart_installed[UART_NUM_0], "driver should be deleted after last deinit");
    CHECK(g_uart_delete_count[UART_NUM_0] == 1, "driver should be deleted exactly once");
}

/* --- UART: lease conflict — different preferred controller for same pins --- */
static void test_uart_lease_conflict(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx1, ctx2;
    uint8_t cfg[6];
    make_uart_cfg(cfg, 16, 17, 115200);

    /* First: bind to UART0 (hw-derived) */
    esp_err_t r1 = bus_dma_init(&ctx1, BUS_TYPE_UART, false, cfg, sizeof(cfg));
    CHECK(r1 == ESP_OK, "first init should succeed");
    CHECK(ctx1.cfg.uart.port == UART_NUM_0, "should be UART0");

    /* Second: same pins but prefer UART1 → conflict */
    esp_err_t r2 = bus_dma_init_preferred(&ctx2, BUS_TYPE_UART, false, cfg, sizeof(cfg), 1);
    CHECK(r2 == ESP_ERR_INVALID_STATE, "conflicting preferred controller should fail");

    bus_dma_deinit(&ctx1);
}

/* --- UART: port already allocated to different config --- */
static void test_uart_port_occupied(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx1, ctx2;
    uint8_t cfg1[6], cfg2[6];
    make_uart_cfg(cfg1, 16, 17, 115200);
    make_uart_cfg(cfg2, 3, 4, 9600);

    /* Occupy UART0 via hw-derive */
    esp_err_t r1 = bus_dma_init(&ctx1, BUS_TYPE_UART, false, cfg1, sizeof(cfg1));
    CHECK(r1 == ESP_OK, "first init should succeed");

    /* Try to force UART0 with different pins → port occupied */
    esp_err_t r2 = bus_dma_init_preferred(&ctx2, BUS_TYPE_UART, false, cfg2, sizeof(cfg2), 0);
    CHECK(r2 == ESP_ERR_INVALID_STATE, "occupied port should be rejected");

    bus_dma_deinit(&ctx1);
}

/* --- UART: reserved pin rejection (C6: USB_D-=12, USB_D+=13, LED=8) --- */
static void test_uart_reserved_pins(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[6];

    /* TX on USB_D- (GPIO12) */
    make_uart_cfg(cfg, 12, 17, 115200);
    CHECK(bus_dma_init(&ctx, BUS_TYPE_UART, false, cfg, sizeof(cfg)) == ESP_ERR_INVALID_ARG,
          "TX on USB_D- should be rejected");

    /* RX on USB_D+ (GPIO13) */
    make_uart_cfg(cfg, 16, 13, 115200);
    CHECK(bus_dma_init(&ctx, BUS_TYPE_UART, false, cfg, sizeof(cfg)) == ESP_ERR_INVALID_ARG,
          "RX on USB_D+ should be rejected");

    /* TX on LED (GPIO8) */
    make_uart_cfg(cfg, 8, 17, 115200);
    CHECK(bus_dma_init(&ctx, BUS_TYPE_UART, false, cfg, sizeof(cfg)) == ESP_ERR_INVALID_ARG,
          "TX on LED pin should be rejected");
}

/* --- UART: pin out of range (C6: max GPIO=30) --- */
static void test_uart_pin_out_of_range(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[6];

    make_uart_cfg(cfg, 31, 17, 115200);
    CHECK(bus_dma_init(&ctx, BUS_TYPE_UART, false, cfg, sizeof(cfg)) == ESP_ERR_INVALID_ARG,
          "TX pin 31 should be rejected on C6");

    make_uart_cfg(cfg, 16, 50, 115200);
    CHECK(bus_dma_init(&ctx, BUS_TYPE_UART, false, cfg, sizeof(cfg)) == ESP_ERR_INVALID_ARG,
          "RX pin 50 should be rejected on C6");
}

/* --- UART: config too short --- */
static void test_uart_config_too_short(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[4] = {16, 17, 0, 0};

    CHECK(bus_dma_init(&ctx, BUS_TYPE_UART, false, cfg, 4) == ESP_ERR_INVALID_SIZE,
          "config < 6 bytes should be rejected");
}

/* --- UART: event queue accessor --- */
static void test_uart_event_queue_accessor(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[6];
    make_uart_cfg(cfg, 16, 17, 115200);

    /* Uninitialized → NULL */
    memset(&ctx, 0, sizeof(ctx));
    CHECK(bus_dma_uart_event_queue(&ctx) == NULL, "uninitialized should return NULL");

    /* NULL → NULL */
    CHECK(bus_dma_uart_event_queue(NULL) == NULL, "NULL ctx should return NULL");

    /* Initialized UART → non-NULL */
    bus_dma_init(&ctx, BUS_TYPE_UART, false, cfg, sizeof(cfg));
    QueueHandle_t q = bus_dma_uart_event_queue(&ctx);
    CHECK(q != NULL, "initialized UART should return event queue");
    CHECK(q == ctx.uart_event_queue, "accessor should match ctx field");

    /* After deinit → NULL */
    bus_dma_deinit(&ctx);
    CHECK(bus_dma_uart_event_queue(&ctx) == NULL, "deinitialized should return NULL");
}

/* --- UART: UART0 unavailable (download mode) --- */
static void test_uart0_unavailable(void)
{
    reset_all_state();
    s_uart0_available = false;
    bus_dma_ctx_t ctx;
    uint8_t cfg[6];
    make_uart_cfg(cfg, 3, 4, 115200);

    /* Prefer UART0 but it's reserved */
    esp_err_t r = bus_dma_init_preferred(&ctx, BUS_TYPE_UART, false, cfg, sizeof(cfg), 0);
    CHECK(r == ESP_ERR_NOT_SUPPORTED, "preferred UART0 should fail when unavailable");

    s_uart0_available = true;
}

/* --- UART: all ports exhausted --- */
static void test_uart_ports_exhausted(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx[3];
    uint8_t cfg[6];

    /* Occupy all 3 UART ports */
    make_uart_cfg(cfg, 16, 17, 115200);
    CHECK(bus_dma_init(&ctx[0], BUS_TYPE_UART, false, cfg, 6) == ESP_OK, "UART0");
    make_uart_cfg(cfg, 20, 21, 115200);
    CHECK(bus_dma_init(&ctx[1], BUS_TYPE_UART, false, cfg, 6) == ESP_OK, "UART1");
    make_uart_cfg(cfg, 3, 4, 115200);
    CHECK(bus_dma_init(&ctx[2], BUS_TYPE_UART, false, cfg, 6) == ESP_OK, "UART2");

    /* Fourth should fail */
    uint8_t cfg4[6];
    make_uart_cfg(cfg4, 5, 6, 115200);
    bus_dma_ctx_t ctx4;
    esp_err_t r = bus_dma_init(&ctx4, BUS_TYPE_UART, false, cfg4, 6);
    CHECK(r == ESP_ERR_NO_MEM, "fourth UART should fail with NO_MEM");

    for (int i = 0; i < 3; i++) bus_dma_deinit(&ctx[i]);
}

/* --- UART0 boot init: NVS flag not set → available --- */
static void test_uart0_boot_available(void)
{
    reset_all_state();
    g_nvs_dl_flag = 0;
    bool avail = bus_dma_uart0_boot_init();
    CHECK(avail == true, "UART0 should be available when no download flag");
    CHECK(bus_dma_uart0_is_available() == true, "is_available should return true");
}

/* --- UART0 boot init: NVS flag set → download mode --- */
static void test_uart0_boot_download(void)
{
    reset_all_state();
    g_nvs_dl_flag = 1;
    bool avail = bus_dma_uart0_boot_init();
    CHECK(avail == false, "UART0 should be unavailable in download mode");
    CHECK(bus_dma_uart0_is_available() == false, "is_available should return false");
    CHECK(g_nvs_dl_flag == 0, "NVS flag should be cleared after reading");
    s_uart0_available = true;  /* restore for other tests */
}

/* --- SPI: basic init with preferred controller --- */
static void test_spi_preferred_controller(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[9];
    make_spi_cfg(cfg, 5, 0, 1000000, 23, 19, 18);  /* C6 SPI2 default pins */

    esp_err_t r = bus_dma_init_preferred(&ctx, BUS_TYPE_SPI, true, cfg, sizeof(cfg), 2);
    CHECK(r == ESP_OK, "SPI preferred init should succeed");
    CHECK(ctx.cfg.spi.host == SPI2_HOST, "should bind to preferred SPI2");
    CHECK(g_spi_initialized[SPI2_HOST], "SPI2 should be initialized");
    CHECK(g_spi_dma_mode[SPI2_HOST] == SPI_DMA_CH_AUTO, "DMA mode should be AUTO");

    bus_dma_deinit(&ctx);
    CHECK(!g_spi_initialized[SPI2_HOST], "SPI2 should be freed after deinit");
}

/* --- SPI: bus sharing — same pins, ref_count --- */
static void test_spi_bus_sharing(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx1, ctx2;
    uint8_t cfg[9];
    make_spi_cfg(cfg, 5, 0, 1000000, 23, 19, 18);

    esp_err_t r1 = bus_dma_init(&ctx1, BUS_TYPE_SPI, true, cfg, sizeof(cfg));
    CHECK(r1 == ESP_OK, "first SPI init should succeed");

    /* Second device on same bus, different CS */
    uint8_t cfg2[9];
    make_spi_cfg(cfg2, 6, 0, 1000000, 23, 19, 18);
    esp_err_t r2 = bus_dma_init(&ctx2, BUS_TYPE_SPI, true, cfg2, sizeof(cfg2));
    CHECK(r2 == ESP_OK, "second SPI device should share bus");
    CHECK(ctx2.cfg.spi.host == ctx1.cfg.spi.host, "should share same host");

    spi_bus_entry_t *entry = spi_find_bus(23, 19, 18, true);
    CHECK(entry != NULL, "bus entry should exist");
    CHECK(entry->ref_count == 2, "ref_count should be 2");

    bus_dma_deinit(&ctx1);
    entry = spi_find_bus(23, 19, 18, true);
    CHECK(entry != NULL && entry->ref_count == 1, "ref_count should be 1 after first deinit");
    CHECK(g_spi_initialized[SPI2_HOST], "bus should still be initialized");

    bus_dma_deinit(&ctx2);
    CHECK(!g_spi_initialized[SPI2_HOST], "bus should be freed after last deinit");
}

/* --- SPI: preferred controller conflict --- */
static void test_spi_lease_conflict(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx1, ctx2;
    uint8_t cfg[9];
    make_spi_cfg(cfg, 5, 0, 1000000, 23, 19, 18);

    bus_dma_init_preferred(&ctx1, BUS_TYPE_SPI, true, cfg, sizeof(cfg), 2);

    /* Same pins, different preferred host */
    esp_err_t r = bus_dma_init_preferred(&ctx2, BUS_TYPE_SPI, true, cfg, sizeof(cfg), 3);
    CHECK(r == ESP_ERR_INVALID_STATE, "conflicting SPI preferred should fail");

    bus_dma_deinit(&ctx1);
}

/* --- SPI: reserved pin rejection --- */
static void test_spi_reserved_pins(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[9];

    /* CS on LED pin (GPIO8) */
    make_spi_cfg(cfg, 8, 0, 1000000, 23, 19, 18);
    CHECK(bus_dma_init(&ctx, BUS_TYPE_SPI, true, cfg, sizeof(cfg)) == ESP_ERR_INVALID_ARG,
          "CS on LED pin should be rejected");

    /* MOSI on USB_D- (GPIO12) */
    make_spi_cfg(cfg, 5, 0, 1000000, 12, 19, 18);
    CHECK(bus_dma_init(&ctx, BUS_TYPE_SPI, true, cfg, sizeof(cfg)) == ESP_ERR_INVALID_ARG,
          "MOSI on USB_D- should be rejected");
}

/* --- I2C: basic init --- */
static void test_i2c_basic_init(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[7];
    make_i2c_cfg(cfg, 21, 22, 0x76, 400000);  /* C6 I2C0 default pins */

    esp_err_t r = bus_dma_init(&ctx, BUS_TYPE_I2C, false, cfg, sizeof(cfg));
    CHECK(r == ESP_OK, "I2C init should succeed");
    CHECK(ctx.cfg.i2c.port == I2C_NUM_0, "SDA21/SCL22 should derive to I2C0");
    CHECK(ctx.cfg.i2c.addr == 0x76, "addr should match");
    CHECK(ctx.cfg.i2c.dev_handle != NULL, "device handle should be created");

    bus_dma_deinit(&ctx);
}

/* --- I2C: bus sharing — same SDA/SCL, different address --- */
static void test_i2c_bus_sharing(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx1, ctx2;
    uint8_t cfg1[7], cfg2[7];
    make_i2c_cfg(cfg1, 21, 22, 0x76, 400000);
    make_i2c_cfg(cfg2, 21, 22, 0x77, 400000);

    bus_dma_init(&ctx1, BUS_TYPE_I2C, false, cfg1, sizeof(cfg1));
    bus_dma_init(&ctx2, BUS_TYPE_I2C, false, cfg2, sizeof(cfg2));

    CHECK(ctx2.cfg.i2c.bus_handle == ctx1.cfg.i2c.bus_handle,
          "same SDA/SCL should share bus handle");
    CHECK(ctx2.cfg.i2c.port == ctx1.cfg.i2c.port, "should share same port");

    i2c_bus_entry_t *entry = i2c_find_bus(21, 22);
    CHECK(entry != NULL && entry->ref_count == 2, "ref_count should be 2");

    bus_dma_deinit(&ctx1);
    entry = i2c_find_bus(21, 22);
    CHECK(entry != NULL && entry->ref_count == 1, "ref_count should be 1");

    bus_dma_deinit(&ctx2);
}

/* --- I2C: preferred controller conflict --- */
static void test_i2c_lease_conflict(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx1, ctx2;
    uint8_t cfg[7];
    make_i2c_cfg(cfg, 21, 22, 0x76, 400000);

    bus_dma_init_preferred(&ctx1, BUS_TYPE_I2C, false, cfg, sizeof(cfg), 0);

    /* Same pins, different preferred port */
    esp_err_t r = bus_dma_init_preferred(&ctx2, BUS_TYPE_I2C, false, cfg, sizeof(cfg), 1);
    CHECK(r == ESP_ERR_INVALID_STATE, "conflicting I2C preferred should fail");

    bus_dma_deinit(&ctx1);
}

/* --- I2C: SDA == SCL rejection --- */
static void test_i2c_same_pin_rejection(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[7];
    make_i2c_cfg(cfg, 21, 21, 0x76, 400000);

    CHECK(bus_dma_init(&ctx, BUS_TYPE_I2C, false, cfg, sizeof(cfg)) == ESP_ERR_INVALID_ARG,
          "SDA==SCL should be rejected");
}

/* --- I2C: reserved pin rejection --- */
static void test_i2c_reserved_pins(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[7];

    make_i2c_cfg(cfg, 12, 22, 0x76, 400000);  /* SDA on USB_D- */
    CHECK(bus_dma_init(&ctx, BUS_TYPE_I2C, false, cfg, sizeof(cfg)) == ESP_ERR_INVALID_ARG,
          "SDA on USB_D- should be rejected");
}

/* --- I2C: config too short --- */
static void test_i2c_config_too_short(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[5] = {21, 22, 0x76, 0, 0};

    CHECK(bus_dma_init(&ctx, BUS_TYPE_I2C, false, cfg, 5) == ESP_ERR_INVALID_SIZE,
          "I2C config < 7 bytes should be rejected");
}

/* --- bus_config_get_dma_enabled inline helper --- */
static void test_bus_config_get_dma_enabled(void)
{
    /* UART: flags at offset 6, bit 0 */
    uint8_t uart_cfg[7] = {16, 17, 0, 1, 0xC2, 0x00, 0x01};
    CHECK(bus_config_get_dma_enabled(BUS_TYPE_UART, uart_cfg, 7) == true,
          "UART flags=0x01 should be DMA enabled");

    uint8_t uart_cfg_no_dma[7] = {16, 17, 0, 1, 0xC2, 0x00, 0x00};
    CHECK(bus_config_get_dma_enabled(BUS_TYPE_UART, uart_cfg_no_dma, 7) == false,
          "UART flags=0x00 should be DMA disabled");

    /* Missing flags byte → default true */
    CHECK(bus_config_get_dma_enabled(BUS_TYPE_UART, uart_cfg, 6) == true,
          "UART missing flags byte should default to DMA enabled");

    /* I2C: flags at offset 7 */
    uint8_t i2c_cfg[8] = {21, 22, 0x76, 0, 6, 0x1A, 0x80, 0x00};
    CHECK(bus_config_get_dma_enabled(BUS_TYPE_I2C, i2c_cfg, 8) == false,
          "I2C flags=0x00 should be DMA disabled");

    /* SPI: flags at offset 6 */
    uint8_t spi_cfg[7] = {5, 0, 0, 0xF, 0x42, 0x40, 0x01};
    CHECK(bus_config_get_dma_enabled(BUS_TYPE_SPI, spi_cfg, 7) == true,
          "SPI flags=0x01 should be DMA enabled");

    /* NULL config → default true */
    CHECK(bus_config_get_dma_enabled(BUS_TYPE_UART, NULL, 0) == true,
          "NULL config should default to DMA enabled");

    /* Unknown bus type → default true */
    CHECK(bus_config_get_dma_enabled(99, uart_cfg, 7) == true,
          "unknown bus type should default to DMA enabled");
}

/* --- Public API guards --- */
static void test_api_guards(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t cfg[6];
    make_uart_cfg(cfg, 16, 17, 115200);
    uint8_t rx_buf[64];
    size_t rx_len;

    /* NULL ctx */
    CHECK(bus_dma_init(NULL, BUS_TYPE_UART, false, cfg, 6) == ESP_ERR_INVALID_ARG,
          "NULL ctx init should fail");
    CHECK(bus_dma_write(NULL, cfg, 6) == ESP_ERR_INVALID_ARG,
          "NULL ctx write should fail");
    CHECK(bus_dma_read(NULL, rx_buf, sizeof(rx_buf)) == 0,
          "NULL ctx read should return 0");
    CHECK(bus_dma_transact(NULL, cfg, 6, rx_buf, sizeof(rx_buf), &rx_len) == ESP_ERR_INVALID_ARG,
          "NULL ctx transact should fail");
    CHECK(bus_dma_deinit(NULL) == ESP_ERR_INVALID_ARG,
          "NULL ctx deinit should fail");

    /* NULL config */
    CHECK(bus_dma_init(&ctx, BUS_TYPE_UART, false, NULL, 6) == ESP_ERR_INVALID_ARG,
          "NULL config should fail");

    /* Uninitialized ctx */
    memset(&ctx, 0, sizeof(ctx));
    CHECK(bus_dma_write(&ctx, cfg, 6) == ESP_ERR_INVALID_ARG,
          "uninitialized write should fail");
    CHECK(bus_dma_read(&ctx, rx_buf, sizeof(rx_buf)) == 0,
          "uninitialized read should return 0");
    CHECK(bus_dma_deinit(&ctx) == ESP_OK,
          "uninitialized deinit should be no-op OK");

    /* Wrong bus type for write/read (SPI ctx) */
    uint8_t spi_cfg[9];
    make_spi_cfg(spi_cfg, 5, 0, 1000000, 23, 19, 18);
    bus_dma_init(&ctx, BUS_TYPE_SPI, true, spi_cfg, sizeof(spi_cfg));
    CHECK(bus_dma_write(&ctx, cfg, 6) == ESP_ERR_NOT_SUPPORTED,
          "write on SPI should fail");
    CHECK(bus_dma_read(&ctx, rx_buf, sizeof(rx_buf)) == 0,
          "read on SPI should return 0");
    CHECK(bus_dma_uart_event_queue(&ctx) == NULL,
          "event queue on SPI should be NULL");

    /* Transact on UART → not supported */
    bus_dma_deinit(&ctx);
    bus_dma_init(&ctx, BUS_TYPE_UART, false, cfg, sizeof(cfg));
    CHECK(bus_dma_transact(&ctx, cfg, 6, rx_buf, sizeof(rx_buf), &rx_len) == ESP_ERR_NOT_SUPPORTED,
          "transact on UART should fail");
    bus_dma_deinit(&ctx);

    /* Unknown bus type */
    CHECK(bus_dma_init(&ctx, 99, false, cfg, 6) == ESP_ERR_NOT_SUPPORTED,
          "unknown bus type should fail");

    /* controller_id < -1 */
    CHECK(bus_dma_init_preferred(&ctx, BUS_TYPE_UART, false, cfg, 6, -2) == ESP_ERR_INVALID_ARG,
          "controller_id < -1 should fail");
}

/* =====================================================================
 * SPI full-duplex length contract (S3 field defect, 2026-10-05)
 *
 * The S3 node 30EDA0A9A808 reported, every 5s, forever:
 *     E spi_master: check_trans_valid(1119): rx length > tx length in full duplex mode
 *     I CMD_SPI: Stats: txn=3 err=3 (0%) no_ctx=0
 * and both SPI/I2C edge devices stayed offline.  Root cause: spi_transact()
 * set .length = tx_len*8 and .rxlength = rx_size*8 unconditionally, so every
 * read-after-write whose response was longer than the request was rejected by
 * IDF before a single clock was emitted.
 *
 * These cases use the two production frame shapes from config_templates
 * 643 (8-byte Modbus RTU request, 9-byte response) and 644 (1-byte register
 * pointer, 6-byte response).  Both are legal protocol frames; only the
 * firmware's one-clock-train representation of them was wrong.
 * ===================================================================== */

/* --- 643: Modbus RTU read, 8-byte request, 9-byte response --- */
static void test_spi_transact_rx_longer_than_tx(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t spi_cfg[9];
    make_spi_cfg(spi_cfg, 5, 0, 1000000, 23, 19, 18);
    CHECK(bus_dma_init(&ctx, BUS_TYPE_SPI, true, spi_cfg, sizeof(spi_cfg)) == ESP_OK,
          "SPI init should succeed");

    const uint8_t tx[8] = { 0x01, 0x03, 0x00, 0x00, 0x00, 0x02, 0xC4, 0x0B };
    uint8_t rx[64];
    size_t rx_len = 0;
    memset(rx, 0, sizeof(rx));

    /* The slave answers with 9 bytes after consuming the 8-byte command. */
    const uint8_t resp[9] = { 0x01, 0x03, 0x04, 0x00, 0x64, 0x00, 0xC8, 0x00, 0x00 };
    spi_slave_set_miso(resp, sizeof(resp), sizeof(tx));

    esp_err_t e = bus_dma_transact(&ctx, tx, sizeof(tx), rx, 9, &rx_len);

    /* This is the defect: IDF rejected the frame, so the caller saw an error
     * and the channel counted err++ every sample. */
    CHECK(e == ESP_OK, "SPI 8->9 byte read-after-write must be accepted");
    CHECK(g_spi_reject_fd_calls == 0,
          "spi_transact must never issue rxlength > length in full duplex");
    CHECK(rx_len == 9, "SPI 8->9 must report all 9 response bytes");
    CHECK(memcmp(rx, resp, 9) == 0, "SPI 8->9 must return the slave response verbatim");

    /* One clock train, CS held low for the whole frame: exactly one transmit. */
    CHECK(g_spi_transmit_calls == 1,
          "SPI read-after-write must be a single transaction (CS stays low)");
    /* The single-CS frame must clock the command AND the response: 8 + 9. */
    CHECK(g_spi_last.length == (8 + 9) * 8,
          "read-after-write frame must clock tx_len + rx_size bytes");
    CHECK(g_spi_last.rxlength == (8 + 9) * 8,
          "read-after-write frame must capture the whole frame");
    /* MOSI pad after the command must be neutral, not stale stack bytes. */
    CHECK(memcmp(g_spi_last_tx, tx, 8) == 0, "command bytes must be shifted out first");
    for (int i = 8; i < 17; i++)
        CHECK(g_spi_last_tx[i] == 0x00, "MOSI pad byte must be 0x00");

    bus_dma_deinit(&ctx);
}

/* --- 644: I2C register-pointer idiom, 1-byte request, 6-byte response --- */
static void test_spi_transact_short_command_long_response(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t spi_cfg[9];
    make_spi_cfg(spi_cfg, 5, 0, 1000000, 23, 19, 18);
    CHECK(bus_dma_init(&ctx, BUS_TYPE_SPI, true, spi_cfg, sizeof(spi_cfg)) == ESP_OK,
          "SPI init should succeed");

    const uint8_t tx[1] = { 0xF7 };  /* BMP280-style register pointer */
    uint8_t rx[8];
    size_t rx_len = 0;
    memset(rx, 0, sizeof(rx));

    const uint8_t resp[6] = { 0x6C, 0x51, 0x80, 0x00, 0x1F, 0xA0 };
    spi_slave_set_miso(resp, sizeof(resp), sizeof(tx));

    esp_err_t e = bus_dma_transact(&ctx, tx, sizeof(tx), rx, 6, &rx_len);

    CHECK(e == ESP_OK, "SPI 1->6 byte register read must be accepted");
    CHECK(g_spi_reject_fd_calls == 0, "no full-duplex length violation for 1->6");
    CHECK(rx_len == 6, "SPI 1->6 must report all 6 response bytes");
    CHECK(memcmp(rx, resp, 6) == 0, "SPI 1->6 must return the slave response verbatim");
    CHECK(g_spi_transmit_calls == 1, "SPI 1->6 must stay one transaction");
    CHECK(g_spi_last.length == (1 + 6) * 8,
          "1->6 register read must clock the pointer plus the 6 response bytes");
    CHECK(g_spi_last_tx[0] == 0xF7, "register pointer must be shifted out first");
    for (int i = 1; i < 7; i++)
        CHECK(g_spi_last_tx[i] == 0x00, "pad bytes must all be 0x00");

    bus_dma_deinit(&ctx);
}

/* --- the legal shapes must stay legal and must not be widened --- */
static void test_spi_transact_symmetric_and_tx_only(void)
{
    reset_all_state();
    bus_dma_ctx_t ctx;
    uint8_t spi_cfg[9];
    make_spi_cfg(spi_cfg, 5, 0, 1000000, 23, 19, 18);
    CHECK(bus_dma_init(&ctx, BUS_TYPE_SPI, true, spi_cfg, sizeof(spi_cfg)) == ESP_OK,
          "SPI init should succeed");

    /* Symmetric 8->8: must NOT be widened and must use the caller's buffers. */
    const uint8_t tx[8] = { 0xAA, 0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07 };
    const uint8_t resp[8] = { 0x10, 0x20, 0x30, 0x40, 0x50, 0x60, 0x70, 0x80 };
    uint8_t rx[8];
    size_t rx_len = 0;
    spi_slave_set_miso(resp, sizeof(resp), sizeof(tx));

    CHECK(bus_dma_transact(&ctx, tx, 8, rx, 8, &rx_len) == ESP_OK,
          "symmetric SPI 8->8 must succeed");
    CHECK(rx_len == 8, "symmetric SPI must return 8 bytes");
    CHECK(memcmp(rx, resp, 8) == 0, "symmetric SPI must return the slave response");
    CHECK(g_spi_last.length == 16 * 8 && g_spi_last.rxlength == 16 * 8,
          "write-then-read frame must be tx_len + rx_size even when symmetric");
    CHECK(g_spi_last.tx_buffer != tx,
          "read-after-write must use the scratch frame, not the caller's TX buffer");

    /* TX-only: no RX requested at all, and the caller's buffer is used. */
    spi_slave_set_miso(NULL, 0, 0);
    CHECK(bus_dma_transact(&ctx, tx, 8, NULL, 0, &rx_len) == ESP_OK,
          "TX-only SPI write must succeed");
    CHECK(rx_len == 0, "TX-only SPI must report no RX bytes");
    CHECK(g_spi_last.rxlength == 0, "TX-only SPI must not request an RX phase");
    CHECK(g_spi_last.tx_buffer == tx, "TX-only SPI must use the caller's TX buffer");
    CHECK(g_spi_reject_fd_calls == 0, "no length violation in any legal shape");

    bus_dma_deinit(&ctx);
}

/* =====================================================================
 * Main
 * ===================================================================== */
int main(void)
{
    test_uart_init_hw_derive();
    test_uart_dma_buffer_sizes();
    test_uart_preferred_controller();
    test_uart_no_preferred_dynamic();
    test_uart_port_sharing();
    test_uart_lease_conflict();
    test_uart_port_occupied();
    test_uart_reserved_pins();
    test_uart_pin_out_of_range();
    test_uart_config_too_short();
    test_uart_event_queue_accessor();
    test_uart0_unavailable();
    test_uart_ports_exhausted();
    test_uart0_boot_available();
    test_uart0_boot_download();
    test_spi_preferred_controller();
    test_spi_bus_sharing();
    test_spi_lease_conflict();
    test_spi_reserved_pins();
    test_i2c_basic_init();
    test_i2c_bus_sharing();
    test_i2c_lease_conflict();
    test_i2c_same_pin_rejection();
    test_i2c_reserved_pins();
    test_i2c_config_too_short();
    test_bus_config_get_dma_enabled();
    test_api_guards();
    test_spi_transact_rx_longer_than_tx();
    test_spi_transact_short_command_long_response();
    test_spi_transact_symmetric_and_tx_only();

    if (g_failures > 0) {
        fprintf(stderr, "\nbus_dma_tests: %d FAILURES\n", g_failures);
        return 1;
    }
    puts("bus_dma_tests: all tests passed");
    return 0;
}
