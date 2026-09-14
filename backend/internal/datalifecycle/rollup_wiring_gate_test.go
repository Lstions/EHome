package datalifecycle

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// =====================================================================
// rollup 接线门禁 (INV-7 修正版, 条件式)。
//
// 背景 (docs/分析/rollup-读取路径裁决-2026-09-14.md §5): 2026-09-14 裁决为
// **停写 + 删读取侧孤儿 precisionFor**。rollup 的写入材料 (RollupConsumer /
// EnsureRollupTable / 模型 / 已写入积压) 保留为**冻结件**, 作为"补读取路径还是
// 退役"的限期决策输入。
//
// INV-7 (修正版) 是**条件式**的:
//
//	若且仅若 生产代码中存在对 unified_data_rollup_1m 的消费者
//	(或有人把 rollup sink 接回去), 则必须同时存在保留期配置;
//	两者都没有时不红 (不约束一个不存在的功能 —— 原版 INV-7 恰在此处"保护空气")。
//
// 本条门禁是回归锁: 任何人把 SetRollupSink 接回去 (或新增查 rollup 的端点),
// 却没有同步配置保留期 ⇒ 立刻红 —— 精确地防止"只写不读的半成品"再次出现。
//
// 范围声明 (诚实边界): 本门禁是**源码扫描**, 只断言"保留期配置存在";
// "窗口 ≥ MAX(logical_devices.retention_days)" 与"结果被截断时显式提示"两条
// 需要运行时配置, 不在本扫描能力内 —— 读取路径真正落地时必须在实现里补这两条断言。
// =====================================================================

// rollupWiringState 是 rollup 链路在生产 (非测试) Go 源码中的接线状态。
type rollupWiringState struct {
	// WriteWired: 生产代码注入了 rollup sink (即已恢复写入)。
	WriteWired bool
	// ReadConsumer: 生产代码对 rollup 表发起了 SELECT (即存在真实消费者)。
	ReadConsumer bool
	// Retention: 存在 rollup 保留期配置 (清理窗口/清理器)。
	Retention bool
}

// rollupRetentionMarkers 是"保留期已配置"的源码标记。当前全仓无一处命中
// (实测), 这正是停写状态应有的样子。
var rollupRetentionMarkers = []string{
	"rollup_retention",
	"RollupRetention",
	"rollupRetention",
	"RollupCleanup",
	"rollupCleanup",
	"rollup_cutoff",
	"rollupCutoff",
}

// isCommentLine reports whether line is a comment (a commented-out wiring is
// not wiring).
func isCommentLine(line string) bool {
	t := strings.TrimSpace(line)
	return strings.HasPrefix(t, "//") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "/*")
}

// isRollupSelectLine reports whether line is a production SELECT against the
// rollup table. The UPSERT path references the table name too
// (INSERT ... ON CONFLICT ... SET unified_data_rollup_1m.min_v = ...), so lines
// belonging to a write statement are excluded — otherwise the frozen writer
// would masquerade as a consumer forever.
func isRollupSelectLine(line string) bool {
	if isCommentLine(line) || !strings.Contains(line, "unified_data_rollup_1m") {
		return false
	}
	up := strings.ToUpper(line)
	if !strings.Contains(up, "SELECT") {
		return false
	}
	for _, write := range []string{"INSERT", "ON CONFLICT", "EXCLUDED", "UPDATE "} {
		if strings.Contains(up, write) {
			return false
		}
	}
	return true
}

// classifyRollupSource 检测单份源码的接线信号。path 以 _test.go 结尾时返回
// 零值: 测试里的接线 (如 rollup_consumer_test.go 的 SetRollupSink) 不是生产接线。
func classifyRollupSource(path, content string) rollupWiringState {
	var st rollupWiringState
	if strings.HasSuffix(filepath.Base(path), "_test.go") {
		return st
	}
	for _, line := range strings.Split(content, "\n") {
		if isCommentLine(line) {
			continue
		}
		if strings.Contains(line, ".SetRollupSink(") {
			st.WriteWired = true
		}
		if isRollupSelectLine(line) {
			st.ReadConsumer = true
		}
		for _, m := range rollupRetentionMarkers {
			if strings.Contains(line, m) {
				st.Retention = true
			}
		}
	}
	return st
}

// merge 把单文件状态并入总体状态 (只增不减)。
func (st *rollupWiringState) merge(part rollupWiringState) {
	st.WriteWired = st.WriteWired || part.WriteWired
	st.ReadConsumer = st.ReadConsumer || part.ReadConsumer
	st.Retention = st.Retention || part.Retention
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

// scanRollupWiring 扫描 backend 的生产源码 (internal/ + cmd/, 跳过 _test.go)。
func scanRollupWiring(t *testing.T) rollupWiringState {
	t.Helper()
	root := backendRoot(t)
	var st rollupWiringState
	scanned := 0
	for _, sub := range []string{"internal", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, sub), func(p string, d fs.DirEntry, err error) error {
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
			st.merge(classifyRollupSource(p, string(b)))
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s: %v", sub, err)
		}
	}
	if scanned == 0 {
		t.Fatal("gate scanner found no production sources — refusing to pass vacuously")
	}
	return st
}

// TestRollupWiringGate_ConditionalRetentionObligation 即 INV-7 (修正版):
// 有消费者 (或写入已接线) ⇒ 必须有保留期配置; 二者皆无 ⇒ 不红。
func TestRollupWiringGate_ConditionalRetentionObligation(t *testing.T) {
	st := scanRollupWiring(t)

	if !st.WriteWired && !st.ReadConsumer {
		// 2026-09-14 停写后的状态: 无消费者 ⇒ 条件式不变式不生效。
		// 这不是"缺陷", 而是裁决本身 (rollup 为冻结件)。
		t.Logf("rollup 无消费者 (write=%v read=%v retention=%v): 条件式 INV-7 不生效; "+
			"详见 docs/分析/rollup-读取路径裁决-2026-09-14.md", st.WriteWired, st.ReadConsumer, st.Retention)
		return
	}

	if !st.Retention {
		t.Fatalf("INV-7 (修正版) 违反: rollup 已接线 (write=%v read=%v) 但全仓无保留期配置。 "+
			"接线读取路径必须同步配置 rollup 保留期 (窗口 ≥ MAX(logical_devices.retention_days), "+
			"且结果被截断时显式提示) —— 见 docs/分析/rollup-读取路径裁决-2026-09-14.md §5",
			st.WriteWired, st.ReadConsumer)
	}
}

// TestRollupWiringGate_ClassifierSelfCheck 证明扫描器**有能力**看见三种信号。
// 没有这条自检, 一个坏掉的扫描器会让门禁永远"绿" (vacuous pass)。
func TestRollupWiringGate_ClassifierSelfCheck(t *testing.T) {
	cases := []struct {
		name    string
		path    string
		content string
		want    rollupWiringState
	}{
		{
			name:    "production sink injection is write wiring",
			path:    "internal/nodemgr/manager.go",
			content: "\tparser.SetRollupSink(rollup.Upsert)\n",
			want:    rollupWiringState{WriteWired: true},
		},
		{
			name:    "test sink injection is not production wiring",
			path:    "internal/databus/rollup_consumer_test.go",
			content: "\tconsumer.SetRollupSink(func(records []models.UnifiedData) {})\n",
			want:    rollupWiringState{},
		},
		{
			name:    "production select on rollup table is a consumer",
			path:    "internal/api/handler_data.go",
			content: "\tdb.Raw(\"SELECT bucket, last_v FROM unified_data_rollup_1m WHERE device_id = ?\", id)\n",
			want:    rollupWiringState{ReadConsumer: true},
		},
		{
			name:    "frozen upsert must not masquerade as a consumer",
			path:    "internal/databus/rollup_consumer.go",
			content: "\t\t\t   min_v  = LEAST(unified_data_rollup_1m.min_v, EXCLUDED.min_v),\n",
			want:    rollupWiringState{},
		},
		{
			name:    "commented-out wiring is not wiring",
			path:    "internal/api/handler_data.go",
			content: "\t// db.Raw(\"SELECT * FROM unified_data_rollup_1m\")\n",
			want:    rollupWiringState{},
		},
		{
			name:    "retention config is detected",
			path:    "internal/datalifecycle/rollup_retention.go",
			content: "\tconst rollup_retention = 90 * 24 * time.Hour\n",
			want:    rollupWiringState{Retention: true},
		},
	}
	for _, c := range cases {
		if got := classifyRollupSource(c.path, c.content); got != c.want {
			t.Errorf("%s: classifyRollupSource(%s)=%+v want %+v", c.name, c.path, got, c.want)
		}
	}
}
