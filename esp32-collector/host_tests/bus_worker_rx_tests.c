/*
 * bus_worker_rx_tests.c
 *
 * Host tests for bus_worker.c's event-driven RX state machine.
 * Complements bus_worker_report_tests.c (report path) by covering:
 *
 *   1. rx_append_from_event — stream accumulation, overflow flush
 *   2. handle_uart_event — event dispatch (DATA, BREAK, FIFO_OVF, batch)
 *   3. expire_uart_state — RX timeout expiration
 *   4. rx_wait_ticks — next wake deadline calculation
 *   5. uart_slot_from_event_queue — event queue → channel index mapping
 *   6. decode_batch_step — batch step frame decoding
 *
 * Build:
 *   gcc -std=c11 -Wall -Wextra -Werror -Wno-unused-function \
 *       -DCONFIG_IDF_TARGET_ESP32C6=1 \
 *       -I stubs -I stubs/rom \
 *       -I../components/bus_worker/include \
 *       -I../components/bus_dma/include \
 *       -I../components/bus_manager/include \
 *       -I../components/scheduler \
 *       -I../components/config_mgr \
 *       -I../components/dma_pool/include \
 *       -I../components/hw_profile/include \
 *       -I../components/frame \
 *       -I../components/msg_handler \
 *       -o /tmp/test_bw_rx \
 *       bus_worker_rx_tests.c ../components/frame/frame_codec.c
 */

#include <stdio.h>
#include <string.h>
#include <stdlib.h>

#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"
#include "freertos/queue.h"
#include "freertos/event_groups.h"
#include "freertos/task.h"
#include "esp_err.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "esp_task_wdt.h"
#include "rom/ets_sys.h"
#include "driver/uart.h"
#include "driver/spi_master.h"
#include "driver/i2c_master.h"

#include "config_mgr.h"
#include "bus_dma.h"
#include "cmd_queue.h"
#include "scheduler.h"
#include "bus_worker.h"
#include "bus_queue_policy.h"
#include "bus_rx_boundary.h"
#include "frame_codec.h"

/* ---- Controllable time ---- */
int64_t g_test_time_us = 0;

/* ---- ESP stubs ---- */
void host_test_log_record(char level, const char *tag, const char *format, ...) {
    (void)level; (void)tag; (void)format;
}
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_OK"; }
void esp_restart(void) { /* no-op in tests */ }

/* ---- scheduler stubs ---- */
void scheduler_notify_channel_error(uint32_t channel_id) { (void)channel_id; }
void scheduler_notify_channel_success(uint32_t channel_id) { (void)channel_id; }

/* ---- Controllable bus_dma_read stub ---- */
#define FAKE_RX_BUF_SIZE 2048
static uint8_t g_fake_rx_data[FAKE_RX_BUF_SIZE];
static size_t g_fake_rx_len = 0;
static size_t g_fake_rx_pos = 0;

esp_err_t bus_dma_write(bus_dma_ctx_t *ctx, const uint8_t *data, size_t len) {
    (void)ctx; (void)data; (void)len; return ESP_OK;
}
size_t bus_dma_read(bus_dma_ctx_t *ctx, uint8_t *buf, size_t buf_size) {
    (void)ctx;
    if (g_fake_rx_pos >= g_fake_rx_len) return 0;
    size_t avail = g_fake_rx_len - g_fake_rx_pos;
    size_t n = avail < buf_size ? avail : buf_size;
    memcpy(buf, g_fake_rx_data + g_fake_rx_pos, n);
    g_fake_rx_pos += n;
    return n;
}
esp_err_t bus_dma_transact(bus_dma_ctx_t *ctx, const uint8_t *tx, size_t tx_len,
                           uint8_t *rx, size_t rx_size, size_t *rx_len) {
    (void)ctx; (void)tx; (void)tx_len; (void)rx; (void)rx_size;
    if (rx_len) *rx_len = 0;
    return ESP_OK;
}
QueueHandle_t bus_dma_uart_event_queue(const bus_dma_ctx_t *ctx) {
    if (!ctx || !ctx->initialized || ctx->bus_type != BUS_TYPE_UART) return NULL;
    return ctx->uart_event_queue;
}

/* ---- semaphore stubs ---- */
SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (SemaphoreHandle_t)1; }
int xSemaphoreTake(SemaphoreHandle_t sem, uint32_t ticks) { (void)sem; (void)ticks; return 1; }
int xSemaphoreGive(SemaphoreHandle_t sem) { (void)sem; return 1; }

/* ---- Include bus_worker.c directly to access static functions ---- */
#include "../components/bus_worker/bus_worker.c"

/* =====================================================================
 * Test infrastructure
 * ===================================================================== */
static int g_failures = 0;
static uint32_t g_data_rpt_count = 0;
static uint32_t g_control_final_count = 0;
static uint32_t g_last_rpt_channel_id = 0;
static uint32_t g_last_rpt_error_code = 0;
static uint32_t g_last_rpt_request_id = 0;
static size_t g_last_rpt_len = 0;
static uint8_t g_last_final_slot = 0;
static bool g_last_final_success = false;

#define CHECK(cond, msg) do { \
    if (!(cond)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (msg)); \
        g_failures++; \
    } \
} while (0)

static void test_data_rpt_cb(uint32_t ch, uint64_t ts, uint32_t seq,
                             const uint8_t *data, size_t len,
                             uint32_t ec, uint32_t rid,
                             uint32_t eid, uint32_t tid, uint8_t ci) {
    (void)ts; (void)seq; (void)data; (void)eid; (void)tid; (void)ci;
    g_data_rpt_count++;
    g_last_rpt_channel_id = ch;
    g_last_rpt_error_code = ec;
    g_last_rpt_request_id = rid;
    g_last_rpt_len = len;
}

static void test_control_final_cb(uint8_t slot, bool success, uint32_t code,
                                  const uint8_t *raw, size_t raw_len) {
    (void)code; (void)raw; (void)raw_len;
    g_control_final_count++;
    g_last_final_slot = slot;
    g_last_final_success = success;
}

static void reset_counters(void) {
    g_data_rpt_count = 0;
    g_control_final_count = 0;
    g_last_rpt_channel_id = 0;
    g_last_rpt_error_code = 0;
    g_last_rpt_request_id = 0;
    g_last_rpt_len = 0;
    g_last_final_slot = 0;
    g_last_final_success = false;
    g_test_time_us = 0;
    g_fake_rx_len = 0;
    g_fake_rx_pos = 0;
}

/* Drain all report queues so allocations are fresh */
static void drain_report_queues(void) {
    report_desc_t desc;
    control_final_desc_t final;
    write_rsp_desc_t wrsp;
    while (s_report_critical_q && xQueueReceive(s_report_critical_q, &desc, 0) == pdTRUE) {
        if (desc.emergency) report_free_emergency_block(desc.block_index);
        else report_free_block(desc.critical, desc.block_index);
    }
    while (s_report_critical_emergency_q && xQueueReceive(s_report_critical_emergency_q, &desc, 0) == pdTRUE) {
        report_free_emergency_block(desc.block_index);
    }
    while (s_report_telemetry_q && xQueueReceive(s_report_telemetry_q, &desc, 0) == pdTRUE) {
        report_free_block(desc.critical, desc.block_index);
    }
    while (s_control_final_q && xQueueReceive(s_control_final_q, &final, 0) == pdTRUE) { }
    while (s_write_rsp_q && xQueueReceive(s_write_rsp_q, &wrsp, 0) == pdTRUE) { }
}

/* Build a minimal bus_runtime_t with one UART channel at index 0 */
static bus_dma_ctx_t g_test_bus_ctx[SCHED_MAX_CHANNELS];
static uint32_t g_test_bus_ch[SCHED_MAX_CHANNELS];
static QueueHandle_t g_test_pending_q[SCHED_MAX_CHANNELS];
static QueueHandle_t g_test_event_queues[SCHED_MAX_CHANNELS];

static bus_runtime_t g_test_rt;

static void setup_test_runtime(void) {
    memset(g_test_bus_ctx, 0, sizeof(g_test_bus_ctx));
    memset(g_test_bus_ch, 0, sizeof(g_test_bus_ch));
    memset(&g_test_rt, 0, sizeof(g_test_rt));

    /* Channel 0: UART, initialized, with event queue */
    g_test_bus_ctx[0].initialized = true;
    g_test_bus_ctx[0].bus_type = BUS_TYPE_UART;
    g_test_bus_ctx[0].cfg.uart.port = UART_NUM_0;
    g_test_event_queues[0] = (QueueHandle_t)&g_test_event_queues[0]; /* distinct address */
    g_test_bus_ctx[0].uart_event_queue = g_test_event_queues[0];
    g_test_bus_ch[0] = 100;  /* channel_id = 100 */

    /* Pending queue for channel 0 */
    g_test_pending_q[0] = xQueueCreate(PENDING_QUEUE_DEPTH, sizeof(pending_cmd_t));

    g_test_rt.bus_ctx = g_test_bus_ctx;
    g_test_rt.bus_ch = g_test_bus_ch;
    g_test_rt.pending_queues = g_test_pending_q;
    g_test_rt.find_ctx = NULL;

    /* Reset stream state */
    memset(s_streams, 0, sizeof(s_streams));
    memset(s_last_rx_us, 0, sizeof(s_last_rx_us));
    memset(s_rx_sequence, 0, sizeof(s_rx_sequence));
    memset(s_stream_chunked, 0, sizeof(s_stream_chunked));
    memset(s_rx_overflow_count, 0, sizeof(s_rx_overflow_count));
    memset(s_rx_error_count, 0, sizeof(s_rx_error_count));
    memset(s_rx_timeout_count, 0, sizeof(s_rx_timeout_count));
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) s_plan_active[i] = false;
    memset(s_batch_rx, 0, sizeof(s_batch_rx));
}

static void teardown_test_runtime(void) {
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (g_test_pending_q[i]) { vQueueDelete(g_test_pending_q[i]); g_test_pending_q[i] = NULL; }
    }
}

/* =====================================================================
 * Test 1: uart_slot_from_event_queue
 * ===================================================================== */
static void test_uart_slot_from_event_queue(void) {
    setup_test_runtime();

    /* Valid: event queue of channel 0 → slot 0 */
    CHECK(uart_slot_from_event_queue(&g_test_rt, g_test_event_queues[0]) == 0,
          "event queue of ch0 should map to slot 0");

    /* NULL member → -1 */
    CHECK(uart_slot_from_event_queue(&g_test_rt, NULL) == -1,
          "NULL member should return -1");

    /* NULL rt → -1 */
    CHECK(uart_slot_from_event_queue(NULL, g_test_event_queues[0]) == -1,
          "NULL rt should return -1");

    /* Unknown member → -1 */
    int dummy;
    CHECK(uart_slot_from_event_queue(&g_test_rt, (QueueSetMemberHandle_t)&dummy) == -1,
          "unknown member should return -1");

    /* Non-UART channel should not match */
    g_test_bus_ctx[0].bus_type = BUS_TYPE_SPI;
    CHECK(uart_slot_from_event_queue(&g_test_rt, g_test_event_queues[0]) == -1,
          "SPI channel should not match UART event queue");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 2: rx_append_from_event — basic accumulation
 * ===================================================================== */
static void test_rx_append_basic(void) {
    reset_counters();
    setup_test_runtime();
    drain_report_queues();
    bus_worker_set_callbacks(NULL, test_data_rpt_cb);

    /* Feed 10 bytes (< 512 block size, no pending cmd → no emission) */
    uint8_t data[10] = {1,2,3,4,5,6,7,8,9,10};
    memcpy(g_fake_rx_data, data, 10);
    g_fake_rx_len = 10;
    g_fake_rx_pos = 0;
    g_test_time_us = 1000;

    uint8_t rx[256];
    rx_append_from_event(&g_test_rt, 0, rx, sizeof(rx));

    CHECK(s_streams[0].len == 10, "stream should accumulate 10 bytes");
    CHECK(s_last_rx_us[0] == 1000, "last_rx_us should be updated");
    CHECK(g_data_rpt_count == 0, "no emission below block boundary");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 3: rx_append_from_event — 512B block emission
 * ===================================================================== */
static void test_rx_append_block_emission(void) {
    reset_counters();
    setup_test_runtime();
    drain_report_queues();

    /* Feed exactly 512 bytes → one full block emitted */
    for (int i = 0; i < 512; i++) g_fake_rx_data[i] = (uint8_t)(i & 0xFF);
    g_fake_rx_len = 512;
    g_fake_rx_pos = 0;
    g_test_time_us = 2000;

    uint8_t rx[256];
    rx_append_from_event(&g_test_rt, 0, rx, sizeof(rx));

    /* report_task not running — check telemetry queue directly */
    report_desc_t desc;
    bool got = (s_report_telemetry_q &&
                xQueueReceive(s_report_telemetry_q, &desc, 0) == pdTRUE);
    CHECK(got, "one 512B block should be in telemetry queue");
    if (got) {
        CHECK(desc.len == 512, "emitted block should be 512 bytes");
        CHECK(desc.channel_id == 100, "channel_id should be 100");
        CHECK(desc.critical == false, "telemetry block should not be critical");
        report_free_block(false, desc.block_index);
    }
    CHECK(s_streams[0].len == 0, "stream should be empty after emission");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 4: rx_append_from_event — overflow handling
 * ===================================================================== */
static void test_rx_append_overflow(void) {
    reset_counters();
    setup_test_runtime();
    drain_report_queues();
    bus_worker_set_callbacks(NULL, test_data_rpt_cb);

    /* Pre-fill stream to near capacity */
    s_streams[0].len = STREAM_RX_BUF_SIZE - 10;
    memset(s_streams[0].buffer, 0xAA, s_streams[0].len);

    /* Feed 100 bytes → overflow (len + 100 > 1024) */
    for (int i = 0; i < 100; i++) g_fake_rx_data[i] = (uint8_t)i;
    g_fake_rx_len = 100;
    g_fake_rx_pos = 0;
    g_test_time_us = 3000;

    uint8_t rx[256];
    rx_append_from_event(&g_test_rt, 0, rx, sizeof(rx));

    CHECK(s_rx_overflow_count[0] >= 1, "overflow counter should increment");
    /* After overflow flush + reset, stream should have the new data */
    CHECK(s_streams[0].len <= STREAM_RX_BUF_SIZE, "stream must not exceed capacity");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 5: handle_uart_event — UART_DATA dispatches to rx_append
 * ===================================================================== */
static void test_handle_uart_data_event(void) {
    reset_counters();
    setup_test_runtime();
    drain_report_queues();
    bus_worker_set_callbacks(NULL, test_data_rpt_cb);

    uint8_t data[5] = {0xDE, 0xAD, 0xBE, 0xEF, 0x42};
    memcpy(g_fake_rx_data, data, 5);
    g_fake_rx_len = 5;
    g_fake_rx_pos = 0;
    g_test_time_us = 4000;

    uart_event_t event = { .type = UART_DATA, .size = 5 };
    uint8_t rx[256];
    handle_uart_event(&g_test_rt, 0, &event, rx, sizeof(rx));

    CHECK(s_streams[0].len == 5, "DATA event should accumulate bytes");
    CHECK(s_last_rx_us[0] == 4000, "last_rx_us should be set");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 6: handle_uart_event — FIFO_OVF resets stream
 * ===================================================================== */
static void test_handle_fifo_ovf_event(void) {
    reset_counters();
    setup_test_runtime();
    drain_report_queues();

    /* Pre-fill stream */
    s_streams[0].len = 100;
    s_last_rx_us[0] = 5000;

    uart_event_t event = { .type = UART_FIFO_OVF, .size = 0 };
    uint8_t rx[256];
    handle_uart_event(&g_test_rt, 0, &event, rx, sizeof(rx));

    CHECK(s_streams[0].len == 0, "FIFO_OVF should reset stream length");
    CHECK(s_last_rx_us[0] == 0, "FIFO_OVF should reset last_rx_us");
    CHECK(s_rx_error_count[0] == 1, "FIFO_OVF should increment error count");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 7: handle_uart_event — BREAK retains buffered data
 * ===================================================================== */
static void test_handle_break_event(void) {
    reset_counters();
    setup_test_runtime();
    drain_report_queues();
    bus_worker_set_callbacks(NULL, test_data_rpt_cb);

    /* Pre-fill stream with 10 bytes */
    s_streams[0].len = 10;
    memset(s_streams[0].buffer, 0xBB, 10);
    s_last_rx_us[0] = 6000;

    /* BREAK event with some new bytes available */
    uint8_t data[3] = {0x11, 0x22, 0x33};
    memcpy(g_fake_rx_data, data, 3);
    g_fake_rx_len = 3;
    g_fake_rx_pos = 0;
    g_test_time_us = 6100;

    uart_event_t event = { .type = UART_BREAK, .size = 0 };
    uint8_t rx[256];
    handle_uart_event(&g_test_rt, 0, &event, rx, sizeof(rx));

    CHECK(s_rx_error_count[0] == 1, "BREAK should increment error count");
    CHECK(s_streams[0].len == 13, "BREAK should retain buffered data + new bytes");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 8: handle_uart_event — batch mode (plan_active)
 * ===================================================================== */
static void test_handle_batch_mode(void) {
    reset_counters();
    setup_test_runtime();
    drain_report_queues();

    /* Activate batch mode for channel 0 */
    s_plan_active[0] = true;
    memset(&s_batch_rx[0], 0, sizeof(s_batch_rx[0]));

    uint8_t data[8] = {1,2,3,4,5,6,7,8};
    memcpy(g_fake_rx_data, data, 8);
    g_fake_rx_len = 8;
    g_fake_rx_pos = 0;
    g_test_time_us = 7000;

    uart_event_t event = { .type = UART_DATA, .size = 8 };
    uint8_t rx[256];
    handle_uart_event(&g_test_rt, 0, &event, rx, sizeof(rx));

    CHECK(s_batch_rx[0].len == 8, "batch mode should accumulate into batch buffer");
    CHECK(s_batch_rx[0].last_rx_us == 7000, "batch last_rx_us should be set");
    CHECK(s_streams[0].len == 0, "stream should NOT be used in batch mode");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 9: handle_uart_event — batch overflow
 * ===================================================================== */
static void test_handle_batch_overflow(void) {
    reset_counters();
    setup_test_runtime();
    drain_report_queues();

    s_plan_active[0] = true;
    memset(&s_batch_rx[0], 0, sizeof(s_batch_rx[0]));
    /* Pre-fill batch buffer near capacity */
    s_batch_rx[0].len = CONTROL_FINAL_RAW_MAX - 2;

    /* Feed 10 bytes → overflow */
    for (int i = 0; i < 10; i++) g_fake_rx_data[i] = (uint8_t)i;
    g_fake_rx_len = 10;
    g_fake_rx_pos = 0;

    uart_event_t event = { .type = UART_DATA, .size = 10 };
    uint8_t rx[256];
    handle_uart_event(&g_test_rt, 0, &event, rx, sizeof(rx));

    CHECK(s_batch_rx[0].error == true, "batch overflow should set error flag");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 10: handle_uart_event — NULL/invalid guards
 * ===================================================================== */
static void test_handle_event_guards(void) {
    setup_test_runtime();

    uart_event_t event = { .type = UART_DATA, .size = 0 };
    uint8_t rx[256];

    /* NULL rt — should not crash */
    handle_uart_event(NULL, 0, &event, rx, sizeof(rx));
    /* Invalid index */
    handle_uart_event(&g_test_rt, -1, &event, rx, sizeof(rx));
    handle_uart_event(&g_test_rt, SCHED_MAX_CHANNELS, &event, rx, sizeof(rx));
    /* NULL event */
    handle_uart_event(&g_test_rt, 0, NULL, rx, sizeof(rx));
    /* NULL rx buffer */
    handle_uart_event(&g_test_rt, 0, &event, NULL, sizeof(rx));
    /* Zero rx_cap */
    handle_uart_event(&g_test_rt, 0, &event, rx, 0);

    CHECK(1, "guard tests should not crash");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 11: expire_uart_state — RX timeout
 * ===================================================================== */
static void test_expire_rx_timeout(void) {
    reset_counters();
    setup_test_runtime();
    drain_report_queues();
    bus_worker_set_callbacks(NULL, test_data_rpt_cb);

    /* Enqueue a pending cmd with rx_timeout_ms=100, tx at t=1000us (non-zero) */
    pending_cmd_t pcmd = {
        .request_id = 42,
        .read_size = 10,
        .rx_timeout_ms = 100,
        .tx_timestamp = 1000,
        .edge_device_id = 7,
        .command_template_id = 3,
        .command_index = 1,
    };
    xQueueSend(g_test_pending_q[0], &pcmd, 0);

    /* At t=50ms → not expired */
    g_test_time_us = 50 * 1000;
    uint32_t completions = expire_uart_state(&g_test_rt);
    CHECK(completions == 0, "should not expire at 50ms (timeout=100ms)");
    CHECK(uxQueueMessagesWaiting(g_test_pending_q[0]) == 1, "pending cmd should remain");

    /* At t=150ms → expired (150ms - 1ms = 149ms > 100ms) */
    g_test_time_us = 150 * 1000;
    completions = expire_uart_state(&g_test_rt);
    CHECK(completions == 1, "should expire at 150ms");
    CHECK(s_rx_timeout_count[0] == 1, "timeout counter should increment");
    /* timeout report goes to critical queue (error_code != 0) */
    report_desc_t desc;
    bool got = (s_report_critical_q &&
                xQueueReceive(s_report_critical_q, &desc, 0) == pdTRUE);
    CHECK(got, "timeout should emit error report to critical queue");
    if (got) {
        CHECK(desc.error_code == 0x01, "timeout error code should be 0x01");
        CHECK(desc.request_id == 42, "request_id should be preserved");
        report_free_block(true, desc.block_index);
    }
    CHECK(uxQueueMessagesWaiting(g_test_pending_q[0]) == 0, "pending cmd should be consumed");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 12: expire_uart_state — no timeout when rx_timeout_ms=0
 * ===================================================================== */
static void test_expire_no_timeout_when_disabled(void) {
    reset_counters();
    setup_test_runtime();
    drain_report_queues();

    pending_cmd_t pcmd = {
        .request_id = 99,
        .rx_timeout_ms = 0,  /* disabled */
        .tx_timestamp = 0,
    };
    xQueueSend(g_test_pending_q[0], &pcmd, 0);

    g_test_time_us = 999999 * 1000;  /* very late */
    uint32_t completions = expire_uart_state(&g_test_rt);
    CHECK(completions == 0, "should not expire when rx_timeout_ms=0");
    CHECK(uxQueueMessagesWaiting(g_test_pending_q[0]) == 1, "pending cmd should remain");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 13: expire_uart_state — V2 timeout emits control_final
 * ===================================================================== */
static void test_expire_v2_timeout(void) {
    reset_counters();
    setup_test_runtime();
    drain_report_queues();
    bus_worker_set_channel_cmd_v2_final_cb(test_control_final_cb);

    pending_cmd_t pcmd = {
        .request_id = 0,
        .rx_timeout_ms = 50,
        .tx_timestamp = 1000,
        .channel_cmd_v2 = true,
        .control_slot = 3,
    };
    xQueueSend(g_test_pending_q[0], &pcmd, 0);

    g_test_time_us = 100 * 1000;
    uint32_t completions = expire_uart_state(&g_test_rt);
    CHECK(completions == 1, "V2 timeout should complete");
    /* control_final goes to s_control_final_q; callback fires in report_task */
    control_final_desc_t final;
    bool got = (s_control_final_q &&
                xQueueReceive(s_control_final_q, &final, 0) == pdTRUE);
    CHECK(got, "V2 timeout should enqueue control_final");
    if (got) {
        CHECK(final.slot == 3, "control slot should be 3");
        CHECK(final.success == false, "V2 timeout should be failure");
        CHECK(final.error_code == 1, "V2 timeout error code should be 1");
    }

    teardown_test_runtime();
}

/* =====================================================================
 * Test 14: rx_wait_ticks — deadline calculation
 * ===================================================================== */
static void test_rx_wait_ticks(void) {
    reset_counters();
    setup_test_runtime();

    /* No pending, no stream data → full wake period */
    g_test_time_us = 0;
    TickType_t ticks = rx_wait_ticks(&g_test_rt);
    CHECK(ticks == pdMS_TO_TICKS(RX_WAKE_PERIOD_MS),
          "idle should use full wake period");

    /* Stream data with recent RX → shorter deadline */
    s_streams[0].len = 10;
    s_last_rx_us[0] = 0;
    g_test_time_us = 0;
    /* idle threshold = UART_IDLE_THRESHOLD_US from bus_queue_policy.h */
    ticks = rx_wait_ticks(&g_test_rt);
    /* Should be <= full period since idle deadline is sooner */
    CHECK(ticks <= pdMS_TO_TICKS(RX_WAKE_PERIOD_MS),
          "pending idle should shorten wait");

    /* Pending cmd with rx_timeout → deadline from timeout */
    s_streams[0].len = 0;
    s_last_rx_us[0] = 0;
    pending_cmd_t pcmd = {
        .rx_timeout_ms = 30,
        .tx_timestamp = 1000,  /* 1ms, non-zero so tracking is active */
    };
    xQueueSend(g_test_pending_q[0], &pcmd, 0);
    g_test_time_us = 0;
    ticks = rx_wait_ticks(&g_test_rt);
    /* deadline = 1ms + 30ms = 31ms from epoch; now=0 → ~31ms wait */
    CHECK(ticks <= pdMS_TO_TICKS(31) && ticks >= pdMS_TO_TICKS(30),
          "rx_timeout deadline should bound wait");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 15: decode_batch_step — valid frame
 * ===================================================================== */
static void test_decode_batch_step_valid(void) {
    /* Build a valid batch step frame:
     * field 1 (kind) = 1 (varint)
     * field 2 (tx) = {0x01, 0x03} (length-delimited)
     * field 3 (read_size) = 4 (varint)
     * field 4 (rx_timeout_ms) = 1000 (varint)
     * field 5 (post_tx_delay_ms) = 0 (varint)
     */
    uint8_t frame[] = {
        0x08, 0x01,             /* field 1, varint, value=1 */
        0x12, 0x02, 0x01, 0x03, /* field 2, len-delim, len=2, data={01,03} */
        0x18, 0x04,             /* field 3, varint, value=4 */
        0x20, 0xE8, 0x07,       /* field 4, varint, value=1000 */
        0x28, 0x00,             /* field 5, varint, value=0 */
    };

    batch_step_t step;
    bool ok = decode_batch_step(frame, sizeof(frame), &step);
    CHECK(ok, "valid batch step should decode");
    CHECK(step.kind == 1, "kind should be 1");
    CHECK(step.tx_len == 2, "tx_len should be 2");
    CHECK(step.tx[0] == 0x01 && step.tx[1] == 0x03, "tx data should match");
    CHECK(step.read_size == 4, "read_size should be 4");
    CHECK(step.rx_timeout_ms == 1000, "rx_timeout_ms should be 1000");
    CHECK(step.post_tx_delay_ms == 0, "post_tx_delay_ms should be 0");
}

/* =====================================================================
 * Test 16: decode_batch_step — invalid frames
 * ===================================================================== */
static void test_decode_batch_step_invalid(void) {
    batch_step_t step;

    /* NULL data */
    CHECK(!decode_batch_step(NULL, 10, &step), "NULL data should fail");

    /* NULL step */
    uint8_t frame[] = {0x08, 0x01};
    CHECK(!decode_batch_step(frame, sizeof(frame), NULL), "NULL step should fail");

    /* Empty frame */
    CHECK(!decode_batch_step(frame, 0, &step), "empty frame should fail");

    /* Missing required fields (only kind) */
    CHECK(!decode_batch_step(frame, sizeof(frame), &step),
          "incomplete frame should fail");

    /* kind > 3 */
    uint8_t bad_kind[] = {
        0x08, 0x04,             /* kind=4 (invalid) */
        0x12, 0x01, 0xAA,       /* tx */
        0x18, 0x04,             /* read_size */
        0x20, 0xE8, 0x07,       /* rx_timeout */
        0x28, 0x00,             /* post_tx_delay */
    };
    CHECK(!decode_batch_step(bad_kind, sizeof(bad_kind), &step),
          "kind > 3 should fail");

    /* rx_timeout_ms = 0 (must be > 0) */
    uint8_t bad_timeout[] = {
        0x08, 0x01,
        0x12, 0x01, 0xAA,
        0x18, 0x04,
        0x20, 0x00,             /* rx_timeout=0 */
        0x28, 0x00,
    };
    CHECK(!decode_batch_step(bad_timeout, sizeof(bad_timeout), &step),
          "rx_timeout_ms=0 should fail");

    /* read_size > 256 */
    uint8_t bad_readsize[] = {
        0x08, 0x01,
        0x12, 0x01, 0xAA,
        0x18, 0x81, 0x02,       /* read_size=257 */
        0x20, 0xE8, 0x07,
        0x28, 0x00,
    };
    CHECK(!decode_batch_step(bad_readsize, sizeof(bad_readsize), &step),
          "read_size > 256 should fail");
}

/* =====================================================================
 * Test 17: rx_append with pending read_size command
 * ===================================================================== */
static void test_rx_append_with_read_size(void) {
    reset_counters();
    setup_test_runtime();
    drain_report_queues();
    bus_worker_set_callbacks(NULL, test_data_rpt_cb);

    /* Enqueue pending cmd with read_size=8 */
    pending_cmd_t pcmd = {
        .request_id = 55,
        .read_size = 8,
        .rx_timeout_ms = 1000,
        .tx_timestamp = 1000,
    };
    xQueueSend(g_test_pending_q[0], &pcmd, 0);

    /* Feed exactly 8 bytes */
    for (int i = 0; i < 8; i++) g_fake_rx_data[i] = (uint8_t)(0xA0 + i);
    g_fake_rx_len = 8;
    g_fake_rx_pos = 0;
    g_test_time_us = 8000;

    uint8_t rx[256];
    rx_append_from_event(&g_test_rt, 0, rx, sizeof(rx));

    /* read_size match with request_id → critical queue */
    report_desc_t desc;
    bool got = (s_report_critical_q &&
                xQueueReceive(s_report_critical_q, &desc, 0) == pdTRUE);
    CHECK(got, "read_size boundary should trigger emission to critical queue");
    if (got) {
        CHECK(desc.len == 8, "emitted length should match read_size");
        CHECK(desc.request_id == 55, "request_id should be preserved");
        report_free_block(true, desc.block_index);
    }
    CHECK(s_streams[0].len == 0, "stream should be empty after read_size emission");
    CHECK(uxQueueMessagesWaiting(g_test_pending_q[0]) == 0,
          "pending cmd should be consumed after read_size match");

    teardown_test_runtime();
}

/* =====================================================================
 * Test 18: handle_uart_event — batch error event
 * ===================================================================== */
static void test_handle_batch_error_event(void) {
    reset_counters();
    setup_test_runtime();
    drain_report_queues();

    s_plan_active[0] = true;
    memset(&s_batch_rx[0], 0, sizeof(s_batch_rx[0]));

    uart_event_t event = { .type = UART_PARITY_ERR, .size = 0 };
    uint8_t rx[256];
    handle_uart_event(&g_test_rt, 0, &event, rx, sizeof(rx));

    CHECK(s_batch_rx[0].error == true, "batch error event should set error flag");
    CHECK(s_rx_error_count[0] == 1, "error count should increment");

    teardown_test_runtime();
}

/* =====================================================================
 * Main
 * ===================================================================== */
int main(void)
{
    /* Initialize report path (needed for report_enqueue) */
    report_path_init();

    test_uart_slot_from_event_queue();
    test_rx_append_basic();
    test_rx_append_block_emission();
    test_rx_append_overflow();
    test_handle_uart_data_event();
    test_handle_fifo_ovf_event();
    test_handle_break_event();
    test_handle_batch_mode();
    test_handle_batch_overflow();
    test_handle_event_guards();
    test_expire_rx_timeout();
    test_expire_no_timeout_when_disabled();
    test_expire_v2_timeout();
    test_rx_wait_ticks();
    test_decode_batch_step_valid();
    test_decode_batch_step_invalid();
    test_rx_append_with_read_size();
    test_handle_batch_error_event();

    report_path_deinit();

    if (g_failures > 0) {
        fprintf(stderr, "\nbus_worker_rx_tests: %d FAILURES\n", g_failures);
        return 1;
    }
    puts("bus_worker_rx_tests: all tests passed");
    return 0;
}
