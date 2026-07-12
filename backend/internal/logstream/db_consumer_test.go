package logstream

import (
	"testing"
	"time"

	"ehome/backend/internal/models"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
)

func TestDBConsumer_PersistsOnlyEnabledNode(t *testing.T) {
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
	if err := db.Create(&models.Node{NodeID: "enabled", LogPersistEnabled: true}).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&models.Node{NodeID: "disabled", LogPersistEnabled: false}).Error; err != nil {
		t.Fatal(err)
	}

	consumer := NewDBConsumer(db)
	consumer.Consume(LogBatch{NodeID: "enabled", Seq: 7, Logs: []LogEntry{{NodeID: "enabled", Level: 2, Ts: 101, Tag: "TEST", Message: "saved"}}})
	consumer.Consume(LogBatch{NodeID: "disabled", Seq: 8, Logs: []LogEntry{{NodeID: "disabled", Level: 2, Ts: 102, Tag: "TEST", Message: "discarded"}}})

	// Consume writes synchronously; tiny delay only protects SQLite scheduling on CI.
	time.Sleep(time.Millisecond)
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
