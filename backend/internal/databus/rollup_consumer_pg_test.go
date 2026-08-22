package databus

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

// PG-only: rollup UPSERT 增量聚合语义 (min/max LEAST/GREATEST、avg 增量、
// last_v/last_id 取新、cnt 累加、跨分钟独立 bucket)。
// SQLite 单测下 Upsert no-op (见 rollup_consumer_test.go)。

type rollupRow struct {
	DeviceID   uint      `gorm:"column:device_id"`
	SensorName string    `gorm:"column:sensor_name"`
	Bucket     time.Time `gorm:"column:bucket"`
	MinV       float64   `gorm:"column:min_v"`
	MaxV       float64   `gorm:"column:max_v"`
	AvgV       float64   `gorm:"column:avg_v"`
	LastV      float64   `gorm:"column:last_v"`
	LastID     uint      `gorm:"column:last_id"`
	Cnt        int64     `gorm:"column:cnt"`
}

func testRollupRows(t *testing.T, db *gorm.DB, deviceID uint) []rollupRow {
	t.Helper()
	var rows []rollupRow
	if err := db.Raw(
		"SELECT device_id, sensor_name, bucket, min_v, max_v, avg_v, last_v, last_id, cnt FROM unified_data_rollup_1m WHERE device_id = ? ORDER BY bucket",
		deviceID,
	).Scan(&rows).Error; err != nil {
		t.Fatalf("query rollup rows: %v", err)
	}
	return rows
}

func TestRollupConsumer_PostgresAggregation(t *testing.T) {
	if !testutil.IsPostgres() {
		t.Skip("requires PostgreSQL (make test-integration)")
	}
	db := testutil.OpenTestDB(t)
	if err := datalifecycle.EnsureRollupTable(db); err != nil {
		t.Fatalf("EnsureRollupTable: %v", err)
	}

	rc := NewRollupConsumer(db)
	base := time.Date(2026, 8, 21, 10, 0, 0, 0, time.UTC)

	// 批1: 两条同分钟 (bucket 10:00) + 一条次分钟 (bucket 10:01)。
	rc.Upsert([]models.UnifiedData{
		{ID: 1, DeviceID: 7, SensorName: "temp", Value: 10, Timestamp: base.Add(5 * time.Second)},
		{ID: 2, DeviceID: 7, SensorName: "temp", Value: 20, Timestamp: base.Add(45 * time.Second)},
		{ID: 3, DeviceID: 7, SensorName: "temp", Value: 5, Timestamp: base.Add(75 * time.Second)},
	})

	rows := testRollupRows(t, db, 7)
	if len(rows) != 2 {
		t.Fatalf("rollup rows = %d, want 2 buckets", len(rows))
	}
	b0, b1 := rows[0], rows[1]
	if !b0.Bucket.Equal(base) {
		t.Errorf("bucket0 = %v, want %v (minute truncation)", b0.Bucket, base)
	}
	if b0.Cnt != 2 || b0.MinV != 10 || b0.MaxV != 20 || b0.AvgV != 15 || b0.LastV != 20 || b0.LastID != 2 {
		t.Errorf("bucket0 = %+v, want cnt=2 min=10 max=20 avg=15 last=20 last_id=2", b0)
	}
	if b1.Cnt != 1 || b1.MinV != 5 || b1.MaxV != 5 || b1.AvgV != 5 || b1.LastV != 5 || b1.LastID != 3 {
		t.Errorf("bucket1 = %+v, want single-sample values", b1)
	}

	// 批2: 同 bucket 再来一条 — 增量聚合 (avg = (15*2+30)/3 = 20)。
	rc.Upsert([]models.UnifiedData{
		{ID: 4, DeviceID: 7, SensorName: "temp", Value: 30, Timestamp: base.Add(50 * time.Second)},
	})
	rows = testRollupRows(t, db, 7)
	if len(rows) != 2 {
		t.Fatalf("rollup rows after batch2 = %d, want 2", len(rows))
	}
	b0 = rows[0]
	if b0.Cnt != 3 || b0.MinV != 10 || b0.MaxV != 30 || b0.AvgV != 20 || b0.LastV != 30 || b0.LastID != 4 {
		t.Errorf("bucket0 after batch2 = %+v, want cnt=3 min=10 max=30 avg=20 last=30 last_id=4", b0)
	}

	// last_id 取 GREATEST: 乱序到达的旧 id 不得回退锚点。
	rc.Upsert([]models.UnifiedData{
		{ID: 2, DeviceID: 7, SensorName: "temp", Value: 1, Timestamp: base.Add(55 * time.Second)},
	})
	rows = testRollupRows(t, db, 7)
	b0 = rows[0]
	if b0.LastID != 4 {
		t.Errorf("last_id = %d, want 4 (GREATEST must not regress)", b0.LastID)
	}
	if b0.MinV != 1 {
		t.Errorf("min_v = %v, want 1 (LEAST on out-of-order sample)", b0.MinV)
	}

	// 不同 sensor 独立 bucket 行。
	rc.Upsert([]models.UnifiedData{
		{ID: 9, DeviceID: 7, SensorName: "humidity", Value: 60, Timestamp: base.Add(10 * time.Second)},
	})
	rows = testRollupRows(t, db, 7)
	if len(rows) != 3 {
		t.Errorf("rows after distinct sensor = %d, want 3", len(rows))
	}
}
