//go:build simulation

package harness

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

// WSConn 是一条真实的 WebSocket 连接。
//
// 为什么放在 harness 而不是各场景自建：连接建立、握手失败诊断、
// 事件读取与超时是每个实时类场景都要重复的样板；
// 更重要的是"未携带有效令牌必须被拒绝"这条断言需要能拿到
// **握手阶段的 HTTP 状态码**（gorilla 把它放在 ErrUnexpectedResponse 里），
// 自行实现极易把它降级成一句模糊的"连接失败"。
type WSConn struct {
	conn   *websocket.Conn
	events chan map[string]any
	errs   chan error
	once   sync.Once
	closed chan struct{}

	// URL 是实际拨号的地址（含 token 查询参数，已脱敏前的原文）。
	URL string
}

// WSURL 构造带令牌的 WebSocket 地址（**唯一**的地址构造入口）。
//
// 令牌以 ?token= 传递：middleware.go 的 extractToken 支持该查询参数回退
// 分支，因为浏览器 WebSocket API 无法设置 Authorization 请求头。
// withToken=false 时用于验证"未携带有效令牌必须被拒绝"。
//
// scheme 归一化是必须的：Session.BaseURL 是 http://…，而 gorilla/websocket
// 的 Dialer.Dial 只接受 ws/wss —— 原样透传会让**每一次**拨号都在客户端
// 就以 "malformed ws or wss URL" 失败（连 HTTP 请求都没发出去，
// 表现为响应为 nil、状态码 0）。这里显式转换并在最后自检，
// 绝不静默返回一个必然失败的地址。
func (s *Session) WSURL(path string, withToken bool) (string, error) {
	if path == "" {
		path = "/api/v1/ws"
	}
	target, err := url.Parse(s.BaseURL + path)
	if err != nil {
		return "", fmt.Errorf("解析 WebSocket 地址失败: %w", err)
	}
	switch target.Scheme {
	case "http":
		target.Scheme = "ws"
	case "https":
		target.Scheme = "wss"
	}
	if withToken && s.Token != "" {
		query := target.Query()
		query.Set("token", s.Token)
		target.RawQuery = query.Encode()
	}
	address := target.String()
	if !strings.HasPrefix(address, "ws://") && !strings.HasPrefix(address, "wss://") {
		return "", fmt.Errorf("WebSocket 地址 %q 的 scheme 不是 ws/wss（BaseURL=%q）", address, s.BaseURL)
	}
	return address, nil
}

// DialWS 是原始拨号助手：直接返回 gorilla 连接与握手响应，
// 让调用方能拿到握手阶段的 HTTP 状态码（未授权场景需要断言 401）。
// 带超时的事件读取请用 DialWSConn。
func (e *Env) DialWS(path string) (*websocket.Conn, *http.Response, error) {
	if e.Admin == nil {
		return nil, nil, fmt.Errorf("Env.Admin 为 nil，无法建立已鉴权的 WebSocket 连接")
	}
	target, err := e.Admin.WSURL(path, true)
	if err != nil {
		return nil, nil, err
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	return dialer.Dial(target, nil)
}

// DialWSConn 用当前会话的令牌连接 WebSocket，返回带事件读取能力的封装。
func (s *Session) DialWSConn(path string) (*WSConn, error) {
	target, err := s.WSURL(path, true)
	if err != nil {
		return nil, err
	}
	dialer := websocket.Dialer{HandshakeTimeout: 10 * time.Second}
	conn, resp, err := dialer.Dial(target, nil)
	if err != nil {
		status := 0
		body := ""
		if resp != nil {
			status = resp.StatusCode
			if resp.Body != nil {
				buffer := make([]byte, 1024)
				n, _ := resp.Body.Read(buffer)
				body = string(buffer[:n])
				resp.Body.Close()
			}
		}
		return nil, &WSDialError{URL: redactToken(target), Status: status, Body: body, Err: err}
	}

	ws := &WSConn{
		conn:   conn,
		events: make(chan map[string]any, 256),
		errs:   make(chan error, 1),
		closed: make(chan struct{}),
		URL:    redactToken(target),
	}
	go ws.readLoop()
	return ws, nil
}

// DialWSConn 用管理员会话连接 WebSocket（绝大多数实时场景的入口）。
func (e *Env) DialWSConn(path string) (*WSConn, error) {
	if e.Admin == nil {
		return nil, fmt.Errorf("Env.Admin 为 nil，无法建立已鉴权的 WebSocket 连接")
	}
	return e.Admin.DialWSConn(path)
}

// WSDialError 保留握手阶段的 HTTP 状态码与响应体，
// 让"未携带有效令牌被拒绝"能被断言成 401 而不是"连接失败"。
type WSDialError struct {
	URL    string
	Status int
	Body   string
	Err    error
}

func (e *WSDialError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("WebSocket 拨号 %s 失败（无 HTTP 响应）: %v", e.URL, e.Err)
	}
	return fmt.Sprintf("WebSocket 拨号 %s 被拒绝: HTTP %d body=%s", e.URL, e.Status, e.Body)
}

func (e *WSDialError) Unwrap() error { return e.Err }

func (c *WSConn) readLoop() {
	defer close(c.closed)
	for {
		_, payload, err := c.conn.ReadMessage()
		if err != nil {
			select {
			case c.errs <- err:
			default:
			}
			return
		}
		var event map[string]any
		if json.Unmarshal(payload, &event) != nil {
			// 非 JSON 帧（例如心跳文本）跳过，不污染事件流。
			continue
		}
		select {
		case c.events <- event:
		case <-c.closed:
			return
		}
	}
}

// EventTypeOf 兼容多种事件信封形态，取出事件类型。
// 后端 websocket.Hub 广播的载荷里类型字段名不止一种（type / event），
// harness 统一在此归一，避免每个场景各写一遍。
func EventTypeOf(event map[string]any) string {
	for _, key := range []string{"type", "event", "event_type"} {
		if value, ok := event[key].(string); ok && value != "" {
			return value
		}
	}
	return ""
}

// AwaitEvent 轮询等待指定类型的事件（设计 §3 原则 3：不用 sleep 同步）。
// 返回的事件是原始 map，场景自行按点路径断言。
func (c *WSConn) AwaitEvent(eventType string, timeout time.Duration) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	seen := make([]string, 0, 16)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("等待 WebSocket 事件 %q 超时 %s；期间收到: [%s]",
				eventType, timeout, strings.Join(seen, ", "))
		}
		select {
		case event := <-c.events:
			got := EventTypeOf(event)
			seen = append(seen, got)
			if got == eventType {
				return event, nil
			}
		case err := <-c.errs:
			return nil, fmt.Errorf("WebSocket 连接中断: %w（期间收到: [%s]）", err, strings.Join(seen, ", "))
		case <-time.After(minDuration(remaining, 200*time.Millisecond)):
		}
	}
}

// AwaitEventWhere 等待第一个满足 predicate 的事件（同类型事件很多时使用，
// 例如只关心某个 node_id 的 data_update）。
func (c *WSConn) AwaitEventWhere(eventType string, timeout time.Duration, predicate func(map[string]any) bool) (map[string]any, error) {
	deadline := time.Now().Add(timeout)
	for {
		remaining := time.Until(deadline)
		if remaining <= 0 {
			return nil, fmt.Errorf("等待满足条件的 WebSocket 事件 %q 超时 %s", eventType, timeout)
		}
		event, err := c.AwaitEvent(eventType, minDuration(remaining, time.Second))
		if err != nil {
			if time.Now().After(deadline) {
				return nil, err
			}
			continue
		}
		if predicate == nil || predicate(event) {
			return event, nil
		}
	}
}

// Close 关闭连接（幂等）。
func (c *WSConn) Close() {
	c.once.Do(func() {
		_ = c.conn.Close()
	})
}

// redactToken 从 URL 中抹掉 token，避免它进入日志与证据文件。
func redactToken(raw string) string {
	parsed, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	query := parsed.Query()
	if query.Has("token") {
		query.Set("token", "***")
		parsed.RawQuery = query.Encode()
	}
	return parsed.String()
}

func minDuration(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
