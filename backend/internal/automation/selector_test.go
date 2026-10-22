package automation

// 字段解析层接入的单元测试（fake Plan/Provider；真实 sensorfield 由 impl-core 覆盖）。
//
// 覆盖：literal 逐字节等价、family predicate、conditions 走 plan、kill switch、
// 混合规则 fail-closed、(ruleID,slot,deviceID) 缓存键、审计去重，以及窄复核 R2
// 的窗口状态清理矩阵 T2a–T2j 中 automation 侧部分。

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
	"ehome/backend/pkg/parser"

	"gorm.io/gorm"
)

// ── fake Plan / PlanProvider ────────────────────────────────────────────────

type fakePlan struct {
	selector string
	kind     sensorfield.Kind
	epoch    uint64
	verdict  func(frame []sensorfield.Reading, op sensorfield.CmpOp, threshold float64) sensorfield.Verdict
	result   sensorfield.EvalResult
}

func (p *fakePlan) Selector() string       { return p.selector }
func (p *fakePlan) Kind() sensorfield.Kind { return p.kind }
func (p *fakePlan) Epoch() uint64          { return p.epoch }
func (p *fakePlan) Eval(_ []sensorfield.Reading) sensorfield.EvalResult { return p.result }
func (p *fakePlan) EvalPredicate(frame []sensorfield.Reading, op sensorfield.CmpOp, threshold float64) sensorfield.Verdict {
	if p.verdict != nil {
		return p.verdict(frame, op, threshold)
	}
	return sensorfield.Verdict{Reason: "missing"}
}

type fakeProvider struct {
	mu       sync.Mutex
	enabled  bool
	epoch    uint64
	factory  func(deviceID uint, selector string) sensorfield.Plan
	compiles map[string]int
	devices  map[string][]uint
	errs     map[string]error
}

func newFakeProvider(factory func(uint, string) sensorfield.Plan) *fakeProvider {
	return &fakeProvider{
		enabled:  true,
		factory:  factory,
		compiles: map[string]int{},
		devices:  map[string][]uint{},
		errs:     map[string]error{},
	}
}

func (f *fakeProvider) Compile(deviceID uint, selector string) (sensorfield.Plan, error) {
	// 注意：factory 不能在持锁时调用（测试工厂会回调 Epoch() 读同一把锁 ⇒ 死锁）。
	f.mu.Lock()
	f.compiles[selector]++
	f.devices[selector] = append(f.devices[selector], deviceID)
	err, hasErr := f.errs[selector]
	factory := f.factory
	f.mu.Unlock()
	if hasErr {
		return nil, err
	}
	if factory == nil {
		return nil, errors.New("no factory")
	}
	return factory(deviceID, selector), nil
}

func (f *fakeProvider) Enabled() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enabled
}

func (f *fakeProvider) Epoch() uint64 {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.epoch
}

func (f *fakeProvider) compileCount(selector string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.compiles[selector]
}

func (f *fakeProvider) lastDevice(selector string) uint {
	f.mu.Lock()
	defer f.mu.Unlock()
	list := f.devices[selector]
	if len(list) == 0 {
		return 0
	}
	return list[len(list)-1]
}

func (f *fakeProvider) setEnabled(v bool) {
	f.mu.Lock()
	f.enabled = v
	f.mu.Unlock()
}

func (f *fakeProvider) setEpoch(v uint64) {
	f.mu.Lock()
	f.epoch = v
	f.mu.Unlock()
}

// alwaysVerdict 返回固定结论的 plan 工厂。
func alwaysVerdict(kind sensorfield.Kind, ok, satisfied bool, value float64, reason string) func(uint, string) sensorfield.Plan {
	return func(_ uint, selector string) sensorfield.Plan {
		return &fakePlan{
			selector: selector,
			kind:     kind,
			verdict: func([]sensorfield.Reading, sensorfield.CmpOp, float64) sensorfield.Verdict {
				return sensorfield.Verdict{OK: ok, Satisfied: satisfied, Value: value, Reason: reason}
			},
		}
	}
}

// frame 构造一帧；重复名称按出现顺序保留（literal 取第一个同名）。
func frame(pairs ...interface{}) []parser.Field {
	out := make([]parser.Field, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		name, ok := pairs[i].(string)
		if !ok {
			continue
		}
		var value float64
		switch v := pairs[i+1].(type) {
		case float64:
			value = v
		case int:
			value = float64(v)
		default:
			continue
		}
		out = append(out, parser.Field{Name: name, Value: value})
	}
	return out
}

// condJSON 构造一条附加条件（避免 Go raw string 的转义噪声）。
func condJSON(sensor, op string, threshold float64) string {
	return fmt.Sprintf("[{\"sensor_name\":%q,\"comparator\":%q,\"threshold\":%v}]", sensor, op, threshold)
}

// ── 测试 ────────────────────────────────────────────────────────────────────

// literal 路径必须绕开 plan（逐字节等价：取第一个同名、原始比较语义）。
func TestSelectorLiteralByteIdentical(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	// 故意让 plan 给出"不满足"的结论：literal 若走 plan 就不会触发。
	prov := newFakeProvider(alwaysVerdict(sensorfield.KindLiteral, true, false, 0, ""))
	ev.SetPlanProvider(prov)

	r := automationRule(1, "illuminance", "gt", 500, 0)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	// 第一个同名满足阈值、第二个不满足：若实现"取最后一个/取最大值"会错。
	ev.Evaluate(1, frame("illuminance", 600, "illuminance", 100), time.Now())
	if len(h.events) != 1 {
		t.Fatalf("literal 应逐字节等价触发，got %d 次", len(h.events))
	}
	if h.events[0].Value != 600 {
		t.Fatalf("literal 必须取第一个同名字段，got value=%v", h.events[0].Value)
	}
}

// 特殊 selector：比较走 EvalPredicate（family any/all 不能用 Plan.Eval）。
func TestSelectorSpecialUsesPredicate(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	prov := newFakeProvider(alwaysVerdict(sensorfield.KindFamily, true, true, 55, ""))
	ev.SetPlanProvider(prov)

	r := automationRule(1, "temperature_*:max", "gt", 50, 0)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	ev.Evaluate(1, frame("temperature_1", 55, "temperature_2", 40), time.Now())
	if len(h.events) != 1 {
		t.Fatalf("特殊 selector 应触发，got %d", len(h.events))
	}
	if h.events[0].Value != 55 {
		t.Fatalf("TriggerEvent.Value 应为 predicate 值 55，got %v", h.events[0].Value)
	}
}

// unbound（OK=false）⇒ 不触发 + 审计 result=unresolved_selector，按 (rule,selector) 去重。
func TestSelectorUnboundAudits(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	prov := newFakeProvider(alwaysVerdict(sensorfield.KindFamily, false, false, 0, "partial_frame"))
	ev.SetPlanProvider(prov)

	r := automationRule(1, "temperature_*:max", "gt", 50, 0)
	r.CooldownSec = 0
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	at := time.Now()
	ev.Evaluate(1, frame("temperature_1", 55), at)
	if len(h.events) != 0 {
		t.Fatalf("unbound 不得触发，got %d", len(h.events))
	}
	var n int64
	db.Model(&models.AutomationEvent{}).Where("result = ?", ResultUnresolvedSelector).Count(&n)
	if n != 1 {
		t.Fatalf("应有 1 条 unresolved_selector 审计，got %d", n)
	}
	ev.Evaluate(1, frame("temperature_1", 55), at.Add(time.Minute))
	db.Model(&models.AutomationEvent{}).Where("result = ?", ResultUnresolvedSelector).Count(&n)
	if n != 1 {
		t.Fatalf("同 (rule,selector) 5min 内必须去重，got %d", n)
	}
}

// conditions 也走 plan（评审 B5）：trigger 满足但 special 条件不满足 ⇒ 不触发。
func TestSelectorConditionsUsePlan(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	satisfied := false
	prov := newFakeProvider(func(_ uint, selector string) sensorfield.Plan {
		return &fakePlan{selector: selector, kind: sensorfield.KindSemantic,
			verdict: func([]sensorfield.Reading, sensorfield.CmpOp, float64) sensorfield.Verdict {
				return sensorfield.Verdict{OK: true, Satisfied: satisfied, Value: 20}
			}}
	})
	ev.SetPlanProvider(prov)

	r := automationRule(1, "illuminance", "gt", 500, 0)
	r.ConditionsJSON = condJSON("@soc", "lt", 30)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	ev.Evaluate(1, frame("illuminance", 600), time.Now())
	if len(h.events) != 0 {
		t.Fatalf("special 条件不满足时不得触发，got %d", len(h.events))
	}
	satisfied = true
	ev.Evaluate(1, frame("illuminance", 600), time.Now().Add(time.Second))
	if len(h.events) != 1 {
		t.Fatalf("special 条件满足后应触发，got %d", len(h.events))
	}
}

// kill switch：enabled=false 时特殊 selector 不触发 + 落 field_resolver_disabled。
func TestSelectorKillSwitch(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	// kind 必须按 selector 形态区分：literal 规则若被编译成 family plan，
	// kill switch 会把它也拒掉（测试自身会把兼容路径测成假红）。
	prov := newFakeProvider(func(_ uint, selector string) sensorfield.Plan {
		kind := sensorfield.KindLiteral
		if isSpecialSelector(selector) {
			kind = sensorfield.KindFamily
		}
		return &fakePlan{selector: selector, kind: kind,
			verdict: func([]sensorfield.Reading, sensorfield.CmpOp, float64) sensorfield.Verdict {
				return sensorfield.Verdict{OK: true, Satisfied: true, Value: 55}
			}}
	})
	prov.setEnabled(false)
	ev.SetPlanProvider(prov)

	special := automationRule(1, "temperature_*:max", "gt", 50, 0)
	literal := automationRule(2, "illuminance", "gt", 500, 0)
	if err := db.Create(&special).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&literal).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	at := time.Now()
	ev.Evaluate(1, frame("temperature_1", 55), at)
	ev.Evaluate(1, frame("illuminance", 600), at)
	if len(h.events) != 1 || h.events[0].Rule.ID != 2 {
		t.Fatalf("kill switch 下只允许 literal 规则触发，got %+v", h.events)
	}
	var n int64
	db.Model(&models.AutomationEvent{}).Where("result = ?", ResultFieldResolverDisabled).Count(&n)
	if n != 1 {
		t.Fatalf("应落 1 条 field_resolver_disabled 审计，got %d", n)
	}
}

// 混合规则（literal trigger + 特殊 conditions）在 enabled=false 时整条 fail-closed。
func TestSelectorMixedRuleDisabledFailClosed(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	prov := newFakeProvider(alwaysVerdict(sensorfield.KindSemantic, true, true, 20, ""))
	prov.setEnabled(false)
	ev.SetPlanProvider(prov)

	r := automationRule(1, "illuminance", "gt", 500, 0)
	r.ConditionsJSON = condJSON("@soc", "lt", 30)
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	ev.Evaluate(1, frame("illuminance", 600), time.Now())
	if len(h.events) != 0 {
		t.Fatalf("混合规则在关闭态必须整条不执行，got %d", len(h.events))
	}
	var n int64
	db.Model(&models.AutomationEvent{}).Where("result = ?", ResultFieldResolverDisabled).Count(&n)
	if n != 1 {
		t.Fatalf("混合规则 fail-closed 必须落 field_resolver_disabled 审计，got %d", n)
	}
}

// 缓存键：同 selector 不重编译；epoch/LoadRules 变化重编译；deviceID 用 Evaluate 入参。
func TestSelectorPlanCacheKey(t *testing.T) {
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	var prov *fakeProvider
	prov = newFakeProvider(func(_ uint, selector string) sensorfield.Plan {
		return &fakePlan{selector: selector, kind: sensorfield.KindFamily, epoch: prov.Epoch()}
	})
	// 起始 epoch 必须非 0：PlanEpoch==0 表示"实现未携带 epoch"，缓存会保守命中。
	prov.setEpoch(100)
	ev.SetPlanProvider(prov)

	r := automationRule(1, "temperature_*:max", "gt", 50, 0)
	r.TriggerEdgeDeviceID = 0 // any-device 规则
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()

	at := time.Now()
	ev.Evaluate(7, frame("temperature_1", 55), at)
	ev.Evaluate(9, frame("temperature_1", 55), at)
	if got := prov.compileCount("temperature_*:max"); got != 2 {
		t.Fatalf("不同 deviceID 必须各自编译（key 含 deviceID），got %d", got)
	}
	if got := prov.lastDevice("temperature_*:max"); got != 9 {
		t.Fatalf("Compile 的 deviceID 必须是 Evaluate 入参 9，got %d", got)
	}
	ev.Evaluate(9, frame("temperature_1", 55), at)
	if got := prov.compileCount("temperature_*:max"); got != 2 {
		t.Fatalf("缓存命中不应重编译，got %d", got)
	}
	prov.setEpoch(1)
	ev.Evaluate(9, frame("temperature_1", 55), at)
	if got := prov.compileCount("temperature_*:max"); got != 3 {
		t.Fatalf("epoch 变化必须重编译，got %d", got)
	}
	ev.LoadRules()
	ev.Evaluate(9, frame("temperature_1", 55), at)
	if got := prov.compileCount("temperature_*:max"); got != 4 {
		t.Fatalf("LoadRules 后必须重编译，got %d", got)
	}
}

// ── R2：触发语义变更清窗口（T2a–T2f/T2i/T2g/Y1）───────────────────────────

func evalWithRule(t *testing.T, r models.AutomationRule, prov PlanProvider) (*Evaluator, *captureHandler, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)
	h := &captureHandler{}
	ev := NewEvaluator(db, h)
	if prov != nil {
		ev.SetPlanProvider(prov)
	}
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	return ev, h, db
}

// T2a 改阈值：旧窗口的 satisfied 不得继续生效。
func TestR2ThresholdChangeClearsWindow(t *testing.T) {
	r := automationRule(1, "temperature", "gt", 400, 10)
	r.CooldownSec = 0
	ev, h, db := evalWithRule(t, r, nil)

	t0 := time.Now()
	ev.Evaluate(1, frame("temperature", 500), t0)
	ev.Evaluate(1, frame("temperature", 500), t0.Add(5*time.Second))
	if len(h.events) != 0 {
		t.Fatalf("span<DurationSec 不应触发，got %d", len(h.events))
	}
	if err := db.Model(&models.AutomationRule{}).Where("id = ?", 1).Update("trigger_threshold", 100).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	ev.Evaluate(1, frame("temperature", 500), t0.Add(11*time.Second))
	if len(h.events) != 0 {
		t.Fatalf("改阈值后必须清窗口（旧样本会凑满 DurationSec 立即误触发），got %d", len(h.events))
	}
}

// T2b 改比较符：同上。
func TestR2ComparatorChangeClearsWindow(t *testing.T) {
	r := automationRule(1, "temperature", "gt", 400, 10)
	r.CooldownSec = 0
	ev, h, db := evalWithRule(t, r, nil)

	t0 := time.Now()
	ev.Evaluate(1, frame("temperature", 500), t0)
	ev.Evaluate(1, frame("temperature", 500), t0.Add(5*time.Second))
	db.Model(&models.AutomationRule{}).Where("id = ?", 1).Update("trigger_comparator", "gte")
	ev.LoadRules()
	ev.Evaluate(1, frame("temperature", 500), t0.Add(11*time.Second))
	if len(h.events) != 0 {
		t.Fatalf("改比较符后必须清窗口，got %d", len(h.events))
	}
}

// T2c 缩短 DurationSec：不得凭旧样本立即触发。
func TestR2ShorterDurationClearsWindow(t *testing.T) {
	r := automationRule(1, "temperature", "gt", 400, 60)
	r.CooldownSec = 0
	ev, h, db := evalWithRule(t, r, nil)

	t0 := time.Now()
	ev.Evaluate(1, frame("temperature", 500), t0)
	ev.Evaluate(1, frame("temperature", 500), t0.Add(30*time.Second))
	db.Model(&models.AutomationRule{}).Where("id = ?", 1).Update("trigger_duration_sec", 1)
	ev.LoadRules()
	ev.Evaluate(1, frame("temperature", 500), t0.Add(31*time.Second))
	if len(h.events) != 0 {
		t.Fatalf("缩短 DurationSec 后必须清窗口，got %d", len(h.events))
	}
}

// T2d 改 conditions_json：旧样本不得参与新条件门控。
func TestR2ConditionsChangeClearsWindow(t *testing.T) {
	r := automationRule(1, "temperature", "gt", 400, 10)
	r.CooldownSec = 0
	ev, h, db := evalWithRule(t, r, nil)

	t0 := time.Now()
	ev.Evaluate(1, frame("temperature", 500), t0)
	ev.Evaluate(1, frame("temperature", 500), t0.Add(5*time.Second))
	db.Model(&models.AutomationRule{}).Where("id = ?", 1).
		Update("conditions_json", condJSON("humidity", "gt", 50))
	ev.LoadRules()
	ev.Evaluate(1, frame("temperature", 500, "humidity", 80), t0.Add(11*time.Second))
	if len(h.events) != 0 {
		t.Fatalf("改 conditions 后必须清窗口，got %d", len(h.events))
	}
}

// T2e 改 selector：旧 satisfied 不得继承。
func TestR2SelectorChangeClearsWindow(t *testing.T) {
	r := automationRule(1, "illuminance", "gt", 400, 10)
	r.CooldownSec = 0
	ev, h, db := evalWithRule(t, r, nil)

	t0 := time.Now()
	ev.Evaluate(1, frame("illuminance", 500), t0)
	ev.Evaluate(1, frame("illuminance", 500), t0.Add(5*time.Second))
	db.Model(&models.AutomationRule{}).Where("id = ?", 1).Updates(map[string]interface{}{
		"trigger_sensor_name": "temperature", "trigger_comparator": "gt", "trigger_threshold": 50,
	})
	ev.LoadRules()
	ev.Evaluate(1, frame("illuminance", 500, "temperature", 100), t0.Add(11*time.Second))
	if len(h.events) != 0 {
		t.Fatalf("改 selector 后必须清窗口，got %d", len(h.events))
	}
}

// T2f TriggerType 来回切换：旧窗口不得复活。
func TestR2TriggerTypeRoundTripNoResurrect(t *testing.T) {
	r := automationRule(1, "temperature", "gt", 400, 10)
	r.CooldownSec = 0
	ev, h, db := evalWithRule(t, r, nil)

	t0 := time.Now()
	ev.Evaluate(1, frame("temperature", 500), t0)
	ev.Evaluate(1, frame("temperature", 500), t0.Add(5*time.Second))
	db.Model(&models.AutomationRule{}).Where("id = ?", 1).Updates(map[string]interface{}{
		"trigger_type": models.AutomationTriggerTimeWindow, "trigger_window_start": "00:00",
		"trigger_window_end": "23:59", "trigger_window_edge": models.AutomationWindowEnter,
	})
	ev.LoadRules()
	db.Model(&models.AutomationRule{}).Where("id = ?", 1).Update("trigger_type", models.AutomationTriggerSensorThreshold)
	ev.LoadRules()
	ev.Evaluate(1, frame("temperature", 500), t0.Add(11*time.Second))
	if len(h.events) != 0 {
		t.Fatalf("类型回切后旧窗口不得复活，got %d", len(h.events))
	}
}

// T2i 换绑定设备：A 的旧样本不得参与 B 的判定。
func TestR2DeviceRebindClearsWindow(t *testing.T) {
	r := automationRule(1, "temperature", "gt", 400, 10)
	r.CooldownSec = 0
	ev, h, db := evalWithRule(t, r, nil)

	t0 := time.Now()
	ev.Evaluate(1, frame("temperature", 500), t0)
	ev.Evaluate(1, frame("temperature", 500), t0.Add(5*time.Second))
	db.Model(&models.AutomationRule{}).Where("id = ?", 1).Update("trigger_edge_device_id", 2)
	ev.LoadRules()
	ev.Evaluate(2, frame("temperature", 500), t0.Add(11*time.Second))
	if len(h.events) != 0 {
		t.Fatalf("换绑定设备后必须清窗口，got %d", len(h.events))
	}
}

// T2g（A-17 镜像/误清方向）：仅 Name/UpdatedAt 漂移不得清窗口。
func TestR2BookkeepingChangeKeepsWindow(t *testing.T) {
	r := automationRule(1, "temperature", "gt", 400, 10)
	r.CooldownSec = 0
	ev, h, db := evalWithRule(t, r, nil)

	t0 := time.Now()
	ev.Evaluate(1, frame("temperature", 500), t0)
	ev.Evaluate(1, frame("temperature", 500), t0.Add(5*time.Second))
	db.Model(&models.AutomationRule{}).Where("id = ?", 1).Update("name", "改名不影响窗口")
	ev.LoadRules()
	ev.Evaluate(1, frame("temperature", 500), t0.Add(11*time.Second))
	if len(h.events) != 1 {
		t.Fatalf("仅 Name/UpdatedAt 漂移必须保留窗口（否则 DurationSec 永不满足），got %d", len(h.events))
	}
}

// Y1 冷却锚不清：内容变化不得绕过 CooldownSec。
func TestR2ContentChangeKeepsCooldownAnchor(t *testing.T) {
	r := automationRule(1, "temperature", "gt", 400, 0)
	r.CooldownSec = 300
	ev, h, db := evalWithRule(t, r, nil)

	t0 := time.Now()
	ev.Evaluate(1, frame("temperature", 500), t0)
	if len(h.events) != 1 {
		t.Fatalf("首次应触发，got %d", len(h.events))
	}
	db.Model(&models.AutomationRule{}).Where("id = ?", 1).Update("trigger_threshold", 100)
	ev.LoadRules()
	ev.Evaluate(1, frame("temperature", 500), t0.Add(time.Second))
	if len(h.events) != 1 {
		t.Fatalf("内容变化不得清 triggered（否则绕过 CooldownSec），got %d", len(h.events))
	}
	var n int64
	db.Model(&models.AutomationEvent{}).Where("result = ?", models.AutomationResultSuppressedCooldown).Count(&n)
	if n == 0 {
		t.Fatal("冷却抑制应落 suppressed_cooldown 审计")
	}
}
