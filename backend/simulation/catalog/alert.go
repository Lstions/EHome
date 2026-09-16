//go:build simulation

// 场景目录 · SIM-ALERT 阈值告警与通知（设计 §9 SIM-ALERT-001..004）。
//
// 契约：docs/设计/场景仿真验证框架.md（§5 harness API、§5.4 场景模型、§7 红线、§9 场景清单）。
// 设计依据：docs/设计/自动化策略引擎方案.md（阈值告警引擎与自动化策略共用挂接点）、
// docs/设计/通知中心.md。
//
// 本域守护的不变量：
//  1. 告警规则是用户可见的持久化事实：创建（201）后能在列表/过滤查询里读到，
//     且更新/启停是即时生效的（不是只写库不改求值器缓存）；
//  2. 只有真实越过阈值的数据才会产生 firing 事件 —— 数据必须走真实
//     MQTT 上行 → SensorParserConsumer 解析后回调，HTTP 侧没有造数据入口；
//  3. 数据回到阈值内必须解除告警：同一条事件被就地关闭（state=resolved +
//     resolved_at），而不是留下一个永远 firing 的悬空告警；
//  4. 每条告警都必须落到通知中心（source=alert_rule + source_id=规则 ID），
//     否则用户界面看不到它。
package catalog

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehome/backend/simulation/harness"
)

func init() {
	Register(Scenario{
		ID:     "SIM-ALERT-001",
		Title:  "管理员建好一条阈值告警规则后，能在告警规则列表里查到它并随时改阈值或停用",
		Domain: DomainALERT,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-ALERT-001；docs/设计/自动化策略引擎方案.md（阈值告警引擎）",
		Run:    alertRun001,
	})
	Register(Scenario{
		ID:     "SIM-ALERT-002",
		Title:  "室内温度越过设定上限后，告警中心出现一条正在告警的记录",
		Domain: DomainALERT,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-ALERT-002；docs/设计/自动化策略引擎方案.md（阈值告警引擎）",
		Run:    alertRun002,
	})
	Register(Scenario{
		ID:     "SIM-ALERT-003",
		Title:  "温度回落到安全范围后，之前那条告警自动变成已恢复",
		Domain: DomainALERT,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-ALERT-003；docs/设计/自动化策略引擎方案.md（阈值告警引擎）",
		Run:    alertRun003,
	})
	Register(Scenario{
		ID:     "SIM-ALERT-004",
		Title:  "告警发生后通知中心收到一条未读告警通知",
		Domain: DomainALERT,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-ALERT-004；docs/设计/通知中心.md",
		Run:    alertRun004,
	})
}

// ---------------------------------------------------------------------------
// 领域数据类型与工具
// ---------------------------------------------------------------------------

// alertRuleRow 只取断言需要的字段（models.AlertRule 的 JSON 形状）。
type alertRuleRow struct {
	ID          uint    `json:"id"`
	TargetType  string  `json:"target_type"`
	TargetID    uint    `json:"target_id"`
	SensorName  string  `json:"sensor_name"`
	Comparator  string  `json:"comparator"`
	Threshold   float64 `json:"threshold"`
	DurationSec int     `json:"duration_sec"`
	SilenceSec  int     `json:"silence_sec"`
	Level       string  `json:"level"`
	Enabled     bool    `json:"enabled"`
	Name        string  `json:"name"`
}

// alertEventRow 只取断言需要的字段（models.AlertEvent 的 JSON 形状）。
// FiredAt/ResolvedAt 用 *string：nil ↔ JSON null，正是“有没有真的触发/恢复”的判据。
type alertEventRow struct {
	ID         uint    `json:"id"`
	RuleID     uint    `json:"rule_id"`
	State      string  `json:"state"`
	Value      float64 `json:"value"`
	FiredAt    *string `json:"fired_at"`
	ResolvedAt *string `json:"resolved_at"`
	NotifiedAt *string `json:"notified_at"`
}

// alertListRules 读告警规则列表（可带 level/target_type/target_id 之类的过滤串）。
//
// 端点形状：**裸数组**（已核对 handler_alert.go:80-101 的 listAlertRules
// 走 Success(c, items)，没有 page/page_size/Count）。注意这与同文件的
// /alert-events 不同 —— 同一 handler 文件里两个端点形状不同，正是本缺陷
// 容易扩散的地方，所以每个调用点都必须写明依据。
func alertListRules(e *harness.Env, query string) ([]alertRuleRow, error) {
	path := "/api/v1/alert-rules" + query
	return simBareListItems[alertRuleRow](e.Admin.Get(path), path)
}

// alertListEvents 读告警事件（可带 rule_id/state 过滤串）。
//
// 端点形状：**分页信封** {items,total,page,page_size}
// （handler_alert.go:294-352 的 listAlertEvents，提交 ea9ce296 改）。
//
// 与 autoListEvents 同理读**全部页**：调用方断言的是"该规则下的全部告警"
// （如"恢复后 firing 集合里不再有它"），只读第一页会漏掉窗口外的行。
func alertListEvents(e *harness.Env, query string) ([]alertEventRow, error) {
	return simPageAll[alertEventRow](e, "/api/v1/alert-events", query)
}

// alertCreateRule 创建一条告警规则并把自清理挂到当前场景上（§5.6 场景自清理）。
//
// 为什么断言 201：POST /api/v1/alert-rules 走 api.SuccessWithCode(201)，与
// automation-rules 的 200 不同 —— 这是产品真实行为，不是笔误。
func alertCreateRule(e *harness.Env, body map[string]any) int64 {
	t := e.T
	t.Helper()
	created := e.Admin.Post("/api/v1/alert-rules", body).Expect(http.StatusCreated)
	id := created.DataInt("id")
	if id == 0 {
		e.Fatalf("创建告警规则未返回 id: %s", created.BodyString())
	}
	t.Cleanup(func() {
		autoCleanup(t, "告警规则",
			e.Admin.Delete("/api/v1/alert-rules/"+strconv.FormatInt(id, 10)), http.StatusOK)
	})
	return id
}

// alertFindRule 在告警规则列表里按 ID 找一条（找不到返回 nil）。
func alertFindRule(e *harness.Env, query string, id int64) (*alertRuleRow, int, error) {
	rows, err := alertListRules(e, query)
	if err != nil {
		return nil, 0, err
	}
	for i := range rows {
		if rows[i].ID == uint(id) {
			return &rows[i], len(rows), nil
		}
	}
	return nil, len(rows), nil
}

// ---------------------------------------------------------------------------
// SIM-ALERT-001 创建阈值告警规则并可在列表查询
// ---------------------------------------------------------------------------

func alertRun001(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-ALERT-001", "rule", "sim_alert_001_sensor")
	name := e.NS("SIM-ALERT-001", "rule")

	// silence_sec 显式传 0：产品把它归一为默认 300（defaultAlertSilenceSec），
	// 本场景断言归一后的真实取值，避免后续场景建立在错误的默认值假设上。
	ruleID := alertCreateRule(e, map[string]any{
		"target_type":  "edge_device",
		"target_id":    fx.edgeDeviceID,
		"sensor_name":  "temperature",
		"comparator":   "gt",
		"threshold":    20.0,
		"duration_sec": 0,
		"silence_sec":  0,
		"level":        "critical",
		"enabled":      true,
		"name":         name,
	})

	// 不变式 1：创建响应必须原样回显用户填的规则（不是只回一个 id）。
	found, total, err := alertFindRule(e, "", ruleID)
	if err != nil {
		e.Fatalf("%v", err)
	}
	if found == nil {
		e.Fatalf("新建的告警规则 %d 未出现在 GET /api/v1/alert-rules 的 %d 条结果中", ruleID, total)
	}
	if found.TargetType != "edge_device" || found.TargetID != fx.edgeDeviceID ||
		found.SensorName != "temperature" || found.Comparator != "gt" ||
		found.Level != "critical" || found.Name != name || !found.Enabled || found.DurationSec != 0 {
		e.Fatalf("告警规则回显与请求不一致: %+v", *found)
	}
	autoEventuallyFloat(e.T, "threshold", found.Threshold, 20.0)
	if found.SilenceSec != 300 {
		e.Fatalf("silence_sec 传 0 应被归一为默认 300，实际 %d", found.SilenceSec)
	}

	// 不变式 2：过滤查询必须能查到它（level / target_type+target_id 两种口径）。
	for _, query := range []string{
		"?level=critical",
		"?target_type=edge_device&target_id=" + strconv.FormatUint(uint64(fx.edgeDeviceID), 10),
	} {
		hit, hits, err := alertFindRule(e, query, ruleID)
		if err != nil {
			e.Fatalf("%v", err)
		}
		if hit == nil {
			e.Fatalf("告警规则 %d 未出现在 GET /api/v1/alert-rules%s 的 %d 条结果中", ruleID, query, hits)
		}
	}

	// 不变式 3：改阈值与停用都必须是用户可见的即时变化（不是只写库）。
	updated := e.Admin.Put("/api/v1/alert-rules/"+strconv.FormatInt(ruleID, 10), map[string]any{
		"threshold":   25.0,
		"silence_sec": 30,
	}).Expect(http.StatusOK)
	var afterUpdate alertRuleRow
	updated.Decode(&afterUpdate)
	autoEventuallyFloat(e.T, "更新后的 threshold", afterUpdate.Threshold, 25.0)
	if afterUpdate.SilenceSec != 30 {
		e.Fatalf("更新后的 silence_sec=%d，期望 30", afterUpdate.SilenceSec)
	}

	disabled := e.Admin.Patch("/api/v1/alert-rules/"+strconv.FormatInt(ruleID, 10)+"/enabled",
		map[string]any{"enabled": false}).Expect(http.StatusOK)
	var afterPatch alertRuleRow
	disabled.Decode(&afterPatch)
	if afterPatch.Enabled {
		e.Fatalf("停用后响应里 enabled 仍为 true: %+v", afterPatch)
	}
	hit, hits, err := alertFindRule(e, "", ruleID)
	if err != nil {
		e.Fatalf("%v", err)
	}
	if hit == nil {
		e.Fatalf("停用后告警规则 %d 从列表消失了（共 %d 条）", ruleID, hits)
	}
	if hit.Enabled {
		e.Fatalf("停用后列表里的告警规则 %d 仍为 enabled=true", ruleID)
	}

	e.Evidence("SIM-ALERT-001.rule", map[string]any{
		"id": ruleID, "name": name, "threshold_after_update": 25.0, "silence_sec_after_update": 30,
		"target_type": "edge_device", "target_id": fx.edgeDeviceID,
	})
}

// alertWaitFiring 等到指定规则出现一条 firing 事件，返回该事件。
// 告警求值发生在 SensorParserConsumer 的解析后回调里，时序不可预知，
// 因此一律用有界轮询收敛，不用 sleep 同步（设计 §3 原则 3）。
func alertWaitFiring(e *harness.Env, ruleID int64) alertEventRow {
	e.T.Helper()
	var fired alertEventRow
	e.Eventually(25*time.Second, func() error {
		rows, err := alertListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10)+"&state=firing")
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("规则 %d 尚未产生 firing 事件", ruleID)
		}
		fired = rows[0]
		return nil
	})
	return fired
}

// ---------------------------------------------------------------------------
// SIM-ALERT-002 数据越过阈值后产生告警事件
// ---------------------------------------------------------------------------

func alertRun002(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-ALERT-002", "fire", "sim_alert_002_sensor")
	name := e.NS("SIM-ALERT-002", "fire")

	// duration_sec=0（本点满足即告警）、silence_sec=0（归一为 300，本场景只报一帧，
	// 不影响时序）—— 两个时间参数都取最简值，避免场景依赖具体静默窗口。
	ruleID := alertCreateRule(e, map[string]any{
		"target_type":  "edge_device",
		"target_id":    fx.edgeDeviceID,
		"sensor_name":  "temperature",
		"comparator":   "gt",
		"threshold":    20.0,
		"duration_sec": 0,
		"silence_sec":  0,
		"level":        "critical",
		"name":         name,
	})

	// 真实数据流：235 * 0.1 = 23.5 ℃ > 20.0 ℃。
	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}

	fired := alertWaitFiring(e, ruleID)

	// 不变式：事件必须能被归因到规则 + 触发时值，并且真的记录了触发时刻。
	if fired.RuleID != uint(ruleID) {
		e.Fatalf("告警事件 rule_id=%d，期望 %d", fired.RuleID, ruleID)
	}
	if fired.State != "firing" {
		e.Fatalf("告警事件 state=%q，期望 firing", fired.State)
	}
	if fired.FiredAt == nil || *fired.FiredAt == "" {
		e.Fatalf("firing 事件必须带 fired_at（否则界面无法展示告警时刻）: %+v", fired)
	}
	if fired.ResolvedAt != nil && *fired.ResolvedAt != "" {
		e.Fatalf("尚未恢复的事件不该有 resolved_at: %+v", fired)
	}
	autoEventuallyFloat(e.T, "告警事件 value", fired.Value, 23.5)

	// 不变式：不带 state 过滤的列表口径读到的是同一条事实。
	rows, err := alertListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10))
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(rows) == 0 {
		e.Fatalf("告警事件 %d 在无过滤列表里查不到", fired.ID)
	}

	e.Evidence("SIM-ALERT-002.firing", map[string]any{
		"rule_id": ruleID, "event_id": fired.ID, "value": fired.Value, "fired_at": *fired.FiredAt,
	})
}

// ---------------------------------------------------------------------------
// SIM-ALERT-003 数据回到阈值内后告警解除
// ---------------------------------------------------------------------------

func alertRun003(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-ALERT-003", "resolve", "sim_alert_003_sensor")
	name := e.NS("SIM-ALERT-003", "resolve")

	ruleID := alertCreateRule(e, map[string]any{
		"target_type":  "edge_device",
		"target_id":    fx.edgeDeviceID,
		"sensor_name":  "temperature",
		"comparator":   "gt",
		"threshold":    20.0,
		"duration_sec": 0,
		"silence_sec":  0,
		"level":        "warning",
		"name":         name,
	})

	// 第一步：越限 23.5 ℃ → firing。
	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报越限数据失败: %v", err)
	}
	fired := alertWaitFiring(e, ruleID)
	autoEventuallyFloat(e.T, "越限时的事件值", fired.Value, 23.5)

	// 第二步：回落到 15.0 ℃ → 必须解除。
	if err := fx.report(150); err != nil {
		e.Fatalf("节点上报恢复数据失败: %v", err)
	}

	// 不变式：同一行事件被就地关闭（state=resolved + resolved_at + 恢复时值），
	// 而不是留下一条永远 firing 的悬空告警。
	var resolved alertEventRow
	e.Eventually(25*time.Second, func() error {
		rows, err := alertListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10))
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.ID != fired.ID {
				continue
			}
			if row.State != "resolved" {
				return fmt.Errorf("事件 %d 仍为 state=%q", row.ID, row.State)
			}
			if row.ResolvedAt == nil || *row.ResolvedAt == "" {
				return fmt.Errorf("已恢复的事件 %d 缺少 resolved_at", row.ID)
			}
			resolved = row
			return nil
		}
		return fmt.Errorf("告警事件 %d 从列表消失了（共 %d 条）", fired.ID, len(rows))
	})
	autoEventuallyFloat(e.T, "恢复时的事件值", resolved.Value, 15.0)

	// 不变式：firing 态必须清空 —— 恢复后不能同时存在一条 firing 事件。
	stillFiring, err := alertListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10)+"&state=firing")
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(stillFiring) != 0 {
		e.Fatalf("恢复后仍有 %d 条 firing 事件: %+v", len(stillFiring), stillFiring[0])
	}
	recovered, err := alertListEvents(e, "?rule_id="+strconv.FormatInt(ruleID, 10)+"&state=resolved")
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(recovered) == 0 || recovered[0].ID != fired.ID {
		e.Fatalf("state=resolved 过滤查不到刚恢复的事件 %d: %+v", fired.ID, recovered)
	}

	e.Evidence("SIM-ALERT-003.resolved", map[string]any{
		"rule_id": ruleID, "event_id": resolved.ID,
		"fired_at": *resolved.FiredAt, "resolved_at": *resolved.ResolvedAt,
		"value_at_resolve": resolved.Value,
	})
}

// ---------------------------------------------------------------------------
// SIM-ALERT-004 告警产生后写入通知中心并可查询
// ---------------------------------------------------------------------------

func alertRun004(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-ALERT-004", "notify", "sim_alert_004_sensor")
	name := e.NS("SIM-ALERT-004", "notify")

	before := autoUnreadCount(e)

	ruleID := alertCreateRule(e, map[string]any{
		"target_type":  "edge_device",
		"target_id":    fx.edgeDeviceID,
		"sensor_name":  "temperature",
		"comparator":   "gt",
		"threshold":    20.0,
		"duration_sec": 0,
		"silence_sec":  0,
		"level":        "critical",
		"name":         name,
	})

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}
	alertWaitFiring(e, ruleID)

	// 不变式 1：告警必须落到通知中心，且能被回链到规则（source + source_id）。
	// level=critical 映射为通知 type=error（models.NotificationType）。
	var landed autoNotificationRow
	e.Eventually(20*time.Second, func() error {
		rows, err := autoNotifications(e)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Source == "alert_rule" && row.SourceID == strconv.FormatInt(ruleID, 10) {
				landed = row
				return nil
			}
		}
		return fmt.Errorf("通知中心尚无规则 %d 的告警通知（共 %d 条）", ruleID, len(rows))
	})
	if landed.Type != "error" {
		e.Fatalf("critical 告警的通知 type=%q，期望 error", landed.Type)
	}
	if !strings.Contains(landed.Title, name) {
		e.Fatalf("告警通知标题 %q 未包含规则名 %q（用户无法辨认是哪条规则）", landed.Title, name)
	}
	if landed.Read {
		e.Fatalf("新告警通知 %d 不应是已读状态", landed.ID)
	}

	// 不变式 2：未读数必须增加（否则界面上看不到"有新告警"）。
	after := autoUnreadCount(e)
	if after <= before {
		e.Fatalf("告警后未读数未增加：告警前 %d，告警后 %d", before, after)
	}

	e.Evidence("SIM-ALERT-004.notification", map[string]any{
		"rule_id": ruleID, "notification_id": landed.ID, "type": landed.Type,
		"unread_before": before, "unread_after": after,
	})
}
