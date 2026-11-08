package testutil

// live_connections_pg_test.go — 活主判据的**可执行验证**（2026-11-08）。
//
// ============ 守什么 ============
//
// 回收器的安全规则里，「有活主 ⇒ 不回收」是唯一一条**能防止误删正在使用的
// schema** 的规则。它的实现是：
//
//	SELECT count(*) FROM pg_stat_activity WHERE application_name = ?
//
// 而纯函数测试（reap_test.go 的 TestShouldReapSchema）只用**注入的整数**
// 验证「liveConns > 0 时不回收」—— 它证明不了这个查询本身是对的。
//
// 变异实测（2026-11-08）：把查询改成 `WHERE false`（恒返回 0）之后，
// **全部纯函数用例仍然全绿**。也就是说「活主判据的实现坏了」这件事，
// 在纯函数层面完全不可见 —— 而这正是最危险的失效方向：
// 恒 0 ⇒ 回收器认为任何 schema 都无活主 ⇒ **删掉别人正在用的 schema**。
//
// 所以必须有这条打真 PG 的用例：它断言「有连接时计数 > 0，关掉之后归 0」。
//
// 变异验证：把查询改成 `WHERE false` ⇒ 本例红（第一个断言就失败）。

import (
	"fmt"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// TestLiveConnectionsFor_CountsTaggedConnections 证明活主判据真的能数到连接。
func TestLiveConnectionsFor_CountsTaggedConnections(t *testing.T) {
	if !IsPostgres() {
		t.Skip("需要 PostgreSQL（EHOME_TEST_DB=postgres）")
	}

	host := envOr("EHOME_DB_HOST", "localhost")
	port := envOr("EHOME_DB_PORT", "5432")
	user := envOr("EHOME_DB_USER", "ehome")
	pass := envOr("EHOME_DB_PASSWORD", "ehome123")
	dbname := envOr("EHOME_DB_NAME", "ehome_test")
	GuardProductionDB(t, dbname)

	// 观察者连接：不带 application_name（它就是「回收器」扮演的角色）。
	observer, err := gorm.Open(postgres.Open(dsnFor(host, port, user, pass, dbname)), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open observer: %v", err)
	}
	defer func() {
		if d, _ := observer.DB(); d != nil {
			_ = d.Close()
		}
	}()

	// 一个**独一无二**的标记，避免与库里其他测试的 application_name 撞上。
	tag := fmt.Sprintf("zz_probe_live_%d", nowForGuardTest().UnixNano())

	// ── ① 没有连接时计数必须是 0 ──
	if n := liveConnectionsFor(observer, tag); n != 0 {
		t.Fatalf("未建立带标记的连接时 liveConnectionsFor = %d，期望 0", n)
	}

	// ── ② 建一条带该标记的连接，计数必须 >= 1 ──
	tagged, err := gorm.Open(
		postgres.Open(dsnFor(host, port, user, pass, dbname)+" application_name="+tag),
		&gorm.Config{Logger: gormlogger.Default.LogMode(gormlogger.Silent)},
	)
	if err != nil {
		t.Fatalf("open tagged: %v", err)
	}
	// 强制真正建立连接（gorm 是惰性的，不查一次可能还没连）。
	var one int
	if err := tagged.Raw("SELECT 1").Scan(&one).Error; err != nil {
		t.Fatalf("ping tagged: %v", err)
	}

	n := liveConnectionsFor(observer, tag)
	if n < 1 {
		t.Fatalf("有带标记 %q 的活连接时 liveConnectionsFor = %d，期望 >= 1。"+
			"这正说明活主判据失效（恒 0）—— 后果是回收器会删掉别人正在用的 schema", tag, n)
	}

	// ── ③ 关掉它之后计数必须归 0 ──
	//
	// 这一步是「判据能双向返回」的证明：只会升不会降的计数器同样不可用
	// （它会让已死的 schema 永远不被回收）。
	if d, _ := tagged.DB(); d != nil {
		_ = d.Close()
	}
	// 连接关闭在 PG 端是**异步**的（后端进程要退出、pg_stat_activity 要刷新）。
	// 所以这里必须有界重试，而不是 sleep 一次就断言 —— 后者会是 flaky 的。
	var last int
	for i := 0; i < 50; i++ {
		last = liveConnectionsFor(observer, tag)
		if last == 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if last != 0 {
		t.Fatalf("关闭带标记的连接后 liveConnectionsFor 仍为 %d，期望 0 —— "+
			"只会升不会降的判据会让已死的 schema 永远不被回收", last)
	}
}
