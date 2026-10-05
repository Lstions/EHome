/*
 * handler_data_tests.c
 *
 * Host tests for handler_data.c — StatusReport/DataReport/OtaProg encoding.
 * Covers the multi-bus buffer expansion (512→1400) that prevents the
 * status-task stack overflow regression (实机 panic root cause).
 *
 * Coverage:
 *   1. msg_handler_send_status — 1400B buffer holds full perf sub-message
 *   2. msg_handler_send_data_report — 512B and 1024B payloads encode OK
 *   3. msg_handler_send_ota_prog — basic encoding
 *   4. handler_data_process_ota — valid and malformed OTA commands
 */

#include <stdio.h>
#include <string.h>
#include <stdlib.h>
#include <stdint.h>

/* ---- Stub headers ---- */
#include "freertos/FreeRTOS.h"
#include "freertos/queue.h"
#include "freertos/semphr.h"
#include "freertos/task.h"
#include "esp_err.h"
#include "esp_log.h"
#include "esp_system.h"
#include "driver/uart.h"
#include "driver/spi_master.h"
#include "driver/i2c_master.h"

/* ---- Component headers ---- */
#include "frame_codec.h"
#include "msg_handler.h"
#include "msg_handler_internal.h"
#include "data_report_codec.h"
#include "data_batch_codec.h"
#include "config_mgr.h"
#include "sync_manager.h"
#include "scheduler.h"
#include "bus_worker.h"
#include "ota.h"
#include "wifi_mgr.h"

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
 * Captured publish output
 * ===================================================================== */
static uint8_t g_published[2048];
static size_t g_published_len = 0;
static int g_publish_count = 0;

/* =====================================================================
 * ESP stubs
 * ===================================================================== */
void host_test_log_record(char level, const char *tag, const char *format, ...) {
    (void)level; (void)tag; (void)format;
}
const char *esp_err_to_name(esp_err_t err) { (void)err; return "ESP_ERR"; }
void esp_restart(void) {}
size_t esp_get_free_heap_size(void) { return 200000; }
size_t esp_get_minimum_free_heap_size(void) { return 150000; }

/* =====================================================================
 * msg_handler publish stubs — capture output
 * ===================================================================== */
esp_err_t msg_handler_publish_checked(const uint8_t *data, size_t len) {
    if (len <= sizeof(g_published)) {
        memcpy(g_published, data, len);
        g_published_len = len;
    }
    g_publish_count++;
    return ESP_OK;
}
void msg_handler_publish(const uint8_t *data, size_t len) {
    if (len <= sizeof(g_published)) {
        memcpy(g_published, data, len);
        g_published_len = len;
    }
    g_publish_count++;
}

/* =====================================================================
 * config_mgr stubs
 * ===================================================================== */
static uint64_t g_test_epoch = 42;
static const char *g_test_manifest_id = "v2-abc123";
static config_manifest_t g_test_manifest;
static bool g_test_manifest_valid = false;

uint64_t config_mgr_get_epoch(void) { return g_test_epoch; }
const char *config_mgr_get_manifest_id(void) { return g_test_manifest_id; }
const config_manifest_t *config_mgr_get_manifest(void) {
    return g_test_manifest_valid ? &g_test_manifest : NULL;
}
const config_template_t *config_mgr_get_template(uint32_t id) { (void)id; return NULL; }

/* =====================================================================
 * sync_manager stubs
 * ===================================================================== */
sync_state_enum_t sync_manager_get_state_enum(void) { return SYNC_STATE_IDLE; }

/* =====================================================================
 * scheduler stubs
 * ===================================================================== */
void scheduler_get_performance(scheduler_performance_t *out) {
    if (out) { out->min_queue_spaces = 12; out->stack_high_water_words = 3000; }
}
void scheduler_get_queue_metrics(scheduler_queue_metrics_t *out) {
    if (out) memset(out, 0, sizeof(*out));
}

/* =====================================================================
 * bus_worker stubs
 * ===================================================================== */
uint32_t bus_worker_get_min_stack_watermark(void) { return 2048; }
uint32_t bus_worker_get_report_drop_count(void) { return 3; }
uint32_t bus_worker_get_report_queue_high_water(void) { return 7; }

/* =====================================================================
 * channel_cmd_v2 metrics stub
 * ===================================================================== */
void handler_channel_cmd_v2_get_metrics(channel_cmd_v2_metrics_t *m) {
    if (m) { m->accepted = 10; m->rejected = 1; m->completed = 9; m->replayed = 2; }
}

/* =====================================================================
 * wifi_mgr stubs — controllable RSSI
 * ===================================================================== */
static int g_mock_rssi_dbm = 0;   /* 0 => disconnected / no WiFi data */

int wifi_mgr_get_rssi_dbm(void) { return g_mock_rssi_dbm; }

/* =====================================================================
 * OTA stubs
 * ===================================================================== */
static int g_ota_start_count = 0;
static int g_ota_replay_count = 0;
static ota_cmd_class_t g_ota_class = OTA_CMD_NEW;

ota_cmd_class_t ota_classify_cmd(const ota_cmd_t *cmd) { (void)cmd; return g_ota_class; }
esp_err_t ota_start(const ota_cmd_t *cmd) { (void)cmd; g_ota_start_count++; return ESP_OK; }
void ota_replay_last_progress(const char *id) { (void)id; g_ota_replay_count++; }
void ota_forget_duplicate(const char *id) { (void)id; }

/* =====================================================================
 * Include handler_data.c directly
 * ===================================================================== */
#include "../components/msg_handler/handler_data.c"

/* =====================================================================
 * Test helpers
 * ===================================================================== */
static void reset_publish(void) {
    g_published_len = 0;
    g_publish_count = 0;
    memset(g_published, 0, sizeof(g_published));
}

/* =====================================================================
 * Test 1: StatusReport encodes successfully with full perf sub-message
 * ===================================================================== */
static void test_status_report_full_perf(void) {
    reset_publish();
    g_test_manifest_valid = false;

    esp_err_t r = msg_handler_send_status(3600, "running", 2, NULL);
    CHECK(r == ESP_OK, "send_status should succeed");
    CHECK(g_publish_count == 1, "should publish exactly once");
    CHECK(g_published_len > 0, "published frame should not be empty");
    CHECK(g_published_len <= 1400, "published frame must fit in 1400B buffer");

    /* Verify frame starts with MSG_STATUS_RPT */
    CHECK(g_published[0] == MSG_STATUS_RPT, "frame type should be STATUS_RPT");

    /* Decode and verify perf sub-message is present (field 9) */
    frame_decoder_t dec;
    frame_field_t field;
    bool found_perf = false;
    bool found_control = false;
    frame_decoder_init(&dec, g_published, g_published_len);
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == STATUS_RPT_F_RUNTIME_PERF) found_perf = true;
        if (field.field_num == STATUS_RPT_F_CONTROL_STATS) found_control = true;
    }
    CHECK(found_perf, "StatusReport should contain runtime perf (field 9)");
    CHECK(found_control, "StatusReport should contain control stats (field 10)");
}

/* =====================================================================
 * Test 2: StatusReport perf sub-message contains all 27 fields
 * ===================================================================== */
static void test_status_report_perf_field_count(void) {
    reset_publish();
    g_test_manifest_valid = false;

    msg_handler_send_status(100, "ok", 0, NULL);

    /* Extract perf sub-message (field 9) */
    frame_decoder_t dec;
    frame_field_t field;
    frame_decoder_init(&dec, g_published, g_published_len);
    uint8_t perf_data[256] = {0};
    size_t perf_len = 0;
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == STATUS_RPT_F_RUNTIME_PERF &&
            field.wire_type == WIRE_LENGTH_DELIMITED) {
            perf_len = field.value.bytes.len;
            if (perf_len <= sizeof(perf_data))
                memcpy(perf_data, field.value.bytes.ptr, perf_len);
            break;
        }
    }
    CHECK(perf_len > 0, "perf sub-message should be present");

    /* Count fields in perf sub-message: expect 7 base + 5*4 queue + rssi = 28 */
    frame_decoder_t pdec;
    frame_field_t pfield;
    frame_decoder_init_sub(&pdec, perf_data, perf_len);
    int field_count = 0;
    int max_field_num = 0;
    while (frame_decoder_next(&pdec, &pfield) == FRAME_OK) {
        field_count++;
        if ((int)pfield.field_num > max_field_num)
            max_field_num = (int)pfield.field_num;
    }
    CHECK(field_count == 28, "perf sub-message should have 28 fields (7 base + 20 queue + rssi)");
    CHECK(max_field_num == 28, "highest field number should be 28 (wifi rssi)");
}

/* =====================================================================
 * Test 2b: StatusReport perf field 28 carries |RSSI|; 0 when disconnected
 * ===================================================================== */
static uint64_t extract_perf_field28(const uint8_t *frame, size_t frame_len,
                                     bool *found) {
    *found = false;
    frame_decoder_t dec;
    frame_field_t field;
    frame_decoder_init(&dec, frame, frame_len);
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == STATUS_RPT_F_RUNTIME_PERF &&
            field.wire_type == WIRE_LENGTH_DELIMITED) {
            frame_decoder_t pdec;
            frame_field_t pfield;
            frame_decoder_init_sub(&pdec, field.value.bytes.ptr,
                                   field.value.bytes.len);
            while (frame_decoder_next(&pdec, &pfield) == FRAME_OK) {
                if (pfield.field_num == 28 && pfield.wire_type == WIRE_VARINT) {
                    *found = true;
                    return pfield.value.varint;
                }
            }
        }
    }
    return 0;
}

static void test_status_report_rssi_field28(void) {
    bool found;
    uint64_t v;

    /* Connected at -55 dBm -> wire value 55 */
    reset_publish();
    g_test_manifest_valid = false;
    g_mock_rssi_dbm = -55;
    msg_handler_send_status(100, "ok", 0, NULL);
    v = extract_perf_field28(g_published, g_published_len, &found);
    CHECK(found, "field 28 must be present when RSSI available");
    CHECK(v == 55, "field 28 should encode abs(RSSI): -55 dBm -> 55");

    /* Connected at -92 dBm -> wire value 92 */
    reset_publish();
    g_mock_rssi_dbm = -92;
    msg_handler_send_status(100, "ok", 0, NULL);
    v = extract_perf_field28(g_published, g_published_len, &found);
    CHECK(found, "field 28 must be present");
    CHECK(v == 92, "field 28 should encode abs(RSSI): -92 dBm -> 92");

    /* Disconnected / query failed -> wire value 0 ("no WiFi data") */
    reset_publish();
    g_mock_rssi_dbm = 0;
    msg_handler_send_status(100, "ok", 0, NULL);
    v = extract_perf_field28(g_published, g_published_len, &found);
    CHECK(found, "field 28 must still be present when disconnected");
    CHECK(v == 0, "field 28 should be 0 when no WiFi data");

    g_mock_rssi_dbm = 0;
}

/* =====================================================================
 * Test 3: StatusReport with channel health
 * ===================================================================== */
static void test_status_report_channel_health(void) {
    reset_publish();
    g_test_manifest_valid = false;

    /* Build a scheduler state with one unhealthy channel */
    static sched_channel_t channels[1];
    memset(channels, 0, sizeof(channels));
    channels[0].active = true;
    channels[0].config.id = 42;
    channels[0].edge_device_count = 1;
    channels[0].edge_devices[0].edge_device_id = 7;
    channels[0].edge_devices[0].command_count = 1;
    channels[0].edge_devices[0].commands[0].enabled = true;
    channels[0].edge_devices[0].commands[0].error_count = 5;

    scheduler_state_t state = { .channels = channels, .channel_count = 1 };

    msg_handler_send_status(200, "running", 1, &state);
    CHECK(g_publish_count == 1, "should publish");

    /* Verify channel health field 7 is present */
    frame_decoder_t dec;
    frame_field_t field;
    frame_decoder_init(&dec, g_published, g_published_len);
    bool found_health = false;
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == STATUS_RPT_F_CH_HEALTH) found_health = true;
    }
    CHECK(found_health, "StatusReport should contain channel health (field 7)");
}

/* =====================================================================
 * Test 4: DataReport with 512-byte payload (fixed block size)
 * ===================================================================== */
static void test_data_report_512_payload(void) {
    reset_publish();

    uint8_t payload[512];
    memset(payload, 0xAB, sizeof(payload));

    msg_handler_send_data_report(100, 1000000, 1, payload, 512,
                                 0, 0, 0, 0, 0);
    CHECK(g_publish_count == 1, "512B DataReport should publish");
    CHECK(g_published_len > 512, "frame should be larger than payload (has headers)");
    CHECK(g_published_len <= 1400, "frame must fit in 1400B buffer");
    CHECK(g_published[0] == MSG_DATA_RPT, "frame type should be DATA_RPT");
}

/* =====================================================================
 * Test 5: DataReport with 1024-byte payload (full block pool size)
 * ===================================================================== */
static void test_data_report_1024_payload(void) {
    reset_publish();

    uint8_t payload[1024];
    memset(payload, 0xCD, sizeof(payload));

    msg_handler_send_data_report(200, 2000000, 5, payload, 1024,
                                 0, 0, 0, 0, 0);
    CHECK(g_publish_count == 1, "1024B DataReport should publish");
    CHECK(g_published_len > 1024, "frame should be larger than payload");
    CHECK(g_published_len <= 1400, "frame must fit in 1400B buffer");
}

/* =====================================================================
 * Test 6: DataReport with routing metadata
 * ===================================================================== */
static void test_data_report_with_routing(void) {
    reset_publish();

    uint8_t payload[8] = {1,2,3,4,5,6,7,8};
    msg_handler_send_data_report(300, 3000000, 10, payload, 8,
                                 0x01, 42, 7, 3, 1);
    CHECK(g_publish_count == 1, "DataReport with routing should publish");

    /* Decode and verify routing fields */
    frame_decoder_t dec;
    frame_field_t field;
    frame_decoder_init(&dec, g_published, g_published_len);
    bool found_error = false, found_rid = false, found_edge = false;
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == 6 && field.wire_type == WIRE_VARINT) found_error = true;
        if (field.field_num == 7 && field.wire_type == WIRE_VARINT) found_rid = true;
        if (field.field_num == 8 && field.wire_type == WIRE_VARINT) found_edge = true;
    }
    CHECK(found_error, "error_code field should be present");
    CHECK(found_rid, "request_id field should be present");
    CHECK(found_edge, "edge_device_id field should be present");
}

/* =====================================================================
 * Test 7: OtaProg encoding
 * ===================================================================== */
static void test_ota_prog_encoding(void) {
    reset_publish();

    msg_handler_send_ota_prog("ota-123", 1, 50, NULL);
    CHECK(g_publish_count == 1, "OtaProg should publish");
    CHECK(g_published[0] == MSG_OTA_PROG, "frame type should be OTA_PROG");
    CHECK(g_published_len < 256, "OtaProg should fit in 256B buffer");

    /* With error message */
    reset_publish();
    msg_handler_send_ota_prog("ota-456", 2, 0, "checksum mismatch");
    CHECK(g_publish_count == 1, "OtaProg with error should publish");
}

/* =====================================================================
 * Test 8: OTA command processing — valid command
 * ===================================================================== */
static void test_ota_cmd_valid(void) {
    reset_publish();
    g_ota_start_count = 0;
    g_ota_replay_count = 0;
    g_ota_class = OTA_CMD_NEW;

    /* Build a valid OtaCmd frame */
    uint8_t buf[512];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_OTA_CMD);
    frame_encode_string(&enc, 1, "fw-001");
    frame_encode_string(&enc, 2, "http://example.com/fw.bin");
    frame_encode_string(&enc, 3, "abc123def456");
    frame_encode_varint(&enc, 4, 1048576);
    frame_encode_string(&enc, 5, "1.2.3");
    frame_encode_varint(&enc, 6, 1);

    frame_decoder_t dec;
    frame_decoder_init(&dec, frame_encoder_data(&enc),
                       frame_encoder_size(&enc));
    handler_data_process_ota(&dec);

    CHECK(g_ota_start_count == 1, "valid OTA cmd should trigger ota_start");
    CHECK(g_ota_replay_count == 0, "new cmd should not replay");
}

/* =====================================================================
 * Test 9: OTA command processing — duplicate replay
 * ===================================================================== */
static void test_ota_cmd_replay(void) {
    reset_publish();
    g_ota_start_count = 0;
    g_ota_replay_count = 0;
    g_ota_class = OTA_CMD_EXACT_REPLAY;

    uint8_t buf[512];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_OTA_CMD);
    frame_encode_string(&enc, 1, "fw-001");
    frame_encode_string(&enc, 2, "http://example.com/fw.bin");
    frame_encode_string(&enc, 3, "abc123def456");
    frame_encode_varint(&enc, 4, 1048576);
    frame_encode_string(&enc, 5, "1.2.3");
    frame_encode_varint(&enc, 6, 1);

    frame_decoder_t dec;
    frame_decoder_init(&dec, frame_encoder_data(&enc),
                       frame_encoder_size(&enc));
    handler_data_process_ota(&dec);

    CHECK(g_ota_start_count == 0, "replay should not trigger ota_start");
    CHECK(g_ota_replay_count == 1, "replay should trigger replay_last_progress");
}

/* =====================================================================
 * Test 10: OTA command processing — malformed (missing fields)
 * ===================================================================== */
static void test_ota_cmd_malformed(void) {
    reset_publish();
    g_ota_start_count = 0;

    /* Missing URL field */
    uint8_t buf[256];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_OTA_CMD);
    frame_encode_string(&enc, 1, "fw-001");
    /* no field 2 (URL) */
    frame_encode_string(&enc, 3, "abc123");
    frame_encode_varint(&enc, 4, 1024);
    frame_encode_string(&enc, 5, "1.0");
    frame_encode_varint(&enc, 6, 1);

    frame_decoder_t dec;
    frame_decoder_init(&dec, frame_encoder_data(&enc),
                       frame_encoder_size(&enc));
    handler_data_process_ota(&dec);

    CHECK(g_ota_start_count == 0, "malformed OTA cmd should be rejected");
}

/* =====================================================================
 * Test 11: StatusReport with sync_id in manifest
 * ===================================================================== */
static void test_status_report_sync_id(void) {
    reset_publish();
    memset(&g_test_manifest, 0, sizeof(g_test_manifest));
    strcpy(g_test_manifest.sync_id, "sync-xyz-789");
    g_test_manifest_valid = true;

    msg_handler_send_status(500, "ok", 0, NULL);
    CHECK(g_publish_count == 1, "should publish");

    /* Verify field 8 (sync_id) is present */
    frame_decoder_t dec;
    frame_field_t field;
    frame_decoder_init(&dec, g_published, g_published_len);
    bool found_sync = false;
    while (frame_decoder_next(&dec, &field) == FRAME_OK) {
        if (field.field_num == 8 && field.wire_type == WIRE_LENGTH_DELIMITED) {
            found_sync = true;
            CHECK(field.value.bytes.len == strlen("sync-xyz-789"),
                  "sync_id length should match");
        }
    }
    CHECK(found_sync, "StatusReport should contain sync_id (field 8)");

    g_test_manifest_valid = false;
}

/* =====================================================================
 * V3-2a: msg_handler_send_data_batch() —— **生产代码路径**的端到端锚点。
 *
 * 为什么这条必须有：bus_worker_data_batch_tests 用测试自带的编码回调，
 * data_batch_codec_tests 直接调编码器。只有这条用例走的是固件真正运行的
 * 那个函数（msg_handler_send_data_batch，main.c 注入给 bus_worker 的回调），
 * 因此它同时验证了"1400B 预算够用""签名与回调契约一致""发布的是 0x20"。
 * ===================================================================== */
static void test_send_data_batch_production_path(void)
{
    static const uint8_t raw0[] = { 0x01, 0x03, 0x02, 0x00, 0x00, 0xb8, 0x44 };
    static const uint8_t raw1[] = { 0x02, 0x04, 0xaa, 0xbb, 0xcc, 0xdd };
    /* delta = 10,000 us —— 与 v3-backend 的锚点向量 A 完全一致
     * （100 Hz 采样间隔的 10 倍，便于肉眼区分 delta 字段）。 */
    const uint64_t ts[2] = { 1700000000000ULL, 1700000010000ULL };
    const uint8_t *raw[2] = { raw0, raw1 };
    const size_t lens[2] = { sizeof(raw0), sizeof(raw1) };

    g_published_len = 0;
    g_publish_count = 0;
    CHECK(msg_handler_send_data_batch(3, 4096, ts, raw, lens, 2, 42, 7, 1) == true,
          "send_data_batch must report success");
    CHECK(g_publish_count == 1, "send_data_batch must publish exactly once");
    CHECK(g_published_len == 47, "published frame must be 47 bytes");
    CHECK(g_published[0] == MSG_DATA_BATCH, "published frame must be 0x20");

    /* 与 v3-backend 共享的锚点字节，逐字节比对。 */
    static const uint8_t expect[47] = {
        0x20, 0x08, 0x02, 0x10, 0x80, 0xd0, 0x95, 0xff, 0xbc, 0x31,
        0x18, 0x80, 0x20, 0x20, 0x03, 0x2a, 0x0b, 0x08, 0x00, 0x12,
        0x07, 0x01, 0x03, 0x02, 0x00, 0x00, 0xb8, 0x44, 0x2a, 0x0b,
        0x08, 0x90, 0x4e, 0x12, 0x06, 0x02, 0x04, 0xaa, 0xbb, 0xcc,
        0xdd, 0x30, 0x2a, 0x38, 0x07, 0x40, 0x01,
    };
    CHECK(g_published_len == sizeof(expect), "anchor length must match");
    if (g_published_len == sizeof(expect)) {
        CHECK(memcmp(g_published, expect, sizeof(expect)) == 0,
              "production frame must match the backend-shared anchor byte-for-byte");
    }

    data_batch_decoded_t dec;
    CHECK(data_batch_decode(g_published, g_published_len, &dec) == FRAME_OK,
          "production frame must decode");
    CHECK(dec.count == 2 && dec.samples[1].delta_us == 10000,
          "production frame sample semantics");

    /* 契约 §2.2：4 x 300B 必须仍能放进 1400B 的生产缓冲。
     * deltas = 0/10,000/20,000/30,000 us，正好是 100 Hz 通道 4 个样本的形态。 */
    static uint8_t mid[300];
    memset(mid, 0x33, sizeof(mid));
    const uint64_t ts4[4] = { 1000, 11000, 21000, 31000 };
    const uint8_t *raw4[4] = { mid, mid, mid, mid };
    const size_t len4[4] = { sizeof(mid), sizeof(mid), sizeof(mid), sizeof(mid) };
    g_published_len = 0; g_publish_count = 0;
    CHECK(msg_handler_send_data_batch(1, 1, ts4, raw4, len4, 4, 0, 0, 0) == true,
          "4 x 300B must fit the production 1400B buffer");
    CHECK(g_published_len == 1247,
          "4 x 300B production frame must be 1247 bytes");
    CHECK(data_batch_decode(g_published, g_published_len, &dec) == FRAME_OK &&
              dec.count == 4,
          "4 x 300B production frame must decode with count=4");

    /* 非法输入必须被拒绝且**不发布**（fail-closed）。 */
    g_publish_count = 0;
    const uint64_t bad_ts[2] = { 2000, 1000 };  /* 时间戳倒退 */
    CHECK(msg_handler_send_data_batch(3, 1, bad_ts, raw, lens, 2, 0, 0, 0) == false,
          "backwards timestamps must be rejected");
    CHECK(g_publish_count == 0, "a rejected batch must not publish anything");

    const size_t zero_len[2] = { 0, sizeof(raw1) };
    CHECK(msg_handler_send_data_batch(3, 1, ts, raw, zero_len, 2, 0, 0, 0) == false,
          "empty raw_data must be rejected");
    CHECK(g_publish_count == 0, "a rejected batch must not publish anything");

    /* count 越界（1 / 5）不得通过。 */
    CHECK(msg_handler_send_data_batch(3, 1, ts, raw, lens, 1, 0, 0, 0) == false,
          "count=1 must not go through the batch path");
    CHECK(msg_handler_send_data_batch(3, 1, ts, raw, lens, 5, 0, 0, 0) == false,
          "count=5 must be rejected");
}

/* =====================================================================
 * Main
 * ===================================================================== */
int main(void)
{
    test_status_report_full_perf();
    test_status_report_perf_field_count();
    test_status_report_rssi_field28();
    test_status_report_channel_health();
    test_data_report_512_payload();
    test_data_report_1024_payload();
    test_data_report_with_routing();
    test_ota_prog_encoding();
    test_ota_cmd_valid();
    test_ota_cmd_replay();
    test_ota_cmd_malformed();
    test_status_report_sync_id();
    test_send_data_batch_production_path();

    if (g_failures > 0) {
        fprintf(stderr, "\nhandler_data_tests: %d FAILURES\n", g_failures);
        return 1;
    }
    puts("handler_data_tests: all tests passed");
    return 0;
}
