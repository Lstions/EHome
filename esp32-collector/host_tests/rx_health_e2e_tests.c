/*
 * rx_health_e2e_tests.c
 *
 * Full-stack regression for the 2026-09-22 "silent sensor reported healthy"
 * defect (defect 2, statistics-ownership migration, 2026-09-30).
 *
 * Why a separate target exists at all:
 *   - bus_worker_rx_tests.c includes the REAL bus_worker.c but STUBS
 *     scheduler_notify_command_outcome, so it can only prove "rx_task calls a
 *     hook".  It cannot prove the hook moves the counter the server reads.
 *   - handler_data_tests.c includes the REAL handler_data.c but feeds it a
 *     hand-built sched_channel_t, so it cannot prove anything feeds that
 *     struct.
 *
 * This target compiles the THREE real components together:
 *
 *   bus_worker.c   -- rx_task, expire_uart_state(), the RX-timeout path
 *   scheduler.c    -- owns sched_command_t.error_count
 *   handler_data.c -- encodes the StatusReport wire frame
 *
 * and drives the real field sequence end to end:
 *
 *   1. a v2 channel/edge-device/command is registered with the scheduler
 *   2. a request is put in flight on a USB channel and the sensor never answers
 *   3. rx_task's real expire_uart_state() observes the timeout
 *   4. msg_handler_send_status() encodes StatusReport
 *   5. the encoded frame MUST carry EdgeDeviceHealth with a non-zero
 *      error_count / comm_status
 *
 * On the pre-fix code the channel-level counter was bumped instead, the
 * command counter stayed 0, and step 5 produced NO EdgeDeviceHealth at all --
 * exactly what the server observed for 7 days.
 *
 * Build: see host_tests/CMakeLists.txt (rx_health_e2e_tests).
 */

#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <stdarg.h>
#include <stdint.h>

#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#include "esp_err.h"
#include "esp_log.h"
#include "esp_system.h"
#include "esp_timer.h"
#include "esp_task_wdt.h"
#include "rom/ets_sys.h"
#include "driver/uart.h"
#include "driver/spi_master.h"
#include "driver/i2c_master.h"

#include "frame_codec.h"
#include "msg_handler.h"
#include "msg_handler_internal.h"
#include "config_mgr.h"
#include "sync_manager.h"
#include "scheduler.h"
#include "bus_worker.h"
#include "bus_dma.h"
#include "cmd_queue.h"
#include "hw_tables.h"
#include "ota.h"
#include "wifi_mgr.h"

/* =====================================================================
 * Test infrastructure
 * ===================================================================== */
static int g_failures = 0;

/* Controllable time for the esp_timer.h host stub (see stubs/esp_timer.h). */
int64_t g_test_time_us = 0;

#define CHECK(cond, msg) do { \
    if (!(cond)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (msg)); \
        g_failures++; \
        return; \
    } \
} while (0)

/* =====================================================================
 * ESP stubs
 * ===================================================================== */
void host_test_log_record(char level, const char *tag, const char *format, ...) {
    (void)level; (void)tag; (void)format;
}
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_OK"; }
void esp_restart(void) {}
size_t esp_get_free_heap_size(void) { return 200000; }
size_t esp_get_minimum_free_heap_size(void) { return 150000; }

/* ---- FreeRTOS task stubs ---- */
#define eDeleted 0
static inline int xTaskCreatePinnedToCore(void (*task_fn)(void *), const char *name,
                                          uint32_t stack, void *param,
                                          UBaseType_t prio, TaskHandle_t *handle,
                                          BaseType_t core)
{
    (void)task_fn; (void)name; (void)stack; (void)param; (void)prio; (void)core;
    if (handle) *handle = (TaskHandle_t)1;
    return 1;
}
static inline UBaseType_t eTaskGetState(TaskHandle_t task) { (void)task; return eDeleted; }
static inline void vTaskDelayUntil(TickType_t *prev, TickType_t inc) { (void)prev; (void)inc; }

/* ---- semaphore stubs (bus_worker.c's report path) ---- */
SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (SemaphoreHandle_t)1; }
int xSemaphoreTake(SemaphoreHandle_t sem, uint32_t ticks) { (void)sem; (void)ticks; return 1; }
int xSemaphoreGive(SemaphoreHandle_t sem) { (void)sem; return 1; }

/* =====================================================================
 * Controllable bus_dma stubs — no bytes ever arrive (silent sensor)
 * ===================================================================== */
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
QueueHandle_t bus_dma_uart_event_queue(const bus_dma_ctx_t *ctx) {
    if (!ctx || !ctx->initialized) return NULL;
    if (ctx->bus_type != BUS_TYPE_UART && ctx->bus_type != BUS_TYPE_USB) return NULL;
    return ctx->uart_event_queue;
}
esp_err_t bus_dma_flush_input(const bus_dma_ctx_t *ctx) { (void)ctx; return ESP_OK; }

/* =====================================================================
 * hw_tables stub — the fixture uses C6 UART0 pins (16/17)
 * ===================================================================== */
uart_port_t hw_derive_uart_port(int tx_pin, int rx_pin, uart_port_t default_port) {
    (void)rx_pin; (void)default_port;
    return tx_pin == 16 ? UART_NUM_0 : UART_NUM_1;
}

/* =====================================================================
 * config_mgr stubs — controllable manifest + real templates
 * ===================================================================== */
static uint64_t g_epoch = 42;
static const char *g_manifest_id = "v2-e2e";
static config_manifest_t g_manifest;
static bool g_manifest_valid = false;

uint64_t config_mgr_get_epoch(void) { return g_epoch; }
const char *config_mgr_get_manifest_id(void) { return g_manifest_id; }
const config_manifest_t *config_mgr_get_manifest(void) {
    return g_manifest_valid ? &g_manifest : NULL;
}
const config_template_t *config_mgr_get_template(uint32_t id) {
    static config_template_t t;
    t.id = id;
    t.write_data[0] = 0x01;
    t.write_data[1] = 0x03;
    t.write_data_len = 2;
    t.read_length = 7;
    t.delay_ms = 0;
    return &t;
}

/* =====================================================================
 * sync_manager / ota / wifi_mgr stubs
 * ===================================================================== */
sync_state_enum_t sync_manager_get_state_enum(void) { return SYNC_STATE_IDLE; }
ota_cmd_class_t ota_classify_cmd(const ota_cmd_t *cmd) { (void)cmd; return OTA_CMD_NEW; }
esp_err_t ota_start(const ota_cmd_t *cmd) { (void)cmd; return ESP_OK; }
void ota_replay_last_progress(const char *id) { (void)id; }
void ota_forget_duplicate(const char *id) { (void)id; }
int wifi_mgr_get_rssi_dbm(void) { return 0; }
void handler_channel_cmd_v2_get_metrics(channel_cmd_v2_metrics_t *m) {
    if (m) memset(m, 0, sizeof(*m));
}

/* =====================================================================
 * msg_handler publish stub — capture the encoded frame
 * ===================================================================== */
static uint8_t g_published[2048];
static size_t g_published_len;
static int g_publish_count;

static void capture(const uint8_t *data, size_t len) {
    if (len <= sizeof(g_published)) {
        memcpy(g_published, data, len);
        g_published_len = len;
    }
    g_publish_count++;
}
esp_err_t msg_handler_publish_checked(const uint8_t *data, size_t len) {
    capture(data, len);
    return ESP_OK;
}
void msg_handler_publish(const uint8_t *data, size_t len) { capture(data, len); }

/* =====================================================================
 * Include the REAL components under test.
 *
 * Each defines its own TAG; undef between includes so the later definitions
 * do not collide (each file's own macros are already expanded by then).
 * ===================================================================== */
#include "../components/bus_worker/bus_worker.c"
#undef TAG_RX
#undef TAG_U0
#undef TAG_U1
#undef TAG_SPI
#undef TAG_I2C
#include "../components/scheduler/scheduler.c"
#undef TAG
#include "../components/msg_handler/handler_data.c"

/* =====================================================================
 * Fixture: one v2 channel -> one edge device -> one command
 * ===================================================================== */
#define CH_ID       100
#define ED_ID       7
#define TEMPLATE_ID 11

/* A USB channel exercises the non-UART arm of expire_uart_state() too. */
static bus_dma_ctx_t g_bus_ctx[SCHED_MAX_CHANNELS];
static uint32_t      g_bus_ch[SCHED_MAX_CHANNELS];
static QueueHandle_t g_pending_q[SCHED_MAX_CHANNELS];
static QueueHandle_t g_event_q[SCHED_MAX_CHANNELS];
static bus_runtime_t g_rt;

static void setup_channel(void)
{
    scheduler_init();
    memset(&s_queues, 0, sizeof(s_queues));

    config_channel_t ch;
    memset(&ch, 0, sizeof(ch));
    ch.id = CH_ID;
    ch.bus_type = BUS_TYPE_USB;      /* native USB endpoint, silent device */
    ch.enabled = true;
    ch.interval_ms = 5000;
    ch.edge_device_count = 1;
    ch.edge_devices[0].edge_device_id = ED_ID;
    ch.edge_devices[0].hardware_id = ED_ID;
    ch.edge_devices[0].command_count = 1;
    ch.edge_devices[0].commands[0].template_id = TEMPLATE_ID;
    ch.edge_devices[0].commands[0].interval_ms = 5000;
    ch.edge_devices[0].commands[0].enabled = true;
    if (scheduler_add_channel(&ch) != SCHED_OK) {
        fprintf(stderr, "fixture: scheduler_add_channel failed\n");
        g_failures++;
    }

    /* RX runtime: channel 0 maps to CH_ID, USB, no bytes ever arrive. */
    memset(g_bus_ctx, 0, sizeof(g_bus_ctx));
    memset(g_bus_ch, 0, sizeof(g_bus_ch));
    memset(&g_rt, 0, sizeof(g_rt));
    g_bus_ctx[0].initialized = true;
    g_bus_ctx[0].bus_type = BUS_TYPE_USB;
    g_event_q[0] = (QueueHandle_t)&g_event_q[0];
    g_bus_ctx[0].uart_event_queue = g_event_q[0];
    g_bus_ch[0] = CH_ID;
    g_pending_q[0] = xQueueCreate(PENDING_QUEUE_DEPTH, sizeof(pending_cmd_t));
    g_rt.bus_ctx = g_bus_ctx;
    g_rt.bus_ch = g_bus_ch;
    g_rt.pending_queues = g_pending_q;
    g_rt.find_ctx = NULL;

    memset(s_streams, 0, sizeof(s_streams));
    memset(s_last_rx_us, 0, sizeof(s_last_rx_us));
    memset(s_rx_sequence, 0, sizeof(s_rx_sequence));
    memset(s_stream_chunked, 0, sizeof(s_stream_chunked));
    memset(s_rx_timeout_count, 0, sizeof(s_rx_timeout_count));

    report_path_init();
}

static void teardown_channel(void)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (g_pending_q[i]) { vQueueDelete(g_pending_q[i]); g_pending_q[i] = NULL; }
    }
    report_path_deinit();
}

static sched_command_t *reported_command(void)
{
    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].config.id == CH_ID) {
            for (int ed = 0; ed < s_channels[i].edge_device_count; ed++) {
                sched_edge_device_t *dev = &s_channels[i].edge_devices[ed];
                if (dev->command_count > 0 && dev->commands[0].template_id == TEMPLATE_ID)
                    return &dev->commands[0];
            }
        }
    }
    return NULL;
}

/* Put one request in flight the way uart_cmd_loop does after a TX, then let
 * rx_task's real timeout path expire it. */
static void drive_one_rx_timeout(void)
{
    pending_cmd_t pcmd = {0};
    pcmd.edge_device_id = ED_ID;
    pcmd.command_template_id = TEMPLATE_ID;
    pcmd.command_index = 0;
    pcmd.read_size = 7;
    pcmd.rx_timeout_ms = 100;
    pcmd.tx_timestamp = g_test_time_us;
    if (xQueueSend(g_pending_q[0], &pcmd, 0) != pdTRUE) {
        fprintf(stderr, "fixture: pending queue send failed\n");
        g_failures++;
        return;
    }
    /* Advance well past the 100 ms timeout with zero bytes received. */
    g_test_time_us += 200 * 1000;
    (void)expire_uart_state(&g_rt);
}

/* Decode the captured StatusReport and return the EdgeDeviceHealth sub-frame
 * fields the server consumes.  Returns false when no health sub-frame is
 * present at all (the exact pre-fix symptom). */
static bool decode_edge_device_health(uint32_t *out_edge_device_id,
                                      uint64_t *out_command_index,
                                      uint64_t *out_error_count,
                                      uint64_t *out_comm_status)
{
    frame_decoder_t dec;
    frame_field_t field;
    if (frame_decoder_init(&dec, g_published, g_published_len) != FRAME_OK) return false;

    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num != STATUS_RPT_F_CH_HEALTH ||
            field.wire_type != WIRE_LENGTH_DELIMITED) continue;

        frame_decoder_t ch_dec;
        frame_field_t ch_field;
        if (frame_decoder_init_sub(&ch_dec, field.value.bytes.ptr,
                                   field.value.bytes.len) != FRAME_OK) continue;
        while (frame_decoder_next(&ch_dec, &ch_field) == FRAME_OK) {
            if (ch_field.field_num != 2 || ch_field.wire_type != WIRE_LENGTH_DELIMITED)
                continue;
            frame_decoder_t ed_dec;
            frame_field_t ed_field;
            if (frame_decoder_init_sub(&ed_dec, ch_field.value.bytes.ptr,
                                       ch_field.value.bytes.len) != FRAME_OK) continue;
            while (frame_decoder_next(&ed_dec, &ed_field) == FRAME_OK) {
                if (ed_field.wire_type != WIRE_VARINT) continue;
                switch (ed_field.field_num) {
                case 1: if (out_edge_device_id) *out_edge_device_id = (uint32_t)ed_field.value.varint; break;
                case 2: if (out_command_index) *out_command_index = ed_field.value.varint; break;
                case 3: if (out_error_count) *out_error_count = ed_field.value.varint; break;
                case 4: if (out_comm_status) *out_comm_status = ed_field.value.varint; break;
                default: break;
                }
            }
            return true;  /* one edge-device sub-frame is enough */
        }
    }
    return false;
}

static void encode_status(const scheduler_state_t *st, uint32_t uptime)
{
    g_publish_count = 0;
    g_published_len = 0;
    msg_handler_send_status(uptime, "running", 1, st);
}

/* =====================================================================
 * Test 1: the 7-day failure, end to end through the real RX-timeout path.
 *
 * A USB device that never answers must appear in the StatusReport the server
 * receives.  Pre-fix this produced NO EdgeDeviceHealth sub-frame at all.
 * ===================================================================== */
static void test_silent_sensor_becomes_visible_in_status_report(void)
{
    setup_channel();
    g_test_time_us = 1000;

    sched_command_t *cmd = reported_command();
    CHECK(cmd != NULL, "fixture: the reported command slot must exist");

    const scheduler_state_t *st = scheduler_get_state();

    /* A fresh channel starts healthy: nothing to report yet.  This mirrors
     * handler_data.c's `error_count == 0 -> skip` contract. */
    encode_status(st, 100);
    CHECK(g_publish_count == 1, "fixture: StatusReport must be published");
    CHECK(!decode_edge_device_health(NULL, NULL, NULL, NULL),
          "a healthy command must not emit EdgeDeviceHealth (baseline)");

    /* The sensor never answers.  This is the REAL rx_task timeout path. */
    drive_one_rx_timeout();

    CHECK(s_rx_timeout_count[0] == 1, "fixture: rx_task must observe the timeout");
    CHECK(cmd->error_count == 1,
          "the real RX-timeout path must increment the REPORTED "
          "sched_command_t.error_count");

    encode_status(st, 200);
    CHECK(g_publish_count == 1, "StatusReport must still be published");

    uint32_t eid = 0;
    uint64_t idx = 0, err = 0, comm = 0;
    CHECK(decode_edge_device_health(&eid, &idx, &err, &comm),
          "a command with an RX timeout MUST appear as EdgeDeviceHealth in "
          "the StatusReport (this is the exact frame the server never saw)");
    CHECK(eid == ED_ID, "the health sub-frame must name the edge device");
    CHECK(idx == 0, "the health sub-frame must name the command index");
    CHECK(err >= 1, "error_count must be non-zero so edge_devices.error_code "
                    "is no longer 0 on the server");
    CHECK(comm == 1, "comm_status must be TIMEOUT(1) for a single timeout");

    /* Sustained silence must escalate to FAULT(3), not stay at 0. */
    drive_one_rx_timeout();
    drive_one_rx_timeout();
    encode_status(st, 300);
    CHECK(decode_edge_device_health(&eid, &idx, &err, &comm),
          "a sustained outage must keep emitting EdgeDeviceHealth");
    CHECK(comm == 3, "comm_status must escalate to FAULT(3) after >=3 timeouts");
    CHECK(err == 3, "error_count must reflect all three unanswered requests");

    teardown_channel();
}

/* =====================================================================
 * Test 2: enqueue success must NOT clear the reported counter.
 *
 * schedule_v2_channel used to zero error_count on every successful enqueue,
 * so a channel that never got an answer still reported error_code == 0.
 * ===================================================================== */
static void test_enqueue_success_does_not_clear_reported_counter(void)
{
    setup_channel();
    g_test_time_us = 1000;

    sched_command_t *cmd = reported_command();
    CHECK(cmd != NULL, "fixture: the reported command slot must exist");

    /* Three RX timeouts through the real path. */
    drive_one_rx_timeout();
    drive_one_rx_timeout();
    drive_one_rx_timeout();
    CHECK(cmd->error_count == 3, "fixture: three timeouts must be recorded");

    /* Now let the scheduler enqueue a sample successfully. */
    QueueHandle_t q = xQueueCreate(CMD_QUEUE_DEPTH, sizeof(bus_cmd_t));
    CHECK(q != NULL, "fixture: queue must be creatable");
    s_queues.uart0_cmd_queue = q;

    for (int i = 0; i < SCHED_MAX_CHANNELS; i++) {
        if (s_channels[i].active && s_channels[i].config.id == CH_ID) {
            s_channels[i].edge_devices[0].commands[0].interval_ms = 0;
            s_channels[i].edge_devices[0].commands[0].last_run_ms = 0;
            uint32_t total = 0, full = 0;
            schedule_v2_channel(&s_channels[i], 1000, false, &total, &full);
            CHECK(total == 1, "fixture: the sample must be enqueued");
        }
    }

    CHECK(cmd->error_count == 3,
          "a successful enqueue must NOT clear the reported error_count "
          "(enqueue success is not channel health)");

    const scheduler_state_t *st = scheduler_get_state();
    encode_status(st, 500);
    uint64_t err = 0;
    CHECK(decode_edge_device_health(NULL, NULL, &err, NULL),
          "the outage must remain visible after a successful enqueue");
    CHECK(err == 3, "error_count must still be 3");

    vQueueDelete(q);
    s_queues.uart0_cmd_queue = NULL;
    teardown_channel();
}

/* =====================================================================
 * Main
 * ===================================================================== */
int main(void)
{
    test_silent_sensor_becomes_visible_in_status_report();
    test_enqueue_success_does_not_clear_reported_counter();

    if (g_failures > 0) {
        fprintf(stderr, "\nrx_health_e2e_tests: %d FAILURES\n", g_failures);
        return 1;
    }
    puts("rx_health_e2e_tests: all tests passed");
    return 0;
}
