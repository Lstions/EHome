package datalifecycle

// rollup_backfill.go — L3 rollup 的**历史回填**（task-22）。
//
// ============ 要解决什么 ============
//
// 帧表已完整覆盖历史，但读路径分派是：
//
//	≤1h      → 帧表解码        ✅ 已验证
//	1h ~ 6h  → unified_data SQL
//	>6h      → rollup（开关关）→ 回落 unified_data SQL
//
// 只要 >1h 的窗口还读 unified_data，老的 12 GB 分区就删不掉（删了 7d/30d 图表会缺数据）。
// 本文件把 rollup 的历史补齐，让长窗改读 rollup。
//
// ============ 与帧表回填（internal/backfill）的关系 ============
//
// 同一个范式：状态表 + 断点续跑 + 后台不阻塞 + 进度可见。但**状态表不同**：
//
//	· 帧表回填的状态单位是「月份分区」+ 复合游标 (ts, device_id) —— 因为帧键要按
//	  (ts, device) 排序才能不重不漏地分批；
//	· rollup 回填的状态单位是「(设备, 日)」—— 因为写的是 (device, bucket) 主键的行，
//	  天然按天可切分，且覆盖元数据表（rollup_coverage）本身就是最自然的状态表。
//
// **不共用一张状态表**：`telemetry_backfill_progress` 的语义是"某个时序表的分区回填到哪"，
// 塞进 rollup 的 (device, day) 会让那张表的每一行有两种不同的含义，判断"回填完了吗"
// 要按 table_name 分支 —— 那种设计半年后没人敢动。
//
// ============ 为什么按天而不是按月 ============
//
// 1. 覆盖元数据的粒度就是天（读路径按天判），按天回填能**天然断点续跑**：
//	   崩在哪天就重跑哪天，不需要额外的游标；
// 2. 单天单设备的聚合量可控（1440 分钟 × ~29 字段），内存不会爆；
// 3. 进度百分比对运维可解释（"36 天 × 5 设备 = 180 个单元"）。
//
// ============ ⚠ 关于 Last 与 Avg 的关键区分（正确性基础）============
//
// live 的 RollupCollector 喂的是**全部解析帧**（L1 跳过入库的也喂，见
// consumers_heavy.go 的 telemetryRollupSink 注释）；而本回填只能从 unified_data
// 聚合（L1 收窄后的行）。所以：
//
//	· **Avg 两者会不同**（分母不同：live 是全采样，回填是已落库行）
//	  ⇒ 回填**不能覆盖** live 已写好的行，必须 ON CONFLICT DO NOTHING；
//	· **Last 两者一致**（L1 只跳过"值未变"的行 ⇒ 最后一个已落库值 == 最后一个
//	  真实采样值）⇒ 这正是 D1「桶内最后值」口径成立的前提。
//
// **「Last 一致、Avg 不一致」是整个回填正确性的基础。** 后人若想"都是聚合值，
// 为什么不能覆盖"，答案就在这里：覆盖会把 live 那份更准的 Avg 换成回填这份偏的。

import (
	"context"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"ehome/backend/pkg/logger"
)

const (
	// rollupBackfillBucketSeconds 是 rollup 的桶宽（1 分钟，与 RollupBucket 一致）。
	rollupBackfillBucketSeconds = 60

	// defaultRollupBackfillBatchSeconds 是单次聚合的时间跨度（默认 1 小时）。
	//
	// 为什么不再大：单天单设备的日志行数可达 40k（29 字段 × 1440 分钟），
	// 一次拉回内存做二次聚合没必要；小时级既能压在几万行，又能让中断粒度足够细。
	defaultRollupBackfillBatchSeconds = 3600
	// defaultRollupBackfillBatchSleep 是**批次间**的休眠（限速，避免与剥离任务抢 IO）。
	//
	// 生产回填期间还有别的重 IO 任务（device_data sensors 剥离）在跑，
	// 把这个值调大即可降速。50ms/小时批次 ≈ 36 天 × 24 小时 × 50ms ≈ 43 秒额外开销，
	// 相对整体回填时间可忽略。
	defaultRollupBackfillBatchSleep = 50 * time.Millisecond
)

// RollupBackfillConfig 是回填的运行配置。
type RollupBackfillConfig struct {
	// Enabled 是否启用（默认 true；生产可用环境变量关掉）。
	Enabled bool
	// BatchSeconds 单次聚合的时间跨度（秒）。<=0 用默认 3600。
	BatchSeconds int
	// BatchSleep 批次间休眠。<=0 用默认 50ms。
	BatchSleep time.Duration
}

// RollupBackfillProgress 是进度快照（给 /api/v1/system/rollup-backfill）。
type RollupBackfillProgress struct {
	Enabled bool   `json:"enabled"`
	State   string `json:"state"`
	// UnitsTotal / UnitsDone 的 unit = (设备, 日)。
	UnitsTotal int `json:"units_total"`
	UnitsDone  int `json:"units_done"`
	// UnitsSkipped 已回填过而跳过的单元数（断点续跑的证据）。
	UnitsSkipped int64 `json:"units_skipped"`
	// BucketsWritten 已写入的分钟桶总数（= rollup 行数）。
	BucketsWritten int64    `json:"buckets_written"`
	CurrentDay     string   `json:"current_day,omitempty"`
	CurrentDevice  uint     `json:"current_device,omitempty"`
	Errors         []string `json:"errors,omitempty"`
}

// rollupBackfillRunner 是历史回填的执行器。
type rollupBackfillRunner struct {
	db  *gorm.DB
	cfg RollupBackfillConfig

	mu       sync.Mutex
	progress RollupBackfillProgress

	// unifiedPresent 缓存「unified_data 表是否存在」的探测结果（nil = 尚未探测）。
	// DROP 之后它是稳定的结构状态，不需要每次判源都问一遍；用 *bool 而不是 bool
	// 是为了区分「还没探测」与「探测过，不存在」。
	unifiedMu      sync.Mutex
	unifiedPresent *bool
	// errors 上限，避免长期跑出巨量字符串。
	errorCap int
}

// NewRollupBackfillRunner 构造回填执行器。
func NewRollupBackfillRunner(db *gorm.DB, cfg RollupBackfillConfig) *rollupBackfillRunner {
	return &rollupBackfillRunner{
		db:       db,
		cfg:      cfg,
		errorCap: 16,
		progress: RollupBackfillProgress{Enabled: cfg.Enabled, State: BackfillPending},
	}
}

// Progress 返回当前进度快照（并发安全）。
func (r *rollupBackfillRunner) Progress() RollupBackfillProgress {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := r.progress
	if len(r.progress.Errors) > 0 {
		out.Errors = append([]string(nil), r.progress.Errors...)
	}
	return out
}

func (r *rollupBackfillRunner) setState(state string) {
	r.mu.Lock()
	r.progress.State = state
	r.mu.Unlock()
}

func (r *rollupBackfillRunner) addError(msg string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.progress.Errors) < r.errorCap {
		r.progress.Errors = append(r.progress.Errors, msg)
	}
}

// rollupBackfillUnit 是一个回填单元：一台设备的一天。
type rollupBackfillUnit struct {
	DeviceID uint
	Day      string
}

// RollupBackfillRunOnce 执行一轮历史回填（幂等，可重复调用/中断续跑）。
//
// 返回 error 只在**整体不可执行**时（表不存在、DB 不可用）；单元级失败只记录、
// 不中断整轮 —— 一天的聚合失败不该让其余 179 个单元白等。
func RollupBackfillRunOnce(ctx context.Context, db *gorm.DB, cfg RollupBackfillConfig) error {
	r := NewRollupBackfillRunner(db, cfg)
	return r.RunOnce(ctx)
}

// RunOnce 是实例方法版本（main.go 持有实例以便读进度）。
func (r *rollupBackfillRunner) RunOnce(ctx context.Context) error {
	r.setState(BackfillRunning)
	defer func() {
		if r.Progress().State == BackfillRunning {
			r.setState(BackfillDone)
		}
	}()

	if r.db == nil {
		return fmt.Errorf("rollup backfill: nil db")
	}
	if !IsTablePartitioned(r.db, TelemetryRollupTable) && !r.db.Migrator().HasTable(TelemetryRollupTable) {
		// 表不存在：派生数据的目标表都没有，谈不上回填。这是**可跳过**的形态，
		// 不是错误（精简部署 / 迁移未跑）。
		r.setState(BackfillDone)
		return nil
	}

	devices, minTS, maxTS, err := r.discoverSource(ctx)
	if err != nil {
		return err
	}
	if len(devices) == 0 {
		// 源表空：没有可回填的历史。
		r.setState(BackfillDone)
		return nil
	}

	units := buildRollupBackfillUnits(devices, minTS, maxTS)
	r.mu.Lock()
	r.progress.UnitsTotal = len(units)
	r.mu.Unlock()
	logger.Infof("[rollup-backfill] 开始：%d 台设备 × %d~%d = %d 个 (设备,日) 单元",
		len(devices), minTS.UTC().Format("2006-01-02"), maxTS.UTC().Format("2006-01-02"), len(units))

	for i, u := range units {
		if err := ctx.Err(); err != nil {
			// 被取消：状态留在 running（下次续跑），不标 done。
			r.setState(BackfillRunning)
			return err
		}
		if done, err := r.unitAlreadyDone(ctx, u); err != nil {
			r.addError(fmt.Sprintf("%s/%d: coverage check: %v", u.Day, u.DeviceID, err))
		} else if done {
			r.mu.Lock()
			r.progress.UnitsDone++
			r.progress.UnitsSkipped++
			r.mu.Unlock()
			continue
		}

		r.mu.Lock()
		r.progress.CurrentDay = u.Day
		r.progress.CurrentDevice = u.DeviceID
		r.mu.Unlock()

		buckets, err := r.backfillUnit(ctx, u)
		if err != nil {
			r.addError(fmt.Sprintf("%s/%d: %v", u.Day, u.DeviceID, err))
			// 标 partial：让读路径知道这一天没跑完（而不是"没数据"）。
			if merr := MarkRollupCoverage(r.db, u.DeviceID, u.Day, 0, RollupCoveragePartial); merr != nil {
				r.addError(fmt.Sprintf("%s/%d: mark partial: %v", u.Day, u.DeviceID, merr))
			}
			continue
		}

		r.mu.Lock()
		r.progress.UnitsDone++
		r.progress.BucketsWritten += int64(buckets)
		r.mu.Unlock()

		if (i+1)%10 == 0 || i+1 == len(units) {
			p := r.Progress()
			logger.Infof("[rollup-backfill] 进度 %d/%d 单元，已写 %d 个分钟桶，跳过 %d",
				p.UnitsDone, p.UnitsTotal, p.BucketsWritten, p.UnitsSkipped)
		}

		if r.cfg.BatchSleep > 0 {
			select {
			case <-time.After(r.cfg.BatchSleep):
			case <-ctx.Done():
				r.setState(BackfillRunning)
				return ctx.Err()
			}
		}
	}
	r.setState(BackfillDone)
	p := r.Progress()
	logger.Infof("[rollup-backfill] 完成：%d/%d 单元，写入 %d 个分钟桶，跳过 %d",
		p.UnitsDone, p.UnitsTotal, p.BucketsWritten, p.UnitsSkipped)
	return nil
}

// discoverSource 取「要回填的设备」与源数据的**日历范围**。
//
// ============ B 修复（2026-11-07 P0）：枚举必须脱钩 unified_data ============
//
// 旧实现：
//
//	SELECT DISTINCT device_id FROM unified_data
//	SELECT min(timestamp), max(timestamp) FROM unified_data
//
// 于是「要回填哪些天」由 unified_data 里有什么决定。**停写之后 max(timestamp)
// 冻结在停写那一刻**，之后的日子永远不会出现在 units 里，于是：
//
//	coverage 停止推进 -> RollupWindowCovered=false -> 读路径回落
//	-> >6h 的窗口走不了帧表(frameDecodeMaxWindow=6h) -> 只能读 unified_data
//	-> 最新行停在停写时刻 -> **图表画到那里就断，HTTP 200，无告警**
//
// 而且这个失效**现在就存在**，只是被"满的 unified_data"掩盖：回填只在进程启动时
// 跑一次（main.go 无 ticker），服务连续运行超过 2 天，coverage 就已经停止推进。
//
// 新实现把三件事**分别**取，各自都不依赖 unified_data 必须非空：
//
//	① 设备  <- edge_devices（元数据表），并入数据表里出现过的设备（覆盖已软删的）
//	② 起点  <- 各数据源最早时间取最小；都为空则 now - sourceWindowFallback
//	③ 终点  <- **now**（buildRollupBackfillUnits 内部排除今天 => 实际推进到昨天）
//
// 这样即使 unified_data 一行都没有，coverage 仍能按天推进。
func (r *rollupBackfillRunner) discoverSource(ctx context.Context) ([]uint, time.Time, time.Time, error) {
	devices, err := r.discoverDevices(ctx)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	if len(devices) == 0 {
		return nil, time.Time{}, time.Time{}, nil
	}
	start, err := r.discoverStart(ctx)
	if err != nil {
		return nil, time.Time{}, time.Time{}, err
	}
	// 终点取 now：今天由 buildRollupBackfillUnits 排除，因此实际最后一个单元是「昨天」。
	// **不能**用 max(timestamp) —— 那正是本修复要脱钩的东西。
	return devices, start, rollupCoverageNow().UTC(), nil
}

// discoverDevices 取要回填的设备集合。
//
// 以 edge_devices 为准（元数据表，与数据写入无关），并入三张数据表里出现过的设备 ——
// 后者覆盖「设备已软删但历史数据还在」的情形（图表仍可能被查看）。
// 单张表不存在时忽略该表（精简部署 / 迁移未跑），不让整个回填失败。
func (r *rollupBackfillRunner) discoverDevices(ctx context.Context) ([]uint, error) {
	seen := map[uint]struct{}{}
	add := func(ids []uint) {
		for _, id := range ids {
			if id != 0 {
				seen[id] = struct{}{}
			}
		}
	}
	var devIDs []uint
	if err := r.db.WithContext(ctx).Raw("SELECT id FROM edge_devices").Scan(&devIDs).Error; err != nil {
		return nil, fmt.Errorf("rollup backfill: list edge devices: %w", err)
	}
	add(devIDs)
	// ============ 表不存在就不查（2026-11-08 DROP 后修正）============
	//
	// 原实现是「查了再忽略错误」（if err == nil { add(ids) }）。那有两个问题：
	//
	//	① 日志噪音：unified_data 已 DROP，每次启动都会打一条 42P01，而它是**预期内**的
	//	   —— 和真故障混在同一个 ERROR 级别里，久了就没人看 ERROR 了；
	//	② 语义错位：「能预知的跳过」被写成了「错误处理」。判据是：
	//	   如果一个失败**必然**发生且**不影响结果**，它就不是错误，是不该发起的查询。
	//
	// 现在对每个源先判表存在性（HasTable 走 pg_class，微秒级，启动时只跑一次）。
	//
	// ⚠ unified_data 这一项在 DROP 之后恒为假，但**不删**：迁移期的历史库、以及
	//   别人本地的旧库仍然需要它 —— 删了会让那些环境少枚举一批设备（静默漏回填）。
	for _, q := range []struct{ table, sql string }{
		{"unified_data", "SELECT DISTINCT device_id FROM unified_data"},
		{TelemetryFrameTable, "SELECT DISTINCT device_id FROM telemetry_frame"},
		{TelemetryRollupTable, "SELECT DISTINCT device_id FROM telemetry_rollup_1m"},
	} {
		if !r.db.Migrator().HasTable(q.table) {
			continue
		}
		var ids []uint
		if err := r.db.WithContext(ctx).Raw(q.sql).Scan(&ids).Error; err == nil {
			add(ids)
		}
	}
	out := make([]uint, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out, nil
}

// sourceWindowFallback 是「任何数据源都没有行」时的回填起点回退窗口。
//
// 取 45 天与 unified_data 的保留期同源：再早的数据本来就已被清理，没有回填价值。
const sourceWindowFallback = 45 * 24 * time.Hour

// discoverStart 取回填起点：各数据源最早时间的最小值；都没有则 now - sourceWindowFallback。
//
// 为什么允许回退：全新部署 / 表都很干净时，没有必要报错 —— 没有数据自然没有单元。
func (r *rollupBackfillRunner) discoverStart(ctx context.Context) (time.Time, error) {
	var earliest *time.Time
	// 同 discoverDevices：表不存在就不查（理由见那里的注释）。
	for _, q := range []struct{ table, sql string }{
		{"unified_data", "SELECT min(timestamp) AS ts FROM unified_data"},
		{TelemetryFrameTable, "SELECT min(ts) AS ts FROM telemetry_frame"},
		{TelemetryRollupTable, "SELECT min(bucket) AS ts FROM telemetry_rollup_1m"},
	} {
		if !r.db.Migrator().HasTable(q.table) {
			continue
		}
		var one struct{ TS *time.Time }
		if err := r.db.WithContext(ctx).Raw(q.sql).Scan(&one).Error; err != nil || one.TS == nil {
			continue
		}
		if earliest == nil || one.TS.Before(*earliest) {
			t := one.TS.UTC()
			earliest = &t
		}
	}
	if earliest != nil {
		return *earliest, nil
	}
	return rollupCoverageNow().UTC().Add(-sourceWindowFallback), nil
}

// buildRollupBackfillUnits 生成全部 (设备, 日) 单元（按日升序、设备升序）。
func buildRollupBackfillUnits(devices []uint, minTS, maxTS time.Time) []rollupBackfillUnit {
	startDay := time.Date(minTS.UTC().Year(), minTS.UTC().Month(), minTS.UTC().Day(), 0, 0, 0, 0, time.UTC)
	endDay := time.Date(maxTS.UTC().Year(), maxTS.UTC().Month(), maxTS.UTC().Day(), 0, 0, 0, 0, time.UTC)
	// ⚠ **排除「今天」（UTC 当天）** —— boot-api 2026-11-07 审计发现的必修项。
	//
	// 根因：`complete` 对一个**尚未结束的 UTC 日**是错误的状态。它的语义是
	// "这一天所有已发生的分钟都回填了"（真），但读路径会把它理解成
	// "这一天将来还会来的分钟也已经有了"（假）。
	//
	// 具体故障：回填在 10:00 跑过并把今天置 complete（buckets=600）。之后 live
	// collector 若漏了某几分钟（flush 失败、进程重启、部署），那几分钟在 rollup 里
	// 是空的；而重启后回填看到 "今天 complete" 就**跳过** ⇒ 那几分钟**永远不会被
	// 回填**（实测：源 18 桶 / rollup 10 桶，回填 skipped=1、buckets_written=0，
	// 而 RollupWindowCovered 仍返回 true）⇒ 图表在那段显示空洞且永远不修复。
	//
	// 修法：**今天完全交给 live collector**，回填只管已结束的日子。
	// 代价：今天的时间窗全部回落 unified_data —— 但这与 liveLag 护栏的效果一致
	// （见 RollupCoverageLiveLag），所以没有额外损失，只是不再制造"半真半假"的
	// complete 状态。
	today := RollupCoverageDay(rollupCoverageNow().UTC())
	if endDayStr := RollupCoverageDay(endDay); endDayStr >= today {
		endDay = endDay.AddDate(0, 0, -1)
		if endDay.Before(startDay) {
			return nil
		}
	}
	var out []rollupBackfillUnit
	for d := startDay; !d.After(endDay); d = d.AddDate(0, 0, 1) {
		day := RollupCoverageDay(d)
		for _, dev := range devices {
			out = append(out, rollupBackfillUnit{DeviceID: dev, Day: day})
		}
	}
	return out
}

// unitAlreadyDone 判断该单元是否已回填过（断点续跑的唯一依据）。
//
// **只认 complete / missing**：partial 表示上次没跑完，必须重跑。
func (r *rollupBackfillRunner) unitAlreadyDone(ctx context.Context, u rollupBackfillUnit) (bool, error) {
	rows, err := ReadRollupCoverage(r.db.WithContext(ctx), u.DeviceID, u.Day, u.Day)
	if err != nil {
		return false, err
	}
	row, ok := rows[u.Day]
	if !ok {
		return false, nil
	}
	return RollupCoverageVerified(row.State), nil
}

// rollupBackfillAggRow 是一次聚合查询的结果行（一个「设备 × 分钟 × 字段」）。
type rollupBackfillAggRow struct {
	DeviceID   uint
	Bucket     int64 // epoch 秒（分钟对齐）
	SensorName string
	AvgV       float64
	MinV       float64
	MaxV       float64
	CntV       int64
	LastV      float64
}

// isSqliteDialect 判断是否 SQLite（回填的聚合 SQL 需要方言分支）。
func isSqliteDialect(db *gorm.DB) bool {
	return db != nil && db.Dialector != nil && db.Dialector.Name() == "sqlite"
}

// rollupBucketEpochExpr 返回「时间戳 -> 分钟对齐的 epoch 秒」的方言表达式。
//
// 为什么统一成 epoch 整数而不是让 SQL 返回时间类型：两方言的时间类型不同
// （PG 返回 timestamptz，SQLite 返回字符串/整数），扫进同一个 Go 字段会踩
// 「unsupported Scan」或时区解析差异。epoch 整数是唯一逐位一致的形式，
// 由 Go 侧统一转成 time.Time —— 与 historyBucketExpr 的选择理由相同。
func rollupBucketEpochExpr(db *gorm.DB) string {
	if isSqliteDialect(db) {
		return "(CAST(strftime('%s', \"timestamp\") AS INTEGER) / 60) * 60"
	}
	return "CAST(floor(extract(epoch FROM \"timestamp\") / 60) * 60 AS BIGINT)"
}

// aggregateBatch 聚合 [from, to) 内所有设备的分钟级统计（含 Last）。
//
// ============ 为什么一条 SQL 同时算 avg/min/max/count/last ============
//
// 直觉写法是两条查询（聚合一条、最后值一条）。这里用**窗口函数一次算完**，因为：
//
// · 回填要扫 7,200 万行，每多一趟扫描就多一份 IO —— 而回填正与 device_data
//
//	sensors 剥离任务抢 IO（Lead 明确要求限速）；
//
// · 两条查询要按 (device, bucket, sensor) 做内存 JOIN，多一层出错空间。
//
// ROW_NUMBER() OVER (PARTITION BY ... ORDER BY timestamp DESC) = 1 取每组的最后一行；
// 其余聚合用同分区窗口。PG 与 SQLite(>=3.25) 都支持 —— 本仓已用同款手法
// （见 historyAvgSQLFor 的 SQLite 分支）。
//
// ⚠ 绝不能退化成 MAX(timestamp) + 裸 value 同 SELECT：SQL 标准不保证两者来自
// 同一行（SQLite 尤其不可靠），会静默取到**错误字段的值**。
func (r *rollupBackfillRunner) aggregateBatch(ctx context.Context, deviceID uint, from, to time.Time) ([]rollupBackfillAggRow, error) {
	b := rollupBucketEpochExpr(r.db)
	part := "PARTITION BY device_id, " + b + ", sensor_name"
	// 分区表达式重复写（而不是用 WINDOW 子句）：WINDOW 子句 SQLite 3.28+ 才支持，
	// 重复写没有版本门槛，代价只是 SQL 长一点。
	sqlText := "SELECT device_id, bucket, sensor_name, avg_v, min_v, max_v, cnt_v, last_v FROM (" +
		"SELECT device_id, " + b + " AS bucket, sensor_name, " +
		"AVG(value) OVER (" + part + ") AS avg_v, " +
		"MIN(value) OVER (" + part + ") AS min_v, " +
		"MAX(value) OVER (" + part + ") AS max_v, " +
		"COUNT(*) OVER (" + part + ") AS cnt_v, " +
		"ROW_NUMBER() OVER (" + part + " ORDER BY \"timestamp\" DESC) AS rn, " +
		"value AS last_v " +
		"FROM unified_data " +
		"WHERE device_id = ? AND \"timestamp\" >= ? AND \"timestamp\" < ?" +
		") t WHERE t.rn = 1"
	var rows []rollupBackfillAggRow
	if err := r.db.WithContext(ctx).Raw(sqlText, deviceID, from, to).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("aggregate device %d [%s, %s): %w", deviceID, from.Format(time.RFC3339), to.Format(time.RFC3339), err)
	}
	return rows, nil
}

// rollupPendingRow 是一条待写入的 rollup 行（bucket epoch + 已编码 payload）。
type rollupPendingRow struct {
	bucket  int64
	payload []byte
}

// backfillUnit 回填一个单元（一台设备的一天），返回写入的分钟桶数。
//
// ============ 执行顺序（为什么不能变）============
//
//  1. **先把这一天全部聚合完、数出 buckets**（写之前一次性数完）；
//  2. 再写 rollup 行（ON CONFLICT DO NOTHING，不覆盖 live 行）；
//  3. 最后写覆盖元数据 complete/missing。
//
// 第 1 步必须在写之前：若 buckets 是边写边数，它与真实写入量**天然相等**，
// 中途断掉时缺的半天永远看不出来 ⇒ 判据失去鉴别力。
// buckets 必须是**独立于写入过程的观测值** —— 一句话能消掉的风险，不必加两列。
//
// 第 3 步必须在写完之后的：先写 complete 再写数据，崩在中间就是"宣称完整但其实没有"，
// 而那是本任务最坏的故障形态。反过来的窗口（数据齐但标 partial）只会多回落一次。
func (r *rollupBackfillRunner) backfillUnit(ctx context.Context, u rollupBackfillUnit) (int, error) {
	dayStart, err := time.ParseInLocation("2006-01-02", u.Day, time.UTC)
	if err != nil {
		return 0, fmt.Errorf("parse day %q: %w", u.Day, err)
	}
	dayEnd := dayStart.AddDate(0, 0, 1)

	batch := normalizeRollupBatchSeconds(r.cfg.BatchSeconds)

	// ── 第 0 步：**按日期选源**（P0 残留缺口修复，2026-11-07；2026-11-08 补表缺失终态）──
	//
	// 分两层判据，**顺序不能换**：
	//
	//	① unified_data 表是否存在（结构性判据，探测一次后缓存）
	//	     不存在 => 一律帧表源。这是 DROP 之后的**正常终态**，不是错误。
	//	     存在   => 进第 ② 层（迁移期两种源都可能有数据）
	//	② 该日 unified_data 是否有行 => 有则用 unified，无则用帧表
	//
	// 为什么 ① 不能省成「查一下，报错就当没有」：那会把「表没了」（正常）与
	// 「DB 挂了」（异常）混为一谈，后者会让回填一路标 missing —— 而 missing 是**粘性的**，
	// 等于把一次连接故障永久写成「这天没有数据」。详见 unifiedDataTablePresent 的注释。
	//
	// 第 ② 层的判据是**日期**而不是「本次查询返回 0 行」：按结果判会让同一天可能被两种
	// 口径各写一次（查询抖动/边界差异），来源不确定、不可复现。详见 rollup_backfill_frames.go。
	source := RollupSourceFrames
	if r.unifiedDataTablePresent() {
		hasUnified, err := r.dayHasUnifiedRows(ctx, u.DeviceID, dayStart, dayEnd)
		if err != nil {
			// 表存在却查不动 => 真实故障（连接/权限/锁），必须让回填失败并报警。
			return 0, fmt.Errorf("day source probe: %w", err)
		}
		if hasUnified {
			source = RollupSourceUnified
		}
	}
	if source == RollupSourceFrames {
		if frameDecoder == nil {
			// 字典不可用（帧表未迁移/精简部署）：无法解帧，如实标 partial 让读路径回落，
			// **不能**标 missing —— missing 是粘性的，会把"暂时解不了"变成"永久没数据"。
			return 0, fmt.Errorf("frames source selected but slot dictionary unavailable")
		}
	}

	// ── 第 1 步：聚合全部批次，按 (bucket) 累积字段统计 ──
	// key = bucket epoch；value = 字段名 -> 统计量
	byBucket := make(map[int64]map[string]*RollupFieldStat, 1440)
	for t := dayStart; t.Before(dayEnd); t = t.Add(batch) {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		to := t.Add(batch)
		if to.After(dayEnd) {
			to = dayEnd
		}
		var rows []rollupBackfillAggRow
		if source == RollupSourceFrames {
			rows, err = r.aggregateBatchFromFrames(ctx, u.DeviceID, t, to)
		} else {
			rows, err = r.aggregateBatch(ctx, u.DeviceID, t, to)
		}
		if err != nil {
			return 0, err
		}
		for i := range rows {
			bg := byBucket[rows[i].Bucket]
			if bg == nil {
				bg = make(map[string]*RollupFieldStat, 32)
				byBucket[rows[i].Bucket] = bg
			}
			bg[rows[i].SensorName] = &RollupFieldStat{
				Avg:   rows[i].AvgV,
				Min:   rows[i].MinV,
				Max:   rows[i].MaxV,
				Last:  rows[i].LastV,
				Count: uint32(rows[i].CntV),
			}
		}
	}

	buckets := len(byBucket)
	if buckets == 0 {
		// 源数据这一天本来就没有（设备离线/未上线）：写 missing，表示**已验证的结论**。
		// 这与"行不存在"（从没验证过）是两件事，见 RollupCoverageRow 的注释。
		// 记来源：missing 也必须记 —— 否则将来无法区分「unified 源下确实没有」
		// 与「帧源下解不出来」，而这两者的排查方向完全不同。
		return 0, MarkRollupCoverageWithSource(r.db, u.DeviceID, u.Day, 0, RollupCoverageMissing, source)
	}

	// ── 第 2 步：写 rollup 行 ──
	// buckets 已数完，此处即使崩溃也不影响观测值的正确性（只会让这天保持 partial）。
	batchSize := 200
	buf := make([]rollupPendingRow, 0, batchSize)
	flush := func() error {
		if len(buf) == 0 {
			return nil
		}
		if err := r.insertRollupRowsNoClobber(ctx, u.DeviceID, buf); err != nil {
			return err
		}
		buf = buf[:0]
		return nil
	}
	// 桶按时间升序处理（地图遍历无序，但写库顺序不影响结果；排序只是便于排障与重放）。
	keys := make([]int64, 0, len(byBucket))
	for k := range byBucket {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	for _, k := range keys {
		payload, err := EncodeRollupPayloadV1(byBucket[k])
		if err != nil {
			// 单桶编码失败：跳过该桶（派生数据），但要记进错误。
			return 0, fmt.Errorf("encode bucket %s: %w", time.Unix(k, 0).UTC().Format(time.RFC3339), err)
		}
		buf = append(buf, rollupPendingRow{bucket: k, payload: payload})
		if len(buf) >= batchSize {
			if err := flush(); err != nil {
				return 0, err
			}
		}
	}
	if err := flush(); err != nil {
		return 0, err
	}

	// ── 第 3 步：写覆盖元数据 ──
	if err := MarkRollupCoverageWithSource(r.db, u.DeviceID, u.Day, buckets, RollupCoverageComplete, source); err != nil {
		return 0, err
	}
	return buckets, nil
}

// insertRollupRowsNoClobber 批量写入 rollup 行，**不覆盖已存在的行**。
//
// ============ 为什么是 DO NOTHING 而不是 DO UPDATE（正确性基础）============
//
// live 的 RollupCollector 喂的是**全部解析帧**（L1 跳过入库的也喂）；本回填只能从
// unified_data 聚合（L1 收窄后的行）。所以同一分钟的同一字段：
//
//	· **Avg 两者不同**（分母不同：live 是全采样，回填是已落库行）
//	  ⇒ 覆盖会把 live 那份更准的 Avg 换成回填这份**偏的**；
//	· **Last 两者一致**（L1 只跳过"值未变"的行 ⇒ 最后一个已落库值 == 最后一个
//	  真实采样值）⇒ 这正是 D1「桶内最后值」口径成立的前提。
//
// 「Last 一致、Avg 不一致」是整个回填正确性的基础。后人若想"都是聚合值，为什么
// 不能覆盖"，答案就在这里。
func (r *rollupBackfillRunner) insertRollupRowsNoClobber(ctx context.Context, deviceID uint, buf []rollupPendingRow) error {
	if len(buf) == 0 {
		return nil
	}
	// 用一条多值 INSERT：单值循环在 40k 行/天时会有 40k 次往返。
	now := rollupCoverageNow().UTC()
	args := make([]any, 0, len(buf)*4)
	placeholders := make([]string, 0, len(buf))
	for i := range buf {
		placeholders = append(placeholders, "(?,?,?,?)")
		args = append(args, deviceID, time.Unix(buf[i].bucket, 0).UTC(), buf[i].payload, now)
	}
	q := "INSERT INTO " + TelemetryRollupTable +
		" (device_id, bucket, payload, updated_at) VALUES " + strings.Join(placeholders, ",") +
		" ON CONFLICT (device_id, bucket) DO NOTHING"
	if err := r.db.WithContext(ctx).Exec(q, args...).Error; err != nil {
		return fmt.Errorf("insert rollup rows (device %d, %d rows): %w", deviceID, len(buf), err)
	}
	return nil
}

// EnvRollupBackfillEnabled / EnvRollupBackfillBatchSeconds / EnvRollupBackfillSleep 是回填的环境变量。
const (
	EnvRollupBackfillEnabled      = "EHOME_TELEMETRY_ROLLUP_BACKFILL"
	EnvRollupBackfillBatchSeconds = "EHOME_TELEMETRY_ROLLUP_BACKFILL_BATCH_SECONDS"
	EnvRollupBackfillSleepMS      = "EHOME_TELEMETRY_ROLLUP_BACKFILL_SLEEP_MS"
)

// RollupBackfillConfigFromEnv 从环境变量构造配置。
//
// 默认**开启**：回填是幂等的（已 complete/missing 的单元直接跳过），重复启动无副作用；
// 而"忘了开"会让长窗永远读不到 rollup ⇒ 12 GB 老分区永远删不掉。
// 限速默认 50ms/单元，生产若与其它重 IO 任务并发可调大。
func RollupBackfillConfigFromEnv() (RollupBackfillConfig, []string) {
	cfg := RollupBackfillConfig{
		Enabled:      true,
		BatchSeconds: int(defaultRollupBackfillBatchSeconds),
		BatchSleep:   defaultRollupBackfillBatchSleep,
	}
	var warnings []string
	if raw, ok := os.LookupEnv(EnvRollupBackfillEnabled); ok {
		if v, err := strconv.ParseBool(strings.TrimSpace(raw)); err == nil {
			cfg.Enabled = v
		} else {
			warnings = append(warnings, fmt.Sprintf("%s=%q 不是合法布尔值，按 true 处理", EnvRollupBackfillEnabled, raw))
		}
	}
	if raw, ok := os.LookupEnv(EnvRollupBackfillBatchSeconds); ok {
		if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && v > 0 {
			cfg.BatchSeconds = int(normalizeRollupBatchSeconds(v) / time.Second)
		} else {
			warnings = append(warnings, fmt.Sprintf("%s=%q 不是合法正整数，按默认 %d 处理",
				EnvRollupBackfillBatchSeconds, raw, cfg.BatchSeconds))
		}
	}
	if raw, ok := os.LookupEnv(EnvRollupBackfillSleepMS); ok {
		if v, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && v >= 0 {
			cfg.BatchSleep = time.Duration(v) * time.Millisecond
		} else {
			warnings = append(warnings, fmt.Sprintf("%s=%q 不是合法非负整数，按默认 %v 处理",
				EnvRollupBackfillSleepMS, raw, cfg.BatchSleep))
		}
	}
	return cfg, warnings
}

// RunRollupBackfillFromEnv 是 main.go 的接线入口（后台 goroutine 内调用）。
func RunRollupBackfillFromEnv(ctx context.Context, db *gorm.DB) error {
	cfg, warnings := RollupBackfillConfigFromEnv()
	for _, w := range warnings {
		logger.Warnf("[rollup-backfill] %s", w)
	}
	if !cfg.Enabled {
		logger.Infof("[rollup-backfill] 已关闭（%s=false），跳过", EnvRollupBackfillEnabled)
		return nil
	}
	if err := EnsureRollupCoverageTable(db); err != nil {
		return fmt.Errorf("ensure rollup coverage table: %w", err)
	}
	return NewRollupBackfillRunner(db, cfg).RunOnce(ctx)
}

// normalizeRollupBatchSeconds 把批次跨度**向上取整到 60 秒的整数倍**。
//
// ============ 为什么必须对齐到整分钟（boot-api 2026-11-07 审计发现）============
//
// `backfillUnit` 用 `byBucket[bucket][sensor] = &RollupFieldStat{...}` **覆盖赋值**。
// 若批次跨度不是 60 的整数倍（如 EHOME_..._BATCH_SECONDS=90），同一个分钟桶会被
// 两个批次各聚合一部分，**后者覆盖前者** ⇒ 写进去的 Avg/Count 只是"半个桶"的值。
//
// 实测（batch=90s）：00:01 桶回填 cnt=3，而整分钟真值是 cnt=6；avg 0.040 vs 0.035。
// 这个错**不报错、不留痕**，只是数字偏 —— 典型的静默错数。
//
// 对齐到整分钟后，批次边界恰好落在分钟边界上，每个桶只可能落在一个批次里，
// 覆盖语义退化成"无冲突"，不需要改成合并逻辑。
//
// 向上取整（而不是截断）是为了不让用户配 1 秒时变成 0 秒。
func normalizeRollupBatchSeconds(seconds int) time.Duration {
	if seconds <= 0 {
		seconds = int(defaultRollupBackfillBatchSeconds)
	}
	if rem := seconds % 60; rem != 0 {
		seconds += 60 - rem
	}
	return time.Duration(seconds) * time.Second
}

// RollupBackfillUnitForTest 回填指定 (设备, 日) 一个单元，返回写入的分钟桶数。
//
// **仅供测试**：生产路径只经由 RunOnce 的单元枚举（它排除今天）。测试需要构造
// "live collector 已经把今天到目前为止的桶写好了"这一形态，所以必须能直接对
// 今天跑一次单元回填 —— 那是集合而不是复制生产逻辑，行为完全一致。
func RollupBackfillUnitForTest(ctx context.Context, db *gorm.DB, cfg RollupBackfillConfig, deviceID uint, day string) (int, error) {
	r := NewRollupBackfillRunner(db, cfg)
	return r.backfillUnit(ctx, rollupBackfillUnit{DeviceID: deviceID, Day: day})
}

// RollupDailyRolloverOffset 是每日 rollover 的时刻偏移（UTC 00:05）。
//
// 为什么不是 00:00：跨日边界上「昨天」刚结束，留 5 分钟让最后一批 rollup flush
// 落库，避免 rollover 与 live collector 的分钟桶写入争同一分钟。
const RollupDailyRolloverOffset = 5 * time.Minute

// NextRollupRolloverAt 返回 now 之后的第一个 rollover 时刻（UTC）。
//
// 为什么需要它：coverage 只在回填**跑起来**时才推进，而回填原本只在进程启动跑一次。
// 服务连续运行多日就会停止推进 => 读路径永远判「未覆盖」=> 长窗回落 => 停写后静默截断。
//
// 每日 ticker 解决「会不会再跑」，本函数解决「什么时候跑」。
// 抽成纯函数是为了可测：ticker 的时序不该靠真实等待来验证。
func NextRollupRolloverAt(now time.Time) time.Time {
	n := now.UTC()
	next := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, time.UTC).Add(RollupDailyRolloverOffset)
	if !next.After(n) {
		next = next.AddDate(0, 0, 1)
	}
	return next
}
