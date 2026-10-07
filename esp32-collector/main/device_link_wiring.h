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
 * ### ⚠ 但必须说清它**不**等于什么（我第一版在这里写错了，实测后更正）
 *
 * 我原本写的是"代码始终编译 ⇒ 门禁的『可达』与『真的被调用』是同一件事"。
 * **这是错的**，实测证据（2026-10-07，s3-n16）：
 *   - 开关 **n**（默认）：ELF 里 `session_create` / `tls_esp_io` 等符号数为 **0**
 *     —— 常量折叠后该分支成为死代码，再被链接器 `--gc-sections` 丢弃；
 *     连被 main 调用的入口 `device_link_wiring_init` 都被内联掉了。
 *   - 开关 **y**：上述符号全部存在，且 `ehome_collector.bin` 的 md5 不同。
 *
 * ⇒ 准确说法：**默认构建里不含这条链路**；开关为 y 时才进镜像。
 *   可达性门禁的"可达"是**源码层**判据，**不保证**代码在默认固件里存在。
 *   详情已写入 `tools/check_component_reachable.py` 注释。
 *   默认 **n** 正是为了不改变出厂行为（约束：不影响生产环境）。
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
