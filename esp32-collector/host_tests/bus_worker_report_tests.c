/*
 * bus_worker_report_tests.c
 *
 * Host tests for bus_worker.c's report routing logic, included directly via
 * the #include pattern.  All external dependencies are stubbed.
 *
 * Coverage:
 *   1. report_is_critical() — error_code != 0 OR request_id != 0
 *   2. report_enqueue() — critical/telemetry pool allocation, telemetry
 *      drop when full, s_report_telemetry_drops counter
 *   3. emit_ready_stream_chunks() — 512B fixed block emission, read_size
 *      boundary, channel_cmd_v2 final routing
 *   4. complete_idle_response() — 10ms idle gap completion
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
 *       -o /tmp/test_bw_report \
 *       bus_worker_report_tests.c ../components/frame/frame_codec.c
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

/* ---- bus_dma stubs ---- */
esp_err_t bus_dma_write(bus_dma_ctx_t *ctx, const uint8_t *data, size_t len) {
    (void)ctx; (void)data; (void)len; return ESP_OK;
}
size_t bus_dma_read(bus_dma_ctx_t *ctx, uint8_t *buf, size_t buf_size) {
    (void)ctx; (void)buf; (void)buf_size; return 0;
}
esp_err_t bus_dma_transact(bus_dma_ctx_t *ctx, const uint8_t *tx, size_t tx_len,
                           uint8_t *rx, size_t rx_size, size_t *rx_len) {
    (void)ctx; (void)tx; (void)tx_len; (void)rx; (void)rx_size;
    if (rx_len) *rx_len = 0;
    return ESP_OK;
}
QueueHandle_t bus_dma_uart_event_queue(const bus_dma_ctx_t *ctx) { (void)ctx; return NULL; }

/* ---- semaphore stubs (freertos/semphr.h declares but does not define) ---- */
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
}

/* Drain all queues so report_enqueue allocations are fresh.  Mirrors what
 * the report_task loop does without actually starting the task. */
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
    /* Drain stale set-event entries.  Real FreeRTOS keeps an event in the
     * set's FIFO when a member is drained directly (not via select); these
     * stale events must not leak into the next test's selection order. */
    while (s_report_ready_set &&
           xQueueSelectFromSet(s_report_ready_set, 0) != NULL) { }
}

/* =====================================================================
 * Test 1: report_is_critical()
 * ===================================================================== */
static void test_report_is_critical(void) {
    /* No error, no request → telemetry (not critical) */
    CHECK(report_is_critical(0, 0, 0, 0) == false,
          "plain sample must not be critical");

    /* error_code != 0 → critical */
    CHECK(report_is_critical(1, 0, 0, 0) == true,
          "error_code != 0 must be critical");
    CHECK(report_is_critical(0x01, 0, 42, 7) == true,
          "error_code with routing metadata still critical");

    /* request_id != 0 → critical (WriteCommand response) */
    CHECK(report_is_critical(0, 1, 0, 0) == true,
          "request_id != 0 must be critical");
    CHECK(report_is_critical(0, 999, 100, 200) == true,
          "request_id with routing metadata still critical");

    /* Either condition is sufficient */
    CHECK(report_is_critical(0x12, 0x34, 0, 0) == true,
          "error_code and request_id both set is critical");

    /* edge_device_id / command_template_id alone do NOT make it critical */
    CHECK(report_is_critical(0, 0, 999, 999) == false,
          "routing metadata without error/request must be telemetry");
}

/* =====================================================================
 * Test 2: report_enqueue() — critical pool, telemetry pool, drops
 * ===================================================================== */
static void test_report_enqueue_critical(void) {
    reset_counters();
    /* report_path_init already called in main; ensure pools are full */
    drain_report_queues();

    uint8_t data[] = {0xAA, 0xBB, 0xCC};

    /* Critical report (error_code != 0) should land in critical queue */
    report_enqueue(1, 1000, 1, data, 3, 0x01, 0, 0, 0, 0);
    CHECK(bus_worker_get_report_drop_count() == 0,
          "critical report must not increment drops");

    /* The report should be in the critical queue (depth=4).  Drain and
     * verify the data_rpt_cb fires with the right metadata. */
    /* Simulate report_task draining the critical queue */
    bus_worker_set_callbacks(NULL, test_data_rpt_cb);

    report_desc_t desc;
    bool got = (s_report_critical_q &&
                xQueueReceive(s_report_critical_q, &desc, 0) == pdTRUE);
    CHECK(got, "critical report should be in critical queue");
    if (got) {
        CHECK(desc.critical == true, "desc.critical must be true");
        CHECK(desc.emergency == false, "desc.emergency must be false");
        CHECK(desc.channel_id == 1, "channel_id preserved");
        CHECK(desc.error_code == 0x01, "error_code preserved");
        CHECK(desc.len == 3, "length preserved");
        report_free_block(true, desc.block_index);
    }
    /* Data report callback was not called (report_task not running) */
    CHECK(g_data_rpt_count == 0, "no callback when task not running");
}

static void test_report_enqueue_telemetry(void) {
    reset_counters();
    drain_report_queues();

    uint8_t data[] = {0x01, 0x02};

    /* Telemetry report (error_code == 0, request_id == 0) */
    report_enqueue(5, 2000, 2, data, 2, 0, 0, 0, 0, 0);
    CHECK(bus_worker_get_report_drop_count() == 0,
          "telemetry report must not increment drops when pool has space");

    report_desc_t desc;
    bool got = (s_report_telemetry_q &&
                xQueueReceive(s_report_telemetry_q, &desc, 0) == pdTRUE);
    CHECK(got, "telemetry report should be in telemetry queue");
    if (got) {
        CHECK(desc.critical == false, "telemetry desc.critical must be false");
        CHECK(desc.emergency == false, "telemetry desc.emergency must be false");
        CHECK(desc.channel_id == 5, "channel_id preserved");
        CHECK(desc.len == 2, "length preserved");
        report_free_block(false, desc.block_index);
    }
}

static void test_report_enqueue_telemetry_drops_when_full(void) {
    reset_counters();
    drain_report_queues();

    /* Fill the telemetry pool (12 blocks) + telemetry queue (12 depth).
     * Each report_enqueue allocates a block from the free pool and enqueues
     * a desc.  After 12 enqueues, the free pool is empty and the queue is
     * also full, so the 13th telemetry report should be dropped. */
    uint8_t data[16];
    memset(data, 0x42, sizeof(data));

    uint32_t drops_before = bus_worker_get_report_drop_count();

    for (int i = 0; i < REPORT_TELEMETRY_BLOCKS; i++) {
        report_enqueue(7, 3000 + i, i + 1, data, 8, 0, 0, 0, 0, 0);
    }
    CHECK(bus_worker_get_report_drop_count() == drops_before,
          "telemetry should not drop while pool has space");

    /* 13th telemetry — pool exhausted, should drop */
    report_enqueue(7, 4000, 13, data, 8, 0, 0, 0, 0, 0);
    CHECK(bus_worker_get_report_drop_count() == drops_before + 1,
          "telemetry must drop when pool exhausted");

    /* Critical reports should NOT be dropped even when telemetry is full */
    report_enqueue(7, 5000, 14, data, 8, 0x02, 0, 0, 0, 0);
    CHECK(bus_worker_get_report_drop_count() == drops_before + 1,
          "critical report must not be dropped when telemetry pool is full");

    /* Clean up: drain everything */
    drain_report_queues();
}

static void test_report_enqueue_oversize_critical(void) {
    reset_counters();
    drain_report_queues();

    /* Critical report with oversized payload: error_code becomes 0x02,
     * data=NULL, len=0, but it still gets enqueued (not dropped). */
    uint8_t big[REPORT_PAYLOAD_BLOCK_SIZE + 100];
    memset(big, 0x55, sizeof(big));

    uint32_t drops_before = bus_worker_get_report_drop_count();
    report_enqueue(3, 6000, 1, big, sizeof(big), 0x01, 0, 0, 0, 0);
    CHECK(bus_worker_get_report_drop_count() == drops_before,
          "oversized critical should not drop — it gets error 0x02");

    report_desc_t desc;
    bool got = (s_report_critical_q &&
                xQueueReceive(s_report_critical_q, &desc, 0) == pdTRUE);
    CHECK(got, "oversized critical should still be enqueued");
    if (got) {
        CHECK(desc.error_code == 0x02, "oversized critical error_code should be 0x02");
        CHECK(desc.len == 0, "oversized critical len should be 0");
        report_free_block(true, desc.block_index);
    }
}

static void test_report_enqueue_oversize_telemetry_drops(void) {
    reset_counters();
    drain_report_queues();

    uint8_t big[REPORT_PAYLOAD_BLOCK_SIZE + 1];
    memset(big, 0x77, sizeof(big));

    uint32_t drops_before = bus_worker_get_report_drop_count();
    /* Oversized telemetry: dropped (not critical) */
    report_enqueue(9, 7000, 1, big, sizeof(big), 0, 0, 0, 0, 0);
    CHECK(bus_worker_get_report_drop_count() == drops_before + 1,
          "oversized telemetry must drop");
}

static void test_report_enqueue_null_data_with_len(void) {
    reset_counters();
    drain_report_queues();

    uint32_t drops_before = bus_worker_get_report_drop_count();
    /* data == NULL but len != 0 — invalid, telemetry path should drop */
    report_enqueue(1, 8000, 1, NULL, 10, 0, 0, 0, 0, 0);
    CHECK(bus_worker_get_report_drop_count() == drops_before + 1,
          "NULL data with len > 0 must drop for telemetry");
}

/* =====================================================================
 * Test 3: emit_ready_stream_chunks() — 512B blocks, read_size boundary
 * ===================================================================== */

/* bus_runtime_t with backing storage for pending_queues etc. */
static bus_dma_ctx_t  s_rt_bus_ctx[SCHED_MAX_CHANNELS];
static uint32_t       s_rt_bus_ch[SCHED_MAX_CHANNELS];
static char           s_rt_bus_hw_id[SCHED_MAX_CHANNELS * 16];
static QueueHandle_t  s_rt_pending_queues[SCHED_MAX_CHANNELS];

static void init_test_runtime(bus_runtime_t *rt) {
    memset(rt, 0, sizeof(*rt));
    rt->bus_ctx = s_rt_bus_ctx;
    rt->bus_ch = s_rt_bus_ch;
    rt->bus_hw_id = s_rt_bus_hw_id;
    rt->pending_queues = s_rt_pending_queues;
    rt->dma_pool = NULL;
    rt->find_ctx = NULL;
    rt->lease_hints_valid = false;
    memset(s_rt_bus_ctx, 0, sizeof(s_rt_bus_ctx));
    memset(s_rt_bus_ch, 0, sizeof(s_rt_bus_ch));
    memset(s_rt_pending_queues, 0, sizeof(s_rt_pending_queues));
    /* Reset stream state */
    memset(s_streams, 0, sizeof(s_streams));
    memset(s_last_rx_us, 0, sizeof(s_last_rx_us));
    memset(s_rx_sequence, 0, sizeof(s_rx_sequence));
    memset(s_stream_chunked, 0, sizeof(s_stream_chunked));
}

static void test_emit_fixed_block_chunks(void) {
    reset_counters();
    drain_report_queues();
    bus_worker_set_callbacks(NULL, test_data_rpt_cb);

    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.bus_ch[0] = 42;

    /* Fill stream with 1024 bytes → 2 × 512B chunks */
    stream_rx_t *s = &s_streams[0];
    s->len = 1024;
    memset(s->buffer, 0xAB, 1024);
    s_last_rx_us[0] = 5000;

    emit_ready_stream_chunks(&rt, 0, 5000);

    /* No pending cmd → report_enqueue with request_id=0 (telemetry).
     * Two 512B chunks should be enqueued. */
    report_desc_t desc;
    int count = 0;
    while (s_report_telemetry_q &&
           xQueueReceive(s_report_telemetry_q, &desc, 0) == pdTRUE) {
        count++;
        CHECK(desc.len == 512, "each chunk should be 512 bytes");
        CHECK(desc.request_id == 0, "no pending cmd → request_id=0");
        report_free_block(false, desc.block_index);
    }
    CHECK(count == 2, "1024 bytes should produce 2 chunks of 512");
    CHECK(s->len == 0, "stream buffer should be empty after emission");
}

static void test_emit_partial_block_no_emission(void) {
    reset_counters();
    drain_report_queues();

    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.bus_ch[0] = 42;

    /* 300 bytes — less than 512, no pending cmd → no emission */
    stream_rx_t *s = &s_streams[0];
    s->len = 300;
    memset(s->buffer, 0xCD, 300);

    emit_ready_stream_chunks(&rt, 0, 6000);

    report_desc_t desc;
    bool got = (s_report_telemetry_q &&
                xQueueReceive(s_report_telemetry_q, &desc, 0) == pdTRUE);
    CHECK(!got, "partial block with no pending cmd should not emit");
    CHECK(s->len == 300, "stream should retain 300 bytes");
}

static void test_emit_read_size_boundary(void) {
    reset_counters();
    drain_report_queues();
    bus_worker_set_callbacks(NULL, test_data_rpt_cb);

    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.bus_ch[0] = 99;

    /* Create a pending queue with a cmd that has read_size=100.
     * Use request_id=0 so the emitted report goes to the telemetry queue
     * (report_is_critical(0, 0, ...) is false).  edge_device_id is still
     * set to verify routing metadata is preserved. */
    rt.pending_queues[0] = xQueueCreate(PENDING_QUEUE_DEPTH, sizeof(pending_cmd_t));
    pending_cmd_t pcmd;
    memset(&pcmd, 0, sizeof(pcmd));
    pcmd.request_id = 0;
    pcmd.read_size = 100;
    pcmd.channel_cmd_v2 = false;
    pcmd.edge_device_id = 55;
    pcmd.command_template_id = 77;
    pcmd.command_index = 3;
    xQueueSend(rt.pending_queues[0], &pcmd, 0);

    /* Fill stream with 250 bytes; read_size=100 → one 100B chunk consumed */
    stream_rx_t *s = &s_streams[0];
    s->len = 250;
    memset(s->buffer, 0xEE, 250);

    emit_ready_stream_chunks(&rt, 0, 7000);

    /* First emit consumes 100B and the pending cmd (consume_pending=true
     * because read_size > 0).  Then 150 bytes remain.  has_pending_cmd is
     * now false (queue empty), so target=512, but 150 < 512 → no emission. */
    report_desc_t desc;
    int count = 0;
    /* request_id=0 → telemetry queue */
    while (s_report_telemetry_q &&
           xQueueReceive(s_report_telemetry_q, &desc, 0) == pdTRUE) {
        count++;
        if (count == 1) {
            CHECK(desc.len == 100, "first chunk should be read_size=100");
            CHECK(desc.edge_device_id == 55, "edge_device_id preserved");
        }
        report_free_block(false, desc.block_index);
    }
    CHECK(count == 1, "should emit one 100B chunk from read_size boundary");
    CHECK(s->len == 150, "150 bytes should remain after 100B consumed");

    /* Cleanup */
    if (rt.pending_queues[0]) {
        vQueueDelete(rt.pending_queues[0]);
        rt.pending_queues[0] = NULL;
    }
}

static void test_emit_channel_cmd_v2_final(void) {
    reset_counters();
    drain_report_queues();
    bus_worker_set_channel_cmd_v2_final_cb(test_control_final_cb);
    bus_worker_set_callbacks(NULL, NULL);

    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.bus_ch[0] = 50;

    /* Pending cmd with channel_cmd_v2 = true → queue_control_final */
    rt.pending_queues[0] = xQueueCreate(PENDING_QUEUE_DEPTH, sizeof(pending_cmd_t));
    pending_cmd_t pcmd;
    memset(&pcmd, 0, sizeof(pcmd));
    pcmd.read_size = 64;
    pcmd.channel_cmd_v2 = true;
    pcmd.control_slot = 7;
    xQueueSend(rt.pending_queues[0], &pcmd, 0);

    stream_rx_t *s = &s_streams[0];
    s->len = 64;
    memset(s->buffer, 0x99, 64);

    emit_ready_stream_chunks(&rt, 0, 8000);

    /* Should route to control_final queue, not telemetry */
    CHECK(g_control_final_count == 0,
          "callback not called directly — report_task not running");
    /* Drain control_final queue and verify */
    control_final_desc_t final;
    bool got = (s_control_final_q &&
                xQueueReceive(s_control_final_q, &final, 0) == pdTRUE);
    CHECK(got, "channel_cmd_v2 should queue control_final");
    if (got) {
        CHECK(final.slot == 7, "control_slot preserved");
        CHECK(final.success == true, "success should be true");
        CHECK(final.raw_len == 64, "raw_len should be 64");
    }

    /* Pending cmd consumed because read_size > 0 */
    pending_cmd_t peek;
    bool still_pending = (rt.pending_queues[0] &&
                          xQueuePeek(rt.pending_queues[0], &peek, 0) == pdTRUE);
    CHECK(!still_pending, "pending cmd with read_size>0 should be consumed");

    vQueueDelete(rt.pending_queues[0]);
    rt.pending_queues[0] = NULL;
}

/* =====================================================================
 * Test 4: complete_idle_response() — 10ms idle gap
 * ===================================================================== */
static void test_idle_no_completion_within_threshold(void) {
    reset_counters();
    drain_report_queues();
    bus_worker_set_callbacks(NULL, test_data_rpt_cb);

    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.bus_ch[0] = 33;

    /* s_last_rx_us = 5000, now = 12000 → delta = 7000 < 10000 → no completion */
    s_last_rx_us[0] = 5000;
    s_streams[0].len = 100;
    memset(s_streams[0].buffer, 0x11, 100);

    bool result = complete_idle_response(&rt, 0, 12000);
    CHECK(result == false, "should not complete within 10ms threshold");
    CHECK(s_streams[0].len == 100, "buffer should be retained");
}

static void test_idle_completion_after_threshold(void) {
    reset_counters();
    drain_report_queues();
    bus_worker_set_callbacks(NULL, test_data_rpt_cb);

    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.bus_ch[0] = 33;

    /* s_last_rx_us = 5000, now = 20000 → delta = 15000 > 10000 → completion */
    s_last_rx_us[0] = 5000;
    s_streams[0].len = 100;
    memset(s_streams[0].buffer, 0x22, 100);

    bool result = complete_idle_response(&rt, 0, 20000);
    CHECK(result == true, "should complete after 10ms idle gap");

    /* Should have enqueued a telemetry report (no pending cmd) */
    report_desc_t desc;
    bool got = (s_report_telemetry_q &&
                xQueueReceive(s_report_telemetry_q, &desc, 0) == pdTRUE);
    CHECK(got, "idle completion should enqueue a report");
    if (got) {
        CHECK(desc.len == 100, "report should contain remaining 100 bytes");
        CHECK(desc.channel_id == 33, "channel_id should match");
        report_free_block(false, desc.block_index);
    }
    CHECK(s_streams[0].len == 0, "buffer should be cleared after completion");
    CHECK(s_last_rx_us[0] == 0, "s_last_rx_us should be reset to 0");
    CHECK(s_stream_chunked[0] == false, "s_stream_chunked should be reset");
}

static void test_idle_no_completion_when_last_rx_zero(void) {
    reset_counters();
    drain_report_queues();

    bus_runtime_t rt;
    init_test_runtime(&rt);

    /* s_last_rx_us == 0 → never received anything → no completion */
    s_last_rx_us[0] = 0;
    s_streams[0].len = 50;

    bool result = complete_idle_response(&rt, 0, 99999);
    CHECK(result == false, "should not complete when s_last_rx_us == 0");
}

static void test_idle_completion_with_pending_cmd(void) {
    reset_counters();
    drain_report_queues();
    bus_worker_set_callbacks(NULL, test_data_rpt_cb);

    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.bus_ch[0] = 77;

    rt.pending_queues[0] = xQueueCreate(PENDING_QUEUE_DEPTH, sizeof(pending_cmd_t));
    pending_cmd_t pcmd;
    memset(&pcmd, 0, sizeof(pcmd));
    pcmd.request_id = 42;
    pcmd.read_size = 0;
    pcmd.channel_cmd_v2 = false;
    pcmd.edge_device_id = 10;
    xQueueSend(rt.pending_queues[0], &pcmd, 0);

    /* 80 bytes in buffer, idle gap > 10ms */
    s_last_rx_us[0] = 1000;
    s_streams[0].len = 80;
    memset(s_streams[0].buffer, 0x33, 80);

    bool result = complete_idle_response(&rt, 0, 15000);
    CHECK(result == true, "should complete with pending cmd after idle");

    /* Should have enqueued a critical report (request_id != 0) */
    report_desc_t desc;
    bool got = (s_report_critical_q &&
                xQueueReceive(s_report_critical_q, &desc, 0) == pdTRUE);
    CHECK(got, "idle completion with pending cmd should enqueue critical report");
    if (got) {
        CHECK(desc.critical == true, "report with request_id should be critical");
        CHECK(desc.request_id == 42, "request_id should match pending cmd");
        CHECK(desc.edge_device_id == 10, "edge_device_id preserved");
        CHECK(desc.len == 80, "len should be 80");
        report_free_block(true, desc.block_index);
    }

    /* Pending cmd should be consumed */
    pending_cmd_t peek;
    bool still_pending = (rt.pending_queues[0] &&
                          xQueuePeek(rt.pending_queues[0], &peek, 0) == pdTRUE);
    CHECK(!still_pending, "pending cmd should be consumed on idle completion");

    vQueueDelete(rt.pending_queues[0]);
    rt.pending_queues[0] = NULL;
}

static void test_idle_completion_chunked_no_residual(void) {
    reset_counters();
    drain_report_queues();

    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.bus_ch[0] = 88;

    rt.pending_queues[0] = xQueueCreate(PENDING_QUEUE_DEPTH, sizeof(pending_cmd_t));
    pending_cmd_t pcmd;
    memset(&pcmd, 0, sizeof(pcmd));
    pcmd.request_id = 0;
    pcmd.read_size = 0;
    pcmd.channel_cmd_v2 = false;
    xQueueSend(rt.pending_queues[0], &pcmd, 0);

    /* Stream was chunked (emitted full blocks), no residual bytes, but
     * pending cmd still alive → idle gap closes the descriptor. */
    s_stream_chunked[0] = true;
    s_last_rx_us[0] = 1000;
    s_streams[0].len = 0;

    bool result = complete_idle_response(&rt, 0, 15000);
    CHECK(result == true, "chunked stream with no residual should complete");

    /* Pending cmd consumed */
    pending_cmd_t peek;
    bool still_pending = (rt.pending_queues[0] &&
                          xQueuePeek(rt.pending_queues[0], &peek, 0) == pdTRUE);
    CHECK(!still_pending, "pending cmd should be consumed on chunked idle completion");
    CHECK(s_stream_chunked[0] == false, "s_stream_chunked cleared");

    vQueueDelete(rt.pending_queues[0]);
    rt.pending_queues[0] = NULL;
}

static void test_idle_short_read_is_failure(void) {
    reset_counters();
    drain_report_queues();
    bus_worker_set_channel_cmd_v2_final_cb(test_control_final_cb);

    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.bus_ch[0] = 91;
    rt.pending_queues[0] = xQueueCreate(PENDING_QUEUE_DEPTH, sizeof(pending_cmd_t));

    pending_cmd_t pcmd;
    memset(&pcmd, 0, sizeof(pcmd));
    pcmd.read_size = 100;
    pcmd.channel_cmd_v2 = true;
    pcmd.control_slot = 9;
    xQueueSend(rt.pending_queues[0], &pcmd, 0);

    s_last_rx_us[0] = 1000;
    s_streams[0].len = 40;
    memset(s_streams[0].buffer, 0x44, 40);

    CHECK(complete_idle_response(&rt, 0, 15000),
          "idle short read should complete as a failure");
    control_final_desc_t final;
    bool got = s_control_final_q && xQueueReceive(s_control_final_q, &final, 0) == pdTRUE;
    CHECK(got,
          "short V2 read should queue a final result");
    if (got && final.slot == 9) {
        CHECK(final.success == false, "short V2 read must not be successful");
        CHECK(final.error_code == 0x03, "short V2 read should use error 0x03");
    }
    CHECK(uxQueueMessagesWaiting(rt.pending_queues[0]) == 0,
          "short read pending descriptor should be consumed");
    CHECK(s_streams[0].len == 0, "short read buffer should be cleared");

    vQueueDelete(rt.pending_queues[0]);
    rt.pending_queues[0] = NULL;
}

static void test_idle_overflow_is_failure(void) {
    reset_counters();
    drain_report_queues();
    bus_worker_set_channel_cmd_v2_final_cb(test_control_final_cb);

    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.bus_ch[0] = 92;
    rt.pending_queues[0] = xQueueCreate(PENDING_QUEUE_DEPTH, sizeof(pending_cmd_t));

    pending_cmd_t pcmd;
    memset(&pcmd, 0, sizeof(pcmd));
    pcmd.read_size = 0;
    pcmd.channel_cmd_v2 = true;
    pcmd.control_slot = 10;
    xQueueSend(rt.pending_queues[0], &pcmd, 0);

    s_last_rx_us[0] = 1000;
    s_streams[0].overflow = true;
    s_streams[0].len = 0;

    CHECK(complete_idle_response(&rt, 0, 15000),
          "overflow should complete as an explicit failure");
    control_final_desc_t final;
    bool got = s_control_final_q && xQueueReceive(s_control_final_q, &final, 0) == pdTRUE;
    CHECK(got,
          "overflow V2 read should queue a final result");
    if (got && final.slot == 10) {
        CHECK(final.success == false, "overflow V2 read must not be successful");
        CHECK(final.error_code == 0x02, "overflow V2 read should use error 0x02");
    }
    CHECK(uxQueueMessagesWaiting(rt.pending_queues[0]) == 0,
          "overflow pending descriptor should be consumed");
    CHECK(s_streams[0].overflow == false, "overflow marker should be cleared after reporting");

    vQueueDelete(rt.pending_queues[0]);
    rt.pending_queues[0] = NULL;
}

static void test_timeout_discards_partial_stream(void) {
    reset_counters();
    drain_report_queues();
    bus_worker_set_channel_cmd_v2_final_cb(test_control_final_cb);

    bus_runtime_t rt;
    init_test_runtime(&rt);
    rt.bus_ch[0] = 93;
    rt.bus_ctx[0].initialized = true;
    rt.bus_ctx[0].bus_type = BUS_TYPE_UART;
    rt.pending_queues[0] = xQueueCreate(PENDING_QUEUE_DEPTH, sizeof(pending_cmd_t));

    pending_cmd_t pcmd;
    memset(&pcmd, 0, sizeof(pcmd));
    pcmd.channel_cmd_v2 = true;
    pcmd.control_slot = 11;
    pcmd.read_size = 100;
    pcmd.tx_timestamp = 1000;
    pcmd.rx_timeout_ms = 1;
    xQueueSend(rt.pending_queues[0], &pcmd, 0);

    /* The hard timeout fires before the 10ms idle boundary. */
    g_test_time_us = 3000;
    s_last_rx_us[0] = 2500;
    s_streams[0].len = 40;
    memset(s_streams[0].buffer, 0x55, 40);
    CHECK(expire_uart_state(&rt) == 1, "partial response should expire once");

    control_final_desc_t final;
    bool got = s_control_final_q && xQueueReceive(s_control_final_q, &final, 0) == pdTRUE;
    CHECK(got, "timeout should queue a final result");
    if (got) {
        CHECK(final.slot == 11, "timeout slot should be preserved");
        CHECK(final.success == false, "timeout must not be successful");
    }
    CHECK(s_streams[0].len == 0, "timeout must discard partial bytes");
    CHECK(s_last_rx_us[0] == 0, "timeout must clear idle timestamp");
    CHECK(s_stream_chunked[0] == false, "timeout must clear chunk state");

    vQueueDelete(rt.pending_queues[0]);
    rt.pending_queues[0] = NULL;
}

static void test_idle_no_completion_empty_no_chunked(void) {
    reset_counters();
    drain_report_queues();

    bus_runtime_t rt;
    init_test_runtime(&rt);

    /* len == 0, not chunked, last_rx set → return false */
    s_last_rx_us[0] = 1000;
    s_streams[0].len = 0;
    s_stream_chunked[0] = false;

    bool result = complete_idle_response(&rt, 0, 99999);
    CHECK(result == false, "empty non-chunked stream should not complete");
}

/* =====================================================================
 * Test: report queue-set selection order (F8.2)
 *
 * report_task no longer pre-scans the critical/emergency queues (F8.2).
 * Selection among ready-set members is FreeRTOS arrival-order FIFO: a
 * member queue that transitions empty->non-empty posts its handle to the
 * set's internal event queue, and xQueueSelectFromSet pops that FIFO.
 * So the service order is: (1) control_final / write_rsp via the strict
 * pre-scans above the set wait, then (2) whichever report queue became
 * ready FIRST — critical, emergency, or telemetry alike.  The plan's
 * "add-to-set order" wording does NOT change arrival order; critical
 * priority is best-effort under the approved F8.2 tradeoff.
 *
 * The host stub implements exactly this arrival-order FIFO semantics
 * (first member that transitioned empty->non-empty, in transition order),
 * so this test pins the contract the real firmware observes.
 * ===================================================================== */
static void test_report_queue_set_selection_order(void)
{
    reset_counters();
    drain_report_queues();

    /* Control plane must still win over any report queue (pre-scan). */
    control_final_desc_t final = { .slot = 1, .success = true,
                                   .error_code = 0, .raw_len = 0 };
    CHECK(xQueueSend(s_control_final_q, &final, 0) == pdPASS,
          "control_final send should pass");

    /* Arrival order: telemetry first, then critical.  Selection must
     * return telemetry (arrival-order FIFO), demonstrating that critical
     * is NOT strictly prioritized over telemetry under F8.2. */
    uint8_t data[] = {0x01, 0x02, 0x03};
    report_enqueue(1, 2000, 1, data, 3, 0, 0, 0, 0, 0);      /* telemetry */
    report_enqueue(2, 2001, 2, data, 3, 0x01, 0, 0, 0, 0);   /* critical  */

    /* With control_final pending, the set selects control_final first. */
    QueueSetMemberHandle_t member = s_report_ready_set
        ? xQueueSelectFromSet(s_report_ready_set, 0) : NULL;
    CHECK(member == s_control_final_q,
          "control_final must be selected before critical/telemetry");

    /* Drain control_final. */
    control_final_desc_t got_final;
    CHECK(xQueueReceive(s_control_final_q, &got_final, 0) == pdPASS,
          "control_final receive should pass");
    CHECK(got_final.slot == 1 && got_final.success == true,
          "control_final payload preserved");

    /* Telemetry arrived before critical -> it is selected first. */
    member = s_report_ready_set
        ? xQueueSelectFromSet(s_report_ready_set, 0) : NULL;
    CHECK(member == s_report_telemetry_q,
          "telemetry (arrived first) must be selected before critical");

    /* Consume telemetry; critical is next in arrival order. */
    report_desc_t desc;
    CHECK(xQueueReceive(s_report_telemetry_q, &desc, 0) == pdPASS,
          "telemetry receive should pass");
    member = s_report_ready_set
        ? xQueueSelectFromSet(s_report_ready_set, 0) : NULL;
    CHECK(member == s_report_critical_q,
          "critical must be selected after telemetry drained");

    CHECK(xQueueReceive(s_report_critical_q, &desc, 0) == pdPASS,
          "critical receive should pass");

    drain_report_queues();
}

/* =====================================================================
 * Main
 * ===================================================================== */
int main(void) {
    /* Set up callbacks before report_path_init so report_task (which won't
     * actually run as a real task) can reference them. */
    bus_worker_set_callbacks(NULL, test_data_rpt_cb);
    bus_worker_set_channel_cmd_v2_final_cb(test_control_final_cb);

    /* Initialize the report path (creates pools + queues) */
    report_path_init();
    CHECK(s_report_path_started == true,
          "report_path_init should set s_report_path_started");

    /* Verify pools were created and pre-filled */
    CHECK(s_report_critical_free != NULL, "critical free pool should exist");
    CHECK(s_report_telemetry_free != NULL, "telemetry free pool should exist");
    CHECK(s_report_critical_q != NULL, "critical queue should exist");
    CHECK(s_report_telemetry_q != NULL, "telemetry queue should exist");

    /* Verify initial drop count is zero */
    CHECK(bus_worker_get_report_drop_count() == 0,
          "drop count should be 0 after init");

    /* Run all test suites */
    test_report_is_critical();
    test_report_enqueue_critical();
    test_report_enqueue_telemetry();
    test_report_enqueue_telemetry_drops_when_full();
    test_report_enqueue_oversize_critical();
    test_report_enqueue_oversize_telemetry_drops();
    test_report_enqueue_null_data_with_len();

    test_report_queue_set_selection_order();

    test_emit_fixed_block_chunks();
    test_emit_partial_block_no_emission();
    test_emit_read_size_boundary();
    test_emit_channel_cmd_v2_final();

    test_idle_no_completion_within_threshold();
    test_idle_completion_after_threshold();
    test_idle_no_completion_when_last_rx_zero();
    test_idle_completion_with_pending_cmd();
    test_idle_completion_chunked_no_residual();
    test_idle_short_read_is_failure();
    test_idle_overflow_is_failure();
    test_timeout_discards_partial_stream();
    test_idle_no_completion_empty_no_chunked();

    /* Clean up */
    report_path_deinit();

    if (g_failures != 0) {
        fprintf(stderr, "%d test(s) failed\n", g_failures);
        return 1;
    }
    puts("bus_worker_report_tests: all tests passed");
    return 0;
}
