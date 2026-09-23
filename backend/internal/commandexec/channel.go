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
	// USB is the ESP32-C6 native USB-Serial-JTAG endpoint used as a data bus.
	// It is a request/response transport exactly like UART, so actions may run on
	// it; omitting it here would reject every action with "bus USB is not
	// supported" even though the manifest encoder and the firmware both accept it.
	case "UART", "I2C", "SPI", "USB":
		return channel, nil
	default:
		return channel, fmt.Errorf("action channel bus %q is not supported", channel.BusType)
	}
}
