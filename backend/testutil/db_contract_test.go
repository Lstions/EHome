package testutil

// testutil 的分派契约门禁（回归锁）。
//
// 背景（2026-09-16 实测）：本包是**测试用库的唯一入口**，但此前**没有任何测试**。
// 计划文档 `docs/分析/后续工作计划与方案-2026-09-15.md` 的 §1.1 门禁表与 §5 验收清单
// 都写着 `EHOME_DB_NAME=ehome_sim_pg go test ./internal/...` —— 看起来是「在 PG 隔离库上跑」，
// 但本包是按 **`EHOME_TEST_DB`** 分派的（见 OpenTestDB），只给 `EHOME_DB_NAME` 会走**内存 SQLite**。
// 实测证据：同一条命令加不加 `EHOME_TEST_DB=postgres`，
//   · 不加：`internal/api` 3.8s，且运行期间 `pg_stat_activity` 里 `ehome_sim_pg` 的连接峰值 = **0**；
//   · 加了：`internal/api` 23.9s，连接峰值 = **1**。
// ⇒ 那一行是**假绿**：命令里写着 PG 库名，实际从未连过 PG。
//
// 为什么需要本文件（而不是只在文档里改对）：
//   文档里的命令**不会被任何东西检查**，改对了下次还会被改回去或复制到别处。
//   本文件把「分派语义」钉成可执行断言，并把**文档里必须出现 `EHOME_TEST_DB`** 也纳入守卫，
//   这样「文档写了 PG 但代码不支持」或「有人把 env 名改掉」都会立刻变红。
//
// ── 本门禁查不了什么 ──────────────────────────────────────────────────────
//   · 查不了 **PG 是否真的可用**（本文件跑在任意后端下都必须通过，故不真连 PG）；
//   · 查不了其它文档（只覆盖计划文档；`docs/操作/开发环境事实.md` 由 P1-A 脚本承担）；
//   · 查不了运行时驱动类型（那需要真连库，由带 `EHOME_TEST_DB=postgres` 的实跑覆盖）。

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestOpenTestDB_DispatchContract 钉住分派语义：Web 与 sqlite 等价，postgres 才走 PG，
// 未知值必须**显式失败**（不能静默回落到 SQLite —— 那正是假绿的温床）。
func TestOpenTestDB_DispatchContract(t *testing.T) {
	src, err := os.ReadFile("db.go")
	if err != nil {
		t.Fatalf("read db.go: %v", err)
	}
	body := string(src)

	// ① 分派依据必须是 EHOME_TEST_DB（而不是 EHOME_DB_NAME）
	//
	// 判据必须锚在**赋值语句**上，不能只 Contains `os.Getenv("EHOME_TEST_DB")`：
	// 该字符串在本文件里出现 3 次（文件头注释、IsPostgres()、分派处），
	// 只查 Contains 会让「把分派换成别的 env、但 IsPostgres 与注释没改」**假绿通过**。
	// （这不是假设：本门禁第一版就是这么写的，被变异 MUT-C 当场抓出。）
	dispatch := regexp.MustCompile(`driver\s*:=\s*os\.Getenv\("EHOME_TEST_DB"\)`)
	if !dispatch.MatchString(body) {
		t.Fatalf("OpenTestDB 的分派语句不再是 `driver := os.Getenv(\"EHOME_TEST_DB\")` —— " +
			"若换成别的 env，所有文档里的 PG 命令都会静默变成 SQLite 假绿。请同步更新文档与本门禁")
	}
	if regexp.MustCompile(`driver\s*:=\s*os\.Getenv\("EHOME_DB_NAME"\)`).MatchString(body) {
		t.Errorf("分派不得改用 EHOME_DB_NAME —— 它是**连上之后**用的库名，不是选驱动器的开关")
	}
	// ② 未知值必须 t.Fatalf，不得静默回落
	if !regexp.MustCompile(`default:\s*\n\s*t\.Fatalf`).MatchString(body) {
		t.Errorf("OpenTestDB 的 default 分支必须 t.Fatalf（未知 EHOME_TEST_DB 值不得静默回落 SQLite）")
	}
	// ③ postgres 分支必须真的建隔离 schema（隔离单元是 schema 而非 database）
	if !strings.Contains(body, "CREATE SCHEMA") {
		t.Errorf("openPostgres 必须建隔离 schema（CREATE SCHEMA）—— " +
			"残留检查查的是 pg_namespace，若隔离单元改成 database，那条检查会失效")
	}
	if !strings.Contains(body, `schemaName := fmt.Sprintf("test_%s"`) {
		t.Errorf("隔离 schema 命名必须是 test_<suffix> —— 残留检查按 'test_%%' 前缀匹配")
	}
}

// normativePart 截掉计划文档的 **§1.7（本轮落地结果/自测记录）** 段落，只留「规范」部分。
//
// 为什么必须排除：§1.7 是**叙事记录**，它为了留下证据会**原样引用**当时那条错误命令
// （「原文是 `EHOME_DB_NAME=… go test …`」）。纯文本扫描器分不清「让你执行的命令」与
// 「作为反例被引用的命令」，不做区分就会把**记录本身**判成违规（本门禁第一版正是如此，
// 被自己的全量跑当场抓出）。
//
// 边界（诚实声明）：本函数按标题切分，**若将来重命名/移动 §1.7 标题，排除会失效**；
// 届时下面的分母守卫会因「找不到段落」而 Failed（不是静默放过）—— 这是刻意选择的失败方向。
func normativePart(doc string) string {
	lines := strings.Split(doc, "\n")
	start, end := -1, len(lines)
	for i, l := range lines {
		if start < 0 && strings.HasPrefix(l, "### 1.7") {
			start = i
			continue
		}
		if start >= 0 && strings.HasPrefix(l, "## ") {
			end = i
			break
		}
	}
	if start < 0 {
		return "" // 找不到 §1.7 ⇒ 由调用方判为失败，不静默当成「全文规范」
	}
	return strings.Join(append(append([]string{}, lines[:start]...), lines[end:]...), "\n")
}

// TestPlanDocPGCommandEnablesPostgresMode 是**文档守卫**：
// 计划文档的**规范部分**里凡出现「在 PG 隔离库上跑 go test」的命令，必须显式带 `EHOME_TEST_DB=postgres`。
// 这一条直接防住本轮实测到的那次假绿复发。
func TestPlanDocPGCommandEnablesPostgresMode(t *testing.T) {
	docPath := filepath.Join("..", "..", "docs", "分析", "后续工作计划与方案-2026-09-15.md")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Skipf("计划文档不在预期路径（%v）—— 本守卫仅在完整仓库内生效", err)
	}
	normative := normativePart(string(raw))
	if normative == "" {
		t.Fatalf("未能在计划文档里定位 §1.7 段落 —— 标题可能已改名。" +
			"本守卫刻意选择**失败**而不是退化成「全文当规范」（那会把叙事里的反例判成违规）")
	}

	// 找出规范部分里所有「带 ehome_sim_pg 的 go test 命令」所在行，要求同行或紧邻上文含 EHOME_TEST_DB=postgres。
	lines := strings.Split(normative, "\n")
	var offenders []string
	for i, line := range lines {
		if !strings.Contains(line, "go test") || !strings.Contains(line, "ehome_sim_pg") {
			continue
		}
		// 同行或前一行（命令常折行）出现即可
		ctx := line
		if i > 0 {
			ctx = lines[i-1] + "\n" + line
		}
		if !strings.Contains(ctx, "EHOME_TEST_DB=postgres") {
			offenders = append(offenders, strings.TrimSpace(line))
		}
	}
	if len(offenders) > 0 {
		t.Errorf("计划文档的规范部分里有命令写着 ehome_sim_pg 却没设 EHOME_TEST_DB=postgres，"+
			"实际会跑内存 SQLite（假绿）：\n  %s", strings.Join(offenders, "\n  "))
	}

	// 分母守卫：规范部分必须真的含 PG 命令，否则本守卫是空转
	if !strings.Contains(normative, "ehome_sim_pg") {
		t.Fatalf("规范部分里找不到 ehome_sim_pg —— 判据或切分失效，本守卫形同虚设")
	}
}

// TestResidueCheckUsesNamespaceNotDatabase 钉住残留检查的对象：
// 文档里的残留检查必须查 `pg_namespace`；查 `pg_database` 恒为 0（隔离单元是 schema），等于没查。
func TestResidueCheckUsesNamespaceNotDatabase(t *testing.T) {
	docPath := filepath.Join("..", "..", "docs", "分析", "后续工作计划与方案-2026-09-15.md")
	raw, err := os.ReadFile(docPath)
	if err != nil {
		t.Skipf("计划文档不在预期路径（%v）", err)
	}
	// 同样只看规范部分：§1.7 会引用「当时的错误写法」作为证据，那是叙事不是指令。
	normative := normativePart(string(raw))
	if normative == "" {
		t.Fatalf("未能在计划文档里定位 §1.7 段落 —— 标题可能已改名")
	}
	if strings.Contains(normative, "FROM pg_database WHERE datname LIKE 'test_") {
		t.Errorf("规范部分的残留检查不得查 pg_database（隔离单元是 schema，该查询恒为 0）—— 应查 pg_namespace.nspname")
	}
	if !strings.Contains(normative, "FROM pg_namespace WHERE nspname LIKE 'test_") {
		t.Errorf("规范部分的残留检查必须查 pg_namespace.nspname LIKE 'test_%%'")
	}
}
