package sourceref

import (
	"sync"
	"sync/atomic"
)

// FrameMatch 是一帧（某边缘设备上报）与某来源组的匹配结果。
//
// 求值期的过滤规则（方案 §12.2.1，按 kind 分流）：
//
//	primary_backup ⇒ 只有 IsActive 的成员帧可以驱动求值（否则 DurationSec 会被
//	  备成员帧拼接，见 S1）；
//	composite ⇒ 任一 IsMember 的帧都驱动**整组求值**（无 active 概念）。
type FrameMatch struct {
	GroupID       uint
	Kind          string
	Category      string
	IsMember      bool
	IsActive      bool
	Revision      int64
	TransitionSeq uint64
	// Group 是命中时的不可变快照（nil = 未命中/未配置），供 Combine 使用。
	Group *GroupSnapshot
	// Configured=false 表示组不存在或未启用：调用方必须 fail-closed 并落审计，
	// **不得**静默退回「任意设备触发」。
	Configured bool
	Reason     string
}

// Accepts 报告该帧是否应驱动求值（按 kind 分流）。
func (m FrameMatch) Accepts() bool {
	if !m.Configured || !m.IsMember {
		return false
	}
	if m.Kind == KindPrimaryBackup {
		return m.IsActive
	}
	return true
}

// view 是原子发布的整体视图（groups 与所有派生索引必须同一次替换）。
//
// 为什么不让 Registry 自己加锁查 map：MatchFrame 在每帧热路径上调用，
// 目标是零 DB、零写锁；用 atomic.Pointer 整体替换快照，读者无锁。
type view struct {
	groups map[uint]*GroupSnapshot
}

// Registry 持有全部来源组的不可变快照。
//
// 所有权（方案 §12.2.2 / N-13）：**快照的唯一发布者是数据源状态机**
// （datasource 在事务提交后调用 Publish/ReplaceAll）。切换钩子只做「清窗加速 +
// 审计」，不在快照真值路径上 —— 钩子丢失不应导致规则用陈旧 active 过滤。
type Registry struct {
	mu sync.Mutex // 只保护发布写路径（冷），读路径无锁
	v  atomic.Pointer[view]
}

// NewRegistry 创建空注册表。
func NewRegistry() *Registry {
	r := &Registry{}
	r.v.Store(&view{groups: map[uint]*GroupSnapshot{}})
	return r
}

// ReplaceAll 用给定快照整体替换（启动/全量重载）。
func (r *Registry) ReplaceAll(gs []*GroupSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	m := make(map[uint]*GroupSnapshot, len(gs))
	for _, g := range gs {
		if g != nil && g.ID != 0 {
			m[g.ID] = g
		}
	}
	r.v.Store(&view{groups: m})
}

// Publish 发布单个组的最新快照（copy-on-write 整表替换）。
//
// 语义：g==nil 或 g.ID==0 无操作；Enabled=false 的快照会保留但 MatchFrame 返回
// Configured=false（reason=group_disabled），由调用方 fail-closed。
func (r *Registry) Publish(g *GroupSnapshot) {
	if g == nil || g.ID == 0 {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.v.Load()
	m := make(map[uint]*GroupSnapshot, len(old.groups)+1)
	for k, v := range old.groups {
		m[k] = v
	}
	m[g.ID] = g
	r.v.Store(&view{groups: m})
}

// Remove 移除组快照（组删除）。
func (r *Registry) Remove(groupID uint) {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.v.Load()
	if _, ok := old.groups[groupID]; !ok {
		return
	}
	m := make(map[uint]*GroupSnapshot, len(old.groups))
	for k, v := range old.groups {
		if k != groupID {
			m[k] = v
		}
	}
	r.v.Store(&view{groups: m})
}

// Group 按 ID 取快照。
func (r *Registry) Group(id uint) (*GroupSnapshot, bool) {
	if r == nil || id == 0 {
		return nil, false
	}
	v := r.v.Load()
	if v == nil {
		return nil, false
	}
	g, ok := v.groups[id]
	return g, ok
}

// Len 返回当前快照中的组数量（运维/门禁用）。
func (r *Registry) Len() int {
	if r == nil {
		return 0
	}
	v := r.v.Load()
	if v == nil {
		return 0
	}
	return len(v.groups)
}

// MatchFrame 判断该帧与来源组的匹配关系（热路径：无锁、无 DB、无分配）。
//
// kind 与 groupID 来自规则绑定列（models.SourceRef）。kind 不是 source_group 时
// 返回零值（调用方走存量单设备路径）。
func (r *Registry) MatchFrame(edgeDeviceID uint, kind string, groupID uint) FrameMatch {
	if kind != GroupKindSourceGroup || groupID == 0 {
		return FrameMatch{}
	}
	g, ok := r.Group(groupID)
	if !ok || g == nil {
		return FrameMatch{GroupID: groupID, Configured: false, Reason: ReasonGroupMissing}
	}
	if !g.Enabled {
		return FrameMatch{GroupID: groupID, Kind: g.Kind, Category: g.Category,
			Configured: false, Reason: ReasonGroupDisabled, Group: g,
			Revision: g.Revision, TransitionSeq: g.TransitionSeq}
	}
	m := FrameMatch{
		GroupID:       groupID,
		Kind:          g.Kind,
		Category:      g.Category,
		IsMember:      g.IsMember(edgeDeviceID),
		Revision:      g.Revision,
		TransitionSeq: g.TransitionSeq,
		Group:         g,
		Configured:    true,
	}
	if g.Kind == KindPrimaryBackup {
		m.IsActive = g.ActiveEdgeDeviceID() == edgeDeviceID
	} else {
		// composite 没有 active 概念（I5）：任一成员帧都可驱动整组求值。
		m.IsActive = m.IsMember
	}
	return m
}

// GroupKindSourceGroup 是「规则绑定到来源组」的 source_kind 取值（与 models 同值）。
const GroupKindSourceGroup = "source_group"
