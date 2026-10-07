/**
 * @file session_transport.c
 * @brief `session` → `transport_ops_t` 适配。理由与语义见 session_transport.h。
 *
 * 结构：**纯判定**（宿主可编、可测）+ **IDF 胶水**（注册/选路）。
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "session_transport.h"
/* devlink_encode_frame —— **上行成帧的唯一实现**（P4：不在这里写第二个）。
 * device_link_wiring.h 只 include variant.h 与标准头，宿主可编（无循环依赖）；
 * 本文件与它同属 main/，宿主 target 的 include 路径已含 ../main。 */
#include "device_link_wiring.h"

/* ══════════════════════ 纯判定（宿主与固件都编）══════════════════════ */

bool session_transport_ready(session_state_t st)
{
    /* 只有握手完成才算"可承载普通上行"。
     * 为什么 WAIT_HANDSHAKE 不算：那时链路虽通，但应用层未就绪（配置/能力未知），
     * 投给它等于**静默丢弃** —— 而 transport_broadcast 只在 is_connected() 为真时发送，
     * 所以这里回答错，症状是"帧消失且无错误"。
     * ⚠ 这不影响 Hello：Hello 由链路任务直接 session_send，不经过本判据（见头文件）。 */
    return st == SESSION_READY;
}

bool session_transport_connected(session_state_t st, bool gate_open)
{
    /* 唯一一处组合判据（P4）。顺序有意如此：先判**语义**（能不能发），
     * 再判**策略**（该不该走 TCP）。反过来会在未握手时走进策略分支，
     * 让"静默丢弃"看起来像"策略没选中"。 */
    return session_transport_ready(st) && gate_open;
}

const char *stx_send_class_name(stx_send_class_t c)
{
    switch (c) {
    case STX_SEND_DONE:         return "DONE";
    case STX_SEND_RETRY:        return "RETRY";
    case STX_SEND_NOT_READY:    return "NOT_READY";
    case STX_SEND_TOO_BIG:      return "TOO_BIG";
    case STX_SEND_STREAM_DIRTY: return "STREAM_DIRTY";
    default:                    return "UNKNOWN";
    }
}

stx_send_class_t stx_classify_send(link_result_t r, size_t progress, size_t len)
{
    /* ⚠ 顺序很重要：**先判"流是否已被污染"**，再判 link_result_t。
     *
     * 为什么：一旦有字节写出去（progress > 0）而本次没写完整帧，TCP 流里就有半帧，
     * 对端重组器会一直等剩余字节。此时**重试整帧是错的**（会写出重复前缀 ⇒ 静默损坏）。
     * 所以"部分写出 + 未完成"必须归为 STREAM_DIRTY（要求重建链路），
     * 而**不能**被 link_result_t 的取值掩盖成 RETRY。
     *
     * 这一条覆盖了 D-30 的另一半：link 层保证"不重发整帧"，
     * 但**只有本层知道"这次到底写出去多少"**，所以判定必须在这里做。 */
    if (progress > 0 && progress < len) {
        return STX_SEND_STREAM_DIRTY;
    }

    switch (r) {
    case LINK_SENT_FULL:
        /* 契约：progress == len。若不符，说明 link 层违约 —— 按污染处理（保守）。 */
        return (progress == len) ? STX_SEND_DONE : STX_SEND_STREAM_DIRTY;

    case LINK_SENT_PARTIAL:
        /* 写了一部分但本次返回 —— 对**单次调用**而言仍是"未完成"。
         * progress>0 时上面已归 STREAM_DIRTY；progress==0 的 PARTIAL 是矛盾的，
         * 保守归为可重试（调用方会再次调用并续写）。 */
        return (progress == 0) ? STX_SEND_RETRY : STX_SEND_STREAM_DIRTY;

    case LINK_BACKPRESSURE:
        /* 一字节都没写出（link.h 的契约）⇒ 整帧稍后重试是**安全**的。 */
        return (progress == 0) ? STX_SEND_RETRY : STX_SEND_STREAM_DIRTY;

    case LINK_NOT_READY:
    case LINK_FATAL:
        return STX_SEND_NOT_READY;

    case LINK_PAYLOAD_TOO_BIG:
        return STX_SEND_TOO_BIG;

    default:
        return STX_SEND_NOT_READY;   /* 未知取值：保守当作"不可用" */
    }
}

int stx_to_esp_err(stx_send_class_t c)
{
    switch (c) {
    case STX_SEND_DONE:         return 0;                    /* ESP_OK */
    case STX_SEND_RETRY:        return 0x107;                /* ESP_ERR_TIMEOUT */
    case STX_SEND_NOT_READY:    return 0x103;                /* ESP_ERR_INVALID_STATE */
    case STX_SEND_TOO_BIG:      return 0x104;                /* ESP_ERR_INVALID_SIZE */
    case STX_SEND_STREAM_DIRTY: return 0x101;                /* ESP_FAIL */
    default:                    return 0x101;                /* ESP_FAIL */
    }
}

/* ══════════════════ ⭐ 上行成帧 + 发送编排（task-32，宿主可编）══════════════════
 *
 * ## 为什么成帧在这里（而不是 msg_handler_publish*）
 * transport_ops.send 是**传输无关**契约，MQTT 与 3.0 TCP 共用：
 *   - sess_tx_send      （main/session_transport.c）        ← 3.0 TCP，**需要**成帧
 *   - mqtt_adapter_send （ehome_mqtt/mqtt_transport_adapter.c:39）← 把 data **原样**发布
 *   - mqtt_tx_send      （main/uplink_mqtt_transport.c:63）  ← 同样原样发布
 * 而 msg_handler_publish_checked 失败时会回退 transport_broadcast_ex()，
 * 后者对**所有**已注册 transport 各发一遍（transport.c:100 的循环，含 MQTT）
 * ⇒ 在 publish 层成帧会**同时污染 MQTT 那一份**（2.x 设备与兜底路径）。
 * ⇒ 结论：**成帧是"3.0 TCP 线协议"的属性，不是"消息语义"的属性**；
 *    适配器正是"payload → 3.0 线字节"的边界。
 *
 * ## 为什么把编排抽到宿主可编段（本卡存在的理由）
 * 此前 sess_tx_send 整个在 #ifndef SESSION_TRANSPORT_HOST_TEST 里：
 * 宿主测试根本编不到它 ⇒ "生产路径忘记成帧"这类缺陷（§134）能长期躲在全绿后面。
 * 现在"**成帧 → 发送（含 progress 循环）**"是纯编排，真正的 session_send
 * 由调用方通过 stx_tx_t.write **注入** ⇒ 宿主用例可以断言"回调收到的字节
 * 以 magic 0x45 0x48 开头、type == payload[0]、payload_len 正确"。
 */

int stx_send_frame(stx_tx_t *tx, const uint8_t *payload, size_t len, uint32_t seq)
{
    if (tx == NULL || tx->write == NULL) return stx_to_esp_err(STX_SEND_NOT_READY);
    if (payload == NULL || len == 0) return stx_to_esp_err(STX_SEND_NOT_READY);
    if (len > STX_TX_PAYLOAD_MAX) {
        /* 超界**响亮失败 + 计数**：静默截断等于发出**半条帧**，
         * 对端定界器会错位，症状是"链路莫名卡死"而不是一条错误日志。 */
        tx->stats.too_big++;
        return stx_to_esp_err(STX_SEND_TOO_BIG);
    }
    if (tx->scratch == NULL || tx->scratch_cap < STX_FRAME_MAX) {
        tx->stats.too_big++;
        return stx_to_esp_err(STX_SEND_TOO_BIG);
    }

    /* ⭐ 唯一的成帧点：复用 task-31 的生产函数（P4：不写第二个实现）。 */
    size_t frame_len = 0;
    int frc = devlink_encode_frame(tx->scratch, tx->scratch_cap, payload, len, seq, &frame_len);
    if (frc != DEVLINK_FRAME_OK) {
        tx->stats.encode_failed++;
        return stx_to_esp_err(STX_SEND_TOO_BIG);
    }
    tx->stats.framed++;

    size_t progress = 0;
    /* 有界重试。**本函数不睡眠** —— 睡眠策略归调用方（宿主测试里不能有真实延时）。 */
    for (int attempt = 0; attempt < STX_TX_MAX_ATTEMPTS; attempt++) {
        link_result_t r = tx->write(tx->ctx, tx->scratch, frame_len, &progress);
        stx_send_class_t c = stx_classify_send(r, progress, frame_len);

        if (c == STX_SEND_DONE) return 0;
        if (c == STX_SEND_RETRY) { tx->stats.retries++; continue; }

        tx->stats.failed++;
        return stx_to_esp_err(c);
    }
    tx->stats.retries_exhausted++;
    return stx_to_esp_err(STX_SEND_RETRY);
}

/* ══════════════════════ IDF 胶水 ══════════════════════ */
#ifndef SESSION_TRANSPORT_HOST_TEST

#include "esp_err.h"
#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

static const char *TAG = "SESS_TX";

typedef struct {
    session_t *sess;
} sess_tx_priv_t;

/* 把 session_send 包成 stx_write_fn，供**宿主可测**的成帧编排 stx_send_frame 注入。
 * task-32：成帧与"发一帧"的编排都在 stx_send_frame 里（宿主可编），
 * 本函数只负责把 IDF 侧的 session 指针接进去 —— 这样"生产路径忘记成帧"
 * 那类缺陷（§134）不可能再躲在全绿后面。 */
static link_result_t sess_tx_write(void *ctx, const uint8_t *data, size_t len,
                                   size_t *progress)
{
    return session_send((session_t *)ctx, data, len, progress);
}

/* 成帧用的 scratch：静态（不进任务栈 —— 本函数在 transport 上下文里被调用）。
 * 上界见 session_transport.h 的 STX_TX_PAYLOAD_MAX 推导。 */
static uint8_t s_tx_scratch[STX_FRAME_MAX];

/* 与 link.h 给的循环模式一致：PARTIAL 续写、BACKPRESSURE 整帧重试。
 * 这里把它包成"一次调用完成整帧"的语义，因为 transport_ops.send 的契约是
 * "发一帧"，没有 progress 出参。 */
static esp_err_t sess_tx_send(transport_t *t, const uint8_t *data, size_t len)
{
    if (t == NULL || t->priv_data == NULL || data == NULL || len == 0) {
        return stx_to_esp_err(STX_SEND_NOT_READY);
    }
    sess_tx_priv_t *p = (sess_tx_priv_t *)t->priv_data;

    /* ⭐ task-32：走**宿主可测**的编排（成帧 + 发送）。
     * 之前这里直接 session_send(data) —— 线上没有 12 B 头，
     * 后端 DecodeHeader 一律 ErrMagic ⇒ 链路永远进不了 READY。 */
    stx_tx_t tx;
    tx.write = sess_tx_write;
    tx.ctx = p->sess;
    tx.scratch = s_tx_scratch;
    tx.scratch_cap = sizeof(s_tx_scratch);
    tx.stats.framed = tx.stats.too_big = tx.stats.encode_failed = 0;
    tx.stats.retries = tx.stats.failed = tx.stats.retries_exhausted = 0;

    int rc = stx_send_frame(&tx, data, len, 0);
    if (rc == 0) return ESP_OK;
    if (rc == stx_to_esp_err(STX_SEND_RETRY)) {
        /* 背压：让出 CPU —— 睡眠策略刻意留在调用方（编排层不睡眠，见其注释），
         * 否则宿主测试里会出现真实延时。 */
        ESP_LOGW(TAG, "send 背压：重试 %u 次后仍未写出（载荷 %u B）",
                 (unsigned)tx.stats.retries, (unsigned)len);
        vTaskDelay(1);
    } else {
        ESP_LOGW(TAG, "send 失败 rc=%d（framed=%u too_big=%u encode_failed=%u failed=%u）",
                 rc, (unsigned)tx.stats.framed, (unsigned)tx.stats.too_big,
                 (unsigned)tx.stats.encode_failed, (unsigned)tx.stats.failed);
    }
    return (esp_err_t)rc;
}

/* 下面这段是**旧的直发实现**，task-32 起不再使用；保留它只为对照。
 * ⚠ 不要删掉上面那条路径又用回这个 —— 它正是"不成帧"的那一版。 */
#if 0
static esp_err_t sess_tx_send_unframed_OLD(transport_t *t, const uint8_t *data, size_t len)
{
    if (t == NULL || t->priv_data == NULL || data == NULL || len == 0) {
        return stx_to_esp_err(STX_SEND_NOT_READY);
    }
    sess_tx_priv_t *p = (sess_tx_priv_t *)t->priv_data;

    size_t progress = 0;
    /* 有界重试：背压时让出 CPU 再试，避免在 transport 上下文里空转。 */
    for (int attempt = 0; attempt < 64; attempt++) {
        link_result_t r = session_send(p->sess, data, len, &progress);
        stx_send_class_t c = stx_classify_send(r, progress, len);

        if (c == STX_SEND_DONE) return ESP_OK;

        if (c == STX_SEND_RETRY) {
            vTaskDelay(1);
            continue;                 /* progress 未动 ⇒ 重发整帧安全 */
        }

        /* 其余（含 STREAM_DIRTY）**如实回报**，绝不压成 ESP_OK。
         * STREAM_DIRTY 尤其重要：流已被污染，调用方需重建链路。 */
        ESP_LOGW(TAG, "send 未完成: class=%s r=%d progress=%u/%u",
                 stx_send_class_name(c), (int)r, (unsigned)progress, (unsigned)len);
        return stx_to_esp_err(c);
    }
    ESP_LOGE(TAG, "send 重试上限用尽（背压持续）: progress=%u/%u", (unsigned)progress, (unsigned)len);
    return stx_to_esp_err(STX_SEND_RETRY);
}
#endif /* 0 —— 旧的直发实现（对照用，不再使用） */

/* task-21：注入式仲裁闸。未注入 ⇒ NULL ⇒ 只看 READY（= 本卡之前的行为）。 */
static session_transport_gate_fn s_gate = NULL;

void session_transport_set_gate(session_transport_gate_fn gate)
{
    s_gate = gate;
}

static bool sess_tx_is_connected(transport_t *t)
{
    if (t == NULL || t->priv_data == NULL) return false;
    sess_tx_priv_t *p = (sess_tx_priv_t *)t->priv_data;

    /* 两层含义**都要**，缺一不可：
     *   1. session_transport_ready —— **语义**："这一帧能不能投给它"（未握手=静默丢弃）；
     *   2. 仲裁闸                  —— **策略**："现在该不该走 TCP"（否则双栈稳态双发）。
     *
     * ⚠ 复用的是同一个 session_transport_ready()，**没有第二份定义**（P4）。
     * ⚠ 闸为 NULL 时退化为只判 1 ⇒ 与 task-21 之前逐位相同。 */
    bool gate_open = (s_gate == NULL) ? true : s_gate();
    return session_transport_connected(session_state(p->sess), gate_open);
}

static const transport_ops_t s_sess_tx_ops = {
    .init         = NULL,          /* 有意为 NULL：会话由 main 创建/拥有，本层不重复初始化 */
    .start        = NULL,          /* 同上：链路任务由 device_link_wiring 驱动 */
    .stop         = NULL,          /* 有意为 NULL：本层不拥有会话生命周期（见头文件） */
    .send         = sess_tx_send,
    .is_connected = sess_tx_is_connected,
    .deinit       = NULL,          /* 幂等：无运行时资源需清理 */
};

transport_t *session_transport_create(session_t *s)
{
    if (s == NULL) return NULL;

    transport_t *t = (transport_t *)calloc(1, sizeof(*t));
    if (t == NULL) { ESP_LOGE(TAG, "transport_t 分配失败"); return NULL; }
    sess_tx_priv_t *p = (sess_tx_priv_t *)calloc(1, sizeof(*p));
    if (p == NULL) { free(t); ESP_LOGE(TAG, "priv 分配失败"); return NULL; }

    p->sess = s;
    t->ops = &s_sess_tx_ops;
    t->type = TRANSPORT_TYPE_TCP;   /* 复用既有枚举：3.0 链路就是 TCP+mTLS */
    /* ⚠ 常量名是 TRANSPORT_DISCONNECTED（不是 TRANSPORT_STATE_DISCONNECTED）。
     * 这个错**只在 IDF 构建里暴露**：整个 IDF 胶水段在 `#ifndef SESSION_TRANSPORT_HOST_TEST` 内，
     * 宿主构建根本不编它 ⇒ 宿主 102/102 全绿，而 IDF `error: undeclared`。
     * （2026-10-07 实测：本文件首次被编进固件时才暴露。） */
    t->state = TRANSPORT_DISCONNECTED;
    t->priv_data = p;

    /* 注册**不要求**已 READY：未 READY 时 is_connected 返回 false，
     * broadcast 自然跳过它，MQTT 兜底照常工作（见头文件的"为什么本轮不接 transport_sel"）。 */
    if (transport_register(t) != ESP_OK) {
        ESP_LOGE(TAG, "transport_register 失败");
        free(p); free(t);
        return NULL;
    }
    ESP_LOGI(TAG, "3.0 会话已注册为 transport（上行可在 READY 后走 TCP）");
    return t;
}

void session_transport_destroy(transport_t *t)
{
    if (t == NULL) return;
    (void)transport_unregister(t);
    free(t->priv_data);
    free(t);
}

#endif /* !SESSION_TRANSPORT_HOST_TEST */
