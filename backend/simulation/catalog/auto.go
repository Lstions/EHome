//go:build simulation

// 场景目录 · SIM-AUTO 自动化策略（设计 §9 SIM-AUTO-001..006）。
//
// 契约：docs/设计/场景仿真验证框架.md（§5 harness API、§5.4 场景模型、§7 红线、§9 场景清单）。
// 设计依据：docs/设计/自动化策略引擎方案.md、docs/设计/自动化确认制闭环实现方案.md。
package catalog

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"ehome/backend/pkg/frame"
	"ehome/backend/simulation/harness"
)

// 本域守护的不变量（每条断言都对应其中之一）：
//  1. 规则是声明式的“条件 → 动作”，创建后即可在列表/详情读到（用户可见的持久化事实）；
//  2. 条件成立只能来自真实数据流（MQTT nodes/<id>/up → nodemgr → DataEventBus →
//     SensorParserConsumer 解析后回调）。HTTP 侧没有任何“造数据”入口，因此本域所有
//     “条件成立”场景都必须先让仿真节点真上报一帧；
//  3. 条件不成立时绝不产生事件（无误报）——用同一设备上的“对照规则”证明链路是活的，
//     否则“0 条事件”可能只是链路根本没跑；
//  4. require_confirmed 的规则只落 pending_confirm + 待确认通知，人工确认后才真正下发；
//  5. 手动触发是“用户点击即确认”，但审计上必须可区分（trigger_source=manual）。

func init() {
	Register(Scenario{
		ID:     "SIM-AUTO-001",
		Title:  "管理员建好一条“温度高了就通知我”的策略后，能在策略列表里查到它",
		Domain: DomainAUTO,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-AUTO-001；docs/设计/自动化策略引擎方案.md",
		Run:    autoRun001,
	})
	Register(Scenario{
		ID:     "SIM-AUTO-002",
		Title:  "室温升过设定值后，策略自动执行动作并留下一条执行记录",
		Domain: DomainAUTO,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-AUTO-002；docs/设计/自动化策略引擎方案.md",
		Run:    autoRun002,
	})
	Register(Scenario{
		ID:     "SIM-AUTO-003",
		Title:  "温度没到设定值时策略不会误报（同一条数据只触发该触发的那条策略）",
		Domain: DomainAUTO,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-AUTO-003；docs/设计/自动化策略引擎方案.md",
		Run:    autoRun003,
	})
	Register(Scenario{
		ID:     "SIM-AUTO-004",
		Title:  "需要人工确认的策略触发后停在“待确认”，不会自己动手",
		Domain: DomainAUTO,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-AUTO-004；docs/设计/自动化确认制闭环实现方案.md",
		Run:    autoRun004,
	})
	Register(Scenario{
		ID:     "SIM-AUTO-005",
		Title:  "管理员确认待确认的策略后，动作真正下发且事件闭环",
		Domain: DomainAUTO,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-AUTO-005；docs/设计/自动化确认制闭环实现方案.md",
		Run:    autoRun005,
	})
	Register(Scenario{
		ID:     "SIM-AUTO-006",
		Title:  "管理员点“立即触发”后策略马上产生一条手动触发的执行记录",
		Domain: DomainAUTO,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-AUTO-006；docs/设计/自动化策略引擎方案.md",
		Run:    autoRun006,
	})
}

// ---------------------------------------------------------------------------
// 领域数据类型与共用工具
// ---------------------------------------------------------------------------

// autoRuleRow 只取断言需要的字段（models.AutomationRule 的 JSON 形状）。
type autoRuleRow struct {
	ID                  uint    `json:"id"`
	Name                string  `json:"name"`
	Enabled             bool    `json:"enabled"`
	TriggerType         string  `json:"trigger_type"`
	TriggerSensorName   string  `json:"trigger_sensor_name"`
	TriggerComparator   string  `json:"trigger_comparator"`
	TriggerThreshold    float64 `json:"trigger_threshold"`
	TriggerDurationSec  int     `json:"trigger_duration_sec"`
	TriggerEdgeDeviceID uint    `json:"trigger_edge_device_id"`
	// 时间窗口三件套（设计/自动化引擎场景仿真验证.md SIM-WIND 子域需要）。
	// 加在这里而不是各子域各自声明副本：autoRuleRow 是**自动化域共用的**规则行类型，
	// 同包内多处读同一份 API 响应时应共用一个形状，否则字段一多就会漂移。
	TriggerWindowStart string `json:"trigger_window_start"`
	TriggerWindowEnd   string `json:"trigger_window_end"`
	TriggerWindowEdge  string `json:"trigger_window_edge"`
	ActionType         string `json:"action_type"`
	ActionDeviceID     uint   `json:"action_device_id"`
	ActionID           string `json:"action_id"`
	ActionLevel        string `json:"action_level"`
	CooldownSec        int    `json:"cooldown_sec"`
	MaxDailyExec       int    `json:"max_daily_exec"`
	RequireConfirmed   bool   `json:"require_confirmed"`
}

// autoEventRow 只取断言需要的字段（models.AutomationEvent 的 JSON 形状）。
type autoEventRow struct {
	ID            uint     `json:"id"`
	RuleID        uint     `json:"rule_id"`
	TriggeredAt   string   `json:"triggered_at"`
	TriggerValue  *float64 `json:"trigger_value"`
	TriggerSource string   `json:"trigger_source"`
	Result        string   `json:"result"`
	CommandID     string   `json:"command_id"`
	Detail        string   `json:"detail"`
}

// autoListRules 读策略列表（可带 trigger_type/action_type 之类的过滤串）。
func autoListRules(e *harness.Env, query string) ([]autoRuleRow, error) {
	r := e.Admin.Get("/api/v1/automation-rules" + query)
	if r.Status != http.StatusOK {
		return nil, fmt.Errorf("GET /api/v1/automation-rules%s 返回 %d: %s", query, r.Status, r.BodyString())
	}
	var rows []autoRuleRow
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		return nil, fmt.Errorf("解析策略列表失败: %w（data=%s）", err, autoHead(string(r.Data), 200))
	}
	return rows, nil
}

// autoListEvents 读策略事件（可带 rule_id/result 过滤串）。
func autoListEvents(e *harness.Env, query string) ([]autoEventRow, error) {
	r := e.Admin.Get("/api/v1/automation-events" + query)
	if r.Status != http.StatusOK {
		return nil, fmt.Errorf("GET /api/v1/automation-events%s 返回 %d: %s", query, r.Status, r.BodyString())
	}
	var rows []autoEventRow
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		return nil, fmt.Errorf("解析策略事件失败: %w（data=%s）", err, autoHead(string(r.Data), 200))
	}
	return rows, nil
}

// autoCreateRule 创建一条策略并把它的自清理挂到当前场景上（§5.6 场景自清理）。
func autoCreateRule(e *harness.Env, body map[string]any) int64 {
	t := e.T
	t.Helper()
	created := e.Admin.Post("/api/v1/automation-rules", body).Expect(http.StatusOK)
	id := created.DataInt("id")
	if id == 0 {
		e.Fatalf("创建策略未返回 id: %s", created.BodyString())
	}
	t.Cleanup(func() {
		autoCleanup(t, "自动化策略",
			e.Admin.Delete("/api/v1/automation-rules/"+strconv.FormatInt(id, 10)), http.StatusOK)
	})
	return id
}

// autoCleanup 清理阶段的断言：清理失败必须显式失败（不静默），
// 用 Errorf 而非 Fatalf，保证同场景其余清理步骤仍会执行。
func autoCleanup(t *testing.T, what string, r *harness.Response, want int) {
	t.Helper()
	if r.Status != want {
		t.Errorf("清理 %s 失败：期望 HTTP %d，实际 %d，body=%s", what, want, r.Status, autoHead(r.BodyString(), 300))
	}
}

// autoEventuallyFloat 断言实测浮点数与期望值在容差内一致。
// 为什么不用 ==：物理量来自 ConfigParser 的 value*scale（scale=0.1 不是二进制精确值），
// 直接比较会因最后一位舍入而假红；容差 1e-3 远小于任何业务刻度。
func autoEventuallyFloat(t *testing.T, label string, got, want float64) {
	t.Helper()
	if math.Abs(got-want) > 1e-3 {
		t.Fatalf("%s = %v，期望 %v（容差 1e-3）", label, got, want)
	}
}

// autoProvisionDevice 用真实产品端点搭出
// “MQTT nodes/<id>/up → nodemgr → DataEventBus → SensorParserConsumer → 统一数据/告警/策略求值”
// 这条链需要的全部前置数据（一律走真实 API，不直连数据库造数）：
//
//	POST /api/v1/nodes            建节点，node_id 与 MQTT 仿真设备同名
//	POST /api/v1/device-configs   建带 binary 解析器的设备配置（uint16 @0, scale 0.1 → ℃）
//	POST /api/v1/edge-devices     内联建一条 uart 通道并绑定该配置
//
// deviceType 决定边缘设备的 type：
//   - 传场景私有类型（如 sim_auto_002_sensor）时，SensorParserConsumer 走
//     DeviceConfig.Parser 分支，不依赖驱动注册表，也不需要校准数据；
//   - 传 "sn3001_rain" 时边缘设备同时具备动作目录（commandexec 的 action_id 存在性
//     校验与 device_action 动作都要求它），而解析仍走 DeviceConfig.Parser
//     （SN3001RainDriver 不是 CalibrationAwareDriver，不会被驱动分支截走）。
//
// 该夹具同时被 alert.go 复用（同包）。
func autoProvisionDevice(e *harness.Env, scenarioID, suffix, deviceType string) *autoFixture {
	t := e.T
	t.Helper()

	// e.DeviceFor 生成「运行 + 场景」双重命名空间（设计 §5.6 v1.3）：
	// 仅用 e.Device(local) 时隔离性退化成"各域作者恰好选中不同后缀"，
	// 两个域同时选 "rule" 就会直接 409。
	dev := e.DeviceFor(scenarioID, suffix)
	name := e.NS(scenarioID, suffix)

	e.Admin.Post("/api/v1/nodes", map[string]any{
		"node_id": dev.NodeID,
		"name":    name + "-节点",
	}).Expect(http.StatusCreated)
	t.Cleanup(func() {
		autoCleanup(t, "节点 "+dev.NodeID, e.Admin.Delete("/api/v1/nodes/"+dev.NodeID), http.StatusOK)
	})

	cfg := e.Admin.Post("/api/v1/device-configs", map[string]any{
		"name":          name + "-配置",
		"device_type":   deviceType,
		"hardware_type": "uart",
		"status":        "active",
		"parser": map[string]any{
			"data_format": "binary",
			"fields": []map[string]any{
				{"name": "temperature", "type": "uint16", "scale": 0.1, "offset": 0, "length": 2, "unit": "℃"},
			},
		},
	}).Expect(http.StatusCreated)
	cfgID := cfg.DataInt("id")
	t.Cleanup(func() {
		autoCleanup(t, "设备配置",
			e.Admin.Delete("/api/v1/device-configs/"+strconv.FormatInt(cfgID, 10)), http.StatusOK)
	})

	edge := e.Admin.Post("/api/v1/edge-devices", map[string]any{
		"name":             name + "-设备",
		"node_id":          dev.NodeID,
		"device_config_id": cfgID,
		"enabled":          true,
		// bus_config 是 UART 通道的引脚路由（hex: tx,rx）。它不是可选项：
		// nodemgr 的 validateManifestAuthority 会拒收"启用但 bus_config 不可解码"的
		// 通道，导致该节点的配置清单永远推不下去（config_status 卡在 failed）。
		// 04/05 与节点 ResourceReport 上报的 uart0 默认引脚一致。
		"channel": map[string]any{"hardware_type": "uart", "bus_config": "0405"},
	}).Expect(http.StatusCreated)
	edgeID := edge.DataInt("id")
	channelID := edge.DataInt("channel_id")
	if edgeID == 0 || channelID == 0 {
		e.Fatalf("边缘设备创建返回不完整: id=%d channel_id=%d body=%s", edgeID, channelID, edge.BodyString())
	}
	t.Cleanup(func() {
		autoCleanup(t, "边缘设备",
			e.Admin.Delete("/api/v1/edge-devices/"+strconv.FormatInt(edgeID, 10)), http.StatusOK)
	})

	if err := dev.Connect(); err != nil {
		e.Fatalf("MQTT 仿真设备连接失败（%s）: %v", dev.NodeID, err)
	}
	t.Cleanup(dev.Close)

	dev.ChannelID = uint32(channelID)
	dev.EdgeDeviceID = uint32(edgeID)

	return &autoFixture{
		nodeID:       dev.NodeID,
		edgeDeviceID: uint(edgeID),
		configID:     uint(cfgID),
		channelID:    uint(channelID),
		device:       dev,
	}
}

// autoFixture 是“节点 + 通道 + 边缘设备 + MQTT 仿真设备”这套前置数据的句柄。
type autoFixture struct {
	nodeID       string
	edgeDeviceID uint
	configID     uint // 该设备的 DeviceConfig 主键（供"同型号重复登记"这类用例复用）
	channelID    uint
	device       *harness.Device
}

// report 让仿真设备上报一个温度原值（uint16 大端；解析规则 scale=0.1 → 摄氏度）。
func (f *autoFixture) report(raw uint16) error {
	return f.device.DataReport(uint32(f.channelID), uint64(time.Now().UnixMilli()),
		[]byte{byte(raw >> 8), byte(raw)})
}

// autoManifestIDs 从 ConfigManifest(0x04) 原始字节里取出 field1=manifest_id 与
// field8=sync_id —— 它们是 ConfigResult(0x05) 必须原样回显的两个身份字段
// （backend/internal/nodemgr/handler_config.go 的过期结果判定）。
func autoManifestIDs(raw []byte) (string, string, error) {
	fields, err := harness.DecodeFrameFields(raw)
	if err != nil {
		return "", "", fmt.Errorf("解码 ConfigManifest 失败: %w", err)
	}
	manifestField, okManifest := fields[1]
	syncField, okSync := fields[8]
	if !okManifest || !okSync {
		return "", "", fmt.Errorf("ConfigManifest 缺少 field1(manifest_id)/field8(sync_id)，实际字段数 %d", len(fields))
	}
	return frame.GetString(&manifestField), frame.GetString(&syncField), nil
}

// ---------------------------------------------------------------------------
// SIM-AUTO-001 创建“条件 → 动作”自动化规则并可在列表查询
// ---------------------------------------------------------------------------

func autoRun001(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-AUTO-001", "rule", "sim_auto_001_sensor")
	name := e.NS("SIM-AUTO-001", "rule")

	// 条件：本场景设备上温度 > 20.0 ℃ 时，发一条 warning 通知。
	ruleID := autoCreateRule(e, map[string]any{
		"name":                   name,
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           60,
		"max_daily_exec":         0,
	})

	// 不变式 1：创建响应必须原样回显用户填的“条件 → 动作”（不是只回一个 id）。
	detail := e.Admin.Get("/api/v1/automation-rules/" + strconv.FormatInt(ruleID, 10)).
		Expect(http.StatusOK)
	var created autoRuleRow
	detail.Decode(&created)
	if created.ID != uint(ruleID) {
		e.Fatalf("详情返回的 id=%d，期望 %d", created.ID, ruleID)
	}
	if created.Name != name || created.TriggerType != "sensor_threshold" ||
		created.TriggerSensorName != "temperature" || created.TriggerComparator != "gt" ||
		created.TriggerEdgeDeviceID != fx.edgeDeviceID || created.ActionType != "notification" ||
		created.ActionLevel != "warning" {
		e.Fatalf("策略详情回显与请求不一致: %+v", created)
	}
	autoEventuallyFloat(e.T, "trigger_threshold", created.TriggerThreshold, 20.0)
	if !created.Enabled {
		e.Fatalf("新建策略默认应为启用，实际 enabled=false（%+v）", created)
	}

	// 不变式 2：列表查询能看到它（默认全量 + 按 trigger_type/action_type 过滤三种口径）。
	for _, query := range []string{"", "?trigger_type=sensor_threshold", "?action_type=notification"} {
		rows, err := autoListRules(e, query)
		if err != nil {
			e.Fatalf("%v", err)
		}
		found := false
		for _, row := range rows {
			if row.ID == uint(ruleID) {
				found = true
				if row.Name != name {
					e.Fatalf("列表里 id=%d 的 name=%q，期望 %q", ruleID, row.Name, name)
				}
			}
		}
		if !found {
			e.Fatalf("策略 %d 未出现在 GET /automation-rules%s 的 %d 条结果中", ruleID, query, len(rows))
		}
	}

	// 不变式 3：启停开关是用户可见状态，列表必须跟着变。
	e.Admin.Patch("/api/v1/automation-rules/"+strconv.FormatInt(ruleID, 10)+"/enabled",
		map[string]any{"enabled": false}).Expect(http.StatusOK)
	rows, err := autoListRules(e, "")
	if err != nil {
		e.Fatalf("%v", err)
	}
	for _, row := range rows {
		if row.ID == uint(ruleID) && row.Enabled {
			e.Fatalf("停用后列表里的策略 %d 仍为 enabled=true", ruleID)
		}
	}

	e.Evidence("SIM-AUTO-001.rule", map[string]any{
		"id": ruleID, "name": name, "edge_device_id": fx.edgeDeviceID,
		"trigger": "temperature gt 20.0", "action": "notification/warning",
	})
}

// ---------------------------------------------------------------------------
// SIM-AUTO-002 条件满足时自动触发动作并产生事件
// ---------------------------------------------------------------------------

func autoRun002(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-AUTO-002", "trigger", "sim_auto_002_sensor")
	name := e.NS("SIM-AUTO-002", "trigger")

	ruleID := autoCreateRule(e, map[string]any{
		"name":                   name,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0, // 0 = 本点满足即触发
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           60,
		"max_daily_exec":         0,
	})

	// 真实数据流：235 * 0.1 = 23.5 ℃ > 20.0 ℃。
	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}

	// 收敛条件：该策略下出现一条 result=notification 的自动事件。
	var fired autoEventRow
	e.Eventually(25*time.Second, func() error {
		rows, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10))
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Result == "notification" {
				fired = row
				return nil
			}
		}
		return fmt.Errorf("策略 %d 尚未产生 notification 事件（当前 %d 条）", ruleID, len(rows))
	})

	// 不变式：事件必须能被归因到“哪条策略、什么来源、触发时值多少”。
	if fired.RuleID != uint(ruleID) {
		e.Fatalf("事件 rule_id=%d，期望 %d", fired.RuleID, ruleID)
	}
	if fired.TriggerSource != "auto" {
		e.Fatalf("自动触发的事件 trigger_source=%q，期望 auto", fired.TriggerSource)
	}
	if fired.TriggerValue == nil {
		e.Fatalf("sensor_threshold 事件必须带 trigger_value，实际为空（event=%+v）", fired)
	}
	autoEventuallyFloat(e.T, "事件的 trigger_value", *fired.TriggerValue, 23.5)

	// 不变式：notification 动作必须在通知中心留下用户可见的落地行。
	var landed bool
	e.Eventually(15*time.Second, func() error {
		rows, err := autoNotifications(e)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Source == "automation_rule" && row.SourceID == strconv.FormatInt(ruleID, 10) {
				landed = true
				if row.Read {
					return fmt.Errorf("新通知 %d 竟是已读状态", row.ID)
				}
				return nil
			}
		}
		return fmt.Errorf("通知中心尚无策略 %d 的通知（共 %d 条）", ruleID, len(rows))
	})
	if !landed {
		e.Fatalf("通知未落地")
	}

	e.Evidence("SIM-AUTO-002.event", map[string]any{
		"rule_id": ruleID, "result": fired.Result,
		"trigger_source": fired.TriggerSource, "trigger_value": *fired.TriggerValue,
	})
}

// ---------------------------------------------------------------------------
// SIM-AUTO-003 条件不满足时不产生事件（无误报）
// ---------------------------------------------------------------------------

// autoSensorSample 只取断言需要的字段（models.UnifiedData 的 JSON 形状）。
type autoSensorSample struct {
	SensorName string  `json:"sensor_name"`
	Value      float64 `json:"value"`
}

func autoRun003(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-AUTO-003", "guard", "sim_auto_003_sensor")

	// 被观察的规则：阈值远高于实际上报值 → 绝不该触发。
	guardedID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-AUTO-003", "no-false-alarm"),
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      100.0,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           60,
	})

	// 对照规则：同一台设备、同一个传感器，阈值低于上报值 → 必须触发。
	// 没有它，“0 条事件”既可能是“没有误报”，也可能是“链路根本没跑”，两者无法区分。
	controlID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-AUTO-003", "control"),
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           60,
	})

	// 连报三帧，保证求值回调被真实调用多次（每帧一次 Evaluate）。
	const frames = 3
	for i := 0; i < frames; i++ {
		if err := fx.report(235); err != nil {
			e.Fatalf("节点第 %d 次上报失败: %v", i+1, err)
		}
	}

	// 先证明数据确实流过了同一条链路：统一数据里必须出现本设备的 23.5 ℃。
	e.Eventually(25*time.Second, func() error {
		r := e.Admin.Get("/api/v1/devices/" + strconv.FormatUint(uint64(fx.edgeDeviceID), 10) + "/sensor-data?limit=50")
		if r.Status != http.StatusOK {
			return fmt.Errorf("GET /devices/%d/sensor-data 返回 %d: %s", fx.edgeDeviceID, r.Status, r.BodyString())
		}
		var samples []autoSensorSample
		if err := json.Unmarshal(r.Data, &samples); err != nil {
			return fmt.Errorf("解析统一数据失败: %w", err)
		}
		for _, sample := range samples {
			if sample.SensorName == "temperature" && math.Abs(sample.Value-23.5) <= 1e-3 {
				return nil
			}
		}
		return fmt.Errorf("本设备尚无 temperature=23.5 的统一数据（当前 %d 条）", len(samples))
	})

	// 对照规则必须触发 —— 证明求值链路是活的。
	e.Eventually(20*time.Second, func() error {
		rows, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(controlID, 10))
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("对照策略 %d 未触发，无法证明求值链路是活的", controlID)
		}
		return nil
	})

	// 不变式：同一批数据下，阈值未达的规则一条事件都不能有。
	rows, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(guardedID, 10))
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(rows) != 0 {
		e.Fatalf("阈值未达的策略 %d 产生了 %d 条事件（误报）: %+v", guardedID, len(rows), rows[0])
	}

	e.Evidence("SIM-AUTO-003.no_false_alarm", map[string]any{
		"guarded_rule_id": guardedID, "guarded_events": len(rows),
		"control_rule_id": controlID, "frames_reported": frames,
	})
}

// ---------------------------------------------------------------------------
// SIM-AUTO-004 需人工确认的规则产生“待确认”事件
// ---------------------------------------------------------------------------

func autoRun004(e *harness.Env) {
	// 设备类型必须是带动作目录的驱动型号：require_confirmed 只对 device_action 有意义，
	// 而 device_action 的 action_id 会在创建时被 Catalog 校验存在性。
	fx := autoProvisionDevice(e, "SIM-AUTO-004", "confirm", "sn3001_rain")
	name := e.NS("SIM-AUTO-004", "confirm")

	// reset_rainfall：risk=high + bounded_sequence + readback，是目录里可被人工确认的动作。
	ruleID := autoCreateRule(e, map[string]any{
		"name":                   name,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "device_action",
		"action_device_id":       fx.edgeDeviceID,
		"action_id":              "reset_rainfall",
		"action_params_json":     "{}",
		"require_confirmed":      true,
		"cooldown_sec":           60,
	})

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}

	var pending autoEventRow
	e.Eventually(25*time.Second, func() error {
		rows, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10))
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Result == "pending_confirm" {
				pending = row
				return nil
			}
		}
		return fmt.Errorf("策略 %d 尚未产生 pending_confirm 事件（当前 %d 条）", ruleID, len(rows))
	})

	// 不变式 1：待确认事件绝不能已经执行（确认制的核心：没有人工确认就不动手）。
	if pending.CommandID != "" {
		e.Fatalf("待确认事件的 command_id=%q —— 未经人工确认就下发了动作", pending.CommandID)
	}
	if pending.TriggerSource != "auto" {
		e.Fatalf("自动触发的事件 trigger_source=%q，期望 auto", pending.TriggerSource)
	}

	// 不变式 2：必须同时产生“建议执行”的待确认通知（否则用户不知道要确认什么）。
	e.Eventually(15*time.Second, func() error {
		rows, err := autoNotifications(e)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Source == "automation_rule" && row.SourceID == strconv.FormatInt(ruleID, 10) {
				return nil
			}
		}
		return fmt.Errorf("通知中心尚无策略 %d 的待确认通知", ruleID)
	})

	e.Evidence("SIM-AUTO-004.pending", map[string]any{
		"rule_id": ruleID, "event_id": pending.ID, "result": pending.Result,
	})
}

// ---------------------------------------------------------------------------
// SIM-AUTO-005 人工确认后动作执行且事件闭环
// ---------------------------------------------------------------------------

func autoRun005(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-AUTO-005", "approve", "sn3001_rain")
	name := e.NS("SIM-AUTO-005", "approve")

	// 让节点具备“可被 commandexec 接纳”的运行时事实。这些事实只能由 MQTT 帧写入，
	// HTTP 侧没有任何写入口（已全仓 grep 确认）：
	//   ResourceReport → boot_id / resource_reported_at / command_engine_* /
	//                    hardware_info.channels[{id,enabled}]
	//   ConfigResult   → config_status=applied + config_sync_state=in_sync
	// 顺序不可颠倒：Hello 会主动清空上一代能力报告（nodemgr/handler_hello.go:222）。
	fx.device.HelloThenReport("1.0.0", "sim-c6", 1, []harness.ReportedChannel{
		{ID: uint64(fx.channelID), Enabled: true},
	})

	// ① ResourceReport 是异步落库的；服务端在它到达之前算不出合法清单
	//    （validateManifestAuthority 要求节点已上报总线事实），会把这次下发判为 failed。
	//    因此先等能力事实真正可经 HTTP 看见，再谈回执。
	e.Eventually(20*time.Second, func() error {
		node := e.Admin.Get("/api/v1/nodes/" + fx.nodeID)
		if err := node.Check(http.StatusOK); err != nil {
			return err
		}
		caps := strings.TrimSpace(node.DataString("capabilities"))
		if caps == "" || caps == "{}" {
			return fmt.Errorf("节点 %s 的总线能力尚未落库（capabilities=%q）", fx.nodeID, caps)
		}
		if node.DataString("boot_id") == "" || node.DataInt("command_engine_revision") == 0 {
			return fmt.Errorf("节点 %s 的固件能力事实不完整（boot_id=%q revision=%d）",
				fx.nodeID, node.DataString("boot_id"), node.DataInt("command_engine_revision"))
		}
		return nil
	})

	// ② 能力就绪后强制生成一份**新的**清单：能力上报之前推送的那份已被服务端
	//    自己拒收（sync 状态为 failed），其 sync_id 再也无法被回执命中。
	if err := e.Admin.Post("/api/v1/nodes/"+fx.nodeID+"/config/sync", map[string]any{}).
		Check(http.StatusOK); err != nil {
		e.Fatalf("触发强制配置下发失败: %v", err)
	}

	// ③ 回执“最近一帧 ConfigManifest”的 manifest_id + sync_id（服务端只认最新一份，
	//    过期回执被忽略且不改状态）。轮询而非 sleep：帧到达时刻不可预知。
	var manifestID, syncID string
	e.Eventually(30*time.Second, func() error {
		node := e.Admin.Get("/api/v1/nodes/" + fx.nodeID)
		if err := node.Check(http.StatusOK); err != nil {
			return err
		}
		if node.DataString("config_status") == "applied" && node.DataString("config_sync_state") == "in_sync" {
			return nil
		}
		frames := fx.device.FramesOf(frame.MsgConfigMfst)
		if len(frames) == 0 {
			return fmt.Errorf("尚未收到 ConfigManifest")
		}
		latest := frames[len(frames)-1].Raw
		manifest, sync, err := autoManifestIDs(latest)
		if err != nil {
			return err
		}
		manifestID, syncID = manifest, sync
		if err := fx.device.ConfigResult(manifestID, syncID, true); err != nil {
			return fmt.Errorf("上报 ConfigResult 失败: %w", err)
		}
		return fmt.Errorf("已回执清单 %s（sync=%s），等待节点状态收敛", manifestID, syncID)
	})
	e.Evidence("SIM-AUTO-005.node_manifest", map[string]any{"manifest_id": manifestID, "sync_id": syncID})

	ruleID := autoCreateRule(e, map[string]any{
		"name":                   name,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "device_action",
		"action_device_id":       fx.edgeDeviceID,
		"action_id":              "reset_rainfall",
		"action_params_json":     "{}",
		"require_confirmed":      true,
		"cooldown_sec":           60,
	})

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}

	var pending autoEventRow
	e.Eventually(25*time.Second, func() error {
		rows, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10)+"&result=pending_confirm")
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("策略 %d 尚未产生 pending_confirm 事件", ruleID)
		}
		pending = rows[0]
		return nil
	})

	// 人工确认前的近认证门（commandexec 的 10 分钟窗口）只能靠重新登录刷新：
	// 产品里并不存在 POST /auth/manual-confirmation 端点 —— handler_automation.go:507
	// 的注释指向了一个未实现的路由（已全仓核实，属注释与实现不符）。
	fresh, err := e.Session(e.AdminUser, e.AdminPass)
	if err != nil {
		e.Fatalf("重新登录以刷新近认证时间失败: %v", err)
	}

	confirmed := fresh.Post("/api/v1/automation-events/"+strconv.FormatUint(uint64(pending.ID), 10)+"/confirm",
		map[string]any{}).Expect(http.StatusOK)
	var closed autoEventRow
	confirmed.Decode(&closed)

	// 不变式 1：确认后事件必须闭环为 executed，且带上真实下发的指令号。
	if closed.Result != "executed" {
		e.Fatalf("人工确认后事件 result=%q，期望 executed（detail=%q）", closed.Result, closed.Detail)
	}
	if closed.CommandID == "" {
		e.Fatalf("executed 事件必须带 command_id（回链 command_executions），实际为空")
	}
	if closed.ID != pending.ID {
		e.Fatalf("确认返回的事件 id=%d，期望 %d", closed.ID, pending.ID)
	}

	// 不变式 2：列表口径读到的是同一事实（不是只在确认响应里“看起来成功”）。
	e.Eventually(10*time.Second, func() error {
		rows, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10)+"&result=executed")
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.ID == pending.ID && row.CommandID != "" {
				return nil
			}
		}
		return fmt.Errorf("策略 %d 的执行记录尚未收敛（当前 %d 条 executed）", ruleID, len(rows))
	})

	// 不变式 3：已闭环的事件不得再被确认（条件 UPDATE 是第一道防重闸）。
	again := fresh.Post("/api/v1/automation-events/"+strconv.FormatUint(uint64(pending.ID), 10)+"/confirm",
		map[string]any{})
	if again.Status >= 200 && again.Status < 300 {
		e.Fatalf("已闭环的事件 %d 竟可被重复确认（status=%d body=%s）", pending.ID, again.Status, again.BodyString())
	}
	if again.Status < 400 || again.Status >= 500 {
		e.Fatalf("重复确认应返回 4xx（非待确认语义），实际 %d body=%s", again.Status, again.BodyString())
	}

	e.Evidence("SIM-AUTO-005.closed_loop", map[string]any{
		"rule_id": ruleID, "event_id": pending.ID,
		"result": closed.Result, "command_id": closed.CommandID,
		"duplicate_confirm_status": again.Status,
	})
}

// ---------------------------------------------------------------------------
// SIM-AUTO-006 手动触发规则立即产生事件
// ---------------------------------------------------------------------------

func autoRun006(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-AUTO-006", "manual", "sim_auto_006_sensor")

	ruleID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-AUTO-006", "manual"),
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "notification",
		"action_level":           "info",
		"cooldown_sec":           0,
	})

	// 前置：本场景一帧数据都没上报，自动路径不可能产生事件。
	// 这样“手动触发后的事件”才能唯一归因于用户点击。
	before, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10))
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(before) != 0 {
		e.Fatalf("点击前策略 %d 已有 %d 条事件，无法把手动触发归因", ruleID, len(before))
	}

	triggered := e.Admin.Post("/api/v1/automation-rules/"+strconv.FormatInt(ruleID, 10)+"/trigger",
		map[string]any{}).Expect(http.StatusOK)
	var ev autoEventRow
	triggered.Decode(&ev)

	// 不变式 1：点击必须立即返回落库的事件本身（含结果），而不是“已受理”。
	if ev.ID == 0 || ev.RuleID != uint(ruleID) {
		e.Fatalf("手动触发返回的事件不完整: %+v", ev)
	}
	if ev.Result != "notification" {
		e.Fatalf("手动触发 notification 策略的 result=%q，期望 notification", ev.Result)
	}
	// 不变式 2：审计上必须与自动触发可区分。
	if ev.TriggerSource != "manual" {
		e.Fatalf("手动触发的事件 trigger_source=%q，期望 manual", ev.TriggerSource)
	}

	// 不变式 3：列表口径读到同一条事实，且只有它一条。
	rows, err := autoListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10))
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(rows) != 1 {
		e.Fatalf("手动触发后策略 %d 应有且仅有 1 条事件，实际 %d 条: %+v", ruleID, len(rows), rows)
	}
	if rows[0].ID != ev.ID || rows[0].TriggerSource != "manual" {
		e.Fatalf("列表里的事件与触发响应不一致: 响应=%+v 列表=%+v", ev, rows[0])
	}

	e.Evidence("SIM-AUTO-006.manual", map[string]any{
		"rule_id": ruleID, "event_id": ev.ID,
		"result": ev.Result, "trigger_source": ev.TriggerSource,
	})
}

// ---------------------------------------------------------------------------
// 通知中心（auto.go 与 alert.go 共用）
// ---------------------------------------------------------------------------

// autoNotificationRow 只取断言需要的字段（models.Notification 的 JSON 形状）。
type autoNotificationRow struct {
	ID       uint   `json:"id"`
	Type     string `json:"type"`
	Title    string `json:"title"`
	Source   string `json:"source"`
	SourceID string `json:"source_id"`
	Read     bool   `json:"read"`
}

// autoNotifications 读通知中心列表（最多 100 条，够覆盖单次仿真运行的量）。
func autoNotifications(e *harness.Env) ([]autoNotificationRow, error) {
	r := e.Admin.Get("/api/v1/notifications?limit=100")
	if r.Status != http.StatusOK {
		return nil, fmt.Errorf("GET /api/v1/notifications 返回 %d: %s", r.Status, r.BodyString())
	}
	var rows []autoNotificationRow
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		return nil, fmt.Errorf("解析通知列表失败: %w（data=%s）", err, autoHead(string(r.Data), 200))
	}
	return rows, nil
}

// autoUnreadCount 读未读数（信封 data.count）。
func autoUnreadCount(e *harness.Env) int64 {
	e.T.Helper()
	return e.Admin.Get("/api/v1/notifications/unread-count").Expect(http.StatusOK).DataInt("count")
}

// autoHead 截断长文本用于失败信息，避免刷屏。
// 刻意不叫 head：同包其它域文件可能各自持有同名小工具，重名会直接编译失败。
func autoHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(截断)"
}
