package commandexec

import (
	"fmt"
	"strings"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// loadActionChannel is deliberately independent of API helpers: the control
// worker must enforce the same ownership and physical-bus boundary even when
// an execution was created by a non-HTTP caller.
func loadActionChannel(db *gorm.DB, edge models.EdgeDevice) (models.Channel, error) {
	var channel models.Channel
	if db == nil || edge.ChannelID == 0 || strings.TrimSpace(edge.NodeID) == "" {
		return channel, fmt.Errorf("edge device has no transport channel")
	}
	if err := db.Where("id = ? AND node_id = ?", edge.ChannelID, edge.NodeID).First(&channel).Error; err != nil {
		return channel, fmt.Errorf("load action channel: %w", err)
	}
	if !channel.Enabled {
		return channel, fmt.Errorf("action channel is disabled")
	}
	switch strings.ToUpper(strings.TrimSpace(channel.BusType)) {
	case "UART", "I2C", "SPI":
		return channel, nil
	default:
		return channel, fmt.Errorf("action channel bus %q is not supported", channel.BusType)
	}
}
