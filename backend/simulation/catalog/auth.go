//go:build simulation

// 场景目录 · SIM-AUTH 认证与会话（设计 §9 SIM-AUTH-001..008）。
//
// 本域守护的是单主体会话的完整生命周期：口令校验、失败限流、
// 登出/改密的全局吊销、令牌篡改与缺失的拒绝语义、以及"记住我"的有效期差异。
//
// 会话吊销类场景会临时改变运行期口令与会话状态，因此每个场景结束前
// 都必须把 Env.Admin / Env.AdminPass 恢复到可用状态 —— 后续场景依赖它们。
//
// 命名约定（设计 §4.1）：本文件只声明 auth 前缀的包级标识符，
// 跨域共用助手（simLoginAttempt / simCountRows）放在 catalog.go。
package catalog

import (
	"errors"
	"math"
	"net/http"
	"strings"
	"time"

	"ehome/backend/simulation/harness"
)

const authDoc = "docs/设计/场景仿真验证框架.md §9 SIM-AUTH；docs/设计/认证授权.md"

func init() {
	Register(Scenario{
		ID:     "SIM-AUTH-001",
		Title:  "管理员用正确用户名口令登录并取得会话令牌",
		Domain: DomainAUTH,
		Doc:    authDoc,
		Run:    authRun001,
	})
	Register(Scenario{
		ID:     "SIM-AUTH-002",
		Title:  "口令错误返回 401，且不暴露账号是否存在",
		Domain: DomainAUTH,
		Doc:    authDoc,
		Run:    authRun002,
	})
	Register(Scenario{
		ID:     "SIM-AUTH-003",
		Title:  "连续口令错误触发限流并返回可重试提示",
		Domain: DomainAUTH,
		Doc:    authDoc,
		Run:    authRun003,
	})
	Register(Scenario{
		ID:     "SIM-AUTH-004",
		Title:  "登出后原令牌立即失效，无法重放",
		Domain: DomainAUTH,
		Doc:    authDoc,
		Run:    authRun004,
	})
	Register(Scenario{
		ID:     "SIM-AUTH-005",
		Title:  "修改口令后旧令牌全部失效，新口令可重新登录",
		Domain: DomainAUTH,
		Doc:    authDoc,
		Run:    authRun005,
	})
	Register(Scenario{
		ID:     "SIM-AUTH-006",
		Title:  "未携带令牌访问受保护接口返回 401",
		Domain: DomainAUTH,
		Doc:    authDoc,
		Run:    authRun006,
	})
	Register(Scenario{
		ID:     "SIM-AUTH-007",
		Title:  "篡改签名的令牌被拒绝，不会降级为匿名访问",
		Domain: DomainAUTH,
		Doc:    authDoc,
		Run:    authRun007,
	})
	Register(Scenario{
		ID:     "SIM-AUTH-008",
		Title:  "勾选“记住我”签发更长有效期的令牌",
		Domain: DomainAUTH,
		Doc:    authDoc,
		Run:    authRun008,
	})
}

// authRun001 守护的不变量：正确凭据必须换到可用的会话令牌
// （登录接口返回 token 只是第一步，令牌必须真能通过 /account 的鉴权）。
func authRun001(e *harness.Env) {
	login := simLoginAttempt(e, e.AdminUser, e.AdminPass, false).Expect(http.StatusOK)
	token := login.DataString("token")
	if token == "" {
		e.Fatalf("登录成功但未返回令牌: %s", login.BodyString())
	}
	if segments := strings.Split(token, "."); len(segments) != 3 {
		e.Fatalf("令牌不是三段式 JWT（段数 %d）", len(segments))
	}
	e.Evidence("login_status", login.Status)
	e.Evidence("token_length", len(token))
	e.Evidence("login_user_id", login.DataInt("user.id"))
	e.Evidence("login_username", login.DataString("user.username"))

	// 令牌由服务端签发给独立会话，用它读 /account 验证签名与主体绑定。
	session := e.NewSession()
	session.Token = token
	account := session.Get("/api/v1/account").Expect(http.StatusOK)
	if got := account.DataString("username"); got != e.AdminUser {
		e.Fatalf("令牌读到的账号 %q 与登录账号 %q 不符", got, e.AdminUser)
	}
	e.Evidence("account_username", account.DataString("username"))
	e.Evidence("account_enabled", account.DataBool("enabled"))
}

// authRun002 守护的不变量：口令错误与账号不存在必须返回完全相同的
// 外部观测 —— 否则登录接口就成了账号枚举器。
func authRun002(e *harness.Env) {
	missing := e.NS("SIM-AUTH-002", "nobody")

	wrongPassword := simLoginAttempt(e, e.AdminUser, "definitely-not-the-password", false)
	missingAccount := simLoginAttempt(e, missing, "definitely-not-the-password", false)

	wrongPassword.Expect(http.StatusUnauthorized)
	missingAccount.Expect(http.StatusUnauthorized)

	if wrongPassword.Message != missingAccount.Message {
		e.Fatalf("账号存在与否泄露：口令错误的 message=%q，账号不存在的 message=%q",
			wrongPassword.Message, missingAccount.Message)
	}
	if wrongPassword.ErrorCode != missingAccount.ErrorCode {
		e.Fatalf("账号存在与否泄露：error_code 不同 %q vs %q",
			wrongPassword.ErrorCode, missingAccount.ErrorCode)
	}
	lowered := strings.ToLower(wrongPassword.Message)
	if strings.Contains(lowered, "not found") || strings.Contains(lowered, "不存在") || strings.Contains(lowered, "no such") {
		e.Fatalf("401 消息泄露了账号存在性: %q", wrongPassword.Message)
	}
	e.Evidence("wrong_password_status", wrongPassword.Status)
	e.Evidence("missing_account_status", missingAccount.Status)
	e.Evidence("observable_message", wrongPassword.Message)
	e.Evidence("observable_error_code", wrongPassword.ErrorCode)

	// 对照组：正确口令仍然成功 —— 证明上面的 401 来自口令校验而不是别的门禁。
	ok := simLoginAttempt(e, e.AdminUser, e.AdminPass, false).Expect(http.StatusOK)
	e.Evidence("correct_password_status", ok.Status)
}

// authRun003 守护的不变量：连续失败必须被限流，且限流响应要给出
// 可机器读取的等待时长（Retry-After），否则客户端只能盲目重试。
//
// 限流器按 (IP, 用户名) 建桶，窗口 15 分钟且**成功登录会重置窗口**，
// 因此本场景只影响自己：连续失败达到阈值后立即用一次成功登录收尾。
func authRun003(e *harness.Env) {
	const maxAttempts = 12
	statuses := make([]int, 0, maxAttempts)
	var limited *harness.Response

	for i := 0; i < maxAttempts; i++ {
		resp := simLoginAttempt(e, e.AdminUser, "wrong-password-attempt", false)
		statuses = append(statuses, resp.Status)
		if resp.Status == http.StatusTooManyRequests {
			limited = resp
			break
		}
		if resp.Status != http.StatusUnauthorized {
			e.Fatalf("第 %d 次失败登录返回了意外状态 %d: %s", i+1, resp.Status, resp.BodyString())
		}
	}
	if limited == nil {
		e.Fatalf("连续 %d 次口令错误仍未触发限流；实际状态序列 %v", maxAttempts, statuses)
	}
	limited.ExpectError(http.StatusTooManyRequests, "")

	retryAfter := limited.HeaderValue("Retry-After")
	if retryAfter == "" {
		e.Fatalf("限流响应缺少 Retry-After 头: %s", limited.BodyString())
	}
	seconds, err := time.ParseDuration(retryAfter + "s")
	if err != nil || seconds <= 0 {
		e.Fatalf("Retry-After 不是正整数秒: %q", retryAfter)
	}
	if seconds > 15*time.Minute {
		e.Fatalf("Retry-After %s 超过限流窗口（15 分钟），客户端会被误导", seconds)
	}
	e.Evidence("status_sequence", statuses)
	e.Evidence("retry_after_seconds", int(seconds.Seconds()))
	e.Evidence("limited_message", limited.Message)

	// 收尾：一次成功登录会重置限流窗口，避免把后续场景一起锁死。
	simLoginAttempt(e, e.AdminUser, e.AdminPass, false).Expect(http.StatusOK)
	e.Evidence("limiter_reset_by_success", true)
}

// authRun004 守护的不变量：登出必须让令牌立即失效（不可重放）。
// 单主体系统里登出会吊销该主体的全部会话，因此 Env.Admin 的令牌同样失效，
// 场景结束前必须重建它。
func authRun004(e *harness.Env) {
	session, err := e.Session(e.AdminUser, e.AdminPass)
	if err != nil {
		e.Fatalf("登出场景准备会话失败: %v", err)
	}
	doomed := session.Token

	logout := session.Post("/api/v1/auth/logout", nil).Expect(http.StatusOK)
	e.Evidence("logout_status", logout.Status)
	e.Evidence("logout_body", strings.TrimSpace(string(logout.Raw)))

	// 原令牌立即失效：重放必须 401。
	replay := e.NewSession()
	replay.Token = doomed
	rejected := replay.Get("/api/v1/account").Expect(http.StatusUnauthorized)
	e.Evidence("replay_status", rejected.Status)
	e.Evidence("replay_message", rejected.Message)

	// 单主体语义：并发存在的另一个会话（Env.Admin）同样被吊销。
	other := e.Admin.Get("/api/v1/account")
	if other.Status != http.StatusUnauthorized {
		e.Fatalf("登出应吊销该主体的全部会话，但另一个会话仍返回 %d: %s", other.Status, other.BodyString())
	}
	e.Evidence("other_session_status", other.Status)

	// 恢复运行期会话，后续场景依赖 Env.Admin。
	if err := e.RefreshAdmin(); err != nil {
		e.Fatalf("登出后重建管理员会话失败: %v", err)
	}
	e.Admin.Get("/api/v1/account").Expect(http.StatusOK)
	e.Evidence("admin_session_restored", true)
}

// authRun005 守护的不变量：改密必须让所有旧令牌失效（会话版本递增），
// 新口令立即可用；场景结束前把口令改回，保证后续场景可登录。
func authRun005(e *harness.Env) {
	original := e.AdminPass
	rotated := original + "-Rotated1"

	oldToken := e.Admin.Token
	change := e.Admin.Post("/api/v1/account/password", map[string]any{
		"old_password": original,
		"new_password": rotated,
	}).Expect(http.StatusOK)
	e.Evidence("change_status", change.Status)
	e.Evidence("change_reauthenticate", change.DataBool("reauthenticate"))

	// 旧令牌立即失效。
	stale := e.NewSession()
	stale.Token = oldToken
	rejected := stale.Get("/api/v1/account").Expect(http.StatusUnauthorized)
	e.Evidence("stale_token_status", rejected.Status)

	// 新口令可登录，新令牌可用。
	fresh, err := e.Session(e.AdminUser, rotated)
	if err != nil {
		e.Fatalf("改密后新口令登录失败: %v", err)
	}
	fresh.Get("/api/v1/account").Expect(http.StatusOK)
	e.Evidence("new_password_login", "ok")

	// 旧口令必须不再可用。
	oldAttempt := simLoginAttempt(e, e.AdminUser, original, false)
	if oldAttempt.Status == http.StatusOK {
		e.Fatalf("改密后旧口令仍可登录: %s", oldAttempt.BodyString())
	}
	e.Evidence("old_password_login_status", oldAttempt.Status)

	// 自我清理（设计 §5.6）：把口令改回原值，并把 Env.Admin / Env.AdminPass
	// 一起恢复到可用状态。两步都必须走 RefreshAdminWith —— 它会同步
	// Env.AdminPass；若这里用 RefreshAdmin()（读的是当前 AdminPass，
	// 此刻已是 rotated），最后一步登录会拿错口令，把后续所有场景一起打挂。
	if err := e.RefreshAdminWith(rotated); err != nil {
		e.Fatalf("改密后无法用新口令重建会话: %v", err)
	}
	e.Admin.Post("/api/v1/account/password", map[string]any{
		"old_password": rotated,
		"new_password": original,
	}).Expect(http.StatusOK)
	if err := e.RefreshAdminWith(original); err != nil {
		e.Fatalf("恢复原口令后重新登录失败: %v", err)
	}
	e.Admin.Get("/api/v1/account").Expect(http.StatusOK)
	if e.AdminPass != original {
		e.Fatalf("改密场景未把 Env.AdminPass 恢复到原值")
	}
	e.Evidence("password_restored", true)
}

// authRun006 守护的不变量：受保护接口在缺少令牌时必须拒绝，
// 且拒绝发生在业务处理之前（返回统一信封的 401，而不是 200 空数据）。
func authRun006(e *harness.Env) {
	anonymous := e.NewSession()

	cases := []struct {
		name   string
		method string
		path   string
		body   any
	}{
		{"节点列表", http.MethodGet, "/api/v1/nodes", nil},
		{"当前账号", http.MethodGet, "/api/v1/account", nil},
		{"通道列表", http.MethodGet, "/api/v1/channels", nil},
		{"创建通道", http.MethodPost, "/api/v1/channels", map[string]any{"node_id": "sim-x", "hardware_type": "uart"}},
		{"概览", http.MethodGet, "/api/v1/overview", nil},
	}
	for _, item := range cases {
		resp := anonymous.Do(item.method, item.path, item.body).Expect(http.StatusUnauthorized)
		if len(resp.Data) > 0 && string(resp.Data) != "null" {
			e.Fatalf("%s 未鉴权时返回了数据负载: %s", item.name, resp.BodyString())
		}
		e.Evidence("unauthorized_"+item.name, resp.Status)
	}

	// 只有 "Bearer " 前缀、令牌为空的写法同样必须拒绝。
	emptyBearer := e.NewSession().WithHeader("Authorization", "Bearer ")
	emptyBearer.Get("/api/v1/nodes").Expect(http.StatusUnauthorized)
	e.Evidence("empty_bearer_status", http.StatusUnauthorized)

	// 对照组：携带有效令牌时同样的请求成功。
	e.Admin.Get("/api/v1/nodes").Expect(http.StatusOK)
	e.Evidence("authorized_nodes_status", http.StatusOK)
}

// authRun007 守护的不变量：签名被篡改的令牌必须被拒绝，
// 且拒绝是 401 —— 绝不能"校验失败就当作匿名"继续处理请求。
func authRun007(e *harness.Env) {
	// 篡改**签名段**（第 3 段）：只有签名变了，才叫「签名被篡改」。
	// 原实现改的是整串末字符（落在 header 段），语义不对，且可能不改变解码结果。
	sigParts := strings.Split(e.Admin.Token, ".")
	if len(sigParts) != 3 {
		e.Fatalf("令牌不是三段式 JWT（段数 %d）", len(sigParts))
	}
	tampered := sigParts[0] + "." + sigParts[1] + "." + authTamperSegment(sigParts[2])
	if tampered == e.Admin.Token {
		e.Fatalf("未能构造出篡改令牌")
	}

	session := e.NewSession()
	session.Token = tampered
	resp := session.Get("/api/v1/nodes")
	if resp.Status != http.StatusUnauthorized {
		e.Fatalf("篡改令牌应返回 401，实际 %d: %s", resp.Status, resp.BodyString())
	}
	if len(resp.Data) > 0 && string(resp.Data) != "null" {
		e.Fatalf("篡改令牌返回了数据负载（降级为匿名）: %s", resp.BodyString())
	}
	e.Evidence("tampered_status", resp.Status)
	e.Evidence("tampered_message", resp.Message)

	// 篡改 payload 段（保留原签名）同样必须被拒绝。
	parts := strings.Split(e.Admin.Token, ".")
	if len(parts) == 3 {
		payloadTampered := parts[0] + "." + authTamperSegment(parts[1]) + "." + parts[2]
		session2 := e.NewSession()
		session2.Token = payloadTampered
		resp2 := session2.Get("/api/v1/account")
		if resp2.Status != http.StatusUnauthorized {
			e.Fatalf("篡改 payload 的令牌应返回 401，实际 %d: %s", resp2.Status, resp2.BodyString())
		}
		e.Evidence("tampered_payload_status", resp2.Status)
	}

	// 对照组：未篡改的令牌仍然可用。
	e.Admin.Get("/api/v1/account").Expect(http.StatusOK)
	e.Evidence("original_token_status", http.StatusOK)
}

// authRun008 守护的不变量："记住我"必须真的延长令牌有效期，
// 且两个令牌都能通过鉴权（不能签出一个谁也验证不过的令牌）。
func authRun008(e *harness.Env) {
	short, err := authTokenTTL(e, false)
	if err != nil {
		e.Fatalf("普通登录令牌解析失败: %v", err)
	}
	long, err := authTokenTTL(e, true)
	if err != nil {
		e.Fatalf("“记住我”令牌解析失败: %v", err)
	}

	const day = 24 * time.Hour
	if math.Abs(short.Seconds()-day.Seconds()) > 120 {
		e.Fatalf("普通登录令牌有效期 %s，期望约 24 小时", short)
	}
	if math.Abs(long.Seconds()-(7*day).Seconds()) > 120 {
		e.Fatalf("“记住我”令牌有效期 %s，期望约 7 天", long)
	}
	if long <= short {
		e.Fatalf("“记住我”令牌有效期 %s 未长于普通令牌 %s", long, short)
	}
	e.Evidence("default_ttl_seconds", int(short.Seconds()))
	e.Evidence("remember_me_ttl_seconds", int(long.Seconds()))
	e.Evidence("ttl_ratio", long.Hours()/short.Hours())

	// 两个令牌都必须可用（有效期差异不是以牺牲可验证性换来的）。
	for _, remember := range []bool{false, true} {
		login := simLoginAttempt(e, e.AdminUser, e.AdminPass, remember).Expect(http.StatusOK)
		session := e.NewSession()
		session.Token = login.DataString("token")
		session.Get("/api/v1/account").Expect(http.StatusOK)
	}
	e.Evidence("both_tokens_usable", true)
}

// authTokenTTL 登录并解码 JWT payload，返回 exp - iat。
// 不校验签名：这里断言的是令牌自身公开的有效期属性。
func authTokenTTL(e *harness.Env, rememberMe bool) (time.Duration, error) {
	login := simLoginAttempt(e, e.AdminUser, e.AdminPass, rememberMe).Expect(http.StatusOK)
	claims, err := harness.DecodeJWTPayload(login.DataString("token"))
	if err != nil {
		return 0, err
	}
	exp, expOK := claims["exp"].(float64)
	iat, iatOK := claims["iat"].(float64)
	if !expOK || !iatOK {
		return 0, errors.New("JWT payload 缺少 exp 或 iat")
	}
	return time.Duration((exp - iat) * float64(time.Second)), nil
}

// authTamperSegment 篡改一个 base64url 段的**首字符**并返回结果，
// 用于构造"签名被篡改"的令牌。
//
// ⚠️ 2026-09-17 修复一处**会让本用例变成假红/假绿的真缺陷**（CI 两次失败之一）：
//
// 原实现把**最后一个字符**改成 'A'（若已是 'A' 则改 'B'）。但 JWT 第三段是
// base64url(无填充) 的 32 字节 HS256 签名，长度恰为 **43 字符**：
// 43×6 = 258 位，而有效载荷只有 256 位 ⇒ **末字符仅低 2 位参与解码，高 4 位被丢弃**。
// 当末字符本来就是 'A'（index 0）时，改成 'B'（index 1）后低 2 位相同 ⇒
// **解码后的签名逐字节不变** ⇒ 令牌根本没被篡改，服务端当然返回 200，
// 而用例却断言 401 ⇒ 报出「篡改令牌应返回 401，实际 200」的**假红**。
// 实测该情形概率 = 1/16 ≈ 6.25%（canonical 编码的末字符只可能是 16 个值中的
// AEIMQUYcgkosw048，其中仅 'A' 会触发），这正是两次 CI 红、而本地多轮绿的原因。
//
// ⇒ 修法：改**首字符**。base64url 首字符的 6 位**全部**有效，改它必然改变解码结果。
// 同时保留末字符处理路径的语义（若首字符已是 'A' 则改 'B'，保证确实变了）。
//
// 注意：ota.go 里另有一个 flipLastChar（同名不同文件），按设计 §4.1
// 的域前缀规则应由 ota 域自行改名；这里只改自己文件内的引用。
func authTamperSegment(value string) string {
	if value == "" {
		return value
	}
	// 改首字符而非末字符：末字符在 43 字符的 base64url 里只有低 2 位有效，
	// 改成 'A' 可能不改变解码结果（详见上方说明）。
	first := value[0]
	replacement := byte('A')
	if first == 'A' {
		replacement = 'B'
	}
	return string(replacement) + value[1:]
}
