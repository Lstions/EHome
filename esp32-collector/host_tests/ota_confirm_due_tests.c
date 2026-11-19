/* OTA 回滚确认判据（ota_confirm_due）宿主用例 —— 方案 §6.6 / AC-15 / M3a·T3a.1。
 *
 * 为什么这条判据必须单独测
 * ------------------------
 * 引导器的语义是：「镜像处于 PENDING_VERIFY，只要应用没把它标成 VALID 就复位，
 * 下一次启动把它置 ABORTED 并自动退回旧分区」（bootloader_support/
 * bootloader_utility.c:396-400、445-447）。也就是说，本函数**直接决定设备会不会
 * 回滚**：
 *   · 判据太松（把安全模式、关键初始化未完成也算通过）⇒ 坏镜像被钉成 VALID，
 *     自动回滚彻底失效；
 *   · 判据太紧/掺入外部条件（联网、Hello 应答、首条 StatusReport）⇒ “应用正常但
 *     网络不通”的设备永远不确认，任何一次意外复位都被误滚回旧固件 —— 这正是
 *     DEF-4 的原始形态，也是 v1.1 方案被推翻的那一条。
 * 因此这里把**完整真值表**穷举一遍，并要求「4 个布尔输入 × uptime 两侧」只有
 * 唯一一种组合为真：以后谁往判据里“顺手加一个条件”，这条用例立刻变红。
 *
 * 测什么 / 不测什么
 * -----------------
 *   · 只测纯判据（无 I/O、无状态、无堆）。判据自身**没有任何网络入参**，签名被
 *     设计成 4 个本地事实就是为了让“断网也能确认”成为**结构属性**而不是纪律；
 *   · 调用点（main.c 的 ota_boot_validation_poll）与 ota_confirm_valid() 的真实
 *     副作用需要 IDF 的 otadata/分区表 ⇒ 只能在实机（FW-RB-01..04）验证。
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>

#include "ota.h"

static int failures;

#define CHECK(condition, message) do { \
    if (!(condition)) { \
        fprintf(stderr, "FAIL %s:%d: %s\n", __func__, __LINE__, (message)); \
        failures++; \
    } \
} while (0)

/* 30s 是“确认”与“过早确认”的分界线。改它必须同时改这里、ota.h 的注释与
 * docs/设计/固件OTA.md 的表述 —— 三处不一致就等于文档在撒谎。 */
static void test_confirm_delay_is_30s(void)
{
    CHECK(OTA_CONFIRM_DELAY_S == 30u, "OTA_CONFIRM_DELAY_S must stay 30s");
}

/* 结构断言：判据的签名被**钉死**为 4 个本地事实。若有人给判据加第 5 个参数
 * （例如“WiFi 已连接”“Hello 已应答”），下面这次赋值会**编译失败** —— 让
 * “确认依赖网络”卡在编译器这道关，而不是只靠注释与自觉（DEF-4 的原始形态
 * 正是“加了个看起来合理的条件”）。 */
static void test_signature_is_local_only(void)
{
    bool (*fn)(bool need_confirm, uint32_t uptime_sec, bool safe_mode, bool core_ready)
        = ota_confirm_due;
    CHECK(fn(true, (uint32_t)OTA_CONFIRM_DELAY_S, false, true),
          "predicate must keep its 4-local-facts signature");
}

/* 全部本地条件齐备 ⇒ 必须确认（这是“断网也能确认”的正向锚点）。 */
static void test_all_local_criteria_confirm(void)
{
    CHECK(ota_confirm_due(true, (uint32_t)OTA_CONFIRM_DELAY_S, false, true),
          "PENDING_VERIFY + uptime>=delay + not safe mode + core ready => confirm");
}

/* uptime 边界：29 不确认，30/31 确认；0 当然不确认。 */
static void test_uptime_boundary(void)
{
    CHECK(!ota_confirm_due(true, (uint32_t)OTA_CONFIRM_DELAY_S - 1u, false, true),
          "uptime=delay-1 must NOT confirm (too early)");
    CHECK(ota_confirm_due(true, (uint32_t)OTA_CONFIRM_DELAY_S, false, true),
          "uptime=delay must confirm (boundary is inclusive)");
    CHECK(ota_confirm_due(true, (uint32_t)OTA_CONFIRM_DELAY_S + 1u, false, true),
          "uptime=delay+1 must confirm");
    CHECK(!ota_confirm_due(true, 0u, false, true), "uptime=0 must NOT confirm");
}

/* 安全模式（启动熔断判定“正在降级运行/反复重启”）永远不确认：
 * 否则“降级但活着”的坏固件会被钉死（§6.6 实施要点 2）。 */
static void test_safe_mode_never_confirms(void)
{
    CHECK(!ota_confirm_due(true, (uint32_t)OTA_CONFIRM_DELAY_S, true, true),
          "safe mode must NOT confirm even when everything else is fine");
    CHECK(!ota_confirm_due(true, 3600u, true, true),
          "safe mode must NOT confirm after long uptime either");
}

/* 关键初始化未跑完不确认（app_main 卡在关键序列中途的形态）。 */
static void test_core_not_ready_never_confirms(void)
{
    CHECK(!ota_confirm_due(true, (uint32_t)OTA_CONFIRM_DELAY_S, false, false),
          "unfinished critical init must NOT confirm");
    CHECK(!ota_confirm_due(true, 3600u, false, false),
          "unfinished critical init must NOT confirm after long uptime either");
}

/* 只有 PENDING_VERIFY 的镜像才需要确认；普通启动（VALID / UNDEFINED）不该去写
 * otadata —— 那不是“多确认一次更保险”，而是给每次正常启动都加一次 flash 写。 */
static void test_not_pending_verify_never_confirms(void)
{
    CHECK(!ota_confirm_due(false, (uint32_t)OTA_CONFIRM_DELAY_S, false, true),
          "non-PENDING_VERIFY image must NOT confirm");
    CHECK(!ota_confirm_due(false, 3600u, false, true),
          "non-PENDING_VERIFY image must NOT confirm after long uptime either");
}

/* 穷举：need_confirm / safe_mode / core_ready / uptime>=delay 四个布尔维度，
 * 16 种组合，**只有一种**为真。任何新增条件（比如“WiFi 已连”）都会让
 * 为真的组合变成 0 种或多个 —— 这正是本用例要守住的。 */
static void test_full_truth_table_has_single_true(void)
{
    int confirmed = 0;
    int checked = 0;

    for (int need = 0; need < 2; need++) {
        for (int safe = 0; safe < 2; safe++) {
            for (int core = 0; core < 2; core++) {
                for (int late = 0; late < 2; late++) {
                    const uint32_t uptime = late
                        ? (uint32_t)OTA_CONFIRM_DELAY_S
                        : (uint32_t)OTA_CONFIRM_DELAY_S - 1u;
                    const bool want = (need != 0) && (safe == 0) && (core != 0) && (late != 0);
                    const bool got = ota_confirm_due(need != 0, uptime, safe != 0, core != 0);

                    checked++;
                    if (got != want) {
                        fprintf(stderr,
                                "FAIL %s:%d: need=%d safe=%d core=%d uptime=%u -> %d, want %d\n",
                                __func__, __LINE__, need, safe, core,
                                (unsigned)uptime, (int)got, (int)want);
                        failures++;
                    }
                    if (got) confirmed++;
                }
            }
        }
    }

    CHECK(checked == 16, "truth table must cover all 16 boolean combinations");
    CHECK(confirmed == 1, "exactly 1 of 16 input combinations may confirm");
}

int main(void)
{
    test_confirm_delay_is_30s();
    test_signature_is_local_only();
    test_all_local_criteria_confirm();
    test_uptime_boundary();
    test_safe_mode_never_confirms();
    test_core_not_ready_never_confirms();
    test_not_pending_verify_never_confirms();
    test_full_truth_table_has_single_true();

    if (failures != 0) {
        fprintf(stderr, "ota_confirm_due_tests: %d check(s) failed\n", failures);
        return 1;
    }
    puts("ota_confirm_due_tests: all tests passed");
    return 0;
}
