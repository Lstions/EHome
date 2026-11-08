package datalifecycle

import "time"

// batch_delete.go — 大表分批操作的共用参数。
//
// 历史: 这些常量原本分散定义在 purge.go（逻辑设备 purge 任务）与 migrate.go
// （逻辑设备合并搬迁）里，却被 8 个表清理器与分区迁移共用。2026-10-23 逻辑设备层
// 移除时那两个文件被删，常量必须**独立存在** —— 这本身说明它们不属于任何
// 逻辑设备语义，只是「大表分批写」的通用参数。
const (
	// purgeBatchSizePostgres 是 PostgreSQL 每批删除行数。
	// 每批一个独立事务: 大表单条 DELETE 会长时间持锁并放大 WAL。
	purgeBatchSizePostgres = 10000
	// purgeBatchSizeSQLite 是 SQLite（测试环境）每批行数 —— 库级写锁更粗，故缩小。
	purgeBatchSizeSQLite = 1000
	// purgeBatchSleep 是批间间隔，避免 WAL 膨胀 / 副本延迟。
	purgeBatchSleep = 200 * time.Millisecond
	// maxPurgeBatches 是单次清理的批数上限（安全网，防止无限循环）。
	maxPurgeBatches = 100000

	// migrateBatchSizePostgres 是分区迁移（copyMonthBatched）每批行数。
	// 与删除侧同量级: 每批一个独立事务, 避免长事务锁住整个月分区。
	migrateBatchSizePostgres = 10000
)
