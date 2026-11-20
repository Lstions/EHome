package commandexec

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"

	"gorm.io/gorm"
)

// 本文件钉住 2026-11-20 生产缺陷 D5：后端重启时 nodemgr 在 Hello 里主动作废节点
// 能力快照（boot_id / resource_reported_at / command_engine_revision 归零），只有
// ResourceReport 回来才恢复。自动化 planner 有自己的自愈，手动路径（确认 + 创建）
// 以前没有 —— 用户点任何 bounded 动作都得到 `action unavailable`，且没有界面入口。
//
// 三条不变量：
//  1. capability_stale ⇒ 刷一次快照 + 重试一次，最终**成功**（不是多等一个超时后失败）；
//  2. 刷不回来（节点不回帧）⇒ 仍然是一句可诊断的 action unavailable，且只试一次；
//  3. 未接线刷新器（单测/仿真）⇒ 行为与改造前完全一致，不 panic、不空转。

const freshCapabilitiesJSON = `{"supports_channel_cmd_v2":true,"supports_finally":true,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`

type fakeCapabilityRefresher struct {
	mu     sync.Mutex
	calls  []string
	onCall func(deviceID string)
	err    error
}

func (f *fakeCapabilityRefresher) SendQueryResources(deviceID string) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, deviceID)
	cb, err := f.onCall, f.err
	f.mu.Unlock()
	if err != nil {
		return "", err
	}
	if cb != nil {
		cb(deviceID)
	}
	return "req-test", nil
}

func (f *fakeCapabilityRefresher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// makeCapabilityStale 复刻 handler_hello.go 的作废动作。
func makeCapabilityStale(t *testing.T, db *gorm.DB, nodeID string) {
	t.Helper()
	if err := db.Model(&models.Node{}).Where("node_id = ?", nodeID).Updates(map[string]interface{}{
		"boot_id": "", "resource_reported_at": nil, "command_engine_revision": 0,
		"command_engine_capabilities": "{}",
	}).Error; err != nil {
		t.Fatal(err)
	}
}

// reportCapabilities 复刻 ResourceReport 落库（时间戳前进 + revision 恢复）。
func reportCapabilities(t *testing.T, db *gorm.DB, nodeID string) {
	t.Helper()
	now := time.Now().UTC()
	if err := db.Model(&models.Node{}).Where("node_id = ?", nodeID).Updates(map[string]interface{}{
		"boot_id": "boot-refreshed", "resource_reported_at": &now, "command_engine_revision": 2,
		"command_engine_capabilities": freshCapabilitiesJSON,
	}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestCreateSelfHealsStaleCapabilityReport(t *testing.T) {
	service, edge := setupService(t)
	makeCapabilityStale(t, service.db, edge.NodeID)

	refresher := &fakeCapabilityRefresher{onCall: func(string) { reportCapabilities(t, service.db, edge.NodeID) }}
	service.SetCapabilityRefresher(refresher)

	execution, replayed, err := service.Create(context.Background(), createInput(edge.ID, "stale-heal-0001"))
	if err != nil {
		t.Fatalf("Create 未自愈: %v", err)
	}
	if replayed || execution == nil || execution.CommandID == "" {
		t.Fatalf("execution = %+v replayed=%v", execution, replayed)
	}
	if refresher.count() != 1 {
		t.Fatalf("SendQueryResources 次数 = %d, want 1（只刷一次）", refresher.count())
	}
}


func TestCreateStaleRefreshIsBoundedToOneAttempt(t *testing.T) {
	service, edge := setupService(t)
	service.capabilityRefreshTimeout = 120 * time.Millisecond
	service.capabilityRefreshPoll = 20 * time.Millisecond
	makeCapabilityStale(t, service.db, edge.NodeID)

	refresher := &fakeCapabilityRefresher{} // 节点不回帧
	service.SetCapabilityRefresher(refresher)

	started := time.Now()
	_, _, err := service.Create(context.Background(), createInput(edge.ID, "stale-heal-0002"))
	elapsed := time.Since(started)
	if !errors.Is(err, ErrActionUnavailable) {
		t.Fatalf("err = %v, want ErrActionUnavailable", err)
	}
	if !errors.Is(err, ErrCapabilityStale) {
		t.Fatalf("err = %v, want 错误链里带 ErrCapabilityStale（可诊断）", err)
	}
	if refresher.count() != 1 {
		t.Fatalf("SendQueryResources 次数 = %d, want 1（不可自愈时不许空转）", refresher.count())
	}
	if elapsed > time.Second {
		t.Fatalf("耗时 %v, want <= 1s（超时必须受 capabilityRefreshTimeout 约束）", elapsed)
	}
}


func TestCreateWithoutRefresherKeepsOldBehaviour(t *testing.T) {
	service, edge := setupService(t)
	makeCapabilityStale(t, service.db, edge.NodeID)

	_, _, err := service.Create(context.Background(), createInput(edge.ID, "stale-heal-0003"))
	if !errors.Is(err, ErrActionUnavailable) || !errors.Is(err, ErrCapabilityStale) {
		t.Fatalf("err = %v, want ErrActionUnavailable+ErrCapabilityStale（未接线时行为不变）", err)
	}
}


func TestIssueConfirmationSelfHealsStaleCapabilityReport(t *testing.T) {
	service, edge, _ := setupConfirmationService(t)
	makeCapabilityStale(t, service.db, edge.NodeID)

	refresher := &fakeCapabilityRefresher{onCall: func(string) { reportCapabilities(t, service.db, edge.NodeID) }}
	service.SetCapabilityRefresher(refresher)

	grant, err := service.IssueConfirmation(context.Background(), confInput(edge.ID, "medium_read"))
	if err != nil {
		t.Fatalf("IssueConfirmation 未自愈: %v", err)
	}
	if grant == nil || grant.Token == "" {
		t.Fatalf("grant = %+v", grant)
	}
	if refresher.count() != 1 {
		t.Fatalf("SendQueryResources 次数 = %d, want 1", refresher.count())
	}
}


func TestRefreshNodeCapabilitiesSkipsOfflineNode(t *testing.T) {
	service, edge := setupService(t)
	service.capabilityRefreshTimeout = 80 * time.Millisecond
	service.capabilityRefreshPoll = 10 * time.Millisecond
	makeCapabilityStale(t, service.db, edge.NodeID)
	if err := service.db.Model(&models.Node{}).Where("node_id = ?", edge.NodeID).Update("status", "offline").Error; err != nil {
		t.Fatal(err)
	}
	refresher := &fakeCapabilityRefresher{}
	service.SetCapabilityRefresher(refresher)

	if service.refreshNodeCapabilities(context.Background(), edge.ID) {
		t.Fatal("离线节点不该被判为刷新成功")
	}
	if refresher.count() != 0 {
		t.Fatalf("对离线节点下发了 %d 次 QueryResources, want 0", refresher.count())
	}
}