/**
 * @file uplink_arbiter.c
 * @brief 上行仲裁的**纯判定** + IDF 胶水（tsel 推进 / 门查询）。
 *
 * 设计理由、真值表、以及"为什么不用移出注册表"见 uplink_arbiter.h 的文件头。
 *
 * 本文件分两段：
 *   1. **纯判定**（`uplink_gate_*` / `uplink_gates_overlap`）—— 宿主可编可测，无 IDF 头；
 *   2. **IDF 胶水**（tsel 生命周期 + 从各模块取事实）—— 用 UPLINK_ARBITER_HOST_TEST 隔离。
 */
#include "uplink_arbiter.h"

#include <stddef.h>   /* NULL —— 纯判定段也用，且宿主构建没有 IDF 头 */

/* ══════════════════════ 纯判定（宿主与固件都编）══════════════════════ */

bool uplink_gate_tcp3(const uplink_facts_t *f)
{
    if (f == NULL) return false;
    /* 三个条件缺一不可：
     *   link_enabled     —— 默认构建下根本没有 3.0 链路，门必须关；
     *   three_zero_ready —— 应用层握手完成。**这不是纪律，是语义**：
     *                       WAIT_HANDSHAKE 时投给它 = 静默丢弃；
     *   (tsel_is_tcp || !mqtt_connected)
     *                    —— 仲裁层选择 TCP，**或** MQTT 根本拿不走这一帧。
     *
     * ⭐ 第二项 `|| !mqtt_connected` 是 2026-10-07 由**穷举测试**（2^4=16 组）
     * 抓出来的**活性缺口**修补：
     *
     *   `uplink_get_facts` 读的是 `tsel_current()`，而 `tsel` 只在
     *   `uplink_arbiter_poll`（由链路任务周期调用）里前进 ⇒ **tsel 会滞后于
     *   session 状态**。于是存在这个窗口：
     *       3.0 已 READY（链路确实可用） + tsel 仍停在 MQTT + **MQTT 未连**
     *   ⇒ 旧规则下两条门**都关** ⇒ 帧掉进 msg_handler 的直发兜底 ⇒ MQTT 也没连
     *   ⇒ **静默丢帧**。（这正是本项目最忌讳的形态：丢帧且无任何错误。）
     *
     *   修法为什么"恰好这一项"而不是放宽整个门：
     *   `tsel_is_tcp` 存在的唯一目的是**防止双发**（两条都可用时只能选一条）。
     *   而 `!mqtt_connected` 时 MQTT 根本不可能投递 ⇒ **不存在双发风险**
     *   ⇒ 此时开 3.0 门是纯赚。
     *
     *   互斥性证明（两条门不可能同时为真）：若 MQTT 门为真，则 MQTT 已连 ⇒ M=1；
     *   3.0 门此时要求 `(tsel_is_tcp || !M)` ⇒ 只能靠 `tsel_is_tcp`。
     *   而 MQTT 门为真还要求 `(!link_enabled || tsel==MQTT || !three_zero_ready)`；
     *   在 link_enabled 与 three_zero_ready 都为真的前提下它要求 `tsel==MQTT`，
     *   与 `tsel_is_tcp` 矛盾 ⇒ **不可能同时为真**。16 组穷举已逐组验证。 */
    return f->link_enabled && f->three_zero_ready &&
           (f->tsel_is_tcp || !f->mqtt_connected);
}

bool uplink_gate_mqtt(const uplink_facts_t *f)
{
    if (f == NULL) return false;
    if (!f->mqtt_connected) return false;

    /* 链路未启用（默认构建）⇒ 与今天逐位相同：只看 MQTT 自己连没连。 */
    if (!f->link_enabled) return true;

    /* 链路启用时，MQTT 是**兜底**：
     *   tsel==MQTT       —— TCP 连续失败到阈值，已切兜底；
     *   或 3.0 未 READY  —— ⭐ 这一项是"恰好一条"的关键。
     *
     * 为什么必须有第二项：否则"tsel=TCP 且 3.0 未 READY"（开机首连、**重连退避期**）
     * 会出现两条都不可投递 ⇒ 上行掉进 msg_handler 的直发兜底。那条能救，
     * 但不应作为设计的一部分（Lead 硬约束 2）。
     *
     * 这里**不会**与 3.0 门重叠：MQTT 门为真要求 (tsel==MQTT || !ready)，
     * 而 3.0 门为真要求 (tsel==TCP && ready) —— 二者互斥。见 uplink_gates_overlap()。 */
    return !f->tsel_is_tcp || !f->three_zero_ready;
}

bool uplink_gates_overlap(const uplink_facts_t *f)
{
    return uplink_gate_tcp3(f) && uplink_gate_mqtt(f);
}

/* ══════════════════════ IDF 胶水 ══════════════════════ */
#ifndef UPLINK_ARBITER_HOST_TEST

#include <stddef.h>

#include "esp_log.h"
#include "transport_sel.h"
#include "session.h"
/* ⚠ 必须显式包含：`session_transport_ready` 声明在 session_transport.h，
 * **不在** session.h。少了它只是**隐式声明**（按 int 返回）——
 * 宿主测试不会红（这一段在 `#ifndef UPLINK_ARBITER_HOST_TEST` 内，宿主构建不编它），
 * 而 IDF 构建直接 `error: implicit-function-declaration`。
 * ⇒ 这是"宿主绿、目标红"的典型形态，只有真跑 IDF 构建才会暴露。 */
#include "session_transport.h"
#include "ehome_mqtt.h"
#include "device_link_wiring.h"
#include "uplink_mqtt_transport.h"

static const char *TAG = "UPLINK";

/* ⚠ 阈值**未标定（无现场数据）**：3/1 取自 transport_sel.h:66-78 的设计论证，
 * 不是实测值。标定需要实机 MQTT/TCP 抖动场景（本卡禁刷设备 ⇒ 不在范围）。
 * 取 3：单次失败多为瞬时抖动，连续 3 次配上 link 层退避至少跨过一个退避周期；
 * 取 1：TCP 一旦真正握手成功就没必要再观望，立即切回最快回到目标态。 */
#define UPLINK_TSEL_FAIL_THRESHOLD  3u   /* 未标定 */
#define UPLINK_TSEL_RECOVER_SUCCESS 1u   /* 未标定 */

static tsel_t *s_tsel = NULL;
static bool    s_link_enabled = false;
/* 上一轮看到的 TCP 就绪态 —— 用于推出"本轮失败/本轮成功"边沿。 */
static bool    s_prev_ready = false;
static bool    s_prev_ready_valid = false;

static void uplink_get_facts(uplink_facts_t *f)
{
    f->link_enabled = s_link_enabled;
    f->tsel_is_tcp = (s_tsel == NULL) ? true : (tsel_current(s_tsel) == TSEL_TCP);

    session_t *sess = device_link_wiring_session();
    f->three_zero_ready = (sess != NULL) && session_transport_ready(session_state(sess));
    f->mqtt_connected = mqtt_client_is_connected_impl();
}

bool uplink_arbiter_tcp3_connected(void)
{
    uplink_facts_t f;
    uplink_get_facts(&f);
    return uplink_gate_tcp3(&f);
}

bool uplink_arbiter_mqtt_connected(void)
{
    uplink_facts_t f;
    uplink_get_facts(&f);
    return uplink_gate_mqtt(&f);
}

void uplink_arbiter_init(bool link_enabled)
{
    s_link_enabled = link_enabled;
    s_prev_ready = false;
    s_prev_ready_valid = false;
    if (s_tsel != NULL) return;   /* 幂等 */

    tsel_config_t cfg;
    cfg.tcp_fail_threshold   = UPLINK_TSEL_FAIL_THRESHOLD;
    cfg.tcp_recover_success  = UPLINK_TSEL_RECOVER_SUCCESS;
    /* §7.3 P2/P3 的开关，来自 Kconfig（P5：单一来源、构建期可见）。
     * y = P2 双栈期（保留 MQTT 兜底）；n = P3 迁移目标态（永不返回 MQTT）。
     * ⚠ 这里**不写死 true** —— 写死会让"当前处于哪一阶段"只能靠读注释判断，
     *   而注释不是判据（本卡 §160.3 刚吃过一次"注释预言了分叉却没人看"的亏）。 */
#if defined(CONFIG_EHOME_DEVICE_LINK_MQTT_FALLBACK) && (CONFIG_EHOME_DEVICE_LINK_MQTT_FALLBACK == 1)
    cfg.allow_mqtt_fallback  = true;   /* §7.3 P2：保留兜底 */
#else
    cfg.allow_mqtt_fallback  = false;  /* §7.3 P3：TCP 是唯一上行 */
#endif
    s_tsel = tsel_create(&cfg);
    if (s_tsel == NULL) {
        /* 不静默：没有 tsel 就不能保证单发，必须看得见。 */
        ESP_LOGE(TAG, "tsel_create 失败 ⇒ 仲裁层不可用（上行将回退到 broadcast 双发风险）");
        return;
    }
    /* ⚠ 必须报出**当前处于 P2 还是 P3**：这个开关决定"TCP 挂了设备还有没有上行"，
     * 是现场排障第一个要看的量。只报阈值而不报它，等于把最关键的语义留给读代码的人。 */
    ESP_LOGI(TAG, "上行仲裁已接线：link_enabled=%d 阈值=%u/%u MQTT兜底=%s（%s；**阈值未标定**，见 transport_sel.h:66-78）",
             (int)link_enabled, (unsigned)UPLINK_TSEL_FAIL_THRESHOLD,
             (unsigned)UPLINK_TSEL_RECOVER_SUCCESS,
             cfg.allow_mqtt_fallback ? "允许" : "**禁止**",
#if defined(CONFIG_EHOME_DEVICE_LINK_MQTT_FALLBACK) && (CONFIG_EHOME_DEVICE_LINK_MQTT_FALLBACK == 1)
             "§7.3 P2 双栈期"
#else
             "§7.3 P3 迁移目标态"
#endif
    );
}

/** 推进一次仲裁。由持有 session 的任务周期调用（见 device_link_wiring 链路任务）。
 *
 * 输入边沿的构造：
 *   tcp_ready     = 当前 session 是否 READY（tsel 的主要判据）
 *   tcp_succeeded = "本轮刚从非 READY 变成 READY"（上升沿）
 *   tcp_failed    = "本轮刚从 READY 变成非 READY"（下降沿）
 *
 * ⚠ 为什么用**边沿**而不是电平：tsel 的契约是"连续失败达阈值才切"。若把"未 READY"
 * 当电平反复喂进去，一个长时间的重连期会被算成**很多次**失败，阈值语义失真。 */
void uplink_arbiter_poll(void)
{
    if (s_tsel == NULL) return;

    uplink_facts_t f;
    uplink_get_facts(&f);

    bool failed = false, succeeded = false;
    if (s_prev_ready_valid) {
        if (s_prev_ready && !f.three_zero_ready) failed = true;
        if (!s_prev_ready && f.three_zero_ready) succeeded = true;
    }
    s_prev_ready = f.three_zero_ready;
    s_prev_ready_valid = true;

    tsel_input_t in;
    in.tcp_ready     = f.three_zero_ready;
    in.tcp_failed    = failed;
    in.tcp_succeeded = succeeded;

    tsel_reason_t reason = TSEL_REASON_TCP_HEALTHY;
    tsel_which_t w = tsel_poll(s_tsel, &in, &reason);

    /* 只在**切换**时打日志：稳态每帧一条会淹掉串口（P3：变化才响）。 */
    static tsel_which_t s_last = TSEL_TCP;
    if (w != s_last) {
        ESP_LOGW(TAG, "上行切换 %s -> %s（理由 %s）",
                 tsel_which_name(s_last), tsel_which_name(w), tsel_reason_name(reason));
        s_last = w;
    }
}

/* ── 真实 IO 绑定：把纯模块（uplink_mqtt_transport.c）接到 ehome_mqtt 与仲裁层 ── */

/** mqtt_client_publish_ex 的返回值 -> esp_err_t。
 *
 * 映射与 mqtt_transport_adapter.c:60-67 **逐条一致**（保持 sent_count / rep 语义）：
 *   MQTT_PUBLISH_OK -> ESP_OK；NOT_CONNECTED / FAILED / BACKPRESSURE -> ESP_FAIL。
 * ⚠ BACKPRESSURE 也映为 ESP_FAIL：适配层当年也把它并入 default；
 *   transport_ops.send 的返回类型没有背压档（见 adapter.c:48-59 的理由），
 *   若要区分需先扩展接口 —— 不在本卡范围。 */
static esp_err_t uplink_mqtt_publish_adapter(const uint8_t *data, size_t len)
{
    return (mqtt_client_publish_ex(data, len) == MQTT_PUBLISH_OK) ? ESP_OK : ESP_FAIL;
}

/** MQTT 客户端已连 **且** 仲裁层门开。 */
static bool uplink_mqtt_connected_adapter(void)
{
    if (!mqtt_client_is_connected_impl()) return false;
    return uplink_arbiter_mqtt_connected();
}

esp_err_t uplink_mqtt_transport_register(void)
{
    uplink_mqtt_io_t io;
    io.publish      = uplink_mqtt_publish_adapter;
    io.is_connected = uplink_mqtt_connected_adapter;

    transport_t *t = uplink_mqtt_transport_ops(&io);
    if (t == NULL) {
        ESP_LOGE(TAG, "uplink_mqtt_transport_ops 返回 NULL");
        return ESP_ERR_INVALID_ARG;
    }
    esp_err_t ret = transport_register(t);
    if (ret != ESP_OK) {
        ESP_LOGE(TAG, "transport_register 失败: %d", (int)ret);
        return ret;
    }
    /* 刻意强调：本注册**不触碰**任何 mqtt_client_register_* 槽 ——
     * 这正是"不重蹈 unregister/re-register 覆盖 app 回调"的关键。 */
    ESP_LOGI(TAG, "MQTT 上行出口已注册（门控版；未触碰任何回调槽）");
    return ESP_OK;
}

#endif /* !UPLINK_ARBITER_HOST_TEST */
