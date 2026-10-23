package datasource

import (
	"context"
	"fmt"

	"ehome/backend/internal/models"
	"ehome/backend/internal/source"

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
		DeviceID     uint
		Category     string
		EdgeDeviceID uint
		Status       string
		ManagedBy    string
	}
	var rows []existingRow
	if err := s.db.WithContext(ctx).Model(&models.DataSource{}).
		Select("id, device_id, category, edge_device_id, status, managed_by").
		Find(&rows).Error; err != nil {
		return res, fmt.Errorf("datasource: load existing sources: %w", err)
	}

	type triple struct {
		deviceID     uint
		category     string
		edgeDeviceID uint
	}
	autoByTriple := make(map[triple]*existingRow, len(rows))
	manualTriples := make(map[triple]struct{}, len(rows))
	for i := range rows {
		t := triple{rows[i].DeviceID, rows[i].Category, rows[i].EdgeDeviceID}
		if rows[i].ManagedBy == models.DataSourceManagedManual {
			manualTriples[t] = struct{}{}
			continue
		}
		autoByTriple[t] = &rows[i]
	}

	// 2) 展开期望态为三元组（每台成员设备一行）。
	desired := make(map[triple]struct{}, 0)
	desiredStatus := make(map[triple]string, 0)
	for _, h := range handles {
		if h == nil {
			continue
		}
		key := h.Key()
		if key.LogicalDeviceID == 0 || key.Category == "" {
			continue
		}
		for _, m := range h.Members() {
			t := triple{key.LogicalDeviceID, key.Category, m.EdgeDeviceID}
			desired[t] = struct{}{}
			// 主备：active 成员 active，其余 standby；直连/复合：active
			// （复合组没有单一权威，但每个成员都在供数，标 active 更符合事实）。
			status := StatusActive
			if h.Kind() == source.KindPrimaryBackup && !m.Active {
				status = StatusStandby
			}
			desiredStatus[t] = status
		}
	}
	res.Total = len(desired)

	// 3) 收敛（单事务：部分成功会让台账处于自相矛盾状态）。
	err := s.db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for t := range desired {
			status := desiredStatus[t]
			existing, hasAuto := autoByTriple[t]
			if _, isManual := manualTriples[t]; isManual {
				// 人工配置优先：跳过，不覆盖。
				res.SkippedManual++
				continue
			}
			if !hasAuto {
				row := models.DataSource{
					DeviceID:     t.deviceID,
					Category:     t.category,
					EdgeDeviceID: t.edgeDeviceID,
					SourceType:   "edge_device",
					Name:         derivedName(t.deviceID, t.category, t.edgeDeviceID),
					Priority:     0,
					IsPrimary:    status == StatusActive,
					MaxFailCount: 3,
					Status:       status,
					ManagedBy:    models.DataSourceManagedAuto,
					DerivedAt:    &now,
				}
				if err := tx.Create(&row).Error; err != nil {
					return fmt.Errorf("datasource: create derived source (%d,%s,%d): %w",
						t.deviceID, t.category, t.edgeDeviceID, err)
				}
				res.Created++
				continue
			}
			// 已存在 auto 行：只在状态或派生时间需要更新时写库（减少无谓更新）。
			if existing.Status == status {
				// 状态没变，只刷新派生时间（可观测「派生器还活着」）。
				if err := tx.Model(&models.DataSource{}).Where("id = ?", existing.ID).
					Update("derived_at", now).Error; err != nil {
					return fmt.Errorf("datasource: touch derived_at %d: %w", existing.ID, err)
				}
				continue
			}
			if err := tx.Model(&models.DataSource{}).Where("id = ?", existing.ID).
				Updates(map[string]interface{}{
					"status":     status,
					"is_primary": status == StatusActive,
					"derived_at": now,
				}).Error; err != nil {
				return fmt.Errorf("datasource: update derived source %d: %w", existing.ID, err)
			}
			res.Updated++
		}

		// 4) 期望态里没有的 auto 行 ⇒ disable（**不删除**，保留血缘）。
		for t, row := range autoByTriple {
			if _, want := desired[t]; want {
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

// derivedName 生成可读的派生名（如 "dev14·rsoc"）。
//
// 为什么不用纯 ID：纯 ID 在页面上无法阅读；带逻辑设备与类别的组合
// 既能读又能唯一标识（唯一性由 DB 的 uniq_ds_source 索引保证）。
func derivedName(deviceID uint, category string, edgeDeviceID uint) string {
	return fmt.Sprintf("dev%d·%s@e%d", deviceID, category, edgeDeviceID)
}

// DeriveInputFrom 从 DB 读取派生所需的输入（收敛器用，**唯一**查库点）。
//
// 为什么单列一个函数：派生本身是纯函数（internal/source.Derive 零 DB），
// 把查库集中在这里，好处是「派生逻辑可穷举单测、IO 可独立优化」。
//
// 数据源选择（性能关键）：
//   - 设备与观测字段来自 sensor_field_obs（轻量投影表），**不是** unified_data
//     （生产 3000 万行，扫它会把一次派生变成分钟级操作）。
//   - 只取已绑定逻辑设备的设备：未绑定设备无法定来源键。
func (s *Service) DeriveInputFrom(ctx context.Context) (source.DeriveInput, error) {
	var in source.DeriveInput
	if s == nil || s.db == nil {
		return in, fmt.Errorf("datasource: nil service or db")
	}

	// 设备：只取已绑定逻辑设备的。
	if err := s.db.WithContext(ctx).
		Where("logical_device_id IS NOT NULL AND logical_device_id > 0").
		Find(&in.Devices).Error; err != nil {
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
