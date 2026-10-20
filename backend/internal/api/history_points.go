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
// §1.6 (2026-10-20 22:57 verifier 实测): 聚合的 scope 来源必须是 UNION ALL
// 两分支 (logical 分支 + NULL-logical 的 device 分支), 否则 dataScopeCond 的
// OR 条件会把 logical_device_id 降级为 Filter, 复合索引完全用不上。SQL 组装在
// scope_union.go (historyAggregateSQL), 本文件只负责模式解析、桶宽、执行与
// 响应点形状; 参数顺序见 historyAggregateSQL 的注释。
//
// 响应 item 的形状: {"timestamp":..., "value":...} (HistoryPoint) —— 不再带
// id/device_id/sensor_name/unit/created_at 等冗余字段 (每点 198 B -> 约 40 B)。
//
// 保形去重 (qs.DedupNeeded) 仍必须先于聚合: 在 union 源外层按
// (sensor_name, timestamp) 用 ROW_NUMBER() ... ORDER BY id DESC 去重 (与
// datalifecycle.ApplyShapeDedup 同语义, MAX(id) 胜出), 再在其上 avg/lag ——
// 两台物理设备同一时刻的两行不得被平均 (无物理意义)。
//
// 本文件不依赖 gin, 纯函数与 SQL 执行辅助可独立单测 (history_points_test.go /
// history_points_pg_test.go)。

import (
	"context"
	"fmt"
	"strings"
	"time"

	"ehome/backend/internal/datalifecycle"

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

// historyPointQuery describes one aggregated history read.
type historyPointQuery struct {
	// Scope is the resolved §六 data query scope. Its branches are rendered as
	// an index-friendly UNION ALL source (契约 §1.6, see scope_union.go).
	Scope *datalifecycle.DataQueryScope
	// DedupNeeded enables 保形去重 (qs.DedupNeeded) before aggregation.
	DedupNeeded bool
	Category    string
	Start       time.Time
	End         time.Time
	MaxPoints   int
	// Mode is the resolved mode: AggModeAvg or AggModeChange.
	Mode AggMode
}

// queryHistoryPoints runs one aggregated PostgreSQL history query and returns
// at most maxPoints+1 lean points.
func queryHistoryPoints(ctx context.Context, db *gorm.DB, q historyPointQuery) ([]HistoryPoint, error) {
	widthSeconds := bucketWidthSeconds(historyWindowSeconds(q.Start, q.End), q.MaxPoints)
	if widthSeconds <= 0 {
		return nil, fmt.Errorf(
			"history points: invalid aggregation window (start=%s end=%s max_points=%d)",
			q.Start.Format(time.RFC3339), q.End.Format(time.RFC3339), q.MaxPoints)
	}

	sqlText, args := historyAggregateSQL(q, widthSeconds)
	points := make([]HistoryPoint, 0, q.MaxPoints+1)
	if sqlText == "" {
		// scope 匹配不到任何行 (空 logical/instance 集合): 不必打扰数据库。
		return points, nil
	}
	// 一条 raw SQL 自带两个互斥分支; 绑定请求上下文 (客户端超时/断开即取消)。
	if err := db.WithContext(ctx).Raw(sqlText, args...).Scan(&points).Error; err != nil {
		return nil, fmt.Errorf("history points: query category %q: %w", q.Category, err)
	}
	return points, nil
}
