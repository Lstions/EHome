//go:build simulation

// 场景目录 · SIM-COND 附加条件（设计 §4 SIM-COND-001..005）。
//
// 契约：docs/设计/自动化引擎场景仿真验证.md（§3 能力面、§4 场景清单、§7.3 复核盲区、§9 变异 A5/A8）。
// 设计依据：docs/设计/自动化策略引擎方案.md。
//
// 本域守护的不变量（每条断言都对应其中之一）：
//  1. conditions_json 是数组且全部 AND：只有每一条都满足才触发（evaluator.go:409-420）；
//  2. 任何一条不满足即短路，动作绝不发生 —— 用「只差一条条件」的对照策略证明
//     沉默真的是条件造成的，而不是链路没跑；
//  3. 附加条件引用的传感器本帧没有上报 = 不满足（matchField 未命中），
//     不是「跳过」更不是「满足」；
//  4. 非法 conditions_json 一律 fail-closed：不产生任何动作，并在服务端留下告警
//     （evaluator.go:411-414 的 warn）；
//  5. device_action 在真正下发之前会用最新值复核触发条件与附加条件，失效则落
//     condition_changed 且不下发（planner.go:163-197，变异 A5）。
//
// 复用：autoProvisionDevice / autoCreateRule / autoListEvents / autoSensorSample，
// 以及 trig.go 里的运行时工具（同包跨文件调用；框架 §4.1 只约束声明名前缀）。
package catalog

import (
	"encoding/json"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"ehome/backend/simulation/harness"
)

func init() {
	Register(Scenario{
		ID:     "SIM-COND-001",
		Title:  "多个附加条件同时满足时才执行动作",
		Domain: DomainCOND,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-COND-001（变异 A8）；docs/设计/自动化策略引擎方案.md（conditions_json 全部 AND）",
		Run:    condRun001,
	})
	Register(Scenario{
		ID:     "SIM-COND-002",
		Title:  "任一附加条件不满足时策略就不执行",
		Domain: DomainCOND,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-COND-002（变异 A8）；docs/设计/自动化策略引擎方案.md（条件短路）",
		Run:    condRun002,
	})
	Register(Scenario{
		ID:     "SIM-COND-003",
		Title:  "附加条件引用的传感器这一帧没上报时不会执行",
		Domain: DomainCOND,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-COND-003；docs/设计/自动化策略引擎方案.md（字段缺失即不满足）",
		Run:    condRun003,
	})
	Register(Scenario{
		ID:     "SIM-COND-004",
		Title:  "附加条件配置成非法格式时策略不执行，并留下可排查的告警",
		Domain: DomainCOND,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-COND-004；docs/设计/自动化策略引擎方案.md（非法 JSON fail-closed）",
		Run:    condRun004,
	})
	Register(Scenario{
		ID:     "SIM-COND-005",
		Title:  "触发之后、动作执行之前条件已经失效时不会真的执行动作",
		Domain: DomainCOND,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-COND-005（变异 A5）；docs/设计/自动化策略引擎方案.md（F4 执行前复核）",
		Run:    condRun005,
	})
}

// ---------------------------------------------------------------------------
// 本域夹具
// ---------------------------------------------------------------------------

// condCondition 构造一条附加条件（AutomationCondition 的 JSON 形状）。
func condCondition(sensor, comparator string, threshold float64) map[string]any {
	return map[string]any{
		"sensor_name": sensor,
		"comparator":  comparator,
		"threshold":   threshold,
	}
}

// condConditionsJSON 把条件数组序列化为请求体里的 conditions_json 字符串。
// 序列化失败直接失败：宁可红，也不要静默退化成「没有条件」的规则（那会让
// 「条件不满足所以没触发」的断言全部变成假绿）。
func condConditionsJSON(e *harness.Env, conds []map[string]any) string {
	e.T.Helper()
	if len(conds) == 0 {
		e.Fatalf("拒绝生成空的 conditions_json：那会退化成「无条件」，让断言失去意义")
	}
	body, err := json.Marshal(conds)
	if err != nil {
		e.Fatalf("序列化附加条件失败: %v", err)
	}
	return string(body)
}

// condUseTwoFieldParser 把本场景设备配置的解析器整体替换为两个字段：
//
//	field0 illumiance  uint16 @0  scale=1.0 → lx（原始值即读数）
//	field1 temperature  uint16 @2  scale=0.1 → ℃
//
// 为什么需要双字段：COND 域的语义是「触发看一个物理量、附加条件看另一个」，
// 单字段设备上所有条件都退化成对同一个值的重复判断，无法证明 AND 与短路；
// COND-005 还需要「最新值缓存里存的是另一条记录」这一真实时序事实。
// 走真实 PUT（被引用的配置只允许改 parser），不直连数据库造数。
func condUseTwoFieldParser(e *harness.Env, fx *autoFixture, scenarioID string) {
	e.T.Helper()
	if fx.configID == 0 {
		e.Fatalf("夹具未返回设备配置 ID，无法替换解析器")
	}
	path := "/api/v1/device-configs/" + strconv.FormatUint(uint64(fx.configID), 10)
	e.Admin.Put(path, map[string]any{
		"name": e.NS(scenarioID, "dual-config"),
		"parser": map[string]any{
			"data_format": "binary",
			"fields": []map[string]any{
				{"name": "illuminance", "type": "uint16", "scale": 1.0, "offset": 0, "length": 2, "unit": "lx"},
				{"name": "temperature", "type": "uint16", "scale": 0.1, "offset": 2, "length": 2, "unit": "℃"},
			},
		},
	}).Expect(http.StatusOK)

	cfg := e.Admin.Get(path).Expect(http.StatusOK)
	var parsed struct {
		Parser struct {
			Fields []struct {
				Name string `json:"name"`
			} `json:"fields"`
		} `json:"parser"`
	}
	cfg.Decode(&parsed)
	if len(parsed.Parser.Fields) != 2 ||
		parsed.Parser.Fields[0].Name != "illuminance" || parsed.Parser.Fields[1].Name != "temperature" {
		e.Fatalf("设备配置 %d 未切换到双字段解析器：%s", fx.configID, autoHead(string(cfg.Data), 300))
	}
}

// condReport 上报一帧双字段数据（illuminance 与 temperature 各 2 字节大端）。
// 不能复用 autoFixture.report：它只会发 2 字节，双字段解析器会因为越界读失败而
// 整帧解析失败（parser.Parse 一个字段错即整帧错）。
func condReport(fx *autoFixture, lux, tempRaw uint16) error {
	payload := []byte{byte(lux >> 8), byte(lux), byte(tempRaw >> 8), byte(tempRaw)}
	return fx.device.DataReport(uint32(fx.channelID), uint64(time.Now().UnixMilli()), payload)
}

// condTryCreateRule 创建策略并返回原始响应（不做状态断言）：
// COND-004 要断言的恰恰是「非法配置会不会被拒收」，因此不能预设 200。
// 只有真的建出来了才登记自清理。
func condTryCreateRule(e *harness.Env, body map[string]any) (*harness.Response, int64) {
	e.T.Helper()
	created := e.Admin.Post("/api/v1/automation-rules", body)
	id := int64(0)
	if created.Status >= 200 && created.Status < 300 {
		id = created.DataInt("id")
		if id == 0 {
			e.Fatalf("创建策略成功但未返回 id: %s", created.BodyString())
		}
		e.T.Cleanup(func() {
			autoCleanup(e.T, "自动化策略",
				e.Admin.Delete("/api/v1/automation-rules/"+strconv.FormatInt(id, 10)), http.StatusOK)
		})
	}
	return created, id
}

// condRuleByName 在策略列表里按名字找一条（用于确认「被拒收的规则没有入库」）。
func condRuleByName(e *harness.Env, name string) *autoRuleRow {
	e.T.Helper()
	rows, err := autoListRules(e, "")
	if err != nil {
		e.Fatalf("%v", err)
	}
	for i := range rows {
		if rows[i].Name == name {
			return &rows[i]
		}
	}
	return nil
}

// ---------------------------------------------------------------------------
// SIM-COND-001 多个附加条件同时满足才触发（全部 AND）
// ---------------------------------------------------------------------------

func condRun001(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-COND-001", "and", "sim_cond_001_sensor")
	condUseTwoFieldParser(e, fx, "SIM-COND-001")

	// 触发：光照 > 100 lx；附加条件（全部 AND）：温度 > 20℃ 且 光照 < 500 lx。
	// 两个条件一个看温度、一个看光照，覆盖「跨传感器 AND」这一真实用法。
	conds := []map[string]any{
		condCondition("temperature", "gt", 20.0),
		condCondition("illuminance", "lt", 500.0),
	}
	ruleID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-COND-001", "and"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "illuminance",
		"trigger_comparator":     "gt",
		"trigger_threshold":      100.0,
		"trigger_duration_sec":   0,
		"conditions_json":        condConditionsJSON(e, conds),
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           300,
	})

	// 创建响应必须原样回显条件数组（用户填的条件不能被静默丢掉，
	// 否则「无条件」的规则会在任何越限时都触发）。
	detail := e.Admin.Get("/api/v1/automation-rules/" + strconv.FormatInt(ruleID, 10)).Expect(http.StatusOK)
	var created struct {
		ConditionsJSON string `json:"conditions_json"`
	}
	detail.Decode(&created)
	if created.ConditionsJSON != condConditionsJSON(e, conds) {
		e.Fatalf("策略详情里的 conditions_json=%q，期望 %q", created.ConditionsJSON, condConditionsJSON(e, conds))
	}

	// 真实数据流：光照 250 lx、温度 23.5℃ —— 两个附加条件都满足。
	if err := condReport(fx, 250, 235); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	trigWaitSensorValue(e, fx.edgeDeviceID, "temperature", 23.5, 25*time.Second)
	trigWaitSensorValue(e, fx.edgeDeviceID, "illuminance", 250, 25*time.Second)

	rows := trigWaitResult(e, ruleID, "notification", 1, 25*time.Second)
	trigAssertValues(e, "全部附加条件满足时的触发值", rows, "notification", 250)

	e.Evidence("SIM-COND-001.all_and", map[string]any{
		"rule_id": ruleID, "frame": map[string]any{"illuminance": 250, "temperature": 23.5},
		"conditions": conds, "fired": trigValuesOf(e, rows, "notification"),
	})
}

// ---------------------------------------------------------------------------
// SIM-COND-002 任一附加条件不满足就不触发
// ---------------------------------------------------------------------------

func condRun002(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-COND-002", "short", "sim_cond_002_sensor")
	condUseTwoFieldParser(e, fx, "SIM-COND-002")

	// 被观察的策略与对照策略**只在最后一条附加条件上不同**：
	//   被观察：光照 > 100 且 温度 > 20 且 光照 < 100   ← 第三条不可能与触发同时成立
	//   对照：  光照 > 100 且 温度 > 20                 ← 同一帧必须触发
	// 差异只有一条条件，因此「沉默」不可能由链路、设备、阈值或其它因素解释。
	guardedConds := []map[string]any{
		condCondition("temperature", "gt", 20.0),
		condCondition("illuminance", "lt", 100.0),
	}
	guardedID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-COND-002", "guarded"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "illuminance",
		"trigger_comparator":     "gt",
		"trigger_threshold":      100.0,
		"trigger_duration_sec":   0,
		"conditions_json":        condConditionsJSON(e, guardedConds),
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           300,
	})
	controlConds := []map[string]any{
		condCondition("temperature", "gt", 20.0),
	}
	controlID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-COND-002", "control"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "illuminance",
		"trigger_comparator":     "gt",
		"trigger_threshold":      100.0,
		"trigger_duration_sec":   0,
		"conditions_json":        condConditionsJSON(e, controlConds),
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           300,
	})

	if err := condReport(fx, 250, 235); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	trigWaitSensorValue(e, fx.edgeDeviceID, "temperature", 23.5, 25*time.Second)

	// 对照策略必须先触发：同一帧、同一触发条件、少一条不可能成立的条件。
	controlRows := trigWaitResult(e, controlID, "notification", 1, 25*time.Second)
	trigAssertValues(e, "对照策略（少了那条不满足的条件）", controlRows, "notification", 250)

	// 不变式：有一条条件不满足，这条策略就一次都不能触发。
	trigAssertNoResult(e, "存在不满足条件的策略", guardedID, "notification", 2*time.Second)

	e.Evidence("SIM-COND-002.short_circuit", map[string]any{
		"guarded_rule_id": guardedID,
		"guarded_conds":   guardedConds,
		"control_rule_id": controlID,
		"control_conds":   controlConds,
		"frame":           map[string]any{"illuminance": 250, "temperature": 23.5},
		"guarded_events":  0,
		"control_fired":   trigValuesOf(e, controlRows, "notification"),
	})
}

// ---------------------------------------------------------------------------
// SIM-COND-003 附加条件引用的传感器本帧没有上报时不触发
// ---------------------------------------------------------------------------

func condRun003(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-COND-003", "missing", "sim_cond_003_sensor")
	// 本设备的解析器只产出 illuminance：附加条件引用的 temperature 在每一帧里都不存在。
	trigUseSingleFieldParser(e, fx, "SIM-COND-003")

	// 被观察：附加条件引用一个本帧根本不存在的传感器。
	guardedID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-COND-003", "missing"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "illuminance",
		"trigger_comparator":     "gt",
		"trigger_threshold":      100.0,
		"trigger_duration_sec":   0,
		"conditions_json": condConditionsJSON(e, []map[string]any{
			condCondition("temperature", "gt", 20.0),
		}),
		"action_type":  "notification",
		"action_level": "warning",
		"cooldown_sec": 300,
	})
	// 对照：完全相同的触发条件，附加条件换成**本帧真实存在**的字段。
	controlID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-COND-003", "control"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "illuminance",
		"trigger_comparator":     "gt",
		"trigger_threshold":      100.0,
		"trigger_duration_sec":   0,
		"conditions_json": condConditionsJSON(e, []map[string]any{
			condCondition("illuminance", "lt", 500.0),
		}),
		"action_type":  "notification",
		"action_level": "warning",
		"cooldown_sec": 300,
	})

	if err := fx.report(250); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	trigWaitSensorValue(e, fx.edgeDeviceID, "illuminance", 250, 25*time.Second)
	controlRows := trigWaitResult(e, controlID, "notification", 1, 25*time.Second)
	trigAssertValues(e, "对照策略（附加条件引用存在的字段）", controlRows, "notification", 250)

	// 直接证据：这一帧解析出来的物理量里没有 temperature。
	trigAssertSensorAbsent(e, fx.edgeDeviceID, "temperature")
	// 不变式：缺失 = 不满足，绝不触发。
	trigAssertNoResult(e, "附加条件引用缺失字段的策略", guardedID, "notification", 2*time.Second)

	e.Evidence("SIM-COND-003.missing_condition_field", map[string]any{
		"guarded_rule_id": guardedID, "guarded_condition_sensor": "temperature（本帧不存在）",
		"control_rule_id": controlID, "frame_illuminance": 250,
		"guarded_events": 0, "control_fired": trigValuesOf(e, controlRows, "notification"),
	})
}

// ---------------------------------------------------------------------------
// SIM-COND-004 附加条件配置成非法 JSON 时规则不触发且有告警（fail-closed）
// ---------------------------------------------------------------------------

func condRun004(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-COND-004", "invalid", "sim_cond_004_sensor")

	// 少一个右花括号 —— 不是合法 JSON 数组（ParseConditions 会返回错误）。
	const badJSON = `[{"sensor_name":"temperature","comparator":"gt","threshold":20}`
	ruleName := e.NS("SIM-COND-004", "invalid")

	body := map[string]any{
		"name":                   ruleName,
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0,
		"conditions_json":        badJSON,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           300,
	}
	created, ruleID := condTryCreateRule(e, body)

	// 对照策略：同样的触发条件、没有附加条件 —— 它触发即证明这一帧被求值过。
	controlID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-COND-004", "control"),
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

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	trigWaitSensorValue(e, fx.edgeDeviceID, "temperature", 23.5, 25*time.Second)
	controlRows := trigWaitResult(e, controlID, "notification", 1, 25*time.Second)
	trigAssertValues(e, "对照策略（同帧同触发条件）", controlRows, "notification", 23.5)

	switch {
	case created.Status >= 400:
		// 分支 A：产品在创建/更新时就拒收非法 conditions_json（更强的 fail-closed）。
		// 那么「规则不触发」的前提根本不存在 —— 必须断言它没有入库。
		if created.Status >= 500 {
			e.Fatalf("非法 conditions_json 应被 4xx 拒收，实际 %d：%s", created.Status, created.BodyString())
		}
		if found := condRuleByName(e, ruleName); found != nil {
			e.Fatalf("非法 conditions_json 被 %d 拒收，但策略 %d 仍然入库了", created.Status, found.ID)
		}
		e.Evidence("SIM-COND-004.rejected_at_create", map[string]any{
			"create_status":  created.Status,
			"error_code":     created.ErrorCode,
			"message":        created.Message,
			"rule_persisted": false,
		})
	default:
		// 分支 B（当前实现的真实行为）：创建不做 JSON 结构校验，非法配置被原样保存，
		// fail-closed 落在求值侧 —— 解析失败即不触发，并留下可排查的告警。
		if ruleID == 0 {
			e.Fatalf("创建既未成功也未返回 id：status=%d body=%s", created.Status, created.BodyString())
		}
		trigAssertNoResult(e, "非法 conditions_json 的策略", ruleID, "notification", 2*time.Second)

		// 「且有告警」：服务端必须留下能定位到这条规则的 warn 日志。
		// 日志的字段是 zap console 编码器的 JSON（实测渲染为 {"rule_id": 6, ...}），
		// 因此按 "rule_id": <数字> 匹配，并要求数字边界（避免 rule_id=6 命中 60）。
		//
		// 实测原始渲染（server.log）：
		//   WARN logger/logger.go:72 automation: invalid conditions_json	{"rule_id": 32, "error": "..."}
		// 三个要点：
		//  1. 字段之间是 **TAB**，不是空格 → 用 \s* 容忍任意空白；
		//  2. rule_id 是**自增**的，每次运行都不同 → 必须用本场景真实的 ruleID 拼正则，不能写死；
		//  3. 数字边界用 \b，避免 rule_id=6 命中 rule_id=60。
		//
		// 历史缺陷（本次修复）：这里曾写成 `:s*`（反斜杠丢失，只匹配字面量 s），
		// 且边界字节被误写成 0x08（退格）→ 断言恒不匹配，而它守护的
		// 「非法配置必须留下可排查告警」正是 fail-closed 语义的可观测性保证。
		pattern := regexp.MustCompile(`invalid conditions_json\s*\{"rule_id":\s*` +
			strconv.FormatInt(ruleID, 10) + `\b`)
		matched, err := e.Logs.WaitForMatch(pattern, 10*time.Second)
		if err != nil {
			e.Fatalf("非法 conditions_json 未在服务端留下可排查的告警（%v）", err)
		}
		e.Evidence("SIM-COND-004.fail_closed_at_eval", map[string]any{
			"create_status": created.Status,
			"rule_id":       ruleID,
			"log_line":      matched[0],
			"events":        0,
		})
	}

	e.Evidence("SIM-COND-004.control", map[string]any{
		"control_rule_id": controlID,
		"control_fired":   trigValuesOf(e, controlRows, "notification"),
	})
}

// ---------------------------------------------------------------------------
// SIM-COND-005 触发后、动作执行前条件已失效时不会执行动作（落 condition_changed）
// ---------------------------------------------------------------------------

func condRun005(e *harness.Env) {
	// 设备类型必须是带动作目录的驱动型号：只有 device_action 才会走 F4 执行前复核
	// （notification 是纯通知，planner 复核不到它）。
	fx := autoProvisionDevice(e, "SIM-COND-005", "recheck", "sn3001_rain")
	condUseTwoFieldParser(e, fx, "SIM-COND-005")

	// 触发看光照（> 200 lx），附加条件看温度（> 20℃）。两个字段都会在解析后被写入
	// 最新值缓存；缓存键是「设备」，值是该帧**最后一条**记录（temperature）。
	//
	// 于是执行前复核读到的是一条真实的、属于这台设备的最新记录，但它是温度而不是
	// 触发用的光照 —— 用触发阈值复核这条记录必然不成立，引擎因此必须落
	// condition_changed 且不下发动作。这正是 F4 想守住的语义：复核用的是「最新值」，
	// 一旦它与触发时的判定不一致，就必须停手（这一实现细节已作为盲区记录在
	// 设计 §7.3，本场景断言的是它对外可见的结果：不下发 + 落 condition_changed）。
	conds := []map[string]any{condCondition("temperature", "gt", 20.0)}
	ruleID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-COND-005", "recheck"),
		"enabled":                true,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "illuminance",
		"trigger_comparator":     "gt",
		"trigger_threshold":      200.0,
		"trigger_duration_sec":   0,
		"conditions_json":        condConditionsJSON(e, conds),
		"action_type":            "device_action",
		"action_device_id":       fx.edgeDeviceID,
		"action_id":              "reset_rainfall",
		"action_params_json":     "{}",
		"require_confirmed":      false,
		"cooldown_sec":           300,
	})

	// 一帧同时满足触发（250 > 200）与附加条件（23.5 > 20）。
	if err := condReport(fx, 250, 235); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	trigWaitSensorValue(e, fx.edgeDeviceID, "illuminance", 250, 25*time.Second)
	trigWaitSensorValue(e, fx.edgeDeviceID, "temperature", 23.5, 25*time.Second)

	rows := trigWaitResult(e, ruleID, "condition_changed", 1, 25*time.Second)

	// 不变式 1：复核不通过 → 事件落 condition_changed，且必须写明原因。
	row := rows[0]
	if row.Result != "condition_changed" {
		e.Fatalf("执行前复核失效时应落 condition_changed，实际 %q：%+v", row.Result, row)
	}
	if row.Detail == "" {
		e.Fatalf("condition_changed 事件必须写明原因（detail 为空）：%+v", row)
	}
	// 不变式 2：绝不能真的下发动作 —— 没有指令号，也没有 executed 事件。
	if row.CommandID != "" {
		e.Fatalf("复核失效却仍然下发了指令 %q —— F4 执行前复核没有生效", row.CommandID)
	}
	if trigCountResult(rows, "executed") != 0 {
		e.Fatalf("复核失效却出现了 executed 事件：%+v", rows)
	}
	// 触发值仍是那一帧的读数（用户能看到「当时是多少」）。
	if row.TriggerValue == nil || *row.TriggerValue != 250 {
		e.Fatalf("condition_changed 事件的触发值应为 250，实际 %v", row.TriggerValue)
	}

	e.Evidence("SIM-COND-005.recheck", map[string]any{
		"rule_id": ruleID, "event_id": row.ID, "result": row.Result, "detail": row.Detail,
		"command_id": row.CommandID, "trigger_value": *row.TriggerValue,
		"executed_events": trigCountResult(rows, "executed"),
	})
}
