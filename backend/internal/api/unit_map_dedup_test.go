package api

// unit_map_dedup_test.go — unitMapForDevice 的输入去重契约 + 空 unit 语义。
//
// ============ 为什么存在（2026-11-08 P0）============
//
// DROP unified_data 后生产日志刷了 12,140 条 42P01（一次翻页）。根因三层叠加：
//
//	① sensor_field_obs 里「观测到但无单位」的字段（alarm_*/status 类）被
//	   `if r.Unit != ""` 滤掉 => 当成「没查到」=> 永远进 missing => 每次重查；
//	② 调用方传进来的 fields 含大量重复（763 快照 × 16 字段 = 12,208 项），
//	   而本函数按数组长度迭代发查询 => 单字段被查 763 次；
//	③ 兜底分支查 unified_data => 已 DROP => 每项一条 42P01。
//
// ①② 各配一条用例（③ 是删除，没有可断言对象）：
//
//	TestUnitMapForDevice_DuplicateFieldsIssueOneQuery   ② 去重（性能）
//	TestUnitMapForDevice_EmptyUnitCountsAsObserved      ① 空 unit（正确性）
//
// 两条都不依赖 unified_data：用 sensor_field_obs 直接验证。

import (
	"testing"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// queryCounter 统计发出去的 Raw 查询次数。
//
// 为什么用回调而不是 DryRun：DryRun 不真正执行，而我们要数的是「真实发出了几条 SQL」
// —— 那正是生产日志里 42P01 的条数口径。
type queryCounter struct {
	n int
}

func (c *queryCounter) register(db *gorm.DB) {
	// ⚠ 必须挂 **Row()** 链，不是 Raw() 链。
	//
	// db.Raw(...).Scan(&x) 走的是 Row() 链 —— 这是今天早些时候用独立探针证过的
	// （见 handler_context_test.go 的 queryContextRecorder 注释）。
	// 挂 Raw() 链会让计数恒为 0，于是「断言只查 1 次」变成**恒真的空跑**，
	// 而这正是本用例要防的那类 bug（vacuous pass）。
	//
	// 三链都挂，防止将来调用方换成 Exec/Find 形态时计数静默失效。
	capture := func(tx *gorm.DB) {
		if tx.DryRun {
			return
		}
		c.n++
	}
	db.Callback().Raw().Before("gorm:raw").Register("test:count_raw", capture)
	db.Callback().Row().Before("gorm:row").Register("test:count_row", capture)
	db.Callback().Query().Before("gorm:query").Register("test:count_query", capture)
}

// TestUnitMapForDevice_DuplicateFieldsIssueOneQuery 是 ② 的回归锁。
//
// 判据：查询次数由「字段种类数」决定，不由「输入数组长度」决定。
//
// 没有它，dedupFieldNames 那行被删掉时不会有任何东西变红 ——
// 功能仍然正确（unit 照样补得上），只是每次请求多打 762 倍的 SQL。
func TestUnitMapForDevice_DuplicateFieldsIssueOneQuery(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ResetDeviceUnitCache()
	t.Cleanup(ResetDeviceUnitCache)

	if err := db.Create(&models.SensorFieldObs{
		DeviceID: 9001, SensorName: "rsoc", Unit: "%",
	}).Error; err != nil {
		t.Fatalf("seed sensor_field_obs: %v", err)
	}

	fields := make([]string, 1000)
	for i := range fields {
		fields[i] = "rsoc"
	}

	c := &queryCounter{}
	c.register(db)
	got := unitMapForDevice(db, 9001, fields)

	if got["rsoc"] != "%" {
		t.Fatalf("rsoc 单位 = %q, want %% —— 去重不该影响结果", got["rsoc"])
	}
	if c.n > 1 {
		t.Fatalf("1000 个同名字段触发 %d 次查询，want 恰好 1 次 —— "+
			"去重丢了（每多一次就是 762 倍放大，生产实测 12,140 条/翻页）", c.n)
	}
	t.Logf("1000 个重复字段 => %d 次查询（去重生效）", c.n)
}

// TestUnitMapForDevice_EmptyUnitCountsAsObserved 是 ① 的回归锁。
//
// 判据：空 unit 表示「观测过，但该字段本来没有单位」，不等于「没查到」。
//
// 没有它，把 got[r.SensorName] = r.Unit 改回 if r.Unit != "" {...} 不会有东西变红 ——
// 而那个改法会让 alarm_* 这类字段每次请求都重查，且在 unified_data 存在时
// 静默放大成 N 倍查询（实测 763 倍）。
func TestUnitMapForDevice_EmptyUnitCountsAsObserved(t *testing.T) {
	db := testutil.OpenTestDB(t)
	ResetDeviceUnitCache()
	t.Cleanup(ResetDeviceUnitCache)

	if err := db.Create(&models.SensorFieldObs{
		DeviceID: 9002, SensorName: "alarm_pv_low", Unit: "",
	}).Error; err != nil {
		t.Fatalf("seed sensor_field_obs: %v", err)
	}

	c := &queryCounter{}
	c.register(db)
	got := unitMapForDevice(db, 9002, []string{"alarm_pv_low"})

	u, ok := got["alarm_pv_low"]
	if !ok {
		t.Fatal("空 unit 字段没被记入结果 —— 它会被当成「没查到」，" +
			"于是每次请求都对它重查（DROP 后每次翻页 16 条 42P01 的来源）")
	}
	if u != "" {
		t.Fatalf("alarm_pv_low 单位 = %q, want 空串", u)
	}
	c.n = 0
	unitMapForDevice(db, 9002, []string{"alarm_pv_low"})
	if c.n != 0 {
		t.Fatalf("第二次调用发了 %d 次查询，want 0 —— 空 unit 没进缓存，"+
			"于是 missing 永不为空、缓存判定永不成立", c.n)
	}
	t.Log("空 unit 进缓存 => 第二次调用 0 次查询")
}

// TestDedupFieldNames 锁 dedupFieldNames 的三个契约：去重、保持首次出现顺序、边界。
//
// 为什么单独测顺序：missingUnitFields 的注释强调过「保持输入顺序，便于测试稳定」——
// 顺序一旦随 map 迭代随机化，「对比两次请求的 SQL 序列」这类调试手段就失效，
// 而没有任何别的东西会因此变红。
func TestDedupFieldNames(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"去重且保序", []string{"a", "b", "a", "c", "b"}, []string{"a", "b", "c"}},
		{"无重复原样返回", []string{"x", "y", "z"}, []string{"x", "y", "z"}},
		{"全重复", []string{"a", "a", "a"}, []string{"a"}},
		{"nil", nil, nil},
		{"空切片", []string{}, []string{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := dedupFieldNames(tc.in)
			if len(got) != len(tc.want) {
				t.Fatalf("长度 = %d, want %d（in=%v got=%v）", len(got), len(tc.want), tc.in, got)
			}
			for i := range tc.want {
				if got[i] != tc.want[i] {
					t.Fatalf("第 %d 项 = %q, want %q —— 顺序必须保持首次出现，"+
						"否则「对比两次请求的 SQL 序列」这类调试手段会失效（in=%v got=%v）",
						i, got[i], tc.want[i], tc.in, got)
				}
			}
		})
	}
}
