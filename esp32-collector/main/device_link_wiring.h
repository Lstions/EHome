/**
 * @file device_link_wiring.h
 * @brief 把 3.0 设备侧链路（TCP + mTLS）接进 main —— "固件里根本没有 3.0 链路"的直接修法
 *
 * ## 这一层接什么
 *
 *     tls_esp(mTLS I/O) ─注入 io─> session ─内含─> link_tcp / link_rx_adapt / rx_pump / wire
 *
 * `session` 自己就把 `link_tcp` / `link_rx_adapt` / `rx_pump` / `wire` 串起来了
 * （见 session.c 里 `rx_pump_create(...)`），所以 main 只需创建 tls_esp 的 I/O 配置
 * 与 `session`，整条 3.0 接收链就都在固件里了。
 *
 * ## 决策与胶水分开（本仓的既定做法，见 hello_handshake_*.c）
 *
 * 真正**有判断**的三件事被抽成纯函数，可在宿主上测（`host_tests/device_link_wiring_tests.c`）：
 *   1. `device_link_delim_bytes` —— 定界器要一次性分配多少**连续**字节；
 *   2. `device_link_check_placement` —— 这个连续块在本型号上放得下吗（P8：差异只影响放置）；
 *   3. `devlink_cert_check` —— 单份证书材料的判定（空 / 超上限 / 可用）。
 * 剩下的只是**胶水**（读 NVS、建任务、装配置），不产生判断。
 *
 * ## 为什么不是 `#ifdef` 掉整段实现
 *
 * 实现不带 `#ifdef` 包整段（只在函数里判 `CONFIG_EHOME_DEVICE_LINK_ENABLED`），
 * 目的是让"接线"在**源码层是真实的**：main 里有一个真实调用点，编译器确实编译它，
 * 而不是让整个文件在预处理期消失。
 *
 * ### 开关 n 时到底发生什么（我在这里错了**两次**，最终结论经反汇编验证）
 *
 * **第一次错**：写过"代码始终编译 ⇒ 门禁的『可达』与『真的被调用』是同一件事"。
 *   编译得过 ≠ 进了镜像，这句已删。
 *
 * **第二次错（更隐蔽）**：改成"开关 n 时整个分支被常量折叠成死代码、被 `--gc-sections`
 *   丢弃，ELF 里符号数为 0"。**这也是错的**，两个独立证据（2026-10-07，s3-n16）：
 *   1. 默认构建（`# CONFIG_EHOME_DEVICE_LINK_ENABLED is not set`）的 ELF 里
 *      `session_create / session_poll / tls_esp_io / tls_esp_connect / rx_pump_create /
 *      wire_delim_create / link_tcp_read / link_rx_adapt_read / sntp_mgr_create …`
 *      **11/11 全部存在**；`device_link_wiring_init` = `T 4201599c size 0x29f`。
 *   2. 反汇编该函数，内部**真的调用** `device_link_check_placement` 与 `variant_caps`。
 *
 *   根因（我为什么会测出"0"）：`devlink_wanted()` 是**运行期函数**而非编译期常量，
 *   分支不会被消除。而我当时跑 `xtensa-esp32s3-elf-nm` **没有 source export.sh**
 *   ⇒ 命令不存在 ⇒ 我把 stderr 用 `2>/dev/null` 吞掉 ⇒ `grep -c` 打印 `0`
 *   ⇒ 我把"命令根本没跑成"读成了"符号不存在"。
 *   **又一次"命令跑通 ≠ 测到了东西"。**
 *
 * **正确表述（已由符号表 + 反汇编验证）**：
 *   - 开关 n 时**代码在镜像里**，保护机制是 `device_link_wiring_init` 开头的
 *     **运行期早退**（`if (!devlink_wanted()) { log; return; }`，先于一切副作用）；
 *   - ⇒ **可观测行为不变**（不建任务、不分配堆、不发一个包）—— 这是验收口径；
 *   - ⇒ **但镜像不是逐字节不变**：s3-n16 `.bin` 约 +3.2 KB（代码/字符串/对齐）。
 *     若要按"默认产物完全不变"验收，这条**不成立**，请按"**行为**不变"验收。
 *
 * ## ⚠ 两个前置条件**尚未**落地（因此现在开启会停在 FATAL，这是**设计如此**）
 *
 * 1. **SNTP**：`tls_esp_config_t.now_epoch` 目前传 NULL ⇒ 时间不可信 ⇒
 *    `tls_guard` 把证书类失败判为**可自愈**（soft，退避重试），而不是致命。
 *    这正是 §52.2 的分级意图：没有可信时间时不该断言"证书坏了"。
 * 2. **证书铺开**：设计指定"证书/私钥入 **NVS 加密分区**"，该分区**尚未实现**。
 *    读不到材料时**不假装成功**，而是照常构造 → `tls_guard` 判 `hard_fatal`
 *    ⇒ `SESSION_FATAL`（**不重试**，避免把"没铺开"掩盖成"网络抖动"而无限重试）。
 *
 * ⇒ 开启开关**不等于**能用；它等于"把剩下的缺口变成可见、且带明确处置的状态"。
 */
#ifndef EHOME_DEVICE_LINK_WIRING_H
#define EHOME_DEVICE_LINK_WIRING_H

#include <stdbool.h>
#include <stddef.h>
#include <stdint.h>

#include "variant.h"   /* variant.h 不依赖 IDF，宿主可编译 */

#ifdef __cplusplus
extern "C" {
#endif

/* ── 纯判定（宿主可测）── */

/** 单份证书材料的判定。 */
typedef enum {
    DEVLINK_CERT_OK = 0,     /* 有材料且在容量上限内 */
    DEVLINK_CERT_EMPTY,      /* 长度 0：没铺开 */
    DEVLINK_CERT_TOO_BIG,    /* 超上限：**不放宽上限**，按不可用处理 */
} devlink_cert_t;

/** 放置判定：这条链路在本型号上起得来吗。 */
typedef enum {
    DEVLINK_PLACE_OK = 0,
    DEVLINK_PLACE_NO_VARIANT,          /* 取不到型号能力：**不猜**，拒绝 */
    DEVLINK_PLACE_EXCEEDS_CONTIGUOUS,  /* 需要的连续块超型号上界 */
} devlink_place_t;

const char *devlink_cert_name(devlink_cert_t v);
const char *devlink_place_name(devlink_place_t v);

/** 单份证书判定。cap 是容量上限。 */
devlink_cert_t devlink_cert_check(size_t blob_bytes, size_t cap);

/**
 * 定界器一次性分配的**连续**字节数 = max_payload + 12(头) + 4(CRC)。
 * 上界常量取自 wire.h（P5：唯一来源，不在这里重抄一份）。
 */
uint32_t device_link_delim_bytes(uint32_t max_payload);

/**
 * 这个连续块在本型号上放得下吗。
 * 用型号的 `internal_contiguous_max`（largest 类判据）而不是 free ——
 * OTA 那次"free 够、largest 不够"的事故就是这一类。
 */
devlink_place_t device_link_check_placement(uint32_t max_payload,
                                            const variant_caps_t *caps);

/* ── SNTP 接线里的判定（宿主可测）── */

/**
 * 网络可用性的**边沿**。
 *
 * 为什么要把这件事显式抽出来而不是写 `if (net != prev)`：
 * sntp_mgr_network_down() 会把状态压回 IDLE，**每轮都通知 down** 会让状态机
 * 永远停在"不能发起"的那一步 —— 而这么写出来的代码看起来正在认真接线，
 * 也没有任何一处会报错（正是本仓反复出现的"建好了但没插电"）。
 * 抽成纯函数后，"只在边沿通知"这条契约可以在宿主上被锁住。
 */
typedef enum {
    DEVLINK_NET_EDGE_NONE = 0,   /* 无变化：**不要**重复通知 */
    DEVLINK_NET_EDGE_UP,         /* 不可用 -> 可用 */
    DEVLINK_NET_EDGE_DOWN,       /* 可用 -> 不可用 */
} devlink_net_edge_t;

devlink_net_edge_t devlink_net_edge(bool was_up, bool is_up);

/**
 * `tls_esp_config_t.now_epoch` 的取值规则。
 *
 * 取不到时间一律给 **0**（由 tls_guard 判为不可信），**绝不猜**一个
 * "看起来合理"的值：伪造一个 2026 年的时间会让证书校验**看起来**通过，
 * 把"根本没校时"变成一次无法归因的失败。
 *
 * ⚠ 刻意**不**在这里做区间钳制：TLS_GUARD_MIN/MAX_EPOCH 是 tls_guard 的
 * 单一来源（P4）。适配器里再判一次，阈值就有了两个答案；而且"被适配器
 * 钳过的时间"会让 tls_guard 的判定失去意义（它才是唯一裁判）。
 */
uint64_t devlink_now_epoch_value(bool have_epoch, uint64_t epoch);

/* ── 胶水 ── */

/**
 * 初始化 3.0 设备侧链路。幂等（重复调用只有第一次生效）。
 * 未启用时**不产生任何副作用**，只打一行说明后返回。
 * 自己等 WiFi 就绪，不要求调用方保证网络已在。
 */
void device_link_wiring_init(void);

/** 当前会话状态名（未启用/未创建时返回 "DISABLED" / "NONE"）；诊断用。 */
const char *device_link_wiring_state_name(void);

#ifdef __cplusplus
}
#endif
#endif /* EHOME_DEVICE_LINK_WIRING_H */
