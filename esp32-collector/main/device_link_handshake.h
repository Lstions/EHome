/**
 * @file device_link_handshake.h
 * @brief 3.0 链路的**应用层握手推进**（纯决策，宿主可测，不依赖 IDF）
 *
 * ## 为什么单独一个文件
 * task-15 给 session 补了 `session_send`，但 **main/ 里没有任何地方调用它**，
 * 也没有任何地方调用 `session_note_handshake`。后果：即使把
 * CONFIG_EHOME_DEVICE_LINK_ENABLED 打开，链路也只会停在 WAIT_HANDSHAKE ——
 * 设备发不出 Hello ⇒ 收不到 HelloAck ⇒ 永远进不了 READY。
 * 这条"接线了但走不完握手"的死路**没有任何一处会报错**（构建绿、门禁绿）。
 *
 * 本文件把"下一轮该做什么"抽成一个**纯函数**，于是那些容易写错的边界
 * （只发一次、收到 0x12 才 note、重连后允许重发）都能在宿主机上被穷举，
 * 而不是只能靠真机 + 后端才能发现。
 *
 * ## 与 session / tls_guard 的分工
 * 本文件**不做 I/O**、不碰 socket、不判时间可信 —— 只回答"下一步动作"。
 * 真正的发送由调用方用 `session_send` 完成（见 device_link_wiring.c）。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试。
 */
#ifndef EHOME_DEVICE_LINK_HANDSHAKE_H
#define EHOME_DEVICE_LINK_HANDSHAKE_H

#include <stdbool.h>
#include <stdint.h>

#include "session.h"   /* session_state_t —— 决策的输入之一 */

#ifdef __cplusplus
extern "C" {
#endif

/** 下一轮要做的动作。每个取值对应调用方**一个**明确分支（P1）。 */
typedef enum {
    DLHS_IDLE = 0,          /* 什么都不做（链路没通 / 已就绪且无事可做） */
    DLHS_SEND_HELLO,        /* 发一条 0x01 Hello（**只发一次**，见下） */
    DLHS_NOTE_HANDSHAKE,    /* 收到 0x12 HelloAck ⇒ session_note_handshake() */
    DLHS_DONE,              /* 握手已完成（READY），且本轮无需动作 */
} dlhs_action_t;

const char *dlhs_action_name(dlhs_action_t a);

/**
 * ⭐ 决策函数（纯函数：同样的输入永远给同样的输出，无副作用、无全局状态）。
 *
 * @param st          当前 session 状态（session_state() 的返回值）
 * @param hello_sent  本**连接代际**内是否已经发过 Hello
 *                    （"代际"的含义见下方"重连"一节）
 * @param rx_type     刚收到的那条消息的类型；**本轮没收到任何消息时传 0**
 *
 * @return 下一步动作
 *
 * ## 决策表（逐条由 host_tests/device_link_handshake_tests.c 锁定）
 *
 * | st | rx_type | hello_sent | 动作 | 理由 |
 * |---|---|---|---|---|
 * | 任意 | `MSG_HELLO_ACK`(0x12) | 任意 | **NOTE_HANDSHAKE** | 收到 HelloAck 是进入 READY 的**唯一**途径 |
 * | READY | 其它 | 任意 | DONE | 已就绪，不重复 note |
 * | WAIT_HANDSHAKE | 非 0x12 | **false** | **SEND_HELLO** | 链路刚通，发 Hello |
 * | WAIT_HANDSHAKE | 非 0x12 | **true** | IDLE | **只发一次**，不要每轮 poll 都发 |
 * | DOWN / BACKOFF / FATAL | 任意 | 任意 | IDLE | 链路没通，发了也白发 |
 *
 * ## ⚠ 两条最容易写错的规则（都由测试钉住）
 *
 * ### 1. Hello **只发一次**
 * 调用方每轮 poll 都会问一次。若 `WAIT_HANDSHAKE` 就无脑发，会变成
 * **每轮一条 Hello**（本任务 10ms 一轮 ⇒ 每秒 100 条）——把对端打爆，
 * 且日志上看只是"一直在发 Hello"，看不出是 bug。
 * ⇒ `hello_sent` 是这条规则的载体。
 *
 * ### 2. ⭐ 重连后必须**允许重发** Hello
 * 这是最容易漏的一条：`hello_sent` 若只在启动时清零，那么
 * **第一次连接失败重连后，新链路上永远不发 Hello** ⇒ 新连接永远进不了
 * READY ⇒ 表现为"第一次没连上就再也连不上"。
 * ⇒ 调用方**必须**在链路代际变化时把 `hello_sent` 清零。
 *   判据用 `dlhs_link_generation_changed()`（见下），不要在调用方自己发明
 *   一个"什么时候算重连"的规则 —— 那正是同一语义两处定义（P4）。
 *
 * 注意：本函数**不**修改 `hello_sent`。它是纯函数，状态的推进由调用方做
 * （这样决策与状态推进各自可测，也让"忘了推进"这类缺陷暴露在调用方一处）。
 */
dlhs_action_t dlhs_decide(session_state_t st, bool hello_sent, uint8_t rx_type);

/**
 * 链路代际是否变化 —— 用于决定"何时把 hello_sent 清零"。
 *
 * 为什么需要它而不是让调用方自己比较：`WAIT_HANDSHAKE` 可能连续出现多次
 * （正常），也可能"断开后又回到 WAIT_HANDSHAKE"（重连）。两者在**状态序列**
 * 上无法区分，必须靠"进入过非 WAIT_HANDSHAKE 的态"来判定 ——
 * 这是个容易写反的判据，所以放在这里、由测试钉住。
 *
 * @param prev        上一轮的 session 状态
 * @param now         本轮的状态
 * @return true 表示发生了"链路重建"，调用方应把 hello_sent 清零
 *
 * 判据（测试锁定）：
 *   - 由**任意非 WAIT_HANDSHAKE 态**进入 WAIT_HANDSHAKE ⇒ true（这是"新连上"）；
 *   - 其余（含 WAIT_HANDSHAKE -> WAIT_HANDSHAKE 持续）⇒ false。
 */
bool dlhs_link_generation_changed(session_state_t prev, session_state_t now);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_DEVICE_LINK_HANDSHAKE_H */
