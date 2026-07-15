package auth

import (
	"context"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

type RevocationHandler func(subjectID uint, version int64, reason string)

type OutboxProcessor struct {
	db      *gorm.DB
	deliver RevocationHandler
}

func NewOutboxProcessor(db *gorm.DB, deliver RevocationHandler) *OutboxProcessor {
	return &OutboxProcessor{db: db, deliver: deliver}
}

func (p *OutboxProcessor) ProcessOnce(ctx context.Context) error {
	var events []models.AuthOutbox
	if err := p.db.WithContext(ctx).Where("processed_at IS NULL AND event_type = ?", "session.revoked").Order("id").Limit(100).Find(&events).Error; err != nil {
		return err
	}
	for _, event := range events {
		p.deliver(event.SubjectID, event.SessionVersion, event.Reason)
		now := time.Now().UTC()
		if err := p.db.WithContext(ctx).Model(&models.AuthOutbox{}).Where("id = ? AND processed_at IS NULL", event.ID).Update("processed_at", now).Error; err != nil {
			return err
		}
	}
	return nil
}

func (p *OutboxProcessor) Run(ctx context.Context, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := p.ProcessOnce(ctx); err != nil { /* retry on next tick */
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
