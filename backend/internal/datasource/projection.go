package datasource

import (
	"context"
	"fmt"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/source"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// projection.go 是 data_sources 的**自动收敛器**（数据流架构 v1 §四 M2）。
//
// # 它解决什么
//
// 改造前 data_sources 是纯人工配置表，生产实测 0 行 —— 用户看到「有 10 台设备
// 在传数据，数据源页面却是空的」，这是必然的困惑。而它**没有任何下游消费者**
// （grep 证明：automation/alert 零引用），所以空表既不影响采集也不影响规则，
// 只是让人误以为功能坏了。
//
// 结论：把它变成**自动派生的台账**（事实投影），让页面永远反映系统的真实状态。
//
// # 三条纪律
//
//  1. **只碰 auto 行**：manual 行永不覆盖 —— 自动管理的边界是「不覆盖人的决定」
//  2. **不删除，只 disable**：设备下线/字段消失时置 disabled 保留血缘
//  3. **幂等**：可重复调用任意次，结果一致
//
// # 组键（2026-10-23 逻辑设备层移除后）
//
//	data_sources 的唯一键是 (edge_device_id, category) —— 「某台边缘设备的某个类别
//	就是一条来源」。逻辑身份层消失后没有第二套 id 空间，投影的两元组也少一维：
//	不再出现「同一物理来源同时挂在逻辑 id 与边缘 id 上」的双写。
//
// # 性能
//
// 收敛在**冷路径**（启动后一次 + 周期性 ticker），不在采集热路径。
// 生产 unified_data 每 10 分钟约 1.9 万行，任何在 sink 回调里写库的设计都会反压 ingest。

// ReconcileResult 是一次收敛的结果（用于日志与指标）。
type ReconcileResult struct {
	// Created 新建的 auto 行数。
	Created int
	// Updated 更新的 auto 行数（状态/派生时间变化）。
	Updated int
	// Disabled 因不再期望而被置为 disabled 的行数。
	Disabled int
	// SkippedManual 因为对应 manual 行而被跳过的数量（前端提示用）。
	SkippedManual int
	// Total 期望态总数（用于诊断「派生是否工作」）。
	Total int
}

// ReconcileDerived 把来源层的期望态收敛到 data_sources 表。
//
// 幂等：可重复调用任意次。只处理 managed_by='auto' 的行。
func (s *Service) ReconcileDerived(ctx context.Context, handles []*source.Handle) (ReconcileResult, error) {
	var res ReconcileResult
	if s == nil || s.db == nil {
		return res, fmt.Errorf("datasource: nil service or db")
	}
	now := s.now()

	// 1) 载入现有行（一次性，避免 N+1 查询）。
	//    只载 auto 与 manual 两类，不需要全部字段。
	type existingRow struct {
		ID           uint
		EdgeDeviceID uint
		Category     string
		Status       string
		ManagedBy    string
	}
	var rows []existingRow
	if err := s.db.WithContext(ctx).Model(&models.DataSource{}).
		Select("id, edge_device_id, category, status, managed_by").
		Find(&rows).Error; err != nil {
		return res, fmt.Errorf("datasource: load existing sources: %w", err)
	}

	// 组键 = (edge_device_id, category)。
	type key struct {
		edgeDeviceID uint
		category     string
	}
	autoByKey := make(map[key]*existingRow, len(rows))
	manualKeys := make(map[key]struct{}, len(rows))
	for i := range rows {
		k := key{rows[i].EdgeDeviceID, rows[i].Category}
		if rows[i].ManagedBy == models.DataSourceManagedManual {
			manualKeys[k] = struct{}{}
			continue
		}
		autoByKey[k] = &rows[i]
	}

	// 2) 展开期望态为二元组（每个成员设备一行）。
	desired := make(map[key]struct{}, 0)
	desiredStatus := make(map[key]string, 0)
	for _, h := range handles {
		if h == nil {
			continue
		}
		cat := h.Key().Category
		if cat == "" {
			continue
		}
		for _, m := range h.Members() {
			if m.EdgeDeviceID == 0 {
				continue
			}
			k := key{m.EdgeDeviceID, cat}
			desired[k] = struct{}{}
			// 主备：active 成员 active，其余 standby；直连/复合：active
			// （复合组没有单一权威，但每个成员都在供数，标 active 更符合事实）。
			status := StatusActive
			if h.Kind() == source.KindPrimaryBackup && !m.Active {
				status = StatusStandby
			}
			desiredStatus[k] = status
		}
	}
	res.Total = len(desired)

	// 2b) 收集 last_success（事实观测，不是状态机）。
	//
	// # 为什么投影器必须写它（M3 删除 legacy 状态机后暴露的缺口）
	//
	// 删除 datasource.Service.MarkSuccess 后，data_sources.last_success **无人写入**
	// —— 但前端来源总览读的正是这一列（handler_source_overview.go:152），
	// 不修就是"页面上最后成功时间永远为空"的功能缺口。
	//
	// 事实来源：source_group_members.last_success（由 GroupService.MarkSuccess 维护）。
	// 直连来源（无组）没有成员行，此时留空 —— 这是诚实的：没有组就没有"成员健康"概念，
	// 而设备本身的在线状态在 edge_devices.status 里。
	lastSuccessByKey := make(map[key]*time.Time, len(desired))
	if len(desired) > 0 {
		type memRow struct {
			EdgeDeviceID uint
			Category     string
			LastSuccess  *time.Time
		}
		var mems []memRow
		// 容错：未迁移的库/精简测试库可能没有组成员表。此时没有成员健康事实
		// 可回填 ⇒ last_success 留空，而不是让整次对账失败。
		// 投影是台账，个别列取不到不应阻塞其余字段的收敛。
		if s.db.Migrator().HasTable("source_group_members") {
			if err := s.db.WithContext(ctx).Table("source_group_members AS sgm").
				Select("sgm.edge_device_id AS edge_device_id, sg.category AS category, "+
					"sgm.last_success AS last_success").
				Joins("JOIN source_groups sg ON sg.id = sgm.group_id").
				Where("sg.enabled = ?", true).Scan(&mems).Error; err != nil {
				logger.Warn("datasource: load member last_success failed (will stay empty)",
					"error", err)
			}
		}
		// 组件的 (edge_device_id, category) 直接就是组键 —— 不再需要「逻辑设备映射」
		// 那一层反推（它是逻辑身份层的副产物：组键曾含逻辑 id，而成员表只有边缘 id）。
		for _, m := range mems {
			if m.LastSuccess == nil {
				continue
			}
			k := key{m.EdgeDeviceID, m.Category}
			if _, want := desired[k]; !want {
				continue
			}
			ts := *m.LastSuccess
			if cur, ok := lastSuccessByKey[k]; !ok || ts.After(*cur) {
				lastSuccessByKey[k] = &ts
			}
		}
	}

	// 3) 收敛（单事务：部分成功会让台账处于自相矛盾状态）。
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for k := range desired {
			status := desiredStatus[k]
			existing, hasAuto := autoByKey[k]
			if _, isManual := manualKeys[k]; isManual {
				// 人工配置优先：跳过，不覆盖。
				res.SkippedManual++
				continue
			}
			if !hasAuto {
				row := models.DataSource{
					EdgeDeviceID: k.edgeDeviceID,
					Category:     k.category,
					SourceType:   SourceTypeEdgeDevice,
					Name:         derivedName(k.edgeDeviceID, k.category),
					Priority:     0,
					IsPrimary:    status == StatusActive,
					MaxFailCount: defaultMaxFailCount,
					Status:       status,
					ManagedBy:    models.DataSourceManagedAuto,
					DerivedAt:    &now,
				}
				// 新建行也要带 last_success —— 否则首次派生出来的行永远显示"从未成功"，
				// 直到状态发生变化才补上（而这可能永远不发生）。三个写入路径
				// （新建 / 状态变化 / 状态未变）必须一致。
				if ts := lastSuccessByKey[k]; ts != nil {
					row.LastSuccess = ts
				}
				if err := tx.Create(&row).Error; err != nil {
					return fmt.Errorf("datasource: create derived source (%d,%s): %w",
						k.edgeDeviceID, k.category, err)
				}
				res.Created++
				continue
			}
			// 已存在 auto 行：只在状态或派生时间需要更新时写库（减少无谓更新）。
			if existing.Status == status {
				// 状态没变（最常见的路径）：刷新派生时间 + last_success。
				//
				// ⚠ 这里**必须**也写 last_success。若只在状态变化时写，那么
				// "设备一直正常"的来源永远不更新最后成功时间 —— 页面上会显示
				// 一个越来越旧的时间，看起来像设备掉线了。
				touch := map[string]interface{}{"derived_at": now}
				if ts := lastSuccessByKey[k]; ts != nil {
					touch["last_success"] = *ts
				}
				if err := tx.Model(&models.DataSource{}).Where("id = ?", existing.ID).
					Updates(touch).Error; err != nil {
					return fmt.Errorf("datasource: touch derived_at %d: %w", existing.ID, err)
				}
				continue
			}
			updates := map[string]interface{}{
				"status":     status,
				"is_primary": status == StatusActive,
				"derived_at": now,
			}
			// last_success 只在有新值时写（不清空既有事实）。
			if ts := lastSuccessByKey[k]; ts != nil {
				updates["last_success"] = *ts
			}
			if err := tx.Model(&models.DataSource{}).Where("id = ?", existing.ID).
				Updates(updates).Error; err != nil {
				return fmt.Errorf("datasource: update derived source %d: %w", existing.ID, err)
			}
			res.Updated++
		}

		// 4) 期望态里没有的 auto 行 ⇒ disable（**不删除**，保留血缘）。
		for k, row := range autoByKey {
			if _, want := desired[k]; want {
				continue
			}
			if row.Status == StatusDisabled {
				continue // 已停用，幂等
			}
			if err := tx.Model(&models.DataSource{}).Where("id = ?", row.ID).
				Update("status", StatusDisabled).Error; err != nil {
				return fmt.Errorf("datasource: disable stale source %d: %w", row.ID, err)
			}
			res.Disabled++
		}
		return nil
	})
	if err != nil {
		return res, err
	}
	return res, nil
}

// derivedName 生成可读的派生名（如 "e14·rsoc"）。
//
// 为什么不用纯 ID：纯 ID 在页面上无法阅读；「设备 + 类别」的组合既能读
// 又能唯一标识（唯一性由 DB 的 uniq_ds_source 索引保证）。
func derivedName(edgeDeviceID uint, category string) string {
	return fmt.Sprintf("e%d·%s", edgeDeviceID, category)
}

// DeriveInputFrom 从 DB 读取派生所需的输入（收敛器用，**唯一**查库点）。
//
// 为什么单列一个函数：派生本身是纯函数（internal/source.Derive 零 DB），
// 把查库集中在这里，好处是「派生逻辑可穷举单测、IO 可独立优化」。
//
// 数据源选择（性能关键）：
//   - 设备与观测字段来自 sensor_field_obs（轻量投影表），**不是** unified_data
//     （生产 3000 万行，扫它会把一次派生变成分钟级操作）。
//   - 不再按逻辑身份过滤：组键是 (edge_device_id, category)，每台有观测的设备
//     都该进台账。旧版只取已绑逻辑设备的行，逻辑层删除后这个过滤条件无对应列。
func (s *Service) DeriveInputFrom(ctx context.Context) (source.DeriveInput, error) {
	var in source.DeriveInput
	if s == nil || s.db == nil {
		return in, fmt.Errorf("datasource: nil service or db")
	}

	// 设备：全部存活的边缘设备（软删的由 GORM 默认范围排除）。
	if err := s.db.WithContext(ctx).Find(&in.Devices).Error; err != nil {
		return in, fmt.Errorf("datasource: load devices for derive: %w", err)
	}

	// 观测字段：按设备分组。
	type obsRow struct {
		DeviceID   uint
		SensorName string
	}
	var obs []obsRow
	if err := s.db.WithContext(ctx).Model(&models.SensorFieldObs{}).
		Select("device_id, sensor_name").Find(&obs).Error; err != nil {
		return in, fmt.Errorf("datasource: load field observations: %w", err)
	}
	in.Observed = make(map[uint][]string, len(in.Devices))
	for _, o := range obs {
		in.Observed[o.DeviceID] = append(in.Observed[o.DeviceID], o.SensorName)
	}
	return in, nil
}
