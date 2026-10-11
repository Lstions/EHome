package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// =====================================================================
// UART DMA 槽位不变量的**写入期**守护（2026-10-11 P1 修复）
//
// 症状（C6 F0F5BD02F35C 现场）：ch89/ch90 两条 UART 都开了 DMA，
// 节点每次 StatusReport 都触发整份 manifest 被拒
// （"UART DMA 槽位不足…配置里有 2 条 UART 通道开启了 DMA"），
// 永久 config_sync_state=failed，UI 只显示"配置失败"，看不出是 DMA。
//
// 根因：validateUARTDMASlots 原先只在下发 manifest 时调用，
// 通道写入 API 不校验 ⇒ DB 里能存出"写入期合法、下发期必炸"的组合。
//
// 本文件守的是**写入期就拒绝**：非法组合不该进 DB。
// =====================================================================

// dmaCapabilities 造一份含 UART 的 ResourceReport 形态 capabilities。
func dmaCapabilities() string {
	return `{"buses":{"uart":[` +
		`{"id":"UART0","port":0,"default_tx_pin":43,"default_rx_pin":44,"max_baud":5000000},` +
		`{"id":"UART1","port":1,"default_tx_pin":4,"default_rx_pin":5,"max_baud":5000000}]}}`
}

// createDMAChannel 建一条带 dma_enabled 的 UART 通道。
func createDMAChannel(t *testing.T, r http.Handler, nodeID, hardwareID string, dma bool) *httptest.ResponseRecorder {
	t.Helper()
	body, _ := json.Marshal(map[string]interface{}{
		"node_id":       nodeID,
		"hardware_type": "UART",
		"bus_type":      "UART",
		"hardware_id":   hardwareID,
		"enabled":       true,
		"dma_enabled":   dma,
	})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// 正向：单条 UART 开 DMA 必须成功（不误伤合法配置）。
func TestChannelCreate_SingleUARTDMAAllowed(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online", Capabilities: dmaCapabilities()})

	if w := createDMAChannel(t, r, "NODE001", "UART0", true); w.Code != http.StatusCreated {
		t.Fatalf("单条 UART 开 DMA 被误拒：%d %s", w.Code, w.Body.String())
	}
}

// 反向（本缺陷的回归锁）：第二条 UART 再开 DMA 必须在**写入期**被拒，
// 且不落库 —— 否则节点会永久 config failed。
func TestChannelCreate_SecondUARTDMARejectedAtWriteTime(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online", Capabilities: dmaCapabilities()})

	if w := createDMAChannel(t, r, "NODE001", "UART0", true); w.Code != http.StatusCreated {
		t.Fatalf("第一条 UART 开 DMA 应成功：%d %s", w.Code, w.Body.String())
	}
	w := createDMAChannel(t, r, "NODE001", "UART1", true)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("第二条 UART 开 DMA 应在写入期被拒（400），实际 %d %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "UART DMA 槽位不足") {
		t.Errorf("错误信息应说明是 DMA 槽位，实际：%s", w.Body.String())
	}
	var count int64
	db.Model(&models.Channel{}).Where("hardware_id = ?", "UART1").Count(&count)
	if count != 0 {
		t.Errorf("被拒的非法通道仍落库了 %d 行 —— 节点会永久 config failed", count)
	}
}

// 反向：UPDATE 把已有通道改成第二条开 DMA 的 UART，也必须被拒。
func TestChannelUpdate_SecondUARTDMARejected(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online", Capabilities: dmaCapabilities()})
	if w := createDMAChannel(t, r, "NODE001", "UART0", true); w.Code != http.StatusCreated {
		t.Fatalf("第一条应成功：%d %s", w.Code, w.Body.String())
	}
	if w := createDMAChannel(t, r, "NODE001", "UART1", false); w.Code != http.StatusCreated {
		t.Fatalf("第二条（DMA 关）应成功：%d %s", w.Code, w.Body.String())
	}
	var uart1 models.Channel
	if err := db.Where("hardware_id = ?", "UART1").First(&uart1).Error; err != nil {
		t.Fatalf("读取 UART1 通道失败：%v", err)
	}

	body, _ := json.Marshal(map[string]interface{}{"dma_enabled": true})
	req := httptest.NewRequest(http.MethodPut, "/api/v1/channels/"+strconv.FormatUint(uint64(uart1.ID), 10), bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("PUT 开启第二条 UART 的 DMA 应被拒（400），实际 %d %s", w.Code, w.Body.String())
	}
	var after models.Channel
	db.First(&after, uart1.ID)
	if after.DmaEnabled {
		t.Errorf("被拒的更新仍写入了 dma_enabled=true")
	}
}
