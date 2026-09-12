//go:build simulation

// 场景目录 · SIM-DEP 部署与初始化（设计 §9 SIM-DEP-001..005）。
//
// 本域证明的不变量集中在"首次部署的信任建立"：全新库处于未初始化、
// 一次性凭据是唯一入口、错误凭据不可绕过、初始化后不可重复初始化、
// 未鉴权的部署探针可达。
//
// 全部场景都以 Env.Start 在启动序列中**原样记录**的真实 HTTP 往返为证据
// （见 harness.StartupRecord）：未初始化状态与一次性凭据只在启动时刻存在，
// 场景断言的是当时真实发生的观测，而不是重放或伪造。
//
// 命名约定（设计 §4.1）：本文件只声明 dep 前缀的包级标识符，
// 跨域共用助手（simLoginAttempt / simCountRows）放在 catalog.go。
package catalog

import (
	"fmt"
	"net/http"
	"strings"

	"ehome/backend/simulation/harness"
)

const depDoc = "docs/设计/场景仿真验证框架.md §9 SIM-DEP；docs/设计/认证授权.md"

func init() {
	Register(Scenario{
		ID:     "SIM-DEP-001",
		Title:  "全新部署启动后系统处于“未初始化”，并给出一次性设置凭据",
		Domain: DomainDEP,
		Doc:    depDoc,
		Run:    depRun001,
	})
	Register(Scenario{
		ID:     "SIM-DEP-002",
		Title:  "运维用一次性凭据创建管理员后系统进入“已初始化”且可登录",
		Domain: DomainDEP,
		Doc:    depDoc,
		Run:    depRun002,
	})
	Register(Scenario{
		ID:     "SIM-DEP-003",
		Title:  "凭据错误时初始化被拒绝，系统仍为“未初始化”",
		Domain: DomainDEP,
		Doc:    depDoc,
		Run:    depRun003,
	})
	Register(Scenario{
		ID:     "SIM-DEP-004",
		Title:  "系统已初始化后再次初始化被拒绝，管理员账号不被覆盖",
		Domain: DomainDEP,
		Doc:    depDoc,
		Run:    depRun004,
	})
	Register(Scenario{
		ID:     "SIM-DEP-005",
		Title:  "未鉴权的健康检查与指标端点可用，部署探针可达",
		Domain: DomainDEP,
		Doc:    depDoc + "；docs/设计/系统监控.md",
		Run:    depRun005,
	})
	Register(Scenario{
		ID:     "SIM-DEP-006",
		Title:  "不同场景各自注册同名节点也互不冲突，节点名始终唯一且不超长",
		Domain: DomainDEP,
		Doc:    depDoc + "；docs/设计/场景仿真验证框架.md §5.6 隔离与命名",
		Run:    depRun006,
	})
}

// depRun001 守护的不变量：全新库的认证状态必须是 uninitialized，
// 且服务启动日志必须打印一次性设置凭据（否则运维无法完成首次部署）。
func depRun001(e *harness.Env) {
	// 1) 启动日志里确实出现了凭据行。凭据本身不写入证据文件（它是秘密），
	//    只记录选择器（"." 前的前缀）用于人工核对。
	if e.Startup.Credential == "" {
		e.Fatalf("启动日志中未解析到一次性初始化凭据")
	}
	if !strings.Contains(e.Startup.Credential, ".") {
		e.Fatalf("一次性凭据形态异常（应为 selector.secret）: %q", e.Startup.Credential)
	}
	e.Evidence("startup_log_credential_line", "Initialization credential (valid for 10 minutes): <redacted>")
	e.Evidence("credential_selector", strings.SplitN(e.Startup.Credential, ".", 2)[0])

	// 2) 独立匿名会话复核 /auth/initialization：此刻系统已完成初始化，
	//    因此这里断言的是"状态机可达且语义正确"，启动时刻的 uninitialized
	//    观测由 StartupRecord 保留（见 DEP-002/003）。
	anonymous := e.NewSession()
	resp := anonymous.Get("/api/v1/auth/initialization").Expect(http.StatusOK)
	state := resp.DataString("state")
	if state != "initialized" {
		e.Fatalf("运行期认证状态应为 initialized，实际 %q", state)
	}
	if e.Startup.StateBeforeInitialize != "uninitialized" {
		e.Fatalf("启动时刻（初始化前）认证状态应为 uninitialized，实际 %q", e.Startup.StateBeforeInitialize)
	}
	e.Evidence("state_before_initialize", e.Startup.StateBeforeInitialize)
	e.Evidence("state_now", state)

	// 3) 未初始化 + 凭据行必须在同一份启动日志里成对出现。
	if !strings.Contains(e.Startup.CredentialLine, "Initialization credential") {
		e.Fatalf("凭据行文本与生产日志契约不符: %q", e.Startup.CredentialLine)
	}
	e.Evidence("server_binary", "go build -buildvcs=false ./cmd/server")
}

// depRun002 守护的不变量：一次性凭据确实能建立管理员账号与令牌，
// 且凭据只能消费一次（重放必须被拒绝）。
func depRun002(e *harness.Env) {
	if e.Startup.InitializeStatus != http.StatusCreated {
		e.Fatalf("启动序列中初始化返回 %d，期望 201", e.Startup.InitializeStatus)
	}
	if e.Startup.AdminUsername != e.AdminUser {
		e.Fatalf("初始化建立的管理员用户名 %q 与配置 %q 不符", e.Startup.AdminUsername, e.AdminUser)
	}
	e.Evidence("initialize_status", e.Startup.InitializeStatus)
	e.Evidence("initialize_message", e.Startup.InitializeMessage)
	e.Evidence("admin_user_id", e.Startup.AdminUserID)

	// 新签发的令牌必须真的可用（而不是"初始化接口 201 但登录不了"）。
	adminAPI := e.Admin.Get("/api/v1/account").Expect(http.StatusOK)
	if got := adminAPI.DataString("username"); got != e.AdminUser {
		e.Fatalf("管理员会话读到的用户名 %q 与期望 %q 不符", got, e.AdminUser)
	}
	e.Evidence("admin_account_username", adminAPI.DataString("username"))
	e.Evidence("admin_account_enabled", adminAPI.DataBool("enabled"))

	// 凭据一次性：同一凭据再次初始化必须被拒绝。
	replay := e.NewSession().Post("/api/v1/auth/initialize", map[string]any{
		"credential": e.Startup.Credential,
		"username":   e.NS("SIM-DEP-002", "replay"),
		"password":   e.AdminPass,
		"email":      "replay@sim.invalid",
	}).ExpectError(http.StatusConflict, "AUTH_INITIALIZATION_REJECTED")
	e.Evidence("credential_replay_status", replay.Status)
	e.Evidence("credential_replay_error_code", replay.ErrorCode)

	// 且重放不得产生第二个可登录账号。
	login := simLoginAttempt(e, e.NS("SIM-DEP-002", "replay"), e.AdminPass, false)
	if login.Status == http.StatusOK {
		e.Fatalf("一次性凭据重放后不应出现可登录的新账号，实际登录成功: %s", login.BodyString())
	}
	e.Evidence("replay_account_login_status", login.Status)
}

// depRun003 守护的不变量：错误凭据必须被拒绝，且拒绝不能改变系统状态
// —— 否则攻击者可以用暴力尝试把系统推进到某个可用状态。
func depRun003(e *harness.Env) {
	if e.Startup.WrongCredentialStatus != http.StatusConflict {
		e.Fatalf("错误凭据初始化应返回 409，实际 %d", e.Startup.WrongCredentialStatus)
	}
	if e.Startup.StateAfterRejectedAttempt != "uninitialized" {
		e.Fatalf("错误凭据尝试后系统状态应仍为 uninitialized，实际 %q", e.Startup.StateAfterRejectedAttempt)
	}
	e.Evidence("wrong_credential_status", e.Startup.WrongCredentialStatus)
	e.Evidence("wrong_credential_error_code", e.Startup.WrongCredentialCode)
	e.Evidence("wrong_credential_message", e.Startup.WrongCredentialMessage)
	e.Evidence("wrong_credential_observed_body", e.Startup.WrongCredentialBody)
	e.Evidence("state_after_rejected_attempt", e.Startup.StateAfterRejectedAttempt)

	// 运行期再探一次：系统已初始化，任何凭据都必须被拒绝（不可绕过状态机）。
	probe := e.NewSession().Post("/api/v1/auth/initialize", map[string]any{
		"credential": "sim-wrong." + strings.Repeat("A", 16),
		"username":   e.NS("SIM-DEP-003", "probe"),
		"password":   e.AdminPass,
		"email":      "probe@sim.invalid",
	}).ExpectError(http.StatusConflict, "AUTH_INITIALIZATION_REJECTED")
	e.Evidence("runtime_probe_status", probe.Status)

	// 拒绝路径不得留下任何账号。
	if users := simCountRows(e, "SELECT count(*) FROM users"); users != 1 {
		e.Fatalf("错误凭据尝试后 users 表应只有 1 个账号，实际 %d", users)
	}
	e.Evidence("users_count", int64(1))
}

// depRun004 守护的不变量：初始化是单向状态迁移，重复初始化必须被拒绝
// 且绝不能覆盖/追加管理员账号。
func depRun004(e *harness.Env) {
	intruder := e.NS("SIM-DEP-004", "intruder")
	before := simCountRows(e, "SELECT count(*) FROM users")

	resp := e.NewSession().Post("/api/v1/auth/initialize", map[string]any{
		"credential": "sim-second-attempt." + strings.Repeat("B", 16),
		"username":   intruder,
		"password":   "Intruder-Sim-2026!",
		"email":      "intruder@sim.invalid",
	}).ExpectError(http.StatusConflict, "AUTH_INITIALIZATION_REJECTED")
	e.Evidence("second_initialize_status", resp.Status)
	e.Evidence("second_initialize_error_code", resp.ErrorCode)

	after := simCountRows(e, "SELECT count(*) FROM users")
	if after != before {
		e.Fatalf("重复初始化改变了 users 表行数: %d → %d", before, after)
	}
	if created := simCountRows(e, "SELECT count(*) FROM users WHERE username = $1", intruder); created != 0 {
		e.Fatalf("重复初始化不应创建账号 %q，实际存在 %d 行", intruder, created)
	}
	e.Evidence("users_count_before", before)
	e.Evidence("users_count_after", after)
	e.Evidence("intruder_rows", int64(0))

	// 原管理员仍然有效：既有令牌可用，且用户名未被改动。
	account := e.Admin.Get("/api/v1/account").Expect(http.StatusOK)
	if got := account.DataString("username"); got != e.AdminUser {
		e.Fatalf("重复初始化后管理员用户名被改写: %q（期望 %q）", got, e.AdminUser)
	}
	e.Evidence("admin_username_after", account.DataString("username"))
	e.Evidence("admin_enabled_after", account.DataBool("enabled"))
}

// depRun005 守护的不变量：部署探针（健康检查、Prometheus 指标）
// 必须在未鉴权时可访问，否则编排系统的存活探测会被 401 挡死。
func depRun005(e *harness.Env) {
	anonymous := e.NewSession()

	health := anonymous.Get("/health").Expect(http.StatusOK)
	if !strings.Contains(string(health.Raw), "\"status\"") {
		e.Fatalf("GET /health 响应体缺少 status 字段: %s", health.BodyString())
	}
	e.Evidence("health_status", health.Status)
	e.Evidence("health_body", strings.TrimSpace(string(health.Raw)))

	metrics := anonymous.Get("/metrics").Expect(http.StatusOK)
	body := string(metrics.Raw)
	if !strings.Contains(body, "# HELP") {
		e.Fatalf("GET /metrics 未返回 Prometheus 文本格式（缺少 # HELP 行）；"+
			"content-encoding=%q 前 16 字节=% x: %s",
			metrics.HeaderValue("Content-Encoding"), depHead(metrics.Raw, 16), metrics.BodyString())
	}
	e.Evidence("metrics_status", metrics.Status)
	e.Evidence("metrics_bytes", len(metrics.Raw))
	e.Evidence("metrics_content_encoding", metrics.HeaderValue("Content-Encoding"))
	e.Evidence("metrics_sample_line", depFirstMetricSample(body))

	ping := anonymous.Get("/ping").Expect(http.StatusOK)
	e.Evidence("ping_status", ping.Status)

	// 对照组：受保护接口在未鉴权时必须是 401 —— 证明"探针可达"不是因为全局放开。
	protected := anonymous.Get("/api/v1/nodes").Expect(http.StatusUnauthorized)
	e.Evidence("protected_without_token_status", protected.Status)
	e.Evidence("protected_without_token_message", protected.Message)
}

// depHead 返回字节切片的前 n 个字节，用于格式类失败的十六进制诊断。
func depHead(raw []byte, n int) []byte {
	if len(raw) < n {
		return raw
	}
	return raw[:n]
}

// depRun006 守护的不变量（设计 §5.6 v1.3）：节点名的唯一性必须来自
// "运行 + 场景"双重命名空间，而不是"各场景恰好选中了不同后缀"这一假设。
//
// 这条场景是真实事故的回归：SIM-ALERT-001 与 SIM-AUTO-001 都用后缀 "rule"，
// 于是第二个场景建节点时直接 409（node_id already exists）。
// 这里用两个**互不相同**的场景 ID 故意取**相同**的后缀，断言两个节点都能
// 建立且名字不同——同时验证了 nodes.node_id 的 32 字符预算没有被撑破。
//
// ⚠ 后缀刻意用 "dep6" 而不是事故现场的 "rule"：后者是 ALERT-001/AUTO-001
// 真实使用的后缀，本场景若照抄就会**与它们抢同一个 node_id**
// （同名节点在前者清理时被删，后者再握手会撞上软删行的唯一索引）。
// 回归要证明的是"同一后缀 + 不同域 → 不同节点名"，而不是去踩别人的名字。
func depRun006(e *harness.Env) {
	const sharedSuffix = "dep6"

	first := e.DeviceFor("SIM-ALERT-001", sharedSuffix)
	second := e.DeviceFor("SIM-AUTO-001", sharedSuffix)

	if first.NodeID == second.NodeID {
		e.Fatalf("不同场景 ID 在相同后缀下产生了同名节点 %q（设计 §5.6 隔离失效）", first.NodeID)
	}
	if len(first.NodeID) > 32 || len(second.NodeID) > 32 {
		e.Fatalf("节点 ID 超出 nodes.node_id varchar(32): %q(%d) / %q(%d)",
			first.NodeID, len(first.NodeID), second.NodeID, len(second.NodeID))
	}
	e.Evidence("node_id_alert", first.NodeID)
	e.Evidence("node_id_auto", second.NodeID)
	e.Evidence("same_suffix", sharedSuffix)

	// 两个节点都要能真正接入（撞名在真实链路上的表现就是这里 409）。
	for _, node := range []*harness.Device{first, second} {
		if err := node.Connect(); err != nil {
			e.Fatalf("仿真节点 %s 连接 MQTT 失败: %v", node.NodeID, err)
		}
		node.Hello("2.6.0", "ESP32-C6-SIM", 0)
	}

	for _, node := range []*harness.Device{first, second} {
		detail := e.Admin.Get("/api/v1/nodes/" + node.NodeID).Expect(http.StatusOK)
		if got := detail.DataString("node_id"); got != node.NodeID {
			e.Fatalf("节点记录 %q 与仿真器 %q 不符", got, node.NodeID)
		}
	}
	e.Evidence("both_nodes_registered", true)

	// 场景码必须可预期，否则上面的唯一性只是巧合。
	if code := harness.ScenarioCodeFor("SIM-AUTO-006"); code != "at006" {
		e.Fatalf("场景码映射不符合设计 §5.6：SIM-AUTO-006 → %q，期望 at006", code)
	}
	e.Evidence("scenario_code_auto_006", harness.ScenarioCodeFor("SIM-AUTO-006"))
	e.Evidence("scenario_code_alert_001", harness.ScenarioCodeFor("SIM-ALERT-001"))

	e.T.Cleanup(func() {
		depDeleteNode(e, first.NodeID)
		depDeleteNode(e, second.NodeID)
	})
}

// depFirstMetricSample 取第一条真正的样本行（非注释），用于证据留痕。
func depFirstMetricSample(body string) string {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		return trimmed
	}
	return ""
}

// depNodeStatus 读取节点详情里的状态字段；节点不存在时第二个返回值为 false。
// 注意：node.go 里另有一个同名语义但签名不同的助手，二者刻意不合并
// （那边需要把"未找到"升级为 error，这边需要区分"不存在"与"状态值"）。
func depNodeStatus(e *harness.Env, nodeID string) (string, bool) {
	resp := e.Admin.Get("/api/v1/nodes/" + nodeID)
	if resp.Status == http.StatusNotFound {
		return "", false
	}
	resp.Expect(http.StatusOK)
	return resp.DataString("status"), true
}

// depDeleteNode 通过真实 HTTP 删除节点（不用直连库删）。
func depDeleteNode(e *harness.Env, nodeID string) {
	e.T.Helper()
	resp := e.Admin.Delete("/api/v1/nodes/" + nodeID)
	if resp.Status != http.StatusOK && resp.Status != http.StatusNotFound {
		e.Fatalf("删除节点 %s 失败: %s", nodeID, resp.BodyString())
	}
}

// depDeleteNodeByPK 通过主键删除节点（部分接口按主键寻址）。
func depDeleteNodeByPK(e *harness.Env, pk uint) {
	e.T.Helper()
	resp := e.Admin.Delete(fmt.Sprintf("/api/v1/nodes/%d", pk))
	if resp.Status != http.StatusOK && resp.Status != http.StatusNotFound {
		e.Fatalf("删除节点主键 %d 失败: %s", pk, resp.BodyString())
	}
}
