package main

import (
	"os"
	"strings"
	"testing"
)

// =====================================================================
// 外发通知引擎不得再次成为孤儿: 接线门禁 (源码级) —— D-1 步骤 3
// =====================================================================
//
// 背景 (主控实测, 本步骤要解决的缺陷): internal/notify 包完整实现了出站 HTTP
// client + SSRF + 超时重试 + 脱敏 + 投递审计 + 指标, 且有测试全绿 —— 但生产代码
// **从不构造它** (grep NewDispatcher 在 internal/ + cmd/ 零命中, 只有 commandexec
// 的同名符号)。外发能力因此是一段**孤立的引擎**: 测试全绿, 线上永不触发。
//
// 为什么需要这条门禁: 既有测试无法发现"没人接线" —— notify 包内的测试全都直接调
// d.Deliver(...), 它们在一个测试自己构造的 Dispatcher 上跑, 与"生产是否构造它"
// 完全无关。alert 包的端到端测试能覆盖 alert 这一条链, 但 automation /
// datalifecycle / datasource 三个包的接线 (以及 main.go 里各处 SetNotifier)
// 仍可能被一次重构静默删掉, 而所有测试照旧全绿。
//
// 本门禁断言 (扫描面: cmd/server/main.go 的生产源码):
//   (1) main.go 构造了 notify.NewDispatcher;
//   (2) main.go 对 4 个通知生产包逐个接线 (alert / automation / datalifecycle /
//       datasource) —— 漏一个就有半个域的通知永不外发;
//   (3) datalifecycle 的**两个** worker (Migrator + RetentionTask) 各自接线:
//       它们是两个独立写入点, 只接一个会有半个包仍是孤岛;
//   (4) Dispatcher 只构造一次, 且该实例被交给 API 层 (步骤 4 的 /test 端点) ——
//       否则"测试消息"与真实通知走两套客户端, 测试通过不代表线上通道可用。
//
// 范围声明 (诚实边界): 这是**源码扫描**, 不证明运行时投递成功 (那由
// internal/alert/e2e_notify_wiring_test.go 的真实 HTTP 接收端覆盖); 也不能识别
// "把接线写在注释里"这类规避 —— 门禁防的是**无意的重构删除**, 不是故意绕过。
// 扫描器自检 (TestNotifyWiringGate_MutationSelfCheck) 是变异重放: 把"未接线"的
// 真实写法喂给判定逻辑, 要求逐条判红 —— 否则一个坏掉的扫描器会让门禁永远"绿"
// (vacuous pass), 本仓已有此教训 (见 datalifecycle/rollup_retirement_gate_test.go)。

// notifyWiringMainFile 是被检的生产文件 (本包目录下)。
const notifyWiringMainFile = "main.go"

// notifyWiringNeedle 是一条"必须出现"的接线证据。
type notifyWiringNeedle struct {
	name   string
	needle string
}

// notifyWiringRequired 是 main.go 中必须出现的接线调用。
//
// 用"字面量必须出现"而非解析 AST: 本仓门禁同范式 (源码扫描), 且接线是机械的
// setter 调用, 字面量足够精确。
var notifyWiringRequired = []notifyWiringNeedle{
	{"构造 Dispatcher", "notify.NewDispatcher(db)"},
	{"alert 接线", "alertEvaluator.SetNotifier(notifyDispatcher)"},
	{"automation 接线", "automationPlanner.SetNotifier(notifyDispatcher)"},
	{"datalifecycle Migrator 接线", "migrator.SetNotifier(notifyDispatcher)"},
	{"datalifecycle RetentionTask 接线", "retentionTask.SetNotifier(notifyDispatcher)"},
	{"datasource 接线", "datasourceSvc.SetDispatchNotifier(notifyDispatcher)"},
}

// missingWiring 返回 src 中缺失的接线项。抽成纯函数以便变异自检直接喂字符串
// (自检不依赖磁盘上的 main.go 当前内容, 否则"接线被删"时自检会跟着一起变)。
func missingWiring(src string) []string {
	var missing []string
	for _, req := range notifyWiringRequired {
		if !strings.Contains(src, req.needle) {
			missing = append(missing, req.name+": 期望出现 "+req.needle)
		}
	}
	return missing
}

// countDispatcherConstructions 统计 Dispatcher 的构造次数。
func countDispatcherConstructions(src string) int {
	return strings.Count(src, "notify.NewDispatcher(")
}

func readMainSource(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(notifyWiringMainFile)
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", notifyWiringMainFile, err)
	}
	return string(data)
}

// TestNotifyWiringGate_MainWiresAllProducers 断言 main.go 真实接线了 4 个包。
func TestNotifyWiringGate_MainWiresAllProducers(t *testing.T) {
	src := readMainSource(t)
	for _, msg := range missingWiring(src) {
		t.Errorf("main.go 缺少接线 %s —— 外发通知引擎将再次成为孤儿", msg)
	}
}

// TestNotifyWiringGate_DispatcherIsSharedSingleton 断言 Dispatcher 只构造一次,
// 且该实例被交给 API 层 (步骤 4 的 /test 端点)。
func TestNotifyWiringGate_DispatcherIsSharedSingleton(t *testing.T) {
	src := readMainSource(t)
	if got := countDispatcherConstructions(src); got != 1 {
		t.Errorf("main.go 构造 Dispatcher %d 次, 期望恰好 1 次 (必须是共享实例)", got)
	}
	// 实例必须被交给 API 层, 否则 POST /notification-channels/:id/test 永远 503。
	if !strings.Contains(src, "datasourceSvc, notifyDispatcher, api.ControlPolicy{") {
		t.Error("notifyDispatcher 未传给 api.SetupRoutes: /test 端点将拿不到投递器 (503)")
	}
}

// TestNotifyWiringGate_MutationSelfCheck 是**变异重放**。
//
// 三条变异对应三种真实的"孤儿化"方式:
//   - 恢复接线前的 main.go (完全不提 notify.*) → 全项缺失;
//   - 只删掉 datalifecycle 两个 worker 之一的接线 (最易漏的一处: 一个包两个写入点);
//   - 删掉 alert 接线 (端到端测试覆盖的那条链在 main.go 上被摘掉)。
//
// 要求逐条判红。自检用例是硬编码字符串, 不读磁盘 —— 因此"有人把 main.go 改坏"
// 不会让自检跟着失效 (那是被测门禁的职责, 不是自检的)。
func TestNotifyWiringGate_MutationSelfCheck(t *testing.T) {
	// 变异 1: 接线前的 main.go — 只有既有的 datasource 旧回调, 完全不提 notify 包。
	beforeWiring := `func main() {
	datasourceSvc := datasource.New(db, datasource.Options{})
	datasourceSvc.SetNotifier(func(n models.Notification) {
		if err := db.Create(&n).Error; err != nil {
			logger.Warn("datasource: failed to write notification", "source_id", n.SourceID, "error", err)
		}
	})
	alertEvaluator := alert.NewEvaluator(db, wsHub.BroadcastEvent)
}`
	if missing := missingWiring(beforeWiring); len(missing) != len(notifyWiringRequired) {
		t.Errorf("变异 1 (接线前 main.go): 判定出 %d 项缺失, 期望全部 %d 项 —— 扫描器失效",
			len(missing), len(notifyWiringRequired))
	}
	if got := countDispatcherConstructions(beforeWiring); got != 0 {
		t.Errorf("变异 1: 构造计数 = %d, 期望 0", got)
	}

	// 变异 2: 只漏掉 datalifecycle 的 RetentionTask 接线 (一个包两个写入点, 最易漏)。
	missingRetention := `notifyDispatcher := notify.NewDispatcher(db)
alertEvaluator.SetNotifier(notifyDispatcher)
automationPlanner.SetNotifier(notifyDispatcher)
migrator.SetNotifier(notifyDispatcher)
datasourceSvc.SetDispatchNotifier(notifyDispatcher)`
	missing := missingWiring(missingRetention)
	if len(missing) != 1 {
		t.Fatalf("变异 2 (漏 RetentionTask): 判定出 %d 项缺失, 期望恰好 1 项", len(missing))
	}
	if !strings.Contains(missing[0], "RetentionTask") {
		t.Errorf("变异 2: 判红的是 %q, 期望指出 RetentionTask", missing[0])
	}

	// 变异 3: 漏掉 alert 接线。
	missingAlert := `notifyDispatcher := notify.NewDispatcher(db)
automationPlanner.SetNotifier(notifyDispatcher)
migrator.SetNotifier(notifyDispatcher)
retentionTask.SetNotifier(notifyDispatcher)
datasourceSvc.SetDispatchNotifier(notifyDispatcher)`
	missing = missingWiring(missingAlert)
	if len(missing) != 1 {
		t.Fatalf("变异 3 (漏 alert): 判定出 %d 项缺失, 期望恰好 1 项", len(missing))
	}
	if !strings.Contains(missing[0], "alert") {
		t.Errorf("变异 3: 判红的是 %q, 期望指出 alert", missing[0])
	}

	// 变异 4: 两次构造 Dispatcher (第二处会让测试消息与真实通知走不同实例)。
	doubleConstruct := `a := notify.NewDispatcher(db)
b := notify.NewDispatcher(db)`
	if got := countDispatcherConstructions(doubleConstruct); got != 2 {
		t.Errorf("变异 4: 构造计数 = %d, 期望 2", got)
	}

	// 正向对照: 完整接线必须判绿 (否则门禁是"永远红", 同样没有信息量)。
	complete := `notifyDispatcher := notify.NewDispatcher(db)
alertEvaluator.SetNotifier(notifyDispatcher)
automationPlanner.SetNotifier(notifyDispatcher)
migrator.SetNotifier(notifyDispatcher)
retentionTask.SetNotifier(notifyDispatcher)
datasourceSvc.SetDispatchNotifier(notifyDispatcher)`
	if missing := missingWiring(complete); len(missing) != 0 {
		t.Errorf("正向对照 (完整接线) 判定出 %d 项缺失, 期望 0 项: %v", len(missing), missing)
	}
}
