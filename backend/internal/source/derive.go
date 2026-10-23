package source

import (
	"sort"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"
)

// DeriveInput 是派生器的输入（由收敛器一次性查库填充）。
//
// 关键：派生器本身**零 DB**（不 import gorm）—— 它只做纯计算。
// 这样全部判定逻辑都能在无数据库环境下穷举测试。
type DeriveInput struct {
	// Devices 是**已绑定逻辑设备**的边缘设备。
	//
	// 未绑定逻辑设备的设备不产生来源：来源键需要 (logicalDevice, category)，
	// 没有逻辑身份就无法定键（这类设备仍会正常采集入库，只是不参与来源台账）。
	Devices []models.EdgeDevice
	// Observed 是各设备实际观测过的类别（来自 sensor_field_obs，不是 unified_data ——
	// 后者生产 3000 万行，扫它会把一次派生变成分钟级操作）。
	Observed map[uint][]string
	// Groups 是现有来源组快照（只含 enabled 的，由调用方过滤）。
	Groups []*sourceref.GroupSnapshot
}

// GroupSnapshotOf 把 GroupSnapshot 转成 Handle 所需的成员信息。
//
// 单独函数是为了让「组 → Handle」的转换可独立测试，且避免 Derive 里嵌套过深。
type groupMember struct {
	EdgeDeviceID uint
	Priority     int
	Required     bool
}

// Derive 从观测事实推导期望态（**纯函数**：无 DB、无时钟、无随机，可重复调用）。
//
// # 判定规则
//
//  1. 遍历 Groups：每个组产生一个 Handle（带 GroupID/Kind/members/active）
//  2. 遍历 Devices+Observed：对**未被任何组覆盖**的 (logicalDevice, category)，
//     产生一个单设备 Handle（KindDirect）
//  3. 若同一 key 同时有组与单设备 ⇒ **组优先**（组是用户显式配置的意图，
//     自动派生不得覆盖人工决定）
//
// # 为什么组优先
//
// 这直接落实用户的「来源组手动创建、只有来源组支持主备」要求：用户建的组是
// 唯一的主备语义来源；自动派生只填「用户没配过」的空隙，绝不与人工配置竞争。
func Derive(in DeriveInput) []*Handle {
	// groupKeys 记录被来源组覆盖的键，用于步骤 3 的「组优先」。
	groupKeys := make(map[SourceKey]struct{}, len(in.Groups))
	out := make([]*Handle, 0, len(in.Groups)+len(in.Devices))

	// ── 步骤 1：来源组 → Handle ──
	for _, g := range in.Groups {
		if g == nil || !g.Enabled {
			// disabled 组不产生运行时来源（但它在 DB 里保留，用户可再启用）。
			continue
		}
		key := SourceKey{LogicalDeviceID: logicalDeviceForGroup(g, in.Devices), Category: g.Category}
		// ⚠ 陷阱：GroupSnapshot.ActiveMemberID 是**成员行 ID**（SourceGroupMember.ID），
		// 不是 EdgeDevice.ID。用错会得到「永远没有 active」或「随机命中」——
		// 且如果测试里恰好让两者数值相等，bug 会被完全掩盖（本项目踩过）。
		// 正确做法是用 sourceref 提供的 ActiveEdgeDeviceID()（它内部做 MemberID→EdgeDeviceID 映射）。
		activeEdgeID := g.ActiveEdgeDeviceID()
		members := make([]Member, 0, len(g.Members))
		memberByEdge := make(map[uint]struct{}, len(g.Members))
		activeByEdge := make(map[uint]struct{}, 1)
		for _, m := range g.Members {
			isActive := g.Kind == string(KindPrimaryBackup) && m.EdgeDeviceID == activeEdgeID
			members = append(members, Member{
				EdgeDeviceID: m.EdgeDeviceID,
				Priority:     m.Priority,
				Active:       isActive,
				Enabled:      true,
			})
			memberByEdge[m.EdgeDeviceID] = struct{}{}
			if isActive {
				activeByEdge[m.EdgeDeviceID] = struct{}{}
			}
		}
		kind := Kind(g.Kind)
		if kind == "" {
			kind = KindPrimaryBackup // 防御：DB 有 CHECK，理论上不会空
		}
		h := &Handle{
			key:           key,
			groupID:       g.ID,
			kind:          kind,
			members:       members,
			activeByEdge:  activeByEdge,
			memberByEdge:  memberByEdge,
			revision:      g.Revision,
			transitionSeq: g.TransitionSeq,
		}
		out = append(out, h)
		// 覆盖键以「组内全部成员设备 + 组 category」登记，供步骤 3 判重。
		// 注意：来源组的键可能跨逻辑设备（组是跨设备对象），因此这里用
		// 成员设备自身的逻辑身份逐个登记 —— 一台设备参与组之后，
		// 它的 (logicalDevice, category) 不应再派生出一条单设备直连。
		for _, m := range g.Members {
			if ld := logicalDeviceOfEdge(m.EdgeDeviceID, in.Devices); ld != 0 {
				groupKeys[SourceKey{LogicalDeviceID: ld, Category: g.Category}] = struct{}{}
			}
		}
		groupKeys[key] = struct{}{}
	}

	// ── 步骤 2：未被组覆盖的 (逻辑设备, 类别) → 单设备直连 ──
	// 先按 key 聚合候选设备（同一 key 可能有多台设备观测过同一类别）。
	type agg struct {
		members []Member
	}
	aggByKey := make(map[SourceKey]*agg)
	keyOrder := make([]SourceKey, 0, len(in.Devices))
	for i := range in.Devices {
		d := in.Devices[i]
		if d.LogicalDeviceID == nil || *d.LogicalDeviceID == 0 {
			continue // 无逻辑身份 ⇒ 无法定键（仍会采集入库，只是不进来源台账）
		}
		ld := *d.LogicalDeviceID
		for _, cat := range in.Observed[d.ID] {
			if cat == "" {
				continue
			}
			key := SourceKey{LogicalDeviceID: ld, Category: cat}
			if _, covered := groupKeys[key]; covered {
				continue // 组优先
			}
			a, ok := aggByKey[key]
			if !ok {
				a = &agg{}
				aggByKey[key] = a
				keyOrder = append(keyOrder, key)
			}
			a.members = append(a.members, Member{
				EdgeDeviceID: d.ID,
				Priority:     0,
				Active:       true, // 直连：自己就是权威
				Enabled:      d.Enabled,
			})
		}
	}
	for _, key := range keyOrder {
		a := aggByKey[key]
		// 排序：enabled 优先、再按设备 ID 升序（确定性 —— 不排序会让 active
		// 在多设备同类别时随机漂移，用户看到「来源在跳」）。
		sort.Slice(a.members, func(i, j int) bool {
			if a.members[i].Enabled != a.members[j].Enabled {
				return a.members[i].Enabled
			}
			return a.members[i].EdgeDeviceID < a.members[j].EdgeDeviceID
		})
		memberByEdge := make(map[uint]struct{}, len(a.members))
		activeByEdge := make(map[uint]struct{}, 1)
		for i := range a.members {
			memberByEdge[a.members[i].EdgeDeviceID] = struct{}{}
			// 直连语义：每台成员都是自己的权威（Accept 只看成员判定）。
			activeByEdge[a.members[i].EdgeDeviceID] = struct{}{}
		}
		out = append(out, &Handle{
			key:          key,
			groupID:      0,
			kind:         KindDirect,
			members:      a.members,
			activeByEdge: activeByEdge,
			memberByEdge: memberByEdge,
		})
	}
	sortHandles(out)
	return out
}

// logicalDeviceOfEdge 查设备绑定的逻辑设备 ID（未绑定返回 0）。
func logicalDeviceOfEdge(edgeDeviceID uint, devices []models.EdgeDevice) uint {
	for i := range devices {
		if devices[i].ID == edgeDeviceID {
			if devices[i].LogicalDeviceID != nil {
				return *devices[i].LogicalDeviceID
			}
			return 0
		}
	}
	return 0
}

// logicalDeviceForGroup 推断来源组对应的逻辑设备。
//
// 来源组是**跨设备**对象（成员可能分属不同逻辑设备），因此没有天然的单值
// logicalDevice。这里取「第一个有逻辑身份的成员」的逻辑设备作为代表键 ——
// 仅用于让组也有一个可展示的键；组内的权威判定走 Handle.Accepts，
// 不依赖这个代表键。
func logicalDeviceForGroup(g *sourceref.GroupSnapshot, devices []models.EdgeDevice) uint {
	for _, m := range g.Members {
		if ld := logicalDeviceOfEdge(m.EdgeDeviceID, devices); ld != 0 {
			return ld
		}
	}
	return 0
}

// sortHandles 按 (LogicalDeviceID, Category) 排序，保证输出稳定。
func sortHandles(hs []*Handle) {
	sort.Slice(hs, func(i, j int) bool {
		if hs[i].key.LogicalDeviceID != hs[j].key.LogicalDeviceID {
			return hs[i].key.LogicalDeviceID < hs[j].key.LogicalDeviceID
		}
		return hs[i].key.Category < hs[j].key.Category
	})
}
