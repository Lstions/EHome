package auth

import (
	"context"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func TestOutboxProcessorDeliversAndMarksRevocations(t *testing.T) {
	db := testutil.OpenTestDB(t)
	event := models.AuthOutbox{EventType: "session.revoked", SubjectID: 7, SessionVersion: 2, Reason: "logout", CreatedAt: time.Now().UTC()}
	if err := db.Create(&event).Error; err != nil {
		t.Fatal(err)
	}
	delivered := make(chan uint, 1)
	processor := NewOutboxProcessor(db, func(subjectID uint, _ int64, _ string) { delivered <- subjectID })
	if err := processor.ProcessOnce(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case id := <-delivered:
		if id != 7 {
			t.Fatalf("id=%d", id)
		}
	default:
		t.Fatal("event not delivered")
	}
	var stored models.AuthOutbox
	if err := db.First(&stored, event.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.ProcessedAt == nil {
		t.Fatal("event not marked processed")
	}
}
