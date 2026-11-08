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
	// Devices 是参与来源派生的边缘设备（未软删除的那些）。
	//
	// 来源键 = (edge_device_id, category)：每台设备的每个**已观测类别**都是一条
	// 独立来源。历史口径曾是 (逻辑设备, 类别)，那让「未绑逻辑身份的设备不进台账」，
	// 而逻辑身份层已被移除（2026-10-23）—— 现在没有中间层可缺。
	Devices []models.EdgeDevice
	// Observed 是各设备实际观测过的类别（来自 sensor_field_obs，不是 unified_data ——
	// 后者生产 3000 万行，扫它会把一次派生变成分钟级操作）。
	Observed map[uint][]string
	// Groups 是现有来源组快照（只含 enabled 的，由调用方过滤）。
	Groups []*sourceref.GroupSnapshot
}

// Derive 从观测事实推导期望态（**纯函数**：无 DB、无时钟、无随机，可重复调用）。
//
// # 判定规则
//
//  1. 遍历 Groups：每个组产生一个 Handle（带 GroupID/Kind/members/active）
//  2. 遍历 Devices+Observed：对**未被任何组覆盖**的 (edge_device_id, category)，
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
		key := SourceKey{EdgeDeviceID: groupRepEdgeDevice(g), Category: g.Category}
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
		// 覆盖键按「组内全部成员设备 + 组 category」登记，供步骤 3 判重。
		//
		// 来源组是**跨设备**对象，但组键口径是 (edge_device_id, category)：
		// 一台设备参与组之后，它自己的 (edge_device_id, category) 不应再派生出一条
		// 单设备直连 —— 否则同一台设备同一类别会有两条来源，投影互相 disable。
		for _, m := range g.Members {
			groupKeys[SourceKey{EdgeDeviceID: m.EdgeDeviceID, Category: g.Category}] = struct{}{}
		}
		groupKeys[key] = struct{}{}
	}

	// ── 步骤 2：未被组覆盖的 (edge_device_id, 类别) → 单设备直连 ──
	seen := make(map[SourceKey]struct{}, len(in.Devices))
	for i := range in.Devices {
		d := in.Devices[i]
		if d.ID == 0 {
			continue // 行未落库（ID 未生成），无法定键
		}
		for _, cat := range in.Observed[d.ID] {
			if cat == "" {
				continue
			}
			key := SourceKey{EdgeDeviceID: d.ID, Category: cat}
			if _, covered := groupKeys[key]; covered {
				continue // 组优先
			}
			if _, dup := seen[key]; dup {
				// 同一设备同一类别被观测多次（sensor_field_obs 有重复行）⇒ 只一条来源。
				continue
			}
			seen[key] = struct{}{}
			edgeID := d.ID
			out = append(out, &Handle{
				key:          key,
				groupID:      0,
				kind:         KindDirect,
				members:      []Member{{EdgeDeviceID: edgeID, Priority: 0, Active: true, Enabled: d.Enabled}},
				activeByEdge: map[uint]struct{}{edgeID: {}},
				memberByEdge: map[uint]struct{}{edgeID: {}},
			})
		}
	}
	sortHandles(out)
	return out
}

// groupRepEdgeDevice 为来源组挑一个**代表**边缘设备 ID。
//
// 只用作 SourceKey 的 map 身份（byKey 的唯一性），**不参与任何数据判定** ——
// 组内「谁在供数」由 Handle.Accepts/Authoritative 按成员集合判定。
//
// 为什么取**最小**成员 ID：确定性（快照成员顺序可能随优先级调整而变）
// 且与主备切换解耦（active 变化不会让来源键漂移，否则投影会看到
// 「一条来源消失+另一条出现」的伪变化）。
func groupRepEdgeDevice(g *sourceref.GroupSnapshot) uint {
	if g == nil {
		return 0
	}
	var rep uint
	for _, m := range g.Members {
		if m.EdgeDeviceID == 0 {
			continue
		}
		if rep == 0 || m.EdgeDeviceID < rep {
			rep = m.EdgeDeviceID
		}
	}
	return rep
}

// sortHandles 按 (EdgeDeviceID, Category) 排序，保证输出稳定。
func sortHandles(hs []*Handle) {
	sort.Slice(hs, func(i, j int) bool {
		if hs[i].key.EdgeDeviceID != hs[j].key.EdgeDeviceID {
			return hs[i].key.EdgeDeviceID < hs[j].key.EdgeDeviceID
		}
		return hs[i].key.Category < hs[j].key.Category
	})
}
