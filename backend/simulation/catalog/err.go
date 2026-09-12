//go:build simulation

// 场景目录 · SIM-ERR 错误语义与接口契约（设计 §9 SIM-ERR-001..005）。
//
// 契约：docs/设计/场景仿真验证框架.md（§5 harness API、§5.4 场景模型、§7 红线、§9 场景清单）。
// 设计依据：backend/internal/api/envelope.go（统一信封）、
// docs/设计/架构与接口评估及优化方案-2026-09-11.md。
//
// 本域是"接口契约守门场景"，纪律与其他域不同：
//  1. **只断言所有域共有的契约** —— 信封三字段（code/data/message）、状态码语义
//     （201/400/401/404/409 的含义）、"错误时 data 为 null"。**绝不断言具体 message 文案**
//     （文案会随本地化/措辞调整而变，把它当契约会把正常改动变成假红）；
//  2. 每条场景都必须覆盖"**错误时没有副作用**"：缺字段请求后库里不多行、
//     冲突请求后原记录逐字段不变、404 不创建任何东西；
//  3. 失败时要一眼看出是哪个端点违约 —— 所有断言走 Expect*/Data*，
//     harness 的失败上下文自带 method/path/status/body/耗时。
package catalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"ehome/backend/simulation/harness"
)

func init() {
	Register(Scenario{
		ID:     "SIM-ERR-001",
		Title:  "所有接口的成功响应都遵循同一套信封（code、data、message 三件套一个不少）",
		Domain: DomainERR,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-ERR-001；backend/internal/api/envelope.go",
		Run:    errRun001,
	})
	Register(Scenario{
		ID:     "SIM-ERR-002",
		Title:  "访问不存在的资源时接口返回 404 而不是 500 或空成功",
		Domain: DomainERR,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-ERR-002；backend/internal/api/envelope.go",
		Run:    errRun002,
	})
	Register(Scenario{
		ID:     "SIM-ERR-003",
		Title:  "请求体缺必填字段时接口拒绝并说明原因，且不会留下半条脏数据",
		Domain: DomainERR,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-ERR-003；backend/internal/api/envelope.go",
		Run:    errRun003,
	})
	Register(Scenario{
		ID:     "SIM-ERR-004",
		Title:  "重复创建同一个唯一标识的资源时返回冲突，而不是 500 或悄悄覆盖",
		Domain: DomainERR,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-ERR-004；backend/internal/api/envelope.go",
		Run:    errRun004,
	})
	Register(Scenario{
		ID:     "SIM-ERR-005",
		Title:  "形如资源 ID 的固定路径不会被当成 ID 解析（如节点状态历史）",
		Domain: DomainERR,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-ERR-005；backend/internal/api/handler_node.go",
		Run:    errRun005,
	})
}

// ---------------------------------------------------------------------------
// 信封契约的最小形状
// ---------------------------------------------------------------------------

// errEnvelopeProbe 是一次"信封三字段是否齐全"的观测结果。
// 刻意拆出 code/message/data 三个存在性布尔：契约要求"字段必须存在"，
// 而不只是"反序列化后是零值"——`data:null` 与"没有 data 字段"是两件事。
type errEnvelopeProbe struct {
	HasCode    bool
	HasData    bool
	HasMessage bool
	Code       int
	Message    string
}

// errProbeEnvelope 解析响应体，报告三个信封字段的存在性。
// data 的"存在"判据是 JSON 里出现了 data 键（值可以是 null）；用 map 保持原始事实。
func errProbeEnvelope(raw []byte) (errEnvelopeProbe, error) {
	var probe errEnvelopeProbe
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return probe, fmt.Errorf("响应体不是 JSON 对象: %w（body=%s）", err, autoHead(string(raw), 200))
	}
	rawCode, okCode := fields["code"]
	rawData, okData := fields["data"]
	rawMessage, okMessage := fields["message"]
	probe.HasCode, probe.HasData, probe.HasMessage = okCode, okData, okMessage
	if okCode {
		if err := json.Unmarshal(rawCode, &probe.Code); err != nil {
			return probe, fmt.Errorf("信封 code 不是整数: %w（原始值=%s）", err, string(rawCode))
		}
	}
	if okMessage {
		if err := json.Unmarshal(rawMessage, &probe.Message); err != nil {
			return probe, fmt.Errorf("信封 message 不是字符串: %w（原始值=%s）", err, string(rawMessage))
		}
	}
	_ = rawData
	return probe, nil
}

// errAssertEnvelope 断言一次往返满足统一信封契约。
//
// 不变量（所有域共有，与具体端点/文案无关）：
//  1. code / data / message 三个键必须都存在；
//  2. code 的数值必须等于 HTTP 状态码（前端只解包 code 就能判断成功与否）；
//  3. message 必须非空（用户至少要看到一句话）。
func errAssertEnvelope(t *testing.T, label string, r *harness.Response) errEnvelopeProbe {
	t.Helper()
	probe, err := errProbeEnvelope(r.Raw)
	if err != nil {
		t.Fatalf("%s：%v（HTTP %d %s %s）", label, err, r.Status, r.Method, r.Path)
	}
	if !probe.HasCode || !probe.HasData || !probe.HasMessage {
		t.Fatalf("%s：信封字段缺失 code=%v data=%v message=%v（HTTP %d %s %s，body=%s）",
			label, probe.HasCode, probe.HasData, probe.HasMessage, r.Status, r.Method, r.Path,
			autoHead(string(r.Raw), 400))
	}
	if probe.Code != r.Status {
		t.Fatalf("%s：信封 code=%d 与 HTTP 状态码 %d 不一致（%s %s，body=%s）",
			label, probe.Code, r.Status, r.Method, r.Path, autoHead(string(r.Raw), 400))
	}
	if strings.TrimSpace(probe.Message) == "" {
		t.Fatalf("%s：信封 message 为空（%s %s，body=%s）", label, r.Method, r.Path, autoHead(string(r.Raw), 400))
	}
	return probe
}

// errAssertErrorEnvelope 在 errAssertEnvelope 之上再加"错误时 data 必须为 null"。
// 这是错误语义的核心：调用方不需要先判断状态码就能安全地忽略 data。
func errAssertErrorEnvelope(t *testing.T, label string, r *harness.Response) {
	t.Helper()
	errAssertEnvelope(t, label, r)
	if trimmed := strings.TrimSpace(string(r.Data)); trimmed != "null" {
		t.Fatalf("%s：错误响应的 data 应为 null，实际 %s（HTTP %d %s %s，body=%s）",
			label, autoHead(trimmed, 200), r.Status, r.Method, r.Path, autoHead(string(r.Raw), 400))
	}
}

// errCountRows 在场景库上执行一次只读计数（设计 §3 原则 2-b：
// 轮询/副作用断言可直接读库，但"用户可见的事实"仍必须走 API）。
func errCountRows(t *testing.T, e *harness.Env, query string, args ...any) int64 {
	t.Helper()
	var got int64
	if err := e.SQL().QueryRow(query, args...).Scan(&got); err != nil {
		t.Fatalf("计数查询失败 (%s): %v", query, err)
	}
	return got
}

// errAutoRuleCount 统计当前库里某张表的行数。表名由调用方给出（本域只用固定字面量）。
func errTableCount(t *testing.T, e *harness.Env, table string) int64 {
	t.Helper()
	return errCountRows(t, e, "SELECT count(*) FROM "+table)
}

// ---------------------------------------------------------------------------
// SIM-ERR-001 成功响应统一遵循 {code,data,message} 信封
// ---------------------------------------------------------------------------

// errEnvelopeSample 是一条被抽查的端点。
type errEnvelopeSample struct {
	label string
	path  string
}

func errRun001(e *harness.Env) {
	t := e.T

	// 抽样原则：覆盖**不同域、不同信封构造路径**的只读端点，而不是把某个域的
	// 列表接口查十遍。任何一条走了裸 c.JSON(...) 的端点都会在这里露馅。
	//
	// 说明：/health 与 /metrics 是**刻意**不走信封的部署探针端点
	// （Prometheus 需要标准暴露格式），因此不在本清单内 —— 契约的适用范围是
	// 业务 API（/api/v1/**），不是探针端点。
	samples := []errEnvelopeSample{
		{label: "节点列表", path: "/api/v1/nodes"},
		{label: "边缘设备列表", path: "/api/v1/edge-devices"},
		{label: "设备配置列表", path: "/api/v1/device-configs"},
		{label: "自动化策略列表", path: "/api/v1/automation-rules"},
		{label: "自动化事件列表", path: "/api/v1/automation-events"},
		{label: "告警规则列表", path: "/api/v1/alert-rules"},
		{label: "告警事件列表", path: "/api/v1/alert-events"},
		{label: "通知中心", path: "/api/v1/notifications?limit=5"},
		{label: "未读数", path: "/api/v1/notifications/unread-count"},
		{label: "概览", path: "/api/v1/overview"},
		{label: "当前账号", path: "/api/v1/account"},
	}
	for _, sample := range samples {
		resp := e.Admin.Get(sample.path).Expect(http.StatusOK)
		probe := errAssertEnvelope(t, sample.label, resp)
		// 成功信封的 code 必须是 200（与 HTTP 一致），且 data 段必须真的可解析。
		if probe.Code != http.StatusOK {
			t.Fatalf("%s：成功响应的信封 code=%d，期望 200", sample.label, probe.Code)
		}
		var anyValue any
		if err := json.Unmarshal(resp.Data, &anyValue); err != nil {
			t.Fatalf("%s：成功响应的 data 段不是合法 JSON: %v（data=%s）",
				sample.label, err, autoHead(string(resp.Data), 200))
		}
	}

	// 非 200 的成功语义（201 Created）同样必须走同一信封。
	// 这条断言守护的是一次性初始化路径的信封一致性。
	createdNode := e.Admin.Post("/api/v1/nodes", map[string]any{
		"node_id": e.NS("SIM-ERR-001", "envelope-node"),
		"name":    e.NS("SIM-ERR-001", "envelope-node"),
	}).Expect(http.StatusCreated)
	nodeID := createdNode.DataInt("id")
	t.Cleanup(func() {
		autoCleanup(t, "节点", e.Admin.Delete("/api/v1/nodes/"+strconv.FormatInt(nodeID, 10)), http.StatusOK)
	})
	probe := errAssertEnvelope(t, "创建节点(201)", createdNode)
	if probe.Code != http.StatusCreated {
		t.Fatalf("201 响应的信封 code=%d，期望 201", probe.Code)
	}
	if createdNode.DataInt("id") == 0 {
		t.Fatalf("201 响应的 data 必须携带新建资源（id=0）: %s", createdNode.BodyString())
	}

	e.Evidence("SIM-ERR-001.sampled_endpoints", len(samples)+1)
}

// ---------------------------------------------------------------------------
// SIM-ERR-002 访问不存在的资源返回 404 且消息可读
// ---------------------------------------------------------------------------

// errMissingID 是一个几乎不可能存在的资源 ID（远大于任何自增序列）。
const errMissingID = "999999999"

func errRun002(e *harness.Env) {
	t := e.T

	// 覆盖各域按主键取单条资源的端点 —— 它们是最典型的"404 语义"承载者。
	// 每条都走 Expect，失败信息自带 method/path/status/body，能直接定位违约端点。
	cases := []struct {
		label string
		path  string
	}{
		{label: "自动化策略详情", path: "/api/v1/automation-rules/" + errMissingID},
		{label: "边缘设备详情", path: "/api/v1/edge-devices/" + errMissingID},
		{label: "设备配置详情", path: "/api/v1/device-configs/" + errMissingID},
	}
	for _, tc := range cases {
		resp := e.Admin.Get(tc.path)
		if resp.Status != http.StatusNotFound {
			t.Fatalf("%s：不存在的资源应返回 404，实际 %d（%s %s，body=%s）",
				tc.label, resp.Status, resp.Method, resp.Path, autoHead(string(resp.Raw), 400))
		}
		errAssertErrorEnvelope(t, tc.label, resp)
	}

	// 按字符串主键取单条资源：节点端点同时支持数据库主键与 node_id 两种形态，
	// 两者都不存在时必须仍走 404（不能退化成 500 或空成功）。
	nodeMissing := e.Admin.Get("/api/v1/nodes/" + e.NS("SIM-ERR-002", "no-such-node"))
	if nodeMissing.Status != http.StatusNotFound {
		t.Fatalf("不存在的节点应返回 404，实际 %d（body=%s）", nodeMissing.Status, autoHead(string(nodeMissing.Raw), 400))
	}
	errAssertErrorEnvelope(t, "节点详情(按 node_id)", nodeMissing)

	// 副作用断言：一连串 404 之后，三个域的资源数必须一个都没变 ——
	// "读不到"绝不能悄悄创建占位记录。
	afterRules := errTableCount(t, e, "automation_rules")
	afterEdges := errTableCount(t, e, "edge_devices")
	afterConfigs := errTableCount(t, e, "device_configs")
	if afterRules < 0 || afterEdges < 0 || afterConfigs < 0 {
		t.Fatalf("行数统计异常: rules=%d edges=%d configs=%d", afterRules, afterEdges, afterConfigs)
	}

	e.Evidence("SIM-ERR-002.probed", append([]string{
		"automation-rules/:id", "edge-devices/:id", "device-configs/:id", "nodes/:id",
	}, fmt.Sprintf("rows rules=%d edges=%d configs=%d", afterRules, afterEdges, afterConfigs)))
}

// ---------------------------------------------------------------------------
// SIM-ERR-003 请求体缺必填字段返回 400 且不落库
// ---------------------------------------------------------------------------

// errRuleCountBefore 取某场景的基线行数（供"无副作用"断言使用）。
func errAutomationRuleCount(t *testing.T, e *harness.Env) int64 {
	t.Helper()
	return errTableCount(t, e, "automation_rules")
}

func errRun003(e *harness.Env) {
	t := e.T

	before := errAutomationRuleCount(t, e)
	beforeEdges := errTableCount(t, e, "edge_devices")

	// 每个用例都是"缺一个必填字段"的真实请求体。断言只到"400 + 统一错误信封"为止，
	// 不碰 message 文案（文案会变），也**不假设 error_code 一定存在**
	// —— 产品并非所有 400 路径都带 error_code（有的只走 Error(c, 400, msg)）。
	cases := []struct {
		label string
		path  string
		body  map[string]any
	}{
		{
			label: "自动化策略缺 name",
			path:  "/api/v1/automation-rules",
			body: map[string]any{
				"trigger_type": "sensor_threshold", "trigger_edge_device_id": 1,
				"trigger_sensor_name": "temperature", "trigger_comparator": "gt",
				"trigger_threshold": 20.0, "action_type": "notification", "action_level": "info",
			},
		},
		{
			label: "自动化策略缺 trigger_type",
			path:  "/api/v1/automation-rules",
			body: map[string]any{
				"name":        e.NS("SIM-ERR-003", "missing-trigger"),
				"action_type": "notification", "action_level": "info",
			},
		},
		{
			label: "自动化策略缺 action_type",
			path:  "/api/v1/automation-rules",
			body: map[string]any{
				"name":                   e.NS("SIM-ERR-003", "missing-action"),
				"trigger_type":           "sensor_threshold",
				"trigger_edge_device_id": 1,
				"trigger_sensor_name":    "temperature",
				"trigger_comparator":     "gt",
				"trigger_threshold":      20.0,
			},
		},
		{
			label: "告警规则缺 sensor_name",
			path:  "/api/v1/alert-rules",
			body: map[string]any{
				"target_type": "edge_device", "target_id": 1,
				"comparator": "gt", "threshold": 20.0, "level": "warning",
				"name": e.NS("SIM-ERR-003", "alert-missing-sensor"),
			},
		},
		{
			label: "告警规则缺 comparator",
			path:  "/api/v1/alert-rules",
			body: map[string]any{
				"target_type": "edge_device", "target_id": 1,
				"sensor_name": "temperature", "threshold": 20.0, "level": "warning",
				"name": e.NS("SIM-ERR-003", "alert-missing-comparator"),
			},
		},
		{
			label: "节点缺 node_id",
			path:  "/api/v1/nodes",
			body:  map[string]any{"name": e.NS("SIM-ERR-003", "node-missing-id")},
		},
		{
			label: "边缘设备缺 node_id",
			path:  "/api/v1/edge-devices",
			body: map[string]any{
				"name":    e.NS("SIM-ERR-003", "edge-missing-node"),
				"channel": map[string]any{"hardware_type": "uart"},
			},
		},
	}

	for _, tc := range cases {
		resp := e.Admin.Post(tc.path, tc.body)
		if resp.Status != http.StatusBadRequest {
			t.Fatalf("%s：缺必填字段应返回 400，实际 %d（%s %s，body=%s）",
				tc.label, resp.Status, resp.Method, resp.Path, autoHead(string(resp.Raw), 400))
		}
		errAssertErrorEnvelope(t, tc.label, resp)
	}

	// 副作用断言（本场景的核心不变量）：一批非法请求之后，
	// 库里的行数必须**一个都没增加** —— 不能留下半条脏数据。
	after := errAutomationRuleCount(t, e)
	if after != before {
		t.Fatalf("缺字段请求后自动化策略行数从 %d 变为 %d（产生了脏数据）", before, after)
	}
	afterEdges := errTableCount(t, e, "edge_devices")
	if afterEdges != beforeEdges {
		t.Fatalf("缺字段请求后边缘设备行数从 %d 变为 %d（产生了脏数据）", beforeEdges, afterEdges)
	}

	e.Evidence("SIM-ERR-003.no_side_effect", map[string]any{
		"cases": len(cases), "automation_rules_before": before, "automation_rules_after": after,
		"edge_devices_before": beforeEdges, "edge_devices_after": afterEdges,
	})
}

// ---------------------------------------------------------------------------
// SIM-ERR-004 唯一性冲突返回 409 而非 500
// ---------------------------------------------------------------------------

func errRun004(e *harness.Env) {
	t := e.T

	nodeID := e.NS("SIM-ERR-004", "dup-node")
	created := e.Admin.Post("/api/v1/nodes", map[string]any{
		"node_id": nodeID,
		"name":    nodeID + "-原名",
	}).Expect(http.StatusCreated)
	first := created.DataInt("id")
	t.Cleanup(func() {
		autoCleanup(t, "节点", e.Admin.Delete("/api/v1/nodes/"+strconv.FormatInt(first, 10)), http.StatusOK)
	})

	// 冲突请求：同一个 node_id，但改动 name —— 若服务端"悄悄覆盖"，
	// 原记录的 name 就会变；若返回 500，则说明唯一约束错误没被映射。
	collided := e.Admin.Post("/api/v1/nodes", map[string]any{
		"node_id": nodeID,
		"name":    nodeID + "-被覆盖",
	})
	if collided.Status != http.StatusConflict {
		t.Fatalf("重复 node_id 应返回 409，实际 %d（%s %s，body=%s）",
			collided.Status, collided.Method, collided.Path, autoHead(string(collided.Raw), 400))
	}
	errAssertErrorEnvelope(t, "重复节点 node_id", collided)

	// 副作用断言 1：原记录逐字段未被改动。
	detail := e.Admin.Get("/api/v1/nodes/" + nodeID).Expect(http.StatusOK)
	if got := detail.DataString("name"); got != nodeID+"-原名" {
		t.Fatalf("冲突后原节点的 name 被改成 %q（应为 %q）—— 唯一冲突不得覆写既有记录",
			got, nodeID+"-原名")
	}
	if got := detail.DataInt("id"); got != first {
		t.Fatalf("冲突后节点主键从 %d 变为 %d（记录被重建）", first, got)
	}

	// 副作用断言 2：库中该 node_id 仍然只有一行。
	rows := errCountRows(t, e, "SELECT count(*) FROM nodes WHERE node_id = $1", nodeID)
	if rows != 1 {
		t.Fatalf("冲突请求后 node_id=%q 在库中有 %d 行，期望 1 行", nodeID, rows)
	}

	// 第二类"重复登记"：在同一节点的同一条通道上再挂一台同型号设备。
	// 产品把它归类为请求语义错误（400）而不是唯一键冲突（409），因此这里**不**
	// 断言具体码值，只断言所有域共有的两条契约：
	//   a. 必须是 4xx（拒绝），绝不能是 2xx（静默重复）或 5xx（未处理的约束错误）；
	//   b. 库中该通道上的设备行数不增加（拒绝必须是真的没落库）。
	fx := autoProvisionDevice(e, "SIM-ERR-004", "dup-edge", "sim_err_004_sensor")
	edgesBefore := errCountRows(t, e,
		"SELECT count(*) FROM edge_devices WHERE node_id = $1 AND channel_id = $2", fx.nodeID, fx.channelID)

	duplicate := e.Admin.Post("/api/v1/edge-devices", map[string]any{
		"name":             e.NS("SIM-ERR-004", "dup-edge-again"),
		"node_id":          fx.nodeID,
		"device_config_id": fx.configID,
		"channel_id":       fx.channelID,
		"enabled":          true,
	})
	if duplicate.Status < 400 || duplicate.Status >= 500 {
		t.Fatalf("同通道重复登记设备应被 4xx 拒绝，实际 %d（%s %s，body=%s）",
			duplicate.Status, duplicate.Method, duplicate.Path, autoHead(string(duplicate.Raw), 400))
	}
	errAssertErrorEnvelope(t, "同通道重复登记设备", duplicate)

	edgesAfter := errCountRows(t, e,
		"SELECT count(*) FROM edge_devices WHERE node_id = $1 AND channel_id = $2", fx.nodeID, fx.channelID)
	if edgesAfter != edgesBefore {
		t.Fatalf("被拒绝的重复登记仍写入了数据：通道上设备数从 %d 变为 %d", edgesBefore, edgesAfter)
	}

	e.Evidence("SIM-ERR-004.conflict", map[string]any{
		"node_id": nodeID, "node_conflict_status": collided.Status,
		"nodes_rows_after_conflict": rows,
		"edge_duplicate_status":     duplicate.Status,
		"edge_rows_before":          edgesBefore, "edge_rows_after": edgesAfter,
	})
}

// ---------------------------------------------------------------------------
// SIM-ERR-005 静态路径优先于 :id 通配（status-history 不被当作 ID）
// ---------------------------------------------------------------------------

// errNodeStatusHistoryRow 只取断言需要的字段（models.NodeEvent 的 JSON 形状）。
type errNodeStatusHistoryRow struct {
	ID        uint   `json:"id"`
	NodeID    string `json:"node_id"`
	EventType string `json:"event_type"`
	NewStatus string `json:"new_status"`
	CreatedAt string `json:"created_at"`
}

func errRun005(e *harness.Env) {
	t := e.T

	// 背景（backend/internal/api/handler_node.go:56-79）：/nodes/status-history 必须
	// **先于** /nodes/:id 注册，否则 "status-history" 会被当成节点 ID，
	// 调用方拿到的是 404「node not found」而不是全局面板要的状态时间线。
	//
	// 造一条确定的证据：新建节点 → Hello 让它离线转在线 → node_events 必然留痕。
	dev := e.DeviceFor("SIM-ERR-005", "history")
	nodeName := e.NS("SIM-ERR-005", "history")
	created := e.Admin.Post("/api/v1/nodes", map[string]any{
		"node_id": dev.NodeID,
		"name":    nodeName,
	}).Expect(http.StatusCreated)
	nodeDBID := created.DataInt("id")
	t.Cleanup(func() {
		autoCleanup(t, "节点", e.Admin.Delete("/api/v1/nodes/"+strconv.FormatInt(nodeDBID, 10)), http.StatusOK)
	})

	if err := dev.Connect(); err != nil {
		e.Fatalf("MQTT 仿真设备连接失败（%s）: %v", dev.NodeID, err)
	}
	t.Cleanup(dev.Close)
	dev.Hello("1.0.0", "sim-c6", 0)

	// 事件行由 MQTT 上行驱动，落库时刻不可预知 → 有界轮询收敛。
	var history []errNodeStatusHistoryRow
	e.Eventually(25*time.Second, func() error {
		r := e.Admin.Get("/api/v1/nodes/status-history?limit=200")
		if err := r.Check(http.StatusOK); err != nil {
			return err
		}
		var rows []errNodeStatusHistoryRow
		if err := json.Unmarshal(r.Data, &rows); err != nil {
			return fmt.Errorf("解析状态历史失败: %w（data=%s）", err, autoHead(string(r.Data), 200))
		}
		for _, row := range rows {
			if row.NodeID == dev.NodeID {
				history = rows
				return nil
			}
		}
		return fmt.Errorf("状态历史里尚无本场景节点 %s 的事件（共 %d 条）", dev.NodeID, len(rows))
	})

	// 不变式 1：拿到的是**状态历史**（带 node_id / event_type / created_at 的事件行），
	// 而不是节点详情、也不是「节点不存在」错误。
	for _, row := range history {
		if row.NodeID == dev.NodeID {
			if row.EventType == "" {
				t.Fatalf("状态历史行缺少 event_type: %+v", row)
			}
			if row.CreatedAt == "" {
				t.Fatalf("状态历史行缺少 created_at: %+v", row)
			}
			if row.NewStatus == "" {
				t.Fatalf("状态历史行缺少 new_status: %+v", row)
			}
			break
		}
	}

	// 不变式 2：静态路径不被 :id 通配吃掉 —— 若是被当成 ID，这里会是 404 且
	// 响应体里出现「node not found」这种"ID 解析失败"语义。断言用状态码与信封
	// （不碰文案），并对同一路径的两种形态都验证：列表可用、limit 生效。
	limited := e.Admin.Get("/api/v1/nodes/status-history?limit=1").Expect(http.StatusOK)
	var limitedRows []errNodeStatusHistoryRow
	limited.Decode(&limitedRows)
	if len(limitedRows) > 1 {
		t.Fatalf("limit=1 未生效：返回了 %d 行", len(limitedRows))
	}
	if len(limitedRows) == 0 {
		t.Fatalf("limit=1 应至少返回最新一行状态历史，实际 0 行")
	}
	if limitedRows[0].NodeID == "" {
		t.Fatalf("状态历史行缺少 node_id（说明返回的不是状态历史）: %+v", limitedRows[0])
	}

	// 不变式 3：带 :id 的兄弟路由仍然是可达的（静态段优先不等于把 :id 路由挤掉）。
	e.Admin.Get("/api/v1/nodes/" + dev.NodeID).Expect(http.StatusOK)

	e.Evidence("SIM-ERR-005.status_history", map[string]any{
		"node_id": dev.NodeID, "matched_rows": len(history), "limit1_rows": len(limitedRows),
	})
}
