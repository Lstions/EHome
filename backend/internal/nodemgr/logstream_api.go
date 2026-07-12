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

// SetLogPersist is retained as an API compatibility boundary. Persistence is
// now evaluated per node by DBConsumer from nodes.log_persist_enabled, which
// has already been written transactionally by the API handler. ESP32 is not
// notified because storage policy is a backend-only concern.
func (m *Manager) SetLogPersist(nodeID string, enabled bool) {
	_ = nodeID
	_ = enabled
}

// GetLogPersist is no longer meaningful globally because persistence is
// per-node. Callers should read Node.LogPersistEnabled instead.
func (m *Manager) GetLogPersist() bool {
	return false
}
