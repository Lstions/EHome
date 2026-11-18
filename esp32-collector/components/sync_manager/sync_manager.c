/**
 * @file sync_manager.c
 * @brief Sync Manager v2.1 Implementation
 *
 * 7-reason decision model + periodic sync task.
 * Integrates with config_mgr for NVS epoch/manifest persistence.
 */

#include "sync_manager.h"

/* ⚠ 2026-10-08：本文件用了 strncpy（:100/:240）却**没有**包含 <string.h>。
 * 它之所以能在 IDF 下编过，只是因为别处**恰好**间接带进了这个声明 ——
 * 而"恰好"不是保证：把它编进宿主目标时立刻报
 *   "implicit declaration of function 'strncpy' [-Werror=implicit-function-declaration]"
 * ⇒ 补上。这类"靠传递包含活着"的用法会在改动包含图时静默炸掉。 */
#include <string.h>

#include "config_mgr.h"
/* 消息号（MSG_CONFIG_MFST / MSG_CONFIG_RSLT / MSG_HELLO_ACK）的**唯一来源**是
 * frame_codec.h。本模块原先在 switch 里写裸 0x04/0x05/0x12 —— 于是 "这个分支
 * 在等哪条消息" 只能靠记忆，而编号一旦改动这里会静默错配。
 * ⚠ 不能改成包含 msg_handler.h：msg_handler 已经 REQUIRES 本模块，
 *   反向依赖会成环（component_cycles 门禁盯着）。frame 是无依赖的纯协议
 *   头，正好是这一层该依赖的东西。 */
#include "frame_codec.h"
#include "esp_log.h"
#include "esp_timer.h"
#include "rgb_led.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#define TAG "SYNC"

/* === Config defaults (overridable via Kconfig) === */
#ifndef CONFIG_COLLECTOR_SYNC_PERIODIC_SEC
#define CONFIG_COLLECTOR_SYNC_PERIODIC_SEC 600
#endif

#ifndef CONFIG_COLLECTOR_SYNC_DEDUP_SEC
#define CONFIG_COLLECTOR_SYNC_DEDUP_SEC 30
#endif

/* === 整轮同步看门狗（task-5：缺陷 A + 缺陷 B）===
 *
 * 【为什么必须覆盖**整轮**，而不是只覆盖 "HelloAck 之后"】
 * 旧实现把 120 s 表的起表点放在 handle_hello_success()，即 "HelloAck 已被
 * 接受" 之后。于是 "这一轮有没有人看着" 这件事本身没有兜底：
 *   · HelloAck 丢在链路上           ⇒ 回调进不去，没人起表；
 *   · HelloAck 被 stale-nonce 拒绝  ⇒ handler_hello.c 提前 return，同样没人起表；
 *   · 上行未就绪、Hello 根本没发出   ⇒ request_sync 里也没有起表点。
 * 三种情形下 s_sync_enum 都停在 SYNCING，而**没有任何超时/重试被 arm**。
 * 现场后果：节点 sync_state 钉在 1 达 11 h，期间只有 1 次 Hello，
 * 后端只能靠周期性重推兜底 —— 设备**放弃了自己恢复**。
 * ⇒ 起表点必须挪到 "这一轮**开始**的时刻"（sync_manager_request_sync()），
 *   而不是 "某一个中间步骤成功的时刻"。判据要覆盖整件事，不是半件事。
 *
 * 【为什么超时回调必须**无条件**复位】
 * 旧回调在 config_mgr_has_manifest() 为真时只点灯、**不复位状态**；而稳态设备
 * （NVS 里早就有 manifest）恰恰是常态 ⇒ 那条 else 在现场等于 "超时什么也不做"，
 * 把缺陷 A 永久藏在后面。配置的有无说明 "设备能不能干活"，
 * **不能**说明 "这一轮同步成功了没有" —— 用前者去决定后者，就是本次缺陷。
 * ⇒ 复位与重试只看 "这一轮是否还在 SYNCING"；has_manifest 只用于 LED 与
 *   "无配置激进重试" 的判断。
 *
 * 【为什么重试要退避且有上界】
 * 坏链路下按整轮预算无脑重发，一小时几十条 Hello，对窄带上行是负担；而干脆
 * 不重试就退回 "靠后端重推" 的退化状态。⇒ 指数退避 30→60→120→240→300 s（封顶），
 * 一轮成功即归零。上界是必需的：没有上界的退避等价于 "慢慢把同步停掉"。
 */
#define SYNC_ROUND_TIMEOUT_SEC       120  /* 整轮预算：Hello→HelloAck→ConfigManifest→ConfigResult */
#define SYNC_RETRY_BACKOFF_MIN_SEC    30  /* 首次退避 */
#define SYNC_RETRY_BACKOFF_MAX_SEC   300  /* 退避上界：必须存在，否则等于 "慢慢停用" */

/* 看门狗此刻在等什么。**一个**定时器承载两件事，靠它区分：
 *   OFF   —— 没有待办（既不等整轮，也不等退避）；
 *   ROUND —— 在等某一轮走完；
 *   RETRY —— 在等退避到期，到点后重开一轮。
 * 为什么必须显式区分：同一个回调里 "整轮超时" 与 "退避到期" 要做**相反**的事
 * （前者判失败，后者发起新请求）。拿 arg 或时刻去推断都不可靠。 */
typedef enum {
    SYNC_TIMER_OFF = 0,
    SYNC_TIMER_ROUND,
    SYNC_TIMER_RETRY,
} sync_timer_mode_t;

static esp_timer_handle_t s_round_timer = NULL;
static sync_timer_mode_t s_timer_mode = SYNC_TIMER_OFF;
static uint32_t s_retry_backoff_sec = SYNC_RETRY_BACKOFF_MIN_SEC;

/* === Internal state === */
static sync_state_t s_state = {0};
static sync_state_enum_t s_sync_enum = SYNC_STATE_IDLE;
static bool s_initialized = false;
static sync_send_hello_cb_t s_send_hello_cb = NULL;
/* 见 sync_manager.h 的说明：由 main 侧接到上行仲裁（P4：一处定义）。 */
static sync_uplink_available_cb_t s_uplink_available_cb = NULL;

/* === Forward declarations === */
static bool should_request_sync(sync_reason_t reason);
static uint32_t get_time_sec(void);
static void update_has_active_config(void);
static void sync_round_timer_callback(void *arg);
static void timer_arm(uint32_t delay_sec, sync_timer_mode_t mode);
static void arm_round_watchdog(void);
static void schedule_retry(const char *why);
static void cancel_round_watchdog(void);
static void note_round_succeeded(void);

/* === Public API === */

static bool ensure_round_timer(void)
{
    if (s_round_timer != NULL) return true;

    const esp_timer_create_args_t args = {
        .callback = sync_round_timer_callback,
        .name = "sync_round",
    };
    esp_err_t err = esp_timer_create(&args, &s_round_timer);
    if (err != ESP_OK) {
        /* 不假装成功：没有看门狗时本模块只剩周期任务兜底（10 min），
         * 而 "状态被钉住" 正是要消除的退化状态 ⇒ 必须留下响亮记录。 */
        s_round_timer = NULL;
        ESP_LOGE(TAG, "整轮看门狗创建失败：%s（只剩周期任务兜底）",
                 esp_err_to_name(err));
        return false;
    }
    return true;
}

/* 起/换表。**任何**时刻最多一个待办动作：先停旧的，再按新语义起新的。 */
static void timer_arm(uint32_t delay_sec, sync_timer_mode_t mode)
{
    if (!ensure_round_timer()) return;

    esp_timer_stop(s_round_timer);
    s_timer_mode = mode;
    esp_err_t err = esp_timer_start_once(s_round_timer,
                                         (uint64_t)delay_sec * 1000000ULL);
    if (err != ESP_OK) {
        s_timer_mode = SYNC_TIMER_OFF;
        ESP_LOGE(TAG, "看门狗启动失败：%s", esp_err_to_name(err));
    }
}

/* 起 "整轮" 看门狗。
 *
 * ⚠ 只在**没有在途轮次**时起（已有 ROUND 就保持原截止时间）：
 *   如果后续每一次 request_sync 都重起表，表就永远走不到期 —— 那正是本缺陷
 *   "无限续命" 的另一种写法。退避重试是**另一条**路径（schedule_retry），
 *   只有它到点后才会重开一轮并重新起表。 */
static void arm_round_watchdog(void)
{
    if (s_timer_mode == SYNC_TIMER_ROUND) {
        ESP_LOGD(TAG, "整轮看门狗已在途，保持原截止时间");
        return;
    }
    timer_arm(SYNC_ROUND_TIMEOUT_SEC, SYNC_TIMER_ROUND);
    ESP_LOGI(TAG, "整轮看门狗已启动：%d s（Hello→HelloAck→ConfigManifest→ConfigResult）",
             SYNC_ROUND_TIMEOUT_SEC);
}

/* 按**有界**退避安排一次重试。 */
static void schedule_retry(const char *why)
{
    const uint32_t delay = s_retry_backoff_sec;
    const uint32_t next = (delay >= SYNC_RETRY_BACKOFF_MAX_SEC)
                        ? SYNC_RETRY_BACKOFF_MAX_SEC : delay * 2U;

    timer_arm(delay, SYNC_TIMER_RETRY);
    ESP_LOGW(TAG, "本轮未成功（%s）：%u s 后重试（下次退避 %u s，上限 %d s）",
             why, (unsigned)delay, (unsigned)next, SYNC_RETRY_BACKOFF_MAX_SEC);
    s_retry_backoff_sec = next;
}

static void cancel_round_watchdog(void)
{
    if (s_round_timer != NULL) {
        esp_timer_stop(s_round_timer);
    }
    s_timer_mode = SYNC_TIMER_OFF;
}

/* 一轮成功：退避归零。为什么在 "成功" 而不是在 "超时" 里归零 ——
 * 退避描述的是 "连续失败持续了多久"，链路一恢复就该立刻回到最小间隔，
 * 而不是继续慢慢等。 */
static void note_round_succeeded(void)
{
    s_retry_backoff_sec = SYNC_RETRY_BACKOFF_MIN_SEC;
}

static void sync_round_timer_callback(void *arg)
{
    (void)arg;

    if (s_timer_mode == SYNC_TIMER_RETRY) {
        s_timer_mode = SYNC_TIMER_OFF;
        /* 为什么用 FORCED 而不是 DOUBT：退避本身就是节流器；再让 30 s 去重
         * 窗口把这次重试吞掉，"有界退避" 就变成了 "无界拖延"。 */
        ESP_LOGI(TAG, "退避到期，重开一轮同步");
        sync_manager_request_sync(SYNC_REASON_FORCED);
        return;
    }

    s_timer_mode = SYNC_TIMER_OFF;

    const bool nvs_has_manifest = config_mgr_has_manifest();
    const bool still_syncing = (s_sync_enum == SYNC_STATE_SYNCING);

    /* ① 状态复位：**先复位，再决定要不要重试**，而且**不看 has_manifest**。
     *    这里只问一个问题："这一轮成功了吗？" —— 还停在 SYNCING 就是没有。
     *    报 ERROR 而不是 IDLE：sync_state 是后端判定 "配置同步是否健康" 的
     *    唯一输入，报 ERROR 能让它在下一份 StatusReport 里立刻看见异常，
     *    而不是继续显示一个假的 "进行中"。 */
    if (still_syncing) {
        s_sync_enum = SYNC_STATE_ERROR;
    }

    /* ② LED：**这才是** has_manifest 该回答的问题 —— "设备现在能不能干活"。 */
    rgb_led_set_state(nvs_has_manifest ? LED_STATE_RUNNING : LED_STATE_SERVER_OFFLINE);

    ESP_LOGW(TAG, "整轮同步超时（%d s）：state=%d, nvs_has=%d",
             SYNC_ROUND_TIMEOUT_SEC, (int)s_sync_enum, (int)nvs_has_manifest);

    /* ③ 重试：还在 SYNCING（整轮没走完）**或** 没有可用配置（服务端本该推
     *    配置却没推）—— 两种都是 "还有未完成的事"。已有配置且已收到 Ack 时，
     *    本轮没有待办（按 Hello field 6 的语义服务端不会重推），交给周期任务。 */
    if (still_syncing) {
        schedule_retry("整轮超时");
    } else if (!nvs_has_manifest) {
        schedule_retry("等待首个配置");
    }
}

void sync_manager_on_hello_ack_accepted(void)
{
    /* Ack 已被接受（stale-nonce 分支在 handler_hello.c 里就被挡掉了）。
     *
     * ⚠ 这里**不重起**整轮表：表在 request_sync() 起表时就已经覆盖了 "等配置"
     *   这一段。若在这里重起，每一个重复 Ack 都会给本轮**续命** ⇒ 表永远
     *   走不到期，缺陷 A 会以另一种形式回来（"永不超时" 换了个位置）。
     *
     * 有配置时直接收表：按 Hello field 6 的语义，has_manifest=1 的服务端
     * **不会**再推配置 ⇒ 本轮该等的都到齐了。若服务端之后仍要下发新配置，
     * 那是一次**新的**事件（0x04 会自己来），与本轮无关。
     *
     * 没有配置时保持表在途：还在等服务端推 ConfigManifest，这一段必须有界。
     * 表若不在途（Hello 也可能由 3.0 链路自己发起，不经过 request_sync），
     * 就补一个。 */
    note_round_succeeded();

    if (config_mgr_has_manifest()) {
        cancel_round_watchdog();
        return;
    }
    if (s_timer_mode == SYNC_TIMER_OFF) {
        timer_arm(SYNC_ROUND_TIMEOUT_SEC, SYNC_TIMER_ROUND);
    }
}

void sync_manager_cancel_round_timeout(void)
{
    cancel_round_watchdog();
}

void sync_manager_init(void)
{
    if (s_initialized) {
        return;
    }

    ESP_LOGI(TAG, "Initializing sync manager v2.1...");

    /* Load state from config_mgr (NVS) */
    s_state.epoch = config_mgr_get_epoch();

    const char *mid = config_mgr_get_last_known_manifest_id();
    if (mid && mid[0] != '\0') {
        strncpy(s_state.manifest_id, mid, sizeof(s_state.manifest_id) - 1);
        s_state.manifest_id[sizeof(s_state.manifest_id) - 1] = '\0';
    }

    update_has_active_config();

    s_state.last_sync_time_sec = 0;
    s_state.last_sync_id_hash = 0;
    s_sync_enum = SYNC_STATE_IDLE;

    s_initialized = true;

    /* 在这里就把整轮看门狗建好，而不是等第一次 request_sync 懒创建：
     * 退避重试是在**看门狗回调里**发起的，若那时才第一次 esp_timer_create()，
     * 就等于"在定时器任务回调里创建定时器"—— 不是必须冒的风险。
     * 创建失败会在这里响亮记录（ensure_round_timer 内），而不是让同步静默退化。 */
    (void)ensure_round_timer();

    ESP_LOGI(TAG, "Sync manager ready: epoch=%llu, manifest=%s, nvs_has=%d",
             (unsigned long long)s_state.epoch,
             s_state.manifest_id[0] ? s_state.manifest_id : "(none)",
             s_state.has_active_config);

    /* If NVS is empty, immediately request sync */
    if (!s_state.has_active_config) {
        ESP_LOGI(TAG, "No active config, requesting sync");
        sync_manager_request_sync(SYNC_REASON_NO_CONFIG);
    }
}

void sync_manager_register_send_hello_cb(sync_send_hello_cb_t cb)
{
    s_send_hello_cb = cb;
}

void sync_manager_register_uplink_available_cb(sync_uplink_available_cb_t cb)
{
    s_uplink_available_cb = cb;
}

/* "现在有没有可用上行"的**唯一**判定（P4）。
 *
 * ⭐ 2026-10-08（§194）：MQTT 已彻底移除 ⇒ 这条函数的语义就是"3.0 传输是否已连接"，
 *    由注入回调（main.c 的 on_sync_uplink_available → transport_any_connected）提供。
 *
 * ⚠ 历史教训（留着别再犯）：本模块曾**硬编码**判 mqtt_client_is_connected_impl()，
 *    于是在"某条上行不可用但另一条可用"时，三条**主动请求同步**的路径会**永久死掉
 *    且不报错**（只留一条 WARN）。真机实测 §164：t=334 与 t=31493 两次请求都被挡住。
 *    ⇒ 判据要问"**这件事需要什么**"，不要问"某个特定实现是否在线"。 */
static bool uplink_available(void)
{
    if (s_uplink_available_cb != NULL) return s_uplink_available_cb();
    /* 未注入 ⇒ **保守返回 false**（不知道就说没有）：宁可少发一次同步请求，
     * 也不要在没有上行时假装有。⚠ 这与"逐位一致"的旧兜底行为**不同**，
     * 是有意为之 —— 旧兜底依赖的 mqtt_client_* 已经不存在。 */
    return false;
}

void sync_manager_request_sync(sync_reason_t reason)
{
    if (!s_initialized) {
        ESP_LOGW(TAG, "Sync manager not initialized, ignoring request");
        return;
    }

    ESP_LOGI(TAG, "Sync request: reason=%d", reason);

    if (!should_request_sync(reason)) {
        ESP_LOGD(TAG, "Sync suppressed by dedup policy (reason=%d)", reason);
        return;
    }

    /* Check **uplink** availability（不是某个传输的可用性 —— 见 uplink_available() 的说明）。
     * ⚠ 日志措辞改过两次，两次都是因为**措辞比代码活得久**：
     *   ① 原文写死 "MQTT not connected" —— 3.0 就绪时仍这么说，操作员会去查 MQTT；
     *   ② 改成 "MQTT 与 3.0 均未就绪" —— 但 MQTT 已于 2026-10-08 **整体移除**
     *      （§194）⇒ 这句话在**设备日志里点名了一个不存在的传输**，
     *      真机上实测仍会打印（C6 验证时 t=375ms 那行就是这么来的）。
     * ⇒ 现在只说"上行"：**日志不该点名具体实现，正如判据不该**（同一族教训）。 */
    if (!uplink_available()) {
        ESP_LOGW(TAG, "无可用上行（3.0 链路未就绪），暂缓同步请求");
        s_sync_enum = SYNC_STATE_ERROR;
        /* 这一轮**根本没开始**，但也不能就这么把它忘掉：起一个退避闹钟，
         * 上行恢复后自己继续。否则 "上行短暂抖动" 会把恢复完全交给周期任务
         * （最长 10 min）—— 与缺陷 A 的症状同类，只是起点更早。 */
        schedule_retry("无可用上行");
        return;
    }

    /* Update state */
    s_sync_enum = SYNC_STATE_SYNCING;
    s_state.last_sync_time_sec = get_time_sec();

    /* ⭐ task-5：**在这里**起整轮看门狗 —— 而不是等 HelloAck 之后。
     * 起表点必须与 "这一轮开始的时刻" 重合：Hello 发不出去、Ack 丢了、
     * Ack 被拒，三种失败都发生在旧实现起表**之前** ⇒ 旧实现里它们全都
     * 无人看管。现在无论失败发生在哪一腿，都会在整轮预算内被发现。 */
    arm_round_watchdog();

    /* Send Hello with v2.1 fields (includes epoch/nvs_has/manifest_id) */
    ESP_LOGI(TAG, "Triggering sync via Hello (reason=%d, epoch=%llu, has_config=%d)",
             reason, (unsigned long long)s_state.epoch, s_state.has_active_config);

    /* Invoke callback to send Hello */
    if (s_send_hello_cb && uplink_available()) {
        s_send_hello_cb();
    } else {
        ESP_LOGW(TAG, "Cannot send Hello: cb=%p, uplink_available=%d",
                 s_send_hello_cb, (int)uplink_available());
        s_sync_enum = SYNC_STATE_ERROR;
        /* Hello 根本发不出去 ⇒ 本轮不可能完成：收掉整轮表，改排退避重试。
         * 否则表会在 120 s 后再报一次 "超时"，把同一件事说两遍。 */
        schedule_retry("Hello 未发出");
    }
}

/* 本轮同步没有成功（★task-5 缺陷 C）。
 *
 * 两个调用点都是 "这一轮**不会**再完成" 的确定信号，但都和成功走同一条链路
 * 之前就 return 了，于是旧实现里 sync_manager 什么都不知道：
 *   ① handler_hello.c：HelloAck 的 nonce 已过期（stale），被**拒绝**；
 *   ② hello_handshake.c：Hello 重试用尽（HELLO_SM_ACTION_FAILED）。
 * ⇒ 由它们显式通知本模块，立刻复位状态并按退避重试，而不是干等整轮看门狗
 *   的 120 s 到点。（看门狗仍然是最终兜底：这条通知是**加速**，不是唯一依赖。）
 *
 * 为什么不复用 sync_manager_on_downlink_received()：那个入口的语义是
 * "收到了一条下行"，而 stale-nonce 恰恰是 "这条下行**不算数**" ——
 * 混在一起就再也分不清 "服务端接受了" 与 "服务端拒绝了"。 */
void sync_manager_on_round_failed(void)
{
    if (!s_initialized) return;

    /* 只有**本轮确实在途**才需要处理：连接建立时可能收到上一代连接残留的
     * 陈旧 ACK，那时 state 是 IDLE，不该被误判成一次失败。 */
    if (s_sync_enum != SYNC_STATE_SYNCING) {
        ESP_LOGD(TAG, "本轮未成功通知：当前无在途轮次（state=%d），忽略",
                 (int)s_sync_enum);
        return;
    }

    ESP_LOGW(TAG, "本轮同步被判定失败：离开 SYNCING 并按退避重试");
    s_sync_enum = SYNC_STATE_ERROR;
    schedule_retry("本轮被告知失败");
}

void sync_manager_on_downlink_received(uint8_t msg_type)
{
    if (!s_initialized) return;

    switch (msg_type) {
    case MSG_CONFIG_MFST:
        /* ConfigManifest 到了：本轮 "等服务端配置" 的那一腿已经兑现。
         * 收表（task-5 要求 ④）并归零退避；真正的状态推进仍由
         * sync_manager_on_config_applied() 在事务提交后完成。 */
        note_round_succeeded();
        cancel_round_watchdog();
        break;

    case MSG_HELLO_ACK:
        /* Ack 被接受 ⇒ 这一轮的往返在链路上是通的。
         * ⚠ 这里**不**收表：设备若还没有配置，服务端紧接着会推 ConfigManifest，
         *   "有 Ack 但迟迟没有配置" 正是必须被看门狗抓住的情形。
         *   "有配置 ⇒ 本轮完成" 的判定在 sync_manager_on_hello_ack_accepted()。 */
        s_sync_enum = SYNC_STATE_IDLE;
        note_round_succeeded();
        break;

    case MSG_CONFIG_RSLT:
        /* 服务端确认了 ConfigResult（成功）：整轮往返闭合，收表。 */
        s_sync_enum = SYNC_STATE_IDLE;
        note_round_succeeded();
        cancel_round_watchdog();
        break;

    default:
        break;
    }
}

sync_state_t *sync_get_state(void)
{
    return &s_state;
}

sync_state_enum_t sync_manager_get_state_enum(void)
{
    return s_sync_enum;
}

esp_err_t sync_manager_on_config_applied(uint64_t server_epoch, const char *manifest_id)
{
    if (!s_initialized || !manifest_id || !manifest_id[0]) return ESP_ERR_INVALID_STATE;

    esp_err_t persist_err = config_mgr_persist_sync_metadata(server_epoch, manifest_id);
    if (persist_err != ESP_OK) {
        ESP_LOGE(TAG, "Failed to persist sync metadata: %s", esp_err_to_name(persist_err));
        return persist_err;
    }

    ESP_LOGI(TAG, "Config applied: server_epoch=%llu, manifest_id=%s",
             (unsigned long long)server_epoch, manifest_id ? manifest_id : "(null)");

    /* Update local epoch */
    if (server_epoch > 0) {
        s_state.epoch = server_epoch;

    }

    /* Update manifest_id */
    if (manifest_id && manifest_id[0] != '\0') {
        strncpy(s_state.manifest_id, manifest_id, sizeof(s_state.manifest_id) - 1);
        s_state.manifest_id[sizeof(s_state.manifest_id) - 1] = '\0';

    }

    /* Update NVS has config flag */
    update_has_active_config();

    /* Update sync time */
    s_state.last_sync_time_sec = get_time_sec();
    s_sync_enum = SYNC_STATE_IDLE;

    /* 配置真的落地了 = 整轮往返成功（task-5 要求 ④）：
     * 收表并归零退避，别让一个已经完成的轮次在 120 s 后再报一次超时。 */
    note_round_succeeded();
    cancel_round_watchdog();

    ESP_LOGI(TAG, "Sync state updated: epoch=%llu, manifest=%s, nvs_has=%d",
             (unsigned long long)s_state.epoch,
             s_state.manifest_id[0] ? s_state.manifest_id : "(none)",
             s_state.has_active_config);
	return ESP_OK;
}

void sync_manager_periodic_task(void *pvParameters)
{
    (void)pvParameters;

    ESP_LOGI(TAG, "Periodic sync task started (interval=%d sec, dedup=%d sec)",
             CONFIG_COLLECTOR_SYNC_PERIODIC_SEC, CONFIG_COLLECTOR_SYNC_DEDUP_SEC);

    while (1) {
        if (!s_initialized) {
            vTaskDelay(pdMS_TO_TICKS(60 * 1000));
            continue;
        }

        /* Refresh active config state */
        update_has_active_config();

        /* Priority 1: No config — aggressive 30s retry
         *
         * ⚠ 2026-10-18 真机修复（C6 实测）：上面的 update_has_active_config() 与
         * 下面**延时之后**的 request_sync 之间存在一个过期的判定 —— 本分支先按
         * "无配置"选择路径，然后睡 30 s；在这 30 s 里 ConfigManifest 很可能已经
         * 到了（实测 t=2560 应用配置，本分支 t=31779 才醒）。醒来后仍执行旧分支
         * 的 request_sync(NO_CONFIG) ⇒ 置 SYNCING，而**同一行日志**里
         * has_config=1，自相矛盾。
         *
         * 后果不是"多发一次同步"这么轻：状态机被永久钉在 SYNCING（后端实测 60/89
         * 条 StatusReport 是 syncing，从未回到 idle），而 sync_state 是后端判定
         * "配置同步是否健康"的唯一输入。
         *
         * 修法：延时**之后**重新求值一次，而不是沿用延时前的结论。这不是"多查一
         * 次"的防御性冗余 —— 判据必须在**使用它的时刻**成立，而不是在**选择分支
         * 的时刻**成立。 */
        if (!s_state.has_active_config) {
            vTaskDelay(pdMS_TO_TICKS(30 * 1000));
            /* 重新求值：这 30 s 内配置可能已经到达。 */
            update_has_active_config();
            if (s_state.has_active_config) {
                /* 配置已到 ⇒ 本分支的前提消失，回到循环顶部走正常路径。
                 * 不在这里发任何请求：配置刚到，sync_manager_on_config_applied
                 * 已经把状态置为 IDLE。 */
                continue;
            }
            uint32_t now = get_time_sec();
            if (now - s_state.last_sync_time_sec > 25) {  /* >25s since last attempt */
                ESP_LOGI(TAG, "No active config, requesting sync (30s interval)");
                sync_manager_request_sync(SYNC_REASON_NO_CONFIG);
            }
            continue;
        }

        /* Priority 2: Has config — normal 60s periodic sync */
        vTaskDelay(pdMS_TO_TICKS(60 * 1000));
        uint32_t now = get_time_sec();
        if (now - s_state.last_sync_time_sec > CONFIG_COLLECTOR_SYNC_PERIODIC_SEC) {
            ESP_LOGI(TAG, "Periodic check: %lu sec since last sync, requesting",
                     (unsigned long)(now - s_state.last_sync_time_sec));
            sync_manager_request_sync(SYNC_REASON_PERIODIC);
        }
    }
}

/* === Internal === */

static bool should_request_sync(sync_reason_t reason)
{
    sync_state_t *st = sync_get_state();
    uint32_t now = get_time_sec();

    switch (reason) {
    case SYNC_REASON_NO_CONFIG:
    case SYNC_REASON_FORCED:
    case SYNC_REASON_USER_ACTION:
    case SYNC_REASON_EPOCH_LAG:
    case SYNC_REASON_MANIFEST_MISMATCH:
        /* Always allow - these are critical/forced reasons */
        return true;

    case SYNC_REASON_PERIODIC:
        /* Only if enough time has passed since last sync */
        return (now - st->last_sync_time_sec) > CONFIG_COLLECTOR_SYNC_PERIODIC_SEC;

    case SYNC_REASON_DOUBT:
        /* Short dedup window for doubt (30s default) */
        return (now - st->last_sync_time_sec) > CONFIG_COLLECTOR_SYNC_DEDUP_SEC;

    default:
        return false;
    }
}

static uint32_t get_time_sec(void)
{
    return (uint32_t)(esp_timer_get_time() / 1000000LL);
}

static void update_has_active_config(void)
{
    s_state.has_active_config = config_mgr_has_manifest();
}

#ifdef SYNC_MANAGER_HOST_TEST
/* 宿主用例专用（与 hello_handshake_test_reset() 同一手法，生产固件不编这段）。
 *
 * 为什么需要它：本模块的状态是**进程级 static**，而整轮看门狗、退避、注入的
 * 回调都是跨用例留存的状态 ⇒ 没有复位点时，第二个用例只能在第一个用例的残留
 * 上断言，失败信息会指向错误的地方。
 *
 * 刻意**不**碰注入的回调：生产里 main 只注册一次，测试照同一语义使用
 * （sync_manager_uplink_tests 的注释也记了这条）。
 *
 * 为什么把 s_initialized 直接置位：init() 在"无配置"时会自己发起一轮请求，
 * 用例需要的是"已就绪、没有在途轮次"的干净起点，而不是再制造一轮真实请求。 */
void sync_manager_test_reset(void)
{
    if (s_round_timer != NULL) {
        esp_timer_stop(s_round_timer);
    }
    s_timer_mode = SYNC_TIMER_OFF;
    s_retry_backoff_sec = SYNC_RETRY_BACKOFF_MIN_SEC;
    s_state = (sync_state_t){0};
    s_sync_enum = SYNC_STATE_IDLE;
    s_initialized = true;
}
#endif
