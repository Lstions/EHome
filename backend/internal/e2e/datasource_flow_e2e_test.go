//go:build e2e

package e2e

import (
	"testing"
	"time"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"
)

// TestEdgeDeviceDataFlow_DatasourceThenRule 覆盖用户要求的完整业务闭环：
//
//	边缘设备上报一帧 → 数据源域（健康/失败计数/恢复） → 规则引擎（同一帧触发）
//
// 断言的是"同一帧同时驱动两个下游"，而不是两个独立单元测试——这正是接线类缺陷
// （sink 未注入、MarkSuccess 未接）唯一能暴露的地方。
func TestEdgeDeviceDataFlow_DatasourceThenRule(t *testing.T) {
	rule := &models.AutomationRule{
		Name: "SOC 低电量通知", Enabled: true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerEdgeDeviceID: 0,
		TriggerSensorName:   "@soc", TriggerComparator: "lt", TriggerThreshold: 30,
		ActionType: models.AutomationActionNotification,
	}
	c := newE2EChain(t, rule)

	frame := bmsSample{
		totalVoltageDeciV: 5120, currentCentiA: 1000,
		remainingCentiAh: 1100, nominalCentiAh: 5000, cycles: 42,
		rsoc: 27, cellCount: 4, temps: []int16{2981, 2992},
	}

	// ── ① 成功上报：数据源健康 + 规则触发，同一帧 ──
	c.publish(t, frame.frame(), 1)

	var src models.DataSource
	if err := c.db.First(&src, c.dataSource.ID).Error; err != nil {
		t.Fatalf("reload data source: %v", err)
	}
	if src.LastSuccess == nil {
		t.Error("data_sources.last_success 未更新 —— MarkSuccess 未接线")
	}
	if src.FailCount != 0 || src.Status != datasource.StatusActive {
		t.Errorf("成功上报后 source = status:%s fail:%d, want active/0", src.Status, src.FailCount)
	}

	if n := c.countByResult(t, rule.ID, models.AutomationResultNotification); n != 1 {
		t.Errorf("同一帧应触发规则 1 次，实际=%d（数据源与规则引擎必须共享同一份解析结果）", n)
	}

	// ── ② 连续失败 3 次：达到 max_fail_count ⇒ error + 健康表 3 行 ──
	for i := 0; i < 3; i++ {
		c.dsSvc.MarkFailure(c.device.ID, datasource.TriggerDeviceOffline)
	}
	if err := c.db.First(&src, c.dataSource.ID).Error; err != nil {
		t.Fatalf("reload data source: %v", err)
	}
	if src.Status != datasource.StatusError {
		t.Errorf("连续 3 次失败后 status=%s, want error", src.Status)
	}
	if src.FailCount < 3 {
		t.Errorf("fail_count=%d, want >=3", src.FailCount)
	}
	var healthRows []models.DataSourceHealth
	if err := c.db.Where("source_id = ?", c.dataSource.ID).Find(&healthRows).Error; err != nil {
		t.Fatalf("query health: %v", err)
	}
	if len(healthRows) != 3 {
		t.Errorf("data_source_health rows=%d, want 3（每次失败落一条）", len(healthRows))
	}
	for _, h := range healthRows {
		if h.Category != "rsoc" {
			t.Errorf("health category=%q, want rsoc（同域同宽）", h.Category)
		}
	}
	var failovers int64
	if err := c.db.Model(&models.FailoverLog{}).Count(&failovers).Error; err != nil {
		t.Fatalf("count failover logs: %v", err)
	}
	if failovers != 0 {
		t.Errorf("单来源不应产生切换日志，实际=%d", failovers)
	}

	// ── ③ 恢复上报：error ⇒ standby，失败计数归零 ──
	c.publish(t, frame.frame(), 2)
	if err := c.db.First(&src, c.dataSource.ID).Error; err != nil {
		t.Fatalf("reload data source: %v", err)
	}
	if src.Status != datasource.StatusStandby {
		t.Errorf("恢复上报后 status=%s, want standby（error 不自动抢占 active）", src.Status)
	}
	if src.FailCount != 0 {
		t.Errorf("恢复上报后 fail_count=%d, want 0", src.FailCount)
	}
	// 恢复后的成功时间必须晚于之前的失败时间（时间线单调）
	if src.LastSuccess == nil || src.LastFailure == nil {
		t.Error("last_success/last_failure 应同时存在（先失败后成功）")
	} else if src.LastSuccess.Before(*src.LastFailure) {
		t.Errorf("last_success(%v) 早于 last_failure(%v)，时间线异常", src.LastSuccess, src.LastFailure)
	}

	// ── ④ 数据仍应持续入库（失败/恢复不应影响数据面） ──
	var rows int64
	if err := c.db.Model(&models.UnifiedData{}).Where("device_id = ?", c.device.ID).Count(&rows).Error; err != nil {
		t.Fatalf("count unified_data: %v", err)
	}
	if rows != 24 { // 2 帧 × 12 字段
		t.Errorf("unified_data rows=%d, want 24（2 帧 × 12 字段）", rows)
	}
	_ = time.Now
}
