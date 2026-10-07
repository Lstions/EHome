package protoframe

import (
	"encoding/hex"
	"errors"
	"testing"
)

// TestProductionTxBytesAreNotFramed_DocumentedGap — 2026-10-07。
//
// ## 这个用例记录的是一个**真实缺陷**，不是"期望行为"
//
// 名字里的 "DocumentedGap" 是字面意思：它把"生产固件的上行**没有加 12 字节帧头**"
// 这件事**钉成可执行的证据**，免得它继续只存在于别人的记忆里。
//
// ## 证据链（全部可在本仓核验）
// 生产上行路径：
//
//	devlink_send_frame(payload, len)        main/device_link_wiring.c:551
//	  -> session_send(s, frame, len, ...)   components/session/session.c:123（原样透传）
//	    -> link_send(l, frame, len, ...)    components/link/link.c:96（frame + want_from 直接给驱动）
//	      -> tcp_send(...)                  components/link_tcp/link_tcp.c:149（io->write 原样写）
//
// 而**唯一**会写 magic(0x4548) 的函数是 `wire_encode_header`（components/wire/wire.c:91），
// 它在**生产代码里零调用**：`grep -rn wire_encode_header main/ components/ --include=*.c`
// 只命中它自己的定义。⇒ 上行的字节是 **payload-only**：1 字节类型 + 字段，
// **没有 12 字节头、没有 CRC32C**。
//
// ## 为什么本地全绿却漏了它
// 跨语言对锚用的客户端 firmware_tcp_e2e_client.c **自己加了头**：
//
//	memcpy(hello_frame + WIRE_HEADER_BYTES, hello_payload, hello_payload_len);
//	wire_encode_header(hello_frame, sizeof(hello_frame), &hh);
//
// ⇒ 对锚证明的是"**一个会正确成帧的客户端**能和后端互通"，
//
//	**不是**"生产固件能和后端互通"。两者是两个不同的程序。
//
// ## 这个用例断言什么
// 把 **payload-only** 的字节直接喂给 DecodeHeader ⇒ 必须报 ErrMagic。
// 这证明后端的流解析器**无法**解析生产固件的上行。
//
// ⚠ 真实的 Hello 载荷首字节是 0x01（MSG_HELLO），第二个字节是 field 1 的 tag。
//
//	用它构造"生产上行的前 12 字节"，看后端怎么判。
func TestProductionTxBytesAreNotFramed_DocumentedGap(t *testing.T) {
	// 真实的 Hello 载荷前缀（来自共享向量 hello_from_device 的 wiring）：
	// 首字节 0x01 + field 1 (tag 0x0A) + len + "2-link-node"...
	payloadPrefix, err := hex.DecodeString("010a0c326c696e6b2d6e6f6465")
	if err != nil {
		t.Fatalf("bad fixture: %v", err)
	}

	// 后端的流解析器对**每一帧**都先 DecodeHeader。
	_, err = DecodeHeader(payloadPrefix)
	if err == nil {
		t.Fatal("payload-only 字节竟然被当成了合法帧头 —— " +
			"那样本节记录的'生产上行未成帧'就不成立，请重新核验证据链")
	}
	if !errors.Is(err, ErrMagic) {
		t.Fatalf("期望 ErrMagic（前两字节不是 0x4548），实际 %v", err)
	}
	t.Logf("确认：生产上行字节被 DecodeHeader 判为 ErrMagic（%v）", err)

	// 对照组：同一份载荷**加上**真正的 12 字节头之后，必须能通过。
	h := Header{
		Ver:        Version,
		Type:       0x01,
		Flags:      0,
		Seq:        1,
		PayloadLen: uint16(len(payloadPrefix)),
	}
	framed := make([]byte, HeaderSize)
	if err := EncodeHeader(framed, h); err != nil {
		t.Fatalf("EncodeHeader: %v", err)
	}
	got, err := DecodeHeader(framed)
	if err != nil {
		t.Fatalf("加上帧头后应当能解：%v", err)
	}
	if got.Type != 0x01 || got.PayloadLen != uint16(len(payloadPrefix)) {
		t.Fatalf("解出的头不对：%+v", got)
	}
	t.Logf("对照：加上 12 字节头后 DecodeHeader 成功（type=0x%02X payload_len=%d）",
		got.Type, got.PayloadLen)
}
