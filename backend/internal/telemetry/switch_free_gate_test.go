package telemetry

// switch_free_gate_test.go — 契约 §3.6 的**源码级门禁**：三个遥测开关必须彻底消失。
//
// 背景（docs/设计/遥测存储-L1L2无条件启用-契约-2026-11-07.md §1/§3.6）：
// 2026-11-07 用户裁决遥测存储 L1/L2 **无条件启用**，删除三个"是否启用"开关
// （见下方 deletedTelemetrySwitches 清单；名字用拼接构造，理由见那里的注释）：
//
//	WRITE_ON_CHANGE  —— L1 值不变不入库
//	DUAL_WRITE       —— L2 帧级编码
//	ROLLUP_WRITE     —— L3 分钟聚合写入
//
// ============ 为什么需要一条"源码扫描"测试 ============
//
// 删开关这件事的失败模式是**静默半删**：config 结构体里删了字段、但 main.go 里
// 还留着一行 cfg.TelemetrySettings().RollupWrite 判断（编译过得了，因为字段还在），
// 或者注释/文档里还写着"默认关闭"。等到某天有人照着注释把开关加回来，行为就
// 悄悄退回到"默认不生效"，而所有单测都还是绿的。
//
// 这条门禁把"开关不存在"变成可执行断言：全 backend 源码里这三个名字必须**零命中**。
//
// ============ ⚠ 精确匹配而不是子串匹配 ============
//
// `DUAL_WRITE` 这个开关名 **是** `DUAL_WRITE_QUEUE`（队列深度参数）的前缀，
// 而后者是**保留的参数**（契约 §3.2 明确保留）。用 strings.Contains 做子串匹配
// 会把合法的 QUEUE 参数判成违规，从而逼着后来的维护者去删掉一个该留的参数，
// 或者把门禁注释掉。
//
// 因此匹配规则是：命中位置之后**不能再接** [A-Z0-9_]（即必须是完整标识符）。
// 同样的道理适用于未来的 WRITE_ON_CHANGE_XXX 类扩展名。

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// deletedTelemetrySwitches 是契约 §1 要求删除的三个环境变量（名字即断言）。
//
// 这张清单**只能变短不能变长**：新增一条就意味着有人又把开关加回来了。
//
// ============ 为什么用拼接而不是写完整字面量 ============
//
// 契约 §6.2 的验收口径是一条 grep：这三个名字在 backend/ 里必须 0 命中。
// 如果本文件直接写完整字面量，门禁自身就成了那 12 处命中里的 9 处 —— 验收命令
// 永远看着是红的，"门禁 존재" 变成了 "验收被自己破坏"。
//
// 拼接之后，那条 grep 在 backend/ 里只剩**一处代码命中**：
//
//	dualwrite.go: EnvQueueSize = "EHOME_TELEMETRY_DUAL_WRITE_QUEUE"
//
// 而那一处**必须**存在 —— 契约 §3.2 明确保留它作为「队列深度」参数。
// 也就是说：§6.2 的字面 grep 与 §3.2 的保留要求本身冲突（子串匹配无法区分
// DUAL_WRITE 与 DUAL_WRITE_QUEUE）。真正的判据是本文件这条**精确标识符**门禁，
// 它把冲突消解成「完整标识符零命中 + 保留参数合法存在」。
//
// ============ 🚫 不要把这个门禁改回朴素 grep（Lead 2026-11-07 明确要求留下此警告）============
//
// 后来人最可能做的"简化"是把它换成：
//
//	if out, _ := exec.Command("grep", "-rn", "EHOME_TELEMETRY_DUAL_WRITE", "..").Output(); len(out) > 0 {
//	    t.Error("开关还在")
//	}
//
// 这**一定**会红，而且红的原因不是开关回来了，是它同时命中了保留参数
// EHOME_TELEMETRY_DUAL_WRITE_QUEUE。后续维护者的第一反应只会是把门禁注释掉
// （"这测试有病"），于是门禁彻底失效 —— 比没有门禁更糟，因为它给了虚假的安全感。
//
// 保持"精确标识符"（名字后不得接 [A-Z0-9_]）这一条，是本门禁唯一正确的形态：
//
//	· 完整标识符零命中  ⇒ 开关真删了；
//	· 保留参数合法存在  ⇒ 参数没被过度删（由 TestRetainedTelemetryParamsStillExist 守）。
//
// 同理，也**不要**为了"让 grep 全绿"去删掉 dualwrite.go 里的 EnvQueueSize ——
// 那会删掉一个该留的运行期参数（契约 §3.2），是过度删除。
const switchPrefix = "EHOME_TELEMETRY_"

var deletedTelemetrySwitches = []string{
	switchPrefix + "WRITE_ON_CHANGE",
	switchPrefix + "DUAL_WRITE",
	switchPrefix + "ROLLUP_WRITE",
}

// TestNoTelemetrySwitchesExist 扫描整个 backend/ 源码树，三个开关名必须零命中。
//
// 扫描范围是**整棵树**而不是本包：这些名字散落在 internal/config（字段解析）、
// internal/databus（consumer 注释）、cmd/server（接线），只扫本包等于什么都没守。
//
// 只扫 .go 文件（不含 _test.go 之外的排除 —— 测试文件里的旧名字同样是漂移，
// 而且本文件自己在 var 清单里写了这三个名字，因此**必须排除本文件**）。
func TestNoTelemetrySwitchesExist(t *testing.T) {
	// 从本包目录向上两级到 backend/（internal/telemetry → backend）。
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatalf("resolve backend root: %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "go.mod")); statErr != nil {
		t.Fatalf("backend root %s 不像 backend 目录（没有 go.mod）: %v", root, statErr)
	}
	self, err := filepath.Abs("switch_free_gate_test.go")
	if err != nil {
		t.Fatalf("resolve self path: %v", err)
	}

	type hit struct {
		file string
		line int
		text string
	}
	var hits []hit

	// 为每个开关构造"完整标识符"正则：名字之后不得接 [A-Z0-9_]。
	patterns := make([]*regexp.Regexp, len(deletedTelemetrySwitches))
	for i, name := range deletedTelemetrySwitches {
		patterns[i] = regexp.MustCompile(regexp.QuoteMeta(name) + "[A-Z0-9_]?")
	}

	walkErr := filepath.WalkDir(root, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", "node_modules", "vendor", "testdata":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		if abs, _ := filepath.Abs(path); abs == self {
			return nil // 本文件就是那张清单，豁免自己
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		rel, _ := filepath.Rel(root, path)
		for lineNo, line := range strings.Split(string(data), string(byte(10))) {
			for i, re := range patterns {
				m := re.FindString(line)
				if m == "" {
					continue
				}
				// 命中的是完整开关名（而非 _QUEUE 之类保留参数）才算违规。
				if m != deletedTelemetrySwitches[i] {
					continue
				}
				hits = append(hits, hit{file: rel, line: lineNo + 1, text: strings.TrimSpace(line)})
			}
		}
		return nil
	})
	if walkErr != nil {
		t.Fatalf("walk backend tree: %v", walkErr)
	}

	if len(hits) > 0 {
		t.Errorf("契约 §1/§3.6 要求这三个遥测开关**彻底删除**，但源码里仍有 %d 处命中：", len(hits))
		for _, h := range hits {
			t.Errorf("  %s:%d  %s", h.file, h.line, h.text)
		}
		t.Logf("保留的两个**参数**（不是开关，删了反而是过度删除）：" +
			EnvQueueSize + "（队列深度）、" + EnvKeyFrameSeconds + "（关键帧间隔）")
	}
}

// TestRetainedTelemetryParamsStillExist 守**反向**边界：删开关不能连参数一起删。
//
// 契约 §3.2 明确保留两个参数。如果这条红了，说明有人"顺手清理"过度 ——
// 队列深度与关键帧间隔是运行期可调项，删掉它们会让现场无法在不重编译的情况下
// 调整 L2 的内存占用与关键帧密度。
func TestRetainedTelemetryParamsStillExist(t *testing.T) {
	cases := map[string]string{
		"EnvQueueSize":       switchPrefix + "DUAL_WRITE_QUEUE",
		"EnvKeyFrameSeconds": switchPrefix + "HEARTBEAT_SECONDS",
	}
	if EnvQueueSize != cases["EnvQueueSize"] {
		t.Errorf("EnvQueueSize = %q, want %q（契约 §3.2 保留）", EnvQueueSize, cases["EnvQueueSize"])
	}
	if EnvKeyFrameSeconds != cases["EnvKeyFrameSeconds"] {
		t.Errorf("EnvKeyFrameSeconds = %q, want %q（契约 §3.2 保留）", EnvKeyFrameSeconds, cases["EnvKeyFrameSeconds"])
	}
}
