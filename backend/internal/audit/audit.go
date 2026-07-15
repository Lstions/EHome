package audit

import (
	"encoding/json"
	"errors"
	"strings"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

var ErrSensitiveMetadata = errors.New("security audit metadata contains a sensitive key")

type Event struct {
	ActorType     string
	ActorUserID   *uint
	ActorSnapshot string
	EventName     string
	EventVersion  int
	Result        string
	RequestID     string
	SourceIP      string
	TargetType    string
	TargetID      string
	Metadata      map[string]interface{}
}

type Writer struct {
	db *gorm.DB
}

func NewWriter(db *gorm.DB) *Writer {
	return &Writer{db: db}
}

func (w *Writer) Write(event Event) error {
	if containsSensitiveKey(event.Metadata) {
		return ErrSensitiveMetadata
	}
	metadata, err := json.Marshal(event.Metadata)
	if err != nil {
		return err
	}
	version := event.EventVersion
	if version == 0 {
		version = 1
	}
	return w.db.Create(&models.SecurityAuditEvent{
		ActorType:     event.ActorType,
		ActorUserID:   event.ActorUserID,
		ActorSnapshot: event.ActorSnapshot,
		EventName:     event.EventName,
		EventVersion:  version,
		Result:        event.Result,
		RequestID:     event.RequestID,
		SourceIP:      event.SourceIP,
		TargetType:    event.TargetType,
		TargetID:      event.TargetID,
		Metadata:      string(metadata),
		CreatedAt:     time.Now().UTC(),
	}).Error
}

func containsSensitiveKey(metadata map[string]interface{}) bool {
	for key, value := range metadata {
		normalized := strings.ToLower(key)
		for _, sensitive := range []string{"password", "token", "authorization", "secret"} {
			if strings.Contains(normalized, sensitive) {
				return true
			}
		}
		if nested, ok := value.(map[string]interface{}); ok && containsSensitiveKey(nested) {
			return true
		}
	}
	return false
}
