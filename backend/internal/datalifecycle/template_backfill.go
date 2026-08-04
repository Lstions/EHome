package datalifecycle

import (
	"fmt"
	"log/slog"
	"strings"

	"gorm.io/gorm"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
)

// BackfillConfigTemplateOwnership 尽力回填 config_templates.edge_device_id
// (方案 v3.3 §2.4-2 / §七-3, P4 收尾)。
//
// 规则: 对每个 edge_device_id IS NULL 的模板, 找该模板所属 node 上同
// channel 的存活设备, 其 type 的 driver CommandTemplates.WriteData 与模板
// WriteData 精确匹配 (大小写归一)。恰好一个候选 → 写入归属; 0 个或多个
// (multi-drop 共享池, WriteData 无法可靠区分归属) → 留 NULL (宁留勿删,
// 删除设备时只处理归属明确的模板)。
//
// 幂等: 只处理 IS NULL 行, 重复执行不重复写入。失败返回错误 (调用方决定
// 是否阻断启动); 单模板失败不中断整体, 记 warn 继续。
func BackfillConfigTemplateOwnership(db *gorm.DB, driverRegistry *drivers.Registry) (backfilled int, err error) {
	if driverRegistry == nil {
		return 0, nil
	}
	var templates []models.ConfigTemplate
	if err := db.Where("edge_device_id IS NULL").Find(&templates).Error; err != nil {
		return 0, fmt.Errorf("datalifecycle: scan unowned config_templates: %w", err)
	}
	if len(templates) == 0 {
		return 0, nil
	}

	// 预载存活设备 (deleted_at IS NULL), 按 channel_id 分组。
	devByChannel := map[uint][]models.EdgeDevice{}
	var devices []models.EdgeDevice
	if err := db.Where("deleted_at IS NULL").Find(&devices).Error; err != nil {
		return 0, fmt.Errorf("datalifecycle: scan living edge_devices for template backfill: %w", err)
	}
	for i := range devices {
		devByChannel[devices[i].ChannelID] = append(devByChannel[devices[i].ChannelID], devices[i])
	}

	for i := range templates {
		tmpl := &templates[i]
		ownerID, matched, merr := matchTemplateOwner(driverRegistry, tmpl, devByChannel)
		if merr != nil {
			slog.Warn("datalifecycle: template ownership match failed",
				"template_id", tmpl.ID, "error", merr)
			continue
		}
		if !matched {
			continue // 0 或多个候选 → 留 NULL
		}
		if err := db.Model(&models.ConfigTemplate{}).Where("id = ?", tmpl.ID).
			Update("edge_device_id", ownerID).Error; err != nil {
			slog.Warn("datalifecycle: template ownership backfill write failed",
				"template_id", tmpl.ID, "error", err)
			continue
		}
		backfilled++
	}
	if backfilled > 0 {
		slog.Info("datalifecycle: config_template ownership backfilled",
			"templates", backfilled)
	}
	return backfilled, nil
}

// matchTemplateOwner returns the single owning edge device for a template, or
// matched=false when the ownership is ambiguous (0 or >1 candidates).
func matchTemplateOwner(driverRegistry *drivers.Registry, tmpl *models.ConfigTemplate, devByChannel map[uint][]models.EdgeDevice) (ownerID uint, matched bool, err error) {
	var candidates []uint
	for _, devs := range devByChannel {
		for _, dev := range devs {
			if dev.NodeID != tmpl.NodeID {
				continue
			}
			drv, derr := driverRegistry.Get(dev.Type)
			if derr != nil {
				continue // 无 driver → 无法匹配
			}
			provider, ok := drv.(drivers.CommandTemplateProvider)
			if !ok {
				continue
			}
			if driverTemplateMatches(provider, tmpl.WriteData) {
				candidates = append(candidates, dev.ID)
			}
		}
	}
	switch len(candidates) {
	case 0:
		return 0, false, nil
	case 1:
		return candidates[0], true, nil
	default:
		// multi-drop 共享池: 多个设备 WriteData 相同, 无法可靠区分归属。
		return 0, false, nil
	}
}

// driverTemplateMatches reports whether any schedulable CommandTemplate of the
// driver has WriteData equal (case-normalized) to the template's WriteData.
func driverTemplateMatches(provider drivers.CommandTemplateProvider, writeData string) bool {
	want := strings.ToUpper(strings.TrimSpace(writeData))
	for _, cmd := range provider.GetCommandTemplates() {
		if strings.ToUpper(strings.TrimSpace(cmd.WriteData)) == want {
			return true
		}
	}
	return false
}
