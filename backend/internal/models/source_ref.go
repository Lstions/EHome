package models

import "fmt"

// ── 规则「数据来源」引用（方案 §4.3 / N-6）──
//
// 背景：规则原先只有一根裸设备 ID（automation_rules.trigger_edge_device_id /
// alert_rules.target_type+target_id），无法表达「用支持主备切换的数据源」或
// 「1+2 合成数据源」。本文件是**回落判据的唯一实现** —— evaluator / planner /
// precheck / 前端校验 / 盘点脚本全部调用它，禁止各自判断 source_kind != ""。
//
// 三态（N-6）：两列均可空（*string/*uint），NULL = 存量老路。
// Go 侧**必须用指针**：string 无法承载 SQL NULL，用零值判等会让「未设置」与
// 「设置成空串」不可区分，并把非法态静默回落成「任意设备触发」。

// SourceKind 规则来源种类。
type SourceKind string

const (
	// SourceKindEdgeDevice 直接用某台边缘设备（存量语义，行为不变）。
	SourceKindEdgeDevice SourceKind = "edge_device"
	// SourceKindSourceGroup 用来源组（主备 / 复合）。
	SourceKindSourceGroup SourceKind = "source_group"
)

// SourceRef 是解析后的来源引用。
//
// EdgeDeviceID 只在 Kind==edge_device 时有意义；GroupID 只在 Kind==source_group 时有意义。
type SourceRef struct {
	Kind         SourceKind
	EdgeDeviceID uint
	GroupID      uint
}

// IsGroup 报告该引用是否指向来源组。
func (r SourceRef) IsGroup() bool {
	return r.Kind == SourceKindSourceGroup && r.GroupID != 0
}

// IsZero 报告该引用是否完全未设置（未接线时的兜底）。
func (r SourceRef) IsZero() bool {
	return r.Kind == "" && r.EdgeDeviceID == 0 && r.GroupID == 0
}

// SourceRefError 是来源绑定校验失败的类型化错误（handler 映射为 400 并带 code）。
type SourceRefError struct {
	Code string
	Msg  string
}

func (e *SourceRefError) Error() string { return e.Msg }

// 校验失败码。
const (
	SourceRefCodeKindOrphan   = "source_kind_orphan"   // kind 为空但 group_id 非空
	SourceRefCodeKindInvalid  = "source_kind_invalid"  // kind 不在白名单
	SourceRefCodeGroupMissing = "source_group_missing" // kind=source_group 但 group_id 为空/0
)

// ValidateSourceRefColumns 校验规则上的两列绑定是否合法（五态判据，方案 N-6）。
//
// 合法态：
//
//	① kind=nil, group=nil          → 存量老路（edge_device）
//	② kind=edge_device, group=nil → 显式单设备
//	③ kind=source_group, group>0  → 来源组
//
// 非法态（一律 400，**不得静默回落**）：
//
//	④ kind=nil/空 但 group 非空    → 孤儿 group_id
//	⑤ kind 不在白名单              → 拼写/前端漂移
//	⑥ kind=source_group 但 group 为空/0
func ValidateSourceRefColumns(kind *string, groupID *uint) error {
	if kind == nil || *kind == "" {
		if groupID != nil && *groupID != 0 {
			return &SourceRefError{Code: SourceRefCodeKindOrphan,
				Msg: "source_group_id 已设置但 source_kind 为空"}
		}
		return nil
	}
	switch SourceKind(*kind) {
	case SourceKindEdgeDevice:
		if groupID != nil && *groupID != 0 {
			return &SourceRefError{Code: SourceRefCodeKindInvalid,
				Msg: "source_kind=edge_device 时不得设置 source_group_id"}
		}
		return nil
	case SourceKindSourceGroup:
		if groupID == nil || *groupID == 0 {
			return &SourceRefError{Code: SourceRefCodeGroupMissing,
				Msg: "source_kind=source_group 时必须设置 source_group_id"}
		}
		return nil
	default:
		return &SourceRefError{Code: SourceRefCodeKindInvalid,
			Msg: fmt.Sprintf("source_kind 非法: %q（允许 edge_device|source_group）", *kind)}
	}
}


// InvalidSourceKind 报告规则加载期是否需要告警"非法 source_kind"（方案 N-6 / 架构终审 H-3）。
//
// 语义（Lead 2026-10-22 裁决）：
//   - kind 为 nil / 空串 ⇒ false（存量老路，合法）；
//   - kind ∈ {edge_device, source_group} ⇒ false；
//   - 其它任何值（脏数据、手工 SQL、未来版本回退遗留）⇒ true。
//
// **调用方只做 Warn + 指标，不跳过规则、不改求值行为**：求值继续走 SourceRef() 的
// 保守回落（edge_device + TriggerEdgeDeviceID）。为什么不是 fail-closed 跳过：
// 「规则静默不生效」与「静默乱触发」同属静默失效，前者更难被发现；保守回落 + 可见告警
// 是唯一能同时满足"行为可预测"与"问题可见"的选择（source_group_guard 的保存期 400 已挡住新写入）。
//
// 唯一实现纪律：automation/alert 的 LoadRules 必须调用本函数，禁止各自判断
// source_kind 字符串（与 SourceRef()/ValidateSourceRefColumns 同源，防规则漂移）。
func InvalidSourceKind(kind *string) bool {
	if kind == nil || *kind == "" {
		return false
	}
	switch SourceKind(*kind) {
	case SourceKindEdgeDevice, SourceKindSourceGroup:
		return false
	default:
		return true
	}
}

// SourceRef 解析自动化规则的来源引用（唯一实现）。
func (r AutomationRule) SourceRef() SourceRef {
	if r.SourceKind != nil && *r.SourceKind == string(SourceKindSourceGroup) &&
		r.SourceGroupID != nil && *r.SourceGroupID != 0 {
		return SourceRef{Kind: SourceKindSourceGroup, GroupID: *r.SourceGroupID}
	}
	return SourceRef{Kind: SourceKindEdgeDevice, EdgeDeviceID: r.TriggerEdgeDeviceID}
}

// SourceRef 解析告警规则的来源引用（唯一实现）。
//
// 注意：告警存量用 target_type/target_id，其中 target_type=logical_device 是
// 「逻辑设备下任一成员驱动」，与主备语义不同 —— 本函数只负责分派，不改存量语义。
func (r AlertRule) SourceRef() SourceRef {
	if r.SourceKind != nil && *r.SourceKind == string(SourceKindSourceGroup) &&
		r.SourceGroupID != nil && *r.SourceGroupID != 0 {
		return SourceRef{Kind: SourceKindSourceGroup, GroupID: *r.SourceGroupID}
	}
	return SourceRef{Kind: SourceKindEdgeDevice, EdgeDeviceID: r.TargetID}
}
