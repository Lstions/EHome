/**
 * @file tls_io.h
 * @brief esp_tls 返回值 -> 本仓统一语义（归约层）
 *
 * ## 为什么需要单独一层（本轮实测 esp_tls 源码后确认的三个陷阱）
 *
 * esp_tls 的返回值语义与本仓 link_tcp 的契约**不是一回事**，
 * 而"朴素映射"会造成**静默失联**：
 *
 * | 陷阱 | esp_tls 实际语义 | 朴素映射的后果 |
 * |---|---|---|
 * | 1 | \c esp_tls_conn_read 返回 **0 = 对端关闭**（源码：\c PEER_CLOSE_NOTIFY -> return 0）| 映射成本仓的 0(暂无数据) ⇒ **永不重连**，且看不出异常 |
 * | 2 | \c ESP_TLS_ERR_SSL_WANT_READ/WRITE 是**负数**，含义却是"暂无数据，稍后再来" | 映射成 ERROR ⇒ 健康连接被反复重连 |
 * | 3 | \c ESP_TLS_ERR_SSL_TIMEOUT(-0x6800) 是**负数**，含义同样是"窗口内没数据" | 同上 |
 *
 * ⇒ **"<0 就是错误"是错的**。必须逐码归约。
 *
 * 本模块把归约做成**纯函数**（不依赖 IDF）⇒ 可在宿主穷举测试；
 * 真调用 esp_tls 的薄适配层另放（\c tls_esp.c，依赖 IDF）。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_TLS_IO_H
#define EHOME_TLS_IO_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/* === 镜像 mbedTLS/esp_tls 常量（值必须与 IDF 一致，由 check_tls_constants 核对）===
 * mbedtls/ssl.h:  WANT_READ -0x6900 / WANT_WRITE -0x6880 / TIMEOUT -0x6800
 *                 PEER_CLOSE_NOTIFY -0x7880
 */
#define TLS_IO_WANT_READ   (-0x6900)
#define TLS_IO_WANT_WRITE  (-0x6880)
#define TLS_IO_TIMEOUT     (-0x6800)
#define TLS_IO_PEER_CLOSE  (-0x7880)

/** 读操作归约结果 —— 每个取值对应调用方一个明确分支（P1）。 */
typedef enum {
    TLS_IO_READ_DATA = 0,   /* n > 0：读到了字节 */
    TLS_IO_READ_AGAIN,      /* 暂无数据：WANT_READ / WANT_WRITE / TIMEOUT —— **正常** */
    TLS_IO_READ_CLOSED,     /* 对端正常关闭（esp 返回 0 / PEER_CLOSE_NOTIFY）—— **非故障** */
    TLS_IO_READ_ERROR,      /* 真错误：应重建连接并计数 */
} tls_io_read_t;

const char *tls_io_read_name(tls_io_read_t r);

/** 写操作归约结果。 */
typedef enum {
    TLS_IO_WRITE_WROTE = 0, /* n >= 0：写出 n 字节（含 0 = 一个都没写出） */
    TLS_IO_WRITE_AGAIN,     /* WANT_READ / WANT_WRITE / TIMEOUT —— 发送缓冲满，**可重试** */
    TLS_IO_WRITE_ERROR,     /* 真错误 */
} tls_io_write_t;

const char *tls_io_write_name(tls_io_write_t r);

/**
 * ⭐ 归约读返回值。
 *
 * @param raw  esp_tls_conn_read 的原始返回值（ssize_t）
 * @param n_out 当结果为 DATA 时，输出字节数（否则写 0）
 *
 * 规则（逐条由 host_tests/tls_io_tests.c 锁定）：
 *   raw > 0                        -> DATA(raw)
 *   raw == 0                       -> **CLOSED**（对端关闭，**不是"暂无数据"**）
 *   raw == WANT_READ/WANT_WRITE    -> AGAIN
 *   raw == TIMEOUT                 -> AGAIN
 *   raw == PEER_CLOSE_NOTIFY       -> CLOSED
 *   其它 raw < 0                   -> ERROR
 *
 * **最要紧的一条**：\c raw == 0 必须归为 CLOSED 而不是 AGAIN。
 * 归错会让断开的连接被当成"还在等数据"⇒ 永不重连（静默失联）。
 */
tls_io_read_t tls_io_reduce_read(long raw, size_t *n_out);

/** 归约写返回值。raw 是 esp_tls_conn_write 的原始返回值。
 *  raw >= 0 是字节数（含 0=一个都没写出，属背压）；
 *  WANT_READ / WANT_WRITE / TIMEOUT 表示稍后重试；其余负数才是错误。 */
tls_io_write_t tls_io_reduce_write(long raw, size_t *n_out);

/** 该结果是否表示"连接已不可再用"（调用方必须重建）。 */
bool tls_io_read_ends_connection(tls_io_read_t r);

/** 归约层计数（P3：每条路径都要可见，否则"为什么一直连不上"无法回答）。 */
typedef struct {
    uint32_t read_data;
    uint32_t read_again;
    uint32_t read_closed;
    uint32_t read_error;
    uint32_t write_wrote;
    uint32_t write_again;
    uint32_t write_error;
} tls_io_stats_t;

tls_io_read_t  tls_io_note_read(long raw, size_t *n_out);
tls_io_write_t tls_io_note_write(long raw, size_t *n_out);
void tls_io_get_stats(tls_io_stats_t *out);
void tls_io_reset_stats(void);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_TLS_IO_H */
