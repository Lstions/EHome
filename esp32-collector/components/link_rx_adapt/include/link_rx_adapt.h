/**
 * @file link_rx_adapt.h
 * @brief 适配层：把 link_tcp_read 接成 rx_pump 的 rx_read_fn_t
 *
 * ## 这是**第 4 个**读返回值世界（同一类陷阱的第 4 次出现）
 *
 * | 世界 | 暂无数据 | 对端关闭 | 错误 | 有数据 |
 * |---|---|---|---|---|
 * | esp_tls 原始 | （负值）| **0** | 其余负值 | >0 |
 * | tls_io 归约 | AGAIN | CLOSED | ERROR | DATA |
 * | link_tcp_io_t | 0 | -1 | -2 | >0 |
 * | **link_read_result_t** | AGAIN | CLOSED | FATAL | **DATA = 0** |
 * | rx_read_fn_t | **0** | -1 | -2 | >0 |
 *
 * ## ⭐ 本层最危险的一点：**枚举值撞车**
 * `LINK_READ_DATA` 是 **0**，而 `RX_IO_AGAIN` 也是 **0**，
 * 但两者含义**相反**（一个有数据、一个没数据）。
 *
 * ⇒ 任何人图省事写 `return (int)link_tcp_read(...)` 或"直接透传"，
 *   都会把**读到的数据当成"暂无数据"丢掉**，且计数上看只是 `again++`，
 *   **看不出任何异常**（连接看着正常、就是一直没消息）。
 *
 * 这类"枚举值撞车"比映射写错更隐蔽：**编译器不会警告**（两边都是 int）。
 * ⇒ 本层用 `_Static_assert` 把这个撞车**显式记录下来**，
 *   并用逐值 switch 翻译（而不是算术/强制转换）。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_LINK_RX_ADAPT_H
#define EHOME_LINK_RX_ADAPT_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "link_tcp.h"   /* link_read_result_t, link_tcp_read */
#include "rx_pump.h"    /* RX_IO_AGAIN / RX_IO_CLOSED / RX_IO_ERROR, rx_read_fn_t */

#ifdef __cplusplus
extern "C" {
#endif

/* 编译期守卫：把"两套约定"钉死。任一边改了，这里编译失败而不是静默错位。 */
_Static_assert(RX_IO_AGAIN == 0 && RX_IO_CLOSED == -1 && RX_IO_ERROR == -2,
               "rx_pump 的读约定变了 —— 请同步本适配层与它的测试");
_Static_assert(LINK_READ_DATA == 0,
               "LINK_READ_DATA 不再是 0 —— 本层注释里那条'撞车'警告需要更新");

/**
 * ⭐ 把 link_read_result_t 翻成 rx_read_fn_t 的 int 约定。
 *
 * @param r     link_tcp_read 的结果
 * @param n     当 r == LINK_READ_DATA 时的字节数
 * @return      >0 字节数 ／ RX_IO_AGAIN(0) ／ RX_IO_CLOSED(-1) ／ RX_IO_ERROR(-2)
 *
 * 映射表（逐条由 host_tests/link_rx_adapt_tests.c 锁定）：
 *   LINK_READ_DATA      -> (int)n          **不是 0**（这是本层的核心）
 *   LINK_READ_AGAIN     -> RX_IO_AGAIN
 *   LINK_READ_CLOSED    -> RX_IO_CLOSED
 *   LINK_READ_NOT_READY -> RX_IO_CLOSED    （见下）
 *   LINK_READ_FATAL     -> RX_IO_ERROR
 *
 * **为什么 NOT_READY 也翻成 CLOSED**：rx_pump 的 4 个槽位里没有"链路未建立"
 * 这一档，而 NOT_READY 与 CLOSED 对调用方的**动作相同** —— 都是"连接不可用，
 * 去重建，且**不算故障**"（不要告警）。为了不丢信息，本层在自己的计数里
 * 把两者分开记（见 link_rx_adapt_get_stats），rx_pump 侧则合并为 closed。
 */
int link_rx_adapt_result(link_read_result_t r, size_t n);

/**
 * rx_read_fn_t 的实现：从绑定的 link_tcp 上下文读。
 *
 * 用法：
 *   link_rx_binding_t *b = link_rx_binding_new(tcp_ctx);
 *   rx_pump_t *p = rx_pump_create(max_payload, link_rx_adapt_read, b, cb, NULL,
 *                                 rbuf, sizeof rbuf);
 *   ...
 *   rx_pump_destroy(p);
 *   link_rx_binding_free(b);   // 在 rx_pump 之后释放
 */
typedef struct link_rx_binding link_rx_binding_t;

link_rx_binding_t *link_rx_binding_new(link_tcp_ctx_t *tcp);
void link_rx_binding_free(link_rx_binding_t *b);

int link_rx_adapt_read(void *ctx, uint8_t *buf, size_t cap);

/** 诊断计数。**分开记** NOT_READY 与 CLOSED，避免信息丢失（P3）。 */
typedef struct {
    uint32_t data;
    uint32_t again;
    uint32_t closed;
    uint32_t not_ready;
    uint32_t fatal;
} link_rx_adapt_stats_t;

void link_rx_adapt_get_stats(link_rx_adapt_stats_t *out);
void link_rx_adapt_reset_stats(void);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_LINK_RX_ADAPT_H */
