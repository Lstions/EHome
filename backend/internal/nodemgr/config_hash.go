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

// ShouldSendConfig checks if config should be sent (hash changed or first time)
// M6 fix: do NOT resend when hash is same and dedup expired — only send when hash changes
func (m *ConfigHashManager) ShouldSendConfig(deviceID string, newHash string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check hash: if hash changed, always send
	oldHash, hasOld := m.hashes[deviceID]
	if hasOld && oldHash != newHash {
		m.hashes[deviceID] = newHash
		m.lastSent[deviceID] = time.Now()
		return true
	}

	// First time (no previous hash): send and record
	if !hasOld {
		m.hashes[deviceID] = newHash
		m.lastSent[deviceID] = time.Now()
		return true
	}

	// Same hash: do NOT resend (M6 fix: previous logic incorrectly resent every 30s)
	// Only update lastSent if we actually sent, otherwise just skip
	return false
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
