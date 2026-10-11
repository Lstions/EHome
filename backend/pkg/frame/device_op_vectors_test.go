package frame

import (
	"testing"
)

// device_op_vectors_test.go -- 让【真实实现】消费共享向量，而不只是通用编码器。
//
// # 为什么需要这个文件（一个真实缺口）
//
// 共享向量文件 `protocol/vectors/wire_primitives.txt` 的既有消费者
// （Go 的 TestGoldenVectorsFromSharedContract、C 的 golden_vectors_tests、
// Python 门禁）都只用**通用** encoder/decoder 去编解向量。
//
// 那证明了"通用编码器能产出这串字节"，但**没有**证明
// `EncodeDeviceOp` 用的是 field 1/2、`DecodeDeviceOpAck` 读的是 field 1/2/3。
// 换句话说：**字段号写错，通用向量照样全绿** ——
// 这正是本仓反复出现的"绿了但没测到那个东西"。
//
// 本文件复用 golden_vectors_test.go 里既有的解析器（parseVectors / findVectors），
// 不另写一份：同一份向量两种解析方式，本身就是一个漂移源（P4）。
//
// 对应地，C 侧由 host_tests/handler_device_op_tests.c 里的
// test_shared_vectors_* 断言同一串字节。两端任一处字段号漂移，两边各自会红。

// deviceOpVectors 从共享向量里挑出 device_op_* 用例。
func deviceOpVectors(t *testing.T) []vecCase {
	t.Helper()
	all := parseVectors(t, findVectors(t))
	var out []vecCase
	for _, c := range all {
		if len(c.name) > len("device_op_") && c.name[:len("device_op_")] == "device_op_" {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		t.Fatal("共享向量里找不到任何 device_op_* 用例 —— 向量被删了，" +
			"两端的 0x22/0x23 字段约定就失去了唯一的对锚")
	}
	return out
}

// vectorField 取一个字段（按字段号），没有则返回零值。
func vectorFieldU64(c vecCase, num uint8) (uint64, bool) {
	for _, f := range c.fields {
		if f.fieldNum == num && f.data == nil {
			return f.u64, true
		}
	}
	return 0, false
}

func vectorFieldBytes(c vecCase, num uint8) ([]byte, bool) {
	for _, f := range c.fields {
		if f.fieldNum == num && f.data != nil {
			return f.data, true
		}
	}
	return nil, false
}

// TestDeviceOpEncodeMatchesSharedVectors -- 下行 0x22：真实编码器 vs 共享向量。
func TestDeviceOpEncodeMatchesSharedVectors(t *testing.T) {
	for _, c := range deviceOpVectors(t) {
		if c.msgType != MsgDeviceOp {
			continue
		}
		op, ok := vectorFieldU64(c, DeviceOpFieldOp)
		if !ok {
			t.Fatalf("%s: 向量里没有 field %d (op_code)", c.name, DeviceOpFieldOp)
		}
		rid, ok := vectorFieldBytes(c, DeviceOpFieldRequestID)
		if !ok {
			t.Fatalf("%s: 向量里没有 field %d (request_id)", c.name, DeviceOpFieldRequestID)
		}
		got, err := EncodeDeviceOp(DeviceOp(op), string(rid))
		if err != nil {
			t.Fatalf("%s: EncodeDeviceOp: %v", c.name, err)
		}
		if string(got) != string(c.wire) {
			t.Fatalf("%s: **真实实现与共享向量不一致**\n got %x\nwant %x\n"+
				"（通用编码器的向量测试测不到这里：字段号写错它照样绿）",
				c.name, got, c.wire)
		}
	}
}

// TestDeviceOpDecodeAcceptsSharedVectors -- 下行 0x22：真实解码器解共享向量。
func TestDeviceOpDecodeAcceptsSharedVectors(t *testing.T) {
	seen := 0
	for _, c := range deviceOpVectors(t) {
		if c.msgType != MsgDeviceOp {
			continue
		}
		seen++
		req, err := DecodeDeviceOp(c.wire)
		if err != nil {
			t.Fatalf("%s: **真实解码器解不了共享向量**: %v", c.name, err)
		}
		wantOp, _ := vectorFieldU64(c, DeviceOpFieldOp)
		wantRID, _ := vectorFieldBytes(c, DeviceOpFieldRequestID)
		if uint8(req.Op) != uint8(wantOp) {
			t.Fatalf("%s: op = %d, want %d", c.name, req.Op, wantOp)
		}
		if req.RequestID != string(wantRID) {
			t.Fatalf("%s: request_id = %q, want %q", c.name, req.RequestID, wantRID)
		}
	}
	if seen == 0 {
		t.Fatal("没有 0x22 用例被检查")
	}
}

// TestDeviceOpAckDecodeAcceptsSharedVectors -- 上行 0x23：真实解码器解共享向量。
//
// 特别钉住 device_op_ack_unknown_result_keeps_number：
// 未知结果码必须**原样保留数字**，不能被折叠成某个已知值 ——
// 折叠等于把"设备比服务端新"误报成"操作失败"。
func TestDeviceOpAckDecodeAcceptsSharedVectors(t *testing.T) {
	seen := 0
	for _, c := range deviceOpVectors(t) {
		if c.msgType != MsgDeviceOpAck {
			continue
		}
		seen++
		ack, err := DecodeDeviceOpAck(c.wire)
		if err != nil {
			t.Fatalf("%s: **真实解码器解不了共享向量**: %v", c.name, err)
		}
		wantResult, _ := vectorFieldU64(c, DeviceOpAckFieldResult)
		wantRID, _ := vectorFieldBytes(c, DeviceOpAckFieldRequestID)
		if uint8(ack.Result) != uint8(wantResult) {
			t.Fatalf("%s: result = %d, want %d（未知结果码必须原样保留数字，"+
				"折叠成已知值会把'设备比服务端新'误报成'操作失败'）",
				c.name, ack.Result, wantResult)
		}
		if ack.RequestID != string(wantRID) {
			t.Fatalf("%s: request_id = %q, want %q", c.name, ack.RequestID, wantRID)
		}
	}
	if seen == 0 {
		t.Fatal("没有 0x23 用例被检查")
	}
}

// TestDeviceOpAckEncodeMatchesSharedVectors -- 上行 0x23：真实编码器 vs 共享向量。
func TestDeviceOpAckEncodeMatchesSharedVectors(t *testing.T) {
	seen := 0
	for _, c := range deviceOpVectors(t) {
		if c.msgType != MsgDeviceOpAck {
			continue
		}
		seen++
		result, _ := vectorFieldU64(c, DeviceOpAckFieldResult)
		rid, _ := vectorFieldBytes(c, DeviceOpAckFieldRequestID)
		detail, hasDetail := vectorFieldBytes(c, DeviceOpAckFieldDetail)
		if !hasDetail {
			detail = nil
		}
		got, err := EncodeDeviceOpAck(string(rid), DeviceOpResult(result), string(detail))
		if err != nil {
			t.Fatalf("%s: EncodeDeviceOpAck: %v", c.name, err)
		}
		if string(got) != string(c.wire) {
			t.Fatalf("%s: **真实实现与共享向量不一致**\n got %x\nwant %x",
				c.name, got, c.wire)
		}
	}
	if seen == 0 {
		t.Fatal("没有 0x23 用例被检查")
	}
}
