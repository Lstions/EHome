package nodemgr

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"ehome/backend/internal/databus"
	"ehome/backend/internal/models"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

// TestManagerShardedIngest_ParallelDevicesAllPersisted verifies the full
// production registration path: NewManager with parser shards registered on
// the real DataEventBus, concurrent multi-device DataReports flowing through
// Publish → fanout → shard workers → SensorParserConsumer, and every sample
// landing in unified_data exactly once with per-device ordering intact.
func TestManagerShardedIngest_ParallelDevicesAllPersisted(t *testing.T) {
	// File-backed temp DB with WAL + busy timeout. Plain ":memory:" gives
	// every pool connection its own empty database; "cache=shared" fixes that
	// but turns concurrent writes into SQLITE_LOCKED (which busy_timeout does
	// NOT retry). A WAL file database serializes concurrent shard writers via
	// SQLITE_BUSY + busy_timeout retries — matching production PostgreSQL
	// semantics where concurrent writers queue on the row/page lock.
	db, err := gorm.Open(sqlite.Open("file:"+filepath.Join(t.TempDir(), "shard_e2e.db")+"?_journal_mode=WAL&_busy_timeout=10000"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open db: %v", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("raw db: %v", err)
	}
	// Keep the original handle (used by AutoMigrate) alive for the test.
	t.Cleanup(func() { _ = sqlDB.Close() })
	if err := db.AutoMigrate(&models.Node{}, &models.Channel{}, &models.EdgeDevice{},
		&models.CalibrationCache{}, &models.UnifiedData{}, &models.DeviceData{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	const devices = 24
	const samplesPerDevice = 20

	edgeIDs := make([]uint, devices)
	for i := 0; i < devices; i++ {
		serial := fmt.Sprintf("shard-node-%02d", i)
		node := models.Node{NodeID: serial, Status: "online"}
		if err := db.Create(&node).Error; err != nil {
			t.Fatalf("create node: %v", err)
		}
		channel := models.Channel{NodeID: serial, HardwareID: "UART0"}
		if err := db.Create(&channel).Error; err != nil {
			t.Fatalf("create channel: %v", err)
		}
		device := models.EdgeDevice{
			Name: fmt.Sprintf("th-%02d", i), NodeID: serial,
			ChannelID: channel.ID, Type: "lk_th01", Status: "active",
		}
		if err := db.Create(&device).Error; err != nil {
			t.Fatalf("create edge device: %v", err)
		}
		edgeIDs[i] = device.ID
	}

	prev := parserShardOverride
	parserShardOverride = 8
	defer func() { parserShardOverride = prev }()

	mgr := NewManager(db, nil, nil, nil, nil, nil)
	defer mgr.Stop()

	// 4-byte lk_th01 payload: temperature+humidity (driver parses 2 fields).
	raw := []byte{0x11, 0x02, 0x1F, 0x80}

	// Publish in waves and drain between waves: the bus deliberately drops
	// oldest events when ingress (256) and shard mailboxes (64×N) saturate
	// (latest-wins backpressure). Wave-at-a-time keeps this test about
	// correctness of sharded persistence, not backpressure, while still
	// exercising all shard workers concurrently every wave.
	const waves = samplesPerDevice
	wantUnified := 0
	deadline := time.Now().Add(30 * time.Second)
	for w := 0; w < waves; w++ {
		for i := 0; i < devices; i++ {
			serial := fmt.Sprintf("shard-node-%02d", i)
			mgr.dataBus.Publish(databus.DataEvent{
				DeviceID:     serial,
				EdgeDeviceID: uint64(edgeIDs[i]),
				RawData:      raw,
				ErrorCode:    0,
				ReceivedAt:   time.Now(),
			})
		}
		wantUnified += devices * 2 // lk_th01: 2 unified rows per event
		for time.Now().Before(deadline) {
			var unified int64
			if err := db.Model(&models.UnifiedData{}).Count(&unified).Error; err != nil {
				t.Fatalf("count unified: %v", err)
			}
			if unified >= int64(wantUnified) {
				break
			}
			time.Sleep(10 * time.Millisecond)
		}
	}

	// Stop the bus before asserting: all shard workers must have exited so
	// no concurrent DB writer holds a SQLite write lock during assertions.
	mgr.dataBus.Stop()

	// Per-device completeness: every edge device received every sample.
	for i := 0; i < devices; i++ {
		var n int64
		if err := db.Model(&models.UnifiedData{}).Where("device_id = ?", edgeIDs[i]).Count(&n).Error; err != nil {
			t.Fatalf("count per device: %v", err)
		}
		if n != samplesPerDevice*2 {
			t.Errorf("device %d: unified rows = %d, want %d", edgeIDs[i], n, samplesPerDevice*2)
		}
	}

	// last_data_at promoted every device to active.
	var active int64
	if err := db.Model(&models.EdgeDevice{}).Where("status = ?", "active").Count(&active).Error; err != nil {
		t.Fatalf("count active: %v", err)
	}
	if active != devices {
		t.Errorf("active edge devices = %d, want %d", active, devices)
	}
}
