package datalifecycle

// row_estimate.go — 数据量**估算**（绝不精确计数）。
//
// 出处: 这些函数原在 scope.go（逻辑设备 scope 解析）里，按 logical_device_id
// 估算。2026-10-23 逻辑设备层移除后，估算口径回到设备维度 —— 语义没变
//（「这台设备大概有多少行历史」），只是不再需要一个中间身份。
//
// 为什么只能估算: 精确 COUNT(*) 在 3200 万行的 unified_data 上是秒级全表扫描，
// 而调用方（删除确认弹窗、数据摘要）只是要给人一个量级感受。PostgreSQL 用
// 规划器的 reltuples 派生估计（EXPLAIN，O(1) 规划、零扫描）；SQLite / 解析失败
// 时退化为截断 COUNT（最多数 10 万行）。

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"time"

	"gorm.io/gorm"
)

// rowEstimateCap bounds truncated COUNTs (SQLite path) so the estimate can
// never trigger a full-table scan.
const rowEstimateCap = 100000

// EstimateTimeout 是估算段的降级截止时间: 超时则调用方省略数据量字段，
// 绝不让端点阻塞（原 §1.3 契约，语义不变）。
const EstimateTimeout = 3 * time.Second

var explainRowsRe = regexp.MustCompile(`rows=(\d+)`)

// EstimateDeviceRows 估算一台（或多台，如合并历史时）边缘设备的**原始数据行数**。
//
// ============ 为什么故意**不**把 telemetry_frame 算进来 ============
//
// 契约 §4 的六处生命周期清单里没有本文件，是刻意的：这里要回答的是
// "这台设备有多少历史数据"（删除确认弹窗的"预计删除 N 行"、合并弹窗的
// "预计迁移 N 行"）。帧表存的是**同一条观测的压缩表示**，一帧对应原始表里
// 的一行或多行的编码 —— 把它加进来会让同一个数字被数两遍，用户看到的
// "预计迁移行数"会比实际搬动的原始数据多出好几倍。
//
// 清理动作则相反：purge / retention 必须**两张账都清**（那是数据存续问题，
// 不是计数口径问题），见 telemetry_frame_lifecycle.go。
//
// ⚠ 由此产生一个**必然会出现的口径差**，运维必须知道：
// **实际删除量会大于本函数给出的预计值。** 本函数只数 unified_data + device_data，
// 而"彻底删除设备"（purge）和保留期清理都会**额外**删掉该设备的帧以及其它 6 张
// 清理器管的表。所以"预计删除 100 万行"执行完看到 130 万行是**正确**的，不是 bug。
// 反过来说：如果把帧表加进本函数，预计值会虚高到远超实际（同一观测被数两遍），
// 那才是真正会误导用户的口径错误。
//
// 返回 false 表示估算不可用（超时/解析失败），调用方必须省略该数字。
func EstimateDeviceRows(ctx context.Context, db *gorm.DB, deviceIDs ...uint) (int64, bool) {
	if len(deviceIDs) == 0 {
		return 0, true
	}
	ctx, cancel := estimateDeadline(ctx)
	defer cancel()
	db = db.WithContext(ctx)

	cond, args := deviceCond(deviceIDs)
	var total int64
	for _, table := range []string{"unified_data", "device_data"} {
		n, ok := estimateTable(ctx, db, table, cond, args)
		if !ok {
			return 0, false
		}
		total += n
	}
	return total, true
}

// CountDeviceRowsCapped 对单台设备做**有界精确计数**（最多 rowEstimateCap 行）。
//
// 与 EstimateDeviceRows 的分工：普通设备用估算（O(1)）；而「设备行已不存在、只有
// 孤儿数据」的救援场景必须用真实计数 —— 计划器估算在空表/新表上会凭空给出十几行
// （2026-10-25 runtime R-07 实测：不存在的 source 被 data-summary 报成 rows=18，
// 前端显示「预计迁移 18 行」却什么都搬不动）。
func CountDeviceRowsCapped(ctx context.Context, db *gorm.DB, deviceID uint) (int64, bool) {
	ctx, cancel := estimateDeadline(ctx)
	defer cancel()
	cond, args := deviceCond([]uint{deviceID})
	db = db.WithContext(ctx)
	var total int64
	for _, table := range []string{"unified_data", "device_data"} {
		n, ok := estimateTruncatedCount(ctx, db, table, cond, args)
		if !ok {
			return 0, false
		}
		total += n
	}
	return total, true
}

// deviceCond 渲染 device_id 条件（单设备走等值，多设备走 IN，均为索引前导列）。
func deviceCond(deviceIDs []uint) (string, []interface{}) {
	if len(deviceIDs) == 1 {
		return "device_id = ?", []interface{}{deviceIDs[0]}
	}
	return "device_id IN ?", []interface{}{deviceIDs}
}

// estimateDeadline keeps the degradation bound even when the caller's
// context carries no deadline; endpoint callers pass their own timeout.
func estimateDeadline(ctx context.Context) (context.Context, context.CancelFunc) {
	if _, ok := ctx.Deadline(); ok {
		return ctx, func() {}
	}
	return context.WithTimeout(ctx, EstimateTimeout)
}

func estimateTable(ctx context.Context, db *gorm.DB, table, cond string, args []interface{}) (int64, bool) {
	if db.Dialector != nil && db.Dialector.Name() == "postgres" {
		return estimatePostgres(ctx, db, table, cond, args)
	}
	return estimateTruncatedCount(ctx, db, table, cond, args)
}

// estimatePostgres reads the planner row estimate (derived from
// pg_class.reltuples and column statistics) via EXPLAIN — O(1) planning,
// no data scan. Falls back to truncated COUNT when parsing fails.
// GORM Raw is used (not database/sql) so slice args in `IN ?` expand.
func estimatePostgres(ctx context.Context, db *gorm.DB, table, cond string, args []interface{}) (int64, bool) {
	query := fmt.Sprintf("EXPLAIN SELECT id FROM %s WHERE %s", table, cond)
	var lines []string
	if err := db.WithContext(ctx).Raw(query, args...).Scan(&lines).Error; err != nil {
		return estimateTruncatedCount(ctx, db, table, cond, args)
	}
	// The plan's top node is printed first; its rows= figure is the total
	// estimate for the whole plan.
	for _, line := range lines {
		if m := explainRowsRe.FindStringSubmatch(line); m != nil {
			n, perr := strconv.ParseInt(m[1], 10, 64)
			if perr == nil {
				// 行数估算在分区表上噪声很大：**空设备可能显示 10-16 行**
				// （reltuples/页面启发式；2026-10-25 runtime R-10 实测）。
				// 「这台设备到底有没有数据」是用户最关心的判断，用一次索引探针
				// （EXISTS ... LIMIT 1，O(1)）把它钉死；非空时仍保留估算值。
				if n > 0 {
					var exists int
					probe := fmt.Sprintf("SELECT CASE WHEN EXISTS(SELECT 1 FROM %s WHERE %s LIMIT 1) THEN 1 ELSE 0 END", table, cond)
					if err := db.WithContext(ctx).Raw(probe, args...).Scan(&exists).Error; err == nil && exists == 0 {
						return 0, true
					}
				}
				return n, true
			}
		}
	}
	return estimateTruncatedCount(ctx, db, table, cond, args)
}

// estimateTruncatedCount counts at most rowEstimateCap rows — the
// truncated COUNT for SQLite, and the PostgreSQL fallback.
func estimateTruncatedCount(ctx context.Context, db *gorm.DB, table, cond string, args []interface{}) (int64, bool) {
	query := fmt.Sprintf("SELECT COUNT(*) FROM (SELECT 1 FROM %s WHERE %s LIMIT %d) AS capped", table, cond, rowEstimateCap)
	var n int64
	if err := db.WithContext(ctx).Raw(query, args...).Scan(&n).Error; err != nil {
		return 0, false
	}
	return n, true
}
