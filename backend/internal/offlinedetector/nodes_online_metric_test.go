package offlinedetector

import (
	"strconv"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/metrics"

	dto "github.com/prometheus/client_model/go"
	"gorm.io/gorm"
)

// readEdgeDeviceGauge 读取 ehome_edge_device_total{status=...} 的当前值。
func readEdgeDeviceGauge(t *testing.T, status string) float64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.EdgeDeviceTotal.WithLabelValues(status).Write(&m); err != nil {
		t.Fatalf("读取 EdgeDeviceTotal{%s} 失败: %v", status, err)
	}
	return m.GetGauge().GetValue()
}

// TestEdgeDeviceTotalMetricTracksDBStatus 锁定 ehome_edge_device_total 的写入。
//
// 缺陷（2026-10-06 审计发现）：该 GaugeVec 注册后**全仓没有任何写入点** ⇒
// 从未出现在 /metrics 上。而 docs/设计/系统监控.md:41 与
// docs/设计/总体设计.md:147 都把它列为对外指标 —— 文档承诺了、实际拿不到。
func TestEdgeDeviceTotalMetricTracksDBStatus(t *testing.T) {
	d, db := setupExtraDetector(t)

	statuses := []string{
		models.EdgeDeviceStatusActive, models.EdgeDeviceStatusActive,
		models.EdgeDeviceStatusPending, models.EdgeDeviceStatusOffline,
	}
	for i, st := range statuses {
		dev := models.EdgeDevice{
			Name:   "dev-" + strconv.Itoa(i),
			NodeID: "node-" + strconv.Itoa(i),
			Status: st,
		}
		if err := db.Create(&dev).Error; err != nil {
			t.Fatalf("创建边缘设备失败: %v", err)
		}
	}

	d.checkEdgeDevicesOffline(db.Session(&gorm.Session{}))

	for status, want := range map[string]float64{
		models.EdgeDeviceStatusActive:  2,
		models.EdgeDeviceStatusPending: 1,
		models.EdgeDeviceStatusOffline: 1,
	} {
		if got := readEdgeDeviceGauge(t, status); got != want {
			t.Errorf("ehome_edge_device_total{status=%q} 期望 %v，实际 %v", status, want, got)
		}
	}

	// 设备整体消失后必须归零 —— 只 Set 不 Reset 的写法会让已消失的状态
	// 永远停在最后一次读数上（对"离线数"而言就是永久的假离线）。
	if err := db.Where("1 = 1").Delete(&models.EdgeDevice{}).Error; err != nil {
		t.Fatalf("清空边缘设备失败: %v", err)
	}
	d.checkEdgeDevicesOffline(db.Session(&gorm.Session{}))
	for _, status := range []string{
		models.EdgeDeviceStatusActive, models.EdgeDeviceStatusPending, models.EdgeDeviceStatusOffline,
	} {
		if got := readEdgeDeviceGauge(t, status); got != 0 {
			t.Errorf("设备全部删除后 ehome_edge_device_total{status=%q} 必须为 0，实际 %v"+
				"（缺少 Reset 会使该序列永久卡在旧值）", status, got)
		}
	}
}

// TestDeprecatedNodeOnlineCountStaysInSync 锁定历史指标名与权威值同步。
//
// ehome_node_online_count 原先被匿名注册（_ = promauto.NewGauge）且无写入点 ⇒ 恒为 0。
// 恒为 0 比"不暴露"更坏：按旧名做看板会一直显示"0 个在线"而不是"无数据"。
func TestDeprecatedNodeOnlineCountStaysInSync(t *testing.T) {
	d, db := setupExtraDetector(t)

	now := time.Now()
	db.Create(&models.Node{NodeID: "sync-001", Status: "online", LastSeen: &now})
	db.Create(&models.Node{NodeID: "sync-002", Status: "online", LastSeen: &now})

	d.checkDBLastSeen(db.Session(&gorm.Session{}))

	var cur dto.Metric
	if err := metrics.NodesOnline.Write(&cur); err != nil {
		t.Fatalf("读取 NodesOnline 失败: %v", err)
	}
	var old dto.Metric
	if err := metrics.NodeOnlineCountDeprecated.Write(&old); err != nil {
		t.Fatalf("读取 NodeOnlineCountDeprecated 失败: %v", err)
	}

	if got := old.GetGauge().GetValue(); got != 2 {
		t.Errorf("ehome_node_online_count 期望 2，实际 %v（该名字此前恒为 0）", got)
	}
	if a, b := cur.GetGauge().GetValue(), old.GetGauge().GetValue(); a != b {
		t.Errorf("两个指标名必须同步：ehome_nodes_online=%v 而 ehome_node_online_count=%v", a, b)
	}
}

// readNodesOnlineGauge 读取 ehome_nodes_online 的**当前真实值**。
//
// 不能用 pkg/metrics 里的 assertGaugeValue —— 它只断言 gauge 非 nil，读不出数值，
// 因此对本类缺陷（值陈旧）完全没有分辨力。
func readNodesOnlineGauge(t *testing.T) float64 {
	t.Helper()
	var m dto.Metric
	if err := metrics.NodesOnline.Write(&m); err != nil {
		t.Fatalf("读取 NodesOnline 失败: %v", err)
	}
	if m.GetGauge() == nil {
		t.Fatalf("NodesOnline 不是 gauge")
	}
	return m.GetGauge().GetValue()
}

// TestNodesOnlineMetricTracksDBStatus 是 ehome_nodes_online 陈旧值的回归用例。
//
// 缺陷（2026-10-06 现场实测）：该 gauge 只在 nodemgr.NewManager 里 Set() 一次
// （manager.go:209-212），全仓无 Inc/Dec ⇒ 进程启动后永远停在**启动瞬间**的在线数。
// 生产实测：DB 3 个节点 online，而 /metrics 报 ehome_nodes_online 2。
//
// 危害不是"数字不准"这么轻：deploy/monitoring/alert_rules.yml:192 的
// EhomeAllNodesOffline 是 **critical** 级告警，判据 ehome_nodes_online == 0；
// 若启动时恰无节点在线，则该指标恒为 0 ⇒ 全部节点离线也永不告警（假阴性），
// 反过来启动时在线数偏高又会掩盖真实离线。
//
// 本用例锁定：在线数必须随 DB 的 status 变化而变化。
func TestNodesOnlineMetricTracksDBStatus(t *testing.T) {
	d, db := setupExtraDetector(t)

	// 三个在线节点，last_seen 都是"刚刚"，不会被判离线。
	now := time.Now()
	for i, id := range []string{"online-001", "online-002", "online-003"} {
		n := models.Node{NodeID: id, Status: "online", LastSeen: &now}
		if i == 2 {
			// 第三个的 last_seen 已超阈值 ⇒ 本轮应被判离线。
			stale := now.Add(-(NodeOfflineThreshold + time.Second))
			n.LastSeen = &stale
		}
		if err := db.Create(&n).Error; err != nil {
			t.Fatalf("创建节点失败: %v", err)
		}
	}

	// 一次检测循环：把 stale 的一个标离线，同时刷新指标。
	d.checkDBLastSeen(db.Session(&gorm.Session{}))

	var online int64
	db.Model(&models.Node{}).Where("status = ?", "online").Count(&online)
	if online != 2 {
		t.Fatalf("前置条件不成立：期望 DB 剩 2 个在线，实际 %d", online)
	}

	if got := readNodesOnlineGauge(t); got != float64(online) {
		t.Errorf("ehome_nodes_online 必须等于 DB 在线数：期望 %d，实际 %v"+
			"（该指标陈旧正是本用例要锁定的缺陷；它同时驱动 critical 告警 EhomeAllNodesOffline）",
			online, got)
	}

	// 再让最后一个在线节点也超时：指标必须跟着降到 0。
	past := now.Add(-(NodeOfflineThreshold + time.Second))
	db.Model(&models.Node{}).Where("node_id IN ?", []string{"online-001", "online-002"}).
		Update("last_seen", past)

	d.checkDBLastSeen(db.Session(&gorm.Session{}))

	db.Model(&models.Node{}).Where("status = ?", "online").Count(&online)
	if got := readNodesOnlineGauge(t); got != float64(online) {
		t.Errorf("全部离线后 ehome_nodes_online 必须为 %d，实际 %v"+
			"（若卡在旧值，EhomeAllNodesOffline 这条 critical 告警就永远不会触发）",
			online, got)
	}

	// 新节点上线（Hello 路径）后，下一次检测循环也必须反映出来。
	db.Create(&models.Node{NodeID: "online-004", Status: "online", LastSeen: &now})
	d.checkDBLastSeen(db.Session(&gorm.Session{}))
	db.Model(&models.Node{}).Where("status = ?", "online").Count(&online)
	if got := readNodesOnlineGauge(t); got != float64(online) {
		t.Errorf("新节点上线后 ehome_nodes_online 必须为 %d，实际 %v", online, got)
	}
}
