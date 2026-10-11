package nodemgr

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"ehome/backend/pkg/logger"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func setupEventBusTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("failed to open sqlite: %v", err)
	}
	db.AutoMigrate()
	return db
}

func TestConfigEventBus_Publish_ReceiveEvent(t *testing.T) {
	_ = setupEventBusTestDB(t)
	bus := NewConfigEventBus(10)

	ch := bus.Subscribe()

	evt := ConfigChangeEvent{
		Type:     CfgChangeChannel,
		Action:   CfgActionUpdate,
		NodeID:   "node-5",
		EntityID: "42",
	}
	bus.Publish(evt)

	select {
	case received := <-ch:
		if received.Type != evt.Type {
			t.Fatalf("wrong type: got %s want %s", received.Type, evt.Type)
		}
		if received.NodeID != evt.NodeID {
			t.Fatalf("wrong node: got %s want %s", received.NodeID, evt.NodeID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout waiting for event")
	}
}

func TestConfigEventBus_Publish_NonBlocking(t *testing.T) {
	_ = setupEventBusTestDB(t)
	// Buffer size 1 — will fill quickly
	bus := NewConfigEventBus(1)

	// Publish more events than buffer can hold
	for i := 0; i < 10; i++ {
		bus.Publish(ConfigChangeEvent{
			Type:     CfgChangeChannel,
			Action:   CfgActionUpdate,
			NodeID:   "node",
			EntityID: "item",
		})
	}
	// Should not block or panic
}

func TestConfigEventBus_Publish_SetsDefaults(t *testing.T) {
	_ = setupEventBusTestDB(t)
	bus := NewConfigEventBus(10)

	ch := bus.Subscribe()

	// Publish without EventID or Timestamp
	bus.Publish(ConfigChangeEvent{
		Type:     CfgChangeChannel,
		Action:   CfgActionDelete,
		NodeID:   "1",
		EntityID: "1",
	})

	select {
	case evt := <-ch:
		if evt.EventID == "" {
			t.Fatal("EventID should be auto-generated")
		}
		if evt.Timestamp.IsZero() {
			t.Fatal("Timestamp should be auto-set")
		}
	case <-time.After(time.Second):
		t.Fatal("timeout")
	}
}

func TestConfigEventBus_CurrentEpoch_ReturnsZero(t *testing.T) {
	_ = setupEventBusTestDB(t)
	bus := NewConfigEventBus(10)

	if bus.CurrentEpoch() != 0 {
		t.Fatalf("CurrentEpoch should always return 0 in v2, got %d", bus.CurrentEpoch())
	}

	bus.Publish(ConfigChangeEvent{
		Type:   CfgChangeChannel,
		Action: CfgActionUpdate,
		NodeID: "1",
	})

	if bus.CurrentEpoch() != 0 {
		t.Fatalf("CurrentEpoch should still return 0 after publish, got %d", bus.CurrentEpoch())
	}
}

// =============================================================================
// S5 回归契约 (2026-10-07): "事件被丢弃" 必须对调用方可察觉。
//
// 缺陷本体: Publish 在 channel 满时丢事件却 `return nil`, 于是**全部调用方的
// 错误分支都是死代码** —— 缓冲区满与发布成功在调用方看来完全一样。
//
// 断言打在效果层: 只看返回值, 不看日志/指标 (那两者在修复前就存在, 区分不了)。
// 每条"必红"的用例都配了**反向对照** (未满时必须返回 nil), 否则
// "永远返回错误" 也能骗过正向断言。
// =============================================================================

// TestConfigEventBus_Publish_FullBufferIsObservable 是 S5 的核心用例。
//
// 造满手法: NewConfigEventBus(1) 且**不订阅** —— 缓冲位不会被消费,
// 第 2 次 Publish 必然走 default 分支。
func TestConfigEventBus_Publish_FullBufferIsObservable(t *testing.T) {
	// capacity=1, 不订阅 ⇒ 无消费者 ⇒ 第 1 条占满缓冲, 第 2 条必被丢弃。
	bus := NewConfigEventBus(1)

	// 先确认第 1 条真的进得去 (否则"第 2 条失败"可能只是因为总线根本不可写,
	// 那样本用例就没在测"满"这个条件)。
	first := ConfigChangeEvent{Type: CfgChangeChannel, Action: CfgActionUpdate, NodeID: "n1", EntityID: "e1"}
	if err := bus.Publish(first); err != nil {
		t.Fatalf("容量未满时第 1 条就不该失败: %v", err)
	}

	// 缓冲已满 —— 这正是 S5 的场景。调用方必须能察觉。
	second := ConfigChangeEvent{Type: CfgChangeChannel, Action: CfgActionUpdate, NodeID: "n2", EntityID: "e2"}
	err := bus.Publish(second)
	if err == nil {
		t.Fatal("缓冲区满时 Publish 返回 nil —— 调用方的错误分支仍不可达 (S5 未修复)")
	}
	if !errors.Is(err, ErrConfigEventBusFull) {
		t.Fatalf("错误不可区分: 期望 errors.Is(err, ErrConfigEventBusFull), 实际 %#v", err)
	}
}

// TestConfigEventBus_Publish_NotFullReturnsNil 是反向对照。
//
// 没有这条, 把 Publish 写成"永远返回 ErrConfigEventBusFull" 也能让上面那条通过;
// 而"永远报错"同样是 S5 缺陷 (只不过换了个方向): 调用方会对每一次成功发布
// 走错误分支, 重试/告警全部变成噪声。
func TestConfigEventBus_Publish_NotFullReturnsNil(t *testing.T) {
	for _, capacity := range []int{1, 4, 64} {
		t.Run(fmt.Sprintf("capacity=%d", capacity), func(t *testing.T) {
			bus := NewConfigEventBus(capacity)
			// 恰好填满, 一条都不溢出: 每一次都必须返回 nil。
			for i := 0; i < capacity; i++ {
				if err := bus.Publish(ConfigChangeEvent{
					Type: CfgChangeChannel, Action: CfgActionUpdate, NodeID: "n", EntityID: fmt.Sprint(i),
				}); err != nil {
					t.Fatalf("第 %d/%d 条 (容量内) 不应失败: %v", i+1, capacity, err)
				}
			}
		})
	}
}

// TestConfigEventBus_Publish_DrainedBufferRecovers 钉住"满→腾空→又能成功"。
//
// 这条防的是把"满"错实现成粘性状态 (例如给 bus 加个 permantently-failed 标志):
// 丢过一次之后就再也发不出去, 调用方会永久走错误分支。
func TestConfigEventBus_Publish_DrainedBufferRecovers(t *testing.T) {
	bus := NewConfigEventBus(1)
	ch := bus.Subscribe()

	if err := bus.Publish(ConfigChangeEvent{Type: CfgChangeNode, Action: CfgActionUpdate, NodeID: "n1"}); err != nil {
		t.Fatalf("第 1 条不应失败: %v", err)
	}
	if err := bus.Publish(ConfigChangeEvent{Type: CfgChangeNode, Action: CfgActionUpdate, NodeID: "n2"}); err == nil {
		t.Fatal("缓冲满时第 2 条应当报错")
	}

	// 消费掉缓冲里那一条, 腾出空间。
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timeout 消费")
	}

	if err := bus.Publish(ConfigChangeEvent{Type: CfgChangeNode, Action: CfgActionUpdate, NodeID: "n3"}); err != nil {
		t.Fatalf("缓冲腾空后必须恢复可发布, 实际: %v", err)
	}
}

// TestConfigEventBus_Publish_EventNotEnqueuedWhenFull 证明"报错"与"真的没入队"一致。
//
// 只断言返回值还不够: 若实现改成"报错但仍把事件塞进去"(或反过来"返回 nil 但丢弃"),
// 调用方会依据错误的返回值做决策。这里把返回值与缓冲实际内容对起来。
func TestConfigEventBus_Publish_EventNotEnqueuedWhenFull(t *testing.T) {
	bus := NewConfigEventBus(1)
	ch := bus.Subscribe()

	kept := ConfigChangeEvent{Type: CfgChangeChannel, Action: CfgActionCreate, NodeID: "kept", EntityID: "1"}
	if err := bus.Publish(kept); err != nil {
		t.Fatalf("第 1 条不应失败: %v", err)
	}

	dropped := ConfigChangeEvent{Type: CfgChangeChannel, Action: CfgActionCreate, NodeID: "dropped", EntityID: "2"}
	if err := bus.Publish(dropped); err == nil {
		t.Fatal("缓冲满时应当报错")
	}

	// 缓冲里只能有 kept 那一条, 且必须收到它。
	select {
	case got := <-ch:
		if got.NodeID != "kept" {
			t.Fatalf("缓冲内容被破坏: 期望 kept, 实际 %q (报错时仍写入了?)", got.NodeID)
		}
	case <-time.After(time.Second):
		t.Fatal("timeout: 已入队的事件不见了")
	}

	select {
	case got := <-ch:
		t.Fatalf("缓冲里出现了第 2 条 %q —— 报错却仍入队, 调用方会重复投递", got.NodeID)
	case <-time.After(50 * time.Millisecond):
		// 预期: 没有第二条。
	}
}

// TestEmitConfigChange_FullBusIsLogged 证明**经 wrapper 的路径**同样可察觉。
//
// EmitConfigChange 无法向上返回错误 (fire-and-forget, 被 ~20 个已提交的 CRUD
// handler 调用), 它的处置是"保留但显式记录"。本用例锁住"记录"这一半:
// 缓冲满时必须出现 Warn 级日志, 且带得动定位信息。
func TestEmitConfigChange_FullBusIsLogged(t *testing.T) {
	core, observed := observer.New(zapcore.WarnLevel)
	previous := logger.Swap(zap.New(core).Sugar())
	defer func() { logger.Swap(previous) }()

	bus := NewConfigEventBus(1)
	// 占满。
	if err := bus.Publish(ConfigChangeEvent{Type: CfgChangeNode, Action: CfgActionUpdate, NodeID: "filler"}); err != nil {
		t.Fatalf("占位事件不应失败: %v", err)
	}

	// 经过 wrapper 发射 —— 此时必然被丢弃。
	EmitConfigChange(nil, bus, CfgChangeDeviceConfig, CfgActionUpdate, "node-x", "42")

	entries := observed.FilterLevelExact(zapcore.WarnLevel).All()
	if len(entries) == 0 {
		t.Fatal("缓冲满时 EmitConfigChange 未产生 Warn 日志 —— 丢弃仍是静默的")
	}
	found := false
	for _, e := range entries {
		if strings.Contains(e.Message, "DROPPED") && strings.Contains(e.Message, "node-x") {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("丢弃日志缺少可定位信息 (期望含 DROPPED 与 node-x): %#v", entries)
	}
}

// TestTriggerConfigSync_PropagatesDrop 证明**传播型调用方**拿到的是真错误。
//
// TriggerConfigSync 直接 return bus.Publish(...), 所以缓冲满必须一路透出,
// 而不是被 wrapper 吞掉。它的上层 (api 的 log-config 端点) 据此走告警日志分支。
func TestTriggerConfigSync_PropagatesDrop(t *testing.T) {
	bus := NewConfigEventBus(1)
	if err := bus.Publish(ConfigChangeEvent{Type: CfgChangeNode, Action: CfgActionUpdate, NodeID: "filler"}); err != nil {
		t.Fatalf("占位事件不应失败: %v", err)
	}
	mgr := &Manager{eventBus: bus}

	err := mgr.TriggerConfigSync("node-y")
	if err == nil {
		t.Fatal("TriggerConfigSync 吞掉了丢弃错误 —— 上层错误分支不可达")
	}
	if !errors.Is(err, ErrConfigEventBusFull) {
		t.Fatalf("错误未透传为哨兵: %#v", err)
	}

	// 反向对照: 总线空着时必须成功, 否则"永远报错"也能过。
	bus2 := NewConfigEventBus(4)
	mgr2 := &Manager{eventBus: bus2}
	if err := mgr2.TriggerConfigSync("node-z"); err != nil {
		t.Fatalf("总线未满时 TriggerConfigSync 不应失败: %v", err)
	}
}
