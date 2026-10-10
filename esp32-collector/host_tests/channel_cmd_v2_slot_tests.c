/*
 * channel_cmd_v2_slot_tests.c
 *
 * Host tests for handler_channel_cmd_v2.c's RAM slot state machine
 * (F5 in the 边缘设备修复优化方案-v2 doc).  Covers the three core
 * branches the plain decoder suite does not exercise directly:
 *
 *   1. reserve_slot collision  -> -2 (same command_id, different identity)
 *      (handler_channel_cmd_v2.c:161-163 sends V2_ERR_COLLISION)
 *   2. Replayed FINAL re-transmit  (:311-320) — a replay of a command whose
 *      slot is V2_SLOT_FINAL must ACK(accepted) AND resend the Final frame
 *      with replayed=1 (field 9)
 *   3. CAS reject non-QUEUED complete  (:210-211) — completing a slot that
 *      is not V2_SLOT_QUEUED must be a no-op (no Final published)
 *
 * The handler source is included directly (weak symbols — the app bridge
 * callbacks — are overridden by strong definitions in this TU, exactly like
 * channel_cmd_v2_decoder_tests.c).
 *
 * Build:
 *   gcc -std=c11 -Wall -Wextra -Werror \
 *       -I stubs \
 *       -I../components/frame \
 *       -I../components/msg_handler \
 *       -o /tmp/test_v2_slot \
 *       channel_cmd_v2_slot_tests.c \
 *       ../components/frame/frame_codec.c \
 *       ../components/msg_handler/handler_channel_cmd_v2.c
 */

#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "msg_handler_internal.h"

/* ---- Shared capture state, defined in channel_cmd_v2_slot_app_stubs.c ---- */
#define V2T_PUBLISH_CAPTURE_MAX 4
extern unsigned v2t_callback_count;
extern uint8_t  v2t_last_slot;
extern unsigned v2t_publish_count;
extern uint8_t  v2t_published[V2T_PUBLISH_CAPTURE_MAX][256];
extern size_t   v2t_published_len[V2T_PUBLISH_CAPTURE_MAX];

/* The handler's weak app-bridge symbols are overridden by strong symbols in
 * channel_cmd_v2_slot_app_stubs.c (linked into this executable).  Include
 * the handler source here so the tests can drive the file-static slot table
 * directly (per the F5 host-test inclusion pattern). */
#include "../components/msg_handler/handler_channel_cmd_v2.c"

/* ---- Test helpers ---- */

static int g_failures;

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (message)); \
        g_failures++; \
    } \
} while (0)

static void reset_capture(void)
{
    v2t_publish_count = 0;
    v2t_callback_count = 0;
    v2t_last_slot = CHANNEL_CMD_V2_SLOT_NONE;
    memset(v2t_published, 0, sizeof(v2t_published));
    memset(v2t_published_len, 0, sizeof(v2t_published_len));
}

/* Slot state machine is pure RAM: memset the whole control table (plus the
 * counters that are file-static in the included handler).  ACK/complete
 * counters are updated via atomic ops, so a plain memset would race nothing
 * in host tests but resets them as well. */
static void reset_slot_state(void)
{
    for (int i = 0; i < CHANNEL_CMD_V2_SLOT_COUNT; i++) {
        memset(&s_slots[i], 0, sizeof(s_slots[i]));
        s_slots[i].state = V2_SLOT_FREE;
    }
    s_reservation_lock = 0;
    s_completed_sequence = 0;
    s_accepted_count = 0;
    s_rejected_count = 0;
    s_completed_count = 0;
    s_replayed_count = 0;
}

static void process(const uint8_t *buf, size_t len)
{
    frame_decoder_t dec;
    if (frame_decoder_init(&dec, buf, len) == FRAME_OK) {
        handler_channel_cmd_v2_process(&dec);
    }
}

/* Build one V2 command frame.  Batch steps are optional (plan). */
static size_t build_cmd(uint8_t *out, size_t cap,
                        const uint8_t command_id[16], const uint8_t digest[16],
                        uint32_t attempt, uint32_t edge_id, uint32_t channel_id,
                        uint64_t deadline, const uint8_t *plan_steps,
                        size_t plan_steps_len)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, out, cap, MSG_CHANNEL_CMD_V2);
    frame_encode_bytes(&enc, 1, command_id, 16);
    frame_encode_bytes(&enc, 2, digest, 16);
    frame_encode_varint(&enc, 3, attempt);
    frame_encode_string(&enc, 4, "boot-1");
    frame_encode_varint(&enc, 5, edge_id);
    frame_encode_varint(&enc, 6, channel_id);
    frame_encode_varint(&enc, 7, deadline);
    frame_encode_bytes(&enc, 8, (const uint8_t *)"x", 1);
    frame_encode_varint(&enc, 9, 0);
    frame_encode_varint(&enc, 10, 30);
    frame_encode_varint(&enc, 11, 5);
    frame_encode_varint(&enc, 12, 0);
    frame_encode_varint(&enc, 13, 0);
    frame_encode_varint(&enc, 14, 1);
    if (plan_steps && plan_steps_len) {
        if (frame_encode_bytes(&enc, 15, plan_steps, plan_steps_len) != FRAME_OK)
            return 0;
    }
    return frame_encoder_size(&enc);
}

static bool published_type_is(uint8_t frame_index, uint8_t type)
{
    if (frame_index >= V2T_PUBLISH_CAPTURE_MAX ||
        v2t_published_len[frame_index] < 1) return false;
    return v2t_published[frame_index][0] == type;
}

static bool published_field_equals(uint8_t frame_index, uint8_t field, uint64_t wanted)
{
    if (frame_index >= V2T_PUBLISH_CAPTURE_MAX) return false;
    frame_decoder_t dec;
    frame_field_t f;
    if (frame_decoder_init(&dec, v2t_published[frame_index],
                           v2t_published_len[frame_index]) != FRAME_OK)
        return false;
    while (frame_decoder_next(&dec, &f) == FRAME_OK) {
        if (f.field_num == field && f.wire_type == WIRE_VARINT &&
            f.value.varint == wanted) return true;
    }
    return false;
}

#define ACK_FIELD_EQUALS(field, value) published_field_equals(0, (field), (value))
#define FINAL_FIELD_EQUALS(field, value) published_field_equals(1, (field), (value))

/* =====================================================================
 * Test 1: reserve_slot collision (-2) — same command_id, different payload
 * identity  -> V2_ERR_COLLISION ack + slot stays usable for the replier.
 * ===================================================================== */
static void test_collision_same_command_id_different_identity(void)
{
    uint8_t cmd[320];
    uint8_t id_a[16], id_b[16], digest_a[16], digest_b[16];
    memset(id_a, 1, sizeof(id_a));
    memset(id_b, 1, sizeof(id_b));
    memset(digest_a, 0x11, sizeof(digest_a));
    memset(digest_b, 0x22, sizeof(digest_b));

    /* First command occupies a slot with identity A. */
    size_t n = build_cmd(cmd, sizeof(cmd), id_a, digest_a, 1, 1, 1,
                         1700000000000ULL, NULL, 0);
    reset_capture();
    process(cmd, n);
    CHECK(v2t_callback_count == 1, "first command must be admitted");
    CHECK(published_type_is(0, MSG_CHANNEL_CMD_V2_ACK), "first command must ACK");
    CHECK(ACK_FIELD_EQUALS(6, 1) && ACK_FIELD_EQUALS(7, 0),
          "first command ACK must be accepted");

    /* Same command_id but different digest → collision.  ACK error 1003. */
    n = build_cmd(cmd, sizeof(cmd), id_b, digest_b, 1, 1, 1,
                  1700000000000ULL, NULL, 0);
    reset_capture();
    process(cmd, n);
    CHECK(v2t_callback_count == 0, "colliding command must NOT dispatch");
    CHECK(published_type_is(0, MSG_CHANNEL_CMD_V2_ACK), "collision must ACK");
    CHECK(ACK_FIELD_EQUALS(6, 0), "collision ACK must be rejected");
    CHECK(ACK_FIELD_EQUALS(7, 1003), "collision must be V2_ERR_COLLISION");
}

/* =====================================================================
 * Test 2: reserve_slot collision across a completed(FINAL) slot
 * ===================================================================== */
static void test_collision_with_completed_final(void)
{
    uint8_t cmd[320];
    uint8_t id[16], digest[16];
    memset(id, 3, sizeof(id));
    memset(digest, 0x33, sizeof(digest));

    size_t n = build_cmd(cmd, sizeof(cmd), id, digest, 1, 2, 1,
                         1700000000000ULL, NULL, 0);
    reset_capture();
    process(cmd, n);
    CHECK(v2t_callback_count == 1, "command must be admitted");
    uint8_t slot = v2t_last_slot;
    CHECK(slot != CHANNEL_CMD_V2_SLOT_NONE, "command must have a slot");

    /* Complete it (FINAL). */
    static const uint8_t raw[] = {0xDE, 0xAD};
    handler_channel_cmd_v2_complete(slot, true, 0, raw, sizeof(raw));
    CHECK(s_slots[slot].state == V2_SLOT_FINAL, "slot must be FINAL after complete");

    /* Same command_id, different digest → -2 collision even against FINAL. */
    uint8_t other_digest[16];
    memset(other_digest, 0x44, sizeof(other_digest));
    n = build_cmd(cmd, sizeof(cmd), id, other_digest, 1, 2, 1,
                  1700000000000ULL, NULL, 0);
    reset_capture();
    process(cmd, n);
    CHECK(v2t_callback_count == 0, "collision vs FINAL must not dispatch");
    CHECK(ACK_FIELD_EQUALS(7, 1003), "collision vs FINAL must ACK 1003");
}

/* =====================================================================
 * Test 3: replay of a FINAL slot — ACK(accepted) + Final re-sent with
 * replayed=1 (field 9), carrying the stored raw response.
 * ===================================================================== */
static void test_replay_final_resends_final(void)
{
    uint8_t cmd[320];
    uint8_t id[16], digest[16];
    memset(id, 5, sizeof(id));
    memset(digest, 0x55, sizeof(digest));

    size_t n = build_cmd(cmd, sizeof(cmd), id, digest, 1, 3, 2,
                         1700000000000ULL, NULL, 0);
    reset_capture();
    process(cmd, n);
    CHECK(v2t_callback_count == 1, "original must be admitted");
    uint8_t slot = v2t_last_slot;
    CHECK(slot != CHANNEL_CMD_V2_SLOT_NONE, "original must have a slot");

    static const uint8_t raw[] = {0x01, 0x03, 0x02, 0x00, 0x14};
    handler_channel_cmd_v2_complete(slot, true, 0, raw, sizeof(raw));
    CHECK(s_slots[slot].state == V2_SLOT_FINAL, "slot must be FINAL");

    /* Same full identity replayed. */
    n = build_cmd(cmd, sizeof(cmd), id, digest, 1, 3, 2,
                  1700000000000ULL, NULL, 0);
    reset_capture();
    process(cmd, n);
    CHECK(v2t_callback_count == 0, "replayed command must NOT dispatch again");
    CHECK(published_type_is(0, MSG_CHANNEL_CMD_V2_ACK), "replay must ACK");
    CHECK(ACK_FIELD_EQUALS(6, 1), "replay ACK must be accepted");
    /* The handler then publishes the replay Final. */
    CHECK(v2t_publish_count >= 2, "replay must publish ACK + Final");
    /* Last published frame is the Final. */
    CHECK(FINAL_FIELD_EQUALS(9, 1), "Final must carry replayed=1");
    CHECK(FINAL_FIELD_EQUALS(6, 1), "Final must carry success");
}

/* =====================================================================
 * Test 4: replay of a QUEUED (not yet complete) slot — ACK(accepted) only,
 * NO Final (the result does not exist yet).  No replayed counter bump is
 * required on this path; the Final comes later with replayed=0.
 * ===================================================================== */
static void test_replay_queued_no_final(void)
{
    uint8_t cmd[320];
    uint8_t id[16], digest[16];
    memset(id, 6, sizeof(id));
    memset(digest, 0x66, sizeof(digest));

    size_t n = build_cmd(cmd, sizeof(cmd), id, digest, 1, 4, 1,
                         1700000000000ULL, NULL, 0);
    reset_capture();
    process(cmd, n);
    CHECK(v2t_callback_count == 1, "original must be admitted");
    uint8_t slot = v2t_last_slot;   /* capture BEFORE the next reset_capture */
    CHECK(slot != CHANNEL_CMD_V2_SLOT_NONE, "original must have a slot");
    CHECK(s_slots[slot].state == V2_SLOT_QUEUED, "slot must be QUEUED");

    n = build_cmd(cmd, sizeof(cmd), id, digest, 1, 4, 1,
                  1700000000000ULL, NULL, 0);
    reset_capture();
    process(cmd, n);
    CHECK(v2t_callback_count == 0, "replay must not dispatch twice");
    CHECK(published_type_is(0, MSG_CHANNEL_CMD_V2_ACK), "replay must ACK");
    CHECK(ACK_FIELD_EQUALS(6, 1), "replay of queued must be accepted");
    CHECK(v2t_publish_count == 1, "replay of queued must NOT publish a Final");
    CHECK(s_slots[slot].state == V2_SLOT_QUEUED,
          "replay must leave slot QUEUED");
}

/* =====================================================================
 * Test 5: CAS rejects a complete on a slot that is not QUEUED.
 * Completing CHANNEL_CMD_V2_SLOT_NONE is a no-op (no FINAL published,
 * no crash).  Also completes the last slot index to a free slot.
 * ===================================================================== */
static void test_complete_cas_rejects_non_queued(void)
{
    /* Directly drive the slot table — no need to go through process(). */
    reset_slot_state();

    uint8_t slot = 0;
    /* Slot 0 is FREE.  A complete must not transition it. */
    static const uint8_t raw[] = {0xAA};
    unsigned before = v2t_publish_count;
    handler_channel_cmd_v2_complete(slot, true, 0, raw, sizeof(raw));
    CHECK(v2t_publish_count == before, "complete on FREE must not publish");
    CHECK(s_slots[slot].state == V2_SLOT_FREE, "complete on FREE must not change state");

    /* Now directly force a FINAL state and complete it — also no-op. */
    __atomic_store_n(&s_slots[slot].state, V2_SLOT_FINAL, __ATOMIC_RELEASE);
    before = v2t_publish_count;
    handler_channel_cmd_v2_complete(slot, true, 0, raw, sizeof(raw));
    CHECK(v2t_publish_count == before, "complete on FINAL must not publish");
    CHECK(s_slots[slot].state == V2_SLOT_FINAL, "complete on FINAL must not change state");

    /* Fresh PROCESSED command (QUEUED) — completes normally. */
    uint8_t cmd[320];
    uint8_t id[16], digest[16];
    memset(id, 7, sizeof(id));
    memset(digest, 0x77, sizeof(digest));
    size_t n = build_cmd(cmd, sizeof(cmd), id, digest, 1, 5, 1,
                         1700000000000ULL, NULL, 0);
    reset_capture();
    process(cmd, n);
    uint8_t qslot = v2t_last_slot;
    before = v2t_publish_count;
    handler_channel_cmd_v2_complete(qslot, true, 0, raw, sizeof(raw));
    CHECK(v2t_publish_count == before + 1, "complete on QUEUED must publish Final");
    CHECK(s_slots[qslot].state == V2_SLOT_FINAL, "complete must transition to FINAL");
}

/* =====================================================================
 * Test 6: metrics reflect collision / replay / complete counters
 * ===================================================================== */
static void test_slot_metrics(void)
{
    reset_slot_state();
    reset_capture();

    uint8_t cmd[320];
    uint8_t id[16], digest[16], other[16];
    memset(id, 8, sizeof(id));
    memset(digest, 0x88, sizeof(digest));
    memset(other, 0x99, sizeof(other));

    size_t n = build_cmd(cmd, sizeof(cmd), id, digest, 1, 6, 1,
                         1700000000000ULL, NULL, 0);
    process(cmd, n); /* accepted */
    uint8_t slot = v2t_last_slot;

    n = build_cmd(cmd, sizeof(cmd), id, other, 1, 6, 1,
                  1700000000000ULL, NULL, 0);
    process(cmd, n); /* collision → rejected */

    handler_channel_cmd_v2_complete(slot, true, 0, (const uint8_t *)"x", 1);

    channel_cmd_v2_metrics_t m = {0};
    handler_channel_cmd_v2_get_metrics(&m);
    CHECK(m.accepted == 1, "metrics accepted must be 1");
    CHECK(m.rejected == 1, "metrics rejected must be 1");
    CHECK(m.completed == 1, "metrics completed must be 1");

    /* Replay of the FINAL slot bumps replayed. */
    n = build_cmd(cmd, sizeof(cmd), id, digest, 1, 6, 1,
                  1700000000000ULL, NULL, 0);
    reset_capture();
    process(cmd, n);
    handler_channel_cmd_v2_get_metrics(&m);
    CHECK(m.replayed == 1, "metrics replayed must be 1 after replay");
}


/* =====================================================================
 * ⭐ 方案 D（2026-10-10）：channel_cmd_v2_borrow_plan() 专项测试
 *
 * 该访问器是方案 D 的**安全支点**：队列元素不再内联 plan，bus_worker 改为
 * 按 control_slot 从槽位借用。它替代的是一根裸指针，因此必须证明：
 *   ① 正常路径能借到正确的 plan（字节/长度/步数）
 *   ② 槽位被复用后**拒绝**交出（裸指针方案在此会静默 UAF）
 *   ③ 没有 plan 的命令返回 false（调用方据此走单步路径）
 *   ④ 非法 slot / 空出参不崩
 *
 * ⚠ wire 语义（本测试踩过的坑，记录以免复犯）：字段 15 是
 *   **每个 step 一个字段**，不是"整个 plan 打包成一个字段"。
 *   handler_channel_cmd_v2.c 每遇到一个字段 15 就:
 *     validate_batch_step(f.bytes) → 追加到 plan_data（2 B 长度前缀）
 *     → plan_step_count++
 *   所以下面的 build_cmd_with_steps() 对每个 step 各编码一次字段 15。
 * ===================================================================== */

/* 构造一个合法的 batch step（protobuf 子帧，字段 1-5 全必填）。
 * 依据 validate_batch_step()：
 *   1=kind(0..3), 2=tx(bytes 1..128), 3=read_size(<=256), 4=timeout(>0), 5=delay。 */
static size_t build_valid_step(uint8_t *out, size_t cap,
                               uint32_t kind, const uint8_t *tx, size_t tx_len)
{
    frame_encoder_t enc;
    frame_encoder_init_sub(&enc, out, cap);
    if (frame_encode_varint(&enc, 1, kind) != FRAME_OK) return 0;
    if (frame_encode_bytes(&enc, 2, tx, tx_len) != FRAME_OK) return 0;
    if (frame_encode_varint(&enc, 3, 2) != FRAME_OK) return 0;
    if (frame_encode_varint(&enc, 4, 100) != FRAME_OK) return 0;
    if (frame_encode_varint(&enc, 5, 0) != FRAME_OK) return 0;
    return frame_encoder_size(&enc);
}

/* 构造带 N 步 plan 的 V2 命令帧（每步一个字段 15）。 */
static size_t build_cmd_with_steps(uint8_t *out, size_t cap,
                                   uint8_t id_seed,
                                   const uint8_t *step1, size_t n1,
                                   const uint8_t *step2, size_t n2)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, out, cap, MSG_CHANNEL_CMD_V2);
    uint8_t id[16], digest[16];
    memset(id, id_seed, sizeof(id));
    memset(digest, (int)(id_seed ^ 0x01), sizeof(digest));
    frame_encode_bytes(&enc, 1, id, 16);
    frame_encode_bytes(&enc, 2, digest, 16);
    frame_encode_varint(&enc, 3, 1);                       /* attempt */
    frame_encode_string(&enc, 4, "boot-1");                /* 与 stub 一致 */
    frame_encode_varint(&enc, 5, 7);                       /* edge_device_id */
    frame_encode_varint(&enc, 6, 1);                       /* channel_id */
    frame_encode_varint(&enc, 7, 1700000000000ULL);        /* deadline */
    frame_encode_bytes(&enc, 8, (const uint8_t *)"x", 1);  /* tx_data */
    frame_encode_varint(&enc, 9, 0);                       /* read_size */
    frame_encode_varint(&enc, 10, 30);                     /* rx_timeout_ms */
    frame_encode_varint(&enc, 11, 5);                      /* post_tx_delay_ms */
    frame_encode_varint(&enc, 12, 0);                      /* risk: 必须 0 */
    frame_encode_varint(&enc, 13, 0);                      /* flags: 必须 0 */
    frame_encode_varint(&enc, 14, 1);                      /* V2_PROTOCOL */
    /* ⚠ 每个 step 一个字段 15 */
    if (step1 && n1 && frame_encode_bytes(&enc, 15, step1, n1) != FRAME_OK) return 0;
    if (step2 && n2 && frame_encode_bytes(&enc, 15, step2, n2) != FRAME_OK) return 0;
    return frame_encoder_size(&enc);
}

/* 期望的 plan 字节布局（handler 侧会加 2 B 小端长度前缀）。 */
static size_t expected_plan(uint8_t *out, size_t cap,
                            const uint8_t *s1, size_t n1,
                            const uint8_t *s2, size_t n2)
{
    size_t p = 0;
    if (p + 2 + n1 > cap) return 0;
    out[p++] = (uint8_t)(n1 & 0xff); out[p++] = (uint8_t)(n1 >> 8);
    memcpy(out + p, s1, n1); p += n1;
    if (p + 2 + n2 > cap) return 0;
    out[p++] = (uint8_t)(n2 & 0xff); out[p++] = (uint8_t)(n2 >> 8);
    memcpy(out + p, s2, n2); p += n2;
    return p;
}

static void test_borrow_plan_happy_path(void)
{
    reset_slot_state();
    reset_capture();

    uint8_t s1[64], s2[64], cmd[320], want[256];
    static const uint8_t tx1[] = {0x01, 0x03, 0x00, 0x00};
    static const uint8_t tx2[] = {0x02, 0x04, 0x00, 0x01};
    size_t n1 = build_valid_step(s1, sizeof(s1), 1, tx1, sizeof(tx1));
    size_t n2 = build_valid_step(s2, sizeof(s2), 2, tx2, sizeof(tx2));
    CHECK(n1 > 0 && n2 > 0, "step encoding must succeed");
    size_t want_len = expected_plan(want, sizeof(want), s1, n1, s2, n2);
    CHECK(want_len > 0, "expected plan assembly must succeed");

    size_t n = build_cmd_with_steps(cmd, sizeof(cmd), 0xA1, s1, n1, s2, n2);
    CHECK(n > 0, "frame encoding must succeed");
    process(cmd, n);

    CHECK(v2t_last_slot != CHANNEL_CMD_V2_SLOT_NONE, "command must be accepted");
    uint8_t slot = v2t_last_slot;

    const uint8_t *got = NULL;
    size_t got_len = 0;
    uint8_t got_steps = 0;
    bool ok = channel_cmd_v2_borrow_plan(slot, &got, &got_len, &got_steps);
    CHECK(ok, "borrow must succeed while the slot is QUEUED");
    CHECK(got_len == want_len, "borrowed length must equal the encoded plan length");
    CHECK(got_steps == 2, "borrowed step count must be 2");
    CHECK(got != NULL && memcmp(got, want, want_len) == 0,
          "borrowed bytes must equal the length-prefixed plan");
}

/* ⭐ 核心负例：槽位被释放/复用后，访问器必须**拒绝**。
 * 这正是裸指针方案会静默 use-after-free 的场景。 */
static void test_borrow_plan_rejects_released_slot(void)
{
    reset_slot_state();
    reset_capture();

    uint8_t s1[64], s2[64], cmd[320];
    static const uint8_t tx1[] = {0x01, 0x03, 0x00, 0x00};
    static const uint8_t tx2[] = {0x02, 0x04, 0x00, 0x01};
    size_t n1 = build_valid_step(s1, sizeof(s1), 1, tx1, sizeof(tx1));
    size_t n2 = build_valid_step(s2, sizeof(s2), 2, tx2, sizeof(tx2));
    CHECK(n1 > 0 && n2 > 0, "step encoding must succeed");

    size_t n = build_cmd_with_steps(cmd, sizeof(cmd), 0xA2, s1, n1, s2, n2);
    process(cmd, n);
    CHECK(v2t_last_slot != CHANNEL_CMD_V2_SLOT_NONE, "command must be accepted");
    uint8_t slot = v2t_last_slot;

    const uint8_t *got = NULL;
    size_t got_len = 0;
    uint8_t got_steps = 0;
    CHECK(channel_cmd_v2_borrow_plan(slot, &got, &got_len, &got_steps),
          "first borrow must succeed");

    /* 完成它（槽位 → FINAL，可被 reserve_slot 复用），再标记为 FREE。 */
    handler_channel_cmd_v2_complete(slot, true, 0, (const uint8_t *)"y", 1);
    s_slots[slot].state = V2_SLOT_FREE;

    got = NULL; got_len = 0; got_steps = 0;
    bool ok = channel_cmd_v2_borrow_plan(slot, &got, &got_len, &got_steps);
    CHECK(!ok, "borrow MUST refuse a slot that is no longer QUEUED");
    CHECK(got == NULL && got_len == 0 && got_steps == 0,
          "refused borrow must zero its outputs");
}

/* 没有 plan 的命令：返回 false，供调用方判定走单步路径。 */
static void test_borrow_plan_no_plan_returns_false(void)
{
    reset_slot_state();
    reset_capture();

    uint8_t cmd[192], id[16], digest[16];
    memset(id, 0x77, sizeof(id));
    memset(digest, 0x78, sizeof(digest));
    size_t n = build_cmd(cmd, sizeof(cmd), id, digest, 1, 8, 1,
                         1700000000000ULL, NULL, 0);   /* 无 plan */
    process(cmd, n);
    CHECK(v2t_last_slot != CHANNEL_CMD_V2_SLOT_NONE, "command must be accepted");

    const uint8_t *got = NULL;
    size_t got_len = 0;
    uint8_t got_steps = 0;
    CHECK(!channel_cmd_v2_borrow_plan(v2t_last_slot, &got, &got_len, &got_steps),
          "a plan-less command must report false");
    CHECK(got == NULL && got_len == 0 && got_steps == 0, "outputs must stay zeroed");
}

/* 非法参数：不崩、不交出任何东西。 */
static void test_borrow_plan_rejects_bad_args(void)
{
    reset_slot_state();
    reset_capture();

    const uint8_t *got = (const uint8_t *)0x1;
    size_t got_len = 99;
    uint8_t got_steps = 77;

    CHECK(!channel_cmd_v2_borrow_plan(CHANNEL_CMD_V2_SLOT_COUNT, &got, &got_len, &got_steps),
          "out-of-range slot must be refused");
    CHECK(got == NULL && got_len == 0 && got_steps == 0,
          "out-of-range slot must zero outputs");

    CHECK(!channel_cmd_v2_borrow_plan(0, NULL, &got_len, &got_steps),
          "NULL out_plan must be refused");
    CHECK(!channel_cmd_v2_borrow_plan(0, &got, NULL, &got_steps),
          "NULL out_len must be refused");
    CHECK(!channel_cmd_v2_borrow_plan(0, &got, &got_len, NULL),
          "NULL out_steps must be refused");

    got = (const uint8_t *)0x1; got_len = 99; got_steps = 77;
    CHECK(!channel_cmd_v2_borrow_plan(0, &got, &got_len, &got_steps),
          "a FREE slot must not hand out a plan");
    CHECK(got == NULL && got_len == 0 && got_steps == 0, "FREE slot must zero outputs");
}

/* =====================================================================
 * Main
 * ===================================================================== */
int main(void)
{
    reset_slot_state();
    reset_capture();

    test_collision_same_command_id_different_identity();
    test_collision_with_completed_final();
    test_replay_final_resends_final();
    test_replay_queued_no_final();
    test_complete_cas_rejects_non_queued();
    test_slot_metrics();
    test_borrow_plan_happy_path();
    test_borrow_plan_rejects_released_slot();
    test_borrow_plan_no_plan_returns_false();
    test_borrow_plan_rejects_bad_args();

    if (g_failures > 0) {
        fprintf(stderr, "\nchannel_cmd_v2_slot_tests: %d FAILURES\n", g_failures);
        return 1;
    }
    puts("channel_cmd_v2_slot_tests: all tests passed");
    return 0;
}
