//go:build simulation

package harness

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// WebhookReceiver：场景内**真实出站 HTTP** 的接收端（收件箱 + 端点）
// ---------------------------------------------------------------------------
//
// 存在理由（这是本工作包唯一的新增 harness 概念，必须先说清楚边界）：
//
// 外发通知通道（设计/外发通知通道.md）的核心承诺是"用户人不在系统前面时也能收到
// 告警"。证明它只能靠**真实收到一个 HTTP 请求**，而本仓的仿真纪律（框架 §7 红线 2）
// 禁止场景连真实 QQ/微信。因此需要第三个东西：一个"外部世界"的替身。
//
// 它**不是 mock**：没有打桩、没有替换产品代码里的任何东西。它就是候选的
// "公网端点"本身（所以放在 harness 里由场景负责**扮演云端**）。被测服务仍然
// 走完整的出站路径 —— DNS/字面 IP 解析 → SSRF 判定 → 拨号 → 超时/重定向/重试
// → 脱敏 → 审计。场景改变的是"互联网那一端是谁"，不是"服务怎么连出去"。
//
// 地址与 SSRF 的关系（如实说明，不含糊）：
//   - 监听 127.0.0.1 的临时端口（:0），loopback 正是 SSRF 判定**默认拒绝**的地址
//     （ssrf.go BlockedAddressReason: "loopback (127.0.0.0/8)"）。
//   - 因此指向本接收端的通道**必须** allow_private=true。这正是设计 §7.1 为
//     "家庭内网 OneBot（http://192.168.x.x:5700/send_msg）"冻结的那个例外：
//     本接收端扮演的就是"家庭内网里的一台机器"，语义完全对齐。
//   - 反向价值：本接收端顺带给 SSRF 门禁提供了一个**活的白名单断言点** ——
//     同一地址在 allow_private=false 时收不到任何请求（见 SIM-NTFY-004 的第二段）。
type WebhookReceiver struct {
	server *http.Server
	base   string // 形如 http://127.0.0.1:34567，不带尾斜杠

	mu       sync.Mutex
	requests []ReceivedRequest
	// responder 按"第 N 个到达的请求"决定响应；nil = 一律 200 + 短 JSON。
	responder func(seq int, body string) ReceiverReply
}

// ReceivedRequest 是接收端收到的一次真实 HTTP 往返的完整记录。
// 断言必须建立在这些字段上，而不是"服务日志里有一行"。
type ReceivedRequest struct {
	Seq        int // 从 1 开始的到达序号（用于"重试发生了 N 次"这类断言）
	Method     string
	Path       string
	Query      string
	Header     http.Header
	Body       string
	Remote     string
	ReceivedAt time.Time
}

// ReceiverReply 是接收端对某次请求的应答。
type ReceiverReply struct {
	Status int // 0 → 200
	Body   string
	// Delay 在写出响应前等待；用于验证"超时不阻塞主流程"（SIM-NTFY-006）。
	// 实现时**必须**尊重请求 context：客户端超时后立刻返回，否则服务端会
	// 一直占着连接，场景的收尾（Stop）会被拖住。
	Delay time.Duration
}

// NewWebhookReceiver 起一个监听 loopback 临时端口的真实 HTTP 服务。
//
// 收尾由 t.Cleanup 保证（与 harness 其余资源同一纪律）：场景不需要自己关。
func NewWebhookReceiver(t interface {
	Helper()
	Cleanup(func())
	Fatalf(string, ...any)
	Logf(string, ...any)
}) *WebhookReceiver {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("启动 webhook 接收端失败（loopback 监听被拒）: %v", err)
	}
	receiver := &WebhookReceiver{
		base: fmt.Sprintf("http://%s", listener.Addr().String()),
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", receiver.handle)
	receiver.server = &http.Server{
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() {
		_ = receiver.server.Serve(listener)
	}()
	t.Cleanup(func() { receiver.Stop() })
	t.Logf("webhook 接收端已监听 %s", receiver.base)
	return receiver
}

// handle 记录请求后再应答，顺序不可颠倒：先记录，即使客户端中途超时断开，
// "服务端收到了这次请求"这一事实也已经留下（超时场景的证据只能来自这里）。
func (r *WebhookReceiver) handle(w http.ResponseWriter, req *http.Request) {
	raw, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	body := string(raw)

	r.mu.Lock()
	seq := len(r.requests) + 1
	responder := r.responder
	record := ReceivedRequest{
		Seq:        seq,
		Method:     req.Method,
		Path:       req.URL.Path,
		Query:      req.URL.RawQuery,
		Header:     req.Header.Clone(),
		Body:       body,
		Remote:     req.RemoteAddr,
		ReceivedAt: time.Now().UTC(),
	}
	r.requests = append(r.requests, record)
	r.mu.Unlock()

	reply := ReceiverReply{Status: http.StatusOK, Body: "{\"ok\":true}"}
	if responder != nil {
		reply = responder(seq, body)
	}
	if reply.Status == 0 {
		reply.Status = http.StatusOK
	}
	if reply.Body == "" {
		reply.Body = "{\"ok\":true}"
	}
	if reply.Delay > 0 {
		// 尊重客户端取消：请求 context 结束时立刻放弃等待。
		select {
		case <-time.After(reply.Delay):
		case <-req.Context().Done():
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(reply.Status)
	_, _ = io.WriteString(w, reply.Body)
}

// URL 返回接收端根地址（形如 http://127.0.0.1:34567）。
func (r *WebhookReceiver) URL() string { return r.base }

// Endpoint 返回接收端上的一个具体路径，例如 Endpoint("/alerts")。
func (r *WebhookReceiver) Endpoint(path string) string {
	if path == "" {
		return r.base
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	return r.base + path
}

// SetResponder 安装应答策略（按到达序号）。传 nil 恢复"一律 200"。
// 必须在触发投递**之前**设置，否则会出现"偶尔 200 偶尔 500"的不确定现场。
func (r *WebhookReceiver) SetResponder(fn func(seq int, body string) ReceiverReply) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.responder = fn
}

// Requests 返回已收到请求的快照（副本，调用方可安全遍历）。
func (r *WebhookReceiver) Requests() []ReceivedRequest {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ReceivedRequest, len(r.requests))
	copy(out, r.requests)
	return out
}

// Count 是已收到请求数。
func (r *WebhookReceiver) Count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.requests)
}

// Reset 清空收件箱（应答策略保留），用于同一场景内的分段断言。
func (r *WebhookReceiver) Reset() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.requests = nil
}

// WaitForCount 轮询等待收件箱达到 want 条，返回快照。
// 场景里**不允许** sleep 后直接断言（框架 §3 原则 3），因此等待必须走这里。
func (r *WebhookReceiver) WaitForCount(timeout time.Duration, want int) ([]ReceivedRequest, error) {
	deadline := time.Now().Add(timeout)
	for {
		got := r.Requests()
		if len(got) >= want {
			return got, nil
		}
		if time.Now().After(deadline) {
			return got, fmt.Errorf("等待 %s 后 webhook 接收端仍只收到 %d 条请求，期望 ≥%d", timeout, len(got), want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// WaitForMatching 轮询等待出现一条满足 match 的请求。
func (r *WebhookReceiver) WaitForMatching(timeout time.Duration, match func(ReceivedRequest) bool) (ReceivedRequest, error) {
	deadline := time.Now().Add(timeout)
	var last ReceivedRequest
	for {
		for _, item := range r.Requests() {
			if match(item) {
				return item, nil
			}
			last = item
		}
		if time.Now().After(deadline) {
			return last, fmt.Errorf("等待 %s 后仍未收到匹配的 webhook 请求（已收到 %d 条）", timeout, r.Count())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// Stop 关闭接收端。幂等；也由 t.Cleanup 调用。
func (r *WebhookReceiver) Stop() {
	if r == nil || r.server == nil {
		return
	}
	_ = r.server.Close()
}

// BodyJSON 把某条请求的 body 解析成 map（诊断用，不做断言）。
func (r ReceivedRequest) BodyJSON() map[string]any {
	var parsed map[string]any
	if err := json.Unmarshal([]byte(r.Body), &parsed); err != nil {
		return nil
	}
	return parsed
}

// String 给失败信息用的一行摘要（**不含 header**，避免把 Authorization 打进日志）。
func (r ReceivedRequest) String() string {
	return fmt.Sprintf("#%d %s %s?%s body=%s", r.Seq, r.Method, r.Path, r.Query, receiverHead(r.Body, 160))
}

// receiverHead 截断长文本供失败信息使用（本包不引入 catalog 的 autoHead，
// harness 是被依赖方，不能反向依赖 catalog）。
func receiverHead(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	return text[:limit] + fmt.Sprintf("…(截断，共 %d 字节)", len(text))
}
