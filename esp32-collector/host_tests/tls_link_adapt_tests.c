/* tls_link_adapt_tests.c —— 跨契约翻译（tls_io -> link_tcp_io_t）
 *
 * ## 本用例存在的全部理由
 * 本仓同时存在**三套**读返回值约定，对 0 的含义各不相同：
 *
 *   世界                     暂无数据   对端关闭   错误
 *   esp_tls 原始              (负值)     0         其余负值
 *   tls_io 归约              AGAIN      CLOSED     ERROR
 *   link_tcp_io_t / rx_read  0          -1         -2
 *
 * 把 esp 的值**直接透传**给 link（那里 0=暂无数据）就等于把上一个陷阱
 * 换个位置重演：**对端关闭被当成没数据 ⇒ 永不重连**。
 * 所以这一层必须显式翻译，并且翻译表必须被穷举锁死。
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>

#include "link_tcp.h"
#include "tls_io.h"
#include "tls_link_adapt.h"

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

/* ============ 1. ⭐ CLOSED 必须翻成 -1，不能翻成 0 ============ */
static void test_closed_maps_to_minus_one_not_zero(void)
{
    int v = tls_link_adapt_read_result(TLS_IO_READ_CLOSED, 0);
    CHECK(v == LINK_TCP_IO_CLOSED,
          "对端关闭应翻成 LINK_TCP_IO_CLOSED(-1)，实际 %d", v);
    CHECK(v != LINK_TCP_IO_AGAIN,
          "**绝不能翻成 0** —— link 层把 0 当'暂无数据'，会让断开的连接永不重连");
    CHECK(tls_link_read_ends_connection(v), "关闭必须表示连接不可再用");

    /* 反向确认 AGAIN 才是 0 —— 两个世界的"0"含义确实不同 */
    CHECK(tls_link_adapt_read_result(TLS_IO_READ_AGAIN, 0) == LINK_TCP_IO_AGAIN,
          "AGAIN 应翻成 0");
    CHECK(tls_link_adapt_read_result(TLS_IO_READ_AGAIN, 0) != LINK_TCP_IO_CLOSED,
          "AGAIN 不是关闭");
}

/* ============ 2. 其余三档的翻译 ============ */
static void test_other_read_results(void)
{
    CHECK(tls_link_adapt_read_result(TLS_IO_READ_DATA, 512) == 512,
          "DATA 应原样传出字节数");
    CHECK(tls_link_adapt_read_result(TLS_IO_READ_DATA, 1) == 1, "1 字节");
    CHECK(tls_link_adapt_read_result(TLS_IO_READ_ERROR, 0) == LINK_TCP_IO_ERROR,
          "ERROR 应翻成 -2");
    CHECK(tls_link_read_ends_connection(LINK_TCP_IO_ERROR), "ERROR 表示连接不可再用");
    CHECK(!tls_link_read_ends_connection(LINK_TCP_IO_AGAIN),
          "AGAIN 不应结束连接");
}

/* ============ 3. 上游违约：DATA 但 0 字节 ============ */
static void test_data_with_zero_is_error_not_again(void)
{
    /* 归约层保证 DATA 时 n>0。若违反，**不得**伪装成"暂无数据"
     * —— 那正好制造出本层要防的那种混淆。 */
    int v = tls_link_adapt_read_result(TLS_IO_READ_DATA, 0);
    CHECK(v == LINK_TCP_IO_ERROR,
          "DATA 配 0 字节属上游违约，应报 ERROR，实际 %d", v);
    CHECK(v != LINK_TCP_IO_AGAIN, "违约不得伪装成暂无数据");
}

/* ============ 4. 写侧：背压必须是 0，不能是负值 ============ */
static void test_write_backpressure_is_zero(void)
{
    /* 写出 0 字节是**合法**的（背压）。若返回负值，link 层会当成链路故障
     * 而触发无谓重建。 */
    CHECK(tls_link_adapt_write_result(TLS_IO_WRITE_WROTE, 0) == 0,
          "写出 0 字节应返回 0（背压，可重试），不能是负值");
    CHECK(tls_link_adapt_write_result(TLS_IO_WRITE_WROTE, 100) == 100,
          "写出的字节数应原样传出");
    CHECK(tls_link_adapt_write_result(TLS_IO_WRITE_AGAIN, 0) == 0,
          "软等待应返回 0（背压）");
    CHECK(tls_link_adapt_write_result(TLS_IO_WRITE_ERROR, 0) < 0,
          "写硬错误应返回负值");
}

/* ============ 5. 溢出保护：size_t 超过 int 范围不静默截断 ============ */
static void test_overflow_guard(void)
{
    /* 读的返回值是 int，字节数是 size_t。若超范围而静默截断，
     * 调用方会以为读到了错误的字节数 ⇒ 流错位。这里必须报错。 */
    int big = tls_link_adapt_read_result(TLS_IO_READ_DATA, (size_t)0x80000000ULL);
    CHECK(big == LINK_TCP_IO_ERROR, "超过 int 范围应报 ERROR，实际 %d", big);
    int bigw = tls_link_adapt_write_result(TLS_IO_WRITE_WROTE, (size_t)0x80000000ULL);
    CHECK(bigw < 0, "写侧超范围应报错，实际 %d", bigw);
}

/* ============ 6. ⭐ 全映射穷举：不留未定义组合 ============ */
static void test_full_read_mapping_table(void)
{
    /* 每个 tls_io_read_t 都必须翻成 link 侧**恰好一个**已知值 */
    for (int r = 0; r <= (int)TLS_IO_READ_ERROR; r++) {
        int v = tls_link_adapt_read_result((tls_io_read_t)r, 64);
        bool known = (v > 0) || v == LINK_TCP_IO_AGAIN ||
                     v == LINK_TCP_IO_CLOSED || v == LINK_TCP_IO_ERROR;
        CHECK(known, "读结果 %d 翻出的 %d 不是 link 侧已知取值", r, v);
    }
    for (int w = 0; w <= (int)TLS_IO_WRITE_ERROR; w++) {
        int v = tls_link_adapt_write_result((tls_io_write_t)w, 64);
        bool known = (v >= 0) || v == -2;
        CHECK(known, "写结果 %d 翻出的 %d 不是合法写返回", w, v);
    }
    /* 越界的归约值（理论上到不了）必须兜底成错误，而不是崩或返回 0 */
    CHECK(tls_link_adapt_read_result((tls_io_read_t)99, 0) == LINK_TCP_IO_ERROR,
          "越界读结果应兜底为 ERROR");
    CHECK(tls_link_adapt_write_result((tls_io_write_t)99, 0) == -2,
          "越界写结果应兜底为错误");
}

/* ============ 7. ⭐ 端到端：esp 的 0 一路翻到 link 侧仍是"关闭" ============ */
static void test_end_to_end_zero_stays_closed(void)
{
    /* 模拟真实链路：esp_tls_conn_read 返回 0（对端发 close_notify）*/
    long esp_raw = 0;
    size_t n = 0;
    tls_io_read_t r = tls_io_reduce_read(esp_raw, &n);
    CHECK(r == TLS_IO_READ_CLOSED, "第一跳：应归 CLOSED");

    int link_ret = tls_link_adapt_read_result(r, n);
    CHECK(link_ret == LINK_TCP_IO_CLOSED,
          "第二跳：应翻成 -1，实际 %d", link_ret);
    CHECK(tls_link_read_ends_connection(link_ret),
          "**端到端必须让调用方知道要重连**（这正是两跳都不能出错的原因）");

    /* 同样的链路，走"暂无数据" */
    esp_raw = TLS_IO_WANT_READ;
    r = tls_io_reduce_read(esp_raw, &n);
    link_ret = tls_link_adapt_read_result(r, n);
    CHECK(link_ret == LINK_TCP_IO_AGAIN, "软等待端到端应翻成 0");
    CHECK(!tls_link_read_ends_connection(link_ret), "软等待不结束连接");
}

int main(void)
{
    test_closed_maps_to_minus_one_not_zero();
    test_other_read_results();
    test_data_with_zero_is_error_not_again();
    test_write_backpressure_is_zero();
    test_overflow_guard();
    test_full_read_mapping_table();
    test_end_to_end_zero_stays_closed();

    if (s_failures) { printf("tls_link_adapt_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("tls_link_adapt_tests: all checks passed\n");
    return 0;
}
