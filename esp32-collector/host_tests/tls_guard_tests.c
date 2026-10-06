/* tls_guard_tests.c —— mTLS 前置条件：时间可信性 + 失败分级
 *
 * 本用例把设计文档里标为"待实测"的风险 U6 / K11 变成**已证实并可测**：
 *
 *   3.0 用 mTLS 取代 MQTT 账号密码。mTLS 要校验 notBefore/notAfter，
 *   而本仓（2026-10-06 实测）：
 *     - sdkconfig.defaults* 里没有任何 SNTP 配置；
 *     - 全仓没有 esp_sntp_* / settimeofday / time(NULL) 调用；
 *     - sync_manager.c:308 的 get_time_sec() 返回的是开机秒数，不是墙上时间。
 *   => now(1970) < notBefore(2026) => 证书"尚未生效" => 握手必然失败。
 *
 * 最容易写错的一条（本文件重点锁住）：
 *   时间不可信时，**绝不能**把证书失败报成 FATAL —— 那会让设备永久放弃，
 *   而它其实只要校个时就能自愈。
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>

#include "tls_guard.h"

static int s_failures = 0;
#define CHECK(cond, ...)                                                     \
    do {                                                                     \
        if (!(cond)) {                                                       \
            printf("FAIL %s:%d  ", __FILE__, __LINE__);                      \
            printf(__VA_ARGS__);                                             \
            printf("\n");                                                    \
            s_failures++;                                                    \
        }                                                                    \
    } while (0)

/* ============ 1. 时间可信性：两端都要判 ============ */
static void test_time_trusted_bounds(void)
{
    /* 设备的真实处境：没有 SNTP 时系统时间 = 0（1970-01-01） */
    CHECK(!tls_guard_time_is_trusted(0),
          "**未校时（epoch 0）必须判为不可信** —— 这是本问题的根因");
    CHECK(!tls_guard_time_is_trusted(1), "开机 1 秒也不可信");
    /* 注意：1600000000 = 2020-09-13，**晚于**下限(2020-01-01) ⇒ 其实是可信的。
     * 我最初把它写成"不可信"是**测试自己的错** —— 算法没错。
     * 用一个真正早于下限的值（2019-12-31 = 1577836799）来测。 */
    CHECK(!tls_guard_time_is_trusted(TLS_GUARD_MIN_EPOCH - 1),
          "下限前一秒应不可信（2019-12-31）");
    CHECK(tls_guard_time_is_trusted(1600000000ULL),
          "2020-09-13 晚于下限，应可信（我最初写错了这条）");

    CHECK(tls_guard_time_is_trusted(TLS_GUARD_MIN_EPOCH), "恰为下限应可信");
    CHECK(tls_guard_time_is_trusted(1767225600ULL), "2026-01 应可信（本仓证书年代）");
    CHECK(tls_guard_time_is_trusted(TLS_GUARD_MAX_EPOCH), "恰为上限应可信");

    /* 上限也要判：只判下限会漏掉"时钟被设成很远的将来" */
    CHECK(!tls_guard_time_is_trusted(TLS_GUARD_MAX_EPOCH + 1),
          "超过上限应不可信（只判下限会漏掉这种）");
    CHECK(!tls_guard_time_is_trusted(0xFFFFFFFFFFFFFFFFULL), "溢出值应不可信");
}

/* ============ 2. ⭐ 核心：时间不可信 + 证书失败 ⇒ 先校时，不是 FATAL ============ */
static void test_untrusted_time_makes_cert_failure_recoverable(void)
{
    /* 这是"全站失联"与"自愈一次"的分界 */
    CHECK(tls_guard_classify(false, TLS_FAIL_CERT_EXPIRED) == TLS_ACTION_SYNC_TIME_FIRST,
          "时间不可信 + 证书'过期' 应 **先校时**（很可能是 1970 造成看起来过期）");
    CHECK(tls_guard_classify(false, TLS_FAIL_CERT_UNTRUSTED) == TLS_ACTION_SYNC_TIME_FIRST,
          "时间不可信 + 链不受信 应 **先校时**");

    /* 同样两种失败，在时间可信时就是真问题 ⇒ FATAL */
    CHECK(tls_guard_classify(true, TLS_FAIL_CERT_EXPIRED) == TLS_ACTION_FATAL,
          "时间可信 + 证书过期 才是真 FATAL");
    CHECK(tls_guard_classify(true, TLS_FAIL_CERT_UNTRUSTED) == TLS_ACTION_FATAL,
          "时间可信 + 链不受信 才是真 FATAL");

    /* 反向断言：绝不能把可自愈的情况报成 FATAL */
    CHECK(tls_guard_classify(false, TLS_FAIL_CERT_EXPIRED) != TLS_ACTION_FATAL,
          "**时间不可信的证书失败绝不能 FATAL** —— 那会让设备永久放弃");
}

/* ============ 3. 与时间无关的失败：分级不受时间影响 ============ */
static void test_time_independent_failures(void)
{
    /* 网络类：无论时间如何都退避重试 */
    CHECK(tls_guard_classify(false, TLS_FAIL_NETWORK) == TLS_ACTION_RETRY_BACKOFF,
          "网络失败应退避（与时间无关）");
    CHECK(tls_guard_classify(true, TLS_FAIL_NETWORK) == TLS_ACTION_RETRY_BACKOFF,
          "网络失败应退避（与时间无关）");

    /* 协议/配置：重试不会变，两种时间状态下都 FATAL */
    CHECK(tls_guard_classify(false, TLS_FAIL_PROTOCOL) == TLS_ACTION_FATAL, "协议失败应 FATAL");
    CHECK(tls_guard_classify(true,  TLS_FAIL_PROTOCOL) == TLS_ACTION_FATAL, "协议失败应 FATAL");
    CHECK(tls_guard_classify(false, TLS_FAIL_CONFIG)   == TLS_ACTION_FATAL, "配置缺失应 FATAL");
    CHECK(tls_guard_classify(true,  TLS_FAIL_CONFIG)   == TLS_ACTION_FATAL, "配置缺失应 FATAL");
}

/* ============ 4. 没失败就放行 ============ */
static void test_no_failure_proceeds(void)
{
    CHECK(tls_guard_classify(true, TLS_FAIL_NONE) == TLS_ACTION_PROCEED, "无失败应放行");
    /* 注意：时间不可信但**没失败**时也放行 —— 守卫不阻止首次尝试，
     * 只在失败后用于归因。这样"其实时间没问题只是没校"的情况不会被误挡。 */
    CHECK(tls_guard_classify(false, TLS_FAIL_NONE) == TLS_ACTION_PROCEED,
          "无失败应放行（守卫在失败【后】归因，不做前置阻断）");
}

/* ============ 5. 等价类扫全：不留下未定义组合 ============ */
static void test_all_combinations_defined(void)
{
    for (int t = 0; t < 2; t++) {
        for (int f = 0; f < TLS_FAIL_COUNT; f++) {
            tls_action_t a = tls_guard_classify(t == 1, (tls_failure_t)f);
            CHECK(a >= 0 && a < TLS_ACTION_COUNT,
                  "组合 (time_trusted=%d, failure=%s) 应给出有效动作，实际 %d",
                  t, tls_failure_name((tls_failure_t)f), (int)a);
        }
    }
    /* 枚举名必须可用（日志与告警要用） */
    for (int f = 0; f < TLS_FAIL_COUNT; f++) {
        CHECK(tls_failure_name((tls_failure_t)f)[0] != 'U' ||
              tls_failure_name((tls_failure_t)f)[1] != 'N',
              "失败名不应是 UNKNOWN");
    }
    for (int a = 0; a < TLS_ACTION_COUNT; a++) {
        CHECK(tls_action_name((tls_action_t)a) != NULL, "动作名不应为空");
    }
}

/* ============ 6. 计数：每档可见（P3） ============ */
static void test_stats_cover_every_branch(void)
{
    tls_guard_reset_stats();
    (void)tls_guard_note(true, TLS_FAIL_NONE);              /* proceed */
    (void)tls_guard_note(false, TLS_FAIL_CERT_EXPIRED);     /* sync_time_first */
    (void)tls_guard_note(true, TLS_FAIL_NETWORK);           /* retry_backoff */
    (void)tls_guard_note(true, TLS_FAIL_CERT_UNTRUSTED);    /* fatal */

    tls_guard_stats_t st;
    tls_guard_get_stats(&st);
    CHECK(st.proceed == 1, "proceed 应 1，实际 %u", st.proceed);
    CHECK(st.sync_time_first == 1, "sync_time_first 应 1，实际 %u", st.sync_time_first);
    CHECK(st.retry_backoff == 1, "retry_backoff 应 1，实际 %u", st.retry_backoff);
    CHECK(st.fatal == 1, "fatal 应 1，实际 %u", st.fatal);

    uint32_t sum = st.proceed + st.sync_time_first + st.retry_backoff + st.fatal;
    CHECK(sum == 4, "四个计数之和应等于 4（没有'其它'桶），实际 %u", sum);

    tls_guard_reset_stats();
    tls_guard_get_stats(&st);
    CHECK(st.proceed == 0 && st.fatal == 0, "复位后应全 0");
}

/* ============ 7. ⭐ 场景回归：设备无 SNTP 的真实序列 ============ */
static void test_field_scenario_without_sntp(void)
{
    /* 设备上电，无 SNTP ⇒ 系统时间 1970。第一次连 mTLS： */
    tls_guard_reset_stats();
    uint64_t device_time = 0;                    /* 没有 SNTP 的真实值 */
    bool trusted = tls_guard_time_is_trusted(device_time);
    CHECK(!trusted, "场景前提：无 SNTP 时时间不可信");

    /* 服务端证书 2026 年签发 ⇒ notBefore 在未来 ⇒ 校验报"未生效" */
    tls_action_t a1 = tls_guard_note(trusted, TLS_FAIL_CERT_EXPIRED);
    CHECK(a1 == TLS_ACTION_SYNC_TIME_FIRST,
          "第一步应是'先校时'，实际 %s", tls_action_name(a1));

    /* 校时成功后重试 */
    device_time = 1767225600ULL;                 /* 2026-01-01 */
    trusted = tls_guard_time_is_trusted(device_time);
    CHECK(trusted, "校时后时间应可信");
    tls_action_t a2 = tls_guard_note(trusted, TLS_FAIL_NONE);
    CHECK(a2 == TLS_ACTION_PROCEED, "校时后应放行，实际 %s", tls_action_name(a2));

    tls_guard_stats_t st;
    tls_guard_get_stats(&st);
    CHECK(st.sync_time_first == 1 && st.proceed == 1 && st.fatal == 0,
          "整个过程**不应出现 FATAL**（若出现即 K11：全站失联）");
}

int main(void)
{
    test_time_trusted_bounds();
    test_untrusted_time_makes_cert_failure_recoverable();
    test_time_independent_failures();
    test_no_failure_proceeds();
    test_all_combinations_defined();
    test_stats_cover_every_branch();
    test_field_scenario_without_sntp();

    if (s_failures) { printf("tls_guard_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("tls_guard_tests: all checks passed\n");
    return 0;
}
