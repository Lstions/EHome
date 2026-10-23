package source

import (
	"sync"
	"sync/atomic"
)

// Registry 持有全部来源的不可变快照，并提供**设备 → 来源**的反向索引。
//
// # 并发模型（公理 A3，与 sourceref.Registry 同范式）
//
// 读路径（Resolve/Accepts）零锁、零分配：原子读 atomic.Pointer。
// 写路径（Publish）只在数据源状态机事务提交后发生（冷路径），用 mu 串行化。
//
// # 为什么反向索引是必需的
//
// consumer 在每帧路径上只有 edgeDeviceID，而来源键是 (logicalDevice, category)。
// 没有 byDevice 索引就只能每帧查库或用 O(n) 线性扫 —— 前者违反「热路径零 DB」，
// 后者在来源数量增长后不可接受。
type Registry struct {
	mu sync.Mutex
	v  atomic.Pointer[view]
}

// view 是原子发布的整体视图：正向索引与反向索引必须同一次替换，
// 否则读者可能看到「正向有、反向无」的中间态。
type view struct {
	byKey map[SourceKey]*Handle
	// byDevice 是反向索引：edgeDeviceID → 它参与的来源键列表。
	//
	// 一台设备可能参与多个来源（例如同一台设备既供 BMS 的电压也供温度），
	// 因此值是切片而非单值。
	byDevice map[uint][]SourceKey
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	r := &Registry{}
	r.v.Store(&view{byKey: map[SourceKey]*Handle{}, byDevice: map[uint][]SourceKey{}})
	return r
}

// Publish 用给定 handles **整体替换**快照（唯一写入路径）。
//
// 语义：不在入参里的来源会被移除（真实反映派生结果）；因此调用方必须传完整集合，
// 而不是增量。这也是为什么它叫 Publish 而不是 Upsert。
func (r *Registry) Publish(handles []*Handle) {
	byKey := make(map[SourceKey]*Handle, len(handles))
	byDevice := make(map[uint][]SourceKey, len(handles))
	for _, h := range handles {
		if h == nil {
			continue
		}
		byKey[h.key] = h
		for edgeID := range h.memberByEdge {
			byDevice[edgeID] = append(byDevice[edgeID], h.key)
		}
	}
	r.mu.Lock()
	r.v.Store(&view{byKey: byKey, byDevice: byDevice})
	r.mu.Unlock()
}

// Resolve 按「设备 + 类别」解析来源（热路径：无锁、无分配）。
//
// 实现细节：按设备查候选源键，再按 category 匹配。同一设备通常只参与 1–3 个来源，
// 因此线性扫候选键比维护第二级 map 更省内存且更快（无 map 分配）。
func (r *Registry) Resolve(edgeDeviceID uint, category string) (*Handle, bool) {
	if r == nil {
		return nil, false
	}
	v := r.v.Load()
	if v == nil {
		return nil, false
	}
	keys := v.byDevice[edgeDeviceID]
	for _, k := range keys {
		if k.Category != category {
			continue
		}
		if h, ok := v.byKey[k]; ok {
			return h, true
		}
	}
	return nil, false
}

// ResolveByKey 按来源键解析。
func (r *Registry) ResolveByKey(k SourceKey) (*Handle, bool) {
	if r == nil {
		return nil, false
	}
	v := r.v.Load()
	if v == nil {
		return nil, false
	}
	h, ok := v.byKey[k]
	return h, ok
}

// Handles 返回全部来源（**只读快照切片**，按设备 ID 与类别排序保证稳定输出）。
//
// 排序是为了让投影/前端展示稳定 —— 不排序时 map 遍历顺序随机，
// 会导致同一份数据每次刷新行序不同（用户会以为数据在跳）。
func (r *Registry) Handles() []*Handle {
	if r == nil {
		return nil
	}
	v := r.v.Load()
	if v == nil {
		return nil
	}
	out := make([]*Handle, 0, len(v.byKey))
	for _, h := range v.byKey {
		out = append(out, h)
	}
	sortHandles(out)
	return out
}

// Len 返回来源数量（O(1)，用于「无来源」短路）。
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	v := r.v.Load()
	if v == nil {
		return 0
	}
	return len(v.byKey)
}

// DeviceSourceCount 返回某设备参与的来源数（诊断用：0 说明派生漏了该设备）。
func (r *Registry) DeviceSourceCount(edgeDeviceID uint) int {
	if r == nil {
		return 0
	}
	v := r.v.Load()
	if v == nil {
		return 0
	}
	return len(v.byDevice[edgeDeviceID])
}
