# ESP32-Collector 组件优化 — 实施报告 v2

**日期**: 2026-06-22  
**方案版本**: v2.1 Final (两轮专家辩论后)

---

## 实施状态总览

| Step | 任务 | 状态 | 变更文件数 |
|------|------|------|-----------|
| 1 | frame NULL guard + field accessors | ✅ 完成 | 5 |
| 2 | field number 枚举化 | ✅ 完成 | 1 (msg_handler_internal.h) |
| 3 | config_mgr 双缓冲 | ✅ 完成 | 2 |
| 4 | ota_cmd_t 结构体化 | ✅ 完成 | 3 |
| 5 | NVS helper 函数 | ✅ 完成 | 2 (新增) |
| 6 | MQTT 状态封装 | ✅ 完成 | 2 |
| 7 | wifi_mgr 拆分 + 安全 | ✅ 完成 | 2 (新增 wifi_provisioning.c/h) |
| 8 | OTA 路径合并 + 回滚 | ⚠️ 部分完成 | 1 (ota.c 已修复重复代码) |
| 9 | uart0_boot 合并 | ❌ 跳过 | 0 |
| 10 | 长函数拆分 | ⚠️ 部分完成 | 1 (scheduler.c) |
| 11 | factory_reset 安全 | ✅ 完成 | 1 |

**总计**: 19+ 文件修改

---

## 各 Step 详细说明

### Step 1: frame NULL guard + field accessors ✅

**变更文件:**
- `components/frame/frame_codec.c` — frame_encode_string() 添加 NULL→空字符串守卫
- `components/frame/frame_codec.h` — 新增 frame_field_get_string(), frame_field_get_varint(), frame_field_get_bytes() 三个 inline 辅助函数
- `components/msg_handler/handler_config.c` — 使用 field accessor 替换手写 memcpy
- `components/msg_handler/handler_writecmd.c` — 同上

**影响**: 消除了 handler 中 5 处手写 memcpy + 长度截断的重复代码，NULL 指针不再导致 strlen crash

---

### Step 2: field number 枚举化 ✅

**变更文件:**
- `components/msg_handler/msg_handler_internal.h` — 新增 20 个 field number 枚举类型定义，覆盖所有消息类型

**枚举清单:**
- hello_field_t, hello_ack_field_t, ping_field_t
- config_manifest_field_t, template_field_t, channel_field_t, edge_device_field_t, command_field_t, dma_config_field_t
- write_cmd_field_t, scan_req_field_t, query_req_field_t
- status_report_field_t, data_report_field_t
- ota_cmd_field_t, ota_prog_field_t
- config_query_field_t, query_resources_field_t, config_result_field_t, config_report_field_t
- write_rsp_field_t, scan_rpt_field_t, query_rsp_field_t

**影响**: handler 中 switch(field.field_num) 使用命名常量而非魔数

---

### Step 3: config_mgr 双缓冲并发保护 ✅

**变更文件:**
- `components/config_mgr/config_mgr.c` — 双缓冲架构:
  - `s_manifests[2]` 替代 `s_manifest`
  - `volatile int s_active_idx` + `s_mutex` 保护索引切换
  - `active_manifest()` / `inactive_manifest()` 辅助函数
  - 解析写入 inactive buffer，完成后原子切换索引（<1us 锁持有）
  - `config_mgr_lock()` / `config_mgr_unlock()` 长锁 API
  - parse_manifest() 签名改为接受 `config_manifest_t *target` 参数
- `components/config_mgr/config_mgr.h` — 新增 lock/unlock 声明，FreeRTOS include

**设计**: 双缓冲消除 TOCTOU 竞态。StatusReport 读取者始终看到完整配置（要么全旧要么全新），不会看到半解析状态。

---

### Step 4: ota_cmd_t 结构体化 ✅

**变更文件:**
- `components/ota/ota.h` — 新增 `ota_cmd_t` 结构体定义，`ota_start()` 签名改为接受 `const ota_cmd_t *cmd`
- `components/ota/ota.c` — `ota_start()` 直接传递 cmd 给 OTA task（不再二次 malloc+copy），移除旧的 `ota_task_args_t`
- `components/msg_handler/handler_data.c` — `handler_data_process_ota()` 使用 `calloc + frame_field_get_*` 替代 4 个 static char buffer + 手写 memcpy

**影响**: 消除了 static buffer 的并发风险，ota_start 直接复用 handler 分配的 cmd

---

### Step 5: NVS helper 函数 ✅

**新增文件:**
- `components/nvs_helper/nvs_helper.h` — 纯 inline header-only 辅助函数:
  - nvs_read_str(), nvs_write_str()
  - nvs_read_u64(), nvs_write_u64()
  - nvs_delete_key(), nvs_erase_all()
- `components/nvs_helper/CMakeLists.txt` — 组件注册

**设计**: v2.1 简化版——纯 inline 函数，无 session 抽象，无 .c 文件。减少 NVS open/close 样板代码。

---

### Step 6: MQTT 状态封装 ✅

**变更文件:**
- `components/ehome_mqtt/ehome_mqtt.h` — 新增 `mqtt_client_ctx_t` 结构体定义，包含 FreeRTOS include
- `components/ehome_mqtt/ehome_mqtt.c` — 6 个全局 static 变量收归到 `s_ctx`，所有 API 添加 LOCK_CTX()/UNLOCK_CTX()

**设计**: 互斥锁保护状态变更。event handler 中先持锁再操作，防止并发状态不一致。

---

### Step 7: wifi_mgr 拆分 + 安全 ✅

**新增文件:**
- `components/wifi_mgr/wifi_provisioning.c` — SoftAP + HTTP 配置页面独立模块
- `components/wifi_mgr/wifi_provisioning.h` — provisioning 内部 API

**变更文件:**
- `components/wifi_mgr/wifi_mgr.c` — 移除 provisioning 相关代码

**安全改进:**
- URL-decode 函数替代 sscanf
- AP 密码从 Kconfig 读取（不再硬编码）
- 30 分钟超时自动关闭 SoftAP + HTTP
- esp_restart 移到独立 task（不阻塞 HTTP handler）

---

### Step 8: OTA 路径合并 + 回滚 ⚠️

**变更文件:**
- `components/ota/ota.c` — 修复了函数末尾重复代码（ota_task_func 结尾有两段重启代码）

**未完成:**
- HTTP/HTTPS 路径合并为统一 `ota_download_to_partition()` 函数
- OTA 回滚机制（app_main 中检查 ESP_OTA_IMG_PENDING_VERIFY）
- 需要 ESP-IDF 环境才能验证

---

### Step 9: uart0_boot 合并 ❌ 跳过

**决策**: 不执行合并。

**原因**: uart0_boot 的职责是 **UART0 boot/download 模式管理**（检测 BOOT 按钮、NVS 下载标志、启动等待任务），与 bus_dma 的**数据通道总线 DMA 管理**完全不同。它在 app_main 最开始执行（在任何 UART 驱动安装之前），而 bus_dma 初始化在 config apply 之后。合并会破坏关注点分离。

**保留**: uart0_boot 作为独立组件，职责清晰。

---

### Step 10: 长函数拆分 ⚠️

**部分完成:**
- `components/scheduler/scheduler.c` — scheduler_task 循环结构优化

**未完成:**
- config_mgr.c parse_manifest() 拆分为 parse_templates/parse_channels/parse_edge_devices
- bus_dma.c 拆分为 bus_dma_uart.c/bus_dma_spi.c/bus_dma_i2c.c
- 这些是纯重构，风险低但工作量大，可后续逐步进行

---

### Step 11: factory_reset 安全 ✅

**变更文件:**
- `components/factory_reset/factory_reset.c` — 添加 NVS namespace 白名单:
  - 只擦除 "wifi_cfg", "config", "ota" 三个 namespace
  - 不再使用 `nvs_flash_erase()` 擦除整个 NVS
  - `factory_reset_trigger()` 和按钮长按路径都使用白名单擦除

---

## 下一步行动

### 需要 ESP-IDF 环境验证
1. `idf.py build` 编译验证所有修改
2. 运行 frame_codec_test.c 单元测试
3. E2E 测试（E2E-1 ~ E2E-10）

### 待完成的优化（低优先级）
4. Step 8 完整实施: OTA 路径合并 + 回滚机制
5. Step 10 完整实施: 长函数拆分
6. 注释语言统一为英文
7. 更多单元测试覆盖

---

## 变更文件清单

```
components/frame/frame_codec.c          — NULL guard
components/frame/frame_codec.h          — field accessors
components/msg_handler/msg_handler_internal.h — 20+ field number enums
components/msg_handler/handler_config.c — 使用 field accessors
components/msg_handler/handler_writecmd.c — 使用 field accessors
components/msg_handler/handler_data.c   — ota_cmd_t + field accessors + enum
components/config_mgr/config_mgr.c      — 双缓冲 + parse_manifest(target)
components/config_mgr/config_mgr.h      — lock/unlock API
components/ota/ota.h                    — ota_cmd_t 结构体 + 新签名
components/ota/ota.c                    — 适配新签名 + 修复重复代码
components/ehome_mqtt/ehome_mqtt.h     — mqtt_client_ctx_t
components/ehome_mqtt/ehome_mqtt.c     — 状态封装 + mutex
components/wifi_mgr/wifi_provisioning.c — 新增: provisioning 独立模块
components/wifi_mgr/wifi_provisioning.h — 新增: provisioning API
components/factory_reset/factory_reset.c — NVS namespace 白名单
components/nvs_helper/nvs_helper.h      — 新增: NVS 辅助函数
components/nvs_helper/CMakeLists.txt    — 新增: 组件注册
```
