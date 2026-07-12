package logstream

import (
	"log/slog"
	"time"

	"ehome/backend/internal/models"
	"gorm.io/gorm"
)

// LogCleanup runs a background goroutine that periodically deletes old node_logs
// entries. This prevents unbounded table growth from log persistence.
type LogCleanup struct {
	db          *gorm.DB
	maxAge      time.Duration
	interval    time.Duration
	stopCh      chan struct{}
}

// NewLogCleanup creates a new cleanup worker.
// maxAge: entries older than this are deleted (default 72h)
// interval: check interval (default 1h)
func NewLogCleanup(db *gorm.DB, maxAge time.Duration, interval time.Duration) *LogCleanup {
	if maxAge <= 0 {
		maxAge = 72 * time.Hour
	}
	if interval <= 0 {
		interval = time.Hour
	}
	return &LogCleanup{
		db:       db,
		maxAge:   maxAge,
		interval: interval,
		stopCh:   make(chan struct{}),
	}
}

// Start launches the cleanup goroutine.
func (lc *LogCleanup) Start() {
	go lc.run()
}

// Stop signals the cleanup goroutine to exit.
func (lc *LogCleanup) Stop() {
	close(lc.stopCh)
}

func (lc *LogCleanup) run() {
	ticker := time.NewTicker(lc.interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			lc.cleanup()
		case <-lc.stopCh:
			return
		}
	}
}

func (lc *LogCleanup) cleanup() {
	threshold := time.Now().Add(-lc.maxAge)
	result := lc.db.Where("created_at < ?", threshold).Delete(&models.NodeLog{})
	if result.Error != nil {
		slog.Warn("logstream: cleanup failed", "error", result.Error, "threshold", threshold)
		return
	}
	if result.RowsAffected > 0 {
		slog.Info("logstream: cleanup deleted old logs",
			"deleted", result.RowsAffected, "older_than", threshold.Format(time.RFC3339))
	}
}
