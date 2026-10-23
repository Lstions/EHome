package main

import (
	"context"
	"time"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"
	"ehome/backend/internal/source"
	"ehome/backend/internal/sourceref"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// source_projection.go 是 L2 来源层在进程内的接线与驱动（数据流架构 v1 §四 M2）。
//
// # 职责边界
//
// 本文件只做**装配与驱动**：领域逻辑在 internal/source（纯函数）
// 与 internal/datasource（收敛器）。三条红线在此落地：
//
//  1. 派生是**纯函数**（source.Derive 零 DB）；查库集中在 DeriveInputFrom；
//  2. 收敛在**冷路径**（启动一次 + 定期对账），绝不在采集热路径写库；
//  3. 失败**不阻塞启动**（投影是台账，坏了不影响采集/规则/告警）。
//
// # 为什么失败不 fail-closed
//
// 对照 source_group 的启动自检（那条是 fail-closed，因为配置自相矛盾会让
// 「用哪台供数」不确定）。本层不同：data_sources 是**只读台账**，它出问题
// 最多让页面显示不全，不影响任何数据流。为它拒绝启动是过度反应。

// projectionRuntime 是来源层的启动期装配结果。
type projectionRuntime struct {
	registry *source.Registry
	svc      *datasource.Service
	groupSvc *datasource.GroupService

	initial dataprojectionStats
}

// dataprojectionStats 是一次对账的统计（打日志用）。
type dataprojectionStats struct {
	Sources       int
	Created       int
	Updated       int
	Disabled      int
	SkippedManual int
}

// setupSourceProjection 构造来源注册表、做首次全量对账，并返回运行期句柄。
//
// 调用顺序硬约束：必须在 sourceGroupSvc.PublishAll() **之后**
// —— 需要读到已发布的组快照作为派生输入。
func setupSourceProjection(ctx context.Context, db *gorm.DB,
	svc *datasource.Service, groupSvc *datasource.GroupService,
	groupReg *sourceref.Registry) *projectionRuntime {

	rt := &projectionRuntime{
		registry: source.NewRegistry(),
		svc:      svc,
		groupSvc: groupSvc,
	}

	// 首次全量对账：让页面在启动后立刻有数据（不必等第一个 ticker 周期）。
	stats, err := rt.reconcileOnce(ctx, db, groupReg)
	if err != nil {
		// 降级不阻塞：投影是台账，坏了不影响采集/规则/告警。
		logger.Warn("source_projection: 启动对账失败（降级运行，不影响数据流）", "error", err)
	} else {
		rt.initial = stats
		logger.Infof("source_projection: 启动对账完成 sources=%d created=%d updated=%d disabled=%d skipped_manual=%d",
			stats.Sources, stats.Created, stats.Updated, stats.Disabled, stats.SkippedManual)
	}
	return rt
}

// reconcileOnce 做一次「查库 → 派生 → 收敛」循环。
//
// 这是**唯一**的写路径，只在冷路径被调用（启动一次 + ticker）。
func (rt *projectionRuntime) reconcileOnce(ctx context.Context, db *gorm.DB,
	groupReg *sourceref.Registry) (dataprojectionStats, error) {

	var stats dataprojectionStats
	if rt == nil || rt.svc == nil || rt.registry == nil {
		return stats, nil
	}

	in, err := rt.svc.DeriveInputFrom(ctx)
	if err != nil {
		return stats, err
	}
	// 来源组快照：从 DB 读 enabled 组并转快照，作为派生的显式配置来源。
	//
	// 为什么不直接用 groupReg（运行时注册表）：它持有的是**已发布快照**，
	// 而这里需要的是「DB 里真实存在且 enabled 的组」—— 两者在组被停用但
	// 未重新发布时可能不同。以 DB 为准更符合「收敛到事实」的语义。
	groups, err := loadEnabledGroupSnapshots(db)
	if err != nil {
		return stats, err
	}
	in.Groups = groups

	handles := source.Derive(in)
	rt.registry.Publish(handles)

	res, err := rt.svc.ReconcileDerived(ctx, handles)
	if err != nil {
		return stats, err
	}
	stats = dataprojectionStats{
		Sources:       len(handles),
		Created:       res.Created,
		Updated:       res.Updated,
		Disabled:      res.Disabled,
		SkippedManual: res.SkippedManual,
	}
	return stats, nil
}

// RunProjection 周期性对账（冷路径）。
//
// 周期默认 60s：复用字段解析层 flush 的节奏。不设得更短是因为派生要扫
// sensor_field_obs（设备数 × 字段数行），生产 10 设备约几百行，60s 足够。
func (rt *projectionRuntime) RunProjection(ctx context.Context, db *gorm.DB,
	groupReg *sourceref.Registry, interval time.Duration) {

	if interval <= 0 {
		interval = 60 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			stats, err := rt.reconcileOnce(ctx, db, groupReg)
			if err != nil {
				// 单次失败只 Warn，下个周期重试（幂等，安全）。
				logger.Warn("source_projection: 周期对账失败", "error", err)
				continue
			}
			// 只在有变化时打日志，避免每 60s 一条噪音。
			if stats.Created > 0 || stats.Updated > 0 || stats.Disabled > 0 {
				logger.Infof("source_projection: 对账变化 sources=%d created=%d updated=%d disabled=%d skipped_manual=%d",
					stats.Sources, stats.Created, stats.Updated, stats.Disabled, stats.SkippedManual)
			}
		}
	}
}

// loadEnabledGroupSnapshots 从 DB 读 enabled 来源组并转成不可变快照。
//
// 为什么这里自己转而不复用 groupSvc 的发布逻辑：PublishAll 会把快照推给
// sourceref 注册表（规则求值用），而这里只需要一份**只读输入**给派生器。
// 复用会让两件事耦合（派生失败会污染规则快照）。
func loadEnabledGroupSnapshots(db *gorm.DB) ([]*sourceref.GroupSnapshot, error) {
	var groups []models.SourceGroup
	if err := db.Where("enabled = ?", true).Find(&groups).Error; err != nil {
		return nil, err
	}
	out := make([]*sourceref.GroupSnapshot, 0, len(groups))
	for i := range groups {
		g := &groups[i]
		var members []models.SourceGroupMember
		if err := db.Where("group_id = ?", g.ID).
			Order("priority DESC, id ASC").Find(&members).Error; err != nil {
			return nil, err
		}
		snap := &sourceref.GroupSnapshot{
			ID:           g.ID,
			Kind:         g.Kind,
			Category:     g.Category,
			Combine:      g.Combine,
			MinMembers:   g.MinMembers,
			StalenessSec: g.StalenessSec,
			OnDegrade:    g.OnDegrade,
			Enabled:      g.Enabled,
			Revision:     g.Revision,
			// TransitionSeq 不在组行上，而是由 GroupService 的运行态维护
			// （见 group_service.go transitionSeqOf）。这里派生只需要 active 判定，
			// 不依赖它 ⇒ 留 0。若将来投影需要窗口身份，应从 groupSvc 取而不是猜。
		}
		for _, m := range members {
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
			// ⚠ ActiveMemberID 是**成员行 ID**（m.ID），不是 EdgeDeviceID。
			// 这与 group_service.go:311 的赋值完全一致。
			if m.Status == models.SourceMemberStatusActive {
				snap.ActiveMemberID = m.ID
			}
			snap.Members = append(snap.Members, ms)
		}
		out = append(out, snap)
	}
	return out, nil
}
