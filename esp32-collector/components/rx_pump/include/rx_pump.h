/**
 * @file rx_pump.h
 * @brief 接收泵：把**字节流**变成**消息**（定界），并如实上报每条失败路径
 *
 * ## 它解决什么
 * 旧实现（ehome_tcp.c:477）把一次 recv() 的内容直接当一条完整消息回调：
 *
 *     ssize_t received = recv(socket, recv_buf, 2048, 0);
 *     transport->msg_cb(recv_buf, received, ...);   // <-- 一次 recv == 一条消息
 *
 * 这在 MQTT 上恰好成立、在 TCP 流上不成立 —— 属 D-09。
 * 本模块把"读字节 → 定界 → 交付消息"这条链**显式化**，并让每一档失败可观测。
 *
 * ## 为什么单独一个组件（不塞进 link_tcp）
 * 1. **协议无关**：定界归 wire，读字节归 link_tcp，本模块只负责"循环 + 派发"；
 * 2. **宿主可测**：用假 io 驱动，无需真 socket 就能断言"半条消息不会被交付"、
 *    "一次读到三条要交付三条"、"EOF 与硬错误处置不同"；
 * 3. **可复用**：将来若接 BLE 配网通道（若做数据面），同一套定界逻辑可用。
 *
 * ## 三种收尾必须区分（P1）
 * | 情况 | 含义 | 处置 |
 * |---|---|---|
 * | 无数据（超时） | 正常 | 继续读，不计错 |
 * | 对端关闭（EOF） | 对端有意结束 | 重建连接，**不算故障** |
 * | 硬错误 | 链路故障 | 重建连接 + 计数（可能要告警） |
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_RX_PUMP_H
#define EHOME_RX_PUMP_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "wire.h"

#ifdef __cplusplus
extern "C" {
#endif

/** 一次 rx_pump_step 的结果。 */
typedef enum {
    RX_PUMP_IDLE = 0,     /* 本轮无数据（超时）。正常，继续下一步 */
    RX_PUMP_DELIVERED,    /* 交付了 >=1 条消息（见 delivered 出参） */
    RX_PUMP_CLOSED,       /* 对端正常关闭（EOF）：调用方应重建连接，**非故障** */
    RX_PUMP_ERROR,        /* 读或定界失败：调用方应重建连接并计数 */
    RX_PUMP_FATAL,        /* 参数错/内部错 */
} rx_pump_result_t;

const char *rx_pump_result_name(rx_pump_result_t r);

/**
 * 一条被定界出来的消息。
 *
 * 生命周期：payload 指向定界器**内部**缓冲，仅在本次回调期间有效 ——
 * 回调返回后不得保存该指针（与 wire_delim 的零拷贝契约一致）。
 */
typedef struct {
    uint8_t  ver;
    uint8_t  type;
    uint16_t flags;
    uint32_t seq;
    uint16_t payload_len;
    const uint8_t *payload;
} rx_msg_t;

/** 收一条消息时的回调。返回 false 表示调用方要求**停止**本轮泵送。
 *  需要拷贝就走拷贝 —— 回调返回后 payload 指针即失效。 */
typedef bool (*rx_msg_cb_t)(const rx_msg_t *msg, void *ctx);

/** 读一段字节流（通常就是 link_tcp_read 的适配）。
 *  返回：>0 字节数；RX_IO_AGAIN 暂无数据；RX_IO_CLOSED 对端关闭；RX_IO_ERROR 硬错误。 */
#define RX_IO_AGAIN  0
#define RX_IO_CLOSED (-1)
#define RX_IO_ERROR  (-2)
typedef int (*rx_read_fn_t)(void *ctx, uint8_t *buf, size_t cap);

/**
 * 错误计数 —— **单一事实来源**（P3）：每条失败路径都必须能被观测到。
 * 不静默丢弃：任何一条计数非零都说明线上出过对应情况。
 */
typedef struct {
    uint32_t msgs_delivered;   /* 成功交付的消息数 */
    uint32_t bytes_read;       /* 累计读到的**字节数**（与消息数无关） */
    uint32_t crc_errors;       /* CRC32C 不符 */
    uint32_t malformed;        /* 头非法（magic/ver），已重新同步 */
    uint32_t too_large;        /* 声明长度超上界（**未缓冲**） */
    uint32_t io_errors;        /* 读硬错误 */
    uint32_t closed;           /* 对端正常关闭（非错误） */
    uint32_t again;            /* 暂无数据（非错误） */
    uint32_t oversize_feed;    /* 单次读入超过定界缓冲 */
} rx_pump_stats_t;

typedef struct rx_pump rx_pump_t;

/**
 * 创建接收泵。
 * @param max_payload 单条消息载荷上界（传给定界器，P5：唯一来源）
 * @param read_fn     读取函数（注入 ⇒ 宿主可测）
 * @param read_ctx    传给 read_fn 的上下文
 * @param cb          收到完整消息时的回调
 * @param cb_ctx      传给 cb 的上下文
 * @param read_buf    读缓冲（**由调用方提供**，避免本模块隐式分配大块内存）
 * @param read_buf_cap 读缓冲容量
 */
rx_pump_t *rx_pump_create(uint32_t max_payload,
                          rx_read_fn_t read_fn, void *read_ctx,
                          rx_msg_cb_t cb, void *cb_ctx,
                          uint8_t *read_buf, size_t read_buf_cap);

/**
 * 创建"投喂型"接收泵（**由调用方自己读字节**）。
 *
 * ## 为什么需要第二个构造函数
 * rx_pump_create 要求注入 read_fn，适用于"泵自己驱动读取"的形态（link_tcp）。
 * 但既有的 ehome_tcp.c 已经在一个阻塞 recv() 循环里、**手里已经拿着这段字节** ——
 * 要求它再包一个 read_fn 只是把同一段数据绕一圈。
 *
 * 于是本构造函数显式表达"我只做定界与交付，不负责读"（P1：能力与依赖都摆在签名上）。
 * 与 rx_pump_create **共用同一个定界器与同一套交付代码** —— 这不是第二套实现，
 * 否则就是 P4 禁止的"同一语义两处定义"。
 *
 * 注意：该形态下 rx_pump_step 不可用（无 read_fn），只能调 rx_pump_feed。
 */
rx_pump_t *rx_pump_create_feeder(uint32_t max_payload,
                                 rx_msg_cb_t cb, void *cb_ctx);

void rx_pump_destroy(rx_pump_t *p);

/**
 * 泵送一次：读一段字节流，尽可能多地定界并交付。
 *
 * delivered_out 输出本轮交付的消息条数（可为 0）。
 *
 * 确定性行为（由 host_tests/rx_pump_tests.c 逐条锁定）：
 *  - **半条消息不会被交付**（字节流累积直到长度足够）；
 *  - 一次读入含 N 条消息 ⇒ 交付 N 条（旧实现只交付 1 条，剩余字节被当垃圾）；
 *  - 一条消息被拆成多次读入 ⇒ 仍然只交付 1 条（拼接正确）；
 *  - 回调返回 false ⇒ 立即停止泵送（剩余消息留到下一次）；
 *  - 头非法 / CRC 不符 / 超上界 ⇒ 计数 + 报 ERROR（**不静默跳过**）。
 */
rx_pump_result_t rx_pump_step(rx_pump_t *p, uint32_t *delivered_out);

/**
 * 把**已经读到的**一段字节投入定界，并交付其中所有完整消息。
 *
 * 这是 D-09 修复的缝合点：把旧的
 *     transport->msg_cb(recv_buf, received, ...)   // 一次 recv == 一条消息
 * 换成
 *     rx_pump_feed(pump, recv_buf, received, &res); // 字节流 -> 定界 -> 消息
 *
 * 与 rx_pump_step 的差别只有一个：字节由调用方给，而不是自己去 read。
 * 定界、交付、计数三者**完全共用**（同一 deliver_loop）。
 *
 * @param p       泵（须由 rx_pump_create_feeder 创建）
 * @param in      字节
 * @param n       字节数
 * @param res_out 可选，输出本轮结论；与 rx_pump_step 同一套枚举语义
 * @return 本轮交付的消息条数（0 表示"还没凑够一条完整消息"）
 *
 * 确定性行为（由 host_tests/rx_pump_feed_tests.c 逐条锁定）：
 *  - **半条消息不会被交付**（返回 0，字节留在定界器里等待后续）；
 *  - 一段字节含 N 条完整消息 ⇒ 交付 N 条（旧实现只交付 1 条，其余被当中文乱码）；
 *  - 一条消息被拆到两次 feed ⇒ 累积后**仍只交付 1 条**，且载荷拼接正确；
 *  - 头非法/CRC 不符/超上界 ⇒ 计数 + res_out = ERROR（**不静默跳过**）。
 */
size_t rx_pump_feed(rx_pump_t *p, const uint8_t *in, size_t n,
                    rx_pump_result_t *res_out);

/** 取错误计数（只读快照）。 */
void rx_pump_get_stats(const rx_pump_t *p, rx_pump_stats_t *out);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_RX_PUMP_H */
