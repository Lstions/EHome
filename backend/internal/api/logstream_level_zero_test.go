package api

// `log_stream_level` 的零值必须真的落库（潜伏陷阱回归锁）。
//
// ===== 缺陷实测（2026-09-16）=====
// `models.Node.LogStreamLevel` 带 `gorm:"default:2"`，而模型注释写明 `0=ERROR` 是合法级别。
// 探针实测：`db.Create(&models.Node{LogStreamLevel: 0})` ⇒ DB 回读 **2**（INFO）。
// 与本仓已**四次**踩到的 GORM 零值陷阱同源：带非零 `default:` 的列，显式零值在 INSERT 时
// 会被静默替换成默认值（详见 `internal/models/defaults_gate_test.go` 的文件头汇总）。
//
// ===== 为什么它是「潜伏陷阱」而非现行缺陷（如实记录，不夸大）=====
//   · `log_stream_level` 全仓只有**一个**写入点：`handler_logstream.go` 的 `Updates(map)`，
//     该形态**不会**丢零值（本用例即端到端证明）；
//   · 节点创建的两条 `Create` 路径（`handler_node.go` / `handler_nodemgr` 的 hello）都不设置该字段。
//   ⇒ 今天不坏。但任何新代码写出 `Create(&models.Node{LogStreamLevel: 0, …})` 就会静默变成 INFO ——
//     用户把日志调到最安静档（ERROR），却拿到 INFO 级噪音。
//
// ===== 本用例的价值 ===== 把「唯一写入点必须保持 Updates(map) 形态」钉死：
// 若将来有人改成 `Save(&node)` / `Create`，这条会立刻变红。
// 另一条同类回归见 `handler_logstream_test.go` 的 `…RetainZeroValues`（走 API 断言响应），
// 本文件额外**回读 DB**，因为只断言响应区分不了「存了 0」与「读时转换」。

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/models"
)

func TestLogStreamLevelZeroPersistsToDB(t *testing.T) {
	db := setupTestDB(t)
	if err := db.AutoMigrate(&models.NodeLog{}); err != nil {
		t.Fatal(err)
	}
	node := models.Node{NodeID: "node-level-zero", LogStreamLevel: 2}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}

	r := setupRouter()
	v1 := r.Group("/api/v1")
	v1.Use(JWTAuth())
	registerLogStreamRoutes(v1.Group("/nodes"), db, nil)

	token, err := GenerateToken(1, "admin")
	if err != nil {
		t.Fatal(err)
	}
	put := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPut, "/api/v1/nodes/"+node.NodeID+"/log-config", bytes.NewReader([]byte(body)))
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		return w
	}

	// ① 走 API 把 level 设为 0（ERROR）—— 唯一的生产写入路径。
	if w := put(`{"level":0}`); w.Code != http.StatusOK {
		t.Fatalf("set level=0: %d %s", w.Code, w.Body.String())
	}

	// ② **直连库**复核：落库值必须是 0。
	var stored models.Node
	if err := db.Where("node_id = ?", node.NodeID).First(&stored).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.LogStreamLevel != 0 {
		t.Errorf("DB 里 log_stream_level=%d，期望 0（ERROR）—— "+
			"0 是合法级别，不得被 gorm default:2 回填；若此断言红，说明写入点被改成了 Create/Save",
			stored.LogStreamLevel)
	}

	// ③ API 也要读回 0（与 ② 互为独立来源：一个查库、一个查响应）
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/"+node.NodeID+"/log-config", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var cfg map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &cfg); err != nil {
		t.Fatal(err)
	}
	if cfg["level"] != float64(0) {
		t.Errorf("API 读回 level=%v，期望 0", cfg["level"])
	}
}
