package alert

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
	if err := db.AutoMigrate(&models.AlertRule{}, &models.AlertEvent{}, &models.Notification{}, &models.EdgeDevice{}, &models.LogicalDevice{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	gin.SetMode(gin.TestMode)
	return db
}

func newTestEvaluator(t *testing.T, db *gorm.DB, broadcast func(string, any)) *Evaluator {
	t.Helper()
	return NewEvaluator(db, broadcast)
}

func rule(id uint, sensor string, cmp string, threshold float64, durationSec int) models.AlertRule {
	return models.AlertRule{
		ID:          id,
		Name:        "测试规则",
		TargetType:  models.AlertTargetEdgeDevice,
		TargetID:    1,
		SensorName:  sensor,
		Comparator:  cmp,
		Threshold:   threshold,
		DurationSec: durationSec,
		SilenceSec:  defaultSilenceSec,
		Level:       models.AlertLevelWarning,
		Enabled:     true,
	}
}

func tempFields(v float64) []parser.Field { return nil }

// 直通触发: DurationSec=0, 单点超阈值即 firing + Notification + WS 广播。
func TestEvaluateImmediateFire(t *testing.T) {
	db := newTestDB(t)
	var broadcasts []string
	ev := newTestEvaluator(t, db, func(eventType string, payload any) {
		broadcasts = append(broadcasts, eventType)
	})
	r := rule(1, "temperature", "gt", 50, 0)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	now := time.Now()
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 60}}, now)

	var events []models.AlertEvent
	db.Where("rule_id = ?", 1).Find(&events)
	if len(events) != 1 || events[0].State != stateFiring {
		t.Fatalf("expected 1 firing event, got %+v", events)
	}
	if events[0].Value != 60 || events[0].FiredAt == nil {
		t.Fatalf("unexpected firing event: %+v", events[0])
	}
	var notifications []models.Notification
	db.Where("source = ?", "alert_rule").Find(&notifications)
	if len(notifications) != 1 || notifications[0].Type != "warning" || notifications[0].SourceID != "1" {
		t.Fatalf("expected 1 warning notification, got %+v", notifications)
	}
	if len(broadcasts) != 1 || broadcasts[0] != "notification" {
		t.Fatalf("expected 1 ws broadcast, got %v", broadcasts)
	}

	// 抑制: firing 未 resolved 不重复发事件。
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 61}}, now.Add(time.Second))
	db.Where("rule_id = ?", 1).Find(&events)
	if len(events) != 1 {
		t.Fatalf("expected suppression while firing, got %d events", len(events))
	}
	if len(broadcasts) != 1 {
		t.Fatalf("expected no extra broadcast while firing, got %d", len(broadcasts))
	}
}

// 恢复: 任一不满足值出现 → resolved 事件 + Notification + WS。
func TestEvaluateResolve(t *testing.T) {
	db := newTestDB(t)
	ev := newTestEvaluator(t, db, nil)
	r := rule(2, "temperature", "gt", 50, 0)
	db.Create(&r)
	ev.LoadRules()

	now := time.Now()
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 60}}, now)
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 40}}, now.Add(time.Second))

	var events []models.AlertEvent
	db.Where("rule_id = ?", 2).Order("id ASC").Find(&events)
	if len(events) != 1 {
		t.Fatalf("expected single-row episode (firing→resolved), got %+v", events)
	}
	if events[0].State != stateResolved || events[0].ResolvedAt == nil || events[0].Value != 40 {
		t.Fatalf("unexpected resolved event: %+v", events[0])
	}
	if events[0].FiredAt == nil {
		t.Fatalf("resolved event must keep fired_at: %+v", events[0])
	}
	// 恢复后再次满足可重新触发 (静默窗口只抑制通知, 不抑制新事件判定)。
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 70}}, now.Add(2*time.Second))
	db.Where("rule_id = ?", 2).Order("id ASC").Find(&events)
	if len(events) != 2 {
		t.Fatalf("expected re-fire after resolve as new episode, got %+v", events)
	}
	if events[1].State != stateFiring || events[1].Value != 70 {
		t.Fatalf("unexpected second episode: %+v", events[1])
	}
}

// 持续窗口: DurationSec>0 需连续满足达到时长才触发; 中途回落则不触发。
func TestEvaluateDurationWindow(t *testing.T) {
	db := newTestDB(t)
	ev := newTestEvaluator(t, db, nil)
	r := rule(3, "humidity", "gte", 80, 10)
	db.Create(&r)
	ev.LoadRules()

	base := time.Now()
	// t=0..9s 连续满足但未达 10s — 不触发。
	for s := 0; s < 10; s++ {
		ev.Evaluate(1, []parser.Field{{Name: "humidity", Value: 85}}, base.Add(time.Duration(s)*time.Second))
	}
	var count int64
	db.Model(&models.AlertEvent{}).Where("rule_id = ?", 3).Count(&count)
	if count != 0 {
		t.Fatalf("expected no fire before duration met, got %d", count)
	}
	// t=10s 达到时长 — 触发。
	ev.Evaluate(1, []parser.Field{{Name: "humidity", Value: 85}}, base.Add(10*time.Second))
	db.Model(&models.AlertEvent{}).Where("rule_id = ?", 3).Count(&count)
	if count != 1 {
		t.Fatalf("expected fire at duration met, got %d", count)
	}
}

// 持续窗口中途回落: 窗口被不满足样本打断, 不触发。
func TestEvaluateDurationBroken(t *testing.T) {
	db := newTestDB(t)
	ev := newTestEvaluator(t, db, nil)
	r := rule(4, "humidity", "gte", 80, 5)
	db.Create(&r)
	ev.LoadRules()

	base := time.Now()
	ev.Evaluate(1, []parser.Field{{Name: "humidity", Value: 85}}, base)
	ev.Evaluate(1, []parser.Field{{Name: "humidity", Value: 85}}, base.Add(3*time.Second))
	// 回落点清空窗口内连续性。
	ev.Evaluate(1, []parser.Field{{Name: "humidity", Value: 70}}, base.Add(4*time.Second))
	ev.Evaluate(1, []parser.Field{{Name: "humidity", Value: 85}}, base.Add(6*time.Second))

	var count int64
	db.Model(&models.AlertEvent{}).Where("rule_id = ?", 4).Count(&count)
	if count != 0 {
		t.Fatalf("expected no fire after broken window, got %d", count)
	}
}

// 静默窗口: SilenceSec 内重复满足不重复通知; 到期后更新 AlertEvent 并再通知。
func TestEvaluateSilenceWindow(t *testing.T) {
	db := newTestDB(t)
	ev := newTestEvaluator(t, db, nil)
	r := rule(5, "temperature", "gt", 50, 0)
	r.SilenceSec = 60
	db.Create(&r)
	ev.LoadRules()

	base := time.Now()
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 60}}, base)
	// 30s 后仍满足 — 静默期内, 不通知。
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 65}}, base.Add(30*time.Second))
	var notifications []models.Notification
	db.Where("source = ?", "alert_rule").Find(&notifications)
	if len(notifications) != 1 {
		t.Fatalf("expected silence suppression, got %d notifications", len(notifications))
	}
	// 90s 后仍满足 — 超出静默窗口, 更新 AlertEvent + 再通知。
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 66}}, base.Add(90*time.Second))
	db.Where("source = ?", "alert_rule").Find(&notifications)
	if len(notifications) != 2 {
		t.Fatalf("expected re-notify after silence window, got %d", len(notifications))
	}
	var ev_ models.AlertEvent
	db.Where("rule_id = ? AND state = ?", 5, stateFiring).First(&ev_)
	if ev_.Value != 66 {
		t.Fatalf("expected firing event value updated to 66, got %v", ev_.Value)
	}
	if ev_.NotifiedAt == nil || !ev_.NotifiedAt.After(base.Add(60*time.Second)) {
		t.Fatalf("expected notified_at refreshed, got %+v", ev_.NotifiedAt)
	}
}

// 规则失效重载: Invalidate 后新增/禁用规则即时生效 (v0.3 修正)。
func TestInvalidateReloadsRules(t *testing.T) {
	db := newTestDB(t)
	ev := newTestEvaluator(t, db, nil)
	now := time.Now()

	// 缓存为空时求值不产生事件。
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 99}}, now)
	var count int64
	db.Model(&models.AlertEvent{}).Count(&count)
	if count != 0 {
		t.Fatalf("expected no events with empty cache, got %d", count)
	}

	r := rule(6, "temperature", "gt", 50, 0)
	db.Create(&r)
	ev.Invalidate()
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 99}}, now)
	db.Model(&models.AlertEvent{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected event after invalidate+reload, got %d", count)
	}

	// 禁用规则后 Invalidate 即失效。
	db.Model(&models.AlertRule{}).Where("id = ?", 6).Update("enabled", false)
	ev.Invalidate()
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 40}}, now.Add(time.Second)) // resolved
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 99}}, now.Add(2*time.Second))
	db.Model(&models.AlertEvent{}).Where("state = ?", stateFiring).Count(&count)
	if count != 1 {
		t.Fatalf("disabled rule must not fire again, got %d firing events", count)
	}
}

// 目标匹配: 仅命中目标设备的字段求值; logical_device 规则经合并链解析。
func TestEvaluateTargetMatching(t *testing.T) {
	db := newTestDB(t)
	logical := models.LogicalDevice{Name: "逻辑设备"}
	db.Create(&logical)
	dev := models.EdgeDevice{Name: "边缘设备", NodeID: "N1", ChannelID: 1, LogicalDeviceID: &logical.ID}
	db.Create(&dev)

	ev := newTestEvaluator(t, db, nil)
	re := rule(7, "voltage", "lt", 10, 0)
	re.TargetType = models.AlertTargetLogicalDevice
	re.TargetID = logical.ID
	db.Create(&re)
	ev.LoadRules()

	now := time.Now()
	// 其他设备不触发。
	ev.Evaluate(dev.ID+100, []parser.Field{{Name: "voltage", Value: 5}}, now)
	var count int64
	db.Model(&models.AlertEvent{}).Count(&count)
	if count != 0 {
		t.Fatalf("other device must not trigger, got %d", count)
	}
	// 本设备 (经逻辑身份链) 触发。
	ev.Evaluate(dev.ID, []parser.Field{{Name: "voltage", Value: 5}}, now)
	db.Model(&models.AlertEvent{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected logical-device rule to fire, got %d", count)
	}
}

// compare 全比较符覆盖。
func TestCompare(t *testing.T) {
	cases := []struct {
		op    string
		v, th float64
		want  bool
	}{
		{"gt", 51, 50, true}, {"gt", 50, 50, false},
		{"gte", 50, 50, true}, {"gte", 49.9, 50, false},
		{"lt", 49, 50, true}, {"lt", 50, 50, false},
		{"lte", 50, 50, true}, {"lte", 50.1, 50, false},
		{"eq", 50, 50, true}, {"eq", 49, 50, false},
		{"neq", 49, 50, true}, {"neq", 50, 50, false},
		{"bogus", 51, 50, false},
	}
	for _, c := range cases {
		if got := compare(c.op, c.v, c.th); got != c.want {
			t.Errorf("compare(%q,%v,%v)=%v want %v", c.op, c.v, c.th, got, c.want)
		}
	}
}

// NotificationType 级别映射 (§5.1.3)。
func TestNotificationTypeMapping(t *testing.T) {
	if got := models.NotificationType(models.AlertLevelCritical); got != "error" {
		t.Errorf("critical→error, got %q", got)
	}
	if got := models.NotificationType(models.AlertLevelWarning); got != "warning" {
		t.Errorf("warning→warning, got %q", got)
	}
	if got := models.NotificationType(models.AlertLevelInfo); got != "info" {
		t.Errorf("info→info, got %q", got)
	}
}

// ── 负债 D-3: WS 广播载荷必须是通知实体 (与 GET /notifications 列表项同形) ──
//
// 契约: 载荷顶层含 id/type/title/description/message/source/source_id/read/
// created_at; 告警领域详情 (rule_id/sensor_name/threshold/comparator) 收进嵌套
// detail, 不平铺 —— 否则前端无法把推送插入通知列表 (拿不到 id, 无法标记已读)。
// 铁律: Create 失败 (拿不到 ID) 时不得广播。
func TestNotifyBroadcastPayloadIsNotificationEntity(t *testing.T) {
	db := newTestDB(t)
	type captured struct {
		eventType string
		payload   map[string]any
	}
	var got []captured
	ev := newTestEvaluator(t, db, func(eventType string, payload any) {
		m, ok := payload.(gin.H)
		if !ok {
			t.Fatalf("payload type = %T, want gin.H", payload)
		}
		got = append(got, captured{eventType: eventType, payload: m})
	})
	r := rule(7, "temperature", "gt", 50, 0)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	ev.Evaluate(1, []parser.Field{{Name: "temperature", Value: 60}}, time.Now())

	if len(got) != 1 {
		t.Fatalf("broadcasts = %d, want 1", len(got))
	}
	if got[0].eventType != "notification" {
		t.Fatalf("event type = %q, want notification", got[0].eventType)
	}
	p := got[0].payload

	// 通知实体字段必须存在且与落库行一致。
	var n models.Notification
	if err := db.Where("source = ?", "alert_rule").First(&n).Error; err != nil {
		t.Fatalf("notification row missing: %v", err)
	}
	if p["id"] != n.ID || n.ID == 0 {
		t.Fatalf("payload id = %v, want %d (非零自增 ID)", p["id"], n.ID)
	}
	if p["type"] != n.Type {
		t.Fatalf("payload type = %v, want %v", p["type"], n.Type)
	}
	if p["title"] != n.Title || p["description"] != n.Description || p["message"] != n.Message {
		t.Fatalf("payload title/description/message 与落库行不一致: %+v vs %+v", p, n)
	}
	if p["source"] != "alert_rule" || p["source_id"] != "7" {
		t.Fatalf("payload source/source_id = %v/%v, want alert_rule/7", p["source"], p["source_id"])
	}
	if p["read"] != false {
		t.Fatalf("payload read = %v, want false", p["read"])
	}
	if _, ok := p["created_at"]; !ok {
		t.Fatalf("payload 缺 created_at: %+v", p)
	}
	// 领域详情必须嵌套, 不得平铺。
	for _, flat := range []string{"rule_id", "sensor_name", "threshold", "comparator", "level", "event_id"} {
		if _, exists := p[flat]; exists {
			t.Fatalf("领域字段 %q 不得平铺在载荷顶层: %+v", flat, p)
		}
	}
	detail, ok := p["detail"].(gin.H)
	if !ok {
		t.Fatalf("payload detail 缺失或类型错误: %T", p["detail"])
	}
	if detail["rule_id"] != r.ID || detail["sensor_name"] != "temperature" ||
		detail["threshold"] != r.Threshold || detail["comparator"] != "gt" {
		t.Fatalf("detail 内容不符: %+v", detail)
	}
	if detail["state"] != stateFiring {
		t.Fatalf("detail state = %v, want firing", detail["state"])
	}
}

// Create 失败时不得广播 (否则前端插入一条库里不存在的通知, 永远无法标记已读)。
func TestNotifyDoesNotBroadcastWhenCreateFails(t *testing.T) {
	db := newTestDB(t)
	var broadcasts int
	ev := newTestEvaluator(t, db, func(string, any) { broadcasts++ })
	r := rule(8, "temperature", "gt", 50, 0)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	// 触发前删表: alert_events 写入必失败 → onFire 提前 return; 再直接调 notify
	// 覆盖"Notification 落库失败"分支。
	if err := db.Migrator().DropTable(&models.Notification{}); err != nil {
		t.Fatal(err)
	}
	var evRow models.AlertEvent
	evRow.RuleID = r.ID
	ev.notify(r, evRow, 60, time.Now())
	if broadcasts != 0 {
		t.Fatalf("Create 失败时广播了 %d 次, want 0", broadcasts)
	}
}
