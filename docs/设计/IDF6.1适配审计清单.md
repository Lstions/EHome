# IDF 6.1 适配审计清单

> 审计人：`idf61-source-auditor`（task-3） · 日期：2026-10-05
> 口径：**静态审计，只读，不改任何代码**
> 结论先行：**未发现会导致编译失败或静默行为改变的硬断点。** 见"总判"。

---

## 0. 总判（先看这里）

**没有发现"能编过但配置/行为静默改变"的条目。** 6.1 适配基本完备。

三条支撑证据：

1. **6.1 官方迁移指南只有 3 项**，且本固件一项都不涉及。
   扫描 `$IDF_PATH/docs/en/migration-guides/release-6.x/6.1/`（3 个 rst，105 行）：
   - LCD `on_refresh_done` → `on_frame_buf_complete`；本固件 `grep esp_lcd` **0 命中**。
   - UART wakeup `uart_set_wakeup_threshold`/`uart_get_wakeup_threshold` 废弃；
     本固件 `grep uart_set_wakeup_threshold|uart_get_wakeup_threshold|uart_wakeup_setup|esp_sleep_enable_uart_wakeup` **0 命中**。
   - `idf.py flash` 默认改为"快速重刷"；本仓刷机不走 `idf.py flash`
     （`tools/flash_firmware.py:24` 明确"始终用 python -m esptool"，且脚本内无 `idf.py flash` 调用）。
   **→ 官方迁移项 3/3 不适用。**

2. **`sdkconfig.defaults` 全部键在 6.1 下仍有效。**
   口径：解析 6.1 全树 685 个 Kconfig 文件、6323 个符号；对照 3 个 defaults 文件共 40 个赋值键。
   **0 个**在 6.1 中不存在（唯一"未命中"的 `CONFIG_IDF_TARGET` 是 IDF 自身生成的目标符号，非 Kconfig 定义，属正常）。
   **→ 不存在"被 6.1 静默忽略的 defaults 键"。**

3. **固件引用的每个 IDF 符号在 6.1 头文件里都存在。**
   口径：抽取 `main/` + `components/` 下 92 个 .c/.h 的 171 个 IDF 命名空间调用符号，
   对照 6.1 全部 4432 个公开头文件（10098 个符号）。
   剩余 59 个"未命中"全部是本仓自有 API（`gpio_ctrl_*`、`i2c_*`、`spi_transact`、`uart_route` 等）
   或 `esp_mqtt_client_*`（来自托管组件而非 IDF）——**逐条核对，无一是"被 6.1 移除的 IDF API"**。

> Lead 已实测 4 个 profile 在真 6.1 下全部编译通过（RC=0），与上述静态结论一致。

---

## 1. 需要复核的条目（2 条，均非 6.1 引入的回归）

这两条**不是** 6.1 适配断点，而是审计中顺带发现的**既有隐患**。按重要性排序。

### R-1 【高】MQTT 任务栈被陈旧的派生 sdkconfig 钉在 6144，defaults 的 8192 从未生效

| 项 | 内容 |
|---|---|
| 位置 | `sdkconfig.defaults:31` → `build/c6-n8/sdkconfig:3688`、`build/s3-n16/sdkconfig:3652` |
| 当前写法 | defaults 声明 `CONFIG_MQTT_TASK_STACK_SIZE=8192`；实际生效值 **6144** |
| 证据 | 托管组件 `managed_components/espressif__mqtt/Kconfig:105-107` 的 default 恰为 **6144**；`build_firmware.sh:337` 的 `_guard_symbols` **不含**该符号 |
| 为何可疑 | 该文件 `:25-31` 的注释明确记载：6144 对"ConfigManifest 事务 + bus_manager 1792 字节栈帧 + 发布链"余量偏薄，2026-10-04 已因此栈溢出崩溃（PANIC in `prvTaskCheckFreeStackSpace`）。即**这是修过的一个崩溃，但修复没有落到实际生效的配置里** |
| 建议改法 | 把 `CONFIG_MQTT_TASK_STACK_SIZE` 加入 `build_firmware.sh:337` 的 `_guard_symbols`，并 `rm -rf build/<profile>` 重建；或直接删除派生 sdkconfig 让它从 defaults 重新生成 |
| 与 6.1 的关系 | **无**。6.0.2 下同样成立（见"§3 交叉验证"） |

### R-2 【低】配置漂移守卫只覆盖 5 个符号

| 项 | 内容 |
|---|---|
| 位置 | `build_firmware.sh:337-345`（`_guard_symbols`） |
| 当前写法 | 仅守卫 `ESP_WIFI_IRAM_OPT`、`ESP_WIFI_RX_IRAM_OPT`、`ESP_WIFI_EXTRA_IRAM_OPT`、`SPIRAM`、`SPIRAM_MODE_OCT` |
| 为何可疑 | 守卫机制本身是对的（脚本注释已精确描述"陈旧 user-set 值静默压过 defaults"这一失败模式），但名单太短——**R-1 正是从这个缺口漏出去的**。defaults 里还有 30+ 个"产品形态"键（分区表、flash 容量、日志版本、TWDT、MQTT 栈）同样会静默漂移 |
| 建议改法 | 把 defaults 中所有"产品形态"键纳入守卫，或改为通用机制：对 defaults 中出现的每个键都比对派生 sdkconfig |
| 与 6.1 的关系 | **无**，既有问题 |

---

## 2. 已核对无问题（附证据，便于复核）

### 2.1 sdkconfig 键（最高优先级类别）

| 检查 | 口径 | 结果 |
|---|---|---|
| defaults 键是否在 6.1 存在 | 40 个赋值键 × 6.1 全树 6323 符号 | **0 个失效** |
| 现有派生 sdkconfig 是否有"6.1 无 rename 且静默丢弃"的启用键 | `build/c6-n8/sdkconfig`、`build/s3-n16/sdkconfig` 各 1642/1655 键 × 561 条 rename 规则 | **0 个**。196/279 个"6.1 未定义"键中，**全部**是 IDF 内部派生符号（`CONFIG_SOC_*`、`CONFIG_IDF_TARGET*`、`CONFIG_COMPILER_OPTIMIZATION_*` 等），由 IDF 自身重建，非用户配置 |
| rename 是否会丢值 | 168/176 个旧键有 rename 规则 | **仅 2 个** rename 目标缺失，且二者均为 `=n`（`CONFIG_MBEDTLS_ATCA_HW_ECDSA_SIGN/VERIFY`，都已禁用）→ **无值丢失** |

### 2.2 固件 `CONFIG_*` 预处理引用

口径：92 个固件 .c/.h 中提取 36 个 `CONFIG_*` 引用，对照 6.1 Kconfig + 6.0.2 生成的 `sdkconfig.h`。

- 18 个是**真实 Kconfig 符号**，全部在 6.1 存在。
- 12 个是**头文件卫哨 / 枚举常量**（`CONFIG_APPLY_*`、`CONFIG_BUS_*`、`CONFIG_MGR_H`、`CONFIG_APPLY_TRANSACTION_H`），
  其中 `components/periph_owner/periph_owner.c:8-10` 直接 `#define CONFIG_BUS_UART 1` 等——
  **遮蔽 IDF 符号的命名坏味道**（与 6.1 无关），但当前不构成缺陷。
- 6 个 `CONFIG_*` 是**带 `#ifndef` 兜底的项目私有宏**（`CONFIG_COLLECTOR_SYNC_*`、`CONFIG_MQTT_RECONNECT_MAX` 等），
  本就不是 Kconfig 符号，走代码内默认值，**不受 IDF 版本影响**。

### 2.3 已废弃 API 逐条比对

口径：扫描 6.1 中固件所用组件的全部公开头文件（`__attribute__((deprecated`，排除 `esp_private/`）。

| 6.1 废弃项 | 固件是否使用 | 证据 |
|---|---|---|
| `uart_set_wakeup_threshold` / `uart_get_wakeup_threshold` | **否** | grep 0 命中 |
| `spi_get_actual_clock` | **否** | grep 0 命中 |
| `ledc_isr_register` / `ledc_intr_type_t` | **否** | grep 0 命中 |
| `heap_caps_aligned_free` / `multi_heap_aligned_free` | **否** | grep 0 命中 |
| `esp_sntp_*` 旧名 | **否** | grep 0 命中 |
| `IP_EVENT_AP_STAIPASSIGNED` / `ip_event_ap_staipassigned_t` | **否** | grep 0 命中 |
| `esp_set_watchpoint` / `esp_clear_watchpoint` | **否** | grep 0 命中 |
| `esp_hw_support` 51 项（`periph_rtc_*`、`cpu_ll_*` 等） | **否** | grep 0 命中 |

**→ 8/8 类废弃 API 均未被使用。**

### 2.4 内核 / FreeRTOS 语义

| 检查 | 6.1 事实 | 固件写法 | 判定 |
|---|---|---|---|
| `configSTACK_DEPTH_TYPE` | 6.1 `FreeRTOSConfig.h:126` = `uint32_t`（字节，SMP 下 `StackType_t` 亦为 4 字节） | `log_stream.c:261-264` 用 `LOG_TX_STACK / sizeof(StackType_t)`（`LOG_TX_STACK=4096` 字节，÷4 → 1024 字，**字节语义，正确**） | 正确 |
| `xTaskCreatePinnedToCore` | 6.1 签名未变，仍收字节 | `scheduler.c:186`、`:273` 传 `SCHED_TASK_STACK=4096` 字节 | 正确 |
| `esp_task_wdt_*` | 6.1 `esp_task_wdt.h`：`init`/`reconfigure`/`deinit` 签名与 6.0.2 **逐字相同**；`deinit` 在 `p_twdt_obj==NULL` 时返回 `ESP_ERR_INVALID_STATE`（`task_wdt.c:644`） | `main/main.c:388-394` 先 `deinit()`（**忽略返回值**，未用 `ESP_ERROR_CHECK`）再 `init(&cfg)` | 正确。若 6.1 不自动初始化 TWDT，`deinit` 返回错误码被忽略，随后 `init` 成功——行为与预期一致 |
| TWDT 自动初始化前提 | 6.1 `Kconfig:316-319` `ESP_TASK_WDT_INIT` default **y**（与 6.0.2 同） | `main/main.c:386-387` 的注释"IDF v6.0 自动初始化 TWDT(5s) 后我们再 deinit 重设" | 前提在 6.1 仍成立 |

### 2.5 条件编译分支

| 检查 | 结果 |
|---|---|
| `ESP_IDF_VERSION >= ...` 版本门 | **0 处**。全部 `#if` 都是 `CONFIG_IDF_TARGET_ESP32S3`/`ESP32C6`（芯片门，非 IDF 版本门）或 `SOC_*` 能力门（`SOC_USB_SERIAL_JTAG_SUPPORTED`、`SOC_UART_LP_NUM`） |
| 这些芯片/SOC 门在 6.1 是否仍成立 | 成立。`SOC_UART_LP_NUM`、`SOC_USB_SERIAL_JTAG_SUPPORTED` 在 6.1 soc 头中仍定义；Lead 实编 4 profile 通过即证明分支可编译 |
| `bus_dma.c:437-443` 的 `SOC_UART_LP_NUM >= 1` 分支 | 6.1 `uart.h:45-47` 中 `source_clk` 与 `lp_source_clk` 是**同一 union 的两个成员**，代码二选一填充，正确 |

### 2.6 第三方托管组件

| 检查 | 结果 |
|---|---|
| `managed_components/espressif__mqtt` 版本约束 | `idf_component.yml:2-3` 声明 `idf: version: '>=5.3'` → **6.1 在约束内** |
| `dependencies.lock` 锁定 | `espressif/mqtt 1.1.0`（`component_hash fb18bc3b…`）+ `idf 6.1.0`。注意：仓库根的 `dependencies.lock` 是**按 target 生成的**（`:20` `target: esp32c6`），实际生效的是各 build 目录下的副本（`CMakeLists.txt:18` 已把 `DEPENDENCIES_LOCK` 重定向到 `${CMAKE_BINARY_DIR}`） |
| 组件是否用 `esp_private` 头 | **0 处**（grep 0 命中）→ 无私有 API 漂移风险 |
| 组件内 deprecated 用法 | 仅 `mqtt5_client.h` 的 MQTT5 枚举别名，**本固件用的是 MQTT 3.1.1 API**，未触及 |
| `MQTT_TRANSPORT_TCP` | 在 6.1 与托管组件 1.1.0 中**均不存在**（grep 0 命中）。`sdkconfig.defaults:17-22` 的注释与处理**正确**——该键确已从 defaults 移除，避免"每次构建打印 unknown kconfig symbol 但静默无效" |

### 2.7 私有头依赖

| 位置 | 6.1 状态 | 判定 |
|---|---|---|
| `main/panic_capture.c:29` `esp_private/panic_internal.h` | 6.1 仍存在；`panic_info_t` 字段（`core`/`exception`/`addr`/`frame`）**与 6.0.2 逐字段相同** | 稳定 |
| `components/log_stream/log_capture_esp.c:8` `esp_private/log_util.h` | 6.1 仍存在；`esp_log_util_is_constrained()` 在 `:106` 仍定义 | 稳定 |
| `main/CMakeLists.txt:41` `-Wl,--wrap=esp_panic_handler` | 6.1 `components/esp_system/port/panic_handler.c:264` 仍是**跨 TU 调用** `esp_panic_handler(&info)`，`--wrap` 仍可拦截 | 稳定 |
| `components/log_stream/CMakeLists.txt` `-Wl,--wrap=esp_log` | 6.1 `esp_log.h:38` 签名 `void esp_log(esp_log_config_t, const char*, const char*, ...)` 未变；`log_capture_esp.c:72` 的 wrapper 签名与之匹配 | 稳定 |

### 2.8 linker.lf / 内存布局

| 检查 | 结果 |
|---|---|
| `main/linker.lf:46-49` 映射 `libnet80211.a` 的 `ieee80211_debug` 到 `noflash_text` | **对象存在**：`ar t $IDF_PATH/components/esp_wifi/lib/esp32s3/libnet80211.a`（50 个对象）中 `ieee80211_debug.o` 命中 1 次 |
| 是否会与 IDF 自身映射冲突 | 6.1 `components/esp_wifi/linker.lf:132-140` 对 `libnet80211.a` 的映射**无对象过滤器**（仅 `wifi_iram`/`wifi_rx_iram` 段）→ 与本仓按对象名的映射不冲突（ldgen 要求"重复映射须带对象过滤器"，此处满足） |
| 目标门 | `linker.lf:48` 限定 `if IDF_TARGET_ESP32S3 = y`，C6 不受影响——与文件头注释的设计意图一致 |

---

## 3. 交叉验证：R-1 不是 6.1 引入的

`build/c6-n8/sdkconfig`（2026-10-04 15:32）与 `build/s3-n16/sdkconfig`（2026-10-04 17:00）
**都是 IDF 6.0.2 生成的**——由各自 `project_description.json` 的 `git_revision: v6.0.2` 确认；
而 `sdkconfig.defaults` 的 mtime 是 17:03，**晚于**两个派生 sdkconfig。

→ `CONFIG_MQTT_TASK_STACK_SIZE=8192` 是在派生 sdkconfig 生成之后才写进 defaults 的，
因此 6.0.2 的产物里同样停在 6144。**这是既有的配置漂移，与 IDF 6.1 无关**，
只是恰好由本次审计的"默认值比对"手法暴露出来。

---

## 4. 未解决的问题 / 局限（诚实声明）

1. **无法独立复核 Lead 的 6.1 实编结论。**
   我在审计时点未在工作树中看到 6.1 构建产物：`build/c6-n8/sdkconfig`、`build/s3-n16/sdkconfig`
   仍是 6.0.2 生成的（`project_description.json` 报 `v6.0.2`），且 `git status --porcelain` 为空。
   §0 的"编译通过"依据**完全来自 Lead 的通报**，我未亲自复现。

2. **"静默行为变化"无法用静态手段完全排除。**
   本轮能证伪的是：键被忽略、API 被移除、枚举/签名变化、条件分支改道。
   **不能**用静态手段排除的：IDF 内部实现变化导致的时序/性能/默认运行时行为差异
   （例如 Wi-Fi 驱动内部重试时序、lwIP 缓冲默认值、esp_timer 精度）。
   这类需要 6.1 实机运行对比，超出本任务范围。

3. **6.0.2 与 6.1 的完整 Kconfig 符号差集未逐条人工过目。**
   脚本给出 120 个"6.0.2 有、6.1 无"的符号，我确认**没有一个是本项目显式设置或固件引用的**，
   但没有逐条研究其语义变化。

4. **未审计 `host_tests/`**（200 个文件中的大部分）与 `tests/`。
   它们是主机侧测试，不参与固件构建；但若其 stub 头（`host_tests/stubs/`）模拟了 IDF API，
   则 6.1 语义变化可能导致**测试通过但固件行为不同**。本次未覆盖。

---

## 附：审计方法与度量

| 项 | 值 |
|---|---|
| 固件源码口径 | `esp32-collector/main/` + `esp32-collector/components/`，**92 个 .c/.h**（排除 build/、host_tests/、tests/） |
| 组件数 | 22 个组件目录 + main |
| 6.1 参照 | `/home/sun/env/esp-idf` @ v6.1.0（`esp_idf_version.h` MINOR=1, PATCH=0）**只读** |
| Kconfig 扫描 | 685 个 Kconfig 文件 / 6323 个符号定义 / 561 条 rename 规则 |
| 头文件扫描 | 4432 个公开头文件 / 10098 个符号 |
| 迁移指南 | `docs/en/migration-guides/release-6.x/6.1/` 3 文件 105 行，全文已读 |
| 未触碰 | 未修改任何代码；未在 `/home/sun/env/esp-idf` 中执行 checkout/fetch/build；未刷机；未碰 `/dev/ttyACM0`；未碰 192.168.20.6；未 commit/push |
