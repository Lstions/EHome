package api

import (
	"sync"

	"ehome/backend/internal/models"
)

// latestValueCache 数据层时序化 (方案 v3.4 §3.2.4)。
// overview/latest-value 类查询原本 DISTINCT ON (device_id) ... ORDER BY
// created_at DESC 无时间谓词——unified_data 分区表下会扫全部子分区。
// 改为内存缓存: SensorParserConsumer 持久化成功后旁路更新, API 缓存优先、
// miss 回落原 SQL (正确性兜底)。
//
// **按传感器分槽**（2026-09-16 修复的缺陷）：初版是 `map[uint]UnifiedData`，
// 每设备只留**一条**记录。而生产者对一帧解析出的 N 个物理量**逐条**调用 `SetLatestValue`
// （`consumers_heavy.go` 的 `for i := range records { c.latestSink(records[i]) }`），
// 于是后写覆盖先写 —— 实测一帧 14 个物理量最后只剩 1 个。
//
// 后果（实测，非推断）：`/overview` 的 `latest_data` 形状**随缓存冷热而变** ——
// 缓存 miss 走回落 SQL 时某设备返回 **14** 个物理量，命中时只剩 **1** 个，
// 用户会看到数据「消失」。`docs/设计/场景仿真验证框架.md` §13.1 把它记为
// 「产品语义裁决」，但按代码事实，缓存本就**无法表达**多物理量，属实现缺陷。
type latestValueCache struct {
	mu sync.RWMutex
	// key: edge_device_id → (sensor_name → 该传感器最新一条)
	entries map[uint]map[string]models.UnifiedData
	// last 记录每设备**最后写入**的那条，供 LatestValue 的单值语义使用
	// （自动化引擎 F4 条件复核依赖「最新那条」，不能因多槽化而改变其返回）。
	last map[uint]models.UnifiedData
}

var globalLatestValueCache = &latestValueCache{
	entries: make(map[uint]map[string]models.UnifiedData),
	last:    make(map[uint]models.UnifiedData),
}

// SetLatestValue 更新单设备某个物理量的最新值 (persist 成功路径调用)。
// 注: UnifiedData.DeviceID 在 v2.2 即表示 edge_device_id (见 models.go:256 注释);
// EdgeDeviceID 字段是另一独立指针列, 历史回填不全, 不能作为缓存键。
func SetLatestValue(rec models.UnifiedData) {
	if rec.DeviceID == 0 {
		return
	}
	globalLatestValueCache.mu.Lock()
	defer globalLatestValueCache.mu.Unlock()
	byDevice, ok := globalLatestValueCache.entries[rec.DeviceID]
	if !ok {
		byDevice = make(map[string]models.UnifiedData)
		globalLatestValueCache.entries[rec.DeviceID] = byDevice
	}
	byDevice[rec.SensorName] = rec
	globalLatestValueCache.last[rec.DeviceID] = rec
}

// LatestValue returns the **last written** cached record for a device; ok=false on miss.
//
// 为什么保留这个单值读取（而不是让所有调用方改用 LatestValues）：
// 自动化引擎的 `checkConditionsStillSatisfied` 需要「该设备最新的那条记录」来复核触发条件，
// 多槽化不应改变它的返回语义 —— 否则会静默改变条件复核行为。
func LatestValue(deviceID uint) (models.UnifiedData, bool) {
	globalLatestValueCache.mu.RLock()
	defer globalLatestValueCache.mu.RUnlock()
	rec, ok := globalLatestValueCache.last[deviceID]
	return rec, ok
}

// LatestValues 返回该设备**全部**已缓存物理量（顺序不保证，调用方自行排序）。
// 用于 `/overview` 与边缘设备列表：它们要展示一帧里的所有物理量，与回落 SQL 的形状对齐。
func LatestValues(deviceID uint) []models.UnifiedData {
	globalLatestValueCache.mu.RLock()
	defer globalLatestValueCache.mu.RUnlock()
	byDevice, ok := globalLatestValueCache.entries[deviceID]
	if !ok {
		return nil
	}
	out := make([]models.UnifiedData, 0, len(byDevice))
	for _, rec := range byDevice {
		out = append(out, rec)
	}
	return out
}

// resetLatestValueCacheForTest 清空缓存（仅测试用）。
// 包级缓存会跨用例串味，测试必须能重置。
func resetLatestValueCacheForTest() {
	globalLatestValueCache.mu.Lock()
	defer globalLatestValueCache.mu.Unlock()
	globalLatestValueCache.entries = make(map[uint]map[string]models.UnifiedData)
	globalLatestValueCache.last = make(map[uint]models.UnifiedData)
}

// WarmupLatestValues 启动回填: 每设备**最新时刻的全部物理量** (仅启动一次)。
//
// ⚠️ **历史状态（2026-09-16 实测）**：本函数自 2026-08-21 引入以来**从未被生产代码调用**
// （`grep -rn WarmupLatestValues` 全仓只有定义与注释；git 历史确认引入那次提交就没有调用点）。
// 但 `docs/设计/架构优化实施计划.md:45` 明确要求「persist 旁路更新 **+ 启动回填** + miss 回落 SQL」，
// 且其验收清单 `latest-value 缓存命中/回落双路径一致` 至今未勾选 ⇒ 这是**未完成的实现**，不是被否决的设计。
//
// 正确性影响：**无**（miss 时回落 SQL，结果仍正确）。
// 影响的是**性能与一致性**：不 warmup ⇒ 每次重启后所有设备的首次查询都扫分区表；
// 且「冷启动」与「跑了一会儿」两条路径的**形状**必须一致（这正是本函数存在的意义）。
//
// **必须与运行时 `SetLatestValue` 的粒度一致**：运行时对一帧的每个物理量各写一条，
// 所以回填也必须是「该设备最新 `created_at` 上的所有行」，而不是「每设备一行」。
//
// 若回填只取一行（原实现用 `DISTINCT ON (device_id) … ORDER BY created_at DESC`），
// 冷启动后缓存里每设备只有 1 个物理量 —— 与运行一段时间后的状态**不一致**，
// 于是同一个接口在「刚启动」与「跑了一会儿」两种情况下返回不同形状（本缺陷的启动侧）。
//
// SQL 与 `/overview`、边缘设备列表的**回落查询同构**（DISTINCT ON 取最新时间戳 + JOIN 取该时刻全部行），
// 这样「缓存命中」与「缓存 miss」两条路径的语义才真正一致。
// 注意生产者用**同一个 `now`** 写一帧的所有物理量（`consumers_heavy.go` 的 `now := time.Now()`），
// 因此按 `created_at` 相等即可取回整帧。
// WarmupLatestValuesFrom 用**已取到的行**回填缓存（纯函数，便于测试；不碰 DB）。
func WarmupLatestValuesFrom(rows []models.UnifiedData) {
	globalLatestValueCache.mu.Lock()
	defer globalLatestValueCache.mu.Unlock()
	for _, r := range rows {
		if r.DeviceID == 0 {
			continue
		}
		byDevice, ok := globalLatestValueCache.entries[r.DeviceID]
		if !ok {
			byDevice = make(map[string]models.UnifiedData)
			globalLatestValueCache.entries[r.DeviceID] = byDevice
		}
		byDevice[r.SensorName] = r
		// last 按 created_at 取最新（回填顺序不保证，必须显式比较）
		if prev, seen := globalLatestValueCache.last[r.DeviceID]; !seen || !r.CreatedAt.Before(prev.CreatedAt) {
			globalLatestValueCache.last[r.DeviceID] = r
		}
	}
}

// WarmupLatestValues 启动回填入口：向已注入的数据来源取行并回填。
//
// 未注入来源时为 no-op（与「回填失败不阻塞启动」的既有约定一致）——
// Miss 路径回落 SQL 仍是正确兜底。
// 返回回填的行数，便于启动日志与测试断言。
func WarmupLatestValues() int {
	if warmupSource == nil {
		return 0
	}
	rows, err := warmupSource()
	if err != nil {
		// 回填失败不阻塞启动, miss 回落原 SQL 兜底
		return 0
	}
	WarmupLatestValuesFrom(rows)
	return len(rows)
}

// latestValuesSource 是启动回填的**数据来源函数**（由 cmd/server 注入）。
//
// 为什么用函数注入而不是像初版那样定义 `gormDB` 窄接口：
//
//	初版声明 `Raw(sql string, ...) queryResult`，而 `*gorm.DB.Raw` 的真实签名是
//	`Raw(string, ...interface{}) *gorm.DB` —— **返回类型不同，*gorm.DB 并不实现它**，
//	故 `api.WarmupLatestValues(db)` 无法编译。这正是该函数自 2026-08-21 引入以来
//	**一直没有生产调用者**的机械原因。
//	函数注入与本仓既有范式一致（`nodemgr.SetLatestSinkFn`、
//	`automationPlanner.SetLatestValueFn` 都是这么接线的），且同样避免 api→gorm 依赖。
type latestValuesSource func() ([]models.UnifiedData, error)

// warmupSource 由 cmd/server 在启动时注入；nil 时 WarmupLatestValues 为 no-op。
var warmupSource latestValuesSource

// SetLatestValuesSource 注入启动回填的数据来源（在 cmd/server 的接线处调用）。
func SetLatestValuesSource(fn latestValuesSource) {
	warmupSource = fn
}

// 注 (2026-09-15 退役, docs/分析/rollup-退役裁决-2026-09-15.md):
// 本文件原含 precisionFor (方案 §3.2.2 的 "跨度 > 48h 且无 logical scope 走
// rollup" 路由函数), 生产零调用者, 已于 2026-09-14 删除。它不是"待接线的读取路径",
// 而是一条在本仓查询协议下不可达的分支: logical scope 为空 ⟺ 实例
// logical_device_id IS NULL (query_scope.go), 而启动 BackfillLogicalDevices
// (identity.go) 对全量实例回填、新实例创建即赋逻辑身份 ⇒ 生产查询恒有
// logical scope ⇒ 恒返回 raw。且 rollup 表无 logical_device_id 列, §六 scope
// 条件落不到该表上。
// rollup 的写入件 (RollupConsumer / rollupSink / 建表路径 / 模型) 与表本身已于
// 2026-09-15 一并**退役**: EXPLAIN 实测 30 天窗口查询仅毫秒级 (索引 + 分区裁剪),
// rollup 的收益是"省几毫秒", 代价是第二张无界表 + 改数据模型。门禁 (死表不得复活)
// 见 datalifecycle/rollup_retirement_gate_test.go。
