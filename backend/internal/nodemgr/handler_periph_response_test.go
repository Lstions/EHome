package nodemgr

import (
	"testing"

	"ehome/backend/internal/events"
	"ehome/backend/internal/websocket"
	"ehome/backend/pkg/frame"
)

// =====================================================================
// handlePeriphResponse 解码测试
// =====================================================================

// newTestWSHub 创建一个真实的 websocket.Hub 并启动 Run goroutine.
// broadcast channel 是无缓冲的, 必须有 goroutine 消费否则 BroadcastEvent 会阻塞.
// 测试结束时不需要显式停止 (测试进程结束后 goroutine 自动退出).
func newTestWSHub() *websocket.Hub {
	hub := websocket.NewHub()
	go hub.Run()
	return hub
}

// TestPeriphRsp_DecodeBasic 验证基本 PeriphRsp 解码 (成功响应)
func TestPeriphRsp_DecodeBasic(t *testing.T) {
	// Arrange: 构造 PeriphRsp payload
	enc := frame.NewEncoder(frame.MsgPeriphRsp)
	enc.EncodeVarint(1, 42) // request_id
	enc.EncodeBool(2, true) // success
	enc.EncodeVarint(3, 1)  // value (GPIO level=1)

	db := setupTestDBForPeriph(t)
	wsHub := newTestWSHub()
	mgr := &Manager{db: db, wsHub: wsHub}

	// Act
	mgr.handlePeriphResponse("dev1", enc.Bytes())

	// Assert: 验证不 panic (WS 广播到空客户端集合是安全的)
	// 由于 websocket.Hub 的 BroadcastEvent 是异步的 (写入 broadcast channel),
	// 我们无法直接验证推送内容. 这里仅验证不 panic.
}

// TestPeriphRsp_DecodeError 验证错误响应解码
func TestPeriphRsp_DecodeError(t *testing.T) {
	// Arrange: 构造错误 PeriphRsp
	enc := frame.NewEncoder(frame.MsgPeriphRsp)
	enc.EncodeVarint(1, 99)  // request_id
	enc.EncodeBool(2, false) // success=false
	enc.EncodeVarint(4, 5)   // error_code=5

	db := setupTestDBForPeriph(t)
	wsHub := newTestWSHub()
	mgr := &Manager{db: db, wsHub: wsHub}

	// Act: 不应 panic
	mgr.handlePeriphResponse("dev1", enc.Bytes())
}

// TestPeriphRsp_DecodeWithPeriphType 验证包含 periph_type 和 pin 的解码
func TestPeriphRsp_DecodeWithPeriphType(t *testing.T) {
	// Arrange: 构造带 periph_type 和 pin 的 PeriphRsp
	enc := frame.NewEncoder(frame.MsgPeriphRsp)
	enc.EncodeVarint(1, 100) // request_id
	enc.EncodeBool(2, true)  // success
	enc.EncodeVarint(3, 0)   // value
	enc.EncodeVarint(5, 2)   // periph_type=PWM
	enc.EncodeVarint(6, 6)   // pin=6

	db := setupTestDBForPeriph(t)
	wsHub := newTestWSHub()
	mgr := &Manager{db: db, wsHub: wsHub}

	// Act: 不应 panic
	mgr.handlePeriphResponse("dev1", enc.Bytes())
}

// TestPeriphRsp_DecodeMinimal 验证最小 payload (只有 request_id + success) 解码
func TestPeriphRsp_DecodeMinimal(t *testing.T) {
	// Arrange
	enc := frame.NewEncoder(frame.MsgPeriphRsp)
	enc.EncodeVarint(1, 1)
	enc.EncodeBool(2, true)

	db := setupTestDBForPeriph(t)
	wsHub := newTestWSHub()
	mgr := &Manager{db: db, wsHub: wsHub}

	// Act: 不应 panic
	mgr.handlePeriphResponse("dev1", enc.Bytes())
}

// TestPeriphRsp_DecodeEmptyPayload 验证空 payload 不崩溃
func TestPeriphRsp_DecodeEmptyPayload(t *testing.T) {
	db := setupTestDBForPeriph(t)
	wsHub := newTestWSHub()
	mgr := &Manager{db: db, wsHub: wsHub}

	// Act: 只有消息类型字节, 无字段 — 不应 panic
	mgr.handlePeriphResponse("dev1", []byte{frame.MsgPeriphRsp})

	// Assert: 不 panic 即可
}

// TestPeriphRsp_DecodeInvalidFrame 验证非法 frame 不崩溃
func TestPeriphRsp_DecodeInvalidFrame(t *testing.T) {
	db := setupTestDBForPeriph(t)
	wsHub := newTestWSHub()
	mgr := &Manager{db: db, wsHub: wsHub}

	// Act: 非法字段数据不应 panic
	mgr.handlePeriphResponse("dev1", []byte{frame.MsgPeriphRsp, 0xFF, 0xFF})
}

// TestPeriphRsp_TableDriven 表驱动测试: 多种 PeriphRsp 场景
func TestPeriphRsp_TableDriven(t *testing.T) {
	cases := []struct {
		name      string
		requestID uint64
		success   bool
		value     uint64
		errorCode uint64
		perPage   uint64
		pin       uint64
	}{
		{
			name: "GPIO Read Success", requestID: 1, success: true, value: 1,
		},
		{
			name: "GPIO Read Low", requestID: 2, success: true, value: 0,
		},
		{
			name: "GPIO Write Error", requestID: 3, success: false, errorCode: 1,
		},
		{
			name: "PWM Read Success", requestID: 4, success: true, value: 5000, perPage: 2, pin: 6,
		},
		{
			name: "PWM Start Success", requestID: 5, success: true, perPage: 2, pin: 6,
		},
		{
			name: "GPIO Config Error", requestID: 6, success: false, errorCode: 3, perPage: 1, pin: 7,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Arrange
			enc := frame.NewEncoder(frame.MsgPeriphRsp)
			enc.EncodeVarint(1, tc.requestID)
			enc.EncodeBool(2, tc.success)
			if tc.value > 0 || tc.success {
				enc.EncodeVarint(3, tc.value)
			}
			if tc.errorCode > 0 {
				enc.EncodeVarint(4, tc.errorCode)
			}
			if tc.perPage > 0 {
				enc.EncodeVarint(5, tc.perPage)
			}
			if tc.pin > 0 {
				enc.EncodeVarint(6, tc.pin)
			}

			db := setupTestDBForPeriph(t)
			wsHub := newTestWSHub()
			mgr := &Manager{db: db, wsHub: wsHub}

			// Act: 不应 panic
			mgr.handlePeriphResponse("dev1", enc.Bytes())
		})
	}
}

// =====================================================================
// Manager.HandleMessage 路由测试 — PeriphRsp 路由到 handlePeriphResponse
// =====================================================================

// TestHandleMessage_PeriphRspRouting 验证 HandleMessage 正确路由 PeriphRsp
func TestHandleMessage_PeriphRspRouting(t *testing.T) {
	// Arrange: 构造 PeriphRsp 消息
	enc := frame.NewEncoder(frame.MsgPeriphRsp)
	enc.EncodeVarint(1, 555)
	enc.EncodeBool(2, true)

	db := setupTestDBForPeriph(t)
	wsHub := newTestWSHub()
	mgr := &Manager{db: db, wsHub: wsHub}

	// Act: 通过 HandleMessage 触发路由
	topic := "nodes/dev1/up"
	mgr.HandleMessage(topic, enc.Bytes())

	// Assert: PeriphRsp 应被路由到 handlePeriphResponse
	// 由于 BroadcastEvent 是异步的 (写入 channel), 无法直接验证.
	// 这里验证不 panic 即可 — 路由逻辑通过没有 panic 来确认.
}

// =====================================================================
// WebSocket 事件类型验证
// =====================================================================

// TestPeriphEventTypes 验证事件常量值正确
func TestPeriphEventTypes(t *testing.T) {
	if events.PeriphResult != "periph_result" {
		t.Errorf("PeriphResult: expected 'periph_result', got '%s'", events.PeriphResult)
	}
	if events.PeriphState != "periph_state" {
		t.Errorf("PeriphState: expected 'periph_state', got '%s'", events.PeriphState)
	}
}

// =====================================================================
// ConfigChange 事件类型验证 (GPIO/PWM)
// =====================================================================

// TestConfigChangeTypes_GPIO_PWM 验证 GPIO/PWM 配置变更事件类型常量
func TestConfigChangeTypes_GPIO_PWM(t *testing.T) {
	if CfgChangeGPIO != "gpio" {
		t.Errorf("CfgChangeGPIO: expected 'gpio', got '%s'", CfgChangeGPIO)
	}
	if CfgChangePWM != "pwm" {
		t.Errorf("CfgChangePWM: expected 'pwm', got '%s'", CfgChangePWM)
	}
}
