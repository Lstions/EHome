package datalifecycle

// backfill_progress.go — 老数据回填到帧表的**状态表**（契约 §4.3）。
//
// 契约全文：docs/设计/老数据迁移到帧表-分析与契约-2026-11-07.md §4.3。
//
// ============ 为什么需要一张状态表 ============
//
// 生产 unified_data 72,695,394 行 / 3,419,991 帧，按 10k 帧/批 ≈ 342 批，
// 预计 20~40 分钟。这个时长决定了三件事：
//
//  1. **不能同步阻塞启动** —— 维护页会挂 40 分钟，服务不可用；
//  2. **必须可中断续跑** —— 滚动发布 / 重启 / OOM 都会打断它，重头再来是
//     40 分钟 × N 次；
//  3. **必须有进度可见性** —— 运维要知道"还要多久"和"卡在哪"。
//
// 三条都要求断点落在**数据库**里（进程内变量活不过重启）。
//
// ============ 为什么用 AutoMigrate 而不是裸 DDL（重要）============
//
// 本仓的分区母表必须走裸 DDL（AutoMigrate 会把分区母表降级改写成普通表，
// 让分区 DROP 保留期机制静默失效）。但**本表不是分区表** —— 它只有几十行
// （一张表一个月的分区一行）。
//
// 对非分区表，AutoMigrate 是**唯一正确**的选择，而且恰好避免了 2026-11-07 的
// 那类事故（telemetry_rollup_1m 的裸 DDL 漏了 updated_at，SQLite 走 AutoMigrate
// 全绿、生产必炸）。只有一份定义 ⇒ 不可能漂移。
//
// ⚠ 因此：**不要**把它加进 partitionParentDDL，也不需要列校正器兜底。

import (
	"time"

	"gorm.io/gorm"
)

// BackfillProgressTable 是状态表的物理表名。
const BackfillProgressTable = "telemetry_backfill_progress"

// 回填状态机（契约 §4.3 的 state 列取值）。
const (
	// BackfillPending 尚未开始（或上次还没跑到）。
	BackfillPending = "pending"
	// BackfillRunning 正在回填。进程崩溃留下的 running 是可续跑的（从 watermark 继续），
	// 不是错误态 —— 这正是断点续跑依赖的性质。
	BackfillRunning = "running"
	// BackfillDone 该分区已回填完成（可重复运行，幂等）。
	BackfillDone = "done"
	// BackfillFailed 上次失败（保留 watermark，下次从断点重试）。
	BackfillFailed = "failed"
)

// BackfillProgressRow 是状态表的一行（一个时序表的一个分区）。
//
// 契约 §4.3 的 DDL 与下面的字段一一对应：
//
//	table_name  varchar(64)  NOT NULL   物理表名，如 unified_data
//	part_name   varchar(64)  NOT NULL   分区名，如 unified_data_202610
//	state       varchar(16)  NOT NULL   pending/running/done/failed
//	watermark   timestamptz             断点（最后一条**已完整处理**的 timestamp）
//	frames_done bigint       NOT NULL   该分区已回填帧数
//	rows_done   bigint       NOT NULL   该分区已消费的 unified_data 行数
//	started_at  timestamptz
//	finished_at timestamptz
//	error       text
//	PRIMARY KEY (table_name, part_name)
type BackfillProgressRow struct {
	// SrcTable 物理表名（unified_data）。保留这一列是为了将来把 device_data
	// 也回填进帧表时复用同一张状态表，而不必再加一张。
	//
	// 字段名不叫 TableName：那会与 GORM 的 TableName() 接口方法冲突（编译期报错）。
	SrcTable string `gorm:"column:table_name;size:64;not null;primaryKey" json:"table_name"`
	// PartName 分区名。它同时决定了回填的**时间范围**（按月分区名派生），
	// 因此不需要额外的 lo/hi 列。
	PartName string `gorm:"column:part_name;size:64;not null;primaryKey" json:"part_name"`
	// State 见 BackfillPending / Running / Done / Failed。
	State string `gorm:"column:state;size:16;not null;default:pending" json:"state"`
	// Watermark / WatermarkDeviceID 是**复合游标**断点：(最后一条已完整提交的帧的
	// timestamp, 该帧的 device_id)。
	//
	// 为什么必须带 device_id（2026-11-07 P0）：
	//   · 帧键按 (timestamp, device_id) 排序，一批在中间截断时最后一条是 (ts, dev)；
	//     若只记 ts，下一批用 `timestamp > ts` 会**跳过同一 ts 内 device_id 更大的帧**
	//     —— 静默丢数据；
	//   · 更重要的是：原先靠 "watermark + 1ns" 推进，而 **PG timestamptz 精度是
	//     微秒**，加 1ns 等于没加 ⇒ 每轮取回同一批 ⇒ 回填永不完成、CPU 100%。
	//     复合游标彻底不依赖时间精度（PG 微秒 / SQLite 纳秒都正确）。
	Watermark *time.Time `gorm:"column:watermark" json:"watermark,omitempty"`
	// WatermarkDeviceID 是复合游标的第二分量（与 Watermark 同时为 NULL 或同时有值）。
	WatermarkDeviceID uint `gorm:"column:watermark_device_id;not null;default:0" json:"watermark_device_id"`
	// FramesDone 已回填帧数（= 已写入 telemetry_frame 的行数，重跑幂等不累加）。
	FramesDone int64 `gorm:"column:frames_done;not null;default:0" json:"frames_done"`
	// RowsDone 已消费的 unified_data 行数（≈ frames_done × 平均字段数，用于对账）。
	RowsDone int64 `gorm:"column:rows_done;not null;default:0" json:"rows_done"`
	// StartedAt 首次开始时间（重跑不覆盖，便于看"跑了多久"）。
	StartedAt *time.Time `gorm:"column:started_at" json:"started_at,omitempty"`
	// FinishedAt 完成时间。
	FinishedAt *time.Time `gorm:"column:finished_at" json:"finished_at,omitempty"`
	// Error 最后一次失败原因（成功时清空）。
	Error string `gorm:"column:error;type:text" json:"error,omitempty"`
}

// TableName 显式声明表名。
func (BackfillProgressRow) TableName() string { return BackfillProgressTable }

// EnsureBackfillProgressTable 建/补齐状态表（幂等）。
//
// 在 main.go 的分区迁移之后、回填 goroutine 之前调用一次；测试里各自调用。
// 失败语义由调用方决定：生产上状态表缺失只影响"回填能不能跑"，不影响主写入，
// 因此是 Warn 不 Fatal（与 telemetry_rollup_1m 同口径，不是主路径）。
func EnsureBackfillProgressTable(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	return db.AutoMigrate(&BackfillProgressRow{})
}
