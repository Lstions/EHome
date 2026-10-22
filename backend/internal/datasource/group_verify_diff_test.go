package datasource

// 独立验证（impl-core）：legacy data_sources 状态机 vs 来源组适配器 **等价性差分**。
//
// 方法：同一随机操作序列分别打在两个独立世界（文件型 SQLite）：
//   - legacy：data_sources 行 + Service（MarkSuccess/MarkFailure/ScanStale/Activate/Deactivate/Reset）
//   - group ：source_group + members + GroupService（同名语义方法）
// 每步之后比较**可观测状态轨迹**：active 的 edgeDeviceID、每个成员的 status/fail_count/
// last_success 是否已写、切换日志条数、扫描返回值、手动操作错误类别。任一步不一致即失败。
//
// Windows of intentional divergence（不在差分内）：
//   - N-21 自愈：组无 active 时 GroupService 会自愈补选，legacy **没有**该行为；
//     因此生成器禁止在两世界都无 active 时执行 scan（先修好权威再比）。
//   - revision / ActiveChange / health 事件是组路径新增能力，不属于 legacy 可观测状态。
//
// 这是"决策逻辑只有一份"的反向证明：若 group 适配器复制/漂移了任何一个判定
// （阈值、候选序、冷却、驻留期、错误方向），差分会在对应步骤直接抓到。

import (
	"errors"
	"fmt"
	"math/rand"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/sourceref"

	"gorm.io/gorm"
)

// diffMember 是某边缘设备在一组来源中的可观测状态。
type diffMember struct {
	Status         string
	FailCount      int
	LastSuccessSet bool
}

// diffState 是一个世界在某时刻的可观测状态。
type diffState struct {
	ActiveEdge uint // 0 = 无权威
	Members    map[uint]diffMember
	SwitchLogs int
}

func (s diffState) String() string {
	out := fmt.Sprintf("active=%d logs=%d members={", s.ActiveEdge, s.SwitchLogs)
	for _, e := range []uint{0, 1, 2} { // 稳定顺序（至多 3 成员；多余的忽略）
		if m, ok := s.Members[e]; ok {
			out += fmt.Sprintf("%d:%s/%d/%v ", e, m.Status, m.FailCount, m.LastSuccessSet)
		}
	}
	return out + "}"
}

// legacyWorld 是 legacy data_sources 世界。
type legacyWorld struct {
	svc    *Service
	db     *gorm.DB
	cfg    CreateInput // 模板
	srcIDs map[uint]uint
}

func (w *legacyWorld) state(t *testing.T) diffState {
	t.Helper()
	var rows []models.DataSource
	if err := w.db.Where("device_id = ? AND category = ?", w.cfg.DeviceID, w.cfg.Category).
		Order("edge_device_id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("legacy query sources: %v", err)
	}
	st := diffState{Members: map[uint]diffMember{}}
	for _, r := range rows {
		st.Members[r.EdgeDeviceID] = diffMember{
			Status: r.Status, FailCount: r.FailCount, LastSuccessSet: r.LastSuccess != nil,
		}
		if r.Status == StatusActive {
			st.ActiveEdge = r.EdgeDeviceID
		}
	}
	var logs int64
	if err := w.db.Model(&models.FailoverLog{}).
		Where("device_id = ? AND category = ?", w.cfg.DeviceID, w.cfg.Category).Count(&logs).Error; err != nil {
		t.Fatalf("legacy count failover logs: %v", err)
	}
	st.SwitchLogs = int(logs)
	return st
}

// groupWorld 是来源组世界。
type groupWorld struct {
	gsvc    *GroupService
	reg     *sourceref.Registry
	db      *gorm.DB
	groupID uint
	memIDs  map[uint]uint // edgeDeviceID -> member row ID
}

func (w *groupWorld) state(t *testing.T) diffState {
	t.Helper()
	var rows []models.SourceGroupMember
	if err := w.db.Where("group_id = ?", w.groupID).Order("edge_device_id ASC").Find(&rows).Error; err != nil {
		t.Fatalf("group query members: %v", err)
	}
	st := diffState{Members: map[uint]diffMember{}}
	for _, r := range rows {
		st.Members[r.EdgeDeviceID] = diffMember{
			Status: r.Status, FailCount: r.FailCount, LastSuccessSet: r.LastSuccess != nil,
		}
		if r.Status == models.SourceMemberStatusActive {
			st.ActiveEdge = r.EdgeDeviceID
		}
	}
	var logs int64
	if err := w.db.Model(&models.SourceGroupFailoverLog{}).
		Where("group_id = ?", w.groupID).Count(&logs).Error; err != nil {
		t.Fatalf("group count failover logs: %v", err)
	}
	st.SwitchLogs = int(logs)
	return st
}

// diffOp 是一次操作。
type diffOp struct {
	kind string // success|failure|scan|activate|deactivate|reset
	edge uint   // 目标边缘设备（scan 忽略）
}

// setupDiffWorlds 建两个等价世界（同一个 Now 时钟、同一组 edgeDeviceID=1/2/3）。
func setupDiffWorlds(t *testing.T, clock *time.Time) (*legacyWorld, *groupWorld) {
	t.Helper()
	nowFn := func() time.Time { return *clock }
	opts := Options{Cooldown: 5 * time.Minute, MinResidency: 2 * time.Minute,
		Staleness: 5 * time.Minute, ScanInterval: time.Hour, Now: nowFn}
	const deviceID = uint(1)
	const category = "illuminance"
	priorities := []int{100, 90, 80}
	edges := make([]uint, len(priorities))
	for i := range edges {
		edges[i] = uint(i + 1) // 统一用 edgeDeviceID = 1,2,3（两世界可比较）
	}

	// ── legacy 世界：data_sources 行 ──
	ldb := openVerifyDB(t)
	lw := &legacyWorld{svc: New(ldb, opts), db: ldb, srcIDs: map[uint]uint{},
		cfg: CreateInput{DeviceID: deviceID, Category: category}}
	lw.svc.SetNotifier(func(models.Notification) {})
	for i, p := range priorities {
		ds, err := lw.svc.Create(CreateInput{
			DeviceID: deviceID, Category: category, EdgeDeviceID: edges[i],
			Priority: p, MaxFailCount: defaultMaxFailCount, Name: fmt.Sprintf("legacy-%d", edges[i]),
		})
		if err != nil {
			t.Fatalf("legacy Create edge %d: %v", edges[i], err)
		}
		lw.srcIDs[edges[i]] = ds.ID
	}

	// ── group 世界：边缘设备 + 来源组 ──
	gdb := openVerifyDB(t)
	reg := sourceref.NewRegistry()
	gw := &groupWorld{gsvc: NewGroupService(gdb, opts), reg: reg, db: gdb, memIDs: map[uint]uint{}}
	gw.gsvc.SetGroupRegistry(reg)
	gw.gsvc.SetNotifier(func(models.Notification) {})
	members := make([]GroupMemberInput, 0, len(edges))
	for i, edge := range edges {
		ed := models.EdgeDevice{ID: edge, Name: fmt.Sprintf("edge-%d", edge), NodeID: "node-1", ChannelID: 1,
			IntervalMs: 5000, Enabled: true, Type: "prs3001"}
		if err := gdb.Create(&ed).Error; err != nil {
			t.Fatalf("create edge device: %v", err)
		}
		if err := gdb.Create(&models.SensorFieldObs{DeviceID: ed.ID, SensorName: category}).Error; err != nil {
			t.Fatalf("create obs: %v", err)
		}
		members = append(members, GroupMemberInput{EdgeDeviceID: ed.ID, Priority: priorities[i]})
	}
	groupRow, err := gw.gsvc.CreateGroup(CreateGroupInput{
		Name: "diff-group", Kind: models.SourceGroupKindPrimaryBackup, Category: category,
		StalenessSec: 300, // 关键：与 legacy opts.Staleness=5m 对齐（否则 DeriveStaleness 走 30s 默认）
		Members:      members,
	})
	if err != nil {
		t.Fatalf("group CreateGroup: %v", err)
	}
	gw.groupID = groupRow.ID
	for _, m := range gw.gsvc.mustListMembers(t, groupRow.ID) {
		gw.memIDs[m.EdgeDeviceID] = m.ID
	}
	if len(gw.memIDs) != len(priorities) {
		t.Fatalf("group members = %d want %d", len(gw.memIDs), len(priorities))
	}
	return lw, gw
}

// mustListMembers 是测试辅助（避免 t 参数在循环里丢失）。
func (g *GroupService) mustListMembers(t *testing.T, groupID uint) []models.SourceGroupMember {
	t.Helper()
	ms, err := g.ListMembers(groupID)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	return ms
}

// runDiffOp 在两侧执行同一操作，并比较错误类别。
func runDiffOp(t *testing.T, lw *legacyWorld, gw *groupWorld, clock *time.Time, op diffOp) {
	t.Helper()
	switch op.kind {
	case "success":
		*clock = clock.Add(time.Second)
		lw.svc.MarkSuccess(op.edge, []string{lw.cfg.Category}, *clock)
		gw.gsvc.MarkSuccess(op.edge, []string{"illuminance"}, *clock)
	case "failure":
		*clock = clock.Add(time.Second)
		lw.svc.MarkFailure(op.edge, TriggerDeviceOffline)
		gw.gsvc.MarkFailure(op.edge, TriggerDeviceOffline)
	case "scan":
		*clock = clock.Add(30 * time.Second)
		lN, lErr := lw.svc.ScanStale(*clock)
		gN, gErr := gw.gsvc.ScanGroups(*clock)
		if (lErr == nil) != (gErr == nil) {
			t.Fatalf("scan 错误类别不一致: legacy=%v group=%v", lErr, gErr)
		}
		if lN != gN {
			t.Fatalf("scan 切换次数不一致: legacy=%d group=%d", lN, gN)
		}
	case "activate":
		*clock = clock.Add(time.Second)
		_, lErr := lw.svc.Activate(lw.srcIDs[op.edge])
		_, gErr := gw.gsvc.ActivateGroupMember(gw.groupID, gw.memIDs[op.edge])
		compareErrClass(t, "activate", lErr, gErr)
	case "deactivate":
		*clock = clock.Add(time.Second)
		_, lErr := lw.svc.Deactivate(lw.srcIDs[op.edge])
		_, gErr := gw.gsvc.DeactivateGroupMember(gw.groupID, gw.memIDs[op.edge])
		compareErrClass(t, "deactivate", lErr, gErr)
	case "reset":
		*clock = clock.Add(time.Second)
		_, lErr := lw.svc.Reset(lw.srcIDs[op.edge])
		_, gErr := gw.gsvc.ResetGroupMember(gw.groupID, gw.memIDs[op.edge])
		compareErrClass(t, "reset", lErr, gErr)
	default:
		t.Fatalf("unknown op %q", op.kind)
	}
}

// errClass 把错误归为可比较类别（两域哨兵错误不同名，按语义归类）。
func errClass(err error) string {
	switch {
	case err == nil:
		return "nil"
	case errors.Is(err, ErrConflict):
		return "conflict"
	case errors.Is(err, ErrInvalidRequest):
		return "invalid"
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrGroupNotFound):
		return "notfound"
	default:
		return "other:" + err.Error()
	}
}

func compareErrClass(t *testing.T, op string, lErr, gErr error) {
	t.Helper()
	lc, gc := errClass(lErr), errClass(gErr)
	if lc != gc {
		t.Fatalf("%s 错误类别不一致: legacy=%q(%v) group=%q(%v)", op, lc, lErr, gc, gErr)
	}
}

// genDiffOps 生成确定性操作序列；scan 仅在两侧都有 active 时允许（避开 N-21 新行为）。
func genDiffOps(seed int64, steps int, lw *legacyWorld, gw *groupWorld, t *testing.T) []diffOp {
	rng := rand.New(rand.NewSource(seed))
	edges := make([]uint, 0, len(lw.srcIDs))
	for e := range lw.srcIDs {
		edges = append(edges, e)
	}
	// 稳定顺序（map 遍历随机）。
	for i := 1; i < len(edges); i++ {
		for j := i; j > 0 && edges[j] < edges[j-1]; j-- {
			edges[j], edges[j-1] = edges[j-1], edges[j]
		}
	}
	kinds := []string{"success", "success", "failure", "failure", "failure", "reset", "activate", "deactivate"}
	ops := make([]diffOp, 0, steps)
	for len(ops) < steps {
		kind := kinds[rng.Intn(len(kinds))]
		if rng.Intn(100) < 12 {
			kind = "scan"
		}
		edge := edges[rng.Intn(len(edges))]
		if kind == "scan" {
			ls, gs := lw.state(t), gw.state(t)
			if ls.ActiveEdge == 0 || gs.ActiveEdge == 0 {
				continue // 无 active 时 scan 会触发组自愈（新行为），跳过该步
			}
			ops = append(ops, diffOp{kind: "scan"})
			continue
		}
		ops = append(ops, diffOp{kind: kind, edge: edge})
	}
	return ops
}

// TestVerifyDiff_LegacyVsGroupEquivalence 是等价性差分主用例。
func TestVerifyDiff_LegacyVsGroupEquivalence(t *testing.T) {
	for _, seed := range []int64{1, 7, 23} {
		t.Run(fmt.Sprintf("seed_%d", seed), func(t *testing.T) {
			clock := time.Now().UTC().Add(5 * time.Minute)
			lw, gw := setupDiffWorlds(t, &clock)

			// 初始状态必须一致。
			ls, gs := lw.state(t), gw.state(t)
			if ls.String() != gs.String() {
				t.Fatalf("初始状态不一致:\n legacy=%s\n group =%s", ls, gs)
			}

			ops := genDiffOps(seed, 40, lw, gw, t)
			for i, op := range ops {
				runDiffOp(t, lw, gw, &clock, op)
				ls, gs := lw.state(t), gw.state(t)
				if ls.String() != gs.String() {
					t.Fatalf("第 %d 步(%s edge=%d)后状态不一致:\n legacy=%s\n group =%s",
						i+1, op.kind, op.edge, ls, gs)
				}
			}
			t.Logf("seed=%d 40 步等价（末态 %s）", seed, ls.String())
		})
	}
}
