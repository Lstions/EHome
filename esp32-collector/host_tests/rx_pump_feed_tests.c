/* rx_pump_feed_tests.c —— "投喂型"接收泵：D-09 修复真正落地的那个缝合点
 *
 * ## 与 rx_pump_tests.c 的关系（不是重复）
 * rx_pump_tests.c 测的是 rx_pump_step：泵**自己**通过注入的 read_fn 去读，
 * 锁的是"读-定界-交付"这条链。
 *
 * 但 ehome_tcp.c 的形态是**阻塞 recv 循环**——字节已经在手里了。
 * 它需要的是"把这段字节投进去"。本文件锁的就是那个入口 rx_pump_feed。
 *
 * 为什么必须单独锁：D-09 的生产缺陷形状是
 *
 *     transport->msg_cb(recv_buf, received, ...);   // 一次 recv == 一条消息
 *     msg_handler_process(data, len) 里 data[0] 当消息类型读
 *
 * 一旦有人把缝合点写回"直接回调原始字节"，rx_pump_tests.c 会**全绿**
 * ——因为它根本不经过那条路径。这正是本项目反复出现的"假绿"：
 * 测试覆盖的是引擎，而缺陷在接口上。
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

/* ================= 收集交付的消息 ================= */
typedef struct {
    int      count;
    uint8_t  type[16];
    uint32_t seq[16];
    uint16_t plen[16];
    uint8_t  payload[16][96];
    size_t   copied[16];
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
        s_sink.seq[i]  = m->seq;
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

static rx_pump_t *make_feeder(uint32_t max_payload)
{
    return rx_pump_create_feeder(max_payload, sink_cb, NULL);
}

/* ============ 1. ⭐⭐ D-09 核心：半条消息不得交付 ============
 *
 * 旧路径下这段字节会被原样交给 msg_handler_process(data, len)，
 * 而那里第 184 行读 data[0] 当消息类型 —— 它拿到的不是类型而是
 * 3.0 帧的 magic 首字节（0x45）。于是"半条消息"被当成"一条格式不对的消息"，
 * 真正的错误被申报成"未知类型"，且剩余半条字节**永久丢失**。
 */
static void test_partial_feed_not_delivered(void)
{
    sink_reset();
    rx_pump_t *p = make_feeder(WIRE_PAYLOAD_MAX);
    CHECK(p != NULL, "feeder 构造应成功");
    if (!p) return;

    uint8_t pl[40]; for (int i = 0; i < 40; i++) pl[i] = (uint8_t)(0xA0 + i);
    uint8_t frame[128];
    size_t flen = mkmsg(frame, sizeof(frame), 0x21, 7, pl, 40);
    CHECK(flen == WIRE_HEADER_BYTES + 40, "帧长应为 12+40");

    /* 先投头 6 字节：连头都没凑齐 */
    rx_pump_result_t res = RX_PUMP_FATAL;
    size_t d = rx_pump_feed(p, frame, 6, &res);
    CHECK(d == 0, "只投 6 字节不得交付任何消息（实际 %zu）", d);
    CHECK(s_sink.count == 0, "回调不应被调用（实际 %d 次）", s_sink.count);
    CHECK(res == RX_PUMP_IDLE, "结论应为 IDLE，实际 %s", rx_pump_result_name(res));

    /* 再投到"头齐、载荷缺 1" */
    d = rx_pump_feed(p, frame + 6, flen - 6 - 1, &res);
    CHECK(d == 0, "头齐但载荷缺 1 字节也不得交付（实际 %zu）", d);
    CHECK(s_sink.count == 0, "回调仍不应被调用（实际 %d 次）", s_sink.count);

    /* 补上最后 1 字节 -> 这时才交付，且只有一条 */
    d = rx_pump_feed(p, frame + flen - 1, 1, &res);
    CHECK(d == 1, "补全后应交付恰好 1 条（实际 %zu）", d);
    CHECK(res == RX_PUMP_DELIVERED, "结论应为 DELIVERED，实际 %s", rx_pump_result_name(res));
    CHECK(s_sink.count == 1, "回调应恰好 1 次（实际 %d）", s_sink.count);
    CHECK(s_sink.type[0] == 0x21, "类型应为 0x21，实际 0x%02X", s_sink.type[0]);
    CHECK(s_sink.seq[0] == 7, "seq 应为 7，实际 %u", s_sink.seq[0]);
    CHECK(s_sink.plen[0] == 40, "载荷长应为 40，实际 %u", s_sink.plen[0]);
    /* 拼接正确：40 字节逐个比对，防止"头对但载荷错位"这种会通过长度断言的假绿 */
    CHECK(memcmp(s_sink.payload[0], pl, 40) == 0, "累积拼接的载荷必须逐字节正确");

    /* 字节计数如实：投了 6 + 33 + 1 = 40 字节 */
    rx_pump_stats_t st; rx_pump_get_stats(p, &st);
    /* 期望值从 flen 派生，不手算 —— 我第一版把"载荷 40 B"当成"整帧 40 B"写死了
     * 40，测试自己红了。断言里出现的数字必须是可推导的，否则它测的是我的算术。 */
    CHECK(st.bytes_read == (uint32_t)flen,
          "bytes_read 应恰为整帧长 %zu（实际 %u）—— 多计会掩盖重复投喂", flen, st.bytes_read);
    CHECK(st.msgs_delivered == 1, "msgs_delivered 应为 1（实际 %u）", st.msgs_delivered);

    rx_pump_destroy(p);
}

/* ============ 2. 一次投喂含 N 条 -> 交付 N 条 ============
 *
 * 旧实现只交付第一条，其余 N-1 条被**静默丢弃**（不是报错，是根本没有代码路径）。 */
static void test_many_in_one_feed(void)
{
    sink_reset();
    rx_pump_t *p = make_feeder(WIRE_PAYLOAD_MAX);
    if (!p) { CHECK(false, "构造失败"); return; }

    uint8_t blob[512]; size_t off = 0;
    const int N = 3;
    for (int i = 0; i < N; i++) {
        uint8_t pl[10]; memset(pl, (uint8_t)(0x10 + i), sizeof(pl));
        off += mkmsg(blob + off, sizeof(blob) - off, (uint8_t)(0x30 + i),
                     (uint32_t)(100 + i), pl, sizeof(pl));
    }
    CHECK(off == (size_t)N * (WIRE_HEADER_BYTES + 10), "三条总长应为 %d，实际 %zu", N * 22, off);

    rx_pump_result_t res = RX_PUMP_FATAL;
    size_t d = rx_pump_feed(p, blob, off, &res);
    CHECK(d == (size_t)N, "一次投喂 %d 条应交付 %d 条（实际 %zu）—— 少交就是静默丢弃", N, N, d);
    CHECK(res == RX_PUMP_DELIVERED, "结论应为 DELIVERED，实际 %s", rx_pump_result_name(res));
    for (int i = 0; i < N; i++) {
        CHECK(s_sink.type[i] == (uint8_t)(0x30 + i), "第 %d 条类型应为 0x%02X，实际 0x%02X",
              i, 0x30 + i, s_sink.type[i]);
        CHECK(s_sink.seq[i] == (uint32_t)(100 + i), "第 %d 条 seq 应为 %u，实际 %u",
              i, 100 + i, s_sink.seq[i]);
        for (int k = 0; k < 10; k++) {
            CHECK(s_sink.payload[i][k] == (uint8_t)(0x10 + i),
                  "第 %d 条载荷第 %d 字节应为 0x%02X，实际 0x%02X —— 三条串味",
                  i, k, 0x10 + i, s_sink.payload[i][k]);
        }
    }
    rx_pump_destroy(p);
}

/* ============ 3. 1.5 条：交付 1 条、剩下半个字节**留在缓冲**，不得丢 ============ */
static void test_one_and_a_half(void)
{
    sink_reset();
    rx_pump_t *p = make_feeder(WIRE_PAYLOAD_MAX);
    if (!p) { CHECK(false, "构造失败"); return; }

    uint8_t a[8]; memset(a, 0xAA, sizeof(a));
    uint8_t b[8]; memset(b, 0xBB, sizeof(b));
    uint8_t fA[64], fB[64];
    size_t la = mkmsg(fA, sizeof(fA), 0x41, 1, a, sizeof(a));
    size_t lb = mkmsg(fB, sizeof(fB), 0x42, 2, b, sizeof(b));

    uint8_t chunk[128]; size_t off = 0;
    memcpy(chunk + off, fA, la); off += la;
    memcpy(chunk + off, fB, 7);  off += 7;      /* B 只给 7 字节（< 12 头长） */

    rx_pump_result_t res = RX_PUMP_FATAL;
    size_t d = rx_pump_feed(p, chunk, off, &res);
    CHECK(d == 1, "1.5 条只应交付前 1 条（实际 %zu）", d);
    CHECK(s_sink.type[0] == 0x41, "交付的应是 A（实际 0x%02X）", s_sink.type[0]);

    /* 补上 B 的其余字节 */
    d = rx_pump_feed(p, fB + 7, lb - 7, &res);
    CHECK(d == 1, "补全后应交付 B（实际 %zu）", d);
    CHECK(s_sink.count == 2, "总计应交付 2 条（实际 %d）", s_sink.count);
    CHECK(s_sink.type[1] == 0x42, "第二条应是 B（实际 0x%02X）", s_sink.type[1]);
    CHECK(s_sink.seq[1] == 2, "B 的 seq 应为 2（实际 %u）", s_sink.seq[1]);
    for (int k = 0; k < 8; k++) {
        CHECK(s_sink.payload[1][k] == 0xBB, "B 载荷第 %d 字节应为 0xBB（实际 0x%02X）"
              " —— 说明跨 feed 的字节被错位或丢失", k, s_sink.payload[1][k]);
    }
    rx_pump_destroy(p);
}

/* ============ 4. 头非法：计数 + 报 ERROR，不静默跳过 ============ */
static void test_bad_magic_reported(void)
{
    sink_reset();
    rx_pump_t *p = make_feeder(WIRE_PAYLOAD_MAX);
    if (!p) { CHECK(false, "构造失败"); return; }

    uint8_t bad[32]; memset(bad, 0, sizeof(bad));
    bad[0] = 0xDE; bad[1] = 0xAD;   /* magic 错 */
    rx_pump_result_t res = RX_PUMP_IDLE;
    size_t d = rx_pump_feed(p, bad, sizeof(bad), &res);
    CHECK(d == 0, "坏头不得交付（实际 %zu）", d);
    CHECK(res == RX_PUMP_ERROR, "坏头应报 ERROR，实际 %s", rx_pump_result_name(res));
    rx_pump_stats_t st; rx_pump_get_stats(p, &st);
    CHECK(st.malformed == 1, "malformed 计数应为 1（实际 %u）—— 不计数就是把损坏当正常", st.malformed);

    /* 错误不得粘住：紧接着一条好帧必须正常交付 */
    uint8_t pl[4] = { 1, 2, 3, 4 };
    uint8_t good[64]; size_t gl = mkmsg(good, sizeof(good), 0x55, 9, pl, sizeof(pl));
    d = rx_pump_feed(p, good, gl, &res);
    CHECK(d == 1, "坏头之后的好帧应恢复交付（实际 %zu）", d);
    CHECK(s_sink.type[0] == 0x55, "类型应为 0x55（实际 0x%02X）", s_sink.type[0]);
    rx_pump_destroy(p);
}

/* ============ 5. 回调要求停止后，剩余消息**不得丢**：下次投喂仍能取出 ============ */
static void test_stop_keeps_remaining(void)
{
    sink_reset();
    rx_pump_t *p = make_feeder(WIRE_PAYLOAD_MAX);
    if (!p) { CHECK(false, "构造失败"); return; }

    uint8_t blob[256]; size_t off = 0;
    for (int i = 0; i < 3; i++) {
        uint8_t pl[4] = { (uint8_t)i, 0, 0, 0 };
        off += mkmsg(blob + off, sizeof(blob) - off, (uint8_t)(0x60 + i), (uint32_t)i, pl, sizeof(pl));
    }

    s_sink.stop_after = 1;
    rx_pump_result_t res = RX_PUMP_FATAL;
    size_t d = rx_pump_feed(p, blob, off, &res);
    CHECK(d == 1, "回调停止后本轮应只交付 1 条（实际 %zu）", d);
    CHECK(res == RX_PUMP_DELIVERED, "结论应为 DELIVERED，实际 %s", rx_pump_result_name(res));

    /* 关键：剩余的 2 条已完整躺在定界器里，必须能在**不再投喂新字节**时取出。
     * 若实现只在"投新字节"时才派发，这 2 条会永远交不出来，且计数看不出异常。 */
    s_sink.stop_after = 0;
    d = rx_pump_feed(p, NULL, 0, &res);
    CHECK(d == 2, "不投新字节也应取出缓冲里剩余的 2 条（实际 %zu）—— 否则消息静默卡死", d);
    CHECK(s_sink.count == 3, "总计应交付 3 条（实际 %d）", s_sink.count);
    CHECK(s_sink.type[1] == 0x61 && s_sink.type[2] == 0x62,
          "剩余两条应为 0x61/0x62（实际 0x%02X/0x%02X）", s_sink.type[1], s_sink.type[2]);
    rx_pump_destroy(p);
}

/* ============ 6. 契约守卫：参数错说清楚，不靠崩 ============ */
static void test_create_and_entry_guards(void)
{
    CHECK(rx_pump_create_feeder(WIRE_PAYLOAD_MAX, NULL, NULL) == NULL,
          "cb 为空不得构造（P1：不造半成品）");

    rx_pump_t *p = make_feeder(WIRE_PAYLOAD_MAX);
    CHECK(p != NULL, "正常构造应成功");
    if (p) {
        /* 投喂型泵没有 read_fn：用错入口必须**明说**，而不是空指针调用 */
        uint32_t dl = 0;
        rx_pump_result_t r = rx_pump_step(p, &dl);
        CHECK(r == RX_PUMP_FATAL, "对投喂型泵调 step 应返回 FATAL（实际 %s）",
              rx_pump_result_name(r));
        /* in==NULL 且 n>0 是调用方 bug */
        rx_pump_result_t res = RX_PUMP_IDLE;
        size_t d = rx_pump_feed(p, NULL, 5, &res);
        CHECK(d == 0 && res == RX_PUMP_FATAL, "in=NULL,n>0 应为 FATAL（实际 %s）",
              rx_pump_result_name(res));
        rx_pump_destroy(p);
    }
    /* NULL 泵不得崩 */
    rx_pump_result_t res2 = RX_PUMP_IDLE;
    CHECK(rx_pump_feed(NULL, (const uint8_t *)"", 0, &res2) == 0, "NULL 泵应安全返回 0");
    CHECK(res2 == RX_PUMP_FATAL, "NULL 泵结论应为 FATAL，实际 %s", rx_pump_result_name(res2));
}

/* ============ 7. ⚠ 层间守卫：2.x 帧**不是** 3.0 帧，本定界器不得被塞给旧路径 ============
 *
 * 这条断言的存在理由是我自己差点犯的错：
 *
 * 复盘里我把"下一步"写成"把 rx_pump 接进 ehome_tcp.c 替换 :477 的裸回调"。
 * 实际读代码后才发现 —— ehome_tcp 那条路径送的是 **2.x 帧**：
 *   - 消费侧 msg_handler_process 读 data[0] 当消息类型，再 frame_decoder_init(data, len)；
 *   - 2.x 帧**没有长度前缀**（首字节=类型 + TLV 字段）。
 * 而 3.0 定界器要求 magic=0x4548 / ver=0x30 / 12 B 定长头 + payload_len。
 *
 * 若真把本定界器接进旧路径，**每一条帧都会被判 MALFORMED** —— 不是修好，
 * 而是把"静默解析错"换成"全部拒收"。两个方向的失败都不报对。
 *
 * 所以这条守卫断言的是"两层不兼容"这个事实本身：
 * 谁再想把 3.0 定界器当 2.x 的补丁，会在这里看到明确的红。
 */
static void test_legacy_2x_frame_is_not_3x_frame(void)
{
    sink_reset();
    rx_pump_t *p = make_feeder(WIRE_PAYLOAD_MAX);
    if (!p) { CHECK(false, "构造失败"); return; }

    /* 一条 2.x 帧：首字节是消息类型（0x0A = OtaCmd），其后是 TLV，**无长度前缀**。
     * 前两字节 0x0A,0x08 与 3.0 magic 0x4548 完全不同。
     *
     * ⚠ 必须投满 >= WIRE_HEADER_BYTES(12) 字节：定界器在字节不足 12 时返回
     * NEED_MORE（"等着"，不是"报错"）。我第一版只投 8 字节，于是拿到 IDLE
     * 而不是 ERROR —— 这是**测试自己的错**，不是实现错。这个区别本身值得记住：
     * 短前缀是"静默等待"，只有凑满一个头才会被判非法。 */
    const uint8_t legacy2x[16] = {
        0x0A, 0x08, 0x01, 0x00, 0x10, 0x02, 0x34, 0x12,
        0x20, 0x04, 0x00, 0x01, 0x02, 0x03, 0x04, 0x05
    };
    rx_pump_result_t res = RX_PUMP_IDLE;
    size_t d = rx_pump_feed(p, legacy2x, sizeof(legacy2x), &res);
    CHECK(d == 0, "2.x 帧不得被 3.0 定界器交付（实际 %zu）", d);
    CHECK(res == RX_PUMP_ERROR, "2.x 帧应报 ERROR（实际 %s）—— 两层格式不兼容",
          rx_pump_result_name(res));
    rx_pump_stats_t st; rx_pump_get_stats(p, &st);
    CHECK(st.malformed == 1, "应计 1 次 malformed（实际 %u）", st.malformed);
    CHECK(st.msgs_delivered == 0, "不得出现任何「已交付」（实际 %u）", st.msgs_delivered);
    rx_pump_destroy(p);

    /* 反证：真正的 3.0 帧必须被接受 —— 否则上面那组断言可能只是
     * "这个定界器什么都不收"，属假守卫。
     * 用**新实例**：旧实例在 MALFORMED 后按设计丢弃累积数据并重新同步，
     * 紧跟其后的那一条好帧会被一并吃掉（重同步的固有代价，wire_tests 已锁）。 */
    sink_reset();
    rx_pump_t *q = make_feeder(WIRE_PAYLOAD_MAX);
    if (!q) { CHECK(false, "构造失败"); return; }
    uint8_t pl[4] = { 0x0A, 0x01, 0x02, 0x03 };
    uint8_t f[64]; size_t fl = mkmsg(f, sizeof(f), 0x21, 1, pl, sizeof(pl));
    d = rx_pump_feed(q, f, fl, &res);
    CHECK(d == 1, "3.0 帧应被接受（实际 %zu）—— 否则是「一律拒收」的假守卫", d);
    CHECK(s_sink.type[0] == 0x21, "类型应为 0x21（实际 0x%02X）", s_sink.type[0]);
    rx_pump_destroy(q);
}

int main(void)
{
    test_partial_feed_not_delivered();
    test_many_in_one_feed();
    test_one_and_a_half();
    test_bad_magic_reported();
    test_stop_keeps_remaining();
    test_create_and_entry_guards();
    test_legacy_2x_frame_is_not_3x_frame();

    if (s_failures) { printf("rx_pump_feed_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("rx_pump_feed_tests: all checks passed\n");
    return 0;
}
