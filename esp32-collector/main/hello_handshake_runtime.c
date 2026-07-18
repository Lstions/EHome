#include "hello_handshake_runtime.h"

#include <stddef.h>
#include "esp_random.h"

void hello_runtime_init(hello_runtime_t *runtime)
{
    uint32_t seed = esp_random();
    if (seed == 0) seed = 1;
    hello_runtime_init_with_seed(runtime, seed);
}

void hello_runtime_init_with_seed(hello_runtime_t *runtime, uint32_t seed)
{
    if (runtime == NULL) return;
    if (seed == 0) seed = 1;
    atomic_init(&runtime->current_generation, 0);
    atomic_init(&runtime->latest_ready_generation, 0);
    atomic_init(&runtime->armed_nonce, 0);
    atomic_init(&runtime->pending_nonce, 0);
    /* The allocator increments this cursor before returning. Keeping the
     * random seed itself in the cursor guarantees a non-zero startup state
     * while making every boot's first wire nonce unpredictable. */
    atomic_init(&runtime->next_nonce, seed);
    atomic_init(&runtime->sync_request_generation, 0);
    atomic_init(&runtime->creation_failed, false);
}

void hello_runtime_set_creation_failed(hello_runtime_t *runtime, bool failed)
{
    if (runtime == NULL) return;
    atomic_store_explicit(&runtime->creation_failed, failed,
                          memory_order_release);
}

bool hello_runtime_creation_failed(const hello_runtime_t *runtime)
{
    return runtime != NULL && atomic_load_explicit(
        &runtime->creation_failed, memory_order_acquire);
}

void hello_runtime_on_transport_connected(hello_runtime_t *runtime,
                                          uint32_t generation)
{
    if (runtime == NULL || generation == 0) return;
    /* current_generation is the reset linearization point. ACK validation also
     * checks ready==current, so the old nonce is no longer current immediately
     * even before the following bounded disarm stores complete. */
    atomic_store_explicit(&runtime->current_generation, generation,
                          memory_order_release);
    atomic_store_explicit(&runtime->latest_ready_generation, 0,
                          memory_order_release);
    atomic_store_explicit(&runtime->armed_nonce, 0, memory_order_release);
    atomic_store_explicit(&runtime->pending_nonce, 0, memory_order_release);
    atomic_store_explicit(&runtime->sync_request_generation, 0,
                          memory_order_release);
}

void hello_runtime_on_ready(hello_runtime_t *runtime, uint32_t generation)
{
    if (runtime == NULL || generation == 0 ||
        atomic_load_explicit(&runtime->current_generation,
                             memory_order_acquire) != generation) {
        return;
    }
    atomic_store_explicit(&runtime->latest_ready_generation, generation,
                          memory_order_release);
    /* If reset raced the store, remove only the value written by this callback.
     * Worker-side validation is the final authority even if the CAS loses. */
    if (atomic_load_explicit(&runtime->current_generation,
                             memory_order_acquire) != generation) {
        uint_least32_t expected = generation;
        (void)atomic_compare_exchange_strong_explicit(
            &runtime->latest_ready_generation, &expected, 0,
            memory_order_acq_rel, memory_order_acquire);
    }
}

uint32_t hello_runtime_current_generation(const hello_runtime_t *runtime)
{
    if (runtime == NULL) return 0;
    return (uint32_t)atomic_load_explicit(&runtime->current_generation,
                                          memory_order_acquire);
}

uint32_t hello_runtime_ready_generation(const hello_runtime_t *runtime)
{
    if (runtime == NULL) return 0;
    uint32_t current = hello_runtime_current_generation(runtime);
    uint32_t ready = (uint32_t)atomic_load_explicit(
        &runtime->latest_ready_generation, memory_order_acquire);
    return current != 0 && ready == current ? current : 0;
}

bool hello_runtime_request_sync(hello_runtime_t *runtime)
{
    if (runtime == NULL) return false;
    uint32_t generation = hello_runtime_current_generation(runtime);
    if (generation == 0 ||
        hello_runtime_ready_generation(runtime) != generation) {
        return false;
    }

    /* Latest-wins mailbox: repeated callbacks in one ready generation merge
     * into one worker restart. CAS is required here: an old callback paused
     * across transport reset must never overwrite a newer generation request. */
    uint_least32_t observed = atomic_load_explicit(
        &runtime->sync_request_generation, memory_order_acquire);
    for (;;) {
        if (hello_runtime_current_generation(runtime) != generation ||
            hello_runtime_ready_generation(runtime) != generation) {
            return false;
        }
        if (observed == generation) return true;
        if (atomic_compare_exchange_weak_explicit(
                &runtime->sync_request_generation, &observed, generation,
                memory_order_acq_rel, memory_order_acquire)) {
            break;
        }
    }
    if (hello_runtime_current_generation(runtime) == generation &&
        hello_runtime_ready_generation(runtime) == generation) {
        return true;
    }

    uint_least32_t expected = generation;
    (void)atomic_compare_exchange_strong_explicit(
        &runtime->sync_request_generation, &expected, 0,
        memory_order_acq_rel, memory_order_acquire);
    return false;
}

bool hello_runtime_take_sync_request(hello_runtime_t *runtime,
                                     uint32_t generation)
{
    if (runtime == NULL || generation == 0) return false;
    uint_least32_t expected = generation;
    if (!atomic_compare_exchange_strong_explicit(
            &runtime->sync_request_generation, &expected, 0,
            memory_order_acq_rel, memory_order_acquire)) {
        return false;
    }
    return hello_runtime_current_generation(runtime) == generation &&
           hello_runtime_ready_generation(runtime) == generation;
}

static uint32_t hello_runtime_allocate_nonce(hello_runtime_t *runtime)
{
    uint32_t nonce = (uint32_t)atomic_fetch_add_explicit(
        &runtime->next_nonce, 1, memory_order_acq_rel) + 1U;
    if (nonce == 0) {
        nonce = (uint32_t)atomic_fetch_add_explicit(
            &runtime->next_nonce, 1, memory_order_acq_rel) + 1U;
    }
    return nonce;
}

bool hello_runtime_prepare_send(hello_runtime_t *runtime, uint32_t generation,
                                uint32_t *nonce)
{
    if (runtime == NULL || nonce == NULL || generation == 0 ||
        hello_runtime_current_generation(runtime) != generation ||
        hello_runtime_ready_generation(runtime) != generation) {
        return false;
    }

    uint32_t own_nonce = hello_runtime_allocate_nonce(runtime);
    atomic_store_explicit(&runtime->pending_nonce, 0, memory_order_release);
    atomic_store_explicit(&runtime->armed_nonce, own_nonce,
                          memory_order_release);
    if (hello_runtime_current_generation(runtime) == generation &&
        hello_runtime_ready_generation(runtime) == generation) {
        *nonce = own_nonce;
        return true;
    }

    hello_runtime_clear_nonce(runtime, own_nonce);
    return false;
}

bool hello_runtime_finish_send(hello_runtime_t *runtime, uint32_t generation,
                               uint32_t nonce)
{
    if (runtime == NULL || generation == 0 || nonce == 0) return false;
    bool valid = hello_runtime_current_generation(runtime) == generation &&
                 hello_runtime_ready_generation(runtime) == generation &&
                 hello_runtime_armed_nonce(runtime) == nonce;
    if (!valid) hello_runtime_clear_nonce(runtime, nonce);
    return valid;
}

bool hello_runtime_notify_ack(hello_runtime_t *runtime, uint32_t nonce)
{
    if (runtime == NULL || nonce == 0) return false;
    uint32_t current = hello_runtime_current_generation(runtime);
    if (current == 0 || hello_runtime_ready_generation(runtime) != current ||
        hello_runtime_armed_nonce(runtime) != nonce) {
        return false;
    }

    atomic_store_explicit(&runtime->pending_nonce, nonce,
                          memory_order_release);
    if (hello_runtime_current_generation(runtime) == current &&
        hello_runtime_ready_generation(runtime) == current &&
        hello_runtime_armed_nonce(runtime) == nonce) {
        return true;
    }

    uint_least32_t expected = nonce;
    (void)atomic_compare_exchange_strong_explicit(
        &runtime->pending_nonce, &expected, 0,
        memory_order_acq_rel, memory_order_acquire);
    return false;
}

bool hello_runtime_consume_ack(hello_runtime_t *runtime, uint32_t generation,
                               uint32_t nonce)
{
    if (runtime == NULL || generation == 0 || nonce == 0) return false;
    uint_least32_t expected = nonce;
    if (!atomic_compare_exchange_strong_explicit(
            &runtime->pending_nonce, &expected, 0,
            memory_order_acq_rel, memory_order_acquire)) {
        return false;
    }
    return hello_runtime_current_generation(runtime) == generation &&
           hello_runtime_ready_generation(runtime) == generation &&
           hello_runtime_armed_nonce(runtime) == nonce;
}

void hello_runtime_clear_nonce(hello_runtime_t *runtime, uint32_t nonce)
{
    if (runtime == NULL || nonce == 0) return;
    uint_least32_t expected = nonce;
    (void)atomic_compare_exchange_strong_explicit(
        &runtime->armed_nonce, &expected, 0,
        memory_order_acq_rel, memory_order_acquire);
    expected = nonce;
    (void)atomic_compare_exchange_strong_explicit(
        &runtime->pending_nonce, &expected, 0,
        memory_order_acq_rel, memory_order_acquire);
}

uint32_t hello_runtime_armed_nonce(const hello_runtime_t *runtime)
{
    if (runtime == NULL) return 0;
    return (uint32_t)atomic_load_explicit(&runtime->armed_nonce,
                                          memory_order_acquire);
}
