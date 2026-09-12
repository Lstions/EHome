//go:build simulation

// 场景目录 · SIM-RT 实时推送与运行观测（设计 §9 SIM-RT-001..005）。
//
// 契约：docs/设计/场景仿真验证框架.md（§5 harness API、§5.4 场景模型、§7 红线、§9 场景清单）。
// 纪律：
//   - 只用设计冻结的 harness API 与产品真实 HTTP/MQTT/WS 端点；
//   - 断言走 Expect*/Data*/Decode，时序断言一律走 Eventually/Await*（禁止 sleep 后直接断言）；
//   - 前置数据一律走真实 API/MQTT（harness.ProvisionSimpleDevice 共享夹具），不直连数据库造数。
package catalog

import (
	"bytes"
	"compress/gzip"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/simulation/harness"

	"github.com/gorilla/websocket"
)

// 域标识直接使用 catalog.go 冻结的常量（设计 v1.1 §6 表短名）：
// 不变量 ID == "SIM-" + string(Domain) + "-" + NNN 由 TestCatalogGate 校验。

func init() {
	Register(Scenario{
		ID:     "SIM-RT-001",
		Title:  "管理员打开实时看板后，节点刚上报的数据会立刻推到页面上",
		Domain: DomainRT,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-RT-001",
		Run:    rtRun001,
	})
	Register(Scenario{
		ID:     "SIM-RT-002",
		Title:  "没带令牌或令牌不对的人连不上实时推送通道",
		Domain: DomainRT,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-RT-002",
		Run:    rtRun002,
	})
	Register(Scenario{
		ID:     "SIM-RT-003",
		Title:  "首页概览能看到节点数、设备数以及设备最新的数据",
		Domain: DomainRT,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-RT-003",
		Run:    rtRun003,
	})
	Register(Scenario{
		ID:     "SIM-RT-004",
		Title:  "监控系统能抓到本系统的运行指标，且指标文本格式规范可解析",
		Domain: DomainRT,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-RT-004",
		Run:    rtRun004,
	})
	Register(Scenario{
		ID:     "SIM-RT-005",
		Title:  "在通知中心标记已读后，未读数字会相应减少",
		Domain: DomainRT,
		Doc:    "docs/设计/场景仿真验证框架.md §9 SIM-RT-005",
		Run:    rtRun005,
	})
}

// ---------------------------------------------------------------------------
// SIM-RT-001 WebSocket 连接后收到节点数据更新推送
// ---------------------------------------------------------------------------

func rtRun001(e *harness.Env) {
	t := e.T

	fx := rtProvisionFixture(e, "SIM-RT-001", "reader")

	// 本场景上报的哨兵值：夹具自检用的是 21.37、类别初始值是 0，
	// 23.5 只可能来自下面这一次上报，因此能作为"这条推送就是我刚发的"的判据。
	const reportedTemp = 23.5

	// 必须先连上推送通道、再让设备上报：本场景证明的是"刚上报的数据会被推送"，
	// 先连后报才能把"推送发生了"与"只是后来查得到"区分开。
	conn, err := e.DialWSConn("/api/v1/ws")
	if err != nil {
		t.Fatalf("WebSocket 拨号失败（%s）: %v", e.Admin.BaseURL+"/api/v1/ws", err)
	}
	t.Cleanup(conn.Close)

	// 真实上行：MQTT nodes/<node_id>/up 发一帧 DataReport（0x03），
	// 由 nodemgr → DataEventBus → SensorParserConsumer → Hub.BroadcastEvent 推向页面。
	if err := fx.Report(harness.FixtureTempCategory, reportedTemp); err != nil {
		t.Fatalf("节点上报数据失败: %v", err)
	}

	// 只认"本场景那台设备 + 本次上报的那个值"的事件。
	//
	// 为什么必须带上取值条件：夹具的哨兵上报(21.37)与本次上报走的是同一条异步消费链，
	// SensorParserConsumer 先写库、后广播；夹具自检一看到库里有行就返回并连上 WS，
	// 此时哨兵那一次的广播可能还没发出，于是它会作为"本设备的第一个 data_update"抵达。
	// 只按 edge_device_id 过滤就会把这条**连接之前产生的**推送误当成"连上之后收到的"，
	// 断言随之失效（实测正是如此）。加上取值条件后，能匹配的只可能是本次上报的推送，
	// 因此这条断言反而比"收到任意一条 data_update"更强。
	// 注意 predicate 收到的是整个事件信封（{type,payload}），不是 payload 本身。
	event, err := conn.AwaitEventWhere(events.DataUpdate, 20*time.Second, func(candidate map[string]any) bool {
		inner, ok := candidate["payload"].(map[string]any)
		if !ok || rtJSONNumber(inner, "edge_device_id") != float64(fx.EdgeDeviceID) {
			return false
		}
		data, ok := inner["data"].(map[string]any)
		if !ok {
			return false
		}
		return data[harness.FixtureTempCategory] == float64(reportedTemp)
	})
	if err != nil {
		t.Fatalf("未收到本场景设备（edge_device_id=%d）携带刚上报值 %v 的 %s 推送: %v",
			fx.EdgeDeviceID, reportedTemp, events.DataUpdate, err)
	}
	payload, ok := event["payload"].(map[string]any)
	if !ok {
		t.Fatalf("推送事件缺少 payload 对象: %v", event)
	}

	// 不变式：推送必须能定位到"哪个节点的哪台设备"以及刚上报的数值本身，
	// 否则看板只能显示一个没有归属的数字。
	// node_id 与 channel_data / REST 全站一致：字符串节点 ID（nodes.node_id），
	// 不是数据库数值主键。历史上此处曾是数值主键，与同一函数广播的 channel_data 语义冲突。
	if got, ok := payload["node_id"].(string); !ok || got != fx.NodeID {
		t.Fatalf("推送里的 node_id = %v (%T)，期望字符串节点 ID %q（payload=%v）",
			payload["node_id"], payload["node_id"], fx.NodeID, payload)
	}
	if got := rtJSONNumber(payload, "edge_device_id"); got != float64(fx.EdgeDeviceID) {
		t.Fatalf("推送里的 edge_device_id = %v，期望 %d（payload=%v）", got, fx.EdgeDeviceID, payload)
	}
	data, ok := payload["data"].(map[string]any)
	if !ok {
		t.Fatalf("推送里缺少 data 对象（payload=%v）", payload)
	}
	if got := data[harness.FixtureTempCategory]; got != float64(reportedTemp) {
		t.Fatalf("推送里的 %s = %v，期望 %v（payload=%v）",
			harness.FixtureTempCategory, got, reportedTemp, payload)
	}
	e.Evidence("SIM-RT-001.data_update", payload)
}

// ---------------------------------------------------------------------------
// SIM-RT-002 未携带有效令牌的 WebSocket 连接被拒绝
// ---------------------------------------------------------------------------

func rtRun002(e *harness.Env) {
	t := e.T

	// 形态一（harness 原生路径）：匿名会话没有令牌，DialWS 会返回带握手状态码的
	// WSDialError —— 这条断言直接证明"未鉴权被 401 拒绝"，而不是模糊的"连接失败"。
	_, err := e.NewSession().DialWSConn("/api/v1/ws")
	if err == nil {
		t.Fatalf("匿名会话竟然成功连上了实时推送通道（%s）", "/api/v1/ws")
	}
	var dialErr *harness.WSDialError
	if !errors.As(err, &dialErr) {
		t.Fatalf("匿名 WebSocket 拨号返回的错误类型非预期: %T %v", err, err)
	}
	if dialErr.Status != http.StatusUnauthorized {
		t.Fatalf("匿名 WebSocket 拨号应被 401 拒绝，实际 status=%d err=%v", dialErr.Status, err)
	}
	e.Evidence("SIM-RT-002.anonymous_dial", dialErr.Error())

	// 形态二：伪造/缺失各种凭据的裸拨号。
	// 鉴权发生在路由层（JWTAuthWithDB），因此预期是握手 401；
	// 但"升级成功后被服务端立即关闭"同样是拒绝的合法形态，两种都要能识别。
	cases := []struct {
		name   string
		path   string
		header http.Header
	}{
		{name: "完全不带令牌", path: "/api/v1/ws", header: nil},
		{name: "查询串令牌非法", path: "/api/v1/ws?token=not-a-jwt", header: nil},
		{name: "请求头令牌非法", path: "/api/v1/ws", header: http.Header{"Authorization": []string{"Bearer not-a-jwt"}}},
	}
	for _, tc := range cases {
		conn, resp, err := rtDialWSRaw(e, tc.path, tc.header)
		rtAssertWSRejected(t, "SIM-RT-002 "+tc.name, conn, resp, err)
	}
	e.Evidence("SIM-RT-002.rejected_cases", len(cases))
}

// ---------------------------------------------------------------------------
// SIM-RT-003 概览接口返回节点/设备/数据量统计
// ---------------------------------------------------------------------------

// rtOverviewSnapshot 只取本场景断言需要的字段（信封 data 的形状见 handler_overview.go）。
type rtOverviewSnapshot struct {
	Nodes struct {
		Total   int64 `json:"total"`
		Online  int64 `json:"online"`
		Offline int64 `json:"offline"`
	} `json:"nodes"`
	EdgeDevices struct {
		Total   int64 `json:"total"`
		Online  int64 `json:"online"`
		Offline int64 `json:"offline"`
	} `json:"edge_devices"`
	LatestData []struct {
		DeviceID    uint               `json:"device_id"`
		DeviceName  string             `json:"device_name"`
		NodeName    string             `json:"node_name"`
		Data        map[string]float64 `json:"data"`
		CollectedAt string             `json:"collected_at"`
	} `json:"latest_data"`
}

func rtRun003(e *harness.Env) {
	t := e.T

	fx := rtProvisionFixture(e, "SIM-RT-003", "meter")
	// 一帧同时上报两个物理量，且取值都是本场景独有的（夹具自检的哨兵是 21.37、
	// 另一个类别的初始值是 0），因此下面"概览里的数值必须是刚上报的"才有鉴别力。
	expected := map[string]float64{
		harness.FixtureTempCategory:  18.75,
		harness.FixtureLevelCategory: 4242,
	}
	if err := fx.ReportMany(expected); err != nil {
		t.Fatalf("节点上报数据失败: %v", err)
	}

	// 本场景的节点必须真的落进了权威列表（/api/v1/nodes 直接查库、没有缓存）。
	// 概览的 nodes 段只有聚合数字、没有 node_id 列表，所以"概览统计到了我这个节点"
	// 是通过"概览总数 == 权威列表长度"来传递证明的（见下方 Eventually）。
	authoritativeNodeIDs, authoritativeEdgeIDs := rtLiveInventory(t, e)
	if !rtContainsString(authoritativeNodeIDs, fx.NodeID) {
		t.Fatalf("新建节点 %s 未出现在 GET /api/v1/nodes（共 %d 个）: %v",
			fx.NodeID, len(authoritativeNodeIDs), authoritativeNodeIDs)
	}

	// 先读一次概览：无论这一读是命中缓存还是穿透到库，都会把缓存时间戳刷新到"此刻"。
	// 这一步刻意不参与任何计数断言 —— 见下方说明。
	e.Admin.Get("/api/v1/overview").Expect(http.StatusOK)

	// ── 为什么不用"总数 = 基线 + 1" ──
	// /overview 有进程级 30s 缓存（handler_overview.go 的 C2 fix）。取基线的那一读
	// 很可能命中缓存里的陈旧快照（服务启动早期、或上一个场景删节点之后未刷新的值），
	// 而之后的读取又会读到真实的当前值 —— 两个读数跨越了不同的缓存窗口，
	// "基线 + 1"这个等式从一开始就可能不成立，与产品是否正确无关。
	//
	// 正确的做法：用**权威接口**（GET /api/v1/nodes、GET /api/v1/edge-devices，
	// 二者都直接查库、无缓存）作为对照物，等到概览的某一次读取确实穿透到库里，
	// 断言两者一致。上面那一次读取已把缓存时间戳定在此刻，因此最多再等
	// 一个 TTL(30s) + 余量，必然出现一次穿透读取；穿透时算出来的就是当前真值。
	//
	// 这条断言捕捉的真实故障："概览的节点/设备统计没有把新接入的节点算进去"
	// （例如统计逻辑被改坏、计数恒为 0）—— 那时概览数字永远不会与权威列表一致。
	var after rtOverviewSnapshot
	e.Eventually(75*time.Second, func() error {
		r := e.Admin.Get("/api/v1/overview")
		if r.Status != http.StatusOK {
			return fmt.Errorf("GET /api/v1/overview 返回 %d: %s", r.Status, string(r.Raw))
		}
		r.Decode(&after)

		// 每次轮询都重新读权威列表：两张表之间不做算术，只做同刻一致性比对，
		// 因此不受缓存窗口影响，也不受其它场景遗留数据的影响。
		liveNodeIDs, liveEdgeIDs := rtLiveInventory(t, e)
		if !rtContainsString(liveNodeIDs, fx.NodeID) {
			return fmt.Errorf("节点 %s 已从权威列表消失", fx.NodeID)
		}

		// 自洽性（与缓存无关的恒等式）：离线数必须等于总数减在线数。
		if after.Nodes.Online+after.Nodes.Offline != after.Nodes.Total {
			return fmt.Errorf("节点统计不自洽: total=%d online=%d offline=%d",
				after.Nodes.Total, after.Nodes.Online, after.Nodes.Offline)
		}
		if after.EdgeDevices.Online+after.EdgeDevices.Offline != after.EdgeDevices.Total {
			return fmt.Errorf("边缘设备统计不自洽: total=%d online=%d offline=%d",
				after.EdgeDevices.Total, after.EdgeDevices.Online, after.EdgeDevices.Offline)
		}

		// 核心不变式：概览的聚合数字必须覆盖当前真实存在的节点与设备。
		if after.Nodes.Total != int64(len(liveNodeIDs)) {
			return fmt.Errorf("概览节点总数 %d 与权威列表长度 %d 不一致（缓存窗口可能尚未穿透，继续等待）",
				after.Nodes.Total, len(liveNodeIDs))
		}
		if after.EdgeDevices.Total != int64(len(liveEdgeIDs)) {
			return fmt.Errorf("概览边缘设备总数 %d 与权威列表长度 %d 不一致",
				after.EdgeDevices.Total, len(liveEdgeIDs))
		}

		// 数据面：本场景设备必须带着刚上报的数值出现在 latest_data。
		//
		// 注意概览对每台设备只呈现**一个**物理量：缓存分支取该设备最近一次更新的
		// 那条 unified_data 记录，回落分支用 DISTINCT ON (device_id) 取 created_at
		// 最新的那条。因此这里不要求两个类别同时出现，但出现的那一个必须是本场景
		// 刚刚上报的值 —— 哨兵 21.37 与初始 0 都满足不了，所以并未放宽断言。
		for _, entry := range after.LatestData {
			if entry.DeviceID != fx.EdgeDeviceID {
				continue
			}
			for name, value := range entry.Data {
				if want, ok := expected[name]; ok && want == value {
					return nil
				}
			}
			return fmt.Errorf("设备 %d 在 latest_data 里的数据 %v 不是本场景刚上报的值 %v（概览未反映最新数据）",
				entry.DeviceID, entry.Data, expected)
		}
		return fmt.Errorf("设备 %d 尚未出现在 latest_data（共 %d 条）", fx.EdgeDeviceID, len(after.LatestData))
	})

	e.Evidence("SIM-RT-003.overview", map[string]any{
		"nodes_authoritative": len(authoritativeNodeIDs),
		"nodes_overview":      after.Nodes.Total,
		"edges_authoritative": len(authoritativeEdgeIDs),
		"edge_total_overview": after.EdgeDevices.Total,
		"latest_data_entries": len(after.LatestData),
	})
}

// rtLiveInventory 读"权威"的当前节点 node_id 列表与边缘设备主键列表。
//
// 这两个接口（GET /api/v1/nodes、GET /api/v1/edge-devices）都直接查库、
// 没有 /overview 那样的进程级缓存，因此可以作为概览聚合数字的对照物。
func rtLiveInventory(t *testing.T, e *harness.Env) ([]string, []int64) {
	t.Helper()

	var nodes []struct {
		NodeID string `json:"node_id"`
	}
	e.Admin.Get("/api/v1/nodes").Expect(http.StatusOK).Decode(&nodes)
	nodeIDs := make([]string, 0, len(nodes))
	for _, node := range nodes {
		nodeIDs = append(nodeIDs, node.NodeID)
	}

	var edges []struct {
		ID uint `json:"id"`
	}
	e.Admin.Get("/api/v1/edge-devices").Expect(http.StatusOK).Decode(&edges)
	edgeIDs := make([]int64, 0, len(edges))
	for _, edge := range edges {
		edgeIDs = append(edgeIDs, int64(edge.ID))
	}
	return nodeIDs, edgeIDs
}

// rtContainsString 判定切片里是否含目标字符串。
func rtContainsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// SIM-RT-004 Prometheus 指标包含业务计数器且格式可解析
// ---------------------------------------------------------------------------

func rtRun004(e *harness.Env) {
	t := e.T

	// 先制造一次受保护接口调用：ehome_http_requests_total 是带标签的 CounterVec，
	// 只有在 WithLabelValues 被调用后才会出现在输出里（全局中间件对每个请求打点）。
	e.Admin.Get("/api/v1/nodes").Expect(http.StatusOK)

	// /metrics 注册在根路由、无鉴权（api/routes.go），供 Prometheus 从集群外抓取。
	resp := e.Admin.Get("/metrics").Expect(http.StatusOK)
	text := string(resp.Raw)
	if strings.TrimSpace(text) == "" {
		t.Fatalf("/metrics 返回空文本（status=%d）", resp.Status)
	}

	samples, err := rtParsePrometheusText(text)
	if err != nil {
		t.Fatalf("/metrics 文本不符合 Prometheus 暴露格式: %v（前 400 字节: %s）", err, rtHead(text, 400))
	}

	// 指标名取自 backend/pkg/metrics/metrics.go（不得编造）：
	//   ehome_nodes_online                 —— Gauge，注册即暴露；
	//   ehome_data_reports_processed_total —— Counter，注册即暴露；
	//   ehome_http_requests_total          —— CounterVec，任何 HTTP 请求后即出现。
	required := []string{
		"ehome_nodes_online",
		"ehome_data_reports_processed_total",
		"ehome_http_requests_total",
	}
	for _, name := range required {
		if _, ok := samples[name]; !ok {
			t.Fatalf("业务指标 %s 未出现在 /metrics（已解析 %d 个样本）", name, len(samples))
		}
	}
	e.Evidence("SIM-RT-004.samples", len(samples))
	e.Evidence("SIM-RT-004.required", required)

	// ── 追加（设计 v1.2）：以 Prometheus 的真实抓取方式再验一次 ──
	//
	// 为什么必须单独做这一段：harness 的 Session 为了让响应体确定，
	// 在 transport 上设了 DisableCompression=true（harness/http.go），
	// 也就是**仿真客户端从不声明 Accept-Encoding: gzip**；
	// 而 deploy/monitoring/prometheus.yml 里的抓取器一定会声明。
	// 只测 harness 客户端会漏掉「压缩协商被破坏」这类缺陷：
	// 历史上 /metrics 曾被全局 gzip 中间件与 promhttp 各自压缩，
	// 产出 [gzip头][gzip头][单个 deflate 流] 的畸形体，标准解码器读不了，
	// 结果是 Prometheus 抓取整体失明、所有依赖它的告警规则静默失效。
	status, header, compressed := rtPrometheusStyleScrape(t, e, "/metrics")
	if status != http.StatusOK {
		t.Fatalf("Prometheus 方式抓取 /metrics 返回 %d；%s", status, rtGzipForensics(compressed))
	}
	if encoding := header.Get("Content-Encoding"); encoding != "gzip" {
		t.Fatalf("声明 Accept-Encoding: gzip 后响应头 Content-Encoding = %q，期望 \"gzip\""+
			"（压缩能力丢失，Prometheus 将以同样方式抓取失败）；%s", encoding, rtGzipForensics(compressed))
	}

	// 用 Go 标准库 gzip 解压：**不**借助 harness 的任何容错解包，
	// 因为 Prometheus 用的也是标准解码器 —— 能容错反而会把缺陷藏起来。
	reader, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		t.Fatalf("响应体不是合法的 gzip 流（Prometheus 将以同样方式抓取失败）: %v；%s",
			err, rtGzipForensics(compressed))
	}
	plain, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("gzip 解压中断（Prometheus 将以同样方式抓取失败）: %v；%s", err, rtGzipForensics(compressed))
	}
	if err := reader.Close(); err != nil {
		t.Fatalf("gzip 流未正常结束（Prometheus 将以同样方式抓取失败）: %v；%s", err, rtGzipForensics(compressed))
	}

	scraped, err := rtParsePrometheusText(string(plain))
	if err != nil {
		t.Fatalf("解压后的指标文本不符合 Prometheus 暴露格式: %v；%s", err, rtGzipForensics(compressed))
	}
	businessMetrics := 0
	for name := range scraped {
		if strings.HasPrefix(name, "ehome_") {
			businessMetrics++
		}
	}
	if businessMetrics == 0 {
		t.Fatalf("解压后的指标文本没有任何 ehome_ 业务指标（共 %d 个样本）；%s",
			len(scraped), rtGzipForensics(compressed))
	}
	for _, name := range required {
		if _, ok := scraped[name]; !ok {
			t.Fatalf("解压后缺少业务指标 %s（共 %d 个样本）；%s", name, len(scraped), rtGzipForensics(compressed))
		}
	}
	e.Evidence("SIM-RT-004.gzip_scrape", map[string]any{
		"content_encoding": header.Get("Content-Encoding"),
		"compressed_bytes": len(compressed),
		"plain_bytes":      len(plain),
		"business_metrics": businessMetrics,
	})
}

// rtPrometheusStyleScrape 完全按 Prometheus 抓取器的方式请求 /metrics：
// 显式声明 Accept-Encoding: gzip，并使用**未禁用压缩**的默认 transport。
//
// 两个细节决定了这段代码能不能抓住缺陷：
//  1. 必须显式设置 Accept-Encoding 请求头。Go 的 Transport 只在自己加上该头时
//     才会透明解压并抹掉 Content-Encoding；调用方显式设置时，响应体保持压缩原样
//     返回 —— 这正是 Prometheus（自带 gzip 解码器）看到的形态。
//  2. 必须使用默认 transport。任何 DisableCompression=true 的客户端都看不到
//     压缩协商，也就看不到「压缩被破坏」。
func rtPrometheusStyleScrape(t *testing.T, e *harness.Env, path string) (int, http.Header, []byte) {
	t.Helper()
	request, err := http.NewRequest(http.MethodGet, e.BaseURL+path, nil)
	if err != nil {
		t.Fatalf("构造 Prometheus 抓取请求失败 %s: %v", path, err)
	}
	request.Header.Set("Accept-Encoding", "gzip")
	// Prometheus 抓取时声明的接受类型。
	request.Header.Set("Accept", "text/plain;version=0.0.4;q=1,*/*;q=0.1")

	client := &http.Client{Timeout: 15 * time.Second} // 默认 Transport：DisableCompression=false
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("Prometheus 方式抓取 %s 失败: %v", path, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("读取 Prometheus 抓取响应失败 %s: %v", path, err)
	}
	return response.StatusCode, response.Header, body
}

// rtGzipForensics 生成「压缩形态」诊断信息，让失败一眼能看出是畸形压缩而不是别的。
//
// 指纹：正常的 /metrics gzip 体只有一个 gzip 头；
// 「全局 gzip 中间件 + promhttp 各自压缩」会产生两个 gzip 头
// （1f8b08 出现在偏移 0 与 ~10），标准 gzip 解码器读到第二个头就报错。
func rtGzipForensics(body []byte) string {
	hexHead := rtHexHead(body, 16)
	window := body
	if len(window) > 64 {
		window = window[:64]
	}
	if bytes.HasPrefix(body, []byte{0x1f, 0x8b, 0x08}) {
		if second := bytes.Index(window[3:], []byte{0x1f, 0x8b, 0x08}); second >= 0 {
			return fmt.Sprintf("响应体前 16 字节 %s（在偏移 %d 处检测到第二个 gzip 头 —— 典型的双层压缩框架），"+
				"Prometheus 将以同样方式抓取失败", hexHead, second+3)
		}
		return fmt.Sprintf("响应体前 16 字节 %s，Prometheus 将以同样方式抓取失败", hexHead)
	}
	return fmt.Sprintf("响应体前 16 字节 %s（未检测到 gzip 头），Prometheus 将以同样方式抓取失败", hexHead)
}

// rtHexHead 返回响应体前 n 字节的十六进制表示（body 不足时全给）。
func rtHexHead(body []byte, n int) string {
	if len(body) < n {
		n = len(body)
	}
	return hex.EncodeToString(body[:n])
}

// rtParsePrometheusText 把 Prometheus 文本暴露格式解析为 指标名 → 数值。
// 逐行校验：注释行必须是 # HELP / # TYPE 形式，样本行必须能拆成
// "name[{labels}] <float>"，否则报错——本函数同时承担"格式可解析"的断言。
func rtParsePrometheusText(text string) (map[string]float64, error) {
	samples := make(map[string]float64)
	helpSeen, typeSeen := 0, 0
	for i, raw := range strings.Split(text, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		if strings.HasPrefix(line, "#") {
			switch {
			case strings.HasPrefix(line, "# HELP "):
				helpSeen++
			case strings.HasPrefix(line, "# TYPE "):
				typeSeen++
			default:
				return nil, fmt.Errorf("第 %d 行注释不符合暴露格式: %q", i+1, line)
			}
			continue
		}
		name, valueStr, err := rtSplitPromSample(line)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行: %w", i+1, err)
		}
		value, err := strconv.ParseFloat(valueStr, 64)
		if err != nil {
			return nil, fmt.Errorf("第 %d 行数值 %q 不是合法浮点数: %w", i+1, valueStr, err)
		}
		samples[name] = value
	}
	if helpSeen == 0 || typeSeen == 0 {
		return nil, fmt.Errorf("缺少 # HELP/# TYPE 元数据行（help=%d type=%d）", helpSeen, typeSeen)
	}
	if len(samples) == 0 {
		return nil, fmt.Errorf("没有任何样本行")
	}
	return samples, nil
}

// rtSplitPromSample 把一行样本拆成指标名与数值字符串，兼容带/不带标签两种形态。
func rtSplitPromSample(line string) (string, string, error) {
	if idx := strings.IndexByte(line, '{'); idx >= 0 {
		end := strings.LastIndexByte(line, '}')
		if end < idx {
			return "", "", fmt.Errorf("标签未闭合: %q", line)
		}
		name := strings.TrimSpace(line[:idx])
		if name == "" {
			return "", "", fmt.Errorf("标签前缺少指标名: %q", line)
		}
		return name, strings.TrimSpace(line[end+1:]), nil
	}
	cut := strings.LastIndexByte(line, ' ')
	if cut <= 0 {
		return "", "", fmt.Errorf("样本行缺少数值: %q", line)
	}
	return strings.TrimSpace(line[:cut]), strings.TrimSpace(line[cut+1:]), nil
}

// ---------------------------------------------------------------------------
// SIM-RT-005 通知中心已读操作后未读数减少
// ---------------------------------------------------------------------------

// rtNotificationRow 只取断言需要的字段（models.Notification 的 JSON 形状）。
type rtNotificationRow struct {
	ID     uint   `json:"id"`
	Title  string `json:"title"`
	Source string `json:"source"`
	Read   bool   `json:"read"`
}

func rtRun005(e *harness.Env) {
	t := e.T

	ruleName := e.NS("SIM-RT-005", "rule")

	// 产品里唯一能经纯 HTTP 造出通知的路径是"自动化策略的纯通知动作"
	// （planner.notifyAction，POST /automation-rules/:id/trigger）。
	// 窗口取 00:00-00:01 且 edge=inside：1 分钟的 time_window ticker 在
	// 一天里的绝大多数时刻都判定"不在窗口内"，因此本次未读数的变化
	// 只可能来自下面这次手动触发，而不是后台自动补发。
	rule := e.Admin.Post("/api/v1/automation-rules", map[string]any{
		"name":                 ruleName,
		"trigger_type":         "time_window",
		"trigger_window_start": "00:00",
		"trigger_window_end":   "00:01",
		"trigger_window_edge":  "inside",
		"action_type":          "notification",
		"action_level":         "info",
		"cooldown_sec":         0,
	}).Expect(http.StatusOK)
	ruleID := rule.DataInt("id")
	t.Cleanup(func() {
		rtCleanupExpect(t, "自动化规则", e.Admin.Delete("/api/v1/automation-rules/"+strconv.FormatInt(ruleID, 10)), http.StatusOK)
	})

	before := rtUnreadCount(e, t)

	// 手动触发 = 用户点击"立即执行"：跳过条件评估，直接产生一条未读通知。
	e.Admin.Post("/api/v1/automation-rules/"+strconv.FormatInt(ruleID, 10)+"/trigger", map[string]any{}).
		Expect(http.StatusOK)

	var mine rtNotificationRow
	found := false
	e.Eventually(15*time.Second, func() error {
		rows, err := rtListNotifications(e)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.Source == "automation_rule" && strings.Contains(row.Title, ruleName) {
				mine, found = row, true
				if row.Read {
					return fmt.Errorf("新通知 %d 竟是已读状态", row.ID)
				}
				return nil
			}
		}
		return fmt.Errorf("通知中心尚未出现本场景的通知（共 %d 条）", len(rows))
	})
	if !found {
		t.Fatalf("未找到本场景创建的通知")
	}

	afterTrigger := rtUnreadCount(e, t)
	if afterTrigger <= before {
		t.Fatalf("触发通知后未读数未增加：触发前 %d，触发后 %d", before, afterTrigger)
	}

	// 用户点"标记已读"。
	e.Admin.Put("/api/v1/notifications/"+strconv.FormatUint(uint64(mine.ID), 10)+"/read", map[string]any{}).
		Expect(http.StatusOK)

	// 不变式 1：这条通知本身变为已读（通知中心列表可见）。
	e.Eventually(10*time.Second, func() error {
		rows, err := rtListNotifications(e)
		if err != nil {
			return err
		}
		for _, row := range rows {
			if row.ID == mine.ID {
				if !row.Read {
					return fmt.Errorf("通知 %d 仍未标记为已读", mine.ID)
				}
				return nil
			}
		}
		return fmt.Errorf("通知 %d 已从列表消失", mine.ID)
	})

	// 不变式 2：未读数必须减少（至少减少本场景这一条）。
	// 用"≤ 触发后-1"而不是严格相等：其它场景遗留的异步通知不应让本场景假红，
	// 但"没有减少"与"反而增加"都能被抓住。
	afterRead := rtUnreadCount(e, t)
	if afterRead > afterTrigger-1 {
		t.Fatalf("标记已读后未读数未减少：已读前 %d，已读后 %d", afterTrigger, afterRead)
	}

	// 收尾：把通知中心恢复成"没有未读"，避免影响后续场景的基线。
	e.Admin.Post("/api/v1/notifications/read-all", map[string]any{}).Expect(http.StatusOK)
	e.Evidence("SIM-RT-005.unread", map[string]int64{"before": before, "after_trigger": afterTrigger, "after_read": afterRead})
}

// rtUnreadCount 读 GET /api/v1/notifications/unread-count（信封 data.count）。
func rtUnreadCount(e *harness.Env, t *testing.T) int64 {
	t.Helper()
	return e.Admin.Get("/api/v1/notifications/unread-count").
		Expect(http.StatusOK).DataInt("count")
}

// rtListNotifications 读通知中心列表（最多 100 条，足够覆盖单次仿真运行的量）。
func rtListNotifications(e *harness.Env) ([]rtNotificationRow, error) {
	r := e.Admin.Get("/api/v1/notifications?limit=100")
	if r.Status != http.StatusOK {
		return nil, fmt.Errorf("GET /api/v1/notifications 返回 %d: %s", r.Status, string(r.Raw))
	}
	var rows []rtNotificationRow
	if err := json.Unmarshal(r.Data, &rows); err != nil {
		return nil, fmt.Errorf("解析通知列表失败: %w（data=%s）", err, rtHead(string(r.Data), 200))
	}
	return rows, nil
}

// ---------------------------------------------------------------------------
// 共用工具（SIM-OTA 也使用）
// ---------------------------------------------------------------------------

// rtDialWSRaw 是"不带任何有效凭据"的裸拨号：地址由 harness 的 WSURL(path, false)
// 构造（因此绝不会带上令牌），gorilla 负责真正的握手，好让场景拿到
// **握手响应状态码**以及"升级后是否被立即关闭"这两种形态。
func rtDialWSRaw(e *harness.Env, path string, header http.Header) (*websocket.Conn, *http.Response, error) {
	target, err := e.Admin.WSURL(path, false)
	if err != nil {
		return nil, nil, err
	}
	dialer := websocket.Dialer{HandshakeTimeout: 5 * time.Second}
	return dialer.Dial(target, header)
}

// rtAssertWSRejected 断言一次未鉴权拨号确实被拒绝，兼容两种真实形态：
//  1. 路由层 JWTAuthWithDB 直接拒绝握手（401），gorilla 返回 ErrBadHandshake；
//  2. 握手升级成功但服务端立刻关闭连接（读到关闭帧/EOF，而不是任何数据帧）。
//
// 关键不变量：未鉴权者绝不能收到任何数据帧（不得降级为匿名订阅）。
func rtAssertWSRejected(t *testing.T, label string, conn *websocket.Conn, resp *http.Response, err error) {
	t.Helper()
	if resp != nil && resp.Body != nil {
		defer resp.Body.Close()
	}
	if err != nil {
		if resp != nil && resp.StatusCode == http.StatusUnauthorized {
			return // 形态 1
		}
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("%s: 期望握手被拒(401)，实际 err=%v status=%d", label, err, status)
	}
	// 形态 2
	defer conn.Close()
	_ = conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	_, data, readErr := conn.ReadMessage()
	if readErr == nil {
		t.Fatalf("%s: 未鉴权连接收到了数据帧 %q —— 鉴权被降级为匿名订阅", label, rtHead(string(data), 200))
	}
	if nerr, ok := readErr.(net.Error); ok && nerr.Timeout() {
		t.Fatalf("%s: 未鉴权连接升级成功后被挂起（3s 内既未关闭也无拒绝），无法判定为拒绝", label)
	}
}

// rtProvisionFixture 把 harness 的共享夹具（harness/fixture.go 的
// ProvisionSimpleDevice）接进场景：它用真实 API 建好
// "节点 + 通道 + 设备配置(ConfigParser) + 边缘设备"，并做一次哨兵上报自检，
// 保证数据真的能落库 —— 后续失败才能归因到场景断言，而不是"夹具没配好"。
//
// 本函数不重复实现夹具，只负责登记自清理（设计 §5.6）。
func rtProvisionFixture(e *harness.Env, scenarioID, suffix string) *harness.Fixture {
	t := e.T
	t.Helper()
	fx, err := e.ProvisionSimpleDevice(scenarioID, suffix)
	if err != nil {
		t.Fatalf("场景夹具准备失败: %v", err)
	}
	// t.Cleanup 后进先出：节点删除先注册 → 最后执行，
	// 确保边缘设备/设备配置已由夹具先清掉，不留悬挂引用。
	t.Cleanup(func() {
		rtCleanupExpect(t, "节点 "+fx.NodeID, e.Admin.Delete("/api/v1/nodes/"+fx.NodeID), http.StatusOK)
	})
	t.Cleanup(fx.Cleanup)
	return fx
}

// rtCleanupExpect 清理阶段的断言：清理失败必须显式失败（不静默），
// 但用 Errorf 而非 Fatalf，保证同场景其余清理步骤仍会执行。
func rtCleanupExpect(t *testing.T, what string, r *harness.Response, want int) {
	t.Helper()
	if r.Status != want {
		t.Errorf("清理 %s 失败：期望 HTTP %d，实际 %d，body=%s", what, want, r.Status, rtHead(string(r.Raw), 300))
	}
}

// rtJSONNumber 取 payload 里的数值字段（JSON 数字统一解为 float64）。
func rtJSONNumber(payload map[string]any, key string) float64 {
	if v, ok := payload[key].(float64); ok {
		return v
	}
	return -1
}

// rtHead 截断长文本用于失败信息，避免刷屏。
func rtHead(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(截断)"
}
