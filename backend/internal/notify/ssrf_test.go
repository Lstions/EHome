package notify

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// TestBlockedAddressReason 表驱动覆盖设计 §7.1 冻结的全部地址范围。
// 判定必须只依赖 IP, 因此这些用例全部不依赖网络。
func TestBlockedAddressReason(t *testing.T) {
	blocked := []struct {
		addr string
		want string // 期望原因里包含的片段
	}{
		{"127.0.0.1", "loopback"},
		{"127.13.14.15", "loopback"},
		{"10.0.0.5", "10.0.0.0/8"},
		{"10.255.255.254", "10.0.0.0/8"},
		{"172.16.0.1", "172.16.0.0/12"},
		{"172.31.255.254", "172.16.0.0/12"},
		{"192.168.1.100", "192.168.0.0/16"},
		{"169.254.10.10", "169.254.0.0/16"},
		{"0.0.0.0", "unspecified"},
		{"100.64.0.1", "carrier-grade NAT"},
		{"198.18.0.1", "benchmarking"},
		{"239.1.1.1", "multicast/reserved"},
		{"::ffff:127.0.0.1", "loopback"},
		{"::ffff:10.1.2.3", "10.0.0.0/8"},
		{"::ffff:192.168.0.9", "192.168.0.0/16"},
		{"::1", "loopback"},
		{"fc00::1", "fc00::/7"},
		{"fd12:3456:789a::1", "fc00::/7"},
		{"fe80::1", "link-local"},
		{"ff02::1", "multicast"},
		{"::", "unspecified"},
	}
	for _, tc := range blocked {
		t.Run("blocked/"+tc.addr, func(t *testing.T) {
			addr := mustAddr(t, tc.addr)
			reason, isBlocked := BlockedAddressReason(addr)
			if !isBlocked {
				t.Fatalf("%s 必须被判定为禁止出站, 实际放行", tc.addr)
			}
			if !strings.Contains(reason, tc.want) {
				t.Fatalf("%s 的判定原因 = %q, 期望包含 %q", tc.addr, reason, tc.want)
			}
		})
	}

	allowed := []string{
		"1.1.1.1", "8.8.8.8", "93.184.216.34",
		"172.15.255.255", "172.32.0.1", // 172.16/12 的两个边界之外
		"192.167.255.255", "192.169.0.1",
		"11.0.0.1", "126.255.255.255", "128.0.0.1",
		"2606:4700:4700::1111", "2001:db8::1",
	}
	for _, raw := range allowed {
		t.Run("allowed/"+raw, func(t *testing.T) {
			if reason, isBlocked := BlockedAddressReason(mustAddr(t, raw)); isBlocked {
				t.Fatalf("公网地址 %s 被误判为禁止出站: %s", raw, reason)
			}
		})
	}
}

// TestAssertPublicHostRejectsInternalHosts 是"解析后校验 IP"的核心用例:
// 主机名一律先解析成 IP 再判定, 因此字面量 / 数字编码 / 域名三种写法
// 走的是同一条判定路径 (设计 §7.1)。
func TestAssertPublicHostRejectsInternalHosts(t *testing.T) {
	cases := []struct{ name, host string }{
		{"IPv4 字面量 loopback", "127.0.0.1"},
		{"IPv4 字面量 10/8", "10.0.0.5"},
		{"IPv4 字面量 172.16/12", "172.20.1.1"},
		{"IPv4 字面量 192.168/16", "192.168.1.100"},
		{"IPv4 字面量 link-local", "169.254.10.10"},
		{"IPv4-mapped IPv6 的 10/8", "::ffff:10.9.9.9"},
		{"IPv6 字面量 loopback", "::1"},
		{"IPv6 字面量 ULA", "fd00::1"},
		{"IPv6 字面量 link-local", "fe80::1"},
		{"域名解析后为 loopback", "localhost"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := AssertPublicHost(context.Background(), tc.host, "8080")
			if err == nil {
				t.Fatalf("主机 %q 必须被 SSRF 策略拒绝, 实际放行", tc.host)
			}
			var blocked *ErrBlockedAddress
			if !errors.As(err, &blocked) {
				t.Fatalf("主机 %q 的错误类型 = %T (%v), 期望 *ErrBlockedAddress", tc.host, err, err)
			}
			// 核心证据: 判定对象是**解析出来的 IP**, 而不是主机名字符串 ——
			// ErrBlockedAddress.Addr 必须是一个可解析的 IP, 且它本身就是被禁止的那种。
			resolved, parseErr := netip.ParseAddr(blocked.Addr)
			if parseErr != nil {
				t.Fatalf("拒绝证据 Addr=%q 不是 IP (说明判定用的是主机名): %v", blocked.Addr, parseErr)
			}
			if _, isBlocked := BlockedAddressReason(resolved); !isBlocked {
				t.Fatalf("拒绝证据 Addr=%q 按地址判定并未被禁止", blocked.Addr)
			}
		})
	}

	// 数字 / 十六进制编码的 loopback (2130706433、0x7f.1) 必须被拒绝。
	// 在 Go 的标准解析器下它们通常在 DNS 阶段就 "no such host"; 若某个平台真的
	// 解析成功, 走的就是上面同一条地址判定路径 (ErrBlockedAddress)。两种拒绝
	// 都满足"不放行", 因此这里只断言拒绝, 并接受两种原因之一。
	t.Run("编码写法的 loopback 一律拒绝", func(t *testing.T) {
		for _, host := range []string{"2130706433", "0x7f.1", "0177.0.0.1"} {
			err := AssertPublicHost(context.Background(), host, "8080")
			if err == nil {
				t.Fatalf("编码写法的主机 %q 必须被拒绝, 实际放行", host)
			}
			var blocked *ErrBlockedAddress
			if errors.As(err, &blocked) {
				continue
			}
			if !strings.Contains(err.Error(), "resolve host") {
				t.Fatalf("主机 %q 的拒绝原因既不是地址判定也不是解析失败: %v", host, err)
			}
		}
	})

	t.Run("公网地址放行", func(t *testing.T) {
		for _, host := range []string{"1.1.1.1", "8.8.8.8", "2606:4700:4700::1111"} {
			if err := AssertPublicHost(context.Background(), host, "443"); err != nil {
				t.Fatalf("公网地址 %s 被拒绝: %v", host, err)
			}
		}
	})

	t.Run("空主机名拒绝", func(t *testing.T) {
		if err := AssertPublicHost(context.Background(), "", "80"); err == nil {
			t.Fatal("空主机名必须被拒绝")
		}
	})
}

// TestResolveEndpointAllowPrivate 验证设计 §7.1 的例外开关:
// 默认关闭时拒绝, 显式开启时放行 (家庭内网 OneBot 的合法场景)。
func TestResolveEndpointAllowPrivate(t *testing.T) {
	internal := []string{
		"http://192.168.1.10:5700/send_msg",
		"http://127.0.0.1:8080/hook",
		"http://[fd00::1]:8080/hook",
		"http://localhost:8080/hook",
	}
	for _, raw := range internal {
		t.Run("默认拒绝/"+raw, func(t *testing.T) {
			if _, _, err := resolveEndpoint(context.Background(), raw, false); err == nil {
				t.Fatalf("默认配置下 %s 必须被拒绝", raw)
			}
		})
		t.Run("allow_private 放行/"+raw, func(t *testing.T) {
			target, allowPrivate, err := resolveEndpoint(context.Background(), raw, true)
			if err != nil {
				t.Fatalf("allow_private=true 时 %s 必须放行, 实际: %v", raw, err)
			}
			if !allowPrivate {
				t.Fatalf("allow_private=true 时必须返回已放宽标记")
			}
			if target.Host == "" {
				t.Fatalf("放行后必须返回可用目标")
			}
		})
	}

	t.Run("allow_private 不影响 URL 形态校验", func(t *testing.T) {
		for _, raw := range []string{"", "   ", "ftp://example.com/x", "file:///etc/passwd", "http://"} {
			if _, _, err := resolveEndpoint(context.Background(), raw, true); err == nil {
				t.Fatalf("非法地址 %q 即使在 allow_private 下也必须被拒绝", raw)
			}
		}
	})

	t.Run("URL 中的用户凭据被剥掉", func(t *testing.T) {
		target, _, err := resolveEndpoint(context.Background(), "http://user:pass@1.1.1.1/hook", false)
		if err != nil {
			t.Fatalf("公网地址应放行: %v", err)
		}
		if target.User != nil {
			t.Fatalf("URL 内嵌凭据必须被剥掉, 实际保留 %v", target.User)
		}
	})
}

func mustAddr(t *testing.T, raw string) netip.Addr {
	t.Helper()
	parsed, err := netip.ParseAddr(raw)
	if err != nil {
		t.Fatalf("测试用例地址 %q 非法: %v", raw, err)
	}
	return parsed
}
