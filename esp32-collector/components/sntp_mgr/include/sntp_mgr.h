/**
 * @file sntp_mgr.h
 * @brief SNTP 时间同步管理器 —— mTLS 的**前置条件**，也是 SYNC_TIME_FIRST 的落点
 *
 * ## 为什么必须有它（§52 已实测证实）
 * 设计 §4.3 把"设备时间可信"标为 U6，风险 K11。我实测后确认：
 *   - sdkconfig 里没有任何 SNTP 开关；
 *   - 全仓没有 esp_sntp_* / settimeofday / time(NULL) 调用;
 *   - sync_manager.c:308 的 get_time_sec() 返回的是**开机秒数**，不是墙上时间。
 * ⇒ 设备**根本没有墙上时间来源**。于是：
 *      now(1970) < notBefore(2026) ⇒ 证书"尚未生效" ⇒ mTLS 握手必然失败。
 *
 * 与此同时 tls_guard 已经能判定 "时间不可信 + 证书类失败 ⇒ 先校时"
 * （TLS_ACTION_SYNC_TIME_FIRST），但**下一步"真的去校时"并不存在**。
 * 本模块就是那一步。
 *
 * ## 本模块负责什么
 * 「尽量把时间弄对」，仅此而已。它是一个**尽力而为**的状态机：
 *   - 网络起来后才发起（没网时发起只是浪费）；
 *   - 等待有**超时**（不能无限等）；
 *   - 失败后**退避重试**（不要对 NTP 服务器打风暴）；
 *   - 成功即进入 SYNCED（时间可信由 tls_guard 判定，**单一来源**）。
 *
 * ## ⭐ 本模块【不】阻断 TLS
 * 时间不可信**不构成**"不许连"的理由：
 *   - 证书校验可能本来就不需要可信时间（例如服务端不校时效）；
 *   - 失败原因也可能是网络，与时间无关。
 * ⇒ tls_guard 的定位是**失败【后】归因**，不是前置闸门。
 *   本模块只负责"把时间尽量弄对"，**不是**"时间不对就不许连"。
 *   若做成前置闸门，反而会挡死本可通的路。
 *
 * ## 宿主可测
 * 时钟、NTP 发起、取时间**全部注入** ⇒ 状态机与超时/退避逻辑可在宿主穷举，
 * 真 esp_sntp 调用放在依赖 IDF 的薄适配里。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_SNTP_MGR_H
#define EHOME_SNTP_MGR_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/** 默认：等待同步结果的上限（毫秒）。超时即视为本轮失败，进退避。
 *  取值理由：NTP 往返通常 < 1s；30s 已足够宽松，又不至于让调用方卡住。 */
#define SNTP_MGR_DEFAULT_WAIT_MS 30000u

/** 退避表（毫秒）：5s, 15s, 60s, 300s, 900s（上限）。
 *  为什么不像 TCP 那样从 1s 起：NTP 服务器多为公共池，
 *  短退避会造成无谓的重复查询，且校时本身不紧急。 */
#define SNTP_MGR_BACKOFF_STEPS 5u

typedef enum {
    SNTP_MGR_IDLE = 0,      /* 网络未就绪：**不发起** */
    SNTP_MGR_WAITING,       /* 已发起，等在途结果 */
    SNTP_MGR_SYNCED,        /* 时间已可信（由 is_time_trusted 判定） */
    SNTP_MGR_BACKOFF,       /* 本轮失败：退避中，到点再发起 */
    SNTP_MGR_DISABLED,      /* 被显式关闭（例如配置无 NTP 服务器） */
} sntp_mgr_state_t;

const char *sntp_mgr_state_name(sntp_mgr_state_t s);

/**
 * 注入的 I/O（真实现调 esp_sntp_*；宿主测试用假实现）。
 */
typedef struct {
    /** 发起一次 SNTP 同步（应幂等：重复调用不出错）。 */
    void (*start)(void *ctx, const char *server);

    /** 取当前墙上时间（Unix 秒）。取不到返回 false。
     *  注意：设备无 SNTP 时这里通常给出 0（1970）。 */
    bool (*get_time)(void *ctx, uint64_t *epoch_out);

    /** 单调毫秒时钟。 */
    uint64_t (*now_ms)(void *ctx);
} sntp_mgr_io_t;

/** 判定"时间是否可信"的注入点。
 *  为什么要注入：**单一来源**（P4）—— 复用 tls_guard 的判定，
 *  而不是在本模块再写一遍"多大算合理"。 */
typedef bool (*sntp_time_trusted_fn)(uint64_t epoch);

typedef struct {
    const sntp_mgr_io_t *io;
    void                *io_ctx;

    const char          *server;      /* NTP 服务器名；NULL/空 => DISABLED */
    uint32_t             wait_ms;    /* 等待上限；0 => 用默认 */
    sntp_time_trusted_fn is_time_trusted;  /* 必填：复用 tls_guard 的判定 */
} sntp_mgr_config_t;

typedef struct sntp_mgr sntp_mgr_t;

/** 创建。io / is_time_trusted 必填（缺失返回 NULL，不造半成品）。 */
sntp_mgr_t *sntp_mgr_create(const sntp_mgr_config_t *cfg);
void        sntp_mgr_destroy(sntp_mgr_t *m);

/** 网络就绪（WiFi 拿到 IP）：此后才允许发起同步。 */
void sntp_mgr_network_up(sntp_mgr_t *m);

/** 网络断开：回到 IDLE。**不重置退避计数**（断网不是"同步失败"）。 */
void sntp_mgr_network_down(sntp_mgr_t *m);

/**
 * ⭐ 外部请求"现在去校时"（由 tls_guard 判定 SYNC_TIME_FIRST 时调用）。
 * 与"网络起来时自动发起"的区别：这会把退避**提前到点**（立即尝试），
 * 因为此时我们**知道**时间不对正在造成真实故障。
 */
void sntp_mgr_request_now(sntp_mgr_t *m);

/**
 * 推进一次。
 * @param epoch_out 可选：当前墙上时间（取不到则不写）
 * @return 当前状态
 *
 * 确定性行为（由 host_tests/sntp_mgr_tests.c 逐条锁定）：
 *  - 网络未就绪 -> IDLE，**不调用** io->start；
 *  - 网络就绪且 IDLE -> 发起（start 恰好一次）-> WAITING；
 *  - WAITING 期间 is_time_trusted 为真 -> SYNCED（退避计数归零）；
 *  - WAITING 超时 wait_ms -> BACKOFF（计数 +1）；
 *  - BACKOFF 到点 -> 重新发起；
 *  - 未配置 server -> DISABLED（**永不发起**，且不计失败）；
 *  - 断网 -> IDLE，**不清退避计数**。
 */
sntp_mgr_state_t sntp_mgr_poll(sntp_mgr_t *m, uint64_t *epoch_out);

sntp_mgr_state_t sntp_mgr_state(const sntp_mgr_t *m);

/** 取当前墙上时间（不可信也返回，调用方自行判定）。 */
bool sntp_mgr_now(const sntp_mgr_t *m, uint64_t *epoch_out);

/** 诊断计数（P3：每条路径都要可见）。 */
typedef struct {
    uint32_t starts;          /* 发起同步次数 */
    uint32_t synced;          /* 进入 SYNCED 次数（可能 >1：时间回退后又同步） */
    uint32_t timeouts;        /* 等待超时次数 */
    uint32_t request_now;     /* 外部紧急请求次数 */
    uint32_t skipped_no_net;  /* 因网络未就绪而未发起（说明网络还没好） */
} sntp_mgr_stats_t;

void sntp_mgr_get_stats(const sntp_mgr_t *m, sntp_mgr_stats_t *out);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_SNTP_MGR_H */
