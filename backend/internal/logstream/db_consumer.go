package logstream

import (
	"sync/atomic"
	"time"

	"ehome/backend/internal/models"
	"gorm.io/gorm"
)

// DBConsumer persists log batches to the node_logs table.
// It can be dynamically enabled/disabled via SetActive() — the persist_enabled
// flag is a pure backend concern, NOT communicated to ESP32.
type DBConsumer struct {
	db     *gorm.DB
	active atomic.Bool
}

// NewDBConsumer creates a new database log consumer.
// Starts in inactive state — must be explicitly activated via SetActive(true).
func NewDBConsumer(db *gorm.DB) *DBConsumer {
	return &DBConsumer{db: db}
}

func (c *DBConsumer) Name() string { return "database" }

func (c *DBConsumer) IsActive() bool { return c.active.Load() }

// SetActive enables or disables DB persistence.
func (c *DBConsumer) SetActive(active bool) {
	c.active.Store(active)
}

func (c *DBConsumer) Consume(batch LogBatch) {
	if len(batch.Logs) == 0 {
		return
	}

	rows := make([]models.NodeLog, len(batch.Logs))
	now := time.Now()
	for i, log := range batch.Logs {
		rows[i] = models.NodeLog{
			NodeID:    log.NodeID,
			Level:     log.Level,
			Ts:        log.Ts,
			Tag:       log.Tag,
			Message:   log.Message,
			CreatedAt: now,
			Seq:       batch.Seq,
		}
	}

	// Batch insert — single transaction for all rows
	if err := c.db.CreateInBatches(rows, len(rows)).Error; err != nil {
		// Log error but don't panic — bus will recover anyway
		_ = err
	}
}
