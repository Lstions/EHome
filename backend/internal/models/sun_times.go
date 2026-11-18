package models

import "time"

// SunTimePoint 是单个日出/日落事件的可读结果（只读预览，不落库）。
//
// At 在 Status != StatusOK 时为 nil —— 极昼/极夜没有该事件，**绝不能用零值
// 时间冒充**（与 sun.SunEvent 同约定）。Status 取值：ok / always_up / always_down。
type SunTimePoint struct {
	At     *time.Time `json:"at,omitempty"`
	Status string     `json:"status"`
}

// SunTimesPreview 是规则表单「按当前位置今天的日出/日落」预览（P3，2026-11-18）。
//
// 为什么由后端算而不是前端算：时区归日、极昼/极夜、偏移跨本地午夜全部是 sun 包
// 与求值器的既有判据；前端复刻一份必然与执行侧漂移 —— 这正是 next_run 当初放到
// 后端的同一条理由。本结构只用于只读展示，不落库。
type SunTimesPreview struct {
	Timezone string       `json:"timezone"`
	Date     string       `json:"date"` // 规则时区下的日期 "2006-01-02"
	Sunrise  SunTimePoint `json:"sunrise"`
	Sunset   SunTimePoint `json:"sunset"`
	// Selected 只在请求带 kind 时出现：把「这条规则会怎么触发」一并算好。
	Selected *SunSelection `json:"selected,omitempty"`
}

// SunSelection 是某条 sun_event 规则在当前坐标/偏移下的触发预览。
type SunSelection struct {
	Kind      string     `json:"kind"`       // sunrise|sunset
	OffsetMin int        `json:"offset_min"` // 负=提前，正=延后
	EventAt   *time.Time `json:"event_at,omitempty"`
	FireAt    *time.Time `json:"fire_at,omitempty"`
	// CrossesMidnight = fireAt 与 eventAt 不在同一规则本地日（偏移跨午夜）。
	// 旧实现这类规则永远不触发；修复后由候选集处理，但前端必须提示用户。
	CrossesMidnight bool   `json:"crosses_midnight"`
	Status          string `json:"status"` // ok / always_up / always_down
}
