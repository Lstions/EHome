package nodemgr

import (
	"fmt"
	"hash/crc32"
)

// ConfigHashManager manages config hash calculation (pure function, no state)
type ConfigHashManager struct{}

// NewConfigHashManager creates a new config hash manager
func NewConfigHashManager() *ConfigHashManager {
	return &ConfigHashManager{}
}

// CalcConfigHash calculates CRC32 hash of the combined hash data
func (m *ConfigHashManager) CalcConfigHash(hashData []byte) string {
	return fmt.Sprintf("%08x", crc32.ChecksumIEEE(hashData))
}
