package collector

import (
	"sync"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// EpochGenerator generates monotonically increasing epoch values persisted to DB.
// It is safe for concurrent use.
type EpochGenerator struct {
	mu      sync.Mutex
	current uint64
	db      *gorm.DB
}

// NewEpochGenerator creates a new EpochGenerator backed by the given DB.
// Caller must invoke Restore() before first use.
func NewEpochGenerator(db *gorm.DB) *EpochGenerator {
	return &EpochGenerator{
		db: db,
	}
}

// Next increments the epoch by 1, persists to DB, and returns the new value.
// Persistence is best-effort (async); a failure is logged but does not block.
func (g *EpochGenerator) Next() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()

	g.current++

	// Persist asynchronously — do not block the caller
	go func(val uint64) {
		result := g.db.Model(&models.ConfigMeta{}).Where("id = 1").
			Assign(map[string]interface{}{"epoch": val, "id": 1}).
			FirstOrCreate(&models.ConfigMeta{})
		if result.Error != nil {
			logger.Errorf("epoch persist failed: %v (epoch=%d)", result.Error, val)
		}
	}(g.current)

	return g.current
}

// Current returns the current epoch value without incrementing.
func (g *EpochGenerator) Current() uint64 {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.current
}

// Restore loads the epoch from DB. If no row exists, seeds with time-based value.
// Must be called once at startup before any Next/Current calls.
func (g *EpochGenerator) Restore() error {
	g.mu.Lock()
	defer g.mu.Unlock()

	var meta models.ConfigMeta
	result := g.db.Where("id = 1").First(&meta)
	if result.Error == nil {
		g.current = meta.Epoch
		logger.Infof("epoch restored from DB: %d", g.current)
	} else {
		// First startup: seed epoch from current time in milliseconds
		g.current = uint64(time.Now().UnixMilli())
		// Persist the seed
		meta = models.ConfigMeta{ID: 1, Epoch: g.current}
		if err := g.db.Create(&meta).Error; err != nil {
			// Try upsert in case of race
			g.db.Model(&models.ConfigMeta{}).Where("id = 1").
				Assign(map[string]interface{}{"epoch": g.current, "id": 1}).
				FirstOrCreate(&meta)
		}
		logger.Infof("epoch seeded: %d", g.current)
	}
	return nil
}
