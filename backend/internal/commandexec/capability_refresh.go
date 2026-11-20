package commandexec

import (
	"context"
	"errors"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/logger"
)

// CapabilityRefresher 让「能力快照过期」这件事可以自愈：向节点下发 QueryResources
// (0x1A)，节点回 ResourceReport，快照落库后 current_capabilities 门禁即可通过。
//
// 由 nodemgr.Manager 实现（SendQueryResources）。commandexec 只依赖接口，不 import
// nodemgr —— 否则命令域会把设备传输层拉进来（且 nodemgr 已 import commandexec 做
// ChannelCmdV2 收口，双向 import 会成环）。
//
// 为什么必须放在命令域（2026-11-20 生产缺陷 D5）：后端重启时 nodemgr 会在 Hello
// 里主动作废能力快照（boot_id / resource_reported_at / command_engine_revision 归零，
// 见 handler_hello.go），只有 ResourceReport 回来才恢复。自动化 planner 有自己的自愈
// （planner.refreshCapabilities），但**手动路径完全没有** —— 重启后用户在设备页点任何
// bounded 动作都会得到 `action unavailable`（真实原因 capability_stale），界面也没有
// 任何自愈入口。这里把「见 capability_stale 就刷一次再重试」补到手动确认与手动创建
// 两条路径上。
type CapabilityRefresher interface {
	SendQueryResources(deviceID string) (string, error)
}

// 自愈节奏。实测 QueryResources→ResourceReport 往返 < 600 ms（3.0 TCP 长连接，
// 节点收到 0x1A 立即回帧）；2 s 上限既覆盖 Wi-Fi 抖动，又不会让「连着却不回帧」
// 把一次 HTTP 请求挂到无法忍受。
const (
	defaultCapabilityRefreshTimeout = 2 * time.Second
	defaultCapabilityRefreshPoll    = 100 * time.Millisecond
)

// SetCapabilityRefresher 注入节点侧的资源快照刷新器（启动期一次性注入）。
func (s *Service) SetCapabilityRefresher(r CapabilityRefresher) { s.capabilityRefresher = r }

// errCapabilityStaleUnavailable 把「能力快照过期」包装成既有错误链：外层仍是
// ErrActionUnavailable（HTTP 409 与指标口径不变），内层挂 ErrCapabilityStale，
// 供 runWithCapabilityRefresh 识别「这一种失败可以自愈」。
func errCapabilityStaleUnavailable() error {
	return actionUnavailableError(gateResult{name: gateCurrentCapabilities, reasonCode: "capability_stale"})
}

// runWithCapabilityRefresh 执行一次命令域事务；只有当失败原因是**可自愈**的
// ErrCapabilityStale（能力快照缺失/过期）时才刷新一次快照并重试一次。
//
// 只重试一次是刻意的：刷新后仍然 stale 说明节点不回应 QueryResources（固件过旧/
// 链路半死），再重试只是把同一个失败多等一个超时；其余门禁失败（离线、manifest
// 未应用、动作超出能力）刷新一万次也没用，一律不重试。
//
// 重试是安全的：第一次事务在门禁处失败 ⇒ 整笔回滚 ⇒ 既没有 execution 行，也没有
// 消费掉一次确认令牌（令牌只在门禁全部通过后才 consume）。
func (s *Service) runWithCapabilityRefresh(ctx context.Context, edgeDeviceID uint, attempt func() error) error {
	err := attempt()
	if !errors.Is(err, ErrCapabilityStale) || s.capabilityRefresher == nil {
		return err
	}
	if !s.refreshNodeCapabilities(ctx, edgeDeviceID) {
		return err
	}
	return attempt()
}

// refreshNodeCapabilities 请求节点重发一次 ResourceReport，并等它真正落库。
// 返回 true 表示快照时间戳确实前进了（不是「发了就算」）。
func (s *Service) refreshNodeCapabilities(ctx context.Context, edgeDeviceID uint) bool {
	var edge models.EdgeDevice
	if err := s.db.WithContext(ctx).Select("id", "node_id").First(&edge, edgeDeviceID).Error; err != nil || edge.NodeID == "" {
		return false
	}
	var node models.Node
	if err := s.db.WithContext(ctx).Select("node_id", "status", "resource_reported_at").
		First(&node, "node_id = ?", edge.NodeID).Error; err != nil {
		return false
	}
	// 离线节点不会回应 QueryResources：别在这里白等满超时。
	if node.Status != "online" {
		return false
	}
	before := node.ResourceReportedAt
	requestID, err := s.capabilityRefresher.SendQueryResources(edge.NodeID)
	if err != nil {
		logger.Warnf("[commandexec] 请求节点资源快照失败 node=%s: %v", edge.NodeID, err)
		return false
	}
	deadline := time.Now().Add(s.capabilityRefreshTimeout)
	for {
		var fresh models.Node
		if err := s.db.WithContext(ctx).Select("node_id", "resource_reported_at").
			First(&fresh, "node_id = ?", edge.NodeID).Error; err == nil && fresh.ResourceReportedAt != nil &&
			(before == nil || fresh.ResourceReportedAt.After(*before)) {
			logger.Infof("[commandexec] 节点能力快照已刷新 node=%s request=%s", edge.NodeID, requestID)
			return true
		}
		if !time.Now().Before(deadline) {
			logger.Warnf("[commandexec] 等待节点资源快照超时 node=%s request=%s", edge.NodeID, requestID)
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(s.capabilityRefreshPoll):
		}
	}
}
