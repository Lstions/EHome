/**
 * @file device_link_handshake.c
 * @brief 3.0 链路应用层握手的**纯决策**实现（无 IDF 依赖）
 */
#include "device_link_handshake.h"

#include "frame_codec.h"   /* MSG_HELLO_ACK —— 唯一来源，不在这里重抄 0x12 */

static const char *const s_action_names[] = {
    [DLHS_IDLE]           = "IDLE",
    [DLHS_SEND_HELLO]     = "SEND_HELLO",
    [DLHS_NOTE_HANDSHAKE] = "NOTE_HANDSHAKE",
    [DLHS_DONE]           = "DONE",
};

const char *dlhs_action_name(dlhs_action_t a)
{
    if ((int)a < 0 || a > DLHS_DONE) return "UNKNOWN";
    return s_action_names[a];
}

dlhs_action_t dlhs_decide(session_state_t st, bool hello_sent, uint8_t rx_type)
{
    /* ── 规则 1（最高优先级）：收到 HelloAck ⇒ note ──
     *
     * 为什么放在最前面、且**不先判 state**：
     *   1. 它是进入 READY 的**唯一**途径（session.h 的 note_handshake）；
     *   2. 真实的 HelloAck 恰恰是在 **WAIT_HANDSHAKE** 期间收到的
     *      （设备发了 Hello，链路态仍是"等应用层握手"）—— 若先要求 READY，
     *      就永远等不到；
     *   3. 若先判"DOWN/BACKOFF ⇒ IDLE"，还会丢掉"链路状态刚变化那一轮收到的
     *      HelloAck"，让握手白白重来一次。 */
    if (rx_type == MSG_HELLO_ACK) {
        /* READY 之后又收到 HelloAck：**不再重复 note**。
         *
         * 为什么必须幂等：session_note_handshake() 会把**退避计数清零**。
         * 重复 note 会让"链路已就绪但业务不通"时退避永远归零 ⇒ 每秒重连
         * 一次（重连风暴）—— 正是 session.h §4.2 要防的那件事，且日志上
         * 只看到"不断重连"，看不出"其实一直连上了"。 */
        if (st == SESSION_READY) return DLHS_DONE;
        return DLHS_NOTE_HANDSHAKE;
    }

    /* ── 规则 2：已 READY 且不是 HelloAck ⇒ 无需动作 ── */
    if (st == SESSION_READY) return DLHS_DONE;

    /* ── 规则 3：链路刚通且本代际尚未发过 ⇒ 发 Hello（**只发一次**）──
     *
     * 调用方每轮 poll 都会问一次（本任务 10ms 一轮）。若无脑发，就是每秒
     * 100 条 Hello 打爆对端，而日志上只看到"一直在发 Hello"。
     * ⇒ hello_sent 是这条规则的载体，由调用方在**链路代际变化**时清零
     *   （见 dlhs_link_generation_changed 与头文件里"重连"那节）。 */
    if (st == SESSION_WAIT_HANDSHAKE && !hello_sent) return DLHS_SEND_HELLO;

    /* ── 其余：DOWN / BACKOFF / FATAL（发了也白发），或 WAIT_HANDSHAKE 但已发过 ── */
    return DLHS_IDLE;
}

bool dlhs_link_generation_changed(session_state_t prev, session_state_t now)
{
    /* "新连上"的定义：**从非 WAIT_HANDSHAKE 态进入 WAIT_HANDSHAKE**。
     *
     * 为什么不能简单比较 state 是否变化：WAIT_HANDSHAKE 会连续出现很多轮
     * （正常，等着收 HelloAck），逐轮"变化"会误判成重连并把 hello_sent 反复
     * 清零 ⇒ 退化成"每轮都发 Hello"。
     *
     * 为什么要覆盖"任意非 WAIT_HANDSHAKE 态"而不是只看 DOWN：
     * session_poll 在 BACKOFF 到点的那一次调用里会 **BACKOFF → DOWN →
     * WAIT_HANDSHAKE** 一气呵成（session.c 的 fall through），调用方观察到的是
     * BACKOFF -> WAIT_HANDSHAKE。只认 DOWN 会漏掉这条真实路径 ⇒ 重连后不发
     * Hello ⇒ "第一次没连上就再也连不上"。 */
    return (prev != SESSION_WAIT_HANDSHAKE) && (now == SESSION_WAIT_HANDSHAKE);
}
