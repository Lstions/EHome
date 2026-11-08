package telemetry

import (
	"context"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"

	"ehome/backend/internal/datalifecycle"
)

// PG 集成用例：验证与 partition_mgr **四处同改**里的「建表路径」真的能建出
// 方案 §4.1 要的那张表（月分区 + 主键 (device_id, ts, seq) + 只 2 条索引）。
//
// # 为什么必须真跑 PG
//
// 建表模板是裸 SQL 字符串：写错一个列名、把 PARTITION BY 的键写成没进主键的列、
// 或用 SQLite 不支持的语法（部分索引 WHERE、BYTEA），**在 SQLite 单测里全绿**，
// 但生产启动时 MigrateTableToPartitioned 会失败 —— 而 main.go 里 unified_data/
// device_data 的迁移失败是 logger.Fatalf（拒绝启动）。这类缺陷只能在真 PG 上抓。
//
// # 为什么本用例反过来 import datalifecycle
//
// 只在 **_test.go** 里 import：生产代码的依赖方向仍是 telemetry → models。
// 我们不重复实现一遍 DDL（那必然漂移），而是调用生产同一个入口
// datalifecycle.MigrateTableToPartitioned —— 它读的正是 partitionParentDDL /
// partitionParentIndexes / partitionCopyColumns 三份模板。
//
// 需要 EHOME_TEST_DB=postgres（与 datalifecycle 的 PG 用例同一开关），
// 复用 testutil 的那套 env 命名（EHOME_DB_*）。
func TestPGPartitionedFrameTable(t *testing.T) {
	if os.Getenv("EHOME_TEST_DB") != "postgres" {
		t.Skip("需要 PostgreSQL（EHOME_TEST_DB=postgres）；本用例验证分区 DDL 真能建表")
	}
	db := openPGTestDB(t)

	const table = "telemetry_frame"
	// 幂等清理：分区母表 + 所有月份分区（CASCADE 连带）。
	dropFrameTable(t, db)
	t.Cleanup(func() { dropFrameTable(t, db) })

	if err := datalifecycle.MigrateTableToPartitioned(db, table, table+"_legacy"); err != nil {
		t.Fatalf("MigrateTableToPartitioned(telemetry_frame): %v\n"+
			"这条失败意味着生产启动时同样会失败（main.go 对分区迁移是 Fatal）", err)
	}

	// 1) 必须是分区母表（relkind='p'），且分区键正确。
	var relkind string
	if err := db.Raw(`SELECT c.relkind FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = ? AND n.nspname = current_schema()`, table).Scan(&relkind).Error; err != nil {
		t.Fatalf("lookup relkind: %v", err)
	}
	if relkind != "p" {
		t.Fatalf("telemetry_frame relkind = %q, want 'p'（分区母表）", relkind)
	}

	// 2) 主键必须是 (device_id, ts, seq) —— PG 要求分区键 ts 在主键里。
	var pkCols string
	if err := db.Raw(`SELECT string_agg(a.attname, ',' ORDER BY array_position(i.indkey, a.attnum))
		FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		JOIN pg_attribute a ON a.attrelid = c.oid AND a.attnum = ANY(i.indkey)
		WHERE c.relname = ? AND n.nspname = current_schema() AND i.indisprimary`, table).Scan(&pkCols).Error; err != nil {
		t.Fatalf("lookup primary key: %v", err)
	}
	if pkCols != "device_id,ts,seq" {
		t.Fatalf("主键 = %q, want device_id,ts,seq", pkCols)
	}

	// 3) 索引总数（方案 §4.1「只保留 2 条」：主键 + 关键帧部分索引）。
	var idxCount int64
	if err := db.Raw(`SELECT count(*) FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = ? AND n.nspname = current_schema()`, table).Scan(&idxCount).Error; err != nil {
		t.Fatalf("count indexes: %v", err)
	}
	if idxCount != 2 {
		var defs []string
		db.Raw(`SELECT pg_get_indexdef(i.indexrelid) FROM pg_index i
			JOIN pg_class c ON c.oid = i.indrelid
			JOIN pg_namespace n ON n.oid = c.relnamespace
			WHERE c.relname = ? AND n.nspname = current_schema()`, table).Scan(&defs)
		t.Fatalf("索引数 = %d, want 2（主键 + 关键帧部分索引）:\n%v", idxCount, defs)
	}

	// 4) 关键帧部分索引必须真的带 WHERE frame_kind = 1。
	var partialDefs []string
	if err := db.Raw(`SELECT pg_get_indexdef(i.indexrelid) FROM pg_index i
		JOIN pg_class c ON c.oid = i.indrelid
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname = ? AND n.nspname = current_schema() AND i.indpred IS NOT NULL`, table).Scan(&partialDefs).Error; err != nil {
		t.Fatalf("lookup partial index: %v", err)
	}
	if len(partialDefs) != 1 {
		t.Fatalf("部分索引数 = %d, want 1: %v", len(partialDefs), partialDefs)
	}

	// 5) 写入 + 读回：按月分区真的接住了数据（分区不存在时 PG 报
	//    "no partition of relation found for row"）。
	now := time.Now().UTC()
	frames := []EncodedFrame{
		{DeviceID: 1, TS: now, Seq: 0, FrameKind: 1, SlotID: 1, Payload: []byte{0x01, 0x00}},
		{DeviceID: 1, TS: now.Add(time.Second), Seq: 0, FrameKind: 0, SlotID: 1, Payload: []byte{0x00, 0x01, 0xFF}},
	}
	// slot 表（外键式引用在方案里没建，但写入需要 slot 行存在才能解码 —— 这里只验表结构）
	if err := db.Exec(`CREATE TABLE IF NOT EXISTS telemetry_slot (
		slot_id smallserial PRIMARY KEY,
		signature varchar(64) NOT NULL UNIQUE,
		command_template_id bigint NOT NULL DEFAULT 0,
		spec_version smallint NOT NULL,
		field_count smallint NOT NULL,
		fields_json text NOT NULL,
		created_at timestamptz
	)`).Error; err != nil {
		t.Fatalf("create telemetry_slot: %v", err)
	}
	t.Cleanup(func() { db.Exec("DROP TABLE IF EXISTS telemetry_slot CASCADE") })

	dict := NewDBSlots(db)
	store := NewStore(db, dict, nil)
	if err := store.PersistFrames(frames); err != nil {
		t.Fatalf("insert frames into partitioned table: %v", err)
	}
	var got int64
	if err := db.Raw("SELECT count(*) FROM " + table).Scan(&got).Error; err != nil {
		t.Fatalf("count rows: %v", err)
	}
	if got != int64(len(frames)) {
		t.Fatalf("rows = %d, want %d", got, len(frames))
	}

	// 6) 分区裁剪：本月分区必须存在且能直接按 ts 范围查到。
	var partitionName string
	if err := db.Raw(`SELECT c.relname FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relname LIKE ? AND c.relkind = 'r' AND n.nspname = current_schema()
		ORDER BY c.relname DESC LIMIT 1`, table+"_%").Scan(&partitionName).Error; err != nil {
		t.Fatalf("lookup partition: %v", err)
	}
	if partitionName == "" {
		t.Fatal("没有任何月分区 —— 写入会在生产上报 no partition of relation found")
	}
	t.Logf("分区母表就绪：pk=(%s) 索引=%d 部分索引=%d 当前月分区=%s", pkCols, idxCount, len(partialDefs), partitionName)
}

// openPGTestDB 用 EHOME_DB_* 环境变量连 PG，并建一个隔离 schema。
func openPGTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	base := dsnFromEnv("")
	schema := "test_telemetry_" + itoaNano(time.Now().UnixNano())
	admin, err := gorm.Open(postgres.Open(base), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	if err := admin.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create schema %s: %v", schema, err)
	}
	sqlDB, _ := admin.DB()
	if sqlDB != nil {
		_ = sqlDB.Close()
	}
	db, err := gorm.Open(postgres.Open(dsnFromEnv(schema)), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
	if err != nil {
		t.Fatalf("open postgres (schema %s): %v", schema, err)
	}
	t.Cleanup(func() {
		sqlDB, _ := db.DB()
		if sqlDB != nil {
			_ = sqlDB.Close()
		}
		adm, err := gorm.Open(postgres.Open(base), &gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)})
		if err == nil {
			adm.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")
			if s, e := adm.DB(); e == nil {
				_ = s.Close()
			}
		}
	})
	return db
}

// dsnFromEnv 拼 PG DSN（与 testutil 的 env 命名一致）。
func dsnFromEnv(schema string) string {
	host := envOr("EHOME_DB_HOST", "127.0.0.1")
	port := envOr("EHOME_DB_PORT", "5432")
	user := envOr("EHOME_DB_USER", "ehome")
	pass := envOr("EHOME_DB_PASSWORD", "ehome123")
	name := envOr("EHOME_DB_NAME", "ehome_test")
	dsn := "host=" + host + " port=" + port + " user=" + user + " password=" + pass +
		" dbname=" + name + " sslmode=disable"
	if schema != "" {
		dsn += " search_path=" + schema + ",public"
	}
	return dsn
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func itoaNano(n int64) string {
	const digits = "0123456789"
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = digits[n%10]
		n /= 10
	}
	return string(buf[i:])
}

// dropFrameTable 清理母表与全部月份分区。
func dropFrameTable(t *testing.T, db *gorm.DB) {
	t.Helper()
	// 只匹配**表/分区**（relkind 'r' 普通表 / 'p' 分区母表）。
	//
	// 为什么必须过滤 relkind: telemetry_frame 上的主键索引叫 telemetry_frame_pkey，
	// 它的 relname 同样以 "telemetry_frame" 开头。不过滤时 DROP TABLE IF EXISTS
	// 会打到索引上，报 "telemetry_frame_pkey is not a table" —— 而且这个失败取决于
	// pg_class 的返回顺序（表先到时索引随 CASCADE 一起消失，测试就假绿）。
	var names []string
	db.Raw(`SELECT c.relname FROM pg_class c
		JOIN pg_namespace n ON n.oid = c.relnamespace
		WHERE c.relkind IN ('r', 'p') AND c.relname LIKE ? AND n.nspname = current_schema()
		ORDER BY (c.relkind = 'r') DESC`, "telemetry_frame%").Scan(&names)
	for _, n := range names {
		if err := db.Exec("DROP TABLE IF EXISTS " + n + " CASCADE").Error; err != nil {
			t.Fatalf("drop %s: %v", n, err)
		}
	}
}

// 保持 context 引用（DecodeRange 用得到；本文件暂未直接使用，留作扩展点）。
var _ = context.Background
