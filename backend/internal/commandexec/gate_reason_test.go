package commandexec

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"ehome/backend/internal/models"
)

// TestGateRejectionCarriesGateName 钉死 2026-11-18 的观测性修复：
//
// 门禁拒绝必须把**哪一道门禁**带进错误链。旧实现只 `return ErrActionUnavailable`，
// 12 道门禁的差异在自动化事件 detail 里塌缩成同一句
// "action is unavailable for this device" —— 生产真机事故（capability_stale 被
// 当成"节点离线"排查了一早上）正是这么来的。
func TestGateRejectionCarriesGateName(t *testing.T) {
	s, edge := setupService(t)
	stale := time.Now().UTC().Add(-2 * MaxCapabilityAge)
	if err := s.db.Model(&models.Node{}).Where("node_id = ?", edge.NodeID).
		Update("resource_reported_at", stale).Error; err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Create(context.Background(), createInput(edge.ID, "request-gate-reason-0001"))
	if !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("err=%v, 必须仍命中 ErrActionUnavailable（既有分类与指标口径依赖它）", err)
	}
	if !errors.Is(err, ErrCapabilityStale) {
		t.Fatalf("err=%v, 必须命中 ErrCapabilityStale（自动化自愈路径的唯一判据）", err)
	}
	if !strings.Contains(err.Error(), "gate=current_capabilities") {
		t.Fatalf("err=%q 未携带门禁名 —— 事件 detail 仍是不可诊断的通稿", err.Error())
	}
	// reason 码必须与 Catalog 对齐（operator 习惯按 capability_stale 检索）。
	if !strings.Contains(err.Error(), "reason=capability_stale") {
		t.Fatalf("err=%q, 能力快照过期的 reason 必须是 capability_stale", err.Error())
	}
}

// TestUnsupportedCapabilityIsNotClassifiedAsStale 钉住哨兵的**适用范围**：
//
// 只有"快照缺失/过期"是可自愈的（要一次资源上报就好）；"节点不支持 ChannelCmdV2"
// 刷新一万次也不会好。若把它也挂 ErrCapabilityStale，未来任何"见哨兵就刷新重试"的
// 消费者都会在不可自愈场景里空转。
func TestUnsupportedCapabilityIsNotClassifiedAsStale(t *testing.T) {
	s, edge := setupService(t)
	if err := s.db.Model(&models.Node{}).Where("node_id = ?", edge.NodeID).
		Update("command_engine_capabilities", `{"supports_channel_cmd_v2":false}`).Error; err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Create(context.Background(), createInput(edge.ID, "request-unsupported-0001"))
	if !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if errors.Is(err, ErrCapabilityStale) {
		t.Fatalf("\"不支持 ChannelCmdV2\" 不得挂 ErrCapabilityStale: %v", err)
	}
	if !strings.Contains(err.Error(), "reason=capability_unavailable") {
		t.Fatalf("err=%q, 不可自愈的能力失败必须与 stale 分列", err.Error())
	}
}

// TestNonCapabilityGateIsNotClassifiedAsStale：别的门禁同样带名字，且**不得**被
// 误判为 capability_stale —— 否则自动化会为了一个"节点离线"白白多发一帧
// QueryResources，还把真正的失败原因盖掉。
func TestNonCapabilityGateIsNotClassifiedAsStale(t *testing.T) {
	s, edge := setupService(t)
	if err := s.db.Model(&models.Node{}).Where("node_id = ?", edge.NodeID).
		Update("status", "offline").Error; err != nil {
		t.Fatal(err)
	}
	_, _, err := s.Create(context.Background(), createInput(edge.ID, "request-gate-reason-0002"))
	if !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("err=%v", err)
	}
	if errors.Is(err, ErrCapabilityStale) {
		t.Fatalf("node_offline 被误判为 capability_stale: %v", err)
	}
	if !strings.Contains(err.Error(), "gate=node_status") {
		t.Fatalf("err=%q 未携带门禁名", err.Error())
	}
}
