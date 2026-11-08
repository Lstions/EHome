package datalifecycle

// telemetry_frame_pg_test.go — L2 帧表生命周期在**真实 PostgreSQL** 上的验收。
//
// 为什么必须真跑 PG（SQLite 覆盖不到的三件事）：
//  1. 建表路径：分区母表 + 月分区由裸 DDL 建，SQLite 下 MigrateTableToPartitioned 是 no-op；
//  2. 保留期的分区 DROP：DROP PARTITION 是 PG 专属（O(1) 元数据操作）；
//  3. 批量删的 SQL 形状：帧表没有 id 列，用 (device_id, ts, seq) 行值 IN 子查询 ——
//     这个语法在 SQLite 与 PG 的行为差异（类型推导/行值比较）只有真库能验。
//
// 跑法（与其它 PG 用例一致）：
//
//	EHOME_TEST_DB=postgres EHOME_DB_NAME=ehome_test go test ./internal/datalifecycle/ -run TestPGTelemetryFrame

import (
	"context"
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// seedFrameOnPartitioned 往分区母表写一帧（走生产同一张表，不是普通表）。
func seedFrameOnPartitioned(t *testing.T, db *gorm.DB, deviceID uint, ts time.Time) {
	t.Helper()
	f := models.TelemetryFrame{
		DeviceID:  deviceID,
		TS:        ts,
		Seq:       0,
		FrameKind: models.TelemetryFrameKindKey,
		SlotID:    1,
		Payload:   []byte{0x01, 0x00, 0xFF},
	}
	if err := db.Create(&f).Error; err != nil {
		t.Fatalf("seed frame device=%d ts=%s: %v", deviceID, ts.Format(time.RFC3339), err)
	}
}

// TestPGTelemetryFramePartitionedLifecycle 是帧表生命周期的 PG 端到端验收：
// 建分区母表 → 写跨月数据 → 保留期按**分区 DROP** 清理 → 彻底删除设备按主键锚点清空。
func TestPGTelemetryFramePartitionedLifecycle(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)

	// 1) 走生产同一入口建表（内部 AutoMigrate slot 表 + 建分区母表）。
	dropFrameRelations(t, db)
	t.Cleanup(func() { dropFrameRelations(t, db) })
	if err := MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("MigrateTelemetryFrameToPartitioned: %v", err)
	}
	if !IsTablePartitioned(db, TelemetryFrameTable) {
		t.Fatalf("%s 必须是分区母表（relkind='p'）—— 否则保留期只能逐行 DELETE，永远跑不完", TelemetryFrameTable)
	}
	// 主键必须是 (device_id, ts, seq)：PG 要求分区键进主键。
	var pkCols string
	if err := db.Raw(`SELECT string_agg(a.attname, ',' ORDER BY array_position(i.indkey, a.attnum))
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey)
		WHERE c.relname = ? AND n.nspname = current_schema() AND i.indisprimary`, TelemetryFrameTable).Scan(&pkCols).Error; err != nil {
		t.Fatalf("lookup pk: %v", err)
	}
	if pkCols != "device_id,ts,seq" {
		t.Fatalf("主键 = %q, want device_id,ts,seq", pkCols)
	}

	dev := seedDevice(t, db, "pg-frame-life", "bms_jbd", "pg-frame-hw", false)

	// 2) 写跨月数据：4 个月前（应被赶出保留期）与 3 天前（应保留）。
	//    用月初对齐的远端月份，确保整月分区可以被 DROP。
	//
	// ⚠ 必须先**手工建出那个历史分区**：滚动保障只覆盖 [上月, 未来 3 月]
	// （partitionRollaheadMonths），而 4 个月前的月份不在窗口内 —— PG 分区表
	// 没有兜底 default 分区，直接 INSERT 会报
	// "no partition of relation ... found for row"。这正是生产上「历史数据回填
	// 超出滚动窗口」时的真实形状，所以用例必须显式建分区而不是指望滚动保障。
	oldMonth := monthStart(time.Now().UTC()).AddDate(0, -4, 0).Add(48 * time.Hour)
	if err := NewPartitionManager(db).createPartitionIfNotExists(
		partitionName(TelemetryFrameTable, oldMonth), TelemetryFrameTable,
		monthStart(oldMonth), addMonths(oldMonth, 1)); err != nil {
		t.Fatalf("create history partition %s: %v", partitionName(TelemetryFrameTable, oldMonth), err)
	}
	fresh := time.Now().UTC().Add(-72 * time.Hour)
	seedFrameOnPartitioned(t, db, dev.ID, oldMonth)
	seedFrameOnPartitioned(t, db, dev.ID, fresh)
	// 另一台设备：一帧旧的（必须被保留期清掉）+ 一帧新的（必须留住，且
	// 不能被"删除设备 A"的操作误删）—— 后者是 purge 作用域的守卫。
	otherDev := seedDevice(t, db, "pg-frame-life-2", "bms_jbd", "pg-frame-hw-2", false)
	seedFrameOnPartitioned(t, db, otherDev.ID, oldMonth)
	seedFrameOnPartitioned(t, db, otherDev.ID, fresh)

	// 3) 保留期：先跑分区 DROP（O(1)），再跑精确删（边界分区）。
	//    这里直接验分区 DROP 路径 —— 它是「帧数据真的会消失」的唯一可靠实现。
	if err := (&RetentionTask{db: db}).dropExpiredPartitionsOnce(context.Background(), time.Now().UTC(), 90); err != nil {
		t.Fatalf("dropExpiredPartitionsOnce: %v", err)
	}
	// 4 个月前的分区必须已被 DROP；本月分区必须还在。
	var oldPart string
	db.Raw(`SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = ? AND n.nspname = current_schema()`,
		partitionName(TelemetryFrameTable, oldMonth)).Scan(&oldPart)
	if oldPart != "" {
		t.Errorf("4 个月前的分区 %s 仍在 —— 保留期的分区 DROP 没有覆盖 telemetry_frame",
			partitionName(TelemetryFrameTable, oldMonth))
	}
	var curPart string
	db.Raw(`SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = ? AND n.nspname = current_schema()`,
		partitionName(TelemetryFrameTable, time.Now().UTC())).Scan(&curPart)
	if curPart == "" {
		t.Errorf("当月分区 %s 不存在 —— 保留期把不该删的也删了",
			partitionName(TelemetryFrameTable, time.Now().UTC()))
	}
	var left int64
	db.Model(&models.TelemetryFrame{}).Where("device_id IN ?", []uint{dev.ID, otherDev.ID}).Count(&left)
	if left != 2 {
		t.Errorf("分区 DROP 后帧数 = %d, want 2（两台设备各剩 3 天前那帧）", left)
	}

	// 4) 彻底删除设备：批量删除必须用 (device_id, ts, seq) 锚点（帧表没有 id 列）。
	//    这条 SQL 形状在 PG 上必须真的能跑 —— 用旧表的 "WHERE id IN (...)" 会报
	//    column "id" does not exist。
	if _, err := (&DevicePurger{db: db}).deleteDeviceRows(context.Background(), dev.ID); err != nil {
		t.Fatalf("deleteDeviceRows: %v", err)
	}
	var devLeft int64
	db.Model(&models.TelemetryFrame{}).Where("device_id = ?", dev.ID).Count(&devLeft)
	if devLeft != 0 {
		t.Errorf("彻底删除后该设备仍有 %d 帧（「设备删了但帧数据还在」）", devLeft)
	}
	var otherLeft int64
	db.Model(&models.TelemetryFrame{}).Where("device_id = ?", otherDev.ID).Count(&otherLeft)
	if otherLeft != 1 {
		t.Errorf("误删了其它设备的帧：%d 行, want 1", otherLeft)
	}
}

// TestPGTelemetryFrameMergeMigrates 是合并历史在 PG 上的验收（行值 IN 子查询 + ON CONFLICT）。
func TestPGTelemetryFrameMergeMigrates(t *testing.T) {
	requirePostgres(t)
	db := testutil.OpenTestDB(t)
	dropFrameRelations(t, db)
	t.Cleanup(func() { dropFrameRelations(t, db) })
	if err := MigrateTelemetryFrameToPartitioned(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	target := seedDevice(t, db, "pg-merge-target", "bms_jbd", "t-hw", false)
	src := seedDevice(t, db, "pg-merge-src", "bms_jbd", "s-hw", true)
	now := time.Now().UTC().Truncate(time.Second)
	seedFrameOnPartitioned(t, db, target.ID, now.Add(-time.Minute))
	seedFrameOnPartitioned(t, db, src.ID, now.Add(-3*time.Minute))
	seedFrameOnPartitioned(t, db, src.ID, now.Add(-2*time.Minute))

	moved, skipped, err := MigrateTelemetryFramesToDevice(context.Background(), db, target.ID, []uint{src.ID}, 100)
	if err != nil {
		t.Fatalf("MigrateTelemetryFramesToDevice: %v", err)
	}
	if moved != 2 || skipped != 0 {
		t.Errorf("moved=%d skipped=%d, want 2/0", moved, skipped)
	}
	var srcLeft, targetCnt int64
	db.Model(&models.TelemetryFrame{}).Where("device_id = ?", src.ID).Count(&srcLeft)
	db.Model(&models.TelemetryFrame{}).Where("device_id = ?", target.ID).Count(&targetCnt)
	if srcLeft != 0 {
		t.Errorf("源设备仍有 %d 帧（复制成功但删源失败 = 数据重复）", srcLeft)
	}
	if targetCnt != 3 {
		t.Errorf("目标设备帧数 = %d, want 3", targetCnt)
	}
}

// dropFrameRelations 幂等清理帧表关系（母表 + 全部月分区 + slot 表）。
func dropFrameRelations(t *testing.T, db *gorm.DB) {
	t.Helper()
	// 只匹配**表/分区**（relkind 'r' / 'p'）：主键索引 telemetry_frame_pkey 的
	// relname 也以 telemetry_frame_ 开头，不过滤会让 DROP TABLE IF EXISTS 打到
	// 索引上报 "is not a table"，且是否触发取决于 pg_class 的返回顺序（顺序变了
	// 就假绿）。先删分区（'r'），再删母表（'p'），逻辑更直白。
	var names []string
	db.Raw(`SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p') AND (c.relname = ? OR c.relname LIKE ?)
		  AND n.nspname = current_schema()
		ORDER BY (c.relkind = 'r') DESC`,
		TelemetryFrameTable, TelemetryFrameTable+"_%").Scan(&names)
	for _, n := range names {
		if err := db.Exec("DROP TABLE IF EXISTS " + n + " CASCADE").Error; err != nil {
			t.Fatalf("drop %s: %v", n, err)
		}
	}
	db.Exec("DROP TABLE IF EXISTS telemetry_slot CASCADE")
}
