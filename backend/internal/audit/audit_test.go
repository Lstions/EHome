package audit

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/metrics"
	"ehome/backend/testutil"

	dto "github.com/prometheus/client_model/go"
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
	before := auditFailureMetricValue(t)
	for _, key := range []string{"password", "token", "authorization", "secret"} {
		err := writer.Write(Event{ActorType: "system", EventName: "test", Result: "failure", Metadata: map[string]interface{}{key: "leak"}})
		if err == nil {
			t.Fatalf("sensitive key %q should be rejected", key)
		}
	}
	if delta := auditFailureMetricValue(t) - before; delta != 4 {
		t.Fatalf("audit failure metric delta=%v", delta)
	}
}

func auditFailureMetricValue(t *testing.T) float64 {
	t.Helper()
	var metric dto.Metric
	if err := metrics.SecurityAuditWriteFailuresTotal.Write(&metric); err != nil {
		t.Fatal(err)
	}
	return metric.GetCounter().GetValue()
}
