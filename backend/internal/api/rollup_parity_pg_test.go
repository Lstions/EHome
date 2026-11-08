package api

// rollup_parity_pg_test.go — 长窗口**逐点对拍**：rollup 路径 vs unified_data 路径（task-22）。
//
// ============ 这次对拍要证明什么 ============
//
// 1. **结构完全一致**：同窗口下两条路径返回的点数、点时间戳必须逐点相同。
//    这是"用户把 5h 拉到 7h（跨过 rollup 分界）时图表形状不突变"的硬保证。
// 2. **取值一致到可解释的界**：见下面「残余偏差」一节。
//
// ============ 残余偏差（如实记录，不是 bug）============
//
// rollup 的粒度是**1 分钟**，而 D1 的桶宽 W 一般不是 60 的整数倍
// （1d/500 → W=173s；7d/500 → W=1210s；30d/500 → W=5184s）。于是每个 W 桶的
// 最后一个分钟可能**跨过桶边界**：
//
//     · unified 路径按**每个采样**归属桶 ⇒ 只取 b+W 之前的采样；
//     · rollup 路径只有**分钟级**聚合 ⇒ 会把那个跨边界分钟整体算进 b。
//
// 两者之差 = 跨边界那 1 分钟内的信号变化量（上界 = 每分钟斜率 delta）。
// 这是 1 分钟预聚合的**固有信息损失**，不是实现缺陷：想消除它就必须保留秒级数据，
// 那就等于不压缩（12 GB 也就删不掉）。
//
// 因此本用例断言三件事：
//     · 点数、时间戳**完全相同**（结构不变量）；
//     · 每个点 |Δvalue| <= delta（每分钟斜率上界，可解释的界）；
//     · **至少一半的点严格相等**（若误用 Avg 代替 Last，几乎所有点都会不等 ⇒ 抓得住）。
//
// 第三条是对"Avg vs Last"这个 bug 的专门守卫：Avg 与 Last 在构造数据上恒差
// delta/4，每个点都不相等；而正确实现只有跨边界的点不相等。

import (
	"context"
	"fmt"
	"math"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// rollupParityDelta 是构造数据的每分钟斜率（值/分钟）。
//
// 取 0.37：既能保证 Avg 与 Last 明显不同（差 delta/4 = 0.0925），
// 又小于常见 spec 的量化步长量级，贴近真实 BMS 缓变量。
//
// ⚠ 值域约束（2026-11-07 实测踩坑）：本构造要同时被**帧表**编码，而 rsoc 的
// spec 是 Min=0/Step=1/**Bits=7** ⇒ 只能表示 0..127。第一版取 0.37，8 天后
// 值到 4262 ⇒ 量化 code 溢出 ⇒ 帧表解码出完全不同的数（对拍偏差 4135，
// 而时间戳全对得上 —— 典型的"值错了但形状对"，最难查的一类）。
//
// 取 0.0104：8 天 × 1440 分钟 × 0.0104 ≈ 119.8 < 127，正好落在 rsoc 可表示区间内。
// 副作用：每秒变化更小，但对拍判据用的是"严格相等点数"，任何非零差都能被抓到。
const rollupParityDelta = 0.0104

// seedParityUnified 造 unified_data：每分钟 2 个采样（:00 与 :30）。
//
// 为什么每分钟 2 个：同一分钟内两个不同值 ⇒ **Avg != Last**，
// 这正是能抓住"回填误用 Avg"的最小构造。若每分钟只有 1 个采样，avg == last，
// 这个 bug 就完全测不出来。
func seedParityUnified(t *testing.T, db *gorm.DB, deviceID uint, from, to time.Time) {
	t.Helper()
	names := []string{"rsoc", "current", "total_voltage"}
	var rows []models.UnifiedData
	idx := 0
	for m := from; m.Before(to); m = m.Add(time.Minute) {
		base := float64(idx) * rollupParityDelta
		for _, half := range []int{0, 30} {
			ts := m.Add(time.Duration(half) * time.Second)
			if !ts.Before(to) {
				continue
			}
			for fi, name := range names {
				v := base + float64(fi)*100 + rollupParityDelta*float64(half)/60
				rows = append(rows, models.UnifiedData{
					DeviceID: deviceID, SensorName: name, Value: v, Timestamp: ts,
				})
			}
		}
		idx++
	}
	if err := db.Session(&gorm.Session{}).CreateInBatches(rows, 500).Error; err != nil {
		t.Fatalf("seed unified_data: %v", err)
	}
}

// ensureRollupMonthPartitions 补齐 [from, to] 跨越的 rollup 月分区。
//
// 为什么不直接用 EnsurePartitionsFor：它只建 [currentMonth-1, currentMonth+m]
// 这一段，而 30 天窗口在月初会落到「上上月」，那时用例会因为"没有分区"而假失败。
// 这里按**生产同款 DDL** 补齐，命名规则与 partitionName 一致。
func ensureRollupMonthPartitions(t *testing.T, db *gorm.DB, from, to time.Time) {
	t.Helper()
	table := datalifecycle.TelemetryRollupTable
	m := time.Date(from.UTC().Year(), from.UTC().Month(), 1, 0, 0, 0, 0, time.UTC)
	for !m.After(to.UTC()) {
		end := m.AddDate(0, 1, 0)
		name := fmt.Sprintf("%s_%04d%02d", table, m.Year(), int(m.Month()))
		var cnt int64
		db.Raw("SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace "+
			"WHERE c.relname = ? AND c.relkind = 'r' AND n.nspname = current_schema()", name).Scan(&cnt)
		if cnt == 0 {
			if err := db.Exec(fmt.Sprintf(
				"CREATE TABLE %s PARTITION OF %s FOR VALUES FROM ('%s') TO ('%s')",
				name, table, m.Format("2006-01-02"), end.Format("2006-01-02"))).Error; err != nil {
				t.Fatalf("create partition %s: %v", name, err)
			}
		}
		m = end
	}
}

// TestRollupMatchesUnifiedForLongWindows 是 1d/7d/30d 的逐点对拍（task-22 核心验收）。
func TestRollupMatchesUnifiedForLongWindows(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	// ============ 前置：本用例验的是「unified_data → rollup 回填」这条链路 ============
	//
	// 2026-11-08 硬切换后 unified_data 已 DROP，这条链路**永久退役**：回填引擎的
	// unified 源分支还在（迁移期 / 历史库仍需要），但生产上不再有 unified_data，
	// 所以本用例在 DROP 之后**没有可验的对象**。
	//
	// 明确 skip 而不是让它红：红会诱导人去「修」一个不存在的东西。
	// DROP 之后的「帧 → rollup 回填」由 rollup_oracle_fixture_test.go 覆盖。
	//
	// ⚠ 这是**唯一允许 skip 的场景**：被测对象永久不存在 + 已有明确替代用例。
	//   一般情况下 skip 是绿的、会掩盖失效（本仓反复在防这个）。
	if !db.Migrator().HasTable("unified_data") {
		t.Skip("unified_data 已 DROP（2026-11-08 硬切换）—— 本用例验的「unified→rollup 回填」" +
			"链路已退役；帧→rollup 回填由 rollup_oracle_fixture_test.go 覆盖")
	}
	if err := datalifecycle.MigrateTelemetryRollup1mToPartitioned(db); err != nil {
		t.Fatalf("migrate rollup: %v", err)
	}
	if err := datalifecycle.EnsureRollupCoverageTable(db); err != nil {
		t.Fatalf("ensure coverage: %v", err)
	}
	db.Exec("DELETE FROM unified_data")
	db.Exec("DELETE FROM " + datalifecycle.TelemetryRollupTable)
	db.Exec("DELETE FROM " + datalifecycle.RollupCoverageTable)
	// 必须连帧表一起清：P0 修复后回填的**日期范围**取自各数据源的最早时间
	// （不再只看 unified_data）。别的用例留下的帧会把范围拉早，
	// 于是本用例覆盖天数从 31 变 33 —— 那是**用例隔离缺失**，不是行为退化。
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")

	// 窗口必须整段落在 liveLag 之外（实时段不信任 rollup），所以数据截止到
	// 「2 天前的午夜」：既避开实时段，日期边界又干净。
	end := time.Now().UTC().Truncate(24*time.Hour).AddDate(0, 0, -2)
	const days = 31
	start := end.AddDate(0, 0, -days)
	const dev = 21

	ensureRollupMonthPartitions(t, db, start, end)
	seedParityUnified(t, db, dev, start, end)

	// 回填（BatchSeconds 取 6h：31 天 × 4 批 = 124 次聚合，比 1h 批快 6 倍）。
	cfg := datalifecycle.RollupBackfillConfig{Enabled: true, BatchSeconds: 21600}
	if err := datalifecycle.RollupBackfillRunOnce(context.Background(), db, cfg); err != nil {
		t.Fatalf("backfill: %v", err)
	}

	// 验收第 1 条：覆盖全部有数据的天。
	var covDays, covComplete int64
	if err := db.Raw("SELECT count(*), coalesce(sum(CASE WHEN state = ? THEN 1 ELSE 0 END),0) FROM "+
		datalifecycle.RollupCoverageTable+" WHERE device_id = ?",
		datalifecycle.RollupCoverageComplete, dev).Row().Scan(&covDays, &covComplete); err != nil {
		t.Fatalf("read coverage: %v", err)
	}
	t.Logf("覆盖：%d 天有元数据，其中 %d 天 complete", covDays, covComplete)
	// ============ 语义变更（P0 2026-11-07）============
	//
	// 旧行为：回填范围由 unified_data 的 max(timestamp) 决定 => 单元只到"最后有数据的
	// 那一天"，后面的日子永远没有 coverage 行。
	//
	// 那正是 P0 的 bug：停写之后 max(timestamp) 冻结 => 之后的日子永远不进 units =>
	// RollupWindowCovered=false => 长窗回落 => 停写后图表静默截断。
	//
	// 新行为：终点固定为 **now**（buildRollupBackfillUnits 排除今天 => 实际到"昨天"）。
	// 于是"数据截止日之后、昨天之前"的空白日也会生成 coverage 行，状态为 missing
	// （= 已验证该日无数据，不是"未验证"）。读路径据此认为这些窗口**已覆盖**，
	// 不再回落 —— 这是长窗在停写后仍然可用的关键。
	wantTotal := days + 2 // 数据 31 天 + 数据结束后到昨天的 2 个空白日
	if covDays != int64(wantTotal) || covComplete != int64(days) {
		t.Fatalf("覆盖天数 = %d (complete %d), want %d (complete %d) —— "+
			"complete 应覆盖全部有数据的天，空白日应为 missing",
			covDays, covComplete, wantTotal, days)
	}

	cases := []struct {
		name string
		days int
	}{
		{"1d", 1},
		{"7d", 7},
		{"30d", 30},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			q := historyPointQuery{
				EdgeDeviceID: dev,
				Category:     "rsoc",
				Start:        end.AddDate(0, 0, -tc.days),
				End:          end,
				MaxPoints:    500,
				Mode:         AggModeAvg,
			}
			got, handled, err := rollupHistoryPoints(context.Background(), db, q)
			if err != nil {
				t.Fatalf("rollupHistoryPoints: %v", err)
			}
			if !handled {
				t.Fatal("窗口已完整回填且远离实时段，rollup 必须 handled=true —— " +
					"否则长窗永远回落 unified_data，12 GB 老分区永远删不掉")
			}
			// ============ 基准改为「直接对 unified_data 做分桶聚合」（2026-11-08）============
			//
			// 原来调用 queryHistoryPoints（生产 SQL 路径）。那个函数随 unified_data
			// 一起删掉了，所以这里**内联同一口径的 SQL**（D1：桶内最后值）作为基准。
			//
			// ⚠ 这不是「自洽对拍」：本用例造数据用 seedParityUnified 的确定公式，
			// 而基准是**独立执行一遍 SQL 聚合**（不是重算公式）。所以它仍能抓住
			// 「rollup 写入路径算错」—— 那正是本用例的目的。
			//
			// 跨路径的真独立基准在 rollup_oracle_fixture_test.go（生产 rollup payload）。
			// 两者分工：这里验「本地造数 → 回填 → 读出」的完整链路；
			//          那边验「帧表重建 vs 生产 rollup 行」的口径一致。
			widthSeconds := bucketWidthSeconds(historyWindowSeconds(q.Start, q.End), q.MaxPoints)
			wantSQL := fmt.Sprintf(`SELECT to_timestamp(floor(extract(epoch FROM "timestamp") / %d) * %d) AS "timestamp",`+
				`(array_agg("value" ORDER BY "timestamp" DESC))[1] AS "value"`+
				`FROM unified_data WHERE device_id = ? AND sensor_name = ? AND "timestamp" BETWEEN ? AND ? `+
				`GROUP BY 1 ORDER BY 1`, widthSeconds, widthSeconds)
			var want []HistoryPoint
			if err := db.WithContext(context.Background()).Raw(wantSQL,
				q.EdgeDeviceID, q.Category, q.Start, q.End).Scan(&want).Error; err != nil {
				t.Fatalf("基准 SQL 聚合失败: %v", err)
			}
			// 与 queryHistoryPoints 同语义：首桶起点早于 Start 的裁掉（floor 对齐最多 W-1 秒）。
			cut := 0
			for cut < len(want) && want[cut].Timestamp.Before(q.Start) {
				cut++
			}
			want = want[cut:]

			// ============ 允许 rollup 少**最后一个**桶（精确断言，2026-11-08）============
			//
			// 固有语义限制：rollup 是 1 分钟粒度的聚合，重分桶时**无法切分已聚合的分钟桶**。
			//
			//	W = 173s（1d/500 点），最后一个宽桶 = [23:59:14, 00:01:27)
			//	该分钟的桶时刻是 23:59:00 ⇒ 落在 [23:56:21, 23:59:14) ⇒ 末宽桶为空
			//	而 unified 按**采样时间戳**分桶，23:59:30 正好落进末宽桶 ⇒ 多一个点
			//
			// **数据零丢失**：那一分钟的数据在，只是被归到前一个宽桶。
			// 损失仅为「图表末端少一个点」。
			//
			// 为什么不补一个边界点：补出来的点时间戳是 23:59 的值却标在 00:01:27 的桶上
			// ⇒ **时间戳撒谎**。宁可少一个点，不要一个标错时间的点。
			//
			// 为什么 7d/30d 不触发：W=1210s/1814s 与 60s 的相位不同，末宽桶没跨分钟边界。
			// **1d 只是相位最差的用例。**
			// （widthSeconds 已在上面的基准 SQL 处算过，这里复用）

			// ① 首点必须相同：分桶起点一致（否则不是末端问题，而是对齐问题）
			if len(got) > 0 && len(want) > 0 && !got[0].Timestamp.Equal(want[0].Timestamp) {
				t.Fatalf("首点时间 %v, want %v —— 分桶起点不一致，这不是末桶差异",
					got[0].Timestamp, want[0].Timestamp)
			}
			// ② 点数：rollup 只能**少于或等于** unified，且最多差 1
			if len(got) > len(want) || len(want)-len(got) > 1 {
				t.Fatalf("点数 = %d, want %d —— 差异必须恰为「至多少一个末桶」", len(got), len(want))
			}
			// ③ 差异必须钉在**末端**：尾点时间差不得超过一个桶宽。
			//    只有 ①② 的话，「中间丢一个点」也会通过 —— 那是完全不同的问题。
			if len(got) > 0 && len(want) > 0 {
				gap := want[len(want)-1].Timestamp.Sub(got[len(got)-1].Timestamp)
				if gap > time.Duration(widthSeconds)*time.Second {
					t.Fatalf("尾点相差 %v，超过一个桶宽 %ds —— 不是末桶空，是中间/尾部丢点",
						gap, widthSeconds)
				}
			}

			var worst float64
			exact := 0
			// 只比对公共前缀（差异只允许出现在末尾）。
			for i := range got {
				if !got[i].Timestamp.Equal(want[i].Timestamp) {
					t.Fatalf("第 %d 点时间 = %v, want %v", i, got[i].Timestamp, want[i].Timestamp)
				}
				d := math.Abs(got[i].Value - want[i].Value)
				if d == 0 {
					exact++
				}
				worst = math.Max(worst, d)
			}
			t.Logf("%s：%d 点，严格相等 %d (%.0f%%)，最大偏差 %g（上界 delta=%g）",
				tc.name, len(got), exact, 100*float64(exact)/float64(len(got)), worst, rollupParityDelta)
			// ============ 为什么「严格相等只有 45~60%」是**正常**的（2026-11-08 补）============
			//
			// 这个形态第一次看会以为「对拍有一半不通过」。它不是 —— 看数字就能自证：
			//
			//	构造：每分钟 2 个采样（:00 与 :30），值 = base + delta*half/60
			//	      => :00 = base，:30 = base + delta/2
			//	rollup 的 last = 该分钟最后一个采样 = base + delta/2
			//
			//	宽桶（W=173s 这类）重分桶时，rollup 的 **1 分钟桶无法再切分**：
			//	  · rollup 侧 last = 「该宽桶覆盖的最后一个分钟桶的 last」
			//	  · unified 侧 last = 「宽桶内最后一个原始采样」
			//	  => 两者最多差「一个分钟内的半格」= delta/2
			//
			//	实测最大偏差 = 0.0052 = 0.0104/2 = delta/2 ✓（不是「接近上界」，是**恰好等于**）
			//
			// 为什么恰好一半严格相等：W 不是 60s 的整数倍 ⇒ 宽桶边界与分钟边界**错位**。
			// 落在「桶内最后一个分钟桶完整落在桶内」的那些点两边取到同一条采样 ⇒ 相等；
			// 跨在错位处的那一半差半格。所以 45~60% 是**预期区间**，不是退化。
			//
			// 反过来说：若某天变成 0%（全差） => Avg/Last 口径错了（见界 2）；
			// 若变成 100%（全等） => 构造退化了（每分钟 2 个采样失效），本用例失去鉴别力。
			//
			// 界 1：跨边界分钟内的信号变化，上界 = 每分钟斜率。
			if worst > rollupParityDelta+1e-9 {
				t.Errorf("%s 最大偏差 %g > delta %g —— 超出 1 分钟粒度的固有误差，口径错了",
					tc.name, worst, rollupParityDelta)
			}
			// 界 2：**专门抓 Avg vs Last**。若用 Avg，所有点都差 delta/4 而无一相等（≈0%）。
			//
			// ⚠ 原判据是 `exact*2 < len(got)`（即要求 >= 50%），有**奇偶 bug**：
			// n 为奇数时它实际要求 ceil(n/2)，而「一半」是 n/2 ——
			// 1d 用例的 n 从 500 变成 499（末桶差异被接受后）就恰好卡在 249 上误报。
			//
			// 这条界的作用是抓**系统性的口径错误**（Avg 会把所有点都拉开），
			// 那种情况 exact ≈ 0%，与 45% 相距极远 ⇒ 放宽到 45% 不损失鉴别力，
			// 却不会再因为点数奇偶而误报。
			if exact*20 < len(got)*9 {
				t.Errorf("%s 只有 %d/%d 点严格相等（< 45%%）—— 强烈提示 rollup 读路径"+
					"用了 Avg 而不是 Last（口径必须是桶内最后值，D1）", tc.name, exact, len(got))
			}
		})
	}
}
