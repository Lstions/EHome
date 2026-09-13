package notify

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"sync"
	"time"

	"ehome/backend/internal/models"
)

// 出站客户端 (设计/外发通知通道.md §7)。这是本仓第一个运行时出站 HTTP 封装,
// 所有出站流量都必须经过它, 不得直接使用 http.DefaultClient。
//
// 硬编码的安全边界 (常量见 ssrf.go, 通道不能关闭):
//   - 超时: 默认 10s, 上限 60s (TimeoutSec);
//   - 重定向: 最多 3 跳, 且每一跳都重新做 SSRF 判定, 跨主机跳转丢弃 Authorization;
//   - 响应体: 最多读 4KB;
//   - SSRF: 解析出 IP 后再判定, 并且**按解析出的 IP 拨号** (防 DNS rebinding);
//     通道 allow_private=true 时只跳过地址判定, 其余边界不变。
//
// 代理被显式禁用 (Transport.Proxy = nil): 若走 HTTP(S)_PROXY, 实际连出的
// 目标由代理决定, 本进程的地址判定就形同虚设。

// Result 是一次出站 HTTP 尝试的结果。它不是 Go error: 投递失败是**被审计的
// 业务结果**, 不是要把调用方打挂的异常 (设计 §2.2 fail-open)。
type Result struct {
	// StatusCode 是收到的 HTTP 状态码; 未收到响应 (网络错误/超时/被拒) 时为 0。
	StatusCode int
	// OK 表示 2xx。
	OK bool
	// Retryable 表示这次失败值得重试 (网络错误/超时/5xx/429/408)。
	// SSRF 判定拒绝、400/401/404 这类"再试一次也不会变"的失败不重试。
	Retryable bool
	// Duration 是本次尝试的墙钟耗时。
	Duration time.Duration
	// Body 是响应体片段 (最多 MaxResponseBytes), 只作为返回值给调用方诊断,
	// 不写审计、不外发。
	Body string
	// Truncated 表示响应体超过上限被截断 (设计 §7.4)。
	Truncated bool
	// Err 是失败原因 (已脱敏)。OK=true 时为 nil。
	Err error
}

// Client 是无状态的出站 HTTP 客户端 (除测试钩子外)。
type Client struct {
	hookMu sync.RWMutex
	hook   DialHook
}

// DialHook 是测试用的拨号观察点: 它在 DNS 解析完成、地址判定之后,
// 真正建立 TCP 连接之前被调用。生产代码不设置它。
//
// 为什么钩子放在这里而不是"Post 之前": 只有这个位置能证明被测代码
// **在解析之后**做的判定 (DNS rebinding 防护的可观测点, 见 ssrf.go)。
type DialHook func(ctx context.Context, network, address string, allowPrivate bool, ips []netip.Addr) (net.Conn, error)

// NewClient 构造出站客户端。
func NewClient() *Client { return &Client{} }

// SetDialHook 注入测试拨号钩子。传 nil 清除。
func (c *Client) SetDialHook(hook DialHook) {
	c.hookMu.Lock()
	defer c.hookMu.Unlock()
	c.hook = hook
}

func (c *Client) dialHook() DialHook {
	c.hookMu.RLock()
	defer c.hookMu.RUnlock()
	return c.hook
}

// OutboundTimeout 把通道配置的 timeout_sec 归一化到 [DefaultTimeoutSec, MaxTimeoutSec]。
//
// nil = 用户没配 → 默认 10s; 0 或负数 = 退化为默认值 (不可能在 0 秒内完成一次
// 出站往返, 这里不存在"显式 0"的合法语义); 超过上限截断到 60s。
// 禁止无限等待 (设计 §7.2)。
func OutboundTimeout(timeoutSec *int) time.Duration {
	seconds := DefaultTimeoutSec
	if timeoutSec != nil && *timeoutSec > 0 {
		seconds = *timeoutSec
	}
	if seconds > MaxTimeoutSec {
		seconds = MaxTimeoutSec
	}
	return time.Duration(seconds) * time.Second
}

// Post 向通道地址投递一次 payload。任何失败都以 Result 返回, 不返回 Go error。
func (c *Client) Post(ctx context.Context, channel models.NotificationChannel, payload []byte) Result {
	start := time.Now()
	target, allowPrivate, err := resolveEndpoint(ctx, channel.TargetURL, channel.AllowPrivate)
	if err != nil {
		return Result{Duration: time.Since(start), Err: err}
	}

	timeout := OutboundTimeout(channel.TimeoutSec)
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target.String(), bytes.NewReader(payload))
	if err != nil {
		return Result{Duration: time.Since(start), Err: fmt.Errorf("build outbound request: %w", err)}
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "EHomeSystem-Notify/1.0")
	if secret := strings.TrimSpace(channel.Secret); secret != "" {
		// OneBot v11 的 access_token 与多数 webhook 的 Bearer 约定。
		// 企业微信的 key 在 URL 查询串里, 由 RedactURL 负责脱敏。
		req.Header.Set("Authorization", "Bearer "+secret)
	}

	client := &http.Client{
		Timeout:       timeout,
		Transport:     c.transport(allowPrivate),
		CheckRedirect: redirectPolicy(allowPrivate),
	}
	resp, err := client.Do(req)
	duration := time.Since(start)
	if err != nil {
		return Result{
			Duration:  duration,
			Retryable: isRetryableTransportError(err),
			Err:       fmt.Errorf("outbound request failed: %w", err),
		}
	}
	defer func() { _ = resp.Body.Close() }()

	body, truncated, readErr := ReadBoundedBody(resp.Body, MaxResponseBytes)
	result := Result{
		StatusCode: resp.StatusCode,
		OK:         resp.StatusCode >= 200 && resp.StatusCode < 300,
		Duration:   duration,
		Body:       body,
		Truncated:  truncated,
	}
	if result.OK {
		return result
	}
	result.Retryable = resp.StatusCode >= 500 ||
		resp.StatusCode == http.StatusTooManyRequests ||
		resp.StatusCode == http.StatusRequestTimeout
	// 错误文案只带状态码, 绝不回显响应体: 端点可能把我们发出去的密钥原样回显,
	// 而审计/日志里的任何字段都必须先过脱敏 (设计 §7.3/§7.5)。
	msg := fmt.Sprintf("endpoint returned HTTP %d", resp.StatusCode)
	if readErr != nil {
		msg += " (response body read failed)"
	}
	result.Err = errors.New(msg)
	return result
}

// transport 为每次投递构造独立 Transport: 连接绝不跨通道复用,
// 否则在 allow_private 通道上建立的连接可能被另一个通道拿去用。
func (c *Client) transport(allowPrivate bool) *http.Transport {
	return &http.Transport{
		Proxy:                 nil, // 见文件头: 代理会让本进程的地址判定失效
		DialContext:           c.dialContext(allowPrivate),
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 0, // 由 Client.Timeout / ctx 统一兜底
		DisableKeepAlives:     true,
		MaxIdleConns:          0,
	}
}

// dialContext 是 SSRF 防护的强制点。
//
// 关键: 域名在这里解析成 IP, 判定通过后**直接用该 IP 拨号**
// (net.JoinHostPort(ip.String(), port)), 而不是把主机名交给网络栈再解析一次。
// 因此"判定过的地址"与"真正连出去的地址"必然是同一个, DNS rebinding
// (第一次解析给公网 IP、第二次给 127.0.0.1) 无从下手。
func (c *Client) dialContext(allowPrivate bool) func(ctx context.Context, network, address string) (net.Conn, error) {
	dialer := &net.Dialer{Timeout: time.Duration(MaxTimeoutSec) * time.Second}
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, fmt.Errorf("outbound dial: invalid address %q: %w", address, err)
		}
		ips, err := resolveHost(ctx, host, port)
		if err != nil {
			return nil, err
		}
		if hook := c.dialHook(); hook != nil {
			return hook(ctx, network, address, allowPrivate, ips)
		}
		var lastErr error
		for _, ip := range ips {
			if !allowPrivate {
				if reason, blocked := BlockedAddressReason(ip); blocked {
					lastErr = &ErrBlockedAddress{Host: host, Addr: ip.String(), Reason: reason}
					continue // 绝不尝试连接被拒绝的地址
				}
			}
			conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if dialErr == nil {
				return conn, nil
			}
			lastErr = dialErr
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("host %q resolved to no usable address", host)
		}
		return nil, lastErr
	}
}

// redirectPolicy 限制跳数 (设计 §7.2) 并对每一跳重新做 SSRF 判定:
// 只判定初始 URL 的话, 一个公网端点可以 302 到 http://127.0.0.1:8500 绕过防护。
func redirectPolicy(allowPrivate bool) func(req *http.Request, via []*http.Request) error {
	return func(req *http.Request, via []*http.Request) error {
		if len(via) >= MaxRedirects {
			return fmt.Errorf("stopped after %d redirects", MaxRedirects)
		}
		if !allowPrivate {
			if err := AssertPublicHost(req.Context(), req.URL.Hostname(), req.URL.Port()); err != nil {
				return err
			}
		}
		// 跨主机跳转必须丢掉凭据, 否则密钥会跟着 302 跑到第三方主机。
		if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[len(via)-1].URL.Host) {
			req.Header.Del("Authorization")
		}
		return nil
	}
}

// ReadBoundedBody 读取响应体, 最多 limit 字节, 返回 (内容, 是否被截断)。
// 多读 1 字节用于区分"恰好 limit"与"超过 limit"。设计 §7.4。
func ReadBoundedBody(r io.Reader, limit int) (string, bool, error) {
	if limit <= 0 {
		limit = MaxResponseBytes
	}
	raw, err := io.ReadAll(io.LimitReader(r, int64(limit)+1))
	truncated := len(raw) > limit
	if truncated {
		raw = raw[:limit]
	}
	return string(raw), truncated, err
}

// isRetryableTransportError 区分"值得重试"的传输层失败:
// 调用方主动取消 (父 ctx 结束) 不重试; 其余 (超时/连接失败/DNS 失败) 值得重试。
func isRetryableTransportError(err error) bool {
	return !errors.Is(err, context.Canceled)
}
