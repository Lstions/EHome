package api

// latest_values_warmup_test.go — 启动回填**时间窗**的验收（task-24 步骤 2）。
//
// Lead 给的验收判据：「统计 warmup 载入的设备数 vs 全表 DISTINCT ON 的设备数，必须相等」。
//
// 严格化一下：两者相等**只在所有设备都还在窗口内**时成立。真实生产里总有设备离线超过
// 窗口，所以本用例断言的是**更强也更准确**的形式：
//
//	窗口内设备的结果 == 全表结果中这些设备的结果（逐设备逐字段）
//	窗口外设备被排除，且**被排除的恰好只有它们**
//
// 这比「总数相等」更有鉴别力：
//   · 如果 WHERE 写错（比如过滤了太多），排除数会多于窗口外设备数 => 红；
//   · 如果 WHERE 没生效（窗口被忽略），窗口外设备不会被排除 => 红。
//
// 另外钉住一条**不能被时间窗破坏的不变量**：warmup 的 SQL 必须与 /overview、设备列表
// 的回落查询**同构**（每设备「最新时刻的全部物理量」）。若加了窗口后变成「窗口内最新一行」，
// 缓存命中与 miss 两条路径的形状就不一致了 —— 那正是 latest_value_cache.go 文件头记录的
// 那次历史缺陷（缓存只剩 1 个物理量）。

import (
	"sort"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// seedWarmupRows 造一批 unified_data 行，并把 created_at 改到指定时刻。
//
// 为什么改 created_at 而不是 timestamp：warmup 查询用的是 created_at（沿用既有口径），
// 见 latest_values_warmup.go 的说明。
func seedWarmupRows(t *testing.T, db *gorm.DB, dev uint, at time.Time, sensors []string) {
	t.Helper()
	for _, name := range sensors {
		rec := models.UnifiedData{
			DeviceID:   dev,
			SensorName: name,
			Value:      float64(len(name)),
			Unit:       "u",
			Timestamp:  at,
		}
		if err := db.Create(&rec).Error; err != nil {
			t.Fatalf("create unified row: %v", err)
		}
		if err := db.Model(&models.UnifiedData{}).Where("id = ?", rec.ID).
			UpdateColumn("created_at", at).Error; err != nil {
			t.Fatalf("fix created_at: %v", err)
		}
	}
}

// warmupFullTableRows 是**对照**：原实现（无时间界）的查询，原样保留。
func warmupFullTableRows(t *testing.T, db *gorm.DB) []models.UnifiedData {
	t.Helper()
	var rows []models.UnifiedData
	err := db.Raw("SELECT u.* FROM unified_data u " +
		"INNER JOIN (SELECT DISTINCT ON (device_id) device_id, created_at FROM unified_data " +
		"WHERE device_id IS NOT NULL ORDER BY device_id, created_at DESC) latest " +
		"ON u.device_id = latest.device_id AND u.created_at = latest.created_at").Scan(&rows).Error
	if err != nil {
		t.Fatalf("full-table warmup query: %v", err)
	}
	return rows
}

// groupByDevice 把行按设备分组，返回每设备的字段名有序列表。
func groupByDevice(rows []models.UnifiedData) map[uint][]string {
	out := map[uint][]string{}
	for _, r := range rows {
		out[r.DeviceID] = append(out[r.DeviceID], r.SensorName)
	}
	for k := range out {
		sort.Strings(out[k])
	}
	return out
}

// TestLatestValuesWarmup_WindowMatchesFullTableForActiveDevices 已删除（2026-11-08）。
//
// 它验的是「LatestValuesWarmupRows 的 24h 窗口能覆盖活跃设备的全部字段」。
// 而被测函数随 unified_data 一起删掉了（启动预热的第一个来源已废弃，
// 只保留帧表那一趟 WarmupLatestValuesFromFrames）。
//
// ⚠ 保留下面两条：它们验的是 lookback **常量与环境变量解析**，与数据源无关。

// TestLatestValuesWarmup_DefaultLookbackIs24h 钉住默认窗口值本身。
//
// 为什么值得单测一个常量：窗口值是这个步骤唯一的设计参数。有人把它改成 1h 时，
// 上面那条用例会因为 devActive2（2h 前）被排除而红 —— 但红得不够直白。
// 这条直接说明「默认值变了」，防止有人为了让上面那条变绿而去改测试数据。
func TestLatestValuesWarmup_DefaultLookbackIs24h(t *testing.T) {
	if LatestValuesWarmupLookback != 24*time.Hour {
		t.Fatalf("默认回看窗口 = %v, want 24h —— 若要改它，请先读 latest_values_warmup.go "+
			"文件头的取值依据（>= 设备最长静默间隔），并同步调整本用例与演练清单",
			LatestValuesWarmupLookback)
	}
}

// TestLatestValuesWarmup_LookbackFromEnv 守环境变量解析（非法值不得让服务启动失败）。
func TestLatestValuesWarmup_LookbackFromEnv(t *testing.T) {
	cases := []struct {
		env  string
		want time.Duration
	}{
		{"", 24 * time.Hour},
		{"48h", 48 * time.Hour},
		{"90m", 90 * time.Minute},
		{"12", 12 * time.Hour},      // 纯小时数
		{"garbage", 24 * time.Hour}, // 非法 => 回落默认，不报错
		{"-5", 24 * time.Hour},      // 负数 => 回落默认
		{"0", 24 * time.Hour},       // 0 => 回落默认
	}
	for _, c := range cases {
		t.Setenv("EHOME_LATEST_WARMUP_LOOKBACK", c.env)
		if got := LatestValuesWarmupLookbackFromEnv(); got != c.want {
			t.Errorf("env=%q: got %v, want %v", c.env, got, c.want)
		}
	}
}
