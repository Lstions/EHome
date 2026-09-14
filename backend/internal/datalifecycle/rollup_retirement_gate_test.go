package datalifecycle

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =====================================================================
// 死表不得复活: unified_data_rollup_1m (2026-09-15 退役) —— 源码级门禁。
//
// 背景 (docs/分析/rollup-退役裁决-2026-09-15.md): 本表是"写了没人读, 且读了也不
// 划算"的冻结件。EXPLAIN 实测 (本轮复核): 30 天窗口的 unified_data 查询
//   - 带 LIMIT 的 /devices/:id/history      : Index Scan Backward, 0.79 ms / 104 buffers;
//   - 无 LIMIT 的 /unified-data/historical-batch (全窗口载入后降采样):
//     3.64 ms / 1191 buffers。
// 各分区上的 (logical_device_id, "timestamp" DESC) / ("timestamp") 索引本就存在,
// 长跨度查询本来就索引 + 分区裁剪; rollup 的收益是"省几毫秒", 代价却是第二张无界表
// (+保留期+清理器+测试) 与"必须改数据模型才能接线" (该表无 logical_device_id 列,
// 而本仓查询恒带 logical scope ⇒ 接线在生产不可达)。⇒ 退役。
//
// 2026-09-14 的**条件式 INV-7** ("若接线则必须有保留期") 随本体消失而作废:
// 它看守的功能已不存在, 再留着只能看守空气。本条门禁取而代之, 与
// database/operation_logs_gate_test.go (INV-8) **同范式**。
//
// 本门禁断言 (生产 = 非 _test.go 的 Go 源码, 扫描面 internal/ + cmd/ + testutil):
//
//	(1) 已退役的表名 unified_data_rollup_1m 不出现在任何生产代码语义里
//	    (建表 / AutoMigrate / SELECT / INSERT 全部判红; DROP 除外);
//	(2) 已退役的符号 (EnsureRollupTable / SetRollupSink / rollupSink /
//	    RollupConsumer / UnifiedDataRollup1m) 零生产引用;
//	(3) 退役步骤本身仍在 (internal/database/retire_rollup.go, 幂等
//	    DROP TABLE IF EXISTS), 且被 AutoMigrate 生产路径调用 —— 否则存量库
//	    (ehome/ehome_test) 里残留的表永久无人清理。
//
// 范围声明 (诚实边界): 本门禁是**源码扫描**。"表在运行库中确实不存在"需要真实方言,
// 由 database/retire_rollup_test.go (SQLite 与 PG 隔离 schema) 覆盖, 不在本扫描能力内。
//
// 扫描器自检 (TestRollupGate_ClassifierSelfCheck) 是**变异重放**: 把退役前真实存在
// 过的行 (以及本次退役删掉的 SetRollupSink / EnsureRollupTable / RollupConsumer
// 写法) 喂给扫描器, 要求逐条判红; 且这些用例是**硬编码**的, 不依赖被检清单自身 ——
// 有人把某个符号从 rollupRetiredSymbols 里删掉, 自检会立刻失败。没有这条自检,
// 一个坏掉的扫描器会让门禁永远"绿" (vacuous pass) —— 本仓已有此教训。
// =====================================================================

// rollupRetirementFile 是幂等 DROP 步骤所在文件 (basename): 生产代码中唯一允许
// 出现该表名的地方 (门禁测试自身除外)。**无门禁后门**: 该文件只被允许写 DROP /
// 常量定义 / 行数留痕 / 诊断消息; 它里面若出现建表语义或退役符号, 扫描器照样判红
// (自检覆盖)。
const rollupRetirementFile = "retire_rollup.go"

// rollupTableName 已退役的表名。
const rollupTableName = "unified_data_rollup_1m"

// rollupModelType 已退役的模型类型名。
const rollupModelType = "UnifiedDataRollup1m"

// rollupRetiredSymbols 是随退役必须消失的生产符号, 覆盖三层:
// 建表函数 (datalifecycle) / 写入回调机制 (databus) / 聚合消费者 (databus)。
// 任一非注释出现即红 —— 包括把它们加回 AutoMigrate 或重新接线。
var rollupRetiredSymbols = []string{
	"EnsureRollupTable",
	"SetRollupSink",
	"rollupSink",
	"RollupConsumer",
	rollupModelType,
}

// isCommentLine 报告 line 是否为注释行 (注释掉的接线/建表不是接线; 说明性、
// 历史性提及不算复活)。
func isCommentLine(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*")
}

// codeOf 剥掉行尾行注释, 返回该行的可执行代码部分。
// 必须剥: 注释里的"历史提及"(如"rollup 已退役")否则会被当成真语句。
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

// isDropLine 报告 code 是否为幂等退役 DROP (只看代码部分)。
func isDropLine(code string) bool {
	return strings.Contains(strings.ToUpper(code), "DROP TABLE")
}

// isCreateSemanticsLine 报告 code 是否带建表/AutoMigrate 语义。
func isCreateSemanticsLine(code string) bool {
	up := strings.ToUpper(code)
	for _, marker := range []string{"AUTOMIGRATE", "CREATETABLE", "CREATE TABLE", "PARTITION OF"} {
		if strings.Contains(up, marker) {
			return true
		}
	}
	return false
}

// isAllowedRetirementUsage 报告 code 是否是退役步骤里对表名的合法用法:
// 表名常量定义 / 行数留痕 / 诊断消息 (报错与日志) 里的文字提及。
// 诊断消息是被允许的 —— 它只是给人看的文字, 不是 SQL, 也不会让表复活。
func isAllowedRetirementUsage(code string) bool {
	if strings.HasPrefix(code, "const ") {
		// 常量不是 SQL, 不能建表。若有人把 "CREATE TABLE ..." 塞进常量,
		// 建表语义会先一步判红 (自检覆盖), 故此豁免是安全的。
		return true
	}
	if strings.Contains(strings.ToUpper(code), "SELECT COUNT(*)") {
		return true
	}
	for _, diagnostic := range []string{"logger.", "fmt.Errorf(", "fmt.Sprintf("} {
		if !strings.Contains(code, diagnostic) {
			continue
		}
		for _, after := range []string{rollupTableName + ", ", rollupTableName + ")", rollupTableName + "\"", rollupTableName + ": "} {
			if strings.Contains(code, after) {
				return true
			}
		}
		if !strings.Contains(code, "rollupTable") {
			return true
		}
	}
	return false
}

// classifyRollupRetirementSource 检测单份源码的复活信号, 并返回逐条证据
// ("文件:行号: 内容"), 便于门禁变红时直接定位 —— 门禁的报错必须能指到行。
//
// path 以 _test.go 结尾时返回零值 (测试里的提及不是生产接线);
// 非 Go 源 (docs/** 含 archive) 不扫: 扫描面只含 backend 的 .go 源码树, 归档文档
// 是历史快照, 改写等于伪造历史 ⇒ 其中的历史提及不得让门禁误红。
// retire_rollup.go 是退役步骤本身, 只允许 DROP/常量/行数留痕/诊断消息。
func classifyRollupRetirementSource(path, content string) (createSemantics, retiredRef bool, evidence []string) {
	base := filepath.Base(path)
	if strings.HasSuffix(base, "_test.go") || !strings.HasSuffix(base, ".go") {
		return false, false, nil
	}
	allowDropOnly := base == rollupRetirementFile
	for i, line := range strings.Split(content, "\n") {
		if isCommentLine(line) {
			continue
		}
		code := codeOf(line)
		if code == "" {
			continue
		}
		where := fmt.Sprintf("%s:%d", path, i+1)
		if isDropLine(code) {
			// 幂等 DROP 是退役动作本身, 不是复活信号。
			continue
		}
		if strings.Contains(code, rollupTableName) {
			switch {
			case isCreateSemanticsLine(code):
				createSemantics = true
				evidence = append(evidence, fmt.Sprintf("建表/AutoMigrate 语义 %s: %s", where, strings.TrimSpace(line)))
			case allowDropOnly && isAllowedRetirementUsage(code):
				// 退役步骤内的合法用法 (常量/行数/消息), 放行。
			default:
				retiredRef = true
				evidence = append(evidence, fmt.Sprintf("退役后仍引用该表 %s: %s", where, strings.TrimSpace(line)))
			}
		}
		for _, sym := range rollupRetiredSymbols {
			if strings.Contains(code, sym) {
				retiredRef = true
				evidence = append(evidence, fmt.Sprintf("引用已退役符号 %q %s: %s", sym, where, strings.TrimSpace(line)))
			}
		}
	}
	return createSemantics, retiredRef, evidence
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

// scanRollupProduction 扫描 backend 的生产 Go 源码 (internal/ + cmd/ + testutil,
// 跳过 _test.go), 返回 (建表语义命中, 退役符号/表名命中, 逐条证据, 扫描文件数)。
//
// 排除项: docs/ 与 archive/ 的历史提及不在扫描面内 (只走 backend 的 Go 源码树,
// 天然不含 docs/); 注释行由 isCommentLine 跳过; _test.go 由后缀跳过。
func scanRollupProduction(t *testing.T) (bool, bool, []string, int) {
	t.Helper()
	root := backendRoot(t)
	var createSemantics, retiredRef bool
	var evidence []string
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
			if d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(filepath.Base(p), "_test.go") {
				return nil
			}
			b, rerr := os.ReadFile(p)
			if rerr != nil {
				return rerr
			}
			scanned++
			cs, rr, ev := classifyRollupRetirementSource(p, string(b))
			createSemantics = createSemantics || cs
			retiredRef = retiredRef || rr
			evidence = append(evidence, ev...)
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", sub, err)
		}
	}
	if scanned == 0 {
		t.Fatal("gate scanner found no production sources — refusing to pass vacuously")
	}
	return createSemantics, retiredRef, evidence, scanned
}

// TestRollupGate_NotResurrected 即"死表不得复活": 表名与退役符号均不得回到生产代码。
func TestRollupGate_NotResurrected(t *testing.T) {
	createSemantics, retiredRef, evidence, scanned := scanRollupProduction(t)
	t.Logf("rollup 退役门禁扫描: %d 个生产 Go 文件 (internal/ + cmd/ + testutil, 跳过 _test.go)", scanned)
	if createSemantics {
		for _, ev := range evidence {
			t.Logf("证据: %s", ev)
		}
		t.Fatalf("死表复活: %s 重新出现在建表/AutoMigrate 语义中 —— "+
			"该表已于 2026-09-15 退役 (零读取者 + EXPLAIN 实测无收益), 不得再注册; "+
			"见 docs/分析/rollup-退役裁决-2026-09-15.md", rollupTableName)
	}
	if retiredRef {
		for _, ev := range evidence {
			t.Logf("证据: %s", ev)
		}
		t.Fatalf("死表复活: 生产代码引用了已退役的表/符号 (%s / %s) —— "+
			"rollup 链路 (RollupConsumer/建表/回调) 已于 2026-09-15 整体退役, "+
			"重新引入必须走新的裁决; 见 docs/分析/rollup-退役裁决-2026-09-15.md",
			rollupTableName, strings.Join(rollupRetiredSymbols, ", "))
	}
}

// extractRegion 取出一个源码区块 (找不到标记即 t.Fatal —— 防止有人改写法让门禁
// 静默失效, vacuous pass)。
func extractRegion(t *testing.T, rel, src, startMarker, endMarker string) string {
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

// TestRollupGate_AutoMigrateListClean 钉死两份模型清单都不含该模型/表名
// (防止有人只改一处)。只看清单区块, 不看全文件 —— 否则 AutoMigrate 尾部对
// RetireLegacyRollup1m 的**退役调用**会被误判 (子串陷阱)。
func TestRollupGate_AutoMigrateListClean(t *testing.T) {
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
		region := extractRegion(t, c.rel, string(raw), c.start, c.end)
		for _, line := range strings.Split(region, "\n") {
			if isCommentLine(line) {
				continue
			}
			if strings.Contains(line, rollupModelType) || strings.Contains(line, rollupTableName) {
				t.Fatalf("%s 的模型清单仍含 rollup: %q (死表不得复活)", c.rel, strings.TrimSpace(line))
			}
		}
	}
}

// TestRollupGate_RetirementStepIsIdempotentDrop 断言退役步骤本身仍然存在, 且是
// 幂等 DROP 并被生产路径调用 (有人"顺手"删掉 ⇒ 存量库的表永久残留而无人再清)。
func TestRollupGate_RetirementStepIsIdempotentDrop(t *testing.T) {
	root := backendRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "database", rollupRetirementFile))
	if err != nil {
		t.Fatalf("退役步骤 %s 缺失 (存量库的表将无人清理): %v", rollupRetirementFile, err)
	}
	src := string(raw)
	if !strings.Contains(src, "DROP TABLE IF EXISTS ") {
		t.Fatal("退役步骤必须使用幂等的 DROP TABLE IF EXISTS")
	}
	if !strings.Contains(src, "func RetireLegacyRollup1m(") {
		t.Fatal("退役步骤必须导出 RetireLegacyRollup1m 供 AutoMigrate 调用")
	}
	gormRaw, err := os.ReadFile(filepath.Join(root, "internal", "database", "gorm.go"))
	if err != nil {
		t.Fatalf("read gorm.go: %v", err)
	}
	// 断言调用点位于 AutoMigrate 函数体内 (尾部), 而不是别处。
	autoMigrateRegion := extractRegion(t, "gorm.go", string(gormRaw), "func AutoMigrate() error {", "\n}\n")
	if !strings.Contains(autoMigrateRegion, "RetireLegacyRollup1m(DB)") {
		t.Fatal("gorm.go 的 AutoMigrate 未调用 RetireLegacyRollup1m —— 退役 DDL 不会被生产路径执行")
	}
}

// TestRollupGate_ClassifierSelfCheck 是**变异重放**: 证明扫描器有能力看见复活信号。
//
// 前半段是**硬编码**的复活行 (退役前真实存在过的写法, 以及本轮从代码里删掉的
// SetRollupSink / EnsureRollupTable / RollupConsumer), 要求逐条判红; 后半段是允许项
// (DROP/常量/诊断消息/注释/测试文件/归档文档), 要求不红。若有人把某个符号从
// rollupRetiredSymbols 里删掉 (把门禁改窄), 前半段用例立刻失败 —— 自检不依赖被检
// 清单自身, 因此不能靠改窄门禁蒙混过关。
func TestRollupGate_ClassifierSelfCheck(t *testing.T) {
	mustRed := []struct {
		name    string
		path    string
		content string
	}{
		{
			name:    "退役前的建表 DDL 原样放回 ⇒ 红",
			path:    "internal/datalifecycle/partition_mgr.go",
			content: "ddl := \"CREATE TABLE unified_data_rollup_1m (device_id BIGINT NOT NULL)\"",
		},
		{
			name:    "main.go 的建表调用点放回 ⇒ 红",
			path:    "cmd/server/main.go",
			content: "if err := datalifecycle.EnsureRollupTable(db); err != nil {",
		},
		{
			name:    "databus 的 rollup 回调接线放回 ⇒ 红",
			path:    "internal/nodemgr/manager.go",
			content: "parser.SetRollupSink(rollup.Upsert)",
		},
		{
			name:    "rollupSink 字段放回 ⇒ 红",
			path:    "internal/databus/consumers_heavy.go",
			content: "rollupSink func([]models.UnifiedData)",
		},
		{
			name:    "RollupConsumer 消费者放回 ⇒ 红",
			path:    "internal/databus/rollup_consumer.go",
			content: "rc := NewRollupConsumer(db)",
		},
		{
			name:    "模型类型放回 ⇒ 红",
			path:    "internal/models/models.go",
			content: "type UnifiedDataRollup1m struct {",
		},
		{
			name:    "把模型注册回 AutoMigrate ⇒ 红",
			path:    "internal/database/gorm.go",
			content: "&models.UnifiedDataRollup1m{},",
		},
		{
			name:    "显式 AutoMigrate 该表名 ⇒ 红",
			path:    "internal/database/gorm.go",
			content: "db.AutoMigrate(\"unified_data_rollup_1m\")",
		},
		{
			name:    "新增查该表的 SELECT (补读取路径) ⇒ 红",
			path:    "internal/api/handler_data.go",
			content: "db.Raw(\"SELECT bucket, last_v FROM unified_data_rollup_1m WHERE device_id = ?\", id)",
		},
		{
			name:    "退役步骤里偷偷建表 ⇒ 红",
			path:    rollupRetirementFile,
			content: "db.Exec(\"CREATE TABLE unified_data_rollup_1m (id bigint)\")",
		},
		{
			name:    "退役步骤的常量里夹带建表 SQL ⇒ 红",
			path:    rollupRetirementFile,
			content: "const evil = \"CREATE TABLE unified_data_rollup_1m (id bigint)\"",
		},
		{
			name:    "退役步骤里把符号放回来 ⇒ 红",
			path:    rollupRetirementFile,
			content: "EnsureRollupTable(db)",
		},
	}
	for _, c := range mustRed {
		cs, rr, ev := classifyRollupRetirementSource(c.path, c.content)
		if !cs && !rr {
			t.Errorf("%s: classifyRollupRetirementSource(%s) = (createSemantics=false retiredRef=false) want 红; 证据=%v",
				c.name, c.path, ev)
		}
	}

	mustGreen := []struct {
		name    string
		path    string
		content string
	}{
		{
			name:    "退役步骤的 DROP 语句 ⇒ 不红",
			path:    rollupRetirementFile,
			content: "if err := db.Exec(\"DROP TABLE IF EXISTS \" + rollupTable).Error; err != nil {",
		},
		{
			name:    "退役步骤的行数留痕 ⇒ 不红",
			path:    rollupRetirementFile,
			content: "db.Raw(\"SELECT count(*) FROM \" + rollupTable).Scan(&rows)",
		},
		{
			name:    "退役步骤的表名常量声明 ⇒ 不红",
			path:    rollupRetirementFile,
			content: "const rollupTable = \"unified_data_rollup_1m\"",
		},
		{
			name:    "退役步骤的诊断消息 ⇒ 不红",
			path:    rollupRetirementFile,
			content: "logger.Warnf(\"retire unified_data_rollup_1m: dropped (rows=%d)\", rows)",
		},
		{
			name:    "AutoMigrate 尾部的退役调用 (子串陷阱) ⇒ 不红",
			path:    "internal/database/gorm.go",
			content: "_, err = RetireLegacyRollup1m(DB)",
		},
		{
			name:    "partition_mgr 的退役说明注释 ⇒ 不红",
			path:    "internal/datalifecycle/partition_mgr.go",
			content: "// 注: EnsureRollupTable / RollupConsumer 已于 2026-09-15 随表退役删除",
		},
		{
			name:    "注释掉的接线不是接线 ⇒ 不红",
			path:    "internal/api/handler_data.go",
			content: "// db.Raw(\"SELECT * FROM unified_data_rollup_1m\")",
		},
		{
			name:    "行尾注释里的符号只是文字 (codeOf 剥掉行尾注释后无代码引用) ⇒ 不红",
			path:    "internal/databus/consumers_heavy.go",
			content: "x := 1 // rollupSink",
		},
		{
			name:    "测试文件里的提及不是生产接线 ⇒ 不红",
			path:    "internal/database/retire_rollup_test.go",
			content: "const legacyRollupDDL = \"CREATE TABLE unified_data_rollup_1m (id BIGINT)\"",
		},
		{
			name:    "归档文档里的历史提及 (docs/archive, 非 Go 源) ⇒ 不红",
			path:    "docs/archive/v4.0-重建前归档/设计/架构优化实施方案.md",
			content: "CREATE TABLE unified_data_rollup_1m (",
		},
		{
			name:    "分析文档里的历史提及 (docs/分析, 非 Go 源) ⇒ 不红",
			path:    "docs/分析/rollup-退役裁决-2026-09-15.md",
			content: "| rollup | EnsureRollupTable | rollup 表存在性检查或建表失败 |",
		},
	}
	for _, c := range mustGreen {
		cs, rr, ev := classifyRollupRetirementSource(c.path, c.content)
		if cs || rr {
			t.Errorf("%s: classifyRollupRetirementSource(%s) = (createSemantics=%v retiredRef=%v) want 绿; 证据=%v",
				c.name, c.path, cs, rr, ev)
		}
	}
}
