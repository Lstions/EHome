package main

// device_e2e_firmware_test.go -- 跨语言对锚：**真实固件 C** ↔ **真实后端 Go**，走真实 socket。
//
// # 为什么需要它（本项目 40+ 轮的登记事项）
//
// protocol/vectors/wire_primitives.txt 自己写着：
//   "后端 Go 与固件 C 各自独立实现了同一份字段约定，而两边**从未在真实链路上对话过**。
//    两端各自单测全绿、合起来却不通 —— 正是 S0 要在重构中拦住的事。"
//
// 共享向量解决了"字节是否一致"，但**解决不了流上的行为**：
// 一次 read 拿到半条帧、或一次 read 拿到两条半帧 —— 这正是 D-09 那一类缺陷所在的层面
// （e2e 之外，向量测试对"分片"完全无感，因为向量是**整块**字节）。
//
// # 这个测试真正新测的是什么
//
//   1. 固件 C（真实 session/rx_pump/wire 栈）发出的字节，能否被**后端真实解码器**接受
//      （protoframe.DecodeHeader + frame.DecodeDeviceOpAck，都是生产代码）；
//   2. 后端真实编码器产出的 0x22 帧，**被拆成每次 1 字节写入**时，
//      固件 C 的真实接收路径能否正确组装（这是向量测不到的那一半）；
//   3. 真实路由器（nodemgr.Manager.HandleFrame）收到这些字节不炸。
//
// # 诚实边界（必须写明，否则后人会以为"整链都测过了"）
//
//   - **TLS 不在此测试内**：固件的 esp_tls 是 IDF-only，宿主跑不了；
//     而后端 handleConn 明确要求 *tls.Conn（server.go:374）并从**客户端证书**取 nodeID。
//     TLS/监听/会话层由既有的纯 Go 测试覆盖（device_e2e_test.go，真 mTLS 回环）。
//   - **不是真设备**：跑的是固件**源码**在宿主编译出的程序，没有 ESP32、没有 WiFi。
//   - 因此本测试与 device_e2e_test.go 是**互补**关系，各自覆盖不同的接缝。

import (
	"bytes"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/protoframe"
)

// 共享向量里的精确字节（protocol/vectors/wire_primitives.txt，device_op 一组）。
// 硬编码在这里是**故意**的：本测试要证明"两端对同一串字节的理解一致"，
// 若从向量文件读，就变成了"都读同一个文件"，反而绕过了"各自独立实现"这件事。
// 下面另有断言把这些常量与向量文件钉在一起（见 TestSharedVectorBytesMatchConstants）。
const (
	vectorAckOKPayload   = "230800120a6f702d6e6f6465312d31" // 0x23 result=0 request_id=op-node1-1
	vectorRebootPayload  = "220801120a6f702d6e6f6465312d31" // 0x22 op_code=1 request_id=op-node1-1
	vectorRequestID      = "op-node1-1"
	msgTypeDeviceOp      = 0x22
	msgTypeDeviceOpAck   = 0x23
)

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	b := make([]byte, len(s)/2)
	for i := 0; i < len(b); i++ {
		var v byte
		for _, c := range s[i*2 : i*2+2] {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= byte(c - '0')
			case c >= 'a' && c <= 'f':
				v |= byte(c-'a') + 10
			default:
				t.Fatalf("bad hex digit %q in %q", c, s)
			}
		}
		b[i] = v
	}
	return b
}

// readOneFrameFromStream 用**生产解码器** protoframe.DecodeHeader 读一条帧。
// 与 server.go 的读循环同源（都用 DecodeHeader），只是这里不涉及 TLS。
func readOneFrameFromStream(t *testing.T, r io.Reader) (protoframe.Header, []byte) {
	t.Helper()
	var hdr [protoframe.HeaderSize]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		t.Fatalf("读帧头失败（固件客户端没发出完整帧头？）: %v", err)
	}
	h, err := protoframe.DecodeHeader(hdr[:])
	if err != nil {
		t.Fatalf("DecodeHeader 拒绝了固件发来的帧头 %x: %v", hdr, err)
	}
	payload := make([]byte, h.PayloadLen)
	if _, err := io.ReadFull(r, payload); err != nil {
		t.Fatalf("读载荷(%d B)失败: %v", h.PayloadLen, err)
	}
	if h.HasCRC() {
		var crc [protoframe.CRCSize]byte
		if _, err := io.ReadFull(r, crc[:]); err != nil {
			t.Fatalf("读 CRC 失败: %v", err)
		}
	}
	return h, payload
}

// TestCrossLanguageFirmwareClientOverSocket 是主用例。
func TestCrossLanguageFirmwareClientOverSocket(t *testing.T) {
	bin := os.Getenv("EHOME_FW_E2E_CLIENT")
	if bin == "" {
		// 刻意 **Skip 而不是静默通过**，并指明由哪个脚本负责真正的运行 ——
		// 一个"没配环境就悄悄绿"的测试比没有测试更危险（见 silent-failure-taxonomy 族 2）。
		t.Skip("EHOME_FW_E2E_CLIENT 未设置：本用例需固件侧客户端。" +
			"请用 tools/run_firmware_backend_e2e.sh 运行（它会先构建再设置该变量）")
	}
	if _, err := os.Stat(bin); err != nil {
		// 变量**已设置但文件不存在** ⇒ 这是配置错误，必须失败而不是跳过。
		t.Fatalf("EHOME_FW_E2E_CLIENT=%q 指向的文件不存在: %v", bin, err)
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer ln.Close()
	port := ln.Addr().(*net.TCPAddr).Port

	// 启动固件客户端（真实固件源码编译出的宿主程序）
	cmd := exec.Command(bin, "127.0.0.1", itoa(port))
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	if err := cmd.Start(); err != nil {
		t.Fatalf("启动固件客户端失败: %v", err)
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	// ⚠ finished 必须用**独立布尔**，不能靠 `waitDone = nil` 表示"已回收"。
	//
	// 我第一版就是 `waitDone = nil`，结果客户端**正常退出**（也就是对锚成功）那条路必然死锁：
	// 主 select 收走后把 channel 置 nil ⇒ 下面的 defer 里 `select` 走 default
	// ⇒ `cmd.Process.Kill()`（杀一个已退出的进程，无害）⇒ 然后 `<-waitDone` 从
	// **nil channel** 接收 ⇒ **永久阻塞**。
	// 后果比"测试慢"严重得多：**成功与失败两条路都死锁** ⇒ 该测试无论成败都无法报告结论，
	// 而 CI 看到的是"挂住"而不是"失败"——正是本项目最忌讳的"看不出来"的形态。
	// （由 v3-firmware 用 SIGQUIT 取 goroutine 栈定位：`chan receive (nil chan)`。）
	// 教训：**置 nil 是"让 channel 永不就绪"，不是"标记已完成"**；标记要用布尔。
	finished := false
	defer func() {
		if !finished {
			_ = cmd.Process.Kill()
			<-waitDone
		}
		// 固件侧的原始输出永远打出来 —— 失败时它是唯一的线索。
		t.Logf("── 固件客户端输出 ──\n%s", out.String())
	}()

	_ = ln.(*net.TCPListener).SetDeadline(time.Now().Add(20 * time.Second))
	conn, err := ln.Accept()
	if err != nil {
		t.Fatalf("固件客户端没有连上来: %v", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(20 * time.Second))

	// ── 方向 1：固件 C → 后端 Go ──
	// 用**生产解码器**解析固件真实栈发出的字节。
	h, payload := readOneFrameFromStream(t, conn)
	if h.Type != msgTypeDeviceOpAck {
		t.Fatalf("帧类型 = 0x%02X, 期望 0x%02X（0x23 ACK）；固件发的头: %v", h.Type, msgTypeDeviceOpAck, h)
	}
	if h.Ver != protoframe.Version {
		t.Fatalf("ver = 0x%02X, 期望 0x%02X", h.Ver, protoframe.Version)
	}
	if h.HasCRC() {
		t.Fatalf("固件不该置 CRC 位（本向量未定义 CRC），flags=0x%04X", h.Flags)
	}
	ack, err := frame.DecodeDeviceOpAck(payload)
	if err != nil {
		t.Fatalf("生产解码器解不了固件发来的 0x23 载荷 %x: %v", payload, err)
	}
	if got := ack.RequestID; got != vectorRequestID {
		t.Errorf("request_id = %q, 期望 %q（载荷 %x）", got, vectorRequestID, payload)
	}
	if !bytes.Equal(payload, mustHex(t, vectorAckOKPayload)) {
		t.Errorf("固件发的 0x23 载荷 = %x, 期望共享向量 %s", payload, vectorAckOKPayload)
	}
	// 载荷首字节必须等于头里的 type（已知的"双写"，本仓现状）。
	if len(payload) == 0 || payload[0] != h.Type {
		t.Errorf("载荷首字节(%x) 与头 type(0x%02X) 不一致 —— 这是已知双写约定，两端都必须一致",
			firstByte(payload), h.Type)
	}
	t.Logf("方向1 OK：固件→后端 type=0x%02X seq=%d plen=%d request_id=%q",
		h.Type, h.Seq, h.PayloadLen, ack.RequestID)

	// ── 方向 2：后端 Go → 固件 C，**每次 1 字节写入** ──
	// 这是本测试的核心价值：向量测试是整块字节，永远测不到"半条帧"。
	// 逐字节写强制固件侧接收路径必须真的做流式组装。
	opPayload, err := frame.EncodeDeviceOp(frame.DeviceOpReboot, vectorRequestID)
	if err != nil {
		t.Fatalf("EncodeDeviceOp: %v", err)
	}
	if !bytes.Equal(opPayload, mustHex(t, vectorRebootPayload)) {
		t.Fatalf("后端编码的 0x22 载荷 = %x, 与共享向量 %s 不一致（两端漂移！）",
			opPayload, vectorRebootPayload)
	}
	var wholeFrame []byte
	hdrBuf := make([]byte, protoframe.HeaderSize)
	h2 := protoframe.Header{
		Ver:        protoframe.Version,
		Type:       msgTypeDeviceOp,
		Flags:      0,
		Seq:        7,
		PayloadLen: uint16(len(opPayload)),
	}
	if err := protoframe.EncodeHeader(hdrBuf, h2); err != nil {
		t.Fatalf("EncodeHeader: %v", err)
	}
	wholeFrame = append(wholeFrame, hdrBuf...)
	wholeFrame = append(wholeFrame, opPayload...)

	for i := 0; i < len(wholeFrame); i++ {
		if _, err := conn.Write(wholeFrame[i : i+1]); err != nil {
			t.Fatalf("逐字节写第 %d 字节失败: %v", i, err)
		}
		// 小睡一下，尽量让每个字节成为独立的 TCP 段；即便被合并，
		// 固件侧也必须能处理"一次读到多条/半条"的任意切分。
		time.Sleep(time.Millisecond)
	}
	t.Logf("方向2 OK：后端→固件 已逐字节写入 %d B（0x22, seq=7）", len(wholeFrame))

	// ── 等固件侧结论 ──
	select {
	case err := <-waitDone:
		finished = true
		text := out.String()
		if err != nil {
			t.Fatalf("固件客户端退出码非 0: %v\n%s", err, text)
		}
		if !strings.Contains(text, "E2E-CLIENT result=PASS") {
			t.Fatalf("固件客户端没有报 PASS:\n%s", text)
		}
		// 断言它确实**收到了** 0x22，而不是"没收到也算过"。
		if !strings.Contains(text, "rx type=0x22") {
			t.Fatalf("固件客户端没报告收到 0x22 帧:\n%s", text)
		}
		if !strings.Contains(text, "payload_match=yes") {
			t.Fatalf("固件客户端报告载荷不匹配:\n%s", text)
		}
	case <-time.After(20 * time.Second):
		// 不置 finished：让 defer 去 Kill 并回收，避免留下孤儿进程。
		t.Fatalf("固件客户端超时未退出（挂住了？）\n%s", out.String())
	}
}

func firstByte(b []byte) byte {
	if len(b) == 0 {
		return 0
	}
	return b[0]
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var buf [20]byte
	p := len(buf)
	for i > 0 {
		p--
		buf[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		buf[p] = '-'
	}
	return string(buf[p:])
}

// TestSharedVectorBytesMatchConstants 把本文件的硬编码常量**钉在向量文件上**。
// 没有这条：向量文件改了、而这里没改 ⇒ 本测试会继续对着旧字节自说自话。
func TestSharedVectorBytesMatchConstants(t *testing.T) {
	path := filepath.Join("..", "..", "..", "protocol", "vectors", "wire_primitives.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Skipf("向量文件读不到（%v）：本用例需要仓库布局，跳过", err)
	}
	for _, want := range []string{vectorAckOKPayload, vectorRebootPayload} {
		if !bytes.Contains(data, []byte(want)) {
			t.Errorf("常量 %s 在共享向量文件里找不到 —— 两端约定已漂移，或本文件的常量过期", want)
		}
	}
}
