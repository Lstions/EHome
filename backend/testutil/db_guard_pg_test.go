package testutil

// db_guard_pg_test.go — 守卫 A（隔离失效检测）的**可执行验证**（2026-11-08）。
//
// ============ 为什么需要这条用例 ============
//
// 守卫 A 的形式是「建表前断言 current_schema() == 隔离 schema」。
// 只证明「正常情况下守卫不误报」是**半个判据** —— 一个恒不触发的守卫
// 与没有守卫完全等价。
//
// 所以这条用例反过来做：**故意构造一个 search_path 没生效的连接**，
// 断言守卫能看到它。这样才证明守卫真的在工作。
//
// ============ 怎么造「坏 DSN」============
//
// 直接用 postgres.Open 打开一个带 search_path 的 DSN 不叫坏 ——
// 那种连接是好的。真正要模拟的是「DSN 里没有 search_path 的那条连接」，
// 也就是 openPostgres 里 base 那条（它连上后 current_schema() 就是 public）。
//
// 本用例就造这种连接，然后断言：
//  1. 它的 current_schema() == "public"（即守卫会看到并 fail）
//  2. 加入 search_path 后 current_schema() == 隔离 schema（即守卫不会误报）
//
// 这两条一起构成「守卫能区分两种状态」的证明。

import (
	"fmt"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// TestGuardA_DetectsBrokenSearchPath 证明守卫 A 能识别「隔离失效」的连接。
func TestGuardA_DetectsBrokenSearchPath(t *testing.T) {
	if !IsPostgres() {
		t.Skip("需要 PostgreSQL（EHOME_TEST_DB=postgres）")
	}

	host := envOr("EHOME_DB_HOST", "localhost")
	port := envOr("EHOME_DB_PORT", "5432")
	user := envOr("EHOME_DB_USER", "ehome")
	pass := envOr("EHOME_DB_PASSWORD", "ehome123")
	dbname := envOr("EHOME_DB_NAME", "ehome_test")
	GuardProductionDB(t, dbname)

	// ── ① 坏连接：DSN 里**没有** search_path ──
	// 这正是 openPostgres 里 base 那条连接的形态：它连上后 current_schema() 是 public。
	badDSN := fmt.Sprintf("host=%s port=%s user=%s password=%s dbname=%s sslmode=disable",
		host, port, user, pass, dbname)
	bad, err := gorm.Open(postgres.Open(badDSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open bad dsn: %v", err)
	}
	defer func() {
		if d, _ := bad.DB(); d != nil {
			_ = d.Close()
		}
	}()

	gotBad := currentSchemaOf(t, bad)
	if gotBad != "public" {
		// 不是错误，而是**前提变了**：某种 PG 配置下默认 schema 可能不是 public。
		// 那就让这条用例明说它验不了，而不是假装通过。
		t.Fatalf("无 search_path 的 DSN 期望 current_schema()=public，实际 %q；"+
			"本用例的前提（默认 schema 是 public）不成立，需要重新设计", gotBad)
	}

	// ── ② 好连接：DSN 带 search_path ──
	schema := buildSchemaName(currentNamespace(), 999999, time.Now())
	if err := bad.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer bad.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE")

	goodDSN := badDSN + " search_path=" + schema + ",public"
	good, err := gorm.Open(postgres.Open(goodDSN), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open good dsn: %v", err)
	}
	defer func() {
		if d, _ := good.DB(); d != nil {
			_ = d.Close()
		}
	}()

	gotGood := currentSchemaOf(t, good)
	if gotGood != schema {
		t.Errorf("带 search_path 的连接 current_schema() = %q，期望 %q", gotGood, schema)
	}

	// ── ③ 核心断言：守卫能区分两者 ──
	if gotBad == gotGood {
		t.Fatal("守卫无法区分坏连接与好连接 —— 它永远不会触发，等于没有守卫")
	}
}
