package datalifecycle

import (
	"errors"
	"fmt"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// DataQueryScope is the §六 query-protocol resolution of one edge device
// instance: which logical device (followed to its final merge target) its
// data belongs to, or — when no logical identity exists yet — the plain
// device_id fallback for pre-backfill rows.
type DataQueryScope struct {
	// LogicalID is the final merge target the instance resolves to.
	// 0 → no logical identity; query by FallbackDeviceID instead.
	LogicalID uint
	// FallbackDeviceID is the edge device id used when LogicalID == 0
	// (兼容 backfill 前旧数据).
	FallbackDeviceID uint
	// InstanceDeleted reports the instance as soft-deleted or absent.
	// Instance-scoped endpoints (latest-data / :id/data, §十二) return
	// 404; chart/history endpoints (§六) ignore it and keep legacy
	// empty-result behaviour.
	InstanceDeleted bool
	// Scope is the resolved data scope of LogicalID (target + pending
	// incoming merge sources + Unscoped instance set); nil when
	// LogicalID == 0. Query and cleanup share this resolution (§4.3
	// v3.1-Q2) via ResolveScope.
	Scope *Scope
	// DedupNeeded enables 保形去重 (§六): true only when the resolved
	// target has at least one COMPLETED incoming merge
	// (EXISTS merged_into=? AND merge_status='done', v3.2-终审 B8).
	DedupNeeded bool
}

// ResolveDataQueryScope resolves edge_device_id → data query scope
// (方案 §六 pseudocode). The instance lookup is Unscoped so soft-deleted
// instances still resolve; an instance carrying a logical identity follows
// the merged_into chain to the final target (v3.2-F1 纵深防御: even when
// §3.4 source-liveness validation is bypassed, querying through a stale
// instance cannot tear data).
func ResolveDataQueryScope(db *gorm.DB, edgeDeviceID uint) (*DataQueryScope, error) {
	qs := &DataQueryScope{FallbackDeviceID: edgeDeviceID}

	var dev models.EdgeDevice
	err := db.Unscoped().Select("id", "logical_device_id", "deleted_at").First(&dev, edgeDeviceID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			// 实例不存在 (理论上不该发生): 保持旧行为按 device_id 查 (命中 0 行)。
			qs.InstanceDeleted = true
			return qs, nil
		}
		return nil, fmt.Errorf("datalifecycle: resolve instance %d: %w", edgeDeviceID, err)
	}
	qs.InstanceDeleted = dev.DeletedAt.Valid
	if dev.LogicalDeviceID == nil || *dev.LogicalDeviceID == 0 {
		return qs, nil // 兼容 backfill 前旧数据
	}

	// One graph load serves both the chain walk and the dedup decision
	// (§六: 该判定与 scope 解析同往返, 不增加额外 DB 往返).
	graph, err := LoadMergeGraph(db)
	if err != nil {
		return nil, err
	}
	target := graph.Resolve(*dev.LogicalDeviceID)

	scope, err := ResolveScope(db, target)
	if err != nil {
		return nil, err
	}
	qs.LogicalID = target
	qs.Scope = scope
	qs.DedupNeeded = graph.HasDoneIncomingMerge(target)
	return qs, nil
}

// ApplyShapeDedup wraps a conditioned unified_data query with 保形去重
// (§六): per (sensor_name, timestamp) keep exactly one row — the newest
// (MAX(id)); averaging two physical devices' readings has no physical
// meaning and would break the row-shape contract the frontend relies on.
// Unified window-function spelling for PostgreSQL AND SQLite — never
// DISTINCT ON (v3.2-F5 闭环).
//
// base must be a fresh Model/Table query carrying WHERE conditions ONLY
// (no Select/Order/Limit — they belong on the returned query so they
// apply after dedup). base is consumed: do not reuse it afterwards.
func ApplyShapeDedup(db *gorm.DB, base *gorm.DB) *gorm.DB {
	sub := base.Select("*, ROW_NUMBER() OVER (PARTITION BY sensor_name, timestamp ORDER BY id DESC) AS dedup_rn")
	return db.Table("(?) AS deduped", sub).Where("dedup_rn = 1")
}
