package datasource

// B2/B3 终审收口测试（契约终审 2026-10-22）：
//   - B2/I6：被规则引用的组禁止 enabled=false（ErrConflict => handler 409）；
//   - B3/N-7：成员集合/优先级变更后，同事务回写绑组规则的 legacy 锚点；
//   - B3：被引用组禁止删除最后一个成员（否则产生悬空锚点）。

import (
	"errors"
	"testing"

	"ehome/backend/internal/models"
)

// createGuardRuleForGroup 造一条指向组的 automation + alert 规则（最小必填列）。
func createGuardRuleForGroup(t *testing.T, f *groupFixture, gid, anchor uint, suffix string) (uint, uint) {
	t.Helper()
	kind := string(models.SourceKindSourceGroup)
	auto := &models.AutomationRule{
		Name:                "guard-auto-" + suffix,
		Enabled:             true,
		TriggerType:         models.AutomationTriggerSensorThreshold,
		TriggerSensorName:   "illuminance",
		TriggerComparator:   models.AlertComparatorGT,
		TriggerThreshold:    500,
		TriggerEdgeDeviceID: anchor,
		CooldownSec:         300,
		SourceKind:          &kind,
		SourceGroupID:       &gid,
	}
	if err := f.db.Create(auto).Error; err != nil {
		t.Fatalf("create automation rule: %v", err)
	}
	alert := &models.AlertRule{
		Name:          "guard-alert-" + suffix,
		TargetType:    models.AlertTargetEdgeDevice,
		TargetID:      anchor,
		SensorName:    "illuminance",
		Comparator:    models.AlertComparatorGT,
		Threshold:     500,
		Level:         models.AlertLevelInfo,
		Enabled:       true,
		SourceKind:    &kind,
		SourceGroupID: &gid,
	}
	if err := f.db.Create(alert).Error; err != nil {
		t.Fatalf("create alert rule: %v", err)
	}
	return auto.ID, alert.ID
}

// ① 组被规则引用时停用 ⇒ ErrConflict（handler 层 409）。
func TestUpdateGroupDisableReferencedConflict(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "b2-light-1")
	group := f.createPrimaryBackup(t, d1)
	createGuardRuleForGroup(t, f, group.ID, d1.ID, "disable")

	disabled := false
	if _, err := f.gsvc.UpdateGroup(group.ID, UpdateGroupInput{Enabled: &disabled}); !errors.Is(err, ErrConflict) {
		t.Fatalf("被规则引用时停用应为 ErrConflict，got %v", err)
	}
	// 反向：移除规则后可停用。
	if err := f.db.Where("source_group_id = ?", group.ID).Delete(&models.AutomationRule{}).Error; err != nil {
		t.Fatalf("delete automation rules: %v", err)
	}
	if err := f.db.Where("source_group_id = ?", group.ID).Delete(&models.AlertRule{}).Error; err != nil {
		t.Fatalf("delete alert rules: %v", err)
	}
	updated, err := f.gsvc.UpdateGroup(group.ID, UpdateGroupInput{Enabled: &disabled})
	if err != nil {
		t.Fatalf("移除规则后停用应成功，got %v", err)
	}
	if updated.Enabled {
		t.Fatalf("enabled 应为 false")
	}
}

// ② 成员优先级变更后：规则锚点必须跟着新锚点（priority DESC 首成员）走。
func TestUpdateGroupReconcileRuleAnchors(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "b3-light-1")
	d2 := f.addEdgeDevice(t, "b3-light-2")
	group := f.createPrimaryBackup(t, d1, d2) // d1 priority 100 > d2 90 ⇒ anchor=d1
	autoID, alertID := createGuardRuleForGroup(t, f, group.ID, d1.ID, "anchor")

	// 交换优先级：d2 变成最高优先（新锚点）。
	newMembers := []GroupMemberInput{
		{EdgeDeviceID: d1.ID, Priority: 10},
		{EdgeDeviceID: d2.ID, Priority: 200},
	}
	if _, err := f.gsvc.UpdateGroup(group.ID, UpdateGroupInput{Members: &newMembers}); err != nil {
		t.Fatalf("UpdateGroup members: %v", err)
	}
	var auto models.AutomationRule
	if err := f.db.First(&auto, autoID).Error; err != nil {
		t.Fatalf("load automation rule: %v", err)
	}
	if auto.TriggerEdgeDeviceID != d2.ID {
		t.Fatalf("automation anchor 未随成员优先级重算：got %d want %d（回滚旧二进制会盯错设备）", auto.TriggerEdgeDeviceID, d2.ID)
	}
	var alert models.AlertRule
	if err := f.db.First(&alert, alertID).Error; err != nil {
		t.Fatalf("load alert rule: %v", err)
	}
	if alert.TargetType != models.AlertTargetEdgeDevice || alert.TargetID != d2.ID {
		t.Fatalf("alert anchor 未随成员优先级重算：target_type=%q target_id=%d want edge_device/%d",
			alert.TargetType, alert.TargetID, d2.ID)
	}
}

// ③ 被规则引用时删除最后一个成员 ⇒ ErrConflict（不留"有规则引用但无成员"的悬空锚点）。
func TestUpdateGroupRemoveLastMemberReferencedConflict(t *testing.T) {
	f := newGroupFixture(t)
	d1 := f.addEdgeDevice(t, "b3-light-solo")
	group := f.createPrimaryBackup(t, d1)
	createGuardRuleForGroup(t, f, group.ID, d1.ID, "last")

	empty := []GroupMemberInput{}
	if _, err := f.gsvc.UpdateGroup(group.ID, UpdateGroupInput{Members: &empty}); !errors.Is(err, ErrConflict) {
		t.Fatalf("被引用组删除最后一个成员应为 ErrConflict，got %v", err)
	}
	// 反向：移除规则后允许清空成员。
	if err := f.db.Where("source_group_id = ?", group.ID).Delete(&models.AutomationRule{}).Error; err != nil {
		t.Fatalf("delete automation rules: %v", err)
	}
	if err := f.db.Where("source_group_id = ?", group.ID).Delete(&models.AlertRule{}).Error; err != nil {
		t.Fatalf("delete alert rules: %v", err)
	}
	if _, err := f.gsvc.UpdateGroup(group.ID, UpdateGroupInput{Members: &empty}); err != nil {
		t.Fatalf("移除规则后清空成员应成功，got %v", err)
	}
}
