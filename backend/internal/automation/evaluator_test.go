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
	ev.Evaluate(1, illuminanceField(600), t0) // 触发 #1
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

// Invalidate 重载缓存: 删除规则后不再触发。
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
