/* rx_pump_tests.c —— 字节流 -> 消息（定界）的接收路径
 *
 * 这是 D-09 的回归用例。旧实现（ehome_tcp.c:477）：
 *
 *     ssize_t received = recv(socket, recv_buf, 2048, 0);
 *     transport->msg_cb(recv_buf, received, ...);   // 一次 recv == 一条消息
 *
 * 本用例把三种"一次 recv 不是一条消息"的情形全部锁住：
 *   1. 半条消息到达   -> **不得交付**（旧实现会把半条当一条交上去）；
 *   2. 一次到达三条   -> **交付三条**（旧实现只交第一条，其余被丢）；
 *   3. 一条分多次到达 -> **只交付一条**（拼接正确）。
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>

#include "rx_pump.h"
#include "wire.h"

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

/* ================= 假 io：按脚本喂字节 ================= */
enum { MAX_STEPS = 512 };
typedef struct {
    /* 每一"次 read"返回哪些字节 */
    struct { const uint8_t *data; size_t len; int rc; } step[MAX_STEPS];
    int      n_steps;
    int      idx;
    int      read_calls;
} fake_io_t;

static fake_io_t s_io;

static void io_reset(void) { memset(&s_io, 0, sizeof(s_io)); }

static void io_push(const uint8_t *data, size_t len)
{
    if (s_io.n_steps >= MAX_STEPS) return;
    s_io.step[s_io.n_steps].data = data;
    s_io.step[s_io.n_steps].len = len;
    s_io.step[s_io.n_steps].rc = (int)len;
    s_io.n_steps++;
}

static void io_push_rc(int rc)
{
    if (s_io.n_steps >= MAX_STEPS) return;
    s_io.step[s_io.n_steps].data = NULL;
    s_io.step[s_io.n_steps].len = 0;
    s_io.step[s_io.n_steps].rc = rc;
    s_io.n_steps++;
}

static int fake_read(void *ctx, uint8_t *buf, size_t cap)
{
    (void)ctx;
    s_io.read_calls++;
    if (s_io.idx >= s_io.n_steps) return RX_IO_AGAIN;   /* 脚本用尽：视为暂无数据 */
    int i = s_io.idx++;
    if (s_io.step[i].rc <= 0) return s_io.step[i].rc;
    size_t n = s_io.step[i].len;
    if (n > cap) n = cap;
    memcpy(buf, s_io.step[i].data, n);
    return (int)n;
}

/* ================= 收集交付的消息 ================= */
typedef struct {
    int      count;
    uint8_t  type[16];
    uint32_t seq[16];
    uint16_t plen[16];
    uint8_t  payload[16][64];
    size_t   copied[16];        /* 实际拷进 payload 的字节数（<=64） */
    int      stop_after;     /* >0: 交付到第 N 条后回调返回 false */
} sink_t;

static sink_t s_sink;

static void sink_reset(void) { memset(&s_sink, 0, sizeof(s_sink)); s_sink.stop_after = 0; }

static bool sink_cb(const rx_msg_t *m, void *ctx)
{
    (void)ctx;
    int i = s_sink.count;
    if (i < 16) {
        s_sink.type[i] = m->type;
        s_sink.seq[i] = m->seq;
        s_sink.plen[i] = m->payload_len;
        size_t n = m->payload_len < sizeof(s_sink.payload[0]) ? m->payload_len
                                                             : sizeof(s_sink.payload[0]);
        s_sink.copied[i] = n;
        if (m->payload != NULL) memcpy(s_sink.payload[i], m->payload, n);
    }
    s_sink.count++;
    if (s_sink.stop_after > 0 && s_sink.count >= s_sink.stop_after) return false;
    return true;
}

/* 构造一条完整消息 */
static size_t mkmsg(uint8_t *out, size_t cap, uint8_t type, uint32_t seq,
                    const uint8_t *pl, uint16_t plen)
{
    wire_header_t h = { .ver = WIRE_VER, .type = type, .flags = 0,
                        .seq = seq, .payload_len = plen };
    if (wire_encode_header(out, cap, &h) != WIRE_OK) return 0;
    if ((size_t)WIRE_HEADER_BYTES + plen > cap) return 0;
    if (plen) memcpy(out + WIRE_HEADER_BYTES, pl, plen);
    return (size_t)WIRE_HEADER_BYTES + plen;
}

static uint8_t s_rbuf[4096];
static rx_pump_t *make_pump(uint32_t max_payload)
{
    return rx_pump_create(max_payload, fake_read, NULL, sink_cb, NULL,
                          s_rbuf, sizeof(s_rbuf));
}

/* ============ 1. ⭐ 半条消息不得交付（D-09 核心）============ */
static void test_partial_message_is_not_delivered(void)
{
    io_reset(); sink_reset();
    uint8_t pl[100];
    for (int i = 0; i < 100; i++) pl[i] = (uint8_t)i;
    uint8_t msg[256];
    size_t n = mkmsg(msg, sizeof(msg), 0x03, 1, pl, sizeof(pl));
    CHECK(n == 112, "消息应 12+100=112 B，实际 %zu", n);

    /* 先喂前 50 字节：**不足一条** */
    io_push(msg, 50);

    rx_pump_t *p = make_pump(1024);
    CHECK(p != NULL, "创建失败");

    uint32_t d = 0;
    CHECK(rx_pump_step(p, &d) == RX_PUMP_IDLE, "半条消息应 IDLE（不交付）");
    CHECK(d == 0, "半条消息不得交付，实际交付 %u 条", d);
    CHECK(s_sink.count == 0, "回调不应被调用，实际 %d 次", s_sink.count);

    /* 再喂剩下的 62 字节：这才构成完整一条。
     * 注意 io_push 是**队列**语义：RX_IO_AGAIN 这一档必须放在所有数据之后
     * （它代表"脚本用尽后的兜底"），否则会插在数据前面把后续数据挡住。 */
    io_push(msg + 50, n - 50);
    CHECK(rx_pump_step(p, &d) == RX_PUMP_DELIVERED, "补齐后应交付");
    CHECK(d == 1, "应交付 1 条，实际 %u", d);
    CHECK(s_sink.count == 1, "回调应 1 次，实际 %d", s_sink.count);
    CHECK(s_sink.plen[0] == 100, "载荷长度应 100，实际 %u", s_sink.plen[0]);
    /* 只比较 sink 真正拷贝到的字节数（sink 容量 64，不能拿 100 去 memcmp ——
     * 那样会读到未初始化区，是**测试自己的错**，与实现无关）。 */
    CHECK(s_sink.copied[0] == 64, "sink 应拷到容量上限 64，实际 %zu", s_sink.copied[0]);
    CHECK(memcmp(s_sink.payload[0], pl, s_sink.copied[0]) == 0,
          "拼接后的载荷前 %zu 字节必须与原始一致", s_sink.copied[0]);

    rx_pump_destroy(p);
}

/* ============ 2. ⭐ 一次读入含三条 -> 交付三条 ============ */
static void test_three_messages_in_one_read(void)
{
    io_reset(); sink_reset();
    uint8_t a[3] = {1,2,3}, b[5] = {4,5,6,7,8}, c[2] = {9,10};
    uint8_t buf[256];
    size_t na = mkmsg(buf, sizeof(buf), 0x03, 11, a, sizeof(a));
    size_t nb = mkmsg(buf + na, sizeof(buf) - na, 0x20, 22, b, sizeof(b));
    size_t nc = mkmsg(buf + na + nb, sizeof(buf) - na - nb, 0x0E, 33, c, sizeof(c));
    size_t total = na + nb + nc;

    io_push(buf, total);            /* 一次 read 给出三条 */
    io_push_rc(RX_IO_AGAIN);

    rx_pump_t *p = make_pump(1024);
    uint32_t d = 0;
    CHECK(rx_pump_step(p, &d) == RX_PUMP_DELIVERED, "应交付");
    CHECK(d == 3, "一次读入含 3 条应交付 3 条，实际 %u", d);
    CHECK(s_sink.count == 3, "回调应 3 次，实际 %d", s_sink.count);
    if (s_sink.count == 3) {
        CHECK(s_sink.type[0] == 0x03 && s_sink.seq[0] == 11, "第 1 条应是 0x03/11");
        CHECK(s_sink.type[1] == 0x20 && s_sink.seq[1] == 22, "第 2 条应是 0x20/22");
        CHECK(s_sink.type[2] == 0x0E && s_sink.seq[2] == 33, "第 3 条应是 0x0E/33");
        CHECK(s_sink.plen[1] == 5 && memcmp(s_sink.payload[1], b, 5) == 0,
              "第 2 条载荷应为 b");
    }
    rx_pump_destroy(p);
}

/* ============ 3. 字节级切分：任意切法都得到同一条 ============ */
static void test_arbitrary_byte_splits(void)
{
    uint8_t pl[40];
    for (int i = 0; i < 40; i++) pl[i] = (uint8_t)(i * 3 + 1);
    uint8_t msg[128];
    size_t n = mkmsg(msg, sizeof(msg), 0x03, 7, pl, sizeof(pl));

    for (size_t chunk = 1; chunk <= n; chunk++) {
        io_reset(); sink_reset();
        for (size_t i = 0; i < n; i += chunk) {
            size_t c = (n - i < chunk) ? (n - i) : chunk;
            io_push(msg + i, c);
        }
        io_push_rc(RX_IO_AGAIN);

        rx_pump_t *p = make_pump(1024);
        uint32_t d = 0;
        /* 反复泵送：IDLE（数据不足）**不是终点** —— 脚本里还有后续字节。
         * 只有 CLOSED/ERROR 或"交付已完成"才停。
         * （这里曾写错成"非 DELIVERED 就 break"，把 chunk 较大的情形全判失败。） */
        for (int guard = 0; guard < 256; guard++) {
            rx_pump_result_t r = rx_pump_step(p, &d);
            if (r == RX_PUMP_CLOSED || r == RX_PUMP_ERROR || r == RX_PUMP_FATAL) break;
            if (s_sink.count >= 1) break;
        }
        CHECK(s_sink.count == 1, "chunk=%zu 应交付恰好 1 条，实际 %d", chunk, s_sink.count);
        if (s_sink.count == 1) {
            CHECK(s_sink.plen[0] == 40, "chunk=%zu 载荷长应为 40", chunk);
            CHECK(memcmp(s_sink.payload[0], pl, 40) == 0, "chunk=%zu 载荷内容应一致", chunk);
        }
        rx_pump_destroy(p);
    }
}

/* ============ 4. 超时不算错误；EOF 与硬错误要区分 ============ */
static void test_again_vs_closed_vs_error(void)
{
    io_reset(); sink_reset();
    io_push_rc(RX_IO_AGAIN);
    rx_pump_t *p = make_pump(1024);
    uint32_t d = 0;

    CHECK(rx_pump_step(p, &d) == RX_PUMP_IDLE, "暂无数据应 IDLE");
    rx_pump_stats_t st;
    rx_pump_get_stats(p, &st);
    CHECK(st.again == 1, "again 应计 1");
    CHECK(st.io_errors == 0, "超时**不得**计入错误（旧代码在这里分不清）");
    CHECK(st.closed == 0, "超时不是关闭");

    /* EOF：对端正常关闭 */
    io_reset(); sink_reset();
    io_push_rc(RX_IO_CLOSED);
    CHECK(rx_pump_step(p, &d) == RX_PUMP_CLOSED, "EOF 应 CLOSED");
    rx_pump_get_stats(p, &st);
    CHECK(st.closed == 1, "closed 应计 1");
    CHECK(st.io_errors == 0, "**正常关闭不是硬错误**（处置不同：一个重建、一个告警）");

    /* 硬错误 */
    io_reset(); sink_reset();
    io_push_rc(RX_IO_ERROR);
    CHECK(rx_pump_step(p, &d) == RX_PUMP_ERROR, "硬错误应 ERROR");
    rx_pump_get_stats(p, &st);
    CHECK(st.io_errors == 1, "io_errors 应计 1");

    rx_pump_destroy(p);
}

/* ============ 5. 回调请求停止：剩余消息不丢 ============ */
static void test_callback_may_stop(void)
{
    io_reset(); sink_reset();
    uint8_t a[2] = {1,2}, b[2] = {3,4}, c[2] = {5,6};
    uint8_t buf[128];
    size_t na = mkmsg(buf, sizeof(buf), 0x03, 1, a, 2);
    size_t nb = mkmsg(buf + na, sizeof(buf) - na, 0x03, 2, b, 2);
    size_t nc = mkmsg(buf + na + nb, sizeof(buf) - na - nb, 0x03, 3, c, 2);
    io_push(buf, na + nb + nc);
    io_push_rc(RX_IO_AGAIN);

    s_sink.stop_after = 1;      /* 交完第 1 条就要求停 */
    rx_pump_t *p = make_pump(1024);
    uint32_t d = 0;
    CHECK(rx_pump_step(p, &d) == RX_PUMP_DELIVERED, "应交付");
    CHECK(d == 1, "停在第 1 条，本轮应交付 1 条，实际 %u", d);
    CHECK(s_sink.count == 1, "回调应只 1 次，实际 %d", s_sink.count);

    /* 关键：剩下的两条**没有被丢弃** —— 下次 step 继续取出 */
    s_sink.stop_after = 0;
    for (int guard = 0; guard < 16 && s_sink.count < 3; guard++) {
        rx_pump_result_t r = rx_pump_step(p, &d);
        if (r == RX_PUMP_CLOSED || r == RX_PUMP_ERROR) break;
    }
    CHECK(s_sink.count == 3, "恢复后应把剩余 2 条也交付（共 3），实际 %d", s_sink.count);
    rx_pump_destroy(p);
}

/* ============ 6. 坏头：计数 + 报错，且能重新同步 ============ */
static void test_bad_header_counted_and_resync(void)
{
    io_reset(); sink_reset();
    uint8_t junk[64];
    memset(junk, 0, sizeof(junk));       /* magic = 0000，非法 */
    io_push(junk, sizeof(junk));

    rx_pump_t *p = make_pump(1024);
    uint32_t d = 0;
    CHECK(rx_pump_step(p, &d) == RX_PUMP_ERROR, "坏头应 ERROR（不静默跳过）");
    rx_pump_stats_t st;
    rx_pump_get_stats(p, &st);
    CHECK(st.malformed == 1, "malformed 应计 1，实际 %u", st.malformed);

    /* 重新同步：之后一条合法消息必须能正常交付 */
    io_reset(); sink_reset();
    uint8_t pl[4] = {7,7,7,7};
    uint8_t good[64];
    size_t n = mkmsg(good, sizeof(good), 0x03, 9, pl, 4);
    io_push(good, n);
    io_push_rc(RX_IO_AGAIN);
    CHECK(rx_pump_step(p, &d) == RX_PUMP_DELIVERED, "出错后应能恢复");
    CHECK(s_sink.count == 1 && s_sink.seq[0] == 9, "恢复后消息应正确");
    rx_pump_destroy(p);
}

/* ============ 7. CRC 不符：计数 + 报错 ============ */
static void test_crc_error_counted(void)
{
    io_reset(); sink_reset();
    uint8_t pl[6] = {1,2,3,4,5,6};
    uint8_t buf[64];
    wire_header_t h = { .ver = WIRE_VER, .type = 0x20, .flags = WIRE_FLAG_CRC32C,
                        .seq = 1, .payload_len = 6 };
    wire_encode_header(buf, sizeof(buf), &h);
    memcpy(buf + WIRE_HEADER_BYTES, pl, 6);
    uint32_t c = wire_crc32c(pl, 6);
    size_t n = WIRE_HEADER_BYTES + 6;
    buf[n+0] = (uint8_t)(c >> 24); buf[n+1] = (uint8_t)(c >> 16);
    buf[n+2] = (uint8_t)(c >> 8);  buf[n+3] = (uint8_t)c;
    n += 4;
    buf[n-1] ^= 0xFF;             /* 篡改 CRC */

    io_push(buf, n);
    rx_pump_t *p = make_pump(1024);
    uint32_t d = 0;
    CHECK(rx_pump_step(p, &d) == RX_PUMP_ERROR, "CRC 错应 ERROR");
    rx_pump_stats_t st;
    rx_pump_get_stats(p, &st);
    CHECK(st.crc_errors == 1, "crc_errors 应计 1，实际 %u", st.crc_errors);
    CHECK(s_sink.count == 0, "CRC 错的帧**不得交付**");
    rx_pump_destroy(p);
}

/* ============ 8. 声明超上界：不缓冲 + 计数 ============ */
static void test_too_large_not_buffered(void)
{
    io_reset(); sink_reset();
    uint8_t hdr[WIRE_HEADER_BYTES];
    wire_header_t h = { .ver = WIRE_VER, .type = 1, .flags = 0, .seq = 1, .payload_len = 900 };
    wire_encode_header(hdr, sizeof(hdr), &h);

    io_push(hdr, sizeof(hdr));
    rx_pump_t *p = make_pump(256);      /* 上界 256 < 900 */
    uint32_t d = 0;
    CHECK(rx_pump_step(p, &d) == RX_PUMP_ERROR, "超上界应 ERROR");
    rx_pump_stats_t st;
    rx_pump_get_stats(p, &st);
    CHECK(st.too_large == 1, "too_large 应计 1，实际 %u", st.too_large);
    rx_pump_destroy(p);
}

/* ============ 9. 参数校验 ============ */
static void test_create_rejects_bad_args(void)
{
    CHECK(rx_pump_create(1024, NULL, NULL, sink_cb, NULL, s_rbuf, sizeof(s_rbuf)) == NULL,
          "read_fn 为 NULL 应拒绝");
    CHECK(rx_pump_create(1024, fake_read, NULL, NULL, NULL, s_rbuf, sizeof(s_rbuf)) == NULL,
          "cb 为 NULL 应拒绝");
    CHECK(rx_pump_create(1024, fake_read, NULL, sink_cb, NULL, NULL, 0) == NULL,
          "read_buf 为空应拒绝");
    CHECK(rx_pump_create(0, fake_read, NULL, sink_cb, NULL, s_rbuf, sizeof(s_rbuf)) == NULL,
          "上界 0 应拒绝");
}

int main(void)
{
    test_partial_message_is_not_delivered();
    test_three_messages_in_one_read();
    test_arbitrary_byte_splits();
    test_again_vs_closed_vs_error();
    test_callback_may_stop();
    test_bad_header_counted_and_resync();
    test_crc_error_counted();
    test_too_large_not_buffered();
    test_create_rejects_bad_args();

    if (s_failures) { printf("rx_pump_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("rx_pump_tests: all checks passed\n");
    return 0;
}
