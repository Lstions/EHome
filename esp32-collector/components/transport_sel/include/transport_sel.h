/**
 * @file transport_sel.h
 * @brief 传输选择策略：**TCP 优先，MQTT 兜底**（设计 §7.3 的 P2 阶段）
 *
 * ## 为什么需要它（不做的话现场设备会变砖）
 * 设计 §7.3 把升级顺序**物理锁死**了：
 *
 *     P0 后端同时听 MQTT + TCP（双栈）
 *     P1 通过 **MQTT 下行 OTA** 把 3.0 固件推下去（设备只懂它当前的通道）
 *     P2 3.0 固件 **TCP 优先、MQTT 兜底**  ← 本模块实现的就是这条
 *     P3 关掉固件里的 MQTT 回退分支（下一版固件）
 *     P4 后端关闭 MQTT 监听
 *
 * ⇒ 在 P2 期间，3.0 固件**必须**保留 MQTT 兜底：
 *   - 后端 TCP 尚未就绪 / 证书还没铺开 / 防火墙没放行 8443
 *     —— 这些都不该让一台已在现场的设备失去唯一的上行通道；
 *   - 而"出了故障就永久只走 MQTT"同样是错的（那 P3 就永远关不掉 MQTT）。
 *   ⇒ 需要一条**有明确回切条件**的策略，而不是"哪个先连上就用哪个"。
 *
 * ## 核心规则
 * **TCP 是主通道；MQTT 只在 TCP 反复失败时临时顶替；一旦 TCP 恢复立即切回。**
 *
 * 为什么"立即切回"而不是"跑够多久再切回"：
 *   - TCP 一旦可用就是设计目标态（一条长连接承载双向流量）；
 *   - 留在 MQTT 上会继续吃 MQTT 单事件 2011B 的历史限制（R1）；
 *   - 切回的成本很低（建连 + Hello），而留在旧通道的成本是"功能受限"。
 *
 * 但**不能一失败就切**：TCP 刚出现瞬时抖动就切到 MQTT，
 * 会造成两条通道反复横跳（每次切都断一次连接）。⇒ 需要**连续失败阈值**。
 *
 * ## 本模块只管"策略"，不管"怎么连"
 * 真正的 TCP 会话在 components/session；MQTT 路径是既有的 ehome_mqtt。
 * 本模块是**纯函数 + 小状态机**，可在宿主机穷举测试（约束 C2）。
 */
#ifndef EHOME_TRANSPORT_SEL_H
#define EHOME_TRANSPORT_SEL_H

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/** 选中的传输。 */
typedef enum {
    TSEL_TCP = 0,        /* 主通道：TCP + mTLS（目标态） */
    TSEL_MQTT,           /* 兜底通道（过渡期能力，P3 之后移除） */
} tsel_which_t;

const char *tsel_which_name(tsel_which_t w);

/** 决策理由 —— 每个取值对应调用方一个明确处置（P1），也便于日志归因。 */
typedef enum {
    TSEL_REASON_TCP_HEALTHY = 0,   /* TCP 就绪（或尚未失败到阈值）*/
    TSEL_REASON_TCP_FAILED_N,      /* TCP 连续失败达阈值 -> 切兜底 */
    TSEL_REASON_STAY_MQTT,         /* 已在兜底，TCP 仍未恢复 */
    TSEL_REASON_BACK_TO_TCP,       /* TCP 恢复 -> 立即切回主通道 */
    TSEL_REASON_NO_FALLBACK,       /* TCP 失败但兜底被禁用 -> 只能继续试 TCP */
} tsel_reason_t;

const char *tsel_reason_name(tsel_reason_t r);

/** 策略配置。 */
typedef struct {
    /** TCP 连续失败多少次才切到 MQTT 兜底。
     *  取 3 的理由：单次失败多为瞬时抖动；连续 3 次已能说明
     *  "不是抖动"（配合 link 层的退避，这至少跨过一次退避周期）。
     *  太小（1）会横跳；太大（>5）会让"后端 TCP 没就绪"时长时间无上行。 */
    uint32_t tcp_fail_threshold;

    /** 是否允许 MQTT 兜底。P3 之后应设为 false（那时 MQTT 分支要被拆掉）。 */
    bool allow_mqtt_fallback;

    /** TCP 恢复判据：连续成功多少次才算"恢复"。
     *  取 1 的理由：TCP 一旦真正建连+握手成功（session 的 READY），
     *  就没必要再观望 —— 立即切回能最快回到目标态。 */
    uint32_t tcp_recover_success;
} tsel_config_t;

/** 运行期输入的"当前事实"（由调用方从各模块取，本模块不主动查）。 */
typedef struct {
    bool tcp_ready;      /* session 是否 READY（握手完成）*/
    bool tcp_failed;     /* 本轮 TCP 尝试是否失败 */
    bool tcp_succeeded;  /* 本轮 TCP 是否达成 READY（用于计恢复）*/
} tsel_input_t;

typedef struct tsel tsel_t;

/** 创建。返回 NULL 表示参数非法（阈值 0 无意义，拒绝而不是兜底）。 */
tsel_t *tsel_create(const tsel_config_t *cfg);
void    tsel_destroy(tsel_t *s);

/**
 * 推进一次决策。
 * @param in         当前事实
 * @param reason_out 可选：输出决策理由
 * @return 应当使用的传输
 *
 * 确定性行为（由 host_tests/transport_sel_tests.c 逐条锁定）：
 *  - 初始态 TCP；未达阈值前**不切**（避免抖动横跳）；
 *  - 连续失败达阈值 -> 切 MQTT（若允许兜底）；
 *  - 兜底期间 TCP 一旦 READY -> **立即切回 TCP**，并把失败计数归零；
 *  - 不允许兜底时：永不返回 MQTT（NO_FALLBACK），调用方应继续重试 TCP；
 *  - 切回 TCP 后失败计数归零（下一次要重新攒够阈值才会再切）。
 */
tsel_which_t tsel_poll(tsel_t *s, const tsel_input_t *in, tsel_reason_t *reason_out);

tsel_which_t tsel_current(const tsel_t *s);

/** 诊断计数（P3：每条路径都要可见，否则"为什么在兜底上"无法回答）。 */
typedef struct {
    uint32_t tcp_ok;            /* 判定 TCP 健康的次数 */
    uint32_t switched_to_mqtt;  /* 切到兜底的次数 */
    uint32_t switched_back;     /* 切回 TCP 的次数 */
    uint32_t no_fallback_hits;  /* 想切但兜底被禁用的次数 */
    uint32_t consecutive_fail;  /* 当前连续失败计数（快照）*/
} tsel_stats_t;

void tsel_get_stats(const tsel_t *s, tsel_stats_t *out);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_TRANSPORT_SEL_H */
