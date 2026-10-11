/* tls_io_tests.c —— esp_tls 返回值归约
 *
 * 本文件锁住三个**实测 esp_tls 源码后确认**的陷阱。它们都不会崩，
 * 只会让线上行为悄悄变坏 —— 这正是最该被测的一类。
 *
 * 陷阱 1（最严重）：esp_tls_conn_read 的 0 = **对端关闭**
 *   源码依据：esp_tls_mbedtls.c
 *       if (ret == MBEDTLS_ERR_SSL_PEER_CLOSE_NOTIFY) return 0;
 *   若归成"暂无数据"，断开的连接会被一直等下去 ⇒ **永不重连**（静默失联）。
 *
 * 陷阱 2：WANT_READ / WANT_WRITE 是**负数**，含义却是"稍后再来"。
 * 陷阱 3：ESP_TLS_ERR_SSL_TIMEOUT(-0x6800) 也是负数，含义"窗口内没数据"。
 *   => "<0 就是错误"是错的，会把健康连接反复重连。
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>
#include <stdio.h>

#include "tls_io.h"

static int s_failures = 0;
#define CHECK(cond, ...)                                                     \
    do {                                                                     \
        if (!(cond)) {                                                       \
            printf("FAIL %s:%d  ", __FILE__, __LINE__);                      \
            printf(__VA_ARGS__);                                             \
            printf("\n");                                                    \
            s_failures++;                                                    \
        }                                                                    \
    } while (0)

/* ============ 1. ⭐ 陷阱 1：esp 的 0 是"对端关闭"，不是"暂无数据" ============ */
static void test_zero_means_closed_not_again(void)
{
    size_t n = 999;
    tls_io_read_t r = tls_io_reduce_read(0, &n);
    CHECK(r == TLS_IO_READ_CLOSED,
          "**esp_tls_conn_read 返回 0 是对端关闭**，应归 CLOSED，实际 %s",
          tls_io_read_name(r));
    CHECK(r != TLS_IO_READ_AGAIN,
          "**绝不能归成 AGAIN** —— 那会让断开的连接被当成'还在等数据'，永不重连");
    CHECK(n == 0, "非 DATA 时 n 应为 0，实际 %zu", n);
    CHECK(tls_io_read_ends_connection(r), "CLOSED 必须表示连接不可再用");

    /* PEER_CLOSE_NOTIFY 显式到达（某些路径可能直接透传该负值） */
    CHECK(tls_io_reduce_read(TLS_IO_PEER_CLOSE, &n) == TLS_IO_READ_CLOSED,
          "PEER_CLOSE_NOTIFY 应归 CLOSED（与 0 同义）");
}

/* ============ 2. ⭐ 陷阱 2/3：负值不都是错误 ============ */
static void test_negative_does_not_mean_error(void)
{
    size_t n = 0;

    CHECK(tls_io_reduce_read(TLS_IO_WANT_READ, &n) == TLS_IO_READ_AGAIN,
          "WANT_READ 虽为负，含义是'暂无数据'，应归 AGAIN（不是 ERROR）");
    CHECK(tls_io_reduce_read(TLS_IO_WANT_WRITE, &n) == TLS_IO_READ_AGAIN,
          "WANT_WRITE 应归 AGAIN");
    CHECK(tls_io_reduce_read(TLS_IO_TIMEOUT, &n) == TLS_IO_READ_AGAIN,
          "TIMEOUT(-0x6800) 应归 AGAIN（窗口内没数据，不是故障）");

    /* 反向断言：这些都不能被判成 ERROR，否则健康连接会被反复重连 */
    CHECK(tls_io_reduce_read(TLS_IO_WANT_READ, &n) != TLS_IO_READ_ERROR,
          "**WANT_READ 不是错误**（握手未完成时 mbedTLS 就会这样返回）");
    CHECK(!tls_io_read_ends_connection(TLS_IO_READ_AGAIN),
          "AGAIN 不应结束连接（否则每次暂无数据都重连）");

    /* 真正的错误仍要判成 ERROR */
    CHECK(tls_io_reduce_read(-0x7A00 /* BAD_CERTIFICATE */, &n) == TLS_IO_READ_ERROR,
          "真错误应归 ERROR");
    CHECK(tls_io_reduce_read(-1, &n) == TLS_IO_READ_ERROR, "未知负值应归 ERROR");
    CHECK(tls_io_read_ends_connection(TLS_IO_READ_ERROR), "ERROR 必须表示连接不可再用");
}

/* ============ 3. 读：正数就是字节数 ============ */
static void test_read_positive(void)
{
    size_t n = 0;
    CHECK(tls_io_reduce_read(1, &n) == TLS_IO_READ_DATA && n == 1, "1 字节");
    CHECK(tls_io_reduce_read(16384, &n) == TLS_IO_READ_DATA && n == 16384,
          "16384 字节（一个 TLS 记录满）应正确传出");

    /* 边界：esp 的返回是 ssize_t，"刚好 1" 与 "很大" 都要对 */
    CHECK(tls_io_reduce_read(65535, &n) == TLS_IO_READ_DATA && n == 65535, "大值");
}

/* ============ 4. 写：>=0 都是"写出了 n 字节"（含 0=背压）============ */
static void test_write_reduce(void)
{
    size_t n = 999;

    /* ⭐ 写侧的 0 与读侧的 0 **含义不同**：写 0 = 一个都没写出（背压），
     * 读 0 = 对端关闭。这一条正是"不能共用一张表"的理由。 */
    CHECK(tls_io_reduce_write(0, &n) == TLS_IO_WRITE_WROTE && n == 0,
          "写返回 0 = 写出 0 字节（背压），应归 WROTE 且 n=0");
    CHECK(tls_io_reduce_write(100, &n) == TLS_IO_WRITE_WROTE && n == 100,
          "写返回 100 应传出 100（部分写正常）");

    CHECK(tls_io_reduce_write(TLS_IO_WANT_READ, &n) == TLS_IO_WRITE_AGAIN,
          "写侧 WANT_READ 应归 AGAIN（暂不可写）");
    CHECK(tls_io_reduce_write(TLS_IO_WANT_WRITE, &n) == TLS_IO_WRITE_AGAIN,
          "写侧 WANT_WRITE 应归 AGAIN");
    CHECK(tls_io_reduce_write(TLS_IO_TIMEOUT, &n) == TLS_IO_WRITE_AGAIN,
          "写侧 TIMEOUT 应归 AGAIN");

    CHECK(tls_io_reduce_write(-0x7A00, &n) == TLS_IO_WRITE_ERROR,
          "写侧真错误应归 ERROR");
    CHECK(n == 0, "错误时 n 应为 0，实际 %zu", n);
}

/* ============ 5. 等价类扫全：每个取值都有定义 ============ */
static void test_all_values_defined(void)
{
    long probes[] = { 1, 0, -1, -0x6900, -0x6880, -0x6800, -0x7880, -0x7A00, -0xFFFF };
    for (size_t i = 0; i < sizeof(probes) / sizeof(probes[0]); i++) {
        size_t n = 0;
        tls_io_read_t r = tls_io_reduce_read(probes[i], &n);
        CHECK(r >= 0 && r <= TLS_IO_READ_ERROR,
              "probe %ld 应给出有效读结果，实际 %d", probes[i], (int)r);
        tls_io_write_t w = tls_io_reduce_write(probes[i], &n);
        CHECK(w >= 0 && w <= TLS_IO_WRITE_ERROR,
              "probe %ld 应给出有效写结果，实际 %d", probes[i], (int)w);
        /* 归约结果必须能被上层直接用（名字非空，便于日志） */
        CHECK(tls_io_read_name(r)[0] != 0, "读结果名不应为空");
        CHECK(tls_io_write_name(w)[0] != 0, "写结果名不应为空");
    }
    /* 越界名要兜底 */
    CHECK(tls_io_read_name((tls_io_read_t)99)[0] == 'U', "越界读结果应兜底为 UNKNOWN");
    CHECK(tls_io_write_name((tls_io_write_t)99)[0] == 'U', "越界写结果应兜底为 UNKNOWN");
}

/* ============ 6. 计数：每一档都可见（P3）============ */
static void test_stats_cover_every_branch(void)
{
    tls_io_reset_stats();
    size_t n = 0;
    (void)tls_io_note_read(10, &n);              /* data */
    (void)tls_io_note_read(TLS_IO_WANT_READ, &n);/* again */
    (void)tls_io_note_read(0, &n);               /* closed */
    (void)tls_io_note_read(-1, &n);              /* error */
    (void)tls_io_note_write(5, &n);              /* wrote */
    (void)tls_io_note_write(TLS_IO_WANT_WRITE, &n); /* again */
    (void)tls_io_note_write(-1, &n);             /* error */

    tls_io_stats_t st;
    tls_io_get_stats(&st);
    CHECK(st.read_data == 1 && st.read_again == 1 && st.read_closed == 1 &&
          st.read_error == 1, "读计数应各 1（data=%u again=%u closed=%u error=%u）",
          st.read_data, st.read_again, st.read_closed, st.read_error);
    CHECK(st.write_wrote == 1 && st.write_again == 1 && st.write_error == 1,
          "写计数应各 1（wrote=%u again=%u error=%u）",
          st.write_wrote, st.write_again, st.write_error);

    uint32_t sum = st.read_data + st.read_again + st.read_closed + st.read_error +
                   st.write_wrote + st.write_again + st.write_error;
    CHECK(sum == 7, "七项之和应等于 7（没有'其它'桶），实际 %u", sum);

    tls_io_reset_stats();
    tls_io_get_stats(&st);
    CHECK(st.read_data == 0 && st.read_error == 0, "复位后应全 0");
}

/* ============ 7. ⭐ 场景回归：对端正常关闭必须导致重连 ============ */
static void test_peer_close_leads_to_reconnect(void)
{
    tls_io_reset_stats();

    size_t n = 0;
    /* 正常收数据 */
    CHECK(tls_io_note_read(512, &n) == TLS_IO_READ_DATA, "先收到数据");
    /* 服务端 r20 滚动更新，发 close_notify ⇒ esp 返回 0 */
    tls_io_read_t r = tls_io_note_read(0, &n);
    CHECK(r == TLS_IO_READ_CLOSED, "对端关闭应被识别");
    CHECK(tls_io_read_ends_connection(r),
          "**必须结束连接** —— 否则调用方会一直读一个已关的连接（静默失联）");

    tls_io_stats_t st;
    tls_io_get_stats(&st);
    CHECK(st.read_closed == 1, "closed 应计 1");
    CHECK(st.read_again == 0, "**关闭不得被计入 again**（这两个处置完全不同）");
    CHECK(st.read_error == 0, "**正常关闭不是错误**（不该告警）");
}

int main(void)
{
    test_zero_means_closed_not_again();
    test_negative_does_not_mean_error();
    test_read_positive();
    test_write_reduce();
    test_all_values_defined();
    test_stats_cover_every_branch();
    test_peer_close_leads_to_reconnect();

    if (s_failures) { printf("tls_io_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("tls_io_tests: all checks passed\n");
    return 0;
}
