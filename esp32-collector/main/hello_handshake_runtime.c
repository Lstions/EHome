/* This is the one translation unit in which the Hello runtime atomics must be
 * genuinely lock-free. main/CMakeLists.txt restores hardware atomics for this
 * file only when CONFIG_STDATOMIC_S32C1I_SPIRAM_WORKAROUND is enabled, so the
 * implementation still gets ATOMIC_INT_LOCK_FREE == 2 while the rest of the
 * firmware is compiled with -mdisable-hardware-atomics. */
#define HELLO_RUNTIME_IMPL 1
#include "hello_handshake_runtime.h"

#include <stddef.h>
#include <assert.h>
#include "esp_random.h"
#include "sdkconfig.h"

_Static_assert(ATOMIC_INT_LOCK_FREE == 2,
               "hello runtime implementation requires lock-free 32-bit atomics");

#if CONFIG_SPIRAM && __XTENSA__
/* PSRAM occupies [SOC_EXTRAM_DATA_LOW, SOC_EXTRAM_DATA_HIGH) on S3, and the
 * Xtensa S32C1I instruction is not valid there. IDF's own esp_stdatomic.h:55
 * uses this same range test to decide between the hardware path and the
 * critical-section fallback. s_runtime must therefore never live in PSRAM. */
#include "soc/soc.h"
#endif

void hello_runtime_init(hello_runtime_t *runtime)
{
    uint32_t seed = esp_random();
    if (seed == 0) seed = 1;
    hello_runtime_init_with_seed(runtime, seed);
}

void hello_runtime_init_with_seed(hello_runtime_t *runtime, uint32_t seed)
{
    if (runtime == NULL) return;
#if CONFIG_SPIRAM && __XTENSA__
    /* Defence in depth for the DRAM_ATTR pin in hello_handshake.c: hardware
     * atomics only work on internal RAM, so a runtime object that ended up in
     * PSRAM would silently lose atomicity. Fail loudly at init instead. */
    assert(!((uintptr_t)runtime >= SOC_EXTRAM_DATA_LOW &&
             (uintptr_t)runtime < SOC_EXTRAM_DATA_HIGH));
#endif
    if (seed == 0) seed = 1;
    atomic_init(&runtime->current_generation, 0);
    atomic_init(&runtime->latest_ready_generation, 0);
    atomic_init(&runtime->armed_nonce, 0);
    atomic_init(&runtime->armed_transport, HELLO_ARM_NONE);
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
    /* task-33: 代际复位也必须把"这条 nonce 属于哪条传输"清掉 ——
     * 否则一条陈旧的 3.0 arm 会在 MQTT 重连后仍被当作有效（判据见 notify_ack）。 */
    atomic_store_explicit(&runtime->armed_transport, HELLO_ARM_NONE,
                          memory_order_release);
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
    /* task-33：标记归属，让 notify_ack 用 MQTT 代际那一套判据（与从前逐位一致）。 */
    atomic_store_explicit(&runtime->armed_transport, HELLO_ARM_MQTT,
                          memory_order_release);
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

    /* task-33（决策 B′）：armed nonce 的**归属**决定用哪条新鲜度判据。
     *
     * - 由 MQTT 代际路径 armed（HELLO_ARM_MQTT）：保持**原判据不变** ——
     *   ready_generation 必须等于 current_generation。这是 2.x 的既有语义：
     *   代际变了（重连）之后，旧代际的 ACK 必须被拒。
     * - 由 3.0 链路 armed（HELLO_ARM_LINK）：**不能**用上面那条，因为
     *   ready_generation 是 **MQTT 就绪**的产物（on_mqtt_ready_cb），
     *   而 §7.3 P2 的场景正是"MQTT 未就绪、TCP 顶上"—— 此时它恒为 0，
     *   用它做判据会让 3.0 的 ACK 被永久拒绝（真机 §138 的形态）。
     *   3.0 的新鲜度由它自己保证：重连时重新 arm，旧 nonce 被覆盖；
     *   被覆盖之后旧 ACK 仍会对不上 armed_nonce ⇒ 照样被拒。 */
    hello_arm_transport_t src = (hello_arm_transport_t)atomic_load_explicit(
        &runtime->armed_transport, memory_order_acquire);

    if (src == HELLO_ARM_LINK) {
        if (hello_runtime_armed_nonce(runtime) != nonce) return false;
        atomic_store_explicit(&runtime->pending_nonce, nonce,
                              memory_order_release);
        if (hello_runtime_armed_nonce(runtime) != nonce) {
            uint_least32_t exp = nonce;
            (void)atomic_compare_exchange_strong_explicit(
                &runtime->pending_nonce, &exp, 0,
                memory_order_acq_rel, memory_order_acquire);
            return false;
        }
        return true;
    }

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
    bool cleared = atomic_compare_exchange_strong_explicit(
        &runtime->armed_nonce, &expected, 0,
        memory_order_acq_rel, memory_order_acquire);
    /* task-33：only if WE cleared the armed slot, drop the ownership tag too —
     * 否则会把"刚被别的传输 arm 上的"归属一起抹掉。 */
    if (cleared) {
        atomic_store_explicit(&runtime->armed_transport, HELLO_ARM_NONE,
                              memory_order_release);
    }
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

/* ════════ task-33：3.0 链路的 arm/clear（决策 B′）════════
 *
 * 为什么需要它：3.0 链路此前自带 s_hello_nonce = esp_random()|1，
 * 而校验方（handler_hello → hello_runtime_notify_ack）只认本 runtime 的
 * armed_nonce ⇒ 3.0 的 HelloAck **必然**被判 stale（真机 §138）。
 *
 * B′ 的关键点：**不新增 nonce 生成点**。这里复用的仍是
 * hello_runtime_allocate_nonce()（单调 + 同一个 next_nonce 游标），
 * 存储仍是同一个 armed_nonce 位。3.0 只是"换一种方式告诉 runtime：
 * 这条 nonce 归我这条传输"。
 *
 * ⚠ 覆盖语义（已写进决策文档 §6 重开条件 4）：armed_nonce 是**单值**。
 * 若 MQTT 与 TCP 同时 armed，**后 arm 者覆盖前者**，被覆盖那条的 ACK 会被
 * 如实拒绝（而不是静默 READY —— 那正是 D-B 要消灭的形态）。
 */
bool hello_runtime_arm_link(hello_runtime_t *runtime, uint32_t *nonce)
{
    if (runtime == NULL || nonce == NULL) return false;

    uint32_t own = hello_runtime_allocate_nonce(runtime);
    if (own == 0) return false;

    /* 先置 transport 再置 nonce：校验方读 nonce 前会先读 transport，
     * 若顺序反了，它在两者之间可能看到"transport=LINK 但 nonce 还是旧的"
     * ⇒ 用旧 nonce 也能通过。用 release 保证写序。 */
    atomic_store_explicit(&runtime->pending_nonce, 0, memory_order_release);
    atomic_store_explicit(&runtime->armed_transport, HELLO_ARM_LINK,
                          memory_order_release);
    atomic_store_explicit(&runtime->armed_nonce, own, memory_order_release);
    *nonce = own;
    return true;
}

void hello_runtime_clear_link_arm(hello_runtime_t *runtime)
{
    if (runtime == NULL) return;
    /* 只清"属于 LINK 的那一份"：若当前 armed 的是 MQTT 的，不能误清它。
     * CAS 的 expected 用当前值，避免把别人刚写入的覆盖掉。 */
    uint_least32_t src = atomic_load_explicit(&runtime->armed_transport,
                                              memory_order_acquire);
    if (src != HELLO_ARM_LINK) return;
    atomic_store_explicit(&runtime->armed_nonce, 0, memory_order_release);
    atomic_store_explicit(&runtime->armed_transport, HELLO_ARM_NONE,
                          memory_order_release);
    atomic_store_explicit(&runtime->pending_nonce, 0, memory_order_release);
}

hello_arm_transport_t hello_runtime_armed_transport(const hello_runtime_t *runtime)
{
    if (runtime == NULL) return HELLO_ARM_NONE;
    return (hello_arm_transport_t)atomic_load_explicit(
        &runtime->armed_transport, memory_order_acquire);
}
