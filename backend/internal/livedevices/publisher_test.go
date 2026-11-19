package livedevices

import (
	"context"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func TestBuildIncludesStatusAndLatestValues(t *testing.T) {
	db := testutil.OpenTestDB(t)

	devA := models.EdgeDevice{Name: "A", NodeID: "N1", ChannelID: 1, Status: models.EdgeDeviceStatusActive}
	devB := models.EdgeDevice{Name: "B", NodeID: "N1", ChannelID: 1, Status: models.EdgeDeviceStatusPending}
	if err := db.Create(&devA).Error; err != nil {
		t.Fatalf("create A: %v", err)
	}
	if err := db.Create(&devB).Error; err != nil {
		t.Fatalf("create B: %v", err)
	}

	p := New(db, nil, func(deviceID uint) []models.UnifiedData {
		if deviceID != devA.ID {
			return nil
		}
		return []models.UnifiedData{
			{DeviceID: devA.ID, SensorName: "temperature", Value: 21.5},
			{DeviceID: devA.ID, SensorName: "humidity", Value: 60},
		}
	}, nil)

	payload, err := p.build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	if payload.At == "" {
		t.Fatal("At should be a RFC3339 timestamp")
	}
	if len(payload.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(payload.Items))
	}

	byID := map[uint]Item{}
	for _, item := range payload.Items {
		byID[item.ID] = item
	}

	gotA := byID[devA.ID]
	if gotA.Status != models.EdgeDeviceStatusActive {
		t.Errorf("A status = %q", gotA.Status)
	}
	if gotA.Data["temperature"] != 21.5 || gotA.Data["humidity"] != 60 {
		t.Errorf("A data = %v", gotA.Data)
	}

	// 没有缓存值的设备：status 照发（列表要据此显示"等待数据"），data 省略。
	gotB := byID[devB.ID]
	if gotB.Status != models.EdgeDeviceStatusPending {
		t.Errorf("B status = %q", gotB.Status)
	}
	if gotB.Data != nil {
		t.Errorf("B data should be omitted, got %v", gotB.Data)
	}
}

func TestBuildExcludesSoftDeletedDevices(t *testing.T) {
	db := testutil.OpenTestDB(t)

	dev := models.EdgeDevice{Name: "gone", NodeID: "N1", ChannelID: 1, Status: models.EdgeDeviceStatusActive}
	if err := db.Create(&dev).Error; err != nil {
		t.Fatalf("create: %v", err)
	}
	if err := db.Delete(&dev).Error; err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	p := New(db, nil, nil, nil)
	payload, err := p.build()
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	// 客户端按"整体替换"消费快照：已删除设备必须消失，否则列表永远有幽灵行。
	if len(payload.Items) != 0 {
		t.Fatalf("soft-deleted device leaked into snapshot: %+v", payload.Items)
	}
}

func TestPublishSkippedWithoutClients(t *testing.T) {
	db := testutil.OpenTestDB(t)

	calls := 0
	p := New(db, func(string, interface{}) { calls++ }, nil, func() int { return 0 })
	p.publish()

	if calls != 0 {
		t.Fatalf("broadcast called %d times with no clients, want 0", calls)
	}
}

func TestPublishBroadcastsSnapshotEvent(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if err := db.Create(&models.EdgeDevice{Name: "A", NodeID: "N1", ChannelID: 1}).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	var (
		mu        sync.Mutex
		eventType string
		got       Payload
		ok        bool
	)
	p := New(db, func(name string, payload interface{}) {
		mu.Lock()
		defer mu.Unlock()
		eventType = name
		got, ok = payload.(Payload)
	}, nil, func() int { return 1 })
	p.publish()

	mu.Lock()
	defer mu.Unlock()
	if eventType != events.EdgeDevicesLive {
		t.Fatalf("event type = %q, want %q", eventType, events.EdgeDevicesLive)
	}
	if !ok {
		t.Fatal("payload is not livedevices.Payload")
	}
	if len(got.Items) != 1 {
		t.Fatalf("items = %d, want 1", len(got.Items))
	}
}

func TestRunPublishesUntilContextCancel(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if err := db.Create(&models.EdgeDevice{Name: "A", NodeID: "N1", ChannelID: 1}).Error; err != nil {
		t.Fatalf("create: %v", err)
	}

	ticks := make(chan struct{}, 8)
	p := New(db, func(string, interface{}) {
		select {
		case ticks <- struct{}{}:
		default:
		}
	}, nil, func() int { return 1 }, WithInterval(10*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		p.Run(ctx)
		close(done)
	}()

	select {
	case <-ticks:
	case <-time.After(3 * time.Second):
		cancel()
		t.Fatal("no snapshot published within 3s")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after context cancel")
	}
}
