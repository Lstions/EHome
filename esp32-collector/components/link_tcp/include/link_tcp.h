/**
 * @file link_tcp.h
 * @brief TCP + TLS 的 link 驱动（3.0 主传输）—— 设计依据 ESP32-3.0-重构方案 §4
 *
 * ## 连接模型（设计 §4.1，**原文规定**，不是本文件自定）
 * > 设备作为 **TCP client 主动连接**后端设备端口（建议 EHOME_DEVICE_PORT，
 * > 默认 **8443**，**mTLS**），一条长连接承载双向流量。
 * 因此本驱动是【客户端外连】，**不是**沿用 ehome_tcp.c 的服务端 accept。
 *
 * ## 为什么把 I/O 抽成函数指针
 * 约束 C2（host-first）：状态机（退避、部分写、错误分级）必须能在宿主机上测。
 * 若直接调 esp_tls_*，那些逻辑就只能靠实机验证 —— 而 D-03/D-09/D-10 的教训是
 * "从没被宿主编译过的代码里藏着不可达分支与契约缺失"。
 * ⇒ 本驱动的 I/O 全部经 `link_tcp_io_t` 注入：
 *     设备上注入 esp_tls 实现；宿主测试注入可控假实现。
 *
 * ## 三个必须由【驱动】表达、不能由调用方猜的语义
 *   1. **部分写**：写不完必须续写；写不动（缓冲满）→ LINK_BACKPRESSURE（可重试），
 *      **不是**失败。旧实现把 send()>0 记成功 ⇒ 短写静默发半帧（D-10）。
 *   2. **错误分级**：网络不可达（软）→ 退避重连（LINK_NOT_READY）；
 *      证书/配置错（硬）→ LINK_FATAL，**不该盲目重试**。
 *   3. **退避重置**：设计 §4.2 明确"完成一次**应用层握手**（Hello/HelloAck）
 *      才重置，**不是** socket connect" —— 防"连上但不通"导致退避不重置的抖振。
 */
#ifndef EHOME_LINK_TCP_H
#define EHOME_LINK_TCP_H

#include "link.h"

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/** 单帧上限（字节）。设计 §5.2：total_len 硬上界 16,384 ——
 *  同时不超过 MBEDTLS_SSL_IN_CONTENT_LEN，使**一个 TLS 记录即可承载整条消息**，
 *  避免"重组边界"与"TLS 记录边界"双重分片。
 *  这也就是本驱动向编码层报出的 mtu（P2：编码层据此决定是否分片）。 */
#define LINK_TCP_MTU_BYTES 16384u

/** 退避上限（ms）。设计 §4.2：1→2→4→8→16→30→60（上限）。 */
#define LINK_TCP_BACKOFF_MAX_MS 60000u

/** 抖动幅度（千分比）。设计 §4.2：±20%，防 N 节点同频重连风暴。 */
#define LINK_TCP_JITTER_PERMILLE 200u

/**
 * I/O 原语 —— 设备上由 esp_tls 实现；宿主测试注入假实现。
 */
typedef struct {
    /**
     * 建立连接（含 TLS 握手与 mTLS 校验）。
     * @param hard_fatal 输出：true 表示**不可重试**的失败（证书校验失败、
     *        配置缺失、CA 不匹配）。false 表示软失败（网络不可达、超时），应退避重试。
     * @return 连接句柄；失败返回 NULL。
     */
    void *(*connect)(void *io_ctx, bool *hard_fatal);

    /**
     * 写入。
     * @return >0 已写入的字节数（可小于 len，即部分写）；
     *         0  **暂时**写不动（发送缓冲满）—— 调用方应退避重试，不是错误；
     *         <0 硬错误（连接已断）。
     */
    int (*write)(void *handle, const uint8_t *data, size_t len);

    /** 关闭连接（幂等）。 */
    void (*close)(void *handle);
} link_tcp_io_t;

typedef struct {
    const link_tcp_io_t *io;
    void                *io_ctx;   /* 传给 io->connect 的上下文（host/port/tls 配置） */
} link_tcp_config_t;

/**
 * 链路上下文（**不透明**）—— 持有连接句柄、退避计数、诊断计数。
 *
 * 为什么不用全局单例：本仓的既有教训是"全局状态让模块无法独立建实例测试"
 * （设计 §3 的 S5 目标）。而且第一版就是单例 —— `link_create(drv, NULL)`
 * 把 ctx 传成 NULL，所有回调拿到 NULL 直接失败（测试当场抓出）。
 * ⇒ 显式返回上下文，由调用方交给 link_create。
 *
 * 生命周期：link_tcp_new() 分配；用完由 link_tcp_free() 释放
 * （应在 link_destroy **之后**调用，避免驱动还在用就释放）。
 */
typedef struct link_tcp_ctx link_tcp_ctx_t;

/** 建立上下文并把 cfg【复制】进来（cfg 可随即失效）。失败返回 NULL。 */
link_tcp_ctx_t *link_tcp_new(const link_tcp_config_t *cfg);

/** 释放上下文。内部不隐式关闭连接 —— 先用 link_destroy 关链路。 */
void link_tcp_free(link_tcp_ctx_t *c);

/** 取驱动实例（无状态单例，可安全多处引用）。 */
const link_driver_t *link_tcp_driver(void);

/**
 * 退避毫秒数（**纯函数**，宿主可测）。
 *
 * @param attempt      连续失败次数（0 起算）：0→1s, 1→2s, 2→4s, 3→8s,
 *                     4→16s, 5→30s, 6+→60s（上限）。
 * @param rand_permille 随机数 [0,1000]，映射到 ±20% 抖动。
 *                      由调用方提供 ⇒ 本函数保持纯函数（随机源留在边界）。
 */
uint32_t link_tcp_backoff_ms(uint32_t attempt, uint32_t rand_permille);

/**
 * 通知"**应用层握手**已完成"（收到 HelloAck）⇒ 退避计数归零。
 * 设计 §4.2：重置条件是应用层握手，**不是** socket connect。
 * 传 NULL 表示当前没有活动链路（等价于重置）。
 */
void link_tcp_note_handshake(link_tcp_ctx_t *c);

/** 当前连续失败次数（诊断用）。 */
uint32_t link_tcp_reconnect_attempt(const link_tcp_ctx_t *c);

/** 连接是否已建立（socket 层，**不含**应用层握手）。 */
bool link_tcp_is_connected(const link_tcp_ctx_t *c);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_LINK_TCP_H */
