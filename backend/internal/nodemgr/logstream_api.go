package nodemgr

// TriggerConfigSync emits a config change event to trigger SyncGate to push
// a new ConfigManifest to the ESP32. This is the standard way to push config
// updates without waiting for the device to request a sync.
func (m *Manager) TriggerConfigSync(nodeID string) error {
	if m.eventBus == nil {
		return nil
	}
	return m.eventBus.Publish(ConfigChangeEvent{
		Type:     CfgChangeNode,
		Action:   CfgActionUpdate,
		NodeID:   nodeID,
		EntityID: nodeID,
	})
}

// SetLogPersist enables or disables the DB log consumer for a specific node.
// This is a pure backend operation — ESP32 is NOT notified.
func (m *Manager) SetLogPersist(nodeID string, enabled bool) {
	if m.logDBConsumer != nil {
		m.logDBConsumer.SetActive(enabled)
	}
}

// GetLogPersist returns the current DB persistence state.
func (m *Manager) GetLogPersist() bool {
	if m.logDBConsumer == nil {
		return false
	}
	return m.logDBConsumer.IsActive()
}
