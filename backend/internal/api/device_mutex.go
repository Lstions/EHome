package api

import (
	"sync"
	"sync/atomic"
	"time"
)

// deviceMutexEntry holds a per-device mutex with cleanup metadata
type deviceMutexEntry struct {
	mu    sync.Mutex
	last  time.Time // last use time
	count int64     // reference count (atomic)
}

// deviceMutexMap manages per-device mutexes with automatic cleanup
type deviceMutexMap struct {
	mu      sync.Mutex
	locks   map[string]*deviceMutexEntry
	cleanup time.Time // last cleanup time
}

var deviceLocks = &deviceMutexMap{locks: make(map[string]*deviceMutexEntry)}

func (dm *deviceMutexMap) lock(deviceKey string) {
	dm.mu.Lock()
	// Periodic cleanup: every 10 minutes, remove expired entries
	if time.Since(dm.cleanup) > 10*time.Minute {
		for k, v := range dm.locks {
			if atomic.LoadInt64(&v.count) == 0 && time.Since(v.last) > 30*time.Minute {
				delete(dm.locks, k)
			}
		}
		dm.cleanup = time.Now()
	}
	if dm.locks[deviceKey] == nil {
		dm.locks[deviceKey] = &deviceMutexEntry{}
	}
	entry := dm.locks[deviceKey]
	atomic.AddInt64(&entry.count, 1)
	dm.mu.Unlock()

	entry.mu.Lock()
	entry.last = time.Now()
}

func (dm *deviceMutexMap) unlock(deviceKey string) {
	dm.mu.Lock()
	entry := dm.locks[deviceKey]
	dm.mu.Unlock()
	if entry != nil {
		atomic.AddInt64(&entry.count, -1)
		entry.mu.Unlock()
	}
}
