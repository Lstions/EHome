/**
 * @file tls_esp.h
 * @brief 真 esp_tls 的薄适配：把 esp_tls 接成 link_tcp_io_t 的实现
 *
 * ## 这一层做什么
 * 前面几层已经把"判定"与"翻译"都做成可宿主测试的纯函数：
 *   - tls_io              : esp_tls 裸返回值 -> 四态归约（§56）
 *   - tls_link_adapt      : 四态 -> link_tcp_io_t 的 int 约定（§58）
 *   - tls_guard [+ reduce]: 时间可信性 + 失败分级 -> 下一步动作（§52/§54）
 *
 * 本层**只做三件事**，尽量不引入新判断：
 *   1. 调 esp_tls（init / conn_new_sync / read / write / destroy）；
 *   2. 把裸返回值交给上面三层去判；
 *   3. 把 hard_fatal / 连接句柄如实交回给 link_tcp。
 *
 * ## ⚠ 为什么"不做前置阻断"是有意的
 * `tls_guard` 的定位是**失败【后】归因**，不是前置闸门：
 * 时间不可信只说明"证书类失败很可能是 1970 造成的"，不说明"一定连不上"
 * （例如服务端根本没要求校验时间、或失败原因其实是网络）。
 * ⇒ 先尝试，失败后再按 (time_trusted, failure) 分类。
 * 若一上来就因"时间不可信"拒绝连接，反而会把**本来能通**的路径挡死。
 *
 * ## hard_fatal 的语义（设计 §4.2）
 *   true  -> 重试无意义（证书真不对 / 本地配置缺失），上层应停止盲目退避
 *   false -> 可重试（网络问题，**以及"时间不可信"**）
 *
 * ⭐ 最要紧的一条：**时间不可信导致的证书失败必须 hard_fatal=false**。
 * 报 true 就是 K11（设备永久放弃，而它其实只要校个时就能自愈）。
 *
 * 本组件依赖 IDF（esp_tls / esp_log），**不参与宿主测试**；
 * 它的判断部分已尽量下沉到上述可测层。
 */
#ifndef EHOME_TLS_ESP_H
#define EHOME_TLS_ESP_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "link_tcp.h"

#ifdef __cplusplus
extern "C" {
#endif

/** 证书材料。全部为 PEM 文本；长度含结尾 NUL 与否不限（esp_tls 按长度解析）。 */
typedef struct {
    const unsigned char *ca_pem;      size_t ca_len;       /* 服务端 CA（必填）*/
    const unsigned char *cert_pem;    size_t cert_len;     /* 客户端证书（mTLS 必填）*/
    const unsigned char *key_pem;     size_t key_len;      /* 客户端私钥（mTLS 必填）*/
} tls_esp_certs_t;

/** 连接配置。 */
typedef struct {
    const char *host;        /* 后端主机名（同时用于 SNI 与证书 CN 校验）*/
    uint16_t    port;        /* 设备端口，设计建议 8443 */
    int         timeout_ms;  /* 连接超时；<=0 表示用底层 socket 默认 */

    tls_esp_certs_t certs;

    /** 时间源（注入）。返回 Unix 秒；**取不到返回 0**（= 不可信，不在此钳制）。
     *  为什么要注入：a) 宿主/仿真可替；b) 把"时间从哪来"与"怎么判"解耦 ——
     *  判定逻辑在 tls_guard（可测），这里只负责取数。
     *
     *  ⚠ 括号里原写"（SNTP 尚未落地）"，**已过期**：SNTP 已落地
     *  （components/sntp_mgr，由 main/device_link_wiring.c 在配置 tls_esp
     *  **之前**创建，注入的就是它）。注入的理由本身仍然成立 —— 它使本模块
     *  不依赖具体时间源，宿主测试可给任意时间。 */
    uint64_t (*now_epoch)(void);
} tls_esp_config_t;

/** 不透明连接句柄（内含 esp_tls_t*）。 */
typedef struct tls_esp_conn tls_esp_conn_t;

/** 取本模块用的 link_tcp_io_t 实现（无状态，可直接传给 link_tcp_config_t.io）。 */
const link_tcp_io_t *tls_esp_io(void);

/** 创建配置对象（复制 cfg；证书缓冲需在连接生命周期内保持有效）。 */
tls_esp_config_t *tls_esp_config_new(const tls_esp_config_t *cfg);
void tls_esp_config_free(tls_esp_config_t *cfg);

/**
 * 建立 mTLS 连接（也是 link_tcp_io_t.connect 的实现）。
 * @param io_ctx     由 tls_esp_config_new 得到的配置
 * @param hard_fatal 输出：true = 重试无意义
 * @return 句柄；失败返回 NULL（此时 *hard_fatal 已按 tls_guard 的判定填好）
 *
 * 失败路径：
 *   - 配置缺失（无 CA / 无客户端证书）-> hard_fatal = **true**（重试不会长出证书）
 *   - 证书类失败且**时间不可信**       -> hard_fatal = **false**（先校时可自愈）
 *   - 证书类失败且时间可信             -> hard_fatal = **true**
 *   - 网络 / 未归类错误                -> hard_fatal = **false**（退避重试）
 */
void *tls_esp_connect(void *io_ctx, bool *hard_fatal);

/** 读（link_tcp_io_t.read 的实现）：四态由 tls_io + tls_link_adapt 决定。 */
int tls_esp_read(void *handle, uint8_t *buf, size_t cap);

/** 写（link_tcp_io_t.write 的实现）。 */
int tls_esp_write(void *handle, const uint8_t *data, size_t len);

/** 关闭（幂等）。 */
void tls_esp_close(void *handle);

/** 诊断计数。 */
typedef struct {
    uint32_t connects_ok;
    uint32_t connects_failed;
    uint32_t connects_hard_fatal;
    uint32_t connects_soft;        /* 失败但可重试（含"时间不可信"）*/
    uint32_t cert_fail_while_time_untrusted;  /* ⭐ K11 现场计数（应 >0 且不致命）*/
} tls_esp_stats_t;

void tls_esp_get_stats(tls_esp_stats_t *out);
void tls_esp_reset_stats(void);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_TLS_ESP_H */
