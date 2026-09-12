//go:build simulation

// 场景目录 · SIM-TRIG 触发语义（设计 §4 SIM-TRIG-001..008）。
//
// 契约：docs/设计/自动化引擎场景仿真验证.md（§3 能力面、§4 场景清单、§9 变异验收 A7）。
// 设计依据：docs/设计/自动化策略引擎方案.md。
//
// 本域守护的不变量（每条断言都对应其中之一）：
//  1. 比较符 gt/gte/lt/lte/eq/neq 与用户设置一致：只有真正满足的那几帧会触发，
//     且「触发的值集合」能被审计读到（evaluator.go:502 compare / models/alert.go:17-22）；
//  2. TriggerDurationSec 是「连续满足」窗口：中途回落不触发（变异 A7 必须变红），
//     持续满足到点后即使上报间隔稀疏也必然触发（pruneWindow 保留窗口起点样本）；
//  3. TriggerEdgeDeviceID 把策略钉在指定设备上：别的设备同样超限不算数；
//  4. 本帧没有策略监视的传感器字段时 fail-closed（matchField 未命中直接 return），
//     绝不把「没有数据」当「满足」；
//  5. 停用/启用即时生效：缓存里没有的规则根本不参与求值（Invalidate → LoadRules）；
//  6. 一次上报同时命中多条策略时，每条各自产生自己的事件（求值互不吞噬）。
//
// 复用：autoProvisionDevice / autoCreateRule / autoListEvents / autoListRules /
// autoSensorSample（同包 auto.go，设计 §4 明确要求复用现有夹具，不重造）。
//
// ⚠ 本文件同时承载本域三个子域（TRIG/COND/WIND）共用的**运行时工具**（trig 前缀）：
// cond.go / wind.go 直接调用它们。框架 §4.1 只约束包级**声明名**的前缀，不约束调用
// 写法，因此不违反门禁；把这类工具在三个文件里各复制一份，只会让三处断言口径漂移。
package catalog

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strconv"
	"sync"
	"time"

	"ehome/backend/simulation/harness"
)

func init() {
	Register(Scenario{
		ID:     "SIM-TRIG-001",
		Title:  "光照低于设定值就执行动作（四个比较方向都正确）",
		Domain: DomainTRIG,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-TRIG-001；docs/设计/自动化策略引擎方案.md（比较符语义）",
		Run:    trigRun001,
	})
	Register(Scenario{
		ID:     "SIM-TRIG-002",
		Title:  "数值正好等于设定值时，等于/不等于的判定与用户预期一致",
		Domain: DomainTRIG,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-TRIG-002；docs/设计/自动化策略引擎方案.md（eq/neq 边界）",
		Run:    trigRun002,
	})
	Register(Scenario{
		ID:     "SIM-TRIG-003",
		Title:  "要求「持续 N 秒都超限」时，中途回落不会误触发",
		Domain: DomainTRIG,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-TRIG-003（变异 A7）；docs/设计/自动化策略引擎方案.md（TriggerDurationSec）",
		Run:    trigRun003,
	})
	Register(Scenario{
		ID:     "SIM-TRIG-004",
		Title:  "持续超限到设定时长后一定会触发，不会因为上报稀疏而漏掉",
		Domain: DomainTRIG,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-TRIG-004；docs/设计/自动化策略引擎方案.md（滑动窗口剪枝）",
		Run:    trigRun004,
	})
	Register(Scenario{
		ID:     "SIM-TRIG-005",
		Title:  "只对指定设备的读数触发，别的设备同样超限也不触发",
		Domain: DomainTRIG,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-TRIG-005；docs/设计/自动化策略引擎方案.md（TriggerEdgeDeviceID 限定）",
		Run:    trigRun005,
	})
	Register(Scenario{
		ID:     "SIM-TRIG-006",
		Title:  "上报里没有这个传感器时不会触发（不把「没数据」当「满足」）",
		Domain: DomainTRIG,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-TRIG-006；docs/设计/自动化策略引擎方案.md（字段缺失 fail-closed）",
		Run:    trigRun006,
	})
	Register(Scenario{
		ID:     "SIM-TRIG-007",
		Title:  "停用策略后即使条件满足也不再触发，重新启用后恢复",
		Domain: DomainTRIG,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-TRIG-007；docs/设计/自动化策略引擎方案.md（Enabled 开关即时生效）",
		Run:    trigRun007,
	})
	Register(Scenario{
		ID:     "SIM-TRIG-008",
		Title:  "一次上报同时满足多条策略时，每条各自独立触发一次",
		Domain: DomainTRIG,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-TRIG-008；docs/设计/自动化策略引擎方案.md（多规则并行求值）",
		Run:    trigRun008,
	})
}

// ---------------------------------------------------------------------------
// 运行时工具（TRIG/COND/WIND 共用，声明在此文件，前缀 trig）
// ---------------------------------------------------------------------------

// trigEventTime 解析事件的 triggered_at。解析失败即失败：时间断言是本域
// 「连续窗口」「冷却节奏」类不变量的唯一证据，宁可红也不要跳过。
func trigEventTime(e *harness.Env, row autoEventRow) time.Time {
	e.T.Helper()
	at, err := time.Parse(time.RFC3339Nano, row.TriggeredAt)
	if err != nil {
		e.Fatalf("事件 %d 的 triggered_at=%q 无法解析: %v", row.ID, row.TriggeredAt, err)
	}
	return at
}

// trigCountResult 统计事件快照里指定 result 的条数。
func trigCountResult(rows []autoEventRow, result string) int {
	count := 0
	for _, row := range rows {
		if row.Result == result {
			count++
		}
	}
	return count
}

// trigWaitResult 轮询直到策略下出现 want 条指定 result 的事件，返回该策略的
// **全量**事件快照（含 suppressed_cooldown 等旁证，供断言与证据使用）。
func trigWaitResult(e *harness.Env, ruleID int64, result string, want int, timeout time.Duration) []autoEventRow {
	e.T.Helper()
	query := "?rule_id=" + strconv.FormatInt(ruleID, 10)
	var snapshot []autoEventRow
	e.Eventually(timeout, func() error {
		rows, err := autoListEvents(e, query)
		if err != nil {
			return err
		}
		if got := trigCountResult(rows, result); got < want {
			return fmt.Errorf("策略 %d 目前有 %d 条 result=%s 的事件，期望至少 %d 条",
				ruleID, got, result, want)
		}
		snapshot = rows
		return nil
	})
	return snapshot
}

// trigValuesOf 取出指定 result 的事件的触发值集合（升序）。
// 事件缺少 trigger_value 直接失败：sensor_threshold 的触发值必须可审计
// （「当时是多少」是用户判断误报与否的唯一依据）。
func trigValuesOf(e *harness.Env, rows []autoEventRow, result string) []float64 {
	e.T.Helper()
	var out []float64
	for _, row := range rows {
		if row.Result != result {
			continue
		}
		if row.TriggerValue == nil {
			e.Fatalf("策略 %d 的事件 %d（result=%s）没有 trigger_value；sensor_threshold 事件必须记录触发时值",
				row.RuleID, row.ID, row.Result)
		}
		out = append(out, *row.TriggerValue)
	}
	sort.Float64s(out)
	return out
}

// trigAssertValues 断言「触发的值集合」恰好等于期望集合（顺序无关，容差 1e-3）。
//
// 这是本域最核心的断言形状：它同时排除两类错误 —— 少触发（比较符过严/失效）
// 与多触发（比较符方向搞反/过于宽松）；只看「有没有触发」是抓不住后者的。
// 容差 1e-3 与 auto.go 的 autoEventuallyFloat 同源：物理量来自 value*scale，
// 0.1 这类 scale 不是二进制精确值。
func trigAssertValues(e *harness.Env, label string, rows []autoEventRow, result string, want ...float64) {
	e.T.Helper()
	got := trigValuesOf(e, rows, result)
	expected := append([]float64(nil), want...)
	sort.Float64s(expected)
	if len(got) != len(expected) {
		e.Fatalf("%s：实际触发值集合 %v（%d 次）与期望 %v（%d 次）不一致；事件快照 %+v",
			label, got, len(got), expected, len(expected), rows)
	}
	for i := range got {
		if math.Abs(got[i]-expected[i]) > 1e-3 {
			e.Fatalf("%s：实际触发值集合 %v 与期望 %v 不一致（第 %d 项）", label, got, expected, i)
		}
	}
}

// trigAssertResultCount 断言策略下指定 result 的事件**条数保持为 want**。
//
// 先做一段有界负向观察：同一帧会被多条规则求值，排在本规则之前的规则可能先落库，
// 只读一次列表会漏掉「紧接着才写进去」的事件；观察结束后再读一次列表做最终判定
// （事件只增不删，最终判定涵盖观察窗内出现过的任何事件）。
//
// 为什么是「条数」而不是「有没有」：TRIG-007 这类场景在前后两个阶段都会合法地
// 产生事件，只有记下进入负向阶段前的条数，「停用期间没有新增」才是一个真命题。
func trigAssertResultCount(e *harness.Env, label string, ruleID int64, result string, want int, observe time.Duration) {
	e.T.Helper()
	query := "?rule_id=" + strconv.FormatInt(ruleID, 10)
	if result != "" {
		query += "&result=" + result
	}
	_ = e.EventuallyError(observe, func() error {
		rows, err := autoListEvents(e, query)
		if err != nil {
			return err
		}
		if len(rows) != want {
			return nil // 条数变了 → EventuallyError 立即返回 nil，最终判定负责报错
		}
		return fmt.Errorf("负向观察中：期望保持 %d 条 %s 事件", want, result)
	})
	rows, err := autoListEvents(e, query)
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(rows) != want {
		e.Fatalf("%s：期望 %d 条 result=%s 的事件，实际 %d 条：%+v", label, want, result, len(rows), rows)
	}
}

// trigAssertNoResult 断言策略下一条指定 result 的事件都没有。
func trigAssertNoResult(e *harness.Env, label string, ruleID int64, result string, observe time.Duration) {
	e.T.Helper()
	trigAssertResultCount(e, label, ruleID, result, 0, observe)
}

// trigReportPaced 按固定节奏连报多帧（模拟真实传感器的上报间隔）。
//
// 为什么需要节奏而不是连发：冷却窗是**真实时间**（evaluator.go:423-435），
// 连发的后续帧会被冷却压成 suppressed_cooldown。本域要证明的是「哪些值满足
// 比较符」，压制属于 DBLN 域；因此这里按真实节奏（帧间隔 > cooldown_sec）上报，
// 让每一帧都得到一次独立求值。这里的等待只承担仿真上报节奏，不作断言同步。
func trigReportPaced(e *harness.Env, fx *autoFixture, raws []uint16, interval time.Duration) {
	e.T.Helper()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for i, raw := range raws {
		if i > 0 {
			<-ticker.C
		}
		if err := fx.report(raw); err != nil {
			e.Fatalf("节点第 %d 帧（原值 %d）上报失败: %v", i+1, raw, err)
		}
	}
}

// trigSensorSamples 读某边缘设备的统一数据（用户可见的「刚才到底收没收到」）。
func trigSensorSamples(e *harness.Env, edgeDeviceID uint) []autoSensorSample {
	e.T.Helper()
	path := "/api/v1/devices/" + strconv.FormatUint(uint64(edgeDeviceID), 10) + "/sensor-data?limit=100"
	r := e.Admin.Get(path).Expect(http.StatusOK)
	var samples []autoSensorSample
	r.Decode(&samples)
	return samples
}

// trigWaitSensorValue 等到该设备出现指定传感器名的指定读数。
//
// 为什么必须有：本域大量断言是「没有事件」。若不能用独立证据证明「这一帧真的被
// 解析入库了」，「0 条事件」既可能是策略正确，也可能是链路根本没跑（设计 §3
// 原则 2 的对照思路）。统一数据是 HTTP 侧可见的独立证据。
func trigWaitSensorValue(e *harness.Env, edgeDeviceID uint, sensorName string, want float64, timeout time.Duration) {
	e.T.Helper()
	e.Eventually(timeout, func() error {
		samples := trigSensorSamples(e, edgeDeviceID)
		for _, sample := range samples {
			if sample.SensorName == sensorName && math.Abs(sample.Value-want) <= 1e-3 {
				return nil
			}
		}
		return fmt.Errorf("设备 %d 尚无 %s≈%v 的统一数据（当前 %d 条）",
			edgeDeviceID, sensorName, want, len(samples))
	})
}

// trigAssertSensorAbsent 断言该设备的统一数据里没有某个传感器字段。
// 与 trigWaitSensorValue 配对使用，构成「本帧只有 X、没有 Y」的直接证据。
func trigAssertSensorAbsent(e *harness.Env, edgeDeviceID uint, sensorName string) {
	e.T.Helper()
	for _, sample := range trigSensorSamples(e, edgeDeviceID) {
		if sample.SensorName == sensorName {
			e.Fatalf("设备 %d 的统一数据里出现了 %s=%v —— 本场景的解析器不应产出该字段",
				edgeDeviceID, sensorName, sample.Value)
		}
	}
}

// trigUseSingleFieldParser 把本场景设备配置的解析器整体替换为单个 illuminance 字段
// （uint16 @0，scale=1 → 原始值即 lx，无量化误差）。
//
// 为什么走真实 PUT 而不是直连库：设计 §3 原则 2 要求断言基于真实 API 行为；
// 顺带覆盖「被边缘设备引用的配置只改 parser 是允许的」这条真实约束
// （handler_device.go 只禁止改 device_type/hardware_type/status）。
// 为什么需要它：autoProvisionDevice 造出的解析器只产出 temperature，
// 而 §4 的 TRIG-001/002/006 讲的是「光照」读数，TRIG-006/COND-003 还需要
// 「本帧没有策略监视的那个传感器」。
func trigUseSingleFieldParser(e *harness.Env, fx *autoFixture, scenarioID string) {
	e.T.Helper()
	if fx.configID == 0 {
		e.Fatalf("夹具未返回设备配置 ID，无法替换解析器")
	}
	path := "/api/v1/device-configs/" + strconv.FormatUint(uint64(fx.configID), 10)
	e.Admin.Put(path, map[string]any{
		"name": e.NS(scenarioID, "light-config"),
		"parser": map[string]any{
			"data_format": "binary",
			"fields": []map[string]any{
				{"name": "illuminance", "type": "uint16", "scale": 1.0, "offset": 0, "length": 2, "unit": "lx"},
			},
		},
	}).Expect(http.StatusOK)

	// 回读校验：PUT 的响应体好看不代表库里真的换了（后续断言全靠这个字段名）。
	cfg := e.Admin.Get(path).Expect(http.StatusOK)
	var parsed struct {
		Parser struct {
			Fields []struct {
				Name string `json:"name"`
			} `json:"fields"`
		} `json:"parser"`
	}
	cfg.Decode(&parsed)
	if len(parsed.Parser.Fields) != 1 || parsed.Parser.Fields[0].Name != "illuminance" {
		e.Fatalf("设备配置 %d 的解析器未切换到 illuminance：%s", fx.configID, autoHead(string(cfg.Data), 300))
	}
}

// trigReportLoop 按真实设备的上报节奏周期上报同一个原始值，返回停止函数。
//
// time.Sleep（经 ticker）只出现在这里 —— 它模拟的是「传感器每 N 秒报一帧」这一
// 真实物理节奏，不作为断言同步手段（框架 §3 原则 3）；断言一律走 Eventually。
func trigReportLoop(e *harness.Env, fx *autoFixture, raw uint16, interval time.Duration) func() {
	stop := make(chan struct{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			if err := fx.report(raw); err != nil {
				e.T.Errorf("后台上报 %d 失败: %v", raw, err)
				return
			}
			select {
			case <-stop:
				return
			case <-ticker.C:
			}
		}
	}()
	var once sync.Once
	stopFn := func() {
		once.Do(func() {
			close(stop)
			<-done
		})
	}
	e.T.Cleanup(stopFn)
	return stopFn
}

// trigRuleEnabled 从策略列表读某条策略的启用状态（用户界面上看得见的那个开关）。
func trigRuleEnabled(e *harness.Env, ruleID int64) bool {
	e.T.Helper()
	rows, err := autoListRules(e, "")
	if err != nil {
		e.Fatalf("%v", err)
	}
	for _, row := range rows {
		if row.ID == uint(ruleID) {
			return row.Enabled
		}
	}
	e.Fatalf("策略 %d 未出现在策略列表里", ruleID)
	return false
}

// ---------------------------------------------------------------------------
// SIM-TRIG-001 光照低于设定值就执行动作（四个比较方向都正确）
// ---------------------------------------------------------------------------

func trigRun001(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-TRIG-001", "light", "sim_trig_001_sensor")
	trigUseSingleFieldParser(e, fx, "SIM-TRIG-001")

	// 设定值 200 lx，三帧覆盖 <、=、> 三种关系，四个比较方向一次全部验证：
	//
	//	gt  200 → 只有 210 满足
	//	gte 200 → 210 与 200 都满足
	//	lt  200 → 只有 190 满足
	//	lte 200 → 190 与 200 都满足
	//
	// 期望写成「触发的值集合」而不是「有没有触发」：方向搞反（gt↔lt）会让集合
	// 从 {210} 变成 {190,200}，只看有没有事件是发现不了的。
	//
	// 三帧按 3s 节奏上报、冷却取 2s：gte/lte 各自要在两帧上分别触发一次，
	// 若连发就会被冷却压成 suppressed_cooldown（那是 DBLN 域的语义），
	// 本场景证明的是比较符，不是防抖。
	frames := []uint16{210, 190, 200}
	const frameInterval = 3 * time.Second
	cases := []struct {
		suffix     string
		comparator string
		want       []float64
	}{
		{"gt", "gt", []float64{210}},
		{"gte", "gte", []float64{210, 200}},
		{"lt", "lt", []float64{190}},
		{"lte", "lte", []float64{190, 200}},
	}

	ruleIDs := map[string]int64{}
	for _, item := range cases {
		ruleIDs[item.suffix] = autoCreateRule(e, map[string]any{
			"name":                   e.NS("SIM-TRIG-001", item.suffix),
			"enabled":                true,
			"trigger_type":           "sensor_threshold",
			"trigger_edge_device_id": fx.edgeDeviceID,
			"trigger_sensor_name":    "illuminance",
			"trigger_comparator":     item.comparator,
			"trigger_threshold":      200.0,
			"trigger_duration_sec":   0,
			"action_type":            "notification",
			"action_level":           "info",
			"cooldown_sec":           2,
		})
	}

	trigReportPaced(e, fx, frames, frameInterval)
	// 反向证明三帧都真的走完了「解析 → 入库」：否则「谁没触发」可能只是帧丢了。
	for _, raw := range frames {
		trigWaitSensorValue(e, fx.edgeDeviceID, "illuminance", float64(raw), 25*time.Second)
	}

	for _, item := range cases {
		id := ruleIDs[item.suffix]
		rows := trigWaitResult(e, id, "notification", len(item.want), 25*time.Second)
		trigAssertValues(e, fmt.Sprintf("策略 %d（illuminance %s 200）", id, item.comparator),
			rows, "notification", item.want...)
	}

	e.Evidence("SIM-TRIG-001.comparators", map[string]any{
		"edge_device_id": fx.edgeDeviceID,
		"frames_lx":      []float64{210, 190, 200},
		"threshold_lx":   200.0,
		"gt_fired":       []float64{210},
		"gte_fired":      []float64{210, 200},
		"lt_fired":       []float64{190},
		"lte_fired":      []float64{190, 200},
	})
}

// ---------------------------------------------------------------------------
// SIM-TRIG-002 数值正好等于设定值时 eq/neq 的判定与预期一致
// ---------------------------------------------------------------------------

func trigRun002(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-TRIG-002", "exact", "sim_trig_002_sensor")
	trigUseSingleFieldParser(e, fx, "SIM-TRIG-002")

	// 帧序刻意是「先不等、后相等」：若 eq 被实现成恒真或 >=，它会在第一帧
	// （190）就触发，于是触发值集合变成 {190} 而不是 {200} —— 顺序本身就带鉴别力。
	frames := []uint16{190, 200, 200}
	eqID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-TRIG-002", "eq"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "illuminance",
		"trigger_comparator":     "eq",
		"trigger_threshold":      200.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "info",
		"cooldown_sec":           300,
	})
	neqID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-TRIG-002", "neq"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "illuminance",
		"trigger_comparator":     "neq",
		"trigger_threshold":      200.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "info",
		"cooldown_sec":           300,
	})

	for _, raw := range frames {
		if err := fx.report(raw); err != nil {
			e.Fatalf("节点上报 %d lx 失败: %v", raw, err)
		}
	}
	trigWaitSensorValue(e, fx.edgeDeviceID, "illuminance", 190, 25*time.Second)
	trigWaitSensorValue(e, fx.edgeDeviceID, "illuminance", 200, 25*time.Second)

	// eq：正好等于设定值的帧触发，触发值是 200（不是 190）。
	eqRows := trigWaitResult(e, eqID, "notification", 1, 25*time.Second)
	trigAssertValues(e, "illuminance eq 200", eqRows, "notification", 200)
	// neq：只有不等的那一帧触发，触发值是 190。
	neqRows := trigWaitResult(e, neqID, "notification", 1, 25*time.Second)
	trigAssertValues(e, "illuminance neq 200", neqRows, "notification", 190)

	// 相等值连续出现两次只执行一次：第二次命中落在冷却窗内，留下抑制审计
	// （这条把「eq 不是每帧重复执行」也一并钉住，与 DBLN 域的冷却断言互为旁证）。
	// 先等到抑制审计落库再读快照：通知与抑制是两次独立的写库，
	// 「等到 1 条通知就读」可能正好读在两次写之间。
	trigWaitResult(e, eqID, "suppressed_cooldown", 1, 25*time.Second)
	eqRows = trigWaitResult(e, eqID, "notification", 1, 25*time.Second)
	if trigCountResult(eqRows, "suppressed_cooldown") == 0 {
		e.Fatalf("第二帧同样等于设定值，冷却窗内的重复命中必须留下 suppressed_cooldown 审计；事件快照 %+v", eqRows)
	}

	e.Evidence("SIM-TRIG-002.eq_neq", map[string]any{
		"edge_device_id":       fx.edgeDeviceID,
		"frames_lx":            []float64{190, 200, 200},
		"threshold_lx":         200.0,
		"eq_fired":             trigValuesOf(e, eqRows, "notification"),
		"neq_fired":            trigValuesOf(e, neqRows, "notification"),
		"eq_suppressed_count":  trigCountResult(eqRows, "suppressed_cooldown"),
		"neq_suppressed_count": trigCountResult(neqRows, "suppressed_cooldown"),
	})
}

// ---------------------------------------------------------------------------
// SIM-TRIG-003 要求「持续 N 秒都超限」时，中途回落不会误触发
// ---------------------------------------------------------------------------

func trigRun003(e *harness.Env) {
	const durationSec = 3
	fx := autoProvisionDevice(e, "SIM-TRIG-003", "dip", "sim_trig_003_sensor")

	ruleID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-TRIG-003", "duration"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   durationSec,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           300,
	})
	idText := strconv.FormatInt(ruleID, 10)

	// ① 越限一帧（23.5℃）：窗口还没攒够 duration，不触发。
	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报越限帧失败: %v", err)
	}
	trigWaitSensorValue(e, fx.edgeDeviceID, "temperature", 23.5, 25*time.Second)

	// ② 回落一帧（15.0℃）：滑动窗口里落进一个不满足样本，连续窗口被打断。
	dipAt := time.Now()
	if err := fx.report(150); err != nil {
		e.Fatalf("节点上报回落帧失败: %v", err)
	}
	trigWaitSensorValue(e, fx.edgeDeviceID, "temperature", 15.0, 25*time.Second)

	// ③ 之后按真实节奏持续越限（每 500ms 一帧）。
	stop := trigReportLoop(e, fx, 255, 500*time.Millisecond)
	defer stop()

	// ④ 负向观察窗：回落之后的 2 秒内绝不能出现事件。
	//    这一条是「duration 真的生效」的鉴别器：若窗口判定被改成恒真（变异 A7），
	//    回落帧之后的第一帧（约 0.5s 后）就会触发，立刻落进这个窗口。
	appeared := e.EventuallyError(2*time.Second, func() error {
		rows, err := autoListEvents(e, "?rule_id="+idText+"&result=notification")
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("回落打断后仍无事件（本场景期望的状态）")
		}
		return nil
	}) == nil
	if appeared {
		e.Fatalf("回落帧之后 2s 内就出现了触发事件：trigger_duration_sec=%d 的「连续满足」语义没有生效", durationSec)
	}

	// ⑤ 持续满足到点后必然触发（窗口剪枝不能漏触发）。
	rows := trigWaitResult(e, ruleID, "notification", 1, 60*time.Second)
	trigAssertValues(e, "持续满足后的触发值", rows, "notification", 25.5)

	// ⑥ 触发时刻必须比回落帧晚至少 duration —— 再次确认引擎真的等满了一个连续窗口，
	//    而不是把 duration 当成 0 直接放行。留 500ms 余量吸收网络/落库抖动。
	firedAt := trigEventTime(e, rows[0])
	if firedAt.Before(dipAt.Add(durationSec*time.Second - 500*time.Millisecond)) {
		e.Fatalf("触发时刻 %s 距回落帧 %s 仅 %s，短于 trigger_duration_sec=%ds：连续窗口未生效",
			firedAt.Format(time.RFC3339Nano), dipAt.Format(time.RFC3339Nano), firedAt.Sub(dipAt), durationSec)
	}

	e.Evidence("SIM-TRIG-003.duration", map[string]any{
		"rule_id": ruleID, "duration_sec": durationSec,
		"dip_frame_at": dipAt.Format(time.RFC3339Nano),
		"fired_at":     firedAt.Format(time.RFC3339Nano),
		"fired_value":  trigValuesOf(e, rows, "notification"),
		"dip_to_fire":  firedAt.Sub(dipAt).Round(time.Millisecond).String(),
	})
}

// ---------------------------------------------------------------------------
// SIM-TRIG-004 持续满足到点后必然触发，不会因为上报间隔而漏掉
// ---------------------------------------------------------------------------

func trigRun004(e *harness.Env) {
	const durationSec = 3
	fx := autoProvisionDevice(e, "SIM-TRIG-004", "sparse", "sim_trig_004_sensor")

	ruleID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-TRIG-004", "sparse"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   durationSec,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           300,
	})

	// 上报间隔（1s）比 duration（3s）稀疏：窗口里只有 4 个样本时才可能凑满跨度，
	// 这正是 pruneWindow「保留 cutoff 前最后一个样本」要保证的边界情形 ——
	// 少了那个样本，窗口跨度永远差一截，用户配的「持续 3 秒」就永远等不到。
	started := time.Now()
	stop := trigReportLoop(e, fx, 235, time.Second)
	defer stop()

	rows := trigWaitResult(e, ruleID, "notification", 1, 60*time.Second)
	trigAssertValues(e, "稀疏上报下的触发值", rows, "notification", 23.5)

	firedAt := trigEventTime(e, rows[0])
	if firedAt.Before(started.Add(durationSec*time.Second - 500*time.Millisecond)) {
		e.Fatalf("触发时刻 %s 距首帧 %s 仅 %s，短于 trigger_duration_sec=%ds：窗口没有等满就放行",
			firedAt.Format(time.RFC3339Nano), started.Format(time.RFC3339Nano), firedAt.Sub(started), durationSec)
	}

	e.Evidence("SIM-TRIG-004.sparse", map[string]any{
		"rule_id": ruleID, "duration_sec": durationSec, "report_interval": "1s",
		"first_frame_at": started.Format(time.RFC3339Nano),
		"fired_at":       firedAt.Format(time.RFC3339Nano),
		"fired_value":    trigValuesOf(e, rows, "notification"),
		"first_to_fire":  firedAt.Sub(started).Round(time.Millisecond).String(),
	})
}

// ---------------------------------------------------------------------------
// SIM-TRIG-005 只对指定设备的读数触发，别的设备同样超限也不触发
// ---------------------------------------------------------------------------

func trigRun005(e *harness.Env) {
	watched := autoProvisionDevice(e, "SIM-TRIG-005", "watched", "sim_trig_005_watched")
	other := autoProvisionDevice(e, "SIM-TRIG-005", "other", "sim_trig_005_other")

	watchedRule := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-TRIG-005", "watched"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": watched.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           300,
	})
	// 对照策略钉在「另一台设备」上：它的触发证明 other 的上报确实走完了
	// 上行 → 解析 → 入库 → 策略求值整条链路。没有它，watchedRule 的 0 条事件
	// 既可能是「设备限定生效」，也可能是「other 的帧根本没被处理」。
	otherRule := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-TRIG-005", "other"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": other.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           300,
	})

	// ① 别的设备同样越限（23.5℃ > 20℃）：只有对照策略触发。
	if err := other.report(235); err != nil {
		e.Fatalf("节点 %s 上报失败: %v", other.nodeID, err)
	}
	otherRows := trigWaitResult(e, otherRule, "notification", 1, 25*time.Second)
	trigAssertValues(e, "对照策略（另一台设备）", otherRows, "notification", 23.5)
	trigWaitSensorValue(e, other.edgeDeviceID, "temperature", 23.5, 25*time.Second)
	trigAssertNoResult(e, "被限定的策略在别的设备越限时",
		watchedRule, "notification", 2*time.Second)

	// ② 被限定的那台设备自己越限：必须触发。
	if err := watched.report(235); err != nil {
		e.Fatalf("节点 %s 上报失败: %v", watched.nodeID, err)
	}
	watchedRows := trigWaitResult(e, watchedRule, "notification", 1, 25*time.Second)
	trigAssertValues(e, "被限定的策略（指定设备越限）", watchedRows, "notification", 23.5)

	e.Evidence("SIM-TRIG-005.device_scope", map[string]any{
		"watched_edge_device_id":           watched.edgeDeviceID,
		"other_edge_device_id":             other.edgeDeviceID,
		"other_rule_fired":                 trigValuesOf(e, otherRows, "notification"),
		"watched_rule_fired":               trigValuesOf(e, watchedRows, "notification"),
		"watched_events_before_own_report": 0,
	})
}

// ---------------------------------------------------------------------------
// SIM-TRIG-006 上报里没有这个传感器时不会触发（不把「没数据」当「满足」）
// ---------------------------------------------------------------------------

func trigRun006(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-TRIG-006", "absent", "sim_trig_006_sensor")
	// 本设备的解析器只产出 illuminance：策略监视的 temperature 在每一帧里都不存在。
	trigUseSingleFieldParser(e, fx, "SIM-TRIG-006")

	absentRule := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-TRIG-006", "absent"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           300,
	})
	// 对照策略监视本帧真实存在的字段：它触发 = 同一帧确实被求值过。
	presentRule := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-TRIG-006", "present"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "illuminance",
		"trigger_comparator":     "gt",
		"trigger_threshold":      200.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           300,
	})

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	trigWaitSensorValue(e, fx.edgeDeviceID, "illuminance", 235, 25*time.Second)
	presentRows := trigWaitResult(e, presentRule, "notification", 1, 25*time.Second)
	trigAssertValues(e, "对照策略（本帧存在的字段）", presentRows, "notification", 235)

	// 直接证据：这一帧解析出来的物理量里根本没有 temperature 这个字段。
	trigAssertSensorAbsent(e, fx.edgeDeviceID, "temperature")
	// 不变式：字段缺失 = 不满足（fail-closed），绝不触发。
	trigAssertNoResult(e, "监视缺失字段的策略", absentRule, "notification", 2*time.Second)

	e.Evidence("SIM-TRIG-006.missing_field", map[string]any{
		"edge_device_id":     fx.edgeDeviceID,
		"rule_sensor":        "temperature（本帧不存在）",
		"present_sensor":     "illuminance=235",
		"control_rule_fired": trigValuesOf(e, presentRows, "notification"),
		"absent_rule_events": 0,
	})
}

// ---------------------------------------------------------------------------
// SIM-TRIG-007 停用规则后即使条件满足也不再触发，重新启用后恢复
// ---------------------------------------------------------------------------

func trigRun007(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-TRIG-007", "toggle", "sim_trig_007_sensor")

	ruleID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-TRIG-007", "toggle"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "warning",
		// 冷却取 1s：本场景要在「停用 → 启用」之间再做一次真实触发，
		// 冷却若取默认 300s 会把恢复后的那次合法触发压掉，断言就失去意义。
		"cooldown_sec": 1,
	})
	idText := strconv.FormatInt(ruleID, 10)

	// ① 启用状态下先真实触发一次：证明链路本来就是通的。
	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报越限帧失败: %v", err)
	}
	firstRows := trigWaitResult(e, ruleID, "notification", 1, 25*time.Second)
	trigAssertValues(e, "启用状态下的触发", firstRows, "notification", 23.5)
	// 记下进入停用阶段前的条数：停用期间的判据是「条数不增加」，
	// 而不是「一条都没有」——第一阶段本来就已经合法地产生了一条。
	beforeDisable := trigCountResult(firstRows, "notification")

	// ② 停用后，条件更明确地满足（25.5℃）也必须一声不响。
	e.Admin.Patch("/api/v1/automation-rules/"+idText+"/enabled",
		map[string]any{"enabled": false}).Expect(http.StatusOK)
	if trigRuleEnabled(e, ruleID) {
		e.Fatalf("停用后策略列表里仍显示为启用状态")
	}
	if err := fx.report(255); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	// 这一帧确实被解析入库了（否则「没有事件」可能只是帧丢了）。
	trigWaitSensorValue(e, fx.edgeDeviceID, "temperature", 25.5, 25*time.Second)
	trigAssertResultCount(e, "停用期间的越限", ruleID, "notification", beforeDisable, 2*time.Second)

	// ③ 重新启用：新的越限立刻恢复触发，且触发值只能是启用之后那一帧的值。
	e.Admin.Patch("/api/v1/automation-rules/"+idText+"/enabled",
		map[string]any{"enabled": true}).Expect(http.StatusOK)
	if !trigRuleEnabled(e, ruleID) {
		e.Fatalf("重新启用后策略列表里仍显示为停用状态")
	}
	if err := fx.report(265); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	secondRows := trigWaitResult(e, ruleID, "notification", 2, 30*time.Second)
	// 集合里必须恰好是 23.5 与 26.5：25.5 那一帧（停用期间）绝不能留下事件。
	trigAssertValues(e, "重新启用后的触发", secondRows, "notification", 23.5, 26.5)

	e.Evidence("SIM-TRIG-007.enabled_toggle", map[string]any{
		"rule_id":                ruleID,
		"fired_while_enabled":    []float64{23.5},
		"fired_while_disabled":   []float64{},
		"fired_after_reenable":   trigValuesOf(e, secondRows, "notification"),
		"disabled_frame_value":   25.5,
		"disabled_frame_ignored": trigCountResult(secondRows, "notification") == 2,
	})
}

// ---------------------------------------------------------------------------
// SIM-TRIG-008 一次上报同时满足多条规则时，每条各自独立触发一次
// ---------------------------------------------------------------------------

func trigRun008(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-TRIG-008", "fanout", "sim_trig_008_sensor")

	// 同一台设备、同一帧（23.5℃）同时满足三条不同策略：gt 20 / gte 23.5 / lte 23.5。
	cases := []struct {
		suffix     string
		comparator string
		threshold  float64
	}{
		{"gt", "gt", 20.0},
		{"gte", "gte", 23.5},
		{"lte", "lte", 23.5},
	}
	ruleIDs := make([]int64, 0, len(cases))
	for _, item := range cases {
		ruleIDs = append(ruleIDs, autoCreateRule(e, map[string]any{
			"name":                   e.NS("SIM-TRIG-008", item.suffix),
			"enabled":                true,
			"trigger_type":           "sensor_threshold",
			"trigger_edge_device_id": fx.edgeDeviceID,
			"trigger_sensor_name":    "temperature",
			"trigger_comparator":     item.comparator,
			"trigger_threshold":      item.threshold,
			"trigger_duration_sec":   0,
			"action_type":            "notification",
			"action_level":           "info",
			"cooldown_sec":           300,
		}))
	}

	// 只上报一帧。
	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	trigWaitSensorValue(e, fx.edgeDeviceID, "temperature", 23.5, 25*time.Second)

	// 不变式：每条策略各自有一条自己的事件（互不吞噬）。
	eventIDs := map[uint]int64{}
	fired := map[string][]float64{}
	for i, item := range cases {
		id := ruleIDs[i]
		rows := trigWaitResult(e, id, "notification", 1, 25*time.Second)
		trigAssertValues(e, fmt.Sprintf("策略 %d（%s %.1f）", id, item.comparator, item.threshold),
			rows, "notification", 23.5)
		row := rows[0]
		if row.RuleID != uint(id) {
			e.Fatalf("策略 %d 的事件 rule_id=%d 对不上", id, row.RuleID)
		}
		if other, duplicated := eventIDs[row.ID]; duplicated {
			e.Fatalf("事件 %d 同时被归到策略 %d 与 %d —— 多规则求值互相吞噬", row.ID, other, id)
		}
		eventIDs[row.ID] = id
		fired[item.comparator] = trigValuesOf(e, rows, "notification")
	}
	if len(eventIDs) != len(cases) {
		e.Fatalf("一帧上报应产生 %d 条各自独立的事件，实际只有 %d 条", len(cases), len(eventIDs))
	}

	e.Evidence("SIM-TRIG-008.fanout", map[string]any{
		"edge_device_id": fx.edgeDeviceID,
		"frame_value":    23.5,
		"rule_ids":       ruleIDs,
		"fired":          fired,
	})
}
