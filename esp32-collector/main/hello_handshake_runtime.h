#ifndef HELLO_HANDSHAKE_RUNTIME_H
#define HELLO_HANDSHAKE_RUNTIME_H

#include <stdbool.h>
#include <stdint.h>
#include <stdatomic.h>

/* Only the implementation TU is compiled with hardware atomics restored on
 * PSRAM targets (see hello_handshake_runtime.c and main/CMakeLists.txt). Other
 * consumer TUs legitimately see ATOMIC_INT_LOCK_FREE == 1 under
 * CONFIG_STDATOMIC_S32C1I_SPIRAM_WORKAROUND; IDF routes those non-lock-free
 * operations by address. The guarantee is enforced where it is actually used. */
#if defined(HELLO_RUNTIME_IMPL) && ATOMIC_INT_LOCK_FREE != 2
#error "Hello callbacks require always-lock-free 32-bit integer atomics"
#endif

/* task-33：armed nonce 的**归属**。决定 notify_ack 用哪条新鲜度判据
 * （MQTT 代际 vs 3.0 链路自管）—— 见 hello_handshake_runtime.c 的说明。 */
typedef enum {
    HELLO_ARM_NONE = 0,   /* 没有 armed（或已被清）*/
    HELLO_ARM_MQTT = 1,   /* 由 2.x 代际路径 prepare_send 的 armed */
    HELLO_ARM_LINK = 2,   /* 由 3.0 链路 hello_runtime_arm_link 的 armed */
} hello_arm_transport_t;

typedef struct {
    /* MQTT connection_generation is the only transport generation. */
    atomic_uint_least32_t current_generation;
    atomic_uint_least32_t latest_ready_generation;
    /* ACK correlation for the worker's current wire publish. */
    atomic_uint_least32_t armed_nonce;
    /* task-33：这条 armed_nonce 属于哪条传输（HELLO_ARM_*）。 */
    atomic_uint_least32_t armed_transport;
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
    .armed_transport = ATOMIC_VAR_INIT(HELLO_ARM_NONE), \
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

/* === task-33：3.0 链路的 arm 路径（决策 B′）===
 *
 * 3.0 链路**不再自己生成 nonce**（此前是 device_link_wiring.c 的
 * esp_random()|1，与 2.x 的定义并存 ⇒ P4 违反 ⇒ 真机 §138 的 stale nonce）。
 * 改用本入口：同一个分配器、同一个 armed_nonce 存储位，只是标记归属。
 *
 * ⚠ armed_nonce 是**单值**：MQTT 与 TCP 同时 armed 时**后 arm 者覆盖前者**，
 * 被覆盖那条的 ACK 会被**如实拒绝**（不静默 READY）。若将来双栈需并发握手，
 * 必须改为按传输分槽 —— 见 docs/设计/决策-3.0-握手所有权-2026-10-07.md §6。
 */
bool hello_runtime_arm_link(hello_runtime_t *runtime, uint32_t *nonce);

/** 只清"属于 3.0 链路"的那一份 arm（不会误清 MQTT 的）。 */
void hello_runtime_clear_link_arm(hello_runtime_t *runtime);

/** 当前 armed 的归属（诊断/测试用）。 */
hello_arm_transport_t hello_runtime_armed_transport(const hello_runtime_t *runtime);

#endif
