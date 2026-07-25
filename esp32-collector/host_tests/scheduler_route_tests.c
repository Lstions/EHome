/*
 * Route and queue-metric tests for scheduler.c.
 *
 * This test includes scheduler.c directly to access the static functions
 * route_uart_port(), derive_uart_port(), queue_metric_index(),
 * and observe_queue_metrics().
 *
 * Coverage:
 *   1) route_uart_port returns leased port when uart_route callback is set
 *   2) route_uart_port falls back to derive_uart_port when no callback
 *   3) queue_metric_index returns correct index for U0/U1/U2/SPI/I2C
 *   4) observe_queue_metrics updates current_spaces and high_water_used
 */

#include <stdio.h>
#include <string.h>
#include <stdlib.h>

/* Host stubs */
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#include "esp_err.h"
#include "esp_log.h"
#include "driver/uart.h"
#include "driver/spi_master.h"
#include "driver/i2c_master.h"

/* Config + hw_tables (for config_manifest_t, config_template_t, hw_derive_uart_port) */
#include "config_mgr.h"
#include "hw_tables.h"

/* Provide ESP_LOG and esp_err_to_name used by scheduler.c */
void host_test_log_record(char level, const char *tag, const char *format, ...) {
    (void)level; (void)tag; (void)format;
}
const char *esp_err_to_name(esp_err_t err) {
    (void)err; return "ESP_OK";
}

/* ── Task stubs missing from stubs/freertos/task.h ──────────────────
 * scheduler.c calls xTaskCreatePinnedToCore (scheduler_prepare/resume),
 * eTaskGetState + eDeleted (scheduler_stop/pause), and vTaskDelayUntil
 * (scheduler_task).  We never invoke the task loop in this test, but
 * the compiler must resolve all symbols since we #include scheduler.c.
 */
#define eDeleted  0   /* eTaskState enum value for "deleted" */

static inline int xTaskCreatePinnedToCore(void (*task_fn)(void *),
                                          const char *name,
                                          uint32_t stack,
                                          void *param,
                                          UBaseType_t prio,
                                          TaskHandle_t *handle,
                                          BaseType_t core)
{
    (void)task_fn; (void)name; (void)stack; (void)param;
    (void)prio; (void)core;
    if (handle) *handle = (TaskHandle_t)1;
    return 1; /* pdPASS */
}

static inline UBaseType_t eTaskGetState(TaskHandle_t task) {
    (void)task;
    return eDeleted;  /* so scheduler_stop's wait-loop exits immediately */
}

static inline void vTaskDelayUntil(TickType_t *prev, TickType_t inc) {
    (void)prev; (void)inc;
}

/* ── config_mgr stubs ──────────────────────────────────────────────
 * scheduler.c calls config_mgr_get_manifest() (in scheduler_start) and
 * config_mgr_get_template() (in schedule_v1/v2_channel).  Neither path
 * is exercised by the route/metric tests, but the linker needs symbols.
 */
const config_manifest_t *config_mgr_get_manifest(void) { return NULL; }
const config_template_t *config_mgr_get_template(uint32_t id) {
    (void)id; return NULL;
}

/* Now include scheduler.c to access static functions */
#include "../components/scheduler/scheduler.c"

/* ── Test helpers ────────────────────────────────────────────────── */

static int failures = 0;
#define CHECK(cond, msg) do { \
    if (!(cond)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (msg)); \
        failures++; \
        return; \
    } \
} while (0)

/* Build a minimal UART channel config.
 * C6 UART0: TX=16 RX=17 → hw_derive_uart_port returns UART_NUM_0
 * C6 UART1: TX=20 RX=21 → hw_derive_uart_port returns UART_NUM_1
 */
static void make_uart_channel(config_channel_t *ch, uint32_t id,
                              int tx, int rx)
{
    memset(ch, 0, sizeof(*ch));
    ch->id = id;
    ch->bus_type = BUS_TYPE_UART;
    ch->enabled = true;
    ch->interval_ms = 5000;
    ch->bus_config[0] = (uint8_t)tx;
    ch->bus_config[1] = (uint8_t)rx;
    ch->bus_config_len = 2;
}

/* uart_route callback: returns a fixed leased port regardless of input */
static uart_port_t test_route_cb(void *ctx, uint32_t channel_id)
{
    (void)channel_id;
    /* ctx carries the port to return */
    return (uart_port_t)(intptr_t)ctx;
}

/* ── Reset scheduler state between tests ───────────────────────────
 * scheduler.c has file-static s_queues, s_queue_metrics, s_channels.
 * scheduler_init() zeroes s_channels, s_queue_metrics, and s_min_queue_spaces
 * but does NOT clear s_queues.  We call scheduler_init() then manually
 * reset s_queues to zero.
 */
static void reset_scheduler_state(void)
{
    scheduler_init();
    /* s_queues is file-static in scheduler.c; we can't touch it directly,
     * but we can use scheduler_prepare() semantics or just accept that
     * tests set s_queues via the public API.  For route/metric tests we
     * need to control s_queues — the only public setter is
     * scheduler_prepare(), which creates a task.  Instead, we access
     * s_queues indirectly: route_uart_port reads s_queues.uart_route,
     * and observe_queue_metrics reads s_queues.*_cmd_queue.
     *
     * Since these are file-static and we include the .c, we have direct
     * access.  But scheduler_init doesn't clear s_queues.  So we call
     * init, then manually zero s_queues here.
     */
    memset(&s_queues, 0, sizeof(s_queues));
    memset(&s_queue_metrics, 0, sizeof(s_queue_metrics));
}

/* ── Test cases ──────────────────────────────────────────────────── */

/*
 * 1) route_uart_port returns leased port when uart_route callback is set.
 *    Set s_queues.uart_route to a callback that returns UART_NUM_1,
 *    then verify route_uart_port() returns UART_NUM_1 even for a channel
 *    whose pins map to UART_NUM_0.
 */
static void test_route_uart_port_returns_leased(void)
{
    reset_scheduler_state();

    config_channel_t ch;
    /* UART0 pins (TX=16, RX=17) → derive would give UART_NUM_0 */
    make_uart_channel(&ch, 42, 16, 17);

    /* Install a route callback that leases UART_NUM_1 */
    s_queues.uart_route = test_route_cb;
    s_queues.route_ctx  = (void *)(intptr_t)UART_NUM_1;

    uart_port_t port = route_uart_port(&ch);
    CHECK(port == UART_NUM_1, "route should return leased UART_NUM_1");
}

/*
 * 2) route_uart_port falls back to derive_uart_port when no callback.
 *    With uart_route == NULL, route_uart_port() must call derive_uart_port()
 *    which uses hw_derive_uart_port() on the pin pair.
 *    C6 UART0 pins (16,17) → UART_NUM_0
 *    C6 UART1 pins (20,21) → UART_NUM_1
 */
static void test_route_uart_port_falls_back_to_derive(void)
{
    reset_scheduler_state();

    /* No uart_route callback set (NULL) */

    config_channel_t ch0;
    make_uart_channel(&ch0, 1, 16, 17);  /* C6 UART0 pins */
    uart_port_t port0 = route_uart_port(&ch0);
    CHECK(port0 == UART_NUM_0, "derive should map (16,17)→UART0");

    config_channel_t ch1;
    make_uart_channel(&ch1, 2, 20, 21);  /* C6 UART1 pins */
    uart_port_t port1 = route_uart_port(&ch1);
    CHECK(port1 == UART_NUM_1, "derive should map (20,21)→UART1");
}

/*
 * 2b) route_uart_port with NULL channel should not crash (derives from NULL
 *     would segfault, but route_uart_port checks ch first).  Actually
 *     route_uart_port does `if (ch && s_queues.uart_route)` — if ch is NULL
 *     it skips to derive_uart_port(ch) which would deref ch.  So NULL ch
 *     is UB.  Skip this case.
 */

/*
 * 2c) route_uart_port falls back when callback returns invalid port
 *     (e.g., UART_NUM_MAX).  The guard `leased >= UART_NUM_0 && leased < UART_NUM_MAX`
 *     should reject it and fall through to derive_uart_port.
 */
static void test_route_uart_port_rejects_invalid_lease(void)
{
    reset_scheduler_state();

    config_channel_t ch;
    make_uart_channel(&ch, 5, 16, 17);  /* pins → UART_NUM_0 */

    /* Callback returns UART_NUM_MAX (invalid) */
    s_queues.uart_route = test_route_cb;
    s_queues.route_ctx  = (void *)(intptr_t)UART_NUM_MAX;

    uart_port_t port = route_uart_port(&ch);
    CHECK(port == UART_NUM_0, "invalid lease should fall back to derive→UART0");
}

/*
 * 3) queue_metric_index returns correct index for U0/U1/U2/SPI/I2C.
 *    SCHED_Q_UART0=0, SCHED_Q_UART1=1, SCHED_Q_UART2=2, SCHED_Q_SPI=3, SCHED_Q_I2C=4
 */
static void test_queue_metric_index_uart0(void)
{
    bus_cmd_t cmd;
    memset(&cmd, 0, sizeof(cmd));
    cmd.bus_type  = BUS_TYPE_UART;
    cmd.uart_port = UART_NUM_0;

    int idx = queue_metric_index(&cmd);
    CHECK(idx == 0, "UART0 → index 0");
}

static void test_queue_metric_index_uart1(void)
{
    bus_cmd_t cmd;
    memset(&cmd, 0, sizeof(cmd));
    cmd.bus_type  = BUS_TYPE_UART;
    cmd.uart_port = UART_NUM_1;

    int idx = queue_metric_index(&cmd);
    CHECK(idx == 1, "UART1 → index 1");
}

static void test_queue_metric_index_uart2(void)
{
    bus_cmd_t cmd;
    memset(&cmd, 0, sizeof(cmd));
    cmd.bus_type  = BUS_TYPE_UART;
    cmd.uart_port = UART_NUM_2;

    int idx = queue_metric_index(&cmd);
    CHECK(idx == 2, "UART2 → index 2");
}

static void test_queue_metric_index_spi(void)
{
    bus_cmd_t cmd;
    memset(&cmd, 0, sizeof(cmd));
    cmd.bus_type = BUS_TYPE_SPI;

    int idx = queue_metric_index(&cmd);
    CHECK(idx == 3, "SPI → index 3");
}

static void test_queue_metric_index_i2c(void)
{
    bus_cmd_t cmd;
    memset(&cmd, 0, sizeof(cmd));
    cmd.bus_type = BUS_TYPE_I2C;

    int idx = queue_metric_index(&cmd);
    CHECK(idx == 4, "I2C → index 4");
}

static void test_queue_metric_index_null_cmd(void)
{
    int idx = queue_metric_index(NULL);
    CHECK(idx == -1, "NULL cmd → -1");
}

static void test_queue_metric_index_unknown_uart_port(void)
{
    bus_cmd_t cmd;
    memset(&cmd, 0, sizeof(cmd));
    cmd.bus_type  = BUS_TYPE_UART;
    cmd.uart_port = 99;  /* invalid port */

    int idx = queue_metric_index(&cmd);
    CHECK(idx == -1, "unknown UART port → -1");
}

static void test_queue_metric_index_unknown_bus_type(void)
{
    bus_cmd_t cmd;
    memset(&cmd, 0, sizeof(cmd));
    cmd.bus_type = 0;  /* invalid */

    int idx = queue_metric_index(&cmd);
    CHECK(idx == -1, "unknown bus type → -1");
}

/*
 * 4) observe_queue_metrics updates current_spaces and high_water_used.
 *
 * Create real queues (xQueueCreate from stub), install them into s_queues,
 * fill some items, call observe_queue_metrics(), then verify:
 *   - current_spaces[i] matches uxQueueSpacesAvailable(queue)
 *   - high_water_used[i] reflects capacity - spaces (monotonically increasing)
 */
static void test_observe_queue_metrics_updates_spaces_and_high_water(void)
{
    reset_scheduler_state();

    /* Create 5 queues with known capacities.
     * queue_metric_capacity(): SPI/I2C → 8, UART0/1/2 → 16.
     * CMD_QUEUE_DEPTH = 16 (used by queue_spaces_or_depth for NULL queues).
     * We create real queues matching those capacities. */
    QueueHandle_t q_uart0 = xQueueCreate(16, sizeof(bus_cmd_t));
    QueueHandle_t q_uart1 = xQueueCreate(16, sizeof(bus_cmd_t));
    QueueHandle_t q_uart2 = xQueueCreate(16, sizeof(bus_cmd_t));
    QueueHandle_t q_spi   = xQueueCreate(8,  sizeof(bus_cmd_t));
    QueueHandle_t q_i2c   = xQueueCreate(8,  sizeof(bus_cmd_t));

    CHECK(q_uart0 != NULL, "xQueueCreate uart0 failed");
    CHECK(q_uart1 != NULL, "xQueueCreate uart1 failed");
    CHECK(q_uart2 != NULL, "xQueueCreate uart2 failed");
    CHECK(q_spi   != NULL, "xQueueCreate spi failed");
    CHECK(q_i2c   != NULL, "xQueueCreate i2c failed");

    s_queues.uart0_cmd_queue = q_uart0;
    s_queues.uart1_cmd_queue = q_uart1;
    s_queues.uart2_cmd_queue = q_uart2;
    s_queues.spi_cmd_queue   = q_spi;
    s_queues.i2c_cmd_queue   = q_i2c;

    /* Fill 4 items into uart0 (capacity 16 → spaces=12, used=4)
     * Fill 2 items into spi   (capacity 8  → spaces=6,  used=2) */
    bus_cmd_t dummy;
    memset(&dummy, 0, sizeof(dummy));
    for (int i = 0; i < 4; i++) xQueueSend(q_uart0, &dummy, 0);
    for (int i = 0; i < 2; i++) xQueueSend(q_spi,   &dummy, 0);

    /* First call: should set current_spaces and high_water_used */
    observe_queue_metrics();

    /* Verify current_spaces */
    CHECK(s_queue_metrics.current_spaces[0] == 12, "uart0 spaces should be 12");
    CHECK(s_queue_metrics.current_spaces[1] == 16, "uart1 spaces should be 16 (empty)");
    CHECK(s_queue_metrics.current_spaces[2] == 16, "uart2 spaces should be 16 (empty)");
    CHECK(s_queue_metrics.current_spaces[3] == 6,  "spi spaces should be 6");
    CHECK(s_queue_metrics.current_spaces[4] == 8,  "i2c spaces should be 8 (empty)");

    /* Verify high_water_used (capacity - spaces) */
    CHECK(s_queue_metrics.high_water_used[0] == 4, "uart0 high_water should be 4");
    CHECK(s_queue_metrics.high_water_used[1] == 0, "uart1 high_water should be 0");
    CHECK(s_queue_metrics.high_water_used[2] == 0, "uart2 high_water should be 0");
    CHECK(s_queue_metrics.high_water_used[3] == 2, "spi high_water should be 2");
    CHECK(s_queue_metrics.high_water_used[4] == 0, "i2c high_water should be 0");

    /* Add more items to uart0 (now 6 total → spaces=10, used=6)
     * high_water should increase to 6, not stay at 4 */
    for (int i = 0; i < 2; i++) xQueueSend(q_uart0, &dummy, 0);
    observe_queue_metrics();

    CHECK(s_queue_metrics.current_spaces[0] == 10, "uart0 spaces should be 10 after 6 items");
    CHECK(s_queue_metrics.high_water_used[0] == 6, "uart0 high_water should increase to 6");

    /* Drain some items from uart0 (back to 4 items → spaces=12, used=4)
     * high_water should remain at 6 (monotonic — only increases) */
    bus_cmd_t recv;
    xQueueReceive(q_uart0, &recv, 0);
    xQueueReceive(q_uart0, &recv, 0);
    observe_queue_metrics();

    CHECK(s_queue_metrics.current_spaces[0] == 12, "uart0 spaces should return to 12");
    CHECK(s_queue_metrics.high_water_used[0] == 6, "uart0 high_water should remain 6 (monotonic)");

    /* Cleanup */
    vQueueDelete(q_uart0);
    vQueueDelete(q_uart1);
    vQueueDelete(q_uart2);
    vQueueDelete(q_spi);
    vQueueDelete(q_i2c);
}

/*
 * 4b) observe_queue_metrics with NULL queues: queue_spaces_or_depth returns
 *     CMD_QUEUE_DEPTH (16) for NULL queues, so used = capacity - 16.
 *     For UART (cap=16): used = 16-16 = 0.
 *     For SPI/I2C (cap=8): spaces=16 > capacity=8, so used = 0 (clamped).
 */
static void test_observe_queue_metrics_null_queues(void)
{
    reset_scheduler_state();
    /* All queues NULL */

    observe_queue_metrics();

    /* NULL queue → queue_spaces_or_depth returns CMD_QUEUE_DEPTH=16.
     * capacity for UART = 16, so used = 16-16 = 0.
     * capacity for SPI/I2C = 8, spaces=16 > 8, so used = 0 (clamped). */
    CHECK(s_queue_metrics.current_spaces[0] == 16, "NULL uart0 → spaces=CMD_QUEUE_DEPTH");
    CHECK(s_queue_metrics.current_spaces[3] == 16, "NULL spi → spaces=CMD_QUEUE_DEPTH");
    CHECK(s_queue_metrics.high_water_used[0] == 0, "NULL uart0 → used=0");
    CHECK(s_queue_metrics.high_water_used[3] == 0, "NULL spi → used=0 (clamped)");
}

/* ── Main ─────────────────────────────────────────────────────────── */

int main(void)
{
    test_route_uart_port_returns_leased();
    test_route_uart_port_falls_back_to_derive();
    test_route_uart_port_rejects_invalid_lease();

    test_queue_metric_index_uart0();
    test_queue_metric_index_uart1();
    test_queue_metric_index_uart2();
    test_queue_metric_index_spi();
    test_queue_metric_index_i2c();
    test_queue_metric_index_null_cmd();
    test_queue_metric_index_unknown_uart_port();
    test_queue_metric_index_unknown_bus_type();

    test_observe_queue_metrics_updates_spaces_and_high_water();
    test_observe_queue_metrics_null_queues();

    if (failures != 0) {
        fprintf(stderr, "%d test(s) failed\n", failures);
        return 1;
    }
    puts("scheduler_route_tests: all tests passed");
    return 0;
}
