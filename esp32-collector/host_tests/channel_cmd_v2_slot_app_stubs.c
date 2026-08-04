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

bool on_channel_cmd_v2_received(const channel_cmd_v2_t *cmd, uint8_t slot)
{
    (void)cmd;
    v2t_callback_count++;
    v2t_last_slot = slot;
    return true;
}
