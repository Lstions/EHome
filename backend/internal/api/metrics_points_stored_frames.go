package api

// metrics_points_stored_frames.go — points_stored 改从**帧表**计数（2026-11-08）。
//
// ============ 为什么必须改（生产实测）============
//
// 原实现：
//
//	SELECT count(*) FROM (SELECT device_id, timestamp FROM unified_data
//	                      GROUP BY device_id, timestamp) f
//
// 代码注释记的是「72.36M 行 19.8 s」。现在 unified_data 只剩 17.2M 行，
// 生产却出现过 **33.8 s**，还有一条跑了 **5 分 37 秒未完成**。
//
// ============ 真实根因：锁队列 convoy，不是查询本身慢（生产实测 2026-11-08）============
//
//	单跑                      Execution Time: 3,615.138 ms   （Parallel Seq Scan，无落盘）
//	两个实例同时跑             Execution Time: 33,821 ms
//	生产里跑了 5 分 37 秒那条   **卡在 DETACH 的锁队列后面**，不是在算
//
// ⚠ 我最初猜的是「HashAggregate 落盘」——**被生产 EXPLAIN 推翻**（没有 HashAggregate，
//   Buffers hit=55324，纯 Parallel Seq Scan）。留在这里是为了让下一个人别再往那条路猜。
//
// 实际机制：
//
//	1. 本查询正常 3.6 s，持 **AccessShareLock(unified_data)**；
//	2. /metrics/summary 每 10 s 轮询一次；
//	3. DETACH 请求 **AccessExclusiveLock** ⇒ 进入锁队列等待；
//	4. **PG 的锁队列是 FIFO**：一旦 AccessExclusiveLock 在排队，后续所有
//	   AccessShareLock 请求都必须排在它后面 ⇒ 新的 metrics 查询不断堆积；
//	5. 已持有锁的查询跑完 DETACH 才拿到锁；若又有新查询抢到锁，就得重新等；
//	6. 两个 metrics 实例并存时还会互相竞争 ⇒ 33 s。
//
// **所以真正的危害不是「慢」，而是「持 AccessShareLock 的时间」**：
// 它把任何需要 AccessExclusiveLock 的操作（DETACH/ATTACH/ALTER/DROP PARTITION）挡在队列里。
// 这正是它挡住藏表演练、进而挡住 unified_data 退休的方式。
//
// ============ 为什么帧表是**语义等价**且更便宜的替代 ============
//
// points_stored 的口径是「采样帧数」（见 handler_metrics.go 的 D3 注释）。而：
//
//	telemetry_frame 的主键是 (device_id, ts, seq)，**一帧一行**
//	⇒ count(*) 就是帧数，与 count(DISTINCT (device_id,timestamp)) **同义**
//	⇒ 不需要 GROUP BY、不需要哈希表、count(*) 可走 index-only scan
//	⇒ 表 443 MB vs unified_data 3.5 GB
//
// 而且它同时解决了另一件事：unified_data 正在退休，这个指标本来就得迁。
//
// ============ 为什么还要保留回落 ============
//
// 帧表覆盖 10-02 之后；更早的历史只在 unified_data 里。若帧表为空
// （精简部署 / 回填未跑），退回原口径，避免指标直接变成 0。

import (
	"context"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// pointsStoredQueryTimeout 是这两条计数查询的**硬超时**，也是本文件最重要的一个数字。
//
// ============ 为什么是 2 秒（不是 5 秒）============
//
// DETACH 走的是 `lock_timeout = 5s`。如果本查询可以持 AccessShareLock 达 5 秒，
// 它正好**耗尽** DETACH 的整个预算 ⇒ DETACH 必然超时重试，演练永远等不到锁。
//
//	2 秒 ⇒ 给 DETACH 留 3 秒余量 ⇒ 大概率能排到
//
// 而正常代价远低于此：
//
//	帧表 count(*)        生产实测 **223.723 ms**（走 Parallel Seq Scan，非 index-only）
//	unified_data 旧口径  生产实测 3,615 ms（只在帧表不可用时才走）
//
// 所以 2 秒对帧表路径有 8 倍余量，对回落到旧口径的极端情况则是**主动放弃**：
// 宁可指标卡返回上一次的值（上层有 60s 缓存 + 失败返回旧值），
// 也不能让一条统计查询把整张表的 DDL 卡在队列里。
const pointsStoredQueryTimeout = 2 * time.Second

// countStoredFrames 返回「采样帧数」：优先从帧表精确计数，帧表不可用时回落原口径。
func countStoredFrames(db *gorm.DB) (int64, error) {
	if db == nil {
		return 0, gorm.ErrInvalidDB
	}
	if n, ok := countFramesFromFrameTable(db); ok {
		return n, nil
	}
	return countStoredFramesFromUnified(db)
}

// countFramesFromFrameTable 从 telemetry_frame 精确计数。
//
// ok=false 表示帧表不可用或为空（表不存在 / 回填未跑 / 精简部署），调用方应回落。
func countFramesFromFrameTable(db *gorm.DB) (int64, bool) {
	if !db.Migrator().HasTable(&models.TelemetryFrame{}) {
		return 0, false
	}
	ctx, cancel := context.WithTimeout(context.Background(), pointsStoredQueryTimeout)
	defer cancel()
	var n int64
	if err := db.WithContext(ctx).Model(&models.TelemetryFrame{}).Count(&n).Error; err != nil {
		return 0, false
	}
	if n == 0 {
		return 0, false
	}
	return n, true
}

// countStoredFramesFromUnified 是**旧口径**，只在帧表不可用时使用。
//
// 保留原样（含 GROUP BY 形态）是为了行为可对照：它慢，但语义正确，
// 且只在帧表 없는极端情况下才被调用。
func countStoredFramesFromUnified(db *gorm.DB) (int64, error) {
	ctx, cancel := context.WithTimeout(context.Background(), pointsStoredQueryTimeout)
	defer cancel()
	var frames int64
	frameRows := db.WithContext(ctx).Model(&models.UnifiedData{}).
		Select("device_id, timestamp").
		Group("device_id, timestamp")
	if err := db.WithContext(ctx).Table("(?) AS f", frameRows).Count(&frames).Error; err != nil {
		return 0, err
	}
	return frames, nil
}
