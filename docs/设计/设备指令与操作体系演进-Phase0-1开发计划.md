# 设备指令与操作体系演进 Phase 0+1 开发计划（C1–C7）

> **状态**: 任务书（交付弱模型执行者 qwen3.8-flash 逐 commit 实施）
> **版本**: v1.0
> **日期**: 2026-09-06
> **母本**: [设备指令与操作体系演进方案.md](设备指令与操作体系演进方案.md)（main 已入库，commit ec3a8712）
> **基线**: main @ f15f9c30，工作分支 `feat/command-action-evolution`（等同 main，工作区干净）
> **配套文档**: [设备指令与操作体系演进-Phase0-1接口与测试设计.md](设备指令与操作体系演进-Phase0-1接口与测试设计.md)
> **范围**: 仅 Phase 0（C1–C5）+ Phase 1（C6–C7）。Phase 2+（动作门禁台账/前端 Risk 展示）不在本次。

---

## 0. 执行纪律（任务书，逐条遵守，违反即返工）

1. **严格按 C1→C7 顺序实施，一个 commit 一验证**。任何 commit 测试红（`go test ./...` 非零）不得进入下一 commit。
2. **禁止实现本计划未声明的功能**；禁止顺手重构、改名、格式化无关代码（gofmt 仅限本 commit 改动文件）。
3. **禁止修改锚定测试**及其断言：
   - `frontend-shared/src/components/device/__tests__/CommandListControlBoundary.spec.ts`
   - `frontend-shared/e2e/sn3001-real.spec.ts`
   - `frontend-shared/e2e/sn3001-real-writes.spec.ts`
   - `backend/internal/api/handler_edge_device_command_intervals_test.go`（创建路径既有测试，C1 后必须原样全绿）
   - 每个 commit 后执行 `git diff --name-only HEAD~1 HEAD` 自查：上述文件不得出现。
4. **修 bug 类 commit 必须同步新增测试锁定**：C2（PUT 校验）、C3（GET 过滤）、C4（契约）、C7（防线）均为新增测试。
5. **C2 存量清洗必须先于 PUT 校验生效**（同 commit，部署顺序见 C2 §部署顺序）；脚本必须幂等（跑两遍结果一致，测试锁定）。
6. **C6 审计 SQL 先跑**，结果（含空结果）写入 commit message 后才允许删除 11 条 query_*；审计非空 → **停止并上报主 Agent，不自行归属、不自行删除**。
7. **禁止实现蓝本"不做清单" N1–N8**（见 §5）。
8. 每个 commit 的退出门禁（§3 各节）全部满足后才 `git commit`。commit message 用 §3 各节给定模板，不得省略审计证据/测试证据。

## 1. 关键事实速查（执行者自带，勿依赖蓝本；行号已实测核对）

| # | 事实 | 证据（file:line，实测） |
|---|------|------|
| K1 | `CommandTemplate{Schedulable:false}` = "trigger command" 第三态，本方案废除 | `backend/internal/drivers/command_template.go:5-11` |
| K2 | 创建路径已有校验：`validateAndNormalizeCommandIntervals`（拒绝未知 ID + 非 schedulable ID，负数归零，返回 JSON） | `backend/internal/api/handler_edge_device.go:66-107`，调用点 `:568-574` |
| K3 | PUT /edge-devices/:id/commands 只 merge + 负数归零，**无校验** | `backend/internal/api/handler_driver_commands.go:88-133` |
| K4 | GET /drivers/:type/commands **不过滤** | `handler_driver_commands.go:32-33` |
| K5 | GET /edge-devices/:id/commands **不过滤** | `handler_driver_commands.go:56-77` |
| K6 | manifest 编码候选集：`Schedulable && interval>0 && 模板存在` | `backend/internal/nodemgr/sender_snapshot.go:167-185` |
| K7 | template_backfill 匹配函数 `driverTemplateMatches` 注释声称 schedulable，但循环**不看** Schedulable，按 WriteData 全量匹配 | `backend/internal/datalifecycle/template_backfill.go:106-116` |
| K8 | techfine 11 条 query_* 全部 `Schedulable:false`，`GetCommandTemplates` 返回它 | `backend/internal/drivers/inverter_techfine.go:840-861` |
| K9 | `legacyUnsafeCommandTemplates` 死代码 **228 行**（实测 awk 计数 869–1096），`return nil` + 大段注释块 | `inverter_techfine.go:863-1096` |
| K10 | 守卫测试断言 `legacyUnsafeCommandTemplates() != nil` 失败即红 | `backend/internal/drivers/inverter_techfine_test.go:631-635` |
| K11 | `TestTechfine_CommandTemplates` 断言 11 条且非 schedulable（C6 要改写） | `inverter_techfine_test.go:555-592`（蓝本写 555-585，实测到 592） |
| K12 | `asciiToHex` 调用方：`inverter_techfine.go:849-859`（11 条 query_*）+ 测试 `:198,:213`；`crc16ToHex` 调用方仅在 863-1096 死代码块内 | 实测 grep |
| K13 | `CRC16Modbus` 定义在 jiabaida.go:1864，被 jiabaida 自身使用——**不得删除** | `backend/internal/drivers/jiabaida.go:1863-1864` |
| K14 | 内置驱动注册入口 `RegisterBuiltInDrivers(registry *Registry)`，内部 `registry.Register(...)` 9 个驱动 | `backend/internal/drivers/builtin.go:844-894` |
| K15 | `Registry.List() []string` 返回全部注册类型 | `backend/internal/drivers/registry.go:62-68` |
| K16 | `GetCommandTemplates` 实现全集（9 处）：builtin.go:98/140/272/283/355、jiabaida.go:1892、inverter_techfine.go:847、generic_modbus.go:45、generic_i2c.go:44 | 实测 grep |
| K17 | `models.EdgeDevice.CommandIntervals` 是 `json.RawMessage`（JSONB，serializer:json） | `backend/internal/models/models.go:102` |
| K18 | 前端客户端过滤（防御性保留，不动）：`CommandList.vue:131`、`CreateWizardCommandIntervals.vue:109` | 实测 |
| K19 | 前端锚定：`CommandListControlBoundary.spec.ts:54`（触发指令不得出现）、`sn3001-real.spec.ts:24-28`（同断言，Playwright 实机） | 实测 |
| K20 | ehomectl 已有子命令模式：`ehomectl datalifecycle backfill`，复用 `connectDB()` | `backend/cmd/ehomectl/main.go:19-54,59-97` |
| K21 | api 测试基建：`setupEdgeDeviceTest`（sqlite 内存 + 全量 AutoMigrate + JWT + 内置驱动） | `backend/internal/api/handler_edge_device_crud_test.go:26-51` |
| K22 | 既有 api 测试**没有**覆盖 /drivers/:type/commands 与 PUT /edge-devices/:id/commands（grep 0 命中）——C2/C3/C4 新增测试无冲突 | 实测 grep |
| K23 | 11 帧 hex（WriteData 大写形式）：485354530D / 48475249440D / 484F500D / 484241540D / 4850560D / 485056420D / 4854454D500D / 4847454E0D / 48424D53310D / 48454550310D / 48494D5347310D | python3 实测计算 |

## 2. 全局门禁与验证命令

```bash
cd /home/sun/workspace/EHomeSystem/backend
go build ./...          # 必须 0 错误
go test ./...           # 必须全绿（每个 commit 后跑）
gofmt -l internal/ cmd/ | grep -v '^$'   # 本 commit 改动文件必须无输出
```

前端本次**零改动**；锚定前端测试不随本计划修改。可选回归（有 node 环境时）：
```bash
cd /home/sun/workspace/EHomeSystem/frontend-shared
pnpm test:run src/components/device/__tests__/CommandListControlBoundary.spec.ts
```
`sn3001-real.spec.ts` 需要实机 rig（`EHOME_E2E_ADMIN_PASSWORD`），本地无法运行——门禁改为"文件未被修改"（`git diff --name-only` 不含它）。

---

## 3. Commit 逐条设计

### C1 — refactor(api): 提取 command interval 校验 helper（校验/归一化分离）

**目标**：把创建路径的"校验 + 归一化"拆成三个纯 helper，供 C2（PUT 校验）与 C4（契约测试）复用；行为零变化。

**改动文件**：
1. 新建 `backend/internal/api/command_intervals.go`（全部新代码，见下）
2. `backend/internal/api/handler_edge_device.go`：删除 66–107 行（原函数整体移入新文件）

**函数级设计**（新文件完整内容，直接粘贴）：

```go
package api

import (
	"encoding/json"
	"fmt"

	"ehome/backend/internal/drivers"
)

// SchedulableCommandIDs returns the set of schedulable command template IDs
// declared by the driver registered for devType. A driver that is not
// registered or provides no templates yields an error — callers treat the
// schedulable set as authoritative (I-1: command_intervals keys ⊆ this set).
func SchedulableCommandIDs(driverRegistry *drivers.Registry, devType string) (map[string]struct{}, error) {
	drv, err := driverRegistry.Get(devType)
	if err != nil {
		return nil, fmt.Errorf("cannot validate command_intervals: driver for type %q is not registered", devType)
	}
	provider, ok := drv.(drivers.CommandTemplateProvider)
	if !ok {
		return nil, fmt.Errorf("cannot validate command_intervals: driver for type %q provides no command templates", devType)
	}
	schedulable := make(map[string]struct{}, 8)
	for _, tmpl := range provider.GetCommandTemplates() {
		if tmpl.Schedulable {
			schedulable[tmpl.ID] = struct{}{}
		}
	}
	return schedulable, nil
}

// ValidateCommandIntervals rejects any command id that is unknown or belongs
// to a non-schedulable (one-shot) template of the driver registered for
// devType. This is the single authority for invariant I-1 and is shared by
// the create path (POST /edge-devices) and the update path
// (PUT /edge-devices/:id/commands).
func ValidateCommandIntervals(driverRegistry *drivers.Registry, devType string, intervals map[string]int) error {
	schedulable, err := SchedulableCommandIDs(driverRegistry, devType)
	if err != nil {
		return err
	}
	for id := range intervals {
		if _, ok := schedulable[id]; !ok {
			return fmt.Errorf("command_intervals: command %q is not a schedulable command of driver %q", id, devType)
		}
	}
	return nil
}

// NormalizeCommandIntervals returns a copy of intervals with negative values
// clamped to 0 (0 = disabled). Pure function; does not validate ids.
func NormalizeCommandIntervals(intervals map[string]int) map[string]int {
	normalized := make(map[string]int, len(intervals))
	for id, interval := range intervals {
		if interval < 0 {
			interval = 0
		}
		normalized[id] = interval
	}
	return normalized
}

// validateAndNormalizeCommandIntervals is the create-path entry point: it
// validates then normalizes, returning the JSON payload to persist (nil when
// the input map is empty). Kept as a thin composition of the shared helpers
// so POST and PUT share one validation authority.
func validateAndNormalizeCommandIntervals(driverRegistry *drivers.Registry, devType string, intervals map[string]int) (json.RawMessage, error) {
	if len(intervals) == 0 {
		return nil, nil
	}
	if err := ValidateCommandIntervals(driverRegistry, devType, intervals); err != nil {
		return nil, err
	}
	raw, err := json.Marshal(NormalizeCommandIntervals(intervals))
	if err != nil {
		return nil, fmt.Errorf("failed to marshal command_intervals: %w", err)
	}
	return raw, nil
}
```

**handler_edge_device.go 删除锚点**：删除第 66–107 行整段（从 `// validateAndNormalizeCommandIntervals validates a create-time` 到该函数右花括号 `}`）。删除后 `handler_edge_device.go:568-574` 的调用点**不改**（同包函数，签名不变）。删除后确认该文件 `json`、`fmt`、`drivers` import 仍被其他函数使用（getTemplateParamsFromDeviceConfig 用 json/fmt，createTemplatesFromDriver 用 drivers）——**不要**动 import。

**新增测试** `backend/internal/api/command_intervals_test.go`（同包，用假驱动）：

```go
package api

import (
	"testing"

	"ehome/backend/internal/drivers"
)

// fakeMultiDriver 供 C1-C4 测试使用：两个 schedulable + 一个 one-shot 模板。
// 注意：本计划所有 api 测试禁止用 techfine 当非 schedulable fixture（C6 会删它）。
type fakeMultiDriver struct{}

func (d *fakeMultiDriver) DeviceType() string            { return "fake_multi" }
func (d *fakeMultiDriver) DeviceName() string            { return "契约测试假驱动" }
func (d *fakeMultiDriver) OEM() string                   { return "test" }
func (d *fakeMultiDriver) Category() string              { return "test" }
func (d *fakeMultiDriver) HardwareTypes() []string       { return []string{"uart"} }
func (d *fakeMultiDriver) GetSensorDefinitions() []drivers.SensorData { return nil }
func (d *fakeMultiDriver) ParseData([]byte) ([]drivers.SensorData, error) { return nil, nil }
func (d *fakeMultiDriver) GetCommandTemplates() []drivers.CommandTemplate {
	return []drivers.CommandTemplate{
		{ID: "read_a", Name: "读A", Type: "read", WriteData: "AA01", ReadLength: 4, IntervalMs: 5000, Schedulable: true},
		{ID: "read_b", Name: "读B", Type: "read", WriteData: "AA02", ReadLength: 4, IntervalMs: 0, Schedulable: true},
		{ID: "one_shot", Name: "一次性", Type: "write", WriteData: "AA03", ReadLength: 4, IntervalMs: 0, Schedulable: false},
	}
}

func newFakeRegistry() *drivers.Registry {
	r := drivers.NewRegistry()
	r.Register(&fakeMultiDriver{})
	return r
}
```

**C1 测试清单**（新增 `command_intervals_test.go`）：

| 用例名 | 断言 | 所在文件 |
|--------|------|----------|
| TestSchedulableCommandIDs_ReturnsSchedulableOnly | 对 fake_multi 返回 {read_a, read_b}，不含 one_shot | command_intervals_test.go |
| TestSchedulableCommandIDs_UnknownTypeErrors | 未注册类型返回 error | 同上 |
| TestValidateCommandIntervals_RejectsUnknownId | {"nope":1} → error | 同上 |
| TestValidateCommandIntervals_RejectsNonSchedulableId | {"one_shot":1} → error | 同上 |
| TestValidateCommandIntervals_AcceptsSchedulableIds | {"read_a":1,"read_b":0} → nil | 同上 |
| TestNormalizeCommandIntervals_ClampsNegatives | {-5→0, 3→3}；输入 map 不被修改 | 同上 |

**退出门禁**：
```bash
cd backend && go build ./... && go test ./...
```
- 既有 `handler_edge_device_command_intervals_test.go` 全部用例原样全绿（行为零变化证明）。
- `git diff --name-only HEAD~1 HEAD` 只含两个文件。

**风险与回滚**：纯重构，无行为变化。回滚 = `git revert C1`（无依赖）。

**commit message 模板**：
```
refactor(api): 提取 command interval 校验 helper（校验/归一化分离）

- 新建 internal/api/command_intervals.go: SchedulableCommandIDs /
  ValidateCommandIntervals / NormalizeCommandIntervals
- validateAndNormalizeCommandIntervals 改为三个 helper 的组合（创建路径行为不变）
- 新增 command_intervals_test.go 锁定 helper 契约
- 演进方案 C1；为 C2 PUT 校验与 C4 契约测试铺路
```

---

### C2 — fix(api): PUT /edge-devices/:id/commands 复用 schedulable 校验 + 存量清洗脚本

**目标**：PUT 复用 C1 helper enforce I-1（对 merge 后全量 map 校验）；同 commit 交付幂等存量清洗工具，先清洗后上线校验。

**改动文件**：
1. `backend/internal/api/handler_driver_commands.go`：PUT 流程插入校验（行号锚点 107–116）
2. 新建 `backend/internal/datalifecycle/command_intervals_cleanup.go`（清洗核心函数）
3. 新建 `backend/internal/datalifecycle/command_intervals_cleanup_test.go`（幂等测试）
4. 新建 `backend/cmd/ehomectl/command_intervals.go`（CLI 接线）
5. `backend/cmd/ehomectl/main.go`：注册子命令（行号锚点 29–33 switch、39–42 usage）

**PUT 改动**（`handler_driver_commands.go` 第 107–116 行，old → new 精确替换）：

old（107–116 行）：
```go
		// Merge with existing intervals
		existing := parseCommandIntervals(dev.CommandIntervals)
		for cmdID, interval := range req.Intervals {
			if interval < 0 {
				interval = 0
			}
			existing[cmdID] = interval
		}

		intervalsJSON, err := json.Marshal(existing)
```

new：
```go
		// Merge with existing intervals (partial update: request keys overlay
		// the stored map).
		existing := parseCommandIntervals(dev.CommandIntervals)
		for cmdID, interval := range req.Intervals {
			existing[cmdID] = interval
		}

		// I-3 (演进方案 §3.1): validate the *merged* map against the driver's
		// schedulable set — the same helper as the create path. Unknown ids
		// and non-schedulable ids are rejected with 400 before any DB write.
		// Validation runs on the merged map (not the request subset) because
		// PUT is a partial update; legacy dirty keys are removed by the C2
		// cleanup script before this gate goes live.
		if err := ValidateCommandIntervals(driverRegistry, dev.Type, existing); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"code": 400, "message": err.Error()})
			return
		}
		existing = NormalizeCommandIntervals(existing)

		intervalsJSON, err := json.Marshal(existing)
```

> 语义说明：负数归零从 merge 循环移到校验后的 `NormalizeCommandIntervals`（校验只看键不看值，顺序无影响）。`driverRegistry` 已在 `registerDriverCommandRoutes` 第 19 行解析，直接可用。PUT 对"驱动未注册类型"的设备现在返回 400（旧行为直接落库）——这是 I-1 的预期收口，见接口文档 §1 契约变更表。

**清洗核心函数**（新文件 `backend/internal/datalifecycle/command_intervals_cleanup.go` 完整内容）：

```go
package datalifecycle

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"gorm.io/gorm"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
)

// CommandIntervalsCleanupReport summarizes one cleanup pass.
type CommandIntervalsCleanupReport struct {
	DevicesScanned int
	DevicesCleaned int
	KeysRemoved    int
}

// CleanupCommandIntervals removes every key of edge_devices.command_intervals
// that is not a schedulable command template id of the device type's driver
// (I-1 enforcement on legacy data; 演进方案 C2/M4).
//
// Handles the three legacy shapes: NULL, empty map, and maps containing
// non-schedulable keys. A map that becomes empty is stored as NULL.
// A device whose type has no registered driver (or no template provider)
// has an empty schedulable set, so all its keys are removed.
//
// Idempotent: a second pass finds no non-schedulable keys and writes nothing.
func CleanupCommandIntervals(db *gorm.DB, registry *drivers.Registry) (CommandIntervalsCleanupReport, error) {
	var report CommandIntervalsCleanupReport
	if registry == nil {
		return report, fmt.Errorf("datalifecycle: cleanup command_intervals requires a driver registry")
	}
	var devices []models.EdgeDevice
	if err := db.Find(&devices).Error; err != nil {
		return report, fmt.Errorf("datalifecycle: scan edge_devices: %w", err)
	}
	for i := range devices {
		dev := &devices[i]
		report.DevicesScanned++
		intervals := parseCleanupIntervals(dev.CommandIntervals)
		if len(intervals) == 0 {
			continue // NULL or empty map: nothing to remove
		}
		schedulable := map[string]struct{}{}
		if drv, err := registry.Get(dev.Type); err == nil {
			if provider, ok := drv.(drivers.CommandTemplateProvider); ok {
				for _, tmpl := range provider.GetCommandTemplates() {
					if tmpl.Schedulable {
						schedulable[tmpl.ID] = struct{}{}
					}
				}
			}
		}
		cleaned := make(map[string]int, len(intervals))
		removed := 0
		for id, v := range intervals {
			if _, ok := schedulable[id]; ok {
				cleaned[id] = v
			} else {
				removed++
			}
		}
		if removed == 0 {
			continue
		}
		var raw any
		if len(cleaned) == 0 {
			raw = nil // empty after cleanup → NULL
		} else {
			b, err := json.Marshal(cleaned)
			if err != nil {
				return report, fmt.Errorf("datalifecycle: marshal cleaned intervals for device %d: %w", dev.ID, err)
			}
			raw = json.RawMessage(b)
		}
		if err := db.Model(&models.EdgeDevice{}).Where("id = ?", dev.ID).
			Update("command_intervals", raw).Error; err != nil {
			return report, fmt.Errorf("datalifecycle: update command_intervals for device %d: %w", dev.ID, err)
		}
		report.DevicesCleaned++
		report.KeysRemoved += removed
		slog.Info("datalifecycle: command_intervals cleaned",
			"edge_device", dev.ID, "type", dev.Type, "keys_removed", removed)
	}
	return report, nil
}

func parseCleanupIntervals(raw json.RawMessage) map[string]int {
	if len(raw) == 0 {
		return nil
	}
	var m map[string]int
	if err := json.Unmarshal(raw, &m); err != nil {
		return nil
	}
	return m
}
```

**CLI 接线**（新文件 `backend/cmd/ehomectl/command_intervals.go` 完整内容）：

```go
package main

import (
	"fmt"
	"os"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/drivers"
)

// runCommandIntervalsCleanupCLI implements `ehomectl command-intervals cleanup`.
// 演进方案 C2/M4: removes non-schedulable keys from every
// edge_devices.command_intervals before the PUT validation gate goes live.
// Idempotent — running twice yields the same database state.
func runCommandIntervalsCleanupCLI() {
	if len(os.Args) < 3 || os.Args[2] != "cleanup" {
		fatal("usage: ehomectl command-intervals cleanup")
	}
	db := connectDB()
	registry := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(registry)

	report, err := datalifecycle.CleanupCommandIntervals(db, registry)
	if err != nil {
		fatal(fmt.Sprintf("command-intervals cleanup failed: %v", err))
	}
	fmt.Printf("command-intervals cleanup: devices_scanned=%d devices_cleaned=%d keys_removed=%d\n",
		report.DevicesScanned, report.DevicesCleaned, report.KeysRemoved)
}
```

`backend/cmd/ehomectl/main.go` 两处改动：
- switch（29–33 行）加分支：`case "command-intervals": runCommandIntervalsCleanupCLI()`
- usage()（39–42 行）字符串加一行：`"       ehomectl command-intervals cleanup"`

**C2 测试清单**：

新增 `backend/internal/api/handler_driver_commands_test.go`（PUT 校验）：

| 用例名 | 断言 | 所在文件 |
|--------|------|----------|
| TestPutCommands_RejectsNonSchedulableId | 设备 fake_multi 存 {read_a:5000}；PUT {one_shot:1000} → 400；DB 不变（无 one_shot 键） | handler_driver_commands_test.go |
| TestPutCommands_RejectsUnknownId | PUT {nope:1000} → 400；DB 不变 | 同上 |
| TestPutCommands_MergesPartialUpdate | 存 {read_a:5000}；PUT {read_b:3000} → 200；DB == {read_a:5000, read_b:3000} | 同上 |
| TestPutCommands_NormalizesNegative | PUT {read_a:-5} → 200；DB read_a==0 | 同上 |
| TestPutCommands_RejectsLegacyDirtyStoredKey | 存 {one_shot:1000}（模拟清洗前脏数据）；PUT {read_a:5000} → 400（merge 后含脏键）——锁定"清洗必须先于校验"前提 | 同上 |
| TestPutCommands_DriverNotFound | 设备 type="ghost_type"；PUT {x:1} → 400（driver not registered） | 同上 |

测试基建：新文件内定义 `setupDriverCommandsTest(t)`——照抄 `setupEdgeDeviceTest`（handler_edge_device_crud_test.go:26-51）的 sqlite/AutoMigrate/JWT 骨架，但 registry 用 `newFakeRegistry()`（不注册内置驱动），并调用 `registerDriverCommandRoutes(v1, db, mgr, registry)` 代替 `registerEdgeDeviceRoutes`。设备行：`db.Create(&models.EdgeDevice{Name:"D", NodeID:"NODE001", ChannelID:1, Type:"fake_multi", CommandIntervals: json.RawMessage(...)})`。

新增 `backend/internal/datalifecycle/command_intervals_cleanup_test.go`（清洗幂等，用 `testutil.OpenTestDB(t)` + 内置 registry + 测试内假驱动）：

| 用例名 | 断言 | 所在文件 |
|--------|------|----------|
| TestCleanup_NullStaysNull | CommandIntervals 为 NULL 的设备：report.DevicesCleaned==0，DB 仍 NULL | command_intervals_cleanup_test.go |
| TestCleanup_RemovesNonSchedulableKeepsSchedulable | jiabaida_bms 设备存 {read_basic_info:3000, close_discharge_mos:100} → 清洗后只剩 read_basic_info | 同上 |
| TestCleanup_EmptyAfterCleanBecomesNull | 只存非 schedulable 键 → 清洗后 command_intervals IS NULL | 同上 |
| TestCleanup_DriverlessTypeRemovesAll | type="ghost" 设备存 {a:1} → 清洗后 NULL | 同上 |
| TestCleanup_Idempotent | 跑两遍：第二遍 DevicesCleaned==0 且 DB 状态与第一遍后一致 | 同上 |

**退出门禁**：
```bash
cd backend && go build ./... && go test ./...
go run ./cmd/ehomectl command-intervals cleanup   # 本地无 DB 时跳过；语法编译由 go build 保证
```
- PUT 新测试全绿；清洗幂等测试全绿；既有测试全绿。

**部署顺序（运维，写进 commit message）**：① 合并 C2 → ② 停服前先跑 `ehomectl command-intervals cleanup`（对生产 DB）→ ③ 记录输出 → ④ 启动带 PUT 校验的服务。清洗与校验同 commit 保证二者不会只上线其一。

**风险与回滚**：风险=存量脏数据未清洗即上线 PUT 校验 → PUT 全量校验 400（TestPutCommands_RejectsLegacyDirtyStoredKey 已锁定该语义）。回滚 = `git revert C2`：恢复旧 PUT 语义；注意清洗是单向数据操作，revert 不会恢复被删的脏键（这些键按 I-1 本就不合法，可接受）。清洗脚本幂等，重复执行无副作用。

**commit message 模板**：
```
fix(api): PUT /edge-devices/:id/commands 复用 schedulable 校验 + 存量清洗脚本

- PUT 对 merge 后全量 map 执行 ValidateCommandIntervals（I-3，与创建路径同 helper）
- 新增 datalifecycle.CleanupCommandIntervals + ehomectl command-intervals cleanup
  （幂等：二遍结果一致，测试锁定）
- 部署顺序：先跑清洗再上线校验（同 commit 保证）
- 演进方案 C2/M3/M4
```

---

### C3 — fix(api): GET drivers/:type/commands 与 edge-devices/:id/commands 过滤 schedulable

**目标**：两个 GET 端点后端过滤非 schedulable 模板（I-2）。前端客户端过滤保留为防御（不动前端）。

**改动文件**：仅 `backend/internal/api/handler_driver_commands.go`。

**改动 1**（第 32–33 行，old → new）：
old：
```go
		cmds := getCommandTemplates(drv)
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": cmds})
```
new：
```go
		cmds := getCommandTemplates(drv)
		// I-2: only schedulable polling templates are returned; one-shot
		// commands belong to the Action Catalog (DeviceControlPanel).
		c.JSON(http.StatusOK, gin.H{"code": 200, "data": filterSchedulableTemplates(cmds)})
```

**改动 2**（第 56 行，old → new）：
old：
```go
		templates := getCommandTemplates(drv)
```
new：
```go
		templates := filterSchedulableTemplates(getCommandTemplates(drv))
```
（后续 `make([]commandView, len(templates))` 基于过滤后长度，无需改。）

**改动 3**（文件末尾，`getCommandTemplates` 函数后追加 helper）：
```go
// filterSchedulableTemplates returns only Schedulable templates, preserving
// order. I-2: GET endpoints return schedulable templates only; the frontend's
// client-side filter (CommandList.vue:131, CreateWizardCommandIntervals.vue:109)
// stays as defense in depth.
func filterSchedulableTemplates(templates []drivers.CommandTemplate) []drivers.CommandTemplate {
	filtered := make([]drivers.CommandTemplate, 0, len(templates))
	for _, t := range templates {
		if t.Schedulable {
			filtered = append(filtered, t)
		}
	}
	return filtered
}
```

**C3 测试清单**（追加到 `handler_driver_commands_test.go`，复用 C2 基建）：

| 用例名 | 断言 | 所在文件 |
|--------|------|----------|
| TestGetDriverCommands_FiltersNonSchedulable | GET /drivers/fake_multi/commands → 200，ID 集 == {read_a, read_b}，不含 one_shot | handler_driver_commands_test.go |
| TestGetEdgeDeviceCommands_FiltersNonSchedulable | 设备 fake_multi；GET /edge-devices/1/commands → 200，ID 集 == {read_a, read_b} | 同上 |
| TestGetEdgeDeviceCommands_OverlayStillWorks | 存 {read_a:3000} → read_a.current_interval_ms==3000，read_b==模板默认 IntervalMs | 同上 |
| TestGetDriverCommands_UnknownDriver404 | GET /drivers/nope/commands → 404（既有语义回归锁定） | 同上 |

**退出门禁**：`cd backend && go build ./... && go test ./...` 全绿；锚定前端测试文件未被修改（后端过滤后"触发指令不得出现"依然成立——前端过滤是超集防御）。

**风险与回滚**：低。techfine 设备两个 GET 从"11 条非 schedulable"变为"空数组"（前端对 techfine 本就显示空态文案，CommandList.vue:70-77）。回滚 = `git revert C3`。

**commit message 模板**：
```
fix(api): GET drivers/:type/commands 与 edge-devices/:id/commands 过滤 schedulable

- 新增 filterSchedulableTemplates；两个 GET 端点返回前过滤（I-2）
- 前端客户端过滤保留为防御（CommandList.vue:131 / CreateWizardCommandIntervals.vue:109 不动）
- 新增 4 条端点测试锁定过滤语义
- 演进方案 C3/M2
```

---

### C4 — test(api): 四集相等契约测试（GET×2 / 校验键集 / manifest 候选集）

**目标**：锁定四集相等不变量，防三处消费语义漂移。为让"manifest 候选集"与生产代码同源，本 commit 把 `sender_snapshot.go:173-184` 的候选谓词提取为导出函数（**这是 C4 授权的唯一生产代码改动**，行为等价，禁止其他改动）。

**改动文件**：
1. `backend/internal/nodemgr/sender_snapshot.go`：提取谓词 + 循环改写（见下）
2. 新建 `backend/internal/api/command_contract_test.go`：四集相等测试

**nodemgr 提取**（`sender_snapshot.go` 第 173–184 行，old → new）：
old：
```go
			for _, command := range driverCommands {
				if !command.Schedulable {
					continue
				}
				interval := command.IntervalMs
				if value, ok := intervals[command.ID]; ok {
					interval = value
				}
				if interval > 0 && findTemplateIDForCommand(snap.templates, command.WriteData) != 0 {
					commandCount++
				}
			}
```
new：
```go
			for _, command := range driverCommands {
				if CommandIsManifestCandidate(command, intervals, snap.templates) {
					commandCount++
				}
			}
```

在 `validateManifestCapacity` 函数（`sender_snapshot.go` 约 195 行结束）之后追加：
```go
// CommandIsManifestCandidate reports whether a driver command would be encoded
// as a per-command sub-frame in the ConfigManifest: Schedulable, effective
// interval (stored override → template default) > 0, and a matching
// ConfigTemplate exists in the snapshot.
// 演进方案 C4: this predicate is the single authority for the "manifest
// candidate set" of the four-set contract test.
func CommandIsManifestCandidate(command drivers.CommandTemplate, storedIntervals map[string]int, templates []models.ConfigTemplate) bool {
	if !command.Schedulable {
		return false
	}
	interval := command.IntervalMs
	if value, ok := storedIntervals[command.ID]; ok {
		interval = value
	}
	return interval > 0 && findTemplateIDForCommand(templates, command.WriteData) != 0
}
```

**C4 契约测试**（新文件 `backend/internal/api/command_contract_test.go` 完整骨架）：

```go
package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"

	"github.com/gin-gonic/gin"
)

// 四集相等契约（演进方案 C4/W5）:
//   A = GET /drivers/:type/commands 返回的模板 ID 集
//   B = GET /edge-devices/:id/commands 返回的模板 ID 集
//   C = SchedulableCommandIDs 的合法键集
//   D = manifest 编码候选集（nodemgr.CommandIsManifestCandidate）
// 断言 A == B == C == D；且非 schedulable 模板不属于任何一集。
func TestCommandContract_FourSetEquality(t *testing.T) {
	r, db := setupDriverCommandsTest(t) // C2 基建：fake_multi + registerDriverCommandRoutes
	// 设备：全部 schedulable 命令都配置 interval>0；模板 WriteData 全部落库
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	intervals, _ := json.Marshal(map[string]int{"read_a": 3000, "read_b": 7000})
	db.Create(&models.EdgeDevice{Name: "D", NodeID: "NODE001", ChannelID: 1, Type: "fake_multi", CommandIntervals: intervals})
	db.Create(&models.ConfigTemplate{NodeID: "NODE001", WriteData: "AA01", ReadLength: 4, DelayMs: 10})
	db.Create(&models.ConfigTemplate{NodeID: "NODE001", WriteData: "AA02", ReadLength: 4, DelayMs: 10})

	// A
	wa := httptest.NewRecorder()
	reqA := httptest.NewRequest(http.MethodGet, "/api/v1/drivers/fake_multi/commands", nil)
	reqA.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(wa, reqA)
	if wa.Code != http.StatusOK { t.Fatalf("A: %d %s", wa.Code, wa.Body.String()) }
	setA := idsFromDriverCommandsResponse(t, wa)

	// B
	wb := httptest.NewRecorder()
	reqB := httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices/1/commands", nil)
	reqB.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(wb, reqB)
	if wb.Code != http.StatusOK { t.Fatalf("B: %d %s", wb.Code, wb.Body.String()) }
	setB := idsFromDeviceCommandsResponse(t, wb)

	// C
	registry := newFakeRegistry()
	setC, err := SchedulableCommandIDs(registry, "fake_multi")
	if err != nil { t.Fatal(err) }

	// D：与生产编码同源谓词
	drv, _ := registry.Get("fake_multi")
	provider := drv.(drivers.CommandTemplateProvider) // 同包测试可直接断言
	stored := map[string]int{"read_a": 3000, "read_b": 7000}
	templates := []models.ConfigTemplate{
		{ID: 1, WriteData: "AA01"},
		{ID: 2, WriteData: "AA02"},
	}
	setD := map[string]struct{}{}
	for _, tmpl := range provider.GetCommandTemplates() {
		if nodemgr.CommandIsManifestCandidate(tmpl, stored, templates) {
			setD[tmpl.ID] = struct{}{}
		}
	}

	assertSameIDSet(t, "A==B", setA, setB)
	assertSameIDSet(t, "B==C", setB, setC)
	assertSameIDSet(t, "C==D", setC, setD)
	// 非 schedulable 模板不属于任何一集
	if _, ok := setA["one_shot"]; ok { t.Fatal("one_shot leaked into A") }
	if _, ok := setD["one_shot"]; ok { t.Fatal("one_shot leaked into D") }
}
```

> 执行者须补全两个解析 helper（`idsFromDriverCommandsResponse` / `idsFromDeviceCommandsResponse` / `assertSameIDSet`）——从响应 envelope `{"code":200,"data":[...]}` 解出 `[]struct{ID string}` 后取 ID 集合；`assertSameIDSet` 双向包含断言并打印差异。**注意**：`setupDriverCommandsTest` 的 registry 与 `newFakeRegistry()` 是不同实例，C 集计算用 `newFakeRegistry()` 即可（模板相同）；D 的 `templates` 用带 ID 的 `models.ConfigTemplate`（`findTemplateIDForCommand` 只匹配 WriteData，ID 仅需非 0；`ConfigTemplate` 的 ID 是普通 `uint` 字段，无 gorm.Model 嵌入）。import 需补 `"gorm.io/gorm"`（若未使用可省略）。

**C4 附加用例**（同文件）：
| 用例名 | 断言 |
|--------|------|
| TestCommandContract_IntervalZeroDropsFromDOnly | 存 {read_a:3000, read_b:0}：read_b 仍在 A/B/C，但不在 D（D ⊆ C 且相等仅在"全部配置 interval>0"时成立）——锁定候选集谓词边界 |

**退出门禁**：`cd backend && go build ./... && go test ./...` 全绿（含 nodemgr 既有 sender_snapshot_test.go 全绿——谓词提取行为等价）。

**风险与回滚**：nodemgr 提取是行为等价重构，唯一风险是漏改调用点——门禁靠既有 nodemgr 测试。回滚 = `git revert C4`。

**commit message 模板**：
```
test(api): 四集相等契约测试（GET×2 / 校验键集 / manifest 候选集）

- nodemgr 提取 CommandIsManifestCandidate（行为等价，C4 授权的唯一生产改动）
- 新增 command_contract_test.go：A==B==C==D 断言 + 非 schedulable 不入集
- 演进方案 C4/W5
```

---

### C5 — chore(drivers): 删除 legacyUnsafeCommandTemplates 死代码（228 行）+ 守卫测试

**目标**：删除 228 行死代码 + 失去意义的守卫测试 + 失去全部调用方的辅助函数。

**改动文件**：
1. `backend/internal/drivers/inverter_techfine.go`：删除 863–1096 行（`legacyUnsafeCommandTemplates` 整段）；删除 833–838 行（`crc16ToHex`）
2. `backend/internal/drivers/inverter_techfine_test.go`：删除 631–635 行（`TestTechfine_LegacyUnsafeTemplatesFailClosed`）

**删除前 grep 确认（必须执行并记录输出）**：
```bash
cd backend
grep -rn "legacyUnsafeCommandTemplates" --include="*.go" .   # 预期：仅 inverter_techfine.go:863-1096 与测试 632
grep -rn "crc16ToHex" --include="*.go" .                     # 预期：仅 833-838 定义 + 989/1003/1018/1033/1048/1064/1088（全在 863-1096 块内）→ 可删
grep -rn "asciiToHex" --include="*.go" .                      # 预期：849-859（GetCommandTemplates，C6 才删）+ 测试 198/213 → C5 保留
```
**执行规则**：`asciiToHex` 在 C5 仍有调用方（GetCommandTemplates 11 条 + 测试），**保留**；`crc16ToHex` 调用方全部位于被删块内，**一并删除**；`CRC16Modbus`（jiabaida.go:1864）被 jiabaida 自身使用，**不得删除**。

**删除锚点**：
- `inverter_techfine.go` 863–1096：从 `// legacyUnsafeCommandTemplates is intentionally unexported and unused.` 到文件倒数第二个 `}`（1096 行）。删除后文件末尾应为 `GetCommandTemplates` 的收尾 `}`（861 行）+ 空行。删除后文件行数 ≈ 1096−234 = 862 行。
- `inverter_techfine.go` 833–838：`crc16ToHex` 注释 + 函数体。
- `inverter_techfine_test.go` 631–635：`TestTechfine_LegacyUnsafeTemplatesFailClosed` 整函数（含前导空行）。

**C5 测试清单**：本 commit 为纯删除，不新增测试；以"既有测试全绿 + grep 零命中"为锁定。

**退出门禁**：
```bash
cd backend && go build ./... && go test ./...
grep -rn "legacyUnsafeCommandTemplates\|crc16ToHex" --include="*.go" .   # 必须 0 命中
awk 'END{print NR}' internal/drivers/inverter_techfine.go               # 应为 862 行（±2 容忍）
```

**风险与回滚**：删除内容完整保留在 git 历史（`git show f15f9c30:backend/internal/drivers/inverter_techfine.go` 可随时取回）。回滚 = `git revert C5`。

**commit message 模板**：
```
chore(drivers): 删除 legacyUnsafeCommandTemplates 死代码（228 行）+ 守卫测试

- 删除 inverter_techfine.go:863-1096（228 行，实测 awk 计数）
- 同步删除失去调用方的 crc16ToHex（调用方全在被删块内，grep 确认）
- asciiToHex 保留（GetCommandTemplates 与测试仍使用）
- 删除守卫测试 TestTechfine_LegacyUnsafeTemplatesFailClosed（函数删除后失去意义）
- 演进方案 C5/M1
```

---

### C6 — chore(drivers): 删除 techfine 11 条 compatibility metadata（前置审计证据入 message）

**目标**：审计先行，然后删除 11 条 query_*，`GetCommandTemplates` 返回空，改写测试，补 backfill 语义注释。

**步骤 0 — 审计（主 Agent 已于 2026-09-06 在生产库执行完毕：0 行命中，见接口设计文档 §4.1；执行者直接引用该结论，无需也无法重复执行）**。审计 SQL 原文（结果 0 行，连同本 SQL 写入 commit message）：
```sql
SELECT id, node_id, write_data
FROM config_templates
WHERE edge_device_id IS NULL
  AND upper(translate(write_data,' ','')) IN
  ('485354530D','48475249440D','484F500D','484241540D','4850560D','485056420D',
   '4854454D500D','4847454E0D','48424D53310D','48454550310D','48494D5347310D');
```
> 列集已按 2026-09-06 生产库实测修正：config_templates 无 channel_id 列；translate 去空格防御 hex 存储形态差异。
- 结果为空（**即本次实测情况**）→ 继续删除，把 SQL 原文 + "0 行" 写进 commit message。
- 结果非空 → **停止，上报主 Agent**（先人工归属 edge_device_id 或归档），不自行删除、不自行归属。

**改动文件**：
1. `backend/internal/drivers/inverter_techfine.go`：840–861 行整体替换（见下）
2. `backend/internal/drivers/inverter_techfine_test.go`：555–592 行改写（见下）
3. `backend/internal/datalifecycle/template_backfill.go`：106–107 行注释补语义（见下）

**改动 1**（`inverter_techfine.go` 840–861 行，old → new）：
old：第 840–861 行（`// ====...` 注释块 + `func (d *TechfineInverterDriver) GetCommandTemplates()` + 11 条模板 + `}`）。
new：
```go
// ============================================================================
// GetCommandTemplates returns no templates.  The 11 query_* compatibility
// templates were removed (演进方案 C6, 2026-09-06): the third state
// CommandTemplate{Schedulable:false} is abolished, and the same physical
// reads are owned by the Action Catalog (ControlActions, read_*).
// Pre-deletion audit: see commit message — 0 unowned config_templates rows
// matched the removed frame set, so template_backfill loses no matchable
// rows (宁留勿删 semantics unchanged).
func (d *TechfineInverterDriver) GetCommandTemplates() []CommandTemplate {
	return nil
}
```
> 保留方法（返回 nil）以维持 `CommandTemplateProvider` 接口兼容；不删接口。`asciiToHex` 此时生产调用方清零，但测试 198/213 仍使用 → **保留**。

**改动 2**（`inverter_techfine_test.go` 555–592 行，old → new）：
old：`TestTechfine_CommandTemplates` 整函数（555–592 行）。
new：
```go
func TestTechfine_CommandTemplates(t *testing.T) {
	// C6: the 11 query_* compatibility templates are deleted; the third state
	// CommandTemplate{Schedulable:false} is abolished. GetCommandTemplates
	// must return no templates — one-shot reads live in ControlActions().
	templates := (&TechfineInverterDriver{}).GetCommandTemplates()
	if len(templates) != 0 {
		t.Fatalf("expected 0 templates after C6 removal, got %d: %+v", len(templates), templates)
	}
}
```

**改动 3**（`template_backfill.go` 106–107 行注释，old → new）：
old：
```go
// driverTemplateMatches reports whether any schedulable CommandTemplate of the
// driver has WriteData equal (case-normalized) to the template's WriteData.
```
new：
```go
// driverTemplateMatches reports whether any CommandTemplate of the driver has
// WriteData equal (case-normalized) to the template's WriteData.
// The match source is the driver's *currently declared* template set
// (演进方案 §2.4). A driver that declares no templates (e.g. techfine after
// C6) can never match, so its historical rows stay unowned — 宁留勿删,
// correct by design. Do not re-add templates merely to make backfill match.
```

**C6 测试清单**：改写 1 条（上表）；不新增。既有 `TestTechfine_HPV` 等解析测试不受影响（ParseData 与模板无关）。

**退出门禁**：
```bash
cd backend && go build ./... && go test ./...
grep -n "query_status\|query_grid\|query_bms" internal/drivers/inverter_techfine.go   # 0 命中
grep -n "read_status" internal/drivers/inverter_techfine.go                          # 仍命中（ControlActions 保留）
```
- 审计结果（含空）已写入 commit message。

**风险与回滚**：中。审计非空 → 顺延（不删）。回滚 = `git revert C6`（11 条恢复，测试改写一并还原）。

**commit message 模板**（审计结果回填后使用）：
```
chore(drivers): 删除 techfine 11 条 compatibility metadata（前置审计证据入 message）

审计（2026-09-06，删除前执行）:
SELECT id, node_id, write_data FROM config_templates
WHERE edge_device_id IS NULL AND upper(translate(write_data,' ','')) IN
('485354530D','48475249440D','484F500D','484241540D','4850560D','485056420D',
 '4854454D500D','4847454E0D','48424D53310D','48454550310D','48494D5347310D');
→ 0 行（预期：techfine 从未有 Schedulable=true 模板，不会产生可归属模板）
→ template_backfill 匹配源失去该帧集，宁留勿删语义不变（注释已补 §2.4）

改动:
- GetCommandTemplates 返回 nil（11 条 query_* 删除；同一批物理读由 ControlActions read_* 承载）
- TestTechfine_CommandTemplates 改写为断言返回空
- template_backfill.go driverTemplateMatches 注释补语义
- 演进方案 C6/W1
```

---

### C7 — test(drivers): 断言内置驱动模板全 schedulable（第三态防线）+ 注释改写

**目标**：新增测试防线断言所有内置驱动 `GetCommandTemplates()` 返回的模板 `Schedulable==true`；`command_template.go` 注释把 `Schedulable=false` 标 deprecated。

**改动文件**：
1. `backend/internal/drivers/drivers_test.go`：文件末尾追加测试（见下）
2. `backend/internal/drivers/command_template.go`：5–11 行注释替换（见下）

**改动 1**（`drivers_test.go` 末尾追加，完整代码）：
```go
// TestBuiltInDriversTemplatesAllSchedulable asserts the third-state abolition
// (演进方案 P2/W2): every built-in driver's GetCommandTemplates() output must
// be Schedulable==true. Any future driver that sneaks a one-shot command into
// the template domain turns this test red.
func TestBuiltInDriversTemplatesAllSchedulable(t *testing.T) {
	registry := NewRegistry()
	RegisterBuiltInDrivers(registry)
	for _, typ := range registry.List() {
		drv, err := registry.Get(typ)
		if err != nil {
			t.Fatalf("registry.Get(%q): %v", typ, err)
		}
		provider, ok := drv.(CommandTemplateProvider)
		if !ok {
			continue // driver without templates is fine
		}
		for _, tmpl := range provider.GetCommandTemplates() {
			if !tmpl.Schedulable {
				t.Errorf("built-in driver %q template %q has Schedulable=false — the third state is abolished (演进方案 P2)", typ, tmpl.ID)
			}
		}
	}
}
```
> 依赖：`Registry.List()`（registry.go:62）、`RegisterBuiltInDrivers`（builtin.go:844）均已核实。C2/C3/C4 测试用的 `fakeMultiDriver` 不在内置注册路径，不受本防线约束（假驱动仅测试用）。

**改动 2**（`command_template.go` 5–11 行，old → new）：
old：
```go
// Two modes:
//
//	Schedulable=true  → polling command, encoded in ConfigManifest with interval.
//	Schedulable=false → trigger command, executed only on user request via API.
```
new：
```go
// Schedulable=true  → polling command, encoded in ConfigManifest with interval.
// Schedulable=false → DEPRECATED transition state ("trigger command").
//
// 演进方案 P2: 元数据二选一，第三态废除。轮询帧进 CommandTemplate 且
// Schedulable 恒为 true；一次性/受控操作进 ControlAction (Action Catalog)。
// 新驱动禁止返回 Schedulable=false 的模板 —
// TestBuiltInDriversTemplatesAllSchedulable 是防回潮测试防线。
```

**C7 测试清单**：新增 1 条（上表）。无其他测试改动。

**退出门禁**：
```bash
cd backend && go build ./... && go test ./...   # 含新防线测试全绿
```

**风险与回滚**：低。若未来驱动违反 → 测试红（预期行为）。回滚 = `git revert C7`。

**commit message 模板**：
```
test(drivers): 断言内置驱动模板全 schedulable（第三态防线）

- drivers_test.go 新增 TestBuiltInDriversTemplatesAllSchedulable：
  遍历 RegisterBuiltInDrivers 全部驱动，断言每条模板 Schedulable==true
- command_template.go 注释改写：Schedulable=false 标 deprecated，新驱动禁用
- 演进方案 C7/W2
```

---

## 4. 回滚总表

| Commit | 回滚方式 | 说明 |
|--------|----------|------|
| C1 | `git revert C1` | 纯重构，无依赖 |
| C2 | `git revert C2` | 恢复旧 PUT 语义；**不恢复**已被清洗的脏键（按 I-1 本就不合法，可接受） |
| C3 | `git revert C3` | 恢复不过滤 GET |
| C4 | `git revert C4` | 恢复内联谓词 |
| C5 | `git revert C5` | 死代码从 git 历史恢复 |
| C6 | `git revert C6` | 11 条 query_* 恢复（审计证据保留在历史） |
| C7 | `git revert C7` | 防线测试移除 |

顺序：C7→C1 逐个 revert 即可（各 commit 相互独立，无跨 commit 编译依赖；C2 依赖 C1 的 helper，故 revert C1 前必须先 revert C2）。

## 5. 不做清单（蓝本 N1–N8，本计划同样禁止）

| # | 禁止事项 |
|---|----------|
| N1 | 合并 CommandTemplate 与 ControlAction 类型 |
| N2 | 建审批系统/解禁数据库表 |
| N3 | 运行时解禁 API（POST /actions/:id/enable） |
| N4 | 恢复 triggerCommands 分组 |
| N5 | 把 techfine 11 条 query_* 迁移进 Action Catalog（删除而非迁移） |
| N6 | 新增"证据数据库" |
| N7 | 边缘侧自治 / 跨设备编排 / 脚本化条件 |
| N8 | PeriphCmd 加 channel 锁 |

另：本次**不得**改动前端任何文件；不得实现 Phase 2+（动作门禁台账 C8/C9、规则编辑器 Risk 展示 C10、PeriphCmd 文档 C11）。

## 6. 行号核对记录（与蓝本差异）

| 蓝本引用 | 实测 | 差异 |
|----------|------|------|
| handler_driver_commands.go :32 / :56-77 / :88-130 | 32 / 56-77 / 88-133 | PUT 到 133（含响应） |
| handler_edge_device.go:66-107 | 66-107 | 一致 |
| sender_snapshot.go:167-185 | 167-185 | 一致 |
| template_backfill.go:105-114 | 106-116 | 偏移 +1 |
| inverter_techfine.go:849-859 / 863-1096 | 849-859 / 863-1096 | 一致（228 行实测确认） |
| inverter_techfine_test.go:632 附近 | 631-635 | 一致 |
| TestTechfine_CommandTemplates 555-585 | 555-592 | 实测到 592 |
| builtin.go:98/140/272/… | 98/140/272/283/355 + jiabaida.go:1892 + generic_modbus.go:45 + generic_i2c.go:44 | 补全 |
| RegisterBuiltInDrivers | builtin.go:844 | 一致 |
| CommandList.vue:131 / CreateWizardCommandIntervals.vue:109 | 131 / 109 | 一致 |
