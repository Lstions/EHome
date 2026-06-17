# feat/dma-resource-protocol 全面实现计划

> **For Hermes:** 并行 dispatch subagent 执行各独立任务。

**目标:** 完成技术债务清理 + OTA验证 + 后端DMA业务 + 前端DMA开关

**架构:** ESP32固件(C/FreeRTOS)、Go后端(Gin+GORM)、Vue3前端 三线并行

---

## 依赖关系

```
Task 1 (ESP32 技术债务) ── 独立
Task 2 (OTA 验证)       ── 独立 (需硬件)
Task 3 (后端 DMA)       ── 独立
Task 4 (前端 DMA)       ── 依赖 Task 3
```

Task 1/2/3 可完全并行。Task 4 等 Task 3 完成后启动。

---

## Task 1: 优化已知技术债务 (4项)

### 1a. g_cmd_queue 全局变量消除 (优先级: 中)

**文件:**
- `esp32-collector/components/scheduler/scheduler.h`
- `esp32-collector/components/scheduler/scheduler.c`
- `esp32-collector/main/app_state.c`
- `esp32-collector/main/app_state.h`
- `esp32-collector/main/main.c`

**改动:**
1. `scheduler_start(void)` → `scheduler_start(QueueHandle_t cmd_queue)`
2. scheduler.c 内部 static `s_cmd_queue` 存储注入的队列句柄
3. 所有 `g_cmd_queue` 引用替换为 `s_cmd_queue`
4. main.c 调用改为 `scheduler_start(s->cmd_queue)`
5. app_state.c 删除 `g_cmd_queue` 全局变量和桥接代码

### 1b. NVS Blob 版本控制 (优先级: 高)

**文件:**
- `esp32-collector/components/config_mgr/config_mgr.c`
- `esp32-collector/components/config_mgr/config_mgr.h`

**改动:**
1. 定义 `CONFIG_NVS_VERSION = 1` (uint32_t)
2. 在 `config_mgr_save_to_nvs()`: 写入前 prepend 4字节版本号
3. 在 `config_mgr_load_from_nvs()`: 读取前4字节版本号，不匹配则清除旧数据返回 false
4. 修改 `KEY_MANIFEST` blob 保存格式: `[version:4B][manifest:sizeof(config_manifest_t)]`

### 1c. hw_profile 职责拆分 (优先级: 中)

**文件:**
- 新建 `esp32-collector/components/hw_profile/hw_tables.c`
- 新建 `esp32-collector/components/hw_profile/hw_tables.h`
- 修改 `esp32-collector/components/hw_profile/hw_profile.c`
- 修改 `esp32-collector/components/hw_profile/hw_profile.h`
- 修改 `esp32-collector/components/hw_profile/CMakeLists.txt`

**改动:**
1. 将 `hw_uarts[]`, `hw_i2cs[]`, `hw_spis[]`, `hw_gpios[]`, `hw_adcs[]`, `hw_dmas[]` 常量表移至 `hw_tables.c`
2. 将 `HW_*_COUNT`, `HW_PLATFORM_STRING`, `HW_RESERVED_*` 宏移至 `hw_tables.h`
3. `hw_profile.c` 保留 `hw_profile_build_report()` 和编码函数
4. `hw_profile.h` include `hw_tables.h`
5. CMakeLists.txt 添加 `hw_tables.c`

### 1d. 单元测试框架 (优先级: 低, 仅建骨架)

**文件:**
- 新建 `esp32-collector/main/test_dma_pool.c`

**改动:**
- 仅创建一个测试文件框架，列出需要测试的用例
- 不实现完整测试（优先级低）

---

## Task 2: ESP32-C6 OTA 升级验证

### 2a. 诊断并修复 OTA SHA256 卡死问题

**根因分析:**
- `progress.md` 报告: 下载完成(0→100%) → SHA256 计算开始 → 日志停止
- 可能原因: `esp_partition_read()` 读取大分区时看门狗超时、或 Flash 读取权限问题
- 关键代码: `ota.c:381-396`

**文件:**
- `esp32-collector/components/ota/ota.c`

**改动:**
1. 在 SHA256 循环中添加 `ESP_LOGI` 进度日志 (每64KB打印一次)
2. 在 `esp_partition_read` 失败时添加详细错误码日志
3. 添加看门狗喂狗: `vTaskDelay(pdMS_TO_TICKS(10))` 每块后
4. 缩短 delay: `vTaskDelay(1)` → `vTaskDelay(pdMS_TO_TICKS(5))`，增加 `taskYIELD()`
5. HTTPS 路径修正 `total_bytes`: 使用 `esp_ota_get_next_update_partition()->size` 而非 `size` 参数

### 2b. 构建并烧录 ESP32-C6 固件

**命令:**
```bash
cd /home/bcat/workspace/ehome-system/esp32-collector
source ~/env/esp-idf/export.sh
idf.py set-target esp32c6
idf.py build
```

**烧录:**
- 使用 USB-Serial/JTAG (ESP32-C6 native USB: GPIO12=D-, GPIO13=D+)
- 或通过 TCP 8088 端口 OTA (如果旧固件正在运行)

### 2c. 端到端 OTA 验证

1. 启动 HTTP OTA server: `python3 scripts/http_ota_server.py [固件路径]`
2. 通过 TCP 8088 发送 OTA 命令
3. 监控设备日志确认: SHA256 校验 → 分区切换 → 重启 → 新固件运行
4. 验证 `ota_confirm_valid()` 正常调用

---

## Task 3: 后端 DMA 相关业务

### 3a. 数据库模型扩展

**文件:**
- `backend/internal/models/models.go`

**改动:**
1. `Channel` 结构体添加: `DmaEnabled bool` (jsonb 或独立列)
2. `Node` 添加 DMA 信息 JSONB 字段 (或新建 `DmaChannelInfo` 模型)

### 3b. ResourceReport field 8 解码

**文件:**
- `backend/internal/nodemgr/handler_resources.go`

**改动:**
1. 添加 `dmaChannelEntry` 结构体
2. 在 ResourceReport switch 中添加 field 8 (tag `0x42`) 解码分支
3. 调用子消息解码器解析 DMA channel 数据
4. 存储 DMA channel 状态到 Node

### 3c. Channel DMA 状态持久化

**文件:**
- `backend/internal/nodemgr/handler_resources.go`

**改动:**
1. 解码 channelEntry 时保留 `DmaEnabled`
2. upsert Channel 时写入 `DmaEnabled` 到数据库

### 3d. ConfigManifest DmaChannelConfig

**文件:**
- `backend/internal/nodemgr/sender.go`
- `backend/internal/nodemgr/handler_config.go`

**改动:**
1. 在 ConfigManifest 编码时支持 field 5 (DmaChannelConfig)
2. API 提供 DMA 配置下发接口

---

## Task 4: 前端 DMA 开关功能

### 4a. API 类型更新

**文件:**
- `frontend-shared/src/api/node.ts`
- `frontend-shared/src/stores/node.ts`

**改动:**
1. 添加 `DmaChannel` 接口
2. 添加 `DmaEnabled` 到 Channel 相关接口
3. 添加 `getDmaChannels()` / `updateDmaConfig()` API 方法

### 4b. DMA 展示 UI

**文件:**
- `frontend-shared/src/views/node/` (ChannelPanel 或新组件)

**改动:**
1. 在硬件资源面板中显示 DMA 通道列表 (dma_id, name, state, bound_to)
2. DMA 通道卡片: 名称、能力(TX/RX/Burst)、状态(空闲/已分配/禁用)、绑定硬件
3. 颜色区分: 空闲=灰, 已分配=绿, 禁用=红

### 4c. DMA 开关控制

**文件:**
- `frontend-shared/src/views/node/` 

**改动:**
1. 每个 DMA 通道添加 enable/disable 开关
2. 操作后调用后端 API 更新配置
3. 下发 ConfigManifest 到设备
