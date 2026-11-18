package automation

import (
	"fmt"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
)

// MigrateTimeWindowTimezone 为存量 time_window 规则补齐时区（2026-11-18 D7）。
//
// 为什么必须有：改造前 time_window 没有时区语义，执行时按**进程时区**解释
//（生产容器 = UTC）。改造后按规则时区解释，存量行若留空会被 fail-closed 跳过 ——
// 那是"升级即静默停摆"。所以启动时按**旧口径**（进程时区）回填一次，把行为
// 原地保留，再由用户按需改成真实时区。
//
// 幂等：只填 NULL / 空串；已有值一律不动（用户选择优先）。
// 迁移失败必须让启动失败（在 main.go 里 abortStartup）：否则规则会静默不执行，
// 而这正是本次事故的形态。
func MigrateTimeWindowTimezone(db *gorm.DB) error {
	zone := localZoneName()
	if zone == "" {
		// 进程时区取不到 IANA 名时用 UTC —— 与旧行为（now.Location() 的
		// 零值时区/UTC 容器）一致，而不是编一个看似合理的本地时区。
		zone = "UTC"
	}
	res := db.Model(&models.AutomationRule{}).
		Where("trigger_type = ? AND (timezone IS NULL OR TRIM(timezone) = '')",
			models.AutomationTriggerTimeWindow).
		Update("timezone", zone)
	if res.Error != nil {
		return fmt.Errorf("backfill time_window timezone: %w", res.Error)
	}
	if res.RowsAffected > 0 {
		logger.Warnf("automation: %d 条 time_window 规则缺少时区，已按旧口径回填 timezone=%s（请按需改为真实时区）",
			res.RowsAffected, zone)
	}
	return nil
}
