/**
 * @file factory_reset_namespaces.c
 * @brief 白名单的**唯一**实现（P4）。纯数据 + 纯判定，不依赖 IDF ⇒ 宿主可测。
 * 见 factory_reset_namespaces.h 的说明（为什么从 factory_reset.c 抽出来）。
 */
#include "factory_reset_namespaces.h"

#include <string.h>

/* 见头文件：这是"出厂重置擦哪些命名空间"的**唯一**来源。 */
const char *const FACTORY_RESET_NAMESPACES[] = {
    "wifi_cfg",
    "config",
    "ota",
};

const size_t FACTORY_RESET_NAMESPACE_COUNT =
    sizeof(FACTORY_RESET_NAMESPACES) / sizeof(FACTORY_RESET_NAMESPACES[0]);

bool factory_reset_namespace_should_erase(const char *ns)
{
    /* ⚠ 失败安全：判不出来就**不擦**（出厂重置是破坏性操作）。 */
    if (ns == NULL || ns[0] == '\0') return false;

    for (size_t i = 0; i < FACTORY_RESET_NAMESPACE_COUNT; i++) {
        if (strcmp(ns, FACTORY_RESET_NAMESPACES[i]) == 0) return true;
    }
    return false;
}