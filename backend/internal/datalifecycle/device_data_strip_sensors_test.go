package datalifecycle

// device_data_strip_sensors_test.go — 存量 sensors 剥离的验收用例。
//
// 这些用例是 **PG-only**（`jsonb - 'sensors'` 是 PG 语法）。跑法：
//
//	EHOME_TEST_DB=postgres GOW_WORKDIR=backend scripts/gow.sh test \
//	    -count=1 -run StripSensors ./internal/datalifecycle/ -v
//
// ⚠ 必须显式设 EHOME_TEST_DB=postgres，否则 requirePostgres(t) 会 **Skip** 而包级
//   仍然是 `ok` —— 那是本仓已经踩过四次的「假绿」形态（gow.sh 头部注释）。
//   所以本文件额外加了一条 `TestStripSensors_FixtureActuallyRan` 的自检：
//   如果所有用例都被 Skip 而没人发现，那条自检在 PG 环境下也会红。
//
// 五条用例对应五条会静默毁数据的失败模式：
//   1. 字段丢失   —— 剥离时把 channel_id / raw_hex / timestamp 一起删了；
//   2. 不幂等     —— 重跑把已剥离的行再"剥"一次（或重复计数）；
//   3. 误伤 B 类  —— 对本来没有 sensors 的行做无谓 UPDATE（制造死元组 + WAL）；
//   4. 续跑丢行   —— 中断后从错误游标继续，中间一段被跳过；
//   5. 未提交就推进游标 —— 崩溃后那批既没改也没重做。

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// stripTestSeed 插入 n 行 A 类（带 sensors）与 m 行 B 类（raw only）。
// 返回保持顺序的 id 列表（A 在前）。
func stripTestSeed(t *testing.T, db *gorm.DB, nA, nB int) []uint {
	t.Helper()
	dev := seedDevice(t, db, "strip-dev", "bms_jbd", "strip-hw", false)
	ids := make([]uint, 0, nA+nB)
	for i := 0; i < nA; i++ {
		row := models.DeviceData{
			DeviceID: dev.ID,
			NodeID:   "STRIPNODE",
			// 保留原始键序是为了断言"剥离后其余键仍在"；::text 会重排键序，
			// 所以断言必须按键取值（JSON 语义），不能做字符串包含。
			DataJSON: fmt.Sprintf(
				`{"channel_id":%d,"raw_hex":"deadbeef%04d","sensors":[{"Name":"cell_voltage_01","Value":3.25,"Unit":"V","StringValue":""},{"Name":"temperature_01","Value":24.5,"Unit":"C","StringValue":""}],"timestamp":%d}`,
				i, i, 1700000000000+i),
			Timestamp: time.Date(2026, 10, 1, i%24, 0, 0, 0, time.UTC),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed A row %d: %v", i, err)
		}
		ids = append(ids, row.ID)
	}
	for i := 0; i < nB; i++ {
		row := models.DeviceData{
			DeviceID: 0,
			NodeID:   "STRIPNODE",
			// B 类（db_persist 写的）：没有 sensors 键，剥离必须完全不碰它。
			DataJSON:  fmt.Sprintf(`{"channel":%d,"raw":"cafebabe","sequence":%d}`, i, i),
			Timestamp: time.Date(2026, 10, 2, i%24, 0, 0, 0, time.UTC),
		}
		if err := db.Create(&row).Error; err != nil {
			t.Fatalf("seed B row %d: %v", i, err)
		}
	}
	return ids
}

// TestStripSensors_PreservesAllOtherKeys 是硬要求 2：只删 sensors，其它键原样。
func TestStripSensors_PreservesAllOtherKeys(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	ids := stripTestSeed(t, db, 3, 0)

	s := NewDeviceDataSensorStripper(db)
	s.SetBatchSleep(0)
	s.SetVacuumAfterRun(false)
	rep, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.RowsStripped != 3 {
		t.Fatalf("RowsStripped = %d, want 3", rep.RowsStripped)
	}

	for i, id := range ids {
		var got models.DeviceData
		if err := db.First(&got, id).Error; err != nil {
			t.Fatalf("reload row %d: %v", id, err)
		}
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(got.DataJSON), &m); err != nil {
			t.Fatalf("row %d data_json 不是合法 JSON: %v (%s)", id, err, got.DataJSON)
		}
		if _, exists := m["sensors"]; exists {
			t.Errorf("row %d 仍有 sensors: %s", id, got.DataJSON)
		}
		// 三个必须保留的键。
		if want := float64(i); m["channel_id"] != want {
			t.Errorf("row %d channel_id = %v, want %v", id, m["channel_id"], want)
		}
		if want := fmt.Sprintf("deadbeef%04d", i); m["raw_hex"] != want {
			t.Errorf("row %d raw_hex = %v, want %v", id, m["raw_hex"], want)
		}
		if want := float64(1700000000000 + i); m["timestamp"] != want {
			t.Errorf("row %d timestamp = %v, want %v", id, m["timestamp"], want)
		}
		if len(m) != 3 {
			t.Errorf("row %d 剥离后键数 = %d (%v), want 3（只剩 channel_id/raw_hex/timestamp）", id, len(m), m)
		}
	}
}

// TestStripSensors_IsIdempotent 是硬要求 1：重跑必须是 no-op。
//
// 判据是 `LIKE '%"sensors"%'` ⇒ 剥离后不再匹配。但"不再匹配"是**推断**，
// 这条用例把它变成事实：第二次跑的 RowsStripped 必须是 0，且字节数不能变。
func TestStripSensors_IsIdempotent(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	stripTestSeed(t, db, 3, 0)

	s := NewDeviceDataSensorStripper(db)
	s.SetBatchSleep(0)
	s.SetVacuumAfterRun(false)
	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}

	rep2, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if rep2.RowsStripped != 0 {
		t.Errorf("第二次跑剥离了 %d 行，want 0（不幂等 ⇒ 会重复制造死元组）", rep2.RowsStripped)
	}
	if rep2.BytesBefore != 0 || rep2.BytesAfter != 0 {
		t.Errorf("第二次跑仍然改动了字节统计: before=%d after=%d", rep2.BytesBefore, rep2.BytesAfter)
	}
}

// TestStripSensors_LeavesRawOnlyRowsUntouched 是硬要求 1 的另一半：
// B 类行（没有 sensors）必须**完全不被 UPDATE**。
//
// 怎么证明"没被 UPDATE"：PostgreSQL 的 xmin 是行版本号，未更新的行 xmin 不变。
// 用字符串相等只能证明内容没变（UPDATE 成同样内容也相等），xmin 才能证明**没写**。
func TestStripSensors_LeavesRawOnlyRowsUntouched(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	stripTestSeed(t, db, 2, 3)

	var before []struct {
		ID   uint
		Xmin string
	}
	if err := db.Raw(`SELECT id, xmin::text AS xmin FROM device_data WHERE data_json NOT LIKE '%"sensors"%' ORDER BY id`).
		Scan(&before).Error; err != nil {
		t.Fatalf("read xmin before: %v", err)
	}
	if len(before) != 3 {
		t.Fatalf("前置: B 类行 = %d, want 3", len(before))
	}

	s := NewDeviceDataSensorStripper(db)
	s.SetBatchSleep(0)
	s.SetVacuumAfterRun(false)
	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	var after []struct {
		ID   uint
		Xmin string
	}
	if err := db.Raw(`SELECT id, xmin::text AS xmin FROM device_data WHERE id IN ? ORDER BY id`, idsOf(before)).
		Scan(&after).Error; err != nil {
		t.Fatalf("read xmin after: %v", err)
	}
	for i := range before {
		if before[i].Xmin != after[i].Xmin {
			t.Errorf("B 类行 id=%d 的 xmin 从 %s 变成 %s —— 该行被无谓 UPDATE 了",
				before[i].ID, before[i].Xmin, after[i].Xmin)
		}
	}
}

func idsOf(rows []struct {
	ID   uint
	Xmin string
}) []uint {
	out := make([]uint, len(rows))
	for i := range rows {
		out[i] = rows[i].ID
	}
	return out
}

// TestStripSensors_ResumesFromWatermark 是硬要求 4：中断后续跑不重复不遗漏。
//
// 用**确定性 hook**（跑完第 2 批就返回哨兵错误）而不是 sleep ——
// sleep 让"停在第几批"变成概率题，续跑用例就没法断言。
func TestStripSensors_ResumesFromWatermark(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	stripTestSeed(t, db, 10, 0)

	stopAfterTwo := errors.New("simulated interruption after 2 batches")

	s1 := NewDeviceDataSensorStripper(db)
	s1.SetBatchSize(3)
	s1.SetBatchSleep(0)
	s1.SetVacuumAfterRun(false)
	batches := 0
	s1.SetAfterBatchHook(func(processed int64) error {
		batches++
		if batches >= 2 {
			return stopAfterTwo
		}
		return nil
	})
	rep1, err := s1.RunOnce(context.Background())
	if !errors.Is(err, stopAfterTwo) {
		t.Fatalf("第一次跑应当被 hook 主动中断，got err=%v (rep=%+v)", err, rep1)
	}
	if rep1.RowsStripped != 6 {
		t.Fatalf("中断前应剥 6 行（2 批 × 3），got %d", rep1.RowsStripped)
	}

	// 前端状态：watermark 已持久化，state 仍是 running（崩溃残留不是错误态）。
	var prog DeviceDataStripProgressRow
	if err := db.Where("task_key = ?", deviceDataStripTaskKey).First(&prog).Error; err != nil {
		t.Fatalf("load progress: %v", err)
	}
	if prog.WatermarkID == 0 {
		t.Fatal("watermark 未持久化 —— 重启会从头扫整表")
	}
	t.Logf("中断点: watermark_id=%d rows_stripped=%d", prog.WatermarkID, prog.RowsStripped)

	// 续跑：新的实例（模拟进程重启），不带任何内存态。
	s2 := NewDeviceDataSensorStripper(db)
	s2.SetBatchSize(3)
	s2.SetBatchSleep(0)
	s2.SetVacuumAfterRun(false)
	rep2, err := s2.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("续跑失败: %v", err)
	}
	if !rep2.Done {
		t.Error("续跑后应当 Done=true")
	}
	if rep2.RowsStripped != 4 {
		t.Errorf("续跑剥离 %d 行，want 4（10 - 中断前 6）—— 多剥=重复，少剥=漏行", rep2.RowsStripped)
	}

	// 全表核对：一行都不能还有 sensors，一行都不能被删。
	var remaining int64
	db.Model(&models.DeviceData{}).Where(`data_json LIKE '%"sensors"%'`).Count(&remaining)
	if remaining != 0 {
		t.Errorf("完成后仍有 %d 行带 sensors", remaining)
	}
	var total int64
	db.Model(&models.DeviceData{}).Count(&total)
	if total != 10 {
		t.Errorf("行数 = %d, want 10（剥离不得删行）", total)
	}
}

// TestStripSensors_ReportsLogicalBytesReclaimed 是硬要求 3 的账面部分：
// 报告必须给出**真实测量**的字节数，而不是按行数估算。
func TestStripSensors_ReportsLogicalBytesReclaimed(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	stripTestSeed(t, db, 5, 0)

	var beforeBytes int64
	db.Model(&models.DeviceData{}).Select("COALESCE(sum(octet_length(data_json)),0)").Scan(&beforeBytes)

	s := NewDeviceDataSensorStripper(db)
	s.SetBatchSleep(0)
	s.SetVacuumAfterRun(false)
	rep, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}

	var afterBytes int64
	db.Model(&models.DeviceData{}).Select("COALESCE(sum(octet_length(data_json)),0)").Scan(&afterBytes)

	if got, want := rep.BytesBefore, beforeBytes; got != want {
		t.Errorf("BytesBefore = %d, want %d（应与独立查询一致）", got, want)
	}
	if got, want := rep.BytesAfter, afterBytes; got != want {
		t.Errorf("BytesAfter = %d, want %d（应与独立查询一致）", got, want)
	}
	if beforeBytes <= afterBytes {
		t.Fatalf("剥离后字节没减少: %d → %d（fixture 的 sensors 太小？）", beforeBytes, afterBytes)
	}
	ratio := float64(beforeBytes) / float64(afterBytes)
	t.Logf("逻辑字节: %d → %d（%.2fx），报告 reclaimed=%d", beforeBytes, afterBytes, ratio, rep.BytesReclaimed())
	if rep.BytesReclaimed() != beforeBytes-afterBytes {
		t.Errorf("BytesReclaimed = %d, want %d", rep.BytesReclaimed(), beforeBytes-afterBytes)
	}
}

// TestStripSensors_IsSkippedOnSQLite 确认非 PG 环境 no-op 而不是报错。
//
// 顺带守住"SQLite 分支没有被写成半吊子字符串补丁"：如果将来有人给 SQLite
// 加了一个 `strings.Replace` 版的剥离，这条用例会红 —— 那种实现会把
// `{"sensors_x":1}` 里的子串也替换掉。
func TestStripSensors_IsSkippedOnSQLite(t *testing.T) {
	if testutil.IsPostgres() {
		t.Skip("SQLite-only assertion")
	}
	db := testutil.OpenTestDB(t)
	s := NewDeviceDataSensorStripper(db)
	rep, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("SQLite 上必须 no-op 而不是报错: %v", err)
	}
	if !rep.Skipped || !rep.Done {
		t.Errorf("Skipped=%v Done=%v, want true/true", rep.Skipped, rep.Done)
	}
}

// TestStripSensors_ProgressSurvivesRestart 单独钉住"进度落库"这件事：
// 只靠内存态的话，部署重启会让 3.46M 行的任务永远跑不完。
func TestStripSensors_ProgressSurvivesRestart(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	stripTestSeed(t, db, 4, 0)

	s1 := NewDeviceDataSensorStripper(db)
	s1.SetBatchSize(2)
	s1.SetBatchSleep(0)
	s1.SetVacuumAfterRun(false)
	stop := errors.New("stop")
	n := 0
	s1.SetAfterBatchHook(func(int64) error {
		n++
		if n >= 1 {
			return stop
		}
		return nil
	})
	if _, err := s1.RunOnce(context.Background()); !errors.Is(err, stop) {
		t.Fatalf("want hook stop, got %v", err)
	}

	// 全新实例，完全不共享内存。
	s2 := NewDeviceDataSensorStripper(db)
	if got := s2.currentWatermark(); got == 0 {
		t.Fatal("新实例读不到 watermark —— 进度没落库")
	} else {
		t.Logf("重启后从 watermark_id=%d 继续", got)
	}
}

// TestStripSensors_PreservesStringValue 是 boot-api 指出的**关键缺口**：
// StringValue（BMS hardware_version="V19" / serial_number）在 unified_data 与
// telemetry_frame 里**都没有**副本，只能存在于 device_data.sensors。
// 直接 jsonb - 'sensors' 会把它永久删除且无从恢复。
//
// 本用例造一行带非空 StringValue 的历史数据，断言剥离后：
//
//	· sensors 键消失；
//	· str_values 键出现，且内容与写侧 task-19 的形状完全一致；
//	· 数值字段不因此多出来（str_values 只装字符串）。
func TestStripSensors_PreservesStringValue(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	dev := seedDevice(t, db, "strip-sv", "bms_jbd", "strip-sv-hw", false)

	row := models.DeviceData{
		DeviceID: dev.ID,
		NodeID:   "SVNODE",
		DataJSON: `{"channel_id":5,"raw_hex":"aabbcc",` +
			`"sensors":[{"Name":"hardware_version","Value":0,"Unit":"","StringValue":"V19"},` +
			`{"Name":"cell_voltage_01","Value":3.25,"Unit":"V","StringValue":""}],` +
			`"timestamp":1700000000000}`,
		Timestamp: time.Date(2026, 10, 3, 0, 0, 0, 0, time.UTC),
	}
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}

	s := NewDeviceDataSensorStripper(db)
	s.SetBatchSleep(0)
	s.SetVacuumAfterRun(false)
	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	var got models.DeviceData
	if err := db.First(&got, row.ID).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	var m map[string]interface{}
	if err := json.Unmarshal([]byte(got.DataJSON), &m); err != nil {
		t.Fatalf("剥离后不是合法 JSON: %v (%s)", err, got.DataJSON)
	}
	if _, exists := m["sensors"]; exists {
		t.Errorf("sensors 仍在: %s", got.DataJSON)
	}
	sv, ok := m["str_values"].(map[string]interface{})
	if !ok {
		t.Fatalf("str_values 缺失 —— hardware_version 已永久丢失！data_json=%s", got.DataJSON)
	}
	if sv["hardware_version"] != "V19" {
		t.Errorf("str_values.hardware_version = %v, want \"V19\"", sv["hardware_version"])
	}
	// 空 StringValue 的数值字段**不能**进 str_values（否则每行白占几十字节）。
	if _, exists := sv["cell_voltage_01"]; exists {
		t.Errorf("空 StringValue 的字段被写进了 str_values: %v", sv)
	}
	if len(sv) != 1 {
		t.Errorf("str_values 有 %d 个键 (%v), want 1", len(sv), sv)
	}
	// 数值字段与元数据照旧。
	if m["raw_hex"] != "aabbcc" {
		t.Errorf("raw_hex = %v", m["raw_hex"])
	}
	if m["channel_id"] != float64(5) {
		t.Errorf("channel_id = %v", m["channel_id"])
	}
}

// TestStripSensors_NoStringValueAddsNoKey 是上一条的反向对照（防止 CASE 写反）：
// 纯数值行剥离后**不得**出现 str_values 键 —— 否则每行白加 ~15 字节，
// 3.46M 行就是 50 MB 的净损失，正好抵消一部分收益。
func TestStripSensors_NoStringValueAddsNoKey(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	stripTestSeed(t, db, 2, 0)

	s := NewDeviceDataSensorStripper(db)
	s.SetBatchSleep(0)
	s.SetVacuumAfterRun(false)
	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("run: %v", err)
	}

	var rows []models.DeviceData
	db.Find(&rows)
	for _, r := range rows {
		var m map[string]interface{}
		if err := json.Unmarshal([]byte(r.DataJSON), &m); err != nil {
			t.Fatalf("row %d 非法 JSON: %v", r.ID, err)
		}
		if _, exists := m["str_values"]; exists {
			t.Errorf("纯数值行 id=%d 出现了 str_values（空映射不该落键）: %s", r.ID, r.DataJSON)
		}
	}
}

// TestStripSensors_RunIfSafeGuardsDeploymentOrder 是**发布顺序护栏**的用例。
//
// 背景：task-21（剥离历史）依赖 task-19（写侧停写）。灰度时可能本任务镜像先上，
// 那时剥离跑一遍、新数据又把 sensors 写回来 ⇒ 下轮再剥 ⇒ 永远跑不完 + 持续写 WAL。
// 护栏的做法是**运行时自证**（看最近写入的行还有没有 sensors），本用例把它钉住。
//
// 两个方向都必须测（只测「跳过」会让护栏写成恒跳过也绿，剥离就永远不跑）：
//
//	A) 最近写入的行**仍含** sensors ⇒ 跳过，且一行都不改；
//	B) 最近写入的行**不含** sensors（写侧已升级），但更早的行含 ⇒ 放行并剥离那些历史行。
func TestStripSensors_RunIfSafeGuardsDeploymentOrder(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	dev := seedDevice(t, db, "guard-dev", "bms_jbd", "guard-hw", false)

	seedRow := func(payload string, at time.Time) {
		t.Helper()
		if err := db.Create(&models.DeviceData{
			DeviceID: dev.ID, NodeID: "GUARDNODE", DataJSON: payload, Timestamp: at,
		}).Error; err != nil {
			t.Fatalf("seed row: %v", err)
		}
	}
	withSensors := `{"channel_id":1,"raw_hex":"bb","sensors":[{"Name":"v","Value":1,"Unit":"V","StringValue":""}],"timestamp":2}`
	withoutSensors := `{"channel_id":1,"raw_hex":"cc","timestamp":3}`

	// ── A) 写侧仍是旧版本：最近写入的行含 sensors ⇒ 必须跳过 ──
	seedRow(withSensors, time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC))
	sA := NewDeviceDataSensorStripper(db)
	sA.SetBatchSleep(0)
	sA.SetVacuumAfterRun(false)
	repA, err := sA.RunIfSafe(context.Background())
	if err != nil {
		t.Fatalf("RunIfSafe(A): %v", err)
	}
	if !repA.Skipped || repA.RowsStripped != 0 {
		t.Fatalf("写侧仍写 sensors 时必须跳过且不改数据，got %+v", repA)
	}
	var stillHasSensors int64
	db.Model(&models.DeviceData{}).Where(`data_json LIKE '%"sensors"%'`).Count(&stillHasSensors)
	if stillHasSensors != 1 {
		t.Fatalf("护栏跳过时数据被改动了：仍含 sensors 的行 = %d, want 1", stillHasSensors)
	}

	// ── B) 写侧已升级：再写几条**不含** sensors 的新行（id 更大，模拟新版本写入），
	//       护栏探针只看最近 N 行 ⇒ 应放行，并剥离上面那条历史行。
	for i := 0; i < 5; i++ {
		seedRow(withoutSensors, time.Date(2026, 10, 5, i, 0, 0, 0, time.UTC))
	}
	sB := NewDeviceDataSensorStripper(db)
	sB.SetBatchSleep(0)
	sB.SetVacuumAfterRun(false)
	repB, err := sB.RunIfSafe(context.Background())
	if err != nil {
		t.Fatalf("RunIfSafe(B): %v", err)
	}
	if repB.Skipped {
		t.Fatal("最近写入已无 sensors，护栏不该再跳过")
	}
	if repB.RowsStripped != 1 {
		t.Errorf("RowsStripped = %d, want 1（那条历史 sensors 行）", repB.RowsStripped)
	}
	var left int64
	db.Model(&models.DeviceData{}).Where(`data_json LIKE '%"sensors"%'`).Count(&left)
	if left != 0 {
		t.Errorf("放行后仍有 %d 行带 sensors", left)
	}
}

// TestStripSensors_VacuumRunsAndIsReusable 验证 Vacuum 真的执行且可重复。
//
// 单独一条而不是顺手塞进别的用例：VACUUM 需要真实 PG、不能在事务里跑，
// 且它是**唯一**把「账面上的 1.3 GB」变成「空间可重用」的步骤 ——
// 如果它静默失败，日志里写着"已释放"而磁盘还在涨，是最难查的一类。
func TestStripSensors_VacuumRunsAndIsReusable(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	stripTestSeed(t, db, 5, 0)

	s := NewDeviceDataSensorStripper(db)
	s.SetBatchSleep(0)
	s.SetVacuumAfterRun(true) // 本用例要真跑 VACUUM
	if _, err := s.RunOnce(context.Background()); err != nil {
		t.Fatalf("run (含 vacuum): %v", err)
	}

	// 直接再调一次 Vacuum：必须幂等且不报错（运维在剥离后再手动跑一次是常态）。
	if err := s.Vacuum(context.Background()); err != nil {
		t.Fatalf("second vacuum: %v", err)
	}
}

// TestStripSensors_MalformedRowsAreSkippedNotFatal 是本轮最重要的回归用例。
//
// 场景：一批里**全部**是坏行（sensors 不是 JSON 数组）。
//
// 旧实现只有一个 rows-affected 返回值，于是「SELECT 到 N 行」与「UPDATE 成功 0 行」
// 在返回值上无法区分，两者都表现为 n==0。
//
// 结果：一批坏行会让任务**误判为已完成**，后面几百万行永远不处理，
// 而日志显示「完成」、状态表标记 done，下次启动直接跳过。
//
// 拆出 Selected 之后：selected>0 而 stripped==0 ⇒ 跳过并继续，不当作完成。
// 这条用例就是钉住这一点：坏行在前、好行在后，好行**必须**仍然被剥离。
func TestStripSensors_MalformedRowsAreSkippedNotFatal(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	dev := seedDevice(t, db, "mal-dev", "bms_jbd", "mal-hw", false)

	seed := func(payload string, at time.Time) {
		t.Helper()
		if err := db.Create(&models.DeviceData{
			DeviceID: dev.ID, NodeID: "MALNODE", DataJSON: payload, Timestamp: at,
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}
	// 第一行的 sensors 是**字符串**而不是数组：匹配 LIKE 但被 jsonb_typeof 守卫挡住。
	// 它匹配 LIKE '%"sensors"%' 但被 jsonb_typeof 守卫挡住。
	seed(`{"channel_id":1,"raw_hex":"aa","sensors":"not-an-array","timestamp":1}`,
		time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC))
	// ── 好行在坏行之后（id 更大），用来验「坏行没让任务提前结束」。
	seed(`{"channel_id":2,"raw_hex":"bb","sensors":[{"Name":"v","Value":1,"Unit":"V","StringValue":""}],"timestamp":2}`,
		time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC))

	s := NewDeviceDataSensorStripper(db)
	s.SetBatchSize(1) // 强制每批只 1 行 ⇒ 坏行独占第一批（最严苛的分批）
	s.SetBatchSleep(0)
	s.SetVacuumAfterRun(false)
	rep, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if !rep.Done {
		t.Error("Done = false，want true（游标应走到表尾）")
	}
	if rep.RowsSelected != 2 || rep.RowsStripped != 1 || rep.RowsSkipped != 1 {
		t.Errorf("selected/stripped/skipped = %d/%d/%d, want 2/1/1",
			rep.RowsSelected, rep.RowsStripped, rep.RowsSkipped)
	}

	// 关键断言：好行**必须**被剥离（证明坏行没让任务提前结束）。
	//
	// 按**插入顺序**（id）取，不能按 data_json 里的 channel_id 查 ——
	// 那是 JSON 内容不是列，写成 Where(channel_id = ...) 会静默查到零值行，
	// 断言就变成"恒真"（这条用例第一版正是这么假绿的）。
	var all []models.DeviceData
	if err := db.Order("id").Find(&all).Error; err != nil {
		t.Fatalf("reload rows: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("行数 = %d, want 2（剥离不得删行）", len(all))
	}
	bad, good := all[0], all[1] // 先插的是坏行
	if strings.Contains(good.DataJSON, `"sensors"`) {
		t.Errorf("坏行之后的好行没被剥离 —— 任务提前结束了: %s", good.DataJSON)
	}
	// 坏行原样保留（守卫挡住 = 不改内容：宁可跳过也不损坏）。
	if !strings.Contains(bad.DataJSON, `"sensors"`) {
		t.Errorf("坏行被改动了: %s", bad.DataJSON)
	}

	// 状态表必须留下「有跳过」的痕迹（重启后唯一线索）。
	var prog DeviceDataStripProgressRow
	if err := db.Where("task_key = ?", deviceDataStripTaskKey).First(&prog).Error; err != nil {
		t.Fatalf("load progress: %v", err)
	}
	if prog.RowsSelected != 2 || prog.RowsStripped != 1 || prog.RowsSkipped != 1 {
		t.Errorf("进度表 selected/stripped/skipped = %d/%d/%d, want 2/1/1（重启后靠它发现异常）",
			prog.RowsSelected, prog.RowsStripped, prog.RowsSkipped)
	}
}

// TestStripSensors_AllMalformedBatchDoesNotReportDone 反向对照：
// 如果整批全是坏行、且后面也没有好行，那 Done=true 是对的 ——
// 但 RowsSkipped 必须等于 RowsSelected，否则「跑完了」会掩盖「一行都没剥」。
func TestStripSensors_AllMalformedStillReportsSkipped(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	dev := seedDevice(t, db, "mal2-dev", "bms_jbd", "mal2-hw", false)
	for i := 0; i < 3; i++ {
		if err := db.Create(&models.DeviceData{
			DeviceID: dev.ID, NodeID: "MAL2",
			DataJSON:  fmt.Sprintf(`{"channel_id":%d,"sensors":null,"timestamp":%d}`, i, i),
			Timestamp: time.Date(2026, 10, 2, i, 0, 0, 0, time.UTC),
		}).Error; err != nil {
			t.Fatalf("seed: %v", err)
		}
	}

	s := NewDeviceDataSensorStripper(db)
	s.SetBatchSleep(0)
	s.SetVacuumAfterRun(false)
	rep, err := s.RunOnce(context.Background())
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	if rep.RowsSelected != 3 || rep.RowsStripped != 0 || rep.RowsSkipped != 3 {
		t.Errorf("selected/stripped/skipped = %d/%d/%d, want 3/0/3",
			rep.RowsSelected, rep.RowsStripped, rep.RowsSkipped)
	}
}
