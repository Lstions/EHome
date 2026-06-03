# 固件 OTA — 实现

## 代码位置

### 后端
- `backend/internal/ota/ota.go` (P3 修: WireVerifying=4, fail-open default)
- `backend/internal/ota/ota_test.go` (单元测试)
- `backend/internal/api/handler_ota.go` (firmwares 4 + ota/tasks 3 端点)
- `backend/internal/models/models.go` (`Firmware` + `OTATask` struct)
- `backend/internal/collector/handler_hello.go` (`HandleHelloOTACompletion` §6.4.3)
- `backend/internal/database/gorm.go` (AutoMigrate)

### 前端
- `frontend-shared/src/views/firmware/FirmwareManage.vue`
- `frontend-shared/src/api/firmware.ts` + `api/ota.ts`
- `frontend-shared/src/stores/firmware.ts`

### 采集器
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

## 测试覆盖

- ✅ `ota_test.go` (OTA 单元测试)
- ✅ E2E 10/10 (commit 2ea4062)
- ✅ 文档一致性 review (commit 2cc59b4)

## 未实现
- [ ] cancelled 状态 + §6.4.4 用户主动取消 (P3)
- [ ] 模板使用统计 (P2)
- [ ] MQTT QoS 1 (P3)

详见 [设计/固件OTA/详细设计.md](../设计/固件OTA/详细设计.md) + [验收标准.md](../设计/固件OTA/验收标准.md)
