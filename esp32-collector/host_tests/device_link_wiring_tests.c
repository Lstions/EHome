/* device_link_wiring_tests.c —— 3.0 链路接线里的**判定**（宿主可测）
 *
 * ## 为什么这些断言值得存在
 *
 * 接线文件（main/device_link_wiring.c）里只有三件事**有判断**，其余是胶水。
 * 这三件事都是"错了会以很难查的形式回来"的那类：
 *
 * 1. **定界器要多少连续内存** —— wire_delim_create 一次分配
 *    (max_payload + 12 + 4)。算少了会在运行期炸；算多了会让型号放不下。
 * 2. **本型号放得下吗** —— 判据必须是 largest 类的 internal_contiguous_max，
 *    不是 free。OTA 那次"free=18464 但 largest=7680 ⇒ 8KB 静态栈失败"
 *    就是拿 free 判连续块的下场。
 * 3. **证书材料判定** —— 空、超上限、可用，三态必须分开：
 *    把"没铺开"和"配错了上限"混成一个错误，会让人往错方向修。
 *
 * 另外：文件里刻意**没有** #ifdef 掉整段实现，就是为了不出现
 * "门禁说可达、固件里却没有这段代码"的假绿。本测试用
 * -DDEVICE_LINK_HOST_TEST=1 只编译纯判定部分。
 */
#include <stdbool.h>
#include <stdint.h>
#include <stdio.h>

#include "device_link_wiring.h"

/* 测试用的 TLS IN 缓冲字节数。
 *
 * ⚠ 为什么**不能**填 0 就完事：0 表示"不评估并存约束"，会让本用例在
 * (7152, 23536] 这个**真实危险区间**上给出假的 OK（见 §125）。
 * 宿主构建没有 Kconfig，所以这里给一个**与三型号出厂配置一致**的实值
 * （实测 s3/s3p/c6 的 CONFIG_MBEDTLS_SSL_IN_CONTENT_LEN 都是 16384）。 */
#ifndef TLS_IN_TEST
#define TLS_IN_TEST 16384u
#endif
#include "variant.h"
#include "wire.h"

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

/* ════════ 1. 定界器连续字节数：必须与 wire 的常量一致（P5 唯一来源）════════ */
static void test_delim_bytes_matches_wire_constants(void)
{
    /* 不能在这里再写一遍 12 和 4 —— 那正是 P5 禁止的"同一语义两处定义"。
     * 用 wire.h 的常量表达期望值：上游改了头长，这里会跟着动，
     * 而不是留下一个不会红的魔数。 */
    const uint32_t hdr = (uint32_t)WIRE_HEADER_BYTES;
    const uint32_t crc = (uint32_t)WIRE_CRC_BYTES;

    CHECK(device_link_delim_bytes(4096) == 4096 + hdr + crc,
          "4096 载荷的定界缓冲应为 4096+%u+%u，实际 %u",
          hdr, crc, device_link_delim_bytes(4096));

    /* 关键性质：**上界与 TLS 记录的关系**
     * wire.h 把 WIRE_PAYLOAD_MAX 定成 16384-12-4，就是为了让"一条完整消息
     * 恰好放进一个 TLS 记录"这句话成真。所以取最大载荷时，整个连续块
     * 必须 <= 16384。这条断言把这个性质**钉死**：谁改大了头或 CRC，
     * 这里立刻红，而不是等 TLS 层出现"记录装不下"的怪现象。 */
    CHECK(device_link_delim_bytes(WIRE_PAYLOAD_MAX) <= 16384u,
          "最大载荷的定界缓冲(%u) 必须 <= 单个 TLS 记录(16384)",
          device_link_delim_bytes(WIRE_PAYLOAD_MAX));
    CHECK(device_link_delim_bytes(WIRE_PAYLOAD_MAX) == 16384u,
          "按 wire.h 的定义应当恰好等于 16384，实际 %u",
          device_link_delim_bytes(WIRE_PAYLOAD_MAX));

    /* 边界：0 载荷也要占 头+CRC */
    CHECK(device_link_delim_bytes(0) == hdr + crc, "0 载荷应仍占 %u B", hdr + crc);
}

/* ════════ 2. 放置判定：三型号必须**显式**被区分（P8）════════
 *
 * P8：型号差异只影响资源放置。这条断言就是用"同一份固件逻辑、
 * 三种型号给出不同结论"来证明差异确实只落在放置上。 */
static void test_placement_across_all_three_variants(void)
{
    const variant_id_t ids[3] = { VARIANT_S3, VARIANT_S3P, VARIANT_C6 };

    for (int i = 0; i < 3; i++) {
        const variant_caps_t *vc = variant_caps_for(ids[i]);
        CHECK(vc != NULL, "型号 %d 的能力表应存在", (int)ids[i]);
        if (vc == NULL) continue;

        /* 小载荷在任何型号上都该放得下（否则就是上界配得太小） */
        CHECK(device_link_check_placement(256, TLS_IN_TEST, vc) == DEVLINK_PLACE_OK,
              "%s：256 B 载荷应当放得下（连续上界 %u）",
              vc->name, (unsigned)vc->internal_contiguous_max);

        /* ★ 核心：期望值**从型号表推导**，不是我手填的数字。
         * ⚠ 现在是**两条判据**：先判单笔（定界器），再判并存（定界器 + TLS IN）——
         *   顺序不可交换，且两个失败原因必须能区分（上界数字完全不同）。 */
        uint32_t need = device_link_delim_bytes(WIRE_PAYLOAD_MAX);
        devlink_place_t want;
        if (need > vc->internal_contiguous_max) {
            want = DEVLINK_PLACE_EXCEEDS_CONTIGUOUS;
        } else if (need + TLS_IN_TEST > vc->internal_contiguous_max) {
            want = DEVLINK_PLACE_EXCEEDS_COEXIST;
        } else {
            want = DEVLINK_PLACE_OK;
        }
        CHECK(device_link_check_placement(WIRE_PAYLOAD_MAX, TLS_IN_TEST, vc) == want,
              "%s：最大载荷 %u B（+TLS %u B）vs 型号上界 %u ⇒ 期望 %s，实际 %s",
              vc->name, (unsigned)need, (unsigned)TLS_IN_TEST,
              (unsigned)vc->internal_contiguous_max,
              devlink_place_name(want),
              devlink_place_name(device_link_check_placement(WIRE_PAYLOAD_MAX, TLS_IN_TEST, vc)));
    }

    /* 三型号都必须真的不同 —— 若哪天表被抄成一样，说明"型号差异"名存实亡 */
    const variant_caps_t *a = variant_caps_for(VARIANT_S3);
    const variant_caps_t *b = variant_caps_for(VARIANT_S3P);
    const variant_caps_t *c = variant_caps_for(VARIANT_C6);
    CHECK(!(a && b && a->internal_contiguous_max == b->internal_contiguous_max &&
            b->internal_contiguous_max == c->internal_contiguous_max),
          "三型号的连续上界不应完全相同 —— 相同就说明型号差异没有真正被表达出来");
}

/* ════════ 3. 取不到型号能力时**不猜** ════════ */
static void test_no_variant_is_refused_not_guessed(void)
{
    /* 默默用一个默认上界，会把"型号表没接上"伪装成"内存刚好够"，
     * 之后在真机上以随机失败的形式回来。必须显式拒绝。 */
    CHECK(device_link_check_placement(1024, TLS_IN_TEST, NULL) == DEVLINK_PLACE_NO_VARIANT,
          "caps=NULL 应判 NO_VARIANT（不猜），实际 %s",
          devlink_place_name(device_link_check_placement(1024, TLS_IN_TEST, NULL)));
}

/* ════════ 3b. ⭐ 并存判据：守卫不许在"真机必炸"的区间上说 OK ════════
 *
 * 这条用例的存在理由（2026-10-07 实测发现的真实缺口）：
 * 旧守卫只看**单笔**连续块（定界器 = max_payload+16）对型号上界，
 * 而这条链路还要**同时**持有一个 mbedTLS 记录缓冲（三型号都是内部 RAM）。
 * 于是存在一个区间：**守卫说 OK，运行期 TLS 必然失败**。
 *
 * 用 S3/S3P 的 23552 B 上界算：
 *   单笔判据放行到 max_payload ≤ 23536
 *   并存判据只能到        max_payload ≤ 7152
 * ⇒ (7152, 23536] 就是那个"守卫骗人"的区间。
 * 本用例把它钉死：该区间内必须判 EXCEEDS_COEXIST，且**原因可与单笔区分**。 */
static void test_coexist_is_guarded_not_just_single_block(void)
{
    const variant_caps_t *vc = variant_caps_for(VARIANT_S3);
    CHECK(vc != NULL, "S3 能力表应存在");
    if (vc == NULL) return;

    const uint32_t L = vc->internal_contiguous_max;       /* 23552（实测） */
    const uint32_t mp_single_ok  = L - 16u;               /* 单笔判据的上限 */
    const uint32_t mp_coexist_ok = L - TLS_IN_TEST - 16u; /* 并存判据的上限 */
    CHECK(mp_coexist_ok < mp_single_ok,
          "并存上限应严格小于单笔上限（否则本用例没有区分度）");

    /* ① 并存上限之内 ⇒ OK（且必须真的能同时放下两笔） */
    CHECK(device_link_check_placement(mp_coexist_ok, TLS_IN_TEST, vc) == DEVLINK_PLACE_OK,
          "max_payload=%u 恰好放得下（单笔+TLS=%u ≤ 上界 %u）",
          (unsigned)mp_coexist_ok,
          (unsigned)(device_link_delim_bytes(mp_coexist_ok) + TLS_IN_TEST), (unsigned)L);

    /* ② ⭐ 超并存上限 1 字节 ⇒ 必须判 EXCEEDS_COEXIST（这是本用例的核心） */
    CHECK(device_link_check_placement(mp_coexist_ok + 1u, TLS_IN_TEST, vc)
              == DEVLINK_PLACE_EXCEEDS_COEXIST,
          "max_payload=%u：单笔放得下但**并存放不下** ⇒ 必须拒绝，实际 %s",
          (unsigned)(mp_coexist_ok + 1u),
          devlink_place_name(device_link_check_placement(mp_coexist_ok + 1u, TLS_IN_TEST, vc)));

    /* ③ 危险区间里任取一点都必须被拒（不是只挡边界） */
    CHECK(device_link_check_placement((mp_coexist_ok + mp_single_ok) / 2u, TLS_IN_TEST, vc)
              == DEVLINK_PLACE_EXCEEDS_COEXIST,
          "危险区间中点必须被拒 —— 否则就是'守卫说 OK、真机才炸'");

    /* ④ ⭐ 两个失败原因必须**可区分**：单笔超界报 CONTIGUOUS，并存超界报 COEXIST。
     *    合成一个取值会让操作员不知道该把 MAX_PAYLOAD 调小到多少。 */
    CHECK(device_link_check_placement(mp_single_ok + 1u, TLS_IN_TEST, vc)
              == DEVLINK_PLACE_EXCEEDS_CONTIGUOUS,
          "单笔超界应报 EXCEEDS_CONTIGUOUS（不是 COEXIST）");
    CHECK(DEVLINK_PLACE_EXCEEDS_CONTIGUOUS != DEVLINK_PLACE_EXCEEDS_COEXIST,
          "两个失败取值必须不同（否则日志里分不清是哪种超界）");

    /* ⑤ 反向对照：tls_in_bytes==0 ⇒ 只评估单笔（退化为旧行为）。
     *    这条**不是**说"传 0 就安全"，而是钉住"无 TLS 构建的语义没被改坏"。 */
    CHECK(device_link_check_placement(mp_single_ok, 0u, vc) == DEVLINK_PLACE_OK,
          "tls_in_bytes=0（无 TLS 构建）时单笔判据仍应放行到单笔上限");
    CHECK(device_link_check_placement(mp_single_ok + 1u, 0u, vc)
              == DEVLINK_PLACE_EXCEEDS_CONTIGUOUS,
          "tls_in_bytes=0 时超单笔界仍应报 CONTIGUOUS");
}

/* ════════ 4. 证书三态必须分开 ════════ */
static void test_cert_verdict_three_states(void)
{
    CHECK(devlink_cert_check(0, 4096) == DEVLINK_CERT_EMPTY,
          "长度 0 应为 EMPTY（没铺开），实际 %s",
          devlink_cert_name(devlink_cert_check(0, 4096)));

    CHECK(devlink_cert_check(4096, 4096) == DEVLINK_CERT_OK,
          "恰好等于上限应判 OK（边界含端点）");
    CHECK(devlink_cert_check(1, 4096) == DEVLINK_CERT_OK, "1 B 应判 OK");

    CHECK(devlink_cert_check(4097, 4096) == DEVLINK_CERT_TOO_BIG,
          "超上限 1 B 应判 TOO_BIG（不放宽上限），实际 %s",
          devlink_cert_name(devlink_cert_check(4097, 4096)));

    /* ⭐ 顺序性质：上限被配成 0 时，"空"仍须判 EMPTY 而不是 TOO_BIG。
     * 两者修复方向完全不同：EMPTY=去铺证书，TOO_BIG=去改上限。
     * 混成一个错误会让人往错方向修。 */
    CHECK(devlink_cert_check(0, 0) == DEVLINK_CERT_EMPTY,
          "cap=0 且长度 0 仍应是 EMPTY，实际 %s",
          devlink_cert_name(devlink_cert_check(0, 0)));
    CHECK(devlink_cert_check(1, 0) == DEVLINK_CERT_TOO_BIG,
          "cap=0 且长度 1 应是 TOO_BIG");
}

/* ════════ 5. 判定名字（日志/诊断会打印，不能是空串）════════ */
static void test_names_are_nonempty(void)
{
    const devlink_cert_t cv[3] = { DEVLINK_CERT_OK, DEVLINK_CERT_EMPTY, DEVLINK_CERT_TOO_BIG };
    for (int i = 0; i < 3; i++) {
        CHECK(devlink_cert_name(cv[i])[0] != '\0', "证书判定名不应为空");
    }
    const devlink_place_t pv[3] = { DEVLINK_PLACE_OK, DEVLINK_PLACE_NO_VARIANT,
                                    DEVLINK_PLACE_EXCEEDS_CONTIGUOUS };
    for (int i = 0; i < 3; i++) {
        CHECK(devlink_place_name(pv[i])[0] != '\0', "放置判定名不应为空");
    }
    /* 未知值不得崩、也不得返回 NULL */
    CHECK(devlink_cert_name((devlink_cert_t)99) != NULL, "未知枚举应返回非 NULL");
    CHECK(devlink_place_name((devlink_place_t)99) != NULL, "未知枚举应返回非 NULL");
}

/* ════════ 6. SNTP 接线：网络边沿判定（task-11）════════
 *
 * 为什么值得单列：sntp_mgr_network_down() 把状态压回 IDLE。若接线代码**每轮**
 * 都通知一次 down（而不是只在边沿），状态机就永远回不到"可以发起"的那一步
 * —— 而这份代码看起来正在认真接线，也没有任何一处会报错。
 * 把"只在边沿通知"锁在宿主测试里，是这条契约唯一能被真正守住的形态。 */
static void test_net_edge_only_on_change(void)
{
    /* 无变化 -> 不通知（这正是"每轮都调 down"会踩的坑） */
    CHECK(devlink_net_edge(false, false) == DEVLINK_NET_EDGE_NONE,
          "一直是 down 不应重复通知");
    CHECK(devlink_net_edge(true, true) == DEVLINK_NET_EDGE_NONE,
          "一直是 up 不应重复通知");

    /* 两个方向的边沿 */
    CHECK(devlink_net_edge(false, true) == DEVLINK_NET_EDGE_UP,
          "down->up 必须是 UP");
    CHECK(devlink_net_edge(true, false) == DEVLINK_NET_EDGE_DOWN,
          "up->down 必须是 DOWN");

    /* ⭐ 顺序性质：同一状态重复喂进来，只有**第一次**给出边沿。
     * 模拟接线任务的循环。 */
    bool prev = false;
    devlink_net_edge_t e = devlink_net_edge(prev, true);
    CHECK(e == DEVLINK_NET_EDGE_UP, "首次变 up 应给出 UP");
    prev = true;
    for (int i = 0; i < 5; i++) {
        e = devlink_net_edge(prev, true);
        CHECK(e == DEVLINK_NET_EDGE_NONE, "保持 up 时第 %d 轮不应再通知", i + 1);
        prev = true;
    }
    e = devlink_net_edge(prev, false);
    CHECK(e == DEVLINK_NET_EDGE_DOWN, "变 down 应给出 DOWN");
    prev = false;
    for (int i = 0; i < 5; i++) {
        e = devlink_net_edge(prev, false);
        CHECK(e == DEVLINK_NET_EDGE_NONE, "保持 down 时第 %d 轮不应再通知", i + 1);
        prev = false;
    }
}

/* ════════ 7. SNTP 接线：now_epoch 的取值规则（task-11）════════
 *
 * 两条契约：
 *   ① 取不到时间必须给 0 —— 绝不猜一个"看起来合理"的值；
 *   ② **不做区间钳制** —— 阈值只有 tls_guard 一处（P4）。
 * ② 单独测，是因为"顺手钳一下"看起来更安全，实则会让 tls_guard 的判定
 * 失去意义，且阈值出现第二个答案。 */
static void test_now_epoch_never_fabricates_and_never_clamps(void)
{
    /* ① 取不到 => 0。即使调用方传来一个垃圾非零值也必须归零。 */
    CHECK(devlink_now_epoch_value(false, 0u) == 0u, "取不到时间应为 0");
    CHECK(devlink_now_epoch_value(false, 1759999999u) == 0u,
          "取不到时间时，传进来的任何值都必须被丢弃（不得冒充可信时间）");
    CHECK(devlink_now_epoch_value(false, UINT64_MAX) == 0u,
          "取不到时间 + 溢出值 => 0");

    /* ② 取得到 => 原样透传，**包括** tls_guard 会判为不可信的那些值。
     *    适配器若在这里钳制/判定，阈值就有了第二个来源（违反 P4）。 */
    const uint64_t passthrough[] = {
        1u,                          /* 1970 + 1s：tls_guard 会判不可信 */
        1577836799u,                 /* MIN-1：边界外 */
        1577836800u,                 /* MIN  ：恰好可信 */
        4102444800u,                 /* MAX  ：恰好可信 */
        4102444801u,                 /* MAX+1：边界外，tls_guard 判不可信 */
        UINT64_MAX,                  /* 溢出值：也不得被适配器改写 */
    };
    for (size_t i = 0; i < sizeof(passthrough) / sizeof(passthrough[0]); i++) {
        CHECK(devlink_now_epoch_value(true, passthrough[i]) == passthrough[i],
              "取得到时间时必须原样透传 %llu（不在适配器里判可信/钳制）",
              (unsigned long long)passthrough[i]);
    }
}

int main(void)
{
    test_delim_bytes_matches_wire_constants();
    test_placement_across_all_three_variants();
    test_no_variant_is_refused_not_guessed();
    test_coexist_is_guarded_not_just_single_block();
    test_cert_verdict_three_states();
    test_names_are_nonempty();
    test_net_edge_only_on_change();
    test_now_epoch_never_fabricates_and_never_clamps();

    if (s_failures) { printf("device_link_wiring_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("device_link_wiring_tests: all checks passed\n");
    return 0;
}
