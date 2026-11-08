package api

// latest_values_device_enum_test.go — 帧表「每设备最后一帧」枚举的验收（2026-11-08）。
//
// ============ 被验的是什么 ============
//
// `LatestValuesRowsFromFrames` 第一步要拿到 {设备 => 最后一帧时刻}。
// 改前是 `SELECT device_id, max(ts) GROUP BY device_id` —— 生产实测 241~490ms，
// 因为 PostgreSQL 不做 loose index scan，「按前导列分组取极值」只能全表扫。
// 改后是递归 CTE skip-scan + 关联子查询，同一份数据 0.92ms。
//
// ============ 为什么这条不能被「结果对就行」的用例覆盖 ============
//
// 两个写法**返回完全相同的行** —— 逐字节等价（本文件第 1 条用例就是证明这点）。
// 也就是说：**任何只比对结果的用例，在改回 GROUP BY 时都不会红。**
// 和 /nodes/:id/latest 那个「结果完全正确，只是把整张表拉进内存」是同一族：
// 缺陷形态是**代价**，不是输出。
//
// 所以本文件必须同时钉代价侧。判据选的是**扫描行数**而不是耗时：
//   · 耗时随机器负载波动，CI 上不稳（写死阈值必然要么假红要么假绿）；
//   · 扫描行数是**结构性**的 —— GROUP BY 必然 3,600 行，skip-scan 必然 ~6 行。
// 差三个数量级，不存在「临界」情况。

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/telemetry"
	"ehome/backend/testutil"
)

// devLastRow 是枚举查询的输出形状。
type devLastRow struct {
	DeviceID uint
	LastTS   time.Time
}

// queryDeviceLastTS 跑被测查询。
func queryDeviceLastTS(t *testing.T, db *gorm.DB) []devLastRow {
	t.Helper()
	var rows []devLastRow
	if err := db.Raw(latestValuesDeviceLastTSQuery, latestValuesMaxDevices+1).
		Scan(&rows).Error; err != nil {
		t.Fatalf("枚举查询失败: %v", err)
	}
	return rows
}

// seedDevicesWithFrames 造 n 台设备、每台 framesPerDev 帧。
func seedDevicesWithFrames(t *testing.T, db *gorm.DB, devs []uint, framesPerDev int) telemetry.SlotDictionary {
	t.Helper()
	if err := datalifecycle.MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("建帧表: %v", err)
	}
	db.Exec("DELETE FROM telemetry_frame")
	db.Exec("DELETE FROM telemetry_slot")
	dict := telemetry.NewDBSlots(db)
	if err := dict.WarmUp(); err != nil {
		t.Fatalf("dict warmup: %v", err)
	}
	start := time.Now().UTC().Add(-time.Duration(framesPerDev+10) * time.Hour).Truncate(time.Hour)
	for _, dev := range devs {
		seedFramesOnlyFields(t, db, dev, start, start.Add(time.Duration(framesPerDev)*time.Second), dict,
			[]string{"f1", "f2"}, time.Second)
	}
	return dict
}

// TestDeviceLastTS_MatchesGroupByExactly 是**结果正确性**的门禁。
//
// 与 GROUP BY 逐行比对。这条保证「换写法不换答案」。
// 注意它**验不出性能回退** —— 把实现改回 GROUP BY 它照样绿。那是下一条的职责。
func TestDeviceLastTS_MatchesGroupByExactly(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	seedDevicesWithFrames(t, db, []uint{7, 12, 14, 99}, 20)

	got := queryDeviceLastTS(t, db)

	type refRow struct {
		DeviceID uint
		LastTS   time.Time
	}
	var want []refRow
	if err := db.Raw("SELECT device_id, max(ts) AS last_ts FROM telemetry_frame " +
		"WHERE device_id IS NOT NULL GROUP BY device_id ORDER BY device_id").Scan(&want).Error; err != nil {
		t.Fatalf("参照查询失败: %v", err)
	}

	if len(got) != len(want) {
		t.Fatalf("设备数 %d, want %d（got=%v）", len(got), len(want), deviceIDsOf(got))
	}
	for i := range want {
		if got[i].DeviceID != want[i].DeviceID {
			t.Fatalf("第 %d 台设备号 %d, want %d", i, got[i].DeviceID, want[i].DeviceID)
		}
		if !got[i].LastTS.Equal(want[i].LastTS) {
			t.Fatalf("设备 %d 的最后一帧 %v, want %v",
				got[i].DeviceID, got[i].LastTS, want[i].LastTS)
		}
	}
}

// TestDeviceLastTS_ScanRowsDoNotScaleWithFrameCount 是**代价侧**的门禁。
//
// ============ 判据为什么是「扫描行数」而不是「耗时」============
//
// 耗时在 CI 上不稳：几千行全表扫可能只要几毫秒（全在 shared_buffers 里），
// 写死阈值要么假红要么假绿。而**扫描行数是结构性的**：
//
// GROUP BY  必须读完全部行，且随帧数线性增长；
// skip-scan 每台设备只跳读一个索引项 ⇒ 行数 = O(设备数)，与帧数无关。
//
// 实测（4 台设备 / 800 帧，见 zz 探针）：
//
// skip-scan ：seq_tup_read=0    idx_tup_read=8
// GROUP BY  ：seq_tup_read=800  idx_tup_read=0
//
// 差两个数量级且随帧数拉大。阈值取「总帧数的 1/10」，语义是
// 「枚举代价不得与帧数同一量级」，不是一个魔法数字。
//
// ============ 这个判据本身也有假绿风险，必须自检 ============
//
// pg_stat 计数器是**异步**刷新的。踩过：第一次探针两个 delta 都是 0，
// 看起来像「查询没读任何行」—— 那是观察方法的问题，不是被测对象的问题。
// 更糟的是「0 <= 上界」永远成立 ⇒ 实现退化时用例依然绿。
// 所以下面**同时**断言 delta > 0：测量不到也必须算失败，不能算「没有违规」。
// （同族教训：task-25 的 RowsAffected 恒为 0 那个假绿。）
func TestDeviceLastTS_ScanRowsDoNotScaleWithFrameCount(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	devs := []uint{7, 12, 14, 99}
	const framesPerDev = 900 // 4 x 900 = 3,600 帧
	seedDevicesWithFrames(t, db, devs, framesPerDev)
	totalFrames := len(devs) * framesPerDev

	before := readFrameScanRows(t, db)

	rows := queryDeviceLastTS(t, db)
	if len(rows) != len(devs) {
		t.Fatalf("枚举出 %d 台设备, want %d", len(rows), len(devs))
	}

	delta := readFrameScanRows(t, db) - before

	// 上界语义：枚举代价不得与帧数同一量级。
	bound := int64(totalFrames / 10)
	t.Logf("枚举 %d 台设备（%d 帧）：扫描 %d 行（上界 %d）；若走 GROUP BY 应为 %d 行",
		len(devs), totalFrames, delta, bound, totalFrames)

	// 前置自检：必须真的测到了行数，否则上界断言是空转。
	if delta <= 0 {
		t.Fatalf("没有测到任何扫描行数 —— pg_stat 计数器未生效，本断言等于没验。" +
			"（测量不到也必须算失败，不能算「没有违规」）")
	}
	if delta > bound {
		t.Fatalf("扫描 %d 行超过上界 %d —— 枚举代价随帧数增长，不是 skip-scan。"+
			"（若实现被改回 GROUP BY，本断言即为红：它必须扫描全部 %d 帧）",
			delta, bound, totalFrames)
	}
}

// readFrameScanRows 返回帧表及其索引的**累计读取行数**（含全部分区）。
//
// ============ 为什么要 flush + 轮询 ============
//
// pg_stat 计数器由统计收集器**异步**刷新，写完立刻读会拿到旧值。
// pg_stat_force_next_flush() 强制本后端在下一次事务结束时把待发统计刷出去；
// 之后仍需短暂轮询等收集器更新，所以这里 poll 到值稳定或超时。
//
// seq_tup_read 在 pg_stat_user_tables，idx_tup_read 在 pg_stat_user_indexes ——
// **两张不同的视图**（踩过一次：写在一张表上会报 column does not exist）。
func readFrameScanRows(t *testing.T, db *gorm.DB) int64 {
	t.Helper()
	read := func() int64 {
		var seq, idx int64
		if err := db.Raw("SELECT COALESCE(sum(seq_tup_read),0) FROM pg_stat_user_tables "+
			"WHERE relname LIKE ?", "telemetry_frame%").Scan(&seq).Error; err != nil {
			t.Fatalf("读 seq_tup_read: %v", err)
		}
		if err := db.Raw("SELECT COALESCE(sum(idx_tup_read),0) FROM pg_stat_user_indexes "+
			"WHERE relname LIKE ?", "telemetry_frame%").Scan(&idx).Error; err != nil {
			t.Fatalf("读 idx_tup_read: %v", err)
		}
		return seq + idx
	}
	db.Exec("SELECT pg_stat_force_next_flush()")
	prev := read()
	for i := 0; i < 50; i++ {
		time.Sleep(10 * time.Millisecond)
		db.Exec("SELECT pg_stat_force_next_flush()")
		if cur := read(); cur != prev || i == 49 {
			return cur
		}
	}
	return prev
}

// TestDeviceLastTS_TerminatesWithDepthCap 钉住**递归必须能停**。
//
// 与 task-25 的 latestValuesMissSensorNames 同族风险：递归的终止依赖
// 「每跳严格递增」。这个不变量一旦被破坏（有人把 > 改成 >=、或数据畸形），
// 递归会**原地打转** —— 后果不是慢，是连接池被占满、API 挂死。
//
// 所以 depth 上界必须是**与数据无关**的硬常量。本用例把上界改小，
// 验证「撞到上界时截断而不是挂住」。
func TestDeviceLastTS_TerminatesWithDepthCap(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	seedDevicesWithFrames(t, db, []uint{7, 12, 14, 99}, 5)

	// 用一个比设备数小的深度上界：必须在有限步内返回，且行数不超过上界。
	const tinyCap = 2
	done := make(chan struct{})
	var rows []devLastRow
	var err error
	go func() {
		defer close(done)
		err = db.Raw(latestValuesDeviceLastTSQuery, tinyCap).Scan(&rows).Error
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("枚举查询 10s 内未返回 —— 递归深度保护失效（会占满连接池，比慢查询严重得多）")
	}
	if err != nil {
		t.Fatalf("枚举查询失败: %v", err)
	}
	if int64(len(rows)) > tinyCap {
		t.Fatalf("深度上界 %d 却枚举出 %d 台设备 —— 上界未生效", tinyCap, len(rows))
	}
	// 必须真的枚举到了东西，否则「返回了」可能只是因为什么都没查。
	if len(rows) == 0 {
		t.Fatal("深度上界内一台设备都没枚举到 —— 查询本身可能坏了")
	}
}

// TestDeviceLastTS_IncludesDevicesDeletedFromEdgeDevices 钉住设备来源。
//
// ============ 这条来自生产实测的一个反例 ============
//
// 改这层时考虑过「让调用方传 device_ids」，从 edge_devices 取设备列表。
// 生产实测发现：**设备 2 在帧表里有 100,806 帧，但 edge_devices 里已经不存在**
// （硬删除，不是软删）。若用 edge_devices 当设备源，这条历史数据会静默漏掉 ——
// 而「最新值缓存」对已删设备的语义是「没有」而非「报错」，漏掉不会有任何信号。
//
// 所以设备来源必须是**帧表自身**。本用例直接钉这条：只写帧表、不写 edge_devices，
// 枚举仍必须返回该设备。
func TestDeviceLastTS_IncludesDevicesDeletedFromEdgeDevices(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	// 故意用一个**不存在**于 edge_devices 的设备号。
	const orphanDev = uint(987654)
	seedDevicesWithFrames(t, db, []uint{orphanDev}, 5)

	var registered int64
	if err := db.Raw("SELECT count(*) FROM edge_devices WHERE id = ?", orphanDev).
		Scan(&registered).Error; err != nil {
		t.Fatalf("统计 edge_devices: %v", err)
	}
	if registered != 0 {
		t.Fatalf("前置不成立：设备 %d 在 edge_devices 里存在，本用例要求它不存在", orphanDev)
	}

	rows := queryDeviceLastTS(t, db)
	found := false
	for _, r := range rows {
		if r.DeviceID == orphanDev {
			found = true
		}
	}
	if !found {
		t.Fatalf("帧表里有数据、但 edge_devices 里没有的设备被漏掉了（枚举到 %v）——"+
			"生产上设备 2 就是这种形态（100,806 帧），漏掉不会有任何信号",
			deviceIDsOf(rows))
	}
}

// TestDeviceLastTS_EmptyFrameTableReturnsNothing 钉空表。
func TestDeviceLastTS_EmptyFrameTableReturnsNothing(t *testing.T) {
	requirePostgresAPI(t)
	db := testutil.OpenTestDB(t)
	seedDevicesWithFrames(t, db, nil, 5)

	rows := queryDeviceLastTS(t, db)
	if len(rows) != 0 {
		t.Fatalf("空帧表却枚举出 %d 台设备: %v", len(rows), deviceIDsOf(rows))
	}
}

// deviceIDsOf 只用于失败信息，让「枚举错了」这条失败能自己说明错在哪。
func deviceIDsOf(rows []devLastRow) []uint {
	out := make([]uint, 0, len(rows))
	for _, r := range rows {
		out = append(out, r.DeviceID)
	}
	return out
}
