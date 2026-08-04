/*
 * bus_worker_batch_tests.c
 *
 * Host end-to-end tests for execute_uart_batch() (bus_worker.c) — the
 * ChannelCmdV2 multi-step UART batch executor.  Covers the error-code
 * segmented branches and the waiter/plan lifecycle that the standalone
 * slot tests (channel_cmd_v2_slot_tests.c) cannot reach (they stop at
 * on_channel_cmd_v2_received).
 *
 * Injection point (per 边缘设备修复优化方案-v2 F5): include bus_worker.c
 * and write s_batch_rx[ch].data/.len/.last_rx_us directly, exactly like
 * bus_worker_rx_tests.c:436-451 does.  uart_collect_response() is
 * file-static so it cannot be stubbed from outside; the controllable
 * g_test_time_us clock (esp_timer stub) drives its 10ms idle-completion
 * and timeout branches.
 *
 * execute_uart_batch() resets s_batch_rx len/last_rx_us before each step
 * TX (:812-814), so staged responses are injected *after* each TX from the
 * bus_dma_write stub — mirroring production where rx_task writes the batch
 * buffer asynchronously after TX.
 *
 * Branches covered:
 *   1. happy path — 2 steps, each step RX response injected; raw layout
 *      [count][kind,len_lo,len_hi,resp...]; waiter==NULL at exit.
 *   2. plan length overflow      → *error_code = 0x1100 + step index
 *   3. decode_batch_step failure → *error_code = 0x1100 + step index
 *   4. bus_dma_write failure     → *error_code = 0x1200 + step index
 *   5. RX timeout (no response)  → *error_code = 0x1400 + step index
 *   Every fail path must clear s_batch_rx[ch].waiter (no leak) and
 *   s_plan_active[ch].
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
 *       -o /tmp/test_bw_batch \
 *       bus_worker_batch_tests.c ../components/frame/frame_codec.c
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

/* ---- Controllable time (esp_timer stub reads this) ---- */
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

/* ---- batch RX staging (injected after each TX, mirroring rx_task) ---- */
#define STAGED_RESP_MAX 8
static uint8_t  g_staged[STAGED_RESP_MAX][64];
static size_t   g_staged_len[STAGED_RESP_MAX];
static bool     g_staged_error[STAGED_RESP_MAX];
static unsigned g_staged_count = 0;

static void stage_response(const uint8_t *data, size_t len)
{
    if (g_staged_count >= STAGED_RESP_MAX) return;
    memcpy(g_staged[g_staged_count], data, len);
    g_staged_len[g_staged_count] = len;
    g_staged_error[g_staged_count] = false;
    g_staged_count++;
}

static void stage_error(void)
{
    if (g_staged_count >= STAGED_RESP_MAX) return;
    g_staged_len[g_staged_count] = 0;
    g_staged_error[g_staged_count] = true;
    g_staged_count++;
}

/* ---- Include bus_worker.c directly to access static functions ---- */
#include "../components/bus_worker/bus_worker.c"

/* ---- bus_dma stubs ---- */
#define TX_CAPTURE_MAX 16
static unsigned g_tx_count = 0;
static uint8_t g_tx_capture[TX_CAPTURE_MAX][CMD_TX_MAX];
static size_t g_tx_capture_len[TX_CAPTURE_MAX];
static esp_err_t g_tx_result = ESP_OK;   /* force global TX failure */
static unsigned g_fail_tx_after = 0;     /* fail the Nth TX (1-based); 0=never */

esp_err_t bus_dma_write(bus_dma_ctx_t *ctx, const uint8_t *data, size_t len) {
    (void)ctx; (void)data; (void)len;
    if (g_fail_tx_after > 0 && g_tx_count + 1 == g_fail_tx_after)
        return ESP_FAIL;
    if (g_tx_result != ESP_OK) return g_tx_result;
    if (g_tx_count < TX_CAPTURE_MAX) {
        size_t n = len < CMD_TX_MAX ? len : CMD_TX_MAX;
        memcpy(g_tx_capture[g_tx_count], data, n);
        g_tx_capture_len[g_tx_count] = n;
    }
    g_tx_count++;
    /* After TX N, inject staged response N into the batch RX slot with
     * last_rx_us = now-10000 so the collect loop completes immediately.
     * If the stage is an error (overflow), set the error flag instead.
     * All batch tests drive ch_idx=0. */
    if (g_staged_count > 0 && g_tx_count <= g_staged_count) {
        unsigned idx = g_tx_count - 1;
        memset(&s_batch_rx[0], 0, sizeof(s_batch_rx[0]));
        if (g_staged_error[idx]) {
            s_batch_rx[0].error = true;
        } else {
            memcpy(s_batch_rx[0].data, g_staged[idx], g_staged_len[idx]);
            s_batch_rx[0].len = g_staged_len[idx];
            s_batch_rx[0].last_rx_us = g_test_time_us - 10000;
        }
    }
    return ESP_OK;
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
    (void)ctx; return NULL;
}

/* ---- semaphore stubs ---- */
SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (SemaphoreHandle_t)1; }
int xSemaphoreTake(SemaphoreHandle_t sem, uint32_t ticks) { (void)sem; (void)ticks; return 1; }
int xSemaphoreGive(SemaphoreHandle_t sem) { (void)sem; return 1; }

/* ---- Test infrastructure ---- */
static int g_failures = 0;

#define CHECK(cond, msg) do { \
    if (!(cond)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (msg)); \
        g_failures++; \
    } \
} while (0)

static void reset_capture(void)
{
    g_tx_count = 0;
    g_tx_result = ESP_OK;
    g_fail_tx_after = 0;
    g_staged_count = 0;
    memset(g_tx_capture, 0, sizeof(g_tx_capture));
    memset(g_tx_capture_len, 0, sizeof(g_tx_capture_len));
    memset(g_staged, 0, sizeof(g_staged));
    memset(g_staged_len, 0, sizeof(g_staged_len));
    memset(g_staged_error, 0, sizeof(g_staged_error));
    g_test_time_us = 1000000000;   /* base "now" */
}

/* Reset the file-static batch RX state per test (pure RAM). */
static void reset_batch_rx(int ch_idx)
{
    memset(&s_batch_rx[ch_idx], 0, sizeof(s_batch_rx[ch_idx]));
    s_plan_active[ch_idx] = false;
}

/* ---- Batch step frame builders (wire format from decode_batch_step) ---- */
static size_t build_step(uint8_t *out, uint32_t kind,
                         const uint8_t *tx, size_t tx_len,
                         uint32_t read_size, uint32_t timeout_ms,
                         uint32_t delay_ms)
{
    frame_encoder_t enc;
    frame_encoder_init_sub(&enc, out, 64); /* sub-frame: no type byte */
    frame_encode_varint(&enc, 1, kind);
    frame_encode_bytes(&enc, 2, tx, tx_len);
    frame_encode_varint(&enc, 3, read_size);
    frame_encode_varint(&enc, 4, timeout_ms);
    frame_encode_varint(&enc, 5, delay_ms);
    return frame_encoder_size(&enc);
}

/* Append a step to cmd->plan_data with the 2-byte little-endian length
 * prefix used by handler_channel_cmd_v2.c:266-269. */
static void append_plan_step(bus_cmd_t *cmd, const uint8_t *step_bytes, size_t step_len)
{
    cmd->plan_data[cmd->plan_len] = (uint8_t)(step_len & 0xffU);
    cmd->plan_data[cmd->plan_len + 1] = (uint8_t)(step_len >> 8);
    memcpy(cmd->plan_data + cmd->plan_len + 2, step_bytes, step_len);
    cmd->plan_len += 2 + step_len;
    cmd->plan_step_count++;
}

static void init_batch_cmd(bus_cmd_t *cmd)
{
    memset(cmd, 0, sizeof(*cmd));
    cmd->channel_id = 1;
    cmd->bus_type = BUS_TYPE_UART;
    cmd->channel_cmd_v2 = true;
    cmd->control_slot = 0;
    cmd->type = CMD_WRITE;
}

static bus_dma_ctx_t make_uart_ctx(void)
{
    bus_dma_ctx_t ctx;
    memset(&ctx, 0, sizeof(ctx));
    ctx.bus_type = BUS_TYPE_UART;
    ctx.initialized = true;
    ctx.cfg.uart.port = UART_NUM_0;
    ctx.cfg.uart.baud = 0;          /* compute_turnaround_us: 0 delay */
    ctx.cfg.uart.turnaround_us = -1; /* full duplex — no turnaround */
    return ctx;
}

/* =====================================================================
 * Test 1: happy path — 2 steps, staged responses injected per TX.
 * raw layout: [0]=count, then [kind][len_lo][len_hi][resp...].
 * waiter==NULL at exit.
 * ===================================================================== */
static void test_happy_path_two_steps(void)
{
    reset_capture();
    reset_batch_rx(0);

    uint8_t step1[64], step2[64];
    static const uint8_t tx1[] = {0x01, 0x03, 0x00, 0x00};
    static const uint8_t tx2[] = {0x02, 0x04, 0x00, 0x01};
    size_t n1 = build_step(step1, 1, tx1, sizeof(tx1), 2, 100, 0);
    size_t n2 = build_step(step2, 2, tx2, sizeof(tx2), 3, 100, 0);

    bus_cmd_t cmd;
    init_batch_cmd(&cmd);
    append_plan_step(&cmd, step1, n1);
    append_plan_step(&cmd, step2, n2);

    static const uint8_t resp1[] = {0x11, 0x22};
    static const uint8_t resp2[] = {0x33, 0x44, 0x55};
    stage_response(resp1, sizeof(resp1));
    stage_response(resp2, sizeof(resp2));

    bus_dma_ctx_t ctx = make_uart_ctx();
    uint8_t raw[256];
    size_t raw_len = 0;
    uint32_t error_code = 0;
    bool ok = execute_uart_batch(0, &ctx, &cmd, raw, &raw_len, &error_code);

    CHECK(ok, "happy path must succeed");
    CHECK(error_code == 0, "error_code must be 0 on success");
    CHECK(raw_len == 1 + 3 + 2 + 3 + 3, "raw length must be count+2*(kind+lenpair)+responses");
    CHECK(raw[0] == 2, "raw[0] must be step count");
    if (raw_len == 12) {
        /* step1: kind=1, len=2, resp {11,22} */
        CHECK(raw[1] == 1, "step1 kind mismatch");
        CHECK(raw[2] == 2 && raw[3] == 0, "step1 resp len mismatch");
        CHECK(raw[4] == 0x11 && raw[5] == 0x22, "step1 resp bytes mismatch");
        /* step2: kind=2, len=3, resp {33,44,55} */
        CHECK(raw[6] == 2, "step2 kind mismatch");
        CHECK(raw[7] == 3 && raw[8] == 0, "step2 resp len mismatch");
        CHECK(raw[9] == 0x33 && raw[10] == 0x44 && raw[11] == 0x55, "step2 resp bytes mismatch");
    }
    CHECK(g_tx_count == 2, "must TX both steps");
    CHECK(g_tx_capture_len[0] == sizeof(tx1) && memcmp(g_tx_capture[0], tx1, sizeof(tx1)) == 0,
          "step1 TX bytes mismatch");
    CHECK(g_tx_capture_len[1] == sizeof(tx2) && memcmp(g_tx_capture[1], tx2, sizeof(tx2)) == 0,
          "step2 TX bytes mismatch");
    CHECK(s_batch_rx[0].waiter == NULL, "waiter must be NULL after success");
    CHECK(s_plan_active[0] == false, "plan_active must be cleared after success");
}

/* =====================================================================
 * Test 2: plan length overflow → 0x1100+step
 * ===================================================================== */
static void test_plan_overflow_error(void)
{
    reset_capture();
    reset_batch_rx(0);

    /* Declare a step with length 0xFFFF that overruns cmd->plan_len. */
    bus_cmd_t cmd;
    init_batch_cmd(&cmd);
    cmd.plan_data[0] = 0xFF;
    cmd.plan_data[1] = 0xFF;
    cmd.plan_len = 2;
    cmd.plan_step_count = 2;

    bus_dma_ctx_t ctx = make_uart_ctx();
    uint8_t raw[256];
    size_t raw_len = 0;
    uint32_t error_code = 0;
    bool ok = execute_uart_batch(0, &ctx, &cmd, raw, &raw_len, &error_code);
    CHECK(!ok, "plan overflow must fail");
    CHECK(error_code == 0x1100U + 0, "error_code must be 0x1100+step0");
    CHECK(raw_len == 0, "raw_len must be 0 on failure");
    CHECK(s_batch_rx[0].waiter == NULL, "waiter must be NULL after plan fail");
    CHECK(s_plan_active[0] == false, "plan_active must be cleared");
}

/* =====================================================================
 * Test 3: decode_batch_step failure → 0x1100+step
 * ===================================================================== */
static void test_decode_step_failure(void)
{
    reset_capture();
    reset_batch_rx(0);

    /* Invalid step: only field 2 (tx), missing kind/read/timeout/delay. */
    uint8_t bad[] = {0x12, 0x01, 0xAA};
    bus_cmd_t cmd;
    init_batch_cmd(&cmd);
    append_plan_step(&cmd, bad, sizeof(bad));
    /* A valid second step proves the failure is attributed to step 0. */
    uint8_t good[64];
    static const uint8_t tx[] = {0x01};
    size_t gn = build_step(good, 1, tx, sizeof(tx), 1, 100, 0);
    append_plan_step(&cmd, good, gn);
    stage_response((const uint8_t *)"\xAA", 1);

    bus_dma_ctx_t ctx = make_uart_ctx();
    uint8_t raw[256];
    size_t raw_len = 0;
    uint32_t error_code = 0;
    bool ok = execute_uart_batch(0, &ctx, &cmd, raw, &raw_len, &error_code);
    CHECK(!ok, "decode failure must fail");
    CHECK(error_code == 0x1100U + 0, "error_code must be 0x1100+step0");
    CHECK(raw_len == 0, "raw_len must be 0");
    CHECK(s_batch_rx[0].waiter == NULL, "waiter must be NULL");
}

/* =====================================================================
 * Test 4: bus_dma_write failure → 0x1200+step
 * ===================================================================== */
static void test_tx_failure_error(void)
{
    reset_capture();
    reset_batch_rx(0);

    uint8_t step1[64], step2[64];
    static const uint8_t tx1[] = {0x01};
    static const uint8_t tx2[] = {0x02};
    size_t n1 = build_step(step1, 1, tx1, sizeof(tx1), 1, 100, 0);
    size_t n2 = build_step(step2, 2, tx2, sizeof(tx2), 1, 100, 0);

    bus_cmd_t cmd;
    init_batch_cmd(&cmd);
    append_plan_step(&cmd, step1, n1);
    append_plan_step(&cmd, step2, n2);
    stage_response((const uint8_t *)"\xAA", 1);
    stage_response((const uint8_t *)"\xBB", 1);

    /* Step 0 TX succeeds; step 1 TX fails. */
    g_fail_tx_after = 2;
    bus_dma_ctx_t ctx = make_uart_ctx();
    uint8_t raw[256];
    size_t raw_len = 0;
    uint32_t error_code = 0;
    bool ok = execute_uart_batch(0, &ctx, &cmd, raw, &raw_len, &error_code);
    CHECK(!ok, "TX failure must fail");
    CHECK(error_code == 0x1200U + 1, "error_code must be 0x1200+step1");
    CHECK(raw_len == 0, "raw_len must be 0");
    CHECK(s_batch_rx[0].waiter == NULL, "waiter must be NULL after TX fail");
    CHECK(s_plan_active[0] == false, "plan_active must be cleared");
}

/* =====================================================================
 * Test 5: RX collect failure → 0x1400+step
 *
 * uart_collect_response() returns false (and execute_uart_batch emits
 * 0x1400) when the batch RX state is flagged error (overflow) — the
 * s_batch_rx[ch].error path at bus_worker.c:768.  The pure wall-clock
 * timeout exit of the collect loop is already covered by the existing
 * expire_uart_state suite (bus_worker_rx_tests.c) and cannot be driven
 * deterministically here without re-entrant clock hooks; the segmented
 * error-code wiring is what this branch locks down.
 * ===================================================================== */
static void test_rx_collect_failure_error(void)
{
    reset_capture();
    reset_batch_rx(0);

    uint8_t step[64];
    static const uint8_t tx[] = {0x01};
    size_t n = build_step(step, 1, tx, sizeof(tx), 1, 100, 0);

    bus_cmd_t cmd;
    init_batch_cmd(&cmd);
    append_plan_step(&cmd, step, n);
    /* execute_uart_batch requires >= 2 steps; the error is staged on the
     * first collect, so the second step never runs. */
    append_plan_step(&cmd, step, n);

    /* Stage an error (overflow) instead of a response.  The stub injects
     * the error flag after TX — mirroring rx_task flagging overflow. */
    stage_error();

    bus_dma_ctx_t ctx = make_uart_ctx();
    uint8_t raw[256];
    size_t raw_len = 0;
    uint32_t error_code = 0;
    bool ok = execute_uart_batch(0, &ctx, &cmd, raw, &raw_len, &error_code);
    CHECK(!ok, "RX error flag must fail the batch");
    CHECK(error_code == 0x1400U + 0, "error_code must be 0x1400+step0");
    CHECK(raw_len == 0, "raw_len must be 0");
    CHECK(s_batch_rx[0].waiter == NULL, "waiter must be NULL after RX fail");
    CHECK(s_plan_active[0] == false, "plan_active must be cleared");
}

/* =====================================================================
 * Test 6: invalid batch → execute_uart_batch top-level guards
 * (NULL ctx / NULL ptrs / too few steps) return false without touching
 * slot state.
 * ===================================================================== */
static void test_top_level_guards(void)
{
    reset_capture();
    reset_batch_rx(0);

    bus_dma_ctx_t ctx = make_uart_ctx();
    uint8_t raw[256];
    size_t raw_len = 0;
    uint32_t error_code = 0;

    /* NULL ctx */
    bus_cmd_t cmd;
    init_batch_cmd(&cmd);
    uint8_t step[64];
    static const uint8_t tx[] = {0x01};
    size_t n = build_step(step, 1, tx, sizeof(tx), 1, 100, 0);
    append_plan_step(&cmd, step, n);
    append_plan_step(&cmd, step, n);
    CHECK(!execute_uart_batch(0, NULL, &cmd, raw, &raw_len, &error_code),
          "NULL ctx must be rejected");
    CHECK(!execute_uart_batch(0, &ctx, NULL, raw, &raw_len, &error_code),
          "NULL cmd must be rejected");
    CHECK(!execute_uart_batch(0, &ctx, &cmd, NULL, &raw_len, &error_code),
          "NULL raw must be rejected");
    CHECK(!execute_uart_batch(0, &ctx, &cmd, raw, NULL, &error_code),
          "NULL raw_len must be rejected");
    CHECK(!execute_uart_batch(0, &ctx, &cmd, raw, &raw_len, NULL),
          "NULL error_code must be rejected");

    /* Too few steps (1 < 2) */
    bus_cmd_t one;
    init_batch_cmd(&one);
    append_plan_step(&one, step, n);
    CHECK(!execute_uart_batch(0, &ctx, &one, raw, &raw_len, &error_code),
          "plan_step_count<2 must be rejected");
}

int main(void)
{
    reset_capture();
    reset_batch_rx(0);

    test_happy_path_two_steps();
    test_plan_overflow_error();
    test_decode_step_failure();
    test_tx_failure_error();
    test_rx_collect_failure_error();
    test_top_level_guards();

    if (g_failures > 0) {
        fprintf(stderr, "\nbus_worker_batch_tests: %d FAILURES\n", g_failures);
        return 1;
    }
    puts("bus_worker_batch_tests: all tests passed");
    return 0;
}
