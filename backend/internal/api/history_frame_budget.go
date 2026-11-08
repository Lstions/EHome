package api

// history_frame_budget.go — 帧解码的**内存护栏**（P0，2026-11-08）。
//
// ============ 风险 ============
//
// GET /devices/14/history?hours=168 会解出约 1720 万行（168h x 29 字段），
// 按 ~180 B/行折算 ≈ **3 GB 堆**。而生产主机总内存 4 GiB、可用 2.9 GiB，
// 容器**无内存限制**（Memory=0）。
//
//	=> OOM => 被杀的可能是同机的 PostgreSQL => 整站不可用
//
// 触发只需要一个有效 token。这是**活的 DoS**，不是理论风险。
//
// ============ 为什么 max_points 不能免除检查 ============
//
// 降采样发生在解码**之后**（见 device_history_downsample.go），堆峰值与响应大小无关。
// 即使只要 100 个点，解码阶段仍要物化/遍历整个窗口。
//
// ============ 判据按「行数」而不是「帧数」（2026-11-08 Lead 裁决）============
//
// 第一版按帧数（50,000 帧）设限，有两个问题：
//
//	1. 太紧：device14 的 24h 是 84,866 帧，会被一刀闸掉 ——
//	   而 24h 正是唯一能验证「4 段分段解码」的窗口，等于把核心覆盖也闸了。
//	2. **更根本：忽略了「设备字段数」这个维度。**
//	   50,000 帧在 29 字段设备上是 145 万行（~260 MB），
//	   在 100 字段设备上是 500 万行（~900 MB）—— **同样的帧数，内存差 3.4 倍**。
//
// 所以判据是：**帧数 x 该设备字段数 > maxHistoryRowsPerRequest**。
//
// ============ 阈值依据 ============
//
//	maxHistoryRowsPerRequest = 300 万行（Lead 2026-11-08 裁决）
//
// 两个口径都要用：
//
//	TotalAlloc 501 B/行  = 含分配 churn，**上界**（本地实测）
//	live heap  180 B/行  = 稳态驻留，**下界**（boot-api 从 3 GB / 1720 万行反推）
//
// 阈值按 live heap 定，用 TotalAlloc 做余量检查：
//
//	3,000,000 x 180 B = **540 MB live heap**
//	3,000,000 x 501 B = **1.50 GB 分配峰值**  <= 容器限制 1.5 GiB（1.61 GB）
//
// 对照各窗口（device14, 29 字段）：
//
//	 6h    21,600 帧 x 29 =   626,400 行 => 允许（113 MB live）
//	24h    84,866 帧 x 29 = 2,461,114 行 => **允许（443 MB，占可用 2.9 GiB 的 15%）**
//	48h   169,732 帧 x 29 = 4,922,228 行 => 拒绝（886 MB，31%）
//	168h  601,278 帧 x 29 = 17,437,062 行 => 拒绝（3.1 GB，107% => 必然 OOM）
//
// 分界落在 24h 与 48h 之间：24h 单请求占可用 15%（6.6x 余量），
// 48h 占 31%（两个并发就危险），168h 单请求即 OOM。
//
// ============ 与容器内存限制的关系（**这是刻意卡的，不是设松/设紧）**============
//
// 容器限制 1.5 GiB（1.61 GB）。而 3,000,000 行的 TotalAlloc 峰值约 1.50 GB ——
// **刚好容纳一个 24h 请求的分配峰值，不留第二个**。
//
// 这是有意的：单请求可过；并发第二个会 OOM **容器**（容器重启），而不是拖垮主机上的
// PostgreSQL。**宁可容器重启，不要整站不可用。**
//
// ⚠ 这是**按行估算**，我没有生产堆剖面。本地实测（TotalAlloc 口径）约 501 B/行，
//   比 180 B/行更保守；真实 live heap 在两者之间。阈值取 500 万行时，
//   两个口径分别对应 900 MB 与 2.5 GB —— 若生产真接近后者，应下调本常量。

import (
	"context"
	"time"

	"gorm.io/gorm"
)

// maxHistoryRowsPerRequest 是单次历史请求允许物化的最大行数（帧 x 字段）。
const maxHistoryRowsPerRequest = 3000000

// defaultSensorCountForBudget 是**查不到该设备字段数**时使用的保守估计。
//
// 什么时候会查不到：sensor_field_obs 没有该设备的行（新设备、投影未开）。
// 取 100（最坏情况）而不是 1：宁可高估行数（更早拒绝），
// 也不要因为低估而放行一个 OOM 请求。字段数拿不到时按最坏设备算。
const defaultSensorCountForBudget = 100

// historySensorCount 返回该请求实际会解出多少个字段。
//
// 指定了 sensor 参数 => 只解那一个字段（解码器的 Fields 过滤只影响产出，
// 但仍会少产出其余字段的对象）=> 1。
// 否则查维度表：sensor_field_obs 每 (device, sensor) 一行，主键前缀扫描，微秒级。
func historySensorCount(ctx context.Context, db *gorm.DB, deviceID uint, sensorParam string) int {
	if sensorParam != "" {
		return 1
	}
	if db == nil {
		return defaultSensorCountForBudget
	}
	var n int64
	if err := db.WithContext(ctx).Raw(
		"SELECT count(*) FROM sensor_field_obs WHERE device_id = ?", deviceID).Scan(&n).Error; err == nil && n > 0 {
		return int(n)
	}
	return defaultSensorCountForBudget
}

// historyFrameCountUpTo 返回该窗口的帧数，但**最多探测 ceiling+1 条**。
//
// 为什么用 LIMIT 子查询而不是真 count(*)：真 count 在 1720 万行上本身就要扫全量；
// 我们只关心「是否超限」，一看到足够多就停 —— 超额时毫秒级返回。
//
// 返回 0 表示查询失败或表不存在（保守：不因为护栏本身出错就拒绝请求）。
func historyFrameCountUpTo(ctx context.Context, db *gorm.DB, deviceID uint, start, end time.Time, ceiling int64) int64 {
	if db == nil || !end.After(start) || ceiling <= 0 {
		return 0
	}
	if !db.Migrator().HasTable("telemetry_frame") {
		return 0
	}
	var n int64
	err := db.WithContext(ctx).Raw(
		"SELECT count(*) FROM (SELECT 1 FROM telemetry_frame "+
			"WHERE device_id = ? AND ts >= ? AND ts < ? LIMIT ?) t",
		deviceID, start.UTC(), end.UTC(), ceiling+1).Scan(&n).Error
	if err != nil {
		return 0
	}
	return n
}

// historyBudgetVerdict 是对一次历史请求的内存预算判定。
type historyBudgetVerdict struct {
	Exceeded   bool
	Frames     int64 // 探测到的帧数（可能被 ceiling 截断，故用 ">=" 语义）
	Sensors    int   // 该请求会解出的字段数
	RowCeiling int64 // 允许的最大行数
}

// checkHistoryFrameBudget 按**行数**判定：帧数 x 字段数 > maxHistoryRowsPerRequest。
//
// 帧数上限由行数上限除以字段数得到 —— 这就是「按行数」与「按帧数」的差别：
// 字段多的设备，允许的帧数自动变少。
func checkHistoryFrameBudget(
	ctx context.Context, db *gorm.DB, deviceID uint, sensorParam string, start, end time.Time,
) historyBudgetVerdict {
	sensors := historySensorCount(ctx, db, deviceID, sensorParam)
	if sensors < 1 {
		sensors = 1
	}
	frameCeiling := int64(maxHistoryRowsPerRequest / sensors)
	if frameCeiling < 1 {
		frameCeiling = 1
	}
	frames := historyFrameCountUpTo(ctx, db, deviceID, start, end, frameCeiling)
	return historyBudgetVerdict{
		Exceeded:   frames > frameCeiling,
		Frames:     frames,
		Sensors:    sensors,
		RowCeiling: int64(maxHistoryRowsPerRequest),
	}
}
