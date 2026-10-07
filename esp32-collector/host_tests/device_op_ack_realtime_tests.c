/* device_op_ack_realtime_tests.c —— task-24：**真实** 0x22→执行→0x23 回程的字节级证明
 *
 * ## 这个文件补的是哪个洞
 *
 * 跨语言对锚（firmware_tcp_e2e_client.c:90-93）里那条 0x23 是**手写**的常量：
 *     kAckOkPayload = { 0x23, 0x08, 0x00, 0x12, 0x0a, 'o','p','-','n','o','d','e','1','-','1' }
 * ⇒ 对锚只证明了「**一个手写的正确形状 ACK** 能被后端接受」，
 *   没有证明「**设备真的会产生**这条 ACK」。
 * 这与 §121/§122 同一类（用测试装置替代真实路径）。task-23 刚为 Hello 修过一次。
 *
 * ## 本文件证明什么
 *
 * 用**真实**的 handler_device_op.c + device_op.c + frame_codec.c 跑一遍：
 *    0x22 载荷 -> handler_device_op_process -> device_op_execute（真跑）
 *              -> 钩子 send_frame -> 捕获 -> 断言 == 共享向量字节
 * ⇒ 「真实回程产出的字节 == 后端能接受的字节」被钉住。
 *
 * ## ⚠ 本文件【不】证明什么（截断处，如实说明）
 *
 * 它没有覆盖 send_frame 之后的 `msg_handler_publish_checked → 广播 → session 上行`。
 * 那一段由**对锚**覆盖（真实 socket）。
 * 之所以不能把两段合成一个端到端用例，是实测发现的一个**结构性矛盾**：
 *   后端 device_e2e_firmware_test.go **先读 0x23（:237）再发 0x22（:292）**。
 *   而真实设备只能**先收到 0x22** 才可能产生 0x23。
 *   ⇒ 真实 ACK 与当前 Go 侧顺序**互斥**（会死锁到双方超时）。
 *   修它要改 Go 侧的**顺序**（不是断言），属共享契约 ⇒ 已上报，未擅自改。
 *
 * ## 变异自证（本卡的验收核心）
 *
 * 变异 A：把 device_op 的**结果码**改成另一个合法值 ⇒ 本用例必须变红。
 *   「若不变红，说明还在用手写字节」—— 这正是上一轮 Hello 的教训。
 * 变异 B：改 handler_device_op 的 ACK **字段号**（field 2 -> 7）
 *   ⇒ 后端解不出 request_id ⇒ 本用例必须变红。
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "device_op.h"
#include "frame_codec.h"
#include "msg_handler_device_op.h"

/* ESP_LOG 桩：stubs/esp_log.h 把 ESP_LOGx 全部路由到 host_test_log_record，
 * 所以这里只需提供那一个函数（与 msg_handler_publish_tests.c:23 同一手法）。 */
void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level; (void)tag; (void)format;
}

static int s_failures = 0;
#define CHECK(cond, ...)                                                     \
    do {                                                                     \
        if (!(cond)) {                                                       \
            printf("FAIL %s:%d  ", __FILE__, __LINE__);                     \
            printf(__VA_ARGS__);                                             \
            printf("\n");                                                   \
            s_failures++;                                                    \
        }                                                                    \
    } while (0)

/* ── 捕获钩子：这就是「真实出口」的落点（main/device_op_wiring.c:75 的真实出口是
 *    msg_handler_publish_checked；本用例把它的**下游**接住）。── */
/* 与 handler_device_op.c 的 DEVICE_OP_ACK_BUF(160) 同量级；留足余量。 */
#define TEST_ACK_CAP 256
static uint8_t s_ack[TEST_ACK_CAP];
static size_t  s_ack_len;
static int     s_ack_calls;
static int     s_erase_calls;
static int     s_restart_calls;

static int hook_send_frame(const uint8_t *frame, size_t len)
{
    s_ack_calls++;
    if (len == 0 || len > sizeof(s_ack)) return -1;
    memcpy(s_ack, frame, len);
    s_ack_len = len;
    return 0;
}
static int hook_erase(const char *ns) { (void)ns; s_erase_calls++; return 0; }
static void hook_restart(void) { s_restart_calls++; }   /* ⚠ 宿主上不真重启 */

static const device_op_hooks_t HOOKS = {
    .erase_namespace = hook_erase,
    .send_frame      = hook_send_frame,
    .restart         = hook_restart,
};

/* 共享向量（与后端 device_e2e_firmware_test.go:59-61 同一串字节）。 */
static const char *VECTOR_ACK_HEX = "230800120a6f702d6e6f6465312d31";
static const char *REQUEST_ID     = "op-node1-1";

static int hexval(char c)
{
    if (c >= '0' && c <= '9') return c - '0';
    if (c >= 'a' && c <= 'f') return c - 'a' + 10;
    return -1;
}

static size_t hex2bin(const char *hex, uint8_t *out, size_t cap)
{
    size_t n = strlen(hex);
    if (n % 2 != 0 || n / 2 > cap) return 0;
    for (size_t i = 0; i < n / 2; i++) {
        int hi = hexval(hex[2 * i]), lo = hexval(hex[2 * i + 1]);
        if (hi < 0 || lo < 0) return 0;
        out[i] = (uint8_t)((hi << 4) | lo);
    }
    return n / 2;
}

/* 用真实编码器造一条 0x22 reboot 载荷（等价于后端 EncodeDeviceOp 的产物）。 */
static size_t build_reboot_op(uint8_t *out, size_t cap)
{
    frame_encoder_t enc;
    frame_encoder_init(&enc, out, cap, MSG_DEVICE_OP);   /* 返回 void */
    if (frame_encode_varint(&enc, 1, (uint64_t)DEVICE_OP_REBOOT) != FRAME_OK) return 0;
    if (frame_encode_string(&enc, 2, REQUEST_ID) != FRAME_OK) return 0;
    return frame_encoder_size(&enc);
}

static void reset_capture(void)
{
    memset(s_ack, 0, sizeof(s_ack));
    s_ack_len = 0;
    s_ack_calls = 0;
    s_erase_calls = 0;
    s_restart_calls = 0;
}

/* ⭐ 核心：真实回程产出的 ACK 字节 == 共享向量。 */
static void test_real_path_produces_vector_ack(void)
{
    reset_capture();
    msg_handler_set_device_op_hooks(&HOOKS);
    CHECK(msg_handler_device_op_ready(), "钩子注入后 ready 应为真");

    uint8_t op[64];
    size_t op_len = build_reboot_op(op, sizeof(op));
    CHECK(op_len > 0, "0x22 载荷应能编码");

    /* 断言入参本身等于共享向量，否则「输出等于向量」可能是巧合。 */
    uint8_t want_op[64];
    size_t want_op_len = hex2bin("220801120a6f702d6e6f6465312d31", want_op, sizeof(want_op));
    CHECK(want_op_len == op_len && memcmp(want_op, op, op_len) == 0,
          "输入 0x22 载荷应与后端 EncodeDeviceOp 的产物一致");

    frame_decoder_t dec;
    CHECK(frame_decoder_init(&dec, op, op_len) == FRAME_OK, "解码器应能初始化");
    handler_device_op_process(&dec);

    CHECK(s_ack_calls == 1, "真实回程应恰好产出 1 条 ACK，实际 %d", s_ack_calls);
    CHECK(s_erase_calls == 0, "reboot 不该擦命名空间（实际 %d）", s_erase_calls);
    CHECK(s_restart_calls == 1, "reboot 应请求重启一次（实际 %d）", s_restart_calls);

    uint8_t want[64];
    size_t want_len = hex2bin(VECTOR_ACK_HEX, want, sizeof(want));
    CHECK(want_len > 0, "向量 hex 应能解析");
    CHECK(s_ack_len == want_len,
          "真实 ACK 长度 %zu != 向量长度 %zu（真实字节 %02X..）",
          s_ack_len, want_len, s_ack_len ? s_ack[0] : 0);
    if (s_ack_len == want_len) {
        CHECK(memcmp(s_ack, want, want_len) == 0, "真实 ACK 字节与共享向量不一致");
        if (memcmp(s_ack, want, want_len) != 0) {
            printf("  真实=%s\n", VECTOR_ACK_HEX);
            printf("  期望=%s\n", VECTOR_ACK_HEX);
        }
    }
}

/* 反向对照：请求 id 必须**原样回显**（后端靠它把 ACK 配到具体请求上）。
 * 若 ACK 里回显了别的 id，一次迟到的 ACK 会被算到下一次请求头上。 */
static void test_request_id_is_echoed(void)
{
    reset_capture();
    msg_handler_set_device_op_hooks(&HOOKS);

    uint8_t op[64];
    frame_encoder_t e;
    (void)frame_encoder_init(&e, op, sizeof(op), MSG_DEVICE_OP);
    (void)frame_encode_varint(&e, 1, (uint64_t)DEVICE_OP_REBOOT);
    (void)frame_encode_string(&e, 2, "another-id-77");
    size_t op_len = frame_encoder_size(&e);

    frame_decoder_t dec;
    (void)frame_decoder_init(&dec, op, op_len);
    handler_device_op_process(&dec);

    CHECK(s_ack_calls == 1, "应产出 1 条 ACK");
    /* 断言 ACK 里确实含 "another-id-77" 的字节。 */
    const char *needle = "another-id-77";
    size_t nlen = strlen(needle);
    bool found = false;
    for (size_t i = 0; s_ack_len >= nlen && i + nlen <= s_ack_len; i++) {
        if (memcmp(s_ack + i, needle, nlen) == 0) { found = true; break; }
    }
    CHECK(found, "ACK 必须原样回显 request_id=%s（否则服务端会配错请求）", needle);
}

int main(void)
{
    test_real_path_produces_vector_ack();
    test_request_id_is_echoed();

    if (s_failures) {
        printf("device_op_ack_realtime_tests: %d FAILURE(S)\n", s_failures);
        return 1;
    }
    printf("device_op_ack_realtime_tests: all checks passed\n");
    return 0;
}
