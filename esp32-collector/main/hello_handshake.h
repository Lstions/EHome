/**
 * @file hello_handshake.h
 * @brief Long-lived, notification-driven Hello v2.6 handshake supervisor.
 */

#ifndef HELLO_HANDSHAKE_H
#define HELLO_HANDSHAKE_H

#include <stdbool.h>
#include <stdint.h>
#ifdef HELLO_HANDSHAKE_HOST_TEST
#include <app_state.h>
#else
#include "app_state.h"
#endif
#include "hello_handshake_runtime.h"

#ifdef __cplusplus
extern "C" {
#endif

/* Creates the long-lived task and fixes its app_state reference. Call before
 * MQTT can start. The task is never deleted during runtime. */
void hello_handshake_start(app_state_t *state);

/* MQTT callback entry points. Both use MQTT connection_generation directly. */
void hello_handshake_on_transport_connected(uint32_t generation);
void hello_handshake_on_ready(uint32_t generation);

/* Non-blocking periodic-sync entry point. Requests are coalesced and the
 * worker starts a nonce-correlated handshake only in the current READY
 * transport generation. Returns false when transport is not READY. */
bool hello_handshake_request_sync(void);

/* Called by the strict HelloAck parser. Only an exact currently armed,
 * non-zero nonce causes a worker notification. */
bool hello_handshake_notify_ack(uint32_t nonce);

bool hello_handshake_is_running(void);
bool hello_handshake_has_failed(void);

/* One finite worker iteration, shared by the production task and host tests. */
bool hello_handshake_worker_step(uint32_t max_wait_ticks);
uint32_t hello_handshake_debug_armed_nonce(void);
uint32_t hello_handshake_debug_current_generation(void);

#ifdef HELLO_HANDSHAKE_HOST_TEST
void hello_handshake_test_reset(void);
#endif

#ifdef __cplusplus
}
#endif

#endif /* HELLO_HANDSHAKE_H */
