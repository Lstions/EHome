/**
 * @file uplink_arbiter.h
 * @brief 上行**仲裁层**：让"3.0 TCP"与"MQTT 兜底"在任一时刻**恰好一条**被投递。
 *
 * ## 为什么需要它（task-21 的核心）
 *
 * `transport_broadcast` 对**每一个** `is_connected()` 为真的 transport 都发。双栈稳态
 * （MQTT connected + 3.0 READY）下 ⇒ **同一帧投递两次**。已实证：
 * `host_tests/transport_dualstack_tests.c` 用真实 transport.c 数出
 * `mqtt.send_calls==1 && tcp3.send_calls==1`。
 *
 * ## 解法：**让两条门互斥**（而不是把谁移出注册表）
 *
 * 关键洞察：只要两个 transport 的 `is_connected` 互斥，就**不需要动注册表** ——
 * 双发、并发、切换歧义窗口**三个问题同时消失**。
 *
 * ### ⚠ 为什么**不**用"随 tsel 把 MQTT 移出/移回注册表"（本轮已否决）
 * 该做法（Lead 一度批准、实测后作废）有两个硬缺陷：
 *  1. `mqtt_transport_unregister()` 只 `transport_unregister()+free()`，**不还原**它注册时
 *     装进 MQTT 回调槽的 `mqtt_adapter_msg_cb`/`mqtt_adapter_state_cb`
 *     （`mqtt_transport_adapter.c:150-151` 装、`:166-175` 只 free），而 `main.c:553/557`
 *     随后用 app 回调覆盖同一槽。re-register 会把 adapter 回调**再装回去**并覆盖 app 回调，
 *     而 adapter 的 `transport->msg_cb` **恒为 NULL** ⇒ 转发是空操作
 *     ⇒ **MQTT 下行与状态回调被静默切断**（正是本卡要防的那一族）。
 *  2. `transport_unregister`（`transport.c:62-70`）与 `transport_broadcast_ex`（`:99/:105`）
 *     **都无锁**地访问同一数组 ⇒ 移出瞬间正在广播 = 读已释放内存。
 *    要修就得改 `components/transport/` 加锁 —— 那越出 D1 边界。
 *
 * ## 两条门（唯一真值来源）
 *
 * ```
 * 3.0 门 = link_enabled && tsel==TCP && session_state==READY
 * MQTT 门 = mqtt_connected && ( !link_enabled || tsel==MQTT || session!=READY )
 * ```
 *
 * | 条件 | 3.0 门 | MQTT 门 | 被投递 |
 * |---|---|---|---|
 * | tsel=TCP 且 3.0 READY | ✅ | ❌ | 只有 3.0 |
 * | tsel=TCP 且 3.0 未 READY（含**重连退避中**） | ❌ | ✅ | 只有 MQTT |
 * | tsel=MQTT（TCP 连续失败≥阈值） | ❌ | ✅ | 只有 MQTT |
 * | 链路未启用（默认构建） | ❌ | = mqtt_connected | **与今天逐位相同** |
 *
 * ⇒ **任一时刻恰好一条**（只要至少一条链路可用），**永不双发**。
 *
 * 为什么 MQTT 门要有 `|| session!=READY` 这一项（而不是只写 `tsel==MQTT`）：
 * 否则"tsel=TCP 但 3.0 尚未 READY"（开机首连、或**重连退避期**）会出现
 * **两条都不可投递** ⇒ 上行掉进 `msg_handler.c` 的 `mqtt_client_publish_impl` 直发兜底。
 * 那条兜底能救，但**不应作为设计的一部分**（Lead 硬约束 2）。加上这一项后该窗口不存在。
 *
 * ## 阈值来源（未标定）
 *
 * `tcp_fail_threshold=3` / `tcp_recover_success=1` **不是实测标定值**，是
 * `components/transport_sel/include/transport_sel.h:66-78` 的设计论证：
 *   · 3：单次失败多为瞬时抖动；连续 3 次配上 link 层退避至少跨过一个退避周期；
 *     取 1 会横跳，取 >5 会让"TCP 未就绪"期间长时间无上行。
 *   · 1：TCP 一旦真正握手成功（READY）就没必要再观望，立即切回最快回到目标态。
 * ⚠ **无现场数据支撑**（无丢包率/切换频次实测）。标定需实机抖动场景，不在本卡范围。
 */
#ifndef EHOME_UPLINK_ARBITER_H
#define EHOME_UPLINK_ARBITER_H

#include <stdbool.h>

#ifdef __cplusplus
extern "C" {
#endif

/* ══════════════════ 纯判定（宿主与固件都编，无 IDF 依赖）══════════════════ */

/** 一次仲裁决策所需的**全部事实**。
 *
 * ⚠ 做成一个结构体（而不是让门各自去查）是为了让"两条门看到同一组事实"
 * 成为**可测性质**：`uplink_gate_*` 是纯函数，同一份 facts 必得互补答案。 */
typedef struct {
    bool link_enabled;      /* CONFIG_EHOME_DEVICE_LINK_ENABLED */
    bool tsel_is_tcp;       /* transport_sel 当前选择 == TSEL_TCP */
    bool three_zero_ready;  /* session_state(sess) == SESSION_READY */
    bool mqtt_connected;    /* mqtt_client_is_connected_impl() */
} uplink_facts_t;

/** 3.0 TCP 是否可承载普通上行（= 3.0 transport 的 is_connected）。 */
bool uplink_gate_tcp3(const uplink_facts_t *f);

/** MQTT 是否可承载普通上行（= MQTT transport 的 is_connected）。 */
bool uplink_gate_mqtt(const uplink_facts_t *f);

/**
 * 不变量：**永不两条都真**（留一条给测试断言）。
 * 返回 true 表示这两个门同时为真 —— 那意味着双发，必须为 false。
 */
bool uplink_gates_overlap(const uplink_facts_t *f);

/* ── 胶水（依赖 IDF 头 ⇒ 宿主构建时用不到）── */

#ifndef UPLINK_ARBITER_HOST_TEST

#include "esp_err.h"

/** 初始化仲裁层。幂等。link_enabled 应传「链路是否编译启用」。 */
void uplink_arbiter_init(bool link_enabled);

/** 推进一次仲裁（读 tsel + 边沿）。由持有 session 的任务周期调用。 */
void uplink_arbiter_poll(void);

/** 3.0 侧门（供 session_transport.c 注入为闸）。 */
bool uplink_arbiter_tcp3_connected(void);

/** MQTT 侧门（供 uplink_mqtt_transport.c 的 is_connected 调用）。 */
bool uplink_arbiter_mqtt_connected(void);

/**
 * 注册**门控版** MQTT 上行出口。链路启用时用它替代 mqtt_transport_register()。
 *
 * ⚠ 它**只注册一个 transport 对象**，不触碰任何 mqtt_client_register_* 槽
 * （理由见 uplink_mqtt_transport.c 文件头）。
 */
esp_err_t uplink_mqtt_transport_register(void);

/* uplink_mqtt_transport_register() 的实现放在 uplink_arbiter.c 的 IDF 段：
 * 它需要 ehome_mqtt.h（含 mqtt_client.h / freertos/semphr.h），
 * 而那些头在宿主机上没有 ⇒ 真实绑定必须留在 IDF 段，
 * 纯逻辑则留在 uplink_mqtt_transport.c（宿主可测）。 */

#endif /* !UPLINK_ARBITER_HOST_TEST */

#ifdef __cplusplus
}
#endif
#endif /* EHOME_UPLINK_ARBITER_H */
