package datasource

// 来源组（source group）生命周期与状态机适配层（方案《规则数据来源与复合来源》v0.3 §4/§5.4/§6.3）。
//
// 为什么单独一个 GroupService（而不是改 data_sources 的 Service）：
//   - 组键不同（source_group_id vs (device_id,category)），data_sources 行语义不动（P-1）；
//   - R1–R11 语义相同，但**实现目前是两套**（架构终审 H-1 的如实记录）：
//     本文件的 applySuccess/countFailure/pickCandidate/cooldownActive/minResidencyElapsed
//     是组路径的决策实现；legacy data_sources 路径（service.go）**仍保留自己的内联实现，
//     并未改为调用这些 helper**（改 legacy 需动已冻结的 908 行状态机，合并前风险高于收益）。
//     为什么可以接受：等价性由**差分测试**保证（impl-core 3 seeds × 40 步逐步等价，
//     两条状态机末态一致）+ 20+ 组用例 + 33 RUN 独立验证；若未来改任一侧，差分测试会红。
//     若后续要真正共用，需单独立项重写 legacy 并保留差分对照。
//
// 快照所有权（方案 §12.2.2 / N-13）：**本文件是唯一发布者** —— 任何组/成员/active
// 变更在事务提交后调用 publishGroup；切换钩子只做"清窗加速 + 审计"，不在快照真值
// 路径上（钩子丢失不允许导致规则用陈旧 active 过滤）。

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/notify"
	"ehome/backend/internal/sourceref"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"

	"gorm.io/gorm"
)

// ErrGroupNotFound 组不存在（handler 映射 404）。
var ErrGroupNotFound = errors.New("source group not found")

// ActiveChange 是"某来源组的权威成员发生变化"的事件（方案 §5.5 / N-3）。
//
// 语义：Prev 与 Next 恰好有一个可能为 0 —— Next=0 表示组失去权威（degraded / 删除），
// 消费方（evaluator/alert）必须停止用旧 active 过滤、清依赖窗口并落 source_degraded 审计。
type ActiveChange struct {
	GroupID        uint
	Category       string
	PrevMemberID   uint // 0 = 此前无 active
	NextMemberID   uint // 0 = 切换后无 active
	Reason         string // auto|manual|manual_deactivate|reset|delete|degraded|selfheal
	Trigger        string // device_offline|stale_data|""（manual/reset 为空）
	Revision       int64  // 事件对应的 SourceGroup.Revision（发布前已单调 +1）
	TransitionSeq  uint64 // 组内权威变更序号（内存单调；快照窗口 identity 用）
	At             time.Time
}

// GroupChangeHook 是切换钩子签名（main.go 经 SetActiveChangeHook 注入）。
type GroupChangeHook func(ActiveChange)

// activeChangeDispatcher 是有序、非阻塞、失败不阻断的钩子分发器。
//
// 为什么异步：钩子消费方可能做内存重建/清窗；绝不允许它把事务或 API 响应拖慢，
// 更不允许 panic 掀翻状态机。为什么有序：切换事件乱序会让 evaluator 用旧 active
// 覆盖新 active（危险方向），因此单 goroutine 串行消费、事件携带单调 Revision。
type activeChangeDispatcher struct {
	ch    chan ActiveChange
	dirty atomic.Bool

	mu   sync.RWMutex
	hook GroupChangeHook
}

func newActiveChangeDispatcher() *activeChangeDispatcher {
	return &activeChangeDispatcher{ch: make(chan ActiveChange, 64)}
}

func (d *activeChangeDispatcher) setHook(fn GroupChangeHook) {
	d.mu.Lock()
	d.hook = fn
	d.mu.Unlock()
}

// emit 非阻塞投递：满则丢弃并置 dirty（强制消费方全量重载快照）。
func (d *activeChangeDispatcher) emit(ev ActiveChange) {
	d.mu.RLock()
	hasHook := d.hook != nil
	d.mu.RUnlock()
	if !hasHook {
		return
	}
	select {
	case d.ch <- ev:
	default:
		d.dirty.Store(true)
		logger.Warn("datasource: source_group active-change hook queue full; marking dirty for full reload",
			"group_id", ev.GroupID, "next_member_id", ev.NextMemberID)
	}
}

// Dirty 报告是否发生过事件丢弃（消费方下一次求值前必须全量重载）。
func (d *activeChangeDispatcher) Dirty() bool { return d.dirty.Load() }

// clearDirty 在消费方完成全量重载后调用。
func (d *activeChangeDispatcher) clearDirty() { d.dirty.Store(false) }

// run 串行消费；单事件 panic 只记日志，不影响后续事件与状态机。
func (d *activeChangeDispatcher) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-d.ch:
			d.mu.RLock()
			hook := d.hook
			d.mu.RUnlock()
			if hook == nil {
				continue
			}
			func() {
				defer func() {
					if r := recover(); r != nil {
						logger.Warn("datasource: source_group active-change hook panicked",
							"group_id", ev.GroupID, "panic", r)
						d.dirty.Store(true)
					}
				}()
				hook(ev)
			}()
		}
	}
}

// GroupService 是来源组的领域服务（组 CRUD、成员运行态、R1–R11 适配、切换钩子）。
//
// 它**不替代** data_sources 的 Service：两者并存，规则按 source_kind 选择其一。
// 注意（H-1）：两套 R1–R11 实现**当前未共用 helper**，等价性靠差分测试锁定；
// 本文件内的 applySuccess/countFailure/... 只服务组路径，改语义必须同步跑差分。
type GroupService struct {
	db   *gorm.DB
	opts Options

	registry  *sourceref.Registry
	dispatcher *activeChangeDispatcher

	notifySink notify.Notifier
	notify     func(models.Notification)

	seqMu      sync.Mutex
	transition map[uint]uint64 // groupID -> TransitionSeq（单实例内存；重启归零仅重置窗口身份）

	// 求值期降级健康事件节流（(groupID, reason) 1min 一条；见 RecordDegraded）。
	// 为什么放内存：热路径每帧都可能判出降级，写入必须廉价且可丢弃；
	// 节流表丢失（重启）最多多写一条，不影响正确性。
	degradedMu   sync.Mutex
	degradedLast map[degradedKey]time.Time
}

// degradedKey 是降级健康事件节流键（同组同原因 1 分钟内只写一条）。
type degradedKey struct {
	GroupID uint
	Reason  string
}

// degradedHealthThrottle 是求值期降级健康事件的节流周期（与 automation.sourceAuditInterval
// 同思路，但更短：健康看板要能看到"最近 1 分钟还在降级"，而不是 5 分钟前的快照）。
const degradedHealthThrottle = time.Minute

// NewGroupService 构造组服务。opts 与 datasource.New 同源（共享 Cooldown/MinResidency/
// Staleness/ScanInterval/Now 语义），main.go 应传同一份 Options 值。
func NewGroupService(db *gorm.DB, opts Options) *GroupService {
	if opts.Cooldown <= 0 {
		opts.Cooldown = defaultCooldown
	}
	if opts.MinResidency <= 0 {
		opts.MinResidency = defaultMinResidency
	}
	if opts.Staleness <= 0 {
		opts.Staleness = defaultStaleness
	}
	if opts.ScanInterval <= 0 {
		opts.ScanInterval = defaultScanInterval
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &GroupService{
		db:           db,
		opts:         opts,
		dispatcher:   newActiveChangeDispatcher(),
		transition:   map[uint]uint64{},
		degradedLast: map[degradedKey]time.Time{},
	}
}

func (g *GroupService) now() time.Time { return g.opts.Now() }

// SetGroupRegistry 注入 sourceref 注册表（快照唯一发布目标）。
//
// nil 语义（impl-core 探针后修正）：nil 无法证明"没有组"，因此**保守执行状态机**
// （MarkSuccess/MarkFailure 照常查库/更新，只是无法发布快照）。若 nil 也短路，
// 漏注入 registry 的装配会**静默丢 R1/R2 运行态**（last_success 不写、切不切换），
// 比多一次查询危险得多。生产 main.go 恒注入且先 PublishAll，故 0 组快速路径仍然生效
// （registry 非 nil 且 Len()==0 ⇒ 短路，零查询）。
func (g *GroupService) SetGroupRegistry(r *sourceref.Registry) { g.registry = r }

// groupsLive 报告是否存在已发布的组（热路径闸门，O(1)）。
//
// 判据：
//   - registry 已注入且 Len()==0 ⇒ false（确证无组，生产 0 组场景走此分支，零查询）；
//   - registry==nil ⇒ true（**保守执行**：不能把"没接线"误判成"没有组"）；
//   - 其余 ⇒ true（0→N 由 publish 自动翻转，无需失效钩子）。
//
// 为什么读 registry 而不是查 DB：快照是状态机自己发布的真值（唯一发布者），
// 0→N 由 publish 自动翻转；热路径每帧查库会把 0 组场景的收益抵消掉（B-3 第 1 步）。
func (g *GroupService) groupsLive() bool {
	if g == nil {
		return false
	}
	if g.registry == nil {
		return true // 装配缺失：保守跑，宁多查一次也不能静默丢状态
	}
	return g.registry.Len() > 0
}

// Registry 返回注入的注册表（测试/装配断言用）。
func (g *GroupService) Registry() *sourceref.Registry { return g.registry }

// SetActiveChangeHook 注入切换钩子（nil=关闭）。契约见 activeChangeDispatcher：
// 事务后发射、at-least-once + Revision 单调、panic 不阻断、溢出置 dirty。
func (g *GroupService) SetActiveChangeHook(fn GroupChangeHook) { g.dispatcher.setHook(fn) }

// DirtySinceLastReload 暴露给消费方判断是否必须全量重载快照。
func (g *GroupService) DirtySinceLastReload() bool { return g.dispatcher.Dirty() }

// ClearDirty 在消费方完成全量重载后调用。
func (g *GroupService) ClearDirty() { g.dispatcher.clearDirty() }

// SetDispatchNotifier 注入"通知写入 + 投递"入口（与 datasource.Service 同款）。
func (g *GroupService) SetDispatchNotifier(n notify.Notifier) { g.notifySink = n }

// SetNotifier 注入回调式通知（测试收集用；优先级 notifySink > notify > 直接落库）。
func (g *GroupService) SetNotifier(fn func(models.Notification)) { g.notify = fn }

// Start 以与 data_sources 相同的 ScanInterval 周期性执行组健康扫描 + 自愈，
// 直到 ctx 取消。main.go 与 datasourceSvc.Start(ctx) 并行启动（同一 ctx/Options）。
func (g *GroupService) Start(ctx context.Context) {
	// 扫描前先自检（快照冷加载由 main 调 PublishAll；双保险防直连 DB 的脏状态）。
	if err := g.SelfCheck(); err != nil {
		logger.Warn("datasource: source_group self-check failed; scanner disabled", "error", err)
		return
	}
	ticker := time.NewTicker(g.opts.ScanInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := g.ScanGroups(g.now()); err != nil {
				logger.Warn("datasource: source_group scan failed", "error", err)
			}
		}
	}
}

// ── 快照构建与发布（唯一发布者）────────────────────────────────────────────

// buildSnapshot 从 DB 读取组 + 成员，构建不可变快照（SortMembers + MembersHash）。
func (g *GroupService) buildSnapshot(groupID uint) (*sourceref.GroupSnapshot, error) {
	var row models.SourceGroup
	if err := g.db.First(&row, groupID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrGroupNotFound
		}
		return nil, err
	}
	var members []models.SourceGroupMember
	if err := g.db.Where("group_id = ?", groupID).Find(&members).Error; err != nil {
		return nil, err
	}
	return g.snapshotOf(row, members), nil
}

// snapshotOf 是纯函数（DB 行 → 不可变快照），测试可直接构造。
func (g *GroupService) snapshotOf(row models.SourceGroup, members []models.SourceGroupMember) *sourceref.GroupSnapshot {
	snap := &sourceref.GroupSnapshot{
		ID:           row.ID,
		Kind:         row.Kind,
		Category:     row.Category,
		Combine:      row.Combine,
		MinMembers:   row.MinMembers,
		StalenessSec: row.StalenessSec,
		OnDegrade:    row.OnDegrade,
		Enabled:      row.Enabled,
		Revision:     row.Revision,
		TransitionSeq: g.transitionSeqOf(row.ID),
	}
	for i := range members {
		m := members[i]
		ms := sourceref.MemberSnapshot{
			MemberID:     m.ID,
			EdgeDeviceID: m.EdgeDeviceID,
			Priority:     m.Priority,
			Required:     m.Required,
			Status:       m.Status,
			FailCount:    m.FailCount,
		}
		if m.LastSuccess != nil {
			ms.LastSuccess = *m.LastSuccess
		}
		if m.Status == models.SourceMemberStatusActive {
			snap.ActiveMemberID = m.ID
		}
		snap.Members = append(snap.Members, ms)
	}
	sourceref.SortMembers(snap.Members)
	snap.MembersHash = sourceref.ComputeMembersHash(snap.Members)
	return snap
}

// publishGroup 构建并发布单组快照（事务提交后调用；失败只 WARN，不影响已提交状态）。
func (g *GroupService) publishGroup(groupID uint) {
	if g.registry == nil {
		return
	}
	snap, err := g.buildSnapshot(groupID)
	if err != nil {
		if errors.Is(err, ErrGroupNotFound) {
			g.registry.Remove(groupID)
			return
		}
		logger.Warn("datasource: source_group snapshot build failed", "group_id", groupID, "error", err)
		return
	}
	g.registry.Publish(snap)
}

// PublishAll 全量重建并替换注册表（启动冷加载 / 运维重载）。
//
// 启动自检（方案 N-1）内置在冷加载入口：库内若存在 composite active 或单组多 active，
// 直接返回 error 让 main.go 中止启动（fail-closed），绝不带着自相矛盾状态发布快照。
func (g *GroupService) PublishAll() error {
	if err := g.SelfCheck(); err != nil {
		return err
	}
	if g.registry == nil {
		return nil
	}
	var groups []models.SourceGroup
	if err := g.db.Find(&groups).Error; err != nil {
		return err
	}
	snaps := make([]*sourceref.GroupSnapshot, 0, len(groups))
	for i := range groups {
		var members []models.SourceGroupMember
		if err := g.db.Where("group_id = ?", groups[i].ID).Find(&members).Error; err != nil {
			return err
		}
		snaps = append(snaps, g.snapshotOf(groups[i], members))
	}
	g.registry.ReplaceAll(snaps)
	return nil
}

// ── 事件发射（事务提交后唯一入口）──────────────────────────────────────────

// nextTransitionSeq 递增并返回组内权威变更序号（内存单调）。
func (g *GroupService) nextTransitionSeq(groupID uint) uint64 {
	g.seqMu.Lock()
	defer g.seqMu.Unlock()
	g.transition[groupID]++
	return g.transition[groupID]
}

func (g *GroupService) transitionSeqOf(groupID uint) uint64 {
	g.seqMu.Lock()
	defer g.seqMu.Unlock()
	return g.transition[groupID]
}

// commitGroupChange 是**所有组状态变更的统一收尾**（必须在事务返回 nil 后调用）：
//  1. 发布最新快照（真值路径；失败只 WARN）；
//  2. 若权威成员发生变化，发射 ActiveChange（at-least-once；失败不阻断）。
//
// ev 的 Revision/TransitionSeq 由调用方在事务内/后填好；本函数不再修改状态。
func (g *GroupService) commitGroupChange(groupID uint, ev *ActiveChange) {
	g.publishGroup(groupID)
	if ev == nil {
		return
	}
	ev.GroupID = groupID
	if ev.At.IsZero() {
		ev.At = g.now()
	}
	if ev.TransitionSeq == 0 {
		ev.TransitionSeq = g.transitionSeqOf(groupID)
	}
	g.dispatcher.emit(*ev)
}

// ── 纯决策辅助（组路径实现；legacy data_sources 仍用 service.go 内联逻辑，见包注释 H-1）──

// applySuccess 是 R1 的纯逻辑：给定运行态与成功时刻，返回应写入的列。
// 不抢占 active；error → standby。**只服务组路径**（legacy 有自己的内联版本，见 H-1）。
func applySuccess(currentStatus string, at time.Time) map[string]interface{} {
	updates := map[string]interface{}{
		"last_success": at,
		"fail_count":   0,
	}
	if currentStatus == StatusError {
		updates["status"] = StatusStandby
	}
	return updates
}

// countFailure 是 R2 的纯逻辑：累加失败并按阈值判定是否熔断。
// 返回 (新计数, 新状态, 是否达到阈值)。
func countFailure(currentCount, maxFail int, now time.Time) (int, string, bool) {
	next := currentCount + 1
	if next >= maxFail {
		return next, StatusError, true
	}
	return next, "", false
}

// pickCandidate 是 R3/R5 的候选排序：priority DESC, id ASC 的第一条 standby。
// 输入须为已过滤的候选（status=standby）；返回 (被选中, 是否存在)。
func pickCandidate(members []models.SourceGroupMember) (models.SourceGroupMember, bool) {
	var best *models.SourceGroupMember
	for i := range members {
		m := &members[i]
		if m.Status != StatusStandby {
			continue
		}
		if best == nil || m.Priority > best.Priority || (m.Priority == best.Priority && m.ID < best.ID) {
			best = m
		}
	}
	if best == nil {
		return models.SourceGroupMember{}, false
	}
	return *best, true
}

// minResidencyElapsed 是 R11：成员变 active 后未满最小驻留期则不累计停滞。
func (g *GroupService) minResidencyElapsed(updatedAt time.Time, at time.Time) bool {
	return at.Sub(updatedAt) >= g.opts.MinResidency
}

// cooldownActive 是 R10：距该组上次自动切换是否仍在冷却窗内。返回 (是否抑制, 上次切换时间)。
func (g *GroupService) cooldownActive(tx *gorm.DB, groupID uint) (bool, time.Time, error) {
	var last models.SourceGroupFailoverLog
	err := tx.Where("group_id = ?", groupID).Order("id DESC").First(&last).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, time.Time{}, nil
	}
	if err != nil {
		return false, time.Time{}, err
	}
	return g.now().Sub(last.CreatedAt) < g.opts.Cooldown, last.CreatedAt, nil
}

// ── 派生参数（F-3 / §4.1）─────────────────────────────────────────────────

// memberIntervalMillis 读取成员设备的采集间隔（EdgeDevice.IntervalMs；缺省/未配置=0）。
func (g *GroupService) memberIntervalMillis(groupID uint) ([]int, error) {
	var intervals []int
	err := g.db.Table("source_group_members AS sgm").
		Select("COALESCE(ed.interval_ms, 0)").
		Joins("LEFT JOIN edge_devices ed ON ed.id = sgm.edge_device_id").
		Where("sgm.group_id = ?", groupID).
		Scan(&intervals).Error
	if err != nil {
		return nil, err
	}
	return intervals, nil
}

// DeriveStaleness（F-3/§4.1）：StalenessSec>0 用它；否则 max(2×最大采集间隔, 30s)，上限 3600s。
// 导出以便 handler_automation 保存期回填与测试复用（纯函数）。
func DeriveStaleness(configuredSec int, intervalsMillis []int) time.Duration {
	if configuredSec > 0 {
		return time.Duration(configuredSec) * time.Second
	}
	maxMs := 0
	for _, ms := range intervalsMillis {
		if ms > maxMs {
			maxMs = ms
		}
	}
	staleness := time.Duration(maxMs) * time.Millisecond * 2
	if staleness < 30*time.Second {
		staleness = 30 * time.Second
	}
	if staleness > time.Hour {
		staleness = time.Hour
	}
	return staleness
}

// CooldownFloor（F-3）：cooldown 推导下限 = max(成员采集间隔)；全为 0 ⇒ 60s。
// 导出：规则保存期校验（handler_automation）用它拒绝过小 cooldown。
func CooldownFloor(intervalsMillis []int) time.Duration {
	maxMs := 0
	for _, ms := range intervalsMillis {
		if ms > maxMs {
			maxMs = ms
		}
	}
	if maxMs <= 0 {
		return 60 * time.Second
	}
	return time.Duration(maxMs) * time.Millisecond
}

// AnchorMember（N-7 裁决 7）：组锚点成员 = priority DESC, memberID ASC 的第一个成员
// （**静态**，不看运行态；规则保存期把它写入 trigger_edge_device_id/target_id）。
func AnchorMember(members []models.SourceGroupMember) (models.SourceGroupMember, bool) {
	var best *models.SourceGroupMember
	for i := range members {
		m := &members[i]
		if best == nil || m.Priority > best.Priority || (m.Priority == best.Priority && m.ID < best.ID) {
			best = m
		}
	}
	if best == nil {
		return models.SourceGroupMember{}, false
	}
	return *best, true
}

// ResolveGroupStaleness 读取成员间隔并派生组的停滞阈值（handler/求值侧共用）。
func (g *GroupService) ResolveGroupStaleness(groupID uint) (time.Duration, error) {
	var row models.SourceGroup
	if err := g.db.First(&row, groupID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return 0, ErrGroupNotFound
		}
		return 0, err
	}
	intervals, err := g.memberIntervalMillis(groupID)
	if err != nil {
		return 0, err
	}
	return DeriveStaleness(row.StalenessSec, intervals), nil
}

// ResolveCooldownFloor 读取组并返回规则 cooldown 推导下限。
func (g *GroupService) ResolveCooldownFloor(groupID uint) (time.Duration, error) {
	intervals, err := g.memberIntervalMillis(groupID)
	if err != nil {
		return 0, err
	}
	return CooldownFloor(intervals), nil
}

// ── 事务辅助 ──────────────────────────────────────────────────────────────

// bumpGroupRevision 是 revision bump 的**唯一入口**（方案 F-1 白名单）。
//
// 只有拓扑/成员/priority/required/combine/min_members/staleness/on_degrade/enabled/
// active 迁移可以调用；MarkSuccess/MarkFailure 计数不得调用（否则窗口 identity 每帧
// 变化、DurationSec 永不满足——复评最高危回归）。
func (g *GroupService) bumpGroupRevision(tx *gorm.DB, groupID uint) (int64, error) {
	if err := tx.Model(&models.SourceGroup{}).Where("id = ?", groupID).
		UpdateColumn("revision", gorm.Expr("revision + 1")).Error; err != nil {
		return 0, err
	}
	var rev int64
	if err := tx.Model(&models.SourceGroup{}).Where("id = ?", groupID).Pluck("revision", &rev).Error; err != nil {
		return 0, err
	}
	return rev, nil
}

// writeGroupHealth 写一条组健康事件（source_group_health）。
func (g *GroupService) writeGroupHealth(tx *gorm.DB, groupID, memberID uint, event, message string) error {
	return tx.Create(&models.SourceGroupHealth{
		GroupID:   groupID,
		MemberID:  memberID,
		Event:     event,
		Message:   message,
		CreatedAt: g.now(),
	}).Error
}

// writeGroupFailoverLog 写一条组级切换日志（source_group_failover_logs）。
func (g *GroupService) writeGroupFailoverLog(tx *gorm.DB, groupID uint, from, to uint, reason, trigger string) error {
	return tx.Create(&models.SourceGroupFailoverLog{
		GroupID:      groupID,
		FromMemberID: from,
		ToMemberID:   to,
		Reason:       reason,
		Trigger:      trigger,
		CreatedAt:    g.now(),
	}).Error
}


// RecordDegraded 是**求值期降级**的健康事件写入入口（方案 §7.1 E-1/E-2：成员陈旧/缺失
// 必须在组健康表可见，运维看板 GET /source-groups/:id/health 依赖它）。
//
// 为什么由求值侧回调：降级判定发生在 sourceref.CombineGroup（automation/alert 求值路径），
// datasource 不感知求值；因此 main.go 把本方法注入给 automation/alert 的
// SetSourceDegradedFn（签名 func(groupID uint, reason string)），避免 automation 直接
// import datasource（三层职责红线）。
//
// 节流：同一 (groupID, reason) 1 分钟内只写一条（热路径每帧都可能判降级，不能每帧写库；
// 丢失窗口最多让看板少一条，不影响 fail-closed 正确性）。
func (g *GroupService) RecordDegraded(groupID uint, reason string) {
	g.recordDegraded(groupID, reason, nil)
}

// RecordDegradedWithMembers 是 RecordDegraded 的扩展：把缺席成员 id 拼进消息，
// 便于运维直接看到"哪几台没数据"。签名不供 main 默认接线，automation/alert 可按需调用。
func (g *GroupService) RecordDegradedWithMembers(groupID uint, reason string, memberIDs []uint) {
	g.recordDegraded(groupID, reason, memberIDs)
}

func (g *GroupService) recordDegraded(groupID uint, reason string, memberIDs []uint) {
	if g == nil || groupID == 0 || strings.TrimSpace(reason) == "" {
		return
	}
	now := g.now()
	key := degradedKey{GroupID: groupID, Reason: reason}
	g.degradedMu.Lock()
	if last, ok := g.degradedLast[key]; ok && now.Sub(last) < degradedHealthThrottle {
		g.degradedMu.Unlock()
		return
	}
	// 顺手清理陈旧节流项，防止长期运行内存膨胀（组/原因数量有限，但仍做上限保护）。
	if len(g.degradedLast) > 512 {
		for k, last := range g.degradedLast {
			if now.Sub(last) > 10*degradedHealthThrottle {
				delete(g.degradedLast, k)
			}
		}
	}
	g.degradedLast[key] = now
	g.degradedMu.Unlock()

	// 组已被删除时不写孤儿健康行（回调可能晚于删除一个求值周期）。
	var exists int64
	if err := g.db.Model(&models.SourceGroup{}).Where("id = ?", groupID).Count(&exists).Error; err != nil {
		logger.Warn("datasource: RecordDegraded group lookup failed", "group_id", groupID, "error", err)
		return
	}
	if exists == 0 {
		return
	}
	msg := "求值期降级: " + reason
	if len(memberIDs) > 0 {
		parts := make([]string, 0, len(memberIDs))
		for _, id := range memberIDs {
			parts = append(parts, fmt.Sprintf("%d", id))
		}
		msg += " (members=" + strings.Join(parts, ",") + ")"
	}
	row := models.SourceGroupHealth{
		GroupID:   groupID,
		Event:     "degraded",
		Message:   truncateHealthMessage(msg),
		CreatedAt: now,
	}
	if err := g.db.Create(&row).Error; err != nil {
		logger.Warn("datasource: RecordDegraded write failed", "group_id", groupID, "error", err)
	}
}

// truncateHealthMessage 把消息裁到 source_group_health.message 的 255 字节上限，
// 且不切断多字节字符（按 rune 边界回退）。
func truncateHealthMessage(msg string) string {
	const maxBytes = 255
	if len(msg) <= maxBytes {
		return msg
	}
	cut := maxBytes
	for cut > 0 && (msg[cut]&0xC0) == 0x80 { // UTF-8 continuation byte
		cut--
	}
	return msg[:cut]
}

// newGroupNotification 构造组域通知（复用既有 notify 汇聚点；Source=data_source）。
func (g *GroupService) newGroupNotification(typ, title, message string, groupID uint) models.Notification {
	return models.Notification{
		Type:        typ,
		Title:       title,
		Message:     message,
		Description: message,
		Source:      notifySource,
		SourceID:    fmt.Sprintf("group:%d", groupID),
		Read:        false,
		CreatedAt:   g.now(),
	}
}

// emitAll 事务提交后投递通知（fail-open；与 datasource.Service.emitAll 同纪律）。
func (g *GroupService) emitAll(notes []models.Notification) {
	for i := range notes {
		g.emit(notes[i])
	}
}

func (g *GroupService) emit(n models.Notification) {
	if g.notifySink != nil {
		g.notifySink.Create(context.Background(), &n)
		return
	}
	if g.notify != nil {
		g.notify(n)
		return
	}
	if err := g.db.Create(&n).Error; err != nil {
		logger.Warn("datasource: failed to write source_group notification", "source_id", n.SourceID, "error", err)
	}
}

// RunDispatcher 启动切换钩子的有序消费 goroutine（main.go 与 Start 一起接线）。
func (g *GroupService) RunDispatcher(ctx context.Context) { g.dispatcher.run(ctx) }

// SelfCheck 是启动一致性自检（方案 N-1）：main.go 必须在加载快照前调用，返回 error
// 时**拒绝启动**（fail-closed，打印违规组 id），不得带着自相矛盾的组状态提供服务。
//
// 检查项：
//  1. composite 组不得存在 status=active 的成员（I5；API 已禁，DB 直写/恢复旧库可能违规）；
//  2. 任意组不得存在 >1 条 active（部分唯一索引的兜底断言，索引被绕过时仍能发现）。
func (g *GroupService) SelfCheck() error {
	type row struct {
		GroupID uint
		Kind    string
		Count   int64
	}
	var rows []row
	if err := g.db.Table("source_group_members AS sgm").
		Select("sgm.group_id AS group_id, sg.kind AS kind, COUNT(*) AS count").
		Joins("JOIN source_groups sg ON sg.id = sgm.group_id").
		Where("sgm.status = ?", models.SourceMemberStatusActive).
		Group("sgm.group_id, sg.kind").
		Scan(&rows).Error; err != nil {
		return fmt.Errorf("source_group self-check query failed: %w", err)
	}
	var bad []string
	for _, r := range rows {
		if r.Kind != models.SourceGroupKindPrimaryBackup {
			bad = append(bad, fmt.Sprintf("composite group %d has %d active member(s)", r.GroupID, r.Count))
			continue
		}
		if r.Count > 1 {
			bad = append(bad, fmt.Sprintf("primary_backup group %d has %d active members (I1 violated)", r.GroupID, r.Count))
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("source_group self-check failed: %s", strings.Join(bad, "; "))
	}
	return nil
}

// RunDirtyReloader 是切换钩子通道溢出后的**安全阀消费者**（架构终审 H-5）：
// 分发器丢弃事件时会置 dirty（见 activeChangeDispatcher.emit），若不重载，快照会
// 永久陈旧 ⇒ "切换后跟随"失效（正确性仍由 S3 窗口 identity 兜底，但跟随会停）。
//
// 每 interval 检查一次 dirty；脏则 PublishAll（含 SelfCheck）全量重建快照，成功后清 dirty。
// 为什么不用事件驱动：溢出本身就是"队列满了"，再入队只会继续丢；固定小周期重载最稳。
// main.go 接线：go sourceGroupSvc.RunDirtyReloader(sourceGroupCtx, 5*time.Second)。
func (g *GroupService) RunDirtyReloader(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if !g.DirtySinceLastReload() {
				continue
			}
			if err := g.PublishAll(); err != nil {
				// 自检/DB 失败：保持 dirty，下一轮重试（绝不静默当作已重载）。
				logger.Warn("datasource: source_group dirty reload failed", "error", err)
				continue
			}
			g.ClearDirty()
			// 指标：既能发现"重载频繁"（写热/节流失效），也能发现"重载完全没跑"
			// （main 漏接线）——后者是 dirty 永久的典型形态。
			metrics.SourceGroupDirtyReloadTotal.Inc()
			// Warn 而非 Info：钩子通道溢出不重载会让"切换后跟随"永久失效，属需要
			// 运维知晓的降级事件（架构终审 H-5 要求可见）。
			logger.Warn("datasource: source_group hook queue overflowed; registry fully reloaded (dirty cleared)")
		}
	}
}

