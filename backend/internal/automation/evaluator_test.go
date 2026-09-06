package automation

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/parser"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.AutomationRule{}, &models.AutomationEvent{},
		&models.Notification{}, &models.EdgeDevice{}, &models.LogicalDevice{},
		&models.CommandExecution{}, &models.User{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gin.SetMode(gin.TestMode)
	return db
}

// captureHandler 记录 HandleTrigger 调用 (Planner 桩)。
type captureHandler struct {
	events []TriggerEvent
}

func (h *captureHandler) HandleTrigger(ev TriggerEvent) {
	h.events = append(h.events, ev)
}

func automationRule(id uint, sensor string, cmp string, threshold float64, durationSec int) models.AutomationRule {
	return models.AutomationRule{
		ID:                  id,
		Name:                "测试策略",
		Enabled:             true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: 1,
		TriggerSensorName:   sensor,
		TriggerComparator:   cmp,
		TriggerThreshold:    threshold,
		TriggerDurationSec:  durationSec,
		CooldownSec:         300,
		ActionType:          models.AutomationActionNotification,
		ActionLevel:         models.AlertLevelInfo,
	}
}

func illuminanceField(v float64) []parser.Field {
	return []parser.Field{{Name: "illuminance", Value: v}}
}

// 直通触发: DurationSec=0, 单点超阈值即 armed→triggered + HandleTrigger 一次。
func TestEvaluateImmediateTrigger(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := automationRule(1, "illuminance", "gt", 500, 0)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	at := time.Now()
	ev.Evaluate(1, illuminanceField(600), at)
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger, got %d", len(h.events))
	}
	if h.events[0].Rule.ID != 1 || h.events[0].Value != 600 {
		t.Fatalf("trigger event mismatch: %+v", h.events[0])
	}
}

// 设备不匹配: TriggerEdgeDeviceID=1 但上报设备=2 且无逻辑身份, 不触发。
func TestEvaluateDeviceMismatch(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := automationRule(1, "illuminance", "gt", 500, 0)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	ev.Evaluate(2, illuminanceField(600), time.Now())
	if len(h.events) != 0 {
		t.Fatalf("expect 0 trigger for mismatched device, got %d", len(h.events))
	}
}

// 字段缺失: 本批无 illuminance 字段, 不触发也不影响窗口。
func TestEvaluateFieldMissing(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := automationRule(1, "illuminance", "gt", 500, 0)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 99}}, time.Now())
	if len(h.events) != 0 {
		t.Fatalf("expect 0 trigger for missing field, got %d", len(h.events))
	}
}

// 冷却抑制: 触发后冷却期内再满足不重复触发; 冷却到期回 armed 可再触发。
func TestEvaluateCooldownSuppression(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := automationRule(1, "illuminance", "gt", 500, 0)
	r.CooldownSec = 60
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	t0 := time.Now()
	ev.Evaluate(1, illuminanceField(600), t0)                     // 触发 #1
	ev.Evaluate(1, illuminanceField(700), t0.Add(10*time.Second)) // 冷却期内, 抑制
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger (cooldown suppress), got %d", len(h.events))
	}
	ev.Evaluate(1, illuminanceField(700), t0.Add(61*time.Second)) // 冷却到期, 触发 #2
	if len(h.events) != 2 {
		t.Fatalf("expect 2 trigger after cooldown expiry, got %d", len(h.events))
	}
}

// 窗口防抖: DurationSec>0 需窗口内连续满足才触发。
func TestEvaluateWindowDebounce(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := automationRule(1, "illuminance", "gt", 500, 30)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	t0 := time.Now()
	// 只有 1 个点满足, 窗口跨度 0 < 30s, 不触发
	ev.Evaluate(1, illuminanceField(600), t0)
	if len(h.events) != 0 {
		t.Fatalf("expect 0 trigger (window too short), got %d", len(h.events))
	}
	// 第二个点 t0+31s, 窗口跨度 31s ≥ 30s 且连续满足, 触发
	ev.Evaluate(1, illuminanceField(700), t0.Add(31*time.Second))
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger after window satisfied, got %d", len(h.events))
	}
}

// 附加条件: conditions_json 全部 AND, 任一不满足不触发。
func TestEvaluateConditionsAND(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := automationRule(1, "illuminance", "gt", 500, 0)
	r.ConditionsJSON = `[{"sensor_name":"temperature","comparator":"lt","threshold":30}]`
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	at := time.Now()
	// illuminance 满足但 temperature=35 ≥ 30 (条件不满足), 不触发
	fields := []parser.Field{{Name: "illuminance", Value: 600}, {Name: "temperature", Value: 35}}
	ev.Evaluate(1, fields, at)
	if len(h.events) != 0 {
		t.Fatalf("expect 0 trigger (condition unmet), got %d", len(h.events))
	}
	// temperature=25 < 30 (条件满足), 触发
	fields2 := []parser.Field{{Name: "illuminance", Value: 600}, {Name: "temperature", Value: 25}}
	ev.Evaluate(1, fields2, at.Add(time.Second))
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger (condition met), got %d", len(h.events))
	}
}

// 禁用规则不参与求值。
func TestEvaluateDisabledRuleSkipped(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := automationRule(1, "illuminance", "gt", 500, 0)
	r.Enabled = false
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	ev.Evaluate(1, illuminanceField(600), time.Now())
	if len(h.events) != 0 {
		t.Fatalf("expect 0 trigger for disabled rule, got %d", len(h.events))
	}
}

// H1 回归: 重启后冷却状态重建 — 库内已有 executed 历史行的规则,
// NewEvaluator 后冷却期内同条件不重复触发, 冷却到期可再触发。
func TestRebuildCooldownsOnRestart(t *testing.T) {
	db := newTestDB(t)
	r := automationRule(1, "illuminance", "gt", 500, 0)
	r.CooldownSec = 300
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}

	t0 := time.Now()
	// 模拟重启前的 executed 审计行 (t0 时刻触发过)。
	v := 600.0
	if err := db.Create(&models.AutomationEvent{
		RuleID:       r.ID,
		TriggeredAt:  t0,
		TriggerValue: &v,
		Result:       models.AutomationResultExecuted,
		CreatedAt:    t0,
	}).Error; err != nil {
		t.Fatal(err)
	}

	// 重启: 新建 Evaluator 应回填 triggered, 冷却期内同条件不触发。
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	ev.Evaluate(1, illuminanceField(700), t0.Add(10*time.Second))
	if len(h.events) != 0 {
		t.Fatalf("expect 0 trigger (cooldown rebuilt after restart), got %d", len(h.events))
	}

	// 冷却到期 (t0+301s) 回 armed, 可再触发。
	ev.Evaluate(1, illuminanceField(700), t0.Add(301*time.Second))
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger after rebuilt cooldown expiry, got %d", len(h.events))
	}
}

// H1 边界: 无 executed 历史行的规则, NewEvaluator 不重建冷却, 满足即触发。
func TestRebuildCooldownsNoHistory(t *testing.T) {
	db := newTestDB(t)
	r := automationRule(1, "illuminance", "gt", 500, 0)
	r.CooldownSec = 300
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	// 只有 pending_confirm 历史行, 不是 executed — 不应回填冷却。
	if err := db.Create(&models.AutomationEvent{
		RuleID:      r.ID,
		TriggeredAt: time.Now(),
		Result:      models.AutomationResultPendingConfirm,
		CreatedAt:   time.Now(),
	}).Error; err != nil {
		t.Fatal(err)
	}

	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	ev.Evaluate(1, illuminanceField(600), time.Now())
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger (no executed history, no cooldown), got %d", len(h.events))
	}
}

// H2 回归: 规则匹配语义不变 — 逻辑身份命中 (resolveLogicalID 移出 RLock 后)。
// 规则 TriggerEdgeDeviceID=逻辑设备 ID, 上报设备=绑定该逻辑身份的边缘设备, 应触发。
func TestEvaluateLogicalIdentityMatch(t *testing.T) {
	db := newTestDB(t)
	ld := models.LogicalDevice{IdentityKey: "test:ld-1", Name: "逻辑设备1"}
	if err := db.Create(&ld).Error; err != nil {
		t.Fatal(err)
	}
	ed := models.EdgeDevice{HardwareID: "edge-1", Name: "边缘设备1", LogicalDeviceID: &ld.ID}
	if err := db.Create(&ed).Error; err != nil {
		t.Fatal(err)
	}

	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := automationRule(1, "illuminance", "gt", 500, 0)
	r.TriggerEdgeDeviceID = ld.ID // 指向逻辑身份
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	ev.Evaluate(ed.ID, illuminanceField(600), time.Now())
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger via logical identity match, got %d", len(h.events))
	}
}

// H2 回归: TriggerEdgeDeviceID=0 任意设备匹配 + 不匹配设备仍被过滤。
func TestEvaluateMatchSemanticsUnchanged(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)

	// 规则1: TriggerEdgeDeviceID=0 → 任意设备命中。
	r0 := automationRule(1, "illuminance", "gt", 500, 0)
	r0.TriggerEdgeDeviceID = 0
	if err := db.Create(&r0).Error; err != nil {
		t.Fatal(err)
	}
	// 规则2: TriggerEdgeDeviceID=99 → 设备 2 上报不命中。
	r2 := automationRule(2, "illuminance", "gt", 500, 0)
	r2.TriggerEdgeDeviceID = 99
	if err := db.Create(&r2).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	ev.Evaluate(2, illuminanceField(600), time.Now())
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger (only wildcard rule matches), got %d", len(h.events))
	}
	if h.events[0].Rule.ID != 1 {
		t.Fatalf("expect wildcard rule 1 to fire, got rule %d", h.events[0].Rule.ID)
	}
}

// F3 回归: 冷却命中落 suppressed_cooldown 审计事件 (防抖可观测)。
func TestCooldownSuppressionRecordsEvent(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := automationRule(1, "illuminance", "gt", 500, 0)
	r.CooldownSec = 60
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	t0 := time.Now()
	ev.Evaluate(1, illuminanceField(600), t0)                     // 触发 #1
	ev.Evaluate(1, illuminanceField(700), t0.Add(10*time.Second)) // 冷却命中 → suppressed_cooldown
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger, got %d", len(h.events))
	}

	var evs []models.AutomationEvent
	if err := db.Where("rule_id = ? AND result = ?",
		r.ID, models.AutomationResultSuppressedCooldown).Find(&evs).Error; err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("expect 1 suppressed_cooldown event, got %d", len(evs))
	}
	got := evs[0]
	if got.TriggerValue == nil || *got.TriggerValue != 700 {
		t.Fatalf("suppressed event trigger_value mismatch: %+v", got.TriggerValue)
	}
	if !got.TriggeredAt.Equal(t0.Add(10 * time.Second)) {
		t.Fatalf("suppressed event triggered_at mismatch: %v", got.TriggeredAt)
	}
	if got.Detail == "" {
		t.Fatalf("suppressed event detail empty")
	}
}

// F3 节流回归 (主 Agent 复审补): 同一冷却窗内高频命中只落首条 suppressed_cooldown,
// 防 Evaluate 每帧上报刷量 (consumers_heavy.go:337)。冷却到期回 armed 后新窗可再落。
func TestCooldownSuppressionThrottledPerWindow(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := automationRule(1, "illuminance", "gt", 500, 0)
	r.CooldownSec = 60
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	t0 := time.Now()
	ev.Evaluate(1, illuminanceField(600), t0)                     // 触发 #1 → 进入冷却窗 [t0, t0+60s)
	ev.Evaluate(1, illuminanceField(700), t0.Add(10*time.Second)) // 同窗命中 1
	ev.Evaluate(1, illuminanceField(800), t0.Add(20*time.Second)) // 同窗命中 2
	ev.Evaluate(1, illuminanceField(900), t0.Add(30*time.Second)) // 同窗命中 3
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger, got %d", len(h.events))
	}

	var evs []models.AutomationEvent
	if err := db.Where("rule_id = ? AND result = ?",
		r.ID, models.AutomationResultSuppressedCooldown).Find(&evs).Error; err != nil {
		t.Fatal(err)
	}
	if len(evs) != 1 {
		t.Fatalf("same cooldown window must record only 1 suppressed_cooldown, got %d", len(evs))
	}
	// 首条是被抑制的第一次命中 (trigger_value=700), 后续 800/900 被节流
	if evs[0].TriggerValue == nil || *evs[0].TriggerValue != 700 {
		t.Fatalf("first suppressed event should capture value 700, got %+v", evs[0].TriggerValue)
	}
}

func TestInvalidateReloads(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := automationRule(1, "illuminance", "gt", 500, 0)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	ev.Evaluate(1, illuminanceField(600), time.Now())
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger, got %d", len(h.events))
	}

	// 删除规则 + Invalidate → 冷却状态也被清理, 不再触发
	db.Delete(&r)
	ev.Invalidate()
	ev.Evaluate(1, illuminanceField(600), time.Now().Add(400*time.Second))
	if len(h.events) != 1 {
		t.Fatalf("expect still 1 trigger after delete+invalidate, got %d", len(h.events))
	}
}

// =====================================================================
// B1: time_window 求值器测试
// =====================================================================

func timeWindowRule(id uint, start, end, edge string) models.AutomationRule {
	return models.AutomationRule{
		ID:                 id,
		Name:               "时间窗口策略",
		Enabled:            true,
		TriggerType:        models.AutomationTriggerTimeWindow,
		TriggerWindowStart: start,
		TriggerWindowEnd:   end,
		TriggerWindowEdge:  edge,
		CooldownSec:        300,
		ActionType:         models.AutomationActionNotification,
		ActionLevel:        models.AlertLevelInfo,
	}
}

// enter 触发: 规则窗口 08:00-18:00, 从窗口外进入窗口时触发。
func TestTimeWindowEnterTrigger(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := timeWindowRule(1, "08:00", "18:00", models.AutomationWindowEnter)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	// 构造今天 07:59 的时间 (窗口外)。
	now := time.Now()
	outside := time.Date(now.Year(), now.Month(), now.Day(), 7, 59, 0, 0, now.Location())
	ev.evalTimeWindows(outside)
	if len(h.events) != 0 {
		t.Fatalf("expect 0 trigger outside window, got %d", len(h.events))
	}

	// 08:00 进入窗口 → 触发。
	inside := time.Date(now.Year(), now.Month(), now.Day(), 8, 0, 0, 0, now.Location())
	ev.evalTimeWindows(inside)
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger on window enter, got %d", len(h.events))
	}
	if h.events[0].WindowEdge != models.AutomationWindowEnter {
		t.Fatalf("expect WindowEdge=enter, got %q", h.events[0].WindowEdge)
	}
}

// exit 触发: 从窗口内离开窗口时触发。
func TestTimeWindowExitTrigger(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := timeWindowRule(1, "08:00", "18:00", models.AutomationWindowExit)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	now := time.Now()
	// 先进窗口 (记录状态)。
	inside := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	ev.evalTimeWindows(inside)
	if len(h.events) != 0 {
		t.Fatalf("expect 0 trigger inside window (exit edge), got %d", len(h.events))
	}

	// 18:00 离开窗口 → 触发。
	outside := time.Date(now.Year(), now.Month(), now.Day(), 18, 0, 0, 0, now.Location())
	ev.evalTimeWindows(outside)
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger on window exit, got %d", len(h.events))
	}
	if h.events[0].WindowEdge != models.AutomationWindowExit {
		t.Fatalf("expect WindowEdge=exit, got %q", h.events[0].WindowEdge)
	}
}

// inside 触发: 窗口内每次 tick 都触发 (受 cooldown 抑制)。
func TestTimeWindowInsideTrigger(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := timeWindowRule(1, "08:00", "18:00", models.AutomationWindowInside)
	r.CooldownSec = 60
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	now := time.Now()
	t0 := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	// 窗口内第一次 tick → 触发。
	ev.evalTimeWindows(t0)
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger inside window, got %d", len(h.events))
	}
	if h.events[0].WindowEdge != models.AutomationWindowInside {
		t.Fatalf("expect WindowEdge=inside, got %q", h.events[0].WindowEdge)
	}
	// 冷却期内第二次 tick → 抑制。
	ev.evalTimeWindows(t0.Add(30 * time.Second))
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger (cooldown suppress), got %d", len(h.events))
	}
	// 冷却到期第三次 tick → 再触发。
	ev.evalTimeWindows(t0.Add(61 * time.Second))
	if len(h.events) != 2 {
		t.Fatalf("expect 2 trigger after cooldown expiry, got %d", len(h.events))
	}
}

// 跨零点窗口: 19:00-06:00 正确求值。
func TestTimeWindowCrossMidnight(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := timeWindowRule(1, "19:00", "06:00", models.AutomationWindowEnter)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	now := time.Now()
	// 20:00 在窗口内 (跨零点: 19:00-06:00)。
	evening := time.Date(now.Year(), now.Month(), now.Day(), 20, 0, 0, 0, now.Location())
	inside, err := isInWindow("19:00", "06:00", evening)
	if err != nil {
		t.Fatal(err)
	}
	if !inside {
		t.Fatal("20:00 should be inside 19:00-06:00 window")
	}

	// 03:00 也在窗口内。
	earlyMorning := time.Date(now.Year(), now.Month(), now.Day(), 3, 0, 0, 0, now.Location())
	inside, err = isInWindow("19:00", "06:00", earlyMorning)
	if err != nil {
		t.Fatal(err)
	}
	if !inside {
		t.Fatal("03:00 should be inside 19:00-06:00 window")
	}

	// 12:00 不在窗口内。
	noon := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	inside, err = isInWindow("19:00", "06:00", noon)
	if err != nil {
		t.Fatal(err)
	}
	if inside {
		t.Fatal("12:00 should not be inside 19:00-06:00 window")
	}
}

// 冷却抑制: time_window 触发后冷却期内不再触发。
func TestTimeWindowCooldownSuppression(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := timeWindowRule(1, "08:00", "18:00", models.AutomationWindowEnter)
	r.CooldownSec = 300
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	now := time.Now()
	// 先进窗口触发一次。
	inside := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	ev.evalTimeWindows(inside)
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger, got %d", len(h.events))
	}

	// 出窗口再进窗口 (冷却期内) → 不触发。
	outside := time.Date(now.Year(), now.Month(), now.Day(), 19, 0, 0, 0, now.Location())
	ev.evalTimeWindows(outside)
	inside2 := time.Date(now.Year(), now.Month(), now.Day(), 20, 0, 0, 0, now.Location())
	// 重置窗口状态 (模拟出窗后再进)。
	ev.mu.Lock()
	ev.windowStates[1] = false
	ev.mu.Unlock()
	ev.evalTimeWindows(inside2)
	if len(h.events) != 1 {
		t.Fatalf("expect 1 trigger (cooldown suppress), got %d", len(h.events))
	}
}

// 重启后窗口状态: 清空后首次 tick 保守处理 (enter 规则首次 tick 若在窗口内则触发)。
func TestTimeWindowRestartConservative(t *testing.T) {
	db := newTestDB(t)
	r := timeWindowRule(1, "08:00", "18:00", models.AutomationWindowEnter)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}

	// 第一次启动: 在窗口内触发一次。
	h1 := &captureHandler{}
	ev1 := NewEvaluator(db, h1)
	now := time.Now()
	inside := time.Date(now.Year(), now.Month(), now.Day(), 12, 0, 0, 0, now.Location())
	ev1.evalTimeWindows(inside)
	if len(h1.events) != 1 {
		t.Fatalf("expect 1 trigger on first start, got %d", len(h1.events))
	}

	// 模拟重启: 新建 Evaluator (windowStates 清空), 再次在窗口内 → 保守触发。
	h2 := &captureHandler{}
	ev2 := NewEvaluator(db, h2)
	ev2.evalTimeWindows(inside)
	if len(h2.events) != 1 {
		t.Fatalf("expect 1 trigger after restart (conservative enter), got %d", len(h2.events))
	}
}

// time_window 规则不走 sensor_threshold 的 Evaluate 路径。
func TestTimeWindowNotInSensorEvaluate(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	r := timeWindowRule(1, "08:00", "18:00", models.AutomationWindowEnter)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	// Evaluate 只处理 sensor_threshold 规则, time_window 规则不应被触发。
	ev.Evaluate(1, illuminanceField(600), time.Now())
	if len(h.events) != 0 {
		t.Fatalf("expect 0 trigger from Evaluate for time_window rule, got %d", len(h.events))
	}
}

// isInWindow 单元测试: 非跨零点 + 跨零点 + 边界值。
func TestIsInWindow(t *testing.T) {
	loc := time.Local
	tests := []struct {
		name  string
		start string
		end   string
		now   time.Time
		want  bool
	}{
		{"non-cross inside", "08:00", "18:00", time.Date(2025, 1, 1, 12, 0, 0, 0, loc), true},
		{"non-cross before", "08:00", "18:00", time.Date(2025, 1, 1, 7, 59, 0, 0, loc), false},
		{"non-cross at start", "08:00", "18:00", time.Date(2025, 1, 1, 8, 0, 0, 0, loc), true},
		{"non-cross at end", "08:00", "18:00", time.Date(2025, 1, 1, 18, 0, 0, 0, loc), false},
		{"cross midnight evening", "19:00", "06:00", time.Date(2025, 1, 1, 20, 0, 0, 0, loc), true},
		{"cross midnight early morning", "19:00", "06:00", time.Date(2025, 1, 1, 3, 0, 0, 0, loc), true},
		{"cross midnight noon", "19:00", "06:00", time.Date(2025, 1, 1, 12, 0, 0, 0, loc), false},
		{"cross midnight at start", "19:00", "06:00", time.Date(2025, 1, 1, 19, 0, 0, 0, loc), true},
		{"cross midnight at end", "19:00", "06:00", time.Date(2025, 1, 1, 6, 0, 0, 0, loc), false},
		{"same start end", "08:00", "08:00", time.Date(2025, 1, 1, 8, 0, 0, 0, loc), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := isInWindow(tt.start, tt.end, tt.now)
			if err != nil {
				t.Fatal(err)
			}
			if got != tt.want {
				t.Fatalf("isInWindow(%q, %q, %v) = %v, want %v", tt.start, tt.end, tt.now, got, tt.want)
			}
		})
	}
}
