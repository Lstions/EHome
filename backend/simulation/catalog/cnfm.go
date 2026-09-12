//go:build simulation

// 场景目录 · SIM-CNFM 确认制闭环（设计/自动化引擎场景仿真验证.md §4 SIM-CNFM-001..006）。
//
// 契约：docs/设计/自动化引擎场景仿真验证.md（§3 能力面 / §4 场景清单 / §6 命名 / §10 门禁）；
// 基础设施：docs/设计/场景仿真验证框架.md（§5 harness API / §5.6 隔离命名）。
// 设计依据：docs/设计/自动化确认制闭环实现方案.md。
//
// 本域守护的不变量（每条断言都有源码出处）：
//  1. require_confirmed=true 只落 pending_confirm + 待办通知，**绝不下发指令** ——
//     planner.go:90-96 的确认分流直接 return，根本走不到 executeDeviceAction；
//  2. 待办通知要能让用户知道"确认哪一条"：source=automation_rule + source_id=规则 ID 可回链，
//     正文带 event_id（planner.go:289-314 notifyConfirmation）；
//  3. 人工确认走 POST /automation-events/:id/confirm，即铸即销 confirmation token，
//     成功后原事件转 executed 并回填 command_id（planner.go:585-620）；
//  4. 重复确认被"状态判定 + 条件 UPDATE"挡下，不会二次下发（planner.go:562-564、612-618）。
//
// **未实现说明（不在本文件注册，见任务报告）**：
//   - SIM-CNFM-005（未通过近认证的操作者无法确认）：近认证门读的是**用户级**
//     users.last_login_at（commandexec/confirmation.go:143），而不是会话或令牌属性；
//     本产品是单主体（system_admin）且没有任何用户创建 API（全仓无 /users 路由），
//     而 POST /auth/login 与 /account/reauthenticate 都会把 last_login_at 刷成现在
//     （auth/login.go:38-42）。因此"未通过近认证的操作者"在仿真里只能靠
//     （a）真实等待 10 分钟，或（b）直连数据库把 last_login_at 改旧 ——
//     (a) 与框架"禁止用 sleep 做断言同步"冲突，(b) 违反框架 §3 原则 2（黑盒断言优先）。
//     这里不写假断言：留待受控测试通道裁决。
//   - SIM-CNFM-006（24h 未确认过期）：过期判定依据 automation_events.triggered_at
//     （planner.go:650-665 sweepExpiredPending，清扫周期 5min；confirm 侧的双保险在
//     planner.go:566）。triggered_at 只能由引擎在触发当刻写入（planner.go:219-229），
//     公开 API 没有任何回填/改写入口，同样需要直连数据库写入才能构造超窗行。
package catalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"ehome/backend/pkg/frame"
	"ehome/backend/simulation/harness"
)

func init() {
	Register(Scenario{
		ID:     "SIM-CNFM-001",
		Title:  "高风险规则触发后停在「待确认」，不会自己动手",
		Domain: DomainCNFM,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-CNFM-001；docs/设计/自动化确认制闭环实现方案.md",
		Run:    cnfmRun001,
	})
	Register(Scenario{
		ID:     "SIM-CNFM-002",
		Title:  "待确认时会给用户一条明确的待办通知",
		Domain: DomainCNFM,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-CNFM-002；docs/设计/通知中心.md",
		Run:    cnfmRun002,
	})
	Register(Scenario{
		ID:     "SIM-CNFM-003",
		Title:  "管理员确认后动作真正下发并闭环（事件转为 executed）",
		Domain: DomainCNFM,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-CNFM-003；docs/设计/自动化确认制闭环实现方案.md",
		Run:    cnfmRun003,
	})
	Register(Scenario{
		ID:     "SIM-CNFM-004",
		Title:  "重复确认同一事件不会重复下发（幂等）",
		Domain: DomainCNFM,
		Doc:    "docs/设计/自动化引擎场景仿真验证.md §4 SIM-CNFM-004；docs/设计/自动化确认制闭环实现方案.md",
		Run:    cnfmRun004,
	})
}

// ---------------------------------------------------------------------------
// 领域夹具（本域内三个子域文件共用：cnfm / dbln / manu 都需要"可被下发的节点"）
// ---------------------------------------------------------------------------

// cnfmOperationRow 只取断言需要的字段（models.CommandExecution 的 JSON 形状）。
type cnfmOperationRow struct {
	CommandID    string `json:"command_id"`
	EdgeDeviceID uint   `json:"edge_device_id"`
	ActionID     string `json:"action_id"`
	Status       string `json:"status"`
	ActorUserID  uint   `json:"actor_user_id"`
}

// cnfmPrepareDispatchable 让节点具备"可被 commandexec 接纳"的运行时事实。
//
// 为什么必须做这一步：commandexec 的可用性门禁全部 fail-closed，而它们依赖的运行时
// 事实**只能由 MQTT 帧写入**（HTTP 侧没有任何写入口）：
//   - currentCapabilities（channel_cmd_v2_transport.go:249-261）：boot_id 非空 +
//     resource_reported_at 在 5 分钟内 + command_engine_revision != 0 + 能力齐全；
//   - requireReportedActionChannel（runtime_channel.go:22-43）：hardware_info.channels[]
//     含 {id: <channelID>, enabled: true}；
//   - requireAppliedManifest（runtime_channel.go:45-50）：config_status=applied +
//     config_sync_state=in_sync。
//
// 顺序不可颠倒：nodemgr/handler_hello.go:222 在每次 Hello 时主动清空上一代能力报告，
// 因此必须 Hello 之后再 ResourceReport（HelloThenReport 已把这一步固化）。
func cnfmPrepareDispatchable(e *harness.Env, fx *autoFixture) {
	t := e.T
	t.Helper()

	fx.device.HelloThenReport("1.0.0", "sim-c6", 1, []harness.ReportedChannel{
		{ID: uint64(fx.channelID), Enabled: true},
	})

	// ① ResourceReport 是异步落库的：服务端在它到达之前算不出合法清单
	//    （validateManifestAuthority 要求节点已上报总线事实），会把这轮下发判为 failed。
	e.Eventually(20*time.Second, func() error {
		node := e.Admin.Get("/api/v1/nodes/" + fx.nodeID)
		if err := node.Check(http.StatusOK); err != nil {
			return err
		}
		capabilities := strings.TrimSpace(node.DataString("capabilities"))
		if capabilities == "" || capabilities == "{}" {
			return fmt.Errorf("节点 %s 的总线能力尚未落库（capabilities=%q）", fx.nodeID, capabilities)
		}
		if node.DataString("boot_id") == "" || node.DataInt("command_engine_revision") == 0 {
			return fmt.Errorf("节点 %s 的固件能力事实不完整（boot_id=%q revision=%d）",
				fx.nodeID, node.DataString("boot_id"), node.DataInt("command_engine_revision"))
		}
		return nil
	})

	// ② 能力就绪后强制生成一份**新**清单：能力上报前推送的那份已被服务端自己拒收，
	//    其 sync_id 再也无法被回执命中。
	if err := e.Admin.Post("/api/v1/nodes/"+fx.nodeID+"/config/sync", map[string]any{}).Check(http.StatusOK); err != nil {
		e.Fatalf("触发强制配置下发失败: %v", err)
	}

	// ③ 回执"最近一帧 ConfigManifest"的 manifest_id + sync_id（服务端只认最新一份）。
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
		manifest, sync, err := autoManifestIDs(frames[len(frames)-1].Raw)
		if err != nil {
			return err
		}
		manifestID, syncID = manifest, sync
		if err := fx.device.ConfigResult(manifestID, syncID, true); err != nil {
			return fmt.Errorf("上报 ConfigResult 失败: %w", err)
		}
		return fmt.Errorf("已回执清单 %s（sync=%s），等待节点状态收敛", manifestID, syncID)
	})
	e.Evidence("dispatchable_node", map[string]any{
		"node_id": fx.nodeID, "manifest_id": manifestID, "sync_id": syncID,
	})
}

// cnfmReauthenticate 刷新操作者的近认证窗口。
//
// 为什么每个确认场景都要显式做一次：确认链路的 IssueConfirmation 会核对操作者的
// users.last_login_at 是否在 10 分钟内（commandexec/confirmation.go:143、32），
// 而这个窗口只由登录/重新认证刷新（auth/login.go:38-42）。仿真里不能依赖
// "harness 初始化登录到现在还没超 10 分钟"这种时间巧合，必须显式刷新。
func cnfmReauthenticate(e *harness.Env) {
	e.T.Helper()
	e.Admin.Post("/api/v1/account/reauthenticate", map[string]any{
		"password": e.AdminPass,
	}).Expect(http.StatusOK)
}

// cnfmEvents 读某条规则下的全部事件（可带 result 过滤）。
func cnfmEvents(e *harness.Env, ruleID int64, result string) ([]autoEventRow, error) {
	query := "?rule_id=" + strconv.FormatInt(ruleID, 10)
	if result != "" {
		query += "&result=" + result
	}
	return autoListEvents(e, query)
}

// cnfmWaitPending 等到规则出现一条 pending_confirm 事件并返回它。
func cnfmWaitPending(e *harness.Env, ruleID int64) autoEventRow {
	e.T.Helper()
	var pending autoEventRow
	e.Eventually(25*time.Second, func() error {
		rows, err := cnfmEvents(e, ruleID, "pending_confirm")
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("策略 %d 尚未产生 pending_confirm 事件", ruleID)
		}
		// 列表按 id DESC：取列表首条即最新一条待确认事件。
		pending = rows[0]
		return nil
	})
	return pending
}

// cnfmOperations 读某台边缘设备的受控指令记录（用户可见的"下发历史"）。
func cnfmOperations(e *harness.Env, edgeDeviceID uint) ([]cnfmOperationRow, error) {
	r := e.Admin.Get("/api/v1/edge-devices/" + strconv.FormatUint(uint64(edgeDeviceID), 10) + "/operations")
	if r.Status != http.StatusOK {
		return nil, fmt.Errorf("GET /api/v1/edge-devices/%d/operations 返回 %d: %s",
			edgeDeviceID, r.Status, r.BodyString())
	}
	var rows []cnfmOperationRow
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		return nil, fmt.Errorf("解析指令执行记录失败: %w（data=%s）", err, autoHead(string(r.Data), 200))
	}
	return rows, nil
}

// cnfmCountAction 统计某台设备上某个动作的受控指令条数。
//
// 这是"确实没下发"的**无时序依赖**证据：commandexec.Service.Create 是在触发处理
// 的同一次调用里同步写库的（planner.go:137-155），只要事件已经落库可查，
// 指令行要么已存在、要么永远不会因这次触发而出现。比"等 N 秒看有没有帧"更强。
func cnfmCountAction(e *harness.Env, edgeDeviceID uint, actionID string) int {
	e.T.Helper()
	rows, err := cnfmOperations(e, edgeDeviceID)
	if err != nil {
		e.Fatalf("%v", err)
	}
	count := 0
	for _, row := range rows {
		if row.ActionID == actionID {
			count++
		}
	}
	return count
}

// cnfmConfirm 发一次人工确认（body 为空；planner 即铸即销 token）。
func cnfmConfirm(e *harness.Env, eventID uint) *harness.Response {
	e.T.Helper()
	return e.Admin.Post("/api/v1/automation-events/"+strconv.FormatUint(uint64(eventID), 10)+"/confirm",
		map[string]any{})
}

// cnfmPendingRuleBody 生成一条"高风险 + 待确认"的规则体。
//
// action_id 必须选 risk >= medium 的动作：ConfirmEvent 的即铸流程走
// IssueConfirmation，而它对 low 风险动作返回 ErrConfirmationNotNeeded
// （confirmation.go:84-86、116-118）—— 用低风险动作会得到 500 而不是确认闭环。
// reset_rainfall 是 sn3001_rain 目录里 risk=high + bounded_sequence + readback 的动作，
// 也正是 require_confirmed 想要守护的那类"会动现场"的操作。
func cnfmPendingRuleBody(name string, fx *autoFixture, cooldownSec int) map[string]any {
	return map[string]any{
		"name":                   name,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"trigger_duration_sec":   0,
		"action_type":            "device_action",
		"action_device_id":       fx.edgeDeviceID,
		"action_id":              "reset_rainfall",
		"action_params_json":     "{}",
		"require_confirmed":      true,
		"cooldown_sec":           cooldownSec,
		"max_daily_exec":         0,
	}
}

// ---------------------------------------------------------------------------
// SIM-CNFM-001 高风险规则触发后停在「待确认」，不会自己动手
// ---------------------------------------------------------------------------

func cnfmRun001(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-CNFM-001", "hold", "sn3001_rain")
	cnfmPrepareDispatchable(e, fx)

	// 被测规则：高风险动作（reset_rainfall: risk=high + bounded_sequence + readback），
	// 触发后必须停在 pending_confirm，一条指令都不许下发。
	pendingID := autoCreateRule(e, cnfmPendingRuleBody(e.NS("SIM-CNFM-001", "hold"), fx, 60))

	// 对照 A（触发/求值链路是活的）：同一台设备、同一批数据、同一条触发条件，
	// action 换成纯通知 —— 自动路径必须真的产生一条通知事件。
	// 没有它，"没有下发"既可能是确认制挡住了，也可能是引擎压根没求值。
	probeID := autoCreateRule(e, dblnNotificationActionRule(e.NS("SIM-CNFM-001", "probe"), fx, 60))

	// 对照 B（commandexec 下发链路是活的）：同设备的只读动作 read_rainfall（risk=low，
	// 无需确认），走**手动触发**下发 —— 必须产生 executed + 真实指令记录 + 真实下行帧。
	//
	// 两条设计约束，缺一则这个对照会变成假红：
	//  - 用**手动触发**而不是自动触发：手动触发走 executeManualDeviceAction，用 JWT 里的
	//    操作者 ID，不依赖自动路径的健康状况（历史上 systemActorID 缺陷曾让自动路径的
	//    device_action 静默失败，见台账 2026-09-12 第六轮；该缺陷已修，但对照不应
	//    把自己绑在别人的修复状态上）。
	//  - 触发条件写成 **lt 0.0**（温度永远不会低于 0℃）而不是与真实上报同向的 gt 20：
	//    这样自动路径**永远不会**执行这条对照规则，也就不可能抢先占掉它的冷却窗口。
	//    手动触发按设计跳过条件评估（planner.go:359-368），因此照样能执行 ——
	//    这个对照顺带也证明了"手动触发不看条件"。
	//
	// 反面教训（真实踩过）：曾把 cooldown_sec 设成 0 想让冷却兜底整段跳过，但
	// models.AutomationRule.CooldownSec 带 gorm:"default:300"，GORM 的零值规则会在
	// INSERT 时写 DB 默认值 300 —— 实体里的 0 被静默放大成 300s 冷却，实测第二次
	// 触发返回 suppressed_cooldown。这是与 Enabled 同类的缺陷（见 models/automation.go:105-109
	// 对 Enabled 的注释），已单独上报；场景这边一律用显式非零冷却值，不依赖 0。
	dispatchID := autoCreateRule(e, map[string]any{
		"name":                   e.NS("SIM-CNFM-001", "dispatch"),
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "lt",
		"trigger_threshold":      0.0,
		"trigger_duration_sec":   0,
		"action_type":            "device_action",
		"action_device_id":       fx.edgeDeviceID,
		"action_id":              "read_rainfall",
		"action_params_json":     "{}",
		"require_confirmed":      false,
		"cooldown_sec":           60,
		"max_daily_exec":         0,
	})
	e.Evidence("controls", map[string]any{
		"pending_rule": pendingID, "probe_rule": probeID, "dispatch_rule": dispatchID,
	})

	// 真实数据流：235 * 0.1 = 23.5 ℃ > 20.0 ℃。
	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}

	pending := cnfmWaitPending(e, pendingID)

	// 对照 A：自动路径确实求值并触发了。
	e.Eventually(25*time.Second, func() error {
		rows, err := cnfmEvents(e, probeID, "notification")
		if err != nil {
			return err
		}
		if len(rows) == 0 {
			return fmt.Errorf("对照策略 %d 未产生通知事件，无法证明求值链路是活的", probeID)
		}
		return nil
	})

	// 对照 B：受控链路对这台设备真的能下发。
	dispatched := manuTrigger(e, dispatchID).Expect(http.StatusOK)
	var control autoEventRow
	dispatched.Decode(&control)
	if control.Result != "executed" || control.CommandID == "" {
		e.Fatalf("对照策略手动触发未真正下发: result=%q command_id=%q detail=%q",
			control.Result, control.CommandID, control.Detail)
	}
	if got := cnfmCountAction(e, fx.edgeDeviceID, "read_rainfall"); got == 0 {
		e.Fatalf("对照策略执行后设备上没有 read_rainfall 的指令记录，无法证明下发链路是活的")
	}
	// 受控链路真的把帧发到了设备上（不只是数据库里多一行）。
	if _, err := fx.device.AwaitRawAfter(frame.MsgChannelCmdV2, 0, 20*time.Second); err != nil {
		e.Fatalf("对照策略执行后设备未收到 ChannelCmdV2 下行帧: %v", err)
	}

	// 不变式 1：待确认事件绝不能已经执行。
	if pending.CommandID != "" {
		e.Fatalf("待确认事件的 command_id=%q —— 未经人工确认就下发了动作", pending.CommandID)
	}
	if pending.TriggerSource != "auto" {
		e.Fatalf("自动触发的事件 trigger_source=%q，期望 auto", pending.TriggerSource)
	}
	if pending.TriggerValue == nil {
		e.Fatalf("sensor_threshold 事件必须带 trigger_value，实际为空（event=%+v）", pending)
	}
	autoEventuallyFloat(e.T, "待确认事件的 trigger_value", *pending.TriggerValue, 23.5)

	// 不变式 2：那条高风险动作在受控链路上**一条记录都没有**。
	// 这是"确实没下发"的无时序依赖证据（Create 与触发处理同步落库）。
	if got := cnfmCountAction(e, fx.edgeDeviceID, "reset_rainfall"); got != 0 {
		e.Fatalf("未经确认的 reset_rainfall 在设备上出现了 %d 条指令记录（确认制被绕过）", got)
	}

	e.Evidence("SIM-CNFM-001.hold", map[string]any{
		"rule_id": pendingID, "event_id": pending.ID, "result": pending.Result,
		"probe_rule_id": probeID, "dispatch_rule_id": dispatchID,
		"control_command_id": control.CommandID, "reset_rainfall_operations": 0,
	})
}

// ---------------------------------------------------------------------------
// SIM-CNFM-002 待确认时会给用户一条明确的待办通知
// ---------------------------------------------------------------------------

func cnfmRun002(e *harness.Env) {
	// 本场景不验证下发，只要确认分流与通知：无需准备固件能力事实。
	fx := autoProvisionDevice(e, "SIM-CNFM-002", "todo", "sn3001_rain")
	name := e.NS("SIM-CNFM-002", "todo")

	before := autoUnreadCount(e)
	ruleID := autoCreateRule(e, cnfmPendingRuleBody(name, fx, 60))

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}
	pending := cnfmWaitPending(e, ruleID)

	// 不变式：待办通知必须能让用户知道"是哪条策略、确认哪一条事件"。
	var landed autoNotificationRow
	e.Eventually(20*time.Second, func() error {
		rows, err := autoNotifications(e)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Source == "automation_rule" && row.SourceID == strconv.FormatInt(ruleID, 10) {
				landed = row
				return nil
			}
		}
		return fmt.Errorf("通知中心尚无策略 %d 的待确认通知（共 %d 条）", ruleID, len(rows))
	})

	if landed.Type != "warning" {
		e.Fatalf("待确认通知的 type=%q，期望 warning（planner 用 AlertLevelWarning 构造）", landed.Type)
	}
	if !strings.Contains(landed.Title, name) {
		e.Fatalf("待确认通知标题 %q 未包含策略名 %q（用户无法辨认是哪条策略）", landed.Title, name)
	}
	if landed.Read {
		e.Fatalf("新待确认通知 %d 不应是已读状态", landed.ID)
	}
	// 通知正文必须带 event_id：没有它，用户点开也不知道要确认哪一条事件
	// （planner.go:290 的 notifyConfirmation 正是为此把 eventID 拼进正文）。
	if !cnfmNotificationMentionsEvent(e, landed.ID, pending.ID) {
		e.Fatalf("待确认通知 %d 的正文未提到事件 %d，用户无法据此定位要确认的对象", landed.ID, pending.ID)
	}

	after := autoUnreadCount(e)
	if after <= before {
		e.Fatalf("待确认通知后未读数未增加：之前 %d，之后 %d", before, after)
	}

	e.Evidence("SIM-CNFM-002.todo", map[string]any{
		"rule_id": ruleID, "event_id": pending.ID, "notification_id": landed.ID,
		"notification_type": landed.Type, "unread_before": before, "unread_after": after,
	})
}

// cnfmNotificationMentionsEvent 在通知正文里查找 event_id=<id>。
// 为什么按正文查而不是按字段：models.Notification 没有 event_id 列，
// 事件号只存在于正文（planner.go:290 的格式化文本），这也正是界面上唯一的线索。
func cnfmNotificationMentionsEvent(e *harness.Env, notificationID, eventID uint) bool {
	e.T.Helper()
	r := e.Admin.Get("/api/v1/notifications?limit=100")
	if r.Status != http.StatusOK {
		e.Fatalf("GET /api/v1/notifications 返回 %d: %s", r.Status, r.BodyString())
	}
	var rows []struct {
		ID          uint   `json:"id"`
		Message     string `json:"message"`
		Description string `json:"description"`
	}
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		e.Fatalf("解析通知列表失败: %v", err)
	}
	needle := fmt.Sprintf("event_id=%d", eventID)
	for _, row := range rows {
		if row.ID != notificationID {
			continue
		}
		return strings.Contains(row.Message, needle) || strings.Contains(row.Description, needle)
	}
	return false
}

// ---------------------------------------------------------------------------
// SIM-CNFM-003 管理员确认后动作真正下发并闭环（事件转为 executed）
// ---------------------------------------------------------------------------

func cnfmRun003(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-CNFM-003", "ok", "sn3001_rain")
	cnfmPrepareDispatchable(e, fx)

	name := e.NS("SIM-CNFM-003", "ok")
	ruleID := autoCreateRule(e, cnfmPendingRuleBody(name, fx, 60))

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}
	pending := cnfmWaitPending(e, ruleID)

	// 确认前先证明链路上确实还没有这条动作的记录（否则"确认后出现"没有对照）。
	if got := cnfmCountAction(e, fx.edgeDeviceID, "reset_rainfall"); got != 0 {
		e.Fatalf("确认前设备上已有 %d 条 reset_rainfall 指令记录（确认制被绕过）", got)
	}

	// 前置：刷新近认证窗口（commandexec/confirmation.go:143 的 10 分钟门）。
	cnfmReauthenticate(e)

	// 记录确认前的下行帧游标：确认后必须观察到一帧真实的 ChannelCmdV2(0x15)。
	seq := fx.device.FrameSeq()

	confirmed := cnfmConfirm(e, pending.ID).Expect(http.StatusOK)
	var closed autoEventRow
	confirmed.Decode(&closed)

	// 不变式 1：同一行事件闭环为 executed，并带上真实下发的指令号。
	if closed.ID != pending.ID {
		e.Fatalf("确认返回的事件 id=%d，期望 %d", closed.ID, pending.ID)
	}
	if closed.Result != "executed" {
		e.Fatalf("人工确认后事件 result=%q，期望 executed（detail=%q）", closed.Result, closed.Detail)
	}
	if closed.CommandID == "" {
		e.Fatalf("executed 事件必须带 command_id（回链 command_executions），实际为空")
	}

	// 不变式 2：动作真的下了设备 —— 节点在 control 主题收到 ChannelCmdV2 帧。
	// 这是"确认后真正下发"的链路级证据，而不是只看数据库里多了一行。
	if _, err := fx.device.AwaitRawAfter(frame.MsgChannelCmdV2, seq, 20*time.Second); err != nil {
		e.Fatalf("确认后设备未收到 ChannelCmdV2 下行帧: %v", err)
	}

	// 不变式 3：受控链路留下了这条动作的执行记录，且归因到操作者（不是匿名）。
	var execRow cnfmOperationRow
	e.Eventually(15*time.Second, func() error {
		rows, err := cnfmOperations(e, fx.edgeDeviceID)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.CommandID == closed.CommandID {
				execRow = row
				return nil
			}
		}
		return fmt.Errorf("指令 %s 尚未出现在设备的执行记录里（共 %d 条）", closed.CommandID, len(rows))
	})
	if execRow.ActionID != "reset_rainfall" {
		e.Fatalf("指令 %s 的 action_id=%q，期望 reset_rainfall", execRow.CommandID, execRow.ActionID)
	}
	if execRow.ActorUserID == 0 {
		e.Fatalf("确认下发的指令没有操作者归因（actor_user_id=0）")
	}

	// 不变式 4：列表口径读到的是同一事实（不是只在确认响应里"看起来成功"）。
	e.Eventually(10*time.Second, func() error {
		rows, err := cnfmEvents(e, ruleID, "executed")
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

	e.Evidence("SIM-CNFM-003.confirmed", map[string]any{
		"rule_id": ruleID, "event_id": pending.ID, "command_id": closed.CommandID,
		"action_id": execRow.ActionID, "actor_user_id": execRow.ActorUserID,
		"execution_status": execRow.Status,
	})
}

// ---------------------------------------------------------------------------
// SIM-CNFM-004 重复确认同一事件不会重复下发（幂等）
// ---------------------------------------------------------------------------

func cnfmRun004(e *harness.Env) {
	fx := autoProvisionDevice(e, "SIM-CNFM-004", "dup", "sn3001_rain")
	cnfmPrepareDispatchable(e, fx)

	ruleID := autoCreateRule(e, cnfmPendingRuleBody(e.NS("SIM-CNFM-004", "dup"), fx, 60))

	if err := fx.report(235); err != nil {
		e.Fatalf("节点上报数据失败: %v", err)
	}
	pending := cnfmWaitPending(e, ruleID)

	cnfmReauthenticate(e)
	first := cnfmConfirm(e, pending.ID).Expect(http.StatusOK)
	var closed autoEventRow
	first.Decode(&closed)
	if closed.Result != "executed" || closed.CommandID == "" {
		e.Fatalf("首次确认未闭环: result=%q command_id=%q detail=%q",
			closed.Result, closed.CommandID, closed.Detail)
	}

	// 第一次确认的帧已经到达后再取游标，"第二次确认不应再有帧"才有意义。
	if _, err := fx.device.AwaitRawAfter(frame.MsgChannelCmdV2, 0, 20*time.Second); err != nil {
		e.Fatalf("首次确认后设备未收到 ChannelCmdV2 下行帧: %v", err)
	}
	seq := fx.device.FrameSeq()

	// 不变式 1：第二次确认必须被拒绝。
	// planner.go:559-564：result 已不是 pending_confirm → ErrConfirmNotPending，
	// handler_automation.go:536-537 映射为 409。
	again := cnfmConfirm(e, pending.ID)
	again.ExpectError(http.StatusConflict, "")

	// 不变式 2：受控链路上仍然只有第一次那一条指令（没有第二次下发）。
	if got := cnfmCountAction(e, fx.edgeDeviceID, "reset_rainfall"); got != 1 {
		e.Fatalf("重复确认后设备上的 reset_rainfall 指令记录变成了 %d 条，期望 1 条", got)
	}

	// 不变式 3：没有第二条 executed 事件（事件行是就地翻转，不是再插一条）。
	rows, err := cnfmEvents(e, ruleID, "executed")
	if err != nil {
		e.Fatalf("%v", err)
	}
	if len(rows) != 1 {
		e.Fatalf("策略 %d 的 executed 事件有 %d 条，期望 1 条: %+v", ruleID, len(rows), rows)
	}
	if rows[0].ID != pending.ID {
		e.Fatalf("executed 事件 id=%d，期望就是被确认的那条 %d", rows[0].ID, pending.ID)
	}

	// 不变式 4：确认之后没有任何新的下行指令帧。
	// 这里是有界负向观察窗口（等一个"必须不出现"的帧），不是用 sleep 同步正向断言；
	// "确实没下发"的主证据是上面无时序依赖的指令条数。
	if raw, err := fx.device.AwaitRawAfter(frame.MsgChannelCmdV2, seq, 2*time.Second); err == nil {
		e.Fatalf("重复确认后设备仍收到新的 ChannelCmdV2 帧（%d 字节），说明发生了二次下发", len(raw))
	}

	e.Evidence("SIM-CNFM-004.idempotent", map[string]any{
		"rule_id": ruleID, "event_id": pending.ID, "command_id": closed.CommandID,
		"duplicate_confirm_status":  again.Status,
		"reset_rainfall_operations": 1,
	})
}
