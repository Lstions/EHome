/**
 * @file link.h
 * @brief 上行链路抽象 —— 取代 components/transport（设计原则 P1/P2/P3/P4）
 *
 * 设计文档：docs/设计/ESP32-与后端-3.0-接口与模块设计-2026-10-06.md 1.2
 *
 * 为什么重做（不是洁癖）：
 *   旧接口 transport_ops.send() 只返回 esp_err_t，【无法区分】
 *   "本地未连接"与"对端/队列失败"；调用方只能再查 is_connected()，
 *   于是写出"广播已发过一次、再回退重发一次"的代码 —— 这就是 L-02
 *   （实测 PF/NT = 1.998，同一帧发布两次）。
 *   本接口让【返回值本身】携带调用方决策所需的一切（P1）。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_LINK_H
#define EHOME_LINK_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/**
 * 发送结果 —— 调用方据此【唯一地】决定下一步（P1）。
 * 注意 BACKPRESSURE 是独立结果而【不是】错误：调用方应退避重试，而非当作故障。
 */
typedef enum {
    LINK_SENT = 0,        /* 已交给链路层并计入账（可能仍在缓冲） */
    LINK_NOT_READY,       /* 链路未就绪：调用方【不应】换路重试，应降级/入队 */
    LINK_BACKPRESSURE,    /* 缓冲满：调用方【应】稍后重试（不是错误） */
    LINK_PAYLOAD_TOO_BIG, /* 超 MTU：调用方【必须】分片或拒绝 */
    LINK_FATAL,           /* 链路不可用/参数错：调用方【应】通知上层重建 */
    LINK_RESULT_COUNT
} link_result_t;

/** opaque —— 外部【不能】读写字段（P4：状态只能由本模块迁移）。 */
typedef struct link link_t;

/**
 * 驱动接口 —— 只有 5 个【必须实现】的函数。
 * 旧 transport_ops 的 init/deinit 在两个既有实现里分别是 no-op/空函数
 * （D-21：一份"两个实现都不履行的契约"）。这里用构造/析构取代：
 * 生命周期由 link_create/link_destroy 负责，驱动不再有"可选但被假定必需"的钩子。
 */
typedef struct {
    link_result_t (*open)(void *ctx);
    void          (*close)(void *ctx);
    /* 契约：必须【写完全部】len 字节或返回错误。部分写属驱动缺陷，
     * 驱动应自行续写并在无法续写时返回 LINK_BACKPRESSURE（治 D-10：
     * 旧实现把 send()>0 记为成功，短写会静默发出半帧）。 */
    link_result_t (*send)(void *ctx, const uint8_t *data, size_t len);
    uint32_t      (*mtu)(void *ctx);
    bool          (*is_ready)(void *ctx);
    const char    *name;
} link_driver_t;

/** 诊断快照 —— 一次读取得到一致视图（P4；旧代码用 4 个独立 getter，读到 4 个时刻）。 */
typedef struct {
    bool     ready;
    uint32_t mtu;
    uint32_t tx_sent;
    uint32_t tx_too_big;      /* P3：每条拒绝路径都要可观测 */
    uint32_t tx_backpressure;
    uint32_t tx_not_ready;
    uint32_t tx_fatal;
    uint32_t tx_driver_error; /* 驱动返回了未归类结果（应为 0） */
} link_stats_t;

link_t *link_create(const link_driver_t *drv, void *drv_ctx);
void    link_destroy(link_t *l);

/**
 * 打开链路：调用驱动 open，成功则刷新 ready/mtu 快照。返回驱动结果（不压平）。
 */
link_result_t link_open(link_t *l);

/**
 * 唯一的上行入口。确定性行为（由 link_tests.c 逐条锁定）：
 *   1. l/drv/frame 为空 或 len==0      -> LINK_FATAL（参数错，不是背压）
 *      注意：l==NULL 时【无法计数】（没有对象可写）—— 这是接口固有限制，
 *      故该次调用不出现在 stats 里；其余 4 类路径全部可观测。
 *   2. len > drv->mtu()                -> LINK_PAYLOAD_TOO_BIG，且【不调用】drv->send
 *                                         （P2：端到端契约在【发出前】校验）
 *   3. !drv->is_ready()                -> LINK_NOT_READY，且【不调用】drv->send
 *   4. 否则原样返回 drv->send 的结果   （【不压平】—— D-01 的病根就是压平）
 *   5. 每条路径都累加对应计数器        （P3）
 */
link_result_t link_send(link_t *l, const uint8_t *frame, size_t len);

void link_get_stats(const link_t *l, link_stats_t *out);

/** 结果名（日志/测试用）—— 避免结果名在多个文件里各写一遍（P4）。 */
const char *link_result_name(link_result_t r);

/**
 * 该结果是否属于【错误】—— 即调用方应当上报/重建，而不是按正常流程继续或稍后重试。
 *
 * 这条区分就是整个 link 接口存在的理由（旧 esp_err_t 只有成功/失败两档）：
 *   LINK_SENT         否 —— 成功
 *   LINK_BACKPRESSURE 否 —— **可重试的正常状态**，不是故障
 *   LINK_NOT_READY    否 —— 链路尚未就绪，属于等待而非故障
 *   LINK_PAYLOAD_TOO_BIG 是 —— 调用方违反了契约（本应先分片/拒绝）
 *   LINK_FATAL        是 —— 链路不可用
 */
bool link_result_is_error(link_result_t r);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_LINK_H */
