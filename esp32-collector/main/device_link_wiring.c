/**
 * @file device_link_wiring.c
 * @brief 3.0 设备侧链路（TCP + mTLS）接线。判定与胶水分离；理由见 device_link_wiring.h。
 *
 * 编译形态：
 *   - 默认（固件）：纯判定 + IDF 胶水；
 *   - `-DDEVICE_LINK_HOST_TEST=1`（宿主）：只编纯判定，不含任何 IDF 头。
 *     这样"有判断的部分"能被宿主测试真正跑到，而不是靠 EXEMPT 绕过去。
 */
#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "device_link_wiring.h"
#include "variant.h"
#include "wire.h"      /* 只为了 WIRE_HEADER_BYTES / WIRE_CRC_BYTES（IDF 无关）*/

/* ── Kconfig 值兜底（IDF 与宿主两侧都可能没定义）──
 * IDF 对 bool=n **不生成** #define，所以不能直接引用 CONFIG_x，必须先兜底。 */
#if defined(CONFIG_EHOME_DEVICE_LINK_ENABLED) && (CONFIG_EHOME_DEVICE_LINK_ENABLED == 1)
#define EHOME_DEVLINK_ENABLED 1
#else
#define EHOME_DEVLINK_ENABLED 0
#endif

#ifndef CONFIG_EHOME_DEVICE_LINK_HOST
#define CONFIG_EHOME_DEVICE_LINK_HOST "192.0.2.1"
#endif
#ifndef CONFIG_EHOME_DEVICE_LINK_PORT
#define CONFIG_EHOME_DEVICE_LINK_PORT 8443
#endif
#ifndef CONFIG_EHOME_DEVICE_LINK_MAX_PAYLOAD
#define CONFIG_EHOME_DEVICE_LINK_MAX_PAYLOAD 4096
#endif
#ifndef CONFIG_EHOME_DEVICE_LINK_RX_BUF
#define CONFIG_EHOME_DEVICE_LINK_RX_BUF 2048
#endif
#ifndef CONFIG_EHOME_DEVICE_LINK_CERT_BYTES
#define CONFIG_EHOME_DEVICE_LINK_CERT_BYTES 4096
#endif
#ifndef CONFIG_EHOME_DEVICE_LINK_NVS_NS
#define CONFIG_EHOME_DEVICE_LINK_NVS_NS "eh_tls"
#endif
#ifndef CONFIG_EHOME_DEVICE_LINK_TIMEOUT_MS
#define CONFIG_EHOME_DEVICE_LINK_TIMEOUT_MS 10000
#endif
/* 空串（默认）= 没配 NTP 服务器 => sntp_mgr 进 DISABLED，不假装能同步。
 * 兜底不是为了"能编过"：IDF 对 string 选项在 depends 不满足时**不生成**
 * #define，宿主编译更是一个 Kconfig 都没有，两处都必须能落到"空"。 */
#ifndef CONFIG_EHOME_NTP_SERVER
#define CONFIG_EHOME_NTP_SERVER ""
#endif

/* ══════════════════════════ 纯判定（宿主与固件都编）══════════════════════════ */

const char *devlink_cert_name(devlink_cert_t v)
{
    switch (v) {
    case DEVLINK_CERT_OK:      return "OK";
    case DEVLINK_CERT_EMPTY:   return "EMPTY";
    case DEVLINK_CERT_TOO_BIG: return "TOO_BIG";
    default:                   return "UNKNOWN";
    }
}

const char *devlink_place_name(devlink_place_t v)
{
    switch (v) {
    case DEVLINK_PLACE_OK:                   return "OK";
    case DEVLINK_PLACE_NO_VARIANT:           return "NO_VARIANT";
    case DEVLINK_PLACE_EXCEEDS_CONTIGUOUS:   return "EXCEEDS_CONTIGUOUS";
    default:                                 return "UNKNOWN";
    }
}

devlink_cert_t devlink_cert_check(size_t blob_bytes, size_t cap)
{
    /* 顺序有意：0 先于 cap 判 —— 若 cap 被配成 0，"没有材料"与"超上限"
     * 是两件不同的事，报错时要能分清（前者是没铺开，后者是配错了）。 */
    if (blob_bytes == 0) return DEVLINK_CERT_EMPTY;
    if (blob_bytes > cap) return DEVLINK_CERT_TOO_BIG;
    return DEVLINK_CERT_OK;
}

uint32_t device_link_delim_bytes(uint32_t max_payload)
{
    return max_payload + (uint32_t)WIRE_HEADER_BYTES + (uint32_t)WIRE_CRC_BYTES;
}

devlink_place_t device_link_check_placement(uint32_t max_payload,
                                            const variant_caps_t *caps)
{
    /* 取不到型号能力时**不猜**：默默用一个默认上界会把"型号表没接上"
     * 伪装成"内存刚好够"，之后在真机上以随机失败的形式回来。 */
    if (caps == NULL) return DEVLINK_PLACE_NO_VARIANT;
    if (device_link_delim_bytes(max_payload) > caps->internal_contiguous_max) {
        return DEVLINK_PLACE_EXCEEDS_CONTIGUOUS;
    }
    return DEVLINK_PLACE_OK;
}

devlink_net_edge_t devlink_net_edge(bool was_up, bool is_up)
{
    if (was_up == is_up) return DEVLINK_NET_EDGE_NONE;
    return is_up ? DEVLINK_NET_EDGE_UP : DEVLINK_NET_EDGE_DOWN;
}

uint64_t devlink_now_epoch_value(bool have_epoch, uint64_t epoch)
{
    /* 取不到 => 0（tls_guard 判不可信）。**不钳制**：阈值只有 tls_guard 一处。 */
    return have_epoch ? epoch : 0u;
}

/* ══════════════════════════ IDF 胶水（宿主构建不含）══════════════════════════ */
#ifndef DEVICE_LINK_HOST_TEST

#include <stdlib.h>
#include <string.h>

#include "esp_err.h"
#include "esp_heap_caps.h"
#include "esp_log.h"
#include "esp_random.h"
#include "esp_timer.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "nvs_flash.h"

#include <time.h>

#include "esp_sntp.h"

#include "session.h"
#include "tls_esp.h"
#include "wifi_mgr.h"
#include "sntp_mgr.h"     /* IDF 无关的头（组件约束 C2），只在这里被胶水用到 */
#include "tls_guard.h"    /* 「时间是否可信」的单一来源（P4）—— 不再写第二份阈值 */

static const char *TAG = "DEV_LINK";

/** 链路任务栈。**TLS 握手在本任务里跑** ⇒ 不能按"普通轮询任务"给小栈。 */
#define DEVLINK_TASK_STACK 8192
#define DEVLINK_TASK_PRIO  5

/* ── 注入项 ── */

/** 单调毫秒时钟（session 用它判退避是否到点；与"墙上时间"无关）。 */
static uint64_t devlink_now_ms(void)
{
    return (uint64_t)(esp_timer_get_time() / 1000);
}

/** 退避抖动 [0,1000]。 */
static uint32_t devlink_rand_permille(void)
{
    return (uint32_t)(esp_random() % 1001u);
}

/**
 * 收到一条**已被定界**的 3.0 消息。
 *
 * ⚠ 刻意**不解析 payload 语义**：设计里"3.0 payload 是否保留 2.x 的类型首字节"
 * 仍是待确认项。定界与那个决定无关，所以这里只如实记录帧元数据 ——
 * 绝不猜一个 payload 布局，那正是 D-09 那类"靠巧合成立"的契约。
 */
static bool devlink_on_msg(const rx_msg_t *m, void *ctx)
{
    (void)ctx;
    ESP_LOGI(TAG, "rx 3.0 msg ver=0x%02X type=0x%02X seq=%u plen=%u",
             (unsigned)m->ver, (unsigned)m->type, (unsigned)m->seq,
             (unsigned)m->payload_len);
    return true;
}

/* ── 证书：从 NVS 读（设计指定"证书/私钥入 NVS 加密分区"；分区尚未实现）── */

static uint8_t *s_ca;   static size_t s_ca_len;
static uint8_t *s_cert; static size_t s_cert_len;
static uint8_t *s_key;  static size_t s_key_len;

static esp_err_t nvs_read_blob_alloc(nvs_handle_t h, const char *key,
                                     uint8_t **out, size_t *out_len)
{
    size_t need = 0;
    esp_err_t err = nvs_get_blob(h, key, NULL, &need);
    if (err != ESP_OK) return err;                 /* NOT_FOUND 也如实返回 */

    devlink_cert_t v = devlink_cert_check(need, CONFIG_EHOME_DEVICE_LINK_CERT_BYTES);
    if (v != DEVLINK_CERT_OK) {
        ESP_LOGE(TAG, "证书 %s 判定为 %s（%u B，上限 %d）—— 不放宽上限",
                 key, devlink_cert_name(v), (unsigned)need,
                 (int)CONFIG_EHOME_DEVICE_LINK_CERT_BYTES);
        return (v == DEVLINK_CERT_EMPTY) ? ESP_ERR_NVS_NOT_FOUND : ESP_ERR_INVALID_SIZE;
    }

    uint8_t *buf = (uint8_t *)malloc(need);
    if (buf == NULL) return ESP_ERR_NO_MEM;
    err = nvs_get_blob(h, key, buf, &need);
    if (err != ESP_OK) { free(buf); return err; }
    *out = buf; *out_len = need;
    return ESP_OK;
}

/** 读全部证书材料。任一缺失即如实返回该错误码。 */
static esp_err_t devlink_load_certs(tls_esp_certs_t *out)
{
    nvs_handle_t h;
    esp_err_t err = nvs_open(CONFIG_EHOME_DEVICE_LINK_NVS_NS, NVS_READONLY, &h);
    if (err != ESP_OK) return err;

    err = nvs_read_blob_alloc(h, "ca",   &s_ca,   &s_ca_len);
    if (err == ESP_OK) err = nvs_read_blob_alloc(h, "cert", &s_cert, &s_cert_len);
    if (err == ESP_OK) err = nvs_read_blob_alloc(h, "key",  &s_key,  &s_key_len);
    nvs_close(h);

    if (err == ESP_OK) {
        out->ca_pem   = s_ca;   out->ca_len   = s_ca_len;
        out->cert_pem = s_cert; out->cert_len = s_cert_len;
        out->key_pem  = s_key;  out->key_len  = s_key_len;
    }
    return err;
}

static void devlink_free_certs(void)
{
    free(s_ca);   s_ca = NULL;   s_ca_len = 0;
    free(s_cert); s_cert = NULL; s_cert_len = 0;
    free(s_key);  s_key = NULL;  s_key_len = 0;
}

/* ── SNTP：sntp_mgr 的真 I/O 适配器 ──
 *
 * 这一层刻意做**薄**：所有判定（什么时候发起、等多久、退避多久、多大算可信）
 * 都在 sntp_mgr / tls_guard 里，这里只做"把 IDF 的调用摆对位置"。
 * 与 components/tls_esp/tls_esp.c 同一分工（判定下沉、胶水留薄）。 */

static sntp_mgr_t    *s_sntp;
static bool           s_sntp_started;   /* esp_sntp_init 是否已调用过 */
static bool           s_sntp_net_up;    /* 上一轮观察到的网络状态（用于判边沿） */

/** io->start：发起一次同步。**必须幂等** —— sntp_mgr 会在退避到点后重复调用。
 *
 * ⚠ 这里有一个不看 IDF 源码就会写错的地方（我核对了 lwip/sntp.c 才确认）：
 *   - `esp_sntp_init()` 内部是 `if (sntp_pcb == NULL) { ... sntp_request(NULL); }`，
 *     也就是说**第二次调用是彻底的 no-op**，不会再发一个 NTP 查询；
 *   - `esp_sntp_setservername()` 只是把字符串指针存进表里，**不触发重发**。
 *   若 start() 只写 init()，sntp_mgr 的"退避到点重新发起"就会变成
 *   **starts 计数在涨、而网线上一个包都没出去** —— 一次静默的空转。
 *   ⇒ 首次用 init()，之后必须用 restart()（它内部是 stop()+init()，
 *     会重新触发一次 request）。 */
static void sntp_io_start(void *ctx, const char *server)
{
    (void)ctx;
    if (server == NULL || server[0] == '\0') return;   /* 空串由 sntp_mgr 挡在 DISABLED */

    if (!s_sntp_started) {
        esp_sntp_setoperatingmode(ESP_SNTP_OPMODE_POLL);
        esp_sntp_setservername(0, server);
        esp_sntp_init();
        s_sntp_started = true;
        ESP_LOGI(TAG, "SNTP 已发起（server=%s）", server);
        return;
    }
    /* 已初始化：init 是 no-op，必须 restart 才真的再发一次查询。 */
    if (!esp_sntp_restart()) {
        ESP_LOGW(TAG, "esp_sntp_restart 返回 false（SNTP 未启用？）—— 本轮重试没有真正发出");
    }
}

/** io->get_time：当前墙上时间。
 *
 * 只看 `time()` 是否已越过 1970。**不在这里判"可信不可信"** ——
 * 那是 tls_guard 的职责（单一来源）；本函数只如实回答"现在几点"。 */
static bool sntp_io_get_time(void *ctx, uint64_t *epoch_out)
{
    (void)ctx;
    if (epoch_out == NULL) return false;
    time_t now = 0;
    time(&now);
    if (now <= 0) return false;        /* 还没校时：1970 */
    *epoch_out = (uint64_t)now;
    return true;
}

/** io->now_ms：单调毫秒（超时/退避用，与墙上时间无关）。 */
static uint64_t sntp_io_now_ms(void *ctx)
{
    (void)ctx;
    return (uint64_t)(esp_timer_get_time() / 1000);
}

static const sntp_mgr_io_t s_sntp_io = {
    .start    = sntp_io_start,
    .get_time = sntp_io_get_time,
    .now_ms   = sntp_io_now_ms,
};

/** tls_esp 的 now_epoch：取不到就给 0，由 tls_guard 判不可信。
 *  （契约与理由见 device_link_wiring.h 的 devlink_now_epoch_value。） */
static uint64_t devlink_now_epoch(void)
{
    uint64_t epoch = 0;
    bool have = (s_sntp != NULL) && sntp_mgr_now(s_sntp, &epoch);
    return devlink_now_epoch_value(have, epoch);
}

/* ── 运行期状态 ── */

static session_t *s_session;
static uint8_t   *s_rx_buf;
static const char *s_state_txt = "NONE";
static sntp_mgr_state_t s_sntp_last = SNTP_MGR_DISABLED;

const char *device_link_wiring_state_name(void)
{
    return EHOME_DEVLINK_ENABLED ? s_state_txt : "DISABLED";
}

/**
 * "是否启用"单独一个函数，且**不是**编译期常量表达式（编译单元内可见的实现细节）。
 * 目的：让下面的实现**始终被编译**，而不是被 `#ifdef` 掉。
 * 若整段实现被条件编译掉，可达性门禁看到的就只是 REQUIRES 里一行文字，
 * 而固件里并没有这段代码 —— 那正是本项目的"假绿"形态。
 */
static bool devlink_wanted(void)
{
    return EHOME_DEVLINK_ENABLED != 0;
}

static void devlink_task(void *arg)
{
    session_t *s = (session_t *)arg;
    session_state_t last = session_state(s);
    s_state_txt = session_state_name(last);

    ESP_LOGI(TAG, "3.0 链路任务启动：host=%s port=%d max_payload=%d 初始状态=%s",
             CONFIG_EHOME_DEVICE_LINK_HOST, (int)CONFIG_EHOME_DEVICE_LINK_PORT,
             (int)CONFIG_EHOME_DEVICE_LINK_MAX_PAYLOAD, s_state_txt);

    for (;;) {
        /* ⚠ wifi_mgr_get_state() 这一轮只取一次：SNTP 与 3.0 链路必须看到
         * **同一个**网络状态，否则两者可以在同一轮里得出不同结论。 */
        bool net_up = (wifi_mgr_get_state() == WIFI_MGR_CONNECTED);

        /* ── SNTP：复用本任务已有的观察点，**不新建任务** ──
         * 只在**边沿**通知 up/down（理由见 device_link_wiring.h 的
         * devlink_net_edge）。no-server 时 sntp_mgr 处于 DISABLED，通知是 no-op。 */
        if (s_sntp != NULL) {
            switch (devlink_net_edge(s_sntp_net_up, net_up)) {
            case DEVLINK_NET_EDGE_UP:
                s_sntp_net_up = true;
                sntp_mgr_network_up(s_sntp);
                break;
            case DEVLINK_NET_EDGE_DOWN:
                s_sntp_net_up = false;
                sntp_mgr_network_down(s_sntp);
                break;
            case DEVLINK_NET_EDGE_NONE:
                break;
            }
            uint64_t epoch = 0;
            sntp_mgr_state_t sst = sntp_mgr_poll(s_sntp, &epoch);
            if (sst != s_sntp_last) {
                /* 状态变化才打日志：本任务 10ms 一轮，否则会淹掉串口。 */
                ESP_LOGI(TAG, "SNTP %s -> %s（epoch=%llu）",
                         sntp_mgr_state_name(s_sntp_last), sntp_mgr_state_name(sst),
                         (unsigned long long)epoch);
                s_sntp_last = sst;
            }
        }

        /* 网络没起来就不去连 —— 否则每次 session_poll 都白走一遍连接失败/退避，
         * 日志里看不出"其实只是 WiFi 还没好"。 */
        if (!net_up) {
            vTaskDelay(pdMS_TO_TICKS(500));
            continue;
        }

        uint32_t delivered = 0;
        session_state_t st = session_poll(s, &delivered);

        if (st != last) {
            s_state_txt = session_state_name(st);
            /* 状态变化才打日志：稳定期每 10ms 一行会淹掉串口。 */
            ESP_LOGW(TAG, "状态 %s -> %s（重连尝试=%u）",
                     session_state_name(last), s_state_txt,
                     (unsigned)session_reconnect_attempt(s));
            if (st == SESSION_FATAL) {
                ESP_LOGE(TAG, "FATAL：重试无意义（证书/配置类）。"
                              "请确认 NVS 命名空间 '%s' 里已铺开 ca/cert/key；"
                              "时间源状态见上面的 SNTP 日志（now_epoch=%s）",
                         CONFIG_EHOME_DEVICE_LINK_NVS_NS,
                         (s_sntp != NULL) ? sntp_mgr_state_name(sntp_mgr_state(s_sntp))
                                          : "sntp_mgr 未创建");
            }
            last = st;
        }

        vTaskDelay(pdMS_TO_TICKS(10));
    }
}

void device_link_wiring_init(void)
{
    static bool s_inited = false;
    if (s_inited) return;
    s_inited = true;

    if (!devlink_wanted()) {
        ESP_LOGI(TAG, "3.0 设备侧链路**未启用**（CONFIG_EHOME_DEVICE_LINK_ENABLED=n）："
                      "实现已编译进来，但不建任务、不分配堆、不发一个包");
        return;
    }

    /* ── 1) 放置判定（P8：型号差异只影响资源摆放）── */
    devlink_place_t place = device_link_check_placement(
        (uint32_t)CONFIG_EHOME_DEVICE_LINK_MAX_PAYLOAD, variant_caps());
    if (place != DEVLINK_PLACE_OK) {
        /* 不"悄悄调小"——调小会改变可观测行为（大消息被判超上界），必须可见地失败。 */
        const variant_caps_t *vc = variant_caps();
        ESP_LOGE(TAG, "拒绝启动：%s（需 %u B 连续块，型号 %s 上界 %u B）。"
                      "请调小 CONFIG_EHOME_DEVICE_LINK_MAX_PAYLOAD",
                 devlink_place_name(place),
                 (unsigned)device_link_delim_bytes(CONFIG_EHOME_DEVICE_LINK_MAX_PAYLOAD),
                 vc ? vc->name : "(null)", vc ? (unsigned)vc->internal_contiguous_max : 0u);
        return;
    }

    /* ── 2) 读缓冲（调用方提供，session 不隐式分配大块）── */
    s_rx_buf = (uint8_t *)malloc(CONFIG_EHOME_DEVICE_LINK_RX_BUF);
    if (s_rx_buf == NULL) {
        ESP_LOGE(TAG, "读缓冲分配失败（%d B）：free=%u largest=%u",
                 (int)CONFIG_EHOME_DEVICE_LINK_RX_BUF,
                 (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL),
                 (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL));
        return;
    }

    /* ── 3) 证书 ── */
    tls_esp_certs_t certs;
    memset(&certs, 0, sizeof(certs));
    esp_err_t cerr = devlink_load_certs(&certs);
    if (cerr != ESP_OK) {
        ESP_LOGW(TAG, "未取到完整证书材料（%s）：照常构造 ⇒ 由 tls_guard 判为 "
                      "hard_fatal ⇒ SESSION_FATAL（不重试，避免把'没铺开'掩盖成'网络抖动'）",
                 esp_err_to_name(cerr));
        ESP_LOGW(TAG, "铺开步骤：把 ca/cert/key 三个 blob 写进 NVS 命名空间 '%s'"
                      "（设计指定加密分区，该分区尚未实现）",
                 CONFIG_EHOME_DEVICE_LINK_NVS_NS);
    }

    /* ── 3.5) SNTP：mTLS 的时间前置条件 ──
     *
     * 放在 tls_esp 配置**之前**：now_epoch 要指向一个已经存在的 sntp_mgr，
     * 否则首个握手会拿不到时间（而那正是本任务要修的东西）。 */
    {
        sntp_mgr_config_t scfg;
        memset(&scfg, 0, sizeof(scfg));
        scfg.io = &s_sntp_io;
        scfg.io_ctx = NULL;
        scfg.server = CONFIG_EHOME_NTP_SERVER;   /* "" => DISABLED（不假装能同步）*/
        scfg.wait_ms = 0;                        /* 0 => sntp_mgr 用默认 30s */
        /* P4：时间可信与否**只有** tls_guard 一个判据，不在这里重写阈值。 */
        scfg.is_time_trusted = tls_guard_time_is_trusted;
        s_sntp = sntp_mgr_create(&scfg);
        /* 让首条状态日志只报真实变化，而不是初始化顺序造成的假跳变。 */
        if (s_sntp != NULL) s_sntp_last = sntp_mgr_state(s_sntp);
        if (s_sntp == NULL) {
            ESP_LOGE(TAG, "sntp_mgr_create 失败（内存？）—— 时间保持不可信："
                          "now_epoch 会返回 0，证书类失败按可自愈处理（不阻断 TLS）");
        } else if (sntp_mgr_state(s_sntp) == SNTP_MGR_DISABLED) {
            ESP_LOGW(TAG, "未配置 NTP 服务器（CONFIG_EHOME_NTP_SERVER 为空）=> SNTP DISABLED："
                          "不发一个查询，也不假装有时间。证书类失败会被分级为可自愈");
        } else {
            ESP_LOGI(TAG, "SNTP 已装载：server=%s（网络就绪后由本任务发起）",
                     CONFIG_EHOME_NTP_SERVER);
        }
    }

    /* ── 4) tls_esp 配置 ── */
    tls_esp_config_t tcfg;
    memset(&tcfg, 0, sizeof(tcfg));
    tcfg.host = CONFIG_EHOME_DEVICE_LINK_HOST;
    tcfg.port = (uint16_t)CONFIG_EHOME_DEVICE_LINK_PORT;
    tcfg.timeout_ms = CONFIG_EHOME_DEVICE_LINK_TIMEOUT_MS;
    tcfg.certs = certs;
    /* now_epoch 接到 SNTP。取不到时间时它返回 **0**（而不是"看起来合理"的
     * 时间），于是 tls_guard 把证书类失败判为**可自愈**而不是致命 ——
     * 见 §52.2 的分级意图。 */
    tcfg.now_epoch = devlink_now_epoch;

    tls_esp_config_t *tls_cfg = tls_esp_config_new(&tcfg);
    if (tls_cfg == NULL) {
        ESP_LOGE(TAG, "tls_esp_config_new 失败（内存？）");
        devlink_free_certs();
        return;
    }

    /* ── 5) session ── */
    session_config_t scfg;
    memset(&scfg, 0, sizeof(scfg));
    scfg.io = tls_esp_io();
    scfg.io_ctx = tls_cfg;
    scfg.max_payload = (uint32_t)CONFIG_EHOME_DEVICE_LINK_MAX_PAYLOAD;
    scfg.rx_buf = s_rx_buf;
    scfg.rx_buf_cap = CONFIG_EHOME_DEVICE_LINK_RX_BUF;
    scfg.now_ms = devlink_now_ms;
    scfg.rand_permille = devlink_rand_permille;
    scfg.on_msg = devlink_on_msg;
    scfg.on_msg_ctx = NULL;

    s_session = session_create(&scfg);
    if (s_session == NULL) {
        ESP_LOGE(TAG, "session_create 失败（参数/内存）");
        tls_esp_config_free(tls_cfg);
        devlink_free_certs();
        return;
    }

    BaseType_t ok = xTaskCreate(devlink_task, "dev_link", DEVLINK_TASK_STACK,
                                s_session, DEVLINK_TASK_PRIO, NULL);
    if (ok != pdPASS) {
        /* 复用 OTA 那次的教训：连续块不够时 free 会骗人，必须打 largest。 */
        ESP_LOGE(TAG, "链路任务创建失败（需 %d B 连续栈）：free=%u largest=%u",
                 DEVLINK_TASK_STACK,
                 (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL),
                 (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL));
        session_destroy(s_session);
        s_session = NULL;
        tls_esp_config_free(tls_cfg);
        devlink_free_certs();
        return;
    }

    s_state_txt = session_state_name(session_state(s_session));
    ESP_LOGI(TAG, "3.0 设备侧链路已接线：tls_esp -> session(link_tcp/link_rx_adapt/rx_pump/wire)");
    /* 注意：证书缓冲**不释放** —— tls_esp 浅拷贝持有这些指针，
     * 必须活到连接生命周期结束（tls_esp.h 的契约）。 */
}

#endif /* !DEVICE_LINK_HOST_TEST */
