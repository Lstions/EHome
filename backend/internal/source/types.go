// Package source 是数据流架构的 L2 来源层。
//
// # 职责
//
// 把「数据从哪来」变成**运行时对象**。改造前这个位置是空的：数据从设备直接
// 跳到规则/告警，只有一张没人读的 data_sources 空表占着「数据源」的名字。
//
// # 为什么必须新增这一层（而不是复用 internal/sourceref）
//
// sourceref.Registry 的 view 只有 groups map[uint]*GroupSnapshot（key = groupID），
// 即**只能「已知组查组」**。而 consumer 手里只有 edgeDeviceID，不知道 groupID。
// 要在热路径回答「这台设备的这一帧算不算权威来源」，必须有**设备 → 来源**的
// 反向索引 —— sourceref 没有，且它已上线被规则求值链消费（MatchFrame 在每帧
// 路径上），往里加索引会动到已验证的热路径。
//
// 因此本层与 sourceref **并列**：sourceref 管「组的不可变快照 + 帧-组匹配」，
// 本层管「设备 → 来源的映射 + 单设备直连语义」。两者不重复造轮子。
//
// # 依赖方向（公理 A1）
//
//	databus → source → (internal/models, internal/sourceref)
//
// 本包**不得** import internal/datasource（防循环）、internal/databus、internal/api。
package source

import "time"

// Kind 是来源的类型（**有界枚举**，公理 A5）。
//
// 取值必须与既有词表逐字一致：source_groups.kind 用 primary_backup/composite
// （models.SourceGroupKind*），本层额外引入 direct 表示「无来源组」。
// 另造第三套词表会导致前端/后端/仿真三处漂移（既有门禁 SG-16 就是防这个）。
type Kind string

const (
	// KindDirect 单设备直连（无来源组，存量语义）。
	KindDirect Kind = "direct"
	// KindPrimaryBackup 主备组（同组至多一个 active）。
	KindPrimaryBackup Kind = "primary_backup"
	// KindComposite 复合组（多成员合并求值）。
	KindComposite Kind = "composite"
)

// String 实现 fmt.Stringer（日志/指标标签）。
func (k Kind) String() string { return string(k) }

// SourceKey 唯一标识一个「数据来源」。
//
// 与既有 data_sources 表的分组键一致：(逻辑设备, 类别)。这个口径不是新发明的 ——
// data_sources 的 uniqueIndex uniq_ds_source(device_id, category, edge_device_id)
// 里前两列就是它。
type SourceKey struct {
	// LogicalDeviceID 是逻辑设备 ID（= data_sources.device_id 的口径）。
	LogicalDeviceID uint
	// Category 是物理名（= unified_data.sensor_name，不归一）。
	Category string
}

// Member 是一个候选成员（自动派生）。
type Member struct {
	EdgeDeviceID uint
	// EdgeDeviceName 便于展示（派生时一次性带出，避免前端再查）。
	EdgeDeviceName string
	Priority       int
	// Active 仅主备有意义：是否当前权威。
	Active  bool
	Enabled bool
}

// Handle 是一个来源的运行时句柄（**不可变**，随快照整体替换）。
//
// 不可变是并发安全的前提：热路径读者无锁访问，写者整体替换快照（公理 A3）。
type Handle struct {
	key SourceKey

	// groupID = 0 表示无来源组（单设备直连）。
	groupID uint
	kind    Kind

	members []Member
	// activeByEdge 是主备组的 active 判定集合（预计算，热路径避免线性扫 members）。
	activeByEdge map[uint]struct{}
	// memberByEdge 是成员判定集合（预计算）。
	memberByEdge map[uint]struct{}

	// revision / transitionSeq 与 sourceref 同源，用于窗口身份。
	revision      int64
	transitionSeq uint64

	// groupName 便于展示（来源组名；direct 时为空）。
	groupName string
}

// Key 返回来源键。
func (h *Handle) Key() SourceKey {
	if h == nil {
		return SourceKey{}
	}
	return h.key
}

// GroupID 返回来源组 ID（0 = 直连）。
func (h *Handle) GroupID() uint {
	if h == nil {
		return 0
	}
	return h.groupID
}

// GroupName 返回来源组名（direct 时为空串）。
func (h *Handle) GroupName() string {
	if h == nil {
		return ""
	}
	return h.groupName
}

// Kind 返回来源类型。
func (h *Handle) Kind() Kind {
	if h == nil {
		return KindDirect
	}
	return h.kind
}

// Members 返回成员列表的**副本**（防调用方误改内部切片）。
func (h *Handle) Members() []Member {
	if h == nil {
		return nil
	}
	out := make([]Member, len(h.members))
	copy(out, h.members)
	return out
}

// Revision 返回快照 revision。
func (h *Handle) Revision() int64 {
	if h == nil {
		return 0
	}
	return h.revision
}

// TransitionSeq 返回权威成员变化计数（composite 恒 0）。
func (h *Handle) TransitionSeq() uint64 {
	if h == nil {
		return 0
	}
	return h.transitionSeq
}

// Accepts 报告该帧是否代表本来源（热路径：无锁、无分配、无 DB）。
//
// 语义与 sourceref.FrameMatch.Accepts **完全一致**，不另造第二套判定：
//
//   - 直连（groupID==0）：该设备是唯一成员时 true
//   - 主备：仅 active 成员 true（同组至多一个权威）
//   - 复合：任一成员 true（整组求值，无单一权威概念）
func (h *Handle) Accepts(edgeDeviceID uint) bool {
	if h == nil || edgeDeviceID == 0 {
		return false
	}
	if _, ok := h.memberByEdge[edgeDeviceID]; !ok {
		return false
	}
	if h.kind == KindPrimaryBackup {
		_, active := h.activeByEdge[edgeDeviceID]
		return active
	}
	return true
}

// Authoritative 报告该设备是否当前**唯一**权威。
//
// 与 Accepts 的区别：复合组 Accepts=true 但 Authoritative=false（无单一权威）。
// 用途：判断「要不要清窗口/触发切换审计」—— 复合组没有权威切换的概念。
func (h *Handle) Authoritative(edgeDeviceID uint) bool {
	if h == nil {
		return false
	}
	switch h.kind {
	case KindDirect:
		_, ok := h.memberByEdge[edgeDeviceID]
		return ok
	case KindPrimaryBackup:
		_, ok := h.activeByEdge[edgeDeviceID]
		return ok
	default: // composite
		return false
	}
}

// IsMember 报告该设备是否为本来源成员（不看 active）。
func (h *Handle) IsMember(edgeDeviceID uint) bool {
	if h == nil {
		return false
	}
	_, ok := h.memberByEdge[edgeDeviceID]
	return ok
}

// LastSeenHint 是派生时带出的健康信息（可选，投影展示用）。
type LastSeenHint struct {
	EdgeDeviceID uint
	LastSuccess  time.Time
}
