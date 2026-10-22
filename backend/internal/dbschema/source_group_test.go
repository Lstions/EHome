package dbschema_test

import (
	"fmt"
	"os"
	"testing"

	"ehome/backend/internal/dbschema"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// openDB 打开测试库：EHOME_TEST_DB=postgres 走 PG，否则内存 SQLite。
func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("EHOME_TEST_DB") == "postgres" {
		dbname := envOr("EHOME_DB_NAME", "ehome_e2e")
		// 本助手**不走 testutil.OpenTestDB 的 schema 隔离**，直接连目标库的 public schema。
		// 2026-10-22 事故：带着 EHOME_DB_NAME=ehome 跑，把 dup0/dup1 夹具写进了生产库。
		// 因此这里必须显式挡住生产库名（production-name guard）。
		testutil.GuardProductionDB(t, dbname)
		dsn := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
			envOr("EHOME_DB_HOST", "127.0.0.1"), envOr("EHOME_DB_PORT", "5432"),
			envOr("EHOME_DB_USER", "ehome"), envOr("EHOME_DB_PASSWORD", "ehome123"),
			dbname)
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}
		return db
	}
	db, err := gorm.Open(sqlite.Open("file:"+t.Name()+"?mode=memory&cache=shared"),
		&gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	return db
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func migrateSourceGroup(t *testing.T, db *gorm.DB) {
	t.Helper()
	resetState(t, db)
	if err := db.AutoMigrate(&models.SourceGroup{}, &models.SourceGroupMember{},
		&models.SourceGroupFailoverLog{}, &models.SourceGroupHealth{}, &models.DataSource{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	if err := dbschema.EnsureSourceGroupSchema(db); err != nil {
		t.Fatalf("ensure schema: %v", err)
	}
}

// resetState 清理本包测试涉及的表与索引，让每个用例在**持久化的 PG**上也相互独立。
//
// 为什么必须：PG 测试库在同一包内跨用例持续存在，前一个用例建的部分唯一索引会
// 让后一个「模拟旧库无索引」的用例直接失败（实测：seed dup 被 ux_ds_one_active 拒绝）。
func resetState(t *testing.T, db *gorm.DB) {
	t.Helper()
	for _, stmt := range []string{
		"DROP INDEX IF EXISTS ux_ds_one_active",
		"DROP INDEX IF EXISTS ux_sgm_one_active",
		"DELETE FROM source_group_members",
		"DELETE FROM source_group_health",
		"DELETE FROM source_group_failover_logs",
		"DELETE FROM source_groups",
		"DELETE FROM data_sources",
	} {
		if err := db.Exec(stmt).Error; err != nil {
			// 表可能尚未建（sqlite 每用例独立库），忽略不存在错误
			continue
		}
	}
}

func TestEnsureSourceGroupSchema_CreatesPartialUniqueIndexes(t *testing.T) {
	db := openDB(t)
	migrateSourceGroup(t, db)

	for _, idx := range []string{"ux_sgm_one_active", "ux_ds_one_active"} {
		var n int64
		if err := db.Raw("SELECT COUNT(*) FROM sqlite_master WHERE type='index' AND name = ?", idx).Scan(&n).Error; err != nil {
			// PG 走 pg_indexes
			if err2 := db.Raw("SELECT COUNT(*) FROM pg_indexes WHERE indexname = ?", idx).Scan(&n).Error; err2 != nil {
				t.Fatalf("check index %s: sqlite=%v pg=%v", idx, err, err2)
			}
		}
		if n == 0 {
			t.Fatalf("索引 %s 不存在 —— 每组至多一条 active 的 DB 保证缺失", idx)
		}
	}
}

// TestSourceGroupMember_SecondActiveRejected 是「DB 真的拦住双 active」的行为断言。
//
// 反假绿：只查 catalog 不够（索引可能建在错的列上）；必须真插入第二条 active 并期望被拒。
func TestSourceGroupMember_SecondActiveRejected(t *testing.T) {
	db := openDB(t)
	migrateSourceGroup(t, db)

	grp := models.SourceGroup{Name: "g", Kind: models.SourceGroupKindPrimaryBackup, Category: "illuminance", Enabled: true}
	if err := db.Create(&grp).Error; err != nil {
		t.Fatalf("create group: %v", err)
	}
	m1 := models.SourceGroupMember{GroupID: grp.ID, EdgeDeviceID: 1, Priority: 100, Status: models.SourceMemberStatusActive}
	if err := db.Create(&m1).Error; err != nil {
		t.Fatalf("create m1: %v", err)
	}
	m2 := models.SourceGroupMember{GroupID: grp.ID, EdgeDeviceID: 2, Priority: 50, Status: models.SourceMemberStatusActive}
	if err := db.Create(&m2).Error; err == nil {
		t.Fatal("第二条 active 成员竟然插入成功 —— 部分唯一索引没有生效（假绿）")
	}

	// 同一成员重复加入同组也必须被拒。
	dup := models.SourceGroupMember{GroupID: grp.ID, EdgeDeviceID: 1, Status: models.SourceMemberStatusStandby}
	if err := db.Create(&dup).Error; err == nil {
		t.Fatal("同组重复成员插入成功 —— ux_sgm_member 未生效")
	}

	// 不同组同设备允许（主备跨组件复用设备是合法拓扑）。
	g2 := models.SourceGroup{Name: "g2", Kind: models.SourceGroupKindComposite, Category: "rainfall", Combine: models.CombineSum, Enabled: true}
	if err := db.Create(&g2).Error; err != nil {
		t.Fatalf("create g2: %v", err)
	}
	if err := db.Create(&models.SourceGroupMember{GroupID: g2.ID, EdgeDeviceID: 1, Status: ""}).Error; err != nil {
		t.Fatalf("same device in another group should be allowed: %v", err)
	}
}

// TestDataSource_SecondActiveRejected 验证旧表补洞（D-3）。
func TestDataSource_SecondActiveRejected(t *testing.T) {
	db := openDB(t)
	migrateSourceGroup(t, db)

	base := models.DataSource{DeviceID: 1, Category: "illuminance", SourceType: "edge_device", Status: servicesStatusActive, Name: "a"}
	if err := db.Create(&base).Error; err != nil {
		t.Fatalf("create ds1: %v", err)
	}
	second := models.DataSource{DeviceID: 1, Category: "illuminance", EdgeDeviceID: 2, SourceType: "edge_device", Status: servicesStatusActive, Name: "b"}
	if err := db.Create(&second).Error; err == nil {
		t.Fatal("data_sources 第二条 active 插入成功 —— ux_ds_one_active 未生效")
	}
}

// TestEnsureSourceGroupSchema_FailsClosedOnDuplicateActive 钉住「先查重再建索引」。
//
// 场景：旧库已有双 active（历史上只是应用层约束）。迁移必须报错并指出分组键，
// 而不是让 PG 抛一句无法定位的 could not create unique index。
func TestEnsureSourceGroupSchema_FailsClosedOnDuplicateActive(t *testing.T) {
	db := openDB(t)
	// 先只建表、不建索引（resetState 已 DROP 两个部分唯一索引）
	if err := db.AutoMigrate(&models.DataSource{}); err != nil {
		t.Fatalf("automigrate: %v", err)
	}
	resetState(t, db)
	// 纵深防御：即使有人真在生产形状副本上跑（EHOME_ALLOW_PROD_DB_TESTS=1），
	// 也必须把自己种的 dup 行清掉 —— 上次事故就是「种子留下、用例还通过」。
	t.Cleanup(func() {
		if err := db.Unscoped().Where("device_id = ? AND category = ?", 42, "rsoc").
			Delete(&models.DataSource{}).Error; err != nil {
			t.Logf("WARN: 清理 dup 夹具失败（请手工删除 device_id=42/category=rsoc）: %v", err)
		}
	})
	for i := 0; i < 2; i++ {
		ds := models.DataSource{DeviceID: 42, Category: "rsoc", EdgeDeviceID: uint(i + 1),
			SourceType: "edge_device", Status: servicesStatusActive, Name: fmt.Sprintf("dup%d", i)}
		if err := db.Create(&ds).Error; err != nil {
			t.Fatalf("seed dup: %v", err)
		}
	}
	err := dbschema.EnsureSourceGroupSchema(db)
	if err == nil {
		t.Fatal("存在重复 active 时 EnsureSourceGroupSchema 必须失败（fail-closed）")
	}
	if !contains(err.Error(), "migration_required") || !contains(err.Error(), "42") {
		t.Fatalf("错误信息必须带 migration_required 与分组键 42，实际: %v", err)
	}
}

const servicesStatusActive = "active"

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
