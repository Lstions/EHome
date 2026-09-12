package nodemgr

import (
	"testing"
	"time"
)

// TestSetSourceHealthSink_PushesToAllParserConsumers 防"接线从未触发"复发。
// NewManager 构建 parserConsumers 时 sink 为 nil; SetSourceHealthSink 必须把
// 回调推送到每一个已构建的 consumer (不只是存到 Manager 字段), 否则数据源
// 健康引擎永不触发——历史上 alert/automation 正是因缺此断言而单测全绿、
// 线上却"从未触发"。
func TestSetSourceHealthSink_PushesToAllParserConsumers(t *testing.T) {
	db := setupTestDB(t)

	prev := parserShardOverride
	parserShardOverride = 3
	defer func() { parserShardOverride = prev }()

	mgr := NewManager(db, nil, nil, nil, nil, nil)
	defer mgr.Stop()

	if len(mgr.parserConsumers) == 0 {
		t.Fatal("expected NewManager to build parser consumers")
	}

	// 注入前全部为 nil, 否则后续断言无意义。
	for i, p := range mgr.parserConsumers {
		if p.HasSourceHealthSink() {
			t.Fatalf("parser consumer %d already had a source health sink before injection", i)
		}
	}

	mgr.SetSourceHealthSink(func(edgeDeviceID uint, sensorNames []string, at time.Time) {})

	for i, p := range mgr.parserConsumers {
		if !p.HasSourceHealthSink() {
			t.Errorf("parser consumer %d did not receive source health sink (接线未推送，引擎将永不触发)", i)
		}
	}
}
