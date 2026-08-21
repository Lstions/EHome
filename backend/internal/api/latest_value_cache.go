package api

import (
	"sync"
	"time"

	"ehome/backend/internal/models"
)

// latestValueCache 数据层时序化 (方案 v3.4 §3.2.4)。
// overview/latest-value 类查询原本 DISTINCT ON (device_id) ... ORDER BY
// created_at DESC 无时间谓词——unified_data 分区表下会扫全部子分区。
// 改为内存缓存: SensorParserConsumer 持久化成功后旁路更新, API 缓存优先、
// miss 回落原 SQL (正确性兜底)。
type latestValueCache struct {
	mu      sync.RWMutex
	entries map[uint]models.UnifiedData // key: edge_device_id
}

var globalLatestValueCache = &latestValueCache{entries: make(map[uint]models.UnifiedData)}

// SetLatestValue 更新单设备最新值 (persist 成功路径调用; rollupSink 同点注入)。
func SetLatestValue(rec models.UnifiedData) {
	if rec.EdgeDeviceID == nil {
		return
	}
	globalLatestValueCache.mu.Lock()
	defer globalLatestValueCache.mu.Unlock()
	globalLatestValueCache.entries[*rec.EdgeDeviceID] = rec
}

// LatestValue returns the cached record for a device; ok=false on miss.
func LatestValue(deviceID uint) (models.UnifiedData, bool) {
	globalLatestValueCache.mu.RLock()
	defer globalLatestValueCache.mu.RUnlock()
	rec, ok := globalLatestValueCache.entries[deviceID]
	return rec, ok
}

// WarmupLatestValues 启动回填: 每设备最新 1 页 (DISTINCT ON 原查询, 仅启动一次)。
func WarmupLatestValues(db gormDB) {
	var rows []models.UnifiedData
	if err := db.
		Raw("SELECT DISTINCT ON (edge_device_id) * FROM unified_data WHERE edge_device_id IS NOT NULL ORDER BY edge_device_id, created_at DESC").
		Scan(&rows).Error; err != nil {
		return // 回填失败不阻塞启动, miss 回落原 SQL 兜底
	}
	globalLatestValueCache.mu.Lock()
	defer globalLatestValueCache.mu.Unlock()
	for _, r := range rows {
		if r.EdgeDeviceID != nil {
			globalLatestValueCache.entries[*r.EdgeDeviceID] = r
		}
	}
}

// gormDB 最小接口 (避免 api 包直接依赖 gorm 的循环/测试负担)。
type gormDB interface {
	Raw(sql string, values ...interface{}) queryResult
}

type queryResult interface {
	Scan(dest interface{}) error
}

// precisionFor 决定 historical 查询走 rollup 还是 raw 路径。
// 规则 (方案 §3.2.2): 显式参数优先; auto 时跨度 > 48h 且 logical scope 为空
// (即 plain device 过滤) 才允许 rollup; logical scope 强制 raw (保形去重语义
// 定义在 raw 上, 不引入第三种口径)。
func precisionFor(precision string, span time.Duration, hasLogicalScope bool) string {
	switch precision {
	case "rollup":
		if hasLogicalScope {
			return "raw" // 口径裁决: logical 查询强制 raw
		}
		return "rollup"
	case "raw":
		return "raw"
	default: // auto / 空值
		if !hasLogicalScope && span > 48*time.Hour {
			return "rollup"
		}
		return "raw"
	}
}
