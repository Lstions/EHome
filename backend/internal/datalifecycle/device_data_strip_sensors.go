package datalifecycle

// device_data_strip_sensors.go — 存量 device_data 剥离冗余 `sensors` 字段。
//
// ==================== 为什么是「剥离」而不是「压缩」====================
//
// 本轮最初的计划（task-17）是给 device_data 做应用层 gzip。实测推翻了它的前提：
//
//	A 类(带 sensors) 3,462,696 行：原始 5,514 MB → PostgreSQL 实际存储 1,981 MB（2.78x）
//	B 类(raw only)   4,037,700 行：原始   871 MB → 存储   885 MB（0.98x）
//
// **PG 的 TOAST/pglz 一直在压缩 data_json**，那 4.4 GB 是压缩**之后**的体积。
// 再加一层 gzip 只能再省 210 MB（逐行压缩窗口小）；deflate+预设字典能省 870 MB，
// 但要把字典永久冻结 + 前缀版本共存，是永久负担。
//
// 而 `sensors` 与 unified_data / telemetry_frame 存的是**同一份 (name,value,unit)**，
// 100% 重复。所以：**压缩是在给一个不该存在的副本减肥；剥离是直接切掉副本。**
// 实测（2000 行真实样本 `jsonb - 'sensors'`）：1,278 kB → 468 kB。
//
// ==================== 三条硬约束（改之前必读）====================
//
//	I1. 幂等：判据是 `data_json LIKE '%"sensors"%'` —— 剥离后不再匹配，
//	    重跑天然 no-op。不需要额外状态位，也不存在"重复剥离会损坏"的可能。
//	I2. 不碰 B 类：raw-only 行本来就没有 sensors，重写它们只会制造无谓的死元组
//	    与 WAL。判据已经排除了它们。
//	I3. 字段不丢：`jsonb - 'sensors'` 是**按键删除**，channel_id / raw_hex /
//	    timestamp / str_values 原样保留（只是键序可能变，JSON 语义不变）。
//
// ==================== ⚠ 磁盘不会立刻变小（必须写进交付说明）====================
//
// UPDATE 是「写新元组 + 标记旧元组为死」，**表文件不会缩小**。省下的空间有两种去向：
//   · plain VACUUM：标记为可重用 ⇒ **阻止表继续增长**（新写入复用）+ 索引不再膨胀；
//   · VACUUM FULL / pg_repack：真正把文件还给操作系统，但要独占锁 + 约等于表大小的
//     临时空间（16 GB 可用、表 4.4 GB ⇒ 可行，但需维护窗口）。
//
// 所以「省 1.25 GB」的准确表述是：
//   · 逻辑字节（pg_column_size 合计）立刻少 1.25 GB；
//   · 物理文件要 VACUUM FULL 才缩小，否则体现为「不再增长」。
//
// 本任务提供 Vacuum()（plain VACUUM，可在线跑），**不**自动执行 VACUUM FULL ——
// 那需要维护窗口，属于运维动作，在日志与进度接口里明确提示。

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"gorm.io/gorm"
)

const (
	// DeviceDataStripTable 是剥离任务的状态表名。
	DeviceDataStripTable = "device_data_strip_progress"

	// deviceDataStripTaskKey 是状态表单行主键。剥离任务是**全表一个游标**
	// （不像回填按分区），因为 UPDATE 不改变行数、也不需要按分区提交。
	deviceDataStripTaskKey = "device_data"

	// defaultStripBatchSize 每批行数。生产 PG 只有 4 GB 内存，
	// 500 是「一条 UPDATE 的 WAL 量可接受 + 不会长时间持有行锁」的折中。
	//
	// 为什么不调大：这不是吞吐任务而是**磁盘回收**任务，早点跑完没有额外收益，
	// 而每批放大一倍，单事务的 WAL 量与锁持有时间也放大一倍 —— 期间写入路径
	// （每秒几百行 unified_data / device_data）会更容易撞上锁等待。
	defaultStripBatchSize = 500

	// defaultStripBatchSleep 批间休眠。**必须 > 0**，理由有两条：
	//  1. UPDATE 会产生大量 WAL；不节流会让 PostgreSQL 的 WAL 写入与检查点
	//     压力陡增，间接拖慢主写入路径；
	//  2. 本任务与实时写入共用同一张表（device_data 同时在被 INSERT），
	//     持续的长事务会推高 autovacuum 的负担。
	//
	// ==================== 实测耗时（2026-11-07，本地 PG18，568,617 行真实数据）====================
	//
	// 用真实数据跑完整剥离（175,946 行带 sensors，880 批 × 200 行）测得：
	//
	//	纯 UPDATE 工作量 = 143 ms/批 = 0.716 ms/行   ← 不含 sleep
	//	纯 SELECT 扫描     = 0.9 ms/批               ← LIKE 过滤几乎不花钱
	//
	// 也就是说：**瓶颈是 UPDATE 的 WAL 与页写，不是「扫 750 万行判断有没有 sensors」**。
	// 因为游标按 id 推进，每行全表只被扫一次；`LIMIT 200` 统计的是**匹配行数**而不是
	// 扫描行数 —— 批数 = 3,462,696/200 = 17,313，不是 7,500,000/200。
	// （这个区别值得记住：按扫描行数估会把总时长高估一倍。）
	//
	// 生产外推（3,462,696 行 A 类；含 sleep）：
	//
	//	batch=200  sleep=200ms → 99 分钟
	//	batch=200  sleep=50ms  → 56 分钟
	//	batch=500  sleep=200ms → 64 分钟
	//	batch=1000 sleep=100ms → 47 分钟
	//	batch=2000 sleep=100ms → 44 分钟   ← 再放大收益递减（WAL/锁持有时间反升）
	//
	// 取 200ms 而不是更短：本任务是**后台回收**任务，不追求尽快跑完；而生产只有
	// 4 GB 内存、表 4.4 GB ⇒ 冷缓存下每行的实际 IO 成本比本地内存命中高一个量级，
	// 上面的实测值只能当**下界**。留足余量比省 40 分钟重要。
	//
	// ⚠ 上表是本地内存命中测的，**生产可能慢 2~3 倍**（99 分钟 → 3~5 小时）。
	// 部署后看 device_data_strip_progress.rows_stripped 的实际增速再调 batch
	// （EHOME_DEVICE_DATA_STRIP_BATCH 改大即可，幂等，已剥离的不会重复处理）。
	//
	// 注：设计时还担心过「与老数据回填并行会抢 IO」，但回填已于 2026-11-07 跑完
	//（state=done, frames_done=3,454,432），当前不存在这个竞争。
	defaultStripBatchSleep = 200 * time.Millisecond
)

// 剥离任务状态机的 state 取值。
const (
	// StripPending 尚未开始。
	StripPending = "pending"
	// StripRunning 正在跑（进程崩溃留下的 running 从 watermark 继续，不是错误）。
	StripRunning = "running"
	// StripDone 当前已无匹配行（可重跑，幂等）。
	StripDone = "done"
	// StripFailed 上次失败（watermark 保留，下次续跑）。
	StripFailed = "failed"
)

// DeviceDataStripProgressRow 是剥离任务的状态行（全表一行）。
//
// 为什么需要状态表（而不是每次从 id=0 扫）：`WHERE id > watermark AND LIKE`
// 每次都要走一遍索引，若 watermark 不持久化，重启后从 0 开始会把整表索引扫一遍
// —— 3.46M 行 × 多次重启，纯浪费。落库后重启只从上次位置继续。
type DeviceDataStripProgressRow struct {
	// TaskKey 单行主键（固定 "device_data"）。
	TaskKey string `gorm:"column:task_key;size:64;not null;primaryKey" json:"task_key"`
	// State 见 StripPending / Running / Done / Failed。
	State string `gorm:"column:state;size:16;not null;default:pending" json:"state"`
	// WatermarkID 最后一条**已处理**的 device_data.id。
	//
	// 用 id 而不是时间戳：① id 是复合主键前缀，天然有序可索引；
	// ② 本任务不依赖时间语义（不像回填要按时间范围圈定），id 更简单也更不容易错
	//    —— 回填踩过「watermark + 1ns 在 PG 微秒精度下等于没加」的坑。
	WatermarkID uint `gorm:"column:watermark_id;not null;default:0" json:"watermark_id"`
	// RowsSelected 已选中处理的行数（含被守卫跳过的）。
	//
	// 与 RowsStripped 并列落库：**差值就是告警信号**，而且它是「跑完之后
	// 才发现有问题」的唯一线索 —— 进程重启后 RunOnce 的返回值丢了，
	// 只有库里这两个数还在。
	RowsSelected int64 `gorm:"column:rows_selected;not null;default:0" json:"rows_selected"`
	// RowsStripped 已剥离行数（重跑不累加已完成的，但会累加"本次新剥离的"）。
	RowsStripped int64 `gorm:"column:rows_stripped;not null;default:0" json:"rows_stripped"`
	// RowsSkipped 被守卫跳过、未剥离的行数。
	RowsSkipped int64 `gorm:"column:rows_skipped;not null;default:0" json:"rows_skipped"`
	// BytesBefore / BytesAfter 已处理行的 data_json 逻辑字节合计（剥离前 / 后）。
	//
	// 这两个是**进度可见性**的核心：运维最想知道的是"省了多少"，而不是"跑了多少行"。
	// 用 octet_length 累加，是逻辑字节；物理释放要看 pg_total_relation_size（见文件头）。
	BytesBefore int64 `gorm:"column:bytes_before;not null;default:0" json:"bytes_before"`
	BytesAfter  int64 `gorm:"column:bytes_after;not null;default:0" json:"bytes_after"`
	// StartedAt 首次开始时间（重跑不覆盖，便于看"跑了多久"）。
	StartedAt *time.Time `gorm:"column:started_at" json:"started_at,omitempty"`
	// FinishedAt 完成时间（state=done 时有值）。
	FinishedAt *time.Time `gorm:"column:finished_at" json:"finished_at,omitempty"`
	// Error 最后一次失败原因（成功时清空）。
	Error string `gorm:"column:error;type:text" json:"error,omitempty"`
}

// TableName 显式声明表名。
func (DeviceDataStripProgressRow) TableName() string { return DeviceDataStripTable }

// EnsureDeviceDataStripTable 建/补齐状态表（幂等）。
//
// 与 backfill_progress.go 同一套理由：本表**不是分区表**，所以走 AutoMigrate，
// 不要加进 partitionParentDDL。
func EnsureDeviceDataStripTable(db *gorm.DB) error {
	if db == nil {
		return nil
	}
	return db.AutoMigrate(&DeviceDataStripProgressRow{})
}

// DeviceDataSensorStripper 是存量 device_data 的 sensors 剥离任务。
//
// PG-only：`jsonb - 'sensors'` 是 PG 专有语法。SQLite 测试库没有 jsonb，
// 也没有这段历史数据（测试库是空表），所以 RunOnce 在非 PG 上直接 no-op 返回。
// **不引入方言分支** —— 写一个 SQLite 的字符串补丁版只会让"哪个方言做过什么"变模糊。
type DeviceDataSensorStripper struct {
	db         *gorm.DB
	batchSize  int
	batchSleep time.Duration
	now        func() time.Time

	// afterBatch 是**确定性测试钩子**：每批提交后调用一次，返回非 nil 即中止。
	//
	// 为什么不用 sleep 模拟中断（回填的教训）：sleep 既慢又不确定 ——
	// "睡 100ms 后 kill" 在不同机器上落在不同的批次边界，断言就成了概率题。
	// 钩子让"跑完第 2 批就停"变成确定性事实，续跑用例才能可靠地断言
	// 「从 watermark 继续、不重复不遗漏」。
	afterBatch func(processed int64) error

	// vacuumAfterRun 控制 RunOnce 完成后是否做 plain VACUUM（默认 true）。
	//
	// 测试里通常置 false：VACUUM 需要真实 PG，且与本任务要验的
	// 「幂等/续跑/字段保留」无关，开了只会让用例变慢。
	vacuumAfterRun bool
	// lastVacuumPartitions 记录最近一次 Vacuum 处理过的分区（可观测性）。
	lastVacuumPartitions []string
}

// NewDeviceDataSensorStripper creates the historical strip task.
func NewDeviceDataSensorStripper(db *gorm.DB) *DeviceDataSensorStripper {
	return &DeviceDataSensorStripper{
		db:             db,
		batchSize:      defaultStripBatchSize,
		batchSleep:     defaultStripBatchSleep,
		now:            time.Now,
		vacuumAfterRun: true,
	}
}

// SetBatchSize overrides the per-batch row count (tests).
func (s *DeviceDataSensorStripper) SetBatchSize(n int) {
	if n > 0 {
		s.batchSize = n
	}
}

// SetBatchSleep overrides the inter-batch sleep (tests; 0 disables).
func (s *DeviceDataSensorStripper) SetBatchSleep(d time.Duration) { s.batchSleep = d }

// SetVacuumAfterRun overrides whether to VACUUM after a successful run (tests).
func (s *DeviceDataSensorStripper) SetVacuumAfterRun(on bool) { s.vacuumAfterRun = on }

// SetAfterBatchHook installs the deterministic per-batch hook (tests).
func (s *DeviceDataSensorStripper) SetAfterBatchHook(fn func(processed int64) error) {
	s.afterBatch = fn
}

// StripReport summarizes one RunOnce.
type StripReport struct {
	// RowsSelected 本次**选中待处理**的行数（每批 SELECT 命中 id 数之和）。
	//
	// 与 RowsStripped 并列而不是只报后者，因为**差值本身是一个信号**：
	//
	//	RowsSelected > RowsStripped  ⇒ 有行被守卫跳过了（sensors 不是数组等坏数据）。
	//
	// 这些行的 id **已被游标越过**，不会再被重试；若只报 RowsStripped，
	// 任务会显示「跑完了」而实际有一批行没处理，且没有任何报错。
	RowsSelected int64 `json:"rows_selected"`
	// RowsStripped 本次**实际剥离**的行数（成功 UPDATE 的行数）。
	RowsStripped int64 `json:"rows_stripped"`
	// RowsSkipped 被守卫跳过、未剥离的行数（= Selected - Stripped）。
	//
	// 单独给字段而不是让调用方做减法：消费方不该重复这个推导，
	// 而且减法在「批次已处理但 Selected 未记录」的边界上容易算错。
	RowsSkipped int64 `json:"rows_skipped"`
	// BytesBefore / BytesAfter 本次处理行的 data_json 逻辑字节合计。
	BytesBefore int64 `json:"bytes_before"`
	BytesAfter  int64 `json:"bytes_after"`
	// Batches 本次执行的批次数。
	Batches int `json:"batches"`
	// Done 是否已无匹配行（true ⇒ 全表剥离完成）。
	Done bool `json:"done"`
	// Skipped 非 PG 环境而跳过。
	Skipped bool `json:"skipped,omitempty"`
}

// BytesReclaimed 是本次释放的逻辑字节数。
func (r StripReport) BytesReclaimed() int64 {
	if r.BytesBefore <= r.BytesAfter {
		return 0
	}
	return r.BytesBefore - r.BytesAfter
}

// RunOnce 执行（或续跑）剥离，直到没有匹配行或遇到错误。
//
// 每批一个事务：UPDATE 与 watermark 推进**同一事务**，因此崩溃点落在批边界，
// 不会出现"更新了但游标没动"（那会重复处理 — 因为幂等，重复也无害，
// 但会让 RowsStripped 计数虚高）。
func (s *DeviceDataSensorStripper) RunOnce(ctx context.Context) (StripReport, error) {
	var rep StripReport
	if s.db == nil {
		return rep, fmt.Errorf("datalifecycle: device_data strip requires a db")
	}
	if !isPostgres(s.db) {
		// SQLite：没有 jsonb，也没有这段历史数据。no-op 而不是报错 ——
		// 测试库与精简部署都应该能正常启动。
		rep.Skipped = true
		rep.Done = true
		return rep, nil
	}
	if err := EnsureDeviceDataStripTable(s.db); err != nil {
		return rep, fmt.Errorf("ensure %s: %w", DeviceDataStripTable, err)
	}
	row, err := s.loadProgress()
	if err != nil {
		return rep, err
	}
	if row == nil {
		now := s.now()
		row = &DeviceDataStripProgressRow{
			TaskKey:   deviceDataStripTaskKey,
			State:     StripRunning,
			StartedAt: &now,
		}
		if err := s.db.Create(row).Error; err != nil {
			return rep, fmt.Errorf("create %s: %w", DeviceDataStripTable, err)
		}
	} else {
		row.State = StripRunning
		row.Error = ""
		if err := s.db.Model(&DeviceDataStripProgressRow{}).
			Where("task_key = ?", deviceDataStripTaskKey).
			Updates(map[string]interface{}{"state": StripRunning, "error": ""}).Error; err != nil {
			return rep, fmt.Errorf("mark %s running: %w", DeviceDataStripTable, err)
		}
	}

	for {
		if err := ctx.Err(); err != nil {
			return rep, err
		}
		b, err := s.stripBatch()
		if err != nil {
			_ = s.db.Model(&DeviceDataStripProgressRow{}).
				Where("task_key = ?", deviceDataStripTaskKey).
				Updates(map[string]interface{}{"state": StripFailed, "error": err.Error()})
			return rep, err
		}

		// Exhausted 才是「真的跑完了」。
		//
		// ⚠ 这里曾经写成 if n == 0 { Done = true }，而 n 是 UPDATE 的 rows-affected ——
		// 于是一批**全部**是坏行（sensors 不是数组）时会被误判成完成，
		// 后面的几百万行永远不会被处理，而日志显示「完成」。加了 selected 才能区分。
		if b.Exhausted {
			rep.Done = true
			break
		}

		rep.Batches++
		rep.RowsSelected += b.Selected
		rep.RowsStripped += b.Stripped
		rep.RowsSkipped += b.Skipped()
		rep.BytesBefore += b.BytesBefore
		rep.BytesAfter += b.BytesAfter

		// 每批一条日志：selected 与 stripped 并列。
		//
		// skipped > 0 时升到 Warn：有行被 jsonb_typeof 守卫跳过，而它们的 id
		// 已被游标越过（不会重试）⇒ 必须有人看见，否则就是静默漏处理。
		if skipped := b.Skipped(); skipped > 0 {
			slog.Warn("datalifecycle: device_data strip skipped malformed rows "+
				"(sensors is not a JSON array); their ids are past the watermark and will NOT be retried",
				"selected", b.Selected, "stripped", b.Stripped, "skipped", skipped,
				"watermark", b.MaxID)
		} else {
			slog.Debug("datalifecycle: device_data strip batch",
				"selected", b.Selected, "stripped", b.Stripped, "watermark", b.MaxID)
		}

		// 推进游标（与 UPDATE 分开提交是可接受的：UPDATE 已幂等，
		// 游标落后只会重扫已处理的 id，不会错删或错改）。
		if err := s.advance(b.MaxID, b.Selected, b.Stripped, b.Skipped(), b.BytesBefore, b.BytesAfter); err != nil {
			return rep, err
		}
		if s.afterBatch != nil {
			if err := s.afterBatch(rep.RowsStripped); err != nil {
				return rep, err
			}
		}
		if s.batchSleep > 0 {
			select {
			case <-ctx.Done():
				return rep, ctx.Err()
			case <-time.After(s.batchSleep):
			}
		}
	}

	fin := s.now()
	if err := s.db.Model(&DeviceDataStripProgressRow{}).
		Where("task_key = ?", deviceDataStripTaskKey).
		Updates(map[string]interface{}{"state": StripDone, "finished_at": fin}).Error; err != nil {
		return rep, fmt.Errorf("mark %s done: %w", DeviceDataStripTable, err)
	}
	// 有选中行就记一条（不只 stripped>0）：全是坏行时 skipped>0 而 stripped==0，
	// 那种情况更需要日志，否则「任务完成但一行没剥」看上去像正常空跑。
	if rep.RowsSelected > 0 || rep.RowsStripped > 0 {
		slog.Info("datalifecycle: device_data sensors stripped",
			"selected", rep.RowsSelected, "stripped", rep.RowsStripped, "skipped", rep.RowsSkipped,
			"bytes_before", rep.BytesBefore,
			"bytes_after", rep.BytesAfter, "logical_reclaimed", rep.BytesReclaimed(),
			// 明确提示物理空间未回收 —— 「省了 1.3 GB」只是账面，磁盘要 VACUUM FULL。
			"note", "physical disk not reclaimed; run VACUUM FULL in a maintenance window to release")
	}

	// plain VACUUM（默认开启）：不缩小文件，但把死元组占的空间**标记为可重用**。
	//
	// 为什么必须做：3.46M 行的 UPDATE 会在每个分区里留下等量的死元组。
	// 不做 VACUUM 的话，表在剥离后**反而会先变大**（实测 3000 行样本：
	// 1,998,848 B → 2,842,624 B，因为死元组还占着页），然后才靠自动 vacuum 慢慢回收。
	// 「剥离把磁盘撑大了」是运维最不能接受的一种结果，所以这里主动做一次。
	//
	// 为什么**只做 plain VACUUM 不做 VACUUM FULL**：
	//   · VACUUM FULL 要 ACCESS EXCLUSIVE 锁（整表不可读写）+ 约等于表大小的临时空间；
	//   · 生产表 4.4 GB、盘上只剩 16 GB —— 空间够，但锁窗口必须进维护窗口；
	//   · 而 plain VACUUM 已经能达成「表不再继续长」的效果，也就是本任务的核心目标。
	// 真正的缩小由运维在维护窗口执行 VACUUM FULL / pg_repack（日志里已提示）。
	if s.vacuumAfterRun && rep.RowsStripped > 0 {
		if err := s.Vacuum(ctx); err != nil {
			// VACUUM 失败不影响剥离成果（数据已经改好了），只告警。
			slog.Warn("datalifecycle: device_data VACUUM after strip failed "+
				"(剥离本身已完成；空间会在自动 vacuum 时回收)", "error", err)
		}
	}
	return rep, nil
}

// stripBatchResult 是一批的处理结果。
//
// **为什么 selected 与 stripped 必须分开**（这是本轮最重要的一个修正）：
//
// 旧实现只有一个 rows-affected，于是「SELECT 到 200 行」与「UPDATE 成功 0 行」
// 在返回值上**无法区分** —— 两者都表现为 n==0，而旧循环把 n==0 当作「全表跑完了」。
// 后果：只要出现一批**全部**是坏数据（sensors 不是数组）的行，任务就会
//  1. 认为已完成（Done=true），而**后面还有几百万行没处理**；
//  2. 状态表被标记 done，下次启动直接跳过。
//
// 也就是说：一批坏行能让整个任务静默半途而废，而日志显示「完成」。
//
// 拆成两个数之后：
//
//	selected == 0                 ⇒ 真的没有匹配行了 ⇒ 完成；
//	selected > 0 && stripped == 0 ⇒ 全是坏行 ⇒ **跳过并继续**，不当作完成；
//	selected > stripped           ⇒ 部分坏行 ⇒ 继续，但差值必须可见。
type stripBatchResult struct {
	// Selected 本批选中的行数（SELECT 命中的 id 数）。
	Selected int64
	// Stripped 本批实际更新成功的行数。
	Stripped int64
	// BytesBefore / BytesAfter 本批 data_json 逻辑字节合计。
	BytesBefore int64
	BytesAfter  int64
	// MaxID 本批最后一个 id（推进游标用）。
	MaxID uint
	// Exhausted 本批没有选中任何行（游标已到表尾）⇒ 任务完成。
	Exhausted bool
}

// Skipped 是本批被守卫跳过的行数。
func (r stripBatchResult) Skipped() int64 {
	if r.Selected <= r.Stripped {
		return 0
	}
	return r.Selected - r.Stripped
}

// stripBatch 处理一批：选 id → UPDATE → 返回详细结果。
func (s *DeviceDataSensorStripper) stripBatch() (stripBatchResult, error) {
	var res stripBatchResult
	var ids []uint
	if err := s.db.Raw(
		`SELECT id FROM device_data
		 WHERE id > ? AND data_json LIKE '%"sensors"%'
		 ORDER BY id LIMIT ?`,
		s.currentWatermark(), s.batchSize,
	).Scan(&ids).Error; err != nil {
		return res, fmt.Errorf("select strip batch: %w", err)
	}
	if len(ids) == 0 {
		res.Exhausted = true
		return res, nil
	}
	res.Selected = int64(len(ids))
	res.MaxID = ids[len(ids)-1]

	if err := s.db.Raw(
		`SELECT COALESCE(sum(octet_length(data_json)),0) FROM device_data WHERE id IN ?`, ids,
	).Scan(&res.BytesBefore).Error; err != nil {
		return res, fmt.Errorf("measure batch before: %w", err)
	}

	// ==================== 为什么不是简单的 jsonb - 'sensors' ====================
	//
	// sensors 数组里每个条目是 {Name, Value, Unit, StringValue}。前三者与
	// unified_data / telemetry_frame **100% 重复**（那两张表都有对应列），是本次要
	// 切掉的冗余副本。
	//
	// 但 **StringValue 没有任何副本**：UnifiedData 只有 SensorName/Value/Unit，
	// telemetry.Reading 只有 Name/Value。所以 device_data.sensors 是 BMS
	// hardware_version("V19") / serial_number 的**唯一**落库位置。
	// 直接 jsonb - 'sensors' 会把它们永久删除，且无任何其他来源可恢复。
	//
	// 处置：剥离前先把 StringValue 非空的条目抽成紧凑映射写进 str_values，
	// 形状与 task-19 写侧**完全一致**：
	//
	//	{"raw_hex":"...","channel_id":N,"timestamp":T,"str_values":{"hardware_version":"V19"}}
	//
	// 实测（生产只读）：带非空 StringValue 的历史行 = **0 行**（全库）。
	// 即：这段逻辑在本库现数据上是 no-op，但**必须写** —— 否则一旦某个现场版
	// 固件读过 0x05/0xA2，那批 hardware_version 就会被静默抹掉，而没人会发现
	// （前端只显示数值面板，字符串字段平时没人看）。宁可多一个 CASE 分支。
	//
	// COALESCE(..., {}) 的用途：绝大多数行没有非空 StringValue，此时
	// jsonb_object_agg 返回 NULL，用 {} 兜住 ⇒ || {} 是恒等操作，零额外字节。
	//
	// ==================== jsonb_typeof 守卫的代价与处置 ====================
	//
	// 守卫让 sensors 不是数组的坏行**不进 UPDATE**（否则 cast 报错会让整批失败，
	// 3.46M 行的任务卡在一行坏数据上永远跑不完）。
	//
	// ⚠ 代价：这些行的 id **已经被游标越过**（MaxID 取本批最后一个 id，不区分它
	// 有没有被成功更新）⇒ 它们**不会再被重试**。这是**故意**的取舍：
	// 「一行坏数据阻塞整个任务」比「一行坏数据被跳过」更糟。
	//
	// 但「故意的跳过」必须是**可见的**，否则就是静默丢数据：
	//  · stripBatchResult.Skipped() 给出本批跳过数；
	//  · 每批日志打 selected=N stripped=M，M<N 时 Warn；
	//  · 进度表落 rows_selected / rows_skipped，重启后仍能查到。
	//
	// ::text 会规范化键序与空白 —— JSON 语义不变，消费方都是 JSON.parse。
	//
	// 用 IN ? 而不是 = ANY(?)：本仓 GORM 驱动对 = ANY(?) 的参数绑定会走 binary
	// 编解码（_int8 数组），而 []uint 无法编码成 _int8 ⇒ 报
	// 「unable to encode 0x1 into binary format for _int8」。
	// IN ? 是 GORM 原生支持的形式（内部展开成 IN ($1,$2,...)），代价是 500 个
	// 占位符 ⇒ SQL 文本不固定、执行计划不能跨批复用 —— 实测这点开销在 143 ms/批
	// 的 UPDATE 面前可忽略（0.9 ms 的 SELECT 侧也不过如此）。
	upd := s.db.Exec(
		`UPDATE device_data SET data_json = (
		   (data_json::jsonb - 'sensors')
		   || COALESCE(
		        (SELECT jsonb_build_object('str_values', agg.map)
		         FROM (SELECT jsonb_object_agg(e->>'Name', e->>'StringValue') AS map
		               FROM jsonb_array_elements((data_json::jsonb)->'sensors') e
		               WHERE COALESCE(e->>'StringValue','') <> '') agg
		         WHERE agg.map IS NOT NULL),
		        '{}'::jsonb)
		 )::text
		 WHERE id IN ? AND jsonb_typeof((data_json::jsonb)->'sensors') = 'array'`, ids,
	)
	if upd.Error != nil {
		return res, fmt.Errorf("strip sensors batch: %w", upd.Error)
	}
	res.Stripped = upd.RowsAffected

	if err := s.db.Raw(
		`SELECT COALESCE(sum(octet_length(data_json)),0) FROM device_data WHERE id IN ?`, ids,
	).Scan(&res.BytesAfter).Error; err != nil {
		return res, fmt.Errorf("measure batch after: %w", err)
	}
	return res, nil
}

// currentWatermark 读当前游标（无状态行时为 0）。
func (s *DeviceDataSensorStripper) currentWatermark() uint {
	row, err := s.loadProgress()
	if err != nil || row == nil {
		return 0
	}
	return row.WatermarkID
}

// loadProgress 读状态行；不存在返回 (nil, nil)。
func (s *DeviceDataSensorStripper) loadProgress() (*DeviceDataStripProgressRow, error) {
	var row DeviceDataStripProgressRow
	err := s.db.Where("task_key = ?", deviceDataStripTaskKey).Limit(1).Find(&row).Error
	if err != nil {
		return nil, fmt.Errorf("load %s: %w", DeviceDataStripTable, err)
	}
	if row.TaskKey == "" {
		return nil, nil
	}
	return &row, nil
}

// advance 推进游标并累加统计。
// advance 推进游标并累加统计。
//
// selected / stripped / skipped 三个数都要落库，而不是只落 stripped：
// 重启后 RunOnce 的返回值丢了，运维只能查表；若表里只有 stripped，
// 「跑完了但少处理了一批」这个事实就永远查不出来。
func (s *DeviceDataSensorStripper) advance(maxID uint, selected, stripped, skipped, before, after int64) error {
	return s.db.Model(&DeviceDataStripProgressRow{}).
		Where("task_key = ?", deviceDataStripTaskKey).
		Updates(map[string]interface{}{
			"watermark_id":  maxID,
			"rows_selected": gorm.Expr("rows_selected + ?", selected),
			"rows_stripped": gorm.Expr("rows_stripped + ?", stripped),
			"rows_skipped":  gorm.Expr("rows_skipped + ?", skipped),
			"bytes_before":  gorm.Expr("bytes_before + ?", before),
			"bytes_after":   gorm.Expr("bytes_after + ?", after),
		}).Error
}

// ResetProgress 清空状态（重跑从 id=0 开始）。测试与运维手动重置用。
func (s *DeviceDataSensorStripper) ResetProgress() error {
	return s.db.Where("task_key = ?", deviceDataStripTaskKey).
		Delete(&DeviceDataStripProgressRow{}).Error
}

// ==================== 环境变量（与 backfill 同一套「参数不是开关」口径）====================
//
// EHOME_DEVICE_DATA_STRIP_ENABLED 默认 **true**：剥离是 task-21 的既定交付，
// 默认就该跑；设成 false 只是给运维一个「这台机器先别动历史数据」的应急阀
// （例如磁盘告急、要先腾挪空间时）。
//
// 注意：它**不是**遥测开关（L1/L2 那三个已按用户要求真删）。这是运维闸门，
// 语义与 EHOME_BACKFILL_ENABLED 一致。
const (
	// EnvDeviceDataStripEnabled 运维闸门，默认 true。
	EnvDeviceDataStripEnabled = "EHOME_DEVICE_DATA_STRIP_ENABLED"
	// EnvDeviceDataStripBatch 每批行数覆盖，默认 defaultStripBatchSize。
	EnvDeviceDataStripBatch = "EHOME_DEVICE_DATA_STRIP_BATCH"
)

// StripConfig 是本任务的运行参数。
type StripConfig struct {
	Enabled   bool
	BatchSize int
	Warnings  []string
}

// StripConfigFromEnv 从环境变量解析参数（非法值回落默认 + 留告警，不静默）。
func StripConfigFromEnv() StripConfig {
	cfg := StripConfig{Enabled: true, BatchSize: defaultStripBatchSize}
	if v, ok := os.LookupEnv(EnvDeviceDataStripEnabled); ok {
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "1", "true", "yes", "on":
			cfg.Enabled = true
		case "0", "false", "no", "off":
			cfg.Enabled = false
		default:
			cfg.Warnings = append(cfg.Warnings,
				"忽略非法的 "+EnvDeviceDataStripEnabled+"="+v+"（按 true 处理）")
		}
	}
	if v, ok := os.LookupEnv(EnvDeviceDataStripBatch); ok {
		n, err := strconv.Atoi(v)
		switch {
		case err != nil:
			cfg.Warnings = append(cfg.Warnings,
				"忽略非法的 "+EnvDeviceDataStripBatch+"="+v+"（按默认 "+strconv.Itoa(defaultStripBatchSize)+" 处理）")
		case n <= 0:
			cfg.Warnings = append(cfg.Warnings,
				"忽略非正的 "+EnvDeviceDataStripBatch+"="+v+"（按默认 "+strconv.Itoa(defaultStripBatchSize)+" 处理）")
		case n > 20000:
			// 上界：一批要在单个事务里持有 500 行的行锁并写等量 WAL；20000 行
			// 在 4 GB 内存的 PG 上会把 WAL 与锁持有时间放大一个量级，
			// 而剥离本来就是可中断的后台任务，没有"一次做多"的收益。
			cfg.Warnings = append(cfg.Warnings,
				"钳制过大的 "+EnvDeviceDataStripBatch+"="+v+" 到 20000")
			cfg.BatchSize = 20000
		default:
			cfg.BatchSize = n
		}
	}
	return cfg
}

// ==================== 运行时护栏：写侧还在写 sensors 就别剥离 ====================
//
// task-21 依赖 task-19（写侧停写 sensors）。两条任务的发布顺序在生产上**无法保证**
// —— 灰度时可能先把本任务的镜像推上去、写侧的镜像还在旧版本。那种情况下剥离跑一遍，
// 新数据又把 sensors 写回来 ⇒ 下轮启动再剥离，形成「永远跑不完、永远在写 WAL」的循环。
//
// 所以不靠发布顺序，靠**运行时自证**：看最近写入的行还有没有 sensors。
// 有 ⇒ 写侧仍是旧版本，本轮跳过（下轮启动再判断，那时写侧大概率已经更新）。
// 这是把「部署顺序」这个外部约定，变成「代码自己观察到的状态」—— 前者会被人忘记，
// 后者不会。
const (
	// stripSensorParserShapeMarker 是 sensor_parser 写进 device_data 的**独有**键。
	//
	// 用它而不是「随便看最近几行」：device_data 里混着两种形状，
	//   · db_persist（device_id=0）  ：{"raw":...}            —— 从来就没有 sensors
	//   · sensor_parser（真实 device）：{"raw_hex":...,"sensors":[...]} —— 本次要剥离的
	// 若按「最近 N 行里有没有 sensors」判，会同时踩两个坑：
	//   1. N 内只要有一行历史行带 sensors（刚部署时必然如此）就永远判成"写侧还没升级"

	//      ⇒ 剥离永远不跑；
	//   2. db_persist 的行本来就没有 sensors，混进来会稀释判据。
	// 只认 raw_hex 形状，问题 2 消失；只认**最新一行**，问题 1 消失（新的写侧一旦落库，
	// 最新一行就是新形状）。
	stripSensorParserShapeMarker = `"raw_hex"`
)

// writePathStillWritesSensors 报告**写侧当前是否仍在 data_json 里写 sensors**。
//
// 判据：取最新的一行 sensor_parser 形状（含 raw_hex）的数据。
//
//	· 它带 sensors      ⇒ 写侧仍是旧版本（task-19 未生效）⇒ 返回 true；
//	· 它不带 sensors    ⇒ 写侧已是新版本 ⇒ 返回 false；
//	· 一行 sensor_parser 形状都没有 ⇒ 没有可剥离的对象 ⇒ 返回 true（跳过）。
//
// 为什么只看**最新一行**而不是最近 N 行：刚部署时最近 N 行必然混着历史行（都带
// sensors），按"N 内有没有"判会把"写侧已升级"误判成"还没升级"，剥离永远不跑。
// 而"最新一行"恰好就是写侧当前产物 —— 这正是要问的问题。
//
// 返回 (_, err) ⇒ 探测本身失败；调用方按「不要剥离」处理，因为剥离不可逆。
func (s *DeviceDataSensorStripper) writePathStillWritesSensors() (bool, error) {
	var latest []string
	if err := s.db.Raw(
		`SELECT data_json FROM device_data WHERE data_json LIKE ? ORDER BY id DESC LIMIT 1`,
		"%"+stripSensorParserShapeMarker+"%",
	).Scan(&latest).Error; err != nil {
		return false, fmt.Errorf("probe write path: %w", err)
	}
	if len(latest) == 0 {
		// 没有任何 sensor_parser 形状的行 ⇒ 没有东西可剥（新装库 / 只有 db_persist 行）。
		return true, nil
	}
	return strings.Contains(latest[0], `"sensors"`), nil
}

// RunIfSafe 是给 main.go 用的入口：先自证写侧已停写，再跑剥离。
//
// 为什么不把这段逻辑塞进 RunOnce：RunOnce 是**纯执行**（给定可以剥离，就剥离），
// 而「现在该不该剥离」是**部署策略**。混在一起会让单测必须伪造写入侧状态，
// 也会让将来「运维强制剥离」的入口没法复用 RunOnce。
func (s *DeviceDataSensorStripper) RunIfSafe(ctx context.Context) (StripReport, error) {
	var rep StripReport
	if s.db == nil {
		return rep, fmt.Errorf("datalifecycle: device_data strip requires a db")
	}
	if !isPostgres(s.db) {
		rep.Skipped = true
		rep.Done = true
		return rep, nil
	}
	still, err := s.writePathStillWritesSensors()
	if err != nil {
		// 探测失败 ⇒ 不动。剥离不可逆，信息不足时不冒险。
		return rep, err
	}
	if still {
		slog.Warn("datalifecycle: device_data sensors strip SKIPPED — 最近写入的行仍含 \"sensors\"，"+
			"说明写侧 task-19 未生效；现在剥离会被新数据冲掉并造成重复重写",
			"probe_marker", stripSensorParserShapeMarker)
		rep.Skipped = true
		return rep, nil
	}
	return s.RunOnce(ctx)
}

// Vacuum 对 device_data 及其所有分区执行 plain VACUUM（在线，不阻塞读写）。
//
// **不**做 VACUUM FULL：那需要 ACCESS EXCLUSIVE 锁 + 约等于表大小的临时空间，
// 必须在维护窗口执行，属于运维动作。本函数只做「标记空间可重用」，
// 效果是**阻止表继续增长**（新写入复用空闲页），而不是立刻缩小文件。
//
// 必须在事务外调用（PG 不允许 VACUUM 在事务块内）。
func (s *DeviceDataSensorStripper) Vacuum(ctx context.Context) error {
	if s.db == nil || !isPostgres(s.db) {
		return nil
	}
	names, err := deviceDataPartitions(s.db)
	if err != nil {
		return err
	}
	if len(names) == 0 {
		names = []string{"device_data"}
	}
	for _, n := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		// ANALYZE 一起做：剥离后行宽变化显著，统计信息不更新会让后续
		// 查询计划偏保守（回填/图表查询都会受影响）。
		if err := s.db.Exec("VACUUM (ANALYZE) " + quoteIdent(n)).Error; err != nil {
			return fmt.Errorf("vacuum %s: %w", n, err)
		}
		s.lastVacuumPartitions = append(s.lastVacuumPartitions, n)
	}
	slog.Info("datalifecycle: device_data vacuumed", "partitions", s.lastVacuumPartitions)
	return nil
}

// deviceDataPartitions 列出 device_data 的所有分区（非分区表返回空）。
func deviceDataPartitions(db *gorm.DB) ([]string, error) {
	var names []string
	err := db.Raw(
		`SELECT c.relname FROM pg_class c
		 JOIN pg_inherits i ON i.inhrelid = c.oid
		 WHERE i.inhparent = 'device_data'::regclass
		 ORDER BY c.relname`,
	).Scan(&names).Error
	return names, err
}
