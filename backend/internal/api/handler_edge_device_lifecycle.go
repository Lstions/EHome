package api

// handler_edge_device_lifecycle.go — 边缘设备创建/删除的辅助逻辑。
//
// 2026-10-23: 逻辑设备概念层整体移除 (.contract/logical-device-removal.md)，
// 本文件里原本的「创建继承」三件套随之删除：
//   - validateInheritanceTarget —— 没有身份可继承
//   - checkLivingInstanceUniqueness —— 「一个身份只能有一个存活实例」这条不变量
//     的前提（逻辑身份）不存在了；同通道同型号同地址的唯一性由
//     checkDeviceUniqueness 保证（那才是真实的物理约束）
//   - attachNewLogicalDevice —— 不再需要给每台设备挂一个身份锚点
//
// 保留的是两件与「身份」无关、纯设备维度的事：
//   - handleConfigTemplateOnDelete: 删除设备时的模板归属处理
//   - copyCalibrationIfInPlace: 原位置重建时复制工厂校准值

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// errNodeNotFound 是 POST /edge-devices 事务内"node 不存在"的哨兵，
// 出口映射为 HTTP 404（设计文档 §7.2）。用哨兵而非 fmt.Errorf 是为了在
// 事务出口把 404 与其它 400 校验失败区分开。
var errNodeNotFound = errors.New("node not found")

// conflictError 携带 409 语义的事务错误。POST /edge-devices 事务出口按
// errors.As 映射为 HTTP 409, 其余错误维持 400。
type conflictError struct{ msg string }

func (e conflictError) Error() string { return e.msg }

func newConflictError(format string, args ...interface{}) error {
	return conflictError{msg: fmt.Sprintf(format, args...)}
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

// copyCalibrationIfInPlace 原位置重建场景复制校准数据。
//
// 判据（逻辑设备层移除后重写，2026-10-23）: 存在一台**已软删**的旧设备，
// 与待建设备同 channel_id + 同 type + 同 hardware_id（硬件地址）。这只在
// 「同一物理位置上的同一台设备被删掉又重建」时成立 —— 正是工厂校准值仍然
// 有效的那种情形。
//
// 为什么不再按逻辑身份判断: 原来用 logical_device_id 相等来认定「同一物理
// 个体」，但逻辑身份是抽象出来的概念，与物理个体并不等价（它把换过节点的
// 设备也算成同一个）。设备的物理同一性由 (channel, type, hardware_id) 直接
// 表达，不需要中间层。
//
// 其余场景一律不复制: 校准是物理个体的工厂值，跨设备继承在业务上是错的。
func copyCalibrationIfInPlace(tx *gorm.DB, dev *models.EdgeDevice) error {
	hw := strings.TrimSpace(dev.HardwareID)
	var src models.EdgeDevice
	q := tx.Unscoped().
		Where("channel_id = ? AND type = ? AND id <> ? AND deleted_at IS NOT NULL",
			dev.ChannelID, dev.Type, dev.ID)
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
