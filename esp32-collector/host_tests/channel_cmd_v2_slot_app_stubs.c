/*
 * channel_cmd_v2_slot_app_stubs.c
 *
 * Strong override stubs for the weak app-bridge symbols inside
 * handler_channel_cmd_v2.c.  This file is a separate translation unit:
 * the linker resolves the weak definitions (inside the included handler
 * source in channel_cmd_v2_slot_tests.c) to these strong symbols, exactly
 * like channel_cmd_v2_decoder_tests.c does when it links the handler
 * source as its own TU.
 *
 * Also owns the shared capture state both TUs need.
 */

#include <stdbool.h>
#include <stdint.h>
#include <stddef.h>
#include <string.h>
#include <stdarg.h>

#include "msg_handler_internal.h"

/* ---- Shared capture state (declared extern in the test TU) ---- */
unsigned v2t_callback_count;
uint8_t  v2t_last_slot;
unsigned v2t_publish_count;
uint8_t  v2t_published[4][256];
size_t   v2t_published_len[4];
#define V2T_PUBLISH_CAPTURE_MAX 4

void host_test_log_record(char level, const char *tag, const char *format, ...)
{
    (void)level; (void)tag; (void)format;
}

void msg_handler_publish(const uint8_t *data, size_t len)
{
    if (v2t_publish_count < V2T_PUBLISH_CAPTURE_MAX) {
        size_t n = len < 256 ? len : 256;
        memcpy(v2t_published[v2t_publish_count], data, n);
        v2t_published_len[v2t_publish_count] = n;
    }
    v2t_publish_count++;
}

const char *channel_cmd_v2_current_boot_id(void) { return "boot-1"; }
uint64_t channel_cmd_v2_current_time_ms(void) { return 1699999990000ULL; }


/* B2（2026-10-06）：msg_handler_publish_checked 原先是 handler 里的
 * __attribute__((weak)) 默认实现（退化为未校验发布），已删除。
 * 本 TU 需要它 —— 测试里没有真实传输，按成功处理即可，
 * 且不改动被测的解码行为。生产实现见 msg_handler.c。 */
esp_err_t msg_handler_publish_checked(const uint8_t *data, size_t len)
{
    /* 必须真的走一遍 msg_handler_publish() —— 测试用它捕获被发布的帧
     * 来做断言（原弱默认实现也是这么做的：publish 后返回 OK）。
     * 只返回 ESP_OK 而不发布，会让"必须发布 ACK/Final"的断言全部失败。 */
    msg_handler_publish(data, len);
    return ESP_OK;
}

bool on_channel_cmd_v2_received(const channel_cmd_v2_t *cmd, uint8_t slot)
{
    (void)cmd;
    v2t_callback_count++;
    v2t_last_slot = slot;
    return true;
}
