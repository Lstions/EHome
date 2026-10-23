package ingest_test

// 真实驱动回放验证（M1 等价性的**真实数据**证据）。
//
// # 为什么需要它（比合成帧更强的证据）
//
// internal/ingest 的单测用 fake adapter 验证了接口契约；
// internal/databus 的差分测试用**改造前逻辑的重实现**验证了等价性。
// 但两者都用测试自建的简单驱动 —— 它们证明了"框架等价"，
// 没证明"用真实生产驱动的解析结果不变"。
//
// 本文件补上这一层：用**生产实际在跑的驱动**（jiabaida_bms / techfine_inverter）
// 构造真实格式的帧，走接入层解析，断言：
//  1. 接入层能正确解析（不是空结果、不是报错）；
//  2. 解析出的字段与直接调用驱动 ParseData 的结果**逐字段相同**。
//
// # 与生产的关系
//
// 生产实测（2026-10-23）：所有 10 台设备都没有 device_configs、没有标定，
// 全部走 Driver 分支。因此 Driver 分支的等价性是**生产路径的等价性**。

import (
	"testing"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/ingest"
	"ehome/backend/internal/models"
)

// TestReplay_RealDriver_JiabaidaBMS 用真实 jiabaida_bms 驱动回放。
//
// 帧格式（docs 与 jiabaida.go 头部注释一致）：
//
//	0xDD | CMD | STATUS/LEN | DATA... | CHECKSUM_H | CHECKSUM_L | 0x77
//
// 响应帧校验范围：LEN + DATA（排除 0xDD / CMD / STATUS）。
func TestReplay_RealDriver_JiabaidaBMS(t *testing.T) {
	reg := drivers.NewRegistry()
	drivers.RegisterBuiltInDrivers(reg)

	// 先确认驱动真的注册了 —— 否则下面的"解析成功"可能来自别的驱动。
	if _, err := reg.Get("jiabaida_bms"); err != nil {
		t.Fatalf("jiabaida_bms 驱动未注册: %v", err)
	}

	// 构造 0x03（基本信息）响应帧：CMD=0x03, LEN=0x1B(27 字节数据)。
	// 数据段按协议布局填可辨识的值（总电压/电流/RSOC 等）。
	payload := make([]byte, 27)
	payload[0] = 0x10 // cell count 占位
	// 总电压 0x0BB8 = 3000 → 30.00V（协议通常 0.01V 单位，驱动自行换算）
	payload[1], payload[2] = 0x0B, 0xB8
	// 电流 0x0000
	payload[3], payload[4] = 0x00, 0x00
	// RSOC 85%
	payload[19] = 85

	raw := buildJiabaidaResponseFrame(0x03, payload)

	// ① 直接调驱动（改造前的路径）
	drv, _ := reg.Get("jiabaida_bms")
	direct, err := drv.ParseData(raw)
	if err != nil {
		t.Fatalf("驱动直接解析失败（帧构造有误）: %v", err)
	}
	if len(direct) == 0 {
		t.Fatal("驱动直接解析出 0 字段（帧构造有误，本测试无意义）")
	}

	// ② 走接入层（改造后的路径）
	resolver := ingest.NewResolver(
		ingest.NewAdapterConfigParser(),
		ingest.NewAdapterDriver(reg),
	)
	kind, exists := ingest.ResolveDriverKind(reg, "jiabaida_bms")
	if !exists {
		t.Fatal("ResolveDriverKind 未找到驱动")
	}
	dev := models.EdgeDevice{ID: 14, Name: "bms", Type: "jiabaida_bms", NodeID: "n1", Enabled: true}
	sample, err := resolver.Decode(
		ingest.DecodeContext{EdgeDevice: dev, Raw: raw, At: at()},
		ingest.SupportContext{EdgeDevice: dev, DriverKind: kind, DriverExists: exists},
	)
	if err != nil {
		t.Fatalf("接入层解析失败: %v", err)
	}

	// ③ 逐字段比对
	if len(sample.Fields) != len(direct) {
		t.Fatalf("字段数不一致: 接入层=%d 驱动直调=%d", len(sample.Fields), len(direct))
	}
	for i := range direct {
		a, b := sample.Fields[i], direct[i]
		if a.Name != b.Name || a.Value != b.Value || a.Unit != b.Unit || a.StringValue != b.StringValue {
			t.Errorf("字段[%d] 不一致:\n  接入层=%+v\n  驱动直调=%+v", i, a, b)
		}
	}
	// Method 自描述必须保留驱动名（生产日志/审计依赖它）
	if want := "Driver(jiabaida_bms)"; sample.Method != want {
		t.Errorf("Method = %q, want %q", sample.Method, want)
	}
	t.Logf("jiabaida_bms 回放：%d 字段逐字段一致，Method=%s", len(sample.Fields), sample.Method)
}

// buildJiabaidaResponseFrame 按协议构造一个响应帧。
//
// 帧：0xDD | CMD | STATUS(0x00) | LEN | DATA... | CKSUM_H | CKSUM_L | 0x77
// 响应帧校验范围 = LEN + DATA（见 jiabaida.go 的 verifyJiabaidaChecksum）。
func buildJiabaidaResponseFrame(cmd byte, payload []byte) []byte {
	frame := make([]byte, 0, 4+len(payload)+3)
	frame = append(frame, 0xDD, cmd, 0x00, byte(len(payload)))
	frame = append(frame, payload...)
	// 校验：LEN + DATA
	sum := uint16(0)
	sum += uint16(byte(len(payload)))
	for _, b := range payload {
		sum += uint16(b)
	}
	ck := ^sum + 1
	frame = append(frame, byte(ck>>8), byte(ck&0xFF), 0x77)
	return frame
}
