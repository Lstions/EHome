package datalifecycle

// rollup_coverage.go — L3 rollup 的**覆盖元数据表**（task-22）。
//
// ============ 为什么必须有这张表，而不是"运行时算一下覆盖率" ============
//
// 长窗图表改读 rollup 之后，最危险的故障是「rollup 只覆盖了窗口的一部分」——
// 此时返回半截数据，用户看到图表中间凭空少一段。这比空图更难发现。
//
// 所以 `rollupHistoryPoints` 必须先判断「这个窗口的 rollup 是不是齐的」。
// 直觉做法是运行时算：窗口 [from,to) 应有 N 个分钟桶，实际查到 M 个，M==N 就算齐。
// **这个做法有两个致命缺陷，任何一个都足以让整个方案失效**：
//
// 缺陷一：设备天然有停机间隙，按窗口长度硬算「应有桶数」会误伤
//   设备 10:00 上线、10:30 下线 ⇒ 这段真实的分钟桶是 30 个，不是 60 个。
//   按 60 算 ⇒ 永远判「覆盖不足」⇒ 永远回落 unified_data ⇒ 回填白做。
//   而且它在**正确的系统上**判错，会把好数据当成坏数据。
//
// 缺陷二（更致命）：算「应有桶数」必须知道源数据实际有哪些分钟，而那个源就是
//   `unified_data` —— **正是我们回填完之后要删掉的表**。
//   用即将被删除的表当运行时判据 = 自相矛盾：删完之后判据失效，
//   而那恰恰是我们最需要它工作的时候（删了老分区才需要 rollup 顶上来）。
//
// ============ 因此：覆盖率是**回填时写下的元数据**，不是运行时推断 ============
//
// 回填时我们**本来就要逐天扫 unified_data**，顺手数出「这台设备这一天实际有多少个
// 分钟桶」写进本表。运行时只做一次 O(窗口天数) 的查表：
//
//   窗口覆盖的**每一天**都在表里、且 state 表示"已验证" ⇒ handled=true
//   否则 ⇒ 回落 unified_data（fail-open）
//
// 三条好处，正好对应上面两个缺陷：
//   1. **不依赖 unified_data** —— 删了照样能判（元数据留在本表）；
//   2. **天然处理停机间隙** —— buckets 是数出来的实际值，不是按 1440 硬算；
//   3. **O(天数) 查询** —— 30 天窗口 = 30 行，不扫任何数据。
//
// ============ state 的取值与语义（关键：用"行不存在"表达"没验证过"）============
//
//   complete : 已回填且源数据有内容（buckets >= 1）。读路径可用。
//   missing  : 已回填且源数据**这一天本来就没有**（buckets == 0）。读路径可用
//              （这一天没有数据是事实，不是缺失）。
//   partial  : 回填开始过但没跑完这一天。读路径**不可用**，必须回落。
//   （行不存在）: 从没验证过这一天。读路径不可用，必须回落。
//
// 为什么 missing 和"行不存在"必须区分：
//   设备 A 从 11-03 才开始上报，11-01/11-02 它确实没有数据 ⇒ 写 missing；
//   设备 B 的历史回填还没跑到 11-01 ⇒ 那一行**不存在**。
//   两者在数据上都是"没有行"，但一个是可信的结论、一个是未知。混为一谈会让
//   未回填的窗口被当成"这几天本来就没数据"而返回空图。

import (
	"fmt"
	"time"

	"gorm.io/gorm"
)

// RollupCoverageTable 是覆盖元数据表的物理表名。
const RollupCoverageTable = "telemetry_rollup_coverage"

const (
	// RollupCoverageComplete 该设备这一天已回填，且源数据有内容。
	RollupCoverageComplete = "complete"
	// RollupCoverageMissing 该设备这一天已回填，且源数据本来就没有（buckets=0）。
	RollupCoverageMissing = "missing"
	// RollupCoveragePartial 回填开始过但没跑完。读路径必须回落。
	RollupCoveragePartial = "partial"
)

// RollupCoverageLiveLag 是「实时段」护栏：窗口末端离当前时间不足这个量的，一律回落。
//
// ============ 为什么必须有它（「今天」的洞）============
//
// RollupCollector 写的是**当前分钟**，而历史回填只能从 unified_data 聚合**已落库**的行。
// 如果今天这一天被回填跑过并置了 complete，那么**今天尚未发生的分钟也被算作「覆盖」**：
//
//	过去日期：置 complete 时该天所有分钟都已发生 ⇒ 判据正确
//	今天    ：置 complete 时今天只过了 N 分钟 ⇒ 剩余 1440-N 分钟被判「覆盖」但其实是空
//
// 表现为**最近一小时的图表偶发空洞** —— 而那恰恰是用户最容易看到的一段时间。
//
// 所以：**不追求「今天也走 rollup」。先正确，再快。**
// 窗口末端落在「当前时间 - liveLag」之后的，一律回落 unified_data。
//
// 120s 的依据：collector 落盘延迟上界实测约 60~90 秒（一分钟桶在跨分钟后才 flush，
// 加上 flush ticker 周期），取 120s 留余量。
const RollupCoverageLiveLag = 120 * time.Second

// rollupCoverageNow 是可注入的时钟（测试需要构造"相对现在"的窗口）。
// 生产恒为 time.Now，不要在外面改它。
var rollupCoverageNow = func() time.Time { return time.Now() }

// SetRollupCoverageClock 覆写判据时钟（**仅供测试**）。返回 restore 函数。
func SetRollupCoverageClock(now func() time.Time) func() {
	prev := rollupCoverageNow
	rollupCoverageNow = now
	return func() { rollupCoverageNow = prev }
}

// RollupCoverageCutoff 返回「稳定段」与「实时段」的分界时刻：now - liveLag。
//
// 读路径用它把请求窗口切成两段：
//
//	· [Start, cutoff)  交给 rollup（已落定、可信任）；
//	· [cutoff, End)    交给帧表解码（实时段，帧表是完整的主写入路径）。
//
// 为什么不整窗回落：前端的 preset（1h/24h/7d）与后端 hours=N 的 End **都是
// time.Now()**，整窗回落会让长窗 rollup 永远不生效 —— 删掉 unified_data 之后
// 7d/30d 图表就会变成空页。详见 api/rollup_read.go 的「⓪ 窗口末端截断」。
func RollupCoverageCutoff() time.Time {
	return rollupCoverageNow().UTC().Add(-RollupCoverageLiveLag)
}

// RollupCoverageRow 是覆盖元数据的一行（一台设备的一天）。
//
// 为什么 day 存定长文本而不是 SQL 的 date 类型：
//
//	SQLite 没有真正的 date 类型，AutoMigrate 会把它退化成 datetime/TEXT，
//	而 PG 的 date 与字符串比较会走隐式转换 —— 两边语义不一致，同一份代码在
//	两种方言上判定可能不同。定长 YYYY-MM-DD 文本两方言逐位相同、可直接排序、
//	可直接做范围比较，且用例能在 SQLite 上跑（不需要 PG 才能验证判据逻辑）。
type RollupCoverageRow struct {
	// DeviceID = EdgeDevice.ID（与 unified_data.device_id / rollup.device_id 同口径）。
	DeviceID uint `gorm:"column:device_id;primaryKey;not null" json:"device_id"`
	// Day 该设备这一天（UTC）。格式严格 YYYY-MM-DD。
	Day string `gorm:"column:day;size:10;primaryKey;not null" json:"day"`
	// Buckets 回填时**实际数出来**的分钟桶数（不是按 1440 硬算）。
	//
	// ⚠ **这是写之前一次性数完的**（先聚合出该天全部分钟桶，再写 rollup 行，最后写本行）。
	// 不能改成「边写边数」：那样 buckets 与真实写入量天然相等 ⇒ 中途断掉时缺的半天
	// 永远看不出来 ⇒ 判据失去鉴别力。一句话：**buckets 必须是独立于写入过程的观测值**。
	//
	// 它有两个用途：
	//   1. 读路径判 covered=true 时的自证（buckets>0 ⇒ 真的写了行）；
	//   2. 排障时回答"这一天到底该有多少个点"。
	Buckets int `gorm:"column:buckets;not null;default:0" json:"buckets"`
	// State 见 RollupCoverageComplete / Missing / Partial。
	State string `gorm:"column:state;size:16;not null" json:"state"`
	// Source 该日 rollup 的**数据来源**：unified = 从 unified_data 聚合（L1 收窄后口径），
	// frames = 从帧表聚合（全采样口径）。
	//
	// 为什么必须记：两个口径的 avg **本来就不等值**（收窄后 vs 全采样）。将来排查
	// 「为什么这天的 avg 比那天高」时，第一件事就是问它从哪个源算的。不记的话，
	// 这个信息会随进程重启永久丢失，而两个口径的差异无法从数值反推。
	Source string `gorm:"column:source;size:16;not null;default:''" json:"source"`
	// UpdatedAt 行更新时间。
	UpdatedAt time.Time `gorm:"column:updated_at" json:"updated_at"`
}

// TableName 显式声明表名。
func (RollupCoverageRow) TableName() string { return RollupCoverageTable }

// RollupCoverageDay 把时间戳归一成覆盖表的 day 键（UTC 日期，YYYY-MM-DD）。
//
// 必须 UTC：分区键与桶都用 UTC 月/日切（见 RollupBucket 的注释），用本地时区
// 会让日期在月末/跨日时错位一天，进而让读路径把"已覆盖"判成"未覆盖"。
func RollupCoverageDay(at time.Time) string {
	return at.UTC().Format("2006-01-02")
}

// EnsureRollupCoverageTable 建/补齐覆盖元数据表（幂等）。
//
// 与 telemetry_backfill_progress 同款：**非分区表**，走 AutoMigrate 而不是裸 DDL。
// 只有一份定义 ⇒ 不可能出现"SQLite 全绿、生产炸"的 DDL 漂移（2026-11-07 那次
// telemetry_rollup_1m 裸 DDL 漏 updated_at 的教训）。
func EnsureRollupCoverageTable(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("rollup coverage: nil db")
	}
	return db.AutoMigrate(&RollupCoverageRow{})
}

// MarkRollupCoverage 写入/更新一台设备一天的覆盖状态（幂等 UPSERT）。
//
// 用裸 SQL 的 ON CONFLICT 而不是 GORM Save：GORM 的 Save 在 SQLite 与 PG 上对
// 复合主键的 upsert 语义不完全一致（SQLite 会先 SELECT 再 INSERT/UPDATE），
// 而这条语句在两方言上都由 SQL 标准语义保证。
func MarkRollupCoverage(db *gorm.DB, deviceID uint, day string, buckets int, state string) error {
	if db == nil {
		return fmt.Errorf("rollup coverage: nil db")
	}
	if deviceID == 0 {
		return fmt.Errorf("rollup coverage: device_id must be non-zero")
	}
	return db.Exec(
		"INSERT INTO "+RollupCoverageTable+" (device_id, day, buckets, state, updated_at) VALUES (?, ?, ?, ?, ?) "+
			"ON CONFLICT (device_id, day) DO UPDATE SET buckets = EXCLUDED.buckets, "+
			"state = EXCLUDED.state, updated_at = EXCLUDED.updated_at",
		deviceID, day, buckets, state, time.Now().UTC(),
	).Error
}

// MarkRollupCoverageWithSource 同 MarkRollupCoverage，但额外记录**数据来源**。
//
// 回填用它区分「这一天是从 unified_data 聚合的」还是「从帧表聚合的」——
// 两种口径的 avg 不同（L1 收窄后 vs 全采样），不记来源就无法解释历史数值差异。
func MarkRollupCoverageWithSource(db *gorm.DB, deviceID uint, day string, buckets int, state, source string) error {
	if db == nil {
		return fmt.Errorf("rollup coverage: nil db")
	}
	if deviceID == 0 {
		return fmt.Errorf("rollup coverage: device_id must be non-zero")
	}
	return db.Exec(
		"INSERT INTO "+RollupCoverageTable+" (device_id, day, buckets, state, source, updated_at) VALUES (?, ?, ?, ?, ?, ?) "+
			"ON CONFLICT (device_id, day) DO UPDATE SET buckets = EXCLUDED.buckets, "+
			"state = EXCLUDED.state, source = EXCLUDED.source, updated_at = EXCLUDED.updated_at",
		deviceID, day, buckets, state, source, time.Now().UTC(),
	).Error
}

// ReadRollupCoverage 读出一台设备在 [fromDay, toDay] 闭区间内的覆盖行（按 day 索引）。
//
// 用**闭区间**而不是半开：调用方给的是窗口的首末日期，两端都可能只是部分覆盖，
// 但"这一天验证过"这个事实与窗口从这天的几点开始无关 —— 一天一旦回填完整，
// 这一天的任何子区间都是可信的。因此按天判、闭区间查。
func ReadRollupCoverage(db *gorm.DB, deviceID uint, fromDay, toDay string) (map[string]RollupCoverageRow, error) {
	if db == nil {
		return nil, fmt.Errorf("rollup coverage: nil db")
	}
	out := make(map[string]RollupCoverageRow, 64)
	if toDay < fromDay {
		return out, nil
	}
	var rows []RollupCoverageRow
	if err := db.Session(&gorm.Session{}).
		Where("device_id = ? AND day >= ? AND day <= ?", deviceID, fromDay, toDay).
		Find(&rows).Error; err != nil {
		return nil, fmt.Errorf("rollup coverage: read device %d %s..%s: %w", deviceID, fromDay, toDay, err)
	}
	for _, r := range rows {
		out[r.Day] = r
	}
	return out, nil
}

// RollupCoverageVerified 报告一行的状态是否表示"已验证过"（读路径可用）。
//
// complete 与 missing 都算已验证：missing 的含义是"这一天源数据本来就没有"，
// 那是**已验证的结论**，不是缺失。partial 与零值（行不存在）都不可用。
func RollupCoverageVerified(state string) bool {
	return state == RollupCoverageComplete || state == RollupCoverageMissing
}

// RollupWindowDays 返回 [from, to] **闭区间**覆盖的 UTC 日期键列表（升序）。
//
// 返回的是 **[from, to) 半开区间**触及的日期（末端用 to-1ns 推导）。
// 这与帧表路径、ReadRollupStats 的时间语义一致 —— 三处必须是同一个区间口径，
// 任何一处用闭区间都会在对齐到午夜的窗口上多算一天（详见函数体内注释）。
//
// 返回 nil 表示窗口无效（to 早于 from）。
func RollupWindowDays(from, to time.Time) []string {
	if !to.After(from) {
		return nil
	}
	startDay := time.Date(from.UTC().Year(), from.UTC().Month(), from.UTC().Day(), 0, 0, 0, 0, time.UTC)
	// 窗口是**半开区间 [from, to)**（与帧表路径 / ReadRollupStats 的 bucket < to 一致），
	// 所以"窗口内最后一个日期"要用 to-1ns 推导，而不是直接用 to。
	//
	// 为什么这处边界必须精确：若 to 恰好是整点午夜（如 11-04T00:00:00，最常见的
	// "查最近 N 天"形状），直接用 to 会把 11-04 也纳入 —— 而 11-04 这一天在窗口内
	// **一秒都没有**。Hard-要求它也有覆盖元数据，会让所有对齐到午夜的正常窗口
	// 一律回落。这不是保守，是错判。
	lastInstant := to.UTC().Add(-time.Nanosecond)
	endDay := time.Date(lastInstant.Year(), lastInstant.Month(), lastInstant.Day(), 0, 0, 0, 0, time.UTC)
	if endDay.Before(startDay) {
		return nil
	}
	var out []string
	for d := startDay; !d.After(endDay); d = d.AddDate(0, 0, 1) {
		out = append(out, RollupCoverageDay(d))
	}
	return out
}

// RollupWindowCovered 判断一个窗口的 rollup 是否完整覆盖。
//
// 这是读路径唯一的判据入口。四条规则：
//  1. 窗口无效 ⇒ false（让调用方回落，别返回空点集）；
//  2. 窗口跨越的**每一天**都必须有覆盖行；
//  3. 每一行的 state 必须表示已验证（complete / missing）；
//  4. 任意一天不满足 ⇒ false（整个窗口回落，不做"部分拼接"）。
//
// 第 0 条（**先于**上面四条）：窗口末端必须在「当前时间 - RollupCoverageLiveLag」之前。
// 需要两者在边界上口径一致（时间戳对齐、点集不重不漏），任何一处理解偏差都会
// 在图上表现为一个假的跳变。整个窗口回落虽然慢，但结果一定正确 —— 而正确性
// 是这个判据存在的**全部目的**。
func RollupWindowCovered(db *gorm.DB, deviceID uint, from, to time.Time) (bool, error) {
	days := RollupWindowDays(from, to)
	if len(days) == 0 {
		return false, nil
	}
	// ── 实时段护栏（「今天」的洞）──
	//
	// 回填只能从 unified_data 聚合已落库的行；RollupCollector 写的是当前分钟。
	// 若今天这一天被回填置了 complete，那么**今天尚未发生的分钟也被算作覆盖** ⇒
	// 最近一小时的图表偶发空洞（而那恰恰是用户最容易看到的一段时间）。
	//
	// 所以窗口末端进入「当前时间 - liveLag」之后的，一律回落：不追求「今天也走
	// rollup」，先正确再快。详见 RollupCoverageLiveLag 的注释。
	if to.After(rollupCoverageNow().Add(-RollupCoverageLiveLag)) {
		return false, nil
	}
	rows, err := ReadRollupCoverage(db, deviceID, days[0], days[len(days)-1])
	if err != nil {
		return false, err
	}
	// ⚠ 「今天」不参与校验 —— 它由 live collector 负责，回填永远追不上它。
	//
	// 依据（Lead 2026-11-07 裁决 + boot-api 审计）：「今天」这一天的 rollup 数据
	// 本来就该由 live collector 写；回填插手只会制造「半真半假」的 complete。
	// 所以 buildRollupBackfillUnits 排除今天，本函数也必须相应地**不要求**今天
	// 有覆盖行，否则两者自相矛盾 ⇒ 所有包含今天的窗口一律回落 ⇒ 长窗永远走
	// 不到 rollup ⇒ 删掉 unified_data 之后 7d/30d 图表变空页。
	//
	// 残余风险（如实记录，可自愈）：
	// live collector 今天若漏了某几分钟，那几分钟在 rollup 里是空的 ⇒ 当天的图
	// 在那个位置有个小洞。**次日回填会把它补上**（次日回填包含昨天，且那时
	// 昨天已结束 ⇒ 可以安全地置 complete）—— 这是"今天不回填"换来的自愈性质。
	today := RollupCoverageDay(rollupCoverageNow().UTC())
	for _, day := range days {
		if day >= today {
			// 今天/未来：交给 live collector，不要求元数据。
			continue
		}
		row, ok := rows[day]
		if !ok {
			// 这一天从没验证过。不能当成"本来就没数据"—— 那会让未回填的窗口返回空图。
			return false, nil
		}
		if !RollupCoverageVerified(row.State) {
			return false, nil
		}
	}
	return true, nil
}
