/**
 * @file transport.h
 * @brief 统一的传输层抽象接口
 * 
 * MQTT 和 TCP 都实现这个接口，上层业务逻辑使用统一接口
 */

#ifndef TRANSPORT_H
#define TRANSPORT_H

#include <stdint.h>
#include <stdbool.h>
#include <stddef.h>
#include "esp_err.h"

#ifdef __cplusplus
extern "C" {
#endif

/* === Transport 状态 === */
typedef enum {
    TRANSPORT_DISCONNECTED,
    TRANSPORT_CONNECTING,
    TRANSPORT_CONNECTED,
    TRANSPORT_FAILED,
} transport_state_t;

/* === Transport 类型 === */
typedef enum {
    TRANSPORT_TYPE_MQTT,
    TRANSPORT_TYPE_TCP,
} transport_type_t;

/* === 定帧契约（D-09，2026-10-06）===
 *
 * 为什么必须【显式声明】：原先 transport_msg_cb_t 只写了"消息回调"四个字，
 * "一次回调 = 一条完整消息"这个前提【只存在于实现者的脑子里】——
 * MQTT 恰好满足（协议栈自带分帧），TCP **不满足**（字节流：可能半帧、可能粘帧）。
 * 于是 ehome_tcp.c 把一次 recv() 的内容直接当完整消息回调。
 * 契约不写下来，换一个实现就会重新踩一遍。
 *
 * ⇒ 交付语义必须是契约的一部分，并且有唯一来源（可用 transport_framing_of() 查询）。 */
typedef enum {
    /* 每次回调交付【一条完整消息】，消费者可直接解析。 */
    TRANSPORT_DELIVERS_MESSAGES = 0,
    /* 每次回调交付【任意长度的字节块】，消费者【必须】自行定界
     * （本仓使用 components/wire 的 reassembler）。 */
    TRANSPORT_DELIVERS_STREAM,
    TRANSPORT_FRAMING_UNKNOWN,   /* 未知类型：不猜，让调用方看见（P3 精神） */
} transport_framing_t;

/** 查询某类型传输的交付语义 —— "这次回调是什么"的单一来源（P4）。 */
transport_framing_t transport_framing_of(transport_type_t type);

/** 人类可读名（日志/测试用，避免结果名在多处各写一遍）。 */
const char *transport_framing_name(transport_framing_t f);

/* === 回调 ===
 * 注意：data 的【定界语义】取决于 transport_framing_of(transport->type)，
 * 回调实现【必须】先查询后再决定能否直接按完整消息解析。 */
typedef void (*transport_msg_cb_t)(const uint8_t *data, size_t len, void *ctx);
typedef void (*transport_state_cb_t)(transport_state_t state, void *ctx);

/* === Transport 接口 === */
typedef struct transport_ops transport_ops_t;

typedef struct transport {
    const transport_ops_t *ops;
    transport_type_t type;
    transport_state_t state;
    transport_msg_cb_t msg_cb;
    void *msg_cb_ctx;
    transport_state_cb_t state_cb;
    void *state_cb_ctx;
    void *priv_data;  // 各 transport 的私有数据
} transport_t;

/* === 生命周期契约（D-21，2026-10-06）===
 *
 * 先更正一处审计误判：审计称 "init/deinit 在两个实现里都是 no-op/空函数"，
 * 其中对 TCP 的引用（ehome_tcp.c:153-161）实际是 **tcp_init**，不是 deinit。
 * TCP 的 deinit 是 tcp_stop()，做了真实工作；且 tcp_transport_destroy()
 * 会先调 deinit 再释放内存。=> TCP **履行**了契约。
 * 真正未履行的是 **MQTT 的 deinit**（空函数，注释自陈 "ehome_mqtt 没有 deinit API"）。
 *
 * 真正的问题不是"没人实现"，而是【契约没写下来】：
 * 现状下没人能判断 deinit 该不该释放 priv_data。
 * tcp_transport_destroy() 假定 deinit **不**释放（它自己释放）；
 * 若将来有人把释放写进 deinit，就会 **double free**。
 *
 * 因此明确划分【两个生命周期】：
 *   create / destroy —— 拥有【内存】：transport_t 与 priv_data 的分配与释放。
 *   init   / deinit  —— 拥有【运行时资源】：socket、任务、信号量等，以及 stop/close 语义。
 *                       **不负责释放 priv_data 本身**。
 *
 * 三条约定：
 *   1. deinit 必须【幂等】（可被多次调用）—— destroy 会调它，调用方也可能先调；
 *   2. destroy 必须在释放 priv_data **之前**调用 deinit；
 *   3. init / deinit 允许为 NULL，表示"本实现无需运行时初始化/清理"，
 *      但必须【是有意的】而不是遗漏 —— 用 transport_audit_ops() 显式检查。
 */
/* === Transport 操作接口 === */
struct transport_ops {
    esp_err_t (*init)(transport_t *transport, const void *config);
    esp_err_t (*start)(transport_t *transport);
    esp_err_t (*stop)(transport_t *transport);
    esp_err_t (*send)(transport_t *transport, const uint8_t *data, size_t len);
    bool (*is_connected)(transport_t *transport);
    void (*deinit)(transport_t *transport);
};

/* === Transport Manager API === */

/**
 * @brief 初始化 Transport Manager
 */
void transport_manager_init(void);

/**
 * @brief 注册一个 transport
 */
esp_err_t transport_register(transport_t *transport);

/**
 * @brief 注销一个 transport
 */
esp_err_t transport_unregister(transport_t *transport);

/**
 * @brief 向所有已连接的 transport 发送消息
 */
esp_err_t transport_broadcast(const uint8_t *data, size_t len);

/**
 * @brief 查询注册表中是否存在指定类型的 transport
 *
 * 用途：调用方在"广播失败后是否还要直接重试某条具体通路"上做决策。
 * MQTT 适配器一旦注册进 manager，transport_broadcast() 就已经对它发过，
 * 再直接调一次即为**同一帧的重复发布**（L-02 根因）。
 * 它能作答的问题："MQTT 是否已经在我刚刚那次广播里被尝试过了？"
 */
bool transport_registry_has_type(transport_type_t type);

/**
 * @brief 向指定的 transport 发送消息
 */
esp_err_t transport_send(transport_t *transport, const uint8_t *data, size_t len);

/**
 * @brief 获取第一个已连接的 transport
 */
transport_t *transport_get_connected(void);

/**
 * @brief 检查是否有任何 transport 已连接
 */
bool transport_any_connected(void);


/* === 操作表自审（D-21）===
 * 让"哪个钩子有、哪个没有"变成可断言的【数据】，而不是靠人读源码。
 * 与 dispatch 的 disp_audit_table() 同一思路：表驱动 + 可审计。 */
typedef struct {
    bool   has_init;
    bool   has_deinit;
    bool   has_start;
    bool   has_stop;
    bool   has_send;
    bool   has_is_connected;
    size_t required_missing;   /* 必需钩子缺失数（应恒为 0） */
    size_t optional_missing;   /* 可选钩子缺失数（init/deinit，允许 >0，但要看得见） */
} transport_ops_audit_t;

/** 必需钩子：缺失即不可用。init/deinit 为可选（但必须是有意的）。 */
void transport_audit_ops(const transport_ops_t *ops, transport_ops_audit_t *out);

#ifdef __cplusplus
}
#endif

#endif /* TRANSPORT_H */
