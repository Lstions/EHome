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
#include <string.h>

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


/* ════════ task-31：生产上行**成帧**的字节级断言 ════════
 *
 * ## 为什么这些断言必须打在**字节**上
 *
 * 生产上行此前全程不成帧（devlink_send_frame 直接把 payload 交给 session_send），
 * 而"函数能返回 0"这类断言**对它完全无感**：不成帧的路径也能返回成功。
 * 所以这里逐字节检查：magic、ver、type、flags、seq、payload_len、载荷位置，
 * 以及空/超限载荷被**拒绝**（而不是发出畸形帧）。
 */
static void test_frame_bytes_are_exactly_the_wire_contract(void)
{
    /* 载荷首字节就是消息类型（2.x 约定），后端 manager.go:418 强校验它 == header.type */
    const uint8_t payload[] = { 0x01, 0xAA, 0xBB, 0xCC };
    uint8_t out[WIRE_HEADER_BYTES + sizeof(payload)];
    size_t n = 0;

    int rc = devlink_encode_frame(out, sizeof(out), payload, sizeof(payload),
                                  /* seq */ 0x11223344u, &n);
    CHECK(rc == DEVLINK_FRAME_OK, "成帧应成功，实际 rc=%s", devlink_frame_err_name(rc));
    CHECK(n == WIRE_HEADER_BYTES + sizeof(payload),
          "线上总长应=%u，实际 %u", (unsigned)(WIRE_HEADER_BYTES + sizeof(payload)), (unsigned)n);

    /* 逐字节：大端 12 B 头 + 载荷原样。这些字面量来自 protocol/vectors/frame_header.txt
     * 的约定与 protoframe/frame.go 的字段布局，**不**从任何本文件内的常量推导 ——
     * 用被测代码自己的常量去推期望值，等于没测。 */
    const uint8_t want[] = {
        0x45, 0x48,             /* magic "EH" */
        0x30,                   /* ver 3.0 */
        0x01,                   /* type == payload[0] */
        0x00, 0x00,             /* flags = 0（不置 CRC32C 位）*/
        0x11, 0x22, 0x33, 0x44, /* seq 大端 */
        0x00, 0x04,             /* payload_len = 4 大端 */
        0x01, 0xAA, 0xBB, 0xCC, /* 载荷原样 */
    };
    for (size_t i = 0; i < sizeof(want); i++) {
        CHECK(out[i] == want[i], "第 %u 字节 = 0x%02X，期望 0x%02X",
              (unsigned)i, out[i], want[i]);
    }
}

static void test_frame_type_tracks_payload_first_byte(void)
{
    /* type 必须是 payload[0]，不是常量、不是 MSG_HELLO。
     * 后端 manager.go:418 对不上就丢，且只打一条 warn（现场像"设备没反应"）。 */
    const uint8_t types[] = { 0x01, 0x04, 0x22, 0x23 };
    for (size_t i = 0; i < sizeof(types); i++) {
        uint8_t payload[3] = { types[i], 0x01, 0x02 };
        uint8_t out[WIRE_HEADER_BYTES + sizeof(payload)];
        size_t n = 0;
        int rc = devlink_encode_frame(out, sizeof(out), payload, sizeof(payload), 0u, &n);
        CHECK(rc == DEVLINK_FRAME_OK, "type=0x%02X 成帧应成功", types[i]);
        CHECK(out[3] == types[i],
              "header.type 应 == payload[0] = 0x%02X，实际 0x%02X", types[i], out[3]);
        CHECK(out[11] == 0x03, "payload_len 低字节应为 3，实际 %u", out[11]);
    }
}

static void test_frame_rejects_bad_input_instead_of_emitting_garbage(void)
{
    uint8_t out[64];
    size_t n = 12345;   /* 故意预置：被拒绝时它必须**不被改写** */

    /* 空载荷：没有首字节 ⇒ 取不出 type。宁可不发，也不发 type=0 的畸形帧。 */
    CHECK(devlink_encode_frame(out, sizeof(out), (const uint8_t *)"", 0, 0u, &n)
              == DEVLINK_FRAME_ERR_BAD_ARG,
          "空载荷必须被拒绝（空指针或长度 0）");
    CHECK(n == 12345, "被拒绝时 out_len 不得被改写，实际 %u", (unsigned)n);

    const uint8_t payload[4] = { 0x01, 0, 0, 0 };

    /* 缓冲放不下整条帧：必须拒绝，而不是发出**截断的**帧
     * （截断帧会让对端定界器错位 —— 比不发更糟）。 */
    CHECK(devlink_encode_frame(out, WIRE_HEADER_BYTES + sizeof(payload) - 1,
                               payload, sizeof(payload), 0u, &n) == DEVLINK_FRAME_ERR_CAP,
          "cap 少 1 字节时必须拒绝");

    /* 载荷超上界：上界取 wire.h 的唯一来源 WIRE_PAYLOAD_MAX（P5）。 */
    static uint8_t big[WIRE_PAYLOAD_MAX + 1u];
    big[0] = 0x01;
    CHECK(devlink_encode_frame(out, sizeof(out), big, sizeof(big), 0u, &n)
              == DEVLINK_FRAME_ERR_TOO_BIG,
          "载荷超过 WIRE_PAYLOAD_MAX 必须被拒绝");

    /* NULL 参数 */
    CHECK(devlink_encode_frame(NULL, sizeof(out), payload, sizeof(payload), 0u, &n)
              == DEVLINK_FRAME_ERR_BAD_ARG, "out=NULL 必须被拒绝");
    CHECK(devlink_encode_frame(out, sizeof(out), NULL, 4, 0u, &n)
              == DEVLINK_FRAME_ERR_BAD_ARG, "payload=NULL 必须被拒绝");
    CHECK(devlink_encode_frame(out, sizeof(out), payload, sizeof(payload), 0u, NULL)
              == DEVLINK_FRAME_ERR_BAD_ARG, "out_len=NULL 必须被拒绝");
}

static void test_frame_max_payload_boundary_is_accepted(void)
{
    /* 边界：正好 WIRE_PAYLOAD_MAX 必须**成功**（拒绝它就是把上界算错了一位）。
     * 缓冲按需分配，避免在宿主栈上放 16 KB。 */
    static uint8_t payload[WIRE_PAYLOAD_MAX];
    static uint8_t out[WIRE_HEADER_BYTES + WIRE_PAYLOAD_MAX];
    payload[0] = 0x04;
    payload[WIRE_PAYLOAD_MAX - 1] = 0x5A;

    size_t n = 0;
    int rc = devlink_encode_frame(out, sizeof(out), payload, sizeof(payload), 7u, &n);
    CHECK(rc == DEVLINK_FRAME_OK, "最大载荷应被接受，实际 rc=%s", devlink_frame_err_name(rc));
    CHECK(n == WIRE_HEADER_BYTES + (size_t)WIRE_PAYLOAD_MAX, "最大载荷总长不对");
    /* payload_len 是 16 位；16368 = 0x3FF0 */
    CHECK(out[10] == 0x3F && out[11] == 0xF0,
          "payload_len 大端应为 0x3FF0，实际 0x%02X%02X", out[10], out[11]);
    CHECK(out[3] == 0x04, "type 应为 0x04");
    CHECK(out[WIRE_HEADER_BYTES] == 0x04, "载荷首字节位置错误");
    CHECK(out[WIRE_HEADER_BYTES + WIRE_PAYLOAD_MAX - 1] == 0x5A, "载荷末字节位置错误");
}

static void test_frame_round_trips_through_the_wire_decoder(void)
{
    /* 成帧 → wire_decode_header 解回来必须逐个字段相等。
     * 这条把"生产成帧"与 RX 侧的解码器钉在一起：成帧改了字段顺序/宽度，
     * 这里立刻红（而不是等真机上链路起不来）。 */
    const uint8_t payload[3] = { 0x22, 0x11, 0x00 };
    uint8_t out[WIRE_HEADER_BYTES + sizeof(payload)];
    size_t n = 0;
    CHECK(devlink_encode_frame(out, sizeof(out), payload, sizeof(payload), 0xDEADBEEFu, &n)
              == DEVLINK_FRAME_OK, "成帧应成功");

    wire_header_t h;
    CHECK(wire_decode_header(out, n, &h) == WIRE_OK, "生产成帧的字节必须能被 RX 侧解码器接受");
    CHECK(h.ver == (uint8_t)WIRE_VER, "ver 往返不一致");
    CHECK(h.type == 0x22, "type 往返不一致（%u）", h.type);
    CHECK(h.flags == 0, "flags 往返不一致（%u）", h.flags);
    CHECK(h.seq == 0xDEADBEEFu, "seq 往返不一致（%u）", h.seq);
    CHECK(h.payload_len == sizeof(payload), "payload_len 往返不一致（%u）", h.payload_len);
    CHECK(wire_header_has_crc(&h) == false, "不得置 CRC 位（后端条件式校验 + 下行也不置）");
}


/* ════════ 12. PEM 终止符（2026-10-07，真机抓到）════════
 *
 * ## 为什么这条必须有
 * 真机首次联调（S3 30EDA0A9A808）时链路起不来：
 *     E esp-tls-mbedtls: mbedtls_x509_crt_parse of CA cert returned -0x2180
 * -0x2180 = MBEDTLS_ERR_X509_INVALID_FORMAT。
 * 根因不是证书内容（宿主机 openssl verify 通过），而是 **NVS 里的 blob 没有 NUL**：
 * esp-tls 要求 PEM 缓冲以 NUL 终止、且 *_bytes **含**该终止符
 * （esp_tls.h:111-112, 137-140），而 NVS 存的是文件原样字节（末尾 0x0A）。
 *
 * ## 为什么宿主测试原本抓不到
 * 出错的那条路（devlink_load_certs）只能编进 IDF（要 NVS），宿主编不到；
 * 而全仓唯一构造 tls_esp_certs_t 的地方就是它 ⇒ 这段缝**从未被测过**。
 * ⇒ 修法之一就是把契约抽成**纯函数**，让它能被宿主钉住（本用例）。
 *
 * 与 §134（生产上行未成帧）同一族：宿主测试与真实调用之间有一条无人看守的缝。
 */
static void test_pem_terminator_contract(void)
{
    /* 真实形态：NVS 里的 PEM 以 0x0A 结尾，没有 NUL */
    uint8_t pem[] = { '-', '-', '-', '-', '-', 0x0A };
    const size_t raw = sizeof(pem);

    /* ① 所需缓冲 = 原始 + 1（不 +1 就是真机上那个 bug） */
    CHECK(devlink_pem_buf_bytes(raw) == raw + 1,
          "PEM 缓冲应为 raw+1 字节（含终止符），实际 %zu", devlink_pem_buf_bytes(raw));
    CHECK(devlink_pem_buf_bytes(0) == 1, "空材料也要 1 字节放终止符，实际 %zu",
          devlink_pem_buf_bytes(0));

    /* ② 终止符必须真的写在第 raw 字节上 */
    uint8_t buf[8];
    memset(buf, 0xAA, sizeof(buf));
    /* ⚠ 这个函数只**写终止符**，不复制数据（那是调用方的事：NVS 已经把它读进
     *   同一块缓冲）。所以我第一版"直接把 buf 拿去和 pem 比"是**测试写错了** ——
     *   它报"原始字节被改写"，而其实是我从没把 pem 放进去。
     *   教训：断言失败时先怀疑测试自己的前提，再怀疑实现。 */
    memcpy(buf, pem, raw);
    size_t out = 0;
    CHECK(devlink_pem_terminate(buf, sizeof(buf), raw, &out) == true, "正常路径应成功");
    CHECK(buf[raw] == 0, "第 raw 字节必须是 NUL，实际 0x%02X", buf[raw]);
    CHECK(out == raw + 1,
          "返回长度必须**含**终止符（esp-tls 的 cacert_bytes 契约），实际 %zu", out);
    CHECK(memcmp(buf, pem, raw) == 0, "原始字节不得被改写");

    /* ③ ⭐ 长度含终止符这一条单独钉住 —— 真机上失败的正是它。
     * 若有人把 out 改回 raw（"看起来更自然"），这条会红。 */
    CHECK(out != raw, "长度**不能**等于原始长度：esp-tls 要求含终止符；"
          "真机上正是这里传错导致 MBEDTLS_ERR_X509_INVALID_FORMAT");

    /* ④ 容量不足 ⇒ 拒绝，且**一个字节都不写**（不半途而废） */
    uint8_t small[4];
    memset(small, 0x55, sizeof(small));
    size_t out2 = 12345;
    CHECK(devlink_pem_terminate(small, sizeof(small), raw, &out2) == false,
          "容量不足应返回 false");
    CHECK(small[0] == 0x55 && small[3] == 0x55, "失败时不得写入任何字节");
    CHECK(out2 == 12345, "失败时不得改写 out_len");

    /* ⑤ 边界：刚好够 */
    uint8_t exact[7];      /* raw(6) + 1 —— 我第一版写 exact[6]，那是**差一个**，
                           * 函数拒绝得对，是我的断言错了 */
    CHECK(sizeof(exact) == raw + 1, "本边界用例的前提：cap 恰好 == raw+1");
    CHECK(devlink_pem_terminate(exact, sizeof(exact), raw, &out2) == true,
          "cap 恰好等于 raw+1 时必须成功");
    CHECK(out2 == raw + 1 && exact[raw] == 0, "边界情形结果应正确");

    /* ⑥ NULL 参数 */
    CHECK(devlink_pem_terminate(NULL, 8, raw, &out2) == false, "buf=NULL 应拒绝");
    CHECK(devlink_pem_terminate(buf, sizeof(buf), raw, NULL) == false, "out_len=NULL 应拒绝");
}

/* ⭐ task-34：链路缓冲的池选择（P8 归一化）。
 *
 * 为什么必须钉住：3.0 链路在内部 RAM 上新增 2 KB 读缓冲 + 4 KB 定界器缓冲，
 * 而 s3p 的内部连续块余量只剩约 7 KB ⇒ 差 512 字节 ⇒ ConfigManifest 被内存
 * 门禁**永久拒绝**（真机 §139.4）。修法是把这两块纯数据缓冲放 PSRAM。
 *
 * 本判据要保证的是**三型号行为不变**：无 PSRAM 时必须落回内部（= 改动前），
 * 有 PSRAM 时才走外部 —— 差别只是放置，不是可观测行为。 */
static void test_buf_place_is_only_about_psram(void)
{
    CHECK(devlink_buf_place(false) == DEVLINK_BUF_PLACE_INTERNAL,
          "无 PSRAM（s3/c6）⇒ 必须落回内部 RAM（与改动前逐字节相同）");
    CHECK(devlink_buf_place(true) == DEVLINK_BUF_PLACE_PSRAM,
          "有 PSRAM（s3p）⇒ 放外部，把内部连续块还给内存门禁");
    CHECK(devlink_buf_place(true) == devlink_buf_place(true),
          "同一输入必须给同一结论（纯函数，无隐藏状态）");
    CHECK(devlink_buf_place_name(DEVLINK_BUF_PLACE_INTERNAL) != NULL &&
          devlink_buf_place_name(DEVLINK_BUF_PLACE_PSRAM) != NULL,
          "两个取值都要有非空名字（日志里要能看出放哪了）");
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
    /* task-31：生产上行成帧（字节级） */
    test_frame_bytes_are_exactly_the_wire_contract();
    test_frame_type_tracks_payload_first_byte();
    test_frame_rejects_bad_input_instead_of_emitting_garbage();
    test_frame_max_payload_boundary_is_accepted();
    test_frame_round_trips_through_the_wire_decoder();
    test_pem_terminator_contract();
    test_buf_place_is_only_about_psram();   /* task-34 */

    if (s_failures) { printf("device_link_wiring_tests: %d FAILURE(S)\n", s_failures); return 1; }
    printf("device_link_wiring_tests: all checks passed\n");
    return 0;
}
