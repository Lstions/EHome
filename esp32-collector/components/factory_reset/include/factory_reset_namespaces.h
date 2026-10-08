/**
 * @file factory_reset_namespaces.h
 * @brief 出厂重置要擦除的 NVS 命名空间 —— **白名单的唯一来源**（P4）。
 *
 * ## 为什么把它从 factory_reset.c 里抽出来（2026-10-08）
 * 原先这份名单是 `factory_reset.c` 里的一个 **static 数组**，于是：
 *   · 宿主测试**引用不到**它（static + 该文件依赖 nvs_flash/gpio/freertos）；
 *   · `factory_reset.c` 因此长期**零宿主覆盖**（覆盖率门禁里它是"未覆盖"项）；
 *   · 而 `tests/test_factory_reset_whitelist.c` 里**复制**了一份**不同**的名单
 *     （`{wifi_mgr, config_mgr, ota}` vs 生产的 `{wifi_cfg, config, ota}`）⇒
 *     那份"测试"通过 19/19 是真的，但**测的是副本，与生产无关**（§166.4）。
 *
 * ⇒ 抽成**纯数据 + 纯判定**（不依赖 IDF）后：
 *   ① 白名单只有一处定义；② 宿主可 100% 覆盖；
 *   ③ "某命名空间该不该擦"成为可测的**判定**，而不是散在循环里的隐式行为。
 *
 * ⚠ 改动本表会**改变真机行为**（擦错命名空间 = 丢用户配置），
 *   所以本文件刻意保持**极简**：只有数据与一个纯函数，没有任何副作用。
 */
#ifndef FACTORY_RESET_NAMESPACES_H
#define FACTORY_RESET_NAMESPACES_H

#include <stdbool.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

/* 出厂重置**只**擦除这些命名空间；其余一律保留。
 *
 * ⚠ 与生产历史对齐：这三项是设备自身可重建的配置（WiFi 凭据 / 采集配置 / OTA 状态）。
 *   任何"顺便也清掉 X"的想法都必须先回答：X 丢了设备还能自己恢复吗？ */
extern const char *const FACTORY_RESET_NAMESPACES[];

/* 上表的元素个数（由 .c 里的 sizeof 推导，**不要**在别处再写一遍数字）。 */
extern const size_t FACTORY_RESET_NAMESPACE_COUNT;

/**
 * @brief 该 NVS 命名空间是否属于"出厂重置要擦除"的白名单。
 *
 * @param ns 命名空间名；NULL 或空串一律返回 false（**不擦**）。
 * @return true 表示**应当擦除**。
 *
 * ⚠ NULL/空串返回 false 是**有意的失败安全**：
 *   出厂重置是破坏性操作，"判不出来就别擦"比"判不出来就擦"安全得多。
 */
bool factory_reset_namespace_should_erase(const char *ns);

#ifdef __cplusplus
}
#endif

#endif /* FACTORY_RESET_NAMESPACES_H */