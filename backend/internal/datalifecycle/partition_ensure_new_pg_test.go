package datalifecycle

import (
	"testing"
	"time"

	"ehome/backend/testutil"
)

// TestEnsurePartitionsFor_CreatesMissingFramePartition — 「DROP 后真正建一个新分区」
// 这条路径在生产上还没被执行过一次（2026-11-08 实测：启动日志里
// created partition 计数 = 0，因为当前窗口 202610..202702 全部已存在）。
//
// 而它 22 天后就会用上：12-01 时窗口变 [202611, 202703]，必须新建 202703。
// 若那时建不出来，**月初的帧写入会直接失败**（PG 分区表没有 default 分区兜底），
// 而错误信息是「no partition of relation found for row」—— 指向 INSERT，
// 不指向「分区没建」。这正是必须用用例钉住的形态。
//
// 本用例造的是「母表存在、但要建的那个月分区不存在」：
//   ① 先正常建出帧表分区母表
//   ② 手工 DROP 掉 +3 月那个分区（模拟「未来月份还没建」）
//   ③ EnsurePartitionsFor(TelemetryFrameTable, 3) 必须把它建回来
//   ④ 再跑一次必须幂等（不能因为已存在而报错）
//
// ⚠ 为什么不用 unified_data 造数：它已 DROP，而本用例验的是**帧表**的建分区路径
//   —— 生产上真正会用到的是它。用 unified_data 验等于验了一个已退役的对象。
func TestEnsurePartitionsFor_CreatesMissingFramePartition(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	if err := MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表分区母表: %v", err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS " + TelemetryFrameTable + " CASCADE") })

	pm := NewPartitionManager(db)
	// 目标月 = 当前 +3 月（窗口上界），这是**跨月那天最可能缺**的那个。
	future := addMonths(time.Now(), 3)
	name := partitionName(TelemetryFrameTable, future)

	// 前置 1：先确认它原本存在（EnsurePartitionsFor 建过）。
	if n := tableExistsInSchema(t, db, name, "r"); n == 0 {
		t.Fatalf("前置不成立：%s 应已被 EnsurePartitionsFor 建出", name)
	}

	// ② 删掉它，模拟「未来月份还没建」。
	if err := db.Exec("DROP TABLE " + name).Error; err != nil {
		t.Fatalf("drop %s: %v", name, err)
	}
	if n := tableExistsInSchema(t, db, name, "r"); n != 0 {
		t.Fatalf("前置不成立：%s 删除失败", name)
	}

	// ③ 建回来。
	if err := pm.EnsurePartitionsFor(TelemetryFrameTable, 3); err != nil {
		t.Fatalf("EnsurePartitionsFor: %v（若这里失败，跨月那天帧写入会直接报 "+
			"no partition of relation found，而错误指向 INSERT 不指向分区）", err)
	}
	if n := tableExistsInSchema(t, db, name, "r"); n == 0 {
		t.Fatalf("%s 没被建回来 —— 跨月时帧写入会直接失败", name)
	}

	// ④ 幂等：再调一次不能报错（每次启动 + 每 24h ticker 都会调）。
	if err := pm.EnsurePartitionsFor(TelemetryFrameTable, 3); err != nil {
		t.Fatalf("EnsurePartitionsFor 必须幂等，第二次报错: %v", err)
	}

	// ⑤ 真正写一行进新分区，证明它是可用的分区而不只是 pg_class 里的一条记录。
	var devID uint
	if err := db.Raw("SELECT device_id FROM " + TelemetryFrameTable + " LIMIT 1").Scan(&devID).Error; err != nil || devID == 0 {
		devID = 9901
	}
	if err := db.Exec("INSERT INTO "+TelemetryFrameTable+
		" (device_id, ts, seq, frame_kind, slot_id, payload) VALUES (?, ?, 0, 1, 1, ?)",
		devID, future.Add(48*time.Hour), []byte{0x00}).Error; err != nil {
		t.Fatalf("向新建分区写入失败: %v —— 建出来了但不可写，说明 DDL 的 FOR VALUES 边界不对", err)
	}
}
