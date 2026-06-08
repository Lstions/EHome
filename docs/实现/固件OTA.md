# 固件 OTA — 实现

## 代码位置

### 后端
- `backend/internal/ota/ota.go` (P3 修: WireVerifying=4, fail-open default)
- `backend/internal/ota/ota_test.go` (单元测试)
- `backend/internal/api/handler_ota.go` (firmwares 4 + ota/tasks 3 端点)
- `backend/internal/api/routes.go` (`RegisterFirmwareDownload` 独立注册, 无 JWT)
- `backend/internal/models/models.go` (`Firmware` + `OTATask` struct)
- `backend/internal/nodemgr/handler_hello.go` (`HandleHelloOTACompletion` §6.4.3)
- `backend/internal/database/gorm.go` (AutoMigrate)

### 前端
- `frontend-shared/src/views/firmware/FirmwareManage.vue`
- `frontend-shared/src/api/firmware.ts` + `api/ota.ts`
- `frontend-shared/src/stores/firmware.ts`

### 采集器
- `esp32-collector/components/ota/ota.c` (OTA 下载 + TLS 证书验证)
- `esp32-collector/components/ota/ota.h` (OTA 接口)
- `esp32-collector/components/ota_updater/ota_updater.c` (9 步流程)
- `esp32-collector/components/ota_updater/include/ota_updater.h` (`ota_stage_t` 枚举)
- `esp32-collector/components/proto_engine/proto_engine.c` (OtaCommand/Progress 编码)
  - `OTA_STATUS_DOWNLOADING=0, INSTALLING=1, SUCCESS=2, FAILED=3`
- `esp32-collector/main/main.c` (mark_app_valid 早期调用)

## 已实现 (P0 E2E + 文档一致性 review)

- [x] 7 端点 (firmwares 4 + ota/tasks 3)
- [x] 6 态状态机 (pending/downloading/verifying/installing/success/failed)
- [x] 4 wire code 映射 (0/1/2/3)
- [x] 1 server-side 扩展 (WireVerifying=4)
- [x] supersede 旧 in-flight
- [x] Hello 自动完成 (10min 超时)
- [x] fail-open (未知 wire code 保留状态)
- [x] A/B partition + 30s mark valid
- [x] SHA256 校验
- [x] 99% 进度上限
- [x] 文档一致性 7 处修复
- [x] HTTPS OTA 证书验证 (T07-HTTPS)
  - Mozilla CA bundle (公网)
  - 自定义 CA PEM (私有网络)
  - CN 校验
  - 移除 `skip_cert_common_name_check=true` 安全风险

## 测试覆盖

- ✅ `ota_test.go` (OTA 单元测试)
- ✅ E2E 10/10 (commit 2ea4062)
- ✅ 文档一致性 review (commit 2cc59b4)

## OTA 安全 (T07-HTTPS)

### Kconfig 选项

| 选项 | 默认 | 说明 |
|------|------|------|
| `COLLECTOR_OTA_USE_HTTPS` | y | 启用 HTTPS OTA 下载 |
| `COLLECTOR_OTA_VERIFY_CERT` | y | 验证服务器 TLS 证书 |
| `COLLECTOR_OTA_CRT_BUNDLE` | y (默认选择) | 使用 ESP-IDF Mozilla CA 证书包 |
| `COLLECTOR_OTA_CUSTOM_CERT` | n | 嵌入自定义 CA PEM (私有 CA) |
| `COLLECTOR_OTA_CERT_PEM` | "" | 自定义 CA 证书内容 (PEM 格式) |
| `COLLECTOR_OTA_EXPECTED_CN` | "" | 期望的服务器证书 CN |
| `ESP_HTTPS_OTA_ALLOW_HTTP` | n | 允许 HTTP URL (仅开发环境) |

### 三种配置组合

1. **HTTPS + Mozilla CA bundle** (推荐生产环境)
   - `CONFIG_COLLECTOR_OTA_USE_HTTPS=y`
   - `CONFIG_COLLECTOR_OTA_VERIFY_CERT=y`
   - `CONFIG_COLLECTOR_OTA_CRT_BUNDLE=y`
   - 使用 `esp_crt_bundle_attach` 验证公网证书

2. **HTTPS + 自定义 CA** (私有网络/实验室)
   - `CONFIG_COLLECTOR_OTA_CUSTOM_CERT=y`
   - 嵌入 CA PEM 证书，用于自签/私有 CA

3. **HTTP 或 HTTPS 无验证** (仅开发)
   - `CONFIG_COLLECTOR_OTA_USE_HTTPS=n` → 纯 HTTP (WARN)
   - `CONFIG_COLLECTOR_OTA_VERIFY_CERT=n` → HTTPS 但不验证 (WARN)

4. **HTTP (ESP_HTTPS_OTA_ALLOW_HTTP)** (开发/内网)
   - `CONFIG_ESP_HTTPS_OTA_ALLOW_HTTP=y` → HTTPS 库也接受 http:// URL
   - 用于 ESP32 无法访问 HTTPS 固件服务器的内网开发环境
   - 生产环境禁用

### 安全改进

- **移除** `cert_pem = NULL` (之前完全跳过证书验证)
- **移除** `skip_cert_common_name_check = true` (之前跳过 CN 检查)
- **新增** `build_ota_http_config()` 函数，根据 Kconfig 动态配置 TLS
- **新增** `esp_crt_bundle_attach` 支持 (Mozilla CA 证书包)
- **新增** `common_name` 字段支持 (CN 校验)

### 验证脚本

`scripts/verify_https_ota.sh` 本地验证:
- 生成自签 CA 证书
- 启动 HTTPS 服务器
- 验证 TLS 握手 + 证书校验
- 验证错误 CA 被拒绝 (证书固定)

## 未实现
- [ ] cancelled 状态 + §6.4.4 用户主动取消 (P3)
- [ ] 模板使用统计 (P2)
- [ ] MQTT QoS 1 (P3)

详见 [设计/固件OTA/详细设计.md](../设计/固件OTA/详细设计.md) + [验收标准.md](../设计/固件OTA/验收标准.md)
