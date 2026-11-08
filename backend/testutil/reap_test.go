package testutil

// 陈旧 schema 回收器 + 隔离守卫的单元测试（回归锁）。
//
// ============ 本文件守两类事故 ============
//
// 【事故一：schema 残留】
// openPostgres 的清理靠 t.Cleanup，而它**只在进程正常退出时执行**。
// go test 被 SIGKILL / SIGPIPE 打断（`go test … | head` 即 SIGPIPE）时
// schema 会永久残留。实测复现：跑到约 800ms 时 kill -9，留下
// test_3861920_1789489580224261933。
//
// 【事故二：回收器**互相拆台**（2026-11-08 实证）】
// 原判定用 syscall.Kill(pid, 0)，但测试跑在 docker 容器里且 gow.sh 不带
// --pid host ⇒ pid 命名空间是**局部**的：
//
//	容器 A 拿到容器 B 创建的 test_<pidB>_<nano>
//	在 A 的命名空间里查 pidB ⇒ 结果与 B 无关 ⇒ 常判「已死」
//	⇒ DROP SCHEMA ... CASCADE 掉 B **正在用**的 schema
//	⇒ B 的 current_schema() 落回 public ⇒ B 的 CREATE/DROP TABLE 打到 public
//
// PG 日志实证（2026-11-08）：
//
//	ERROR:  "telemetry_rollup_1m_pkey" is not a table
//	STATEMENT: DROP TABLE IF EXISTS telemetry_rollup_1m_pkey CASCADE
//
// 而 ehome_test.public 的 telemetry_rollup_1m 就是那一刻消失的。
//
// ============ 本文件只测纯函数 ============
//
// 不连数据库：这样在 SQLite 模式（默认、无 PG）下也必须全绿，不给默认门禁
// 引入外部依赖。真实回收行为由「跑一次真 PG 测试」的实跑验证。

import (
	"os"
	"strings"
	"testing"
	"time"
)

// TestParseSchemaName 钉住解析规则：只有 `test_<namespace>_<pid>_<nano>` 才认。
//
// 关键边界：**旧格式（无 namespace 段）必须解析失败**。那些 schema 是谁创建的
// 已无从判断，按新规则一律「格式不合法」⇒ 不回收（宁可漏收，不可误删）。
func TestParseSchemaName(t *testing.T) {
	ok := []struct {
		name      string
		namespace string
		pid       int
		nano      int64
	}{
		{"test_abc123_1234_1789489580224261933", "abc123", 1234, 1789489580224261933},
		{"test_host_1_1", "host", 1, 1},
		{"test_my_host_name_999_42", "my_host_name", 999, 42}, // namespace 自身含下划线
	}
	for _, c := range ok {
		got, valid := parseSchemaName(c.name)
		if !valid {
			t.Errorf("parseSchemaName(%q) 应解析成功", c.name)
			continue
		}
		if got.Namespace != c.namespace || got.PID != c.pid || got.CreatedUnixNano != c.nano {
			t.Errorf("parseSchemaName(%q) = %+v，期望 ns=%q pid=%d nano=%d",
				c.name, got, c.namespace, c.pid, c.nano)
		}
	}

	bad := []string{
		"public",                             // 系统 schema
		"test_",                              // 空
		"test",                               // 无前缀下划线
		"test_1234_1789489580224261933",      // **旧格式**（无 namespace）⇒ 必须拒
		"test_abc_1234",                      // 缺 nano 段
		"test_abc_0_123",                     // pid 必须 > 0
		"test_abc_-1_123",                    // 负数 pid
		"test_abc_1234_0",                    // nano 必须 > 0
		"test__1234_1789489580224261933",     // namespace 为空
		"other_abc_1234_1789489580224261933", // 前缀不符
	}
	for _, name := range bad {
		if _, valid := parseSchemaName(name); valid {
			t.Errorf("parseSchemaName(%q) 应判为不合法（fail-closed：宁可漏收也不误删）", name)
		}
	}
}

// TestBuildSchemaNameRoundTrip 生成的 schema 名必须能被解析回来。
//
// 这是「生成器与解析器共用一套格式」的最直接验证 —— 两者漂移的后果是
// **回收器永远认不出本进程创建的 schema**（泄漏）或认错（误删）。
func TestBuildSchemaNameRoundTrip(t *testing.T) {
	now := time.Unix(0, 1789489580224261933)
	cases := []struct {
		ns  string
		pid int
	}{
		{"abc123", 1234},
		{"host", 1},
		{"UPPER_CASE_NS", 999}, // 会被 sanitize 成小写
	}
	for _, c := range cases {
		name := buildSchemaName(c.ns, c.pid, now)
		info, ok := parseSchemaName(name)
		if !ok {
			t.Errorf("buildSchemaName(%q,%d) = %q 无法被解析（生成器与解析器格式漂移）", c.ns, c.pid, name)
			continue
		}
		if info.PID != c.pid {
			t.Errorf("往返后 pid = %d, want %d", info.PID, c.pid)
		}
		if info.CreatedUnixNano != now.UnixNano() {
			t.Errorf("往返后 nano = %d, want %d", info.CreatedUnixNano, now.UnixNano())
		}
		if info.Namespace != sanitizeSchemaSegment(c.ns) {
			t.Errorf("往返后 namespace = %q, want %q", info.Namespace, sanitizeSchemaSegment(c.ns))
		}
	}
}

// TestBuildSchemaNameIsSafeIdentifier 生成的标识符必须无需引号即可使用。
//
// 本文件的 SQL 一律直接拼接（DROP SCHEMA %s CASCADE），一旦生成的名字里
// 出现大写或特殊字符，就会变成需要引号的标识符，那也意味着**注入面**。
// 它同时被当作 application_name 用（活主判据），所以也必须能安全嵌入 DSN。
func TestBuildSchemaNameIsSafeIdentifier(t *testing.T) {
	name := buildSchemaName("Weird-NS/with:chars", 42, time.Unix(0, 1))
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_') {
			t.Fatalf("schema 名 %q 含需要引号的字符 %q（下标 %d）—— 直接拼接就是注入面", name, c, i)
		}
	}
	if len(name) > 63 {
		t.Errorf("schema 名长度 %d 超过 PG 的 63 字节限制", len(name))
	}
}

// TestReapStaleSchemasWiredBeforeCreate 是**接线守卫**：回收必须在 CREATE SCHEMA 之前。
func TestReapStaleSchemasWiredBeforeCreate(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	body := string(src)
	// ⚠ 判据必须锚在**可执行语句**上，不能锚裸字符串："CREATE SCHEMA" 在
	// 本文件的注释里也出现（回收器文件头的说明），裸查会把注释当成代码，
	// 于是「接线是对的」也会被判违规（本文件第一版就是这么翻的）。
	const createStmt = `base.Exec(fmt.Sprintf("CREATE SCHEMA %s", schemaName))`
	iReap := indexOf(body, "reapStaleSchemas(base)")
	iCreate := indexOf(body, createStmt)
	if iReap < 0 {
		t.Fatalf("openPostgres 未调用 reapStaleSchemas —— 陈旧 schema 会永久残留（SIGKILL/SIGPIPE 场景）")
	}
	if iCreate < 0 {
		t.Fatalf("找不到 CREATE SCHEMA —— 判据失效")
	}
	if iReap > iCreate {
		t.Errorf("reapStaleSchemas 必须在 CREATE SCHEMA 之前调用（当前 reap@%d > create@%d）", iReap, iCreate)
	}
}

// TestShouldReapSchema 用**行为**钉住回收决策的全部安全规则。
//
// 为什么不用源码扫描：初版判据是 strings.Contains(body, "processAlive(pid)")，
// 而把安全规则改写成 `if processAlive(pid) && false {…}`（逻辑上已完全失效）后，
// 该字符串**依然存在** ⇒ 门禁全绿放行。抽取纯函数后，这类架空无法再逃过行为断言。
func TestShouldReapSchema(t *testing.T) {
	old := time.Unix(0, 1789489580224261933) // 很久以前
	now := old.Add(minReapAge + time.Minute) // 已超过年龄阈值
	fresh := now.Add(-minReapAge / 2)        // 太年轻

	base := schemaInfo{
		Name: "test_selfns_1234_1789489580224261933", Namespace: "selfns",
		PID: 1234, CreatedUnixNano: old.UnixNano(),
	}

	t.Run("无活主且其余条件满足则回收", func(t *testing.T) {
		if !shouldReapSchema(base, 0, now) {
			t.Error("无活主 + 同格式 + 够旧 ⇒ 必须回收（否则本功能形同虚设）")
		}
	})

	t.Run("有活主则绝不回收", func(t *testing.T) {
		if shouldReapSchema(base, 1, now) {
			t.Error("有活主连接时不得回收 —— 会误删别的测试进程正在用的 schema")
		}
	})

	t.Run("活主查询失败(-1)时不动", func(t *testing.T) {
		if shouldReapSchema(base, -1, now) {
			t.Error("liveConnectionsFor 返回 -1 表示「查不清」，必须当成「不动」" +
				"（fail-closed：查不清时宁可残留，不可误删）")
		}
	})

	// ============ 年龄阈值：兜底，不是活主判据 ============
	t.Run("太年轻的schema不回收", func(t *testing.T) {
		young := base
		young.CreatedUnixNano = fresh.UnixNano()
		if shouldReapSchema(young, 0, now) {
			t.Errorf("创建仅 %v 的 schema 即使无活主也不得回收（阈值 %v）——"+
				"它兜住时钟漂移与「建表到建连」的窗口", now.Sub(fresh), minReapAge)
		}
	})

	t.Run("刚好达到阈值即可回收", func(t *testing.T) {
		exact := base
		exact.CreatedUnixNano = now.Add(-minReapAge).UnixNano()
		if !shouldReapSchema(exact, 0, now) {
			t.Error("年龄刚好等于阈值时应可回收（边界必须包含，否则阈值语义变成 > 而不是 >=")
		}
	})

	// ============ 格式 ============
	t.Run("格式不符一律不回收", func(t *testing.T) {
		for _, bad := range []schemaInfo{
			{Name: "public"},
			{Name: "test_"},
			{Name: "test_abc_1", PID: 1},   // nano 为 0
			{Name: "test_abc_0_1", PID: 0}, // pid 为 0
			{Name: "test_selfns_1234_0", Namespace: "selfns", PID: 1234}, // nano 为 0
		} {
			if shouldReapSchema(bad, 0, now) {
				t.Errorf("格式不符的 %+v 不得回收（fail-closed：宁可漏收也不误删）", bad)
			}
		}
	})

	t.Run("活主判定必须真的影响决策", func(t *testing.T) {
		if shouldReapSchema(base, 0, now) == shouldReapSchema(base, 1, now) {
			t.Error("活主判定的结果未影响决策 —— 安全规则已被架空")
		}
	})

	// ============ 三条规则必须**各自独立**生效 ============
	//
	// 上面每个 t.Run 单独验一条。这里再验「同时只坏一条，其余都好」时仍然拒绝 ——
	// 防止实现写成「或」而不是「与」。
	t.Run("三条规则必须是与关系", func(t *testing.T) {
		for _, c := range []struct {
			name string
			info schemaInfo
		}{
			{"只坏格式", schemaInfo{Name: "test_x_0_1", Namespace: "x", PID: 0, CreatedUnixNano: old.UnixNano()}},
			{"只坏年龄", schemaInfo{Name: "test_x_1_1", Namespace: "x", PID: 1, CreatedUnixNano: fresh.UnixNano()}},
		} {
			if shouldReapSchema(c.info, 0, now) {
				t.Errorf("%s（其余条件都满足）时仍不得回收 —— 三条规则必须是「与」关系", c.name)
			}
		}
	})
}

// TestNamespaceDoesNotAffectReaping 钉住「命名空间段**不参与判活**」这条设计决定。
//
// ============ 为什么这条必须被钉住 ============
//
// 中途试过的方案是「schema 名带 hostname，只回收自己命名空间的」。
// 它错在：hostname 在容器里是**容器 ID**，每次 docker run 都变 ⇒
// 上一轮容器创建的 schema 本轮永远认不出 ⇒ 回收器变成**永久 no-op**。
//
// 而「永久不回收」对一个会累积脏数据的回收器来说，比误删更隐蔽：
// 误删会让测试红（有症状），泄漏只让库慢慢变脏（没症状）。
//
// 所以这条用例反向断言：**别的 namespace 的 schema，只要无活主且够旧，就该回收。**
// 如果有人把 namespace 判据加回来，这条会立刻红。
func TestNamespaceDoesNotAffectReaping(t *testing.T) {
	old := time.Unix(0, 1789489580224261933)
	now := old.Add(minReapAge + time.Minute)

	otherNamespace := schemaInfo{
		Name: "test_someothercontainer_1234_1789489580224261933", Namespace: "someothercontainer",
		PID: 1234, CreatedUnixNano: old.UnixNano(),
	}
	if !shouldReapSchema(otherNamespace, 0, now) {
		t.Error("别的命名空间的 schema，只要**无活主且够旧**就必须回收 —— " +
			"若因 namespace 不同而拒绝，回收器在容器环境下就是永久 no-op" +
			"（hostname 每次 docker run 都变），而「永久不回收」比误删更隐蔽")
	}

	// 反过来：命名空间相同也不构成「可以回收」的理由 —— 有活主照样不回收。
	sameNamespace := schemaInfo{
		Name: "test_selfns_1234_1789489580224261933", Namespace: "selfns",
		PID: 1234, CreatedUnixNano: old.UnixNano(),
	}
	if shouldReapSchema(sameNamespace, 1, now) {
		t.Error("同命名空间但**有活主**时不得回收 —— 命名空间不是活主判据")
	}
}

// TestReaperRecognizesDbschemaPrefix 钉住「回收器认识 dbschema_ 前缀」。
//
// ============ 为什么这条是规则统一性的证明 ============
//
// internal/dbschema 的用例需要「DDL 跨用例持久化」，所以有自己的固定 schema
// `dbschema_<pid>`（而不是占用 public）。但**它也必须被回收器管** ——
// 否则就是「某类 schema 不受生命周期约束」，而那是规则裂缝，
// 下一个例外会顺着裂缝进来。
//
// 正确做法是**放宽合法命名**（对所有前缀一视同仁），而不是在 shouldReapSchema
// 里写 `if isDbschemaPkg { return false }`。
//
// 反向验证：把 schemaPrefixes 里的 "dbschema_" 删掉，本用例立刻红
// —— 那时回收器会永久看不见这类 schema（泄漏），而它自己看起来工作正常。
func TestReaperRecognizesDbschemaPrefix(t *testing.T) {
	old := time.Unix(0, 1789489580224261933)
	now := old.Add(minReapAge + time.Minute)

	// 名字格式与 test_<ns>_<pid>_<nano> **完全同族**，只是前缀不同 ——
	// 这正是「规则统一」的含义：同一个解析器、同一套安全规则，只是合法前缀多一个。
	//
	// 名字由 testutil.NewIsolatedSchemaName("dbschema_") 生成，形如
	// dbschema_<namespace>_<pid>_<nano>（四段）。自己拼 `dbschema_<pid>`（两段）
	// 会解析失败 ⇒ 回收器看不见它 ⇒ 泄漏。
	generated := NewIsolatedSchemaName("dbschema_")
	info, ok := parseSchemaName(generated)
	if !ok {
		t.Fatalf("回收器必须认识 dbschema_ 前缀（%q 解析失败）—— 否则 dbschema 包遗留的 "+
			"schema 永远不会被回收（而 dbschema 包已改为用它自己的 schema，不再占用 public）",
			generated)
	}

	// 断言「只要无活主且够旧，就会被回收」—— 与 test_ 前缀的规则完全一致。
	full := schemaInfo{
		Name: generated, Namespace: info.Namespace,
		PID: info.PID, CreatedUnixNano: old.UnixNano(),
	}
	if !shouldReapSchema(full, 0, now) {
		t.Error("db schema 前缀的 schema 在「无活主 + 够旧」时也必须被回收 —— " +
			"两个前缀的规则必须完全一致，否则就是给某个包开了规则裂缝")
	}

	// 反过来：有活主时照样不回收（前缀不同不构成例外）。
	if shouldReapSchema(full, 1, now) {
		t.Error("dbschema_ 前缀的 schema 有活主时同样不得回收")
	}
}

// TestSchemaNameIsUsedAsApplicationName 接线守卫：schema 名必须被用作
// application_name（活主判据的全部基础）。
//
// 没有这条接线，pg_stat_activity 里就没有标记，活主查询恒为 0
// ⇒ 回收器会**误删正在使用的 schema**（比不回收更糟）。
func TestSchemaNameIsUsedAsApplicationName(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	body := string(src)
	if !strings.Contains(body, "application_name=%s") {
		t.Fatal("DSN 必须包含 application_name —— 它是活主判据的唯一来源；" +
			"缺了它 pg_stat_activity 里无标记，回收器会把正在用的 schema 当残留删掉")
	}
	if !strings.Contains(body, "pg_stat_activity WHERE application_name = ?") {
		t.Fatal("活主查询必须按 application_name 过滤 pg_stat_activity")
	}
	// base DSN 必须在 CREATE SCHEMA **之前**就带上 application_name，
	// 否则「schema 已建、标记连接未建」的窗口里会被误判无活主。
	const createStmt = `base.Exec(fmt.Sprintf("CREATE SCHEMA %s", schemaName))`
	iApp := indexOf(body, "application_name=%s")
	iCreate := indexOf(body, createStmt)
	if iApp < 0 || iCreate < 0 || iApp > iCreate {
		t.Errorf("application_name 必须出现在 CREATE SCHEMA **之前**（当前 app@%d create@%d）；"+
			"否则建表与建连之间有一个「有 schema 无活主」的窗口，可能被别的进程回收", iApp, iCreate)
	}
}

// TestSearchPathExcludesPublic 钉住「search_path 只放隔离 schema」。
//
// ============ 为什么这是结构性的，不是检查性的 ============
//
// 原写法 search_path=<schema>,public 让「隔离 schema 缺某张表」变成
// **静默命中 public 的同名表**，症状是 42P07「表已存在」/42P16「列在主键里」
// 这类完全指错方向的错误（2026-11-08 就发生过一次）。
//
// 去掉 public 后，缺表就是 ERROR: relation "x" does not exist ——
// **每条查询都强制隔离**，不依赖任何一次开库检查。
func TestSearchPathExcludesPublic(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	body := string(src)
	if strings.Contains(body, "search_path=%s,public") {
		t.Fatal("search_path 不得包含 public —— 那会让「隔离 schema 缺表」静默命中 public，" +
			"症状是 42P07/42P16 这类指错方向的错误。见 db.go 里该 DSN 的注释（含三条前提核实）")
	}
	if !strings.Contains(body, "search_path=%s") {
		t.Fatal("找不到 search_path —— 隔离机制失效")
	}
}

// TestCurrentNamespaceNonEmpty 本进程必须能拿到命名空间标识（**仅用于人看**）。
//
// 拿不到不影响正确性（活主判据是 application_name，与它无关），
// 但名字里缺这一段会让人排查时少一条线索。
func TestCurrentNamespaceNonEmpty(t *testing.T) {
	ns := currentNamespace()
	if ns == "" {
		t.Log("currentNamespace() 返回空：不影响正确性（活主判据是 application_name），" +
			"但 schema 名里少一段人读线索")
	}
	if strings.ContainsAny(ns, "ABCDEFGHIJKLMNOPQRSTUVWXYZ-./:") {
		t.Errorf("currentNamespace() = %q 未 sanitize（大小写/特殊字符会让 schema 名需要引号）", ns)
	}
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
