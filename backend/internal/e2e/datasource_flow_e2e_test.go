//go:build e2e

package e2e

import (
	"testing"

	"ehome/backend/internal/datasource"
	"ehome/backend/internal/models"
)

// TestEdgeDeviceDataFlow_DatasourceThenRule 覆盖「同一帧驱动多个下游」的闭环：
//
//	边缘设备上报一帧 → 解析 → 数据面入库 + 规则引擎触发
//	                 → 数据源台账（自动投影，只读）
//
// 断言的是"同一帧同时驱动多个下游"，而不是两个独立单元测试 —— 这正是接线类缺陷
// （sink 未注入、台账未派生）唯一能暴露的地方。
//
// # M3 后的语义变更（2026-10-23）
//
// 本测试原先还断言 data_sources 的**主备状态机**行为：
//
//	"连续 3 次失败 ⇒ status=error ⇒ 自动切换 ⇒ 恢复后 error→standby"
//
// 该状态机已被删除（用户要求「下线旧主备状态机，不需要保留债务」；主备语义
// 只由来源组承担）。data_sources 现在是**自动派生的只读台账**：
//   - status 由投影器按来源组快照写入（唯一写者），不再是"失败计数驱动"；
//   - 没有自动切换、没有 failover 日志；
//   - last_success/fail_count 仍由 health 观测更新（纯事实记录）。
//
// 因此本测试改为断言「台账与数据流共存且互不干扰」，而主备切换的端到端
// 验证在 internal/e2e/source_group_fullchain_*.go（TestE1/E2/E7/E10/E11）。
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

	// ── ① 成功上报：规则触发恰好 1 次（同一帧共享同一份解析结果）──
	c.publish(t, frame.frame(), 1)

	if n := c.countByResult(t, rule.ID, models.AutomationResultNotification); n != 1 {
		t.Errorf("同一帧应触发规则 1 次，实际=%d（数据源与规则引擎必须共享同一份解析结果）", n)
	}

	// ── ② 数据面正常入库（解析链路未被台账逻辑干扰）──
	var rows int64
	if err := c.db.Model(&models.UnifiedData{}).Where("device_id = ?", c.device.ID).Count(&rows).Error; err != nil {
		t.Fatalf("count unified_data: %v", err)
	}
	if rows == 0 {
		t.Fatal("unified_data 无行 —— 解析/入库链路断了")
	}

	// ── ③ 台账行存在且状态由投影器维护（不是状态机）──
	var src models.DataSource
	if err := c.db.First(&src, c.dataSource.ID).Error; err != nil {
		t.Fatalf("reload data source: %v", err)
	}
	// status 必须是投影器认可的取值集合之一；M3 后它**不再**因失败计数变成 error。
	switch src.Status {
	case datasource.StatusActive, datasource.StatusStandby, datasource.StatusDisabled:
		// 合法（由投影器按来源组快照/成员关系决定）
	default:
		t.Errorf("台账 status=%q 不在投影器允许的集合内", src.Status)
	}
	// 记录来源必须是 auto（本链路里台账由投影器派生）或 manual（人工创建）
	switch src.ManagedBy {
	case models.DataSourceManagedAuto, models.DataSourceManagedManual:
	default:
		t.Errorf("managed_by=%q 非法", src.ManagedBy)
	}

	// ── ④ 再上报一帧：数据继续入库（数据面不被阻塞）──
	c.publish(t, frame.frame(), 2)
	var rows2 int64
	if err := c.db.Model(&models.UnifiedData{}).Where("device_id = ?", c.device.ID).Count(&rows2).Error; err != nil {
		t.Fatalf("count unified_data after 2nd frame: %v", err)
	}
	if rows2 <= rows {
		t.Errorf("第二帧后行数未增长（%d → %d）—— 数据面被阻塞", rows, rows2)
	}

	// ── ⑤ 确认没有产生 failover 日志（该能力已下线，不应有残余写入）──
	var failovers int64
	if err := c.db.Model(&models.FailoverLog{}).Count(&failovers).Error; err != nil {
		t.Fatalf("count failover logs: %v", err)
	}
	if failovers != 0 {
		t.Errorf("单来源不应产生切换日志，实际=%d（legacy 状态机已下线，不应有写入）", failovers)
	}
}
