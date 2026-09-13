package notify

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
)

// 出站安全的常量 (设计/外发通知通道.md §7 冻结)。
const (
	// DefaultTimeoutSec 是通道未配置 timeout_sec 时的出站超时。
	DefaultTimeoutSec = 10
	// MaxTimeoutSec 是出站超时的硬上限 (设计 §7.2: 默认 10s, 上限 60s)。
	MaxTimeoutSec = 60
	// MaxRedirects 是重定向跳数上限 (设计 §7.2: CheckRedirect 限制 3 跳)。
	MaxRedirects = 3
	// MaxResponseBytes 是响应体读取上限 (设计 §7.4: 如 4KB)。
	MaxResponseBytes = 4096
)

// ErrBlockedAddress 表示目标地址落在禁止出站的范围内 (private/loopback/
// link-local/ULA 等)。通道可显式开启 AllowPrivate 解除 (设计 §7.1 例外)。
type ErrBlockedAddress struct {
	Host   string // 原始主机名
	Addr   string // 解析出的具体 IP
	Reason string // 判定原因 (固定文案, 不含用户输入)
}

func (e *ErrBlockedAddress) Error() string {
	return fmt.Sprintf("outbound address blocked by SSRF policy: host %q resolved to %s (%s); enable allow_private on the channel only for a trusted LAN endpoint", e.Host, e.Addr, e.Reason)
}

// resolveEndpoint 解析并校验一个出站目标。
//
// 这是本仓唯一的"是否允许连出去"判定点, 被两处调用, 缺一不可:
//  1. 发送前 (Client.Post) —— 给出可读的拒绝原因, 不发出任何字节;
//  2. 拨号时 (DialContext 的 Control 回调) —— 见 client.go。http.Transport 自己在
//     DialContext 里解析域名, 因此"发送前解析"不能代表"真正连出去的那个 IP"
//     (DNS rebinding: 两次解析给出不同答案)。只有 (2) 是不可绕过的强制点。
//
// 校验对象永远是**解析出来的 IP**, 绝不拿主机名字符串做判断 ——
// "10.0.0.1.nip.io" / "localtest.me" 这类域名解析后就是内网地址。
func resolveEndpoint(ctx context.Context, rawURL string, allowPrivate bool) (*url.URL, bool, error) {
	target, err := ParseTargetURL(rawURL)
	if err != nil {
		return nil, false, err
	}
	if allowPrivate {
		// 显式开启的例外: 家庭内网 OneBot (http://192.168.x.x:5700/send_msg)。
		return target, true, nil
	}
	if err := AssertPublicHost(ctx, target.Hostname(), target.Port()); err != nil {
		return nil, false, err
	}
	return target, false, nil
}

// ParseTargetURL 校验 URL 形态并剥掉用户凭据 (凭据按设计走 Secret, 不进 URL 日志)。
func ParseTargetURL(raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("channel target_url is empty")
	}
	target, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("channel target_url is not a valid URL: %w", err)
	}
	switch target.Scheme {
	case "http", "https":
	default:
		return nil, fmt.Errorf("channel target_url scheme %q is not allowed (only http/https)", target.Scheme)
	}
	if target.Hostname() == "" {
		return nil, fmt.Errorf("channel target_url has no host")
	}
	if target.User != nil {
		target.User = nil
	}
	return target, nil
}

// AssertPublicHost 解析主机名并拒绝内网地址 (设计 §7.1)。
//
// 必须"先解析再判断": 主机名字符串校验会被 DNS rebinding 与数字/短写法绕过
// (例如 2130706433 = 127.0.0.1, 0x7f.1 = 127.0.0.1), 但 netip.ParseAddr
// 与 net.Resolver 的规范化结果落在同一批前缀判定上。
func AssertPublicHost(ctx context.Context, host, port string) error {
	if host == "" {
		return fmt.Errorf("channel target_url has no host")
	}
	addrs, err := resolveHost(ctx, host, port)
	if err != nil {
		return err
	}
	if len(addrs) == 0 {
		return fmt.Errorf("host %q resolved to no address", host)
	}
	for _, addr := range addrs {
		if reason, blocked := BlockedAddressReason(addr); blocked {
			return &ErrBlockedAddress{Host: host, Addr: addr.String(), Reason: reason}
		}
	}
	return nil
}

// resolveHost 先把主机名当字面量 IP 解析, 失败才走 DNS。返回结果一律是规范化 IP。
func resolveHost(ctx context.Context, host, port string) ([]netip.Addr, error) {
	if literal, err := netip.ParseAddr(host); err == nil {
		return []netip.Addr{literal.Unmap()}, nil
	}
	_ = port
	ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve host %q: %w", host, err)
	}
	normalized := make([]netip.Addr, 0, len(ips))
	for _, ip := range ips {
		normalized = append(normalized, ip.Unmap())
	}
	return normalized, nil
}

// BlockedAddressReason 判定一个 IP 是否属于禁止出站的地址范围。
// 返回 (原因, 是否禁止)。原因是固定文案, 不含用户输入, 可安全落日志/审计。
func BlockedAddressReason(addr netip.Addr) (string, bool) {
	if !addr.IsValid() {
		return "invalid address", true
	}
	// 4-in-6 (::ffff:10.0.0.1) 必须按 IPv4 判定, 否则会绕过 10/8 检查。
	ip := addr.Unmap()
	switch {
	case ip.Is4():
		v4 := ip.As4()
		switch {
		case v4[0] == 127:
			return "loopback (127.0.0.0/8)", true
		case v4[0] == 10:
			return "private (10.0.0.0/8)", true
		case v4[0] == 172 && v4[1] >= 16 && v4[1] <= 31:
			return "private (172.16.0.0/12)", true
		case v4[0] == 192 && v4[1] == 168:
			return "private (192.168.0.0/16)", true
		case v4[0] == 169 && v4[1] == 254:
			return "link-local (169.254.0.0/16)", true
		case v4[0] == 0:
			return "unspecified (0.0.0.0/8)", true
		case v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127:
			return "carrier-grade NAT (100.64.0.0/10)", true
		case v4[0] == 198 && (v4[1] == 18 || v4[1] == 19):
			return "benchmarking (198.18.0.0/15)", true
		case v4[0] >= 224:
			return "multicast/reserved (224.0.0.0/4)", true
		}
		return "", false
	case ip.Is6():
		switch {
		case ip.IsLoopback():
			return "loopback (::1)", true
		case ip.IsLinkLocalUnicast():
			return "link-local (fe80::/10)", true
		case ip.IsMulticast():
			return "multicast (ff00::/8)", true
		case ip.IsUnspecified():
			return "unspecified (::)", true
		case ip.Is4In6():
			return "IPv4-mapped address", true
		}
		// fc00::/7 = ULA, 含 fd00::/8。
		if ip.As16()[0]&0xfe == 0xfc {
			return "unique local (fc00::/7)", true
		}
		return "", false
	}
	return "unknown address family", true
}
