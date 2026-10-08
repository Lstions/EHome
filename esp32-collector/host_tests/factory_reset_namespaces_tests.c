/* factory_reset_namespaces_tests.c —— 2026-10-08
 *
 * ## 为什么要有它
 * `factory_reset` 的命名空间白名单决定"出厂重置**擦掉什么**" —— 一个破坏性判定。
 * 而它原先藏在 `factory_reset.c` 的 static 数组里：
 *   · 该文件依赖 nvs_flash/gpio/freertos ⇒ **零宿主覆盖**（覆盖率门禁里的未覆盖项）；
 *   · `tests/test_factory_reset_whitelist.c` 只能**复制**一份名单，
 *     且复制错了 —— `{wifi_mgr, config_mgr, ota}` vs 生产的 `{wifi_cfg, config, ota}`
 *     ⇒ 那份"测试"通过 19/19 是真的，但**测的是副本，与生产无关**（§166.4）。
 *
 * ⇒ 抽成纯模块后，本用例**直接引用生产的那份数据**（`FACTORY_RESET_NAMESPACES`），
 *   任何改动都会让它当场红。
 *
 * ⚠ 本用例**刻意断言具体名字**（而不是只断言"有 3 项"）：
 *   只数个数的话，把 wifi_cfg 换成随便什么都能过 —— 而那正是要防的事。
 */
#include <stdio.h>
#include <string.h>

#include "factory_reset_namespaces.h"

static int s_failures = 0;
#define CHECK(cond, msg) do { \
    if (!(cond)) { printf("  FAIL %s\n", msg); s_failures++; } \
    else         { printf("  ok   %s\n", msg); } \
} while (0)

/* ① 白名单必须**恰好**是生产期望的那三个，且顺序不参与语义（用集合比较）。
 * 它凭什么会失败：把任何一项改名/增删，本用例立刻红。
 * 为什么断言具体名字：这三个名字各自对应一类设备配置，改错了会**丢用户数据**。 */
static void test_whitelist_is_exactly_expected(void)
{
    static const char *const want[] = { "wifi_cfg", "config", "ota" };
    const size_t want_n = sizeof(want) / sizeof(want[0]);

    CHECK(FACTORY_RESET_NAMESPACE_COUNT == want_n,
          "白名单项数必须与生产期望一致");

    for (size_t i = 0; i < want_n; i++) {
        bool found = false;
        for (size_t j = 0; j < FACTORY_RESET_NAMESPACE_COUNT; j++) {
            if (strcmp(want[i], FACTORY_RESET_NAMESPACES[j]) == 0) { found = true; break; }
        }
        char msg[96];
        snprintf(msg, sizeof(msg), "白名单必须含 \"%s\"", want[i]);
        CHECK(found, msg);
    }
}

/* ② 判定函数：白名单内的**必须**返回 true。 */
static void test_should_erase_accepts_whitelisted(void)
{
    for (size_t i = 0; i < FACTORY_RESET_NAMESPACE_COUNT; i++) {
        CHECK(factory_reset_namespace_should_erase(FACTORY_RESET_NAMESPACES[i]),
              "白名单内的命名空间必须判定为可擦");
    }
}

/* ③ ⭐ 最要紧的一条：**白名单外**的必须返回 false。
 * 它凭什么会失败：把 should_erase 写成恒 true，本用例立刻红。
 * 为什么最要紧：恒 true 会把**用户不该丢的**命名空间一起擦掉 —— 破坏性。 */
static void test_should_erase_rejects_others(void)
{
    CHECK(!factory_reset_namespace_should_erase("eh_tls"),
          "证书命名空间 eh_tls **不在**白名单 ⇒ 不得擦（擦了就再也连不上后端）");
    CHECK(!factory_reset_namespace_should_erase("crash"),
          "崩溃记录不在白名单 ⇒ 不得擦");
    CHECK(!factory_reset_namespace_should_erase("wifi_mgr"),
          "wifi_mgr 是**旧测试副本里的错名**（生产是 wifi_cfg）⇒ 不得擦");
    CHECK(!factory_reset_namespace_should_erase("config_mgr"),
          "config_mgr 同上，是副本里的错名 ⇒ 不得擦");
}

/* ④ 失败安全：NULL / 空串一律**不擦**。
 * 它凭什么会失败：把 NULL 当"匹配"处理（例如去掉 ns==NULL 检查），本用例立刻红。
 * 为什么重要：出厂重置是破坏性操作，"判不出来就别擦"比"判不出来就擦"安全得多。 */
static void test_should_erase_fails_safe_on_null(void)
{
    CHECK(!factory_reset_namespace_should_erase(NULL), "NULL ⇒ 不得擦（失败安全）");
    CHECK(!factory_reset_namespace_should_erase(""), "空串 ⇒ 不得擦（失败安全）");
}

/* ⑤ 前缀不得误匹配（strcmp 而非 strncmp/strstr）。
 * 它凭什么会失败：把比较改成 strncmp(ns, wl, strlen(wl))，本用例立刻红。 */
static void test_should_erase_is_exact_match(void)
{
    CHECK(!factory_reset_namespace_should_erase("config_backup"),
          "前缀相同的长名不得匹配（必须整串相等）");
    CHECK(!factory_reset_namespace_should_erase("ota2"),
          "ota2 不得匹配 ota（必须整串相等）");
}

int main(void)
{
    printf("=== factory_reset 白名单（2026-10-08 抽出为可测模块）===\n");
    test_whitelist_is_exactly_expected();
    test_should_erase_accepts_whitelisted();
    test_should_erase_rejects_others();
    test_should_erase_fails_safe_on_null();
    test_should_erase_is_exact_match();
    printf("=== %s（失败 %d）===\n", s_failures == 0 ? "PASS" : "FAIL", s_failures);
    return s_failures == 0 ? 0 : 1;
}