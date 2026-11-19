package database

import (
	"encoding/json"
	"strings"
	"testing"

	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"gorm.io/gorm"
)

// dmaCfgView 是测试里读回 node.Config 的最小视图。
type dmaCfgView struct {
	DMAs []struct {
		DmaID   uint32 `json:"dma_id"`
		Enabled bool   `json:"enabled"`
		BindTo  string `json:"bind_to"`
	} `json:"dma_configs"`
	Other json.RawMessage `json:"other"`
}

func reloadNodeConfig(t *testing.T, db *gorm.DB, nodeID string) (models.Node, dmaCfgView) {
	t.Helper()
	var node models.Node
	if err := db.Where("node_id = ?", nodeID).First(&node).Error; err != nil {
		t.Fatalf("reload node %s: %v", nodeID, err)
	}
	var view dmaCfgView
	if err := json.Unmarshal([]byte(node.Config), &view); err != nil {
		t.Fatalf("parse node.Config after migration: %v\nraw=%s", err, node.Config)
	}
	return node, view
}

// TestMigrateDMABindToCanonicalCaseRewritesLegacyLowercase 是本次回归的数据侧锁：
// 前后端修复后，库里已有的 "uart/uart1" 不会自己变干净 —— 它仍会随每次配置同步
// 下发，让设备端对应 GDMA 通道被永久占用（strcmp 匹配不上）。
//
// 反向验证：把 canonicalizeDMABindTo 里 hwid.Normalize 的改写分支去掉，本用例立即红。
func TestMigrateDMABindToCanonicalCaseRewritesLegacyLowercase(t *testing.T) {
	db := testutil.OpenTestDB(t)
	const nodeID = "30EDA0A9A808"
	const legacy = `{"other":{"keep":true},"dma_configs":[` +
		`{"dma_id":1,"enabled":true,"bind_to":"uart/uart1"},` +
		`{"dma_id":0,"enabled":false,"bind_to":""},` +
		`{"dma_id":2,"enabled":true,"bind_to":"spi/SPI2"}]}`
	if err := db.Create(&models.Node{NodeID: nodeID, Name: "s3", Status: "online", Config: legacy}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}

	res, err := MigrateDMABindToCanonicalCase(db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if res.Scanned != 1 || res.Updated != 1 {
		t.Fatalf("Scanned/Updated = %d/%d, 期望 1/1（changes=%v）", res.Scanned, res.Updated, res.Changes)
	}
	if len(res.Changes) != 1 || !strings.Contains(res.Changes[0], `"uart/uart1" -> "uart/UART1"`) {
		t.Fatalf("Changes = %v, 期望一条 uart/uart1 -> uart/UART1", res.Changes)
	}

	_, view := reloadNodeConfig(t, db, nodeID)
	if got := view.DMAs[0].BindTo; got != "uart/UART1" {
		t.Fatalf("dma_configs[0].bind_to = %q, 期望规范化后的 uart/UART1", got)
	}
	if got := view.DMAs[1].BindTo; got != "" {
		t.Errorf("空绑定被改写为 %q —— 空值必须原样保留", got)
	}
	if got := view.DMAs[2].BindTo; got != "spi/SPI2" {
		t.Errorf("已是规范值的 bind_to 被改成 %q", got)
	}
	if string(view.Other) != `{"keep":true}` {
		t.Errorf("无关配置键被改动：%s", view.Other)
	}

	// 幂等：修好后重跑不得再产生任何改写。
	again, err := MigrateDMABindToCanonicalCase(db)
	if err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	if again.Updated != 0 || len(again.Changes) != 0 {
		t.Fatalf("迁移不幂等：第二次 Updated=%d changes=%v", again.Updated, again.Changes)
	}
}

// 未知 id（拼错/别家控制器）不能被"猜"成某个规范值：原样保留并报告。
// 这类值应当由写入校验（400 + 正确写法）让人显式修正，而不是被迁移抹平。
func TestMigrateDMABindToCanonicalCaseLeavesUnknownValues(t *testing.T) {
	db := testutil.OpenTestDB(t)
	const nodeID = "F0F5BD02F35C"
	const cfg = `{"dma_configs":[{"dma_id":0,"enabled":true,"bind_to":"uart/UART9"}]}`
	if err := db.Create(&models.Node{NodeID: nodeID, Name: "c6", Status: "online", Config: cfg}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}

	res, err := MigrateDMABindToCanonicalCase(db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if res.Updated != 0 {
		t.Fatalf("未知值被改写（Updated=%d, changes=%v）", res.Updated, res.Changes)
	}
	if len(res.Unknown) != 1 || !strings.Contains(res.Unknown[0], "uart/UART9") {
		t.Fatalf("Unknown = %v, 期望报告 uart/UART9", res.Unknown)
	}
	_, view := reloadNodeConfig(t, db, nodeID)
	if got := view.DMAs[0].BindTo; got != "uart/UART9" {
		t.Fatalf("未知值被改成 %q，必须原样保留", got)
	}
}

// 单行 JSON 坏掉不能阻断启动：跳过并计数，其它节点照常修。
func TestMigrateDMABindToCanonicalCaseSkipsBrokenJSON(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if err := db.Create(&models.Node{NodeID: "BROKEN", Name: "x", Status: "offline", Config: `{"dma_configs":[`}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}

	res, err := MigrateDMABindToCanonicalCase(db)
	if err != nil {
		t.Fatalf("坏 JSON 不应让迁移整体失败: %v", err)
	}
	if res.Skipped != 1 || res.Updated != 0 {
		t.Fatalf("Skipped/Updated = %d/%d, 期望 1/0", res.Skipped, res.Updated)
	}
}

// 没有 dma_configs 的节点连扫描都不该进入（config LIKE 过滤）。
func TestMigrateDMABindToCanonicalCaseIgnoresNodesWithoutDMA(t *testing.T) {
	db := testutil.OpenTestDB(t)
	if err := db.Create(&models.Node{NodeID: "PLAIN", Name: "x", Status: "online", Config: `{"log_stream_enabled":true}`}).Error; err != nil {
		t.Fatalf("create node: %v", err)
	}
	res, err := MigrateDMABindToCanonicalCase(db)
	if err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if res.Scanned != 0 {
		t.Fatalf("Scanned = %d, 期望 0（无 dma_configs 的节点不应进入扫描）", res.Scanned)
	}
}
