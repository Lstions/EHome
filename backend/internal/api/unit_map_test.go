package api

// unit_map_test.go — unitMapForDevice 的验收。
//
// 事故：device14 的 /devices/:id/history 首次请求 units=33.405s（占 94%）。
// 根因：所谓「三层回落」是**三段串行** —— `if !has` 只决定谁覆盖谁，不决定要不要查。
//
// 演进：
//
//	第一版：给 ② 层加 48h 时间窗。可跑，但**被否决** ——
//	  单位映射是「每设备一次」的，离线超过 48h 的设备会**所有字段单位全部消失**。
//	  当时按「>48h 没数据的字段丢单位」记录，**实际后果比这严重得多**。
//	  （这条留在这里是因为它差点被当成"可接受的取舍"结案。）
//
//	最终版：① 先跑，② 只对仍缺的字段逐个
//	  ORDER BY timestamp DESC LIMIT 1（走 (device_id, sensor_name, timestamp) 索引）。
//	  无时间窗 ⇒ 不丢任何单位；成本 O(字段数) ≈ 30ms，**与设备历史长度无关**。
//
// 本文件断言：离线 5 天的字段**仍然有**单位 —— 这正是否决时间窗方案的理由。
//
// ⚠ 本文件里**没有**任何「时间窗 / lookback」概念（旧文件名 unit_lookback_test.go 已弃用）。
//   若你 grep 不到时间窗，不是代码丢了，是它被撤掉了。

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// seedUnitRows 往 **sensor_field_obs** 写「已观测字段 + 单位」。
//
// ============ 为什么改到这里（2026-11-08）============
//
// 原实现写 unified_data —— 那是 unitMapForDevice 的**旧兜底来源**。
// 那层已在 2026-11-08 删除（unified_data DROP，且实测量到它 unit 永远为空），
// 现在单位只来自 sensor_field_obs（累积投影，UPSERT 永不删）。
//
// ⚠ 这是「造数方式变了」不是「被测对象坏了」：功能仍在，数据源换了。
//
//	所以改的是造数，**不是把 want 29 改成 want 0** —— 后者等于承认功能坏了。
//
// at 参数保留（调用方传「1 小时前 / 5 天前」表达时间远近），但 sensor_field_obs
// 只有 first_seen / last_seen，没有「采样时刻」语义 —— 而那正是本例要证的：
// **单位映射与设备离线多久无关**（见下面 TestUnitMap_StaleFieldStillHasUnit）。
func seedUnitRows(t *testing.T, db *gorm.DB, dev uint, at time.Time, names []string) {
	t.Helper()
	_ = at // 时间远近不再影响单位：sensor_field_obs 是累积投影，不按时间过滤
	for _, n := range names {
		if err := db.Create(&models.SensorFieldObs{
			DeviceID: dev, SensorName: n, Unit: "U-" + n,
			FirstSeen: at, LastSeen: at,
		}).Error; err != nil {
			t.Fatalf("seed sensor_field_obs(%s): %v", n, err)
		}
	}
}

// TestUnitMap_RecentFieldsKeepUnits 守「有数据的字段单位不丢」。
func TestUnitMap_RecentFieldsKeepUnits(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	db.Exec("DELETE FROM sensor_field_obs")
	ResetDeviceUnitCache()
	defer ResetDeviceUnitCache()

	const dev = 9985
	names := make([]string, 0, 29)
	for i := 0; i < 29; i++ {
		names = append(names, "sensor_"+string(rune('a'+i)))
	}
	seedUnitRows(t, db, dev, time.Now().UTC().Add(-time.Hour), names)

	got := unitMapForDevice(db, dev, names)
	if len(got) != len(names) {
		t.Fatalf("单位映射 %d 个字段, want %d", len(got), len(names))
	}
	for _, n := range names {
		if want := "U-" + n; got[n] != want {
			t.Fatalf("字段 %s 的单位 = %q, want %q", n, got[n], want)
		}
	}
	t.Logf("29 个字段单位全部返回")
}

// TestUnitMap_StaleFieldStillHasUnit 守「离线很久的设备不丢单位」。
//
// 这是**否决时间窗方案的直接理由**：单位映射按设备建，
// 时间窗会让「整台离线设备」的所有字段同时失去单位。
func TestUnitMap_StaleFieldStillHasUnit(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	db.Exec("DELETE FROM sensor_field_obs")
	ResetDeviceUnitCache()
	defer ResetDeviceUnitCache()

	const dev = 9986
	seedUnitRows(t, db, dev, time.Now().UTC().Add(-5*24*time.Hour), []string{"stale_sensor"})

	got := unitMapForDevice(db, dev, []string{"stale_sensor"})
	u, has := got["stale_sensor"]
	if !has || u != "U-stale_sensor" {
		t.Fatalf("离线 5 天的字段单位 = %q (has=%v), want %q —— 不该丢单位", u, has, "U-stale_sensor")
	}
	t.Logf("离线 5 天的字段仍有单位：%s（无时间窗，靠逐字段索引探测）", u)
}
