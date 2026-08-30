package automation

// 裁决 4 确认制闭环 (设计/自动化确认制闭环实现方案.md) 行为测试。
// 复用 commandexec 同款 harness 模式: testutil.OpenTestDB + node/channel/edge 真链 +
// SystemAdmin 操作者 (LastLoginAt 新鲜, 满足 IssueConfirmation 近认证门 confirmation.go:104)。
// 覆盖: ①confirm 流转 ②幂等防重 ③超时清扫 ④错误分支 (事件缺失/非pending/expired/规则删/action变更)。

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// setupConfirmPlanner 造可走通 ConfirmEvent 成功路径的最小真实链路:
// node(capability 新鲜)+channel+edge(prs3001/read_rainfall 启用)+planner(dispatch on)。
// 返回 planner / cmdSvc / edge。
func setupConfirmPlanner(t *testing.T) (*Planner, *models.EdgeDevice) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db := testutil.OpenTestDB(t)

	reported := time.Now().UTC()
	node := models.Node{
		NodeID: "node-conf", Name: "t", Status: "online",
		ConfigVersion: "manifest-test", ConfigStatus: "applied", ConfigSyncState: "in_sync",
		BootID: "boot-test", ResourceReportedAt: &reported, CommandEngineRevision: 1,
		CommandEngineCapabilities: `{"supports_channel_cmd_v2":true,"supports_finally":true,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`,
	}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	ch := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&node).Update("hardware_info",
		`{"channels":[{"id":`+itoa(ch.ID)+`,"enabled":true}]}`).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{Name: "rain", NodeID: node.NodeID, ChannelID: ch.ID,
		DeviceConfigID: 1, Type: "prs3001", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}
	actions := deviceaction.NewBuiltInRegistry(nil)
	if err := actions.SetEnabled("prs3001", "read_rainfall", true); err != nil {
		t.Fatal(err)
	}
	// confirm 流转测试须高风险动作 (confirmationRequired=medium/high/critical,
	// confirmation.go:54; read_rainfall 是 low 会被 ErrConfirmationNotNeeded 拒绝)。
	// 用 read 语义 + medium risk: set 语义会触发 "requires a trusted verifier" 门
	// (skill: set 需 trusted verifier, 测试用 read+medium 替代)。
	if err := actions.Register(deviceaction.Definition{
		ID: "confirm_reset", Version: 1, Name: "confirm reset", DeviceType: "prs3001",
		Semantics: "read", Risk: "medium", Enabled: true,
		Transport: deviceaction.ChannelCmdV2Adapter,
		SingleStep: deviceaction.SingleStep{TXData: []byte{0x01, 0x05}, RXTimeoutMS: 1},
	}); err != nil {
		t.Fatal(err)
	}
	// F5/F6 测试需低-risk read 动作 (CurrentEngineAllows 放行 single+read+low,
	// definition.go:408-430), 直接执行成功落 executed 事件。
	if err := actions.Register(deviceaction.Definition{
		ID: "low_read", Version: 1, Name: "low risk read", DeviceType: "prs3001",
		Semantics: "read", Risk: "low", Enabled: true,
		Transport: deviceaction.ChannelCmdV2Adapter,
		SingleStep: deviceaction.SingleStep{TXData: []byte{0x01, 0x06}, RXTimeoutMS: 1},
	}); err != nil {
		t.Fatal(err)
	}
	svc := commandexec.NewService(db, actions)
	svc.SetDispatchEnabled(true)

	// systemActorID=900 内置系统用户占位 (仅触发路径归因, 本文件主测 confirm 操作者路径)。
	return NewPlanner(db, svc, nil, 900), &edge
}

func itoa(v uint) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// newAdminOperator 造满足近认证门的系统管理员操作者 (SubjectKey + LastLoginAt=now)。
func newAdminOperator(t *testing.T, db *gorm.DB, id uint) {
	t.Helper()
	now := time.Now().UTC()
	sk := models.SystemAdminSubjectKey
	u := models.User{ID: id, Username: "op-" + itoa(id), PasswordHash: "hash",
		Enabled: true, SubjectKey: &sk, SessionVersion: 1, LastLoginAt: &now}
	if err := db.Create(&u).Error; err != nil {
		t.Fatal(err)
	}
}

// f64 返回 float64 指针 (TriggerValue 是 *float64)。
func f64(v float64) *float64 { return &v }

// confirmedRule 造 require_confirmed=true 的 device_action 规则 (绑定 edge/read_rainfall)。
func confirmedRule(edgeID uint) models.AutomationRule {
	return models.AutomationRule{
		Name:               "确认制规则",
		Enabled:            true,
		TriggerType:        models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edgeID,
		TriggerSensorName:  "illuminance",
		TriggerComparator:  "gt",
		TriggerThreshold:   500,
		CooldownSec:        0,
		RequireConfirmed:   true,
		ActionType:         models.AutomationActionDeviceAction,
		ActionDeviceID:     edgeID,
		ActionID:           "confirm_reset",
		ActionParamsJSON:   `{}`,
	}
}

// pendingEvent 直接落一行 pending_confirm 事件 (triggered_at 可注入以测超窗)。
func pendingEvent(t *testing.T, p *Planner, ruleID uint, val float64, at time.Time) models.AutomationEvent {
	t.Helper()
	ev := models.AutomationEvent{
		RuleID:       ruleID,
		TriggerValue: f64(val),
		Result:       models.AutomationResultPendingConfirm,
		Detail:       "awaiting manual confirmation",
		TriggeredAt:  at,
	}
	if err := p.db.Create(&ev).Error; err != nil {
		t.Fatal(err)
	}
	return ev
}

// ─── ① confirm 失败路径: Create 被引擎 gate 拦截 → ConfirmEvent 透传错误 + 事件翻转 failed ───
// 架构事实: CurrentEngineAllows 只放行 single+read+low 或 set/reset+bounded_sequence
// (deviceaction/definition.go:408-430)。medium/high single-read 永远被 gate —— 这正是
// 设计目标 (高风险动作须 bounded_sequence)。本用例验证 planner 把 Create 失败正确
// 翻转事件为 failed 并透传错误, 不吞错也不留 pending 悬置。
func TestConfirmEventCreateFailureFlipsFailed(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	newAdminOperator(t, p.db, 7)
	rule := confirmedRule(edge.ID) // ActionID=confirm_reset (read+medium, 被 command_engine_gate 拦)
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ev := pendingEvent(t, p, rule.ID, 600, p.nowFn())

	got, err := p.ConfirmEvent(context.Background(), ev.ID, 7, "127.0.0.1")
	if err == nil {
		t.Fatalf("expect create gate error, got nil (event=%+v)", got)
	}
	// 事件须被翻转为 failed (不留 pending 悬置)。
	var after models.AutomationEvent
	if err := p.db.First(&after, ev.ID).Error; err != nil {
		t.Fatal(err)
	}
	// isGateError(Create 的 ErrActionUnavailable)=false → failed_dispatch (planner.go:339-341;
	// command_engine_gate 在 Create 内部归为 dispatch 失败, 非 availability gate 前置失败)。
	if after.Result != models.AutomationResultFailedDispatch {
		t.Fatalf("result=%s want failed_dispatch (create gate 拦截后须翻转)", after.Result)
	}
}

// ─── ② 幂等防重/状态闸: 非 pending_confirm 事件的 confirm 一律被拒, 不双发 ───
// 条件 UPDATE 闸 (result='pending_confirm') 是防重核心: 已 executed/failed/expired
// 的事件再 confirm 必命中 ErrConfirmNotPending, 绝不二次触发 Create。
func TestConfirmEventRejectsNonPendingStates(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	newAdminOperator(t, p.db, 7)
	rule := confirmedRule(edge.ID)
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	// 状态 → 期望哨兵: expired 有专属哨兵 ErrConfirmEventExpired (planner.go:276-277
	// 双保险), 其余非 pending 统一 ErrConfirmNotPending (planner.go:279-280)。
	for _, tc := range []struct {
		state   string
		wantErr error
	}{
		{models.AutomationResultExecuted, ErrConfirmNotPending},
		{models.AutomationResultFailedGate, ErrConfirmNotPending},
		{models.AutomationResultExpired, ErrConfirmEventExpired},
		{models.AutomationResultConditionChanged, ErrConfirmNotPending},
	} {
		ev := models.AutomationEvent{RuleID: rule.ID, TriggerValue: f64(1),
			Result: tc.state, Detail: "done", TriggeredAt: p.nowFn()}
		if err := p.db.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := p.ConfirmEvent(context.Background(), ev.ID, 7, "127.0.0.1"); !errors.Is(err, tc.wantErr) {
			t.Fatalf("state=%s confirm err=%v want %v (非 pending 须被拒)", tc.state, err, tc.wantErr)
		}
		// 状态不被翻转。
		var after models.AutomationEvent
		if err := p.db.First(&after, ev.ID).Error; err != nil {
			t.Fatal(err)
		}
		if after.Result != tc.state {
			t.Fatalf("state=%s 被翻转为 %s (confirm 不得改动非 pending 事件)", tc.state, after.Result)
		}
	}
	// 断言无任何 command_executions 落行 (全部在闸前被拒, 零下发)。
	var cnt int64
	if err := p.db.Model(&models.CommandExecution{}).Count(&cnt).Error; err != nil {
		t.Fatal(err)
	}
	if cnt != 0 {
		t.Fatalf("command_executions rows=%d want 0 (非 pending confirm 不得触发 Create)", cnt)
	}
}

// ─── ③ 超时清扫: 超窗 pending→expired; 未超窗与非 pending 不动 ───
func TestSweepExpiredPending(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	rule := confirmedRule(edge.ID)
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	old := p.nowFn().Add(-pendingConfirmTTL - time.Hour) // 25h 前, 超窗
	fresh := p.nowFn()                                    // 现在, 未超窗

	expiredEv := pendingEvent(t, p, rule.ID, 1, old)
	freshEv := pendingEvent(t, p, rule.ID, 2, fresh)
	// 已 executed 的超窗行不得被清扫误伤。
	executedOld := models.AutomationEvent{RuleID: rule.ID, TriggerValue: f64(3),
		Result: models.AutomationResultExecuted, TriggeredAt: old}
	if err := p.db.Create(&executedOld).Error; err != nil {
		t.Fatal(err)
	}

	p.sweepExpiredPending()

	var got1, got2, got3 models.AutomationEvent
	p.db.First(&got1, expiredEv.ID)
	p.db.First(&got2, freshEv.ID)
	p.db.First(&got3, executedOld.ID)
	if got1.Result != models.AutomationResultExpired {
		t.Fatalf("old pending result=%s want expired", got1.Result)
	}
	if got2.Result != models.AutomationResultPendingConfirm {
		t.Fatalf("fresh pending result=%s want pending_confirm (swept wrongly)", got2.Result)
	}
	if got3.Result != models.AutomationResultExecuted {
		t.Fatalf("old executed result=%s want executed (swept wrongly)", got3.Result)
	}
}

// ─── ④ 错误分支: 事件缺失/非pending/expired/规则删/action变更/超窗现读 ───
func TestConfirmEventErrorBranches(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	newAdminOperator(t, p.db, 7)
	rule := confirmedRule(edge.ID)
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// (a) 事件不存在
	if _, err := p.ConfirmEvent(ctx, 99999, 7, "127.0.0.1"); !errors.Is(err, ErrConfirmEventNotFound) {
		t.Fatalf("missing event err=%v want ErrConfirmEventNotFound", err)
	}
	// (b) 非 pending (executed)
	done := models.AutomationEvent{RuleID: rule.ID, TriggerValue: f64(1),
		Result: models.AutomationResultExecuted, TriggeredAt: p.nowFn()}
	p.db.Create(&done)
	if _, err := p.ConfirmEvent(ctx, done.ID, 7, "127.0.0.1"); !errors.Is(err, ErrConfirmNotPending) {
		t.Fatalf("executed event err=%v want ErrConfirmNotPending", err)
	}
	// (c) expired 事件
	exp := pendingEvent(t, p, rule.ID, 1, p.nowFn())
	p.db.Model(&models.AutomationEvent{}).Where("id = ?", exp.ID).
		Update("result", models.AutomationResultExpired)
	if _, err := p.ConfirmEvent(ctx, exp.ID, 7, "127.0.0.1"); !errors.Is(err, ErrConfirmEventExpired) {
		t.Fatalf("expired event err=%v want ErrConfirmEventExpired", err)
	}
	// (d) 落库时刻已超窗 (清扫未跑到, 双保险拒)
	oldEv := pendingEvent(t, p, rule.ID, 1, p.nowFn().Add(-pendingConfirmTTL-time.Hour))
	if _, err := p.ConfirmEvent(ctx, oldEv.ID, 7, "127.0.0.1"); !errors.Is(err, ErrConfirmEventExpired) {
		t.Fatalf("stale-window event err=%v want ErrConfirmEventExpired", err)
	}
	// (e) 规则被删 (fail-closed)
	rule2 := confirmedRule(edge.ID)
	rule2.Name = "将被删"
	p.db.Create(&rule2)
	orphan := pendingEvent(t, p, rule2.ID, 1, p.nowFn())
	p.db.Delete(&models.AutomationRule{}, rule2.ID)
	if _, err := p.ConfirmEvent(ctx, orphan.ID, 7, "127.0.0.1"); !errors.Is(err, ErrConfirmRuleMissing) {
		t.Fatalf("orphan event err=%v want ErrConfirmRuleMissing", err)
	}
	// (f) action_type 被改为 notification (fail-closed, 不再 device_action)
	rule3 := confirmedRule(edge.ID)
	rule3.Name = "action变更"
	p.db.Create(&rule3)
	evf := pendingEvent(t, p, rule3.ID, 1, p.nowFn())
	p.db.Model(&models.AutomationRule{}).Where("id = ?", rule3.ID).
		Update("action_type", models.AutomationActionNotification)
	if _, err := p.ConfirmEvent(ctx, evf.ID, 7, "127.0.0.1"); !errors.Is(err, ErrConfirmActionChanged) {
		t.Fatalf("action-changed err=%v want ErrConfirmActionChanged", err)
	}
	// 错误分支一律不翻转原事件行为 executed。
	var chk models.AutomationEvent
	p.db.First(&chk, evf.ID)
	if chk.Result != models.AutomationResultPendingConfirm {
		t.Fatalf("error branch flipped event result=%s", chk.Result)
	}
}

// ─── ⑤ 触发分流: require_confirmed=true 落 pending_confirm + 通知带 event_id ───
func TestHandleTriggerConfirmationBranch(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	rule := confirmedRule(edge.ID)
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	p.HandleTrigger(TriggerEvent{Rule: rule, Value: 600, At: p.nowFn()})

	var ev models.AutomationEvent
	if err := p.db.Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultPendingConfirm).
		First(&ev).Error; err != nil {
		t.Fatalf("pending_confirm event missing: %v", err)
	}
	// 不得直接下发 (无 command_executions 行)。
	var cnt int64
	p.db.Model(&models.CommandExecution{}).Where("edge_device_id = ?", edge.ID).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("confirmation branch dispatched %d commands, want 0", cnt)
	}
	// 通知须携带 event_id 供前端 confirm。
	var n models.Notification
	if err := p.db.Where("type = ?", models.NotificationType(models.AlertLevelWarning)).
		Order("id DESC").First(&n).Error; err != nil {
		t.Fatalf("confirmation notification missing: %v", err)
	}
}

// ─── F4 条件复核: 触发到执行间条件已失效 → condition_changed 不执行 ───
// 覆盖: ①trigger 条件失效 ②附加条件失效 ③条件仍满足 → 正常执行
// 复用 setupConfirmPlanner 链路, 注入 latestValueFn 内存实现。
func TestHandleTriggerConditionChanged(t *testing.T) {
	p, edge := setupConfirmPlanner(t)

	// 注入最新值缓存 (模拟执行时刻值已回落到阈值之下)
	latestValue := 300.0 // 低于 trigger threshold 500
	p.SetLatestValueFn(func(deviceID uint) (models.UnifiedData, bool) {
		if deviceID == edge.ID {
			return models.UnifiedData{
				DeviceID: deviceID, SensorName: "illuminance", Value: latestValue,
			}, true
		}
		return models.UnifiedData{}, false
	})

	rule := models.AutomationRule{
		Name:               "条件复核规则",
		Enabled:            true,
		TriggerType:        models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSensorName:  "illuminance",
		TriggerComparator:  "gt",
		TriggerThreshold:   500,
		CooldownSec:        0,
		RequireConfirmed:   false,
		ActionType:         models.AutomationActionDeviceAction,
		ActionDeviceID:     edge.ID,
		ActionID:           "confirm_reset",
		ActionParamsJSON:   `{}`,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	// 触发时刻值 600 (满足 gt 500), 执行时刻最新值 300 (不满足)
	p.HandleTrigger(TriggerEvent{Rule: rule, Value: 600, At: p.nowFn()})

	// 断言: 落 condition_changed 事件, 无 command_executions 下发
	var ev models.AutomationEvent
	if err := p.db.Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultConditionChanged).
		First(&ev).Error; err != nil {
		t.Fatalf("condition_changed event missing: %v", err)
	}
	var cnt int64
	p.db.Model(&models.CommandExecution{}).Where("edge_device_id = ?", edge.ID).Count(&cnt)
	if cnt != 0 {
		t.Fatalf("condition_changed branch dispatched %d commands, want 0", cnt)
	}

	// 场景②: 附加条件失效 (conditions_json 中 temperature > 30, 最新值不满足)
	rule2 := models.AutomationRule{
		Name:               "附加条件复核",
		Enabled:            true,
		TriggerType:        models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSensorName:  "illuminance",
		TriggerComparator:  "gt",
		TriggerThreshold:   500,
		ConditionsJSON:     `[{"sensor_name":"illuminance","comparator":"gt","threshold":400}]`,
		CooldownSec:        0,
		RequireConfirmed:   false,
		ActionType:         models.AutomationActionDeviceAction,
		ActionDeviceID:     edge.ID,
		ActionID:           "confirm_reset",
		ActionParamsJSON:   `{}`,
	}
	if err := p.db.Create(&rule2).Error; err != nil {
		t.Fatal(err)
	}
	p.HandleTrigger(TriggerEvent{Rule: rule2, Value: 600, At: p.nowFn()})
	var ev2 models.AutomationEvent
	if err := p.db.Where("rule_id = ? AND result = ?", rule2.ID, models.AutomationResultConditionChanged).
		First(&ev2).Error; err != nil {
		t.Fatalf("condition_changed event for conditions missing: %v", err)
	}

	// 场景③: 条件仍满足 → 正常执行 (复核通过, 走 commandexec)
	latestValue = 600.0 // 恢复满足阈值
	rule3 := models.AutomationRule{
		Name:               "条件仍满足",
		Enabled:            true,
		TriggerType:        models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSensorName:  "illuminance",
		TriggerComparator:  "gt",
		TriggerThreshold:   500,
		CooldownSec:        0,
		RequireConfirmed:   false,
		ActionType:         models.AutomationActionDeviceAction,
		ActionDeviceID:     edge.ID,
		ActionID:           "confirm_reset",
		ActionParamsJSON:   `{}`,
	}
	if err := p.db.Create(&rule3).Error; err != nil {
		t.Fatal(err)
	}
	p.HandleTrigger(TriggerEvent{Rule: rule3, Value: 600, At: p.nowFn()})
	var ev3 models.AutomationEvent
	if err := p.db.Where("rule_id = ? AND result IN ?", rule3.ID,
		[]string{models.AutomationResultExecuted, models.AutomationResultFailedDispatch}).
		First(&ev3).Error; err != nil {
		t.Fatalf("executed/failed_dispatch event missing (condition passed): %v", err)
	}
}

// ─── F5 日熔断 warning 通知: 达限后补发, 同日同规则只发一次 ───
func TestHandleTriggerDailyLimitNotification(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	// 不注入 latestValueFn → 跳过 F4 复核, 聚焦 F5

	rule := models.AutomationRule{
		Name:               "日熔断测试",
		Enabled:            true,
		TriggerType:        models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSensorName:  "illuminance",
		TriggerComparator:  "gt",
		TriggerThreshold:   500,
		CooldownSec:        0,
		RequireConfirmed:   false,
		MaxDailyExec:       1, // 限额 1
		ActionType:         models.AutomationActionDeviceAction,
		ActionDeviceID:     edge.ID,
		ActionID:           "low_read",
		ActionParamsJSON:   `{}`,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	// 第一次触发: 正常执行 (落 executed)
	p.HandleTrigger(TriggerEvent{Rule: rule, Value: 600, At: p.nowFn()})
	var ev1 models.AutomationEvent
	if err := p.db.Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultExecuted).
		First(&ev1).Error; err != nil {
		t.Fatalf("first trigger executed event missing: %v", err)
	}

	// 第二次触发: 达限 → suppressed_daily_limit + warning 通知
	p.HandleTrigger(TriggerEvent{Rule: rule, Value: 600, At: p.nowFn()})
	var ev2 models.AutomationEvent
	if err := p.db.Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultSuppressedDailyLimit).
		First(&ev2).Error; err != nil {
		t.Fatalf("suppressed_daily_limit event missing: %v", err)
	}
	var n1 models.Notification
	if err := p.db.Where("type = ? AND source = ? AND source_id = ? AND title = ?",
		models.NotificationType(models.AlertLevelWarning), "automation_rule",
		fmt.Sprintf("%d", rule.ID), "策略日熔断: "+rule.Name).
		First(&n1).Error; err != nil {
		t.Fatalf("daily limit warning notification missing: %v", err)
	}

	// 第三次触发: 幂等 — 仍落 suppressed_daily_limit, 但不再发通知
	p.HandleTrigger(TriggerEvent{Rule: rule, Value: 600, At: p.nowFn()})
	var cnt int64
	p.db.Model(&models.Notification{}).
		Where("type = ? AND source = ? AND source_id = ? AND title = ?",
			models.NotificationType(models.AlertLevelWarning), "automation_rule",
			fmt.Sprintf("%d", rule.ID), "策略日熔断: "+rule.Name).
		Count(&cnt)
	if cnt != 1 {
		t.Fatalf("daily limit notification count=%d want 1 (幂等: 同日同规则只发一次)", cnt)
	}
}

// ─── F6 计数口径: pending_confirm 不占日限额 ───
// 造 pending_confirm 事件 (不 executed), 反复触发同一规则, 日限额不应被占满。
func TestHandleTriggerDailyLimitIgnoresPendingConfirm(t *testing.T) {
	p, edge := setupConfirmPlanner(t)

	rule := models.AutomationRule{
		Name:               "pending不占额",
		Enabled:            true,
		TriggerType:        models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSensorName:  "illuminance",
		TriggerComparator:  "gt",
		TriggerThreshold:   500,
		CooldownSec:        0,
		RequireConfirmed:   false,
		MaxDailyExec:       2, // 限额 2
		ActionType:         models.AutomationActionDeviceAction,
		ActionDeviceID:     edge.ID,
		ActionID:           "low_read",
		ActionParamsJSON:   `{}`,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	// 手动落 2 条 pending_confirm (模拟反复触发待确认, 24h 未 expired)
	for i := 0; i < 2; i++ {
		ev := models.AutomationEvent{
			RuleID: rule.ID, TriggerValue: f64(600),
			Result: models.AutomationResultPendingConfirm,
			Detail: "awaiting manual confirmation",
			TriggeredAt: p.nowFn(),
		}
		if err := p.db.Create(&ev).Error; err != nil {
			t.Fatal(err)
		}
	}

	// 触发: pending_confirm 不占额 → 应正常执行 (落 executed)
	p.HandleTrigger(TriggerEvent{Rule: rule, Value: 600, At: p.nowFn()})
	var ev models.AutomationEvent
	if err := p.db.Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultExecuted).
		First(&ev).Error; err != nil {
		t.Fatalf("executed event missing (pending_confirm should not occupy daily limit): %v", err)
	}

	// 再触发一次: 第 2 次 executed, 仍不应被 suppressed (限额 2, 已执行 1)
	p.HandleTrigger(TriggerEvent{Rule: rule, Value: 600, At: p.nowFn()})
	var cnt int64
	p.db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultExecuted).
		Count(&cnt)
	if cnt != 2 {
		t.Fatalf("executed count=%d want 2 (pending_confirm must not count toward daily limit)", cnt)
	}

	// 第 3 次触发: 达限 → suppressed_daily_limit
	p.HandleTrigger(TriggerEvent{Rule: rule, Value: 600, At: p.nowFn()})
	var suppressed int64
	p.db.Model(&models.AutomationEvent{}).
		Where("rule_id = ? AND result = ?", rule.ID, models.AutomationResultSuppressedDailyLimit).
		Count(&suppressed)
	if suppressed != 1 {
		t.Fatalf("suppressed count=%d want 1 (after 2 executed, 3rd should be suppressed)", suppressed)
	}
}

// ─── 手动触发 (POST /api/v1/automation-rules/:id/trigger) ──
// 与自动触发 (HandleTrigger) 的差异: 跳过条件评估/确认制, 但保留 cooldown/max_daily_exec。

func TestTriggerRuleManualSuccess(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	newAdminOperator(t, p.db, 7)

	rule := models.AutomationRule{
		Name:               "手动触发",
		Enabled:            true,
		TriggerType:        models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSensorName:  "illuminance",
		TriggerComparator:  "gt",
		TriggerThreshold:   500,
		CooldownSec:        0,
		RequireConfirmed:   true, // 手动触发跳过确认制
		ActionType:         models.AutomationActionDeviceAction,
		ActionDeviceID:     edge.ID,
		ActionID:           "low_read",
		ActionParamsJSON:   `{}`,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	ev, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1")
	if err != nil {
		t.Fatalf("manual trigger failed: %v", err)
	}
	if ev.Result != models.AutomationResultExecuted {
		t.Fatalf("result=%s want executed", ev.Result)
	}
	if ev.TriggerSource != models.AutomationTriggerSourceManual {
		t.Fatalf("trigger_source=%s want manual", ev.TriggerSource)
	}
	if ev.CommandID == "" {
		t.Fatal("command_id empty (manual trigger must dispatch)")
	}
}

func TestTriggerRuleManualNotFound(t *testing.T) {
	p, _ := setupConfirmPlanner(t)
	_, err := p.TriggerRule(context.Background(), 9999, 7, "127.0.0.1")
	if !errors.Is(err, ErrTriggerRuleNotFound) {
		t.Fatalf("expect ErrTriggerRuleNotFound, got %v", err)
	}
}

func TestTriggerRuleManualDisabled(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	rule := models.AutomationRule{
		Name:    "禁用规则",
		Enabled: false,
		TriggerType: models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID,
		ActionType: models.AutomationActionNotification,
		ActionLevel: models.AlertLevelInfo,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	_, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1")
	if !errors.Is(err, ErrTriggerRuleDisabled) {
		t.Fatalf("expect ErrTriggerRuleDisabled, got %v", err)
	}
}

func TestTriggerRuleManualCooldownSuppressed(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	newAdminOperator(t, p.db, 7)

	rule := models.AutomationRule{
		Name:               "冷却测试",
		Enabled:            true,
		TriggerType:        models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSensorName:  "illuminance",
		TriggerComparator:  "gt",
		TriggerThreshold:   500,
		CooldownSec:        3600, // 1h
		ActionType:         models.AutomationActionDeviceAction,
		ActionDeviceID:     edge.ID,
		ActionID:           "low_read",
		ActionParamsJSON:   `{}`,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	// 第一次触发: 成功
	ev1, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1")
	if err != nil || ev1.Result != models.AutomationResultExecuted {
		t.Fatalf("first trigger failed: %v result=%s", err, ev1.Result)
	}

	// 第二次触发: cooldown 抑制
	ev2, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1")
	if err != nil {
		t.Fatalf("second trigger err: %v", err)
	}
	if ev2.Result != models.AutomationResultSuppressedCooldown {
		t.Fatalf("result=%s want suppressed_cooldown", ev2.Result)
	}
	if ev2.TriggerSource != models.AutomationTriggerSourceManual {
		t.Fatalf("trigger_source=%s want manual", ev2.TriggerSource)
	}
}

func TestTriggerRuleManualDailyLimit(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	newAdminOperator(t, p.db, 7)

	rule := models.AutomationRule{
		Name:               "日熔断",
		Enabled:            true,
		TriggerType:        models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSensorName:  "illuminance",
		TriggerComparator:  "gt",
		TriggerThreshold:   500,
		CooldownSec:        0,
		MaxDailyExec:       1,
		ActionType:         models.AutomationActionDeviceAction,
		ActionDeviceID:     edge.ID,
		ActionID:           "low_read",
		ActionParamsJSON:   `{}`,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	// 第一次触发: 成功 (占额 1)
	ev1, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1")
	if err != nil || ev1.Result != models.AutomationResultExecuted {
		t.Fatalf("first trigger failed: %v result=%s", err, ev1.Result)
	}

	// 第二次触发: 达限 → suppressed_daily_limit
	ev2, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1")
	if err != nil {
		t.Fatalf("second trigger err: %v", err)
	}
	if ev2.Result != models.AutomationResultSuppressedDailyLimit {
		t.Fatalf("result=%s want suppressed_daily_limit", ev2.Result)
	}
}

func TestTriggerRuleManualNotification(t *testing.T) {
	p, edge := setupConfirmPlanner(t)
	rule := models.AutomationRule{
		Name:    "通知规则",
		Enabled: true,
		TriggerType: models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID,
		ActionType: models.AutomationActionNotification,
		ActionLevel: models.AlertLevelInfo,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	ev, err := p.TriggerRule(context.Background(), rule.ID, 7, "127.0.0.1")
	if err != nil {
		t.Fatalf("notification trigger failed: %v", err)
	}
	if ev.Result != models.AutomationResultNotification {
		t.Fatalf("result=%s want notification", ev.Result)
	}
}

// TestHandleTriggerAutoSystemActorSkipsMediumConfirmation 集成断言:
// planner 自动触发 (executeDeviceAction) 走 ActorKind=system 调 commandexec.Create,
// 对 medium risk 动作跳过 confirmation; command_executions 落库 + audit 落 actor_type=system。
// 用 gpio_set (periph transport, medium) — ChannelCmdV2 transport 只接 low risk, 用 channel 动作过不了 transport gate。
func TestHandleTriggerAutoSystemActorSkipsMediumConfirmation(t *testing.T) {
	p, edge := setupConfirmPlanner(t)

	// 配置 gpio_configs 行使 periph gate 通过
	if err := p.db.Create(&models.GPIOConfig{
		NodeID: edge.NodeID, Pin: 5, Direction: 1, Enabled: true,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 规则: 自动触发 + device_action + medium risk (gpio_set) + require_confirmed=false
	// 注意: periph 动作的 ActionDeviceID = node.id (resolveActionTarget 的 periph 分支)
	rule := models.AutomationRule{
		Name:                "auto-medium-skip-confirm",
		Enabled:             true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: edge.ID,
		TriggerSensorName:   "humidity",
		TriggerComparator:   "gt",
		TriggerThreshold:    80,
		CooldownSec:         0,
		RequireConfirmed:    false,
		ActionType:          models.AutomationActionDeviceAction,
		ActionDeviceID:      edge.ID, // 在 setupConfirmPlanner 里 edge.ID == node.ID 是巧合对齐
		ActionID:            "gpio_set",
		ActionParamsJSON:    `{"pin":5,"level":1}`,
	}
	if err := p.db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	// 触发: planner.HandleTrigger
	p.HandleTrigger(TriggerEvent{Rule: rule, Value: 90, At: time.Now()})

	// 1) automation_events 落 executed
	var ev models.AutomationEvent
	if err := p.db.Where("rule_id = ?", rule.ID).Order("id DESC").First(&ev).Error; err != nil {
		t.Fatalf("event missing: %v", err)
	}
	if ev.Result != models.AutomationResultExecuted {
		t.Fatalf("event result = %q, want %q (detail: %s)", ev.Result, models.AutomationResultExecuted, ev.Detail)
	}
	if ev.CommandID == "" {
		t.Fatal("executed event must carry command_id")
	}

	// 2) command_executions 落库, ActorUserID=systemActorID (900)
	var exec models.CommandExecution
	if err := p.db.Where("command_id = ?", ev.CommandID).First(&exec).Error; err != nil {
		t.Fatalf("command execution missing: %v", err)
	}
	if exec.ActorUserID != 900 {
		t.Fatalf("actor_user_id = %d, want 900 (system actor)", exec.ActorUserID)
	}
	if exec.ActionID != "gpio_set" {
		t.Fatalf("action_id = %q, want gpio_set", exec.ActionID)
	}

	// 3) 审计: ActorType=system (而非默认 user)
	var audit models.SecurityAuditEvent
	if err := p.db.Where("request_id = ?", ev.CommandID).First(&audit).Error; err != nil {
		t.Fatalf("audit row missing: %v", err)
	}
	if audit.ActorType != "system" {
		t.Fatalf("audit actor_type = %q, want system", audit.ActorType)
	}
}
