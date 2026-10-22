package models_test

import (
	"errors"
	"testing"

	"ehome/backend/internal/models"
)

func ptrStr(s string) *string { return &s }
func ptrUint(v uint) *uint    { return &v }

// TestValidateSourceRefColumns_FiveStates 钉住「双指针五态」判据（方案 N-6）。
//
// 为什么这条必须有：v0.1 设计用非指针 string 判等，Go 无法承载 SQL NULL；
// 实现者只能二选一（NOT NULL DEFAULT ” 让 NULL 口径失效 / 改指针但忘了校验），
// 而「非法态静默回落成任意设备触发」是静默误触发级别的事故。
func TestValidateSourceRefColumns_FiveStates(t *testing.T) {
	cases := []struct {
		name     string
		kind     *string
		groupID  *uint
		wantErr  bool
		wantCode string
	}{
		{"① both nil = legacy", nil, nil, false, ""},
		{"② empty string = legacy", ptrStr(""), nil, false, ""},
		{"③ edge_device explicit", ptrStr("edge_device"), nil, false, ""},
		{"④ source_group with id", ptrStr("source_group"), ptrUint(7), false, ""},
		{"⑤ orphan group_id", nil, ptrUint(7), true, models.SourceRefCodeKindOrphan},
		{"⑥ empty kind + group_id", ptrStr(""), ptrUint(7), true, models.SourceRefCodeKindOrphan},
		{"⑦ unknown kind", ptrStr("dataset"), nil, true, models.SourceRefCodeKindInvalid},
		{"⑧ edge_device + group_id", ptrStr("edge_device"), ptrUint(7), true, models.SourceRefCodeKindInvalid},
		{"⑨ source_group without id", ptrStr("source_group"), nil, true, models.SourceRefCodeGroupMissing},
		{"⑩ source_group with zero id", ptrStr("source_group"), ptrUint(0), true, models.SourceRefCodeGroupMissing},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := models.ValidateSourceRefColumns(tc.kind, tc.groupID)
			if tc.wantErr != (err != nil) {
				t.Fatalf("wantErr=%v got %v", tc.wantErr, err)
			}
			if tc.wantErr {
				var refErr *models.SourceRefError
				if !errors.As(err, &refErr) {
					t.Fatalf("want *SourceRefError, got %T: %v", err, err)
				}
				if refErr.Code != tc.wantCode {
					t.Fatalf("code = %q want %q", refErr.Code, tc.wantCode)
				}
			}
		})
	}
}

// TestAutomationRuleSourceRef 钉住回落判据的唯一实现。
func TestAutomationRuleSourceRef(t *testing.T) {
	// 存量：两列 NULL ⇒ 单设备，EdgeDeviceID 取老列。
	legacy := models.AutomationRule{TriggerEdgeDeviceID: 11}
	if ref := legacy.SourceRef(); ref.Kind != models.SourceKindEdgeDevice || ref.EdgeDeviceID != 11 || ref.GroupID != 0 {
		t.Fatalf("legacy ref = %+v", ref)
	}

	// 绑组：Kind 正确 + GroupID 生效。
	grouped := models.AutomationRule{TriggerEdgeDeviceID: 11, SourceKind: ptrStr("source_group"), SourceGroupID: ptrUint(5)}
	ref := grouped.SourceRef()
	if ref.Kind != models.SourceKindSourceGroup || ref.GroupID != 5 {
		t.Fatalf("group ref = %+v", ref)
	}
	if !ref.IsGroup() {
		t.Fatal("IsGroup should be true")
	}

	// 半残：kind=source_group 但 id=0/缺 ⇒ 必须回落单设备而不是「任意设备」。
	broken := models.AutomationRule{TriggerEdgeDeviceID: 11, SourceKind: ptrStr("source_group")}
	if ref := broken.SourceRef(); ref.Kind != models.SourceKindEdgeDevice || ref.EdgeDeviceID != 11 {
		t.Fatalf("broken ref = %+v (must fall back to edge device, not group/any)", ref)
	}
}

// TestAlertRuleSourceRef 告警侧同款回落。
func TestAlertRuleSourceRef(t *testing.T) {
	legacy := models.AlertRule{TargetType: models.AlertTargetEdgeDevice, TargetID: 9}
	if ref := legacy.SourceRef(); ref.Kind != models.SourceKindEdgeDevice || ref.EdgeDeviceID != 9 {
		t.Fatalf("legacy = %+v", ref)
	}
	grouped := models.AlertRule{TargetType: models.AlertTargetEdgeDevice, TargetID: 9,
		SourceKind: ptrStr("source_group"), SourceGroupID: ptrUint(3)}
	if ref := grouped.SourceRef(); !ref.IsGroup() || ref.GroupID != 3 {
		t.Fatalf("group = %+v", ref)
	}
}
