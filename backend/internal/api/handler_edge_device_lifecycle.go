package api

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ehome/backend/internal/datalifecycle"
	"ehome/backend/internal/models"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// handler_edge_device_lifecycle.go — 创建继承后端逻辑 (方案 v3.3 §3.3 T5 P2)。

// conflictError 携带 409 语义的事务错误。POST /edge-devices 事务出口按
// errors.As 映射为 HTTP 409, 其余错误维持 400。
type conflictError struct{ msg string }

func (e conflictError) Error() string { return e.msg }

func newConflictError(format string, args ...interface{}) error {
	return conflictError{msg: fmt.Sprintf(format, args...)}
}

// validateInheritanceTarget 校验继承目标并返回之 (方案 §3.3-2, 事务内):
// 存在 + device_type 匹配 + merged_into IS NULL + purge_requested=FALSE
// (v3.3-N1)。行级锁串行化同一目标的并发继承, 配合后续存活实例唯一性
// 校验关闭"双创建同时通过 count=0 检查"的竞态窗口。
func validateInheritanceTarget(tx *gorm.DB, logicalDeviceID uint, deviceType string) (*models.LogicalDevice, error) {
	var ld models.LogicalDevice
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		First(&ld, logicalDeviceID).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, newConflictError("继承目标逻辑设备 #%d 不存在", logicalDeviceID)
		}
		return nil, fmt.Errorf("lookup logical_device %d: %w", logicalDeviceID, err)
	}
	if ld.DeviceType != deviceType {
		return nil, newConflictError(
			"目标逻辑设备「%s」的设备类型为 %q, 与待创建设备类型 %q 不匹配",
			ld.Name, ld.DeviceType, deviceType)
	}
	if ld.MergedInto != nil {
		return nil, newConflictError(
			"目标逻辑设备「%s」已并入逻辑设备 #%d, 请改为继承合并目标",
			ld.Name, *ld.MergedInto)
	}
	if ld.PurgeRequested {
		// v3.3-N1 闭环: 目标数据已标记删除时拒绝继承。
		return nil, newConflictError("该逻辑设备的数据已标记删除，无法继承")
	}
	return &ld, nil
}

// checkLivingInstanceUniqueness 存活实例唯一性校验 (方案 §3.3-3, HIGH):
// 同一 logical_device 同时只允许一个存活实例。scoped Count (GORM 软删
// 默认 scope 只计未删实例)。冲突 409 文案携带旧实例定位信息 (实例 ID +
// 节点名) 引导"先删旧实例再建新实例"的正确顺序 (终审 C-1)。
func checkLivingInstanceUniqueness(tx *gorm.DB, ld *models.LogicalDevice) error {
	var living []models.EdgeDevice
	if err := tx.Where("logical_device_id = ?", ld.ID).
		Order("id").Limit(1).Find(&living).Error; err != nil {
		return fmt.Errorf("count living instances of logical_device %d: %w", ld.ID, err)
	}
	if len(living) == 0 {
		return nil
	}
	inst := living[0]
	nodeName := inst.NodeID
	var node models.Node
	if err := tx.Where("node_id = ?", inst.NodeID).First(&node).Error; err == nil && strings.TrimSpace(node.Name) != "" {
		nodeName = node.Name
	}
	return newConflictError(
		"目标逻辑设备「%s」已有关联的存活实例（实例 ID %d，节点 %s）。请先删除旧实例（历史数据会保留并自动并入），再重新创建；如需保留两个独立身份，请选择「作为新设备创建」。",
		ld.Name, inst.ID, nodeName)
}

// attachNewLogicalDevice 为"作为新设备创建"路径新建逻辑身份 (方案
// §3.3-4/§2.3.1 路径表): 永不复用既有 key, 撞车追加序号。在 tx.Create
// 之后调用——dev.ID 已就绪, 空 hardware_id 的确定性 uuid v5 派生
// (IdentityKey) 才有稳定基准 (T1.1)。创建后回写 FK。
func attachNewLogicalDevice(tx *gorm.DB, dev *models.EdgeDevice) error {
	ld, err := datalifecycle.EnsureLogicalDevice(tx, dev, datalifecycle.PathCreateNew, datalifecycle.SystemRetentionDays())
	if err != nil {
		return fmt.Errorf("create logical device for edge_device %d: %w", dev.ID, err)
	}
	if err := tx.Model(&models.EdgeDevice{}).Where("id = ?", dev.ID).
		Update("logical_device_id", ld.ID).Error; err != nil {
		return fmt.Errorf("back-write logical_device_id of edge_device %d: %w", dev.ID, err)
	}
	dev.LogicalDeviceID = &ld.ID
	return nil
}

// handleConfigTemplateOnDelete 删除设备时的 ConfigTemplate 归属处理 (方案
// v3.3 §2.4): 查 edge_device_id=:id 的模板 → 引用检查 (同 channel 有其他
// 存活同 type 设备则保留) → 无引用则从 channel.template_ids 移除 (字符串
// 解析重建) 并删除 ConfigTemplate 行。edge_device_id IS NULL 的旧模板
// (multi-drop 共享池 / 自愈创建) 不在删除时动, 宁留勿删。
//
// 必须在删除事务内调用 (与软删同事务, 失败回滚)。inst 是被删实例的
// 已加载行 (含 ChannelID/Type)。被删实例在事务内已置 DeletedAt (软删),
// 因此引用检查用 Unscoped 统计同 channel 同 type 的实例 —— 若只存在
// 已删的同型实例 (multi-drop 逐个删), 其归属模板同样清理, 防止残留
// 模板与 template_ids 条目 (ESP32 maxTemplates=16 溢出风险)。
func handleConfigTemplateOnDelete(tx *gorm.DB, inst *models.EdgeDevice) error {
	var owned []models.ConfigTemplate
	if err := tx.Where("edge_device_id = ?", inst.ID).Find(&owned).Error; err != nil {
		return fmt.Errorf("query owned config_templates of edge_device %d: %w", inst.ID, err)
	}
	if len(owned) == 0 {
		return nil
	}

	// 引用检查: 同 channel 是否还有其他存活同 type 设备 (multi-drop 场景)。
	// 有 → 模板保留不删 (可能仍被其他实例引用)。
	// 注意: 同型实例已全部软删 (含本次被删与先前删的) 时, 其归属模板
	// 一并清理 —— 先收集所有同 channel 同 type 实例 (Unscoped) 的归属
	// 模板, 再统一移除。
	var living int64
	if err := tx.Model(&models.EdgeDevice{}).
		Where("channel_id = ? AND type = ? AND id <> ? AND deleted_at IS NULL", inst.ChannelID, inst.Type, inst.ID).
		Count(&living).Error; err != nil {
		return fmt.Errorf("count living same-type devices on channel %d: %w", inst.ChannelID, err)
	}
	if living > 0 {
		return nil // 保留模板, 不删除
	}

	// 无其他存活引用 → 收集同 channel 同 type 全部实例 (含已软删) 的
	// 归属模板, 一并从 channel.template_ids 移除并删除行。
	ownedIDs := make(map[string]struct{}, len(owned))
	for i := range owned {
		ownedIDs[strconv.FormatUint(uint64(owned[i].ID), 10)] = struct{}{}
	}
	var allSameType []models.EdgeDevice
	if err := tx.Unscoped().Where("channel_id = ? AND type = ?", inst.ChannelID, inst.Type).
		Find(&allSameType).Error; err != nil {
		return fmt.Errorf("scan same-type instances on channel %d: %w", inst.ChannelID, err)
	}
	for i := range allSameType {
		if allSameType[i].ID == inst.ID {
			continue
		}
		var more []models.ConfigTemplate
		if err := tx.Where("edge_device_id = ?", allSameType[i].ID).Find(&more).Error; err != nil {
			return fmt.Errorf("query owned config_templates of edge_device %d: %w", allSameType[i].ID, err)
		}
		for j := range more {
			ownedIDs[strconv.FormatUint(uint64(more[j].ID), 10)] = struct{}{}
		}
	}
	var ch models.Channel
	if err := tx.First(&ch, inst.ChannelID).Error; err != nil {
		return fmt.Errorf("load channel %d for template cleanup: %w", inst.ChannelID, err)
	}
	if strings.TrimSpace(ch.TemplateIDs) != "" {
		var kept []string
		for _, idStr := range strings.Split(ch.TemplateIDs, ",") {
			idStr = strings.TrimSpace(idStr)
			if idStr == "" {
				continue
			}
			if _, drop := ownedIDs[idStr]; drop {
				continue
			}
			kept = append(kept, idStr)
		}
		newIDs := strings.Join(kept, ",")
		if err := tx.Model(&models.Channel{}).Where("id = ?", ch.ID).
			Update("template_ids", newIDs).Error; err != nil {
			return fmt.Errorf("rebuild channel %d template_ids: %w", ch.ID, err)
		}
	}
	// 删除归属模板行 (无 FK 级联, 显式删除)。
	if err := tx.Where("edge_device_id IN ?", keysToUints(ownedIDs)).Delete(&models.ConfigTemplate{}).Error; err != nil {
		return fmt.Errorf("delete owned config_templates of edge_device %d: %w", inst.ID, err)
	}
	return nil
}

// keysToUints 把模板 ID 字符串集合转成 uint 切片 (供 IN 查询)。
func keysToUints(keys map[string]struct{}) []uint {
	out := make([]uint, 0, len(keys))
	for k := range keys {
		if v, err := strconv.ParseUint(k, 10, 64); err == nil {
			out = append(out, uint(v))
		}
	}
	return out
}

// copyCalibrationIfInPlace 原位置重建场景复制校准数据 (方案 §2.5):
// 仅当候选实例集中存在同 channel_id + hardware_id + type 的旧实例
// (权重 100 档) 时, 把该实例的 calibration_cache 行复制到新
// edge_device_id; 其余场景不复制 (校准是物理个体的工厂校准值, 跨实例
// 继承校准业务上错误)。
func copyCalibrationIfInPlace(tx *gorm.DB, dev *models.EdgeDevice) error {
	if dev.LogicalDeviceID == nil {
		return nil
	}
	hw := strings.TrimSpace(dev.HardwareID)
	var src models.EdgeDevice
	q := tx.Unscoped().
		Where("logical_device_id = ? AND channel_id = ? AND type = ? AND id <> ?",
			*dev.LogicalDeviceID, dev.ChannelID, dev.Type, dev.ID)
	if hw != "" {
		q = q.Where("hardware_id = ?", hw)
	} else {
		q = q.Where("hardware_id = '' OR hardware_id IS NULL")
	}
	if err := q.Order("id DESC").First(&src).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil // 非原位置重建场景: 不复制
		}
		return fmt.Errorf("lookup in-place source instance: %w", err)
	}
	var cals []models.CalibrationCache
	if err := tx.Where("edge_device_id = ?", src.ID).Find(&cals).Error; err != nil {
		return fmt.Errorf("lookup calibration of instance %d: %w", src.ID, err)
	}
	for _, cal := range cals {
		row := models.CalibrationCache{
			NodeID:       cal.NodeID,
			EdgeDeviceID: dev.ID,
			DeviceType:   cal.DeviceType,
			Data:         cal.Data,
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("copy calibration row %d to edge_device %d: %w", cal.ID, dev.ID, err)
		}
	}
	return nil
}
