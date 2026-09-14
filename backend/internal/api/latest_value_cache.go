package api

import (
	"sync"

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
// 注: UnifiedData.DeviceID 在 v2.2 即表示 edge_device_id (见 models.go:256 注释);
// EdgeDeviceID 字段是另一独立指针列, 历史回填不全, 不能作为缓存键。
func SetLatestValue(rec models.UnifiedData) {
	if rec.DeviceID == 0 {
		return
	}
	globalLatestValueCache.mu.Lock()
	defer globalLatestValueCache.mu.Unlock()
	globalLatestValueCache.entries[rec.DeviceID] = rec
}

// LatestValue returns the cached record for a device; ok=false on miss.
func LatestValue(deviceID uint) (models.UnifiedData, bool) {
	globalLatestValueCache.mu.RLock()
	defer globalLatestValueCache.mu.RUnlock()
	rec, ok := globalLatestValueCache.entries[deviceID]
	return rec, ok
}

// WarmupLatestValues 启动回填: 每设备最新 1 页 (DISTINCT ON 原查询, 仅启动一次)。
// 与 SetLatestValue 同步: 缓存键用 device_id (= edge_device_id 的 v2.2 语义)。
func WarmupLatestValues(db gormDB) {
	var rows []models.UnifiedData
	if err := db.
		Raw("SELECT DISTINCT ON (device_id) * FROM unified_data WHERE device_id IS NOT NULL ORDER BY device_id, created_at DESC").
		Scan(&rows).Error; err != nil {
		return // 回填失败不阻塞启动, miss 回落原 SQL 兜底
	}
	globalLatestValueCache.mu.Lock()
	defer globalLatestValueCache.mu.Unlock()
	for _, r := range rows {
		if r.DeviceID != 0 {
			globalLatestValueCache.entries[r.DeviceID] = r
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

// 注 (2026-09-14 裁决, docs/分析/rollup-读取路径裁决-2026-09-14.md):
// 本文件原含 precisionFor (方案 §3.2.2 的 "跨度 > 48h 且无 logical scope 走
// rollup" 路由函数), 生产零调用者, 已删除。它不是"待接线的读取路径", 而是
// 一条在本仓查询协议下不可达的分支: logical scope 为空 ⟺ 实例
// logical_device_id IS NULL (query_scope.go), 而启动 BackfillLogicalDevices
// (identity.go) 对全量实例回填、新实例创建即赋逻辑身份 ⇒ 生产查询恒有
// logical scope ⇒ 恒返回 raw。且 rollup 表无 logical_device_id 列
// (partition_mgr.go EnsureRollupTable DDL), §六 scope 条件落不到该表上。
// rollup 写入侧已同步停写 (nodemgr/manager.go 不再注入 rollupSink);
// rollup 消费者/表/积压数据保留为冻结件, 门禁见
// datalifecycle/rollup_wiring_gate_test.go (INV-7 修正版: 条件式)。
