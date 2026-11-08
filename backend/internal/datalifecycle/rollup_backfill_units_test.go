package datalifecycle

// rollup_backfill_units_test.go — 回填的**纯逻辑**单测（SQLite / 无 DB 依赖）。
//
// 这里钉的是 boot-api 2026-11-07 审计发现的两条静默错数：
//   ① 「今天」不该被回填（否则 complete 是半真半假，漏写的分钟永远补不回来）；
//   ② 批次跨度必须是 60 的整数倍（否则同一个分钟桶被两个批次各算一半，后者覆盖前者）。
//
// 两条都**不报错、不留痕**，只是数据错 —— 所以必须有正向用例钉住。
//
// 为什么不放在 _pg_test.go：这两条是纯函数逻辑，不需要 PG。放在 SQLite 也能跑的
// 文件里，CI 默认就会执行（PG 用例在没配 EHOME_TEST_DB 时会 Skip ⇒ 假绿）。

import (
	"testing"
	"time"
)

// TestRollupBackfillExcludesToday 守「回填不碰今天」。
//
// 故障形态（boot-api 实测）：回填 10:00 跑过把今天置 complete(buckets=600)；
// 之后 live collector 漏了几分钟；重启后回填看到 "今天 complete" 就跳过
// ⇒ 那几分钟永远不被回填，而 RollupWindowCovered 仍返回 true ⇒ 图表空洞永不修复。
//
// 修法：今天完全交给 live collector，回填只管**已结束**的日子。
func TestRollupBackfillExcludesToday(t *testing.T) {
	// 固定时钟，排除"跑用例时恰好跨 UTC 午夜"的抖动。
	fixedNow := time.Date(2026, 11, 7, 10, 30, 0, 0, time.UTC)
	restore := SetRollupCoverageClock(func() time.Time { return fixedNow })
	defer restore()

	devices := []uint{14, 15}
	// 源数据从 11-01 一直到"现在"（11-07 10:30）。
	minTS := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	maxTS := fixedNow

	units := buildRollupBackfillUnits(devices, minTS, maxTS)
	if len(units) == 0 {
		t.Fatal("不该为空：11-01 ~ 11-06 有 6 天 × 2 设备")
	}
	for _, u := range units {
		if u.Day >= "2026-11-07" {
			t.Fatalf("回填单元里出现了 %s —— 今天必须排除（今天由 live collector 负责）。"+
				"否则今天会被置一个半真半假的 complete，漏写的分钟永远补不回来", u.Day)
		}
	}
	// 11-01 ~ 11-06 = 6 天 × 2 设备 = 12 个单元。
	if want := 6 * len(devices); len(units) != want {
		t.Fatalf("单元数 = %d, want %d（11-01..11-06 × %d 设备）", len(units), want, len(devices))
	}
}

// TestRollupBackfillExcludesTodayWhenSourceIsOnlyToday 守边界：
// 源数据只有今天 ⇒ 没有可回填的单元（不是"回填一个空天"）。
func TestRollupBackfillExcludesTodayWhenSourceIsOnlyToday(t *testing.T) {
	fixedNow := time.Date(2026, 11, 7, 10, 30, 0, 0, time.UTC)
	restore := SetRollupCoverageClock(func() time.Time { return fixedNow })
	defer restore()

	units := buildRollupBackfillUnits([]uint{14}, fixedNow.Add(-2*time.Hour), fixedNow)
	if len(units) != 0 {
		t.Fatalf("单元数 = %d, want 0（源数据只在今天 ⇒ 全交给 live collector）", len(units))
	}
}

// TestRollupBackfillIncludesYesterdayWhenSourceEndsYesterday 反向守卫：
// 源数据的最后一天若是**昨天**（已结束），必须被包含 —— 否则"永远返回空"的假实现也能过上面两条。
func TestRollupBackfillIncludesYesterdayWhenSourceEndsYesterday(t *testing.T) {
	fixedNow := time.Date(2026, 11, 7, 10, 30, 0, 0, time.UTC)
	restore := SetRollupCoverageClock(func() time.Time { return fixedNow })
	defer restore()

	yesterday := time.Date(2026, 11, 6, 23, 0, 0, 0, time.UTC)
	units := buildRollupBackfillUnits([]uint{14}, yesterday.Add(-24*time.Hour), yesterday)
	if len(units) != 2 {
		t.Fatalf("单元数 = %d, want 2（11-05 与 11-06，都是已结束的日子）", len(units))
	}
	if units[len(units)-1].Day != "2026-11-06" {
		t.Fatalf("最后一个单元 = %s, want 2026-11-06", units[len(units)-1].Day)
	}
}

// TestNormalizeRollupBatchSecondsAlignsToMinute 守「批次必须是整分钟」。
//
// 故障形态：batch=90s ⇒ 同一个分钟桶被两个批次各聚合一部分，覆盖赋值保留后者
// ⇒ Avg/Count 只是半个桶的值（实测 cnt=3 而真值 6，avg 0.040 vs 0.035）。
func TestNormalizeRollupBatchSecondsAlignsToMinute(t *testing.T) {
	cases := []struct {
		in   int
		want time.Duration
	}{
		{3600, time.Hour},       // 已是 60 倍数：不变
		{90, 120 * time.Second}, // 向上取整到 120s
		{60, time.Minute},       // 边界
		{1, time.Minute},        // 1s → 60s（不能截断成 0）
		{61, 120 * time.Second}, // 向上取整
		{0, time.Hour},          // <=0 用默认 3600
		{-5, time.Hour},         // 负数用默认
		{7200, 2 * time.Hour},   // 已是 60 倍数
	}
	for _, tc := range cases {
		if got := normalizeRollupBatchSeconds(tc.in); got != tc.want {
			t.Errorf("normalizeRollupBatchSeconds(%d) = %v, want %v", tc.in, got, tc.want)
		}
		if got := normalizeRollupBatchSeconds(tc.in); int(got/time.Second)%60 != 0 {
			t.Errorf("normalizeRollupBatchSeconds(%d) = %v 不是 60 的整数倍", tc.in, got)
		}
	}
}

// TestRollupBackfillConfigFromEnvNormalizesBatch 守环境变量入口也做了归一化。
//
// 只在 env 层校验"正数"是不够的：90 是正数但会静默写错数据。
func TestRollupBackfillConfigFromEnvNormalizesBatch(t *testing.T) {
	t.Setenv(EnvRollupBackfillBatchSeconds, "90")
	cfg, warns := RollupBackfillConfigFromEnv()
	if cfg.BatchSeconds != 120 {
		t.Fatalf("BatchSeconds = %d, want 120（90 必须向上取整到 60 的倍数）", cfg.BatchSeconds)
	}
	if len(warns) != 0 {
		t.Fatalf("合法但需归一化的值不该产生 warning, got %v", warns)
	}
}
