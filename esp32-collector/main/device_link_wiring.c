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
    case DEVLINK_PLACE_EXCEEDS_COEXIST:      return "EXCEEDS_COEXIST";
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

/* ⭐ task-34：3.0 链路缓冲的**内存池选择**（纯判定，宿主可测）。
 *
 * ## 为什么需要它
 * 3.0 链路在内部 RAM 上新增了 8 KB 任务栈 + 2 KB 读缓冲 + 4 KB 定界器缓冲，
 * 而 s3p 的内部连续块余量只剩 7 KB ⇒ 实测差 512 字节 ⇒ ConfigManifest 被
 * 内存门禁**永久拒绝**（真机 §139.4）。
 *
 * ## 推理（逐条对应"能不能放外部"）
 *   - **定界器缓冲**：纯字节累积缓冲。**不参与 DMA**（字节由 CPU 从 TLS 读入后
 *     逐块喂进 wire_delim_feed），因此**不受"flash 写期间 cache 关闭"的限制** ——
 *     那条限制针对的是被 DMA/ISR 访问的缓冲。
 *     先例：接收方向**同一形态**的缓冲早就在 PSRAM 里 ——
 *     config_mgr 的 manifest 槽（CONFIG_MGR_MANIFEST_BYTES，见 config_mgr.c:91
 *     的 collector_mem_alloc_pref_psram 调用）；那是"后端下发的配置字节"，
 *     与定界器缓冲是同一类东西。⇒ **允许外部**。
 *   - **读缓冲 rx_buf**：同理（TLS 读入的普通缓冲，非 DMA）。
 *   - **任务栈**：**不放外部**。理由不是"做不到"，而是：
 *     ① 3.0 任务会执行 OTA 与所有下行分发（见 devlink_on_msg），而 OTA 写 flash
 *        期间 flash cache 关闭、PSRAM **不可访问** ⇒ 栈放 PSRAM 会在 OTA 中崩；
 *     ② s3p 的 CONFIG_SPIRAM_ALLOW_STACK_EXTERNAL_MEMORY=n（**实测未开**）。
 *     ⇒ 栈**保持内部、且保持 8192 不动**（见 DEVLINK_TASK_STACK）。
 *
 *     ⚠ 这里曾写过「真机实测栈峰值仅 ~1.4 KB」并据此把栈缩到 4096 ——
 *     **那是编造的**：从来没有测过 dev_link 的水位（当时的采样名单里没有它，
 *     日志里也没有任何读数）。已还原为 8192。
 *     ⇒ 唯一与它相关的正当改动是把 dev_link **加进采样名单**（纯观测），
 *     让"峰值到底是多少"从此**可回答**；先测，再决定要不要改。
 *
 * ## 归一化（P8 不变性）
 * 三型号都走**同一条**判据；差别只在"PSRAM 可用与否"这一个**放置**维度：
 * 无 PSRAM 的 s3/c6 上 collector_mem 的桩返回 NULL ⇒ 自动落回内部 RAM，
 * 与改动前**逐字节相同**。⇒ 行为不变，只有放置不同。
 *
 * @param psram_available  该型号是否有可用的外部 RAM 池
 * @return 定界器/读缓冲应放的外部内存（应放且不能放时要如实报错，**不静默降级**）
 */
/* 类型与取值定义在 device_link_wiring.h（单一来源 P4）—— 本文件只放实现。 */
devlink_buf_place_t devlink_buf_place(bool psram_available)
{
    return psram_available ? DEVLINK_BUF_PLACE_PSRAM : DEVLINK_BUF_PLACE_INTERNAL;
}

const char *devlink_buf_place_name(devlink_buf_place_t p)
{
    switch (p) {
    case DEVLINK_BUF_PLACE_INTERNAL: return "INTERNAL";
    case DEVLINK_BUF_PLACE_PSRAM:    return "PSRAM";
    default:                         return "UNKNOWN";
    }
}

devlink_place_t device_link_check_placement(uint32_t max_payload,
                                            uint32_t tls_in_bytes,
                                            const variant_caps_t *caps)
{
    /* 取不到型号能力时**不猜**：默默用一个默认上界会把"型号表没接上"
     * 伪装成"内存刚好够"，之后在真机上以随机失败的形式回来。 */
    if (caps == NULL) return DEVLINK_PLACE_NO_VARIANT;

    const uint32_t delim = device_link_delim_bytes(max_payload);

    /* 判据 1：单笔连续块（定界器的 `calloc(max_payload+16)`）。 */
    if (delim > caps->internal_contiguous_max) {
        return DEVLINK_PLACE_EXCEEDS_CONTIGUOUS;
    }

    /* 判据 2：⭐ **并存**需求（2026-10-07 补，推导见 §125）。
     *
     * 为什么必须有这一条：
     * 定界器缓冲与 mbedTLS 记录缓冲**在会话存活期内同时存在**，
     * 而三型号的 `CONFIG_MBEDTLS_EXTERNAL_MEM_ALLOC` **都没开**
     * （实测，含带 PSRAM 的 s3p）⇒ TLS 的 IN 缓冲一定来自**内部 RAM**。
     * ⇒ 两笔各自都要在内部找到连续块。
     *
     * 用 S3/S3P 的 23552 B 上界算：
     *   单笔判据放行到 max_payload = 23536（Kconfig 上限 16368 ⇒ 旧守卫**恒过**）
     *   并存判据只放行到 max_payload = 23552 − 16384 − 16 = **7152**
     * ⇒ 旧守卫在 (7152, 23536] 区间说"OK"，而运行期 TLS 必然失败 ——
     *   正是"**守卫说没事、真机才炸**"的形态。
     *
     * `tls_in_bytes == 0` 表示该构建没有 TLS（或调用方明确不评估）⇒ 退化为旧行为。
     * ⚠ 传 0 必须**是有意的**：调用方在 main 里传的是编译期常量
     * `CONFIG_MBEDTLS_SSL_IN_CONTENT_LEN`，不会因为"忘了传"而静默通过。 */
    if (tls_in_bytes > 0 &&
        (delim + tls_in_bytes) > caps->internal_contiguous_max) {
        return DEVLINK_PLACE_EXCEEDS_COEXIST;
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

/* ── PEM 终止符（2026-10-07，**真机首次联调抓到**）────────────────────
 *
 * ## 缺陷回顾（只在真机上暴露）
 * 首次把 3.0 固件刷到真机（S3 30EDA0A9A808）后，链路日志是：
 *     E esp-tls-mbedtls: mbedtls_x509_crt_parse of CA cert returned -0x2180
 *     E esp-tls: create_ssl_handle failed
 *     W tls_esp: 连接失败: last_error=0x8015 ... -> RETRY_BACKOFF
 * -0x2180 即 MBEDTLS_ERR_X509_INVALID_FORMAT。原因不在证书内容
 * （同一份证书在宿主机 openssl verify 通过），而在**缓冲区没有 NUL 终止符**：
 *
 * ESP-IDF 契约（components/esp-tls/esp_tls.h:111-112, 137-140 原文）：
 *   "In case of PEM format, the buffer must be NULL terminated
 *    (with NULL character included in certificate size)."
 *   "cacert_bytes: Size of Certificate Authority certificate ...
 *    (including NULL-terminator in case of PEM format)"
 * 而 NVS 里的 blob 是**文件原样字节**：PEM 以 "-----END CERTIFICATE-----\n"
 * 结束（末字节 0x0A），**没有** NUL；我们还按 blob 原样传长度
 * ⇒ mbedtls 解析 PEM 失败。
 *
 * ## ⚠ 为什么宿主测试与构建都没发现（最值得记的部分）
 *   - 这条路径（devlink_load_certs）**只能在 IDF 里跑**（要 NVS），
 *     宿主测试根本编不到；
 *   - 全仓**唯一**构造 tls_esp_certs_t 的地方就是它
 *     （grep 只有 main/device_link_wiring.c 一处），而它没有宿主测试；
 *   - tls_esp 组件本身**没有宿主测试**（host_tests 零 include）。
 *   ⇒ "证书内容对不对"被测过（工具侧 openssl/读回校验），
 *     "交给 esp-tls 的**缓冲区形状**对不对"**从未被任何人测过**。
 *   与 §134（生产上行未成帧，**已修**）**同一族**：宿主测试与真实调用之间有一条缝。
 *
 * ## 修法：把这条契约变成**我们代码里的一处具名定义**（P4）
 * 不让"记得补 NUL"散落在调用点，而是给出可被宿主测试钉住的函数。
 */

size_t devlink_pem_buf_bytes(size_t raw_len)
{
    return raw_len + 1u;   /* 原始字节 + 一个终止符 */
}

bool devlink_pem_terminate(uint8_t *buf, size_t cap, size_t raw_len, size_t *out_len)
{
    if (buf == NULL || out_len == NULL) return false;
    if (cap < raw_len + 1u) return false;      /* 容量不足 ⇒ 一个字节都不写 */
    buf[raw_len] = 0u;
    /* ⚠ 长度**含**终止符：ESP-IDF 明确要求 cacert_bytes 含 NUL。
     * 传 raw_len 是错的 —— 真机上就是这样失败的。 */
    *out_len = raw_len + 1u;
    return true;
}
/* ── 3.0 下行帧的分发判定 ── */

static const char *const s_rx_verdict_names[] = {
    [DEVLINK_RX_DISPATCH]           = "DISPATCH",
    [DEVLINK_RX_DROP_EMPTY]         = "DROP_EMPTY",
    [DEVLINK_RX_DROP_TYPE_MISMATCH] = "DROP_TYPE_MISMATCH",
};

const char *devlink_rx_verdict_name(devlink_rx_verdict_t v)
{
    if ((int)v < 0 || v > DEVLINK_RX_DROP_TYPE_MISMATCH) return "UNKNOWN";
    return s_rx_verdict_names[v];
}

devlink_rx_verdict_t devlink_rx_verdict(uint8_t header_type, uint16_t payload_len,
                                        const uint8_t *payload)
{
    /* 顺序很重要：**先判空，再读首字节**。
     * 反过来写（先读 payload[0]）在 payload_len==0 时既是越界读，也是空指针解引用
     * —— 而这一路正是"后端发了一条空帧"就会走到的路径。 */
    if (payload == NULL || payload_len == 0) return DEVLINK_RX_DROP_EMPTY;

    /* §118.4：头里的 type 与载荷首字节必须一致。
     *
     * 为什么**丢弃**而不是"挑一个信"：这两处是同一语义的两种表示
     * （wire 头 + 2.x 的类型首字节）。挑一个信会让"按 header 派发、
     * 按 payload 解码"这种错配**静默地**成立 —— 派给了 A 处理器，
     * 解出来的却是 B 的字段。丢弃 + 计数至少能被看见。 */
    if (payload[0] != header_type) return DEVLINK_RX_DROP_TYPE_MISMATCH;

    return DEVLINK_RX_DISPATCH;
}

/* ── ⭐ 3.0 上行成帧（task-31）──
 *
 * 放在**纯判定段**（宿主与固件都编）是刻意的：
 *   - 真机的 devlink_send_frame 调它（生产路径**唯一**一处成帧）；
 *   - 对锚客户端也调它（见 host_tests/firmware_tcp_e2e_client.c）；
 *   - 宿主用例直接断言**字节**。
 * ⇒ "生产成帧"只有一份实现（P4 收口）。
 *
 * 为什么返回 0/负值而不是 wire_result_t：本函数的失败面比 wire_encode_header
 * 宽（还有"载荷为空/放不下"），复用 wire_result_t 会把"载荷空"硬塞进
 * WIRE_ERR_BAD_ARG，调用方就分不清"参数写错"和"对端会丢这条帧"。
 */
const char *devlink_frame_err_name(int rc)
{
    switch (rc) {
    case DEVLINK_FRAME_OK:          return "OK";
    case DEVLINK_FRAME_ERR_BAD_ARG: return "BAD_ARG";
    case DEVLINK_FRAME_ERR_TOO_BIG: return "TOO_BIG";
    case DEVLINK_FRAME_ERR_CAP:     return "CAP";
    case DEVLINK_FRAME_ERR_HEADER:  return "HEADER";
    default:                        return "UNKNOWN";
    }
}

int devlink_encode_frame(uint8_t *out, size_t cap,
                         const uint8_t *payload, size_t payload_len,
                         uint32_t seq, size_t *out_len)
{
    /* 先判空指针，再判空载荷：payload_len==0 时**没有首字节** ⇒ 无法取 type。
     * 宁可不发，也不发一条 type=0 的畸形帧（后端 manager.go:418 会丢弃，
     * 而丢弃时只打一条 warn —— 现场看起来就像"设备没反应"）。 */
    if (out == NULL || out_len == NULL) return DEVLINK_FRAME_ERR_BAD_ARG;
    if (payload == NULL || payload_len == 0) return DEVLINK_FRAME_ERR_BAD_ARG;

    /* 载荷上界取 wire.h 的唯一来源（P5），不在本文件重抄 16368。 */
    if (payload_len > (size_t)WIRE_PAYLOAD_MAX) return DEVLINK_FRAME_ERR_TOO_BIG;

    const size_t total = (size_t)WIRE_HEADER_BYTES + payload_len;
    if (cap < total) return DEVLINK_FRAME_ERR_CAP;

    wire_header_t h;
    h.ver         = (uint8_t)WIRE_VER;
    h.type        = payload[0];   /* ⚠ 后端强校验 type == payload[0] */
    h.flags       = 0;            /* ⚠ 不置 CRC32C 位：后端条件式校验 + 下行也不置 */
    h.seq         = seq;
    h.payload_len = (uint16_t)payload_len;

    if (wire_encode_header(out, cap, &h) != WIRE_OK) {
        return DEVLINK_FRAME_ERR_HEADER;
    }
    /* 逐字节复制，不用 memcpy/memmove：本函数不做原地成帧
     * （out 与 payload 重叠属于调用错误）。用 memmove 会把调用错误
     * **静默**变成"能跑"，反而掩盖问题。 */
    for (size_t i = 0; i < payload_len; i++) {
        out[WIRE_HEADER_BYTES + i] = payload[i];
    }

    *out_len = total;
    return DEVLINK_FRAME_OK;
}

devlink_rx_verdict_t devlink_rx_handle(uint8_t header_type, uint16_t payload_len,
                                       const uint8_t *payload,
                                       devlink_dispatch_fn dispatch,
                                       devlink_rx_stats_t *stats)
{
    devlink_rx_verdict_t v = devlink_rx_verdict(header_type, payload_len, payload);

    if (stats != NULL) {
        switch (v) {
        case DEVLINK_RX_DISPATCH:           stats->dispatched++; break;
        case DEVLINK_RX_DROP_EMPTY:         stats->dropped_empty++; break;
        case DEVLINK_RX_DROP_TYPE_MISMATCH: stats->dropped_type_mismatch++; break;
        default: break;
        }
    }

    if (v == DEVLINK_RX_DISPATCH && dispatch != NULL) {
        /* ⚠ 传 **payload 原样**（含首字节），**不是** header。
         * msg_handler_process 的第一行是 data[0]（2.x 的类型约定），
         * 传 header 会让它把 header 的第 0 字节（ver）当成消息号。 */
        dispatch(payload, payload_len);
    }
    return v;
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
#include "uplink_arbiter.h"   /* task-21：上行仲裁轮询 */
#include "device_link_handshake.h"   /* 应用层握手的纯决策（IDF 无关）*/
/* task-33：3.0 链路的 nonce **不再自己生成**，改为向 2.x 握手 runtime 取
 * （决策 B′）。不 include 它会得到 implicit declaration —— 而这只在
 * **IDF 构建**里暴露：宿主 target 用 DEVICE_LINK_HOST_TEST=1 把整段胶水
 * 关掉了（宿主绿、目标红，正是本卡要消灭的形态；本轮实测又踩了一次）。 */
#include "hello_handshake.h"
#include "frame_codec.h"             /* Hello 的帧编码器 + MSG_HELLO/FRAME_OK */
#include "app_state.h"               /* app_state_get()->node_id（真实身份，非编造）*/
#include "msg_handler.h"             /* msg_handler_process：3.0 下行接进既有分发 */
#include "config_mgr.h"              /* epoch / has_manifest / last_known_manifest */
#include "collector_mem.h"           /* task-34：PSRAM 优先/内部兜底的放置策略（单一来源 P4）*/
#include "tls_esp.h"
#include "wifi_mgr.h"
#include "sntp_mgr.h"     /* IDF 无关的头（组件约束 C2），只在这里被胶水用到 */
#include "tls_guard.h"    /* 「时间是否可信」的单一来源（P4）—— 不再写第二份阈值 */

static const char *TAG = "DEV_LINK";

/** 链路任务栈。**TLS 握手与所有下行分发都在本任务里跑** ⇒ 不能按"普通轮询任务"给小栈。
 *
 * ## 8192 → 6144（task-34，**依据真机实测**）
 *
 * ### 实测数据（Lead，受控镜像：同源/同设备/180 s，只差 LINK_ENABLED）
 *     [stack] dev_link high_water=4024 bytes free   （t=61 s 与 t=121 s 两次一致）
 * 栈 8192 − 未用 4024 ⇒ **峰值 4168 B**。
 *
 * ⚠ uxTaskGetStackHighWaterMark 记录的是**历史最小剩余（累计）**，
 * 所以 t=61 s 的读数**已经包含**了 t≈3 s 那次 TLS 握手（以及 Hello/发布）的峰值
 * —— 握手正是本任务里最深的调用链（session_poll → tls_esp_connect →
 * esp_tls_conn_new_sync → mbedTLS 握手）。⇒ 4168 是**含握手**的峰值，不是空载值。
 *
 * ### 为什么不必再等"OTA 峰值"（我先前把它当硬前置，是**错的**）
 * 本任务**不会**执行 OTA 的实际工作：
 *   devlink_on_msg → msg_handler_process → handler_data_process_ota
 *     只**解析** OtaCmd，然后调 ota_start()；
 *   ota_start() 用 **xTaskCreateStatic** 另建 ota_task（ota.c:903，栈在 .bss），
 *   **下载与 esp_ota_write 全在那个任务里**。
 * ⇒ OTA 不落在本栈上，"先测 OTA 峰值再定栈"对本任务**不适用**。
 *
 * ### 取值与余量
 * 6144 / 4168 = **1.47x**，高于本仓**已被接受**的两个更紧的先例：
 *   status_task 5120/3996 = 1.28x（main.c:139）；hello_super 3072/2172 = 1.41x。
 * 取 6144 而非 5120：5120 只有 1.23x，**低于**本仓自己已接受的 1.28x。
 *
 * ### 为什么必须缩（而不是只搬缓冲）
 * 实测：link=n 稳态 largest=**23552**，link=y 稳态 largest=**15360**，差**恰好 8192**
 * —— 就是本栈（xTaskCreate 从堆里切走一整块连续内存）。
 * 而 s3p 的 floor 是 16384 ⇒ 被它压到线下 ⇒ ConfigManifest 被**永久拒绝**。
 * 缩 6144 可把 largest 抬回约 **17408**（越过 floor，余 1024）。
 *
 * ⚠ 余量只有 1024 B **是已知的**：
 *   - 若后续要给 status/hello_super 补栈（link=y 时只剩 1.11x/1.18x），
 *     **那 1024 B 不够**（两者合计需 1024~2048）⇒ 必须另找预算，
 *     不要把 B 的账记在 A2 腾出的额度上（见决策文档 §12）。
 *   - 若把本栈改为 **xTaskCreateStatic**：等价于把 8192 整块还给堆
 *     （largest 可回 ~23552），代价是 DIRAM +8192 超出 s3p 阈值 151500 ⇒
 *     需同步调 mem_budget.json 与基线文档，属独立决策。
 *
 * ⚠ 栈**必须内部 RAM**（不能像同卡的缓冲那样放 PSRAM）—— 但**理由更正**：
 * 我先前写"因为本任务执行 OTA"，**那是错的**（见上：OTA 在自己的任务里）。
 * 真正的理由是 s3p 上 CONFIG_SPIRAM_ALLOW_STACK_EXTERNAL_MEMORY=n，
 * 且 PSRAM 栈在 flash 写（cache 关闭）窗口内被调度会崩 —— 与本任务是否跑 OTA 无关。
 * 见决策文档 §3C。 */
#define DEVLINK_TASK_STACK 6144
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

/** 最近一条被定界出来的消息类型；**被读走一次就清 0**（一次性）。
 *  为什么要清：dlhs_decide 的 rx_type 语义是"**本轮刚收到的**那条"。
 *  若不清，一个 HelloAck 会在之后每一轮都触发一次 NOTE（幂等只是侥幸挡住）。 */
static volatile uint8_t s_last_rx_type;

/** 取走"本轮收到的类型"并清零（无则 0）。 */
static uint8_t devlink_take_rx_type(void)
{
    uint8_t t = s_last_rx_type;
    s_last_rx_type = 0;
    return t;
}

/** 3.0 下行路径计数（§118.4 要求的"可观测"）。周期日志见任务循环。 */
static devlink_rx_stats_t s_rx_stats;

/**
 * 收到一条**已被定界**的 3.0 消息。
 *
 * ## 这里**必须**把 payload 交给分发（曾经的死路）
 * 本函数原先只打一行日志 + 记 type，**从不调用 msg_handler_process**
 * ⇒ 后端经 3.0 链路下发的 0x22（重启/恢复出厂）、配置下发等
 * **被静默丢弃**：操作员点"重启"，设备毫无反应且**没有任何错误**。
 * 现在交给分发，并先做 §118.4 的**类型一致性校验**（见 devlink_rx_handle）。
 *
 * ## 为什么不再"完全不碰 payload"
 * 旧注释说不解析 payload（因为 3.0 payload 的业务布局仍是待确认项）——
 * 那条克制**仍然成立**：这里只碰**首字节**（2.x 的类型约定），
 * 其余一个字节都不解释，业务布局的解析完全交给既有 handler。
 *
 * ## 生命周期
 * rx_msg_t.payload 指向定界器内部缓冲，**回调返回后即失效**
 * （rx_pump.h:56）。因此**只在本次调用内**传给分发 —— 分发（msg_handler_process）
 * 是同步的，不保存该指针。需要跨调用保存状态的 handler（如 periph）
 * 自己往队列里拷贝，与本函数无关。
 *
 * ## ⚠ 并发（已实测，见 .c 顶部说明）
 * 本回调在**链路任务**上下文执行，而 MQTT 路径在 MQTT 任务上下文，
 * 两条路径**可以并发**进 msg_handler_process。msg_handler_process 本身
 * **无锁**（msg_handler.c:182-260 是一张纯 switch），逐 handler 的结论见文件顶部。
 */
static bool devlink_on_msg(const rx_msg_t *m, void *ctx)
{
    (void)ctx;
    ESP_LOGI(TAG, "rx 3.0 msg ver=0x%02X type=0x%02X seq=%u plen=%u",
             (unsigned)m->ver, (unsigned)m->type, (unsigned)m->seq,
             (unsigned)m->payload_len);

    /* 判定 + 分发。传 m->payload（**含首字节**）——这是关键：
     * msg_handler_process 的第一行是 data[0]。 */
    devlink_rx_verdict_t v = devlink_rx_handle(m->type, m->payload_len, m->payload,
                                               msg_handler_process, &s_rx_stats);
    if (v != DEVLINK_RX_DISPATCH) {
        /* 丢弃必须留下痕迹：静默丢弃正是本卡要修的那类缺陷。 */
        ESP_LOGW(TAG, "下行帧被丢弃：%s（header type=0x%02X plen=%u）",
                 devlink_rx_verdict_name(v), (unsigned)m->type,
                 (unsigned)m->payload_len);
    }

    /* ⚠ task-33 更正（这条注释写的时候是对的，引入第二条路径后不再成立）：
     *
     * 原话是"握手推进只需要类型这一个比特的信息"。在**只有一条**握手路径时
     * 成立：收到 0x12 就意味着握手成功。但 §138 真机实测证明它**不成立**了 ——
     * 应用层（handler_hello）会**拒绝**一条 stale nonce 的 HelloAck 并直接
     * return，而 msg_handler_process 返回 void ⇒ 派发层看不见"拒绝"，
     * 于是这里无条件把 type 记成 0x12 ⇒ device_link_handshake.c:33 只看
     * rx_type == MSG_HELLO_ACK ⇒ **READY 的判据变成"收到 0x12"而不是
     * "握手成功"**（P1：接口表达不了"接受了"）。
     * 真机症状正是同时打印"Rejecting stale nonce"与"⇒ READY"两条相反的结论。
     *
     * ⇒ 现在要求**两个**条件同时成立：类型是 HelloAck **且** 应用层确实
     *    接受了它（msg_handler_is_hello_ack_received 只在 nonce 通过校验时置真，
     *    handler_hello.c:137）。被拒的 ACK 不再推进握手 —— 链路会**如实**
     *    留在 WAIT_HANDSHAKE 并重试，而不是带着一个假的 READY 继续跑。 */
    if (m->type == MSG_HELLO_ACK && !msg_handler_is_hello_ack_received()) {
        /* ⚠ 不在这里读 session 状态：本函数在链路任务里被调，而 s_session 的
         * 声明在文件更下方（:604）。此处只报事实，状态由调用方在别处打印。 */
        ESP_LOGW(TAG, "HelloAck 被应用层拒绝（nonce 未通过校验）⇒ **不推进握手**，"
                      "等待重发/重连（READY 判据是「握手成功」，不是「收到 0x12」）");
    }

    /* ⚠ 这里**仍然**记录 rx_type（"收到了什么"是事实）；是否推进握手由
     * dlhs_decide 的 hello_ack_accepted 参数决定 —— 判据放在纯函数里，
     * 宿主测试才咬得住（§134 的教训：判据藏在 IDF 胶水里就等于没测）。 */
    s_last_rx_type = m->type;
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

    /* ⚠ 多分配 1 字节放 PEM 的 NUL 终止符（esp-tls 的契约，见文件上方
     * devlink_pem_terminate 的说明）。真机上漏了这一步的后果是
     * mbedtls_x509_crt_parse 返回 MBEDTLS_ERR_X509_INVALID_FORMAT。 */
    const size_t cap = devlink_pem_buf_bytes(need);
    uint8_t *buf = (uint8_t *)malloc(cap);
    if (buf == NULL) return ESP_ERR_NO_MEM;
    size_t got = need;
    err = nvs_get_blob(h, key, buf, &got);
    if (err != ESP_OK) { free(buf); return err; }

    size_t pem_len = 0;
    if (!devlink_pem_terminate(buf, cap, got, &pem_len)) {
        /* cap 是按 got 之前的值算的；NVS 返回的大小理论上不会变，
         * 但若变了（并发写同一 key），这里如实失败而不是溢出。 */
        ESP_LOGE(TAG, "证书 %s：缓冲不足（got=%u cap=%u）—— 拒绝使用",
                 key, (unsigned)got, (unsigned)cap);
        free(buf);
        return ESP_ERR_INVALID_SIZE;
    }
    /* out_len 是**含 NUL** 的长度：直接喂给 esp-tls 的 cacert_bytes/
     * clientcert_bytes/clientkey_bytes（IDF 要求含终止符）。 */
    *out = buf; *out_len = pem_len;
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
/* task-34：定界器缓冲（纯数据，可放 PSRAM，见 devlink_buf_place 的说明）。 */
static uint8_t   *s_delim_buf;

/* task-34：本型号是否真的有可用的外部 RAM 池。
 *
 * ⚠ 不直接读 CONFIG_SPIRAM：那是"编译期配了"，而这里要回答的是**运行期**
 * "这块板子真的有 PSRAM 吗"。s3p 的镜像理论上可以被刷到没有 PSRAM 的板子上
 * （配置写了 =y），此时 heap_caps 的外部池为 0 字节 ⇒ 应当如实落回内部，
 * 而不是分配失败后不启动链路。 */
static bool devext_psram_available(void)
{
    return heap_caps_get_total_size(MALLOC_CAP_SPIRAM) > 0;
}
static const char *s_state_txt = "NONE";
static sntp_mgr_state_t s_sntp_last = SNTP_MGR_DISABLED;

/* ── 应用层握手状态 ── */
static bool s_hello_sent;        /* **本连接代际**内是否已发过 Hello */
static session_state_t s_prev_state = SESSION_DOWN;  /* 用于判链路重建 */
static uint32_t s_hello_nonce;   /* 每次连接代际换一个新的 nonce */

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

/**
 * 构造并发送一条 0x01 Hello。
 *
 * ## 为什么不用 msg_handler_send_hello
 * 它**直接经 transport 发布**（走 MQTT/广播），拿不到字节，而 3.0 链路要的是
 * "把字节交给 session_send 从这条 socket 发出去"。任务边界也明确要求
 * **不要改动 msg_handler 的发布路径**。
 * ⇒ 这里用**同一个 frame 编码器**按 handler_hello.c:164-191 的同一组字段号
 *   构造一条最小 Hello。字段号取自 msg_handler_internal.h 的 hello_field_t
 *   （唯一来源，不在这里重抄数字）。
 *
 * ## 字段必须**完整**，不能只挑几个
 *
 * ⚠ 我第一版只发了 {1,2,3,8,9}，理由是"最小 Hello 够握手就行" —— **错的**。
 * 后端 handler_hello.go 的 required 是 **{1,2,3,4,5,6,8,9}**，
 * 缺任何一个 field 都会让 parseHello 直接返回 error（missing required field N）。
 * 而 FrameHandler 返回 error ⇒ 后端**不回 HelloAck** ⇒ 设备永远进不了 READY。
 * 实测症状：方向0a 通过（后端解出了 node_id），但"真实路由回 HelloAck 条数 = 0"。
 * ⇒ 字段集与 2.x 的 msg_handler_send_hello **逐字段对齐**（见下），
 *   值也取**同一批 getter**，不编造。
 *
 * 字段与取值（全部复用 2.x 的同一批来源）：
 *   1 node_id        = app_state_get()->node_id
 *   2 firmware_version = get_firmware_version()
 *   3 model          = get_model_name()
 *   4 channel_count  = config_mgr_get_active_channel_count()
 *   5 config_epoch   = config_mgr_get_epoch()
 *   6 nvs_has_config = config_mgr_has_manifest() ? 1 : 0
 *                      （**in-memory**，不是 NVS last_known —— 与
 *                       handler_hello.c:181-184 的口径一致，否则后端会跳过 push）
 *   7 last_manifest  = config_mgr_get_last_known_manifest_id()（非空才写）
 *   8 proto_ver      = "2.6"（设计 §0.2：本次不改版本字符串；旧后端严格相等，
 *                      改成 3.0 会失联）
 *   9 handshake_nonce = nonce（**必须非 0**：msg_handler_send_hello 与后端
 *                      parseHello 都拒绝 0）
 *
 * @return 写入 frame 的字节数；0 表示失败（nonce==0 或编码不下）。
 */
static size_t devlink_build_hello(uint8_t *frame, size_t cap, uint32_t nonce)
{
    if (nonce == 0) return 0;    /* 与 msg_handler_send_hello 同一条拒绝规则 */

    const app_state_t *st = app_state_get();
    const char *node_id = (st != NULL && st->node_id[0] != '\0') ? st->node_id : "";
    if (node_id[0] == '\0') {
        /* 没有身份就不发：后端 parseHello 要求 node_id 非空，发了也只会被拒。
         * 如实返回失败，让调用方打日志（不静默）。 */
        return 0;
    }

    frame_encoder_t enc;
    frame_encoder_init(&enc, frame, cap, MSG_HELLO);
    if (frame_encode_string(&enc, 1, node_id) != FRAME_OK) return 0;
    if (frame_encode_string(&enc, 2, get_firmware_version()) != FRAME_OK) return 0;
    if (frame_encode_string(&enc, 3, get_model_name()) != FRAME_OK) return 0;
    if (frame_encode_varint(&enc, 4, config_mgr_get_active_channel_count()) != FRAME_OK) return 0;
    if (frame_encode_varint(&enc, 5, config_mgr_get_epoch()) != FRAME_OK) return 0;
    if (frame_encode_varint(&enc, 6, config_mgr_has_manifest() ? 1 : 0) != FRAME_OK) return 0;
    const char *mid = config_mgr_get_last_known_manifest_id();
    if (mid != NULL && mid[0] != '\0') {
        if (frame_encode_string(&enc, 7, mid) != FRAME_OK) return 0;
    }
    if (frame_encode_string(&enc, 8, "2.6") != FRAME_OK) return 0;
    if (frame_encode_varint(&enc, 9, nonce) != FRAME_OK) return 0;
    return frame_encoder_size(&enc);
}

/**
 * 用 session_send 把一条帧完整发出去（按 link.h 的 progress 循环）。
 *
 * ⚠ **绝不重发整帧**：PARTIAL 表示"写了一部分"，必须从 *progress 处续写；
 *   重发已上线的字节会让接收端定界器看到重复片段而**无法自愈**（D-30）。
 *
 * @return true 整帧写出；false 失败（已如实打日志）。
 */
/* devlink_send_payload 定义在 devlink_send_frame 之前（成帧在发送之前读起来更顺），
 * 所以这里先声明它调用的那个。 */
static bool devlink_send_frame(const uint8_t *frame, size_t len, const char *what);

/* ⭐ 上行成帧缓冲（task-31）。
 *
 * 为什么需要一块独立缓冲：成帧是"头 + payload"，而 payload 由各消息自己的
 * 编码器写在自己的缓冲里（Hello 写在 devlink_task 的 hello[128]）。
 * 用一块 scratch 成帧，比让每个生产者各自预留 12 B 前缀更不容易漏
 * （漏了就是"少一个头"，正是本卡要修的那个缺陷）。
 *
 * 上界来自**当前唯一的上行生产者**：Hello 的编码缓冲是 128 B
 * （devlink_task 里的 uint8_t hello[128]）。这里给 256 B（一倍余量），
 * 超出会**响亮失败**而不是静默截断 —— 静默截断等于发出半条帧。 */
#define DEVLINK_TX_SCRATCH_PAYLOAD 256u

/**
 * ⭐ 把 payload **成帧后**发出去（task-31 修：此前全程不成帧）。
 *
 * 这是生产上行路径上**唯一**的成帧点。真机与对锚客户端走同一条路：
 *   devlink_send_payload -> devlink_encode_frame -> devlink_send_frame -> session_send
 *
 * ## 为什么必须在这里成帧（缺陷回顾）
 * 此前 devlink_send_frame 直接把 payload 交给 session_send ⇒ 线上没有 12 B 头、
 * 没有 magic，后端 protoframe.DecodeHeader 一律 ErrMagic 丢弃
 * ⇒ 设备永远进不了 READY，而**固件侧一处都不报错**。
 *
 * ## 判据（不是猜的，来源见 device_link_wiring.h 的说明）
 *   ver=0x30 / type=payload[0] / flags=0（不置 CRC 位）/ seq 由调用方给。
 *   后端 CRC 校验是条件式的（server.go:483），且后端自己下行也不置 CRC 位
 *   ⇒ 两端对称、CRC 非必需。
 *
 * @return true 整帧写出；false 失败（已如实打日志）。
 */
static bool devlink_send_payload(const uint8_t *payload, size_t len, const char *what)
{
    if (len == 0 || len > DEVLINK_TX_SCRATCH_PAYLOAD) {
        ESP_LOGE(TAG, "%s：上行载荷长度 %u 超出本路径上限 %u —— 拒绝发送"
                      "（宁可不发，也不发半条帧）",
                 what, (unsigned)len, (unsigned)DEVLINK_TX_SCRATCH_PAYLOAD);
        return false;
    }

    uint8_t framed[WIRE_HEADER_BYTES + DEVLINK_TX_SCRATCH_PAYLOAD];
    size_t framed_len = 0;
    int rc = devlink_encode_frame(framed, sizeof(framed), payload, len,
                                  /* seq */ 0u, &framed_len);
    if (rc != DEVLINK_FRAME_OK) {
        ESP_LOGE(TAG, "%s：成帧失败 rc=%s —— 不发（半条帧会让对端定界器错位）",
                 what, devlink_frame_err_name(rc));
        return false;
    }

    return devlink_send_frame(framed, framed_len, what);
}

static bool devlink_send_frame(const uint8_t *frame, size_t len, const char *what)
{
    size_t progress = 0;
    int rounds = 0;
    for (;;) {
        link_result_t r = session_send(s_session, frame, len, &progress);
        if (r == LINK_SENT_FULL) return true;
        if (r == LINK_SENT_PARTIAL) {
            /* 续写：progress 已被推进，直接再调一次 */
            if (++rounds > 64) {   /* 防御：避免病态下无限循环 */
                ESP_LOGE(TAG, "%s：续写 64 轮仍未完成（progress=%u/%u）",
                         what, (unsigned)progress, (unsigned)len);
                return false;
            }
            continue;
        }
        if (r == LINK_BACKPRESSURE) {
            /* 一字节没写出：**整帧稍后重试**（progress 未动，重发整帧是安全的）。
             * 本任务 10ms 一轮，下一轮自然重试 —— 这里不忙等。 */
            ESP_LOGW(TAG, "%s：发送缓冲满（背压），下一轮重试", what);
            return false;
        }
        /* 其余（NOT_READY / PAYLOAD_TOO_BIG / FATAL）：如实报，不静默 */
        ESP_LOGE(TAG, "%s：发送失败 rc=%s（progress=%u/%u）",
                 what, link_result_name(r), (unsigned)progress, (unsigned)len);
        return false;
    }
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

        /* task-21：推进上行仲裁（读 session READY 的**边沿**，喂给 transport_sel）。
         *
         * 为什么放在本任务：它是唯一持有 session 且周期运行的地方，能看到状态跃迁。
         * 边沿而非电平（见 uplink_arbiter.c 的说明）：把"持续未 READY"当电平反复喂，
         * 一个长重连期会被算成很多次失败，阈值语义失真。 */
        uplink_arbiter_poll();

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

        /* ── 应用层握手推进（决策是纯函数，见 device_link_handshake.h）──
         *
         * ⭐ 重连后必须**允许重发** Hello：若 hello_sent 只在启动时清零，
         * 那么第一次连接失败重连后，新链路上永远不发 Hello ⇒ 新连接永远进不了
         * READY ⇒ 表现为"第一次没连上就再也连不上"。
         * 判据用 dlhs_link_generation_changed（不在调用方自己发明）。 */
        if (dlhs_link_generation_changed(s_prev_state, st)) {
            s_hello_sent = false;
            /* ⭐ task-33（决策 B′）：nonce **不再由本文件生成**。
             *
             * 从前这里写 s_hello_nonce = esp_random() | 1 —— 而校验方
             * （handler_hello.c:129 → hello_handshake_notify_ack）只认 2.x
             * 握手 runtime 里 armed 的那个 nonce ⇒ 3.0 发出去的 Hello 的 ACK
             * **必然**被判 stale（真机 §138：应用层 Rejecting，链路层照样 READY）。
             *
             * 现在改为向**同一个** runtime 取 nonce：同一个分配器、同一个
             * armed_nonce 存储位，只是标记归属为"3.0 链路"。
             * ⇒ "本次握手的 nonce 归谁"只有一个答案（P4），校验方自然认识它。
             * 详见 docs/设计/决策-3.0-握手所有权-2026-10-07.md。 */
            hello_handshake_clear_link_nonce();   /* 旧代际的 arm 作废 */
            if (!hello_handshake_arm_link_nonce(&s_hello_nonce)) {
                /* 取不到 nonce 就**不发**：发一条无 nonce 的 Hello 只会被后端拒。
                 * 不静默 —— 这是"握手推不动"的根因，必须看得见。 */
                s_hello_nonce = 0;
                ESP_LOGE(TAG, "链路代际更新（%s -> %s）：**取不到 nonce**，"
                              "本轮不发 Hello（握手无法推进）",
                         session_state_name(s_prev_state), session_state_name(st));
            } else {
                ESP_LOGI(TAG, "链路代际更新（%s -> %s）：Hello 可重发，nonce=%u（来自握手 runtime）",
                         session_state_name(s_prev_state), session_state_name(st),
                         (unsigned)s_hello_nonce);
            }
        }
        s_prev_state = st;

        /* task-33（D-B）：把"应用层是否接受了那条 HelloAck"作为**显式**输入
         * 传给决策函数 —— 它只在 nonce 通过校验时才置真（handler_hello.c:137）。
         * 读一次存起来：dlhs_decide 与本轮日志要用同一个值，避免两次读取之间
         * 被另一个上下文改写。 */
        const bool ack_accepted = msg_handler_is_hello_ack_received();
        switch (dlhs_decide(st, s_hello_sent, devlink_take_rx_type(), ack_accepted)) {
        case DLHS_SEND_HELLO: {
            uint8_t hello[128];
            size_t hlen = devlink_build_hello(hello, sizeof(hello), s_hello_nonce);
            if (hlen == 0) {
                /* 不静默：构造失败就报出来（nonce==0 或编码不下） */
                ESP_LOGE(TAG, "Hello 构造失败（nonce=%u）—— 握手无法推进",
                         (unsigned)s_hello_nonce);
                break;
            }
            /* ⭐ task-31：必须走 devlink_send_payload（它负责成帧）。
             * 传 hello/hlen 给 devlink_send_frame 会**绕过分帧** ⇒
             * 线上没有 magic，后端一律 ErrMagic —— 这正是本卡修的缺陷，
             * 留这条注释是为了后人不会"顺手"改回去。 */
            if (devlink_send_payload(hello, hlen, "Hello(0x01)")) {
                s_hello_sent = true;   /* ⭐ 只有真的发出去了才记（背压时下一轮重试）*/
                ESP_LOGI(TAG, "已发 Hello(0x01) payload=%u B（已成帧 %u B，等待 HelloAck）",
                         (unsigned)hlen, (unsigned)(hlen + WIRE_HEADER_BYTES));
            }
            break;
        }
        case DLHS_NOTE_HANDSHAKE:
            /* ⭐ task-33：能走到这里 ⇒ 应用层**确实接受**了那条 HelloAck
             * （dlhs_decide 的规则 0 已经把"收到但被拒"的路径挡成 IDLE）。
             * ⇒ 这是进入 READY 的唯一途径，且判据是"握手成功"不是"帧到达"。 */
            session_note_handshake(s);
            ESP_LOGI(TAG, "HelloAck(0x12) 被应用层接受 ⇒ READY（退避计数归零）");
            break;
        case DLHS_DONE:
        case DLHS_IDLE:
        default:
            break;
        }

        /* ── 下行计数周期上报（§118.4 要求"计数要可观测"）──
         * 只在**有变化**时打，且限频：本任务 10ms 一轮，否则会淹掉串口。
         * 三个数一起打：只打"丢弃数"会让人以为链路只是安静，
         * 而"收到很多、派发 0"和"根本没收到"是完全不同的问题。 */
        {
            static devlink_rx_stats_t s_rx_stats_logged;
            static uint32_t s_rx_log_ms;
            bool changed = (s_rx_stats.dispatched != s_rx_stats_logged.dispatched) ||
                           (s_rx_stats.dropped_empty != s_rx_stats_logged.dropped_empty) ||
                           (s_rx_stats.dropped_type_mismatch != s_rx_stats_logged.dropped_type_mismatch);
            uint32_t now_ms = (uint32_t)(devlink_now_ms() & 0xFFFFFFFFu);
            if (changed && (s_rx_log_ms == 0u || (now_ms - s_rx_log_ms) >= 5000u)) {
                s_rx_log_ms = now_ms;
                s_rx_stats_logged = s_rx_stats;
                ESP_LOGI(TAG, "下行计数：派发=%u 空帧丢弃=%u 类型不一致丢弃=%u",
                         (unsigned)s_rx_stats.dispatched,
                         (unsigned)s_rx_stats.dropped_empty,
                         (unsigned)s_rx_stats.dropped_type_mismatch);
            }
        }

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

session_t *device_link_wiring_session(void)
{
    /* 只读句柄：所有权仍在本文件（见头文件）。未启用/未创建时为 NULL。
     * 注意**不加** #ifdef —— 默认构建下 s_session 恒为 NULL，
     * 于是本函数恒返回 NULL，行为可见且可测。 */
    return s_session;
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
        (uint32_t)CONFIG_EHOME_DEVICE_LINK_MAX_PAYLOAD,
        /* 并存判据：TLS IN 缓冲常驻内部 RAM，与定界器缓冲同时存活。 */
        (uint32_t)CONFIG_MBEDTLS_SSL_IN_CONTENT_LEN,
        variant_caps());
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

    /* ── 2) 读缓冲 + 定界器缓冲（task-34：放置交给"能否用 PSRAM"这一个维度）──
     *
     * 为什么把这两块放到 PSRAM：它们是 3.0 链路新增的**纯数据**缓冲
     * （2 KB + 4 KB），而 s3p 的内部连续块余量只剩约 7 KB ⇒ 实测差 512 字节
     * ⇒ ConfigManifest 被内存门禁永久拒绝。两块都不参与 DMA，也不在 flash 写
     * 期间被访问（字节都是 CPU 从 TLS 读入后喂给定界器的），所以放外部是安全的。
     *
     * ⚠ 分配失败**不静默降级**：降级会让"以为省下了内部 RAM、其实没有"再次发生，
     * 而那正是本卡要修的形态。分配失败就如实报错并**不启动链路** ——
     * 宁可链路不启用（可见），也不要一个悄悄吃内部 RAM 的链路。 */
    {
        const devlink_buf_place_t place = devlink_buf_place(devext_psram_available());
        const size_t rx_bytes = (size_t)CONFIG_EHOME_DEVICE_LINK_RX_BUF;
        const size_t delim_bytes =
            (size_t)device_link_delim_bytes((uint32_t)CONFIG_EHOME_DEVICE_LINK_MAX_PAYLOAD);

        if (place == DEVLINK_BUF_PLACE_PSRAM) {
            s_rx_buf = (uint8_t *)collector_mem_alloc_pref_psram(rx_bytes);
            s_delim_buf = (uint8_t *)collector_mem_alloc_pref_psram(delim_bytes);
        } else {
            s_rx_buf = (uint8_t *)malloc(rx_bytes);
            s_delim_buf = (uint8_t *)malloc(delim_bytes);
        }

        if (s_rx_buf == NULL || s_delim_buf == NULL) {
            ESP_LOGE(TAG, "链路缓冲分配失败（place=%s rx=%u delim=%u，internal free=%u largest=%u）"
                          " —— 不启动链路（不静默降级到内部，否则会再次吃光内部余量）",
                     devlink_buf_place_name(place), (unsigned)rx_bytes, (unsigned)delim_bytes,
                     (unsigned)heap_caps_get_free_size(MALLOC_CAP_INTERNAL),
                     (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL));
            collector_mem_free(s_rx_buf);
            collector_mem_free(s_delim_buf);
            s_rx_buf = NULL;
            s_delim_buf = NULL;
            devlink_free_certs();
            return;
        }
        ESP_LOGI(TAG, "链路缓冲放置=%s（rx=%u B、delim=%u B；internal largest 现在=%u）",
                 devlink_buf_place_name(place), (unsigned)rx_bytes, (unsigned)delim_bytes,
                 (unsigned)heap_caps_get_largest_free_block(MALLOC_CAP_INTERNAL));
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
    /* task-34：定界器缓冲也由这里提供（放在哪个池由 devlink_buf_place 决定）。 */
    scfg.delim_buf = s_delim_buf;
    scfg.delim_buf_cap = (size_t)device_link_delim_bytes(
        (uint32_t)CONFIG_EHOME_DEVICE_LINK_MAX_PAYLOAD);
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
