package datalifecycle

// partition_mgr_tz_test.go — 「月边界必须按 UTC 归一化」的回归测试（2026-11-06 实测缺陷）。
//
// 缺陷（已在真实 PG 上复现并定性）:
//
//	pgx 解码 timestamptz 得到的是 time.Local 时区的 time.Time（pgtype/timestamptz.go
//	用 time.Unix(...) 构造，未显式置 UTC）。原 monthStart 用 t.Year()/t.Month()
//	（**本地时区视角**）却构造 time.UTC 的月初 ⇒ 时区非 UTC 时最早月份整体右移一个月，
//	该月前几小时的行落在所有 COPY 窗口之外 ⇒ 行数校验失败 ⇒ 迁移 Fatal。
//	生产容器碰巧 TZ 未设（Go 的 time.Local 退化成 UTC）所以没炸 —— 那是运气，不是设计。
//
// 本文件的三层防线:
//  1. **纯逻辑**（不需要 PG / 不需要动 TZ 环境变量）：同一时刻在 +08 与 UTC 两种
//     Location 下必须映射到**同一个** UTC 月初 / 同一个分区名；
//  2. **缺陷现场**：钉住那个具体瞬间（UTC 10-31T16:30Z = 本地 11-01T00:30+08）
//     的期望结果，并显式断言"不是下一个月"；
//  3. **真库端到端**（EHOME_TEST_DB=postgres）：把 time.Local 设成 +08（复刻
//     TZ=Asia/Shanghai 的机器）跑完整迁移，断言 **src==dst 不漏行**。
//     第 3 条是唯一能真正证明"故障链被切断"的用例 —— 前两条只证明纯函数对。

import (
	"testing"
	"time"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"

	"ehome/backend/testutil"
)

// cstZone 是 UTC+8（不带夏令时），等价于 TZ=Asia/Shanghai 的固定偏移形态。
var cstZone = time.FixedZone("CST", 8*3600)

// TestMonthStartIsTimezoneInvariant 守「同一时刻 ⇒ 同一 UTC 月初」。
//
// 显式用 time.FixedZone 构造，不依赖进程的 TZ 环境变量：
// 依赖环境变量的用例在 CI/开发机上会因 TZ 不同而给出不同结论，是最坏的一种测试。
func TestMonthStartIsTimezoneInvariant(t *testing.T) {
	instants := []time.Time{
		time.Date(2026, 10, 31, 16, 30, 0, 0, time.UTC),  // 缺陷现场：本地=11-01T00:30+08
		time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC), // UTC 月末最后一秒，本地=次日 07:59
		time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC),     // UTC 月初第一秒，本地=当日 08:00
		time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC),     // UTC 月初，本地=当日 08:00
		time.Date(2026, 1, 1, 3, 0, 0, 0, time.UTC),      // 本地已是 1-1 11:00
		time.Date(2025, 12, 31, 20, 0, 0, 0, time.UTC),   // 跨年：本地=2026-01-01T04:00
	}
	for _, inst := range instants {
		utc := inst.UTC()
		local := inst.In(cstZone) // 同一时刻，本地时区视角
		if !utc.Equal(local) {
			t.Fatalf("构造错误：同一时刻的两种表示不相等 (%v vs %v)", utc, local)
		}
		gotUTC := monthStart(utc)
		gotLocal := monthStart(local)
		if !gotUTC.Equal(gotLocal) {
			t.Errorf("monthStart 依赖入参时区: 入参 %s (UTC 视角 %s) → %s，入参 %s (本地视角 %s) → %s —— "+
				"分区边界必须与入参时区无关，否则 TZ=Asia/Shanghai 的机器会少建一个月的分区",
				inst.Format(time.RFC3339), utc.Format("2006-01"), gotUTC.Format(time.RFC3339),
				inst.Format(time.RFC3339), local.Format("2006-01"), gotLocal.Format(time.RFC3339))
		}
		// 月份必须是入参时刻的 **UTC 月**（不是本地月）。
		if gotUTC.Year() != utc.Year() || gotUTC.Month() != utc.Month() {
			t.Errorf("monthStart(%s) = %s，want UTC 月 %04d-%02d",
				inst.Format(time.RFC3339), gotUTC.Format(time.RFC3339), utc.Year(), int(utc.Month()))
		}
		if gotUTC.Location() != time.UTC {
			t.Errorf("monthStart 返回值的 Location = %v, want UTC（分区边界 DDL 用 UTC 字面量拼接）", gotUTC.Location())
		}
		if gotUTC.Day() != 1 || gotUTC.Hour() != 0 || gotUTC.Minute() != 0 || gotUTC.Second() != 0 {
			t.Errorf("monthStart 未截断到月初: %v", gotUTC)
		}
	}
}

// TestMonthStartDefectScene 钉住缺陷现场：UTC 10-31T16:30Z 必须归到 **10 月**。
//
// 原实现在 TZ=+08 下会把它归到 11 月（本地视角 11-01），构造出 11-01T00:00Z，
// 比该行真实时刻还晚 7.5 小时 —— 于是 10 月分区不建、该行不被搬运、行数校验失败。
func TestMonthStartDefectScene(t *testing.T) {
	instant := time.Date(2026, 10, 31, 16, 30, 0, 0, time.UTC)
	want := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)

	got := monthStart(instant.In(cstZone))
	if !got.Equal(want) {
		t.Fatalf("monthStart(2026-10-31T16:30Z 的 +08 视角) = %s，want %s", got, want)
	}
	if got.Month() == time.November {
		t.Fatal("回归：月末 8 小时的行被归进了下一个月（这就是 src!=dst 的根因）")
	}
	// 归出来的月初必须 **不晚于** 原时刻本身，否则该行永远落不进自己的月窗口。
	if got.After(instant) {
		t.Fatalf("monthStart = %s 晚于原时刻 %s —— 该行必然被所有 COPY 窗口排除", got, instant)
	}
}

// TestPartitionNameIsTimezoneInvariant 守分区名里的 YYYYMM 与 UTC 月一致。
func TestPartitionNameIsTimezoneInvariant(t *testing.T) {
	cases := []struct {
		instant time.Time
		want    string
	}{
		// 本地 11 月、UTC 10 月 ⇒ 名字必须是 202610（与分区边界同口径）。
		{time.Date(2026, 10, 31, 16, 30, 0, 0, time.UTC), "probe_202610"},
		{time.Date(2026, 10, 31, 23, 59, 59, 0, time.UTC), "probe_202610"},
		{time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC), "probe_202611"},
		{time.Date(2025, 12, 31, 20, 0, 0, 0, time.UTC), "probe_202512"},
	}
	for _, c := range cases {
		if got := partitionName("probe", c.instant); got != c.want {
			t.Errorf("partitionName(%s) = %q, want %q", c.instant.Format(time.RFC3339), got, c.want)
		}
		// 同一时刻换个时区视角，名字必须不变。
		if got := partitionName("probe", c.instant.In(cstZone)); got != c.want {
			t.Errorf("partitionName(%s @+08) = %q, want %q（分区名必须与入参时区无关）",
				c.instant.Format(time.RFC3339), got, c.want)
		}
	}
}

// TestAddMonthsIsTimezoneInvariant 守 addMonths 继承 monthStart 的 UTC 归一化。
func TestAddMonthsIsTimezoneInvariant(t *testing.T) {
	instant := time.Date(2026, 10, 31, 16, 30, 0, 0, time.UTC)
	for n := -3; n <= 3; n++ {
		gotUTC := addMonths(instant, n)
		gotLocal := addMonths(instant.In(cstZone), n)
		if !gotUTC.Equal(gotLocal) {
			t.Errorf("addMonths(%d) 依赖入参时区: %v vs %v", n, gotUTC, gotLocal)
		}
		if gotUTC.Location() != time.UTC {
			t.Errorf("addMonths(%d) Location = %v, want UTC", n, gotUTC.Location())
		}
	}
	// 跨年跨月仍正确（回归旧用例覆盖，这里补 UTC 视角）。
	if got := addMonths(time.Date(2026, 12, 15, 0, 0, 0, 0, time.UTC), 1); got.Year() != 2027 || got.Month() != time.January {
		t.Errorf("addMonths(2026-12, +1) = %v, want 2027-01-01", got)
	}
}

// TestMonthSpanInclusiveTimezoneInvariant 守进度分母（monthSpanInclusive）不受时区影响。
func TestMonthSpanInclusiveTimezoneInvariant(t *testing.T) {
	from := time.Date(2026, 10, 31, 16, 30, 0, 0, time.UTC)
	to := time.Date(2026, 11, 6, 1, 0, 0, 0, time.UTC)
	want := monthSpanInclusive(from, to)
	for _, loc := range []*time.Location{time.UTC, cstZone, time.FixedZone("X-5", -5*3600)} {
		if got := monthSpanInclusive(from.In(loc), to.In(loc)); got != want {
			t.Errorf("monthSpanInclusive @%v = %d, want %d（分母漂移会让进度百分比跳变）", loc, got, want)
		}
	}
	if want != 2 {
		t.Errorf("monthSpanInclusive(2026-10 → 2026-11) = %d, want 2", want)
	}
}

// TestMigrateKeepsRowsUnderNonUTCLocal 是**端到端回归**：复刻 TZ=Asia/Shanghai 的机器
// 跑完整迁移，断言不漏行。
//
// 为什么必须动 time.Local（而不是只测纯函数）：
//
//	真实故障链的起点是 **pgx 把 timestamptz 扫成 time.Local 的值**。只在纯函数层
//	构造 +08 时刻证明不了这一点 —— 那是我"以为"驱动会给出的形态。这里把 time.Local
//	改掉，让 pgx 的 time.Unix(...) 真的返回 +08 的值，整条链（扫描 → oldestRowMonthFor
//	→ monthStart → 建分区 → COPY → 行数校验）才是被真正覆盖的。
//
// 缺陷复现记录（修复前，同一用例）: 迁移返回
//
//	row count mismatch after migration: src=3 dst=1 (aborting, tables intact)
//
// 修复后: 迁移成功且 src==dst。
func TestMigrateKeepsRowsUnderNonUTCLocal(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	// 复刻 TZ=Asia/Shanghai。time.Local 是进程全局量，用例结束必须还原
	//（本包没有 t.Parallel，PG 用例亦顺序执行，因此不会与其他用例互相干扰）。
	prevLocal := time.Local
	time.Local = cstZone
	t.Cleanup(func() { time.Local = prevLocal })

	const (
		probe       = "zz_tz_probe"
		probeLegacy = "zz_tz_probe_legacy"
	)
	dropProbeTables(t, db, probe, probe+"_new", probeLegacy)
	t.Cleanup(func() { dropProbeTables(t, db, probe, probe+"_new", probeLegacy) })

	ddl := "CREATE TABLE " + probe + " (id BIGSERIAL, device_id BIGINT NOT NULL, sensor_name VARCHAR(32) NOT NULL, " +
		"value DOUBLE PRECISION NOT NULL, unit VARCHAR(16), timestamp TIMESTAMPTZ NOT NULL, created_at TIMESTAMPTZ, " +
		"edge_device_id BIGINT, PRIMARY KEY (id, timestamp))"
	if err := db.Exec(ddl).Error; err != nil {
		t.Fatalf("create probe flat table: %v", err)
	}

	thisMonth := monthStart(time.Now())
	// ⚠ 布局决定这条用例能不能抓到 bug：**最早的那一行必须落在"UTC 月末最后 8 小时"**。
	//
	// 因为 oldestRowMonthFor 只取 min(timestamp) 一个点：只有"最早行"被算错月份，
	// 月循环的起点才会整体右移、才会漏掉一整个 UTC 月的分区。若最早行在月中，
	// 无论 monthStart 对不对它都归到同一月，用例就会**假绿**（第一版正是这么写的，
	// 变异测试当场证明它抓不到缺陷）。
	//
	// thisMonth-2h 与 thisMonth-30min 都落在上一 UTC 月的最后 8 小时内：
	// 它们的 UTC 视角是上月，+08 视角却是本月 —— 正是缺陷现场。
	boundaryRow := thisMonth.Add(-30 * time.Minute) // 最早行的邻居：上月最后一刻钟
	earliestRow := thisMonth.Add(-2 * time.Hour)    // **最早行**：上月最后 4 小时
	currentRow := time.Now().Add(-time.Hour)
	if !earliestRow.Before(boundaryRow) {
		t.Fatalf("用例布局错误：earliestRow(%s) 必须早于 boundaryRow(%s)",
			earliestRow.Format(time.RFC3339), boundaryRow.Format(time.RFC3339))
	}
	// 前置断言：最早行确实"UTC 视角上月、本地视角本月"，否则本用例根本没有复刻缺陷。
	if u, l := earliestRow.UTC(), earliestRow.In(cstZone); u.Month() == l.Month() {
		t.Fatalf("用例布局错误：最早行 %s 的 UTC 月与 +08 月相同，触发不到 monthStart 的缺陷",
			earliestRow.Format(time.RFC3339))
	}
	rows := []struct {
		ts     time.Time
		sensor string
	}{
		{earliestRow, "a"},
		{boundaryRow, "b"},
		{currentRow, "c"},
	}
	for _, r := range rows {
		if err := db.Exec("INSERT INTO "+probe+" (device_id, sensor_name, value, timestamp) VALUES (1, ?, 1, ?)",
			r.sensor, r.ts).Error; err != nil {
			t.Fatalf("seed probe row ts=%s: %v", r.ts.Format(time.RFC3339), err)
		}
	}
	var seeded int64
	if err := db.Raw("SELECT count(*) FROM " + probe).Scan(&seeded).Error; err != nil {
		t.Fatalf("count seeded: %v", err)
	}
	if seeded != int64(len(rows)) {
		t.Fatalf("seeded rows = %d, want %d", seeded, len(rows))
	}

	// 前置断言：驱动确实把 timestamptz 扫成了 +08 视角（否则本用例没复刻出故障前提，
	// 会变成"用 UTC 环境跑一遍就绿"的假绿）。
	var scanned time.Time
	if err := db.Raw("SELECT timestamp FROM " + probe + " WHERE sensor_name = 'b'").Scan(&scanned).Error; err != nil {
		t.Fatalf("scan boundary row: %v", err)
	}
	if name, off := scanned.Zone(); off != 8*3600 {
		t.Fatalf("前提不成立：pgx 扫出的 timestamptz 时区是 %s(offset=%d)，期望 +08 —— "+
			"本用例要在 time.Local!=UTC 下才有意义", name, off)
	}

	if err := MigrateTableToPartitioned(db, probe, probeLegacy); err != nil {
		t.Fatalf("迁移在 TZ=+08 下失败（回归：monthStart 未 UTC 归一化会丢行）: %v", err)
	}

	// 核心断言：src==dst，一行不丢。
	var migrated int64
	if err := db.Raw("SELECT count(*) FROM " + probe).Scan(&migrated).Error; err != nil {
		t.Fatalf("count migrated: %v", err)
	}
	if migrated != seeded {
		t.Fatalf("迁移后行数 = %d, want %d（有行落在所有分区窗口之外被丢弃）", migrated, seeded)
	}
	// 边界行必须真的落在"上一 UTC 月"的分区里（不是被兜到别的月）。
	prevMonthName := partitionName(probe, thisMonth.Add(-time.Second))
	if tableExistsInSchema(t, db, prevMonthName, "r") == 0 {
		t.Errorf("上一 UTC 月的分区 %s 不存在 —— 月循环没有覆盖最早月份", prevMonthName)
	}
	var inPrev int64
	if err := db.Raw("SELECT count(*) FROM " + prevMonthName + " WHERE sensor_name = 'b'").Scan(&inPrev).Error; err != nil {
		t.Fatalf("count boundary row in %s: %v", prevMonthName, err)
	}
	if inPrev != 1 {
		t.Errorf("边界行(%s) 在 %s 中命中 %d 行, want 1", boundaryRow.Format(time.RFC3339), prevMonthName, inPrev)
	}
	// legacy 旧表按既有语义被 DROP（证明走的是完整成功路径）。
	if tableExistsInSchema(t, db, probeLegacy, "r") != 0 {
		t.Error("legacy 旧表应在成功迁移后被 DROP")
	}
}

// TestMonthStartWithNonUTCLocalDefaultPath 让纯逻辑防线在 SQLite 默认测试配置下也有一条入口
// （PG 用例默认 skip；若只依赖它，默认的全包测试里就没人守这个不变量）。
func TestMonthStartWithNonUTCLocalDefaultPath(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// SQLite 上分区路径整体 no-op，这里确认"带着非 UTC 的 time.Local 调迁移入口不炸"。
	prevLocal := time.Local
	time.Local = cstZone
	t.Cleanup(func() { time.Local = prevLocal })

	if err := MigrateTableToPartitioned(db, "zz_tz_noop", "zz_tz_noop_legacy"); err != nil {
		t.Fatalf("非 PG 方言在 TZ=+08 下必须仍是 no-op: %v", err)
	}
	// 纯函数在默认（SQLite）测试配置下同样守一遍缺陷现场。
	if got := monthStart(time.Date(2026, 10, 31, 16, 30, 0, 0, cstZone)); !got.Equal(time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("monthStart = %s, want 2026-10-01T00:00Z", got)
	}
}
