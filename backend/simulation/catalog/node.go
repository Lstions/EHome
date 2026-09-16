//go:build simulation

// SIM-NODE：节点接入与在线状态（设计 docs/设计/场景仿真验证框架.md §9）。
//
// 为什么这样写：本域全部断言都走"真实用户视角"的黑盒路径——真实 HTTP 调用
// + 真实 MQTT 上的二进制帧（复用 backend/pkg/frame 编解码），直连数据库只
// 用于界面看不到的持久化事实（设计 §3 原则 2）。因此本文件不复制任何服务端
// 接线，只扮演"一台 ESP32 节点 + 一位管理员"。
//
// 命名约定（设计 §4.1）：本文件是包 catalog 内的 NODE 域，所有包级声明一律
// 以域小写短名 node 开头，避免 13 个域文件在同一包级命名空间里撞名。
package catalog

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"ehome/backend/pkg/frame"
	"ehome/backend/simulation/harness"
)

// nodeDomain 是本文件注册的场景域。取值 = 设计 §6 表"前缀"列去掉 SIM-
// （门禁不变量：ID == "SIM-" + string(Domain) + "-" + NNN）。
const nodeDomain Domain = "NODE"

// 仿真节点的固件与协议版本。
//
// 协议版本由 harness.Device.Hello 固定按服务端的 ServerMaxProtocolVersion
// （"2.6"）上线——nodemgr.parseHello 对 protocol_version 做等值校验，不匹配
// 的 Hello 会被直接拒收。因此这里断言的是"协商结果"，而不是随便填的字符串。
const (
	nodeWireProtocolVersion = "2.6"
	nodeFirmwareVersion     = "2.6.0-sim"
	nodeModel               = "ESP32-C6"
)

func init() {
	Register(Scenario{
		ID:     "SIM-NODE-001",
		Title:  "新节点首次上电经二进制帧 Hello 完成握手并收到 HelloAck",
		Domain: nodeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-NODE；docs/设计/节点.md §Hello 握手",
		Run:    nodeScenario001,
	})
	Register(Scenario{
		ID:     "SIM-NODE-002",
		Title:  "同一节点重复 Hello 幂等，不产生重复节点记录",
		Domain: nodeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-NODE；docs/设计/节点.md §节点接入",
		Run:    nodeScenario002,
	})
	Register(Scenario{
		ID:     "SIM-NODE-003",
		Title:  "节点持续上报后节点列表展示为在线",
		Domain: nodeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-NODE；docs/设计/节点.md §在线状态",
		Run:    nodeScenario003,
	})
	Register(Scenario{
		ID:     "SIM-NODE-004",
		Title:  "节点停止上报后经离线判定转为离线",
		Domain: nodeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-NODE；docs/设计/节点.md §离线判定",
		Run:    nodeScenario004,
	})
	Register(Scenario{
		ID:     "SIM-NODE-005",
		Title:  "管理员下发 ping 后节点收到 Ping 帧（实测走 down 主题）",
		Domain: nodeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-NODE；docs/设计/节点.md §链路保活",
		Run:    nodeScenario005,
	})
	Register(Scenario{
		ID:     "SIM-NODE-006",
		Title:  "节点注销后从列表与详情中消失，节点接口不再可达其通道",
		Domain: nodeDomain,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-NODE；docs/设计/节点.md §注销",
		Run:    nodeScenario006,
	})
}

// ---------------------------------------------------------------------------
// 夹具（跨文件引用不违反 §4.1 的"声明"归属规则）
// ---------------------------------------------------------------------------

// nodeLocal 生成节点 ID 的局部名（harness 会加上本次运行的 RunID 前缀）。
//
// 为什么不能用 e.NS：nodes.node_id 是 varchar(32)，而 RunID 已占 17 个字符，
// e.NS 返回的 "sim-node-001-n1" 再加前缀就超长（harness.Env.Device 会直接拒绝）。
// 这里按 harness 的同款规则折叠出 "node-001-n1" 这样的短名。
func nodeLocal(scenarioID, suffix string) string {
	base := strings.ToLower(strings.TrimPrefix(strings.ToUpper(scenarioID), "SIM-"))
	if suffix == "" {
		return base
	}
	return base + "-" + suffix
}

// nodeCreate 用管理员会话创建节点（用户视角的"添加节点"）。
func nodeCreate(e *harness.Env, nodeID, name string) {
	e.T.Helper()
	e.Admin.Post("/api/v1/nodes", map[string]any{
		"node_id": nodeID,
		"name":    name,
	}).Expect(http.StatusCreated)
}

// nodeProvision 建好一个已上电的仿真节点：先注册真实节点记录，再连 MQTT。
// 顺序不能反：Hello 会 upsert 节点行，先建记录才能保证"用户添加的节点"就是
// 这台设备，且场景结束时能精确地把它删掉（设计 §5.6 自我清理）。
func nodeProvision(e *harness.Env, scenarioID, suffix, name string) *harness.Device {
	e.T.Helper()
	dev := e.Device(nodeLocal(scenarioID, suffix))
	nodeCreate(e, dev.NodeID, name)
	nodeCleanupDelete(e, "/api/v1/nodes/"+dev.NodeID)
	if err := dev.Connect(); err != nil {
		e.T.Fatalf("节点仿真器连接 MQTT 失败 node=%s: %v", dev.NodeID, err)
	}
	e.T.Cleanup(dev.Close)
	return dev
}

// nodeCleanupDelete 登记场景自清理（设计 §5.6）。
//
// 刻意容忍失败：断言提前失败时资源可能已被删掉，清理阶段的报错不应掩盖
// 场景里真正的首个失败，因此只记录日志。
func nodeCleanupDelete(e *harness.Env, path string) {
	e.T.Cleanup(func() {
		resp := e.Admin.Delete(path)
		if err := resp.Check(http.StatusOK); err != nil {
			e.T.Logf("清理 %s 未成功（断言提前失败时属正常残留）: %v", path, err)
		}
	})
}

// nodeHello 让节点上电握手，并断言确实收到 HelloAck（0x12）。
//
// 为什么断言而不是忽略返回值：握手帧缺失意味着"节点根本没被中心端承认"，
// 是必须暴露的真实缺陷；harness 的 Hello 在多次尝试后仍失败会直接 Fatal。
func nodeHello(e *harness.Env, dev *harness.Device, channelCount int) *frame.Decoder {
	e.T.Helper()
	ack := dev.Hello(nodeFirmwareVersion, nodeModel, channelCount)
	if ack == nil {
		e.T.Fatalf("Hello 未收到 HelloAck：握手帧缺失 node=%s", dev.NodeID)
	}
	if ack.MsgType() != frame.MsgHelloAck {
		e.T.Fatalf("握手响应类型错误 node=%s：期望 0x%02X(HelloAck)，实际 0x%02X(%s)",
			dev.NodeID, frame.MsgHelloAck, ack.MsgType(), frame.MsgTypeName(ack.MsgType()))
	}
	return ack
}

// nodeFields 把一个解码器里剩下的字段全部读出来（断言下行帧内容的依据）。
func nodeFields(dec *frame.Decoder) (map[uint8]*frame.Field, error) {
	fields := map[uint8]*frame.Field{}
	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			return fields, nil
		}
		if err != nil {
			return nil, err
		}
		fields[field.FieldNum] = field
	}
}

// nodeDecodeInto 解出信封 data 段（非致命）：轮询期间"数据还没到"是正常状态。
func nodeDecodeInto(resp *harness.Response, out any) error {
	if len(resp.Data) == 0 || string(resp.Data) == "null" {
		return fmt.Errorf("响应 data 为空: HTTP %d %s", resp.Status, resp.BodyString())
	}
	if err := json.Unmarshal(resp.Data, out); err != nil {
		return fmt.Errorf("解码 data 失败: %w（body=%s）", err, resp.BodyString())
	}
	return nil
}

// nodeDetail 是 GET /api/v1/nodes/:id 的 data 段子集。
type nodeDetail struct {
	ID              uint       `json:"id"`
	NodeID          string     `json:"node_id"`
	Name            string     `json:"name"`
	Model           string     `json:"model"`
	FirmwareVersion string     `json:"firmware_version"`
	ProtocolVersion string     `json:"protocol_version"`
	Status          string     `json:"status"`
	UptimeSeconds   uint32     `json:"uptime_seconds"`
	LastSeen        *time.Time `json:"last_seen"`
}

// nodeGet 读取节点详情；调用方负责断言状态码。
func nodeGet(e *harness.Env, nodeID string) (*harness.Response, *nodeDetail) {
	e.T.Helper()
	resp := e.Admin.Get("/api/v1/nodes/" + nodeID)
	if resp.Status != http.StatusOK {
		return resp, nil
	}
	var node nodeDetail
	resp.Decode(&node)
	return resp, &node
}

// nodeStatus 读取节点状态，供轮询断言使用（非致命）。
func nodeStatus(e *harness.Env, nodeID string) (string, error) {
	resp := e.Admin.Get("/api/v1/nodes/" + nodeID)
	if err := resp.Check(http.StatusOK); err != nil {
		return "", err
	}
	var node nodeDetail
	if err := nodeDecodeInto(resp, &node); err != nil {
		return "", err
	}
	return node.Status, nil
}

// nodeEvent 是 GET /api/v1/nodes/:id/status-history 的元素子集。
type nodeEvent struct {
	NodeID    string    `json:"node_id"`
	EventType string    `json:"event_type"`
	OldStatus string    `json:"old_status"`
	NewStatus string    `json:"new_status"`
	CreatedAt time.Time `json:"created_at"`
}

// nodeStatusHistory 读取某节点的状态变迁历史（真实持久化事实）。
//
// 端点形状：**裸数组**（已核对 handler_node.go:147-168 的
// v1.GET("/nodes/:id/status-history") 走 Success(c, events)）。
func nodeStatusHistory(e *harness.Env, nodeID string) []nodeEvent {
	e.T.Helper()
	return simBareListGet[nodeEvent](e, "/api/v1/nodes/"+nodeID+"/status-history")
}

// nodeListRows 统计节点列表里 node_id 匹配的行（真实 HTTP，不直连库）。
//
// 端点形状：**分页信封** {items,total,page,page_size}
// （handler_node.go:45-98，提交 ffdec935 改）。这里刻意读**全部页**：
// 列表默认 page_size=20 且 Order("id") 升序，本场景刚建的节点排在最后，
// 只读第一页会得到"节点不存在"的假红 —— 那是把分页缺陷换成另一种假象。
func nodeListRows(e *harness.Env, nodeID string) []nodeDetail {
	e.T.Helper()
	nodes := simListAll[nodeDetail](e, "/api/v1/nodes", "")
	var matched []nodeDetail
	for _, node := range nodes {
		if node.NodeID == nodeID {
			matched = append(matched, node)
		}
	}
	return matched
}

// nodeLatestPayload 是 GET /api/v1/nodes/:id/latest 的 data 段。
type nodeLatestPayload struct {
	NodeID string            `json:"node_id"`
	Values []nodeLatestValue `json:"values"`
}

// nodeLatestValue 是最新值条目。
type nodeLatestValue struct {
	ChannelID  uint      `json:"channel_id"`
	SensorName string    `json:"sensor_name"`
	Value      float64   `json:"value"`
	Unit       string    `json:"unit"`
	Timestamp  time.Time `json:"timestamp"`
}

// nodeCountOnlineEvents 统计状态历史里的 online 事件数。
func nodeCountOnlineEvents(events []nodeEvent) int {
	count := 0
	for _, evt := range events {
		if evt.EventType == "online" {
			count++
		}
	}
	return count
}

// ---------------------------------------------------------------------------
// SIM-NODE-001
// ---------------------------------------------------------------------------

// 守护的不变量：新节点第一次合法 Hello 必须得到 HelloAck（且 nonce 回显、
// 服务端时间可信），中心端据此把它记为在线并登记型号与协议版本。
func nodeScenario001(e *harness.Env) {
	dev := nodeProvision(e, "SIM-NODE-001", "n1", "首次上电节点")
	nodeID := dev.NodeID
	ack := nodeHello(e, dev, 0)

	fields, err := nodeFields(ack)
	if err != nil {
		e.T.Fatalf("解析 HelloAck 失败 node=%s: %v", nodeID, err)
	}
	serverTimeField, ok := fields[1]
	if !ok {
		e.T.Fatalf("HelloAck 缺少 server_time（字段 1）：节点无法完成对时")
	}
	serverTimeMs := int64(frame.GetUint64(serverTimeField))
	if serverTimeMs <= 0 {
		e.T.Fatalf("HelloAck server_time 非法: %d", serverTimeMs)
	}
	skew := time.Since(time.UnixMilli(serverTimeMs))
	if skew < -2*time.Minute || skew > 2*time.Minute {
		e.T.Fatalf("HelloAck server_time 与服务端时钟偏差过大: %s（server_time=%d）", skew, serverTimeMs)
	}
	// v2.6 握手关联：服务端必须回显本次 Hello 的 nonce。
	nonceField, ok := fields[frame.HelloAckFieldHandshakeNonce]
	if !ok {
		e.T.Fatalf("HelloAck 缺少握手 nonce（字段 %d）：无法证明这是本次握手的应答", frame.HelloAckFieldHandshakeNonce)
	}
	if nonce := uint32(frame.GetUint64(nonceField)); nonce != dev.LastHelloNonce() {
		e.T.Fatalf("HelloAck 回显的 nonce=%d，本次 Hello 的 nonce=%d", nonce, dev.LastHelloNonce())
	}

	// 握手结果必须可在用户界面看到。
	e.Eventually(15*time.Second, func() error {
		resp, node := nodeGet(e, nodeID)
		if node == nil {
			return fmt.Errorf("节点详情不可读: HTTP %d %s", resp.Status, resp.BodyString())
		}
		if node.Status != "online" {
			return fmt.Errorf("节点状态=%q，期望 online（配置同步中也不该是离线）", node.Status)
		}
		if node.Model != nodeModel {
			return fmt.Errorf("节点型号=%q，期望 %q", node.Model, nodeModel)
		}
		if node.FirmwareVersion != nodeFirmwareVersion {
			return fmt.Errorf("节点固件版本=%q，期望 %q", node.FirmwareVersion, nodeFirmwareVersion)
		}
		if node.ProtocolVersion != nodeWireProtocolVersion {
			return fmt.Errorf("协商协议版本=%q，期望 %q", node.ProtocolVersion, nodeWireProtocolVersion)
		}
		if node.LastSeen == nil {
			return fmt.Errorf("节点 last_seen 为空：握手未刷新在线时间")
		}
		return nil
	})

	if events := nodeStatusHistory(e, nodeID); nodeCountOnlineEvents(events) != 1 {
		e.T.Fatalf("首次握手的 online 事件数=%d，期望 1（status_history=%+v）", nodeCountOnlineEvents(events), events)
	}

	e.Evidence("hello_ack_server_time_ms", serverTimeMs)
	e.Evidence("hello_ack_nonce", dev.LastHelloNonce())
}

// ---------------------------------------------------------------------------
// SIM-NODE-002
// ---------------------------------------------------------------------------

// 守护的不变量：Hello 是幂等的状态刷新，不是"每次上电新增一台节点"。
// 重复 Hello 后节点表仍然只有一行，且不会刷出第二条 online 事件。
func nodeScenario002(e *harness.Env) {
	dev := nodeProvision(e, "SIM-NODE-002", "n1", "重复上电节点")
	nodeID := dev.NodeID
	nodeHello(e, dev, 0)

	var firstID uint
	e.Eventually(15*time.Second, func() error {
		_, node := nodeGet(e, nodeID)
		if node == nil {
			return fmt.Errorf("首次握手后节点详情不可读")
		}
		firstID = node.ID
		return nil
	})

	// 第二次握手：同一物理节点重启/重连。
	nodeHello(e, dev, 0)

	rows := nodeListRows(e, nodeID)
	if len(rows) != 1 {
		e.T.Fatalf("重复 Hello 后节点记录数=%d，期望 1（rows=%+v）", len(rows), rows)
	}
	if rows[0].ID != firstID {
		e.T.Fatalf("重复 Hello 后节点主键从 %d 变为 %d：产生了重复节点记录", firstID, rows[0].ID)
	}

	// 幂等判定 2：online→online 不是状态变迁，不应再写一条 online 事件。
	events := nodeStatusHistory(e, nodeID)
	if count := nodeCountOnlineEvents(events); count != 1 {
		e.T.Fatalf("重复 Hello 后 online 事件数=%d，期望 1（status_history=%+v）", count, events)
	}

	e.Evidence("node_rows", len(rows))
	e.Evidence("online_events", nodeCountOnlineEvents(events))
}

// ---------------------------------------------------------------------------
// SIM-NODE-003
// ---------------------------------------------------------------------------

// 守护的不变量：Hello + 持续 StatusReport/DataReport 之后，节点在用户界面
// 上必须是"在线"，心跳字段跟着设备上报走；"持续上报"也确实产生了数据。
func nodeScenario003(e *harness.Env) {
	fixture := dataProvision(e, "SIM-NODE-003", "n1")
	nodeID := fixture.NodeID

	const reportedUptime = 4242
	if err := fixture.Node.StatusReport(reportedUptime, "online", -55); err != nil {
		e.T.Fatalf("节点上报 StatusReport 失败 node=%s: %v", nodeID, err)
	}
	if err := fixture.Report(harness.FixtureTempCategory, 25.1); err != nil {
		e.T.Fatalf("节点上报 DataReport 失败 node=%s: %v", nodeID, err)
	}
	// 最新值接口在夹具哨兵上报后就非空，因此必须等到"本次上报的值"出现，
	// 否则断言会命中哨兵值（21.37）而给出假绿。
	e.Eventually(30*time.Second, func() error {
		resp := e.Admin.Get("/api/v1/nodes/" + nodeID + "/latest")
		if err := resp.Check(http.StatusOK); err != nil {
			return err
		}
		var latest nodeLatestPayload
		if err := nodeDecodeInto(resp, &latest); err != nil {
			return err
		}
		for _, value := range latest.Values {
			if value.SensorName == harness.FixtureTempCategory && math.Abs(value.Value-25.1) < 0.005 {
				return nil
			}
		}
		return fmt.Errorf("最新值尚未收敛到 25.1: %+v", latest.Values)
	})

	e.Eventually(30*time.Second, func() error {
		_, node := nodeGet(e, nodeID)
		if node == nil {
			return fmt.Errorf("节点详情不可读")
		}
		if node.Status != "online" {
			return fmt.Errorf("节点状态=%q，期望 online", node.Status)
		}
		if node.LastSeen == nil {
			return fmt.Errorf("节点 last_seen 为空：持续上报未刷新在线时间")
		}
		if node.UptimeSeconds != reportedUptime {
			return fmt.Errorf("节点 uptime_seconds=%d，期望 %d（未按设备上报刷新）", node.UptimeSeconds, reportedUptime)
		}
		return nil
	})

	// 节点列表（用户真正看的那个列表）同样必须显示在线。
	rows := nodeListRows(e, nodeID)
	if len(rows) != 1 {
		e.T.Fatalf("节点列表中该节点行数=%d，期望 1", len(rows))
	}
	if rows[0].Status != "online" {
		e.T.Fatalf("节点列表状态=%q，期望 online", rows[0].Status)
	}

	// "持续上报"必须真的产生了数据：最新值接口能读到解析后的物理量。
	var payload nodeLatestPayload
	e.Eventually(30*time.Second, func() error {
		resp := e.Admin.Get("/api/v1/nodes/" + nodeID + "/latest")
		if err := resp.Check(http.StatusOK); err != nil {
			return err
		}
		var latest nodeLatestPayload
		if err := nodeDecodeInto(resp, &latest); err != nil {
			return err
		}
		if len(latest.Values) == 0 {
			return fmt.Errorf("最新值接口为空：上报的数据没有进入数据链路")
		}
		payload = latest
		return nil
	})
	const reportedTemperature = 25.1
	found := false
	for _, value := range payload.Values {
		if value.SensorName == harness.FixtureTempCategory && value.ChannelID == fixture.ChannelID &&
			math.Abs(value.Value-reportedTemperature) < 0.005 {
			found = true
		}
	}
	if !found {
		e.T.Fatalf("最新值里没有夹具节点的通道数据（期望温度 %.1f）: %+v", reportedTemperature, payload.Values)
	}

	e.Evidence("uptime_seconds", reportedUptime)
	e.Evidence("list_status", rows[0].Status)
	e.Evidence("latest_values", len(payload.Values))
}

// ---------------------------------------------------------------------------
// SIM-NODE-004
// ---------------------------------------------------------------------------

// 守护的不变量：节点静默后必须由离线检测器翻成 offline，并留下
// online → offline 的状态事件。
//
// 关于时长：离线判定阈值是 last_seen 超过 90s（backend/internal/offlinedetector
// 的 checkDBLastSeen），检测 ticker 5s，因此真实耗时约 90~95s。这里把等待上限
// 放大到 150s —— 不为"让用例变快"去改生产阈值。
func nodeScenario004(e *harness.Env) {
	dev := nodeProvision(e, "SIM-NODE-004", "n1", "静默节点")
	nodeID := dev.NodeID
	nodeHello(e, dev, 0)

	// 前置：先确实进入在线态，否则"转离线"无从谈起。
	e.Eventually(20*time.Second, func() error {
		status, err := nodeStatus(e, nodeID)
		if err != nil {
			return err
		}
		if status != "online" {
			return fmt.Errorf("节点状态=%q，期望 online", status)
		}
		return nil
	})
	onlineAt := time.Now()

	// 此后节点完全静默：不再有任何 Hello/StatusReport/DataReport。
	e.Eventually(150*time.Second, func() error {
		status, err := nodeStatus(e, nodeID)
		if err != nil {
			return err
		}
		if status != "offline" {
			return fmt.Errorf("节点状态=%q，期望 offline", status)
		}
		return nil
	})
	silence := time.Since(onlineAt)
	if silence < 90*time.Second {
		e.T.Fatalf("静默 %s 就判定离线，短于节点设计规定的 90s 阈值：判定被削弱", silence)
	}

	events := nodeStatusHistory(e, nodeID)
	var offline *nodeEvent
	for i := range events {
		if events[i].EventType == "offline" {
			offline = &events[i]
			break
		}
	}
	if offline == nil {
		e.T.Fatalf("离线后没有 offline 状态事件（status_history=%+v）", events)
	}
	if offline.OldStatus != "online" || offline.NewStatus != "offline" {
		e.T.Fatalf("offline 事件状态迁移非法: %s → %s", offline.OldStatus, offline.NewStatus)
	}

	e.Evidence("silence_to_offline", silence.String())
	e.Evidence("offline_event", fmt.Sprintf("%s → %s", offline.OldStatus, offline.NewStatus))
}

// ---------------------------------------------------------------------------
// SIM-NODE-005
// ---------------------------------------------------------------------------

// 守护的不变量：管理员按下的 ping 必须真的到了设备上，而不是接口返回
// "ping sent" 就算完。
//
// 为什么用时间戳判定：Ping 帧（0x08）字段 1 是服务端在"发送那一刻"生成的
// 微秒时间戳（nodemgr/sender.go SendPing）。Hello 之后服务端还会自动补发一次
// ping，所以不能只看"收到了 Ping 帧"，必须要求帧内时间戳不早于本次 API 调用。
//
// 与设计 §9 的偏差（以真实行为为准）：Ping 帧实测发到 nodes/<id>/down 主题
// （sender.go 用 mqtt.TopicForNode），而不是标题里写的 control 主题。
func nodeScenario005(e *harness.Env) {
	dev := nodeProvision(e, "SIM-NODE-005", "n1", "保活验证节点")
	nodeID := dev.NodeID
	nodeHello(e, dev, 0)

	// 游标取在"下发之前"：握手还会异步补发一次 ping，只检查下发之后新增的帧。
	cursor := len(dev.FramesOf(frame.MsgPing))
	sentAfterMicros := time.Now().UnixMicro()
	e.Admin.Post("/api/v1/nodes/"+nodeID+"/ping", nil).Expect(http.StatusOK)

	var observed []int64
	accepted := false
	e.Eventually(30*time.Second, func() error {
		frames := dev.FramesOf(frame.MsgPing)
		for cursor < len(frames) {
			raw := frames[cursor].Raw
			cursor++
			fields, err := harness.DecodeFrameFields(raw)
			if err != nil {
				return fmt.Errorf("解析 Ping 帧失败: %w", err)
			}
			field, ok := fields[1]
			if !ok {
				return fmt.Errorf("Ping 帧缺少时间戳字段 1：无法校验下发时刻")
			}
			ts := int64(frame.GetUint64(&field))
			observed = append(observed, ts)
			if ts >= sentAfterMicros {
				accepted = true
				return nil
			}
		}
		return fmt.Errorf("尚无下发时刻之后的 Ping 帧（已观察 %v，下发时刻 %d）", observed, sentAfterMicros)
	})
	if !accepted {
		e.T.Fatalf("观察到的 Ping 帧时间戳 %v 全部早于下发时刻 %d：管理员 ping 没有到设备", observed, sentAfterMicros)
	}

	// 时间戳必须是合理的时间点（微秒），不是 0 或随意值。
	if last := observed[len(observed)-1]; math.Abs(float64(time.Now().UnixMicro()-last)) > float64(5*time.Minute/time.Microsecond) {
		e.T.Fatalf("Ping 帧时间戳与当前时钟偏差过大: %d", last)
	}

	e.Evidence("ping_frames_observed", observed)
	e.Evidence("ping_sent_after_micros", sentAfterMicros)
}

// ---------------------------------------------------------------------------
// SIM-NODE-006
// ---------------------------------------------------------------------------

// 守护的不变量：注销节点后，用户在界面上再也看不到它——列表里没有、详情
// 404、节点作用域的通道/数据接口也一并 404。
//
// 已核实的行为缺口（本场景不把缺陷当契约，只作为证据留痕）：
// DELETE /nodes/:id 只软删 nodes 行，不清理该节点的 channels/edge_devices；
// 之后旧通道上的上报仍会被 SensorParserConsumer 写进 unified_data。
// 证据通过 e.Evidence 落入 summary.json，供主控复核。
func nodeScenario006(e *harness.Env) {
	fixture := dataProvision(e, "SIM-NODE-006", "n1")
	nodeID := fixture.NodeID
	nodeCleanupDelete(e, "/api/v1/nodes/"+nodeID)

	if err := fixture.Report(harness.FixtureTempCategory, 31.1); err != nil {
		e.T.Fatalf("节点上报失败 node=%s: %v", nodeID, err)
	}
	// 先确认真有数据落库，删除后的对照才有意义（只读计数，用于轮询收敛）。
	fixture.AwaitRows(harness.FixtureTempCategory, 2, 30*time.Second)
	before, err := fixture.CountRows(harness.FixtureTempCategory)
	if err != nil {
		e.T.Fatalf("统计删除前落库行数失败: %v", err)
	}

	e.Admin.Delete("/api/v1/nodes/" + nodeID).Expect(http.StatusOK)

	// 注销后的用户可见事实。
	e.Admin.Get("/api/v1/nodes/" + nodeID).Expect(http.StatusNotFound)
	if rows := nodeListRows(e, nodeID); len(rows) != 0 {
		e.T.Fatalf("节点已注销但仍在列表中: %+v", rows)
	}
	e.Admin.Get("/api/v1/nodes/" + nodeID + "/channels").Expect(http.StatusNotFound)
	e.Admin.Get("/api/v1/nodes/" + nodeID + "/data").Expect(http.StatusNotFound)
	e.Admin.Get("/api/v1/nodes/" + nodeID + "/status-history").Expect(http.StatusNotFound)

	// 残留行为取证：对已注销节点的旧通道再报一帧，看数据是否仍然入库。
	if err := fixture.Report(harness.FixtureTempCategory, 32.2); err != nil {
		e.T.Logf("节点注销后再次上报失败（设备侧）：%v", err)
	}
	grew := e.EventuallyError(8*time.Second, func() error {
		current, err := fixture.CountRows(harness.FixtureTempCategory)
		if err != nil {
			return err
		}
		if current > before {
			return nil
		}
		return fmt.Errorf("落库行数仍为 %d", current)
	}) == nil
	after, err := fixture.CountRows(harness.FixtureTempCategory)
	if err != nil {
		e.T.Fatalf("统计删除后落库行数失败: %v", err)
	}
	e.Evidence("rows_before_delete", before)
	e.Evidence("rows_after_delete_and_report", after)
	if grew {
		e.T.Logf("已核实的缺陷：节点注销后旧通道上报仍写入历史（%d → %d 行）。"+
			"DELETE /nodes/:id 只软删 nodes 行，未清理 channels/edge_devices；"+
			"本场景只把该行为作为证据留痕，不把它当作契约断言。", before, after)
	}
}
