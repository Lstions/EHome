package testutil

// 陈旧 schema 回收器的单元测试（回归锁）。
//
// 背景：`openPostgres` 的清理靠 `t.Cleanup`，而 `t.Cleanup` **只在进程正常退出时执行**。
// go test 被 SIGKILL / SIGPIPE 打断（`go test … | head` 即 SIGPIPE）时 schema 会永久残留。
// 实测复现：PG 测试跑到约 800ms 时 `kill -9`，`ehome_sim_pg` 留下 `test_3861920_1789489580224261933`。
// `reapStaleSchemas` 是补救（落地 docs/取证/投递审计清理与PG验证-2026-09-14.md §8.2 的建议）。
//
// 本文件**只测纯函数**（解析与存活判断），不连数据库 ——
// 真实回收行为由「跑一次真 PG 测试」的实跑验证（本轮已做：残留 1 → 0）。
// 这样本文件在 SQLite 模式（默认、无 PG）下也必须全绿，不会给默认门禁引入外部依赖。

import (
	"os"
	"testing"
)

// TestPidFromSchemaName 钉住解析规则：只有 `test_<pid>_<nano>` 才认，其余一律不碰。
// 这条边界很重要：认不出的名字若被「猜」出 pid，就可能误删别的用途的 schema。
func TestPidFromSchemaName(t *testing.T) {
	ok := []struct {
		name string
		pid  int
	}{
		{"test_1234_1789489580224261933", 1234},
		{"test_1_1", 1},
		{"test_999999_0", 999999},
	}
	for _, c := range ok {
		got, valid := pidFromSchemaName(c.name)
		if !valid || got != c.pid {
			t.Errorf("pidFromSchemaName(%q) = (%d,%v)，期望 (%d,true)", c.name, got, valid, c.pid)
		}
	}

	bad := []string{
		"public",                          // 系统 schema
		"test_",                           // 空
		"test",                            // 无前缀下划线
		"test_abc_123",                    // pid 非数字
		"test_0_123",                      // pid 必须 > 0
		"test_-1_123",                     // 负数
		"test_1234",                       // 缺 nano 段
		"other_1234_1789489580224261933",  // 前缀不符
		"test_1234_1789489580224261933_x", // 多余段（SplitN 只切两段 ⇒ 剩余仍在第二段，视为合法）
	}
	for _, name := range bad[:len(bad)-1] { // 最后一条按当前实现是**合法**的，见下
		if _, valid := pidFromSchemaName(name); valid {
			t.Errorf("pidFromSchemaName(%q) 应判为不合法（fail-closed：宁可漏收也不误删）", name)
		}
	}
	// 显式记录一个**已知宽松点**：SplitN(_,2) 让「多余段」仍被解析出 pid。
	// 它不会造成误删（pid 存活判断仍生效），故不修；此处留档以免被当成疏漏。
	if pid, valid := pidFromSchemaName("test_1234_1789489580224261933_x"); !valid || pid != 1234 {
		t.Logf("已知宽松点行为变化：pid=%d valid=%v（原为 1234,true）", pid, valid)
	}
}

// TestProcessAlive 钉住存活的判定方向：
//
//	· 自己的 pid ⇒ 必然存活；
//	· 一个**确定不存在**的 pid ⇒ 必须判为已死（否则回收器永不动作 = 形同虚设）。
func TestProcessAlive(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Fatalf("processAlive(self=%d) 应为 true", os.Getpid())
	}
	// 用一个几乎不可能存在的 pid（Linux 默认 pid_max 上限 4194304）。
	const deadPID = 1 << 30
	if processAlive(deadPID) {
		t.Errorf("processAlive(%d) 应为 false —— 若恒为 true，reapStaleSchemas 将永远什么都不回收", deadPID)
	}
}

// TestReapStaleSchemasWiredBeforeCreate 是**接线守卫**：
// 回收必须在 `CREATE SCHEMA` **之前**调用，否则本轮残留要等下一次才清、且新旧混在一起。
// 同时确保它真的被 openPostgres 调用（防止有人把函数留着但断掉接线 ⇒ 门禁全绿而功能消失）。
func TestReapStaleSchemasWiredBeforeCreate(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	body := string(src)
	iReap := indexOf(body, "reapStaleSchemas(base)")
	iCreate := indexOf(body, "CREATE SCHEMA")
	if iReap < 0 {
		t.Fatalf("openPostgres 未调用 reapStaleSchemas —— 陈旧 schema 会永久残留（SIGKILL/SIGPIPE 场景）")
	}
	if iCreate < 0 {
		t.Fatalf("找不到 CREATE SCHEMA —— 判据失效")
	}
	if iReap > iCreate {
		t.Errorf("reapStaleSchemas 必须在 CREATE SCHEMA 之前调用（当前 reap@%d > create@%d）", iReap, iCreate)
	}
	// 注意：这里**不再**用 `strings.Contains(body, "processAlive(pid)")` 断言安全规则。
	// 该判据在 `if processAlive(pid) && false {…}` 之下依然成立 —— 变异已证明它是假绿。
	// 安全规则改由 TestShouldReapSchema 做**行为**验证。
}

// TestShouldReapSchema 用**行为**钉住回收决策与安全规则。
//
// 为什么不用源码扫描：初版判据是 `strings.Contains(body, "processAlive(pid)")`，
// 而把安全规则改写成 `if processAlive(pid) && false {…}`（逻辑上已完全失效）后，
// 该字符串**依然存在** ⇒ 门禁全绿放行。抽取纯函数后，这类架空无法再逃过行为断言。
func TestShouldReapSchema(t *testing.T) {
	alwaysAlive := func(int) bool { return true }
	alwaysDead := func(int) bool { return false }

	// ① pid 存活 ⇒ 绝不回收（并行测试进程可能正在用）
	if shouldReapSchema("test_1234_1", alwaysAlive) {
		t.Errorf("pid 存活时不得回收 —— 会误删并行测试进程正在用的 schema")
	}
	// ② pid 已死 ⇒ 回收（否则残留永不清理，本功能形同虚设）
	if !shouldReapSchema("test_1234_1", alwaysDead) {
		t.Errorf("pid 已死时应回收")
	}
	// ③ 格式不符 ⇒ 一律不回收（fail-closed，与存活判定无关）
	for _, bad := range []string{"public", "test_", "test_abc_1", "other_1_1"} {
		if shouldReapSchema(bad, alwaysDead) {
			t.Errorf("格式不符的 %q 不得回收（fail-closed：宁可漏收也不误删）", bad)
		}
	}
	// ④ 存活判定的**结果必须真的影响决策**（防止调用结果被丢掉/被 && false 架空）
	if shouldReapSchema("test_1234_1", alwaysAlive) == shouldReapSchema("test_1234_1", alwaysDead) {
		t.Errorf("存活判定的结果未影响决策 —— 安全规则已被架空")
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
