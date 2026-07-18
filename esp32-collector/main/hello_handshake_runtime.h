#ifndef HELLO_HANDSHAKE_RUNTIME_H
#define HELLO_HANDSHAKE_RUNTIME_H

#include <stdbool.h>
#include <stdint.h>
#include <stdatomic.h>

#if ATOMIC_INT_LOCK_FREE != 2
#error "Hello callbacks require always-lock-free 32-bit integer atomics"
#endif

typedef struct {
    /* MQTT connection_generation is the only transport generation. */
    atomic_uint_least32_t current_generation;
    atomic_uint_least32_t latest_ready_generation;
    /* ACK correlation for the worker's current wire publish. */
    atomic_uint_least32_t armed_nonce;
    atomic_uint_least32_t pending_nonce;
    atomic_uint_least32_t next_nonce;
    /* Latest-wins periodic-sync request, correlated to the ready generation. */
    atomic_uint_least32_t sync_request_generation;
    atomic_bool creation_failed;
} hello_runtime_t;

#define HELLO_RUNTIME_INITIALIZER { \
    .current_generation = ATOMIC_VAR_INIT(0), \
    .latest_ready_generation = ATOMIC_VAR_INIT(0), \
    .armed_nonce = ATOMIC_VAR_INIT(0), \
    .pending_nonce = ATOMIC_VAR_INIT(0), \
    .next_nonce = ATOMIC_VAR_INIT(1), \
    .sync_request_generation = ATOMIC_VAR_INIT(0), \
    .creation_failed = ATOMIC_VAR_INIT(false), \
}

void hello_runtime_init(hello_runtime_t *runtime);
/* Host-testable initialization path; seed is the first nonce cursor value. */
void hello_runtime_init_with_seed(hello_runtime_t *runtime, uint32_t seed);
void hello_runtime_set_creation_failed(hello_runtime_t *runtime, bool failed);
bool hello_runtime_creation_failed(const hello_runtime_t *runtime);

/* Callback-side operations: bounded lock-free loads/stores only. */
void hello_runtime_on_transport_connected(hello_runtime_t *runtime,
                                          uint32_t generation);
void hello_runtime_on_ready(hello_runtime_t *runtime, uint32_t generation);
bool hello_runtime_notify_ack(hello_runtime_t *runtime, uint32_t nonce);
bool hello_runtime_request_sync(hello_runtime_t *runtime);

/* Worker-side generation/nonce operations. */
uint32_t hello_runtime_current_generation(const hello_runtime_t *runtime);
uint32_t hello_runtime_ready_generation(const hello_runtime_t *runtime);
bool hello_runtime_take_sync_request(hello_runtime_t *runtime,
                                     uint32_t generation);
bool hello_runtime_prepare_send(hello_runtime_t *runtime, uint32_t generation,
                                uint32_t *nonce);
bool hello_runtime_finish_send(hello_runtime_t *runtime, uint32_t generation,
                               uint32_t nonce);
bool hello_runtime_consume_ack(hello_runtime_t *runtime, uint32_t generation,
                               uint32_t nonce);
void hello_runtime_clear_nonce(hello_runtime_t *runtime, uint32_t nonce);
uint32_t hello_runtime_armed_nonce(const hello_runtime_t *runtime);

#endif
