/**
 * @file tls_guard.h
 * @brief mTLS 的**前置条件守卫**：时间可信性 + 失败分级
 *
 * ## 为什么需要它（本轮实测证实的设计风险 U6 / K11）
 *
 * 3.0 用 mTLS 取代 MQTT 的账号密码。mTLS 要校验证书的 notBefore/notAfter，
 * 而这需要**可信的墙上时间**。ESP32 没有 RTC，断电后系统时间回到 1970-01-01。
 *
 * 本仓现状（2026-10-06 实测）：
 *   - `sdkconfig.defaults*` 里**没有任何 SNTP 配置**；
 *   - 全仓**没有** `esp_sntp_*` / `settimeofday` / `time(NULL)` 调用；
 *   - `sync_manager.c:308` 的 `get_time_sec()` 返回的是
 *     `esp_timer_get_time()/1e6`，即**开机以来的秒数**，不是墙上时间。
 *
 * ⇒ 直接用 mTLS 的后果：now(1970) < notBefore(2026) ⇒ 证书"尚未生效"
 *   ⇒ **握手必然失败**。更糟的是失败**不可区分**：会被当成"网络问题"无限退避，
 *   日志里也看不出根因。这正是 K11 描述的"全站失联"。
 *
 * ## 本模块做什么
 * 1. **时间可信性判定**（纯函数，可宿主机测试）；
 * 2. **失败分级**：把"时间不可信导致的证书失败"与"证书真的不对"分开 ——
 *    前者可自愈（先校时再重试），后者必须人工。
 * 3. **不静默**：每档判定都有计数（P3），并明确下一步动作（P1）。
 *
 * ## 本模块【不】做什么
 * 不做 SNTP 本身（要 IDF），也不做 TLS I/O。它只回答一个问题：
 * **"现在能不能用 mTLS；不能用的话，下一步该做什么。"**
 * 这样这段最容易写错的判定逻辑可以在宿主机上被穷举测试。
 *
 * 本头文件【不依赖 IDF】，可在宿主编译并测试（约束 C2）。
 */
#ifndef EHOME_TLS_GUARD_H
#define EHOME_TLS_GUARD_H

#include <stdbool.h>
#include <stdint.h>

#ifdef __cplusplus
extern "C" {
#endif

/** 时间可信的下限：2020-01-01T00:00:00Z。
 *  为什么用固定常量而不是"当前时间 - N 年"：固件要能在多年后仍然正确工作，
 *  把"合理时间"锚在一个**过去**的固定点比锚在编译时刻更稳。 */
#define TLS_GUARD_MIN_EPOCH 1577836800ULL

/** 时间可信的上限：2100-01-01T00:00:00Z（挡住时钟被设成垃圾值/溢出）。 */
#define TLS_GUARD_MAX_EPOCH 4102444800ULL

/** 一次 TLS 尝试的失败原因（由 I/O 层归约后给出）。 */
typedef enum {
    TLS_FAIL_NONE = 0,          /* 没失败 */
    TLS_FAIL_NETWORK,           /* 连不上 / 超时 / 对端重置 —— 与证书无关 */
    TLS_FAIL_CERT_UNTRUSTED,    /* 证书链不信（CA 不匹配、自签、缺中间证书）*/
    TLS_FAIL_CERT_EXPIRED,      /* 证书过期或尚未生效 —— **与时间强相关** */
    TLS_FAIL_PROTOCOL,          /* TLS 版本/密码套件协商失败 */
    TLS_FAIL_CONFIG,            /* 本地配置缺失（没证书、没私钥、没 CA）*/
    TLS_FAIL_COUNT
} tls_failure_t;

const char *tls_failure_name(tls_failure_t f);

/** 判定后应该做什么（P1：每个取值对应调用方一个明确分支）。 */
typedef enum {
    TLS_ACTION_PROCEED = 0,       /* 时间可信，可以建立 mTLS */
    TLS_ACTION_SYNC_TIME_FIRST,   /* **先校时**再重试 —— 可自愈，别当故障 */
    TLS_ACTION_RETRY_BACKOFF,     /* 正常退避重试（网络类） */
    TLS_ACTION_FATAL,             /* 重试无用：证书/配置真的有问题，需人工 */
    TLS_ACTION_COUNT
} tls_action_t;

const char *tls_action_name(tls_action_t a);

/**
 * 时间是否可信（纯函数）。
 *
 * @param now_epoch 当前墙上时间（Unix 秒）。设备无 SNTP 时会是 0 或很小。
 *
 * 判定：MIN ≤ now ≤ MAX。区间**两端**都要判：
 *   - 只判下限会漏掉"时钟被设成 2100 年之后"（同样会让证书校验结果不可信）；
 *   - 只判上限会漏掉"没校时"（本问题的主因）。
 */
bool tls_guard_time_is_trusted(uint64_t now_epoch);

/**
 * ⭐ 核心：给定"时间是否可信"与"TLS 失败原因"，决定下一步动作。
 *
 * 规则表（由 host_tests/tls_guard_tests.c 逐条锁定）：
 *
 * | 失败原因 | 时间可信 | 动作 | 理由 |
 * |---|---|---|---|
 * | NONE | — | PROCEED | — |
 * | 任意**证书**类 | **否** | **SYNC_TIME_FIRST** | 时间错会让有效证书看起来"未生效/过期"，
 * |                |        |  | 此时**无法区分**真伪 ⇒ 先校时，可自愈 |
 * | CERT_UNTRUSTED | 是 | FATAL | 时间没问题，就是证书链不被信任 ⇒ 重试无用 |
 * | CERT_EXPIRED   | 是 | FATAL | 时间没问题，证书真的过期 ⇒ 需人工换证 |
 * | NETWORK | — | RETRY_BACKOFF | 与证书无关，退避即可 |
 * | PROTOCOL | — | FATAL | 版本/套件谈不拢，重试不会变 |
 * | CONFIG | — | FATAL | 本地就没装证书，重试无用 |
 *
 * **最关键的一条**：时间不可信时，**绝不能**把证书失败报成 FATAL
 * —— 那会让设备永久放弃，而它其实只要校个时就能自愈。
 */
tls_action_t tls_guard_classify(bool time_trusted, tls_failure_t failure);

/** 守卫的决策计数（P3：每档都要可见，否则"为什么一直连不上"无法回答）。 */
typedef struct {
    uint32_t proceed;
    uint32_t sync_time_first;
    uint32_t retry_backoff;
    uint32_t fatal;
} tls_guard_stats_t;

/** 记录一次判定（更新计数）。返回与 tls_guard_classify 相同的动作。 */
tls_action_t tls_guard_note(bool time_trusted, tls_failure_t failure);

void tls_guard_get_stats(tls_guard_stats_t *out);

/** 复位计数（测试用；设备上不需要）。 */
void tls_guard_reset_stats(void);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_TLS_GUARD_H */
