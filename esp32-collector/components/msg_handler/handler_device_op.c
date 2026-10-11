/**
 * @file handler_device_op.c
 * @brief MSG_DEVICE_OP (0x22) 处理：远程重启 / 恢复出厂（保留连通性）
 *
 * 契约（与后端 pkg/frame/device_op_wire.go 一一对应，字段号必须一致）：
 *   下行 0x22：field 1 = op_code (varint)，field 2 = request_id (bytes)
 *   上行 0x23：field 1 = result_code (varint)，field 2 = request_id (bytes)，
 *              field 3 = detail (bytes, 可选)
 *
 * request_id 必须**原样回显**：服务端用它把 ACK 配到具体那一次请求上。
 * 不回显的话，一次迟到的 ACK 会被算到下一次请求头上 ⇒ 把失败报成成功。
 *
 * ── 为什么不直接调 nvs / esp_restart ──
 * 本文件属于 msg_handler 组件；nvs_flash 与 esp_restart 属于上层。
 * 沿用本仓既有 DIP 模式（handler_diag.c 注入回调、ota_set_progress_callback
 * 同理）：由 main 在启动时注入三个原语，这里只调用函数指针。
 * 未注入时**不执行任何操作**并明确告警 —— 宁可不动，也不要"以为擦了"。
 *
 * ── ACK 编码放在这里，不放在 main ──
 * 编解码归 msgcodec（frame_codec）；main 只提供"把这串字节送出去"。
 * 这样"结果码从哪来"只有一个来源：device_op_execute 的返回值。
 */
#include <stdio.h>
#include <string.h>

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "esp_log.h"

#include "device_op.h"
#include "frame_codec.h"
#include "msg_handler_device_op.h"

static const char *TAG = "DEVICE_OP";

/* device_op.h 为了能在宿主编译而自带一份消息号，这就有"两处定义会漂移"的风险。
 * 用编译期断言钉死：谁只改一边，**固件直接编不过**。 */
_Static_assert(MSG_DEVICE_OP == 0x22u,
               "MSG_DEVICE_OP must stay 0x22 (frame_codec.h is the authoritative table)");
_Static_assert(MSG_DEVICE_OP_ACK == 0x23u,
               "MSG_DEVICE_OP_ACK must stay 0x23 (frame_codec.h is the authoritative table)");

/** request_id 上限，与后端 MaxDeviceOpRequestIDBytes 对齐。 */
#define DEVICE_OP_REQ_ID_MAX 64

/** ACK 缓冲：足够容纳 1 字节类型 + 两个字段的头 + 64 字节 id + 少量 detail。 */
#define DEVICE_OP_ACK_BUF 160

static device_op_hooks_t s_hooks;
static bool s_hooks_set;

void msg_handler_set_device_op_hooks(const device_op_hooks_t *hooks)
{
    if (hooks == NULL) {
        memset(&s_hooks, 0, sizeof(s_hooks));
        s_hooks_set = false;
        return;
    }
    s_hooks = *hooks;
    s_hooks_set = true;
}

bool msg_handler_device_op_ready(void) { return s_hooks_set; }

/* ── 适配层：把 handler 的上下文喂给 device_op_io_t ──
 *
 * device_op 的 IO 接口是 (void *ctx, ...)，而这里需要同时知道
 * 「注入的三个原语」与「本次请求的 request_id」。用一个栈上结构体装起来，
 * 用 ctx 传下去 —— **不用文件级静态变量**：虽然 device_op 自带单飞保证
 * 同一时刻只有一个请求，但把请求身份放在静态变量里，一旦单飞语义将来被
 * 放宽就会变成"ACK 回显了另一个请求的 id"，那是很难查的错。
 */
typedef struct {
    const device_op_hooks_t *hooks;
    const char *request_id;
    /** adapt_flush 是否被调用过 —— 用来保证"恰好一次 ACK"。 */
    bool flush_called;
} devop_ctx_t;

static int adapt_erase(void *ctx, const char *ns_name)
{
    devop_ctx_t *c = (devop_ctx_t *)ctx;
    if (c == NULL || c->hooks == NULL || c->hooks->erase_namespace == NULL) {
        return -1;
    }
    return c->hooks->erase_namespace(ns_name);
}

/** 编码 0x23 并同步送出。返回 0 成功。 */
static int encode_and_send_ack(const devop_ctx_t *c, device_op_result_t result, const char *detail)
{
    uint8_t buf[DEVICE_OP_ACK_BUF];
    frame_encoder_t enc;
    frame_encoder_init(&enc, buf, sizeof(buf), MSG_DEVICE_OP_ACK);

    if (frame_encode_varint(&enc, 1, (uint64_t)result) != FRAME_OK) return -1;
    if (frame_encode_string(&enc, 2, c->request_id) != FRAME_OK) return -1;
    if (detail != NULL && detail[0] != '\0') {
        if (frame_encode_string(&enc, 3, detail) != FRAME_OK) return -1;
    }
    if (c->hooks->send_frame == NULL) return -1;
    return c->hooks->send_frame(frame_encoder_data(&enc), frame_encoder_size(&enc));
}

static int adapt_flush(void *ctx, device_op_result_t result)
{
    devop_ctx_t *c = (devop_ctx_t *)ctx;
    if (c == NULL || c->hooks == NULL) return -1;
    c->flush_called = true;
    /* detail 留空：原因已经在 result 码里，重复一遍只会占带宽。 */
    return encode_and_send_ack(c, result, NULL);
}

static void adapt_restart(void *ctx)
{
    devop_ctx_t *c = (devop_ctx_t *)ctx;
    if (c == NULL || c->hooks == NULL || c->hooks->restart == NULL) return;
    c->hooks->restart();
}

/* ── 手工解析 0x22 ──
 *
 * 不用 frame_decoder 的字段循环而是自己走一遍，理由是**必须原样拿到
 * request_id 的字节**：frame_field_t 对 length-delimited 给的是
 * "指针 + 长度"，没有 NUL 结尾，直接当 C 字符串用会读越界。
 */
static bool parse_device_op(frame_decoder_t *dec, device_op_t *op_out,
                            char *req_id, size_t req_cap, bool *have_op, bool *have_id)
{
    bool ok = true;
    *have_op = false;
    *have_id = false;

    frame_field_t field;
    while (frame_decoder_next(dec, &field) == FRAME_OK) {
        switch (field.field_num) {
        case 1:
            if (field.wire_type != WIRE_VARINT) { ok = false; break; }
            if (field.value.varint > 0xFF) { ok = false; break; }
            *op_out = (device_op_t)field.value.varint;
            *have_op = true;
            break;
        case 2:
            if (field.wire_type != WIRE_LENGTH_DELIMITED) { ok = false; break; }
            if (field.value.bytes.len == 0 || field.value.bytes.len >= req_cap) {
                ok = false;
                break;
            }
            memcpy(req_id, field.value.bytes.ptr, field.value.bytes.len);
            req_id[field.value.bytes.len] = '\0';
            *have_id = true;
            break;
        default:
            break;
        }
        if (!ok) break;
    }
    return ok;
}

void handler_device_op_process(frame_decoder_t *dec)
{
    device_op_t op = (device_op_t)0;
    char request_id[DEVICE_OP_REQ_ID_MAX + 1];
    request_id[0] = '\0';
    bool have_op = false, have_id = false;

    if (!parse_device_op(dec, &op, request_id, sizeof(request_id), &have_op, &have_id)) {
        /* 畸形帧：**没有任何 id 可回显**，因此无法回 ACK。
         * 这时只能丢弃 —— 乱猜一个 id 回去比不回更糟（会让服务端把
         * ACK 配到别的请求上）。 */
        ESP_LOGW(TAG, "malformed MSG_DEVICE_OP — dropped (no valid request_id to echo)");
        return;
    }
    if (!have_id) {
        ESP_LOGW(TAG, "MSG_DEVICE_OP without request_id — dropped (an ACK could not "
                      "be correlated, so the server would never learn the outcome)");
        return;
    }

    /* 未知操作码也要回 ACK：服务端需要知道"设备不认识这条命令"
     * （版本不匹配），而不是一直等到超时、然后告诉操作员"结果未知"。 */
    if (!have_op) {
        ESP_LOGW(TAG, "MSG_DEVICE_OP without op_code (request_id=%s)", request_id);
        devop_ctx_t ctx = { .hooks = &s_hooks, .request_id = request_id };
        if (s_hooks_set) {
            (void)adapt_flush(&ctx, DEVOP_ERR_UNKNOWN_OP);
        }
        return;
    }

    if (!s_hooks_set) {
        ESP_LOGE(TAG, "device op hooks not injected — refusing to act on %s "
                      "(a reboot without injected primitives would otherwise look "
                      "successful while doing nothing)",
                 device_op_name(op));
        return;
    }

    ESP_LOGI(TAG, "device op request: op=%s request_id=%s", device_op_name(op), request_id);

    devop_ctx_t ctx = { .hooks = &s_hooks, .request_id = request_id, .flush_called = false };
    device_op_io_t io = {
        .erase_namespace = adapt_erase,
        .flush_ack = adapt_flush,
        .restart = adapt_restart,
    };

    bool restarted = false;
    device_op_result_t r = device_op_execute(&io, &ctx, op, &restarted);
    ESP_LOGI(TAG, "device op finished: result=%s restarted=%d",
             device_op_result_name(r), (int)restarted);

    /* ── 保证"每个带 request_id 的请求恰好收到一次 ACK" ──
     *
     * device_op 的契约里有一条：**未知操作码 → UNKNOWN_OP，不刷新 ACK**
     * （它刻意不在自己不认识的命令上做任何动作）。
     * 但"不刷新"与"不回话"是两件事：
     * 不回话 ⇒ 服务端只能等到超时，然后把结果报成"未知"（HTTP 202），
     * 而真相是**设备明确不认识这条命令**（HTTP 409 + UNKNOWN_OP）。
     * 同一件事（"我不认识这个 op"）在两条路径上给出了不同答案：
     *   - 缺 op_code 字段  -> 上面已回 UNKNOWN_OP
     *   - op_code 值不认识 -> 这里补回 UNKNOWN_OP
     * 不补的话，后者在服务端看起来和"设备没反应"完全一样。
     *
     * 因此：**device_op 没刷过 ACK，这里就补一次**，用它的返回值当结果码。
     * 于是不变量变成：带 request_id 的请求 ⇒ 恰好一次 ACK。 */
    if (!ctx.flush_called) {
        (void)encode_and_send_ack(&ctx, r, NULL);
    }
}
