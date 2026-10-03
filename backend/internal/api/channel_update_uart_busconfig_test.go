package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"ehome/backend/internal/models"

	"github.com/gin-gonic/gin"
)

// =====================================================================
// 缺陷 4 的残留缺口（2026-10-03 复核时发现，由子代理如实上报）：
// PUT /channels/:id 曾把 bus_config 原样写回，包括空串。
//
// 创建路径（POST /channels 与向导内联）已经保证"UART 通道的 bus_config 一定可解析、
// 可改波特率"，但更新路径没守同一个不变量 —— 调用方一次 PUT 就能把已有通道
// 打回"以后永远改不了波特率"的状态（reconfigure 报「bus_config 为空」），
// 与用户 2026-10-03 报的缺陷 4 是同一个后果。**不变量只守一半等于没守。**
//
// 本用例锁住"更新后仍能被 withUARTBaudrate 改写"这个用户可见效果，
// 而不是只断言状态码 —— 状态码 200 但 bus_config 为空同样是有缺陷的。
// =====================================================================

// putChannel 发一次 PUT /channels/:id。
func putChannel(t *testing.T, r *gin.Engine, id uint, body map[string]interface{}) *httptest.ResponseRecorder {
	t.Helper()
	payload, _ := json.Marshal(body)
	w := httptest.NewRecorder()
	req := httptest.NewRequest("PUT", "/api/v1/channels/"+strconv.FormatUint(uint64(id), 10), bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	return w
}

// TestChannelUpdate_UARTEmptyBusConfigDoesNotCreateUnfixableChannel：显式把 bus_config 清空，
// 落库后必须仍可被 withUARTBaudrate 改写（补齐），或请求被明确拒绝；
// 唯独不允许"200 且 bus_config 为空"。
//
// 它凭什么会失败：把 PUT 里的 ensureUARTBusConfig 调用去掉，本用例立刻红
// （状态 200，bus_config 为空串）。
func TestChannelUpdate_UARTEmptyBusConfigDoesNotCreateUnfixableChannel(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online", Capabilities: uartCapabilities("UART1", 20, 21, 5000000)})
	valid := "14150000258008010000"
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART",
		HardwareID: "UART1", BusConfig: valid, Enabled: true, IntervalMs: 5000})

	w := putChannel(t, r, 1, map[string]interface{}{"bus_config": ""})
	if w.Code == http.StatusOK {
		// 200 是允许的，但落库结果必须仍可改波特率（即已按能力补齐）。
		var ch models.Channel
		if err := db.First(&ch, 1).Error; err != nil {
			t.Fatalf("reload: %v", err)
		}
		if _, err := withUARTBaudrate(ch.BusConfig, 4800); err != nil {
			t.Fatalf("更新后 bus_config=%q 无法改波特率，正是缺陷 4：%v", ch.BusConfig, err)
		}
	} else if w.Code != http.StatusBadRequest {
		t.Fatalf("清空 bus_config 要么补齐(200) 要么明确拒绝(400)，实得 %d: %s", w.Code, w.Body.String())
	}
}

// TestChannelUpdate_UARTRejectsNonRouteBusConfigWith400：**连引脚路由都不是**的值必须 400，
// 且**不得落库** —— 静默改写会让用户以为自己的引脚被采纳了。
//
// ⚠ 2026-10-03 修正（后端审查发现的 P0）：本用例原先用 "0304"（2 字节）当"非法"样本，
// **那是错的**。2 字节 tx+rx 是 UART 引脚路由的**合法**最短形式（channelRoutePins
// 要求 >=2），仿真套件与存量库都在用；把它判成非法会让 -tags=simulation 门禁大面积变红。
// 真正非法的样本是**非 hex**（如 "zz"）或 <2 字节（如 "01"）。
func TestChannelUpdate_UARTRejectsNonRouteBusConfigWith400(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online", Capabilities: uartCapabilities("UART1", 20, 21, 5000000)})
	original := "14150000258008010000"
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART",
		HardwareID: "UART1", BusConfig: original, Enabled: true, IntervalMs: 5000})

	for _, bad := range []string{"zz", "01"} {
		w := putChannel(t, r, 1, map[string]interface{}{"bus_config": bad})
		if w.Code != http.StatusBadRequest {
			t.Fatalf("bus_config=%q 连引脚路由都不是，应报 400，实得 %d: %s", bad, w.Code, w.Body.String())
		}
	}
	var ch models.Channel
	if err := db.First(&ch, 1).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if ch.BusConfig != original {
		t.Fatalf("被拒绝的更新却改动了 bus_config：%q -> %q", original, ch.BusConfig)
	}
}

// TestChannelUpdate_UART2ByteRouteAccepted 是上一条的**边界对照**，也是 P0 的回归护栏：
// 2 字节 bus_config 是合法引脚路由，必须被接受（哪怕它暂时改不了波特率）。
// 仿真套件（chan.go:482 / edge.go:608 / scene.go:247 / auto.go:242）正是用 2 字节，
// 且断言 201/200；拒绝它们会让 -tags=simulation 门禁变红。
//
// 它凭什么会失败：把 ensureUARTBusConfig 的判据改回 withUARTBaudrate（要求 >=6 字节），
// 本用例立刻红（400）。
func TestChannelUpdate_UART2ByteRouteAccepted(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online", Capabilities: uartCapabilities("UART1", 20, 21, 5000000)})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART",
		HardwareID: "UART1", BusConfig: "14150000258008010000", Enabled: true, IntervalMs: 5000})

	w := putChannel(t, r, 1, map[string]interface{}{"bus_config": "0304"})
	if w.Code != http.StatusOK {
		t.Fatalf("2 字节引脚路由是合法的（channelRoutePins 要求 >=2），应被接受，实得 %d: %s",
			w.Code, w.Body.String())
	}
	var ch models.Channel
	if err := db.First(&ch, 1).Error; err != nil {
		t.Fatalf("reload: %v", err)
	}
	if ch.BusConfig != "0304" {
		t.Fatalf("调用方显式给的引脚路由必须原样保留，实得 %q", ch.BusConfig)
	}
}

// TestChannelUpdate_NonUARTChannelUnaffectedByUARTRules 是对照组：I2C 通道的 bus_config 长度
// 不受 UART 10 字节布局约束，不得因为新的 UART 校验被误伤（避免"修一个坏一个"）。
func TestChannelUpdate_NonUARTChannelUnaffectedByUARTRules(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "I2C", BusType: "I2C",
		HardwareID: "I2C0", BusConfig: "0102", Enabled: true, IntervalMs: 5000})

	w := putChannel(t, r, 1, map[string]interface{}{"interval_ms": 9000})
	if w.Code != http.StatusOK {
		t.Fatalf("I2C 通道改轮询周期不应触发 UART 校验，实得 %d: %s", w.Code, w.Body.String())
	}
}
