package sensorfield

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// 解析层九指标（方案 §10）。这里沿用 pkg/metrics 的 promauto 范式，
// 但不 import pkg/metrics：领域包只依赖 stdlib + pkg/logger + pkg/metrics 的
// “范式”而非其变量表；两者都注册到默认 registry。
//
// ⚠ 指标名全局唯一：其它包（api/automation/alert/main）**禁止**再定义同名
// promauto 指标，否则进程 init 阶段会因重复注册 panic。
// ehome_rule_never_resolved / ehome_rule_never_fired 由规则层（automation/alert）
// 在统计刷新后 Set，域包不维护“规则”概念（红线 5）。
var (
	// FieldResolveTotal 解析结果分布（result=resolved|unbound|error）。
	FieldResolveTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_field_resolve_total",
		Help: "Field selector resolution outcomes by result",
	}, []string{"result"})

	// FieldPartialFrameTotal “已知集合有、本帧缺”的次数（§5.3 定义 2）。
	FieldPartialFrameTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_field_partial_frame_total",
		Help: "Frames where a known family member was missing (partial frame)",
	}, []string{"family"})

	// FieldAliasHitTotal 别名命中（first=false 表示非首候选）。
	FieldAliasHitTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_field_alias_hit_total",
		Help: "Semantic alias resolutions by semantic/physical candidate",
	}, []string{"semantic", "physical", "first"})

	// FieldAliasAmbiguousTotal 多候选同时存在。
	FieldAliasAmbiguousTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_field_alias_ambiguous_total",
		Help: "Semantic alias resolutions where multiple candidates were known",
	}, []string{"semantic"})

	// FieldVirtualUndefinedTotal 派生未定义（nan|inf|div0|missing|...）。
	FieldVirtualUndefinedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_field_virtual_undefined_total",
		Help: "Virtual field evaluations that produced unbound by reason",
	}, []string{"name", "reason"})

	// FieldAliasDeadCandidate 死候选标记（Warnings() 置 1；B 级可观测性，不阻塞启动）。
	FieldAliasDeadCandidate = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ehome_field_alias_dead_candidate",
		Help: "Alias candidates not produced by any declaration/observation (set by Warnings)",
	}, []string{"semantic", "candidate"})

	// FieldObservedTotal 观测计数（按设备类型；Flush 批次内按字段条数累加）。
	FieldObservedTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "ehome_field_observed_total",
		Help: "Observed field count flushed by device type",
	}, []string{"device_type"})

	// FieldSnapshotAgeSeconds 快照年龄（Flush 时刷新为“刷新前快照的年龄”）。
	FieldSnapshotAgeSeconds = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "ehome_field_snapshot_age_seconds",
		Help: "Age in seconds of the registry snapshot at the last flush",
	})

	// RuleNeverResolved 从未成功解析的规则数（由规则层 Set；kind=automation|alert）。
	RuleNeverResolved = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ehome_rule_never_resolved",
		Help: "Rules that never resolved their selector (set by rule layers)",
	}, []string{"kind"})

	// RuleNeverFired 解析成功但从未触发的规则数（由规则层 Set；kind=automation|alert）。
	RuleNeverFired = promauto.NewGaugeVec(prometheus.GaugeOpts{
		Name: "ehome_rule_never_fired",
		Help: "Rules that resolved but never fired (set by rule layers)",
	}, []string{"kind"})
)

// RuleKinds 是 ehome_rule_* 指标 kind label 的合法值集合（有界）。
var RuleKinds = []string{"automation", "alert"}
