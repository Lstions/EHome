package source_test

import (
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/internal/source"
	"ehome/backend/internal/sourceref"
)

// 本文件是 L2 来源层的契约测试。
//
// 纪律：**零 DB**（source 包不 import gorm）。派生是纯函数，没有理由为它起库。

func ld(id uint) *uint { return &id }

func dev(id uint, logical uint, enabled bool) models.EdgeDevice {
	d := models.EdgeDevice{ID: id, Name: "d", Enabled: enabled}
	if logical != 0 {
		d.LogicalDeviceID = ld(logical)
	}
	return d
}

// ---------- 反向索引（本层存在的核心理由）----------

// TestRegistry_ResolveByDevice 验证「已知设备 + 类别 ⇒ 来源」。
//
// 这是 sourceref 做不到的事（它只有 groups map，必须先知道 groupID）。
func TestRegistry_ResolveByDevice(t *testing.T) {
	r := source.NewRegistry()
	handles := source.Derive(source.DeriveInput{
		Devices:  []models.EdgeDevice{dev(1, 10, true)},
		Observed: map[uint][]string{1: {"rsoc", "voltage"}},
	})
	r.Publish(handles)

	if r.Len() != 2 {
		t.Fatalf("Len = %d, want 2（两个类别两条来源）", r.Len())
	}
	h, ok := r.Resolve(1, "rsoc")
	if !ok {
		t.Fatal("Resolve(1, rsoc) 未命中 —— 反向索引失效")
	}
	if h.Key().Category != "rsoc" {
		t.Errorf("category = %q", h.Key().Category)
	}
	if h.Kind() != source.KindDirect {
		t.Errorf("Kind = %q, want direct", h.Kind())
	}
	// 不存在的类别必须未命中（不能返回第一个候选）
	if _, ok := r.Resolve(1, "nonexistent"); ok {
		t.Error("未观测的类别不应命中")
	}
	// 不存在的设备
	if _, ok := r.Resolve(999, "rsoc"); ok {
		t.Error("未知设备不应命中")
	}
}

// TestRegistry_DeviceSourceCount 用于诊断「派生漏了某设备」。
func TestRegistry_DeviceSourceCount(t *testing.T) {
	r := source.NewRegistry()
	r.Publish(source.Derive(source.DeriveInput{
		Devices:  []models.EdgeDevice{dev(1, 10, true)},
		Observed: map[uint][]string{1: {"a", "b", "c"}},
	}))
	if got := r.DeviceSourceCount(1); got != 3 {
		t.Errorf("DeviceSourceCount(1) = %d, want 3", got)
	}
	if got := r.DeviceSourceCount(2); got != 0 {
		t.Errorf("DeviceSourceCount(2) = %d, want 0", got)
	}
}

// TestRegistry_PublishReplaces 验证 Publish 是**整体替换**而非增量。
//
// 这条很重要：若误做成增量，设备消失后来源会残留，投影永远不收敛。
func TestRegistry_PublishReplaces(t *testing.T) {
	r := source.NewRegistry()
	r.Publish(source.Derive(source.DeriveInput{
		Devices:  []models.EdgeDevice{dev(1, 10, true), dev(2, 20, true)},
		Observed: map[uint][]string{1: {"a"}, 2: {"b"}},
	}))
	if r.Len() != 2 {
		t.Fatalf("初始 Len = %d", r.Len())
	}
	// 只发布设备 1 ⇒ 设备 2 的来源应消失
	r.Publish(source.Derive(source.DeriveInput{
		Devices:  []models.EdgeDevice{dev(1, 10, true)},
		Observed: map[uint][]string{1: {"a"}},
	}))
	if r.Len() != 1 {
		t.Errorf("Publish 后 Len = %d, want 1（必须是整体替换，否则残留旧来源）", r.Len())
	}
	if _, ok := r.Resolve(2, "b"); ok {
		t.Error("已移除的来源仍可解析 —— Publish 不是整体替换")
	}
}

// ---------- 直连语义 ----------

// TestHandle_DirectAccepts 验证直连来源的判定。
func TestHandle_DirectAccepts(t *testing.T) {
	handles := source.Derive(source.DeriveInput{
		Devices:  []models.EdgeDevice{dev(1, 10, true)},
		Observed: map[uint][]string{1: {"a"}},
	})
	h := handles[0]
	if !h.Accepts(1) {
		t.Error("成员设备应 Accepts")
	}
	if h.Accepts(2) {
		t.Error("非成员设备不应 Accepts")
	}
	if !h.Authoritative(1) {
		t.Error("直连成员是权威")
	}
}

// TestDerive_UnboundDeviceProducesNoSource 验证未绑逻辑设备的设备不产生来源。
//
// 语义：它能正常采集入库，但没有 (logicalDevice, category) 键，无法定来源。
func TestDerive_UnboundDeviceProducesNoSource(t *testing.T) {
	handles := source.Derive(source.DeriveInput{
		Devices:  []models.EdgeDevice{dev(1, 0, true)}, // logical = 0
		Observed: map[uint][]string{1: {"a"}},
	})
	if len(handles) != 0 {
		t.Errorf("未绑逻辑设备的设备不应产生来源，实际 %d 条", len(handles))
	}
}

// ---------- 来源组优先（用户手动配置不被自动派生覆盖）----------

// TestDerive_GroupTakesPrecedence 是本层最重要的语义断言。
//
// 落实用户要求「来源组手动创建、只有来源组支持主备」：
// 用户建了组，自动派生就不得再为同一 key 生成单设备直连。
func TestDerive_GroupTakesPrecedence(t *testing.T) {
	g := &sourceref.GroupSnapshot{
		ID: 100, Kind: "primary_backup", Category: "rsoc", Enabled: true,
		// 注意：ActiveMemberID 是**成员行 ID**（MemberSnapshot.MemberID），不是设备 ID。
		// 这里成员行 901 对应设备 1，所以权威是设备 1。
		ActiveMemberID: 901,
		Members: []sourceref.MemberSnapshot{
			{MemberID: 901, EdgeDeviceID: 1, Priority: 10},
			{MemberID: 902, EdgeDeviceID: 2, Priority: 5},
		},
	}
	handles := source.Derive(source.DeriveInput{
		Devices:  []models.EdgeDevice{dev(1, 10, true), dev(2, 10, true)},
		Observed: map[uint][]string{1: {"rsoc"}, 2: {"rsoc"}},
		Groups:   []*sourceref.GroupSnapshot{g},
	})

	// 两台设备同属逻辑设备 10、同类别 rsoc ⇒ 若组不优先会产生 2 条直连 + 1 条组
	if len(handles) != 1 {
		t.Fatalf("产生 %d 条来源，want 1（组应覆盖直连，不并存）", len(handles))
	}
	h := handles[0]
	if h.Kind() != source.KindPrimaryBackup {
		t.Errorf("Kind = %q, want primary_backup", h.Kind())
	}
	if h.GroupID() != 100 {
		t.Errorf("GroupID = %d, want 100", h.GroupID())
	}
}

// TestHandle_PrimaryBackupOnlyActiveAccepts 验证主备只认 active。
func TestHandle_PrimaryBackupOnlyActiveAccepts(t *testing.T) {
	g := &sourceref.GroupSnapshot{
		ID: 1, Kind: "primary_backup", Category: "v", Enabled: true,
		// ActiveMemberID 是成员行 ID；成员行 902 对应设备 2 ⇒ 设备 2 是权威。
		ActiveMemberID: 902,
		Members: []sourceref.MemberSnapshot{
			{MemberID: 901, EdgeDeviceID: 1, Priority: 10},
			{MemberID: 902, EdgeDeviceID: 2, Priority: 5},
		},
	}
	handles := source.Derive(source.DeriveInput{
		Devices: []models.EdgeDevice{dev(1, 10, true), dev(2, 10, true)},
		Groups:  []*sourceref.GroupSnapshot{g},
	})
	h := handles[0]

	if h.Accepts(1) {
		t.Error("非 active 成员不应 Accepts（主备只认权威）")
	}
	if !h.Accepts(2) {
		t.Error("active 成员应 Accepts")
	}
	if h.Authoritative(1) {
		t.Error("非 active 不应是权威")
	}
	if !h.Authoritative(2) {
		t.Error("active 应是权威")
	}
	// 非成员
	if h.Accepts(99) {
		t.Error("非成员不应 Accepts")
	}
}

// TestHandle_CompositeAnyMemberAccepts 验证复合组任一成员都可驱动。
func TestHandle_CompositeAnyMemberAccepts(t *testing.T) {
	g := &sourceref.GroupSnapshot{
		ID: 2, Kind: "composite", Category: "t", Enabled: true,
		Members: []sourceref.MemberSnapshot{
			{EdgeDeviceID: 1, Priority: 1},
			{EdgeDeviceID: 2, Priority: 1},
		},
	}
	handles := source.Derive(source.DeriveInput{
		Devices: []models.EdgeDevice{dev(1, 10, true), dev(2, 10, true)},
		Groups:  []*sourceref.GroupSnapshot{g},
	})
	h := handles[0]
	if !h.Accepts(1) || !h.Accepts(2) {
		t.Error("复合组任一成员都应 Accepts")
	}
	// 复合组没有单一权威
	if h.Authoritative(1) || h.Authoritative(2) {
		t.Error("复合组不应有单一权威（Authoritative 必须恒 false）")
	}
}

// TestDerive_DisabledGroupProducesNothing 验证停用的组不产生运行时来源。
func TestDerive_DisabledGroupProducesNothing(t *testing.T) {
	g := &sourceref.GroupSnapshot{
		ID: 3, Kind: "primary_backup", Category: "x", Enabled: false,
		Members: []sourceref.MemberSnapshot{{EdgeDeviceID: 1}},
	}
	handles := source.Derive(source.DeriveInput{
		Devices: []models.EdgeDevice{dev(1, 10, true)},
		Groups:  []*sourceref.GroupSnapshot{g},
	})
	for _, h := range handles {
		if h.GroupID() == 3 {
			t.Error("disabled 组不应产生运行时来源")
		}
	}
}

// ---------- 确定性与幂等 ----------

// TestDerive_Deterministic 验证同一输入多次派生结果稳定。
//
// 不稳定的后果：投影每次刷新行序变化，用户会以为数据在跳。
func TestDerive_Deterministic(t *testing.T) {
	in := source.DeriveInput{
		Devices: []models.EdgeDevice{dev(3, 10, true), dev(1, 10, true), dev(2, 20, true)},
		Observed: map[uint][]string{
			1: {"b", "a"}, 2: {"c"}, 3: {"a"},
		},
	}
	first := source.Derive(in)
	for i := 0; i < 5; i++ {
		next := source.Derive(in)
		if len(next) != len(first) {
			t.Fatalf("第 %d 次派生数量不同: %d vs %d", i, len(next), len(first))
		}
		for j := range next {
			if next[j].Key() != first[j].Key() {
				t.Fatalf("第 %d 次派生顺序不同: [%d] %v vs %v", i, j, next[j].Key(), first[j].Key())
			}
		}
	}
}

// TestDerive_MultiDeviceSameCategoryPicksEnabledFirst 验证同类别多设备时
// enabled 设备排在前面（active 判定优先可用设备）。
func TestDerive_MultiDeviceSameCategoryPicksEnabledFirst(t *testing.T) {
	handles := source.Derive(source.DeriveInput{
		Devices: []models.EdgeDevice{
			dev(5, 10, false), // disabled，ID 更大
			dev(2, 10, true),  // enabled
		},
		Observed: map[uint][]string{5: {"x"}, 2: {"x"}},
	})
	if len(handles) != 1 {
		t.Fatalf("handles = %d", len(handles))
	}
	members := handles[0].Members()
	if len(members) != 2 {
		t.Fatalf("members = %d, want 2", len(members))
	}
	if members[0].EdgeDeviceID != 2 {
		t.Errorf("首个成员 = %d, want 2（enabled 应优先）", members[0].EdgeDeviceID)
	}
}

// TestRegistry_NilSafe 验证 nil 注册表不会 panic（防热路径崩）。
func TestRegistry_NilSafe(t *testing.T) {
	var r *source.Registry
	if _, ok := r.Resolve(1, "a"); ok {
		t.Error("nil registry 应未命中")
	}
	if r.Len() != 0 {
		t.Error("nil registry Len 应为 0")
	}
	if r.Handles() != nil {
		t.Error("nil registry Handles 应为 nil")
	}
	if r.DeviceSourceCount(1) != 0 {
		t.Error("nil registry DeviceSourceCount 应为 0")
	}
}

// TestHandle_NilSafe 验证 nil Handle 的安全降级（fail-closed）。
func TestHandle_NilSafe(t *testing.T) {
	var h *source.Handle
	if h.Accepts(1) {
		t.Error("nil handle 不应 Accepts（fail-closed）")
	}
	if h.Authoritative(1) {
		t.Error("nil handle 不应是权威")
	}
	if h.Kind() != source.KindDirect {
		t.Error("nil handle Kind 应为 direct")
	}
}
