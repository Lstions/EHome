# Driver Registry 单一化与 Techfine 元数据修复方案

日期: 2026-07-18
范围: 后端 `/home/sun/workspace/EHomeSystem-gpio/backend`
性质: 设计文档 (only design, not implementation)

---

## 一、问题清单 (事实核对)

### 问题 1: Techfine 多重注册 + 双 Registry 分裂

**调用链** (`cmd/server/main.go:100-103`):
```go
driverRegistry := drivers.NewRegistry()                                       // line 100
drivers.RegisterBuiltInDriversWithParsers(driverRegistry, parserConfigs)      // line 101
drivers.RegisterBuiltInDriversWithParsers(drivers.GlobalRegistry(), parserConfigs) // line 102
```

**结果**: 进程内同时存在两个 Registry 实例，各持有 6 个 driver (BMP280/LKTH01/SN3000/PRS3001/JiabaidaBMS/TechfineInverter) 的引用。Register 实现为 `r.drivers[typeID] = driver` (`registry.go:47`)，map 覆盖不 panic，但产生两条 "Driver registered: techfine_inverter (...)" 日志，并留下设计漏洞。

### 问题 2: 两条解析路径并存 (实质隐患)

| 路径 | Registry 实例 | 使用方 |
|---|---|---|
| A (注入) | `driverRegistry` (main.go:100) | `handler_device.go:523,961` — `/device-configs/test-parser`、`/device-configs/tree` 端点 |
| B (全局) | `drivers.GlobalRegistry()` (main.go:102) | 其它所有热路径:  `handler_edge_device.go:35`, `handler_driver_commands.go:25,49`, `nodemgr/manager.go:463`, `nodemgr/handler_data.go:204`, `nodemgr/sender.go:496,810`, `databus/consumers_heavy.go:184` |

**热路径走 B，端点 tree/test-parser 走 A**。当前 driver 无状态，行为巧合一致；一旦 driver 变成有状态 (缓存 ConfigParser、设备实例句柄、校准数据等)，A/B 分裂将产生行为漂移。

### 问题 3: 旧 `RegisterBuiltInDrivers` 测试与生产不一致

`RegisterBuiltInDrivers` (`builtin.go:295`) 只注册 4 个 driver (BMP280/LKTH01/SN3000/PRS3001)，不注册 JiabaidaBMS / TechfineInverter。两个测试文件使用它:
- `internal/drivers/drivers_test.go:461` — `TestGlobalRegistry` 仅期望 >=4
- `internal/api/handler_edge_device_crud_test.go:45` — EdgeDevice CRUD 测试 setup

生产路径走 `RegisterBuiltInDriversWithParsers` 注册 6 个 driver。测试环境 `GlobalRegistry` 只有 4 个 driver，导致 `createTemplatesFromDriver` (`handler_edge_device.go:34`) 对 techfine/jiabaida 在测试中走 legacy fallback 分支，与生产环境行为不一致。

### 问题 4: Techfine 元数据错误

`internal/drivers/inverter_techfine.go:21-25`:
- DeviceName "Techfine GB3024 逆变器" → 应为 "泰琪丰 GB3024 逆变器"
- OEM "Techfine" → 应为 "泰琪丰"
- HardwareTypes `["GB3024"]` → 应为 `["uart"]` (GB3024 是型号不是总线，实物走 RS232C/UART，见 `inverter_techfine.go:15` "RS232C, 2400bps")

### 问题 5: LK-TH01 OEM 错误

`internal/drivers/builtin.go:113`:
- OEM "路科" → 应为 "蓝控" (LK = 蓝控)

---

## 二、设计目标

1. **单一 Registry 实例**: 进程内只存在一个 `*drivers.Registry`，由 `main.go` 构造，通过依赖注入传递给所有使用方。
2. **废除运行时对包级 `GlobalRegistry` 的使用**: 保留 `GlobalRegistry` 函数仅供测试与向后兼容，标注 deprecated。
3. **测试与生产一致**: 测试环境也能注册全部 6 个 driver。
4. **元数据修正**: Techfine / LK-TH01 OEM/Name/HardwareTypes 三处。
5. **向后兼容**: 不破坏现有 API、不改变 driver 行为、不改变日志可观测性。

---

## 三、修复方案 (分阶段)

### 阶段 1: 立即止血 — 单 Registry 实例 (2 行改动)

**目标**: 消除双 Registry 分裂，使注入路径与全局路径指向同一实例。

**改动**: `cmd/server/main.go:100-102`

改前:
```go
driverRegistry := drivers.NewRegistry()
drivers.RegisterBuiltInDriversWithParsers(driverRegistry, parserConfigs)
drivers.RegisterBuiltInDriversWithParsers(drivers.GlobalRegistry(), parserConfigs)
```

改后:
```go
driverRegistry := drivers.GlobalRegistry()
drivers.RegisterBuiltInDriversWithParsers(driverRegistry, parserConfigs)
```

**效果**:
- `driverRegistry` 与 `drivers.GlobalRegistry()` 是同一实例，所有路径 A/B 统一。
- 删除 `NewRegistry()` 在 main 中的使用 (Registry 仍保留 `NewRegistry` 供测试)。
- 重复注册日志消失。
- 所有现有 `drivers.Get()` 调用无需改动即可工作。

**风险**: `GlobalRegistry` 成为单例，`Register` 仍可在测试中被重复调用 (map 覆盖)，行为不变。

**验证**:
- `make test` 全绿。
- 启动后日志只出现 6 条 "Driver registered: ..." 而非 12 条。
- `/api/v1/device-configs/tree` 返回 6 个 driver 而非分裂状态。

### 阶段 2: 元数据修正 (3 处)

**改动 1**: `internal/drivers/builtin.go:113`
```go
func (d *LKTH01Driver) OEM() string             { return "路科" }
```
→
```go
func (d *LKTH01Driver) OEM() string             { return "蓝控" }
```

**改动 2**: `internal/drivers/inverter_techfine.go:22`
```go
func (d *TechfineInverterDriver) DeviceName() string      { return "Techfine GB3024 逆变器" }
```
→
```go
func (d *TechfineInverterDriver) DeviceName() string      { return "泰琪丰 GB3024 逆变器" }
```

**改动 3**: `internal/drivers/inverter_techfine.go:23`
```go
func (d *TechfineInverterDriver) OEM() string             { return "Techfine" }
```
→
```go
func (d *TechfineInverterDriver) OEM() string             { return "泰琪丰" }
```

**改动 4**: `internal/drivers/inverter_techfine.go:25`
```go
func (d *TechfineInverterDriver) HardwareTypes() []string { return []string{"GB3024"} }
```
→
```go
func (d *TechfineInverterDriver) HardwareTypes() []string { return []string{"uart"} }
```

**影响面**:
- 前端 `/device-configs/tree` 显示的 OEM/Name/HardwareTypes 会变化。前端若硬编码 "Techfine" 字符串做比对，需同步检查。
- `HardwareTypes` 用于向导选择硬件类型 (handler_device.go:960 tree 构造)，"GB3024" 是型号用作 hardware_type 语义不对，改为 "uart" 与其它 UART 设备 (LK-TH01/SN-3000/PRS-3001/Jiabaida) 一致。
- 现有 DeviceConfig 记录中 `hardware_type` 字段值若为 "GB3024"，仍保留在 DB 中，不会被自动迁移；新创建走 "uart"。

**验证**:
- `internal/drivers/inverter_techfine_test.go` 中若硬编码 "Techfine"/"GB3024" 字符串做断言，需同步更新 (待执行时检查)。
- `make test` 全绿。
- 前端向导显示 "泰琪丰" 而非 "Techfine"。

### 阶段 3: 测试一致性 — 旧 `RegisterBuiltInDrivers` 转发

**目标**: 测试环境也能注册全部 6 个 driver，消除测试与生产差异。

**改动**: `internal/drivers/builtin.go:295-301`

改前:
```go
func RegisterBuiltInDrivers(registry *Registry) {
    registry.Register(&BMP280Driver{})
    registry.Register(&LKTH01Driver{})
    registry.Register(&SN3000Driver{})
    registry.Register(&PRS3001Driver{})
    // JiabaidaBMSDriver registered in RegisterBuiltInDriversWithParsers
}
```

改后:
```go
// RegisterBuiltInDrivers registers all built-in drivers (legacy, without ConfigParser overrides).
// Deprecated: use RegisterBuiltInDriversWithParsers. Kept for tests; forwards to the full
// registration path with nil parserConfigs so JiabaidaBMS and TechfineInverter are also registered.
func RegisterBuiltInDrivers(registry *Registry) {
    RegisterBuiltInDriversWithParsers(registry, nil)
}
```

**效果**:
- `drivers_test.go:461` `TestGlobalRegistry` 期望 >=4 现在实际得到 6，测试仍通过。
- `handler_edge_device_crud_test.go:45` 在 GlobalRegistry 中得到全部 6 个 driver，`createTemplatesFromDriver` 对 techfine/jiabaida 不再走 legacy fallback。

**风险**: 若有测试断言 `List()` 长度恰好等于 4，会失败 — 需要扫描。`drivers_test.go:464` 使用 `len(types) < 4` (不等式)，安全。

**验证**:
- `make test` 全绿。
- 测试中 `drivers.Get("techfine_inverter")` 能返回 driver。

### 阶段 4: 中期 — 依赖注入重构 (可选，独立排期)

**目标**: 彻底消除 `drivers` 包级 `globalRegistry` 在生产路径中的使用，所有 driver 访问通过构造时注入的 `*Registry`。

**涉及改动**:

1. `internal/nodemgr/manager.go`:
   - `Manager` struct 新增字段 `drivers *drivers.Registry`
   - `NewManager` 签名追加参数 `driverRegistry *drivers.Registry`
   - 替换 `manager.go:463` `drivers.Get(dev.Type)` → `m.drivers.Get(dev.Type)`
   - 替换 `handler_data.go:204` 同上
   - 替换 `sender.go:496,810` 同上

2. `internal/databus/consumers_heavy.go`:
   - `SensorParserConsumer` struct 新增字段 `drivers *drivers.Registry`
   - `NewSensorParserConsumer` 签名追加参数 `driverRegistry *drivers.Registry`
   - 替换 `consumers_heavy.go:184` `drivers.Get(device.Type)` → `c.drivers.Get(device.Type)`
   - `manager.go:125,127` 调用 `NewSensorParserConsumer` 时传入 `mgr.drivers`

3. `internal/api/handler_edge_device.go`:
   - `registerEdgeDeviceRoutes` 签名追加 `driverRegistry *drivers.Registry` 参数
   - `handler_edge_device.go:35` `drivers.Get(dev.Type)` → 通过闭包捕获的 `driverRegistry.Get(dev.Type)`
   - `routes.go:66` 调用处传入 `driverRegistry`

4. `internal/api/handler_driver_commands.go`:
   - `registerDriverCommandRoutes` 签名追加 `driverRegistry *drivers.Registry` 参数
   - `handler_driver_commands.go:25,49` 同上替换
   - `routes.go:65` 调用处传入 `driverRegistry`

5. `cmd/server/main.go:125`:
   - `nodemgr.NewManager(db, mqttClient, wsHub, haIntegration, offlineDetector, otaMgr, driverRegistry)`

6. `internal/drivers/registry.go`:
   - `GlobalRegistry` / `Get` / `List` / `Register` 包级函数标注 `// Deprecated: use injected *Registry`，保留供测试。

7. 所有测试文件中 `nodemgr.NewManager(db, nil, nil, nil, nil, nil)` 调用 (28 处) 需追加 `drivers.GlobalRegistry()` 或新建 registry 参数。考虑提供测试辅助函数 `newTestManager(db)` 减少重复。

**影响面**:
- 28 处测试调用 NewManager 需更新签名。
- `internal/api/handler_test.go` 等 30+ 处测试需同步。
- 接口/行为不变，纯内部重构。

**验证**:
- `make test` 全绿。
- `grep -rn 'drivers.Get\|drivers.GlobalRegistry\|drivers.List' --include='*.go' | grep -v _test.go | grep -v 'drivers/registry.go'` 应返回 0 行 (除 deprecated 注释)。

**此阶段独立排期，不阻塞阶段 1-3 的修复**。

---

## 四、执行顺序

| 步骤 | 阶段 | 改动量 | 风险 | 阻塞性 |
|---|---|---|---|---|
| 1 | 阶段 1 (main.go 单 Registry) | 2 行 | 极低 | 立即 |
| 2 | 阶段 2 (元数据 3 处) | 4 行 | 低 (前端显示变化) | 立即 |
| 3 | 阶段 3 (RegisterBuiltInDrivers 转发) | 6 行 | 低 (测试 List 长度) | 立即 |
| 4 | 阶段 4 (依赖注入重构) | ~80 行 + 28 处测试 | 中 (签名破坏) | 独立排期 |

步骤 1-3 可在一次 commit 内完成；步骤 4 单独 PR。

---

## 五、验收清单

阶段 1-3 完成后:

- [ ] `cmd/server/main.go` 不再出现 `NewRegistry()` + 二次 `RegisterBuiltInDriversWithParsers` 重复注册。
- [ ] `internal/drivers/builtin.go` LKTH01Driver OEM 为 "蓝控"。
- [ ] `internal/drivers/inverter_techfine.go` DeviceName 含 "泰琪丰"，OEM 为 "泰琪丰"，HardwareTypes 为 `["uart"]`。
- [ ] `internal/drivers/builtin.go` `RegisterBuiltInDrivers` 调用 `RegisterBuiltInDriversWithParsers(registry, nil)`。
- [ ] `make test` 全绿。
- [ ] `make lint` 不引入新错误 (lint-frontend exit 2 预存可接受)。
- [ ] 后端启动日志只出现 6 条 "Driver registered: ..."。
- [ ] `/api/v1/device-configs/tree` 返回 6 个 driver，Techfine 显示 "泰琪丰"。
- [ ] `internal/drivers/inverter_techfine_test.go` 中如硬编码 "Techfine"/"GB3024" 字符串做断言，已同步更新。

阶段 4 (独立 PR):

- [ ] 生产代码中不再出现 `drivers.Get` / `drivers.GlobalRegistry()` / `drivers.List()` 直接调用 (除 deprecated 注释)。
- [ ] `Manager`、`SensorParserConsumer`、`registerEdgeDeviceRoutes`、`registerDriverCommandRoutes` 全部通过依赖注入持有 `*drivers.Registry`。

---

## 六、关联文件清单

**直接修改**:
- `cmd/server/main.go` (line 100-102)
- `internal/drivers/builtin.go` (line 113, 295-301)
- `internal/drivers/inverter_techfine.go` (line 22, 23, 25)

**阶段 4 涉及**:
- `internal/nodemgr/manager.go` (struct + NewManager + line 463)
- `internal/nodemgr/handler_data.go` (line 204)
- `internal/nodemgr/sender.go` (line 496, 810)
- `internal/databus/consumers_heavy.go` (struct + NewSensorParserConsumer + line 184)
- `internal/api/handler_edge_device.go` (line 35, 189)
- `internal/api/handler_driver_commands.go` (line 25, 49, 18)
- `internal/api/routes.go` (line 65, 66)
- `internal/drivers/registry.go` (deprecated 标注)
- 28 处测试文件中 `nodemgr.NewManager` 调用

**测试需检查**:
- `internal/drivers/inverter_techfine_test.go` (可能硬编码 "Techfine"/"GB3024")
- `internal/drivers/drivers_test.go:461` (TestGlobalRegistry 期望 >=4)
- `internal/api/handler_edge_device_crud_test.go:45` (RegisterBuiltInDrivers)

---

## 七、决策建议

- 阶段 1-3 一起做，diff 小、风险低、立即解决元数据错误与双 Registry 分裂。
- 阶段 4 是中长期清洁化重构，独立 PR 排期，避免与元数据修复混在一起增加 review 难度。
- 若用户偏好"一步到位"，可合并阶段 1-4 一次完成，但需投入更多测试更新工作。
