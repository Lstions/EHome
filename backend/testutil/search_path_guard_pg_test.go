package testutil

// search_path_guard_pg_test.go — 「search_path 不含 public」的**可执行验证**（2026-11-08）。
//
// ============ 守什么 ============
//
// 原 DSN 是 `search_path=<schema>,public`。那半个 `public` 让「隔离 schema 缺某张表」
// 变成**静默命中 public 里的同名表**，症状是 42P07「表已存在」/ 42P16「列在主键里」
// 这类完全指错方向的错误 —— 2026-11-08 全量 PG 套件红成一片，根因就是这个。
//
// 去掉 public 之后，缺表就是 `ERROR: relation "x" does not exist`。
//
// ============ 为什么必须有一条**可执行**验证 ============
//
// 「DSN 字符串里没有 public」这件事，用源码扫描就能断言（TestSearchPathExcludesPublic 做了）。
// 但那只证明「字面量改了」，不证明「行为变了」—— 而真正的契约是后者：
//
//	隔离 schema 里没有的表，查询必须报错，而不是命中 public 里的同名表。
//
// 所以这条用例真的建一个 `zz_probe_searchpath` 在 public，然后在一个
// **只是没建那张表**的隔离 schema 连接里查它，断言报错。
//
// 变异验证：把 DSN 改回 `search_path=<schema>,public` ⇒ 本例红（查询会静默成功）。

import (
	"strings"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// probeTableName 是这条用例专用的探针表名。
//
// 带 zz_ 前缀 + 明确用途：万一清理失败，残留表名本身就能告诉下一个人它是谁的。
const probeTableName = "zz_probe_searchpath"

// ============ 为什么探针必须建在**独立数据库**里（2026-11-08 修）============
//
// 这条用例必须真的在 public 里建一张表 —— 那是被测对象，不能省。
// 但它**不能建在 ehome_test 的 public 里**，原因是一个真实的并发冲突：
//
//	等价于 assertPublicSchemaEmpty 的守卫在每次 OpenTestDB 时都会跑，
//	断言「ehome_test 的 public 必须恒空」。而 go test ./... 是**多包并行**的：
//	本包在这里建表的那几毫秒，internal/database 包可能正好调 OpenTestDB，
//	于是它看到 public 非空 => Fatal「有东西绕过了隔离」。
//
// 实测（2026-11-08，clean tree 3f307203，go test ./...）：
//
//	--- FAIL: TestAutoMigrateDoesNotCreateOperationLogs
//	    retire_operation_logs_test.go:102: public schema 里有 1 张业务表：[zz_probe_searchpath]
//
// 而单包跑永远不红（同包内测试串行），所以这个冲突只在全量时暴露。
//
// 修法不是给守卫加白名单（白名单会让「探针表名被真业务表占用」漏掉，
// 而白名单正是这条守卫的原始失效方式），也不是加乐观重试（治标）——
// 而是**把探针搬出 ehome_test**：另开一个库，它的 public 只有本用例会碰。
// 这样「ehome_test 的 public 恒空」这条信号保持完全可信。
//
// 命名取 ehome_test 的后缀形式（而不是完全无关的名字）：
// 它仍然过 GuardProductionDB 的形状判断，且名字本身说明它从哪来。
const probeDBName = "ehome_test_publicprobe"

// TestSearchPathDoesNotFallThroughToPublic 证明隔离 schema 缺表时**不会**静默命中 public。
func TestSearchPathDoesNotFallThroughToPublic(t *testing.T) {
	if !IsPostgres() {
		t.Skip("需要 PostgreSQL（EHOME_TEST_DB=postgres）")
	}

	host := envOr("EHOME_DB_HOST", "localhost")
	port := envOr("EHOME_DB_PORT", "5432")
	user := envOr("EHOME_DB_USER", "ehome")
	pass := envOr("EHOME_DB_PASSWORD", "ehome123")
	dbname := envOr("EHOME_DB_NAME", "ehome_test")
	GuardProductionDB(t, dbname)

	// 探针用**独立数据库**，不碰 ehome_test 的 public（理由见 probeDBName 的注释）。
	// 从测试库名派生，保持「这是测试库」的语义；显式建 + 结束时 DROP。
	probeDSNBase := "host=" + host + " port=" + port + " user=" + user +
		" password=" + pass + " dbname=" + dbname + " sslmode=disable"
	admin, err := gorm.Open(postgres.Open(probeDSNBase), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open admin: %v", err)
	}
	// CREATE DATABASE 不能跑在事务里，也不能带 IF NOT EXISTS（PG 无此语法）：
	// 先查再建，并容忍「另一个进程刚建好」的 42P04（duplicate_database）。
	var exists int
	if err := admin.Raw("SELECT count(*) FROM pg_database WHERE datname = ?", probeDBName).
		Scan(&exists).Error; err != nil {
		t.Fatalf("probe 库存在性查询失败: %v", err)
	}
	if exists == 0 {
		if err := admin.Exec("CREATE DATABASE " + probeDBName).Error; err != nil {
			// 42P04 = duplicate_database：并发下另一个进程刚建好，可接受。
			if !strings.Contains(err.Error(), "already exists") &&
				!strings.Contains(err.Error(), "42P04") {
				t.Fatalf("create probe db: %v", err)
			}
		}
	}
	if d, _ := admin.DB(); d != nil {
		_ = d.Close()
	}

	probeDSN := "host=" + host + " port=" + port + " user=" + user +
		" password=" + pass + " dbname=" + probeDBName + " sslmode=disable"
	base, err := gorm.Open(postgres.Open(probeDSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open base: %v", err)
	}
	defer func() {
		// 无论成败都把探针表清掉：它是 public 里的东西，留着会影响别人。
		base.Exec("DROP TABLE IF EXISTS public." + probeTableName)
		if d, _ := base.DB(); d != nil {
			_ = d.Close()
		}
	}()

	// ① 在 public 里建探针表（**故意不建在隔离 schema**）。
	if err := base.Exec("CREATE TABLE IF NOT EXISTS public." + probeTableName + " (id int)").Error; err != nil {
		t.Fatalf("create probe table in public: %v", err)
	}
	// ② 造一个干净的隔离 schema。
	schema := buildSchemaName(currentNamespace(), 888888, nowForGuardTest())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create isolated schema: %v", err)
	}
	defer base.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")

	// ③ 用**和生产同一形态**的 DSN（不带 public）连过去。
	//    ⚠ 目标库必须是 probeDBName —— 探针表建在那里，连错库会得到
	//    「relation does not exist」这个**正确但无意义**的结果（其实什么都没验）。
	isolatedDSN := dsnFor(host, port, user, pass, probeDBName) +
		" application_name=" + schema + " search_path=" + schema
	iso, err := gorm.Open(postgres.Open(isolatedDSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open isolated: %v", err)
	}
	defer func() {
		if d, _ := iso.DB(); d != nil {
			_ = d.Close()
		}
	}()

	// 前置：确认这个连接确实落在隔离 schema（不然下面的断言在验别的东西）。
	if got := currentSchemaOf(t, iso); got != schema {
		t.Fatalf("前置失败：current_schema() = %q, want %q", got, schema)
	}

	// ④ 核心断言：查 public 里才有的表，**必须报错**。
	//
	// 若 search_path 里带 public，这里会静默成功（读到 public 的 zz_probe_searchpath）
	// —— 那正是 2026-11-08 那批「42P07/42P16 看似无关错误」的成因。
	var n int
	err = iso.Raw("SELECT count(*) FROM " + probeTableName).Scan(&n).Error
	if err == nil {
		t.Fatalf("隔离 schema 里没有 %q，但查询成功了（读到 %d 行）—— "+
			"说明 search_path 里还带着 public，隔离 schema 缺表时会**静默命中** public 的同名表。"+
			"这正是 2026-11-08 那批 42P07/42P16 的成因：症状完全指错方向。", probeTableName, n)
	}
	t.Logf("符合预期：隔离开关下查 public 专有表报错: %v", err)
}

// nowForGuardTest 返回一个固定的「很旧」的时刻。
//
// 用固定值而不是 time.Now()：这条用例的 schema 只要够旧就不会被回收器盯上，
// 而固定值让「行为可复现」—— 同一个用例跑两次得到完全一样的 schema 名。
func nowForGuardTest() time.Time {
	return time.Unix(0, 1789489580224261933)
}

// dsnFor 拼一个基础 DSN（无 search_path、无 application_name）。
//
// 与 openPostgres 里的 baseDSN 同形状，抽出来供本文件与守卫 A 的用例共用 ——
// 两处各拼一份会在「忘了加 sslmode=disable」这类细节上漂移。
func dsnFor(host, port, user, pass, dbname string) string {
	return "host=" + host + " port=" + port + " user=" + user +
		" password=" + pass + " dbname=" + dbname + " sslmode=disable"
}
