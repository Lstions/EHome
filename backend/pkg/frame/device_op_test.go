package frame

import "testing"

// device_op_test.go -- 3.0 运维消息类型（远程重启 / 恢复出厂）
//
// 这些常量此前**只存在于设备固件**（components/device_op/include/device_op.h:62-63），
// Go 侧从未定义。后果不是编译错误，而是**静默**的：
// 服务端无法命名 0x22 ⇒ 无法路由 ⇒ 前端功能端到端不可达，
// 而两端各自的测试都是绿的。
//
// 本文件把"两端编号一致"钉在 Go 侧，另有 tools/check_message_types.py
// 从设备源码方向做同样的检查（两个方向都看，才不会某一边漏）。

// TestDeviceOpWireValues pins the wire numbers.
//
// These are ON THE WIRE. Renumbering does not fail loudly -- it silently
// changes which operation an operator's click performs (e.g. a "reboot"
// button becoming a "factory reset"). Hence an explicit test rather than
// relying on the constants being obviously right.
func TestDeviceOpWireValues(t *testing.T) {
	cases := []struct {
		name string
		got  uint8
		want uint8
	}{
		{"MsgDeviceOp", MsgDeviceOp, 0x22},
		{"MsgDeviceOpAck", MsgDeviceOpAck, 0x23},
		{"DeviceOpReboot", uint8(DeviceOpReboot), 1},
		{"DeviceOpFactoryResetKeepConn", uint8(DeviceOpFactoryResetKeepConn), 2},
		{"DeviceOpOK", uint8(DeviceOpOK), 0},
		{"DeviceOpErrUnknownOp", uint8(DeviceOpErrUnknownOp), 1},
		{"DeviceOpErrBusy", uint8(DeviceOpErrBusy), 2},
		{"DeviceOpErrEraseFailed", uint8(DeviceOpErrEraseFailed), 3},
		{"DeviceOpErrBadArg", uint8(DeviceOpErrBadArg), 4},
		{"DeviceOpErrAckFlushFailed", uint8(DeviceOpErrAckFlushFailed), 5},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %d (0x%02X), want %d (0x%02X) -- wire value changed",
				c.name, c.got, c.got, c.want, c.want)
		}
	}
}

// TestDeviceOpSlotsDoNotCollide -- 0x22/0x23 must not duplicate any other
// message type. A collision means two different messages are
// indistinguishable on the wire, and one of them gets misrouted.
func TestDeviceOpSlotsDoNotCollide(t *testing.T) {
	all := map[string]uint8{
		"MsgHello": MsgHello, "MsgStatusRpt": MsgStatusRpt,
		"MsgDataRpt": MsgDataRpt, "MsgConfigMfst": MsgConfigMfst,
		"MsgDataBatch": MsgDataBatch, "MsgMemRpt": MsgMemRpt,
		"MsgDeviceOp": MsgDeviceOp, "MsgDeviceOpAck": MsgDeviceOpAck,
	}
	seen := map[uint8]string{}
	for name, v := range all {
		if prev, dup := seen[v]; dup {
			t.Errorf("0x%02X claimed by both %s and %s", v, prev, name)
		}
		seen[v] = name
	}
}

// TestDeviceOpResultNameNeverHidesUnknown -- an unrecognised result code means
// the DEVICE is newer than this server. Rendering it as a generic "unknown"
// would erase that fact, so the number must survive into the string.
func TestDeviceOpResultNameNeverHidesUnknown(t *testing.T) {
	if got := DeviceOpResultName(DeviceOpOK); got != "ok" {
		t.Errorf("OK -> %q", got)
	}
	for _, r := range []DeviceOpResult{
		DeviceOpErrUnknownOp, DeviceOpErrBusy, DeviceOpErrEraseFailed,
		DeviceOpErrBadArg, DeviceOpErrAckFlushFailed,
	} {
		if got := DeviceOpResultName(r); got == "" || got == "unknown" {
			t.Errorf("result %d rendered as %q -- must name the code or show the number", r, got)
		}
	}
	// Unknown code: the number must be visible, not swallowed.
	got := DeviceOpResultName(DeviceOpResult(200))
	if got == "unknown" || got == "" {
		t.Fatalf("unknown result rendered as %q; the number must survive", got)
	}
	if !contains(got, "200") {
		t.Errorf("unknown result %q must contain the raw code 200", got)
	}
}

func contains(hay, needle string) bool {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

// TestDeviceOpAckMustBeSendableBeforeReboot documents the ordering contract
// that the device side already enforces (device_op.c: erase -> flush ACK ->
// restart). If the ACK cannot be flushed, the device does NOT reboot --
// otherwise the operator waits forever for a result that was never sent.
//
// The server half of that contract is: a DeviceOpAck received for an op we
// did not send is a protocol error, not a no-op. That check needs the
// session layer, so here we only pin the result code that signals it.
func TestDeviceOpAckFlushFailureIsDistinct(t *testing.T) {
	if DeviceOpErrAckFlushFailed == DeviceOpOK {
		t.Fatal("ack-flush failure must not share the success code")
	}
	if DeviceOpResultName(DeviceOpErrAckFlushFailed) ==
		DeviceOpResultName(DeviceOpErrEraseFailed) {
		t.Error("ack-flush failure and erase failure must be distinguishable: " +
			"one means 'device still running, retry', the other means 'ACK lost'")
	}
}
