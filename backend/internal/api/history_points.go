package api

// PostgreSQL 历史数据聚合 (BMS 页慢修复-实施契约-2026-10-20 §一 + §1.6).
//
// 背景: 旧 /unified-data/historical 与 /unified-data/historical-batch 把整个
// 时间窗的行全量拉进 Go (SELECT * 无 LIMIT), 再用 downsampleUnifiedData 在内存
// 里抽样 —— max_points 只减小响应体, 不减少 DB 读取与 Go 物化。PG 实测单测点
// 24h 扫 2,808,951 行只返回 86,251 行, 一页 31 个 category 合计约 8,427 万行
// 访问 / 394 MiB 物化 (根因 R2)。
//
// 本文件把降采样下推到 PG SQL (只对 PostgreSQL 生效):
//
//   - avg 模式 (连续量): 桶宽 W = max(1, ceil(windowSeconds/maxPoints)) 秒,
//     to_timestamp(floor(epoch / W) * W) + avg(value) + GROUP BY 1 ORDER BY 1,
//     返回点数 <= maxPoints+1;
//   - change 模式 (状态位/掩码保护): lag() 窗口函数返回"变更点序列" (首点 +
//     每次值变化, 含 1->0), 前端在变更点序列上计 0->1 跃变与持续时长与全量
//     序列等价 —— 因此**不能**用"只保留非零行"代替 (1->0 的结束点会丢);
//   - raw 模式 / SQLite / maxPoints<=0 / 非法时间窗: 调用方完全不进入本文件,
//     继续走旧路径 (SQLite 行为一分不改, 既有单测是契约)。
//
// 2026-10-23 (逻辑设备概念层移除, .contract/logical-device-removal.md §5):
// 曾经为「逻辑身份」服务过的三件东西在这里一并消失 ——
//
//   1. UNION ALL 双分支 scope 源 (logical 分支 + NULL-logical 回退分支,
//      原 scope_union.go 的 historyUnionSQL): 唯一目的是让
//      `logical_device_id` 保持索引前导列。列已 DROP, 分支没有对象;
//   2. 保形去重 (ROW_NUMBER() ... MAX(id), 原 qs.DedupNeeded): 它的前提是
//      「两台物理设备在同一 (sensor_name, timestamp) 各写一行」。逻辑层移除后
//      每个查询只落在一台边缘设备 (device_id 等值条件) 上, 同一设备同一时刻
//      不会出现两行, 去重既无对象也会白付一次全窗口排序;
//   3. 因此聚合 SQL 回归**单表单条件**:
//        FROM unified_data WHERE device_id = ? AND sensor_name = ?
//                           AND "timestamp" BETWEEN ? AND ?
//      设备维度 (device_id) 就是索引前导列, 直接可用 (见
//      datalifecycle/partition_mgr.go 的 unified_data_device_id_idx /
//      device_data_device_id_idx), 不再需要 OR 兜底或 UNION 拼装。
//
// avg/change 模式的 SQL 结构、桶宽、change 点上限、窗口裁剪全部保持不变 ——
// 本次只换数据来源, 不改聚合语义。
//
// 响应 item 的形状: {"timestamp":..., "value":...} (HistoryPoint) —— 不再带
// id/device_id/sensor_name/unit/created_at 等冗余字段 (每点 198 B -> 约 40 B)。
//
// 本文件不依赖 gin, 纯函数与 SQL 执行辅助可独立单测 (history_points_test.go /
// history_points_pg_test.go)。

import (
	"strings"
	"time"

	"gorm.io/gorm"
)

// HistoryPoint is the lean point shape returned by the PostgreSQL aggregation
// path: the chart only needs the timestamp and the value (根因 R7-2: 旧响应
// 每点 198 B, 其中约 90% 是前端从不使用的字段).
type HistoryPoint struct {
	Timestamp time.Time `json:"timestamp"`
	Value     float64   `json:"value"`
}

// AggMode is the optional `agg` query parameter. auto is the default; raw is
// the legacy path (全量行 + Go 侧 downsample), which is also what SQLite
// always uses.
type AggMode string

const (
	// AggModeAuto resolves per category: change categories -> change,
	// everything else -> avg.
	AggModeAuto AggMode = "auto"
	// AggModeAvg aggregates continuous quantities into per-bucket averages.
	AggModeAvg AggMode = "avg"
	// AggModeRaw keeps the legacy behaviour (no server-side aggregation).
	AggModeRaw AggMode = "raw"
	// AggModeChange returns the change-point sequence of a status/mask sensor.
	AggModeChange AggMode = "change"
)

// changeCategories are state/bitmask sensors whose chart semantics are
// "transitions", not a continuous line: averaging them produces meaningless
// intermediate values, and full-row sampling can drop a short pulse. They are
// served the change-point sequence instead.
var changeCategories = map[string]struct{}{
	"protection_status": {},
	"fet_status":        {},
}

// parseAggMode normalises the optional agg query parameter. An unrecognised
// value falls back to auto instead of returning 400 (兼容优先: 旧前端不传 agg).
func parseAggMode(raw string) AggMode {
	switch AggMode(strings.ToLower(strings.TrimSpace(raw))) {
	case AggModeAvg:
		return AggModeAvg
	case AggModeRaw:
		return AggModeRaw
	case AggModeChange:
		return AggModeChange
	default:
		return AggModeAuto
	}
}

// isChangeCategory reports whether category is a state/bitmask sensor served
// by the change-point mode.
func isChangeCategory(category string) bool {
	_, ok := changeCategories[category]
	return ok
}

// resolveAggMode resolves auto into a concrete mode for one category. Explicit
// avg/change/raw values are honoured as-is (operator override).
func resolveAggMode(mode AggMode, category string) AggMode {
	if mode != AggModeAuto {
		return mode
	}
	if isChangeCategory(category) {
		return AggModeChange
	}
	return AggModeAvg
}

// historyWindowSeconds returns the query window in whole seconds; 0 (or
// negative) when the window is empty/sub-second.
func historyWindowSeconds(start, end time.Time) int64 {
	if !end.After(start) {
		return 0
	}
	return int64(end.Sub(start) / time.Second)
}

// bucketWidthSeconds returns the aggregation bucket width in seconds:
// max(1, ceil(windowSeconds/maxPoints)). It returns 0 (meaning "no server-side
// aggregation", i.e. raw path) when maxPoints<=0 or the window is empty.
//
// 24h/500 -> 173s, 1h/500 -> 8s, window<maxPoints -> 1s.
func bucketWidthSeconds(windowSeconds int64, maxPoints int) int64 {
	if windowSeconds <= 0 || maxPoints <= 0 {
		return 0
	}
	width := (windowSeconds + int64(maxPoints) - 1) / int64(maxPoints)
	if width < 1 {
		width = 1
	}
	return width
}

// isPostgresDialect reports whether db talks to PostgreSQL. Aggregation SQL
// uses PG-only constructs (to_timestamp/extract epoch / IS DISTINCT FROM), so
// it must never be issued to SQLite.
func isPostgresDialect(db *gorm.DB) bool {
	return db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres"
}

// historyAggregationEligible is the dialect-independent eligibility rule:
// maxPoints>0 and a window of at least one second (i.e. a positive bucket
// width). It is a pure function so the boundary cases are unit-testable
// without a PostgreSQL instance.
func historyAggregationEligible(maxPoints int, start, end time.Time) bool {
	return bucketWidthSeconds(historyWindowSeconds(start, end), maxPoints) > 0
}

// shouldAggregateHistory reports whether an endpoint request must use the SQL
// aggregation path: PostgreSQL only, and only for a valid maxPoints/window
// pair (契约 §1.2: SQLite / maxPoints<=0 / 非法参数完全保持旧行为).
func shouldAggregateHistory(db *gorm.DB, maxPoints int, start, end time.Time) bool {
	return isPostgresDialect(db) && historyAggregationEligible(maxPoints, start, end)
}

// maxChangePoints 是 change 模式的硬上限 (评审 2026-10-21 发现): change 返回
// 「首点 + 每次值变化」，点数由状态翻转次数决定而不是 max_points；虽然有上限
// 的连续量走 avg 分桶，但 fet_status 这类高频翻转位在极端情况下可远超 500 点
// (旧路径 downsample 始终把响应封在 ~max_points)。超过上限时**不能静默截断**
// ——保护触发次数按 0→1 跃变计数，截断会让数字变小（错误数据比报错更坏）——
// 因此直接报错让调用方返回失败态（前端修复后能显示「加载失败+重试」）。
const maxChangePoints = 50000

// exceedsChangePointCap reports whether a change-mode result exceeds the hard
// cap. Pure function so the boundary is unit-testable without PostgreSQL.
func exceedsChangePointCap(mode AggMode, n int) bool {
	return mode == AggModeChange && n > maxChangePoints
}

// historyPointQuery describes one aggregated history read.
//
// 2026-10-23: 原 `Scope *datalifecycle.DataQueryScope` + `DedupNeeded bool`
// 两个字段随逻辑设备概念层一起删除。查询维度回到**边缘设备**:
// EdgeDeviceID 就是 unified_data.device_id (= EdgeDevice.ID)。
type historyPointQuery struct {
	// EdgeDeviceID 是 unified_data.device_id 的等值条件 (= EdgeDevice.ID)。
	// 0 表示"不查"(调用方直接返回空结果, 不打扰数据库)。
	EdgeDeviceID uint
	Category     string
	Start        time.Time
	End          time.Time
	MaxPoints    int
	// Mode is the resolved mode: AggModeAvg or AggModeChange.
	Mode AggMode
	// BatchHint 表示"同一窗口还会有很多 category 来问"（批端点）。
	//
	// 为什么要有它：帧表解码一次能服务全部字段，但只服务一个字段时全解是
	// 29 倍浪费（实测全解 28 ms vs 单解 5 ms）。批端点知道"后面还有 28 个",
	// 单查端点不知道 —— 所以由**调用方**给出这个信息，而不是让数据层猜。
	BatchHint bool
}

// historyDeviceCond 是全部数据读取的唯一条件形状: 边缘设备维度等值。
//
// 为什么不再有 OR / IN / 分支: 逻辑层移除后「一台设备的历史」= 它自己的
// device_id 的行; 软删设备的数据同样可读 (Unscoped 语义, 契约 §5), 所以这里
// 也**不**做 edge_devices 存在性校验 —— 数据读取与配置读取的可见性分离。
const historyDeviceCond = `device_id = ? AND sensor_name = ? AND "timestamp" BETWEEN ? AND ?`

// D1 需要区分的两种方言。
const (
	sqlDialectPostgres = "postgres"
	sqlDialectSQLite   = "sqlite"
)
