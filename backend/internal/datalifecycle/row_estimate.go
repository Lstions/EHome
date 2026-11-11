package datalifecycle

// row_estimate.go — 数据量**估算**（绝不精确计数）。
//
// 出处: 这些函数原在 scope.go（逻辑设备 scope 解析）里，按 logical_device_id
// 估算。2026-10-23 逻辑设备层移除后，估算口径回到设备维度 —— 语义没变
//（「这台设备大概有多少行历史」），只是不再需要一个中间身份。
//
// 为什么只能估算: 精确 COUNT(*) 在千万行的时序表上是秒级全表扫描，
// 而调用方（删除确认弹窗、数据摘要）只是要给人一个量级感受。PostgreSQL 用
// 规划器的 reltuples 派生估计（EXPLAIN，O(1) 规划、零扫描）；SQLite / 解析失败
// 时退化为截断 COUNT（最多数 10 万行）。
//
// ==================== 2026-11-11：口径从「两条账」收敛为「一条账」====================
//
// 本文件原先数 unified_data + device_data 两张表。两张表先后退役
// （unified_data 2026-11-08 停写；device_data 2026-11-11 整表退役），
// 现在**只剩 telemetry_frame** —— 于是下面那段"为什么故意不数帧表"的论证
// 前提消失了，必须重写（原文见 git history 的 2026-11-11 之前版本）：
//
//	原文论证：帧表存的是「同一条观测的压缩表示」，把它加进来会让同一个数字被数
//	两遍 —— 因为那时 unified_data / device_data 才是"原始行"，帧表是派生。
//
//	前提为何消失：那两张"原始行"表已经不在了。帧表现在是**唯一的**时序数据表，
//	它就是"这台设备有多少历史"的正确答案，不存在被数两遍的第二个来源。
//
// 仍然要保留的两条口径说明：
//
//	1. **帧 ≠ 行**。一帧是一行，但它编码的是"该 slot 的全部字段"（压缩表示）。
//	   所以"预计删除 N 行"的含义从"原始上报行数"变成了"帧数"—— 量级仍然正确
//	   （生产实测帧数与解析出的字段数不同量级，但两者都随采样率线性增长），
//	   前端文案已经叫"数据量"而不是"原始行数"，不需要改。
//	2. **实际删除量仍会大于预计值**：purge / retention 还会清 sensor_field_obs、
//	   data_sources、data_source_health 等台账表（见 purge_edge_device.go 的
//	   清理器清单）。这个口径差是**原有语义**，与本次收缩无关。

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

// EstimateDeviceRows 估算一台（或多台，如合并历史时）边缘设备的**时序数据行数**。
//
// ============ 为什么现在数的是 telemetry_frame（2026-11-11 重写）============
//
// 这里要回答的是"这台设备有多少历史数据"（删除确认弹窗的"预计删除 N 行"、
// 合并弹窗的"预计迁移 N 行"）。
//
// **改前**（2026-11-11 之前）：数 unified_data + device_data 两张"原始行"表，
// 并且**故意不数**帧表 —— 理由是帧表是那两张表的压缩表示，加进来会让同一条
// 观测被数两遍，用户看到"预计迁移 100 万行"却只搬了 30 万帧。
//
// **改后**：那两张表都已退役（unified_data 2026-11-08 停写、device_data
// 2026-11-11 整表退役），帧表是**唯一**的时序数据表 ⇒ "数两遍"的第二个来源
// 不存在了，帧表就是正确答案。继续不数它会得到一个恒为 0 的估算 ——
// 那比不准更糟：删除确认弹窗会显示"预计删除 0 行"，用户会以为没数据可删。
//
// ⚠ 仍会出现的口径差（**原有语义**，不是本次引入）：
// **实际删除量会大于本函数给出的预计值。** "彻底删除设备"（purge）和保留期
// 清理还会额外清掉 sensor_field_obs / data_sources / data_source_health 等台账表
// （见 purge_edge_device.go 的清理器清单）。所以"预计删除 100 万行"执行完看到
// 110 万行是正确行为。
//
// 清理动作与计数口径的分工不变：purge / retention 走 telemetry_frame_lifecycle.go
// 的 TimeseriesTables()（那是"数据存续问题"），本文件只服务"给人一个量级"。
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
	// 表清单只有一个元素（帧表），但仍写成循环 + 存在性判断：
	//
	//	· 帧表在精简测试库 / 未跑迁移的旧部署里可能不存在，此时必须返回
	//	  (0, true) 而不是 (0, false) —— 后者会让调用方省略数字，而 0 是诚实的；
	//	· 将来把 logstream 之类纳入统计时只改这里。与 purge/retention 的
	//	  TimeseriesTables() 保持同一"表清单位于 datalifecycle"的原则。
	var total int64
	for _, table := range []string{TelemetryFrameTable} {
		if !db.Migrator().HasTable(table) {
			continue
		}
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
	// 与 EstimateDeviceRows 同口径：只数帧表，表不存在时按 0 行处理（不是失败）。
	var total int64
	for _, table := range []string{TelemetryFrameTable} {
		if !db.Migrator().HasTable(table) {
			continue
		}
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
	// 投影列写 1 而不是 id：**telemetry_frame 没有 id 列**（主键是
	// (device_id, ts, seq)）。改前这里写的是 SELECT id，在帧表上必然报
	// `column "id" does not exist` ⇒ 静默回落到 estimateTruncatedCount（结果正确，
	// 但失去 O(1) 估算能力，变成有界 COUNT）。2026-11-11 数帧表时才发现。
	//
	// 为什么 SELECT 1 也能拿到 rows=：EXPLAIN 输出的是**计划节点**的估算行数，
	// 与投影列无关（只影响是否 index-only scan，不影响 rows=）。
	query := fmt.Sprintf("EXPLAIN SELECT 1 FROM %s WHERE %s", table, cond)
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
