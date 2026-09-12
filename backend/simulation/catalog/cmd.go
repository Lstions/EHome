//go:build simulation

// 场景目录 · SIM-CMD 指令下发与审计（设计 §9 SIM-CMD-001..006）。
//
// 契约：docs/设计/场景仿真验证框架.md（§5 harness API、§5.4 场景模型、§7 红线、§9 场景清单）；
// 领域依据：docs/设计/设备指令与操作体系演进方案.md、docs/设计/边缘设备控制.md。
//
// 指令下发的门禁全部 fail-closed，且依赖的"设备侧运行时事实"只能由 MQTT 帧写入
// （HTTP 侧没有任何写入口）。因此本域夹具在 Hello 之后必须补齐两件事，
// 否则任何"成功下发"路径都不可能成立（只能断言拒绝路径）：
//
//  1. ResourceReport（0x19）→ nodes.boot_id / resource_reported_at /
//     command_engine_revision / command_engine_capabilities 四件套，
//     以及 hardware_info.channels[]（commandexec.requireReportedActionChannel）；
//  2. ConfigResult（0x05）→ config_status=applied / config_sync_state=in_sync
//     （commandexec.requireAppliedManifest）。
//
// 顺序陷阱：handler_hello.go 在**每次** Hello 时清空能力四件套
// （"Hello 开启新的固件世代"），所以 ResourceReport 必须在 Hello 之后。
//
// 命名纪律（设计 §4.1）：本文件的包级标识符一律以域短名 cmd 开头。
package catalog

import (
	"context"
	"database/sql"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"ehome/backend/pkg/frame"
	"ehome/backend/simulation/harness"
)

// 域标识（设计 v1.1 冻结：取 §6 表"前缀"列去 SIM- 的短名）。
const cmdDomain Domain = "CMD"

// 本域使用的三个动作（drivers/builtin.go 的 SN3001RainDriver.ControlActions）：
const (
	// cmdActionReadRainfall：low 风险、单步读、无参数 —— 成功下发路径。
	cmdActionReadRainfall = "read_rainfall"
	// cmdActionSetSensitivity：high 风险、带参数、需确认 —— 幂等与确认门禁路径。
	cmdActionSetSensitivity = "set_rain_sensitivity"
	// cmdActionClearRainfall：high 风险但引擎未放行（AvailabilityCode=
	// hardware_evidence_required）—— fail-closed 门禁路径。
	cmdActionClearRainfall = "clear_rainfall_write"
)

func init() {
	Register(Scenario{
		ID:     "SIM-CMD-001",
		Title:  "管理员下发一条控制指令后，目标节点确实收到了对应的指令帧",
		Domain: cmdDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-CMD-001；docs/设计/设备指令与操作体系演进方案.md",
		Run:    cmdRun001,
	})
	Register(Scenario{
		ID:     "SIM-CMD-002",
		Title:  "网络抖动导致同一操作被重复提交时，设备只会收到一次指令",
		Domain: cmdDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-CMD-002；docs/设计/边缘设备控制.md（幂等键）",
		Run:    cmdRun002,
	})
	Register(Scenario{
		ID:     "SIM-CMD-003",
		Title:  "每一次控制下发都能在审计记录里查到是谁、对哪台设备、做了什么、结果如何",
		Domain: cmdDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-CMD-003；docs/设计/设备指令与操作体系演进方案.md（审计）",
		Run:    cmdRun003,
	})
	Register(Scenario{
		ID:     "SIM-CMD-004",
		Title:  "还没有放行的高风险指令会被系统直接拒绝，确认凭据也不会被签发",
		Domain: cmdDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-CMD-004；docs/设计/边缘设备控制.md（风控门禁 fail-closed）",
		Run:    cmdRun004,
	})
	Register(Scenario{
		ID:     "SIM-CMD-005",
		Title:  "目标节点离线时下发指令会明确报错，而不是假装成功",
		Domain: cmdDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-CMD-005；docs/设计/边缘设备控制.md",
		Run:    cmdRun005,
	})
	Register(Scenario{
		ID:     "SIM-CMD-006",
		Title:  "下发过的指令都能查到执行记录，并且写明了没有成功的原因",
		Domain: cmdDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-CMD-006；docs/设计/设备指令与操作体系演进方案.md（执行记录）",
		Run:    cmdRun006,
	})
}

// ---------------------------------------------------------------------------
// 夹具与工具
// ---------------------------------------------------------------------------

// cmdNodeStateRow 是 GET /nodes/:id 里与本域门禁相关的事实。
type cmdNodeStateRow struct {
	ID                    uint   `json:"id"`
	NodeID                string `json:"node_id"`
	Status                string `json:"status"`
	ConfigVersion         string `json:"config_version"`
	ConfigStatus          string `json:"config_status"`
	ConfigSyncState       string `json:"config_sync_state"`
	LastSyncID            string `json:"last_sync_id"`
	BootID                string `json:"boot_id"`
	CommandEngineRevision uint32 `json:"command_engine_revision"`
}

// cmdNodeState 读取节点当前状态（管理面）。
func cmdNodeState(e *harness.Env, nodeID string) cmdNodeStateRow {
	resp := e.Admin.Get("/api/v1/nodes/" + nodeID).Expect(http.StatusOK)
	var row cmdNodeStateRow
	resp.Decode(&row)
	return row
}

// cmdOpsSession 返回一个"与管理员同令牌、但请求头独立"的会话。
//
// 为什么需要：幂等键通过 Idempotency-Key 请求头传递，而 Session.WithHeader
// 是**永久**改写该会话的请求头；直接改 Env.Admin 会把上一个场景的幂等键
// 带进下一个场景（同设备同动作会被误判成幂等重放）。独立会话把影响限制在
// 本场景内。
func cmdOpsSession(e *harness.Env) *harness.Session {
	session := e.NewSession()
	session.Token = e.Admin.Token
	return session
}

// cmdArmNode 把一台已握手的仿真节点武装成"动作目录可用"的状态。
//
// 三步缺一不可，且顺序即真实固件的顺序（Hello 已在夹具里完成）：
//
//	Hello → ResourceReport（能力四件套 + 运行期通道）→ 配置清单回执。
func cmdArmNode(e *harness.Env, fx *edgeDevice) {
	t := e.T
	t.Helper()

	// 保持节点在线：命令门禁要求 node.status == "online"，
	// 而 offlinedetector 会把 last_seen 超过 90s 的在线节点判为离线。
	// 这里用真实心跳维持（不是 sleep 同步，见 edgeDevice.heartbeat）。
	fx.heartbeat()

	bootID := "sim-boot-" + fx.NodeID
	if len(bootID) > 32 {
		bootID = bootID[:32] // handler_resources.go 要求 boot_id 长度 1..32
	}
	// 顺序不可颠倒：Hello 会清空能力字段，ResourceReport 必须在其之后。
	if err := fx.Device.ResourceReport(harness.ResourceReportData{
		Platform:             "esp32s3",
		Channels:             []harness.ReportedChannel{{ID: uint64(fx.ChannelID), Enabled: true}},
		BootID:               bootID,
		Revision:             1,
		SupportsChannelCmdV2: true,
		SupportsBoundedBatch: true,
		SupportsFinally:      true,
		MaxBatchSteps:        8,
		MaxTXBytes:           128,
		MaxRXBytes:           256,
		MaxStepTimeoutMS:     5000,
	}); err != nil {
		t.Fatalf("上报 ResourceReport 失败: %v", err)
	}
	// 不变量：能力四件套必须真的落到节点行上（否则门禁仍会 fail-closed）。
	e.Eventually(20*time.Second, func() error {
		state := cmdNodeState(e, fx.NodeID)
		if state.BootID == "" {
			return fmt.Errorf("boot_id 尚未落库")
		}
		if state.CommandEngineRevision == 0 {
			return fmt.Errorf("command_engine_revision 尚未落库")
		}
		return nil
	})
	e.Evidence("SIM-CMD.resource_report", cmdNodeState(e, fx.NodeID))

	// 配置回执：manifest 与 sync_id 由服务端在推送清单时持久化，
	// 设备侧从节点状态读到后原样回执（真实固件从同步协议获得这两个值）。
	e.Eventually(30*time.Second, func() error {
		state := cmdNodeState(e, fx.NodeID)
		if state.ConfigVersion == "" || state.LastSyncID == "" {
			return fmt.Errorf("服务端尚未生成待回执的清单: manifest=%q sync_id=%q",
				state.ConfigVersion, state.LastSyncID)
		}
		if state.ConfigSyncState == "in_sync" && state.ConfigStatus == "applied" {
			return nil
		}
		if state.ConfigSyncState != "syncing" {
			return fmt.Errorf("节点同步状态 = %q，尚未进入等待回执", state.ConfigSyncState)
		}
		return fx.Device.ConfigResult(state.ConfigVersion, state.LastSyncID, true)
	})
	e.Eventually(30*time.Second, func() error {
		state := cmdNodeState(e, fx.NodeID)
		if state.ConfigStatus != "applied" || state.ConfigSyncState != "in_sync" {
			return fmt.Errorf("清单未生效: config_status=%q config_sync_state=%q",
				state.ConfigStatus, state.ConfigSyncState)
		}
		return nil
	})
	e.Evidence("SIM-CMD.config_applied", cmdNodeState(e, fx.NodeID))
}

// cmdActionItem 是 GET /edge-devices/:id/actions 的目录项。
type cmdActionItem struct {
	Definition struct {
		ID        string `json:"id"`
		Version   int    `json:"version"`
		Risk      string `json:"risk"`
		Enabled   bool   `json:"enabled"`
		Transport string `json:"transport"`
	} `json:"definition"`
	Available  bool   `json:"available"`
	Reason     string `json:"reason"`
	ReasonCode string `json:"reason_code"`
}

// cmdActionCatalog 读取某台边缘设备的动作目录。
func cmdActionCatalog(e *harness.Env, edgeDeviceID int64) []cmdActionItem {
	resp := e.Admin.Get(fmt.Sprintf("/api/v1/edge-devices/%d/actions", edgeDeviceID)).
		Expect(http.StatusOK)
	var items []cmdActionItem
	resp.Decode(&items)
	return items
}

// cmdActionByID 在目录里按动作 ID 取一项。
func cmdActionByID(items []cmdActionItem, actionID string) (cmdActionItem, bool) {
	for _, item := range items {
		if item.Definition.ID == actionID {
			return item, true
		}
	}
	return cmdActionItem{}, false
}

// cmdDispatch 下发一条动作指令（真实入口 POST /edge-devices/:id/operations）。
func cmdDispatch(session *harness.Session, edgeDeviceID int64, actionID, idempotencyKey string,
	params map[string]any, confirmationToken, reason string) *harness.Response {
	body := map[string]any{"action_id": actionID}
	if params != nil {
		body["params"] = params
	}
	if confirmationToken != "" {
		body["confirmation_token"] = confirmationToken
	}
	if reason != "" {
		body["reason"] = reason
	}
	return session.WithHeader("Idempotency-Key", idempotencyKey).
		Post(fmt.Sprintf("/api/v1/edge-devices/%d/operations", edgeDeviceID), body)
}

// cmdCommandUUID 把执行记录里的 uuid 文本转成帧里的 16 字节身份。
func cmdCommandUUID(t *testing.T, commandID string) [16]byte {
	t.Helper()
	raw, err := hex.DecodeString(strings.ReplaceAll(commandID, "-", ""))
	if err != nil || len(raw) != 16 {
		t.Fatalf("执行记录 command_id 不是合法 uuid: %q (%v)", commandID, err)
	}
	var out [16]byte
	copy(out[:], raw)
	return out
}

// cmdAwaitChannelCmd 等待节点在 control 主题收到一条 ChannelCmdV2（0x15）指令帧，
// 并校验它是发给"这台设备 + 这条通道"的。
func cmdAwaitChannelCmd(e *harness.Env, fx *edgeDevice, after int, timeout time.Duration) frame.ChannelCmdV2 {
	t := e.T
	t.Helper()
	raw, err := fx.Device.AwaitRawAfter(frame.MsgChannelCmdV2, after, timeout)
	if err != nil {
		t.Fatalf("等待指令帧失败: %v", err)
	}
	decoded, err := frame.DecodeChannelCmdV2(raw)
	if err != nil {
		t.Fatalf("解码指令帧失败: %v", err)
	}
	if uint64(decoded.EdgeDeviceID) != uint64(fx.EdgeDeviceID) {
		t.Fatalf("指令帧的 edge_device_id = %d，期望 %d", decoded.EdgeDeviceID, fx.EdgeDeviceID)
	}
	if uint64(decoded.ChannelID) != uint64(fx.ChannelID) {
		t.Fatalf("指令帧的 channel_id = %d，期望 %d", decoded.ChannelID, fx.ChannelID)
	}
	return decoded
}

// cmdChannelCmdCount 返回该节点累计收到的指令帧条数。
func cmdChannelCmdCount(fx *edgeDevice) int { return len(fx.Device.FramesOf(frame.MsgChannelCmdV2)) }

// cmdAssertNoExecutions 断言设备上没有任何执行记录。
//
// 为什么不用 Decode：commandexec.Service.List 在无记录时返回 nil 切片，
// 信封 data 序列化为 null，Decode 会直接判失败；这里显式检查原始 data 段。
func cmdAssertNoExecutions(e *harness.Env, fx *edgeDevice, label string) {
	t := e.T
	t.Helper()
	resp := e.Admin.Get(fmt.Sprintf("/api/v1/edge-devices/%d/operations", fx.EdgeDeviceID)).
		Expect(http.StatusOK)
	body := strings.TrimSpace(string(resp.Data))
	if body != "null" && body != "[]" {
		t.Fatalf("%s：设备上不应存在执行记录，实际 %s", label, resp.BodyString())
	}
}

// cmdAuditRow 是 security_audit_events 的只读投影。
//
// 为什么直连数据库：审计行在用户界面上没有查询入口，属于设计 §3 原则 2(a)
// 明确允许的"用户界面看不到的持久化事实"。
type cmdAuditRow struct {
	EventName   string
	Result      string
	ActorType   string
	ActorUserID sql.NullInt64
	RequestID   string
	TargetType  string
	TargetID    string
	Metadata    string
}

// cmdAuditEvents 按 request_id（= command_id）读审计事件。
func cmdAuditEvents(e *harness.Env, commandID string) []cmdAuditRow {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const query = "SELECT event_name, result, actor_type, actor_user_id, request_id, target_type, target_id, metadata " +
		"FROM security_audit_events WHERE request_id = $1 ORDER BY id"
	rows, err := e.SQL().QueryContext(ctx, query, commandID)
	if err != nil {
		e.Fatalf("查询审计事件失败: %v", err)
	}
	defer rows.Close()
	var out []cmdAuditRow
	for rows.Next() {
		var row cmdAuditRow
		if err := rows.Scan(&row.EventName, &row.Result, &row.ActorType, &row.ActorUserID,
			&row.RequestID, &row.TargetType, &row.TargetID, &row.Metadata); err != nil {
			e.Fatalf("扫描审计事件失败: %v", err)
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		e.Fatalf("遍历审计事件失败: %v", err)
	}
	return out
}

// cmdProvisionNode 建一台"能在动作目录里看到指令"的仿真设备。
// 型号必须来自驱动注册表（动作定义由驱动注册），因此用 SN-3001 雨量计。
func cmdProvisionNode(e *harness.Env, scenarioID string, handshake bool) *edgeDevice {
	return edgeProvision(e, edgeDeviceSpec{
		Scenario:   scenarioID,
		Local:      "a",
		Suffix:     "a",
		DeviceType: "sn3001_rain",
		HardwareID: "1",
		IntervalMs: 60000,
		Handshake:  handshake,
	})
}

// ---------------------------------------------------------------------------
// SIM-CMD-001 管理员下发控制指令，节点在 control 主题收到对应帧
// ---------------------------------------------------------------------------

func cmdRun001(e *harness.Env) {
	t := e.T
	fx := cmdProvisionNode(e, "SIM-CMD-001", true)
	cmdArmNode(e, fx)

	// 起点必须是"这台设备现在真的可以下发指令"：目录里动作可用，
	// 否则后面的帧断言就只是在验证一条被拒绝的请求。
	catalog := cmdActionCatalog(e, fx.EdgeDeviceID)
	read, ok := cmdActionByID(catalog, cmdActionReadRainfall)
	if !ok {
		t.Fatalf("动作目录里没有 %s：%+v", cmdActionReadRainfall, catalog)
	}
	if !read.Available {
		t.Fatalf("武装后的目录里 %s 仍不可用: reason=%q reason_code=%q（夹具未把运行时事实上报齐）",
			cmdActionReadRainfall, read.Reason, read.ReasonCode)
	}
	if read.Definition.Risk != "low" {
		t.Fatalf("%s 的风险等级 = %q，期望 low", cmdActionReadRainfall, read.Definition.Risk)
	}
	e.Evidence("SIM-CMD-001.catalog", catalog)

	before := fx.Device.FrameSeq()
	session := cmdOpsSession(e)
	resp := cmdDispatch(session, fx.EdgeDeviceID, cmdActionReadRainfall,
		e.NS("SIM-CMD-001", "idem-0001"), nil, "", "").Expect(http.StatusAccepted)
	if replay := resp.DataBool("idempotent_replay"); replay {
		t.Fatalf("首次下发不应被判定为幂等重放：%s", resp.BodyString())
	}
	commandID := resp.DataString("execution.command_id")
	if commandID == "" {
		t.Fatalf("下发未返回执行记录 ID：%s", resp.BodyString())
	}
	if status := resp.DataString("execution.status"); status != "QUEUED" {
		t.Fatalf("新建执行记录状态 = %q，期望 QUEUED", status)
	}
	e.Evidence("SIM-CMD-001.execution", resp.BodyString())

	// 不变量：节点必须在 control 主题收到这条指令帧，且帧身份与执行记录一致。
	cmd := cmdAwaitChannelCmd(e, fx, before, 20*time.Second)
	if want := cmdCommandUUID(t, commandID); cmd.CommandID != want {
		t.Fatalf("指令帧携带的命令 ID 与执行记录不一致：帧=%x 记录=%x", cmd.CommandID, want)
	}
	if len(cmd.TXData) == 0 {
		t.Fatalf("指令帧没有携带任何待发送数据（TXData 为空）")
	}
	e.Evidence("SIM-CMD-001.frame", map[string]any{
		"command_id": commandID, "attempt": cmd.Attempt, "tx_hex": hex.EncodeToString(cmd.TXData),
		"read_size": cmd.ReadSize, "edge_device_id": cmd.EdgeDeviceID, "channel_id": cmd.ChannelID,
		"deadline_unix_ms": cmd.DeadlineUnixMS,
	})

	// 只有一条指令帧：一次下发不能被物理层放大成多次。
	if got := cmdChannelCmdCount(fx); got != 1 {
		t.Fatalf("一次下发产生了 %d 条指令帧，期望 1 条", got)
	}

	// 派发状态推进到"已交给传输层"，而不是停在队列里。
	e.Eventually(20*time.Second, func() error {
		state := e.Admin.Get("/api/v1/device-operations/" + commandID).Expect(http.StatusOK)
		status := state.DataString("status")
		if status != "DISPATCHED" && status != "DEVICE_ACCEPTED" {
			return fmt.Errorf("执行记录状态 = %q，期望已派发", status)
		}
		return nil
	})
}

// ---------------------------------------------------------------------------
// SIM-CMD-002 相同幂等键重复下发只执行一次，返回同一结果
// ---------------------------------------------------------------------------

func cmdRun002(e *harness.Env) {
	t := e.T
	fx := cmdProvisionNode(e, "SIM-CMD-002", true)
	cmdArmNode(e, fx)

	// 高风险带参动作需要"近期认证 + 一次性确认令牌"；重新登录刷新 LastLoginAt，
	// 否则长套件跑到这里会因近认证窗口（10 分钟）过期而被拒。
	if err := e.RefreshAdmin(); err != nil {
		t.Fatalf("刷新管理员会话失败: %v", err)
	}
	session := cmdOpsSession(e)

	params := map[string]any{"value": 60}
	reason := "仿真验证：设置雨量灵敏度"
	confirm := session.Post(
		fmt.Sprintf("/api/v1/edge-devices/%d/actions/%s/confirm", fx.EdgeDeviceID, cmdActionSetSensitivity),
		map[string]any{"params": params, "reason": reason}).Expect(http.StatusOK)
	token := confirm.DataString("token")
	if token == "" {
		t.Fatalf("确认接口未返回令牌：%s", confirm.BodyString())
	}

	key := e.NS("SIM-CMD-002", "idem-replay")
	first := cmdDispatch(session, fx.EdgeDeviceID, cmdActionSetSensitivity, key, params, token, reason).
		Expect(http.StatusAccepted)
	firstID := first.DataString("execution.command_id")
	if firstID == "" {
		t.Fatalf("首次下发未返回执行记录 ID：%s", first.BodyString())
	}
	if replay := first.DataBool("idempotent_replay"); replay {
		t.Fatalf("首次下发不应被判定为幂等重放：%s", first.BodyString())
	}
	e.Evidence("SIM-CMD-002.first", first.BodyString())

	// 重放：同一幂等键 + 同一参数。这里**不**再提供确认令牌——
	// 一次性令牌已被消费，重放必须命中持久化的执行记录，而不是重新过门禁。
	second := cmdDispatch(session, fx.EdgeDeviceID, cmdActionSetSensitivity, key, params, "", reason).
		Expect(http.StatusAccepted)
	if replay := second.DataBool("idempotent_replay"); !replay {
		t.Fatalf("相同幂等键的重复下发未被判定为重放：%s", second.BodyString())
	}
	if secondID := second.DataString("execution.command_id"); secondID != firstID {
		t.Fatalf("重放返回了不同的执行记录：first=%s second=%s", firstID, secondID)
	}
	e.Evidence("SIM-CMD-002.replay", second.BodyString())

	// 同键不同请求：必须 409 冲突，而不是悄悄按旧请求执行。
	collision := cmdDispatch(session, fx.EdgeDeviceID, cmdActionSetSensitivity, key,
		map[string]any{"value": 61}, "", reason).Expect(http.StatusConflict)
	if !strings.Contains(collision.Message, "idempotency key collision") {
		t.Fatalf("同键不同请求的拒绝原因不明确：%q", collision.Message)
	}
	e.Evidence("SIM-CMD-002.collision", collision.Message)

	// 不变量：物理下发只发生一次（幂等不能变成"发了两次但对上层只报一次"）。
	e.Eventually(20*time.Second, func() error {
		if got := cmdChannelCmdCount(fx); got != 1 {
			return fmt.Errorf("幂等重放导致 %d 条指令帧，期望 1 条", got)
		}
		return nil
	})

	// 执行记录也只有一条（幂等不是"建两条但只发一条"）。
	list := e.Admin.Get(fmt.Sprintf("/api/v1/edge-devices/%d/operations", fx.EdgeDeviceID)).
		Expect(http.StatusOK)
	var executions []struct {
		CommandID string `json:"command_id"`
		ActionID  string `json:"action_id"`
	}
	list.Decode(&executions)
	matching := 0
	for _, item := range executions {
		if item.ActionID == cmdActionSetSensitivity {
			matching++
		}
	}
	if matching != 1 {
		t.Fatalf("幂等键相同的两次请求产生了 %d 条执行记录，期望 1 条：%s", matching, list.BodyString())
	}
}

// ---------------------------------------------------------------------------
// SIM-CMD-003 每次下发都留下审计记录（操作者/目标/动作/结果）
// ---------------------------------------------------------------------------

func cmdRun003(e *harness.Env) {
	t := e.T
	fx := cmdProvisionNode(e, "SIM-CMD-003", true)
	cmdArmNode(e, fx)

	before := fx.Device.FrameSeq()
	resp := cmdDispatch(cmdOpsSession(e), fx.EdgeDeviceID, cmdActionReadRainfall,
		e.NS("SIM-CMD-003", "idem-audit"), nil, "", "").Expect(http.StatusAccepted)
	commandID := resp.DataString("execution.command_id")
	if commandID == "" {
		t.Fatalf("下发未返回执行记录 ID：%s", resp.BodyString())
	}
	cmdAwaitChannelCmd(e, fx, before, 20*time.Second)

	// 审计行必须与执行记录一一对应，并写清操作者、目标、动作、结果。
	var rows []cmdAuditRow
	e.Eventually(20*time.Second, func() error {
		rows = cmdAuditEvents(e, commandID)
		if len(rows) == 0 {
			return fmt.Errorf("尚未写入审计事件（request_id=%s）", commandID)
		}
		return nil
	})
	if len(rows) != 1 {
		t.Fatalf("一次下发应留下 1 条审计事件，实际 %d 条：%+v", len(rows), rows)
	}
	row := rows[0]
	if row.EventName != "device_action.created" {
		t.Fatalf("审计事件名 = %q，期望 device_action.created", row.EventName)
	}
	if row.Result != "queued" {
		t.Fatalf("审计结果 = %q，期望 queued", row.Result)
	}
	if row.ActorType != "user" {
		t.Fatalf("审计操作者类型 = %q，期望 user（人工下发）", row.ActorType)
	}
	if !row.ActorUserID.Valid || row.ActorUserID.Int64 <= 0 {
		t.Fatalf("审计事件没有记录操作者用户 ID：%+v", row)
	}
	if row.TargetType != "edge_device" {
		t.Fatalf("审计目标类型 = %q，期望 edge_device", row.TargetType)
	}
	if row.TargetID != fmt.Sprint(fx.EdgeDeviceID) {
		t.Fatalf("审计目标 = %q，期望边缘设备 #%d", row.TargetID, fx.EdgeDeviceID)
	}
	actionFragment := `"action_id":"` + cmdActionReadRainfall + `"`
	if !strings.Contains(row.Metadata, actionFragment) {
		t.Fatalf("审计事件的 metadata 没有记录动作 ID（期望包含 %s）：%s", actionFragment, row.Metadata)
	}
	e.Evidence("SIM-CMD-003.audit_event", map[string]any{
		"event_name": row.EventName, "result": row.Result, "actor_type": row.ActorType,
		"actor_user_id": row.ActorUserID.Int64, "target_type": row.TargetType,
		"target_id": row.TargetID, "metadata": row.Metadata,
	})

	// 相反方向：不存在的执行记录不得凭空产生审计行（防止"审计=噪声"）。
	if extra := cmdAuditEvents(e, "00000000-0000-4000-8000-000000000000"); len(extra) != 0 {
		t.Fatalf("不存在的执行记录却查到了审计事件：%+v", extra)
	}
}

// ---------------------------------------------------------------------------
// SIM-CMD-004 高风险指令在未放行时被门禁拒绝（fail-closed）
// ---------------------------------------------------------------------------

func cmdRun004(e *harness.Env) {
	t := e.T
	fx := cmdProvisionNode(e, "SIM-CMD-004", true)
	// 关键：先把节点武装到"环境门禁全绿"，这样被拒的原因只可能是风控门禁，
	// 而不是"节点离线/能力缺失"这类环境噪声。
	cmdArmNode(e, fx)
	if err := e.RefreshAdmin(); err != nil {
		t.Fatalf("刷新管理员会话失败: %v", err)
	}

	catalog := cmdActionCatalog(e, fx.EdgeDeviceID)
	blocked, ok := cmdActionByID(catalog, cmdActionClearRainfall)
	if !ok {
		t.Fatalf("动作目录里没有 %s：%+v", cmdActionClearRainfall, catalog)
	}
	// 不变量 1：未放行的高风险动作在目录里必须标记为不可用，并给出机器可读原因。
	if blocked.Available {
		t.Fatalf("未放行的高风险动作 %s 在目录里被标记为可用（门禁未 fail-closed）：%+v",
			cmdActionClearRainfall, blocked)
	}
	if blocked.Definition.Risk != "high" && blocked.Definition.Risk != "critical" {
		t.Fatalf("%s 的风险等级 = %q，期望 high 或 critical（本场景要求高风险动作）",
			cmdActionClearRainfall, blocked.Definition.Risk)
	}
	// 该动作带 AvailabilityCode（协议未冻结/仅允许实机证据），
	// deviceaction 的定义转换会强制 enabled=false（definition.go:571-577），
	// Catalog 因此报出 AvailabilityCode 而不是通用的引擎门禁码。
	if blocked.ReasonCode != "hardware_evidence_required" {
		t.Fatalf("高风险动作被拒的机器可读原因 = %q，期望 hardware_evidence_required（reason=%q）",
			blocked.ReasonCode, blocked.Reason)
	}
	if blocked.Definition.Enabled {
		t.Fatalf("未放行的高风险动作 %s 的定义被标记为 enabled（未放行 = 定义必须禁用）：%+v",
			cmdActionClearRainfall, blocked.Definition)
	}
	e.Evidence("SIM-CMD-004.catalog_blocked", blocked)

	session := cmdOpsSession(e)
	key := e.NS("SIM-CMD-004", "idem-blocked")
	rejected := cmdDispatch(session, fx.EdgeDeviceID, cmdActionClearRainfall, key, nil, "", "").
		Expect(http.StatusConflict)
	if !strings.Contains(rejected.Message, "action unavailable") {
		t.Fatalf("未放行动作的拒绝原因不明确：%q", rejected.Message)
	}
	e.Evidence("SIM-CMD-004.dispatch_rejected", rejected.Message)

	// 不变量 2（本场景的核心）：放行凭据本身也不能为未放行动作签发。
	// commandexec.IssueConfirmation 与 Create 共用同一批可用性事实
	// （confirmation.go:112-115 的 `!definition.Enabled` 分支），
	// 因此"先骗一张令牌、再拿令牌绕过动作门禁"这条路走不通：
	// 令牌在铸造环节就被拒，根本不存在可用于绕过的凭据。
	confirm := session.Post(
		fmt.Sprintf("/api/v1/edge-devices/%d/actions/%s/confirm", fx.EdgeDeviceID, cmdActionClearRainfall),
		map[string]any{"params": map[string]any{}, "reason": "仿真验证：申请放行"}).
		Expect(http.StatusConflict)
	if !strings.Contains(confirm.Message, "action unavailable") {
		t.Fatalf("为未放行动作申请确认的拒绝原因不明确：%q", confirm.Message)
	}
	// 注意不能用 DataString("token")：错误信封的 data 段是 null，
	// 点路径取值会直接判失败，反而掩盖了"确实没签发令牌"这个事实。
	if body := strings.TrimSpace(string(confirm.Data)); body != "null" && body != "" {
		t.Fatalf("未放行动作仍被签发了确认令牌：%s", confirm.BodyString())
	}
	e.Evidence("SIM-CMD-004.confirm_rejected", confirm.Message)

	// 不变量 3：被拒的请求不得留下执行记录，也不得产生任何物理下发。
	if got := cmdChannelCmdCount(fx); got != 0 {
		t.Fatalf("被门禁拒绝的请求仍然产生了 %d 条指令帧", got)
	}
	cmdAssertNoExecutions(e, fx, "被门禁拒绝的下发")

	// 不变量 4：已放行引擎、但缺少人工确认令牌的高风险动作，
	// 即使带着理由也必须被拒（理由不能替代令牌）。
	unconfirmed := cmdDispatch(session, fx.EdgeDeviceID, cmdActionSetSensitivity, key+"-2",
		map[string]any{"value": 60}, "", "仿真验证：未确认的高风险下发").Expect(http.StatusConflict)
	if !strings.Contains(unconfirmed.Message, "confirmation") {
		t.Fatalf("缺确认令牌的下发拒绝原因不明确：%q", unconfirmed.Message)
	}
	e.Evidence("SIM-CMD-004.confirmation_required", unconfirmed.Message)
}

// ---------------------------------------------------------------------------
// SIM-CMD-005 目标节点离线时下发以明确失败结束，不静默成功
// ---------------------------------------------------------------------------

func cmdRun005(e *harness.Env) {
	t := e.T
	// 只建管理侧记录、不做 Hello：这台节点从未上线，状态必须是 offline。
	fx := cmdProvisionNode(e, "SIM-CMD-005", false)

	state := cmdNodeState(e, fx.NodeID)
	if state.Status != "offline" {
		t.Fatalf("前置条件不成立：从未上线的节点状态 = %q，期望 offline", state.Status)
	}

	// 目录必须如实说明"设备或节点不可用"，而不是含糊的失败。
	catalog := cmdActionCatalog(e, fx.EdgeDeviceID)
	read, ok := cmdActionByID(catalog, cmdActionReadRainfall)
	if !ok {
		t.Fatalf("动作目录里没有 %s：%+v", cmdActionReadRainfall, catalog)
	}
	if read.Available {
		t.Fatalf("节点离线时动作 %s 仍被标记为可用：%+v", cmdActionReadRainfall, read)
	}
	e.Evidence("SIM-CMD-005.catalog_offline", read)

	failed := cmdDispatch(cmdOpsSession(e), fx.EdgeDeviceID, cmdActionReadRainfall,
		e.NS("SIM-CMD-005", "idem-offline"), nil, "", "").Expect(http.StatusConflict)
	if !strings.Contains(failed.Message, "action unavailable") {
		t.Fatalf("离线下发的拒绝原因不明确：%q", failed.Message)
	}
	e.Evidence("SIM-CMD-005.offline_rejected", failed.Message)

	// 不变量：明确失败 = 没有执行记录 + 没有物理下发（不能"报错但发了"）。
	cmdAssertNoExecutions(e, fx, "离线且被拒的下发")
	if got := cmdChannelCmdCount(fx); got != 0 {
		t.Fatalf("离线且被拒的下发仍然产生了 %d 条指令帧", got)
	}

	// 对照：同一台设备上线并武装后，同一条指令必须被接受。
	// 这证明上面的失败确实来自"节点离线"，而不是请求本身有问题。
	fx.Device.Hello("sim-1.0.0", "SIM-CMD", 1)
	cmdArmNode(e, fx)
	accepted := cmdDispatch(cmdOpsSession(e), fx.EdgeDeviceID, cmdActionReadRainfall,
		e.NS("SIM-CMD-005", "idem-online"), nil, "", "").Expect(http.StatusAccepted)
	e.Evidence("SIM-CMD-005.accepted_after_online", accepted.BodyString())
}

// ---------------------------------------------------------------------------
// SIM-CMD-006 指令尝试记录可查询，含失败原因
// ---------------------------------------------------------------------------

func cmdRun006(e *harness.Env) {
	t := e.T
	fx := cmdProvisionNode(e, "SIM-CMD-006", true)
	cmdArmNode(e, fx)

	before := fx.Device.FrameSeq()
	resp := cmdDispatch(cmdOpsSession(e), fx.EdgeDeviceID, cmdActionReadRainfall,
		e.NS("SIM-CMD-006", "idem-attempt"), nil, "", "").Expect(http.StatusAccepted)
	commandID := resp.DataString("execution.command_id")
	if commandID == "" {
		t.Fatalf("下发未返回执行记录 ID：%s", resp.BodyString())
	}
	// 指令确实出网了（下面断言的失败是"设备没回"，不是"根本没发"）。
	cmdAwaitChannelCmd(e, fx, before, 20*time.Second)

	// 尝试记录可查询：列表里能看到这次尝试。
	list := e.Admin.Get(fmt.Sprintf("/api/v1/edge-devices/%d/operations", fx.EdgeDeviceID)).
		Expect(http.StatusOK)
	var executions []struct {
		CommandID string `json:"command_id"`
		ActionID  string `json:"action_id"`
		Status    string `json:"status"`
	}
	list.Decode(&executions)
	found := false
	for _, item := range executions {
		if item.CommandID == commandID && item.ActionID == cmdActionReadRainfall {
			found = true
		}
	}
	if !found {
		t.Fatalf("执行记录没有出现在设备操作列表里：%s", list.BodyString())
	}
	e.Evidence("SIM-CMD-006.attempt_visible", list.BodyString())

	// 失败原因可查询：仿真设备故意不回 Ack/Final，执行记录必须在 Deadline 之后
	// 以明确终态 + 原因收尾（RecoverExpired 每秒一轮；Deadline = 创建 + 2 分钟），
	// 绝不能永远停在"已下发"这种无法解释的状态。
	var final struct {
		Status      string `json:"status"`
		FinalReason string `json:"final_reason"`
		CompletedAt string `json:"completed_at"`
	}
	e.Eventually(190*time.Second, func() error {
		state := e.Admin.Get("/api/v1/device-operations/" + commandID).Expect(http.StatusOK)
		state.Decode(&final)
		if final.Status != "UNKNOWN" && final.Status != "FAILED" {
			return fmt.Errorf("执行记录状态 = %q，尚未进入失败终态", final.Status)
		}
		if strings.TrimSpace(final.FinalReason) == "" {
			return fmt.Errorf("执行记录进入终态 %q 却没有写明原因", final.Status)
		}
		return nil
	})
	if final.CompletedAt == "" {
		t.Fatalf("失败终态没有完成时间：%+v", final)
	}
	e.Evidence("SIM-CMD-006.failure_reason", final)

	// 物理侧对照：设备只收到过一次尝试（失败不是重试风暴造成的）。
	if got := cmdChannelCmdCount(fx); got != 1 {
		t.Fatalf("未应答的指令产生了 %d 条指令帧，期望 1 条（无新证据不得重复上物理线）", got)
	}
}
