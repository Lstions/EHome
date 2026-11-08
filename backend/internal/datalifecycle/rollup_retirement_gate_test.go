package datalifecycle

// =====================================================================
// 门禁同步 (方案 §5.4): 「死表不得复活」+「新表不得被误伤」。
//
// 历史: 本文件原版 (2026-09-15, commit 2085a548) 随 unified_data_rollup_1m 退役
// 落地, 2026-10-24 的 bb19ee30 (移除逻辑设备概念层, 490 行) 被整文件删除 ——
// 门禁一旦消失, 死表复活就没有任何拦截点。L3 (2026-11-06) 把它按新形态重建,
// 且额外钉死新表 telemetry_rollup_1m 的边界。
//
// 为什么必须有「两条命不同的表」这条门禁: 旧表 unified_data_rollup_1m 与
// 新表 telemetry_rollup_1m 名字同构(都以 _rollup_1m 结尾), 而退役步骤是
// 「启动时无条件 DROP」。任何一次手滑(把新表名加进退役文件 / 把 DROP 写成
// LIKE 'unified_data_rollup_1m%') 都会让新表在下次启动时静默消失 —— 而且
// 表现在是空的、没有消费者在跑, 所以**没有任何运行时现象**会暴露它。
// 只有源码门禁能拦。
//
// 本门禁断言(扫描面 = backend/{internal,cmd,testutil} 的**非 _test.go** Go 源码):
//
//   (1) 已退役表名 unified_data_rollup_1m 不出现在任何建表/AutoMigrate/SELECT/
//       INSERT 语义里 (DROP 除外);
//   (2) 已退役符号 (EnsureRollupTable / SetRollupSink / rollupSink /
//       RollupConsumer / UnifiedDataRollup1m) 零生产引用;
//   (3) 退役步骤本身仍在 (internal/database/retire_rollup.go), 幂等 DROP, 且被
//       AutoMigrate 生产路径调用;
//   (4) 【新】退役文件里**不得出现**新表名 telemetry_rollup_1m 的可执行代码
//       (注释里的说明性提及允许 —— 那段注释正是给下一个改动者看的);
//   (5) 【新】退役 DROP 不得使用通配 (LIKE / %); 通配会连带删掉新表与其月分区;
//   (6) 【新】新表必须真的**在**建表清单里 (partitionParentDDL /
//       partitionParentIndexes), 否则"没误伤"是空的 —— 一张不存在的表谈不上误伤。
//
// 范围声明 (诚实边界): 本门禁是**源码扫描**。"表在运行库中确实不存在/确实存在"
// 需要真实方言, 由 database/retire_rollup_test.go (SQLite 与 PG 隔离 schema) 与
// datalifecycle/partition_mgr_pg_test.go 覆盖, 不在本扫描能力内。
//
// 扫描器自检 (TestRollupGate_ClassifierSelfCheck) 是**变异重放**: 把历史上真实
// 出现过的复活写法喂给扫描器, 要求逐条判红; 用例硬编码, 不依赖被检清单自身。
// 没有这条自检, 一个坏掉的扫描器会让门禁永远"绿" (vacuous pass) —— 本仓已有此教训
// (见 cmd/server/notify_wiring_gate_test.go 的同款注记)。
// =====================================================================

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const (
	// rollupGateRetirementFile 是幂等 DROP 步骤所在文件 (basename)。
	rollupGateRetirementFile = "retire_rollup.go"

	// rollupGateLegacyTable 已退役表名。
	rollupGateLegacyTable = "unified_data_rollup_1m"

	// rollupGateLiveTable 是 L3 新表名 (方案 §5.2)。它是**活的**表, 与旧表不是
	// 同一条命: 退役步骤必须碰不到它。
	rollupGateLiveTable = "telemetry_rollup_1m"

	// rollupGateModelType 已退役的模型类型名。
	rollupGateModelType = "UnifiedDataRollup1m"
)

// rollupGateRetiredSymbols 是随旧表退役必须消失的生产符号。
//
// ⚠ 不要往里加 RollupCollector / TelemetryRollup1m —— 那是 L3 新表的符号,
// 加进来会让门禁把活代码判红 (假阳性会逼人加豁免, 豁免即后门)。
var rollupGateRetiredSymbols = []string{
	"EnsureRollupTable",
	"SetRollupSink",
	"rollupSink",
	"RollupConsumer",
	rollupGateModelType,
}

// rollupGateIsComment 报告 line 是否为注释行。
func rollupGateIsComment(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*")
}

// rollupGateCode 剥掉注释, 返回可执行代码部分。
func rollupGateCode(line string) string {
	t := strings.TrimSpace(line)
	if rollupGateIsComment(t) {
		return ""
	}
	if i := strings.Index(t, "//"); i >= 0 {
		t = strings.TrimSpace(t[:i])
	}
	return t
}

// rollupGateIsDrop 报告代码是否为幂等退役 DROP。
func rollupGateIsDrop(code string) bool {
	return strings.Contains(strings.ToUpper(code), "DROP TABLE")
}

// rollupGateIsExecLine 报告该行是否真的在执行 SQL (而不是拼错误消息)。
// 通配判据必须只落在执行行上: `fmt.Errorf("... drop table: %w", err)` 里的 %w
// 会被裸 `strings.Contains(code, "%")` 误判成通配 DROP (实测踩到)。
func rollupGateIsExecLine(code string) bool {
	return strings.Contains(code, "db.Exec(") || strings.Contains(code, "tx.Exec(") ||
		strings.Contains(code, ".Exec(fmt.Sprintf(") || strings.Contains(code, "Exec(")
}

// rollupGateHasCreateSemantics 报告代码是否带建表/AutoMigrate 语义。
func rollupGateHasCreateSemantics(code string) bool {
	up := strings.ToUpper(code)
	for _, marker := range []string{"AUTOMIGRATE", "CREATE TABLE", "PARTITION OF"} {
		if strings.Contains(up, marker) {
			return true
		}
	}
	return false
}

// rollupGateIsAllowedLegacyUsage 报告退役文件里对**旧表名**的合法用法:
// 常量定义 / 行数留痕 / 诊断消息。
func rollupGateIsAllowedLegacyUsage(code string) bool {
	if strings.HasPrefix(code, "const ") {
		return true
	}
	if strings.Contains(strings.ToUpper(code), "SELECT COUNT(*)") {
		return true
	}
	for _, diagnostic := range []string{"logger.", "fmt.Errorf(", "fmt.Sprintf("} {
		if strings.Contains(code, diagnostic) {
			return true
		}
	}
	return false
}

// rollupGateFindings 是一份源码里发现的全部门禁问题 (空 = 通过)。
type rollupGateFindings struct {
	// legacyResurrection 旧表复活 (建表/AutoMigrate/SELECT/INSERT 语义)。
	legacyResurrection []string
	// retiredSymbol 引用了已退役符号。
	retiredSymbol []string
	// liveTableTouched 退役文件里对新表的可执行用法 (误伤路径)。
	liveTableTouched []string
	// wildcardDrop 退役 DROP 用了通配。
	wildcardDrop []string
}

// Classification 汇总 (供主门禁与自检共用)。
type rollupGateClassification struct {
	legacyResurrection bool
	retiredSymbol      bool
	liveTableTouched   bool
	wildcardDrop       bool
	findings           rollupGateFindings
}

// rollupGateClassify 检测单份源码的门禁违规 (含逐条证据)。
//
// path 以 _test.go 结尾时返回零值 (测试里的提及不是生产接线)。
func rollupGateClassify(path, content string) rollupGateClassification {
	var out rollupGateClassification
	base := filepath.Base(path)
	if strings.HasSuffix(base, "_test.go") {
		return out
	}
	inRetirementFile := base == rollupGateRetirementFile
	for i, line := range strings.Split(content, "\n") {
		where := fmt.Sprintf("%s:%d", path, i+1)
		code := rollupGateCode(line)
		if code == "" {
			continue // 纯注释: 说明性/历史性提及不算复活
		}
		trimmed := strings.TrimSpace(line)

		// (1) 旧表复活语义 (DROP 是退役动作本身, 不算)。
		// 模型类型引用 models.UnifiedDataRollup1m 也是复活的一种形态
		// (&models.X{} 回到 AutoMigrate 列表即重建该表), 单独判一次。
		if strings.Contains(code, rollupGateModelType) {
			out.legacyResurrection = true
			out.findings.legacyResurrection = append(out.findings.legacyResurrection, where+": "+trimmed)
		}
		if strings.Contains(code, rollupGateLegacyTable) && !rollupGateIsDrop(code) && !inRetirementFile &&
			rollupGateHasCreateSemantics(code) {
			out.legacyResurrection = true
			out.findings.legacyResurrection = append(out.findings.legacyResurrection, where+": "+trimmed)
		}
		// 退役文件里除"合法退役用法"外的旧表名用法一律视为复活隐患。
		if inRetirementFile && strings.Contains(code, rollupGateLegacyTable) &&
			!rollupGateIsDrop(code) && !rollupGateIsAllowedLegacyUsage(code) {
			out.legacyResurrection = true
			out.findings.legacyResurrection = append(out.findings.legacyResurrection, where+": "+trimmed)
		}

		// (2) 已退役符号。
		for _, sym := range rollupGateRetiredSymbols {
			if strings.Contains(code, sym) {
				out.retiredSymbol = true
				out.findings.retiredSymbol = append(out.findings.retiredSymbol,
					fmt.Sprintf("%s: %s (符号 %s)", where, trimmed, sym))
				break
			}
		}

		// (4) 退役文件里对新表的可执行用法 (误伤路径)。
		if inRetirementFile && strings.Contains(code, rollupGateLiveTable) {
			out.liveTableTouched = true
			out.findings.liveTableTouched = append(out.findings.liveTableTouched, where+": "+trimmed)
		}

		// (5) 通配 DROP。
		//
		// 判据刻意只落在**退役文件**里, 且要求 DROP TABLE 与通配出现在同一行:
		//   · 诊断消息里的 "drop table %s" (fmt.Errorf) 不是 DROP 语句, 不能判红;
		//   · partition_mgr 的 `DROP TABLE IF EXISTS %s` (表名由变量注入) 是分区分支的
		//     正常形态, 也不在本门禁的扫描对象里 —— 它删的是分区名, 由 retention 的
		//     cutoff 语义决定, 不是"退役步骤"。
		// 本门禁只关心一件事: 退役步骤不得用通配表名删表 (那会把 telemetry_rollup_1m
		// 与 telemetry_rollup_1m_YYYYMM 一并带走)。
		// 诊断消息 (fmt.Errorf / logger) 里的 "drop table %w" 不是 SQL, 已由上面的
		// 豁免排除; 这里再要求该行是一次**执行** (db.Exec/Exec), 双重保险。
		if inRetirementFile && rollupGateIsDrop(code) && rollupGateIsExecLine(code) &&
			(strings.Contains(code, "%") || strings.Contains(strings.ToUpper(code), "LIKE")) {
			out.wildcardDrop = true
			out.findings.wildcardDrop = append(out.findings.wildcardDrop, where+": "+trimmed)
		}
	}
	return out
}

// rollupGateBackendRoot 从工作目录向上定位 backend/ (以 go.mod 为锚)。
func rollupGateBackendRoot(t *testing.T) string {
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

// scanRollupGate 扫描 backend 的生产 Go 源码, 返回合并后的分类与扫描文件数。
func scanRollupGate(t *testing.T) (rollupGateClassification, int) {
	t.Helper()
	root := rollupGateBackendRoot(t)
	var merged rollupGateClassification
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
			got := rollupGateClassify(p, string(b))
			merged.legacyResurrection = merged.legacyResurrection || got.legacyResurrection
			merged.retiredSymbol = merged.retiredSymbol || got.retiredSymbol
			merged.liveTableTouched = merged.liveTableTouched || got.liveTableTouched
			merged.wildcardDrop = merged.wildcardDrop || got.wildcardDrop
			merged.findings.legacyResurrection = append(merged.findings.legacyResurrection, got.findings.legacyResurrection...)
			merged.findings.retiredSymbol = append(merged.findings.retiredSymbol, got.findings.retiredSymbol...)
			merged.findings.liveTableTouched = append(merged.findings.liveTableTouched, got.findings.liveTableTouched...)
			merged.findings.wildcardDrop = append(merged.findings.wildcardDrop, got.findings.wildcardDrop...)
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", sub, err)
		}
	}
	if scanned == 0 {
		t.Fatal("门禁扫描器一个生产文件都没扫到 —— 拒绝空转通过 (vacuous pass)")
	}
	return merged, scanned
}

// TestRollupGate_LegacyTableNotResurrected 是旧表的核心门禁 (1)(2)。
func TestRollupGate_LegacyTableNotResurrected(t *testing.T) {
	got, scanned := scanRollupGate(t)
	t.Logf("rollup 门禁扫描: %d 个生产 Go 文件 (internal/ + cmd/ + testutil, 跳过 _test.go)", scanned)
	if got.legacyResurrection {
		for _, ev := range got.findings.legacyResurrection {
			t.Logf("证据: %s", ev)
		}
		t.Fatalf("%s 已于 2026-09-15 退役 (写了没人读 + 读了不划算), 不得复活; "+
			"L3 的正确载体是 %s (宽行 + 有消费者 + 有保留期); "+
			"裁决见 docs/分析/rollup-退役裁决-2026-09-15.md", rollupGateLegacyTable, rollupGateLiveTable)
	}
	if got.retiredSymbol {
		for _, ev := range got.findings.retiredSymbol {
			t.Logf("证据: %s", ev)
		}
		t.Fatalf("生产代码引用了已退役符号 (%s) —— 退役件的写入/建表机制不得加回",
			strings.Join(rollupGateRetiredSymbols, ", "))
	}
}

// TestRollupGate_RetirementNeverTouchesLiveTable 是 (4)(5): 退役步骤不得误伤新表。
func TestRollupGate_RetirementNeverTouchesLiveTable(t *testing.T) {
	got, _ := scanRollupGate(t)
	if got.liveTableTouched {
		for _, ev := range got.findings.liveTableTouched {
			t.Logf("证据: %s", ev)
		}
		t.Fatalf("退役步骤 %s 里出现新表 %s 的**可执行**用法 —— 方案 §5.4 明令只退役旧表; "+
			"新表是 L3 的活表, 退役 DROP 会把它连同月分区一起删掉",
			rollupGateRetirementFile, rollupGateLiveTable)
	}
	if got.wildcardDrop {
		for _, ev := range got.findings.wildcardDrop {
			t.Logf("证据: %s", ev)
		}
		t.Fatal("退役 DROP 使用通配 (LIKE / %) —— 会连带删除 telemetry_rollup_1m 与其月分区")
	}
}

// TestRollupGate_RetirementStepIsIdempotentDrop 是 (3): 退役步骤仍在且幂等。
func TestRollupGate_RetirementStepIsIdempotentDrop(t *testing.T) {
	root := rollupGateBackendRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "database", rollupGateRetirementFile))
	if err != nil {
		t.Fatalf("退役步骤 %s 缺失 (存量库的旧表将无人清理): %v", rollupGateRetirementFile, err)
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
	if !strings.Contains(string(gormRaw), "RetireLegacyRollup1m(DB)") {
		t.Fatal("gorm.go 的 AutoMigrate 未调用 RetireLegacyRollup1m —— 退役 DDL 不会被生产路径执行")
	}
}

// TestRollupGate_LiveTableIsActuallyRegistered 是 (6): 「没误伤」的前提是表真的存在。
//
// 这条同时防止另一种假绿: 有人把新表从 partitionParentDDL 里删掉 (活表变成不存在的
// 表), 上面几条"没误伤"断言依然全绿 —— 因为没什么可误伤。
func TestRollupGate_LiveTableIsActuallyRegistered(t *testing.T) {
	ddl, ok := partitionParentDDL[rollupGateLiveTable]
	if !ok {
		t.Fatalf("partitionParentDDL 未登记 %q —— 活表根本没有建表路径", rollupGateLiveTable)
	}
	if !strings.Contains(ddl, "PARTITION BY RANGE") {
		t.Errorf("%q 未按分区表建 (%q 里没有 PARTITION BY RANGE)", rollupGateLiveTable, "partitionParentDDL")
	}
	if _, ok := partitionParentIndexes[rollupGateLiveTable]; !ok {
		t.Errorf("partitionParentIndexes 未登记 %q —— 会回退到 unified_data 的索引清单并引用不存在的列", rollupGateLiveTable)
	}
	// 旧表名不得出现在建表清单里 (它就是死表)。
	if _, ok := partitionParentDDL[rollupGateLegacyTable]; ok {
		t.Errorf("partitionParentDDL 登记了已退役的 %q —— 死表被重新纳入建表路径", rollupGateLegacyTable)
	}
}

// TestRollupGate_ClassifierSelfCheck 证明扫描器**有能力**看见复活/误伤信号。
func TestRollupGate_ClassifierSelfCheck(t *testing.T) {
	cases := []struct {
		name         string
		path         string
		content      string
		wantLegacy   bool
		wantSymbol   bool
		wantLive     bool
		wantWildcard bool
	}{
		{
			name:       "把旧表加回 AutoMigrate 列表 ⇒ 红",
			path:       "internal/database/gorm.go",
			content:    "\t\t&models.UnifiedDataRollup1m{},\n",
			wantLegacy: true,
			wantSymbol: true,
		},
		{
			name:       "裸建表 SQL 旧表名 ⇒ 红",
			path:       "internal/database/gorm.go",
			content:    "\tdb.Exec(\"CREATE TABLE unified_data_rollup_1m (id bigint)\")\n",
			wantLegacy: true,
		},
		{
			name:       "重新接线 EnsureRollupTable ⇒ 红",
			path:       "cmd/server/main.go",
			content:    "\tif err := datalifecycle.EnsureRollupTable(db); err != nil {\n",
			wantSymbol: true,
		},
		{
			name:       "重新接线 SetRollupSink ⇒ 红",
			path:       "cmd/server/main.go",
			content:    "\tconsumer.SetRollupSink(sink)\n",
			wantSymbol: true,
		},
		{
			name:       "退役文件里 DROP 旧表 ⇒ 不红",
			path:       rollupGateRetirementFile,
			content:    "\tif err := db.Exec(\"DROP TABLE IF EXISTS \" + rollupTable).Error; err != nil {\n",
			wantLive:   false,
			wantLegacy: false,
		},
		{
			name:       "退役文件里行数留痕 ⇒ 不红",
			path:       rollupGateRetirementFile,
			content:    "\tdb.Raw(\"SELECT count(*) FROM \" + rollupTable).Scan(&rows)\n",
			wantLegacy: false,
		},
		{
			name:     "退役文件里出现新表的 DROP ⇒ 红 (误伤活表)",
			path:     rollupGateRetirementFile,
			content:  "\tdb.Exec(\"DROP TABLE IF EXISTS telemetry_rollup_1m\")\n",
			wantLive: true,
		},
		{
			name:         "退役 DROP 用通配 ⇒ 红",
			path:         rollupGateRetirementFile,
			content:      "\tdb.Exec(\"DROP TABLE IF EXISTS unified_data_rollup_1m%\")\n",
			wantWildcard: true,
		},
		{
			name:     "退役文件里注释提到新表 ⇒ 不红 (说明性提及)",
			path:     rollupGateRetirementFile,
			content:  "// 新表 telemetry_rollup_1m 不归本文件管\n",
			wantLive: false,
		},
		{
			name:       "注释掉的注册不是注册 ⇒ 不红",
			path:       "internal/database/gorm.go",
			content:    "\t\t// &models.UnifiedDataRollup1m{},  // 已退役\n",
			wantLegacy: false,
		},
		{
			// 这两条不是"随便加的反例"，而是**给接线者看的命名契约**：
			// L3 的写入侧 sink 必须用 TelemetryRollup 作词根（SetTelemetryRollupSink /
			// telemetryRollupSink），因为退役符号黑名单是**连续子串**匹配：旧名
			// SetRollupSink / rollupSink 会直接判红，带 Telemetry 前缀的不会。
			// 没有这条自检，下一个人照接线 patch 文档里的旧名字接线就会被门禁拦下，
			// 然后他会去"改门禁"而不是改名字 —— 那才是真危险。
			name: "L3 写入 sink 用 TelemetryRollup 词根 ⇒ 不红（接线者的命名契约）",
			path: "internal/databus/consumers_heavy.go",
			content: "\tc.telemetryRollupSink(device.ID, sensorData, now)\n" +
				"\tconsumer.SetTelemetryRollupSink(sink)\n",
		},
		{
			name:       "L3 写入 sink 误用旧名 SetRollupSink ⇒ 红（撞退役符号）",
			path:       "internal/databus/consumers_heavy.go",
			content:    "\tconsumer.SetRollupSink(sink)\n",
			wantSymbol: true,
		},
		{
			name:       "测试文件里的提及不是生产接线 ⇒ 不红",
			path:       "internal/database/retire_rollup_test.go",
			content:    "\tif db.Migrator().HasTable(\"unified_data_rollup_1m\") {\n",
			wantLegacy: false,
		},
	}
	for _, c := range cases {
		got := rollupGateClassify(c.path, c.content)
		if got.legacyResurrection != c.wantLegacy || got.retiredSymbol != c.wantSymbol ||
			got.liveTableTouched != c.wantLive || got.wildcardDrop != c.wantWildcard {
			t.Errorf("%s: classify=%+v want{legacy:%v symbol:%v live:%v wildcard:%v}",
				c.name, map[string]bool{
					"legacy": got.legacyResurrection, "symbol": got.retiredSymbol,
					"live": got.liveTableTouched, "wildcard": got.wildcardDrop,
				}, c.wantLegacy, c.wantSymbol, c.wantLive, c.wantWildcard)
		}
	}
}
