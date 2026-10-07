/**
 * @file session_transport.h
 * @brief 把 3.0 的 `session` 包装成 `transport_ops_t`，让**上行**能走到 TCP。
 *
 * ## 为什么需要它（实测确认的上行缺口）
 * `main/main.c` 只注册了 MQTT 与 debug 用的 tcp_transport；**3.0 的 session 从未注册**。
 * 而 `msg_handler_publish_checked`（msg_handler.c:55）的选路是
 * "当前 transport → broadcast → MQTT 兜底" ⇒ **设备上行永远走 MQTT**。
 * ⇒ 目标态"TCP+TLS 替代 MQTT"里，**上行那一半完全没接**。
 *
 * ## 三个语义要点（每一条都有对应的错误用法）
 * 1. **`is_connected` 必须是"握手完成"（SESSION_READY）**，不是"TLS 连上了"。
 *    仅 `WAIT_HANDSHAKE` 时链路虽通但**应用层未就绪**；若此处返回 true，
 *    `transport_broadcast` 会把帧投给它，而它其实发不出去 —— 表现为**静默丢弃**。
 *    这与 transport.h:146-154 记的教训同源：`transport_registry_has_type(MQTT)` 曾作为
 *    "是否尝试过 MQTT"的**代理判据**，在"已注册但未连接"时不等价。
 *    ⇒ 这里回答**真实问题**："现在能不能上行"。
 * 2. **`send` 要如实回报**：未就绪 / 背压 / 超 MTU 都**不能压成 ESP_OK**，
 *    否则上层以为发出去了（D-01 的病根就是压平）。
 * 3. **部分写出必须续写，绝不重发整帧**（D-30）：TCP 上"重试"就是再写一遍，
 *    已写出的前缀会被写第二次 ⇒ 接收端重组器无法自愈 ⇒ **静默数据损坏**。
 *
 * ## ⚠⚠ 一个极易混淆的点：`is_connected` 与 `session_send` 的门控**不是同一件事**
 *
 * `session_send` **刻意不做状态门控**（session.h 有完整说明）：设备的第一条 **Hello**
 * 正是在 `WAIT_HANDSHAKE` 期间发出的；若在 session 层要求 READY，就形成**死锁**
 * （收不到 HelloAck 是因为发不出 Hello，而发 Hello 又要等 READY）。
 *
 * 那为什么本适配器的 `is_connected` 仍要求 READY？因为两者回答的是**不同问题**：
 *   - `is_connected` 回答：**"现在能不能承载普通上行（遥测/上报）"** —— 应用层未握手前不能；
 *   - `session_send` 回答：**"这一帧能不能写进流"** —— 链路通了就能。
 *
 * ⇒ **`is_connected` 是"选路判据"，绝不能把它当成 `session_send` 的前置条件。**
 *    握手期的那条 Hello 由链路任务直接调 `session_send`（见 device_link_wiring.c），
 *    **不经过** transport 选路。若谁把 READY 判据加到发送路径上，Hello 就永远发不出去，
 *    而症状是"设备一直停在 WAIT_HANDSHAKE"—— 看起来像网络问题。
 *    （这条由 v3-backend 在复核契约时提出，我采信并写进代码。）
 *
 * ## 与 `transport_sel` 的关系（为什么本轮**不**接它）
 * `transport_sel`（§7.3 P2 的"TCP 优先 / MQTT 兜底"）已实现且有宿主测试，但**未接线**。
 *
 * ### ⚠⚠ 更正（2026-10-07，本轮**实测推翻**了我上一轮写在这里的理由）
 *
 * 我上一轮写的是："本适配器一旦注册，`transport_broadcast` 的既有行为
 * （只对 `is_connected()` 为真的 transport 发送）**已经**给出了正确语义 ——
 * 未 READY 时它不会被选中，MQTT 兜底自然保留。"
 *
 * **这个理由是错的，而且错在危险的方向**：
 * 它只考虑了"**未 READY** 时会不会误投"，却漏了**双栈稳态**（MQTT 与 3.0 都 connected）。
 * `transport_broadcast` 的语义是**对每一个 is_connected() 为真的 transport 都发**
 * —— 所以在双栈稳态下，**同一帧会被投递两次**。
 *
 * 这不是推测，是可测的：`host_tests/transport_dualstack_tests.c` 用**真实**
 * `components/transport/transport.c` 构造该场景并数 send 次数 ⇒
 * `mqtt.send_calls == 1 && tcp3.send_calls == 1`，即**一帧两投**。
 *
 * 而且触发路径正是**设备主动上行**：`msg_handler_publish_checked` 先看
 * `s_current_transport`，而它**只在处理下行期间非 NULL**
 * （`msg_handler_process_with_transport` 进去置、出来清，见 msg_handler.c:145-163）。
 * 设备自己发 Hello/DataReport/DataBatch/状态上报时它是 NULL ⇒ **走 broadcast** ⇒ 双发。
 *
 * 后果（静默）：后端可能把重复 Hello 当重连（无害），
 * 但重复 DataReport/DataBatch 会被当**两批数据**入库；设备侧两次 send 都返回 ESP_OK，
 * **没有任何错误**。⇒ 属审计 D-01 同一族（"压平/重复"）。
 *
 * ### 因此：**接线时必须同时接 `transport_sel`**（或等价地让上行单选）
 * `transport_sel` 正是为这件事设计的（§68）：**TCP 优先、MQTT 兜底、有连续失败阈值**。
 * 本适配器只负责"把 session 变成一条 transport"，**不负责选路** ——
 * 选路必须由 `transport_sel` 决定，否则双栈稳态必然双发。
 * ⇒ 本文件只交付适配层；**注册与选路由接线那一步一起做**（下一步），
 *   且接线后必须让 `transport_dualstack_tests` 的"双发"用例变成"只发一次"。
 */
#ifndef EHOME_SESSION_TRANSPORT_H
#define EHOME_SESSION_TRANSPORT_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "link.h"      /* link_result_t —— IDF 无关 */
#include "session.h"

#ifdef __cplusplus
extern "C" {
#endif

/* ── 纯判定（宿主可测，无 IDF 依赖）── */

/**
 * 会话状态 → "能否上行"。
 *
 * 只有 `SESSION_READY` 为 true。理由见文件头第 1 条：
 * 其它状态下链路要么没建、要么应用层握手未完成，投给它就是**静默丢弃**。
 */
bool session_transport_ready(session_state_t st);

/**
 * task-21：**组合判据** —— "这一帧能不能投给 3.0 transport"。
 *
 * ```
 * = session_transport_ready(st)   // 语义：链路与应用层是否就绪（未握手=静默丢弃）
 *   && gate_open                  // 策略：仲裁层现在是否选中 TCP（否则双栈稳态双发）
 * ```
 *
 * 为什么做成**独立纯函数**而不是内联在 is_connected 里：
 * IDF 段（`#ifndef SESSION_TRANSPORT_HOST_TEST`）在宿主上编不到，内联就没法被测。
 * 而这一行恰好是"双发"与"静默丢弃"两类故障的交点 ⇒ 必须宿主可测。
 *
 * @param st        当前会话状态
 * @param gate_open 仲裁闸是否放行（未注入闸时传 true，退化为只看 READY）
 */
bool session_transport_connected(session_state_t st, bool gate_open);

/**
 * `session_send` 的结果 → transport 层该返回什么。
 *
 * 返回的 esp_err_t 语义（与 transport.h 的契约一致）：
 *   - ESP_OK           ：**整帧**已写出（progress == len）
 *   - ESP_ERR_TIMEOUT  ：可重试（背压；一字节都没写出）—— 调用方稍后再来
 *   - ESP_ERR_INVALID_STATE ：链路未就绪 / 不可用（重试无意义，交给兜底）
 *   - ESP_ERR_INVALID_SIZE  ：超 MTU（调用方必须分片或拒绝，**重试无用**）
 *   - ESP_FAIL         ：其余（含部分写出后失败 —— 流已被污染，**必须重建链路**）
 *
 * ⚠ 为什么部分写出后失败要单列：那一刻 TCP 流里已经有半帧，
 * 对端重组器会一直等剩下的字节。此时**重试整帧是错的**（会写出重复前缀），
 * 正确处置是**让上层重建链路**（新连接 = 干净流）。压成 ESP_OK 或 ESP_ERR_TIMEOUT
 * 都会把"流已污染"伪装成"稍后重试"。
 */
typedef enum {
    STX_SEND_DONE = 0,      /* 整帧写出 */
    STX_SEND_RETRY,         /* 可重试（背压）*/
    STX_SEND_NOT_READY,     /* 链路未就绪/不可用 */
    STX_SEND_TOO_BIG,       /* 超 MTU */
    STX_SEND_STREAM_DIRTY,  /* 部分写出后失败：流已污染，需重建 */
} stx_send_class_t;

const char *stx_send_class_name(stx_send_class_t c);

/** 把一次 `session_send` 的结果分类。`progress` 是本次调用后的累计写出字节数。 */
stx_send_class_t stx_classify_send(link_result_t r, size_t progress, size_t len);

/** 分类 → transport 层返回码。**单一来源**：不要在别处再写一遍映射。 */
int stx_to_esp_err(stx_send_class_t c);

/* ── 胶水（依赖 transport.h，而它含 IDF 头 ⇒ 宿主构建时用不到）── */

#ifndef SESSION_TRANSPORT_HOST_TEST

#include "transport.h"

/**
 * 创建并注册一个把 `session` 包成 transport 的实例。
 *
 * @param s 已创建的会话（本函数**不**拥有它的生命周期；调用方负责 destroy）
 * @return transport 句柄；失败返回 NULL（不注册半成品）
 *
 * ⚠ `is_connected` 会实时查询 `session_state(s)`，所以注册**不需要**等 READY ——
 * 未 READY 时它只是"未连接"，broadcast 会跳过它，MQTT 兜底照常工作。
 */
transport_t *session_transport_create(session_t *s);

/** 注销并释放 transport 包装（**不**销毁 `s`）。幂等。 */
void session_transport_destroy(transport_t *t);

/* === task-21：上行仲裁闸（**唯一**一处把"能不能上行"与"该不该走 TCP"合起来）===
 *
 * 为什么需要闸：`transport_broadcast` 对**每个** is_connected() 为真的 transport
 * 都发。双栈稳态下 3.0 与 MQTT 同时为真 ⇒ **同一帧发两次**。
 * 仲裁层（main/uplink_arbiter.h）让两条门**互斥**，于是不需要动注册表。
 *
 * 为什么闸**不**写在这里的 is_connected 里而是做成注入的钩子：
 * `session_transport.c` 是"纯判定 + 薄胶水"，让它 include uplink_arbiter 会引入
 * IDF 依赖，破坏 `SESSION_TRANSPORT_HOST_TEST` 的可测性。
 * ⇒ 由 main/ 在启动时注入；未注入时**保持今天的行为**（只看 READY）。
 *
 * ⚠ 为什么"未注入 = 只看 READY"是安全的：默认构建根本不创建 3.0 transport，
 * 所以这个分支只在"启用了链路但忘了注入闸"时生效 —— 那种情况等价于本卡之前
 * 的行为（有双发风险），**绝不是**"静默把所有上行关掉"。
 * 后者会让设备变哑，前者只是回到已知状态。
 */
typedef bool (*session_transport_gate_fn)(void);

void session_transport_set_gate(session_transport_gate_fn gate);

#endif /* !SESSION_TRANSPORT_HOST_TEST */

#ifdef __cplusplus
}
#endif
#endif /* EHOME_SESSION_TRANSPORT_H */
