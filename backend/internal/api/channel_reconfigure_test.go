package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"gorm.io/gorm"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
)

// 通道重配端点**必须真的改动 bus_config**（回归锁）。
//
// ===== 缺陷（2026-09-16 实测）=====
// `POST /channels/:channel_id/reconfigure` 曾只做 `ShouldBindJSON` 后直接返回
// `{"status":"reconfigured"}` —— 不解析 baudrate、不查通道、不下发。
// 前端 `ChannelPanel.vue` 的「修改波特率」对话框随后提示 **「重配置命令已发送」**，
// 于是用户以为硬件已改，**实际什么都没发生**。
//
// 这是"谎报成功"类缺陷：最坏之处不是功能缺失，而是**失败被伪装成成功**，
// 让人不去排查。故修法不只是补功能，还要让不支持的情况**明确报错**。
//
// ===== 本文件守什么 =====
//   1. 合法的 UART 波特率重配 ⇒ 200，且 **DB 里的 bus_config 真的变了**（字节 2..5）；
//   2. 目标值与现值相同 ⇒ 200 但 `status=unchanged`（如实说明未改动，不谎称 reconfigured）；
//   3. 非 UART 通道 ⇒ 400（不假装成功）；
//   4. `clock_hz`（SPI）⇒ 501（未实现就明说，不静默忽略）；
//   5. 坏 bus_config ⇒ 400（宁可失败，也不写坏数据下发到硬件）。

// newReconfigureTestRouter 造一个带通道的最小路由。
func newReconfigureTestRouter(t *testing.T) (*reconfigureEnv, *models.Channel) {
	t.Helper()
	db := setupTestDB(t)
	ch := models.Channel{
		NodeID:       "NODE-RC",
		HardwareType: "UART",
		HardwareID:   "UART0",
		BusType:      "UART",
		// 波特率 9600 = 0x00002580 位于字节 2..5；其余字节为占位。
		BusConfig: "AABB00002580CC",
	}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatalf("create channel: %v", err)
	}
	r := setupRouter()
	registerDeviceRoutes(r.Group("/api/v1"), db, nodemgr.NewManager(db, nil, nil, nil, nil, nil), nil)
	return &reconfigureEnv{r: r, db: db}, &ch
}

type reconfigureEnv struct {
	r  http.Handler
	db *gorm.DB
}

func postReconfigure(t *testing.T, r http.Handler, channelID uint, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost,
		fmt.Sprintf("/api/v1/channels/%d/reconfigure", channelID), bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func TestChannelReconfigure_ActuallyUpdatesBusConfig(t *testing.T) {
	env, ch := newReconfigureTestRouter(t)

	w := postReconfigure(t, env.r, ch.ID, `{"baudrate":115200}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reconfigure=%d: %s", w.Code, w.Body.String())
	}

	var stored models.Channel
	if err := env.db.First(&stored, ch.ID).Error; err != nil {
		t.Fatalf("read back: %v", err)
	}
	if stored.BusConfig == ch.BusConfig {
		t.Fatalf("bus_config 未发生任何变化（仍是 %q）—— 端点又变成了谎报成功", ch.BusConfig)
	}
	// 115200 = 0x0001C200 ⇒ 字节 2..5 应为 00 01 C2 00，前缀 AABB 与后缀 CC 保留
	if !strings.Contains(strings.ToUpper(stored.BusConfig), "AABB0001C200CC") {
		t.Errorf("bus_config = %q，期望包含 AABB0001C200CC（字节 2..5 = 115200，其余保留）", stored.BusConfig)
	}
}

func TestChannelReconfigure_SameValueReportsUnchanged(t *testing.T) {
	env, ch := newReconfigureTestRouter(t)
	// 现值就是 9600（0x2580），再设一次 9600 应为 unchanged 而非 reconfigured
	w := postReconfigure(t, env.r, ch.ID, `{"baudrate":9600}`)
	if w.Code != http.StatusOK {
		t.Fatalf("reconfigure=%d: %s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Status string `json:"status"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Status != "unchanged" {
		t.Errorf("同值时 status = %q，期望 unchanged（不得谎称已改）", resp.Data.Status)
	}
}

func TestChannelReconfigure_RejectsNonUART(t *testing.T) {
	db := setupTestDB(t)
	ch := models.Channel{NodeID: "NODE-RC2", HardwareType: "I2C", HardwareID: "I2C0",
		BusType: "I2C", BusConfig: "AABB00002580CC"}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	r := setupRouter()
	registerDeviceRoutes(r.Group("/api/v1"), db, nodemgr.NewManager(db, nil, nil, nil, nil, nil), nil)

	w := postReconfigure(t, r, ch.ID, `{"baudrate":115200}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("非 UART 通道重配 = %d，期望 400（不得假装成功）: %s", w.Code, w.Body.String())
	}
}

func TestChannelReconfigure_ClockHzNotImplemented(t *testing.T) {
	env, ch := newReconfigureTestRouter(t)
	w := postReconfigure(t, env.r, ch.ID, `{"clock_hz":8000000}`)
	if w.Code != http.StatusNotImplemented {
		t.Errorf("clock_hz 重配 = %d，期望 501（未实现必须明说）: %s", w.Code, w.Body.String())
	}
}

func TestChannelReconfigure_RejectsMalformedBusConfig(t *testing.T) {
	db := setupTestDB(t)
	ch := models.Channel{NodeID: "NODE-RC3", HardwareType: "UART", HardwareID: "UART1",
		BusType: "UART", BusConfig: "ZZZZ"}
	if err := db.Create(&ch).Error; err != nil {
		t.Fatal(err)
	}
	r := setupRouter()
	registerDeviceRoutes(r.Group("/api/v1"), db, nodemgr.NewManager(db, nil, nil, nil, nil, nil), nil)

	w := postReconfigure(t, r, ch.ID, `{"baudrate":115200}`)
	if w.Code != http.StatusBadRequest {
		t.Errorf("坏 bus_config = %d，期望 400（宁可失败也不写坏数据下发）: %s", w.Code, w.Body.String())
	}
}
