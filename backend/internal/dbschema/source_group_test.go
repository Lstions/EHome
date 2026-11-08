package dbschema_test

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"ehome/backend/internal/dbschema"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/driver/postgres"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// TestMain 在整包测试结束时删掉本进程的隔离 schema。
//
// ============ 为什么必须是 TestMain 而不是 t.Cleanup ============
//
// 本包的 schema 是**整个进程复用**的（DDL 要跨用例持久化），
// 所以"删 schema"不能挂在任何一条用例上 —— 那会误删后续用例正在用的 schema。
//
// 而 Go 的 testing 只有 TestMain 能表达"整包跑完之后"这个时机。
//
// ============ 为什么删不掉也无所谓 ============
//
// 删失败（进程被 SIGKILL / PG 不可达）时 schema 会残留，
// 而 testutil 的回收器**认识 dbschema_ 前缀**，会在它超过 2h 且无活主时清掉。
// 这条兜底正是「回收器前缀清单里必须有 dbschema_」的原因 ——
// 见 testutil/reap_test.go 的 TestReaperRecognizesDbschemaPrefix。
func TestMain(m *testing.M) {
	code := m.Run()
	if os.Getenv("EHOME_TEST_DB") == "postgres" {
		cleanupSchemaOnExit()
	}
	os.Exit(code)
}

// cleanupSchemaOnExit 尽力删掉本进程的隔离 schema（失败只打印，不改变退出码）。
func cleanupSchemaOnExit() {
	// 该用例集合从未走到 openDB ⇒ 没有 schema 可删。
	if dbschemaSchema == "" {
		return
	}
	host := envOr("EHOME_DB_HOST", "127.0.0.1")
	port := envOr("EHOME_DB_PORT", "5432")
	user := envOr("EHOME_DB_USER", "ehome")
	pass := envOr("EHOME_DB_PASSWORD", "ehome123")
	dbname := envOr("EHOME_DB_NAME", "ehome_e2e")

	db, err := gorm.Open(postgres.Open(fmt.Sprintf(
		"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, pass, dbname)), &gorm.Config{Logger: gormlogger.Discard})
	if err != nil {
		fmt.Fprintf(os.Stderr, "[dbschema] cleanup: open postgres: %v\n", err)
		return
	}
	defer func() {
		if d, err := db.DB(); err == nil {
			_ = d.Close()
		}
	}()
	if err := db.Exec("DROP SCHEMA IF EXISTS " + dbschemaSchema + " CASCADE").Error; err != nil {
		fmt.Fprintf(os.Stderr, "[dbschema] cleanup: drop %s: %v（回收器会在 2h 后接管）\n",
			dbschemaSchema, err)
	}
}

// ============ 为什么本包用自己的隔离 schema，而不是 public（2026-11-08）============
//
// 本包的用例需要「**DDL 跨用例持久化**」：它测的是「旧库里已经有重复 active 行时，
// EnsureSourceGroupSchema 必须 fail-closed」，而那要求表结构在用例之间保留
// （每条用例前由 resetState 清数据、清索引，但表本身要在）。
//
// 原实现靠「不设 search_path ⇒ 表默认落到 public」来满足这个需求。**那是把
// 「需求」和「默认落点」混成了一件事**：public 只是不设 search_path 时的默认值，
// 它是巧合，不是设计。而它的代价是 public 永远非空 ——
// 于是「public 非空 = 有东西越界了」这条本该可信的信号**永久失效**。
//
// 正确做法：给本包一个**自己的固定 schema**，让 public 恒空。
// 固定名（而不是每用例新 schema）正是为了满足「DDL 跨用例持久化」。
//
// ── 为什么带 pid 而不是纯固定名 ──
//
// 纯固定名（dbschema_test）在**两个进程同时跑本包**时会互相拆台：
// 一个在 AutoMigrate，另一个在 resetState 里 DROP INDEX。
// 「本包只有一个、用例串行」是**今天的用法**，不是不变量；
// 而 testutil 的回收器这一整个改造的核心就是「不要依赖『不会有人并发』」——
// 在这里引入一个新的同类假设是最坏的示范。
//
// 所以名字是 `dbschema_<pid>`：
//
//	· 同进程所有用例共用（包级 sync.Once 算一次）=> 满足 DDL 跨用例持久化
//	· pid 区分并发进程                        => 不会互相拆台
//	· 格式与 testutil 的 test_<ns>_<pid>_<nano> 同族
//	  => testutil 的回收器**认得它**（schemaPrefixes 里有 dbschema_），
//	     所以它同样受「活主判据 + 年龄阈值 + 两段式」约束，不会变成规则裂缝
//
// ── 活主标记 ──
//
// DSN 带 application_name=<schemaName>，与 testutil 用同一套机制表达「这个 schema
// 有活主」。pg 连接断开即归零 —— 进程被 SIGKILL 后回收器能安全接管。
var (
	dbschemaOnce   sync.Once
	dbschemaSchema string
)

// dbschemaSchemaName 返回本进程的隔离 schema 名（dbschema_<ns>_<pid>_<nano>）。
//
// 用 sync.Once 而不是每用例算一次：名字必须在整个进程内稳定，
// 否则每条用例都会建一个新 schema，「DDL 跨用例持久化」就不成立了。
//
// ⚠ 名字**必须由 testutil.NewIsolatedSchemaName 生成**，不能自己拼：
// 回收器只认登记过的前缀 + 可解析的格式。自己拼 `dbschema_<pid>`（两段）
// 看起来对，但 parseSchemaName 解析失败 ⇒ 回收器永远看不见它 ⇒ 遗留 schema 泄漏，
// 而回收器本身看起来工作正常。这个约束由 NewIsolatedSchemaName 的 panic 强制。
func dbschemaSchemaName() string {
	dbschemaOnce.Do(func() {
		dbschemaSchema = testutil.NewIsolatedSchemaName("dbschema_")
	})
	return dbschemaSchema
}

// openDB 打开测试库：EHOME_TEST_DB=postgres 走 PG（隔离 schema），否则内存 SQLite。
func openDB(t *testing.T) *gorm.DB {
	t.Helper()
	if os.Getenv("EHOME_TEST_DB") == "postgres" {
		dbname := envOr("EHOME_DB_NAME", "ehome_e2e")
		// 2026-10-22 事故：带着 EHOME_DB_NAME=ehome 跑，把 dup0/dup1 夹具写进了生产库。
		// 生产库名守卫与隔离 schema 是**两道独立的防线**，都要留：
		//   · 隔离 schema 挡住「误写进 public」；
		//   · 生产名守卫挡住「连错库」—— 隔离 schema 在生产库上照样会建出来。
		testutil.GuardProductionDB(t, dbname)

		schema := dbschemaSchemaName()
		host := envOr("EHOME_DB_HOST", "127.0.0.1")
		port := envOr("EHOME_DB_PORT", "5432")
		user := envOr("EHOME_DB_USER", "ehome")
		pass := envOr("EHOME_DB_PASSWORD", "ehome123")

		// ① 管理连接：建 schema（若不存在），然后立刻关掉。
		//
		// 建之前先看 schema 是否存在：同一进程的多条用例会重复走到这里，
		// IF NOT EXISTS 让它幂等，也避免两条用例并发时撞车（同一进程内用例是串行的，
		// 但这条 SQL 本身幂等，读起来也更清楚）。
		admin, err := gorm.Open(postgres.Open(fmt.Sprintf(
			"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable application_name=%s",
			host, port, user, pass, dbname, schema)), &gorm.Config{Logger: gormlogger.Discard})
		if err != nil {
			t.Fatalf("open postgres (admin): %v", err)
		}
		if err := admin.Exec("CREATE SCHEMA IF NOT EXISTS " + schema).Error; err != nil {
			t.Fatalf("create schema %s: %v", schema, err)
		}
		if d, err := admin.DB(); err == nil {
			_ = d.Close()
		}

		// ② 正式连接：search_path **只放隔离 schema**（与 testutil 同一决定）。
		//
		// 不放 public 的理由见 testutil/db.go 的同名注释：那半个 public 会让
		// 「隔离 schema 缺表」静默命中 public 的同名表，症状变成 42P07/42P16
		// 这类完全指错方向的错误。
		dsn := fmt.Sprintf(
			"host=%s port=%s user=%s password=%s dbname=%s sslmode=disable application_name=%s search_path=%s",
			host, port, user, pass, dbname, schema, schema)
		db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: gormlogger.Discard})
		if err != nil {
			t.Fatalf("open postgres: %v", err)
		}

		// ③ 守卫：隔离必须真的生效（与 testutil 的同名守卫同一理由）。
		//
		// 不共享 testutil 的实现是为了让本包不依赖 testutil 的内部函数 ——
		// 但**判据必须一致**（current_schema() == schema），所以这里重复三行代码，
		// 而不是「差不多就行」。
		var got string
		if err := db.Raw("SELECT current_schema()").Scan(&got).Error; err != nil {
			t.Fatalf("读 current_schema(): %v", err)
		}
		if got != schema {
			t.Fatalf("PG 测试隔离失效：current_schema() = %q，期望 %q。"+
				"这会让本用例的建表/删表打到 public —— 而 public 非空会让"+
				"「public 非空 = 有东西越界」这条信号永久失效。", got, schema)
		}

		// ── 进程退出时清掉本包的 schema ──
		//
		// 为什么不能靠 t.Cleanup 逐用例删：schema 是**整个进程复用**的
		// （DDL 要跨用例持久化），所以只能在进程结束时删一次。
		// 而这里注册的是"该用例结束时删" —— 会误删后续用例正在用的 schema。
		//
		// 所以用**只删一次**的开关：第一条用例注册的 cleanup 里，
		// 最后一次执行（进程退出）时删。Go 的 testing 没有"进程退出"钩子，
		// 但 TestMain 有 —— 见 source_group_test.go 的 TestMain。
		//
		// 这里的 cleanup 只负责关闭连接池，删 schema 交给 TestMain。
		t.Cleanup(func() {
			if d, err := db.DB(); err == nil {
				_ = d.Close()
			}
		})
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
	// 旧库残留列清理：本包（openDB）连的是**持久化** public schema，不像
	// testutil.OpenTestDB 那样每用例一个新 schema。逻辑设备层退役前建的
	// data_sources.device_id 是 NOT NULL，GORM AutoMigrate 不会删列，于是一次
	// 遗留就让之后所有不写 device_id 的夹具在 PG 上报 23502（实测）。
	// 这里做的正是生产迁移 RetireLogicalDeviceLayer 的同一步，只是提前到夹具准备阶段。
	if db.Dialector != nil && db.Dialector.Name() == "postgres" &&
		db.Migrator().HasTable("data_sources") && db.Migrator().HasColumn("data_sources", "device_id") {
		_ = db.Exec("ALTER TABLE data_sources DROP COLUMN IF EXISTS device_id").Error
	}
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

	// 组键 = (edge_device_id, category)（逻辑设备层移除后 data_sources 无 device_id 列）。
	base := models.DataSource{EdgeDeviceID: 1, Category: "illuminance", SourceType: "edge_device", Status: servicesStatusActive, Name: "a"}
	if err := db.Create(&base).Error; err != nil {
		t.Fatalf("create ds1: %v", err)
	}
	second := models.DataSource{EdgeDeviceID: 1, Category: "illuminance", SourceType: "edge_device", Status: servicesStatusActive, Name: "b"}
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
	// 还要 DROP 模型级唯一索引 uniq_ds_source：本用例模拟的是**旧库**形态。
	// 组键改为 (edge_device_id, category) 之后，这两列上的普通唯一索引会让
	// 「同组键两行」根本插不进去，用例就永远到不了 fail-closed 分支 ——
	// 那是用"插不进去"冒充"迁移查得出来"。旧库确实能存在这种双行（当时组键
	// 里还含已删除的 device_id），所以这里必须先摘掉该索引再种夹具。
	// (下一次 migrateSourceGroup 的 AutoMigrate 会把它按 tag 重建。)
	if err := db.Exec("DROP INDEX IF EXISTS uniq_ds_source").Error; err != nil {
		t.Fatalf("drop uniq_ds_source (simulate legacy db): %v", err)
	}
	// 纵深防御：即使有人真在生产形状副本上跑（EHOME_ALLOW_PROD_DB_TESTS=1），
	// 也必须把自己种的 dup 行清掉 —— 上次事故就是「种子留下、用例还通过」。
	t.Cleanup(func() {
		if err := db.Unscoped().Where("edge_device_id = ? AND category = ?", 42, "rsoc").
			Delete(&models.DataSource{}).Error; err != nil {
			t.Logf("WARN: 清理 dup 夹具失败（请手工删除 edge_device_id=42/category=rsoc）: %v", err)
		}
	})
	for i := 0; i < 2; i++ {
		// 组键 = (edge_device_id, category)：两行必须同键才构成"双 active"。
		ds := models.DataSource{EdgeDeviceID: 42, Category: "rsoc",
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
