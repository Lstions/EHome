/* session_transport_tests.c —— 3.0 会话→transport 适配的**纯判定**（宿主可测）
 *
 * ## 为什么这些断言值得存在
 *
 * 适配层里只有两件事有判断，而两件错了都**没有报错**：
 *
 * 1. **"能否上行"的判据**（is_connected）。若把 WAIT_HANDSHAKE 也算 true，
 *    transport_broadcast 会把帧投给一个应用层未就绪的链路 ⇒ **静默丢弃**。
 * 2. **"这次发送到底怎么了"的分类**（stx_classify_send）。最危险的是
 *    **部分写出后失败**：那一刻 TCP 流里已有半帧，对端重组器会一直等。
 *    若把它归成"可重试"，调用方重发整帧 ⇒ 前缀写第二遍 ⇒ **静默数据损坏**（D-30）。
 *
 * ⇒ 两条都必须用**后果**断言（不是"返回值等于某枚举"就算），
 *   尤其要覆盖"progress 已推进"这一族。
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>

#include "session_transport.h"

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

/* ════════ 1. "能否上行"只认 READY ════════ */
static void test_only_ready_counts_as_up(void)
{
    CHECK(session_transport_ready(SESSION_READY) == true,
          "READY 必须是可上行");

    /* 其余四个状态**全部**不可上行。逐个断言而不是一句 !=READY：
     * 逐个才能在将来新增状态时立刻发现"新状态没被归类"。 */
    CHECK(session_transport_ready(SESSION_DOWN) == false, "DOWN 不可上行");
    CHECK(session_transport_ready(SESSION_BACKOFF) == false, "BACKOFF 不可上行");
    CHECK(session_transport_ready(SESSION_FATAL) == false, "FATAL 不可上行");

    /* ⭐ 最关键的一条：WAIT_HANDSHAKE **不算**可上行。
     * 链路是通的（TLS 已连），但应用层未握手 ⇒ 投给它就是静默丢弃。
     * 这条若写成 true，症状是"帧消失且没有任何错误"。 */
    CHECK(session_transport_ready(SESSION_WAIT_HANDSHAKE) == false,
          "WAIT_HANDSHAKE 不算可上行（链路通但应用层未就绪）—— "
          "算 true 会让 transport_broadcast 把帧投给发不出去的链路，表现为静默丢弃");
}

/* ════════ 2. 整帧写出 = DONE ════════ */
static void test_full_write_is_done(void)
{
    CHECK(stx_classify_send(LINK_SENT_FULL, 100, 100) == STX_SEND_DONE,
          "progress==len 且 FULL ⇒ DONE");
    /* 契约不符（FULL 但 progress<len）⇒ 保守按"流已污染"，不当作成功。 */
    CHECK(stx_classify_send(LINK_SENT_FULL, 50, 100) == STX_SEND_STREAM_DIRTY,
          "FULL 却 progress<len（link 层违约）⇒ 必须按 STREAM_DIRTY，不能当成功");
}

/* ════════ 3. ⭐ 部分写出后失败 = STREAM_DIRTY（不是 RETRY）════════ */
static void test_partial_write_is_stream_dirty(void)
{
    /* 这是本文件最重要的一组：progress>0 且未完成 ⇒ 流里已有半帧。
     * 归成 RETRY 会让调用方重发整帧 ⇒ 重复前缀 ⇒ 静默损坏。 */
    CHECK(stx_classify_send(LINK_BACKPRESSURE, 40, 100) == STX_SEND_STREAM_DIRTY,
          "BACKPRESSURE 但已写出 40/100 ⇒ STREAM_DIRTY（重发整帧会污染流）");
    CHECK(stx_classify_send(LINK_SENT_PARTIAL, 40, 100) == STX_SEND_STREAM_DIRTY,
          "PARTIAL 且已写出 ⇒ STREAM_DIRTY");
    CHECK(stx_classify_send(LINK_FATAL, 1, 100) == STX_SEND_STREAM_DIRTY,
          "FATAL 但已写出 1 字节 ⇒ STREAM_DIRTY（不是 NOT_READY —— 流已脏）");

    /* 对照：progress==0 时"重试整帧"才是安全的。 */
    CHECK(stx_classify_send(LINK_BACKPRESSURE, 0, 100) == STX_SEND_RETRY,
          "BACKPRESSURE 且一字节没写出 ⇒ RETRY（整帧重发安全）");
    CHECK(stx_classify_send(LINK_SENT_PARTIAL, 0, 100) == STX_SEND_RETRY,
          "PARTIAL 但 progress==0（矛盾输入）⇒ 保守 RETRY");
}

/* ════════ 4. 未就绪 / 超 MTU ════════ */
static void test_not_ready_and_too_big(void)
{
    CHECK(stx_classify_send(LINK_NOT_READY, 0, 100) == STX_SEND_NOT_READY,
          "NOT_READY ⇒ NOT_READY");
    CHECK(stx_classify_send(LINK_FATAL, 0, 100) == STX_SEND_NOT_READY,
          "FATAL 且未写出 ⇒ NOT_READY");
    CHECK(stx_classify_send(LINK_PAYLOAD_TOO_BIG, 0, 20000) == STX_SEND_TOO_BIG,
          "超 MTU ⇒ TOO_BIG（重试无用，调用方必须分片或拒绝）");

    /* 未知取值必须保守（不能默认成功）。 */
    CHECK(stx_classify_send((link_result_t)999, 0, 100) == STX_SEND_NOT_READY,
          "未知 link_result ⇒ 保守 NOT_READY，绝不能默认 DONE");
}

/* ════════ 5. 映射到 esp_err：**成功只有一种，且不是 0 以外的任何值** ════════ */
static void test_esp_err_mapping(void)
{
    CHECK(stx_to_esp_err(STX_SEND_DONE) == 0, "DONE ⇒ ESP_OK(0)");

    /* 关键性质：除 DONE 外**都不得**返回 0。
     * 否则上层会把"没发出去"当成功 —— D-01 的病根就是压平错误。 */
    const stx_send_class_t bad[] = { STX_SEND_RETRY, STX_SEND_NOT_READY,
                                     STX_SEND_TOO_BIG, STX_SEND_STREAM_DIRTY };
    for (unsigned i = 0; i < sizeof(bad) / sizeof(bad[0]); i++) {
        CHECK(stx_to_esp_err(bad[i]) != 0,
              "非 DONE 的分类 %s 不得映射为 ESP_OK(0)",
              stx_send_class_name(bad[i]));
    }

    /* 可重试与不可重试必须**可区分**：否则调用方无法决定"稍后再来"还是"换路"。 */
    CHECK(stx_to_esp_err(STX_SEND_RETRY) != stx_to_esp_err(STX_SEND_NOT_READY),
          "RETRY 与 NOT_READY 必须映射到不同错误码（调用方处置不同）");
    CHECK(stx_to_esp_err(STX_SEND_TOO_BIG) != stx_to_esp_err(STX_SEND_STREAM_DIRTY),
          "TOO_BIG 与 STREAM_DIRTY 必须可区分（前者分片、后者重建链路）");
}

/* ════════ 6. 名字非空（日志/诊断会打印）════════ */
static void test_names_nonempty(void)
{
    const stx_send_class_t all[] = { STX_SEND_DONE, STX_SEND_RETRY, STX_SEND_NOT_READY,
                                     STX_SEND_TOO_BIG, STX_SEND_STREAM_DIRTY };
    for (unsigned i = 0; i < sizeof(all) / sizeof(all[0]); i++) {
        CHECK(stx_send_class_name(all[i])[0] != '\0', "分类名不应为空");
    }
    CHECK(stx_send_class_name((stx_send_class_t)999) != NULL, "未知枚举应返回非 NULL");
}


/* ════════ 7. ⭐ task-21：组合判据（语义 AND 策略）════════ */
/* 为什么这组必须存在：is_connected 是**唯一**决定"这一帧投不投给 3.0"的地方。
 * 它错了有两种相反的静默故障：
 *   过松（只看 READY，不看策略）⇒ 双栈稳态**双发**；
 *   过严（只看策略，不看 READY）⇒ 未握手就被投 ⇒ **静默丢弃**。
 * ⇒ 两个方向各一条断言，缺一不可。 */
static void test_connected_requires_both_semantics_and_policy(void)
{
    /* (a) 就绪 + 放行 ⇒ 可投（正常态） */
    CHECK(session_transport_connected(SESSION_READY, true) == true,
          "READY 且闸放行 ⇒ 可投");

    /* (b) 就绪但闸关（如 tsel 已切 MQTT）⇒ **不可投**。
     *     这一条就是"消除双发"在适配层的落点：
     *     若写成 true，transport_broadcast 会同时投给 3.0 与 MQTT ⇒ 双发。 */
    CHECK(session_transport_connected(SESSION_READY, false) == false,
          "READY 但仲裁未选中 TCP ⇒ 不得投（否则与 MQTT 双发）");

    /* (c) 闸放行但未握手 ⇒ **不可投**（静默丢弃方向）。
     *     这是 session_transport_ready 的既有语义，组合函数必须**继承**它。 */
    const session_state_t not_ready[] = { SESSION_WAIT_HANDSHAKE, SESSION_DOWN,
                                          SESSION_BACKOFF, SESSION_FATAL };
    for (unsigned i = 0; i < sizeof(not_ready) / sizeof(not_ready[0]); i++) {
        CHECK(session_transport_connected(not_ready[i], true) == false,
              "状态 %d 未就绪 ⇒ 即使闸放行也不得投（投了就是静默丢弃）",
              (int)not_ready[i]);
        CHECK(session_transport_connected(not_ready[i], false) == false,
              "状态 %d 未就绪且闸关 ⇒ 更不得投", (int)not_ready[i]);
    }

    /* (d) 与 session_transport_ready **严格一致**（闸恒开时）。
     *     下界断言：穷举全部状态，确认组合函数没有自己另立一套判据。 */
    int checked = 0;
    for (int st = 0; st <= 4; st++) {
        CHECK(session_transport_connected((session_state_t)st, true)
              == session_transport_ready((session_state_t)st),
              "闸恒开时，组合判据必须与 session_transport_ready 完全一致（st=%d）", st);
        checked++;
    }
    CHECK(checked == 5, "应穷举 5 个会话状态，实际 %d", checked);
}


/* ════════ 9. 上行成帧（task-32）—— 本段的理由见 §134 ════════
 *
 * ## 为什么这几条断言值钱
 *
 * 生产上行曾经**全程不成帧**（唯一会写 magic 的 wire_encode_header 在生产代码里
 * 零调用）⇒ 后端 protoframe.DecodeHeader 一律 ErrMagic ⇒ 链路永远进不了 READY。
 * 而当时**宿主 107/107、对锚 rc=0、后端 31 包、17 条门禁全绿**。
 *
 * 它能活下来是因为：出问题的那段代码在 #ifndef SESSION_TRANSPORT_HOST_TEST 里，
 * 宿主测试**结构上看不到**；而对锚客户端**自己拼了 12 B 头**
 * （即对锚证明的是"另一个程序"）。
 *
 * ⇒ 现在成帧编排在**宿主可编段**，且下面断言的是**回调收到的字节**：
 *   "有人把 devlink_encode_frame 这一行删掉"会**立刻**在这里变红。
 */

#include <string.h>
#include "wire.h"   /* WIRE_MAGIC / WIRE_HEADER_BYTES / WIRE_VER —— 判据唯一来源 */

typedef struct {
    uint8_t       buf[4096];
    size_t        len;
    int           calls;
    link_result_t next;      /* 下一次返回什么 */
} cap_t;

static link_result_t cap_write(void *ctx, const uint8_t *data, size_t len, size_t *progress)
{
    cap_t *c = (cap_t *)ctx;
    c->calls++;
    /* ⚠ 契约（link.h + stx_classify_send 都按它判）：
     *   LINK_SENT_FULL  ⇒ *progress 必须被推进到 len，否则分类器按"未完成"处理
     *   LINK_SENT_PARTIAL ⇒ 只写了一部分，progress 推进到实际写出量，
     *                       调用方从这里**续写**（绝不重发整帧）
     * 我第一版忘了推进 progress，于是 LINK_SENT_FULL 被判成 STREAM_DIRTY
     * （rc=257）—— 这恰好说明那些断言**真的在看线上的字节**，不是在看返回值。 */
    if (c->next == LINK_SENT_PARTIAL && *progress == 0) {
        size_t half = len / 2;
        memcpy(c->buf, data, half);      /* 只落下前半 */
        c->len = half;
        *progress = half;                /* 推进到实际写出量 */
        c->next = LINK_SENT_FULL;        /* 下一次写完 */
        return LINK_SENT_PARTIAL;
    }
    if (c->next == LINK_BACKPRESSURE && *progress == 0 && c->calls == 1) {
        /* 第一次：**一个字节都没写出**（link.h 对 BACKPRESSURE 的契约），
         * progress 保持 0 ⇒ 调用方重发整帧是安全的。 */
        return LINK_BACKPRESSURE;
    }
    /* 续写：把剩下那段接到已写下的后面（真实 socket 是顺序追加） */
    size_t from = *progress;
    memcpy(c->buf + from, data, len);
    c->len = from + len;
    *progress = c->len;                  /* == 整帧长度 ⇒ 分类为 DONE */
    return LINK_SENT_FULL;
}

static void test_uplink_is_framed(void)
{
    cap_t cap;
    memset(&cap, 0, sizeof(cap));
    cap.next = LINK_SENT_FULL;
    uint8_t scratch[STX_FRAME_MAX];
    stx_tx_t tx;
    memset(&tx, 0, sizeof(tx));
    tx.write = cap_write;
    tx.ctx = &cap;
    tx.scratch = scratch;
    tx.scratch_cap = sizeof(scratch);

    /* 最小 payload：首字节是**消息类型**（0x23 = device_op ACK）。
     * 值本身不重要，重要的是它与帧头 type 必须相等 —— 后端
     * manager.go:418 会强校验，不一致即丢弃（FrameTypeMismatchTotal）。 */
    const uint8_t payload[] = { 0x23, 0x08, 0x00, 0x12, 0x0a, 0x01, 0x41 };
    int rc = stx_send_frame(&tx, payload, sizeof(payload), 0);
    CHECK(rc == 0, "成帧+发送应成功，实际 rc=%d", rc);
    CHECK(cap.calls == 1, "应只调用一次 write，实际 %d", cap.calls);

    const size_t want = WIRE_HEADER_BYTES + sizeof(payload);
    CHECK(cap.len == want,
          "线上长度应为 header(%u)+payload(%u)=%u，实际 %u —— 若等于 payload 长度，说明成帧被删掉了",
          (unsigned)WIRE_HEADER_BYTES, (unsigned)sizeof(payload), (unsigned)want,
          (unsigned)cap.len);
    CHECK(cap.len > sizeof(payload),
          "线上字节数(%u)必须大于载荷(%u)，否则就是没成帧",
          (unsigned)cap.len, (unsigned)sizeof(payload));
    CHECK(cap.buf[0] == 0x45 && cap.buf[1] == 0x48,
          "magic 必须是 0x45 0x48，实际 0x%02X 0x%02X", cap.buf[0], cap.buf[1]);
    CHECK(cap.buf[2] == (uint8_t)WIRE_VER, "ver 应为 0x%02X，实际 0x%02X",
          (unsigned)WIRE_VER, cap.buf[2]);
    CHECK(cap.buf[3] == payload[0],
          "帧头 type 必须等于 payload[0]=0x%02X（后端强校验），实际 0x%02X",
          payload[0], cap.buf[3]);
    CHECK(cap.buf[4] == 0x00 && cap.buf[5] == 0x00,
          "flags 应为 0（不置 CRC 位，与后端下行对称），实际 0x%02X 0x%02X",
          cap.buf[4], cap.buf[5]);
    {
        const unsigned plen = ((unsigned)cap.buf[10] << 8) | cap.buf[11];
        CHECK(plen == (unsigned)sizeof(payload),
              "payload_len 大端应为 %u，实际 %u", (unsigned)sizeof(payload), plen);
    }
    CHECK(memcmp(cap.buf + WIRE_HEADER_BYTES, payload, sizeof(payload)) == 0,
          "头之后的载荷必须逐字节原样");
}

static void test_uplink_framing_refusals(void)
{
    uint8_t scratch[STX_FRAME_MAX];
    stx_tx_t tx;
    cap_t cap;
    memset(&cap, 0, sizeof(cap));
    memset(&tx, 0, sizeof(tx));
    tx.write = cap_write;
    tx.ctx = &cap;
    tx.scratch = scratch;
    tx.scratch_cap = sizeof(scratch);
    const uint8_t p[] = { 0x23, 0x01 };

    CHECK(stx_send_frame(&tx, p, 0, 0) != 0, "空载荷必须被拒（取不到 type）");
    CHECK(stx_send_frame(&tx, NULL, 2, 0) != 0, "NULL 载荷必须被拒");
    CHECK(cap.calls == 0, "被拒的帧不得触发 write，实际 %d 次", cap.calls);

    static uint8_t big[STX_TX_PAYLOAD_MAX + 1];
    memset(big, 0x23, sizeof(big));
    CHECK(stx_send_frame(&tx, big, sizeof(big), 0) != 0, "超上界必须被拒");
    CHECK(tx.stats.too_big >= 1, "超上界必须计数（可观测），实际 %u",
          (unsigned)tx.stats.too_big);
    CHECK(cap.calls == 0, "超界的帧不得触发 write");

    stx_tx_t tx2 = tx;
    tx2.stats.too_big = 0;
    tx2.scratch_cap = STX_FRAME_MAX - 1;
    CHECK(stx_send_frame(&tx2, p, sizeof(p), 0) != 0, "scratch 不足必须被拒");
    CHECK(tx2.stats.too_big >= 1, "scratch 不足也要计数");
}

static void test_uplink_backpressure_retries_without_duplicating(void)
{
    /* 背压重试：线上不得出现重复前缀（D-30）。 */
    cap_t cap;
    memset(&cap, 0, sizeof(cap));
    cap.next = LINK_BACKPRESSURE;
    uint8_t scratch[STX_FRAME_MAX];
    stx_tx_t tx;
    memset(&tx, 0, sizeof(tx));
    tx.write = cap_write;
    tx.ctx = &cap;
    tx.scratch = scratch;
    tx.scratch_cap = sizeof(scratch);

    const uint8_t payload[] = { 0x23, 0x08, 0x00, 0x12, 0x0a, 0x01, 0x42 };
    int rc = stx_send_frame(&tx, payload, sizeof(payload), 0);
    CHECK(rc == 0, "背压后重试应能成功，实际 rc=%d", rc);
    CHECK(cap.calls >= 2, "背压应触发重试（至少 2 次 write），实际 %d", cap.calls);
    CHECK(tx.stats.retries >= 1, "背压重试应计数，实际 %u", (unsigned)tx.stats.retries);
    CHECK(cap.len == WIRE_HEADER_BYTES + sizeof(payload),
          "重试后线上应恰好一条完整帧 %u 字节，实际 %u（多了=重复写，D-30）",
          (unsigned)(WIRE_HEADER_BYTES + sizeof(payload)), (unsigned)cap.len);
    CHECK(cap.buf[0] == 0x45 && cap.buf[1] == 0x48, "重试后 magic 仍必须正确");
    CHECK(cap.buf[3] == payload[0], "重试后 type 仍必须等于 payload[0]");
    CHECK(memcmp(cap.buf + WIRE_HEADER_BYTES, payload, sizeof(payload)) == 0,
          "重试后载荷必须逐字节原样");
}

static void test_uplink_partial_write_is_stream_dirty(void)
{
    /* 部分写出后返回 ⇒ 归 STREAM_DIRTY 并如实回报（既有语义，见头文件）。 */
    cap_t cap;
    memset(&cap, 0, sizeof(cap));
    cap.next = LINK_SENT_PARTIAL;
    uint8_t scratch[STX_FRAME_MAX];
    stx_tx_t tx;
    memset(&tx, 0, sizeof(tx));
    tx.write = cap_write;
    tx.ctx = &cap;
    tx.scratch = scratch;
    tx.scratch_cap = sizeof(scratch);

    const uint8_t payload[] = { 0x23, 0x08, 0x00, 0x12, 0x0a, 0x01, 0x43 };
    int rc = stx_send_frame(&tx, payload, sizeof(payload), 0);
    CHECK(rc != 0, "部分写出必须**不为 0**（绝不能静默当成成功），实际 rc=%d", rc);
    CHECK(rc == stx_to_esp_err(STX_SEND_STREAM_DIRTY),
          "部分写出应映射为 STREAM_DIRTY，实际 rc=%d", rc);
    CHECK(cap.calls == 1, "STREAM_DIRTY 后不得再写（否则重复字节上线），实际 %d", cap.calls);
    CHECK(tx.stats.failed >= 1, "失败必须计数，实际 %u", (unsigned)tx.stats.failed);
}


int main(void)
{
    test_only_ready_counts_as_up();
    test_full_write_is_done();
    test_partial_write_is_stream_dirty();
    test_not_ready_and_too_big();
    test_esp_err_mapping();
    test_names_nonempty();
    test_connected_requires_both_semantics_and_policy();
    test_uplink_is_framed();
    test_uplink_framing_refusals();
    test_uplink_backpressure_retries_without_duplicating();
    test_uplink_partial_write_is_stream_dirty();

    if (s_failures) { printf("session_transport_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("session_transport_tests: all checks passed\n");
    return 0;
}
