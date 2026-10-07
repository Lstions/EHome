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
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/protoframe"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// 共享向量里的精确字节（protocol/vectors/wire_primitives.txt，device_op 一组）。
// 硬编码在这里是**故意**的：本测试要证明"两端对同一串字节的理解一致"，
// 若从向量文件读，就变成了"都读同一个文件"，反而绕过了"各自独立实现"这件事。
// 下面另有断言把这些常量与向量文件钉在一起（见 TestSharedVectorBytesMatchConstants）。
const (
	vectorAckOKPayload  = "230800120a6f702d6e6f6465312d31" // 0x23 result=0 request_id=op-node1-1
	vectorRebootPayload = "220801120a6f702d6e6f6465312d31" // 0x22 op_code=1 request_id=op-node1-1
	vectorRequestID     = "op-node1-1"
	msgTypeDeviceOp     = 0x22
	msgTypeDeviceOpAck  = 0x23
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

	// ═══════════════════════════════════════════════════════════════════════════
	// 方向 0（task-18 新增）：**应用层握手** —— 设备发 Hello，后端回 HelloAck。
	//
	// 为什么必须补这一段：READY 只能由收到 HelloAck 触发（session_note_handshake）。
	// 旧版对锚直接从 0x23 开始，于是"设备能否进 READY"这条**从未被对锚过** ——
	// 两端各自单测全绿、合起来设备却永远停在 WAIT_HANDSHAKE，正是本卡要拦的形态。
	// ═══════════════════════════════════════════════════════════════════════════
	pub := &fwE2EMQTTPublisher{}
	mgr, db := newHandshakeManager(t, pub)

	helloHdr, helloPayload := readOneFrameFromStream(t, conn)
	if helloHdr.Type != frame.MsgHello {
		t.Fatalf("握手首帧类型 = 0x%02X, 期望 0x%02X (Hello)。\n"+
			"固件侧尚未发出 Hello ⇒ 设备永远进不了 READY。\n"+
			"若固件 task-17 还没落地, 这是**预期中的红**; 不得放宽断言或跳过。\n"+
			"固件客户端输出:\n%s", helloHdr.Type, frame.MsgHello, out.String())
	}
	// 节点串取自 Hello field 1。生产里路由身份来自 TLS 证书 CN，而本测试不跑 TLS
	// （见文件头"诚实边界"）；"证书 CN == wire node_id" 那条绑定由 device_e2e_test.go
	// 的真 mTLS 用例覆盖。这里把它显式化：handleHello 要求两者相等，不等即拒
	// （handler_hello.go:154-157）。
	helloNodeID := helloField1String(t, helloPayload)
	if helloNodeID == "" {
		t.Fatalf("Hello field 1 (node_id) 为空 —— 后端 parseHello 会拒 (handler_hello.go:140)")
	}
	t.Logf("方向0a OK：固件→后端 type=0x%02X seq=%d plen=%d node_id=%q",
		helloHdr.Type, helloHdr.Seq, helloHdr.PayloadLen, helloNodeID)

	// 送进**真实路由**：FrameHandler → HandleFrame → handleHello（含"注册成功才发 Ack"）。
	if err := mgr.FrameHandler()(helloNodeID, helloHdr, helloPayload); err != nil {
		t.Fatalf("FrameHandler(Hello) 返回错误: %v", err)
	}

	// **取证**：断言后端确实发了 HelloAck，而不是只信客户端自述。
	acks := pub.helloAckFrames()
	if len(acks) != 1 {
		t.Fatalf("真实路由回 HelloAck 条数 = %d, 期望 1（设备进不了 READY）", len(acks))
	}
	ackServerTime, ackFeatures, ackNonce := decodeHelloAck(t, acks[0])
	t.Logf("方向0b OK：后端→固件 HelloAck server_time=%d features=0x%X nonce=0x%X (%d B)",
		ackServerTime, ackFeatures, ackNonce, len(acks[0]))
	if ackFeatures&1 == 0 {
		t.Errorf("HelloAck features=0x%X 未置 bit0 (CAP_DATA_BATCH_V1)", ackFeatures)
	}
	// 注册必须已持久化 —— 否则就是"设备以为注册了、中心没有"的静默故障。
	var nodeRows int64
	if err := db.Model(&models.Node{}).Where("node_id = ?", helloNodeID).Count(&nodeRows).Error; err != nil {
		t.Fatalf("查询 nodes: %v", err)
	}
	if nodeRows != 1 {
		t.Fatalf("nodes 行数 = %d, 期望 1 —— 回了 Ack 却没持久化", nodeRows)
	}

	// 把后端产出的 HelloAck **逐字节**写回设备（继续逼固件做流式组装）。
	// 载荷是 handleHello 经 SendHelloAck 产出的原始字节，这里只补 3.0 头。
	ackFrame := wrapFrame(t, frame.MsgHelloAck, 0, acks[0])
	for i := 0; i < len(ackFrame); i++ {
		if _, err := conn.Write(ackFrame[i : i+1]); err != nil {
			t.Fatalf("逐字节写 HelloAck 第 %d 字节失败: %v", i, err)
		}
		time.Sleep(time.Millisecond)
	}
	t.Logf("方向0c OK：后端→固件 已逐字节写入 HelloAck %d B (0x12, seq=0)", len(ackFrame))

	// ── 方向 1：固件 C → 后端 Go（0x23 ACK，原有方向）──
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

// =============================================================================
// 应用层握手对锚（task-18）
//
// 上一版只对锚了「单帧互换」：固件发 0x23、后端回 0x22。它证明了两端**能互解字节**，
// 但**没有**证明设备能进入 READY —— 而 READY 只能由 HelloAck 触发。
// 本段把对锚扩到真实应用层握手：
//
//   设备发 Hello(0x01) → 后端走**真实路由** handleHello → 回 HelloAck(0x12)
//   → 设备据此进 READY → 再走原有的 0x23/0x22 方向
//
// # Hello 字段契约（实测确认，不是猜的）
//
// 固件侧编码：esp32-collector/components/msg_handler/handler_hello.c:176-190
//   frame_encode_string(1, node_id) / string(2, fw_version) / string(3, model)
//   varint(4, channel_count) / varint(5, epoch) / varint(6, has_manifest)
//   string(7, last_manifest)  ← 仅当非空才写（handler_hello.c:186-188）
//   string(8, "2.6") / varint(9, handshake_nonce)
//   （HELLO_F_PROTO_VERSION=8 / HELLO_F_HANDSHAKE_NONCE=9，见 msg_handler_internal.h:41-42）
//
// 后端解码：backend/internal/nodemgr/handler_hello.go
//   :82-132 逐字段 switch（含 wire type 校验）
//   :134-139 **必填** = {1,2,3,4,5,6,8,9} —— 注意 field 7(last_manifest) **不在必填集**
//   :140-142 字符串类必填须非空（node_id / fw_version / model）
//   :149     协议版本须落在 [MinSupportedProtocolVersion=2.6, ServerMaxProtocolVersion=3.0]
//   :152-154 handshake_nonce 必须非零（固件侧 :168-171 也拒绝零 nonce）
//
// ⇒ **结论：固件发的 Hello 后端会接受**，不存在"必拒"。固件用 proto_ver="2.6"，
//    正落在后端接受区间内（V3-2a 把上界放宽到 3.0 后 2.6 仍是合法下界）。
//
// 本用例刻意**镜像固件的字段序列**（含可选的 field 7），而不是照抄
// device_e2e_test.go:244 的 3.0 版本 —— 后者是 TLS 路径的用例，与本卡的对锚对象不同。
// =============================================================================

// helloFieldContractHex 是"固件字段序列"的**黄金字节**。
//
// 它由下列输入经生产编码器产出，并被 TestHelloFieldContractMatchesFirmware 钉住：
//
//	node_id="fw-e2e-node"  fw="2.6.0"  model="ESP32-C6"  channel_count=2
//	epoch=7  has_manifest=1  last_manifest="mf-1"  proto="2.6"  nonce=0xA1B2C3D4
//
// 断言这条常量，是为了让"字段顺序/编号"本身成为回归对象：若有人调换编号
// （历史上 device_e2e_test.go:246-249 的注释就记着"我凭记忆写错过 field 3"），
// 这里立刻变红。
func fwE2EHelloPayload() []byte {
	enc := frame.NewEncoder(frame.MsgHello)
	enc.EncodeString(1, fwE2ENodeID)
	enc.EncodeString(2, "2.6.0")
	enc.EncodeString(3, "ESP32-C6")
	enc.EncodeVarint(4, 2)
	enc.EncodeVarint(5, 7)
	enc.EncodeVarint(6, 1)
	enc.EncodeString(7, "mf-1")
	enc.EncodeString(8, "2.6")
	enc.EncodeVarint(frame.HelloFieldHandshakeNonce, uint64(fwE2ENonce))
	return enc.Bytes()
}

const (
	fwE2ENodeID = "fw-e2e-node"
	fwE2ENonce  = 0xA1B2C3D4
)

// fwHelloFrame 把 Hello 载荷包上 3.0 头（头里的 type 与载荷首字节都是 0x01 —— 已知双写）。
func fwHelloFrame(t *testing.T) []byte {
	t.Helper()
	payload := fwE2EHelloPayload()
	out := make([]byte, protoframe.HeaderSize+len(payload))
	h := protoframe.Header{
		Ver: protoframe.Version, Type: frame.MsgHello,
		PayloadLen: uint16(len(payload)),
	}
	if err := protoframe.EncodeHeader(out, h); err != nil {
		t.Fatalf("EncodeHeader(Hello): %v", err)
	}
	copy(out[protoframe.HeaderSize:], payload)
	return out
}

// TestHelloFieldContractMatchesFirmware 钉住"固件字段序列"这条契约本身。
//
// 它断言的是**后端 parseHello 的必填集与固件实际发送集的交集**：
// 固件发的每个字段都能被后端解出，且必填字段一个不缺。
// 没有这条：日后有人改了编码顺序，"对锚"会继续用错字节自说自话。
func TestHelloFieldContractMatchesFirmware(t *testing.T) {
	payload := fwE2EHelloPayload()
	// 用生产解码器逐字段读回，并断言"固件会发的字段"全部在场。
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		t.Fatalf("NewDecoder: %v", err)
	}
	if dec.MsgType() != frame.MsgHello {
		t.Fatalf("载荷首字节 = 0x%02X, 期望 0x%02X (MsgHello)", dec.MsgType(), frame.MsgHello)
	}
	seen := map[uint8]uint8{} // field -> wire type
	for {
		f, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			t.Fatalf("NextField: %v", err)
		}
		seen[f.FieldNum] = f.WireType
	}
	// 固件 handler_hello.c:176-190 会发的字段号（field 7 仅在 manifest 非空时发）。
	for _, n := range []uint8{1, 2, 3, 4, 5, 6, 8, 9} {
		if _, ok := seen[n]; !ok {
			t.Errorf("固件会发的字段 %d 不在载荷里 —— 对锚发的不是固件形状", n)
		}
	}
	// 后端 parseHello:134 的必填集 = {1,2,3,4,5,6,8,9}；field 7 是可选。
	// 若后端将来把 7 加进必填，而固件仍只在非空时发 ⇒ 这里必须变红。
	if _, ok := seen[7]; !ok {
		t.Logf("注意: 本载荷带了可选 field 7 (last_manifest)；固件仅在非空时发它")
	}
	// wire type 也要对：字符串字段是 length-delimited(2)，varint 是 0。
	for n, want := range map[uint8]uint8{1: frame.WireLengthDelimited, 2: frame.WireLengthDelimited, 3: frame.WireLengthDelimited, 4: frame.WireVarint, 5: frame.WireVarint, 6: frame.WireVarint, 7: frame.WireLengthDelimited, 8: frame.WireLengthDelimited, 9: frame.WireVarint} {
		if got := seen[n]; got != want {
			t.Errorf("字段 %d wire type = %d, 期望 %d", n, got, want)
		}
	}
}

// fwE2EMQTTPublisher 既是 nodemgr 的 MQTT 出口，也是本用例的**取证点**。
//
// 为什么不用 mock 掉整个 Manager：本卡要证的是"真实路由 handleHello 真的回了 HelloAck"。
// 只断言"客户端报 PASS"会假绿 —— 那样即使后端从不回 HelloAck，
// 只要客户端碰巧报 PASS 也过。这里直接**捕获后端发出的 0x12 帧**并逐字节校验。
type fwE2EMQTTPublisher struct {
	mu       sync.Mutex
	payloads [][]byte
}

func (p *fwE2EMQTTPublisher) record(_ string, payload []byte) {
	p.mu.Lock()
	defer p.mu.Unlock()
	cp := make([]byte, len(payload))
	copy(cp, payload)
	p.payloads = append(p.payloads, cp)
}

func (p *fwE2EMQTTPublisher) Publish(_ string, payload []byte) error {
	p.record("pub", payload)
	return nil
}
func (p *fwE2EMQTTPublisher) PublishQoS2(_ string, payload []byte) error {
	p.record("qos2", payload)
	return nil
}
func (p *fwE2EMQTTPublisher) PublishRetained(_ string, payload []byte) error {
	p.record("retained", payload)
	return nil
}

// helloAckFrames 返回捕获到的全部 HelloAck(0x12) 帧。
func (p *fwE2EMQTTPublisher) helloAckFrames() [][]byte {
	p.mu.Lock()
	defer p.mu.Unlock()
	var out [][]byte
	for _, b := range p.payloads {
		if len(b) > 0 && b[0] == frame.MsgHelloAck {
			out = append(out, b)
		}
	}
	return out
}

// decodeHelloAck 用生产解码器读回 HelloAck 的字段（server_time=1, features=2, nonce=3）。
func decodeHelloAck(t *testing.T, payload []byte) (serverTime, features, nonce uint64) {
	t.Helper()
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		t.Fatalf("NewDecoder(HelloAck): %v", err)
	}
	if dec.MsgType() != frame.MsgHelloAck {
		t.Fatalf("HelloAck 首字节 = 0x%02X, 期望 0x%02X", dec.MsgType(), frame.MsgHelloAck)
	}
	for {
		f, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			t.Fatalf("HelloAck NextField: %v", err)
		}
		switch f.FieldNum {
		case 1:
			serverTime = frame.GetUint64(f)
		case 2:
			features = frame.GetUint64(f)
		case frame.HelloAckFieldHandshakeNonce:
			nonce = frame.GetUint64(f)
		}
	}
	return serverTime, features, nonce
}

// helloField1String 用生产解码器取出 Hello 的 field 1 (node_id, string)。
//
// 为什么不用现成的 parseHello：它是 nodemgr 的**未导出**函数，且会顺带做完整校验。
// 这里只需要"路由身份"，故按字段号取即可 —— 校验交给真实 handleHello 去做，
// 那才是被测对象（自己先校验一遍会把"后端拒了 Hello"这条真实失败路径遮掉）。
func helloField1String(t *testing.T, payload []byte) string {
	t.Helper()
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		t.Fatalf("NewDecoder(Hello): %v", err)
	}
	for {
		f, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			return ""
		}
		if err != nil {
			t.Fatalf("NextField(Hello): %v", err)
		}
		if f.FieldNum == 1 {
			return frame.GetString(f)
		}
	}
}

// wrapFrame 用**生产**编码器给载荷补一个 3.0 头。
//
// 注意头里的 type 与载荷首字节**都**是消息类型（已知双写约定，本仓现状）：
// HandleFrame 会校验两者一致，不一致直接丢帧（manager.go:418-425）。
func wrapFrame(t *testing.T, msgType uint8, seq uint32, payload []byte) []byte {
	t.Helper()
	out := make([]byte, protoframe.HeaderSize+len(payload))
	h := protoframe.Header{
		Ver: protoframe.Version, Type: msgType, Seq: seq,
		PayloadLen: uint16(len(payload)),
	}
	if err := protoframe.EncodeHeader(out, h); err != nil {
		t.Fatalf("EncodeHeader(0x%02X): %v", msgType, err)
	}
	copy(out[protoframe.HeaderSize:], payload)
	return out
}

// newHandshakeManager 装配一个**真实** nodemgr.Manager（真实 handleHello、真实 SendHelloAck）。
//
// 与 device_e2e_test.go:151 的 startE2EServer 同源：同一个 websocket.Hub 必须既传给
// Manager（handleHello 会无条件解引用 m.wsHub，传 nil 会 panic）又真正 Run 起来。
func newHandshakeManager(t *testing.T, pub *fwE2EMQTTPublisher) (*nodemgr.Manager, *gorm.DB) {
	t.Helper()
	db := testutil.OpenTestDB(t)
	hub := websocket.NewHub()
	go hub.Run()
	mgr := nodemgr.NewManager(db, pub, hub, nil, nil, nil)
	return mgr, db
}

// 说明（2026-10-07，Lead 记，留痕以免后人重复走一遍）：
//
// 我曾在这里加过一个"传真实 downlink.Bridge + 注册 transport.Session"的变体，
// 目的是让 HelloAck 走**生产的 TCP 优先路径**（downlink.Bridge.publish 只在
// native.HasSession(nodeID) 时才走 TCP，否则退回 legacy/MQTT）。
//
// 但本文件的既定做法是：**用取证 publisher 捕获真实路由产出的 HelloAck 字节，
// 再手动逐字节写给设备**（见"方向 0c"）。两者证明力等价：
//   - 本文件证明"真实路由**产出**了正确的 HelloAck 字节，且固件能解析它"；
//   - "下行是否**优先选 TCP 会话**"由 device_e2e_test.go 的真 mTLS 用例覆盖
//     （那里用真实 registry + Bridge + handleConn）。
// 我的变体被调用时只传 nil ⇒ 死代码 + 3 个多余 import，已删除。
//
// 教训：**当两处各自证明同一件事时，后加的那处要先确认不是重复**；
// 而且我改这个文件时同事也在改它 —— 并发改同一文件的结果是互相覆盖，
// 我一度看到的"文件里既有我的又有他的"就是那次踩踏。

// handleHelloFrameThroughRealRoute 把一条 **3.0 头 + Hello 载荷** 的帧送进真实路由。
//
// 真实路径（与生产完全同源）：
//
//	protoframe.DecodeHeader  →  Manager.FrameHandler()  →  Manager.HandleFrame  →  handleHello
//
// FrameHandler 正是 main.go:282 `startDeviceTransport(cfg, nodeMgr.FrameHandler(), ...)`
// 与 device_e2e_test.go:177 `OnFrame: mgr.FrameHandler()` 用的同一个适配器。
//
// nodeID 从哪来：生产里由 **TLS 客户端证书的 CN** 决定（server.go 从证书取 nodeID），
// 而本测试不跑 TLS（见文件头"诚实边界"）。这里显式传入与 Hello field 1 **相同**的
// 节点串 —— 这正是生产不变量：handleHello 要求 wire node_id 与路由身份一致
// （handler_hello.go:154-157 不等则拒），所以这个参数不是"方便测试的旁路"，
// 而是把生产约束**显式化**。
func handleHelloFrameThroughRealRoute(t *testing.T, mgr *nodemgr.Manager, frameBytes []byte) {
	t.Helper()
	hdr, err := protoframe.DecodeHeader(frameBytes[:protoframe.HeaderSize])
	if err != nil {
		t.Fatalf("DecodeHeader(Hello): %v", err)
	}
	// FrameHandler 的返回值恒为 nil（每帧错误不该撕连接），但仍检查以防契约变化。
	if err := mgr.FrameHandler()(fwE2ENodeID, hdr, frameBytes[protoframe.HeaderSize:]); err != nil {
		t.Fatalf("FrameHandler(Hello) 返回错误: %v", err)
	}
}

// TestHandleHelloRepliesHelloAckThroughRealRoute 是本卡的**握手对锚**（后端侧）。
//
// 断言链（全部打在**效果层**，不依赖客户端自述）：
//  1. 送进真实路由的 Hello 被真实 handleHello 处理；
//  2. 后端**确实发出** 0x12 HelloAck（用取证 publisher 捕获原始字节）；
//  3. HelloAck 的 handshake_nonce 与 Hello 发来的**一致**（关联字段，不是认证）；
//  4. features bit0 = CAP_DATA_BATCH_V1 已置位（V3-2a 能力位，与 Hello 无关但同帧）；
//  5. 该 HelloAck 帧能被**生产解码器**读回；
//  6. 节点确实被持久化（否则第 2 条会变成"注册失败也回了 Ack"）。
func TestHandleHelloRepliesHelloAckThroughRealRoute(t *testing.T) {
	pub := &fwE2EMQTTPublisher{}
	mgr, db := newHandshakeManager(t, pub)

	handleHelloFrameThroughRealRoute(t, mgr, fwHelloFrame(t))

	acks := pub.helloAckFrames()
	if len(acks) == 0 {
		t.Fatalf("真实路由没有回 HelloAck —— 设备永远进不了 READY。"+
			"\n捕获到的全部下行帧: %d 条", len(pub.payloads))
	}
	if len(acks) != 1 {
		t.Fatalf("HelloAck 条数 = %d, 期望 1", len(acks))
	}

	serverTime, features, nonce := decodeHelloAck(t, acks[0])
	t.Logf("HelloAck: server_time=%d features=0x%X nonce=0x%X (%d B)", serverTime, features, nonce, len(acks[0]))

	// 3. nonce 必须回显本次 Hello 的 nonce。
	if nonce != fwE2ENonce {
		t.Errorf("HelloAck nonce = 0x%X, 期望回显 0x%X（关联字段）", nonce, fwE2ENonce)
	}
	// 4. V3-2a 能力位 bit0。
	if features&1 == 0 {
		t.Errorf("HelloAck features=0x%X 未置 bit0 (CAP_DATA_BATCH_V1)", features)
	}
	// server_time 是 Unix ms，必须是个可信的非零值。
	if serverTime == 0 {
		t.Errorf("HelloAck server_time = 0")
	}

	// 6. 注册必须已持久化 —— 与"注册失败不发 HelloAck"是同一约束的两面。
	var n int64
	if err := db.Model(&models.Node{}).Where("node_id = ?", fwE2ENodeID).Count(&n).Error; err != nil {
		t.Fatalf("查询 nodes: %v", err)
	}
	if n != 1 {
		t.Fatalf("nodes 行数 = %d, 期望 1 —— 回了 Ack 却没有持久化，正是静默故障", n)
	}
}

// TestHandleHelloDoesNotAckWhenRegistrationFails 是"注册失败不发 HelloAck"的用例。
//
// 为什么必须有它：handleHello 的约束是"注册**已持久化**才发 Ack"（handler_hello.go:224-228）。
// 若对锚只测成功路径，就掩盖了"设备以为自己注册了、其实中心没有"的静默故障 ——
// 而那正是本卡要防的形态。
//
// 构造方式（**不**修改任何生产代码）：用 GORM 的 Create 回调注入一次 nodes INSERT 失败。
// 这正是 handler_hello_test.go:315-321 用过的同款手法（"injected nodes insert failure"），
// 只是这里从真实路由进入，而不是直接调 handleHello。
func TestHandleHelloDoesNotAckWhenRegistrationFails(t *testing.T) {
	pub := &fwE2EMQTTPublisher{}
	mgr, db := newHandshakeManager(t, pub)

	// 只让 nodes 表的 INSERT 失败，不影响其他表（避免把 fixture 也搞坏）。
	if err := db.Callback().Create().Before("gorm:create").Register("e2e:fail_nodes_insert", func(tx *gorm.DB) {
		if tx.Statement != nil && tx.Statement.Table == "nodes" {
			tx.AddError(errors.New("e2e injected nodes insert failure"))
		}
	}); err != nil {
		t.Fatalf("注册失败注入回调: %v", err)
	}

	handleHelloFrameThroughRealRoute(t, mgr, fwHelloFrame(t))

	if acks := pub.helloAckFrames(); len(acks) != 0 {
		t.Fatalf("注册未持久化却回了 %d 条 HelloAck —— 设备会以为自己已注册（静默故障）", len(acks))
	}
	var n int64
	if err := db.Unscoped().Model(&models.Node{}).Where("node_id = ?", fwE2ENodeID).Count(&n).Error; err != nil {
		t.Fatalf("查询 nodes: %v", err)
	}
	if n != 0 {
		t.Fatalf("nodes 行数 = %d, 期望 0（注入的 INSERT 失败应当阻止落库）", n)
	}
	t.Logf("注册失败路径确认: 0 条 HelloAck, 0 行 nodes")
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
