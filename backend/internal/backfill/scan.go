package backfill

// scan.go — 分区发现、复合游标分页、按帧取行（契约 §4.4 step a/b）。
//
// ============ ⚠ 2026-11-07 P0：游标绝不能依赖时间精度 ============
//
// 第一版用 `watermark + 1ns` 推进游标，SQLite 上全绿（INT 时间是纳秒级，
// 加 1ns 真的前进），**PG 上生产会无限重复处理同一批**：
//
//	SELECT (timestamptz '2026-10-05 00:00:00+00' + interval '0.000000001 second')
//	     = timestamptz '2026-10-05 00:00:00+00';   -- t （加 1ns 等于没加）
//	SELECT ... + interval '0.000001 second'  = ...; -- f （PG timestamptz 精度是微秒）
//
// 症状：`WHERE timestamp >= from` 每轮取回同一批 ⇒ 回填永不完成、一个核 100%、
// 磁盘 IO 持续、frames_done 涨到源数据帧数的上百倍，且**不报错**。
//
// 因此游标改成 **(timestamp, device_id) 复合游标**（cursor 类型）：
//
//	WHERE timestamp > ? OR (timestamp = ? AND device_id > ?)
//
// 三条好处：
//  1. **不依赖时间精度** —— PG 微秒 / SQLite 纳秒 / 未来任何精度都正确；
//  2. 天然处理「同一 ts 多设备」⇒ 不再需要「前瞻取 1 条 + truncateAtCleanBoundary」
//     那套为了绕开精度问题而写的补丁（已整体删除）；
//  3. 续跑语义直白：watermark 是 (ts, device_id) 二元组，没有 +1ns 的 hack。
//
// 为什么用 `> ? OR (= ? AND > ?)` 而不是行值语法 `(a,b) > (?,?)`：
// **SQLite 不支持行值比较**，用它就得按方言分支，而分支意味着 PG 那条路径
// 在 SQLite 单测里永远跑不到 —— 正是本次 bug 的形状（两条路径各自自洽、交叉点
// 没有用例）。拆开写的两方言语义完全一致，且都能被同一批用例覆盖。

import (
	"context"
	"fmt"
	"sort"
	"time"

	"ehome/backend/internal/datalifecycle"
)

// cursor 是回填的**复合游标**：(timestamp, device_id)。
//
// 零值（TS 为零时间 && DeviceID==0）表示「从分区起点开始」。
//
// 为什么必须带 device_id：帧键按 (timestamp, device_id) 排序，一批在中间截断时
// 最后一个已处理帧是 (ts, dev)；若游标只记 ts，下一批用 `timestamp > ts` 就会
// **跳过同一 ts 内 device_id 更大的帧**（静默丢数据，且完整性校验要跑完整个
// 分区才会发现）。
type cursor struct {
	TS       time.Time
	DeviceID uint
}

// isZero 报告是否为零值（无断点）。
func (c cursor) isZero() bool { return c.TS.IsZero() && c.DeviceID == 0 }

// String 供日志/错误信息用。
func (c cursor) String() string {
	if c.isZero() {
		return "<start>"
	}
	return fmt.Sprintf("(%s, dev=%d)", c.TS.UTC().Format(time.RFC3339Nano), c.DeviceID)
}

// parsePartitionMonth 从 <table>_YYYYMM 解析月份。
func parsePartitionMonth(partName, tableName string) (time.Time, bool) {
	prefix := tableName + "_"
	if len(partName) != len(prefix)+6 {
		return time.Time{}, false
	}
	if partName[:len(prefix)] != prefix {
		return time.Time{}, false
	}
	m, err := time.Parse("200601", partName[len(prefix):])
	if err != nil {
		return time.Time{}, false
	}
	return m.UTC(), true
}

// partition 是一个待回填的月分区及其时间范围。
//
// HiInclusive 区分两种上界的**语义**，因为时间精度不能用来做 +1 运算：
//
//	月分区: [lo, hi)  —— hi 是下个月 1 号 0 点，月份算术是精确的；
//	整表降级: [lo, hi] —— hi 是表里最大的 timestamp，必须**闭区间**。
//
// 第一版对第二种用 `hi.Add(time.Nanosecond)` 求闭区间上界 —— PG 上等于没加，
// 恰好等于 max(ts) 的最后一行取不到。这是本次 P0 的第二个面（方向相反、同样致命）。
type partition struct {
	name        string
	lo          time.Time
	hi          time.Time
	hiInclusive bool

	// rows 是该分区的**总行数**（发现阶段用 count(*) 实测一次），用于进度加权。
	//
	// ⚠ 2026-11-07 修复：进度百分比原本按**时间跨度**加权，而生产 6 个月分区里
	// 只有 1~2 个有数据、其余 4 个是空分区（0 行）。按时间跨度等权 ⇒ 分母恒为
	// 6 个月、分子最多 2 个月 ⇒ **percent 永远到不了 34%**，而实际帧数早已过半。
	// 空分区的时间跨度不是"工作"，不该占权重；唯一的正确权重是**数据量**。
	rows int64
}

// span 返回该分区的时间跨度。
func (p partition) span() time.Duration { return p.hi.Sub(p.lo) }

// upperOp 返回上界比较符（开区间用 <，闭区间用 <=）。
func (p partition) upperOp() string {
	if p.hiInclusive {
		return "<="
	}
	return "<"
}

// discoverPartitions 列出待回填的区间。
//
// PG 上优先按**月分区**切；但若源表还是平表（迁移未跑 / 测试库 AutoMigrate 建的），
// 必须退化为整表一个区间 —— 否则 PG 用例里 `relname LIKE 'unified_data_%'`
// 一条都匹配不到，回填直接「done」，用例变成永远绿的假绿。
func (r *Runner) discoverPartitions(ctx context.Context) ([]partition, error) {
	if !r.isPostgres() || !r.isSourceTablePartitioned(ctx) {
		return r.discoverSingleRange(ctx)
	}
	var names []string
	if err := r.db.WithContext(ctx).Raw(
		"SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace "+
			"WHERE c.relkind = 'r' AND n.nspname = current_schema() AND c.relname LIKE ? "+
			"ORDER BY c.relname", r.srcTable()+"_%").Scan(&names).Error; err != nil {
		return nil, fmt.Errorf("list partitions of %s: %w", r.srcTable(), err)
	}
	outs := make([]partition, 0, len(names))
	for _, n := range names {
		month, ok := parsePartitionMonth(n, r.srcTable())
		if !ok {
			continue // _legacy / _new 之类不是月分区
		}
		outs = append(outs, partition{name: n, lo: month, hi: month.AddDate(0, 1, 0)})
	}
	if len(outs) == 0 {
		return r.discoverSingleRange(ctx)
	}
	sort.Slice(outs, func(i, j int) bool { return outs[i].lo.Before(outs[j].lo) })
	return r.fillPartitionRows(ctx, outs)
}

// isSourceTablePartitioned 报告源表在 PG 上是否为分区母表（relkind=p）。
func (r *Runner) isSourceTablePartitioned(ctx context.Context) bool {
	var relkind string
	if err := r.db.WithContext(ctx).Raw(
		"SELECT c.relkind FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace "+
			"WHERE c.relname = ? AND n.nspname = current_schema()", r.srcTable()).Scan(&relkind).Error; err != nil {
		return false
	}
	return relkind == "p"
}

// discoverSingleRange 是非分区表 / 非 PG 的降级路径：整表一个区间。
//
// ⚠ 不用 min()/max() 聚合：SQLite 把 timestamp 聚合成**文本**，驱动不会把它
// 转回 time.Time（实测报 unsupported Scan, storing driver.Value type string into
// type *time.Time）。两端各取一行（ORDER BY ... LIMIT 1）走索引，成本 O(log n)。
//
// ⚠⚠ 上界是**闭区间**（hiInclusive=true），绝不做 `hi.Add(1ns)`：PG timestamptz
// 精度是微秒，加 1ns 等于没加，恰好等于 max(ts) 的那一行会被 `timestamp < hi`
// 漏掉 —— 那就是「少了最后一帧」的静默丢数据。
func (r *Runner) discoverSingleRange(ctx context.Context) ([]partition, error) {
	var lo, hi []time.Time
	if err := r.db.WithContext(ctx).Table(r.srcTable()).
		Order("timestamp ASC").Limit(1).Pluck("timestamp", &lo).Error; err != nil {
		return nil, fmt.Errorf("probe %s lower bound: %w", r.srcTable(), err)
	}
	if len(lo) == 0 {
		return nil, nil // 空表：没有要回填的东西
	}
	if err := r.db.WithContext(ctx).Table(r.srcTable()).
		Order("timestamp DESC").Limit(1).Pluck("timestamp", &hi).Error; err != nil {
		return nil, fmt.Errorf("probe %s upper bound: %w", r.srcTable(), err)
	}
	if len(hi) == 0 {
		return nil, nil
	}
	return r.fillPartitionRows(ctx, []partition{{
		name:        r.srcTable() + "_all",
		lo:          lo[0].UTC(),
		hi:          hi[0].UTC(),
		hiInclusive: true,
	}})
}

// fillPartitionRows 用 count(*) 实测每个分区的行数，作为进度百分比的分母权重。
//
// ============ 为什么必须按**数据量**加权，而不是时间跨度 ============
//
// 生产实测（2026-11-07）：6 个月分区里只有 202610/202611 有数据，其余 4 个是
// **空分区**（0 行）。原来的 recomputePercent 按时间跨度等权 ⇒
//
//	分母 = 6 个月；分子最多 = 有数据的那 1~2 个月；
//	⇒ percent 天花板 = 2/6 ≈ 33%，而实际帧数早已过半（45.2%）。
//
// 空分区的时间跨度**不是工作量**。一个"整个月没有一行数据"的分区，等它回填
// 只需要 1 次空查询（毫秒级），凭什么占 1/6 的进度权重？
//
// 而且这个 bug **不能**靠"把空分区也标 done 同步到内存"来修：那会让 4 个空
// 分区在启动瞬间各自贡献 1/6 ⇒ 进度条一开局就跳到 66.7%，是另一种谎报。
// 唯一的正确权重是数据量。
//
// 成本：PG 上这是**一次性的全表 count(*)**（生产 72.7M 行 ≈ 1.5s，相对 20~40
// 分钟的回填任务可忽略）。不做成估算（reltuples）：估算值在刚建的分区上是 0
// 或负一，会让进度条在开局显示 0% 或负数，比慢 1.5 秒糟糕得多。
func (r *Runner) fillPartitionRows(ctx context.Context, parts []partition) ([]partition, error) {
	for i := range parts {
		n, err := r.countRows(ctx, parts[i])
		if err != nil {
			// 计数失败**不阻断回填**：进度条不准比回填不跑好得多。
			// 失败的分区 rows 保持 0 ⇒ recomputePercent 自动退回时间跨度加权。
			r.addError("统计分区 %s 行数失败（进度将退回按时间跨度估算）: %v", parts[i].name, err)
			continue
		}
		parts[i].rows = n
	}
	return parts, nil
}

// countRows 统计一个分区（或整表降级场景下的整表）的行数。
func (r *Runner) countRows(ctx context.Context, p partition) (int64, error) {
	var n int64
	err := r.db.WithContext(ctx).Raw(
		"SELECT count(*) FROM "+r.srcTable()+" WHERE timestamp >= ? AND timestamp "+p.upperOp()+" ?",
		p.lo, p.hi).Scan(&n).Error
	if err != nil {
		return 0, err
	}
	return n, nil
}

// srcTable 返回回填的源表名（本版只支持 unified_data）。
func (r *Runner) srcTable() string { return "unified_data" }

// isPostgres 报告方言。
func (r *Runner) isPostgres() bool {
	return r.db != nil && r.db.Dialector != nil && r.db.Dialector.Name() == "postgres"
}

// frameKey 是一帧的标识：(device_id, timestamp)。
//
// 它**不是**主键 —— unified_data 的主键是 (id, timestamp)，id 是单行的。
// 把 (device_id, timestamp) 去重后的每一个组合，才对应帧表的一行。
type frameKey struct {
	DeviceID uint
	TS       time.Time
}

// cursorOf 把帧键转成游标。
func cursorOf(k frameKey) cursor { return cursor{TS: k.TS, DeviceID: k.DeviceID} }

// fetchFrameKeys 取本批帧键（按 (timestamp, device_id) 升序）。
//
// 参数 from 是**复合游标**（严格大于语义），to 是分区上界（按 hiInclusive 决定开闭）。
// 返回 keys 与「最后一条已取到的游标」—— 调用方把它作为下一批的 from。
//
// 这里**不需要**任何前瞻/让位逻辑：复合游标本身就精确到 (ts, device_id)，
// 批边界落在哪里都不会重复或遗漏。
func (r *Runner) fetchFrameKeys(ctx context.Context, from cursor, to partition, limit int) ([]frameKey, error) {
	if limit <= 0 {
		limit = DefaultBatchFrames
	}
	var raw []struct {
		DeviceID uint
		TS       time.Time
	}
	// 复合游标条件：两方言通用（不用行值语法，SQLite 不支持）。
	where := "WHERE timestamp >= ? AND timestamp " + to.upperOp() + " ? "
	args := []interface{}{to.lo, to.hi}
	if !from.isZero() {
		where += "AND (timestamp > ? OR (timestamp = ? AND device_id > ?)) "
		args = append(args, from.TS, from.TS, from.DeviceID)
	}
	args = append(args, limit)
	err := r.db.WithContext(ctx).Raw(
		"SELECT device_id, timestamp AS ts FROM "+r.srcTable()+" "+where+
			"GROUP BY device_id, timestamp ORDER BY timestamp, device_id LIMIT ?",
		args...).Scan(&raw).Error
	if err != nil {
		return nil, fmt.Errorf("fetch frame keys after %s in %s: %w", from, to.name, err)
	}
	keys := make([]frameKey, len(raw))
	for i := range raw {
		keys[i] = frameKey{DeviceID: raw[i].DeviceID, TS: raw[i].TS.UTC()}
	}
	return keys, nil
}

// fetchFrameRows 取 [tsLo, tsHi] 闭区间内、指定设备的全部 unified_data 行。
//
// 为什么按**时间范围**取而不是按帧键列表拼 IN：
//
//	· 2000 帧拼 (device_id, timestamp) IN (...) 会有 4000 个参数，且跨方言行为
//	  不一致（SQLite 有变量数上限）；
//	· 范围查询多取到的行只是「同一毫秒内其它设备的帧」，量级很小，而收益是
//	  每批只一次往返。
//
// 上界用 `<=` 而不是 `<`：调用方传的 tsHi 是**本批最后一帧的 ts**（闭区间语义），
// 不做任何时间加法 —— 加法的精度陷阱见文件头。
func (r *Runner) fetchFrameRows(ctx context.Context, deviceIDs []uint, tsLo, tsHi time.Time) (map[frameKey][]rowValue, error) {
	if len(deviceIDs) == 0 {
		return nil, nil
	}
	var rows []struct {
		DeviceID   uint
		TS         time.Time
		SensorName string
		Value      float64
	}
	if err := r.db.WithContext(ctx).Raw(
		"SELECT device_id, timestamp AS ts, sensor_name, value FROM "+r.srcTable()+" "+
			"WHERE timestamp >= ? AND timestamp <= ? AND device_id IN ? "+
			"ORDER BY timestamp, device_id, sensor_name",
		tsLo, tsHi, deviceIDs).Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("fetch frame rows [%s,%s]: %w", tsLo.Format(time.RFC3339Nano), tsHi.Format(time.RFC3339Nano), err)
	}
	out := make(map[frameKey][]rowValue, len(rows)/16+1)
	for i := range rows {
		k := frameKey{DeviceID: rows[i].DeviceID, TS: rows[i].TS.UTC()}
		out[k] = append(out[k], rowValue{Name: rows[i].SensorName, Value: rows[i].Value})
	}
	return out, nil
}

// rowValue 是一帧里的一个字段读数。
type rowValue struct {
	Name  string
	Value float64
}

// 编译期断言：状态表常量确实存在于 datalifecycle（防拼写漂移）。
var _ = datalifecycle.BackfillDone
