package notify

import (
	"context"
	"encoding/binary"
	"net"
	"net/netip"
	"sync"
	"testing"
)

// 本文件是"被测代码主动外发到 mock 接收端"的测试范式基础设施。
//
// 本仓此前的 httptest.NewServer 全部是**入站**方向 (把被测自身挂成服务端),
// 这里是第一次把 httptest.NewServer 当**第三方接收端**用 (见 dispatcher_test.go),
// 并且额外提供一个可在测试内替换 DNS 应答的假解析器 —— 用来复现
// "第一次解析给公网 IP、第二次给 127.0.0.1" 的 DNS rebinding。

// fakeDNS 是一个最小的 DNS 应答器 (UDP, 只处理 A/AAAA 查询)。
// 它按查询顺序依次返回脚本化的 A 记录, 因此可以精确构造 rebinding 场景。
type fakeDNS struct {
	conn *net.UDPConn

	mu      sync.Mutex
	answers map[string][]netip.Addr // 名字 → 依次返回的地址
	index   map[string]int          // 名字 → 已经返回过几次
	queries map[string]int          // 名字 → A 查询次数
}

func newFakeDNS(t *testing.T) *fakeDNS {
	t.Helper()
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("启动假 DNS 服务失败: %v", err)
	}
	server := &fakeDNS{
		conn:    conn,
		answers: map[string][]netip.Addr{},
		index:   map[string]int{},
		queries: map[string]int{},
	}
	go server.serve()
	t.Cleanup(func() { _ = conn.Close() })
	return server
}

// script 为名字设置按顺序返回的 A 记录 (第 n 次查询返回第 n 个, 用尽后停留最后一个)。
func (s *fakeDNS) script(name string, addrs ...netip.Addr) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.answers[name] = addrs
}

// queryCount 返回该名字被 A 查询的次数 (证据: 判定确实发生在解析之后)。
func (s *fakeDNS) queryCount(name string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.queries[name]
}

func (s *fakeDNS) addr() string { return s.conn.LocalAddr().String() }

func (s *fakeDNS) serve() {
	buf := make([]byte, 1500)
	for {
		n, addr, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			return
		}
		if resp := s.respond(buf[:n]); resp != nil {
			_, _ = s.conn.WriteToUDP(resp, addr)
		}
	}
}

func (s *fakeDNS) respond(query []byte) []byte {
	if len(query) < 12 {
		return nil
	}
	name, next, ok := decodeDNSName(query, 12)
	if !ok || next+4 > len(query) {
		return nil
	}
	qtype := binary.BigEndian.Uint16(query[next:])
	qclass := binary.BigEndian.Uint16(query[next+2:])
	question := query[12 : next+4]

	var rdata [][]byte
	if qtype == 1 && qclass == 1 { // A
		s.mu.Lock()
		s.queries[name]++
		scripted := s.answers[name]
		var chosen netip.Addr
		if len(scripted) > 0 {
			idx := s.index[name]
			if idx >= len(scripted) {
				idx = len(scripted) - 1
			}
			chosen = scripted[idx]
			s.index[name] = idx + 1
		}
		s.mu.Unlock()
		if chosen.IsValid() {
			v4 := chosen.As4()
			rdata = append(rdata, v4[:])
		}
	}

	resp := make([]byte, 0, 512)
	resp = append(resp, query[0], query[1]) // 事务 ID
	resp = append(resp, 0x81, 0x80)         // QR=1, RD=1, RA=1, RCODE=0 (NOERROR)
	resp = append(resp, 0x00, 0x01)         // QDCOUNT=1
	resp = append(resp, byte(len(rdata)>>8), byte(len(rdata)))
	resp = append(resp, 0x00, 0x00, 0x00, 0x00) // NSCOUNT=0, ARCOUNT=0
	resp = append(resp, question...)
	for _, data := range rdata {
		resp = append(resp, 0xC0, 0x0C)             // 名字指针指向 question 的名字
		resp = append(resp, 0x00, 0x01)             // TYPE=A
		resp = append(resp, 0x00, 0x01)             // CLASS=IN
		resp = append(resp, 0x00, 0x00, 0x00, 0x3C) // TTL=60
		resp = append(resp, 0x00, byte(len(data)))
		resp = append(resp, data...)
	}
	return resp
}

// decodeDNSName 解析未压缩的域名 (查询报文里不会出现压缩指针)。
func decodeDNSName(msg []byte, offset int) (string, int, bool) {
	labels := make([]byte, 0, 64)
	pos := offset
	for {
		if pos >= len(msg) {
			return "", 0, false
		}
		length := int(msg[pos])
		pos++
		if length == 0 {
			break
		}
		if length&0xC0 != 0 || pos+length > len(msg) {
			return "", 0, false
		}
		if len(labels) > 0 {
			labels = append(labels, '.')
		}
		labels = append(labels, msg[pos:pos+length]...)
		pos += length
	}
	return string(labels), pos, true
}

// installFakeResolver 把 net.DefaultResolver 指向假 DNS。
// 返回恢复函数 (t.Cleanup 也会自动恢复)。
func installFakeResolver(t *testing.T, server *fakeDNS) {
	t.Helper()
	previous := net.DefaultResolver
	net.DefaultResolver = &net.Resolver{
		PreferGo: true,
		Dial: func(ctx context.Context, network, address string) (net.Conn, error) {
			// 无视 /etc/resolv.conf 里的服务器, 一律打到假 DNS。
			return net.Dial("udp", server.addr())
		},
	}
	restore := func() { net.DefaultResolver = previous }
	t.Cleanup(restore)
}
