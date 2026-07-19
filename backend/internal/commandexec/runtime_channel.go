package commandexec

import (
	"encoding/json"
	"fmt"
	"strings"

	"ehome/backend/internal/models"
)

// reportedChannelAvailability is intentionally a minimal projection of the
// ResourceReport runtime state.  It is not the desired Channel configuration:
// it proves that this particular firmware instance applied the channel before
// a business action may be admitted or published.
type reportedChannelAvailability struct {
	Channels []struct {
		ID      uint `json:"id"`
		Enabled bool `json:"enabled"`
	} `json:"channels"`
}

func requireReportedActionChannel(node models.Node, channelID uint) error {
	if channelID == 0 {
		return fmt.Errorf("action channel is missing")
	}
	if strings.TrimSpace(node.HardwareInfo) == "" {
		return fmt.Errorf("node runtime channel report is unavailable")
	}
	var report reportedChannelAvailability
	if err := json.Unmarshal([]byte(node.HardwareInfo), &report); err != nil || report.Channels == nil {
		return fmt.Errorf("node runtime channel report is unavailable")
	}
	for _, channel := range report.Channels {
		if channel.ID != channelID {
			continue
		}
		if !channel.Enabled {
			return fmt.Errorf("action channel is disabled in node runtime")
		}
		return nil
	}
	return fmt.Errorf("action channel is not applied in node runtime")
}
