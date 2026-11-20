package commandexec

// 确认判定唯一性 (方案 §3.3.4 R3 / §8 A-9) 与 actor 分流门禁 (2026-11-18 简化)。

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// ═══ A-9 / R3 判据 1: 确认判定**函数**实现点唯一 ═══════════════════════════
//
// 方案 §3.3.4 R3 的原话是"全仓 requiresConfirmation/confirmationRequired
// 实现点 = 1", 本门禁就照这句话判: 统计全仓生产 Go 源码中**确认判定函数**的
// 声明数, 必须恰好 1 (deviceaction.RiskLevel.RequiresConfirmation)。
//
// 反向验证 (方案 §8 A-9 指定的注入): 在 service.go 里内联第二份判定函数
// `func confirmationRequired(risk string) bool { ... }` → 声明数变 2 → 门禁红。
// isConfirmationPredicateDecl 判定一行是否是**确认判定函数**的声明。
// 只认函数声明形态, 且名字必须是 RequiresConfirmation 或以 onfirmationRequired
// 结尾 (confirmationRequired)。不引入正则: 首版正则里的分组转义在 Go 原始字符串
// 中被多转义了一层, 导致判据恒不匹配 (声明数恒为 0) —— 门禁必须自己先被验证。
func isConfirmationPredicateDecl(line string) bool {
	if !strings.HasPrefix(line, "func ") {
		return false
	}
	if strings.Contains(line, "RequiresConfirmation(") {
		return true
	}
	return strings.Contains(line, "onfirmationRequired(")
}

func TestConfirmationPredicateHasSingleImplementation(t *testing.T) {
	const root = ".."
	var files, decls []string
	err := filepath.Walk(root, func(p string, info os.FileInfo, err error) error {
		if err != nil {
			return nil // 目录缺失等情况跳过, 由分母守卫兜底
		}
		if info.IsDir() {
			if info.Name() == "node_modules" || info.Name() == "testdata" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		files = append(files, p)
		raw, readErr := os.ReadFile(p)
		if readErr != nil {
			return readErr
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if isConfirmationPredicateDecl(strings.TrimSpace(line)) {
				decls = append(decls, p+":"+lineNo(i+1)+": "+strings.TrimSpace(line))
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
	// 分母守卫: 必须真的扫到源码, 否则门禁是空转 (vacuous pass)。
	if len(files) < 50 {
		t.Fatalf("只扫到 %d 个非测试 .go 文件 (期望 >=50) —— 扫描路径错了, 门禁形同虚设", len(files))
	}
	if len(decls) != 1 {
		t.Fatalf("确认判定函数声明数 = %d, 期望恰好 1 (方案 §3.3.4 R3):\n%s", len(decls), strings.Join(decls, "\n"))
	}
	// 分母守卫 2: 唯一那处必须真的是 deviceaction 的 RequiresConfirmation,
	// 否则"零声明"以外的写法 (例如把判定改名) 也能让上面恒绿。
	if !strings.Contains(decls[0], "RequiresConfirmation") || !strings.Contains(decls[0], "risk.go") {
		t.Fatalf("唯一的确认判定实现不是 deviceaction/risk.go 的 RequiresConfirmation: %s", decls[0])
	}
}

// ═══ A-9 / R3 判据 2: 确认判定路径内不得**内联**第二份判定 ═══════════════
//
// 判据 1 只看函数声明, 挡不住"直接在确认门里写 if def.Risk == \"high\" 而不抽
// 函数"这种注入 —— 那同样是第二份判定。因此再扫一遍**确认判定所在的包**
// (commandexec, 确认门与令牌铸造门都在这里), 禁止出现把 risk/RiskLevel 与
// 风险字面量/枚举值直接比较的代码。
//
// 范围声明 (诚实边界): 本判据不扫 deviceaction/definition.go —— 那里对 4 级
// 方言的比较是**引擎门禁/严重度**语义 (如 at-most-once 必须 high/critical),
// 不是确认判定; 把它们一并判红只会让门禁表达错误意图。确认判定源唯一的
// 全仓保证由判据 1 (函数声明数) 提供。
var (
	inlineDriverRiskCompare = regexp.MustCompile(`Risk[ \t]*(==|!=)[ \t]*"`)
	inlineRiskLevelCompare  = regexp.MustCompile(`RiskLevel[ \t]*(==|!=)[ \t]*"`)
)

func TestConfirmationGateHasNoInlinedSecondDecision(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("读取 commandexec 目录失败: %v", err)
	}
	var scanned int
	var offenses []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned++
		raw, readErr := os.ReadFile(name)
		if readErr != nil {
			t.Fatalf("读取 %s 失败: %v", name, readErr)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			code := line
			if idx := strings.Index(code, "//"); idx >= 0 {
				code = code[:idx]
			}
			if strings.TrimSpace(code) == "" {
				continue
			}
			if inlineDriverRiskCompare.MatchString(code) || inlineRiskLevelCompare.MatchString(code) {
				offenses = append(offenses, name+":"+lineNo(i+1)+": "+strings.TrimSpace(line))
			}
		}
	}
	// 分母守卫: 必须真的扫到生产文件。
	if scanned < 5 {
		t.Fatalf("只扫到 %d 个 commandexec 生产 .go 文件 —— 扫描路径错了, 门禁形同虚设", scanned)
	}
	if len(offenses) != 0 {
		t.Fatalf("确认判定路径内出现 %d 处内联风险判定 (方案 §3.3.4 R3):\n%s", len(offenses), strings.Join(offenses, "\n"))
	}
}

// lineNo 把行号转成十进制字符串 (不引入 strconv, 门禁文件保持最小依赖)。
// 注意不能叫 itoa —— 本包 periph_transport_test.go 已有同名 helper。
func lineNo(v int) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte(48 + v%10)
		v /= 10
	}
	return string(b[i:])
}

// ═══ O-2 行为: 授权跳过 / 未授权仍拦 ═══════════════════════════════════════
//
// 用 periph 链路 (gpio_set = medium 风险, PeriphCmd 单步 observation) 驱动确认门:
// 这是本包既有 harness, 能真正走到 Create 的确认门而不是先被引擎门拦掉。
func systemActorRiskEdge(t *testing.T) (*Service, uint, json.RawMessage) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	node := setupPeriphNode(t, db)
	s := NewService(db, periphActions(t))
	s.SetDispatchEnabled(true)
	return s, node.ID, json.RawMessage(`{"pin":5,"level":1}`)
}

// ═══ 2026-11-18 简化: 确认门只按 actor 分流 ═══════════════════════════
//
// 风险等级只决定界面是否弹窗; 自动路径 (system) 是用户保存规则时确认过的,
// 无人值守时不可能再点一次弹窗, 因此直接执行。用户路径 (设备页/手动触发)
// 仍然必须持一次性确认令牌 —— 这条是"简化不得变成绕过"的回归锁。

func TestSystemActorExecutesRiskyActionWithoutToken(t *testing.T) {
	s, nodeID, params := systemActorRiskEdge(t)
	exec, replayed, err := s.Create(context.Background(), CreateInput{
		EdgeDeviceID: nodeID, ActorUserID: 7, ActorKind: ActorKindSystem,
		ActionID: deviceaction.ActionGPIOSet, Params: params,
		IdempotencyKey: "system-risk-direct-0001", Reason: "automation:1:rule",
	})
	if err != nil {
		t.Fatalf("system 路径的风险动作必须直接执行, 实际 err=%v", err)
	}
	if replayed {
		t.Fatal("首次调用不应是 replay")
	}
	if exec.Status != StatusQueued {
		t.Fatalf("status=%s want %s", exec.Status, StatusQueued)
	}
}

func TestUserActorRiskyActionStillRequiresToken(t *testing.T) {
	s, nodeID, params := systemActorRiskEdge(t)
	_, _, err := s.Create(context.Background(), CreateInput{
		EdgeDeviceID: nodeID, ActorUserID: 7, // ActorKind 空 = user
		ActionID: deviceaction.ActionGPIOSet, Params: params,
		IdempotencyKey: "user-risk-token-0001", Reason: "manual reason",
	})
	if !errors.Is(err, ErrConfirmationRequired) {
		t.Fatalf("人工路径的风险动作必须仍需确认令牌, 实际 err=%v", err)
	}
}

// IssueConfirmation 的铸造门同样必须读唯一判定。
func TestIssueConfirmationUsesSingleDecisionSource(t *testing.T) {
	s, edge, user := setupConfirmationService(t)
	if _, err := s.IssueConfirmation(context.Background(), ConfirmationInput{
		EdgeDeviceID: edge.ID, ActorUserID: user.ID, ActionID: "low_read",
		Params: json.RawMessage(`{}`), Reason: "r", SourceIP: "127.0.0.1",
	}); !errors.Is(err, ErrConfirmationNotNeeded) {
		t.Fatalf("普通动作铸造 token 应被拒 (ErrConfirmationNotNeeded), 实际 %v", err)
	}
	if _, err := s.IssueConfirmation(context.Background(), ConfirmationInput{
		EdgeDeviceID: edge.ID, ActorUserID: user.ID, ActionID: "medium_read",
		Params: json.RawMessage(`{}`), Reason: "r", SourceIP: "127.0.0.1",
	}); err != nil {
		t.Fatalf("风险动作必须能铸造 token, 实际 %v", err)
	}
}

// 审计 Metadata 必须留下判定时的风险快照 (方案 §3.3.5 I9)。
func TestAuditRecordsRiskLevelSnapshot(t *testing.T) {
	s, nodeID, params := systemActorRiskEdge(t)
	exec, _, err := s.Create(context.Background(), CreateInput{
		EdgeDeviceID: nodeID, ActorUserID: 7, ActorKind: ActorKindSystem,
		ActionID: deviceaction.ActionGPIOSet, Params: params,
		IdempotencyKey: "risk-audit-0001", Reason: "automation:1:rule",
	})
	if err != nil {
		t.Fatal(err)
	}
	var row models.SecurityAuditEvent
	if err := s.db.Where("request_id = ?", exec.CommandID).First(&row).Error; err != nil {
		t.Fatalf("审计行缺失: %v", err)
	}
	if !strings.Contains(row.Metadata, string(deviceaction.RiskElevated)) {
		t.Fatalf("审计 Metadata 未含 risk_level 快照: %s", row.Metadata)
	}
}
