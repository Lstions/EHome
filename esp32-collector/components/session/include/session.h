/**
 * @file session.h
 * @brief 3.0 设备侧会话：把各层组合成"一条长连接"（对上只暴露状态与消息）
 *
 * ## ⚠ 为什么不是"接进 ehome_tcp.c"（纠正我自己上一轮的路线）
 * 我上一轮把"把 rx_pump 接进 ehome_tcp.c"列为下一步。**那是错的**：
 *   - 设计 §4.1（方案:342）："**3.0：设备作为 TCP client** 主动连接后端设备端口
 *     （默认 8443，mTLS），一条长连接承载双向流量"；
 *   - 而 ehome_tcp.c 是 **TCP server**（bind/listen/accept，多客户端）；
 *   - 且它被 `#ifdef CONFIG_DEBUG_TCP_ENABLED` 包着，注释原文写着
 *     "TCP transport (parallel with MQTT, **debug only**)"。
 *
 * ⇒ 二者是**相反的角色**：`ehome_tcp` = 设备听、别人连进来（调试用）；
 *   3.0 = 设备连出去（生产用）。把客户端接收路径塞进一个调试用的服务端传输
 *   是**范畴错误**，且会同时弄脏两条路径。
 * ⇒ 3.0 的入口是**新的客户端会话**（本模块），不碰 `ehome_tcp.c`。
 *
 * ## 本模块负责什么
 * 串起这些已有层（每层都已被单独测过）：
 *   `tls_esp`(mTLS)  →  `link_tcp`(链路状态机/退避/发送)
 *                    →  `link_rx_adapt`(契约翻译)  →  `rx_pump`(定界交付)
 * 本模块**只新增"什么时候该重连"这一件事**（其余判断都在下层）。
 *
 * ## ⭐ 核心规则（设计 §4.2）：退避计数只由**应用层握手**重置
 * **不是** socket connect 成功就重置。
 * 若一 connect 成功就归零，会得到一种很难查的故障：链路能连上但业务不通
 * （证书/协议/后端未就绪）⇒ 每 1 秒重连一次 ⇒ **重连风暴**，且日志上
 * 只看到"不断重连"，看不出"其实一直连上了"。
 * ⇒ 重置点放在 `session_note_handshake()`，由上层在收到 HelloAck 时调用。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_SESSION_H
#define EHOME_SESSION_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "link.h"
#include "link_tcp.h"
#include "rx_pump.h"

#ifdef __cplusplus
extern "C" {
#endif

/** 会话状态。每个取值对应调用方一个明确处置（P1）。 */
typedef enum {
    SESSION_DOWN = 0,        /* 未连接，可以尝试建立 */
    SESSION_WAIT_HANDSHAKE,  /* 链路已通，等**应用层**握手（Hello/HelloAck）*/
    SESSION_READY,           /* 握手完成，正常收发 */
    SESSION_BACKOFF,         /* 退避中：到点前不重试（**可自愈**）*/
    SESSION_FATAL,           /* 硬失败：重试无意义，需人工（证书/配置）*/
} session_state_t;

const char *session_state_name(session_state_t s);

/**
 * 依赖注入。**全部由调用方提供**，本模块不隐式分配大块内存、不读全局时钟。
 */
typedef struct {
    const link_tcp_io_t *io;    /* tls_esp_io() 或宿主测试的假实现 */
    void                *io_ctx; /* 通常是 tls_esp 的配置对象 */
    uint32_t             max_payload;  /* 单条消息载荷上界（传给定界器）*/
    uint8_t             *rx_buf;       /* 读缓冲，调用方提供 */
    size_t               rx_buf_cap;
    /** 单调毫秒时钟（注入 ⇒ 宿主可测）。 */
    uint64_t (*now_ms)(void);
    /** 随机数 [0,1000]（注入 ⇒ 保持核心逻辑纯；抖动幅度见 link_tcp_backoff_ms）。 */
    uint32_t (*rand_permille)(void);
    /** 收到一条完整消息时回调；返回 false 表示要求停止本轮泵送。 */
    rx_msg_cb_t on_msg;
    void       *on_msg_ctx;
} session_config_t;

typedef struct session session_t;

/** 创建会话。任一必需参数缺失即返回 NULL（不构造半成品）。 */
session_t *session_create(const session_config_t *cfg);
void       session_destroy(session_t *s);

/**
 * 推进一次会话（调用方在循环/任务里反复调用）。
 *
 * @param delivered_out 本轮交付的消息条数（可传 NULL）
 * @return 当前状态（与 session_state() 一致）
 *
 * 确定性行为（由 host_tests/session_tests.c 逐条锁定）：
 *  - DOWN：尝试 link_open；成功 -> WAIT_HANDSHAKE；
 *          软失败 -> BACKOFF（下次重试时刻 = 退避表 + 抖动）；
 *          硬失败（LINK_FATAL）-> **FATAL**（不盲目重试）；
 *  - WAIT_HANDSHAKE / READY：泵读并交付消息；链路断开 -> BACKOFF；
 *  - BACKOFF：未到点不动；到点 -> DOWN；
 *  - FATAL：**不动**（需人工介入，避免无限重试掩盖真问题）。
 */
session_state_t session_poll(session_t *s, uint32_t *delivered_out);

/**
 * ⭐ 上层通知"**应用层握手已完成**"（收到 HelloAck）⇒ 退避计数归零、进入 READY。
 *
 * 这是**唯一**的重置点（设计 §4.2）。socket 连上**不算**握手。
 */
void session_note_handshake(session_t *s);

session_state_t session_state(const session_t *s);
uint32_t session_reconnect_attempt(const session_t *s);

/** 诊断计数。 */
typedef struct {
    uint32_t connects_ok;        /* link_open 成功次数 */
    uint32_t connects_soft_fail; /* 软失败（可重试）*/
    uint32_t connects_hard_fail; /* 硬失败（-> FATAL）*/
    uint32_t handshakes;         /* 应用层握手完成次数（= 退避归零次数）*/
    uint32_t backoffs_entered;
    uint32_t msgs_delivered;
    uint32_t links_dropped;      /* 已连上后断开次数 */
} session_stats_t;

void session_get_stats(const session_t *s, session_stats_t *out);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_SESSION_H */
