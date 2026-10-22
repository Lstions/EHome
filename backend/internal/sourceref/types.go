// Package sourceref 是「规则数据来源」的解析与合并层（方案《规则数据来源与复合来源》§5.1）。
//
// 职责边界（红线，方案 N-22）：
//   - 本包**不 import** gorm / database / databus / notify / websocket / os / io / net；
//     组快照由数据源域（datasource）构建后推入，本包只持有不可变快照。
//   - 本包**不感知** sensorfield（成员读取用字面量物理名，一期）。
//   - Combine 是**纯函数**（combine.go）：无锁、无 IO、无 goroutine、无 time.Now。
//
// 为什么需要它（现状缺陷）：规则原先只有一根裸设备 ID，数据源域的 active
// 在 automation/alert/databus 中零消费（grep 实测 0），所以「主备切换后规则自动
// 跟随」在代码层面无承载。
package sourceref

import (
	"sort"
	"strconv"
	"strings"
	"time"
)

// 组 kind（与 models.SourceGroupKind* 同值；此处不 import models 以保持包纯净，
// 由 datasource 层做映射与一致性门禁 SG-01）。
const (
	KindPrimaryBackup = "primary_backup"
	KindComposite     = "composite"
)

// 合并算子（与 models.Combine* 同值）。
const (
	CombineFirstAvailable = "first_available"
	CombineAvg            = "avg"
	CombineMax            = "max"
	CombineMin            = "min"
	CombineSum            = "sum"
)

// 缺员处置（与 models.OnDegrade* 同值）。
const (
	OnDegradeFailClosed   = "fail_closed"
	OnDegradeUseAvailable = "use_available"
)

// 成员运行态（与 models.SourceMemberStatus* 同值）。
const (
	MemberStatusActive   = "active"
	MemberStatusStandby  = "standby"
	MemberStatusError    = "error"
	MemberStatusDisabled = "disabled"
)

// 分量状态（求值期）。
const (
	// StateOK 有值且新鲜、成员有资格参与。
	StateOK = "ok"
	// StateMissing 成员没有该类别的任何值。
	StateMissing = "missing"
	// StateStale 有值但年龄超过 staleness（**视为缺席，不得用旧值**）。
	StateStale = "stale"
	// StateNotEligible 主备组成员状态不在 {active,standby}（如 error/disabled）。
	StateNotEligible = "not_eligible"
	// StateNotConfigured 组未启用 / 快照缺失（fail-closed，不静默跳过）。
	StateNotConfigured = "not_configured"
)

// 失败原因码（与方案 §7.1 / §12.9.2 对齐；写入事件审计，非 result 枚举）。
const (
	ReasonNoActive          = "source_no_active"
	ReasonRequiredMissing   = "required_missing"
	ReasonBelowMinMembers   = "below_min_members"
	ReasonNoAvailableMember = "no_available_member"
	ReasonActiveStale       = "active_stale"
	ReasonGroupDisabled     = "group_disabled"
	ReasonGroupMissing      = "group_missing"
)

// MemberSnapshot 是组内成员的不可变快照（含组内运行态）。
type MemberSnapshot struct {
	MemberID     uint
	EdgeDeviceID uint
	Priority     int
	Required     bool
	Status       string
	LastSuccess  time.Time // 零值 = 从未成功
	FailCount    int
}

// GroupSnapshot 是来源组的不可变快照。
//
// Revision 只在**拓扑/成员/active 变更**时递增（方案 F-1 bump 白名单）：
// 若把每帧 MarkSuccess 也算进 revision，窗口 identity 会每帧变化、DurationSec
// 永不满足（这是复评阶段发现的最高危回归）。
type GroupSnapshot struct {
	ID           uint
	Kind         string
	Category     string
	Combine      string
	MinMembers   int
	StalenessSec int
	OnDegrade    string
	Enabled      bool
	Revision     int64
	// TransitionSeq 在**权威成员变化**时递增（主备组）；composite 恒 0。
	TransitionSeq  uint64
	ActiveMemberID uint
	Members        []MemberSnapshot // 按 priority DESC, memberID ASC 排序
	MembersHash    string           // 静态成员集合哈希（不含运行态）
}

// ActiveEdgeDeviceID 返回权威成员的边缘设备 ID（0 = 无 active）。
func (g *GroupSnapshot) ActiveEdgeDeviceID() uint {
	if g == nil || g.ActiveMemberID == 0 {
		return 0
	}
	for _, m := range g.Members {
		if m.MemberID == g.ActiveMemberID {
			return m.EdgeDeviceID
		}
	}
	return 0
}

// IsMember 报告该边缘设备是否是组成员。
func (g *GroupSnapshot) IsMember(edgeDeviceID uint) bool {
	if g == nil {
		return false
	}
	for _, m := range g.Members {
		if m.EdgeDeviceID == edgeDeviceID {
			return true
		}
	}
	return false
}

// MemberOf 返回该边缘设备对应的成员快照。
func (g *GroupSnapshot) MemberOf(edgeDeviceID uint) (MemberSnapshot, bool) {
	if g == nil {
		return MemberSnapshot{}, false
	}
	for _, m := range g.Members {
		if m.EdgeDeviceID == edgeDeviceID {
			return m, true
		}
	}
	return MemberSnapshot{}, false
}

// DefaultStaleness 是 StalenessSec 未配置时的兜底阈值（30s）。
//
// 为什么必须有兜底，而不是「0 = 不启用陈旧判定」：
// 求值侧判陈旧用的是 at.Sub(rec.Timestamp) > g.Staleness()。若 0 表示「不启用」并
// 返回 0，则该比较变成 > 0 —— 只要时间戳比 now 早一点点就【恒为 stale】，于是复合组
// 的所有成员永远缺席、规则永不触发，而组看板看起来一切正常。
// 这是本仓「显式零值被当成语义」的又一处陷阱：0 必须是「按默认推导」，
// 绝不能是「全部过期」。
//
// 数据源域在保存期会把 0 归一为推导值（2x最大采集间隔，30s~1h）；这里 30s 是
// 运行时最后防线，防历史行 / 手工 SQL 写入 0 时再次静默全 stale。
const DefaultStaleness = 30 * time.Second

// Staleness 返回判定陈旧的时间阈值；未配置（<=0）时返回 DefaultStaleness。
func (g *GroupSnapshot) Staleness() time.Duration {
	if g == nil || g.StalenessSec <= 0 {
		return DefaultStaleness
	}
	return time.Duration(g.StalenessSec) * time.Second
}

// NormalizedOnDegrade 返回归一后的缺员处置（空串/未知值 = fail_closed）。
//
// first_available 语义就是「任一台在就够」（方案 §12.3 D-4 / §12.9.2 行⑤），
// 因此**强制归一为 use_available**，与 OnDegrade 字段取值无关（显式
// fail_closed 也不行 —— first_available 优先级更高，与 min_members 强制 1 同理）。
// 这是运行时最后防线：保存期（API/状态机）也会归一，但快照可能来自历史数据、
// 扫描器重建或手工修库，不能把正确性押在"每个发布方都记得归一"上。
func (g *GroupSnapshot) NormalizedOnDegrade() string {
	if g == nil {
		return OnDegradeFailClosed
	}
	if g.Combine == CombineFirstAvailable {
		return OnDegradeUseAvailable
	}
	switch g.OnDegrade {
	case OnDegradeUseAvailable:
		return OnDegradeUseAvailable
	default:
		return OnDegradeFailClosed
	}
}

// EffectiveMinMembers 返回生效的最少可用成员数（>=1）。
//
// 规则（方案 §12.9.2 / D-4）：first_available 强制 1（其语义是"任一台可用即可"，
// 与 NormalizedOnDegrade 的 first_available ⇒ use_available 归一同源）；其余取
// max(1, MinMembers)。
func (g *GroupSnapshot) EffectiveMinMembers() int {
	if g == nil {
		return 1
	}
	if g.Combine == CombineFirstAvailable {
		return 1
	}
	if g.MinMembers < 1 {
		return 1
	}
	return g.MinMembers
}

// SortMembers 按 priority DESC, memberID ASC 排序（R3 候选顺序）。
func SortMembers(ms []MemberSnapshot) {
	sort.SliceStable(ms, func(i, j int) bool {
		if ms[i].Priority != ms[j].Priority {
			return ms[i].Priority > ms[j].Priority
		}
		return ms[i].MemberID < ms[j].MemberID
	})
}

// ComputeMembersHash 计算静态成员集合哈希（只含 priority/required/edgeDeviceID）。
//
// **不得**包含 status/last_success/fail_count 等运行态 —— 否则每帧变化，
// 窗口 identity 失效（方案 F-1）。列表须已按 priority/edgeDeviceID 排序。
func ComputeMembersHash(ms []MemberSnapshot) string {
	cp := append([]MemberSnapshot(nil), ms...)
	sort.SliceStable(cp, func(i, j int) bool {
		if cp[i].EdgeDeviceID != cp[j].EdgeDeviceID {
			return cp[i].EdgeDeviceID < cp[j].EdgeDeviceID
		}
		return cp[i].Priority < cp[j].Priority
	})
	var b strings.Builder
	for _, m := range cp {
		b.WriteString(strconv.Itoa(int(m.EdgeDeviceID)))
		b.WriteByte(':')
		b.WriteString(strconv.Itoa(m.Priority))
		b.WriteByte(':')
		if m.Required {
			b.WriteByte('1')
		} else {
			b.WriteByte('0')
		}
		b.WriteByte(';')
	}
	return b.String()
}
