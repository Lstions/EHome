package datasource

// 来源组 CRUD 与成员拓扑（方案 §4.1/§4.2/§7.3 / §12.9.1 F-1）。所有写路径：
//   - 事务内做 DB 变更 + bumpRevision（唯一入口，白名单）；
//   - 事务提交后 publishGroup + 必要时 emit ActiveChange（commitGroupChange）；
//   - 校验失败一律 ErrInvalidRequest/ErrConflict/ErrNotFound（handler 映射 400/409/404）。

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"

	"gorm.io/gorm"
)

// GroupMemberInput 是创建/更新组时的成员声明。
type GroupMemberInput struct {
	EdgeDeviceID uint
	Priority     int
	Required     *bool // nil ⇒ kind 默认（primary_backup=false；composite=true）
}

// CreateGroupInput 是创建来源组的入参。
type CreateGroupInput struct {
	Name         string
	Kind         string
	Category     string
	Combine      string
	MinMembers   int
	StalenessSec int
	OnDegrade    string
	Enabled      *bool // nil ⇒ true
	Description  string
	Members      []GroupMemberInput
}

// UpdateGroupInput 是更新来源组入参；Kind/Category 不可变（变更语义见方案 F-1：
// 拓扑字段变更 bump revision，但本期 API 仅允许 combine/min_members/staleness/on_degrade/enabled/
// name/description/members；kind/category 不可变，避免成员观测口径失效）。
type UpdateGroupInput struct {
	Name         *string
	Combine      *string
	MinMembers   *int
	StalenessSec *int
	OnDegrade    *string
	Enabled      *bool
	Description  *string
	// Members 为 nil 表示不改成员；非 nil 表示**整体替换**（保留同名成员运行态）。
	Members *[]GroupMemberInput
}

// ListGroupFilter 是列表过滤条件。
type ListGroupFilter struct {
	Kind     string
	Enabled  *bool
	Category string
	Page     int
	PageSize int
}

const (
	defaultGroupPageSize = 20
	maxGroupPageSize     = 100
)

// validateGroupConfig 校验 V-1/V-2/V-3（kind×combine×min_members×required 组合）。
func validateGroupConfig(kind, category, combine string, minMembers int, onDegrade string) error {
	category = strings.TrimSpace(category)
	if category == "" {
		return fmt.Errorf("%w: category is required", ErrInvalidRequest)
	}
	kind = strings.TrimSpace(kind)
	combine = strings.TrimSpace(combine)
	onDegrade = strings.TrimSpace(onDegrade)
	switch kind {
	case models.SourceGroupKindPrimaryBackup:
		// V-2：primary_backup 不得设置 combine。
		if combine != "" {
			return fmt.Errorf("%w: primary_backup 组不得设置 combine（V-2）", ErrInvalidRequest)
		}
	case models.SourceGroupKindComposite:
		// V-1：composite 必须显式 combine 且在白名单内。
		valid := false
		for _, c := range models.CombineWhitelist {
			if combine == c {
				valid = true
				break
			}
		}
		if !valid {
			return fmt.Errorf("%w: composite 组必须显式设置 combine ∈ %v（V-1）", ErrInvalidRequest, models.CombineWhitelist)
		}
		if onDegrade != "" && onDegrade != models.OnDegradeFailClosed && onDegrade != models.OnDegradeUseAvailable {
			return fmt.Errorf("%w: on_degrade 非法: %q（允许 fail_closed|use_available）", ErrInvalidRequest, onDegrade)
		}
		if minMembers < 0 || minMembers > 64 {
			return fmt.Errorf("%w: min_members 越界: %d", ErrInvalidRequest, minMembers)
		}
	default:
		return fmt.Errorf("%w: kind 非法: %q（允许 primary_backup|composite）", ErrInvalidRequest, kind)
	}
	return nil
}

// validateMemberConfig 校验 V-4（设备存在+Enabled）/ V-5（category 已观测）/ 重复成员。
//
// db 参数显式传入：UpdateGroup 在事务内调用时必须传 tx —— 否则 SQLite :memory:
// 单连接库下事务占住连接、校验另开连接会看不到表（impl-core 验证报告 F-3）。
func (g *GroupService) validateMemberConfig(db *gorm.DB, kind, combine string, category string, members []GroupMemberInput) error {
	if kind == models.SourceGroupKindPrimaryBackup && len(members) == 0 {
		// 允许先建空组再补成员（规则可稍后绑定）；不在此处强制。
		return nil
	}
	seen := make(map[uint]struct{}, len(members))
	for _, m := range members {
		if m.EdgeDeviceID == 0 {
			return fmt.Errorf("%w: member.edge_device_id is required", ErrInvalidRequest)
		}
		if _, dup := seen[m.EdgeDeviceID]; dup {
			return fmt.Errorf("%w: 成员重复 edge_device_id=%d（I3）", ErrInvalidRequest, m.EdgeDeviceID)
		}
		seen[m.EdgeDeviceID] = struct{}{}
		var dev models.EdgeDevice
		if err := db.First(&dev, m.EdgeDeviceID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: 成员边缘设备 %d 不存在（V-4）", ErrInvalidRequest, m.EdgeDeviceID)
			}
			return err
		}
		if !dev.Enabled {
			return fmt.Errorf("%w: 成员边缘设备 %d 未启用（V-4）", ErrInvalidRequest, m.EdgeDeviceID)
		}
		if !g.categoryObserved(db, m.EdgeDeviceID, category) {
			return fmt.Errorf("%w: 边缘设备 %d 尚未上报字段 %s（V-5 category_not_observed）",
				ErrInvalidRequest, m.EdgeDeviceID, category)
		}
	}
	return nil
}

// categoryObserved 与 api 层 category_not_observed 同口径：投影表优先、unified_data 兜底。
func (g *GroupService) categoryObserved(db *gorm.DB, edgeDeviceID uint, category string) bool {
	var count int64
	if err := db.Model(&models.SensorFieldObs{}).
		Where("device_id = ? AND sensor_name = ?", edgeDeviceID, category).
		Count(&count).Error; err == nil && count > 0 {
		return true
	}
	if err := db.Model(&models.UnifiedData{}).
		Where("device_id = ? AND sensor_name = ?", edgeDeviceID, category).
		Limit(1).Count(&count).Error; err == nil && count > 0 {
		return true
	}
	return false
}

// normalizeStalenessSec 把 StalenessSec=0 归一为**推导值**（Lead 裁决 2026-10-22：
// 「0 = 不过期」不是合法语义——它会让 GroupSnapshot.Staleness()=0 ⇒ at.Sub(ts)>0 恒真
// ⇒ 全体成员恒 stale ⇒ 复合规则静默永不触发）。
//
// 推导规则（与 DeriveStaleness 同口径 + 无间隔兜底）：
//   - 有成员采集间隔 ⇒ 2×最大间隔，下限 30s、上限 3600s；
//   - 成员为空 / IntervalMs 全 0 ⇒ 60s（不是 0，保证组始终有保守过期窗口）；
//   - 显式非 0 值原样保留（用户可自行配置）。
func normalizeStalenessSec(configuredSec int, intervalsMillis []int) int {
	if configuredSec > 0 {
		return configuredSec
	}
	hasPositive := false
	for _, ms := range intervalsMillis {
		if ms > 0 {
			hasPositive = true
			break
		}
	}
	if !hasPositive {
		return 60
	}
	sec := int(DeriveStaleness(0, intervalsMillis).Seconds())
	if sec < 30 {
		sec = 30
	}
	if sec > 3600 {
		sec = 3600
	}
	return sec
}

// edgeDeviceIntervals 查询一组边缘设备的 IntervalMs（缺失设备按 0 计，不影响推导兜底）。
func edgeDeviceIntervals(db *gorm.DB, edgeDeviceIDs []uint) []int {
	if len(edgeDeviceIDs) == 0 {
		return nil
	}
	var intervals []int
	if err := db.Model(&models.EdgeDevice{}).
		Where("id IN ?", edgeDeviceIDs).
		Pluck("interval_ms", &intervals).Error; err != nil {
		return nil
	}
	return intervals
}

// normalizeStalenessForInputs 用于 CreateGroup / 成员整体替换（成员声明已知）。
func (g *GroupService) normalizeStalenessForInputs(db *gorm.DB, configuredSec int, inputs []GroupMemberInput) int {
	ids := make([]uint, 0, len(inputs))
	for _, in := range inputs {
		if in.EdgeDeviceID != 0 {
			ids = append(ids, in.EdgeDeviceID)
		}
	}
	return normalizeStalenessSec(configuredSec, edgeDeviceIntervals(db, ids))
}

// normalizeStalenessForGroup 用于 UpdateGroup（未替换成员时按当前成员推导）。
func (g *GroupService) normalizeStalenessForGroup(db *gorm.DB, configuredSec int, groupID uint) int {
	if configuredSec > 0 {
		return configuredSec
	}
	var members []models.SourceGroupMember
	if err := db.Where("group_id = ?", groupID).Find(&members).Error; err != nil {
		return 60
	}
	ids := make([]uint, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.EdgeDeviceID)
	}
	return normalizeStalenessSec(0, edgeDeviceIntervals(db, ids))
}

// CreateGroup 创建来源组 + 初始成员；primary_backup 组内首个成员自动 active（R7 反向）。
func (g *GroupService) CreateGroup(in CreateGroupInput) (*models.SourceGroup, error) {
	if err := validateGroupConfig(in.Kind, in.Category, in.Combine, in.MinMembers, in.OnDegrade); err != nil {
		return nil, err
	}
	if err := g.validateMemberConfig(g.db, in.Kind, in.Combine, strings.TrimSpace(in.Category), in.Members); err != nil {
		return nil, err
	}
	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	// StalenessSec=0 ⇒ 归一为推导值（绝不落库 0；见 normalizeStalenessSec 注释）。
	normalizedStaleness := g.normalizeStalenessForInputs(g.db, in.StalenessSec, in.Members)
	var out models.SourceGroup
	var event *ActiveChange
	err := g.db.Transaction(func(tx *gorm.DB) error {
		row := models.SourceGroup{
			Name:         strings.TrimSpace(in.Name),
			Kind:         in.Kind,
			Category:     strings.TrimSpace(in.Category),
			Combine:      in.Combine,
			MinMembers:   in.MinMembers,
			StalenessSec: normalizedStaleness,
			OnDegrade:    in.OnDegrade,
			Enabled:      enabled,
			Revision:     0,
			Description:  in.Description,
		}
		if row.Name == "" {
			row.Name = fmt.Sprintf("%s@%s", row.Kind, row.Category)
		}
		if err := tx.Create(&row).Error; err != nil {
			return err
		}
		out = row
		if len(in.Members) == 0 {
			return nil
		}
		created, err := g.createMembers(tx, row, in.Members)
		if err != nil {
			return err
		}
		if row.Kind == models.SourceGroupKindPrimaryBackup && len(created) > 0 {
			// 首成员 active（priority 最高者），其余 standby。
			anchor := created[0]
			if err := tx.Model(&models.SourceGroupMember{}).Where("id = ?", anchor.ID).
				Update("status", models.SourceMemberStatusActive).Error; err != nil {
				return err
			}
			rev, err := g.bumpGroupRevision(tx, row.ID)
			if err != nil {
				return err
			}
			event = &ActiveChange{
				PrevMemberID: 0, NextMemberID: anchor.ID, Reason: ReasonGroupManual,
				Revision: rev, TransitionSeq: g.nextTransitionSeq(row.ID), Category: row.Category,
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if event != nil {
		g.commitGroupChange(out.ID, event)
	} else {
		g.publishGroup(out.ID)
	}
	return g.GetGroup(out.ID)
}

// createMembers 在事务内创建成员行，返回按 priority DESC,id ASC 排序后的成员。
func (g *GroupService) createMembers(tx *gorm.DB, group models.SourceGroup, inputs []GroupMemberInput) ([]models.SourceGroupMember, error) {
	rows := make([]models.SourceGroupMember, 0, len(inputs))
	for _, m := range inputs {
		required := group.Kind == models.SourceGroupKindComposite
		if m.Required != nil {
			required = *m.Required
		}
		if group.Kind == models.SourceGroupKindPrimaryBackup {
			required = false // I2：主备组的 required 无意义
		}
		row := models.SourceGroupMember{
			GroupID:      group.ID,
			EdgeDeviceID: m.EdgeDeviceID,
			Priority:     m.Priority,
			Required:     required,
			Status:       models.SourceMemberStatusStandby,
			MaxFailCount: defaultMaxFailCount,
		}
		if err := tx.Create(&row).Error; err != nil {
			return nil, err
		}
		rows = append(rows, row)
	}
	// 排序：priority DESC, id ASC（R3 候选顺序，等价于 sourceref.SortMembers 的成员序）。
	for i := 1; i < len(rows); i++ {
		for j := i; j > 0; j-- {
			a, b := rows[j-1], rows[j]
			if a.Priority > b.Priority || (a.Priority == b.Priority && a.ID < b.ID) {
				break
			}
			rows[j-1], rows[j] = rows[j], rows[j-1]
		}
	}
	return rows, nil
}

// UpdateGroup 更新组配置；Members 非 nil 时整体替换成员集合（保留同名成员运行态）。
func (g *GroupService) UpdateGroup(groupID uint, in UpdateGroupInput) (*models.SourceGroup, error) {
	var out *models.SourceGroup
	var event *ActiveChange
	err := g.db.Transaction(func(tx *gorm.DB) error {
		var row models.SourceGroup
		if err := tx.First(&row, groupID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: source group %d", ErrGroupNotFound, groupID)
			}
			return err
		}
		// B2 终审收口（I6）：被规则引用的组禁止 enabled=false；否则规则 resolver 读到
		// Configured=false ⇒ 静默 unbound（本方案要消灭的静默失效类）。
		if in.Enabled != nil && !*in.Enabled && row.Enabled {
			refs, err := g.groupRuleReferences(tx, row.ID)
			if err != nil {
				return err
			}
			if refs > 0 {
				return fmt.Errorf("%w: 组 %d 仍被 %d 条规则引用，禁止停用（I6），请先解除规则绑定", ErrConflict, row.ID, refs)
			}
		}
		combine := row.Combine
		if in.Combine != nil {
			combine = strings.TrimSpace(*in.Combine)
		}
		minMembers := row.MinMembers
		if in.MinMembers != nil {
			minMembers = *in.MinMembers
		}
		onDegrade := row.OnDegrade
		if in.OnDegrade != nil {
			onDegrade = strings.TrimSpace(*in.OnDegrade)
		}
		if err := validateGroupConfig(row.Kind, row.Category, combine, minMembers, onDegrade); err != nil {
			return err
		}

		// StalenessSec=0（含"本次不传但库里本来就是 0"的历史脏值）⇒ 归一为推导值，
		// 绝不把 0 留在 DB（Lead 裁决：0 会让 snapshot.Staleness()=0 ⇒ 全体成员恒 stale）。
		effectiveStaleness := row.StalenessSec
		if in.StalenessSec != nil {
			effectiveStaleness = *in.StalenessSec
		}
		stalenessNeedsNormalize := effectiveStaleness <= 0
		if stalenessNeedsNormalize {
			if in.Members != nil {
				effectiveStaleness = g.normalizeStalenessForInputs(tx, 0, *in.Members)
			} else {
				effectiveStaleness = g.normalizeStalenessForGroup(tx, 0, row.ID)
			}
		}

		updates := map[string]interface{}{}
		if in.Name != nil && strings.TrimSpace(*in.Name) != "" {
			updates["name"] = strings.TrimSpace(*in.Name)
		}
		if in.Description != nil {
			updates["description"] = *in.Description
		}
		if in.Combine != nil {
			updates["combine"] = combine
		}
		if in.MinMembers != nil {
			updates["min_members"] = minMembers
		}
		if in.StalenessSec != nil || stalenessNeedsNormalize {
			updates["staleness_sec"] = effectiveStaleness
		}
		if in.OnDegrade != nil {
			updates["on_degrade"] = onDegrade
		}
		if in.Enabled != nil {
			updates["enabled"] = *in.Enabled
		}
		// F-1 revision bump 白名单（方案 §12.9.1）：只有**身份字段**变化才 bump。
		//   bump：成员集合/priority/required（replaceMembers 内部 bump）、combine、
		//         min_members、staleness_sec、on_degrade（均影响 MembersHash/求值口径）；
		//   不 bump：name/description（纯展示，改名重置窗口 identity 是错误行为）、
		//         enabled（GroupSnapshot.Enabled 变更靠 publish 传播，不改 identity）。
		identityChanged := in.Members != nil ||
			in.Combine != nil || in.MinMembers != nil ||
			in.StalenessSec != nil || in.OnDegrade != nil ||
			stalenessNeedsNormalize // 脏 0 → 推导值会改变求值口径，属身份变更
		if identityChanged {
			if _, err := g.bumpGroupRevision(tx, row.ID); err != nil {
				return err
			}
		}
		if len(updates) > 0 {
			if err := tx.Model(&models.SourceGroup{}).Where("id = ?", row.ID).Updates(updates).Error; err != nil {
				return err
			}
		}
		if in.Members != nil {
			if err := g.validateMemberConfig(tx, row.Kind, combine, row.Category, *in.Members); err != nil {
				return err
			}
			ev, err := g.replaceMembers(tx, row, *in.Members)
			if err != nil {
				return err
			}
			// B3 终审收口（N-7）：成员集合/优先级变更后，同事务回写绑组规则的 legacy 锚点。
			if err := g.reconcileRuleAnchors(tx, row.ID); err != nil {
				return err
			}
			event = ev
		}
		updated, err := g.GetGroupTx(tx, row.ID)
		if err != nil {
			return err
		}
		out = updated
		return nil
	})
	if err != nil {
		return nil, err
	}
	g.emitAll(nil)
	g.commitGroupChange(groupID, event)
	return out, nil
}

// replaceMembers 整体替换成员集合（保留 edge_device_id 相同的运行态），按需重选 active。
func (g *GroupService) replaceMembers(tx *gorm.DB, group models.SourceGroup, inputs []GroupMemberInput) (*ActiveChange, error) {
	var existing []models.SourceGroupMember
	if err := tx.Where("group_id = ?", group.ID).Find(&existing).Error; err != nil {
		return nil, err
	}
	byEdge := make(map[uint]models.SourceGroupMember, len(existing))
	for _, m := range existing {
		byEdge[m.EdgeDeviceID] = m
	}
	want := make(map[uint]struct{}, len(inputs))
	for _, in := range inputs {
		want[in.EdgeDeviceID] = struct{}{}
	}
	// 1) 删除不在新集合内的成员（若为 active，稍后重选）。
	removedActive := models.SourceGroupMember{}
	for _, m := range existing {
		if _, keep := want[m.EdgeDeviceID]; keep {
			continue
		}
		if m.Status == models.SourceMemberStatusActive {
			removedActive = m
		}
		if err := tx.Delete(&models.SourceGroupMember{}, m.ID).Error; err != nil {
			return nil, err
		}
	}
	// 2) upsert 新集合。
	for _, in := range inputs {
		required := group.Kind == models.SourceGroupKindComposite
		if in.Required != nil {
			required = *in.Required
		}
		if group.Kind == models.SourceGroupKindPrimaryBackup {
			required = false
		}
		if old, ok := byEdge[in.EdgeDeviceID]; ok {
			if err := tx.Model(&models.SourceGroupMember{}).Where("id = ?", old.ID).
				Updates(map[string]interface{}{"priority": in.Priority, "required": required}).Error; err != nil {
				return nil, err
			}
			continue
		}
		row := models.SourceGroupMember{
			GroupID: group.ID, EdgeDeviceID: in.EdgeDeviceID, Priority: in.Priority,
			Required: required, Status: models.SourceMemberStatusStandby, MaxFailCount: defaultMaxFailCount,
		}
		if err := tx.Create(&row).Error; err != nil {
			return nil, err
		}
	}
	// 3) 自愈式补齐 active（primary_backup 且当前无 active）。
	if group.Kind != models.SourceGroupKindPrimaryBackup {
		return nil, nil
	}
	var members []models.SourceGroupMember
	if err := tx.Where("group_id = ?", group.ID).Find(&members).Error; err != nil {
		return nil, err
	}
	var activeCount int64
	if err := tx.Model(&models.SourceGroupMember{}).
		Where("group_id = ? AND status = ?", group.ID, models.SourceMemberStatusActive).
		Count(&activeCount).Error; err != nil {
		return nil, err
	}
	if activeCount > 0 {
		return nil, nil
	}
	candidate, ok := pickCandidate(members)
	if !ok {
		return nil, nil
	}
	rev, err := g.bumpGroupRevision(tx, group.ID)
	if err != nil {
		return nil, err
	}
	if err := tx.Model(&models.SourceGroupMember{}).Where("id = ?", candidate.ID).
		Update("status", models.SourceMemberStatusActive).Error; err != nil {
		return nil, err
	}
	prev := uint(0)
	reason := ReasonGroupManual
	if removedActive.ID != 0 {
		prev = removedActive.ID
		reason = ReasonGroupDelete
	}
	if err := g.writeGroupHealth(tx, group.ID, candidate.ID, "transition",
		fmt.Sprintf("成员集合变更后补选 active 成员 %d", candidate.ID)); err != nil {
		return nil, err
	}
	return &ActiveChange{
		PrevMemberID: prev, NextMemberID: candidate.ID, Reason: reason,
		Revision: rev, TransitionSeq: g.nextTransitionSeq(group.ID), Category: group.Category,
	}, nil
}

// DeleteGroup 删除来源组（I6/V-6：被规则引用 ⇒ 409）。成员行删除，切换/健康日志保留作审计。
func (g *GroupService) DeleteGroup(groupID uint) error {
	var event *ActiveChange
	err := g.db.Transaction(func(tx *gorm.DB) error {
		var row models.SourceGroup
		if err := tx.First(&row, groupID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return fmt.Errorf("%w: source group %d", ErrGroupNotFound, groupID)
			}
			return err
		}
		refs, err := g.groupRuleReferences(tx, groupID)
		if err != nil {
			return err
		}
		if refs > 0 {
			return fmt.Errorf("%w: source group %d 仍被 %d 条规则引用，拒绝删除（I6）", ErrConflict, groupID, refs)
		}
		var active models.SourceGroupMember
		if err := tx.Where("group_id = ? AND status = ?", groupID, models.SourceMemberStatusActive).
			First(&active).Error; err == nil {
			// 删除 active = 权威迁移，必须带单调 Revision/TransitionSeq（否则消费方按
			// GroupID 清窗会失败——impl-core 验证报告 F-2）。在删行前 bump 以便取到新值。
			rev, err := g.bumpGroupRevision(tx, groupID)
			if err != nil {
				return err
			}
			event = &ActiveChange{
				PrevMemberID: active.ID, NextMemberID: 0, Reason: ReasonGroupDelete,
				Category: row.Category, Revision: rev,
				TransitionSeq: g.nextTransitionSeq(groupID),
			}
		}
		if err := tx.Where("group_id = ?", groupID).Delete(&models.SourceGroupMember{}).Error; err != nil {
			return err
		}
		if err := tx.Delete(&models.SourceGroup{}, groupID).Error; err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		return err
	}
	// 统一收尾：commitGroupChange 会 publishGroup（组已删 ⇒ 自动 registry.Remove），
	// 再按同一事件通道发射 ActiveChange（GroupID/At/TransitionSeq 均由它补齐）。
	g.commitGroupChange(groupID, event)
	return nil
}

// groupRuleReferences 统计引用该组的规则数（automation_rules + alert_rules）。
func (g *GroupService) groupRuleReferences(tx *gorm.DB, groupID uint) (int64, error) {
	var automation, alert int64
	if err := tx.Model(&models.AutomationRule{}).Where("source_group_id = ?", groupID).Count(&automation).Error; err != nil {
		return 0, err
	}
	if err := tx.Model(&models.AlertRule{}).Where("source_group_id = ?", groupID).Count(&alert).Error; err != nil {
		return 0, err
	}
	return automation + alert, nil
}

// reconcileRuleAnchors 在成员变更的**同一事务**内回写引用本组规则的 legacy 锚点列（B3/N-7）。
//
// 为什么必须：锚点（回滚旧二进制时的退化目标）由规则保存期双写；成员/优先级变更后若
// 不更新，锚点会指向"已不在组内"的设备 ⇒ 回滚旧二进制会用错误来源触发动作。
//
// 规则：
//   - 新锚点 = priority DESC, id ASC 首成员（composite 同口径，见 AnchorMember）；
//   - anchor==0（组已空）且仍有规则引用 ⇒ 返回 ErrConflict（禁止制造悬空锚点；
//     调用方应在此之前拒绝"删除最后一个成员"）；
//   - 只更新 source_group_id 指向本组的规则（存量规则 NULL 不受影响）。
func (g *GroupService) reconcileRuleAnchors(tx *gorm.DB, groupID uint) error {
	var members []models.SourceGroupMember
	if err := tx.Where("group_id = ?", groupID).Find(&members).Error; err != nil {
		return err
	}
	anchor, ok := AnchorMember(members)
	if !ok {
		refs, err := g.groupRuleReferences(tx, groupID)
		if err != nil {
			return err
		}
		if refs > 0 {
			return fmt.Errorf("%w: 组 %d 仍有 %d 条规则引用，禁止删除最后一个成员（会产生悬空锚点）", ErrConflict, groupID, refs)
		}
		return nil
	}
	if err := tx.Model(&models.AutomationRule{}).Where("source_group_id = ?", groupID).
		Update("trigger_edge_device_id", anchor.EdgeDeviceID).Error; err != nil {
		return err
	}
	return tx.Model(&models.AlertRule{}).Where("source_group_id = ?", groupID).
		Updates(map[string]interface{}{
			"target_type": models.AlertTargetEdgeDevice,
			"target_id":   anchor.EdgeDeviceID,
		}).Error
}

// GroupRuleReferences 是 handler 用的导出包装（删除前置提示）。
func (g *GroupService) GroupRuleReferences(groupID uint) (int64, error) {
	return g.groupRuleReferences(g.db, groupID)
}

// GetGroup 返回组 + 成员（handler 嵌套渲染用）。成员按 priority DESC, id ASC 排序。
func (g *GroupService) GetGroup(groupID uint) (*models.SourceGroup, error) {
	return g.GetGroupTx(g.db, groupID)
}

// GetGroupTx 在给定连接/事务内读取组。返回值只有组行；成员由 GetGroupWithMembers 提供。
func (g *GroupService) GetGroupTx(db *gorm.DB, groupID uint) (*models.SourceGroup, error) {
	var row models.SourceGroup
	if err := db.First(&row, groupID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, fmt.Errorf("%w: source group %d", ErrGroupNotFound, groupID)
		}
		return nil, err
	}
	return &row, nil
}

// GetGroupWithMembers 返回组 + 成员（handler 的详情/列表渲染）。
func (g *GroupService) GetGroupWithMembers(groupID uint) (*models.SourceGroup, []models.SourceGroupMember, error) {
	row, err := g.GetGroup(groupID)
	if err != nil {
		return nil, nil, err
	}
	var members []models.SourceGroupMember
	if err := g.db.Where("group_id = ?", groupID).Find(&members).Error; err != nil {
		return nil, nil, err
	}
	sortGroupMembers(members)
	return row, members, nil
}

// ListGroups 分页列表。
func (g *GroupService) ListGroups(f ListGroupFilter) ([]models.SourceGroup, int64, error) {
	if f.Page <= 0 {
		f.Page = 1
	}
	if f.PageSize <= 0 {
		f.PageSize = defaultGroupPageSize
	}
	if f.PageSize > maxGroupPageSize {
		f.PageSize = maxGroupPageSize
	}
	q := g.db.Model(&models.SourceGroup{})
	if f.Kind != "" {
		q = q.Where("kind = ?", f.Kind)
	}
	if f.Enabled != nil {
		q = q.Where("enabled = ?", *f.Enabled)
	}
	if f.Category != "" {
		q = q.Where("category = ?", f.Category)
	}
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	var rows []models.SourceGroup
	if err := q.Order("id ASC").Offset((f.Page - 1) * f.PageSize).Limit(f.PageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}

// ListMembers 返回组内成员（排序后）。
func (g *GroupService) ListMembers(groupID uint) ([]models.SourceGroupMember, error) {
	var members []models.SourceGroupMember
	if err := g.db.Where("group_id = ?", groupID).Find(&members).Error; err != nil {
		return nil, err
	}
	sortGroupMembers(members)
	return members, nil
}

func sortGroupMembers(members []models.SourceGroupMember) {
	for i := 1; i < len(members); i++ {
		for j := i; j > 0; j-- {
			a, b := members[j-1], members[j]
			if a.Priority > b.Priority || (a.Priority == b.Priority && a.ID < b.ID) {
				break
			}
			members[j-1], members[j] = members[j], members[j-1]
		}
	}
}

// prepareManualActivate 校验 R7 前置并返回（组、目标成员、原 active 成员 ID）。不 bump revision
// （幂等 no-op 不得改 identity；由调用方在确认发生切换后 bump）。
func (g *GroupService) prepareManualActivate(tx *gorm.DB, groupID, memberID uint) (*models.SourceGroup, models.SourceGroupMember, uint, error) {
	var group models.SourceGroup
	if err := tx.First(&group, groupID).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, models.SourceGroupMember{}, 0, fmt.Errorf("%w: source group %d", ErrGroupNotFound, groupID)
		}
		return nil, models.SourceGroupMember{}, 0, err
	}
	if group.Kind != models.SourceGroupKindPrimaryBackup {
		return nil, models.SourceGroupMember{}, 0, fmt.Errorf("%w: composite 组没有 active 概念，禁止 activate（V-6）", ErrInvalidRequest)
	}
	var target models.SourceGroupMember
	if err := tx.Where("group_id = ? AND id = ?", groupID, memberID).First(&target).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, models.SourceGroupMember{}, 0, fmt.Errorf("%w: member %d", ErrNotFound, memberID)
		}
		return nil, models.SourceGroupMember{}, 0, err
	}
	var prev models.SourceGroupMember
	err := tx.Where("group_id = ? AND status = ?", groupID, models.SourceMemberStatusActive).First(&prev).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, models.SourceGroupMember{}, 0, err
	}
	return &group, target, prev.ID, nil
}

// AnchorEdgeDeviceID 返回组锚点成员的边缘设备 ID（裁决 7 双写用；空组=0）。
func (g *GroupService) AnchorEdgeDeviceID(groupID uint) (uint, error) {
	members, err := g.ListMembers(groupID)
	if err != nil {
		return 0, err
	}
	anchor, ok := AnchorMember(members)
	if !ok {
		return 0, nil
	}
	return anchor.EdgeDeviceID, nil
}

// FailoverLogs 组级切换日志（handler GET /:id/failover-logs）。
func (g *GroupService) FailoverLogs(groupID uint, limit int) ([]models.SourceGroupFailoverLog, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	var rows []models.SourceGroupFailoverLog
	if err := g.db.Where("group_id = ?", groupID).Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// Health 组级健康事件（handler GET /:id/health）。
func (g *GroupService) Health(groupID uint, limit int) ([]models.SourceGroupHealth, error) {
	if limit <= 0 {
		limit = defaultLimit
	}
	if limit > maxLimit {
		limit = maxLimit
	}
	var rows []models.SourceGroupHealth
	if err := g.db.Where("group_id = ?", groupID).Order("id DESC").Limit(limit).Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

var _ = time.Now // 保留 time 导入（构造函数/MarkFailure 的时间语义由 opts.Now 统一）
var _ = logger.Warn
