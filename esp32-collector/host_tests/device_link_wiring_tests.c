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
        CHECK(device_link_check_placement(256, vc) == DEVLINK_PLACE_OK,
              "%s：256 B 载荷应当放得下（连续上界 %u）",
              vc->name, (unsigned)vc->internal_contiguous_max);

        /* ★ 核心：判据是"最高载荷是否超型号连续上界"，
         *   期望值**从型号表推导**，不是我手填的数字。 */
        uint32_t need = device_link_delim_bytes(WIRE_PAYLOAD_MAX);
        devlink_place_t want = (need > vc->internal_contiguous_max)
                             ? DEVLINK_PLACE_EXCEEDS_CONTIGUOUS
                             : DEVLINK_PLACE_OK;
        CHECK(device_link_check_placement(WIRE_PAYLOAD_MAX, vc) == want,
              "%s：最大载荷 %u B 连续块 vs 型号上界 %u ⇒ 期望 %s，实际 %s",
              vc->name, (unsigned)need, (unsigned)vc->internal_contiguous_max,
              devlink_place_name(want),
              devlink_place_name(device_link_check_placement(WIRE_PAYLOAD_MAX, vc)));
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
    CHECK(device_link_check_placement(1024, NULL) == DEVLINK_PLACE_NO_VARIANT,
          "caps=NULL 应判 NO_VARIANT（不猜），实际 %s",
          devlink_place_name(device_link_check_placement(1024, NULL)));
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

int main(void)
{
    test_delim_bytes_matches_wire_constants();
    test_placement_across_all_three_variants();
    test_no_variant_is_refused_not_guessed();
    test_cert_verdict_three_states();
    test_names_are_nonempty();

    if (s_failures) { printf("device_link_wiring_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("device_link_wiring_tests: all checks passed\n");
    return 0;
}
