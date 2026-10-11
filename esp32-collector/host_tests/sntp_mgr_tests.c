/* sntp_mgr_tests.c —— SNTP 时间同步管理器
 *
 * 背景（§52 实测）：设备**没有墙上时间来源** ⇒ now(1970) < notBefore(2026)
 * ⇒ 证书"尚未生效" ⇒ mTLS 握手必然失败。tls_guard 能判定"该去校时"，
 * 但"真的去校时"这一步此前不存在 —— 本模块就是那一步。
 *
 * 本用例锁住三件事：
 *   1. 没网**不发起**（发起也没用）；
 *   2. 等待**必须有超时**（不能无限等）；
 *   3. 失败**退避逐步拉长**（不要对 NTP 打风暴）。
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>
#include <string.h>

#include "sntp_mgr.h"

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

/* ================= 假世界 ================= */
static uint64_t s_now_ms;
static uint64_t s_epoch;          /* 假"墙上时间"：0 = 未校时（1970）*/
static int      s_start_calls;
static char     s_last_server[64];
static uint64_t s_now_ms_fn(void *c) { (void)c; return s_now_ms; }
static bool     s_get_time(void *c, uint64_t *out) { (void)c; *out = s_epoch; return true; }
static void     s_start(void *c, const char *server)
{
    (void)c;
    s_start_calls++;
    if (server != NULL) {
        strncpy(s_last_server, server, sizeof(s_last_server) - 1);
        s_last_server[sizeof(s_last_server) - 1] = 0;
    }
}
static const sntp_mgr_io_t IO = {
    .start = s_start, .get_time = s_get_time, .now_ms = s_now_ms_fn,
};

/* 复用"可信"的判定口径：2020-01-01 之后（与 tls_guard 一致）*/
static bool trusted(uint64_t e) { return e >= 1577836800ULL && e <= 4102444800ULL; }

static void reset_world(void)
{
    s_now_ms = 0; s_epoch = 0; s_start_calls = 0; s_last_server[0] = 0;
}

static sntp_mgr_t *mk(const char *server)
{
    sntp_mgr_config_t cfg = {
        .io = &IO, .io_ctx = NULL,
        .server = server, .wait_ms = 0,
        .is_time_trusted = trusted,
    };
    return sntp_mgr_create(&cfg);
}

/* ============ 1. 没网**不发起** ============ */
static void test_no_network_does_not_start(void)
{
    reset_world();
    sntp_mgr_t *m = mk("pool.ntp.org");
    CHECK(m != NULL, "创建失败");

    /* 网络没起来：反复 poll 也不该发起 */
    for (int i = 0; i < 5; i++) { s_now_ms += 1000; sntp_mgr_poll(m, NULL); }
    CHECK(s_start_calls == 0,
          "网络未就绪**不得发起同步**（发了也没用），实际 %d 次", s_start_calls);
    CHECK(sntp_mgr_state(m) == SNTP_MGR_IDLE,
          "应停在 IDLE，实际 %s", sntp_mgr_state_name(sntp_mgr_state(m)));

    /* 网络起来后才发起，且**恰好一次** */
    sntp_mgr_network_up(m);
    sntp_mgr_poll(m, NULL);
    CHECK(s_start_calls == 1, "网络就绪后应发起 1 次，实际 %d", s_start_calls);
    CHECK(sntp_mgr_state(m) == SNTP_MGR_WAITING, "应进入 WAITING");

    /* WAITING 期间不应重复发起（否则会对 NTP 打风暴）*/
    for (int i = 0; i < 5; i++) { s_now_ms += 100; sntp_mgr_poll(m, NULL); }
    CHECK(s_start_calls == 1, "WAITING 期间不得重复发起，实际 %d", s_start_calls);

    sntp_mgr_destroy(m);
}

/* ============ 2. ⭐ 等待必须有**超时** ============ */
static void test_wait_times_out(void)
{
    reset_world();
    sntp_mgr_t *m = mk("pool.ntp.org");
    sntp_mgr_network_up(m);
    sntp_mgr_poll(m, NULL);
    CHECK(sntp_mgr_state(m) == SNTP_MGR_WAITING, "应 WAITING");

    /* 刚到超时前：仍在等 */
    s_now_ms = SNTP_MGR_DEFAULT_WAIT_MS - 1;
    sntp_mgr_poll(m, NULL);
    CHECK(sntp_mgr_state(m) == SNTP_MGR_WAITING,
          "差 1ms 应仍在等，实际 %s", sntp_mgr_state_name(sntp_mgr_state(m)));

    /* 恰好到期：必须判超时 */
    s_now_ms = SNTP_MGR_DEFAULT_WAIT_MS;
    sntp_mgr_poll(m, NULL);
    CHECK(sntp_mgr_state(m) == SNTP_MGR_BACKOFF,
          "**恰好到期必须判超时**（否则永远等下去），实际 %s",
          sntp_mgr_state_name(sntp_mgr_state(m)));

    sntp_mgr_stats_t st;
    sntp_mgr_get_stats(m, &st);
    CHECK(st.timeouts == 1, "应记 1 次超时，实际 %u", st.timeouts);
    sntp_mgr_destroy(m);
}

/* ============ 3. ⭐ 失败退避**逐步拉长** ============ */
static void test_backoff_grows(void)
{
    reset_world();
    sntp_mgr_t *m = mk("pool.ntp.org");
    sntp_mgr_network_up(m);

    uint64_t delays[3];
    for (int i = 0; i < 3; i++) {
        uint64_t t0 = s_now_ms;
        sntp_mgr_poll(m, NULL);              /* IDLE/BACKOFF -> 发起 */
        CHECK(sntp_mgr_state(m) == SNTP_MGR_WAITING,
              "第 %d 轮应 WAITING，实际 %s", i, sntp_mgr_state_name(sntp_mgr_state(m)));

        /* 等超时 */
        s_now_ms = t0 + SNTP_MGR_DEFAULT_WAIT_MS;
        sntp_mgr_poll(m, NULL);
        CHECK(sntp_mgr_state(m) == SNTP_MGR_BACKOFF, "超时后应 BACKOFF");

        /* 找出"到点重试"那一刻，测出退避时长 */
        uint64_t d = 0;
        for (uint64_t step = 100; step <= 1000000; step += 100) {
            s_now_ms = t0 + SNTP_MGR_DEFAULT_WAIT_MS + step;
            int before = s_start_calls;
            sntp_mgr_poll(m, NULL);
            if (s_start_calls > before) { d = step; break; }
        }
        delays[i] = d;
    }
    CHECK(delays[0] > 0, "首轮退避应 > 0，实际 %llu", (unsigned long long)delays[0]);
    CHECK(delays[1] > delays[0],
          "退避应**逐步拉长**：%llu -> %llu（不该固定值）",
          (unsigned long long)delays[0], (unsigned long long)delays[1]);
    CHECK(delays[2] >= delays[1], "退避不应回退：%llu -> %llu",
          (unsigned long long)delays[1], (unsigned long long)delays[2]);
    sntp_mgr_destroy(m);
}

/* ============ 4. ⭐ 成功即 SYNCED，且退避归零 ============ */
static void test_success_enters_synced_and_resets(void)
{
    reset_world();
    sntp_mgr_t *m = mk("pool.ntp.org");
    sntp_mgr_network_up(m);

    /* 先失败两轮，把退避堆起来 */
    for (int i = 0; i < 2; i++) {
        sntp_mgr_poll(m, NULL);
        s_now_ms += SNTP_MGR_DEFAULT_WAIT_MS;
        sntp_mgr_poll(m, NULL);
        s_now_ms += 1000000;   /* 跨过退避 */
    }

    /* 时间变对了（模拟 NTP 成功）*/
    s_epoch = 1767225600ULL;   /* 2026-01-01 */
    uint64_t seen = 0;
    sntp_mgr_poll(m, &seen);
    CHECK(sntp_mgr_state(m) == SNTP_MGR_SYNCED,
          "时间可信后应 SYNCED，实际 %s", sntp_mgr_state_name(sntp_mgr_state(m)));
    CHECK(seen == s_epoch, "应输出当前墙上时间");

    /* SYNCED 后不再发起（不需要了）*/
    int before = s_start_calls;
    for (int i = 0; i < 5; i++) { s_now_ms += 1000; sntp_mgr_poll(m, NULL); }
    CHECK(s_start_calls == before, "已同步后不应再发起，实际 %d -> %d",
          before, s_start_calls);

    sntp_mgr_stats_t st;
    sntp_mgr_get_stats(m, &st);
    CHECK(st.synced == 1, "应记 1 次同步成功，实际 %u", st.synced);
    sntp_mgr_destroy(m);
}

/* ============ 5. ⭐ 未配置服务器 -> DISABLED，不假装能同步 ============ */
static void test_no_server_is_disabled(void)
{
    reset_world();
    sntp_mgr_t *m = mk(NULL);
    CHECK(m != NULL, "创建应成功（配置缺失不是崩溃理由）");
    CHECK(sntp_mgr_state(m) == SNTP_MGR_DISABLED,
          "无服务器时应 DISABLED，实际 %s", sntp_mgr_state_name(sntp_mgr_state(m)));

    sntp_mgr_network_up(m);
    for (int i = 0; i < 5; i++) { s_now_ms += 100000; sntp_mgr_poll(m, NULL); }
    CHECK(s_start_calls == 0, "DISABLED 状态**永不发起**，实际 %d", s_start_calls);

    sntp_mgr_stats_t st;
    sntp_mgr_get_stats(m, &st);
    CHECK(st.timeouts == 0, "DISABLED 不应被计为超时失败");
    sntp_mgr_destroy(m);

    /* 空串等同未配置 */
    reset_world();
    sntp_mgr_t *m2 = mk("");
    CHECK(sntp_mgr_state(m2) == SNTP_MGR_DISABLED, "空串应等同未配置");
    sntp_mgr_destroy(m2);
}

/* ============ 6. ⭐ 断网不清退避（否则网络抖动会打 NTP 风暴）============ */
static void test_network_down_keeps_backoff(void)
{
    reset_world();
    sntp_mgr_t *m = mk("pool.ntp.org");
    sntp_mgr_network_up(m);

    /* 失败 3 轮，退避已拉长 */
    for (int i = 0; i < 3; i++) {
        sntp_mgr_poll(m, NULL);
        s_now_ms += SNTP_MGR_DEFAULT_WAIT_MS;
        sntp_mgr_poll(m, NULL);
        s_now_ms += 1000000;
    }
    sntp_mgr_poll(m, NULL);   /* 发起第 4 轮 */
    s_now_ms += SNTP_MGR_DEFAULT_WAIT_MS;
    sntp_mgr_poll(m, NULL);   /* 超时 */

    /* 断网再恢复 */
    sntp_mgr_network_down(m);
    CHECK(sntp_mgr_state(m) == SNTP_MGR_IDLE, "断网应回 IDLE");
    sntp_mgr_network_up(m);

    /* 恢复后**立即**重试次数不应回到"首轮就发起"的宽松节奏：
     * 我们要能观察到"退避计数没有被清"这个事实。
     * 判据：恢复后第一次 poll 会发起（因为已回落 IDLE），
     * 但若再次失败，退避应**继续拉长**而不是回到 5s。 */
    int before = s_start_calls;
    sntp_mgr_poll(m, NULL);
    CHECK(s_start_calls == before + 1, "恢复后应重新发起一次");

    uint64_t t0 = s_now_ms;
    s_now_ms = t0 + SNTP_MGR_DEFAULT_WAIT_MS;
    sntp_mgr_poll(m, NULL);   /* 再次超时 */
    CHECK(sntp_mgr_state(m) == SNTP_MGR_BACKOFF, "应再次 BACKOFF");

    uint64_t d = 0, d_first = 0;
    for (uint64_t step = 100; step <= 1000000; step += 100) {
        s_now_ms = t0 + SNTP_MGR_DEFAULT_WAIT_MS + step;
        int b = s_start_calls;
        sntp_mgr_poll(m, NULL);
        if (s_start_calls > b) { d = step; break; }
    }
    d_first = 5000;   /* 首轮退避是 5s */
    CHECK(d > d_first,
          "断网恢复后再次失败的退避应**大于首轮**（说明计数没被清零）：%llu vs %llu",
          (unsigned long long)d, (unsigned long long)d_first);
    sntp_mgr_destroy(m);
}

/* ============ 7. request_now：把退避**提前到点** ============ */
static void test_request_now_bypasses_backoff(void)
{
    reset_world();
    sntp_mgr_t *m = mk("pool.ntp.org");
    sntp_mgr_network_up(m);

    sntp_mgr_poll(m, NULL);
    s_now_ms += SNTP_MGR_DEFAULT_WAIT_MS;
    sntp_mgr_poll(m, NULL);
    CHECK(sntp_mgr_state(m) == SNTP_MGR_BACKOFF, "应 BACKOFF");

    /* 退避中（还远未到点）：此时 tls_guard 说"时间不对正在造成故障" */
    int before = s_start_calls;
    sntp_mgr_request_now(m);
    sntp_mgr_poll(m, NULL);
    CHECK(s_start_calls == before + 1,
          "**request_now 应把退避提前到点**（否则要等满退避），实际 %d -> %d",
          before, s_start_calls);
    CHECK(sntp_mgr_state(m) == SNTP_MGR_WAITING, "应重新 WAITING");

    sntp_mgr_stats_t st;
    sntp_mgr_get_stats(m, &st);
    CHECK(st.request_now == 1, "应记 1 次外部请求，实际 %u", st.request_now);
    sntp_mgr_destroy(m);
}

/* ============ 8. request_now 在没网时不应白试 ============ */
static void test_request_now_without_network(void)
{
    reset_world();
    sntp_mgr_t *m = mk("pool.ntp.org");
    int before = s_start_calls;
    sntp_mgr_request_now(m);        /* 网络还没起 */
    sntp_mgr_poll(m, NULL);
    CHECK(s_start_calls == before, "没网时 request_now 不应发起（实际 %d -> %d）",
          before, s_start_calls);
    sntp_mgr_destroy(m);
}

/* ============ 9. 参数校验 ============ */
static void test_create_validation(void)
{
    sntp_mgr_config_t c = { .io = &IO, .server = "x", .is_time_trusted = trusted };
    CHECK(sntp_mgr_create(NULL) == NULL, "NULL cfg 应拒绝");
    sntp_mgr_config_t c2 = c; c2.io = NULL;
    CHECK(sntp_mgr_create(&c2) == NULL, "缺 io 应拒绝");
    sntp_mgr_config_t c3 = c; c3.is_time_trusted = NULL;
    CHECK(sntp_mgr_create(&c3) == NULL, "缺可信判定应拒绝（否则无法判成功）");
}

int main(void)
{
    test_no_network_does_not_start();
    test_wait_times_out();
    test_backoff_grows();
    test_success_enters_synced_and_resets();
    test_no_server_is_disabled();
    test_network_down_keeps_backoff();
    test_request_now_bypasses_backoff();
    test_request_now_without_network();
    test_create_validation();

    if (s_failures) { printf("sntp_mgr_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("sntp_mgr_tests: all checks passed\n");
    return 0;
}
