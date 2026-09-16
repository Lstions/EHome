//go:build simulation

// 本文件是 SIM-NTFY 域的「通知**外发**」切片（SIM-NTFY-004..008）。
//
// 与 notify.go（SIM-NTFY-001..003）的分工：
//   - notify.go 守的是「通知**产生**」：级别映射、未读计数、冷却去重 —— 全部止步于
//     notifications 表 + WS 广播，用户必须**在系统前面**才能看到；
//   - 本文件守的是「通知**离开系统**」：真实出站 HTTP、真实接收端、旁路纪律。
//     这是自动化最核心的价值（「用户人不在系统前面也能收到告警」）唯一的验证面。
//
// 契约：docs/设计/外发通知通道.md §9.2（SIM-NTFY-004..007 的原始设计）
//   - §9.3（变异验收）；docs/设计/场景仿真验证框架.md §5/§7。
//
// 本切片守护的不变量（每条断言对应其中之一）：
//  1. 投递是**真实出站**：接收端必须收到一个含预期标题的 HTTP POST —— 断言落在
//     接收端的收件箱上，不是「接口返回 200」，也不是「日志里有一行」；
//  2. 旁路纪律（设计 §2.2 fail-open）：接收端 500 / 超时 / 连不上，都**不得**让
//     通知丢失、不得让业务主流程失败 —— 通知必须仍落库，投递审计记 failed；
//  3. 审计可回链：notification_deliveries 每次尝试一行，用户/运维能区分
//     「没发出去」与「压根没配通道」；
//  4. 凭据不外泄：通道密钥以 Authorization 头出站，但**响应/审计/日志**里不得回显。
//
// 命名纪律（框架 §4.1 + 门禁第 8 条）：本文件包级标识符一律以 notify 开头。
// 文件名为什么是 outbound.go 而不是 ntfy_out.go：门禁 8（框架 §4.1）以**文件名**
// 作为包级标识符的前缀依据，带下划线的文件名会强制标识符写成 ntfy_outXxx（非 Go 风格）。
// 单字名 outbound 既保留「出站投递」的语义，又让标识符保持 outboundXxx 的正常写法。
package catalog

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"ehome/backend/simulation/harness"
)

func init() {
	Register(Scenario{
		ID:     "SIM-NTFY-004",
		Title:  "配好外发通道后真能收到告警：接收端收到一条带正确标题的 HTTP 请求",
		Domain: DomainNTFY,
		Doc:    "docs/设计/外发通知通道.md §9.2 SIM-NTFY-004；§7.1 SSRF 例外；docs/设计/场景仿真验证框架.md §5",
		Run:    outboundRun004,
	})
	Register(Scenario{
		ID:     "SIM-NTFY-005",
		Title:  "接收端报 500 时告警不会丢：通知照样落库，审计记下每一次失败尝试",
		Domain: DomainNTFY,
		Doc:    "docs/设计/外发通知通道.md §9.2 SIM-NTFY-005（重试 + 旁路语义）",
		Run:    outboundRun005,
	})
	Register(Scenario{
		ID:     "SIM-NTFY-006",
		Title:  "接收端卡住不回时系统照常运转：超时被记下，通知与主流程都不受影响",
		Domain: DomainNTFY,
		Doc:    "docs/设计/外发通知通道.md §9.2 SIM-NTFY-006（超时 + 不阻塞主流程）",
		Run:    outboundRun006,
	})
	Register(Scenario{
		ID:     "SIM-NTFY-007",
		Title:  "停用的通道不会再收到任何消息，重新启用后又能收到",
		Domain: DomainNTFY,
		Doc:    "docs/设计/外发通知通道.md §9.2 SIM-NTFY-007（禁用后不再投递）",
		Run:    outboundRun007,
	})
	Register(Scenario{
		ID:     "SIM-NTFY-008",
		Title:  "已停用的通道仍能点测试发出去：用户可以先测通再启用",
		Domain: DomainNTFY,
		Doc:    "docs/设计/外发通知通道.md §9.2；框架 §5.5（防假绿回归）",
		Run:    outboundRun008,
	})
}

// ---------------------------------------------------------------------------
// 本切片共用工具
// ---------------------------------------------------------------------------

// outboundChannelCreate 经真实 API 建一条外发通道，并登记清理。
//
// 为什么 target_url 必须带 allow_private=true：接收端监听 127.0.0.1，
// 而 loopback 正是 notify 的 SSRF 判定**默认拒绝**的地址（ssrf.go
// BlockedAddressReason 的 loopback (127.0.0.0/8)）。allow_private 就是设计
// §7.1 为「家庭内网 OneBot」冻结的那个例外，本接收端扮演的正是「内网里的一台机器」。
func outboundChannelCreate(e *harness.Env, name, targetURL string, extra map[string]any) uint {
	t := e.T
	t.Helper()
	body := map[string]any{
		"name":          name,
		"type":          "webhook",
		"target_url":    targetURL,
		"min_level":     "info",
		"enabled":       true,
		"allow_private": true,
	}
	for key, value := range extra {
		body[key] = value
	}
	created := e.Admin.Post("/api/v1/notification-channels", body).Expect(http.StatusOK)
	id := created.DataInt("id")
	if id == 0 {
		e.Fatalf("创建通道返回 id=0: %s", created.BodyString())
	}
	t.Cleanup(func() {
		// 清理容错: 场景可能已自行删除它（SIM-NTFY-007 就是这么做的）。
		resp := e.Admin.Delete("/api/v1/notification-channels/" + strconv.FormatInt(id, 10))
		if resp.Status != http.StatusOK && resp.Status != http.StatusNotFound {
			t.Errorf("清理通道 %d 失败: HTTP %d body=%s", id, resp.Status, autoHead(resp.BodyString(), 200))
		}
	})
	return uint(id)
}

// outboundChannelUpdate 改通道字段（真实 API，PUT 语义是部分更新）。
func outboundChannelUpdate(e *harness.Env, id uint, body map[string]any) {
	e.T.Helper()
	e.Admin.Put("/api/v1/notification-channels/"+strconv.FormatUint(uint64(id), 10), body).Expect(http.StatusOK)
}

// outboundDeliveryRow 是投递审计行（models.NotificationDelivery 的 JSON 形状）。
type outboundDeliveryRow struct {
	ID             uint64 `json:"id"`
	NotificationID uint   `json:"notification_id"`
	ChannelID      uint   `json:"channel_id"`
	State          string `json:"state"`
	AttemptNo      uint32 `json:"attempt_no"`
	StatusCode     int    `json:"status_code"`
	ErrorMessage   string `json:"error_message"`
	DurationMs     int64  `json:"duration_ms"`
}

// outboundListDeliveries 读投递审计（GET /notification-deliveries，与通道详情里的
// deliveries_url 同一口径：channel_id 过滤 + 分页）。
func outboundListDeliveries(e *harness.Env, channelID uint, state string) ([]outboundDeliveryRow, error) {
	query := "?page_size=100&channel_id=" + strconv.FormatUint(uint64(channelID), 10)
	if state != "" {
		query += "&state=" + state
	}
	r := e.Admin.Get("/api/v1/notification-deliveries" + query)
	if r.Status != http.StatusOK {
		return nil, fmt.Errorf("GET /api/v1/notification-deliveries%s 返回 %d: %s", query, r.Status, r.BodyString())
	}
	var page struct {
		Items []outboundDeliveryRow `json:"items"`
		Total int64                 `json:"total"`
	}
	if err := json.Unmarshal(r.Data, &page); err != nil {
		return nil, fmt.Errorf("解析投递审计失败: %w（data=%s）", err, autoHead(string(r.Data), 200))
	}
	return page.Items, nil
}

// outboundWaitTerminalDeliveries 轮询等待某通道出现 want 行**已定终态**的投递审计。
//
// 为什么要轮询而不是 sleep：投递是**异步**的（DeliverToChannelAsync /
// Create 在通知落库后同步投递，但测试端点走异步），到达时刻不可预知；
// 框架 §3 原则 3 明确禁止用 sleep 同步断言。
//
// 为什么必须等**终态**而不是「行数达到 want」：deliverToChannel 是「先写 pending
// （崩溃窗口的证据）→ 拿到结论后改写 delivered/failed」，因此行数达标的那一刻这一行
// 可能还是 pending。带着 pending 往下走会让后续断言建立在中间态上 —— 实测已踩到两次：
//   - SIM-NTFY-006 直接读到 state=pending；
//   - 更隐蔽的一处：它让「投递协程是否已经跑完」变得不确定，于是紧接着的 enabled
//     翻转会与在途投递竞争（SIM-NTFY-007 的原始故障正是这么来的）。
func outboundWaitTerminalDeliveries(e *harness.Env, channelID uint, want int, timeout time.Duration) []outboundDeliveryRow {
	e.T.Helper()
	var rows []outboundDeliveryRow
	e.Eventually(timeout, func() error {
		got, err := outboundListDeliveries(e, channelID, "")
		if err != nil {
			return err
		}
		if len(got) != want {
			return fmt.Errorf("通道 %d 的投递审计行数 = %d，期望 %d", channelID, len(got), want)
		}
		for _, row := range got {
			if row.State == "pending" {
				return fmt.Errorf("通道 %d 仍有停留在 pending 的投递（尝试尚未得出结论）", channelID)
			}
		}
		rows = got
		return nil
	})
	return rows
}

// outboundTriggerRule 建一条「温度越限即通知」的策略并让它真的触发一次，
// 返回（策略 id, 产生的通知 id）。
//
// 条件成立只能来自真实数据流（MQTT 上报 → nodemgr → 解析 → 策略求值），
// HTTP 侧没有任何「造数据」入口 —— 这是本域所有「通知产生」断言的共同前提。
func outboundTriggerRule(e *harness.Env, scenarioID, suffix, name string, fx *autoFixture, raw uint16) (int64, uint) {
	e.T.Helper()
	ruleID := autoCreateRule(e, map[string]any{
		"name":                   name,
		"trigger_type":           "sensor_threshold",
		"trigger_edge_device_id": fx.edgeDeviceID,
		"trigger_sensor_name":    "temperature",
		"trigger_comparator":     "gt",
		"trigger_threshold":      20.0,
		"action_type":            "notification",
		"action_level":           "warning",
		"cooldown_sec":           60,
	})
	if err := fx.report(raw); err != nil {
		e.Fatalf("节点上报失败: %v", err)
	}
	landed := notifyWaitCount(e, ruleID, 1, 25*time.Second)[0]
	return ruleID, landed.ID
}

// outboundAssertNotDelivered 断言「在观察窗内一条都没投递到该通道」。
//
// 为什么必须**等一段时间**：投递是异步的，「此刻没有新行」不能证明「不会投递」（假绿）。
// 这里用一个足够长的观察窗换掉「立刻断言」。
//
// 为什么要基线而不是断言**总行数**为 0：同一个通道在停用**之前**可能已经成功投递过
// （SIM-NTFY-007 的第一段正是如此，它必须先证明链路是活的）。断言总数会把这批历史行
// 误判成「停用后仍在投递」—— 那是断言写错，不是产品缺陷。
func outboundAssertNoNewDelivery(e *harness.Env, channelID uint, window time.Duration, why string) {
	e.T.Helper()
	baseline := len(outboundWaitDeliveriesCount(e, channelID))
	deadline := time.Now().Add(window)
	for time.Now().Before(deadline) {
		rows, err := outboundListDeliveries(e, channelID, "")
		if err != nil {
			e.Fatalf("%v", err)
		}
		if len(rows) > baseline {
			e.Fatalf("%s，但通道 %d 的投递审计从 %d 行涨到了 %d 行（最新 state=%s status=%d）",
				why, channelID, baseline, len(rows), rows[0].State, rows[0].StatusCode)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// outboundWaitDeliveriesCount 读某通道的投递审计行数（读失败即终止场景：
// 读不到审计时「没有新行」会退化成永远为真的空断言）。
func outboundWaitDeliveriesCount(e *harness.Env, channelID uint) []outboundDeliveryRow {
	e.T.Helper()
	rows, err := outboundListDeliveries(e, channelID, "")
	if err != nil {
		e.Fatalf("%v", err)
	}
	return rows
}

// outboundBodyField 从接收端请求体里取一个字段并归一成字符串。
//
// 为什么不能直接断言 string：预设 webhook 模板里 title/level/source 是 JSON 字符串，
// 而 notification_id 是 JSON **数字**（模板用的是 {{.NotificationID}}，不带引号）。
// 早期版本只做 .(string) 断言，结果是「字段明明在 body 里、取值却永远是空串」——
// 这类「断言读不到值」的失败很容易被误读成「服务端没发这个字段」。
func outboundBodyField(raw string, field string) string {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return ""
	}
	switch typed := parsed[field].(type) {
	case string:
		return typed
	case float64:
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(typed)
	default:
		return ""
	}
}

// outboundDeliverySummary 给失败信息用的审计摘要（避免把整个响应体倒进日志）。
func outboundDeliverySummary(e *harness.Env, channelID uint) string {
	rows, err := outboundListDeliveries(e, channelID, "")
	if err != nil {
		return "读取失败: " + err.Error()
	}
	parts := make([]string, 0, len(rows))
	for _, row := range rows {
		parts = append(parts, fmt.Sprintf("{notification=%d state=%s attempt=%d status=%d err=%q}",
			row.NotificationID, row.State, row.AttemptNo, row.StatusCode, autoHead(row.ErrorMessage, 120)))
	}
	return "[" + strings.Join(parts, " ") + "]"
}

// outboundContainsNotification 在通知列表里找某条通知。
func outboundContainsNotification(rows []autoNotificationRow, id uint) bool {
	for _, row := range rows {
		if row.ID == id {
			return true
		}
	}
	return false
}

// ---------------------------------------------------------------------------
// SIM-NTFY-004 真实出站：接收端收到带正确标题的 HTTP POST
// ---------------------------------------------------------------------------

func outboundRun004(e *harness.Env) {
	receiver := harness.NewWebhookReceiver(e.T)
	fx := autoProvisionDevice(e, "SIM-NTFY-004", "out", "sim_ntfy_004_sensor")

	name := e.NS("SIM-NTFY-004", "hook")
	channelID := outboundChannelCreate(e, name, receiver.Endpoint("/alert"),
		map[string]any{"secret": "SIM-NTFY-004-TOKEN-4321"})

	_, notificationID := outboundTriggerRule(e, "SIM-NTFY-004", "out", name+"-规则", fx, 235)

	// 不变式 1：接收端**真的**收到了一条 HTTP POST —— 这是本场景存在的全部理由。
	// 断言落在接收端的收件箱上（而非接口返回 200 / 日志里有一行）。
	received, err := receiver.WaitForCount(20*time.Second, 1)
	if err != nil {
		e.Fatalf("%v；通道 %d 的投递审计=%s", err, channelID, outboundDeliverySummary(e, channelID))
	}
	first := received[0]
	if first.Method != http.MethodPost {
		e.Fatalf("接收端收到的方法 = %s，期望 POST（规格: 投递固定为 POST）", first.Method)
	}
	if first.Path != "/alert" {
		e.Fatalf("接收端收到的路径 = %q，期望 /alert（通道 target_url 的路径必须原样保留）", first.Path)
	}
	// 不变式 1-b：body 必须是**这一条**通知的事实，不是别处的串场请求。
	// 双重锚定：标题含本场景策略名，notification_id 等于本次触发的通知 id。
	if title := outboundBodyField(first.Body, "title"); !strings.Contains(title, name) {
		e.Fatalf("接收端收到的 title=%q，未包含本场景策略名 %q（body=%s）",
			title, name, autoHead(first.Body, 300))
	}
	if got := outboundBodyField(first.Body, "notification_id"); got != strconv.FormatUint(uint64(notificationID), 10) {
		e.Fatalf("接收端收到的 notification_id=%q，期望 %d（body=%s）",
			got, notificationID, autoHead(first.Body, 300))
	}
	if level := outboundBodyField(first.Body, "level"); level != "warning" {
		e.Fatalf("接收端收到的 level=%q，期望 warning（action_level=warning 必须如实外发）", level)
	}
	// 不变式 4：通道密钥以 Authorization 头出站（OneBot 约定），但**只**出现在
	// 出站请求上；响应/审计里不得回显（下面第 2 段断言审计）。
	if auth := first.Header.Get("Authorization"); auth != "Bearer SIM-NTFY-004-TOKEN-4321" {
		e.Fatalf("接收端收到的 Authorization=%q，期望 Bearer <通道密钥>", auth)
	}

	// 不变式 3：投递审计必须记 delivered，且 status_code 是接收端真实返回的 200。
	rows := outboundWaitTerminalDeliveries(e, channelID, 1, 20*time.Second)
	if rows[0].State != "delivered" {
		e.Fatalf("投递审计 state=%q，期望 delivered（行=%+v）", rows[0].State, rows[0])
	}
	if rows[0].StatusCode != http.StatusOK {
		e.Fatalf("投递审计 status_code=%d，期望 200", rows[0].StatusCode)
	}
	if rows[0].NotificationID != notificationID {
		e.Fatalf("投递审计 notification_id=%d，期望 %d（审计必须能回链到具体通知）",
			rows[0].NotificationID, notificationID)
	}

	// 不变式 4（续）：审计与通道响应都不得出现明文密钥。
	auditRaw := e.Admin.Get("/api/v1/notification-deliveries?channel_id=" +
		strconv.FormatUint(uint64(channelID), 10)).Expect(http.StatusOK).BodyString()
	if strings.Contains(auditRaw, "SIM-NTFY-004-TOKEN-4321") {
		e.Fatalf("投递审计泄露了通道密钥: %s", autoHead(auditRaw, 400))
	}
	channelRaw := e.Admin.Get("/api/v1/notification-channels").Expect(http.StatusOK).BodyString()
	if strings.Contains(channelRaw, "SIM-NTFY-004-TOKEN-4321") {
		e.Fatalf("通道列表泄露了通道密钥: %s", autoHead(channelRaw, 400))
	}

	// 第二段（SSRF 白名单的**活**断言）：把 allow_private 关掉，同一地址必须收不到。
	// 这一段把「SSRF 判定真的在出站路径上」钉死 —— 它也是除 notify/ssrf_test.go 之外
	// 唯一用真实拨号验证该判定的地方。
	receiver.Reset()
	outboundChannelUpdate(e, channelID, map[string]any{"allow_private": false})
	testResp := e.Admin.Post("/api/v1/notification-channels/"+strconv.FormatUint(uint64(channelID), 10)+"/test",
		nil).Expect(http.StatusOK)
	testNotificationID := uint(testResp.DataInt("notification_id"))

	// 等一条 failed 审计（终态），再断言接收端**一条都没收到**。
	var blocked outboundDeliveryRow
	e.Eventually(20*time.Second, func() error {
		got, err := outboundListDeliveries(e, channelID, "failed")
		if err != nil {
			return err
		}
		for _, row := range got {
			if row.NotificationID == testNotificationID {
				blocked = row
				return nil
			}
		}
		return fmt.Errorf("尚无 notification_id=%d 的 failed 审计行（channel=%d）", testNotificationID, channelID)
	})
	if receiver.Count() != 0 {
		e.Fatalf("allow_private=false 时接收端仍收到了 %d 条请求（SSRF 判定未生效）: %v",
			receiver.Count(), receiver.Requests())
	}
	if !strings.Contains(blocked.ErrorMessage, "blocked by SSRF policy") {
		e.Fatalf("SSRF 拒绝原因未落到审计: error_message=%q", blocked.ErrorMessage)
	}
	// 审计里的错误文案不得带明文密钥（RedactText 的守护点）。
	if strings.Contains(blocked.ErrorMessage, "SIM-NTFY-004-TOKEN-4321") {
		e.Fatalf("SSRF 拒绝文案泄露了密钥: %q", blocked.ErrorMessage)
	}

	e.Evidence("SIM-NTFY-004.outbound", map[string]any{
		"channel_id": channelID, "notification_id": notificationID,
		"receiver_url": receiver.URL(), "first_request": first.String(),
		"audit_state": rows[0].State, "audit_status": rows[0].StatusCode,
		"ssrf_blocked_state": blocked.State, "ssrf_blocked_reason": blocked.ErrorMessage,
	})
}

// ---------------------------------------------------------------------------
// SIM-NTFY-005 500 → 重试 + 旁路
// ---------------------------------------------------------------------------

func outboundRun005(e *harness.Env) {
	receiver := harness.NewWebhookReceiver(e.T)
	// 接收端**始终**返回 500：这样「收到几条」就精确等于「服务端重试了几次」。
	receiver.SetResponder(func(seq int, body string) harness.ReceiverReply {
		return harness.ReceiverReply{Status: http.StatusInternalServerError, Body: "boom"}
	})
	fx := autoProvisionDevice(e, "SIM-NTFY-005", "rt", "sim_ntfy_005_sensor")

	name := e.NS("SIM-NTFY-005", "retry")
	// max_retries=2（显式）：总尝试次数 = 1 + 2 = 3。
	// 退避是 1s/2s（notify.BackoffFor），因此本场景观察窗必须 ≥3s。
	channelID := outboundChannelCreate(e, name, receiver.Endpoint("/fail"),
		map[string]any{"max_retries": 2})

	_, notificationID := outboundTriggerRule(e, "SIM-NTFY-005", "rt", name+"-规则", fx, 245)

	// 不变式 1：接收端收到**恰好 3 次**尝试（1 次首发 + 2 次重试）。
	// 用「恰好」而非「≥」：多了说明退避/终止条件坏了，少了说明重试没发生。
	received, err := receiver.WaitForCount(30*time.Second, 3)
	if err != nil {
		e.Fatalf("%v；投递审计=%s", err, outboundDeliverySummary(e, channelID))
	}
	// 再给 1.5s 观察窗，确认不会出现第 4 次（终止条件必须收敛）。
	time.Sleep(1500 * time.Millisecond)
	if got := receiver.Count(); got != 3 {
		e.Fatalf("接收端共收到 %d 次尝试，期望恰好 3 次（1 首发 + 2 重试）；多出的说明重试没有收敛", got)
	}
	// 每次尝试都必须带同一份 body（重试是「同一件事再发一次」，不是新通知）。
	for _, item := range received {
		if title := outboundBodyField(item.Body, "title"); !strings.Contains(title, name) {
			e.Fatalf("第 %d 次尝试的 title=%q 与首发不一致（body=%s）",
				item.Seq, title, autoHead(item.Body, 200))
		}
	}

	// 不变式 2：每次尝试一行审计（1:N:M 的 M），最后一条是 failed 终态。
	rows := outboundWaitTerminalDeliveries(e, channelID, 3, 20*time.Second)
	// 审计列表是 Order("id DESC")（最新的在前）—— 那是**给用户看的**顺序。
	// 断言必须按 attempt_no 升序，否则会读出 3,2,1 并误判成「序号不连续」。
	sort.Slice(rows, func(i, j int) bool { return rows[i].AttemptNo < rows[j].AttemptNo })
	for index, row := range rows {
		wantAttempt := uint32(index + 1)
		if row.AttemptNo != wantAttempt {
			e.Fatalf("第 %d 行审计 attempt_no=%d，期望 %d（每次尝试一行且序号连续）",
				index, row.AttemptNo, wantAttempt)
		}
		if row.NotificationID != notificationID {
			e.Fatalf("第 %d 行审计 notification_id=%d，期望 %d", index, row.NotificationID, notificationID)
		}
		if row.State != "failed" {
			e.Fatalf("第 %d 行审计 state=%q，期望 failed（接收端恒返回 500）", index, row.State)
		}
		if row.StatusCode != http.StatusInternalServerError {
			e.Fatalf("第 %d 行审计 status_code=%d，期望 500", index, row.StatusCode)
		}
	}

	// 不变式 3（旁路纪律，设计 §2.2）：投递失败**不影响**主流程。
	// 两条证据：① 通知本体照样落库（用户能在通知中心看到它）；
	//           ② 系统仍能正常服务后续请求。
	notifications, err := autoNotifications(e)
	if err != nil {
		e.Fatalf("%v", err)
	}
	if !outboundContainsNotification(notifications, notificationID) {
		e.Fatalf("投递失败后通知 %d 不见了（旁路纪律要求通知必须照样落库）", notificationID)
	}
	e.Admin.Get("/api/v1/notifications?limit=1").Expect(http.StatusOK)

	notifyReadAll(e)
	e.Evidence("SIM-NTFY-005.retry", map[string]any{
		"channel_id": channelID, "notification_id": notificationID,
		"attempts_received": receiver.Count(), "audit_rows": len(rows),
		"final_state": rows[len(rows)-1].State, "final_status": rows[len(rows)-1].StatusCode,
	})
}

// ---------------------------------------------------------------------------
// SIM-NTFY-006 超时 → 不阻塞主流程
// ---------------------------------------------------------------------------

func outboundRun006(e *harness.Env) {
	receiver := harness.NewWebhookReceiver(e.T)
	// 接收端延迟 3s 才应答，而通道 timeout_sec=1 ⇒ 客户端必然先超时。
	receiver.SetResponder(func(seq int, body string) harness.ReceiverReply {
		return harness.ReceiverReply{Status: http.StatusOK, Body: "ok", Delay: 3 * time.Second}
	})
	fx := autoProvisionDevice(e, "SIM-NTFY-006", "to", "sim_ntfy_006_sensor")

	name := e.NS("SIM-NTFY-006", "timeout")
	// timeout_sec=1 且 max_retries=0：本场景只验证「超时本身」，不叠加重试等待。
	channelID := outboundChannelCreate(e, name, receiver.Endpoint("/slow"),
		map[string]any{"timeout_sec": 1, "max_retries": 0})

	start := time.Now()
	_, notificationID := outboundTriggerRule(e, "SIM-NTFY-006", "to", name+"-规则", fx, 255)
	// 只测到"通知已落库"这一步：通知落库 = 越限求值已完成，出站超时是否拖慢主流程
	// 全在这一段墙钟里。**不能**把随后的审计轮询算进来 —— 审计行是在投递**失败之后**
	// 才由 finishAttempt 写出的，把"等审计"算进墙钟会把"旁路是否阻塞"与"观测延迟"混为一谈。
	elapsed := time.Since(start)

	// 不变式 1：超时被判成 failed，且错误文案说明是超时（不是 500、不是 DNS）。
	// 已停用通道要等它**收敛到终态**再断言：deliverToChannel 会先写一行 pending
	// （崩溃窗口的审计证据），拿到结论后才改成 failed。只看行数会读到 pending 那一瞬
	// —— 那是**中间态**，不是结论。
	var row outboundDeliveryRow
	e.Eventually(25*time.Second, func() error {
		got, err := outboundListDeliveries(e, channelID, "")
		if err != nil {
			return err
		}
		if len(got) != 1 {
			return fmt.Errorf("通道 %d 的审计行数 = %d，期望 1（max_retries=0 不应重试）", channelID, len(got))
		}
		if got[0].State == "pending" {
			return fmt.Errorf("通道 %d 的投递仍停在 pending（尝试尚未得出结论）", channelID)
		}
		row = got[0]
		return nil
	})
	if row.State != "failed" {
		e.Fatalf("超时后审计 state=%q，期望 failed（行=%+v）", row.State, row)
	}
	if row.StatusCode != 0 {
		e.Fatalf("超时未收到响应，status_code 应为 0，实际 %d", row.StatusCode)
	}
	timeoutEvidence := strings.Contains(row.ErrorMessage, "context deadline exceeded") ||
		strings.Contains(row.ErrorMessage, "Client.Timeout") ||
		strings.Contains(row.ErrorMessage, "timeout")
	if !timeoutEvidence {
		e.Fatalf("审计错误文案未体现超时（用户在界面上无法判断是网络慢还是端点坏了）: %q", row.ErrorMessage)
	}
	// 超时耗时必须落在 [timeout, timeout+宽限] 内：1s 的配置不能被静默拉长到 10s。
	if row.DurationMs < 900 || row.DurationMs > 4500 {
		e.Fatalf("超时尝试耗时 %dms，期望约 1000ms（timeout_sec=1）", row.DurationMs)
	}
	// 不变式 2：出站不阻塞采集/求值热路径 —— 从上报到通知落库的墙钟必须接近超时值。
	if elapsed > 12*time.Second {
		e.Fatalf("一次越限上报到通知落库耗时 %s，超过旁路纪律的预算（出站超时不得拖慢主流程）",
			elapsed.Round(time.Millisecond))
	}

	// 不变式 3：接收端**确实收到了**这次请求（证明 failed 是超时而非压根没连出去，
	// 否则「超时」这个结论本身可能是假绿）。
	if receiver.Count() == 0 {
		e.Fatalf("审计记 failed 但接收端一条都没收到 —— 无法区分超时与没发出去")
	}
	// 不变式 4：主流程仍成功（通知照样落库）。
	notifications, err := autoNotifications(e)
	if err != nil {
		e.Fatalf("%v", err)
	}
	if !outboundContainsNotification(notifications, notificationID) {
		e.Fatalf("超时后通知 %d 丢失（旁路纪律要求通知必须照样落库）", notificationID)
	}

	notifyReadAll(e)
	e.Evidence("SIM-NTFY-006.timeout", map[string]any{
		"channel_id": channelID, "notification_id": notificationID,
		"audit_state": row.State, "audit_duration_ms": row.DurationMs,
		"receiver_count": receiver.Count(), "elapsed_ms": elapsed.Milliseconds(),
	})
}

// ---------------------------------------------------------------------------
// SIM-NTFY-007 禁用后不再投递，重新启用后恢复
// ---------------------------------------------------------------------------

func outboundRun007(e *harness.Env) {
	receiver := harness.NewWebhookReceiver(e.T)
	fx := autoProvisionDevice(e, "SIM-NTFY-007", "off", "sim_ntfy_007_sensor")

	name := e.NS("SIM-NTFY-007", "toggle")
	channelID := outboundChannelCreate(e, name, receiver.Endpoint("/toggle"), nil)

	// 第一段：启用态下先证明链路是**活的**。
	// 没有这一段的「禁用后收不到」是假绿 —— 收不到也可能只是链路根本没通。
	outboundTriggerRule(e, "SIM-NTFY-007", "off", name+"-规则A", fx, 235)
	if _, err := receiver.WaitForCount(20*time.Second, 1); err != nil {
		e.Fatalf("启用态下接收端未收到请求，禁用断言将失去意义: %v", err)
	}
	// 等到终态（delivered）而不是"有 1 行"：投递先写 pending 再改写终态，
	// 只看到行数就往下走，会把"投递协程仍在跑"当成"已经投完"。
	firstAudit := outboundWaitTerminalDeliveries(e, channelID, 1, 20*time.Second)
	if firstAudit[0].State != "delivered" {
		e.Fatalf("启用态下投递未成功: %+v", firstAudit[0])
	}
	firstAuditNotificationID := firstAudit[0].NotificationID

	e.Evidence("SIM-NTFY-007.stage1_ready", map[string]any{
		"receiver_url": receiver.URL(), "channel_id": channelID,
		"receiver_count": receiver.Count(), "first_audit": outboundDeliverySummary(e, channelID),
	})

	// 第二段：停用通道 ⇒ 不投递，但通知仍落库。
	//
	// 竞态说明：第一段的投递与「停用」之间**必须**有 happens-before —— 上面已经等到
	// 第一段的审计落到 delivered 终态（投递协程已跑完），因此这里的 enabled 翻转
	// 不会与在途投递竞争。这正是上面那一步不能省的原因。
	outboundChannelUpdate(e, channelID, map[string]any{"enabled": false})
	receiver.Reset()
	e.Evidence("SIM-NTFY-007.阶段2_停用前", map[string]any{
		"channel_id": channelID, "enabled": false,
		"first_audit_notification_id": firstAuditNotificationID,
	})
	// cooldown=60s 会拦住同一条策略的第二次触发，因此这里换一条新策略。
	_, notificationID := outboundTriggerRule(e, "SIM-NTFY-007", "off", name+"-规则B", fx, 245)
	// 通知必须**照样落库**（禁用通道只是不投递，不是「不产生通知」）。
	notifications, err := autoNotifications(e)
	if err != nil {
		e.Fatalf("%v", err)
	}
	if !outboundContainsNotification(notifications, notificationID) {
		e.Fatalf("通道被禁用后通知 %d 没有落库（禁用不应影响通知产生）", notificationID)
	}
	// 观察窗内既不投递也不写审计（禁用**不是失败**：设计 §5 明确不写 delivery 行）。
	outboundAssertNoNewDelivery(e, channelID, 6*time.Second, "通道 enabled=false")
	if receiver.Count() != 0 {
		e.Fatalf("通道被禁用后接收端仍收到 %d 条请求: %v", receiver.Count(), receiver.Requests())
	}
	e.Evidence("SIM-NTFY-007.stage2_disabled", map[string]any{
		"receiver_count":           receiver.Count(),
		"audit_rows":               outboundDeliverySummary(e, channelID),
		"disabled_notification_id": notificationID,
	})

	// 第三段：重新启用**同一条**通道 ⇒ 又能收到。
	// 复用同一条通道正是本段的证明力所在：同一 id 上「禁用期收不到、启用后收得到」，
	// 排除了「换了一条通道所以行为不同」的其它解释。
	outboundChannelUpdate(e, channelID, map[string]any{"enabled": true})
	// 必须再触发一次才有可投递的通知：第二段的通道是停用的，那条通知从未被投递，
	// 重新启用并不会补投历史通知（设计里没有"补投"语义）。少了这一步，等待的就是
	// 一件永远不会发生的事 —— 表现为"重新启用后收不到"，但根因是场景没有制造事件。
	_, reenabledNotificationID := outboundTriggerRule(e, "SIM-NTFY-007", "off", name+"-规则C", fx, 255)
	received, err := receiver.WaitForCount(20*time.Second, 1)
	if err != nil {
		e.Fatalf("重新启用后接收端仍未收到请求（旁路链路未恢复）: %v；审计=%s",
			err, outboundDeliverySummary(e, channelID))
	}
	// 收到的必须正是**第三段刚触发的那条**通知：既非空、也不等于第一段那条。
	// 双重锚定（body 里的值 + 与本次触发的通知 id 相等），排除"收到了历史/串场请求"。
	receivedNotificationID := outboundBodyField(received[0].Body, "notification_id")
	wantNotificationID := strconv.FormatUint(uint64(reenabledNotificationID), 10)
	if receivedNotificationID == "" {
		e.Fatalf("重新启用后收到的请求缺少 notification_id（body=%s）", autoHead(received[0].Body, 300))
	}
	if receivedNotificationID != wantNotificationID {
		e.Fatalf("重新启用后收到的 notification_id=%q，期望第三段刚触发的 %s（第一段是 %d）",
			receivedNotificationID, wantNotificationID, firstAuditNotificationID)
	}
	// 审计恰好 2 行 = 第一段的 1 行 + 第三段的 1 行（禁用期间**不得**写审计行）。
	//
	// 顺序语义：审计列表是 Order("id DESC")，因此 rows[0] 是**最新**一行。
	// 两行必须分别对应两次触发的那两条通知 —— 这比"行数为 2"更强：
	// 它同时证明禁用期间没有偷偷写行、且第三段产生的确实是新投递。
	rows := outboundWaitTerminalDeliveries(e, channelID, 2, 20*time.Second)
	for _, row := range rows {
		if row.State != "delivered" {
			e.Fatalf("重新启用后出现非 delivered 的审计行（禁用期间可能仍在投递）: %+v", row)
		}
	}
	if rows[0].NotificationID != uint(reenabledNotificationID) {
		e.Fatalf("最新一行审计的 notification_id=%d，期望第三段刚触发的 %d",
			rows[0].NotificationID, reenabledNotificationID)
	}
	if rows[len(rows)-1].NotificationID != firstAuditNotificationID {
		e.Fatalf("最早一行审计的 notification_id=%d，期望第一段的 %d",
			rows[len(rows)-1].NotificationID, firstAuditNotificationID)
	}

	notifyReadAll(e)
	e.Evidence("SIM-NTFY-007.toggle", map[string]any{
		"channel_id":                      channelID,
		"enabled_delivered":               firstAudit[0].State,
		"disabled_notification_persisted": true,
		"reenabled_received":              received[0].String(),
		"reenabled_notification_id":       receivedNotificationID,
		"first_delivered_notification_id": firstAuditNotificationID,
		"audit_rows_total":                len(rows),
	})
}

// ---------------------------------------------------------------------------
// SIM-NTFY-008 已停用通道仍能「测试」
// ---------------------------------------------------------------------------

// outboundRun008 守护**一个已修复的假绿缺陷**：POST /notification-channels/:id/test
// 曾经对 enabled=false 的通道静默无操作 —— 用户点「测试」看到成功提示，实际一个字节
// 都没发出去（「产品对用户假绿」，台账 §3.51 的第三形态）。
//
// 修复后的契约（handler_notification_channel.go 的 test 注释）：测试走
// DeliverToChannelAsync **直达用户选中的那条通道**，enabled 与 min_level 过滤都不参与。
// 本场景是那个契约的回归门禁 —— 它存在的意义就是「防止它被改回去」。
func outboundRun008(e *harness.Env) {
	receiver := harness.NewWebhookReceiver(e.T)

	name := e.NS("SIM-NTFY-008", "dt")
	// 一建出来就是停用态（设计 §7.6 的零值陷阱：enabled=false 必须真的能表达）。
	channelID := outboundChannelCreate(e, name, receiver.Endpoint("/probe"),
		map[string]any{"enabled": false, "max_retries": 0})

	// 前置自证：库里确实是停用态（否则本场景证明的是别的东西）。
	var page struct {
		Items []struct {
			ID      uint `json:"id"`
			Enabled bool `json:"enabled"`
		} `json:"items"`
	}
	e.Admin.Get("/api/v1/notification-channels?page_size=100").Expect(http.StatusOK).Decode(&page)
	found := false
	for _, item := range page.Items {
		if item.ID == channelID {
			found = true
			if item.Enabled {
				e.Fatalf("通道 %d 本应是停用态，实际 enabled=true（前置条件不成立）", channelID)
			}
		}
	}
	if !found {
		e.Fatalf("列表里找不到刚建的通道 %d", channelID)
	}

	// 核心断言：对**已停用**通道点「测试」，接收端必须真的收到请求。
	resp := e.Admin.Post("/api/v1/notification-channels/"+strconv.FormatUint(uint64(channelID), 10)+"/test",
		nil).Expect(http.StatusOK)
	notificationID := uint(resp.DataInt("notification_id"))
	if notificationID == 0 {
		e.Fatalf("测试响应未返回 notification_id: %s", resp.BodyString())
	}

	received, err := receiver.WaitForCount(20*time.Second, 1)
	if err != nil {
		e.Fatalf("%v；这通常意味着 /test 又被 enabled 过滤挡住了（假绿回归）；审计=%s",
			err, outboundDeliverySummary(e, channelID))
	}
	if title := outboundBodyField(received[0].Body, "title"); title != "通知通道测试" {
		e.Fatalf("测试消息标题=%q，期望通知通道测试（用户要能区分自检与真实告警）", title)
	}
	rows := outboundWaitTerminalDeliveries(e, channelID, 1, 20*time.Second)
	if rows[0].State != "delivered" || rows[0].StatusCode != http.StatusOK {
		e.Fatalf("停用通道的测试投递未记 delivered/200: %+v", rows[0])
	}
	if rows[0].NotificationID != notificationID {
		e.Fatalf("测试投递审计 notification_id=%d，期望 %d", rows[0].NotificationID, notificationID)
	}
	// 对照：测试**不**受 min_level 限制（同一条契约的另一半）。
	outboundChannelUpdate(e, channelID, map[string]any{"min_level": "critical"})
	receiver.Reset()
	second := e.Admin.Post("/api/v1/notification-channels/"+strconv.FormatUint(uint64(channelID), 10)+"/test",
		nil).Expect(http.StatusOK)
	if _, err := receiver.WaitForCount(20*time.Second, 1); err != nil {
		e.Fatalf("通道 min_level=critical 时测试消息（info 级）被过滤掉了: %v（测试响应=%s）",
			err, second.BodyString())
	}

	e.Evidence("SIM-NTFY-008.disabled-test", map[string]any{
		"channel_id": channelID, "enabled": false, "min_level_after": "critical",
		"received": received[0].String(), "audit_state": rows[0].State,
	})
}
