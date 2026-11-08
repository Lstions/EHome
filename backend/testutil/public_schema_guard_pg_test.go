package testutil

// public_schema_guard_pg_test.go — 「public 必须为空」守卫的**可执行验证**（2026-11-08）。
//
// ============ 为什么守卫需要自己的一条用例 ============
//
// 守卫的形式是「OpenTestDB 里断言 public 没有业务表」。默认情况下它永远不触发
// （因为 public 确实是空的）—— 而**一个永远不会触发的守卫与没有守卫等价**。
//
// 所以这条用例反过来做：自己在 public 里建一张表，然后断言守卫会报告失败。
//
// ============ 怎么断言一次 Fatal ============
//
// Go 没有「期望失败」的原生支持，而 t.Run 的子测试失败会**连带父测试失败**
// （实测确认过）。所以 assertPublicSchemaEmpty 收的是窄接口 schemaGuardTB，
// 这里传一个只记录消息的 fake —— 「守卫会不会触发」于是变成**可断言的事实**。

import (
	"fmt"
	"strings"
	"sync"
	"testing"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// recordingTB 记录 Fatalf/Logf 的调用，不真的让测试失败。
type recordingTB struct {
	mu       sync.Mutex
	fatals   []string
	logfMsgs []string
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.fatals = append(r.fatals, fmt.Sprintf(format, args...))
}

func (r *recordingTB) Logf(format string, args ...any) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logfMsgs = append(r.logfMsgs, fmt.Sprintf(format, args...))
}

func (r *recordingTB) fatalCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.fatals)
}

func (r *recordingTB) lastFatal() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.fatals) == 0 {
		return ""
	}
	return r.fatals[len(r.fatals)-1]
}

// TestPublicSchemaGuard_CatchesStrayTable 证明守卫能发现 public 里的业务表，
// 且清掉之后会恢复安静（**双向**：只会红不会绿的守卫同样不可用）。
func TestPublicSchemaGuard_CatchesStrayTable(t *testing.T) {
	if !IsPostgres() {
		t.Skip("需要 PostgreSQL（EHOME_TEST_DB=postgres）")
	}

	host := envOr("EHOME_DB_HOST", "localhost")
	port := envOr("EHOME_DB_PORT", "5432")
	user := envOr("EHOME_DB_USER", "ehome")
	pass := envOr("EHOME_DB_PASSWORD", "ehome123")
	dbname := envOr("EHOME_DB_NAME", "ehome_test")
	GuardProductionDB(t, dbname)

	db, err := gorm.Open(postgres.Open(dsnFor(host, port, user, pass, dbname)), &gorm.Config{
		Logger: gormlogger.Default.LogMode(gormlogger.Silent),
	})
	if err != nil {
		t.Fatalf("open postgres: %v", err)
	}
	defer func() {
		// 无论成败都要清掉探针表：它建在 public，留着会让**所有**走 OpenTestDB
		// 的测试红（守卫会挡住它们）—— 那正是守卫的作用，不是我们想留下的状态。
		db.Exec("DROP TABLE IF EXISTS public." + strayProbeTable)
		if d, _ := db.DB(); d != nil {
			_ = d.Close()
		}
	}()

	// ── 前置：此刻 public 必须是干净的 ──
	//
	// 否则本用例证明不了任何事：它只证明「public 已经脏时守卫会红」，
	// 那不是我们要验的（那种情况连「新出现的越界表」都区分不出来）。
	var pre int
	if err := db.Raw(
		"SELECT count(*) FROM pg_class c JOIN pg_namespace n ON n.oid = c.relnamespace " +
			"WHERE n.nspname = 'public' AND c.relkind IN ('r','p')").Scan(&pre).Error; err != nil {
		t.Fatalf("前置查询失败: %v", err)
	}
	if pre != 0 {
		t.Fatalf("前置失败：public 里已经有 %d 张业务表。本用例需要干净的 public 才能验证"+
			"「守卫能发现**新出现的**越界表」。先查明它们是谁建的（处置办法见守卫的报错正文）", pre)
	}

	// ── ① 干净时：守卫必须安静 ──
	quiet := &recordingTB{}
	assertPublicSchemaEmpty(quiet, db)
	if n := quiet.fatalCount(); n != 0 {
		t.Fatalf("public 干净时守卫报告了 %d 次失败（应当安静）：%s\n\t"+
			"误报的守卫会让所有测试无端变红，然后被迫加例外 —— 而例外是它唯一的失效方式",
			n, quiet.lastFatal())
	}

	// ── ② 建一张越界表：守卫必须报告，且消息里要带表名 ──
	if err := db.Exec("CREATE TABLE public." + strayProbeTable + " (id int)").Error; err != nil {
		t.Fatalf("create stray table: %v", err)
	}
	loud := &recordingTB{}
	assertPublicSchemaEmpty(loud, db)
	if loud.fatalCount() == 0 {
		t.Fatalf("public 里建了 %q 之后守卫**没有**报告 —— "+
			"这条守卫是「public 非空 = 有东西越界」这条信号的全部依托，"+
			"它不工作时，下次 public 被塞满 67 张表仍然没有人能发现", strayProbeTable)
	}
	if msg := loud.lastFatal(); !strings.Contains(msg, strayProbeTable) {
		t.Fatalf("守卫报告了但没有点出表名 %q —— 运维拿不到可操作信息。实际消息：%s",
			strayProbeTable, msg)
	}

	// ── ③ 清掉之后：守卫必须恢复安静（双向） ──
	if err := db.Exec("DROP TABLE public." + strayProbeTable).Error; err != nil {
		t.Fatalf("drop stray table: %v", err)
	}
	quietAgain := &recordingTB{}
	assertPublicSchemaEmpty(quietAgain, db)
	if n := quietAgain.fatalCount(); n != 0 {
		t.Fatalf("清掉 %q 后守卫仍报告（%d 次）：%s\n\t"+
			"只会红不会绿的守卫会让所有测试永久红，然后被迫加例外",
			strayProbeTable, n, quietAgain.lastFatal())
	}
}

// strayProbeTable 是这条用例专用的探针表名。
//
// 带 zz_ 前缀 + 明确用途：万一清理失败，残留表名本身就能告诉下一个人它是谁的。
const strayProbeTable = "zz_probe_public_guard"
