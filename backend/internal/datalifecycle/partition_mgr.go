package datalifecycle

import (
	"fmt"
	"log/slog"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"ehome/backend/pkg/metrics"
)

// 数据层时序化 (方案 docs/设计/架构优化实施方案.md v0.4 §3.2.1)。
// unified_data 改 PG 声明式 RANGE 分区（月粒度，分区键 timestamp）。
// SQLite（单测环境）不分区：所有函数在非 postgres 方言下直接返回 no-op，
// 与 purge.go:300 / backfill.go:178 的方言分支模式一致。
//
// 分区机制已泛化为按表名参数化（*For 系列）；unified_data 仅作默认表，
// 既有 EnsurePartitions/DropPartitionsBefore/MigrateUnifiedDataToPartitioned
// 保留旧签名并委托，调用方零改动。

const (
	// partitionRollaheadMonths 启动/每日检查时确保未来 N 个月的分区存在。
	partitionRollaheadMonths = 3

	// partitionedTable 分区母表名；legacyTable 为 unified_data 迁移后原平表的
	// 临时名——行数校验通过后即被 DROP，不再作为回滚副本长期保留。
	partitionedTable = "unified_data"
	legacyTable      = "unified_data_legacy"

	// 搬迁批次复用 migrate.go 的 migrateBatchSizePostgres（同包常量，PG 1 万行）。
)

// PartitionManager 管理时序表的月分区生命周期（默认 unified_data）。
type PartitionManager struct {
	db *gorm.DB
}

// NewPartitionManager creates a PartitionManager.
func NewPartitionManager(db *gorm.DB) *PartitionManager {
	return &PartitionManager{db: db}
}

func isPostgres(db *gorm.DB) bool {
	return db != nil && db.Dialector != nil && db.Dialector.Name() == "postgres"
}

// ==================== 迁移/索引维护进度上报（启动期进度展示契约 §3.2）====================
//
// 目标：把「启动期数据库优化」的进度送到 bootstrap.Tracker（main 注入适配器），
// 让维护页看到真实的阶段/百分比，而不是一个只会转圈的黑盒。
//
// 四条硬约束（违反任一条都是回归）：
//  1. **nil 回调 no-op**：未注入（测试、CLI、运行期其它调用）时行为与埋点前逐字一致；
//  2. **非 PG no-op**：所有上报点都落在既有的 isPostgres 分支之内，SQLite 测试库
//     不会因为多了一次回调而改变任何控制流；
//  3. **不改变现有行为与日志**：上报只发生在"已经决定要做"之后 / "已经做成"之后，
//     不参与任何 if 判定、不新增也不删减任何 slog 输出；
//  4. **回调不得 panic**：注入方（api 层适配器）负责保证；这里不 recover ——
//     吞掉 panic 只会把适配器的 bug 变成一份"永远停在 40%"的进度条。
//
// stepID 与 bootstrap 包的常量**必须逐字一致**（契约 §3.1 的 10 个 ID）。
// 这里刻意用本包自己的常量、不 import bootstrap：依赖方向保持
// datalifecycle → 标准库/GORM，启动编排（谁先谁后）不该渗进生命周期包。
// 一致性由 partition_progress_test.go 的跨包断言守着（测试依赖，不是生产依赖）。
const (
	// StepIDPartitionUnifiedData 对应 bootstrap.StepPartitionUnifiedData。
	StepIDPartitionUnifiedData = "partition_unified_data"
	// StepIDPartitionDeviceData 对应 bootstrap.StepPartitionDeviceData。
	StepIDPartitionDeviceData = "partition_device_data"
	// StepIDPartitionRollup 对应 bootstrap.StepPartitionRollup（telemetry_rollup_1m 的建表/分区就绪）。
	StepIDPartitionRollup = "partition_rollup"
	// StepIDParentIndexes 对应 bootstrap.StepParentIndexes（补齐父表索引，生产上最耗时）。
	StepIDParentIndexes = "parent_indexes"
	// StepIDRetireDeadIndexes 对应 bootstrap.StepRetireDeadIndexes（DROP 死索引，ACCESS EXCLUSIVE）。
	StepIDRetireDeadIndexes = "retire_dead_indexes"

	// partitionMonthLoopPctCap 是「按月搬迁循环」在整步进度里占的百分比上限。
	//
	// 剩下的 20% 留给循环之后的四件事（未来分区、行数校验、RENAME swap、DROP legacy）：
	// 这些在大表上不是零成本（建未来分区要逐个 CREATE TABLE，swap 要拿排他锁），
	// 若循环直接报到 100%，进度条会先到 100 再停在原地等最后一步 —— 看起来像卡死。
	partitionMonthLoopPctCap = 80.0
)

// MigrationProgress 是迁移/索引维护的进度回调（可为 nil）。
type MigrationProgress interface {
	StepProgress(stepID string, pct float64, detail string)
}

var (
	migrationProgressMu sync.RWMutex
	migrationProgress   MigrationProgress
)

// SetMigrationProgress 注入全局进度回调（启动期一次性注入；nil 表示关闭）。
//
// 显式把「typed nil」归一成 nil：api 侧若传 (*bootstrap.Tracker)(nil)，接口值本身
// 非 nil，直接调用会在迁移中途 panic —— 那种崩溃发生在 DROP/swap 之间，代价极高。
func SetMigrationProgress(p MigrationProgress) {
	migrationProgressMu.Lock()
	defer migrationProgressMu.Unlock()
	if p != nil {
		// 只有可为 nil 的 kind 才需要进一步判断（接口实现通常是 *Tracker 这样的指针）。
		if v := reflect.ValueOf(p); v.Kind() == reflect.Ptr && v.IsNil() {
			p = nil
		}
	}
	migrationProgress = p
}

// reportMigrationProgress 上报一次进度：nil 回调 / 空 stepID ⇒ no-op。
// pct 统一钳到 [0,100]：调用方算的是比例，浮点误差不该让前端看到 100.0000001。
func reportMigrationProgress(stepID string, pct float64, detail string) {
	if stepID == "" {
		return
	}
	migrationProgressMu.RLock()
	p := migrationProgress
	migrationProgressMu.RUnlock()
	if p == nil {
		return
	}
	p.StepProgress(stepID, clampProgressPct(pct), detail)
}

// clampProgressPct 把百分比钳到 [0,100]。
func clampProgressPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}

// partitionStepIDOf 把表名映射成契约 §3.2 的 stepID；未登记表返回空串（⇒ 不上报）。
//
// 为什么按表名派生而不是让调用方传 stepID：MigrateTableToPartitioned 是泛化入口，
// 调用点（main.go）只传表名。映射集中在这里，新增时序表时只需加一条 case。
func partitionStepIDOf(table string) string {
	switch table {
	case partitionedTable: // unified_data
		return StepIDPartitionUnifiedData
	case "device_data":
		return StepIDPartitionDeviceData
	case TelemetryRollupTable: // telemetry_rollup_1m
		return StepIDPartitionRollup
	default:
		return ""
	}
}

// monthProgress 是一次分区迁移里「某个月」在总体进度中的映射窗口。
//
// 为什么需要它：进度有两级粒度 —— 月份级（第 3/7 批）与批次级（本批已搬 N 行）。
// 两级都会上报，若各自按自己的 0..100 上报，进度条会来回跳。这里把"本月"固定
// 映射到 [base, base+span] 区间：月内批次按 frac∈[0,1] 折算，跨月 base 递增 ⇒
// 整条曲线单调不减。
//
// 零值（stepID==""）表示"不上报"，所以 copyMonthBatched 的既有调用方/测试不受影响。
type monthProgress struct {
	stepID string  // 目标步骤 ID（空 ⇒ 关闭上报）
	base   float64 // 本月在整步里的百分比起点
	span   float64 // 本月占整步的百分比跨度
	label  string  // 月份标签，形如 "202611 第 3/7 批"（拼进 detail）
}

// report 上报月内进度（frac 0..1）。stepID 为空时是纯 no-op。
func (mp monthProgress) report(frac float64, detail string) {
	if mp.stepID == "" {
		return
	}
	if frac < 0 {
		frac = 0
	}
	if frac > 1 {
		frac = 1
	}
	reportMigrationProgress(mp.stepID, mp.base+mp.span*frac, detail)
}

// monthSpanInclusive 返回 [from, to] 覆盖的自然月个数（含两端，最少 1）。
// 用于把"按月循环"换算成百分比分母。
func monthSpanInclusive(from, to time.Time) int {
	f, t := monthStart(from), monthStart(to)
	if t.Before(f) {
		return 1
	}
	return (t.Year()-f.Year())*12 + int(t.Month()-f.Month()) + 1
}

// commatize 给行数加千分位（进度 detail 是给人看的，41000 不如 41,000 易读）。
func commatize(n int64) string {
	s := strconv.FormatInt(n, 10)
	neg := ""
	if strings.HasPrefix(s, "-") {
		neg, s = "-", s[1:]
	}
	if len(s) <= 3 {
		return neg + s
	}
	var b strings.Builder
	b.Grow(len(s) + len(s)/3)
	head := len(s) % 3
	if head == 0 {
		head = 3
	}
	b.WriteString(s[:head])
	for i := head; i < len(s); i += 3 {
		b.WriteByte(',')
		b.WriteString(s[i : i+3])
	}
	return neg + b.String()
}

// partitionName renders the monthly partition table name of table for t.
//
// **必须先 UTC 归一化再取年月**（同 monthStart 的说明）：分区边界是 UTC 月，
// 名字里的 YYYYMM 必须与边界一致，否则 TZ=+08 的机器会把同一个 UTC 月命名成
// 下一个月，导致"按名字找分区"与"按边界找分区"两套口径打架。
func partitionName(table string, t time.Time) string {
	u := t.UTC()
	return fmt.Sprintf("%s_%04d%02d", table, u.Year(), int(u.Month()))
}

// monthStart truncates t to the first day of its **UTC** month.
//
// ⚠ 为什么必须显式 .UTC()（2026-11-06 实测缺陷，不是理论洁癖）
//
// 原实现是 time.Date(t.Year(), t.Month(), 1, …, time.UTC) —— 年月取自 t 的
// **本地时区视角**，但构造出来的是 UTC 月初。两者只在 time.Local==UTC 时一致。
//
// 真实故障链（任何 TZ=Asia/Shanghai 的机器/容器必现，生产容器只是碰巧 TZ 未设
// ⇒ Go 的 time.Local 退化成 UTC）：
//  1. PG 的 timestamptz 经 pgx 解码得到的是 **time.Local** 时区的 time.Time
//     （pgtype/timestamptz.go 用 time.Unix(...) 构造，未显式置 UTC）；
//  2. oldestRowMonthFor 取 min(timestamp) 后调 monthStart ⇒ 在 +08 下，
//     UTC 的 2026-10-31T16:30Z（= 本地 2026-11-01T00:30+08）被取成"11 月 1 日"，
//     构造出 2026-11-01T00:00Z —— 比该行的真实时刻**晚** 7.5 小时；
//  3. 于是最早的那个 UTC 月（10 月）一个分区都不建，10 月前 8 小时的行落在
//     所有 COPY 窗口之外 ⇒ 行数校验 src=3 dst=1 ⇒ 迁移 Fatal。
//     实测对照：同一份数据 TZ=UTC 启动 100% 成功、TZ=Asia/Shanghai 启动必失败。
//
// 归一化到 UTC 后，monthStart 变成"UTC 时刻的 UTC 月初"，与分区名的 YYYYMM、
// 分区边界 FOR VALUES FROM/TO、copyMonthBatched 的 [start, end) 窗口三者同口径。
func monthStart(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), 1, 0, 0, 0, 0, time.UTC)
}

// addMonths returns monthStart(t) advanced by n months.
func addMonths(t time.Time, n int) time.Time {
	return monthStart(t).AddDate(0, n, 0)
}

// EnsurePartitionsFor creates any missing monthly partitions of table covering
// [currentMonth-1, currentMonth+months]. Safe to call repeatedly.
// No-op on non-postgres dialects.
func (pm *PartitionManager) EnsurePartitionsFor(table string, months int) error {
	if !isPostgres(pm.db) {
		slog.Debug("partition_mgr: non-postgres dialect, skip")
		return nil
	}
	if months < 0 {
		months = 0
	}
	now := time.Now()
	// 从上月开始到未来 months 月（上月兜底：跨月边界晚到的数据仍可写入）。
	for i := -1; i <= months; i++ {
		start := addMonths(now, i)
		end := addMonths(start, 1)
		if err := pm.createPartitionIfNotExists(partitionName(table, start), table, start, end); err != nil {
			metrics.LifecycleTaskFailures.WithLabelValues("partition").Inc()
			return fmt.Errorf("ensure partition %s: %w", partitionName(table, start), err)
		}
	}
	return nil
}

// EnsurePartitions 保留旧签名，委托给 EnsurePartitionsFor("unified_data", ...)。
func (pm *PartitionManager) EnsurePartitions(months int) error {
	return pm.EnsurePartitionsFor(partitionedTable, months)
}

// tableExistsSQL 统计当前 schema 内指定 relkind 的同名表数量。
// schema 限定 (n.nspname = current_schema()) 必须: 集成测试每用例独立
// schema, 无限定则跨 schema 误判存在性 (生产单 schema 下同样更健壮)。
const tableExistsSQL = `SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace WHERE c.relname = ? AND c.relkind = ? AND n.nspname = current_schema()`

// createPartitionIfNotExists creates one RANGE partition under parent when absent.
// 存在性判断走 pg_class（PG 无 CREATE TABLE IF NOT EXISTS ... PARTITION OF）。
func (pm *PartitionManager) createPartitionIfNotExists(name, parent string, start, end time.Time) error {
	var count int64
	err := pm.db.Raw(tableExistsSQL, name, "r").Scan(&count).Error
	if err != nil {
		return fmt.Errorf("check pg_class: %w", err)
	}
	if count > 0 {
		return nil
	}
	ddl := fmt.Sprintf(
		"CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
		name, parent,
		start.Format("2006-01-02 15:04:05"), end.Format("2006-01-02 15:04:05"),
	)
	if err := pm.db.Exec(ddl).Error; err != nil {
		return err
	}
	slog.Info("partition_mgr: created partition", "partition", name, "parent", parent)
	return nil
}

// DropPartitionsBeforeFor drops all monthly partitions of table whose entire
// range is older than cutoff. Retention 的 O(1) 替代：整分区 DROP 替代逐行 DELETE。
// Returns the dropped partition names.
func (pm *PartitionManager) DropPartitionsBeforeFor(table string, cutoff time.Time) ([]string, error) {
	if !isPostgres(pm.db) {
		return nil, nil
	}
	var names []string
	rows, err := pm.db.Raw(
		`SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relname LIKE ? AND c.relkind = 'r' AND n.nspname = current_schema()`,
		table+"_%",
	).Rows()
	if err != nil {
		metrics.LifecycleTaskFailures.WithLabelValues("partition").Inc()
		return nil, fmt.Errorf("list partitions: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			names = append(names, n)
		}
	}

	cutoffMonth := monthStart(cutoff)
	var dropped []string
	for _, n := range names {
		// 分区名形如 <table>_YYYYMM: 跳过母表名+下划线再解析月份。
		// (修复 off-by-one: 此前 n[len(partitionedTable):] 带下划线,
		// time.Parse 恒失败, 导致到期分区永不 DROP。)
		pt, parseErr := time.Parse("200601", n[len(table)+1:])
		if parseErr != nil {
			continue // 非 YYYYMM 后缀的表不碰
		}
		pStart := monthStart(pt)
		// 整分区早于 cutoff 才删：分区起点 < cutoff 月起点。
		if pStart.Before(cutoffMonth) {
			if err := pm.db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s", n)).Error; err != nil {
				metrics.LifecycleTaskFailures.WithLabelValues("partition").Inc()
				return dropped, fmt.Errorf("drop %s: %w", n, err)
			}
			dropped = append(dropped, n)
			metrics.LifecycleDroppedPartitions.Inc()
			slog.Info("partition_mgr: dropped expired partition", "partition", n)
		}
	}
	return dropped, nil
}

// DropPartitionsBefore 保留旧签名，委托给 DropPartitionsBeforeFor("unified_data", ...)。
func (pm *PartitionManager) DropPartitionsBefore(cutoff time.Time) ([]string, error) {
	return pm.DropPartitionsBeforeFor(partitionedTable, cutoff)
}

// 注: EnsureRollupTable (分钟级 rollup 建表) 已于 2026-09-15 随表退役删除 ——
// 该表无读取者且 EXPLAIN 实测无性能收益; 表由 database.RetireLegacyRollup1m
// 幂等 DROP。裁决: docs/分析/rollup-退役裁决-2026-09-15.md。

// IsTablePartitioned reports whether table is already a partitioned parent
// (relkind='p', migration done).
func IsTablePartitioned(db *gorm.DB, table string) bool {
	if !isPostgres(db) {
		return false
	}
	var count int64
	// 分区母表 relkind='p'（partitioned table），普通表为 'r'。
	db.Raw(tableExistsSQL, table, "p").Scan(&count)
	return count > 0
}

// IsUnifiedDataPartitioned 保留旧签名，委托给 IsTablePartitioned("unified_data")。
func IsUnifiedDataPartitioned(db *gorm.DB) bool {
	return IsTablePartitioned(db, partitionedTable)
}

// MigrateTableToPartitioned converts the flat table into a declarative
// RANGE-partitioned parent (monthly, key=timestamp), preserving:
//   - id 序列全局不变（业务零感知，id 单调排序语义不变）
//   - 全部既有索引（分区级重建）
//   - 原表数据（双表并存搬迁；校验通过后 DROP legacy 旧表，不保留回滚副本）
//
// legacyName 为迁移 swap 期间原平表的临时名，行数校验通过后立即 DROP
// （产品决策：不再保留回滚副本以节省空间，代价是失去回滚能力）。
// 安全门禁：仅当"迁移后母表行数 == 迁移前平表行数"校验通过才 DROP；校验
// 失败时绝不动 legacy（旧表原样保留并报错返回）。
// 幂等：母表已存在（已迁移过）直接返回；若此时仍残留 legacy 旧表（历史遗留），
// 在确认母表已分区后一并 DROP。
// SQLite no-op。
// 步骤: 建 new 母表 → 按月分批 INSERT SELECT → 校验行数 → RENAME swap → DROP legacy。
func MigrateTableToPartitioned(db *gorm.DB, table, legacyName string) error {
	if !isPostgres(db) {
		slog.Debug("migrate_partitioned: non-postgres dialect, skip")
		return nil
	}
	if IsTablePartitioned(db, table) {
		slog.Info("migrate_partitioned: already partitioned, skip")
		// 幂等路径: 母表已确认分区 (迁移已完成), 若历史遗留仍保留着
		// <table>_legacy 旧表, 一并清理 (不再作为回滚副本保留)。
		if err := dropLegacyTable(db, table, legacyName); err != nil {
			return err
		}
		// 新增索引的补齐点（2026-10-25 F-10）: 已分区库不会再走建表路径，
		// 不在这里补建，partitionParentIndexes 新增的索引在存量生产库上永不生效。
		// 函数幂等（按列清单命中即跳过），代价见 ensurePartitionParentIndexes 注释。
		return finalizePartitionParentIndexes(db, table)
	}
	// 原表不存在（全新部署）→ 直接以最终名建分区母表。
	// 不走 _new 中转: fresh 路径无数据搬迁, 无需 RENAME swap。
	var flatExists int64
	db.Raw(tableExistsSQL, table, "r").Scan(&flatExists)
	if flatExists == 0 {
		slog.Info("migrate_partitioned: no legacy flat table, creating fresh partitioned parent", "table", table)
		// 幂等: 清理上次中断残留的 new 表。
		if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", table+"_new")).Error; err != nil {
			return fmt.Errorf("drop stale %s: %w", table+"_new", err)
		}
		if err := createPartitionedParent(db, table, table, true); err != nil {
			return err
		}
		// 建表路径已按同一份清单建完索引；这里再走一次幂等补齐，守住
		// 「建表路径与补齐路径清单一致」的不变量（存在即跳过，缺失即补）。
		return finalizePartitionParentIndexes(db, table)
	}

	newTable := table + "_new"
	// 幂等: 清理上次中断残留的 new 表 (CASCADE 连带挂在它下面的月份分区)。
	if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", newTable)).Error; err != nil {
		return fmt.Errorf("drop stale %s: %w", newTable, err)
	}
	// M3: 清理同名月份分区残留。上次迁移可能在 <table>_new 下建过
	// <table>_YYYYMM 并灌入部分数据; 若这些关系未随 _new 一起消失 (detached
	// 或其它中断形态), createPartitionIfNotExists 会因"同名表已存在"而跳过在
	// new 母表下建分区, 随后 INSERT 报 "no partition of relation found", 迁移
	// 永不收敛。此刻 <table> 仍是平表 (尚未 swap), 这些残留分区只是平表行的
	// 副本, 平表完整保留 → DROP 不丢任何源数据。
	if err := dropStaleMonthPartitions(db, table); err != nil {
		return err
	}
	if err := createPartitionedParent(db, table, newTable, false); err != nil {
		return fmt.Errorf("create %s: %w", newTable, err)
	}

	// 按月分批搬迁（水位断点续跑: 失败重跑时 INSERT 已存在行会被 ON CONFLICT 跳过——
	// 主键 (id,timestamp) 天生幂等）。
	now := time.Now()
	oldest := oldestRowMonthFor(db, table)
	if oldest.IsZero() {
		oldest = now // 空表
	}
	pm := &PartitionManager{db: db}

	// 启动期进度（契约 §3.2）: 按月循环映射到整步的 [0, partitionMonthLoopPctCap]，
	// 剩余区间留给"未来分区 + 行数校验 + RENAME swap + DROP legacy"（见下方各上报点）。
	// 分母用**闭区间月份数**（最少 1）: 空表时 oldest=now 仍会走一轮循环，除以 0 会把
	// 百分比打成 NaN，前端进度条直接坏掉。
	progressStepID := partitionStepIDOf(table)
	totalMonths := monthSpanInclusive(monthStart(oldest), monthStart(now))
	monthIndex := 0
	// totalMoved 只用于进度 detail 文案（"共搬 N 行"），不参与任何判定。
	var totalMoved int64

	for m := monthStart(oldest); !m.After(monthStart(now)); m = addMonths(m, 1) {
		monthIndex++
		monthLabel := fmt.Sprintf("%s 第 %d/%d 批", m.Format("200601"), monthIndex, totalMonths)
		span := partitionMonthLoopPctCap / float64(totalMonths)
		mp := monthProgress{
			stepID: progressStepID,
			base:   span * float64(monthIndex-1),
			span:   span,
			label:  monthLabel,
		}
		mp.report(0, monthLabel)
		// 历史月份的分区必须先建好, 否则 INSERT INTO new 母表报
		// "no partition of relation found" (PG 分区表无兜底 default 分区)。
		// 分区名以最终母表 (table) 为前缀, 挂在 new 母表下; RENAME 后即
		// table_YYYYMM (与既有 unified_data 命名/retention 扫描一致)。
		if err := pm.createPartitionIfNotExists(partitionName(table, m), newTable, monthStart(m), addMonths(m, 1)); err != nil {
			return fmt.Errorf("create history partition %s: %w", partitionName(table, m), err)
		}
		moved, err := copyMonthBatched(db, newTable, table, m, mp)
		if err != nil {
			return fmt.Errorf("copy month %04d-%02d: %w", m.Year(), int(m.Month()), err)
		}
		totalMoved += moved
		mp.report(1, fmt.Sprintf("%s 已搬 %s 行", monthLabel, commatize(moved)))
	}
	// 未来分区 (含上月兜底): 在 swap 前于 new 母表下建好, RENAME 后即刻可写。
	// 不能调 EnsurePartitionsFor(table) — 它以 table (此刻仍是旧平表) 为母表。
	reportMigrationProgress(progressStepID, partitionMonthLoopPctCap,
		fmt.Sprintf("历史数据搬迁完成（%d 个月 %s 行），正在准备滚动分区",
			totalMonths, commatize(totalMoved)))
	for i := -1; i <= partitionRollaheadMonths; i++ {
		start := addMonths(now, i)
		if err := pm.createPartitionIfNotExists(partitionName(table, start), newTable, start, addMonths(start, 1)); err != nil {
			return fmt.Errorf("ensure partition %s: %w", partitionName(table, start), err)
		}
	}

	// 行数校验门禁（§8 风险表: 搬迁丢数据缓解）。
	reportMigrationProgress(progressStepID, 85, "正在校验搬迁行数")
	var srcCount, dstCount int64
	db.Raw("SELECT count(*) FROM " + table).Scan(&srcCount)
	db.Raw("SELECT count(*) FROM " + newTable).Scan(&dstCount)
	if srcCount != dstCount {
		return fmt.Errorf("row count mismatch after migration: src=%d dst=%d (aborting, tables intact)", srcCount, dstCount)
	}
	reportMigrationProgress(progressStepID, 90,
		fmt.Sprintf("行数校验通过（%s 行），正在切换表名", commatize(dstCount)))

	// RENAME swap: 原表→legacy（临时名），new→table。
	// 行数校验已在上方通过 (安全门禁)，才会执行到这里。
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(fmt.Sprintf("ALTER TABLE %s RENAME TO %s", table, legacyName)).Error; err != nil {
			return err
		}
		return tx.Exec(fmt.Sprintf("ALTER TABLE %s RENAME TO %s", newTable, table)).Error
	}); err != nil {
		return fmt.Errorf("rename swap: %w", err)
	}
	// 推进 id 序列至 MAX(id): BIGSERIAL 的 INSERT...SELECT 搬迁不触碰
	// sequence, 不推进则迁移后首条新数据 nextval 回到 1 (违反 "id 序列
	// 全局不变" 承诺, id 单调排序语义被破坏)。
	if err := db.Exec(fmt.Sprintf(
		"SELECT setval(pg_get_serial_sequence('%s', 'id'), COALESCE((SELECT MAX(id) FROM %s), 1))",
		table, table)).Error; err != nil {
		return fmt.Errorf("advance id sequence: %w", err)
	}
	reportMigrationProgress(progressStepID, 95, "正在清理旧表")
	// 安全门禁通过: 不再保留 legacy 回滚副本, 直接 DROP (产品决策, 省空间)。
	// 放在 swap/setval 之后: 前面的步骤失败时 legacy 原样保留。
	if err := dropLegacyTable(db, table, legacyName); err != nil {
		return err
	}
	slog.Info("migrate_partitioned: migration complete",
		"table", table, "rows", dstCount, "legacy_table", legacyName, "legacy_dropped", true)
	reportMigrationProgress(progressStepID, 100,
		fmt.Sprintf("迁移完成（%s 行）", commatize(dstCount)))
	// 迁移路径的索引建在 <table>_new 上（名字带 _new_ 前缀，PG RENAME 不改名），
	// 幂等补齐按列清单判定存在 ⇒ 不会重复建；清单新增的模板若迁移时不存在，也会在此补上。
	return finalizePartitionParentIndexes(db, table)
}

// MigrateUnifiedDataToPartitioned 保留旧签名，委托给 MigrateTableToPartitioned。
func MigrateUnifiedDataToPartitioned(db *gorm.DB) error {
	return MigrateTableToPartitioned(db, partitionedTable, legacyTable)
}

// dropLegacyTable 删除迁移 swap 后遗留的 <table>_legacy 旧平表。
//
// 调用约束 (安全门禁): 只在母表已确认为分区表时调用——正常迁移路径在行数
// 校验通过之后调用; 幂等早返回路径在 IsTablePartitioned 成立之后调用。
// 行数校验失败的分支绝不调用本函数, 因此旧表在校验未通过时绝不会被删除。
//
// 先采集旧表行数与占用字节数 (写入日志), 再用 IF EXISTS + CASCADE 幂等清理。
// CASCADE 只连带依附于该 legacy 表自身的对象 (其索引/序列), 不触碰其他表。
func dropLegacyTable(db *gorm.DB, table, legacyName string) error {
	if legacyName == "" {
		return nil
	}
	var exists int64
	if err := db.Raw(tableExistsSQL, legacyName, "r").Scan(&exists).Error; err != nil {
		return fmt.Errorf("check legacy table %s: %w", legacyName, err)
	}
	if exists == 0 {
		return nil
	}
	var rows, bytes int64
	db.Raw("SELECT count(*) FROM " + legacyName).Scan(&rows)
	// 大小仅用于日志, 查询失败不阻断 DROP (bytes 保持 0)。
	db.Raw("SELECT pg_total_relation_size(?::regclass)", legacyName).Scan(&bytes)
	if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", legacyName)).Error; err != nil {
		return fmt.Errorf("drop legacy table %s: %w", legacyName, err)
	}
	slog.Info("partition_mgr: dropped legacy flat table",
		"table", table, "legacy_table", legacyName, "legacy_rows", rows, "legacy_bytes", bytes)
	return nil
}

// dropStaleMonthPartitions 删除 <table>_YYYYMM 形态的残留月份分区 (M3)。
//
// 只在 <table> 尚未 swap (仍是平表) 的迁移开始阶段调用: 此时任何
// <table>_YYYYMM 关系都是上一次中断迁移的产物, 其行是对平表数据
// (INSERT ... SELECT) 的副本, 且平表完整保留 → DROP 不丢任何源数据。
// 清理后由 createPartitionIfNotExists 在 <table>_new 下重建干净分区, 保证
// 重跑能收敛。
//
// 仅匹配后缀可解析为 YYYYMM 的关系: <table>_legacy / <table>_new /
// <table>_rollup_1m 等一律不碰。名称 LIKE 的匹配走 schema 限定; 这里已确认
// <table> 是平表 (flatExists>0 且 IsTablePartitioned=false), 因此不会误删
// 已分区母表的分区。
func dropStaleMonthPartitions(db *gorm.DB, table string) error {
	rows, err := db.Raw(
		`SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		 WHERE c.relname LIKE ? AND c.relkind = 'r' AND n.nspname = current_schema()`,
		table+"_%",
	).Rows()
	if err != nil {
		return fmt.Errorf("list stale partitions of %s: %w", table, err)
	}
	var names []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err == nil {
			names = append(names, n)
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return fmt.Errorf("scan stale partitions of %s: %w", table, err)
	}
	for _, n := range names {
		// 分区名形如 <table>_YYYYMM; 其余 (legacy/new/rollup...) 跳过。
		if _, err := time.Parse("200601", n[len(table)+1:]); err != nil {
			continue
		}
		if err := db.Exec(fmt.Sprintf("DROP TABLE IF EXISTS %s CASCADE", n)).Error; err != nil {
			return fmt.Errorf("drop stale partition %s: %w", n, err)
		}
		slog.Info("partition_mgr: dropped stale month partition from interrupted migration",
			"table", table, "partition", n)
	}
	return nil
}

// partitionParentDDL maps a logical table name to the CREATE TABLE template of
// its partitioned parent (%s = physical table name; <table> on the fresh path,
// <table>_new during legacy migration). Per-table switch keeps the existing
// unified_data column definition verbatim (防回归) while device_data aligns
// with models.DeviceData. Both use the composite PK (id, timestamp) required
// by PG partitioning (the partition key must be part of the primary key) and
// RANGE-partition on timestamp.
var partitionParentDDL = map[string]string{
	"unified_data": `CREATE TABLE %s (
		id BIGSERIAL,
		device_id BIGINT NOT NULL,
		sensor_name VARCHAR(32) NOT NULL,
		value DOUBLE PRECISION NOT NULL,
		unit VARCHAR(16),
		timestamp TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ,
		edge_device_id BIGINT,
		PRIMARY KEY (id, timestamp)
	) PARTITION BY RANGE (timestamp)`,
	// device_data: data_json TEXT; device_id/node_id NOT NULL (models 对齐)。
	"device_data": `CREATE TABLE %s (
		id BIGSERIAL,
		device_id BIGINT NOT NULL,
		node_id VARCHAR(32) NOT NULL,
		data_json TEXT,
		timestamp TIMESTAMPTZ NOT NULL,
		created_at TIMESTAMPTZ,
		edge_device_id BIGINT,
		PRIMARY KEY (id, timestamp)
	) PARTITION BY RANGE (timestamp)`,
	// telemetry_rollup_1m: L3 分钟级宽行预聚合 (设计/遥测存储优化方案-2026-11-06.md §5.2)。
	//
	// 与已退役的 unified_data_rollup_1m (同 database/retire_rollup.go) 的区别:
	//   - 宽行: 每设备每分钟**一行** payload bytea (打包 avg/min/max/last), 而不是
	//     每字段一行 —— 实测宽行 327.3 B/行 × 2880 行/天 = 0.9 MB/天, 窄表同口径
	//     10.6 MB/天 (方案 §5.2 的 12 倍差);
	//   - 有明确消费者: 长窗图表 (> 解码阈值) 走本表, 不是"写了没人读";
	//   - 分区键 bucket (不用 timestamp 命名, 避免与 unified_data 的子分区
	//     DROP/retention 扫描口径混淆)。
	//
	// ⚠ 主键必须是 (device_id, bucket): PG 要求分区键出现在主键里, 且宽行语义下
	// 同设备同分钟只能有一行 (旁路聚合每批 UPSERT 覆盖)。缺 bucket 会让
	// PARTITION BY RANGE (bucket) 直接建表失败。
	//
	// ⚠ updated_at 必须与 models.TelemetryRollup1m.UpdatedAt 对齐（2026-11-07 生产事故）:
	// upsertRollupRow 的 INSERT 里带了 updated_at，而本 DDL 曾漏掉该列 ⇒ 生产每分钟
	// 报 column "updated_at" of relation "telemetry_rollup_1m" does not exist (42703)，
	// rollup 表**一行都写不进去**。SQLite 用例走 AutoMigrate（有该列）⇒ 全绿，
	// 这是「四处同改」漏一处的典型形态（方案 §8 风险表最后一条）。
	// 现在由 ensurePartitionParentColumns 兜底：存量库启动时自动 ALTER TABLE 补列。
	"telemetry_rollup_1m": `CREATE TABLE %s (
		device_id  integer NOT NULL,
		bucket     TIMESTAMPTZ NOT NULL,
		payload    BYTEA NOT NULL,
		updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
		PRIMARY KEY (device_id, bucket)
	) PARTITION BY RANGE (bucket)`,
	// telemetry_frame: L2 帧级增量编码的帧表 (设计/遥测存储优化方案-2026-11-06.md §4.1)。
	//
	// 为什么必须在这里登记 (而不是靠 createPartitionedParent 的未登记回退):
	// 未登记表会**沿用 unified_data 的列结构与索引清单** (见 createPartitionedParent
	// 里 ddlTable 的取值)。telemetry_frame 没有 id/sensor_name/value/unit/timestamp
	// 这些列，回退会让建表直接报错，或更糟——建出一张形状全错的表让索引模板
	// 引用不存在的列。
	//
	// 列说明:
	//   - 主键 (device_id, ts, seq): PG 要求分区键 ts 出现在主键里; seq 用来区分
	//     「同一设备同一时间戳的多帧」(device15 实测 1.85 帧/秒; 没有 seq 时主键
	//     冲突会让后一帧**覆盖**前一帧 = 静默丢数据, §8 风险表「秒内多帧」);
	//   - frame_kind: 0=增量 1=关键帧, 与 telemetry.EncodedFrame.FrameKind 同域;
	//   - slot_id: 帧类型编号, 指向 telemetry_slot (字段集合 + 量化规格快照);
	//   - payload: 1B flags + ceil(N/8)B changed_mask + LSB-first 位打包量化值;
	//   - raw: 可选原始总线帧, 只在关键帧/错误帧上保留 (§9.6 可回滚)。
	"telemetry_frame": `CREATE TABLE %s (
		device_id  integer NOT NULL,
		ts         TIMESTAMPTZ NOT NULL,
		seq        smallint NOT NULL DEFAULT 0,
		frame_kind smallint NOT NULL,
		slot_id    smallint NOT NULL,
		payload    BYTEA NOT NULL,
		raw        BYTEA,
		PRIMARY KEY (device_id, ts, seq)
	) PARTITION BY RANGE (ts)`,
}

// indexNameOf 从 "CREATE INDEX <name> ON ..." 里取出索引名（用于方言增强版去重）。
func indexNameOf(stmt string) string {
	const p = "CREATE INDEX "
	if !strings.HasPrefix(stmt, p) {
		return ""
	}
	rest := stmt[len(p):]
	if i := strings.IndexByte(rest, ' '); i > 0 {
		return rest[:i]
	}
	return rest
}

// indexColumnsOf 取出 CREATE INDEX 语句里**第一组**列清单（括号内容）。
// 第一组一定是索引列；INCLUDE (...) 在其后，不会被误取。
func indexColumnsOf(stmt string) (string, bool) {
	open := strings.IndexByte(stmt, '(')
	if open < 0 {
		return "", false
	}
	rest := stmt[open:]
	closeIdx := strings.IndexByte(rest, ')')
	if closeIdx <= 0 {
		return "", false
	}
	cols := strings.TrimSpace(rest[1:closeIdx])
	return cols, cols != ""
}

// normalizeIndexColumns 归一化「列清单 / pg_get_indexdef 文本」以便按列比对：
// PG 会给保留字加引号（"timestamp"），大小写与空白也不稳定，而模板是固定写法。
// 去引号 + 小写 + 折叠空白后比较，避免「同一语义两种拼写」被判成缺失。
func normalizeIndexColumns(s string) string {
	s = strings.ReplaceAll(s, "\"", "")
	s = strings.ToLower(strings.TrimSpace(s))
	return strings.Join(strings.Fields(s), " ")
}

// effectiveParentIndexTemplates 返回当前方言下「分区母表上应当存在」的索引 DDL：
// partitionParentIndexes 的通用模板，PG 上再用 partitionParentIndexesPG 的同名
// 增强版**替换**（不是追加，同名索引只能建一次）。
// 建表路径与补齐路径共用这一份清单，避免二者各抄一套后静默漂移。
func effectiveParentIndexTemplates(db *gorm.DB, table, name string) []string {
	base := partitionParentIndexes[table]
	var pg []string
	if isPostgres(db) {
		pg = partitionParentIndexesPG[table]
	}
	overridden := make(map[string]struct{}, len(pg))
	for _, tmpl := range pg {
		overridden[indexNameOf(fmt.Sprintf(tmpl, name, name))] = struct{}{}
	}
	out := make([]string, 0, len(base)+len(pg))
	for _, tmpl := range base {
		if _, skip := overridden[indexNameOf(fmt.Sprintf(tmpl, name, name))]; skip {
			continue
		}
		out = append(out, tmpl)
	}
	return append(out, pg...)
}

// ensurePartitionParentIndexes 在**已分区**母表上幂等补齐缺失的父表索引。
//
// 为什么必须有（2026-10-25 F-10）: MigrateTableToPartitioned 对「已迁移完成」
// 的库直接 early-return（幂等设计），因此 partitionParentIndexes **新增**的索引
// 只会建在之后首次迁移的库上；生产库早已分区 ⇒ 升级后新索引永远不会被创建，
// F-10 的修复在生产形同虚设。这里在幂等路径上补建缺失项。
//
// 判存在**按列清单而不是按索引名**: 旧库路径的母表先建为 <table>_new，索引名
// 因此带 _new_ 前缀，而 PG 的 ALTER TABLE ... RENAME TO 不会重命名索引（同一
// 事实在 partition_mgr_device_data_pg_test.go 的注释里有记录）。按名字判断会把
// 「已有的等价索引」误判为缺失，重复建索引。
//
// 代价与边界（必须让运维知道）: 首次升级会在启动阶段对已分区母表执行
// CREATE INDEX（取 SHARE 锁；分区表上父索引会为每个现有分区建子索引）——
// 生产 34M 行级别可能需要数十秒到分钟级，期间该表写入阻塞。建议在维护窗口升级，
// 或提前人工用 CREATE INDEX CONCURRENTLY（PG17+ 已支持分区表）建好，本函数随后
// 按列命中即跳过。函数自身幂等：存在即跳过，重启零副作用。
func ensurePartitionParentIndexes(db *gorm.DB, table string) error {
	if !isPostgres(db) {
		return nil // 分区表只存在于 PG；SQLite 测试库 no-op
	}
	defs, err := partitionParentIndexDefs(db, table)
	if err != nil {
		return err
	}
	// 先算出**缺失清单**再逐条建：原实现边判定边建，进度只能按"第几条模板"
	// 估算，而绝大多数模板在稳定库上都是"已存在 ⇒ 跳过"，会让进度条在开工前
	// 就跳到 100% 然后停在原地等真正的 DDL。两趟扫描的判定条件与顺序完全一致
	// （defs 只取一次，判定是纯字符串比较），因此行为无变化。
	var missing []string
	for _, tmpl := range effectiveParentIndexTemplates(db, table, table) {
		stmt := fmt.Sprintf(tmpl, table, table)
		columns, ok := indexColumnsOf(stmt)
		if !ok {
			return fmt.Errorf("unparsable partition parent index template %q", tmpl)
		}
		want := "(" + normalizeIndexColumns(columns) + ")"
		found := false
		for _, def := range defs {
			if strings.Contains(normalizeIndexColumns(def), want) {
				found = true
				break
			}
		}
		if found {
			continue
		}
		missing = append(missing, stmt)
	}
	if len(missing) == 0 {
		return nil
	}
	// 逐条上报（契约 §3.2「每建一条索引前后上报」）。这是生产上最耗时的一步
	// （实测 +4.51 GB / 69.6M 行，分钟级 DDL），维护页必须能看见它在动。
	total := len(missing)
	for i, stmt := range missing {
		name := indexNameOf(stmt)
		before := float64(i) / float64(total) * 100
		after := float64(i+1) / float64(total) * 100
		reportMigrationProgress(StepIDParentIndexes, before,
			fmt.Sprintf("%s 第 %d/%d 条索引：%s", table, i+1, total, name))
		slog.Info("partition_mgr: creating missing partition parent index (startup DDL, may take a while on large tables)",
			"table", table, "statement", stmt)
		if err := db.Exec(stmt).Error; err != nil {
			return fmt.Errorf("create missing parent index on %s: %w", table, err)
		}
		reportMigrationProgress(StepIDParentIndexes, after,
			fmt.Sprintf("%s 第 %d/%d 条索引完成：%s", table, i+1, total, name))
	}
	return nil
}

// partitionParentIndexDefs 列出 table 在当前 schema 下所有**有效**索引的定义文本。
func partitionParentIndexDefs(db *gorm.DB, table string) ([]string, error) {
	var defs []string
	query := "SELECT pg_get_indexdef(x.indexrelid) " +
		"FROM pg_index x " +
		"JOIN pg_class t ON t.oid = x.indrelid " +
		"JOIN pg_namespace n ON n.oid = t.relnamespace " +
		"WHERE t.relname = ? AND n.nspname = current_schema() AND x.indisvalid"
	if err := db.Raw(query, table).Scan(&defs).Error; err != nil {
		return nil, fmt.Errorf("list indexes on %s: %w", table, err)
	}
	return defs, nil
}

// partitionParentIndexes maps a logical table name to the CREATE INDEX
// templates (%s、%s = physical table name) rebuilding the flat table's b-tree
// indexes at partition level. 索引名沿用既有 <name>_<column>_idx 约定。
//
// 2026-10-23: 逻辑设备层移除后，(logical_device_id, ...) 系列复合索引不再存在
// （它们的生产者 indexes.go 已随该层删除）。数据查询一律 device_id 等值 +
// timestamp/created_at 范围，因此 device_id 索引就是主路径。
//
// 2026-10-24（审核 P1）：**只有单列 device_id 索引不够**。生产实测
//
//	/latest-data     0.39 ms → 400~2000 ms（device 14 / device_data 1.0M 行）
//	30 天历史图表     0.46 s → 5.3 s（单设备独占 29.2M 行，device_id 选择性极差）
//
// 根因是旧复合索引 idx_devdata_logical_created / idx_unified_logical_sensor_ts
// 随逻辑列一起被删，却没有 device 维度的等价替代。补两条复合索引：
//
//	unified_data (device_id, sensor_name, timestamp) INCLUDE (value)
//	  —— 历史图表按 (device, 字段) + 时间窗取 value，可走纯 index-only scan
//	device_data  (device_id, created_at DESC)
//	  —— latest-data 取「该设备最新一条」，ORDER BY created_at DESC LIMIT 1
//
// 2026-10-25（F-10 审核）：data-summary 的「时间范围」对每个来源跑
// `WHERE device_id = ? ORDER BY timestamp ASC/DESC LIMIT 1`（handler 的
// /:id/data-summary）。上面的复合索引都带 sensor_name（unified_data）或
// created_at（device_data），对 **ORDER BY timestamp** 帮不上忙；单列 timestamp
// 索引只能「扫时间序 + Filter device_id」，对已停写的老设备（DESC 要扫过比它新的
// 全部行）代价随「新数据量」线性增长 —— data-summary 的核心对象恰是这类老设备。
// 补 (device_id, timestamp) 两条复合索引，让该形态变成有序索引扫描、命中即停。
//
// INCLUDE 仅在 PG 支持；SQLite 无 INCLUDE，故用两条模板按方言选择（见下）。
var partitionParentIndexes = map[string][]string{
	"unified_data": {
		// ── 阶段0 死索引清理（遥测存储优化方案 §7 阶段0 / §1.3 第 2 条）──
		// 删掉两条**没有读路径**的索引:
		//   · sensor_name_idx —— 全仓没有以 sensor_name 为**前导**列的查询。
		//     历史图表 / historical-batch / categories 一律 `device_id = ? AND
		//     sensor_name = ?`（handler_data.go:300/395、history_points.go:264），
		//     由下面的 (device_id, sensor_name, timestamp) 完全覆盖；单列索引
		//     只剩写入与 autovacuum 成本。
		//   · edge_device_id_idx —— 该列在生产上**全 NULL**（写入路径只写
		//     device_id，见 consumers_heavy.go 的 UnifiedData 构造），索引对任何
		//     查询都无选择性，只是每行 8 字节 + 每次 INSERT 维护。
		// ⚠ 删模板**不会**自动 DROP 存量库上的索引（ensurePartitionParentIndexes
		// 只补建、不删除）⇒ 存量清理见本文件尾部 retireDeadTimeseriesIndexes。
		//
		// ── task-20 索引瘦身（2026-11-07）──
		// 再删一条 device_id 单列索引（生产 773 MB）:
		// 它是**复合索引的严格前缀**，任何设备维度等值查询都能由下列任意
		// (device_id, ...) 复合索引以同样代价的 index-only scan 完成 —— 单列版只多了
		// 一份写入与 autovacuum 成本。EXPLAIN 前后对比见
		// partition_dead_index_test.go 的 TestSingleColumnDeviceIDIndexIsPrefixCovered。
		"CREATE INDEX %s_timestamp_idx ON %s (timestamp)",
		"CREATE INDEX %s_device_sensor_ts_idx ON %s (device_id, sensor_name, timestamp)",
		// ORDER BY timestamp 的设备维度边界查询（data-summary / 时间范围）。
		"CREATE INDEX %s_device_ts_idx ON %s (device_id, timestamp)",
	},
	"device_data": {
		// 阶段0 死索引清理: 删 node_id_idx / edge_device_id_idx。
		//   · node_id_idx —— 唯一按 node_id 过滤的读路径是 GET /nodes/:id/data
		//     （handler_node.go:296），前端零调用者（docs/分析/功能模块与业务功能
		//     实测盘点-2026-09-23.md:163「无包装、无调用者」），且该端点同时混着
		//     两种数据形状，已列为待裁决项（S3 已知遗留）。
		//   · edge_device_id_idx —— 与 unified_data 同名索引同因: 该列在生产上
		//     全 NULL（写入只写 device_id / node_id）。
		//
		// ── task-20 索引瘦身（2026-11-07）──
		// 删 device_id 单列索引（生产 57 MB）: 被 (device_id, created_at DESC) 与
		// (device_id, timestamp) 双向前缀覆盖。实测删前删后三条关键查询
		// （latest-data / 分页 / data-summary）计划完全不变。
		"CREATE INDEX %s_timestamp_idx ON %s (timestamp)",
		"CREATE INDEX %s_device_created_idx ON %s (device_id, created_at DESC)",
		// ORDER BY timestamp 的设备维度边界查询（data-summary / 时间范围）。
		"CREATE INDEX %s_device_ts_idx ON %s (device_id, timestamp)",
	},
	// telemetry_frame: **只保留 2 条索引**（方案 §4.1 标定）。
	//
	// 现状 unified_data 每帧 29 行 × 5 条索引 = 一帧要维护 145 次索引项，
	// 这是 §1.3 里 300–400 倍放大的第二个来源。帧级表一帧只有一行，
	// 索引天然少；这里刻意**不再**补 sensor_name / edge_device_id / node_id
	// 索引（实测无读路径，方案 §4.1 原话）。
	//
	//   · 主键 (device_id, ts, seq) —— PG 自动建，覆盖「按设备取时间窗」与
	//     LatestSnapshot 的 "device_id = ? AND frame_kind = 1 ORDER BY ts DESC"；
	//   · 关键帧部分索引 (device_id, ts) WHERE frame_kind = 1 —— LatestSnapshot
	//     要"该设备最后一个关键帧"，没有它就得把 24h 全部帧扫一遍才能找到锚点
	//     (device14 一天 86,342 帧 ⇒ 每次 /latest-data 扫 8.6 万行)。
	//     部分索引只收录关键帧（≈ 每 slot 每 60s 一条），体积约是全表的 1/60。
	//
	// 注意 telemetry_rollup_1m 的空清单也必须登记（见其注释：未登记表会沿用
	// unified_data 的索引清单，而宽行表没有那些列）。
	"telemetry_frame": {
		"CREATE INDEX %s_device_ts_key_idx ON %s (device_id, ts) WHERE frame_kind = 1",
	},
	// telemetry_rollup_1m: **无附加索引**（宽行预聚合，方案 §5.2 标定）。
	//
	// 为什么空清单也必须在 map 里登记: createPartitionedParent 的
	// effectiveParentIndexTemplates 对**未登记表回退到 unified_data** 的索引清单
	// (见 createPartitionedParent 注记 ddlTable 的取值)。不登记 ⇒ 宽行表会被套上
	// unified_data **当前登记的全部**索引模板（数量随阶段0 清理变动，不要在这里
	// 写死条数 —— 2026-11-06 阶段0 删掉 4 条死索引后它从 6 条降到 4 条），其中
	// sensor_name / timestamp / created_at / edge_device_id 等列在宽行表里
	// **不存在**，建表直接报错 (partitionParentDDL 里该表只有
	// device_id/bucket/payload 三列)。
	//
	// 同理: 本表也不在 deadTimeseriesIndexes 里登记 —— 阶段0 的死索引清单只针对
	// unified_data / device_data，本表没有"待退役的历史索引"，finalize 对它是
	// no-op（这正是期望行为，别为了"对称"塞空条目）。
	//
	// 主键 (device_id, bucket) 本身就是唯一需要的访问路径: 长窗图表恒为
	// device_id 等值 + bucket 有序范围扫描 (分区裁剪 + 主键索引前导列命中)，
	// 再加二级索引只会放大写入成本 (方案 §4.1「只保留 2 条索引」的同一条理由)。
	"telemetry_rollup_1m": {},
}

// partitionParentIndexesPG 是 partitionParentIndexes 的 PostgreSQL 增强版：
// 多出的 INCLUDE (value) 让「按设备+字段取时间窗内的值」变成 index-only scan。
// SQLite 不支持 INCLUDE，因此该方言沿用上一份（无 INCLUDE 但含同样的复合列）。
var partitionParentIndexesPG = map[string][]string{
	"unified_data": {
		"CREATE INDEX %s_device_sensor_ts_idx ON %s (device_id, sensor_name, timestamp) INCLUDE (value)",
	},
}

// partitionCopyColumns maps a logical table name to the column list the
// migration INSERT...SELECT copies. 两张表列集不同, 必须按表生成 (否则列不存在
// 或列数不匹配)。
var partitionCopyColumns = map[string]string{
	"unified_data": "id, device_id, sensor_name, value, unit, timestamp, created_at, edge_device_id",
	"device_data":  "id, device_id, node_id, data_json, timestamp, created_at, edge_device_id",
	// telemetry_frame: 新表无存量平表可迁（建表即分区），登记列清单只为
	// 「四处同改」的一致性纪律（方案 §8 风险表最后一条）：partitionParentDDL /
	// partitionParentIndexes / partitionCopyColumns / models 四处必须同源，
	// 少一处就会在下次启动时静默漂移。缺省回退是 unified_data 的列清单，
	// 对帧表来说是错的（没有 id/sensor_name/value 这些列）。
	"telemetry_frame": "device_id, ts, seq, frame_kind, slot_id, payload, raw",
}

// createPartitionedParent creates the partitioned parent for logical table
// table, physically named name (table on the fresh path, table+"_new" during
// legacy migration), with the composite PK (id, timestamp) and per-partition
// indexes inherited via the parent definition. When fresh=true it also creates
// the first partitions.
func createPartitionedParent(db *gorm.DB, table, name string, fresh bool) error {
	// 未登记表沿用 unified_data 的列结构与索引清单（与泛化探针测试及历史硬编码
	// 行为兼容）；登记表（unified_data / device_data）按各自定义。
	ddlTable := table
	if _, ok := partitionParentDDL[ddlTable]; !ok {
		ddlTable = partitionedTable
	}
	if err := db.Exec(fmt.Sprintf(partitionParentDDL[ddlTable], name)).Error; err != nil {
		return err
	}
	// 既有 b-tree 索引在分区级重建（与 models.go 原 index 定义一致）。
	//
	// 方言增强（2026-10-24 P1）：PG 上补 INCLUDE (value) 让「按设备+字段取时间窗
	// 内取值」成为 index-only scan；SQLite 无 INCLUDE 语法，因此只在 PG 追加。
	// 两条模板用同名索引，必须先跑通用版再跑 PG 版会撞名 —— 所以 PG 版是
	// **替换**而非追加。清单与替换规则统一由 effectiveParentIndexTemplates 给出，
	// 建表路径与「已分区库幂等补齐」路径（ensurePartitionParentIndexes）共用，
	// 避免两处各抄一套后静默漂移。
	for _, tmpl := range effectiveParentIndexTemplates(db, ddlTable, name) {
		if err := db.Exec(fmt.Sprintf(tmpl, name, name)).Error; err != nil {
			return err
		}
	}
	if fresh {
		pm := &PartitionManager{db: db}
		if err := pm.EnsurePartitionsFor(name, partitionRollaheadMonths); err != nil {
			return err
		}
	}
	return nil
}

// oldestRowMonthFor returns the month of the earliest row in table, or zero
// time if empty.
func oldestRowMonthFor(db *gorm.DB, table string) time.Time {
	var ts *time.Time
	db.Raw("SELECT min(timestamp) FROM " + table).Scan(&ts)
	if ts == nil {
		return time.Time{}
	}
	return monthStart(*ts)
}

// copyMonthBatched copies one calendar month from src into dst in batches of
// migrateBatchSizePostgres. 断点续跑: (id,timestamp) 主键 + ON CONFLICT DO
// NOTHING, 重跑幂等。
//
// 批次上界必须取自"本批实际扫描到的行的最大 id", 而不是"本月剩余行的
// max(id)"。后者会在 id 稀疏时一次跳到整月末: 第一批只覆盖很小一段 id,
// 但水位被设为整月最大 id, 下一批 `id > watermark` 无行可搬而提前
// 退出。真实开发库实测因此只搬了恰好一批 (src=87487 dst=10000, id 稀疏)。
// 这里先用 `... ORDER BY id LIMIT N` 的子查询固定本批上界 batchMax, 再搬迁
// (watermark, batchMax] 区间; ON CONFLICT 跳过的行仍计入 batchMax, 因此水位
// 推进与 RowsAffected 完全解耦。
//
// 返回值 moved 的语义（**不要当成精确行数用**）:
//
//	⚠ "已搬 N 行" == "已**尝试**搬运 N 行"。每批计入的量是 min(本批前剩余行数, 批大小)，
//	  也就是水位 (watermark, batchMax] 区间覆盖的行数 —— **包含**被 ON CONFLICT DO
//	  NOTHING 跳过（断点续跑时上次已搬过）的那些行。
//
//	为什么刻意这么定义: 进度该反映"还有多少工作没做完"，而 ON CONFLICT 跳过的行同样
//	要扫、要判、要推进水位，它们不是"省下来的工作"。用 RowsAffected 反而会低估、
//	并在断点续跑时让进度条倒着走。
//	为什么不做精确 count: 每批一次 count(*) 在生产月分区上是上千次全扫描，
//	为一条进度文案付出这个代价不值得。
//
// 为什么要返回它（2026-11-06 启动期进度契约 §3.2）:
//
//	· detail 要求形如 "202611 第 3/7 批 已搬 41,000 行"，行数只能从本函数内部拿到；
//	· 该函数此前**一条进度日志都没有**（每批算了 remaining 却从不输出），
//	  是启动期最黑的一段（生产单月可达数千万行）。
//
// 契约约束（Lead 批复）: 行为与日志一字不变；返回值不参与任何控制流
// （所有错误仍走 err，moved 只在成功返回时被调用方用于文案）。
func copyMonthBatched(db *gorm.DB, dst, src string, month time.Time, prog monthProgress) (int64, error) {
	cols := partitionCopyColumns[src]
	if cols == "" {
		// 未登记表沿用 unified_data 列清单 (见 createPartitionedParent 注记)。
		cols = partitionCopyColumns[partitionedTable]
	}
	start := monthStart(month)
	end := addMonths(month, 1)
	watermark := int64(0)
	// moved / movedTotal 只服务进度文案（契约 §3.2），不参与任何退出/水位判定。
	var movedTotal int64
	for {
		// 结束判定基于源表剩余行数, 与批大小/被 ON CONFLICT 跳过的行数无关:
		// 只要本月还有 id > watermark 的行, 循环就继续。
		var remaining int64
		if err := db.Raw(fmt.Sprintf(
			"SELECT count(*) FROM %s WHERE id > ? AND timestamp >= ? AND timestamp < ?",
			src),
			watermark, start, end,
		).Scan(&remaining).Error; err != nil {
			return movedTotal, err
		}
		if remaining == 0 {
			return movedTotal, nil // 本月搬完
		}
		// 本批上界: 本月 id > watermark 的最小 N 行中的最大 id (无行则 0)。
		var batchMax int64
		if err := db.Raw(fmt.Sprintf(
			`SELECT COALESCE(max(id), 0) FROM (
				SELECT id FROM %s
				WHERE id > ? AND timestamp >= ? AND timestamp < ?
				ORDER BY id LIMIT %d
			) AS batch`,
			src, migrateBatchSizePostgres),
			watermark, start, end,
		).Scan(&batchMax).Error; err != nil {
			return movedTotal, err
		}
		// 无进展保护: 有剩余行却拿不到更大的上界 (理论上不可达) → 报错退出,
		// 避免水位不前进时死循环。
		if batchMax <= watermark {
			return movedTotal, fmt.Errorf("copyMonthBatched: no progress for %s month %s (watermark=%d remaining=%d)",
				src, start.Format("2006-01"), watermark, remaining)
		}
		// 搬迁 (watermark, batchMax] 区间的全部行。ON CONFLICT 只负责幂等,
		// 不参与退出/水位判定。
		if err := db.Exec(fmt.Sprintf(
			`INSERT INTO %s (%s)
			 SELECT %s
			 FROM %s
			 WHERE id > ? AND id <= ? AND timestamp >= ? AND timestamp < ?
			 ON CONFLICT (id, timestamp) DO NOTHING`,
			dst, cols, cols, src),
			watermark, batchMax, start, end,
		).Error; err != nil {
			return movedTotal, err
		}
		watermark = batchMax

		// 每批结束上报一次（本函数此前完全没有日志/进度，是启动期最黑的一段）。
		//
		// 行数**不额外查询**：本批扫描的行数就是 min(remaining, 批大小) ——
		// batchMax 取的是"本月 id > watermark 的前 N 行中的最大 id"，所以
		// (watermark, batchMax] 区间恰含 min(remaining, N) 行。为一条进度文案
		// 再 count(*) 一次会在生产上多出成百上千次全分区扫描，不值得。
		// ON CONFLICT 跳过的行也计入（它们是"已尝试搬运"的量，正是不想重复做的工作）。
		batchMoved := remaining
		if batchMoved > migrateBatchSizePostgres {
			batchMoved = migrateBatchSizePostgres
		}
		movedTotal += batchMoved
		monthRemaining := remaining - batchMoved
		if monthRemaining < 0 {
			monthRemaining = 0
		}
		// 月内比例 = 已搬 / (已搬 + 本月剩余)。单调不减且必然收敛到 1。
		frac := 1.0
		if total := movedTotal + monthRemaining; total > 0 {
			frac = float64(movedTotal) / float64(total)
		}
		prog.report(frac, fmt.Sprintf("%s 已搬 %s 行（本月剩 %s）",
			prog.label, commatize(movedTotal), commatize(monthRemaining)))
	}
}

// ==================== 阶段0: 死索引退役（遥测存储优化方案 §7 阶段0）====================
//
// # 为什么需要「删模板」之外的这一步
//
// partitionParentIndexes 只是**建索引的模板**：删掉模板只影响"之后新造的库"，
// 存量生产库上的索引不会被自动删除（ensurePartitionParentIndexes 只补建、不删除，
// 这是有意的最小惊讶原则）。而方案 §1.3 指出的 4 条死索引恰恰是**存量成本**：
// 生产 70M 行 × 4 条索引 × (8B 键 + 24B 行指针 + 页开销) ≈ 数 GB 磁盘 + 每次
// INSERT 的 4 次索引维护。因此需要一次**幂等、只删不建**的退役步骤。
//
// # 判据是「列清单完全相等」，不是索引名
//
// 历史迁移路径（<table>_new 中转）建的索引名带 _new_ 前缀，PG 的
// ALTER TABLE ... RENAME TO 不会重命名索引 —— 按名字匹配会漏掉生产库里真正
// 存在的那几条。因此这里读 pg_index 的列清单，**完全相等**才删：
//   · (sensor_name)     不是 (device_id, sensor_name, timestamp) 的前缀误判；
//   · (edge_device_id)  同理不会被当成有效复合索引；
//   · (device_id) 等仍在读路径上的索引**绝不**出现在清单里。
//
// # 失败处理: 只 Warn 不阻断启动
//
// DROP INDEX 取 ACCESS EXCLUSIVE 锁；若恰有长事务占锁，这里失败。但"没删掉一条
// 死索引"只是空间没省下来，绝不能因此让整个服务起不来（那会把一个空间优化
// 变成可用性事故）。因此: 目录查询失败 → 返回错误（说明 schema 异常，要人看）;
// 单条 DROP 失败 → Warn + 计数器 + 继续（下次启动再试，幂等）。

// deadTimeseriesIndexes 是阶段0 要退役的「死索引」清单: 表 → 索引列清单（完全相等才删）。
//
// 每条的依据见 partitionParentIndexes 里对应条目上方的注释。清单一旦发布就应当
// 只减不增 —— 这里加一条等于"每次启动都去删一条索引"，必须先在评审里过一遍。
var deadTimeseriesIndexes = map[string][][]string{
	"unified_data": {
		{"sensor_name"},
		{"edge_device_id"},
		// task-20 (2026-11-07): device_id 单列索引, 生产 773 MB。
		// 判据同样按**列清单**（不是索引名），与 ensurePartitionParentIndexes 的
		// 判定口径一致。删它的安全性由 EXPLAIN 实测背书: 见
		// partition_dead_index_test.go 的 TestSingleColumnDeviceIDIndexIsPrefixCovered。
		{"device_id"},
	},
	"device_data": {
		{"node_id"},
		{"edge_device_id"},
		// task-20: 生产 57 MB, 被 (device_id, created_at DESC) 前缀覆盖。
		{"device_id"},
	},
}

// finalizePartitionParentIndexes = 补建缺失的父表索引 + 退役死索引。
//
// 两件事的顺序有意义: 先补建（保证读路径的索引在位），再删死索引 —— 万一将来
// 「新模板」与「死索引清单」出现重叠，先补后删至少不会留下"该有的没建、旧的已删"
// 的空窗。
func finalizePartitionParentIndexes(db *gorm.DB, table string) error {
	// 列校正**必须排在最前**：DDL 缺列时后面两步都可能炸（索引模板引用不存在的列），
	// 且「补列」是唯一的正确修复方向（补索引不能替代补列）。
	//
	// 失败语义 = Warn 不阻断（与 retireDeadTimeseriesIndexes 同口径，Lead 2026-11-07 裁决）：
	// 补列修的是「schema 漂移」，漂移的后果是某张表写入失败，不是服务不可用；
	// 挡启动等于把小故障放大成停机。真正的拦截点在开发期 —— TestPartitionDDLMatchesModels
	// 是纯源码门禁（不需要 PG），DDL 与 model 一漂移就在 CI 红。
	if err := ensurePartitionParentColumns(db, table); err != nil {
		slog.Warn("partition_mgr: reconcile partitioned parent columns failed (non-fatal, continuing startup)",
			"table", table, "error", err)
	}
	if err := ensurePartitionParentIndexes(db, table); err != nil {
		return err
	}
	return retireDeadTimeseriesIndexes(db, table)
}

// retireDeadTimeseriesIndexes 幂等删除 table 上的死索引（见 deadTimeseriesIndexes）。
// 非 PG 方言 / 未登记表 / 索引不存在 ⇒ 直接返回 nil（零 SQL 副作用之外的开销）。
func retireDeadTimeseriesIndexes(db *gorm.DB, table string) error {
	if !isPostgres(db) {
		return nil // 分区索引只存在于 PG；SQLite 测试库 no-op
	}
	wants, ok := deadTimeseriesIndexes[table]
	if !ok || len(wants) == 0 {
		return nil
	}
	indexes, err := partitionIndexColumns(db, table)
	if err != nil {
		return err
	}
	// 先收集待删清单，用于把进度折算成"第 k/n 条"。本次 DROP 失败会 continue
	// （见下方「失败处理」），因此 k 按**尝试序号**计而不是按成功数计 ——
	// 进度条只该反映"打到第几条"，不该因为一条失败就倒退。
	var doomed []partitionIndexInfo
	for _, idx := range indexes {
		if matchesAnyColumnList(idx.columns, wants) {
			doomed = append(doomed, idx)
		}
	}
	total := len(doomed)
	for i, idx := range doomed {
		before := float64(i) / float64(total) * 100
		after := float64(i+1) / float64(total) * 100
		reportMigrationProgress(StepIDRetireDeadIndexes, before,
			fmt.Sprintf("%s 第 %d/%d 条死索引：%s", table, i+1, total, idx.name))
		stmt := fmt.Sprintf("DROP INDEX IF EXISTS %s", quoteIdent(idx.name))
		if err := db.Exec(stmt).Error; err != nil {
			// 只 Warn: 见文件头「失败处理」。
			metrics.LifecycleTaskFailures.WithLabelValues("partition").Inc()
			slog.Warn("partition_mgr: drop dead index failed (will retry on next start)",
				"table", table, "index", idx.name, "columns", idx.columns, "error", err)
			continue
		}
		slog.Info("partition_mgr: dropped dead index (阶段0 遥测存储优化)",
			"table", table, "index", idx.name, "columns", idx.columns)
		reportMigrationProgress(StepIDRetireDeadIndexes, after,
			fmt.Sprintf("%s 第 %d/%d 条死索引已 DROP：%s", table, i+1, total, idx.name))
	}
	return nil
}

// partitionIndexInfo 是一个索引的名字与列清单（小写、去引号，便于与模板比对）。
type partitionIndexInfo struct {
	name    string
	columns []string
}

// partitionIndexColumns 列出 table 在当前 schema 下所有有效索引的列清单。
//
// 用 pg_get_indexdef 解析列清单而不是拼 indkey: INCLUDE 列与表达式索引的
// 处理差异会让 indkey 路线难以正确区分"索引列"与"包含列"，而 indexdef 的
// 第一组括号恰好就是索引列（与 indexColumnsOf 的既有约定一致）。
func partitionIndexColumns(db *gorm.DB, table string) ([]partitionIndexInfo, error) {
	var defs []string
	query := "SELECT pg_get_indexdef(x.indexrelid) " +
		"FROM pg_index x " +
		"JOIN pg_class t ON t.oid = x.indrelid " +
		"JOIN pg_namespace n ON n.oid = t.relnamespace " +
		"WHERE t.relname = ? AND n.nspname = current_schema() AND x.indisvalid"
	if err := db.Raw(query, table).Scan(&defs).Error; err != nil {
		return nil, fmt.Errorf("list indexes on %s for retirement: %w", table, err)
	}
	out := make([]partitionIndexInfo, 0, len(defs))
	for _, def := range defs {
		name, cols, ok := parseIndexDef(def)
		if !ok {
			continue
		}
		out = append(out, partitionIndexInfo{name: name, columns: cols})
	}
	return out, nil
}

// parseIndexDef 从 pg_get_indexdef 的输出里取出索引名与第一组列清单。
// 形如: CREATE INDEX unified_data_sensor_name_idx ON public.unified_data USING btree (sensor_name)
//
// ⚠ **唯一索引一律返回 ok=false**（含主键索引）: 唯一索引承载的是**约束**，
// 不是"一条查询加速路径"。退役死索引的前提是"删了只损失写入成本"，而删掉唯一
// 索引会让重复数据静默变得可插入（本仓 data_sources 的 uniq_ds_source 就踩过
// 同类事故，见 database/gorm.go 里 RetireLogicalDeviceLayer 顺序注释）。
// 死索引清单永远不该包含唯一索引，这里用解析层再加一道硬门。
func parseIndexDef(def string) (name string, columns []string, ok bool) {
	const plain = "CREATE INDEX "
	if strings.HasPrefix(def, "CREATE UNIQUE INDEX ") {
		return "", nil, false // 唯一索引 / 主键: 绝不参与退役
	}
	if !strings.HasPrefix(def, plain) {
		return "", nil, false
	}
	rest := def[len(plain):]
	space := strings.IndexByte(rest, ' ')
	if space <= 0 {
		return "", nil, false
	}
	name = rest[:space]
	open := strings.IndexByte(def, '(')
	if open < 0 {
		return "", nil, false
	}
	tail := def[open:]
	closeIdx := strings.IndexByte(tail, ')')
	if closeIdx <= 0 {
		return "", nil, false
	}
	// 索引列里可能带 ASC/DESC/NULLS/opclass 与引号: 复用 normalizeIndexColumns
	// 归一（去引号 + 小写 + 折叠空白），再按逗号切、取每段第一个 token。
	normalized := normalizeIndexColumns(tail[1:closeIdx])
	for _, part := range strings.Split(normalized, ",") {
		fields := strings.Fields(part)
		if len(fields) == 0 {
			continue
		}
		columns = append(columns, fields[0])
	}
	return name, columns, len(columns) > 0
}

// matchesAnyColumnList 报告 columns 是否与 wants 中某一项**完全相等**（顺序敏感）。
func matchesAnyColumnList(columns []string, wants [][]string) bool {
	for _, want := range wants {
		if len(columns) != len(want) {
			continue
		}
		equal := true
		for i := range want {
			if columns[i] != want[i] {
				equal = false
				break
			}
		}
		if equal {
			return true
		}
	}
	return false
}

// quoteIdent 给 PG 标识符加双引号（内部双引号转义）。
// 索引名来自系统目录，正常不会含怪字符；这里仍按规范转义，避免 SQL 注入面。
func quoteIdent(name string) string {
	return `"` + strings.ReplaceAll(name, `"`, `""`) + `"`
}
