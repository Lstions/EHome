package alert

// 字段解析层接入 + R2 状态清理的告警侧单元测试（fake Plan/Provider）。
//
// 覆盖：literal 逐字节等价、特殊 selector 走 EvalPredicate、kill switch、
// deviceID 语义（Evaluate 入参）、以及 R2 的"重算 satisfied 保留 DurationSec 连续性"
// 与"绑定变化必须清窗口/关闭陈旧 firing"。

import (
	"errors"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sensorfield"
	"ehome/backend/pkg/parser"

	"gorm.io/gorm"
)

type fakePlan struct {
	selector string
	kind     sensorfield.Kind
	epoch    uint64
	verdict  func(frame []sensorfield.Reading, op sensorfield.CmpOp, threshold float64) sensorfield.Verdict
}

func (p *fakePlan) Selector() string       { return p.selector }
func (p *fakePlan) Kind() sensorfield.Kind { return p.kind }
func (p *fakePlan) Epoch() uint64          { return p.epoch }
func (p *fakePlan) Eval(_ []sensorfield.Reading) sensorfield.EvalResult {
	return sensorfield.EvalResult{}
}
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
	compiles int
	devices  []uint
}

func newFakeProvider(factory func(uint, string) sensorfield.Plan) *fakeProvider {
	return &fakeProvider{enabled: true, factory: factory}
}

func (f *fakeProvider) Compile(deviceID uint, selector string) (sensorfield.Plan, error) {
	f.mu.Lock()
	f.compiles++
	f.devices = append(f.devices, deviceID)
	factory := f.factory
	f.mu.Unlock()
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

func (f *fakeProvider) setEnabled(v bool) {
	f.mu.Lock()
	f.enabled = v
	f.mu.Unlock()
}

func (f *fakeProvider) lastDevice() uint {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.devices) == 0 {
		return 0
	}
	return f.devices[len(f.devices)-1]
}

func alwaysVerdict(kind sensorfield.Kind, ok, satisfied bool, value float64) func(uint, string) sensorfield.Plan {
	return func(_ uint, selector string) sensorfield.Plan {
		return &fakePlan{selector: selector, kind: kind,
			verdict: func([]sensorfield.Reading, sensorfield.CmpOp, float64) sensorfield.Verdict {
				return sensorfield.Verdict{OK: ok, Satisfied: satisfied, Value: value}
			}}
	}
}

func fields(pairs ...interface{}) []parser.Field {
	out := make([]parser.Field, 0, len(pairs)/2)
	for i := 0; i+1 < len(pairs); i += 2 {
		name, ok := pairs[i].(string)
		if !ok {
			continue
		}
		var v float64
		switch x := pairs[i+1].(type) {
		case float64:
			v = x
		case int:
			v = float64(x)
		default:
			continue
		}
		out = append(out, parser.Field{Name: name, Value: v})
	}
	return out
}

func eventCount(db *gorm.DB, ruleID uint) int64 {
	var n int64
	db.Model(&models.AlertEvent{}).Where("rule_id = ?", ruleID).Count(&n)
	return n
}

func newAlertEval(t *testing.T, r models.AlertRule, prov PlanProvider) (*Evaluator, *gorm.DB) {
	t.Helper()
	db := newTestDB(t)
	ev := NewEvaluator(db, nil)
	if prov != nil {
		ev.SetPlanProvider(prov)
	}
	if err := db.Create(&r).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	return ev, db
}

// literal 路径必须绕开 plan（逐字节等价）。
func TestAlertSelectorLiteralByteIdentical(t *testing.T) {
	r := rule(1, "temperature", "gt", 50, 0)
	prov := newFakeProvider(alwaysVerdict(sensorfield.KindLiteral, true, false, 0))
	ev, db := newAlertEval(t, r, prov)
	ev.Evaluate(1, fields("temperature", 60), time.Now())
	if got := eventCount(db, 1); got != 1 {
		t.Fatalf("literal 必须逐字节等价触发，got events=%d", got)
	}
}

// 特殊 selector：EvalPredicate 满足 ⇒ firing。
func TestAlertSelectorSpecialFires(t *testing.T) {
	r := rule(1, "@soc", "lt", 30, 0)
	prov := newFakeProvider(alwaysVerdict(sensorfield.KindSemantic, true, true, 20))
	ev, db := newAlertEval(t, r, prov)
	ev.Evaluate(1, fields("rsoc", 20), time.Now())
	if got := eventCount(db, 1); got != 1 {
		t.Fatalf("特殊 selector 应触发告警，got events=%d", got)
	}
	var evRow models.AlertEvent
	db.Where("rule_id = ?", 1).First(&evRow)
	if evRow.State != stateFiring {
		t.Fatalf("事件应为 firing，got %s", evRow.State)
	}
}

// kill switch：特殊 selector 不触发、literal 照旧。
func TestAlertSelectorKillSwitch(t *testing.T) {
	db := newTestDB(t)
	ev := NewEvaluator(db, nil)
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

	special := rule(1, "temperature_*:max", "gt", 50, 0)
	literal := rule(2, "temperature", "gt", 50, 0)
	if err := db.Create(&special).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&literal).Error; err != nil {
		t.Fatal(err)
	}
	ev.LoadRules()
	at := time.Now()
	ev.Evaluate(1, fields("temperature_1", 55), at)
	ev.Evaluate(1, fields("temperature", 60), at)
	if got := eventCount(db, 1); got != 0 {
		t.Fatalf("kill switch 下特殊 selector 不得触发，got %d", got)
	}
	if got := eventCount(db, 2); got != 1 {
		t.Fatalf("literal 规则不受 kill switch 影响，got %d", got)
	}
}

// deviceID 语义：plan 的 deviceID 必须是 Evaluate 入参（不是 TargetID 的推导）。
func TestAlertSelectorDeviceIDFromEvaluate(t *testing.T) {
	r := rule(1, "temperature_*:max", "gt", 50, 0)
	r.TargetID = 5
	prov := newFakeProvider(alwaysVerdict(sensorfield.KindFamily, true, true, 55))
	ev, _ := newAlertEval(t, r, prov)
	ev.Evaluate(5, fields("temperature_1", 55), time.Now())
	if got := prov.lastDevice(); got != 5 {
		t.Fatalf("Compile 的 deviceID 必须取 Evaluate 入参 5，got %d", got)
	}
}

// R2：改阈值用原始值重算 satisfied，保留 DurationSec 连续性（不清窗口）。
func TestAlertR2ThresholdChangeRecomputesWindow(t *testing.T) {
	r := rule(1, "temperature", "gt", 100, 10)
	r.SilenceSec = 0
	ev, db := newAlertEval(t, r, nil)

	t0 := time.Now()
	ev.Evaluate(1, fields("temperature", 50), t0)                  // 50>100 false
	ev.Evaluate(1, fields("temperature", 50), t0.Add(5*time.Second)) // false
	if got := eventCount(db, 1); got != 0 {
		t.Fatalf("条件未满足不应 firing，got %d", got)
	}
	// 阈值改 40：旧样本 50>40 应为 satisfied（重算，而非丢弃窗口）。
	db.Model(&models.AlertRule{}).Where("id = ?", 1).Update("threshold", 40)
	ev.LoadRules()
	// 在 t0+10 求值：alert 的滚动窗口是 [now-DurationSec, now]，t0 恰好落在边界内，
	// 只有"保留窗口 + 重算 satisfied"才可能 span==DurationSec 而触发。
	ev.Evaluate(1, fields("temperature", 50), t0.Add(10*time.Second))
	if got := eventCount(db, 1); got != 1 {
		t.Fatalf("改阈值应按原始值重算 satisfied 并保留 DurationSec 连续性，got events=%d", got)
	}
}

// R2：改绑定物理量必须清窗口（A 的旧样本不得参与 B 的判定）。
func TestAlertR2SensorRenameClearsWindow(t *testing.T) {
	r := rule(1, "temperature", "gt", 40, 10)
	r.SilenceSec = 0
	ev, db := newAlertEval(t, r, nil)

	t0 := time.Now()
	ev.Evaluate(1, fields("temperature", 50), t0)
	ev.Evaluate(1, fields("temperature", 50), t0.Add(5*time.Second))
	db.Model(&models.AlertRule{}).Where("id = ?", 1).Update("sensor_name", "humidity")
	ev.LoadRules()
	ev.Evaluate(1, fields("humidity", 50), t0.Add(10*time.Second))
	if got := eventCount(db, 1); got != 0 {
		t.Fatalf("改 sensor_name 后必须清窗口，got events=%d", got)
	}
}

// R2：改绑定目标（设备）必须清窗口。
func TestAlertR2TargetRebindClearsWindow(t *testing.T) {
	r := rule(1, "temperature", "gt", 40, 10)
	r.SilenceSec = 0
	ev, db := newAlertEval(t, r, nil)

	t0 := time.Now()
	ev.Evaluate(1, fields("temperature", 50), t0)
	ev.Evaluate(1, fields("temperature", 50), t0.Add(5*time.Second))
	db.Model(&models.AlertRule{}).Where("id = ?", 1).Update("target_id", 2)
	ev.LoadRules()
	ev.Evaluate(2, fields("temperature", 50), t0.Add(10*time.Second))
	if got := eventCount(db, 1); got != 0 {
		t.Fatalf("换绑定设备后必须清窗口，got events=%d", got)
	}
}

// T2h：firing 中的规则改内容 ⇒ 陈旧 firing 被关闭（不得永久 firing）。
func TestAlertR2ContentChangeResolvesStaleFiring(t *testing.T) {
	r := rule(1, "temperature", "gt", 40, 0)
	ev, db := newAlertEval(t, r, nil)

	ev.Evaluate(1, fields("temperature", 50), time.Now())
	var firing models.AlertEvent
	if err := db.Where("rule_id = ? AND state = ?", 1, stateFiring).First(&firing).Error; err != nil {
		t.Fatalf("应先有 firing 事件: %v", err)
	}
	db.Model(&models.AlertRule{}).Where("id = ?", 1).Update("threshold", 100)
	ev.LoadRules()

	var after models.AlertEvent
	if err := db.First(&after, firing.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.State != stateResolved || after.ResolvedAt == nil {
		t.Fatalf("内容变更后陈旧 firing 必须置 resolved + resolved_at，got state=%s resolvedAt=%v", after.State, after.ResolvedAt)
	}
	// 条件仍满足 ⇒ 允许再次 firing（F3 已接受的代价）。
	ev.Evaluate(1, fields("temperature", 50), time.Now().Add(time.Second))
	if got := eventCount(db, 1); got != 1 {
		t.Fatalf("新条件不满足（50>100 false）时不应新增事件，got %d", got)
	}
}

// T2g（误清方向）：仅 Name/UpdatedAt 漂移必须保留窗口（DurationSec 可积累）。
func TestAlertR2BookkeepingChangeKeepsWindow(t *testing.T) {
	r := rule(1, "temperature", "gt", 40, 10)
	r.SilenceSec = 0
	ev, db := newAlertEval(t, r, nil)

	t0 := time.Now()
	ev.Evaluate(1, fields("temperature", 50), t0)
	ev.Evaluate(1, fields("temperature", 50), t0.Add(5*time.Second))
	db.Model(&models.AlertRule{}).Where("id = ?", 1).Update("name", "改名不影响窗口")
	ev.LoadRules()
	ev.Evaluate(1, fields("temperature", 50), t0.Add(10*time.Second))
	if got := eventCount(db, 1); got != 1 {
		t.Fatalf("仅 Name/UpdatedAt 漂移必须保留窗口，got events=%d", got)
	}
}
