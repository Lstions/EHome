/* handler_device_op_tests.c —— 远程重启/恢复出厂的下行解析与上行 ACK
 *
 * 为什么单独一个宿主测试：这层做的是"设备侧会不会把结果如实、可关联地报回去"。
 * 错法的后果不是崩溃，而是**服务端被喂了错的信息**：
 *   - 不回显 request_id  -> 迟到的 ACK 会被算到下一次请求上 => 失败被报成成功
 *   - 畸形帧乱回一个 id  -> ACK 配到别的请求上
 *   - 未注入原语还执行   -> "重启"了但其实什么都没做
 *
 * 直接编译 components/msg_handler/handler_device_op.c，
 * 用假原语替换 nvs / esp_restart / 发送。
 */
#include <stdbool.h>
#include <stdio.h>
#include <string.h>

#include "device_op.h"
#include "frame_codec.h"
#include "msg_handler_device_op.h"

/* esp_log.h 的宿主桩要求提供这个符号（与其它宿主测试一致的口径）。 */
void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level; (void)tag; (void)format;
}

static int s_failures = 0;
#define CHECK(cond, ...)                                                     \
    do {                                                                     \
        if (!(cond)) {                                                       \
            printf("FAIL %s:%d  ", __FILE__, __LINE__);                      \
            printf(__VA_ARGS__);                                             \
            printf("\n");                                                    \
            s_failures++;                                                    \
        }                                                                    \
    } while (0)

/* ── 假原语 ── */
static int  s_erased_n;
static char s_erased[8][32];
static int  s_erase_fail;
static int  s_send_calls;
static uint8_t s_last_frame[256];
static size_t  s_last_len;
static int  s_restart_calls;

static int h_erase(const char *ns)
{
    if (s_erased_n < 8) snprintf(s_erased[s_erased_n++], 32, "%s", ns);
    return s_erase_fail ? -1 : 0;
}
static int h_send(const uint8_t *frame, size_t len)
{
    s_send_calls++;
    s_last_len = len;
    if (len <= sizeof(s_last_frame)) memcpy(s_last_frame, frame, len);
    return 0;
}
static void h_restart(void) { s_restart_calls++; }

static const device_op_hooks_t HOOKS = {
    .erase_namespace = h_erase,
    .send_frame = h_send,
    .restart = h_restart,
};

static void reset_all(void)
{
    s_erased_n = 0; s_erase_fail = 0; s_send_calls = 0; s_last_len = 0;
    s_restart_calls = 0;
    memset(s_last_frame, 0, sizeof(s_last_frame));
    device_op_reset_state();
}

static size_t build_op(uint8_t *buf, size_t cap, int op, const char *req_id)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, cap, MSG_DEVICE_OP);
    if (op >= 0) frame_encode_varint(&enc, 1, (uint64_t)op);
    if (req_id != NULL) frame_encode_string(&enc, 2, req_id);
    return frame_encoder_size(&enc);
}

typedef struct { bool have_result; uint64_t result; char req_id[80]; } sent_ack_t;

static bool parse_sent_ack(sent_ack_t *out)
{
    memset(out, 0, sizeof(*out));
    if (s_last_len == 0) return false;
    /* 编码器把类型字节放在 [0]；frame_decoder_t 只跳过它、不暴露它，
     * 所以直接读首字节（与 msg_handler_process 的 data[0] 同一口径）。 */
    if (s_last_frame[0] != MSG_DEVICE_OP_ACK) {
        printf("  (sent frame type=0x%02X, expected 0x%02X)\n",
               s_last_frame[0], MSG_DEVICE_OP_ACK);
        return false;
    }
    frame_decoder_t dec;
    if (frame_decoder_init(&dec, s_last_frame, s_last_len) != FRAME_OK) return false;
    frame_field_t f;
    while (frame_decoder_next(&dec, &f) == FRAME_OK) {
        if (f.field_num == 1) { out->result = f.value.varint; out->have_result = true; }
        else if (f.field_num == 2) {
            size_t n = f.value.bytes.len;
            if (n >= sizeof(out->req_id)) n = sizeof(out->req_id) - 1;
            memcpy(out->req_id, f.value.bytes.ptr, n);
            out->req_id[n] = '\0';
        }
    }
    return true;
}

static void feed_downlink(uint8_t *buf, size_t len)
{
    frame_decoder_t dec;
    if (frame_decoder_init(&dec, buf, len) != FRAME_OK) { CHECK(false, "decoder init"); return; }
    handler_device_op_process(&dec);
}

/* ============ 1. 重启：回 OK，且 request_id 原样回显 ============ */
static void test_reboot_acks_ok_with_same_request_id(void)
{
    reset_all();
    msg_handler_set_device_op_hooks(&HOOKS);
    uint8_t buf[128];
    size_t n = build_op(buf, sizeof(buf), DEVICE_OP_REBOOT, "op-abc-123");

    feed_downlink(buf, n);

    CHECK(s_send_calls == 1, "应恰好回一次 ACK，实际 %d", s_send_calls);
    sent_ack_t ack;
    CHECK(parse_sent_ack(&ack), "回上去的帧应能解成 0x23");
    CHECK(ack.have_result, "ACK 应带 result_code");
    CHECK(ack.result == DEVOP_OK, "重启应回 OK，实际 %d", (int)ack.result);
    CHECK(strcmp(ack.req_id, "op-abc-123") == 0,
          "**request_id 必须原样回显**，实际 [%s] —— 不回显的话服务端无法把 ACK "
          "配到具体那次请求上，一次迟到的 ACK 会被算到下一次头上（失败被报成成功）",
          ack.req_id);
    CHECK(s_restart_calls == 1, "重启原语应被调用一次，实际 %d", s_restart_calls);
    CHECK(s_erased_n == 0, "重启不应擦除任何命名空间，实际擦了 %d 个", s_erased_n);
}

/* ============ 2. 恢复出厂：只擦 config，**绝不擦 wifi_cfg** ============ */
static void test_factory_reset_keeps_wifi_and_acks_ok(void)
{
    reset_all();
    msg_handler_set_device_op_hooks(&HOOKS);
    uint8_t buf[128];
    size_t n = build_op(buf, sizeof(buf), DEVICE_OP_FACTORY_RESET_KEEP_CONN, "rid-1");

    feed_downlink(buf, n);

    bool has_wifi = false;
    for (int i = 0; i < s_erased_n; i++) {
        if (strcmp(s_erased[i], "wifi_cfg") == 0) has_wifi = true;
    }
    CHECK(!has_wifi, "**wifi_cfg 被远程擦除了** —— 需求明确禁止（擦了设备永久失联）");
    CHECK(s_restart_calls == 1, "恢复出厂成功后应重启，实际 %d", s_restart_calls);
    sent_ack_t ack;
    CHECK(parse_sent_ack(&ack) && ack.result == DEVOP_OK, "恢复出厂应回 OK");
}

/* ============ 3. 擦除失败：ACK 报 ERASE_FAILED，且**不重启** ============ */
static void test_erase_failure_is_reported_not_hidden(void)
{
    reset_all();
    msg_handler_set_device_op_hooks(&HOOKS);
    s_erase_fail = 1;
    uint8_t buf[128];
    size_t n = build_op(buf, sizeof(buf), DEVICE_OP_FACTORY_RESET_KEEP_CONN, "rid-2");

    feed_downlink(buf, n);

    sent_ack_t ack;
    CHECK(parse_sent_ack(&ack), "擦除失败也必须回 ACK（否则前端永远不知道）");
    CHECK(ack.result == DEVOP_ERR_ERASE_FAILED,
          "**擦除失败时 ACK 必须报 ERASE_FAILED**，实际 %d —— 报 OK 等于告诉操作员"
          "「恢复出厂成功」，而设备根本没擦没重启", (int)ack.result);
    CHECK(s_restart_calls == 0, "擦除失败不应重启");
    CHECK(strcmp(ack.req_id, "rid-2") == 0, "失败路径同样要回显 request_id");
}

/* ============ 4. 缺 op_code：回 UNKNOWN_OP 而不是沉默 ============ */
static void test_missing_op_reports_unknown(void)
{
    reset_all();
    msg_handler_set_device_op_hooks(&HOOKS);
    uint8_t buf[128];
    size_t n = build_op(buf, sizeof(buf), -1, "rid-3");

    feed_downlink(buf, n);

    sent_ack_t ack;
    CHECK(parse_sent_ack(&ack), "缺 op_code 也要回 ACK（让服务端知道设备不认识，"
                                "而不是等到超时、告诉操作员结果未知）");
    CHECK(ack.result == DEVOP_ERR_UNKNOWN_OP, "应回 UNKNOWN_OP，实际 %d", (int)ack.result);
    CHECK(s_restart_calls == 0, "不应重启");
}

/* ============ 5. 缺 request_id：丢弃，**不猜** ============ */
static void test_missing_request_id_is_dropped(void)
{
    reset_all();
    msg_handler_set_device_op_hooks(&HOOKS);
    uint8_t buf[128];
    size_t n = build_op(buf, sizeof(buf), DEVICE_OP_REBOOT, NULL);

    feed_downlink(buf, n);

    CHECK(s_send_calls == 0,
          "无 request_id 时应**丢弃**而不是乱回一个 —— 乱回的 ACK 会被配到别的"
          "请求上，比不回更糟");
    CHECK(s_restart_calls == 0, "缺 request_id 不应执行操作");
}

/* ============ 6. 未注入原语：拒绝执行 ============ */
static void test_without_hooks_it_refuses(void)
{
    reset_all();
    msg_handler_set_device_op_hooks(NULL);
    CHECK(!msg_handler_device_op_ready(), "清除后应报告未就绪");
    uint8_t buf[128];
    size_t n = build_op(buf, sizeof(buf), DEVICE_OP_REBOOT, "rid-4");

    feed_downlink(buf, n);

    CHECK(s_restart_calls == 0,
          "**未注入原语时不得执行** —— 否则'重启'看起来成功而设备什么都没做");
    CHECK(s_send_calls == 0, "未注入时也不该回 ACK（无法确认真的执行过）");
}

/* ============ 7. 未知操作码：回 UNKNOWN_OP，不擦不重启 ============ */
static void test_unknown_op_code(void)
{
    reset_all();
    msg_handler_set_device_op_hooks(&HOOKS);
    uint8_t buf[128];
    size_t n = build_op(buf, sizeof(buf), 99, "rid-5");

    feed_downlink(buf, n);

    sent_ack_t ack;
    CHECK(parse_sent_ack(&ack) && ack.result == DEVOP_ERR_UNKNOWN_OP,
          "未知操作码应回 UNKNOWN_OP");
    CHECK(s_erased_n == 0 && s_restart_calls == 0, "未知操作码不得有任何副作用");
}

/* ============ 8. 畸形字节流：不崩、不乱回 ============ */
static void test_malformed_bytes_do_not_crash(void)
{
    reset_all();
    msg_handler_set_device_op_hooks(&HOOKS);
    uint8_t buf[8] = { MSG_DEVICE_OP, 0x0A, 0x02, 0xFF, 0xFF, 0x00, 0x00, 0x00 };

    feed_downlink(buf, sizeof(buf));

    CHECK(s_restart_calls == 0, "畸形帧不得触发操作");
    printf("  (畸形帧处理完毕，未崩溃)\n");
}

int main(void)
{
    test_reboot_acks_ok_with_same_request_id();
    test_factory_reset_keeps_wifi_and_acks_ok();
    test_erase_failure_is_reported_not_hidden();
    test_missing_op_reports_unknown();
    test_missing_request_id_is_dropped();
    test_without_hooks_it_refuses();
    test_unknown_op_code();
    test_malformed_bytes_do_not_crash();

    if (s_failures) { printf("handler_device_op_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("handler_device_op_tests: all checks passed\n");
    return 0;
}
