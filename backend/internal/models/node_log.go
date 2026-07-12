package models

import "time"

// NodeLog represents a system log entry from an ESP32 collector.
// Persisted by the DB consumer of LogEventBus when persist_enabled=true.
type NodeLog struct {
	ID     uint64 `gorm:"primaryKey;autoIncrement" json:"id"`
	NodeID string `gorm:"column:node_id;type:varchar(64);not null;index:idx_node_logs_node_created,priority:1;index:idx_node_logs_node_level,priority:1" json:"node_id"`
	Level  int    `gorm:"column:level;type:smallint;not null;index:idx_node_logs_node_level,priority:2" json:"level"`
	// Ts is ESP uptime in microseconds; it is diagnostic metadata, never an absolute timestamp.
	Ts        int64     `gorm:"column:ts;type:bigint;not null" json:"ts"`
	Tag       string    `gorm:"column:tag;type:varchar(64);not null" json:"tag"`
	Message   string    `gorm:"column:message;type:text;not null" json:"message"`
	CreatedAt time.Time `gorm:"column:created_at;not null;index:idx_node_logs_node_created,priority:2;index:idx_node_logs_created,priority:1" json:"created_at"`
	Seq       int       `gorm:"column:seq;type:integer;not null;default:0" json:"seq"`
}

func (NodeLog) TableName() string { return "node_logs" }
