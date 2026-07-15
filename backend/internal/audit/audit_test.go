package audit

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"
)

func TestWriterPersistsControlledAuditEvent(t *testing.T) {
	db := testutil.OpenTestDB(t)
	writer := NewWriter(db)
	actorID := uint(7)
	err := writer.Write(Event{
		ActorType:     "admin",
		ActorUserID:   &actorID,
		ActorSnapshot: "admin",
		EventName:     "auth.password.changed",
		Result:        "success",
		RequestID:     "req-1",
		SourceIP:      "127.0.0.1",
		TargetType:    "account",
		TargetID:      "7",
		Metadata:      map[string]interface{}{"method": "password"},
	})
	if err != nil {
		t.Fatal(err)
	}
	var stored models.SecurityAuditEvent
	if err := db.First(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if stored.EventName != "auth.password.changed" || stored.ActorSnapshot != "admin" || stored.Metadata == "" {
		t.Fatalf("unexpected stored event: %+v", stored)
	}
}

func TestWriterRejectsSensitiveMetadataKeys(t *testing.T) {
	db := testutil.OpenTestDB(t)
	writer := NewWriter(db)
	for _, key := range []string{"password", "token", "authorization", "secret"} {
		err := writer.Write(Event{ActorType: "system", EventName: "test", Result: "failure", Metadata: map[string]interface{}{key: "leak"}})
		if err == nil {
			t.Fatalf("sensitive key %q should be rejected", key)
		}
	}
}
