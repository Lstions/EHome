package nodemgr

import (
	"fmt"
	"hash/crc32"
	"sync"
	"time"
)

// ConfigHashManager manages config hash calculation and deduplication
type ConfigHashManager struct {
	mu          sync.RWMutex
	hashes      map[string]string    // device_id -> hash
	lastSent    map[string]time.Time // device_id -> last sent time
	dedupWindow time.Duration
}

// NewConfigHashManager creates a new config hash manager
func NewConfigHashManager() *ConfigHashManager {
	return &ConfigHashManager{
		hashes:      make(map[string]string),
		lastSent:    make(map[string]time.Time),
		dedupWindow: 30 * time.Second,
	}
}

// CalcConfigHash calculates CRC32 hash of templates + channels
func (m *ConfigHashManager) CalcConfigHash(templates, channels []byte) string {
	h := crc32.ChecksumIEEE(templates)
	h = crc32.Update(h, crc32.IEEETable, channels)
	return fmt.Sprintf("%08x", h)
}

// ShouldSendConfig checks if config should be sent (hash changed or dedup window expired)
func (m *ConfigHashManager) ShouldSendConfig(deviceID string, newHash string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check hash first: if hash changed, always send regardless of dedup
	oldHash, hasOld := m.hashes[deviceID]
	if hasOld && oldHash != newHash {
		m.hashes[deviceID] = newHash
		m.lastSent[deviceID] = time.Now()
		return true
	}

	// Same hash or first time: check dedup window
	if lastTime, ok := m.lastSent[deviceID]; ok {
		if time.Since(lastTime) < m.dedupWindow {
			return false // Within 30s window, skip
		}
	}

	// Hash unchanged but dedup expired, or first time
	if hasOld && oldHash == newHash {
		// Same hash, dedup expired — still skip (no point resending identical config)
		return false
	}

	// First time: update state and send
	m.hashes[deviceID] = newHash
	m.lastSent[deviceID] = time.Now()
	return true
}

// Reset clears hash for a device (e.g., on config change)
func (m *ConfigHashManager) Reset(deviceID string) {
	m.mu.Lock()
	delete(m.hashes, deviceID)
	delete(m.lastSent, deviceID)
	m.mu.Unlock()
}

// UpdateLastSent updates the last-sent timestamp for a device without changing the hash.
// Used when config is actually sent to enforce the 30s dedup window.
func (m *ConfigHashManager) UpdateLastSent(deviceID string) {
	m.mu.Lock()
	m.lastSent[deviceID] = time.Now()
	m.mu.Unlock()
}
