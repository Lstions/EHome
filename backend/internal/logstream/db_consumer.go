package logstream

import (
	"log/slog"
	"sync"
	"time"

	"ehome/backend/internal/models"
	"gorm.io/gorm"
)

// DBConsumer persists log batches to node_logs. Persistence is evaluated per
// node from nodes.log_persist_enabled, so a server restart never loses the
// configured policy and one node's switch never changes another node's logs.
type DBConsumer struct {
	db       *gorm.DB
	policies sync.Map
}

type persistPolicy struct {
	mu      sync.Mutex
	loaded  bool
	enabled bool
}

func NewDBConsumer(db *gorm.DB) *DBConsumer {
	return &DBConsumer{db: db}
}

func (c *DBConsumer) Name() string { return "database" }

// Always active as a bus consumer; Consume performs the per-node policy check.
func (c *DBConsumer) IsActive() bool { return true }

// SetPersist writes through a successful API policy update. The per-node lock
// orders it with an in-flight cache miss so the newest API value wins.
func (c *DBConsumer) SetPersist(nodeID string, enabled bool) {
	policy := c.policy(nodeID)
	policy.mu.Lock()
	policy.enabled = enabled
	policy.loaded = true
	policy.mu.Unlock()
}

func (c *DBConsumer) policy(nodeID string) *persistPolicy {
	policy, _ := c.policies.LoadOrStore(nodeID, &persistPolicy{})
	return policy.(*persistPolicy)
}

func (c *DBConsumer) persistEnabled(nodeID string) bool {
	policy := c.policy(nodeID)
	policy.mu.Lock()
	defer policy.mu.Unlock()
	if policy.loaded {
		return policy.enabled
	}

	var node models.Node
	if err := c.db.Select("log_persist_enabled").Where("node_id = ?", nodeID).First(&node).Error; err != nil {
		if err != gorm.ErrRecordNotFound {
			slog.Warn("logstream: failed to read persistence policy", "node_id", nodeID, "error", err)
		}
		return false
	}
	policy.enabled = node.LogPersistEnabled
	policy.loaded = true
	return policy.enabled
}

func (c *DBConsumer) Consume(batch LogBatch) {
	if len(batch.Logs) == 0 {
		return
	}

	if !c.persistEnabled(batch.NodeID) {
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

	if err := c.db.CreateInBatches(rows, len(rows)).Error; err != nil {
		slog.Warn("logstream: batch persistence failed", "node_id", batch.NodeID, "error", err)
	}
}
