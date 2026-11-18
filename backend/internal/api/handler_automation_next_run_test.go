package api

// 列表接口「下次执行时间」接线门禁 (2026-11-17)。
//
// 测的是**接线**而不是算法: 算法在 automation 包由 nextrun_test.go 覆盖,
// 这里只钉两件事 ——
//  1. evaluator 实现 automationNextRunner 时, 响应里确实带 next_run;
//  2. evaluator 为 nil 或未实现该能力时, 列表仍然 200 且不带该字段
//     (老客户端与既有单测替身都不能被这个附加能力打破)。

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ehome/backend/internal/automation"
	"ehome/backend/internal/models"
	"ehome/backend/internal/sun"
)

// fakeNextRunEvaluator 同时实现缓存失效与下次执行时间计算。
type fakeNextRunEvaluator struct {
	calls int
	last  int
}

func (f *fakeNextRunEvaluator) Invalidate() {}

func (f *fakeNextRunEvaluator) NextRuns(rules []models.AutomationRule, now time.Time) map[uint]models.NextRun {
	f.calls++
	f.last = len(rules)
	cst := time.FixedZone("CST", 8*3600)
	at := time.Date(2026, 11, 18, 17, 41, 35, 0, cst)
	out := make(map[uint]models.NextRun, len(rules))
	for i := range rules {
		out[rules[i].ID] = models.NextRun{
			Status:     models.NextRunScheduled,
			At:         &at,
			Timezone:   "Asia/Shanghai",
			Basis:      models.NextRunBasisSun,
			Detail:     "日落 17:41",
			ComputedAt: now,
		}
	}
	return out
}

// invalidateOnlyEvaluator 模拟既有单测替身: 只实现 Invalidate。
type invalidateOnlyEvaluator struct{}

func (invalidateOnlyEvaluator) Invalidate() {}

func TestListAutomationRulesAttachesNextRun(t *testing.T) {
	r, db := setupTestRouter(t)
	if err := db.AutoMigrate(&models.AutomationRule{}, &models.AutomationEvent{},
		&models.AutomationRuleAction{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	rule := models.AutomationRule{
		Name: "关闭BMS充电", Enabled: true,
		TriggerType:    models.AutomationTriggerSunEvent,
		TriggerSunKind: models.AutomationSunKindSunset,
		ActionType:     models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	ev := &fakeNextRunEvaluator{}
	registerAutomationRoutes(r.Group("/api/v1"), db, ev, nil, nil, nil)

	body := doGet(t, r, "/api/v1/automation-rules")
	var resp struct {
		Data []models.AutomationRule `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("规则数 = %d, want 1", len(resp.Data))
	}
	nr := resp.Data[0].NextRun
	if nr == nil {
		t.Fatal("响应必须带 next_run (evaluator 已实现 NextRuns)")
	}
	if nr.Status != models.NextRunScheduled || nr.At == nil {
		t.Fatalf("next_run = %+v, want scheduled + at", nr)
	}
	if nr.Basis != models.NextRunBasisSun {
		t.Fatalf("basis = %q, want sun", nr.Basis)
	}
	if ev.calls != 1 {
		t.Fatalf("NextRuns 调用次数 = %d, want 1 (整页只算一次, 不做 N+1)", ev.calls)
	}
	if ev.last != 1 {
		t.Fatalf("传入规则数 = %d, want 1", ev.last)
	}
}

func TestListAutomationRulesWithoutNextRunCapability(t *testing.T) {
	for name, ev := range map[string]automationEvaluator{
		"nil":             nil,
		"invalidate-only": invalidateOnlyEvaluator{},
	} {
		t.Run(name, func(t *testing.T) {
			r, db := setupTestRouter(t)
			if err := db.AutoMigrate(&models.AutomationRule{}, &models.AutomationEvent{},
				&models.AutomationRuleAction{}); err != nil {
				t.Fatalf("auto migrate: %v", err)
			}
			rule := models.AutomationRule{
				Name: "x", Enabled: true,
				TriggerType: models.AutomationTriggerSensorThreshold,
				ActionType:  models.AutomationActionNotification, ActionLevel: "info",
			}
			if err := db.Create(&rule).Error; err != nil {
				t.Fatal(err)
			}
			registerAutomationRoutes(r.Group("/api/v1"), db, ev, nil, nil, nil)

			body := doGet(t, r, "/api/v1/automation-rules")
			var resp struct {
				Data []models.AutomationRule `json:"data"`
			}
			if err := json.Unmarshal(body, &resp); err != nil {
				t.Fatalf("decode: %v (%s)", err, body)
			}
			if len(resp.Data) != 1 {
				t.Fatalf("规则数 = %d, want 1", len(resp.Data))
			}
			if resp.Data[0].NextRun != nil {
				t.Fatalf("无计算能力时不得凭空产生 next_run, got %+v", resp.Data[0].NextRun)
			}
		})
	}
}

func TestGetAutomationRuleAttachesNextRun(t *testing.T) {
	r, db := setupTestRouter(t)
	if err := db.AutoMigrate(&models.AutomationRule{}, &models.AutomationEvent{},
		&models.AutomationRuleAction{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}
	rule := models.AutomationRule{
		Name: "详情", Enabled: true,
		TriggerType:    models.AutomationTriggerSunEvent,
		TriggerSunKind: models.AutomationSunKindSunrise,
		ActionType:     models.AutomationActionNotification, ActionLevel: "info",
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}
	registerAutomationRoutes(r.Group("/api/v1"), db, &fakeNextRunEvaluator{}, nil, nil, nil)

	body := doGet(t, r, "/api/v1/automation-rules/"+itoaU(rule.ID))
	var resp struct {
		Data models.AutomationRule `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if resp.Data.NextRun == nil || resp.Data.NextRun.Status != models.NextRunScheduled {
		t.Fatalf("详情页同样要带 next_run, got %+v", resp.Data.NextRun)
	}
}

// TestListAutomationRulesNextRunEndToEnd 用**真实的** *automation.Evaluator
// 走完整链路: 库里的规则 → handler → 求值器计算 → JSON。
//
// 这是"已创建的自动规则能显示下次执行时间"这条需求的直接验收: 只要这条红,
// 功能就没有真正上线 (前面那些用 fakeNextRunEvaluator 的用例只证明接线)。
func TestListAutomationRulesNextRunEndToEnd(t *testing.T) {
	r, db := setupTestRouter(t)
	if err := db.AutoMigrate(&models.AutomationRule{}, &models.AutomationEvent{},
		&models.AutomationRuleAction{}); err != nil {
		t.Fatalf("auto migrate: %v", err)
	}

	tzName := "Asia/Shanghai"
	lat, lon := 23.1291, 113.2644 // 广州 (截图里那条规则的实算坐标)
	tz, err := time.LoadLocation(tzName)
	if err != nil {
		t.Fatal(err)
	}
	// 一条"日落 正点"规则 —— 与生产界面截图里「关闭BMS充电」同型。
	rule := models.AutomationRule{
		Name: "关闭BMS充电", Enabled: true,
		TriggerType:         models.AutomationTriggerSunEvent,
		TriggerSunKind:      models.AutomationSunKindSunset,
		TriggerSunOffsetMin: 0,
		ActionType:          models.AutomationActionNotification, ActionLevel: "info",
		CooldownSec: 30, MaxDailyExec: 10,
		Latitude: &lat, Longitude: &lon, Timezone: &tzName,
	}
	if err := db.Create(&rule).Error; err != nil {
		t.Fatal(err)
	}

	ev := automation.NewEvaluator(db, nil)
	ev.LoadRules()
	registerAutomationRoutes(r.Group("/api/v1"), db, ev, nil, nil, nil)

	body := doGet(t, r, "/api/v1/automation-rules")
	var resp struct {
		Data []models.AutomationRule `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode: %v (%s)", err, body)
	}
	if len(resp.Data) != 1 {
		t.Fatalf("规则数 = %d, want 1", len(resp.Data))
	}
	nr := resp.Data[0].NextRun
	if nr == nil {
		t.Fatal("真实求值器下响应必须带 next_run")
	}
	if nr.Status != models.NextRunScheduled {
		t.Fatalf("status = %q, want scheduled (detail=%q)", nr.Status, nr.Detail)
	}
	if nr.At == nil {
		t.Fatal("scheduled 必须带 at")
	}
	if nr.Timezone != tzName {
		t.Fatalf("timezone = %q, want %q (sun_event 用规则时区)", nr.Timezone, tzName)
	}

	// at 必须落在"今天或明天的日落"上 (测试跑在任何时刻, 两种都合法)。
	now := time.Now()
	todaySunset, _, err := sun.SunEvent(lat, lon, tz, now, sun.Sunset)
	if err != nil {
		t.Fatal(err)
	}
	tomorrowSunset, _, err := sun.SunEvent(lat, lon, tz, now.AddDate(0, 0, 1), sun.Sunset)
	if err != nil {
		t.Fatal(err)
	}
	got := nr.At.In(tz).Format("15:04")
	okToday := got == todaySunset.In(tz).Format("15:04")
	okTomorrow := got == tomorrowSunset.In(tz).Format("15:04")
	if !okToday && !okTomorrow {
		t.Fatalf("at = %s (%s), 必须是今日 %s 或次日 %s 的日落时刻",
			got, nr.At.In(tz), todaySunset.In(tz).Format("15:04"), tomorrowSunset.In(tz).Format("15:04"))
	}
	// 不得给出已经过去的时刻 (误差窗口给 2 分钟, 对应"事件刚过、即将触发"分支)。
	if nr.At.Before(now.Add(-2 * time.Minute)) {
		t.Fatalf("at = %v 已经过去 (now=%v), 下次执行时间不能是过去", nr.At, now)
	}
}

// ── 小工具 ────────────────────────────────────────────────────────────────

func doGet(t *testing.T, r interface {
	ServeHTTP(w http.ResponseWriter, req *http.Request)
}, path string) []byte {
	t.Helper()
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET %s => %d (%s)", path, w.Code, w.Body.String())
	}
	return w.Body.Bytes()
}

func itoaU(v uint) string {
	if v == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	return string(buf[i:])
}
