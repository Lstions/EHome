/**
 * @file hello_handshake.h
 * @brief Event-driven Hello handshake state machine.
 *
 * Replaces main.c's polling busy-wait with FreeRTOS EventGroup.
 * msg_handler calls hello_handshake_notify_ack() when HelloAck arrives.
 */

#ifndef HELLO_HANDSHAKE_H
#define HELLO_HANDSHAKE_H

#include <stdint.h>
#include "app_state.h"

#ifdef __cplusplus
extern "C" {
#endif

void hello_handshake_start(app_state_t *state);
void hello_handshake_notify_ack(void);
bool hello_handshake_is_running(void);

#ifdef __cplusplus
}
#endif

#endif /* HELLO_HANDSHAKE_H */
