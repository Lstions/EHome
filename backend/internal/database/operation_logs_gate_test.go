package database

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =====================================================================
// INV-8 死 schema 不得复活 (operation_logs) —— 源码级门禁。
//
// 背景 (docs/分析/运行期无界增长表-保留策略设计-2026-09-13.md §2.7 + INV-8):
// models.OperationLog 是死 schema —— 运行期写入者 0、读取者 0、数据 0 行,
// 唯一生产引用是 AutoMigrate 注册, 且已被 models.SecurityAuditEvent 取代。
// 2026-09 裁决为**退役**(不给清理策略: 给一张没人写/没人读/0 行的表配清理器是空转),
// 处置 = 删模型 + 摘 AutoMigrate 注册 + 幂等 DROP 表 (RetireLegacyOperationLogs)。
//
// 本门禁是回归锁, 断言:
//
//	(1) operation_logs 不出现在任何建表/AutoMigrate 语义里 (注册被加回 ⇒ 立刻红);
//	(2) 生产 (非 _test.go) Go 源码零引用已退役类型 models.OperationLog;
//	(3) 退役步骤本身仍在, 用幂等的 DROP TABLE IF EXISTS, 且被生产路径调用。
//
// 范围声明 (诚实边界): 本门禁是**源码扫描**。"表在运行库中确实不存在"需要
// 真实方言, 由 gorm_test.go 的 OperationLogRetired (SQLite) 与 PG 集成测试覆盖,
// 不在本扫描能力内。扫描器必须能变红 (TestOperationLogsGate_ClassifierSelfCheck),
// 否则一个坏掉的扫描器会让门禁永远"绿" (vacuous pass)。
//
// 假阳性防护 (实测踩过的坑): 退役步骤的导出名 RetireLegacyOperationLogs 与
// 调用行都含 "OperationLog" 子串, 但**不含类型引用** models.OperationLog /
// &OperationLog{}。扫描器只认类型引用与建表语义, 不认裸子串 —— 否则门禁会
// 把自己的退役步骤判红, 逼人加豁免 (豁免即后门)。
// =====================================================================

// operationLogsFileName 是幂等 DROP 步骤所在文件 —— 生产代码中唯一允许出现
// 该表名的地方 (门禁测试自身除外)。**无门禁后门**: 该文件只被允许写 DROP /
// 常量定义 / 行数留痕; 它里面若出现建表语义, 扫描器照样判红 (已自检)。
const operationLogsFileName = "retire_operation_logs.go"

// operationLogsTableName 已退役的表名。
const operationLogsTableName = "operation_logs"

// operationLogsModelType 已退役的模型类型名 (只作后缀拼接用)。
const operationLogsModelType = "OperationLog"

// modelsOperationLogRef 是"引用已退役类型"的唯一两种写法。
var modelsOperationLogRefs = []string{"models." + operationLogsModelType, "&" + operationLogsModelType + "{}"}

// containsRetiredModelRef 报告 line 是否引用了已退役的模型类型。
func containsRetiredModelRef(line string) bool {
	for _, ref := range modelsOperationLogRefs {
		if strings.Contains(line, ref) {
			return true
		}
	}
	return false
}

// isCommentLine 报告 line 是否为注释行 (注释掉的注册不是注册;
// 说明性/历史性提及不算复活)。
func isCommentLine(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*")
}

// codeOf 剥掉行尾行注释, 返回该行的可执行代码部分。
// 必须剥: 注释里的"历史提及"(如 retire 步骤的文档注释提到 DROP TABLE IF EXISTS)
// 否则会被当成真语句 —— 门禁的第一版就栽在这里。
func codeOf(line string) string {
	t := strings.TrimSpace(line)
	if isCommentLine(t) {
		return ""
	}
	if i := strings.Index(t, "//"); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	return t
}

// isDropLine 报告 line 是否为幂等退役 DROP (只看代码部分)。
func isDropLine(line string) bool {
	return strings.Contains(strings.ToUpper(codeOf(line)), "DROP TABLE")
}

// isCreateSemanticsLine 报告 line 是否带建表/AutoMigrate 语义。
func isCreateSemanticsLine(line string) bool {
	up := strings.ToUpper(codeOf(line))
	for _, marker := range []string{"AUTOMIGRATE", "CREATETABLE", "CREATE TABLE", "PARTITION OF"} {
		if strings.Contains(up, marker) {
			return true
		}
	}
	return false
}

// isAutoMigrateRegistrationLine 报告 line 是否把 operation_logs 交回
// AutoMigrate (或任何建表语义)。两种信号各自独立成立:
//
//	a) 类型引用 models.OperationLog / &OperationLog{} 出现在模型清单;
//	b) 表名字面量出现在建表/AutoMigrate 语句里。
//
// 纯 DROP 退役语句返回 false。
func isAutoMigrateRegistrationLine(line string) bool {
	if isCommentLine(line) || isDropLine(line) {
		return false
	}
	if containsRetiredModelRef(line) {
		return true
	}
	if strings.Contains(line, operationLogsTableName) && isCreateSemanticsLine(line) {
		return true
	}
	return false
}

// isAllowedRetirementUsage 报告 line 是否是退役步骤里对表名的合法用法:
// 幂等 DROP / 表名常量定义 / 行数留痕 / 诊断消息 (报错与日志) 里的文字提及。
// 诊断消息是被允许的 —— 它只是给人看的文字, 不是 SQL, 也不会让表复活。
func isAllowedRetirementUsage(line string) bool {
	code := codeOf(line)
	if code == "" {
		// 纯注释行 (文档/裁决引用): 不含任何 SQL, 不会让表复活。
		return true
	}
	if isDropLine(code) {
		return true
	}
	// 表名常量的声明行 (值为表名字符串): 常量不是 SQL, 不能建表。
	// 若有人把 "CREATE TABLE ..." 塞进常量, isAutoMigrateRegistrationLine
	// 已先一步判红 (自检覆盖), 故此豁免是安全的。
	if strings.HasPrefix(code, "const ") {
		return true
	}
	if strings.Contains(strings.ToUpper(code), "SELECT COUNT(*)") {
		return true
	}
	// 诊断消息 (报错/日志) 里的文字提及: 只是给人看的文字, 不是 SQL。
	// 判定: 该行的表名紧跟着 ", " 或 ")" 或字符串结束 —— 即它是消息的一部分,
	// 而不是被当作 SQL 标识符/查询目标。
	for _, diagnostic := range []string{"logger.", "fmt.Errorf(", "fmt.Sprintf("} {
		if !strings.Contains(code, diagnostic) {
			continue
		}
		for _, after := range []string{operationLogsTableName + ", ", operationLogsTableName + ")", operationLogsTableName + "\""} {
			if strings.Contains(code, after) {
				return true
			}
		}
		// 表名只出现在消息文字中 (不含表名常量拼接) 也算诊断。
		if !strings.Contains(code, "operationLogsTable") {
			return true
		}
	}
	return false
}

// classifyOperationLogsSource 检测单份源码的复活信号 (无证据收集, 供自检用)。
func classifyOperationLogsSource(path, content string) (autoMigrateRegistration, modelReference bool) {
	autoMigrateRegistration, modelReference, _ = classifyWithEvidence(path, content)
	return autoMigrateRegistration, modelReference
}

// classifyWithEvidence 同 classifyOperationLogsSource, 但额外返回逐条证据
// ("文件:行号: 内容"), 便于门禁变红时直接定位 —— 门禁的报错必须能指到行。
//
// path 以 _test.go 结尾时返回零值 (测试里的提及不是生产接线);
// retire_operation_logs.go 是退役步骤本身, 只允许 DROP/常量/行数留痕。
func classifyWithEvidence(path, content string) (autoMigrateRegistration, modelReference bool, evidence []string) {
	base := filepath.Base(path)
	if strings.HasSuffix(base, "_test.go") {
		return false, false, nil
	}
	allowDropOnly := base == operationLogsFileName
	for i, line := range strings.Split(content, "\n") {
		where := fmt.Sprintf("%s:%d", path, i+1)
		if isAutoMigrateRegistrationLine(line) {
			autoMigrateRegistration = true
			evidence = append(evidence, fmt.Sprintf("建表/AutoMigrate 语义 %s: %s", where, strings.TrimSpace(line)))
		}
		if isCommentLine(line) {
			continue
		}
		if isDropLine(line) {
			// DROP 是退役动作本身, 不是对类型的引用。
			continue
		}
		if containsRetiredModelRef(line) {
			modelReference = true
			evidence = append(evidence, fmt.Sprintf("引用已退役类型 %s: %s", where, strings.TrimSpace(line)))
		}
		// 退役步骤里除"合法退役用法"外的表名用法一律视为复活隐患
		// (含猜测表名的查询字符串)。
		if allowDropOnly && strings.Contains(line, operationLogsTableName) &&
			!isAllowedRetirementUsage(line) {
			modelReference = true
			evidence = append(evidence, fmt.Sprintf("退役步骤里的非法表名用法 %s: %s", where, strings.TrimSpace(line)))
		}
	}
	return autoMigrateRegistration, modelReference, evidence
}

// scanEvidence 收集全仓生产源码的复活证据 (供门禁报错定位)。
func scanEvidence(t *testing.T, root string) []string {
	t.Helper()
	var evidence []string
	for _, sub := range []string{"internal", "cmd", "testutil"} {
		walkRoot := filepath.Join(root, sub)
		if _, err := os.Stat(walkRoot); err != nil {
			continue
		}
		err := filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(filepath.Base(p), "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			_, _, ev := classifyWithEvidence(p, string(b))
			evidence = append(evidence, ev...)
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", sub, err)
		}
	}
	return evidence
}

// backendRoot 从工作目录向上定位 backend/ (以 go.mod 为锚)。
func backendRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatalf("backend root (go.mod) not found above %s", dir)
		}
		dir = parent
	}
}

// scanOperationLogsRetirement 扫描 backend 的生产 Go 源码 (internal/ + cmd/ +
// testutil, 跳过 _test.go), 返回 (建表语义命中, 模型引用命中, 扫描文件数)。
//
// 排除项 (与 §2.7 清单一致): docs/ 与 archive/ 的历史提及不在 backend/ 扫描面内
// (本函数只走 backend 的 Go 源码树, 天然不含 docs/); 注释行由 isCommentLine 跳过;
// _test.go 由后缀跳过。否则归档文档与历史注释里的提及会让门禁误红。
func scanOperationLogsRetirement(t *testing.T) (bool, bool, int) {
	t.Helper()
	root := backendRoot(t)
	var autoMigrateRegistration, modelReference bool
	scanned := 0
	for _, sub := range []string{"internal", "cmd", "testutil"} {
		walkRoot := filepath.Join(root, sub)
		if _, err := os.Stat(walkRoot); err != nil {
			continue
		}
		err := filepath.WalkDir(walkRoot, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() || !strings.HasSuffix(p, ".go") ||
				strings.HasSuffix(filepath.Base(p), "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			scanned++
			reg, ref := classifyOperationLogsSource(p, string(b))
			autoMigrateRegistration = autoMigrateRegistration || reg
			modelReference = modelReference || ref
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", sub, err)
		}
	}
	if scanned == 0 {
		t.Fatal("gate scanner found no production sources — refusing to pass vacuously")
	}
	return autoMigrateRegistration, modelReference, scanned
}

// TestOperationLogsGate_NotResurrected 即 INV-8: 死 schema 不得复活。
func TestOperationLogsGate_NotResurrected(t *testing.T) {
	reg, ref, scanned := scanOperationLogsRetirement(t)
	t.Logf("INV-8 扫描: %d 个生产 Go 文件 (internal/ + cmd/ + testutil, 跳过 _test.go)", scanned)
	if reg {
		t.Fatalf("INV-8 违反: operation_logs 重新出现在建表/AutoMigrate 语义中 —— " +
			"该表已于 2026-09 退役 (0 写入者/0 读取者/0 行, 已被 security_audit_events 取代), " +
			"不得再注册; 见 docs/分析/运行期无界增长表-保留策略设计-2026-09-13.md §2.7")
	}
	if ref {
		for _, ev := range scanEvidence(t, backendRoot(t)) {
			t.Logf("证据: %s", ev)
		}
		t.Fatalf("INV-8 违反: 生产代码引用了已退役的 models.OperationLog —— " +
			"审计事件唯一载体是 models.SecurityAuditEvent; " +
			"见 docs/分析/运行期无界增长表-保留策略设计-2026-09-13.md §2.7")
	}
}

// extractAutoMigrateRegion 取出一个 AutoMigrate 实参列表/模型清单区块。
// 找不到标记即 t.Fatal —— 防止有人改写法让本门禁静默失效 (vacuous pass)。
func extractAutoMigrateRegion(t *testing.T, rel, src, startMarker, endMarker string) string {
	t.Helper()
	start := strings.Index(src, startMarker)
	if start < 0 {
		t.Fatalf("%s 找不到 %q —— 门禁无法扫描 (写法变更需同步更新门禁)", rel, startMarker)
	}
	rest := src[start:]
	end := strings.Index(rest, endMarker)
	if end < 0 {
		t.Fatalf("%s 的 %q 区块找不到结尾 %q", rel, startMarker, endMarker)
	}
	return rest[:end]
}

// TestOperationLogsGate_AutoMigrateListClean 额外钉死两份"模型清单"都不得含该模型
// (防止有人只改一处)。只看清单区块, 不看全文件 —— 否则 AutoMigrate 尾部对
// RetireLegacyOperationLogs 的**退役调用**会被误判 (子串陷阱)。
func TestOperationLogsGate_AutoMigrateListClean(t *testing.T) {
	root := backendRoot(t)
	cases := []struct {
		rel, start, end string
	}{
		{filepath.Join("internal", "database", "gorm.go"), "DB.AutoMigrate(", "\n\t);"},
		{filepath.Join("testutil", "db.go"), "var allModels = []interface{}{", "\n}"},
	}
	for _, c := range cases {
		raw, err := os.ReadFile(filepath.Join(root, c.rel))
		if err != nil {
			t.Fatalf("read %s: %v", c.rel, err)
		}
		region := extractAutoMigrateRegion(t, c.rel, string(raw), c.start, c.end)
		for _, line := range strings.Split(region, "\n") {
			if isCommentLine(line) {
				continue
			}
			if strings.Contains(line, operationLogsModelType) || strings.Contains(line, operationLogsTableName) {
				t.Fatalf("%s 的模型清单仍含 operation_logs: %q (INV-8: 死 schema 不得复活)", c.rel, strings.TrimSpace(line))
			}
		}
	}
}

// TestOperationLogsGate_RetirementStepIsIdempotentDrop 断言退役步骤本身仍然存在,
// 且是幂等 DROP 并被生产路径调用 (有人"顺手"删掉 ⇒ 存量库的表永久残留而无人再清)。
func TestOperationLogsGate_RetirementStepIsIdempotentDrop(t *testing.T) {
	root := backendRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "database", operationLogsFileName))
	if err != nil {
		t.Fatalf("退役步骤 %s 缺失 (INV-8: 存量库的表将无人清理): %v", operationLogsFileName, err)
	}
	src := string(raw)
	if !strings.Contains(src, "DROP TABLE IF EXISTS ") {
		t.Fatal("退役步骤必须使用幂等的 DROP TABLE IF EXISTS (范式同 datalifecycle/partition_mgr.go dropLegacyTable)")
	}
	if !strings.Contains(src, "func RetireLegacyOperationLogs(") {
		t.Fatal("退役步骤必须导出 RetireLegacyOperationLogs 供 AutoMigrate 调用")
	}
	gormRaw, err := os.ReadFile(filepath.Join(root, "internal", "database", "gorm.go"))
	if err != nil {
		t.Fatalf("read gorm.go: %v", err)
	}
	// 断言调用点位于 AutoMigrate 函数体内 (尾部), 而不是别处。
	autoMigrateRegion := extractAutoMigrateRegion(t, "gorm.go", string(gormRaw), "func AutoMigrate() error {", "\n}\n")
	if !strings.Contains(autoMigrateRegion, "RetireLegacyOperationLogs(DB)") {
		t.Fatal("gorm.go 的 AutoMigrate 未调用 RetireLegacyOperationLogs —— 退役 DDL 不会被生产路径执行")
	}
}

// TestOperationLogsGate_ClassifierSelfCheck 证明扫描器**有能力**看见复活信号。
// 没有这条自检, 一个坏掉的扫描器会让门禁永远"绿" (vacuous pass)。
func TestOperationLogsGate_ClassifierSelfCheck(t *testing.T) {
	cases := []struct {
		name             string
		path             string
		content          string
		wantRegistration bool
		wantModelRef     bool
	}{
		{
			name:             "把注册加回 AutoMigrate 列表 ⇒ 红",
			path:             "internal/database/gorm.go",
			content:          "\t\t&models.OperationLog{},\n",
			wantRegistration: true,
			wantModelRef:     true,
		},
		{
			name:             "生产代码写 models.OperationLog ⇒ 红",
			path:             "internal/api/handler_audit.go",
			content:          "\tvar ol models.OperationLog\n\tdb.Create(&ol)\n",
			wantRegistration: true,
			wantModelRef:     true,
		},
		{
			name:             "显式 AutoMigrate 该表名 ⇒ 红",
			path:             "internal/database/gorm.go",
			content:          "\tdb.AutoMigrate(\"operation_logs\")\n",
			wantRegistration: true,
		},
		{
			name:             "裸建表 SQL 该表名 ⇒ 红",
			path:             "internal/database/gorm.go",
			content:          "\tdb.Exec(\"CREATE TABLE operation_logs (id bigint)\")\n",
			wantRegistration: true,
		},
		{
			name:    "退役步骤的 DROP 语句 ⇒ 不红",
			path:    operationLogsFileName,
			content: "\tif err := db.Exec(\"DROP TABLE IF EXISTS \" + operationLogsTable + \" CASCADE\").Error; err != nil {\n",
		},
		{
			name:    "退役步骤的行数留痕 ⇒ 不红",
			path:    operationLogsFileName,
			content: "\tdb.Raw(\"SELECT count(*) FROM \" + operationLogsTable).Scan(&rows)\n",
		},
		{
			name:             "退役步骤里偷偷建表 ⇒ 红 (建表语义 + 非法表名用法 双命中)",
			path:             operationLogsFileName,
			content:          "\tdb.Exec(\"CREATE TABLE operation_logs (id bigint)\")\n",
			wantRegistration: true,
			wantModelRef:     true,
		},
		{
			name:    "退役步骤的表名常量声明 ⇒ 不红",
			path:    operationLogsFileName,
			content: "const operationLogsTable = \"operation_logs\"\n",
		},
		{
			name:             "常量里夹带建表 SQL ⇒ 红",
			path:             operationLogsFileName,
			content:          "const evil = \"CREATE TABLE operation_logs (id bigint)\"\n",
			wantRegistration: true,
		},
		{
			name:    "AutoMigrate 尾部的退役调用 (子串陷阱) ⇒ 不红",
			path:    "internal/database/gorm.go",
			content: "\t_, err = RetireLegacyOperationLogs(DB)\n",
		},
		{
			name:    "注释掉的注册不是注册 ⇒ 不红",
			path:    "internal/database/gorm.go",
			content: "\t\t// &models.OperationLog{},  // 已退役\n",
		},
		{
			name:    "测试文件里的提及不是生产接线 ⇒ 不红",
			path:    "internal/api/handler_audit_test.go",
			content: "\t&models.OperationLog{},\n",
		},
		{
			name:    "说明性提及 (注释) ⇒ 不红",
			path:    "internal/database/notes.go",
			content: "// 历史提及: operation_logs 已于 2026-09 退役\n",
		},
		{
			// docs/ 与 archive/ 是历史快照, 改写等于伪造历史 ⇒ 必须不扫、不红。
			// 扫描面只含 backend/{internal,cmd,testutil} 的 .go 文件, 本用例把
			// 该排除显式钉死 (归档文档里的历史提及不得让门禁误红)。
			name:    "归档文档里的历史提及 (docs/archive, 非 Go 源) ⇒ 不红",
			path:    "docs/archive/v2.0/设备分析报告.md",
			content: "| operation_logs | 未使用 | 定义存在但业务逻辑未使用 |\n",
		},
		{
			name:    "分析文档里的历史提及 (docs/分析) ⇒ 不红",
			path:    "docs/分析/运行期无界增长表-保留策略设计-2026-09-13.md",
			content: "| 1 | backend/internal/database/gorm.go:94 | **AutoMigrate 注册** | 从列表移除 |\n",
		},
	}
	for _, c := range cases {
		reg, ref := classifyOperationLogsSource(c.path, c.content)
		if reg != c.wantRegistration || ref != c.wantModelRef {
			t.Errorf("%s: classifyOperationLogsSource(%s)= (reg=%v ref=%v) want (reg=%v ref=%v)",
				c.name, c.path, reg, ref, c.wantRegistration, c.wantModelRef)
		}
	}
}
