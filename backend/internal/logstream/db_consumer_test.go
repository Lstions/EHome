package logstream

import (
	"sync"
	"sync/atomic"
	"testing"

	"ehome/backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func newDBConsumerTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&models.Node{}); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec(`CREATE TABLE node_logs (
		id integer PRIMARY KEY AUTOINCREMENT,
		node_id varchar(64) NOT NULL,
		level smallint NOT NULL,
		ts bigint NOT NULL,
		tag varchar(64) NOT NULL,
		message text NOT NULL,
		created_at datetime NOT NULL,
		seq integer NOT NULL DEFAULT 0
	)`).Error; err != nil {
		t.Fatal(err)
	}
	return db
}

func TestDBConsumer_PersistsOnlyEnabledNode(t *testing.T) {
	db := newDBConsumerTestDB(t)
	if err := db.Create(&models.Node{NodeID: "enabled", LogPersistEnabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Node{NodeID: "disabled", LogPersistEnabled: false}).Error; err != nil {
		t.Fatal(err)
	}

	consumer := NewDBConsumer(db)
	consumer.Consume(LogBatch{NodeID: "enabled", Seq: 7, Logs: []LogEntry{{NodeID: "enabled", Level: 2, Ts: 101, Tag: "TEST", Message: "saved"}}})
	consumer.Consume(LogBatch{NodeID: "disabled", Seq: 8, Logs: []LogEntry{{NodeID: "disabled", Level: 2, Ts: 102, Tag: "TEST", Message: "discarded"}}})

	var logs []models.NodeLog
	if err := db.Order("id").Find(&logs).Error; err != nil {
		t.Fatal(err)
	}
	if len(logs) != 1 {
		t.Fatalf("persisted logs = %d, want 1", len(logs))
	}
	if logs[0].NodeID != "enabled" || logs[0].Message != "saved" || logs[0].Seq != 7 {
		t.Fatalf("unexpected persisted log: %+v", logs[0])
	}
}

func TestDBConsumer_CachesPersistencePolicyPerNode(t *testing.T) {
	db := newDBConsumerTestDB(t)
	if err := db.Create(&models.Node{NodeID: "cached", LogPersistEnabled: false}).Error; err != nil {
		t.Fatal(err)
	}

	var policyQueries atomic.Int32
	if err := db.Callback().Query().Before("gorm:query").Register("test:count-policy-query", func(tx *gorm.DB) {
		if tx.Statement.Table == "nodes" {
			policyQueries.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	consumer := NewDBConsumer(db)
	batch := LogBatch{NodeID: "cached", Logs: []LogEntry{{NodeID: "cached", Message: "discarded"}}}
	consumer.Consume(batch)
	consumer.Consume(batch)

	if got := policyQueries.Load(); got != 1 {
		t.Fatalf("persistence policy queries = %d, want 1", got)
	}
}

func TestDBConsumer_SetPersistTakesEffectImmediately(t *testing.T) {
	db := newDBConsumerTestDB(t)
	if err := db.Create(&models.Node{NodeID: "toggle", LogPersistEnabled: false}).Error; err != nil {
		t.Fatal(err)
	}
	consumer := NewDBConsumer(db)
	batch := LogBatch{NodeID: "toggle", Logs: []LogEntry{{NodeID: "toggle", Message: "entry"}}}

	consumer.Consume(batch)
	consumer.SetPersist("toggle", true)
	consumer.Consume(batch)
	consumer.SetPersist("toggle", false)
	consumer.Consume(batch)

	var count int64
	if err := db.Model(&models.NodeLog{}).Count(&count).Error; err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("persisted logs = %d, want exactly 1 after immediate on/off changes", count)
	}
}

func TestDBConsumer_ConcurrentCacheMissQueriesPolicyOnce(t *testing.T) {
	db := newDBConsumerTestDB(t)
	if err := db.Create(&models.Node{NodeID: "concurrent", LogPersistEnabled: false}).Error; err != nil {
		t.Fatal(err)
	}

	var policyQueries atomic.Int32
	if err := db.Callback().Query().Before("gorm:query").Register("test:count-concurrent-policy-query", func(tx *gorm.DB) {
		if tx.Statement.Table == "nodes" {
			policyQueries.Add(1)
		}
	}); err != nil {
		t.Fatal(err)
	}
	consumer := NewDBConsumer(db)
	batch := LogBatch{NodeID: "concurrent", Logs: []LogEntry{{NodeID: "concurrent", Message: "discarded"}}}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			consumer.Consume(batch)
		}()
	}
	wg.Wait()

	if got := policyQueries.Load(); got != 1 {
		t.Fatalf("concurrent persistence policy queries = %d, want 1", got)
	}
}
