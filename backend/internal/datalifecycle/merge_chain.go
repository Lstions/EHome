package datalifecycle

import (
	"fmt"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
)

// mergeChainMaxHops bounds the merged_into walk used by the query/write
// protocol (方案 §六 v3.2-F1: 上限 8 跳防环). Distinct from identity.go's
// maxMergeChainDepth (16), which guards the low-frequency delete-time
// identity-reuse path.
const mergeChainMaxHops = 8

// MergeGraph is the merged_into graph of logical_devices loaded into memory
// with a single query (方案 §六 v3.3-N3 实现指引: 勿逐跳查询 — 最坏 8 次 DB
// 往返/每次数据请求; 逻辑设备量级为几十到几百 (§1.3), 一次性载入内存走链,
// 把 N 次往返压成 1 次). The same load carries merge_status so the
// shape-dedup enablement decision (HasDoneIncomingMerge) shares the round
// trip instead of adding one (§六: 与 scope 解析同往返).
type MergeGraph struct {
	mergedInto map[uint]*uint
	status     map[uint]string
}

// LoadMergeGraph loads id/merged_into/merge_status of every logical device
// in one query.
func LoadMergeGraph(db *gorm.DB) (*MergeGraph, error) {
	type row struct {
		ID          uint    `gorm:"column:id"`
		MergedInto  *uint   `gorm:"column:merged_into"`
		MergeStatus *string `gorm:"column:merge_status"`
	}
	var rows []row
	if err := db.Model(&models.LogicalDevice{}).
		Select("id", "merged_into", "merge_status").
		Scan(&rows).Error; err != nil {
		return nil, fmt.Errorf("datalifecycle: load merge graph: %w", err)
	}
	g := &MergeGraph{
		mergedInto: make(map[uint]*uint, len(rows)),
		status:     make(map[uint]string, len(rows)),
	}
	for _, r := range rows {
		g.mergedInto[r.ID] = r.MergedInto
		if r.MergeStatus != nil {
			g.status[r.ID] = *r.MergeStatus
		}
	}
	return g, nil
}

// Resolve walks merged_into in memory to the final merge target, bounded by
// mergeChainMaxHops so malformed chains (cycles) terminate. Returns
// logicalID itself when it has no merge edge. A merged_into pointing at a
// row that no longer exists also stops the walk (defensive: purge dissolves
// inbound edges before deleting a logical device, §4.3 B4).
func (g *MergeGraph) Resolve(logicalID uint) uint {
	cur := logicalID
	for i := 0; i < mergeChainMaxHops; i++ {
		next, ok := g.mergedInto[cur]
		if !ok || next == nil || *next == 0 || *next == cur {
			return cur
		}
		cur = *next
	}
	return cur
}

// HasDoneIncomingMerge reports whether any logical device has a COMPLETED
// merge into logicalID — the §六 shape-dedup enablement condition
// (EXISTS merged_into=? AND merge_status='done'). Targets without done
// incoming merges keep the original row shape (前端零改动).
func (g *MergeGraph) HasDoneIncomingMerge(logicalID uint) bool {
	for id, target := range g.mergedInto {
		if target != nil && *target == logicalID && g.status[id] == models.MergeStatusDone {
			return true
		}
	}
	return false
}

// ResolveMergeTarget loads the merge graph (one query) and resolves the
// final merge target of logicalID. The write path (consumers_heavy 双写)
// and the query protocol share this resolution so both sides of an
// in-progress merge land on the same chain (v3.2-F1: 写入与查询同链).
func ResolveMergeTarget(db *gorm.DB, logicalID uint) (uint, error) {
	if logicalID == 0 {
		return 0, nil
	}
	graph, err := LoadMergeGraph(db)
	if err != nil {
		return 0, err
	}
	return graph.Resolve(logicalID), nil
}
