/*
 * bus_worker_data_batch_tests.c — V3-2a 聚合路径的宿主契约测试（契约 §3）。
 *
 * 为什么必须有这组测试：
 *   data_batch_codec_tests 只证明"编码器编出的字节符合契约"。它证明不了
 *   固件**什么时候**会去编 0x20 —— 而那正是本次改动风险最高的地方：
 *     1. 兼容性红线：能力位为 0 时不得发 0x20，行为必须与现状逐字节一致；
 *     2. 关键样本（error_code/request_id != 0）永远不得入批；
 *     3. 路由元数据不同的样本不得混进同一批（契约 §2.1.1）；
 *     4. 窗口外的样本不得并入（契约 §3，WINDOW_MS=20）；
 *     5. 队列空即发、不等待；不满足条件的样本**放回队列**、不丢；
 *     6. 编码缓冲不足时**降 n**，不得截断。
 *
 * 手法：直接编入 bus_worker.c（与其既有 host test 同款 #include 模式），
 * 用真实 FreeRTOS 队列 stub 构造 telemetry 队列，直接调
 * report_try_data_batch() —— 不启动真实 report_tx 任务（它依赖调度器）。
 * 捕获 s_data_rpt_cb 收到的第一字节，就能断言"发的是 0x03 还是 0x20"。
 *
 * Build:
 *   gcc -std=c11 -Wall -Wextra -Werror -Wno-unused-function \
 *       -DCONFIG_IDF_TARGET_ESP32C6=1 \
 *       -I stubs -I stubs/rom \
 *       -I ../components/bus_worker/include -I ../components/bus_dma/include \
 *       -I ../components/bus_manager/include -I ../components/scheduler \
 *       -I ../components/config_mgr -I ../components/dma_pool/include \
 *       -I ../components/hw_profile/include -I ../components/frame \
 *       -I ../components/msg_handler \
 *       -o /tmp/test_bw_db \
 *       bus_worker_data_batch_tests.c ../components/frame/frame_codec.c \
 *       ../components/msg_handler/data_batch_codec.c
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
/* bus_worker.c 自身不再包含编码器（它只做聚合决策）；本 suite 用被测编码器
 * 作为注入回调的实现，所以在这里显式引入。 */
#include "data_batch_codec.h"

/* ---- Controllable time ---- */
int64_t g_test_time_us = 0;

/* ---- ESP stubs ---- */
void host_test_log_record(char level, const char *tag, const char *format, ...) {
    (void)level; (void)tag; (void)format;
}
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_OK"; }
void esp_restart(void) { }

/* ---- scheduler stubs ---- */
void scheduler_notify_channel_error(uint32_t channel_id) { (void)channel_id; }
void scheduler_notify_channel_success(uint32_t channel_id) { (void)channel_id; }
bool scheduler_notify_command_outcome(uint32_t channel_id, uint32_t edge_device_id,
                                      uint32_t command_template_id,
                                      uint8_t command_index, bool success) {
    (void)channel_id; (void)edge_device_id; (void)command_template_id;
    (void)command_index; (void)success;
    return false;
}

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
esp_err_t bus_dma_flush_input(const bus_dma_ctx_t *ctx) { (void)ctx; return ESP_OK; }

/* ---- semaphore stubs ---- */
SemaphoreHandle_t xSemaphoreCreateMutex(void) { return (SemaphoreHandle_t)1; }
int xSemaphoreTake(SemaphoreHandle_t sem, uint32_t ticks) { (void)sem; (void)ticks; return 1; }
int xSemaphoreGive(SemaphoreHandle_t sem) { (void)sem; return 1; }

/* ---- Include bus_worker.c directly to access static functions ---- */
#include "../components/bus_worker/bus_worker.c"

/* =====================================================================
 * 能力位注入：hello_get_server_caps() 是 bus_worker.c 的外部依赖，这里给出
 * 测试可控的定义（真实实现在 handler_hello.c，本 target 不编译它）。
 * ===================================================================== */
static uint64_t g_caps = 0;
uint64_t hello_get_server_caps(void) { return g_caps; }

/* =====================================================================
 * 发布捕获：report_try_data_batch 最终调 s_data_rpt_cb 把整帧交给
 * msg_handler。这里只关心"第一字节是 0x03 还是 0x20"以及帧内容。
 * ===================================================================== */
#define CAPTURE_MAX 16
static uint8_t  g_frame[CAPTURE_MAX][2048];
static size_t   g_frame_len[CAPTURE_MAX];
static unsigned g_frame_count = 0;

static void capture_cb(uint32_t ch, uint64_t ts, uint32_t seq,
                       const uint8_t *data, size_t len, uint32_t ec, uint32_t rid,
                       uint32_t eid, uint32_t tid, uint8_t ci) {
    (void)ch; (void)ts; (void)seq; (void)ec; (void)rid; (void)eid; (void)tid; (void)ci;
    if (g_frame_count >= CAPTURE_MAX) return;
    if (len > sizeof(g_frame[0])) return;
    memcpy(g_frame[g_frame_count], data, len);
    g_frame_len[g_frame_count] = len;
    g_frame_count++;
}

/* 真实的 DataBatch 编码回调替身：bus_worker 现在只负责聚合决策，编码由
 * 注入的回调完成（生产环境是 msg_handler_send_data_batch，用它的 1400 B
 * 预算）。这里用被测编码器 data_batch_encode 完成同样的工作并捕获字节，
 * 从而让本 suite 同时验证"聚合决策"与"编出的字节符合契约"。 */
static uint8_t g_encode_buf[DATA_BATCH_MAX_SAMPLES][1400];

static bool test_data_batch_cb(uint32_t channel_id, uint32_t first_sequence,
                               const uint64_t *timestamps_us,
                               const uint8_t *const *raw_data,
                               const size_t *raw_lens, size_t count,
                               uint32_t edge, uint32_t tmpl, uint8_t cmd_idx) {
    if (count < 2 || count > DATA_BATCH_MAX_SAMPLES) return false;
    data_batch_sample_t samples[DATA_BATCH_MAX_SAMPLES];
    for (size_t i = 0; i < count; i++) {
        samples[i].delta_us = timestamps_us[i] - timestamps_us[0];
        samples[i].raw_data = raw_data[i];
        samples[i].raw_len = raw_lens[i];
    }
    uint8_t buf[1400];
    size_t len = 0;
    if (data_batch_encode(buf, sizeof(buf), &len, channel_id, timestamps_us[0],
                          first_sequence, samples, count, edge, tmpl, cmd_idx)
            != FRAME_OK) {
        return false;
    }
    if (g_frame_count < CAPTURE_MAX && len <= sizeof(g_frame[0])) {
        memcpy(g_frame[g_frame_count], buf, len);
        g_frame_len[g_frame_count] = len;
        g_frame_count++;
    }
    return true;
}

static int g_failures = 0;
static int g_checks = 0;
#define CHECK(cond, ...)                                                     \
    do {                                                                     \
        g_checks++;                                                          \
        if (!(cond)) {                                                       \
            g_failures++;                                                    \
            fprintf(stderr, "FAIL %s:%d: ", __func__, __LINE__);             \
            fprintf(stderr, __VA_ARGS__);                                    \
            fprintf(stderr, "\n");                                           \
        }                                                                    \
    } while (0)

/* 直接把 desc 塞进 telemetry 队列（绕开 report_enqueue 的池分配，聚焦聚合
 * 逻辑本身）。block_index 用 i，payload 由 report_payload_of 解析。 */
static uint8_t g_payload[DATA_BATCH_MAX_SAMPLES][REPORT_PAYLOAD_BLOCK_SIZE];

static bool push_telemetry(uint32_t channel, uint64_t ts, uint32_t seq,
                           const uint8_t *raw, size_t raw_len,
                           uint32_t edge, uint32_t tmpl, uint8_t cmd_idx) {
    static uint8_t next_block = 0;
    if (raw_len > REPORT_PAYLOAD_BLOCK_SIZE) return false;
    uint8_t idx = next_block++;
    if (next_block >= DATA_BATCH_MAX_SAMPLES) next_block = 0;
    if (raw_len) memcpy(g_payload[idx], raw, raw_len);
    report_desc_t d = {
        .channel_id = channel, .timestamp_us = ts, .sequence = seq,
        .error_code = 0, .request_id = 0,
        .edge_device_id = edge, .command_template_id = tmpl,
        .command_index = cmd_idx, .block_index = idx, .len = (uint16_t)raw_len,
        .critical = false, .emergency = false,
    };
    return xQueueSend(s_report_telemetry_q, &d, 0) == pdTRUE;
}

/* 把 s_telemetry_payload 指向测试自己的 payload（非 PSRAM 分支下它是个
 * 2 维数组，直接 memcpy 不合适 —— 改用 report_enqueue 提供的池太绕）。
 * 这里用一个技巧：payload_of 读的是 s_telemetry_payload[block_index]，
 * 我们让 block_index 指向已写入 g_payload 的内容。为保持简单，直接把
 * g_payload 复制进 s_telemetry_payload 对应 slot。 */
static void stash_payload(uint8_t idx, const uint8_t *raw, size_t raw_len) {
    if (raw_len) memcpy(s_telemetry_payload[idx], raw, raw_len);
}

static void reset_capture(void) { g_frame_count = 0; }

static void drain_telemetry(void) {
    report_desc_t d;
    while (s_report_telemetry_q && xQueueReceive(s_report_telemetry_q, &d, 0) == pdTRUE) { }
    if (s_report_ready_set) {
        while (xQueueSelectFromSet(s_report_ready_set, 0) != NULL) { }
    }
}

/* ------------------------------------------------------------------ *
 *  T1: 能力位为 0 -> report_try_data_batch 必须返回 false（走 0x03）
 * ------------------------------------------------------------------ */
static void test_caps_zero_never_batches(void)
{
    g_caps = 0;
    drain_telemetry();
    reset_capture();

    uint8_t raw[4] = { 1, 2, 3, 4 };
    stash_payload(0, raw, sizeof(raw));
    stash_payload(1, raw, sizeof(raw));
    CHECK(xQueueSend(s_report_telemetry_q, &(report_desc_t){
              .channel_id = 3, .timestamp_us = 1000, .sequence = 1,
              .block_index = 0, .len = 4 }, 0) == pdTRUE, "seed desc 0");
    CHECK(xQueueSend(s_report_telemetry_q, &(report_desc_t){
              .channel_id = 3, .timestamp_us = 1010, .sequence = 2,
              .block_index = 1, .len = 4 }, 0) == pdTRUE, "seed desc 1");

    report_desc_t first;
    CHECK(xQueueReceive(s_report_telemetry_q, &first, 0) == pdTRUE, "pop first desc");
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == false,
          "caps=0 must NOT batch (compatibility red line)");
    CHECK(g_frame_count == 0, "caps=0 must publish nothing from the batch path");
    /* 第二个 desc 必须原封不动留在队列里，等 0x03 路径处理。 */
    CHECK(uxQueueMessagesWaiting(s_report_telemetry_q) == 1,
          "caps=0 must leave the queue untouched (got %u)",
          (unsigned)uxQueueMessagesWaiting(s_report_telemetry_q));
    drain_telemetry();
}

/* ------------------------------------------------------------------ *
 *  T2: 能力位置位 -> 两个同源样本聚合为一帧 0x20
 * ------------------------------------------------------------------ */
static void test_caps_set_batches_same_source(void)
{
    g_caps = CAP_DATA_BATCH_V1;
    drain_telemetry();
    reset_capture();

    uint8_t raw[3] = { 0xAA, 0xBB, 0xCC };
    report_desc_t d0 = { .channel_id = 3, .timestamp_us = 1000, .sequence = 100,
                         .block_index = 0, .len = 3, .edge_device_id = 42,
                         .command_template_id = 7, .command_index = 1 };
    report_desc_t d1 = d0;
    d1.timestamp_us = 1010; d1.sequence = 101; d1.block_index = 1;
    stash_payload(0, raw, sizeof(raw));
    stash_payload(1, raw, sizeof(raw));
    CHECK(xQueueSend(s_report_telemetry_q, &d0, 0) == pdTRUE, "seed d0");
    CHECK(xQueueSend(s_report_telemetry_q, &d1, 0) == pdTRUE, "seed d1");

    report_desc_t first;
    CHECK(xQueueReceive(s_report_telemetry_q, &first, 0) == pdTRUE, "pop first");
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == true,
          "caps=1 must batch two same-source samples");

    CHECK(g_frame_count == 1, "exactly one frame must be published (got %u)", g_frame_count);
    CHECK(g_frame_len[0] > 0 && g_frame[0][0] == MSG_DATA_BATCH,
          "published frame must be 0x20 (got 0x%02X)", g_frame_len[0] ? g_frame[0][0] : 0);

    data_batch_decoded_t dec;
    CHECK(data_batch_decode(g_frame[0], g_frame_len[0], &dec) == FRAME_OK,
          "published frame must decode");
    CHECK(dec.count == 2, "batch must contain 2 samples (got %zu)", dec.count);
    CHECK(dec.channel_id == 3 && dec.first_sequence == 100, "batch routing metadata");
    CHECK(dec.base_timestamp_us == 1000, "batch base_ts must be the first sample");
    CHECK(dec.samples[0].delta_us == 0 && dec.samples[1].delta_us == 10,
          "deltas must be 0 and 10 us");
    CHECK(dec.samples[1].raw_len == 3 &&
          memcmp(dec.samples[1].raw_data, raw, 3) == 0, "second raw must roundtrip");

    CHECK(uxQueueMessagesWaiting(s_report_telemetry_q) == 0,
          "both descs must be consumed from the queue");
    drain_telemetry();
}

/* ------------------------------------------------------------------ *
 *  T3: 不同 channel / edge / template 不得混批
 * ------------------------------------------------------------------ */
static void test_different_routing_not_batched(void)
{
    g_caps = CAP_DATA_BATCH_V1;
    drain_telemetry();
    reset_capture();

    uint8_t raw[3] = { 1, 2, 3 };
    stash_payload(0, raw, sizeof(raw));
    stash_payload(1, raw, sizeof(raw));

    /* 不同 channel。 */
    report_desc_t a = { .channel_id = 3, .timestamp_us = 1000, .sequence = 1,
                        .block_index = 0, .len = 3 };
    report_desc_t b = a; b.channel_id = 4; b.timestamp_us = 1010;
    b.sequence = 2; b.block_index = 1;
    xQueueSend(s_report_telemetry_q, &a, 0);
    xQueueSend(s_report_telemetry_q, &b, 0);
    report_desc_t first;
    xQueueReceive(s_report_telemetry_q, &first, 0);
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == false,
          "different channel must not be batched together");
    CHECK(g_frame_count == 0, "no frame published for mismatched routing");
    CHECK(uxQueueMessagesWaiting(s_report_telemetry_q) == 1,
          "the mismatched desc must stay queued");
    drain_telemetry();

    /* 不同 edge_device_id。 */
    reset_capture();
    a.edge_device_id = 42; b.edge_device_id = 43;
    xQueueSend(s_report_telemetry_q, &a, 0);
    xQueueSend(s_report_telemetry_q, &b, 0);
    xQueueReceive(s_report_telemetry_q, &first, 0);
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == false,
          "different edge_device_id must not be batched together");
    drain_telemetry();

    /* 不同 command_template_id。 */
    reset_capture();
    a.edge_device_id = 0; b.edge_device_id = 0;
    a.command_template_id = 7; b.command_template_id = 8;
    xQueueSend(s_report_telemetry_q, &a, 0);
    xQueueSend(s_report_telemetry_q, &b, 0);
    xQueueReceive(s_report_telemetry_q, &first, 0);
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == false,
          "different command_template_id must not be batched together");
    drain_telemetry();
}

/* ------------------------------------------------------------------ *
 *  T4: 窗口外的样本不得并入
 * ------------------------------------------------------------------ */
static void test_window_boundary(void)
{
    g_caps = CAP_DATA_BATCH_V1;
    uint8_t raw[3] = { 9, 9, 9 };
    report_desc_t first;

    /* 正好 WINDOW_MS 边界（20 ms = 20000 us）：ts - start == 20000 不在窗口内
     * （契约用 < WINDOW_MS）。 */
    drain_telemetry(); reset_capture();
    stash_payload(0, raw, sizeof(raw)); stash_payload(1, raw, sizeof(raw));
    report_desc_t a = { .channel_id = 3, .timestamp_us = 1000, .sequence = 1,
                        .block_index = 0, .len = 3 };
    report_desc_t b = a; b.timestamp_us = 1000 + 20000; b.sequence = 2; b.block_index = 1;
    xQueueSend(s_report_telemetry_q, &a, 0);
    xQueueSend(s_report_telemetry_q, &b, 0);
    xQueueReceive(s_report_telemetry_q, &first, 0);
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == false,
          "sample exactly at WINDOW_MS must be excluded (delta < window)");
    drain_telemetry();

    /* 窗口内最后一个微秒（19999 us）可以并入。 */
    reset_capture();
    stash_payload(0, raw, sizeof(raw)); stash_payload(1, raw, sizeof(raw));
    b.timestamp_us = 1000 + 19999;
    xQueueSend(s_report_telemetry_q, &a, 0);
    xQueueSend(s_report_telemetry_q, &b, 0);
    xQueueReceive(s_report_telemetry_q, &first, 0);
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == true,
          "sample 1 us inside the window must be batched");
    CHECK(g_frame_count == 1 && g_frame[0][0] == MSG_DATA_BATCH,
          "in-window batch must publish a 0x20 frame");
    drain_telemetry();

    /* 时间戳倒退（乱序）不得并入 —— 否则 delta 会违反单调不变量。 */
    reset_capture();
    stash_payload(0, raw, sizeof(raw)); stash_payload(1, raw, sizeof(raw));
    b.timestamp_us = 900; /* < a.timestamp_us */
    xQueueSend(s_report_telemetry_q, &a, 0);
    xQueueSend(s_report_telemetry_q, &b, 0);
    xQueueReceive(s_report_telemetry_q, &first, 0);
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == false,
          "backwards timestamp must not be batched (delta monotonicity)");
    drain_telemetry();
}

/* ------------------------------------------------------------------ *
 *  T5: 关键样本不入批；n 上限为 4；队列空即发不等待
 * ------------------------------------------------------------------ */
static void test_critical_and_max_n(void)
{
    g_caps = CAP_DATA_BATCH_V1;
    uint8_t raw[3] = { 5, 5, 5 };

    /* 关键样本作为"第一个"：不批。 */
    drain_telemetry(); reset_capture();
    stash_payload(0, raw, sizeof(raw));
    report_desc_t crit = { .channel_id = 3, .timestamp_us = 1000, .sequence = 1,
                           .block_index = 0, .len = 3, .error_code = 0x01,
                           .critical = true };
    report_desc_t nrm = crit; nrm.timestamp_us = 1010; nrm.sequence = 2;
    nrm.block_index = 1; nrm.error_code = 0; nrm.critical = false;
    stash_payload(1, raw, sizeof(raw));
    xQueueSend(s_report_telemetry_q, &crit, 0);
    xQueueSend(s_report_telemetry_q, &nrm, 0);
    report_desc_t first;
    xQueueReceive(s_report_telemetry_q, &first, 0);
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == false,
          "a critical desc must never be batched");
    drain_telemetry();

    /* 后续样本是关键：只批非关键的前缀。 */
    reset_capture();
    stash_payload(0, raw, sizeof(raw));
    report_desc_t c1 = nrm; c1.timestamp_us = 2000; c1.sequence = 10; c1.block_index = 0;
    report_desc_t c2 = nrm; c2.timestamp_us = 2010; c2.sequence = 11; c2.block_index = 1;
    report_desc_t c3 = nrm; c3.timestamp_us = 2020; c3.sequence = 12; c3.block_index = 2;
    c3.error_code = 0x02; c3.critical = true;
    stash_payload(2, raw, sizeof(raw));
    xQueueSend(s_report_telemetry_q, &c1, 0);
    xQueueSend(s_report_telemetry_q, &c2, 0);
    xQueueSend(s_report_telemetry_q, &c3, 0);
    xQueueReceive(s_report_telemetry_q, &first, 0);
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == true,
          "two non-critical samples before a critical one must batch");
    data_batch_decoded_t dec;
    CHECK(data_batch_decode(g_frame[0], g_frame_len[0], &dec) == FRAME_OK,
          "prefix batch must decode");
    CHECK(dec.count == 2, "batch must stop at the critical sample (got %zu)", dec.count);
    CHECK(uxQueueMessagesWaiting(s_report_telemetry_q) == 1,
          "the critical desc must remain queued for the 0x03 path");
    drain_telemetry();

    /* n 上限：放 6 个同源样本，最多只批 4 个。 */
    reset_capture();
    for (uint8_t i = 0; i < 6; i++) {
        stash_payload(i % DATA_BATCH_MAX_SAMPLES, raw, sizeof(raw));
        report_desc_t d = { .channel_id = 3, .timestamp_us = 3000 + i * 5,
                            .sequence = 20 + i, .block_index = i % DATA_BATCH_MAX_SAMPLES,
                            .len = 3 };
        xQueueSend(s_report_telemetry_q, &d, 0);
    }
    xQueueReceive(s_report_telemetry_q, &first, 0);
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == true, "6 samples must batch");
    CHECK(data_batch_decode(g_frame[0], g_frame_len[0], &dec) == FRAME_OK,
          "max-n batch must decode");
    CHECK(dec.count == DATA_BATCH_MAX_SAMPLES,
          "batch must be capped at %d (got %zu)", DATA_BATCH_MAX_SAMPLES, dec.count);
    CHECK(uxQueueMessagesWaiting(s_report_telemetry_q) == 2,
          "the two overflow samples must remain queued (got %u)",
          (unsigned)uxQueueMessagesWaiting(s_report_telemetry_q));
    drain_telemetry();

    /* 队列里只有一个样本：不发批，返回 false，不等待。 */
    reset_capture();
    stash_payload(0, raw, sizeof(raw));
    report_desc_t solo = { .channel_id = 3, .timestamp_us = 4000, .sequence = 40,
                           .block_index = 0, .len = 3 };
    xQueueSend(s_report_telemetry_q, &solo, 0);
    xQueueReceive(s_report_telemetry_q, &first, 0);
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == false,
          "a single sample must not become a 1-sample batch (queue empty -> no wait)");
    CHECK(g_frame_count == 0, "nothing published for a single sample");
    drain_telemetry();
}

/* ------------------------------------------------------------------ *
 *  T6: 缓冲不足 -> 降 n，不截断
 * ------------------------------------------------------------------ */
static void test_buffer_pressure_downgrades_n(void)
{
    g_caps = CAP_DATA_BATCH_V1;
    drain_telemetry();
    reset_capture();

    /* 每个样本 1024 B（上限）。4 个必然超过 DATA_BATCH_BUF_SIZE(1400)，
     * 因此聚合必须在 n=1 时停下 —— 即不发批，退回 0x03。 */
    static uint8_t big[DATA_BATCH_MAX_RAW];
    memset(big, 0x5A, sizeof(big));
    for (uint8_t i = 0; i < DATA_BATCH_MAX_SAMPLES; i++) {
        stash_payload(i, big, sizeof(big));
        report_desc_t d = { .channel_id = 3, .timestamp_us = 5000 + i * 10,
                            .sequence = 50 + i, .block_index = i, .len = sizeof(big) };
        xQueueSend(s_report_telemetry_q, &d, 0);
    }
    report_desc_t first;
    xQueueReceive(s_report_telemetry_q, &first, 0);
    /* n=2 时 2 x 1024 已超预算，所以这里必须不批。 */
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == false,
          "two 1024B samples exceed the 1400B budget -> must not batch");
    CHECK(g_frame_count == 0, "no frame may be published when it cannot fit");
    CHECK(uxQueueMessagesWaiting(s_report_telemetry_q) == 3,
          "all remaining descs must stay queued (got %u)",
          (unsigned)uxQueueMessagesWaiting(s_report_telemetry_q));
    drain_telemetry();

    /* 300 B x 4 是契约 §2.2 的标准形态：必须正好批 4 个。 */
    reset_capture();
    static uint8_t mid[300];
    memset(mid, 0x33, sizeof(mid));
    for (uint8_t i = 0; i < DATA_BATCH_MAX_SAMPLES; i++) {
        stash_payload(i, mid, sizeof(mid));
        report_desc_t d = { .channel_id = 3, .timestamp_us = 6000 + i * 10,
                            .sequence = 60 + i, .block_index = i, .len = sizeof(mid) };
        xQueueSend(s_report_telemetry_q, &d, 0);
    }
    xQueueReceive(s_report_telemetry_q, &first, 0);
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == true,
          "4 x 300B must batch (contract 2.2 budget shape)");
    data_batch_decoded_t dec;
    CHECK(data_batch_decode(g_frame[0], g_frame_len[0], &dec) == FRAME_OK,
          "300B x 4 frame must decode");
    CHECK(dec.count == 4, "300B x 4 must produce a full 4-sample batch (got %zu)", dec.count);
    drain_telemetry();

    /* 500 B x 4：n=3 就超预算（≈1520 > 1400），因此必须"降 n"成 2 个样本的
     * 批，而不是放弃整批、也不是截断。这条是契约 §2.2 的核心断言。 */
    reset_capture();
    static uint8_t half[500];
    memset(half, 0x44, sizeof(half));
    for (uint8_t i = 0; i < DATA_BATCH_MAX_SAMPLES; i++) {
        stash_payload(i, half, sizeof(half));
        report_desc_t d = { .channel_id = 3, .timestamp_us = 6500 + i * 10,
                            .sequence = 65 + i, .block_index = i, .len = sizeof(half) };
        xQueueSend(s_report_telemetry_q, &d, 0);
    }
    xQueueReceive(s_report_telemetry_q, &first, 0);
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == true,
          "4 x 500B must still batch, at a reduced n");
    CHECK(data_batch_decode(g_frame[0], g_frame_len[0], &dec) == FRAME_OK,
          "reduced-n frame must decode");
    CHECK(dec.count == 2,
          "4 x 500B must downgrade to n=2 (got %zu)", dec.count);
    CHECK(uxQueueMessagesWaiting(s_report_telemetry_q) == 2,
          "the two samples that did not fit must remain queued (got %u)",
          (unsigned)uxQueueMessagesWaiting(s_report_telemetry_q));
    drain_telemetry();

    /* 300 B x 4 + 一个 1024 B 的第五个：前 4 个成批，第 5 个留下。 */
    reset_capture();
    for (uint8_t i = 0; i < DATA_BATCH_MAX_SAMPLES; i++) {
        stash_payload(i, mid, sizeof(mid));
        report_desc_t d = { .channel_id = 3, .timestamp_us = 7000 + i * 10,
                            .sequence = 70 + i, .block_index = i, .len = sizeof(mid) };
        xQueueSend(s_report_telemetry_q, &d, 0);
    }
    stash_payload(0, big, sizeof(big));
    report_desc_t extra = { .channel_id = 3, .timestamp_us = 7040, .sequence = 74,
                            .block_index = 0, .len = sizeof(big) };
    xQueueSend(s_report_telemetry_q, &extra, 0);
    xQueueReceive(s_report_telemetry_q, &first, 0);
    CHECK(report_try_data_batch(s_report_telemetry_q, &first) == true, "must batch");
    CHECK(data_batch_decode(g_frame[0], g_frame_len[0], &dec) == FRAME_OK, "decode");
    CHECK(dec.count == 4, "must stop at the n that fits (got %zu)", dec.count);
    CHECK(uxQueueMessagesWaiting(s_report_telemetry_q) == 1,
          "the oversized extra sample must remain queued");
    drain_telemetry();
}

int main(void)
{
    /* 与 bus_worker_report_tests 同款：先建 report 路径（队列 + 池）。 */
    bus_worker_set_callbacks(NULL, capture_cb);
    bus_worker_set_channel_cmd_v2_final_cb(NULL);
    bus_worker_set_data_batch_cb(test_data_batch_cb);
    report_path_init();
    CHECK(s_report_path_started == true, "report_path_init must succeed");

    /* 让 report_payload_of() 读到的遥测池指向测试 payload：直接用它自己
     * 的静态数组即可 —— stash_payload() 写入的正是 s_telemetry_payload。 */

    test_caps_zero_never_batches();
    test_caps_set_batches_same_source();
    test_different_routing_not_batched();
    test_window_boundary();
    test_critical_and_max_n();
    test_buffer_pressure_downgrades_n();

    report_path_deinit();

    if (g_failures != 0) {
        fprintf(stderr, "%d/%d checks failed\n", g_failures, g_checks);
        return 1;
    }
    printf("bus_worker_data_batch_tests: all %d checks passed\n", g_checks);
    return 0;
}
