/**
 * @file tls_link_adapt.h
 * @brief 适配层：把 tls_io 的归约结果接进 link_tcp_io_t 的契约
 *
 * ## 为什么需要（否则会**第二次**掉进同一个陷阱）
 *
 * 本仓现在同时存在**三套**"读返回值"约定，它们对 0 的含义各不相同：
 *
 * | 世界 | 暂无数据 | 对端关闭 | 错误 |
 * |---|---|---|---|
 * | esp_tls 原始 | （负值）| **0** | 其余负值 |
 * | tls_io 归约 | AGAIN | CLOSED | ERROR |
 * | link_tcp_io_t / rx_read_fn | **0** | -1 | -2 |
 *
 * 若把 esp 的返回值**直接透传**给 link/rx（那里 0 = "暂无数据"），
 * 就等于把 §56 刚修掉的陷阱换个位置重演一次：**对端关闭被当成没数据 ⇒ 永不重连**。
 *
 * ⇒ 跨契约必须**显式翻译**，而且翻译函数要能被穷举测试。
 *
 * ## 这一层的形状
 * `tls_io` 的归约结果 -> `link_tcp_io_t.read` 的 int 返回。纯函数，无 IDF 依赖。
 * 真正的 esp_tls 调用放在 `tls_esp.c`（依赖 IDF），它负责把裸返回值先喂给
 * `tls_io_reduce_read`，再交给本层的翻译函数。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_TLS_LINK_ADAPT_H
#define EHOME_TLS_LINK_ADAPT_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "link_tcp.h"   /* LINK_TCP_IO_AGAIN / CLOSED / ERROR */
#include "tls_io.h"     /* tls_io_read_t / tls_io_write_t */

#ifdef __cplusplus
extern "C" {
#endif

/** 编译期守卫：两套契约对"暂无数据"的取值必须一致（都应是 0）。
 *  若哪天有人改了其中一边，这里会**编译失败**而不是静默错位。 */
_Static_assert(LINK_TCP_IO_AGAIN == 0,
               "link_tcp 的 AGAIN 必须是 0 —— 本适配层假定如此");
_Static_assert(LINK_TCP_IO_CLOSED == -1 && LINK_TCP_IO_ERROR == -2,
               "link_tcp 的 CLOSED/ERROR 必须是 -1/-2");
_Static_assert(TLS_IO_WANT_READ == -0x6900 && TLS_IO_TIMEOUT == -0x6800,
               "tls_io 的软等待常量变了 —— 请同步 check_tls_constants");

/**
 * ⭐ 把归约结果翻译成 link_tcp_io_t.read 的返回值。
 *
 * @param r    tls_io_reduce_read 的结果
 * @param n    当 r 为 DATA 时读到的字节数（其它情况忽略）
 * @return     >0 字节数 ／ LINK_TCP_IO_AGAIN ／ LINK_TCP_IO_CLOSED ／ LINK_TCP_IO_ERROR
 *
 * **关键映射**：TLS_IO_READ_CLOSED -> LINK_TCP_IO_CLOSED（**不是 AGAIN**）。
 * 这一条正是本层存在的全部理由。
 */
int tls_link_adapt_read_result(tls_io_read_t r, size_t n);

/** 把归约结果翻译成 link_tcp_io_t.write 的返回值。
 *  契约：>0 已写出字节数；0 暂时写不动（背压）；<0 硬错误。
 *
 *  **注意**：写出 0 字节（背压）要返回 0，**不能**返回负值 ——
 *  link 层把 0 当"可重试"，把负值当"链路故障要重建"。 */
int tls_link_adapt_write_result(tls_io_write_t r, size_t n);

/** 该 link 返回值是否表示"连接已不可再用"。 */
bool tls_link_read_ends_connection(int link_read_ret);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_TLS_LINK_ADAPT_H */
