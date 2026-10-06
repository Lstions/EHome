/* link_rx_adapt_tests.c —— link_tcp_read -> rx_read_fn_t 的翻译
 *
 * ## 本用例存在的全部理由：**枚举值撞车**
 *   LINK_READ_DATA = 0    （读到了数据）
 *   RX_IO_AGAIN    = 0    （暂无数据）
 *
 * 两者都是 0，含义**相反**。若有人图省事 "return (int)link_tcp_read(...)"，
 * 读到的数据会被当成"暂无数据"丢掉 —— 而且计数器只涨 again，
 * **看不出任何异常**（连接看着正常，就是一直没有消息）。
 * 编译器不会警告（两边都是 int）。故必须逐值锁死。
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "link_rx_adapt.h"
#include "link_tcp.h"
#include "rx_pump.h"

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

/* ============ 1. ⭐ 撞车：DATA 必须返回字节数，不能是 0 ============ */
static void test_data_returns_length_not_zero(void)
{
    int v = link_rx_adapt_result(LINK_READ_DATA, 512);
    CHECK(v == 512, "DATA 应返回字节数 512，实际 %d", v);
    CHECK(v != RX_IO_AGAIN,
          "**绝不能返回 0** —— rx_pump 的 0 是'暂无数据'，数据会被静默丢掉");

    /* 最危险的写法：把枚举值当结果直接返回 */
    int cast = (int)LINK_READ_DATA;   /* == 0 */
    CHECK(cast == RX_IO_AGAIN,
          "（记录撞车事实）LINK_READ_DATA 的裸值确实等于 RX_IO_AGAIN = %d", cast);
    CHECK(link_rx_adapt_result(LINK_READ_DATA, 1) != cast,
          "所以翻译**不能**是强制转换 —— 必须用 n");

    CHECK(link_rx_adapt_result(LINK_READ_DATA, 1) == 1, "1 字节");
    CHECK(link_rx_adapt_result(LINK_READ_DATA, 16368) == 16368, "上界尺寸");
}

/* ============ 2. 其余四档的翻译 ============ */
static void test_other_results(void)
{
    CHECK(link_rx_adapt_result(LINK_READ_AGAIN, 0) == RX_IO_AGAIN,
          "AGAIN 应翻成 0");
    CHECK(link_rx_adapt_result(LINK_READ_CLOSED, 0) == RX_IO_CLOSED,
          "CLOSED 应翻成 -1");
    CHECK(link_rx_adapt_result(LINK_READ_FATAL, 0) == RX_IO_ERROR,
          "FATAL 应翻成 -2");

    /* NOT_READY 与 CLOSED 对调用方动作相同（重建、不算故障）*/
    CHECK(link_rx_adapt_result(LINK_READ_NOT_READY, 0) == RX_IO_CLOSED,
          "NOT_READY 应翻成 -1（动作与 CLOSED 相同）");
    CHECK(link_rx_adapt_result(LINK_READ_NOT_READY, 0) != RX_IO_AGAIN,
          "**NOT_READY 绝不能翻成 0** —— 那会让 rx_pump 对着一条未建立的链路空转");
}

/* ============ 3. 上游违约与溢出 ============ */
static void test_guards(void)
{
    CHECK(link_rx_adapt_result(LINK_READ_DATA, 0) == RX_IO_ERROR,
          "DATA 配 0 字节是上游违约，应报错而不是伪装成暂无数据");
    CHECK(link_rx_adapt_result(LINK_READ_DATA, (size_t)0x80000000ULL) == RX_IO_ERROR,
          "超 int 范围应报错，不静默截断");
    CHECK(link_rx_adapt_result((link_read_result_t)99, 0) == RX_IO_ERROR,
          "越界结果应兜底为错误");
}

/* ============ 4. 全映射穷举：每个取值都落进 rx_pump 的四个槽 ============ */
static void test_full_mapping(void)
{
    for (int r = 0; r <= (int)LINK_READ_FATAL; r++) {
        int v = link_rx_adapt_result((link_read_result_t)r, 64);
        bool known = (v > 0) || v == RX_IO_AGAIN || v == RX_IO_CLOSED || v == RX_IO_ERROR;
        CHECK(known, "读结果 %d 翻出的 %d 不是 rx_pump 已知取值", r, v);
    }
}

/* ============ 5. ⭐ 端到端：真数据经 link_tcp -> 适配层 -> rx_pump 回调 ============
 * 这一段走**真实链路**：link_tcp + 假 io 驱动 + 适配层 + rx_pump，
 * 证明"读到的字节"确实能变成"定界出来的消息"，而不是被中间某层吞掉。 */
static uint8_t s_feed[256];
static size_t  s_feed_len, s_feed_off;

static int fake_read(void *handle, uint8_t *buf, size_t cap)
{
    (void)handle;
    size_t left = s_feed_len - s_feed_off;
    if (left == 0) return 0;                       /* 0 = 暂无数据（link_tcp_io_t 约定）*/
    size_t k = left < cap ? left : cap;
    memcpy(buf, s_feed + s_feed_off, k);
    s_feed_off += k;
    return (int)k;
}
static void *fake_connect(void *io_ctx, bool *hard_fatal)
{
    (void)io_ctx;
    if (hard_fatal != NULL) *hard_fatal = false;
    return (void *)1;                              /* 非 NULL = 已连接 */
}
static void fake_close(void *h) { (void)h; }
static int  fake_write(void *h, const uint8_t *d, size_t n) { (void)h; (void)d; return (int)n; }

static const link_tcp_io_t FAKE_IO = {
    .connect = fake_connect,
    .write   = fake_write,
    .read    = fake_read,
    .close   = fake_close,
};

typedef struct { int callbacks; size_t last_len; uint8_t last[8]; } sink_t;
static sink_t s_sink;

static bool sink_cb(const rx_msg_t *m, void *ctx)
{
    (void)ctx;
    s_sink.callbacks++;
    s_sink.last_len = m->payload_len;
    if (m->payload != NULL && m->payload_len <= sizeof(s_sink.last)) {
        memcpy(s_sink.last, m->payload, m->payload_len);
    }
    return true;
}

static size_t mkmsg(uint8_t *out, size_t cap, uint8_t type, uint32_t seq,
                    const uint8_t *pl, uint16_t plen)
{
    wire_header_t h = { .ver = 0x30, .type = type, .flags = 0,
                        .seq = seq, .payload_len = plen };
    if (wire_encode_header(out, cap, &h) != WIRE_OK) return 0;
    memcpy(out + WIRE_HEADER_BYTES, pl, plen);
    return (size_t)WIRE_HEADER_BYTES + plen;
}

static void test_end_to_end_data_reaches_callback(void)
{
    memset(&s_sink, 0, sizeof(s_sink));
    link_rx_adapt_reset_stats();

    const uint8_t pl[5] = {0xAA, 0xBB, 0xCC, 0xDD, 0xEE};
    s_feed_len = mkmsg(s_feed, sizeof(s_feed), 0x03, 7, pl, sizeof(pl));
    s_feed_off = 0;

    link_tcp_config_t cfg = { .io = &FAKE_IO, .io_ctx = NULL };
    link_tcp_ctx_t *tcp = link_tcp_new(&cfg);
    CHECK(tcp != NULL, "link_tcp_new 失败");

    link_t *l = link_create(link_tcp_driver(), tcp);
    CHECK(l != NULL, "link_create 失败");
    CHECK(link_open(l) == LINK_SENT_FULL, "link_open 应成功（假 io 返回非 NULL）");

    link_rx_binding_t *b = link_rx_binding_new(tcp);
    CHECK(b != NULL, "binding 创建失败");

    uint8_t rbuf[128];
    rx_pump_t *p = rx_pump_create(1024, link_rx_adapt_read, b, sink_cb, NULL,
                                  rbuf, sizeof(rbuf));
    CHECK(p != NULL, "rx_pump 创建失败");

    uint32_t delivered = 0;
    for (int guard = 0; guard < 8 && s_sink.callbacks == 0; guard++) {
        rx_pump_result_t rr = rx_pump_step(p, &delivered);
        if (rr == RX_PUMP_CLOSED || rr == RX_PUMP_ERROR) break;
    }

    /* ⭐ 核心断言：数据穿过了 4 层（假 io -> link_tcp -> 适配层 -> rx_pump），
     * 没有被任何一层的"0 = 暂无数据"吞掉。 */
    CHECK(s_sink.callbacks == 1,
          "应收到 1 条消息，实际 %d（数据在中途被吞了？）", s_sink.callbacks);
    CHECK(s_sink.last_len == sizeof(pl),
          "载荷长度应为 %zu，实际 %zu", sizeof(pl), s_sink.last_len);
    CHECK(memcmp(s_sink.last, pl, sizeof(pl)) == 0,
          "载荷内容必须与原始一致");

    link_rx_adapt_stats_t st;
    link_rx_adapt_get_stats(&st);
    CHECK(st.data >= 1, "适配层应记到至少一次 DATA，实际 %u", st.data);


    rx_pump_destroy(p);
    link_rx_binding_free(b);
    link_destroy(l);
    link_tcp_free(tcp);
}

int main(void)
{
    test_data_returns_length_not_zero();
    test_other_results();
    test_guards();
    test_full_mapping();
    test_end_to_end_data_reaches_callback();

    if (s_failures) { printf("link_rx_adapt_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("link_rx_adapt_tests: all checks passed\n");
    return 0;
}
