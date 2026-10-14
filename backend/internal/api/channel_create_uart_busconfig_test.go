package api

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"ehome/backend/internal/models"
)

// =====================================================================
// UART 通道创建时 bus_config 的兜底（2026-10-03 实测缺陷）
//
// 用户可见症状：新建 UART 通道后，在节点详情点「改波特率」报
// 「通道 bus_config 为空，无法重配波特率」。根因是前端在 capabilities.buses.uart
// 里找不到资源时**静默**产出空 bus_config，后端原样落库。
//
// 本文件守的是**效果**而不是字段非空：
//   1. 补齐后的 bus_config 必须能被 withUARTBaudrate 解出/改写（"建完就能改波特率"）；
//   2. 走真实的 reconfigure 端点也必须成功（端到端，而不是只测内部函数）；
//   3. 能力里查不到该资源 ⇒ 400 且**不落库**（宁可拒绝，也不造一个改不了波特率的通道）。
//
// 为什么断言"能改"而不是"非空"：空串和 2 字节串都非空/可存，但都解不出波特率。
// 只断言非空会让"落了个 2 字节垃圾"也通过 —— 那正是本缺陷的同类变体。
// =====================================================================

// uartCapabilities 造一份 ResourceReport 形态的 capabilities（字段名与
// nodemgr/handler_resources.go 的 uartEntry 一致）。
func uartCapabilities(id string, tx, rx, maxBaud int) string {
	return `{"buses":{"uart":[{"id":"` + id + `","port":0,"default_tx_pin":` + strconv.Itoa(tx) +
		`,"default_rx_pin":` + strconv.Itoa(rx) + `,"max_baud":` + strconv.Itoa(maxBaud) + `}]}}`
}

// createUARTChannel posts a UART channel without bus_config and returns the recorder.
func createUARTChannel(t *testing.T, r http.Handler, nodeID, hardwareID string, busConfig any) *httptest.ResponseRecorder {
	t.Helper()
	payload := map[string]interface{}{
		"node_id":       nodeID,
		"hardware_type": "UART",
		"bus_type":      "UART",
		"hardware_id":   hardwareID,
		"enabled":       true,
	}
	if busConfig != nil {
		payload["bus_config"] = busConfig
	}
	body, _ := json.Marshal(payload)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestChannelCreate_UARTFillsBusConfigFromCapabilities 是本次缺陷的正向回归锁。
func TestChannelCreate_UARTFillsBusConfigFromCapabilities(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online", Capabilities: uartCapabilities("UART1", 20, 21, 5000000)})

	w := createUARTChannel(t, r, "NODE001", "UART1", nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("UART 通道（无 bus_config）创建失败：%d %s", w.Code, w.Body.String())
	}

	var ch models.Channel
	if err := db.First(&ch).Error; err != nil {
		t.Fatalf("读取落库通道失败：%v", err)
	}
	if strings.TrimSpace(ch.BusConfig) == "" {
		t.Fatalf("落库 bus_config 仍为空 —— 用户会得到一个改不了波特率的通道")
	}

	// 分辨力断言 1：补齐后的串必须能被动 withUARTBaudrate 解出并改写成 115200。
	updated, err := withUARTBaudrate(ch.BusConfig, 115200)
	if err != nil {
		t.Fatalf("补齐后的 bus_config %q 无法被 withUARTBaudrate 解析（建完改不了波特率）：%v", ch.BusConfig, err)
	}
	if updated == ch.BusConfig {
		t.Fatalf("withUARTBaudrate 认为无需改动，说明补齐值不是 9600 或解析错位：%q", ch.BusConfig)
	}
	// 115200 = 0x0001C200；tx/rx 必须是能力上报的 20/21 = 0x14/0x15。
	if !strings.HasPrefix(strings.ToUpper(updated), "14150001C200") {
		t.Fatalf("改写后 hex = %q，期望前缀 14150001C200（tx=20,rx=21,baud=115200）", updated)
	}

	// 分辨力断言 2：端到端走真实 reconfigure 端点，而不是只测内部函数。
	req := httptest.NewRequest(http.MethodPost, "/api/v1/channels/"+strconv.FormatUint(uint64(ch.ID), 10)+"/reconfigure",
		bytes.NewReader([]byte(`{"baudrate":115200}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	w2 := httptest.NewRecorder()
	r.ServeHTTP(w2, req)
	if w2.Code != http.StatusOK {
		t.Fatalf("新建通道后调用 reconfigure = %d，期望 200（建完必须就能改波特率）：%s", w2.Code, w2.Body.String())
	}
	var stored models.Channel
	if err := db.First(&stored, ch.ID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.ToUpper(stored.BusConfig), "0001C200") {
		t.Fatalf("reconfigure 后落库 bus_config = %q，期望包含 0001C200", stored.BusConfig)
	}
}

// 默认速率取能力上限以下的值；上限低于默认值时退到上限（不造设备明确不支持的速率）。
func TestChannelCreate_UARTDefaultBaudrateClampedToCapability(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online", Capabilities: uartCapabilities("UART1", 20, 21, 4800)})

	w := createUARTChannel(t, r, "NODE001", "UART1", nil)
	if w.Code != http.StatusCreated {
		t.Fatalf("创建失败：%d %s", w.Code, w.Body.String())
	}
	var ch models.Channel
	if err := db.First(&ch).Error; err != nil {
		t.Fatal(err)
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(strings.TrimSpace(ch.BusConfig), `\x`))
	if err != nil || len(decoded) != uartBusConfigLen {
		t.Fatalf("bus_config = %q（err=%v），期望 10 字节合法 hex", ch.BusConfig, err)
	}
	baud := int(decoded[2])<<24 | int(decoded[3])<<16 | int(decoded[4])<<8 | int(decoded[5])
	if baud != 4800 {
		t.Fatalf("默认波特率 = %d，期望被能力上限 4800 夹住", baud)
	}
}

// 能力里查不到该资源 ⇒ 400 且不落库。这是"宁可拒绝，也不制造改不了波特率的通道"。
func TestChannelCreate_UARTWithoutReportedResourceRejects400(t *testing.T) {
	cases := []struct {
		name         string
		capabilities string
		hardwareID   string
	}{
		{"能力完全未上报", "", "UART1"},
		{"上报了别的 UART 资源", uartCapabilities("UART0", 16, 17, 5000000), "UART1"},
		{"hardware_id 拼写不符", uartCapabilities("UART1", 20, 21, 5000000), "UART2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, db := setupDeviceTest(t)
			db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online", Capabilities: tc.capabilities})

			w := createUARTChannel(t, r, "NODE001", tc.hardwareID, nil)
			if w.Code != http.StatusBadRequest {
				t.Fatalf("查不到资源时 = %d，期望 400（不得静默落库空 bus_config）：%s", w.Code, w.Body.String())
			}
			var count int64
			db.Model(&models.Channel{}).Count(&count)
			if count != 0 {
				t.Fatalf("被拒绝的 UART 通道仍落库了 %d 条", count)
			}
		})
	}
}

// 调用方显式给了合法 bus_config 时必须原样保留（兜底不得覆盖用户数据）。
func TestChannelCreate_UARTKeepsExplicitValidBusConfig(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online", Capabilities: uartCapabilities("UART1", 20, 21, 5000000)})

	explicit := "010200002580080100"
	w := createUARTChannel(t, r, "NODE001", "UART1", explicit)
	if w.Code != http.StatusCreated {
		t.Fatalf("创建失败：%d %s", w.Code, w.Body.String())
	}
	var ch models.Channel
	if err := db.First(&ch).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.EqualFold(strings.TrimSpace(ch.BusConfig), explicit) {
		t.Fatalf("显式 bus_config 被兜底覆盖：got %q want %q", ch.BusConfig, explicit)
	}
}

// 非法非空 bus_config 必须明确 400，而不是被兜底静默改写成能力值 ——
// 否则用户以为自己的引脚被采纳了。
func TestChannelCreate_UARTRejectsMalformedNonEmptyBusConfig(t *testing.T) {
	r, db := setupDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online", Capabilities: uartCapabilities("UART1", 20, 21, 5000000)})

	w := createUARTChannel(t, r, "NODE001", "UART1", "zz")
	if w.Code < 400 {
		t.Fatalf("非法 bus_config 被接受：%d %s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.Channel{}).Count(&count)
	if count != 0 {
		t.Fatalf("非法 bus_config 的通道被落库")
	}
}


// 向导内联建 UART 通道时同样不得落空 bus_config（2026-10-03）。
//
// 这条路径（POST /edge-devices 带 channel 子对象）原实现只在 caller 显式传 bus_config
// 时才写，否则落空串 —— 与 POST /channels 是同一个"改不了波特率"缺陷的第二个入口。
func TestEdgeDevice_Create_InlineUARTChannelFillsBusConfig(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{
		NodeID: "NODE001", Name: "Test", Status: "online",
		Capabilities: uartCapabilities("UART1", 20, 21, 5000000),
	})

	body, _ := json.Marshal(map[string]interface{}{
		"name":        "InlineUART",
		"node_id":     "NODE001",
		"type":        "sn3001_rain",
		"hardware_id": "1",
		"channel": map[string]interface{}{
			"hardware_type": "uart",
			"hardware_id":   "UART1",
		},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("向导内联建 UART 通道失败：%d %s", w.Code, w.Body.String())
	}

	var dev models.EdgeDevice
	if err := db.First(&dev, "name = ?", "InlineUART").Error; err != nil {
		t.Fatal(err)
	}
	var ch models.Channel
	if err := db.First(&ch, dev.ChannelID).Error; err != nil {
		t.Fatal(err)
	}
	// 分辨力：补齐后的串必须能被动 withUARTBaudrate 解出并改写（建完就能改波特率）。
	if _, err := withUARTBaudrate(ch.BusConfig, 9600); err != nil {
		t.Fatalf("内联通道 bus_config = %q 无法被 withUARTBaudrate 解析：%v", ch.BusConfig, err)
	}
	updated, err := withUARTBaudrate(ch.BusConfig, 115200)
	if err != nil {
		t.Fatalf("内联通道改波特率失败：%v", err)
	}
	if !strings.HasPrefix(strings.ToUpper(updated), "1415") {
		t.Fatalf("内联通道改写后 hex = %q，期望前缀 1415（tx=20,rx=21）", updated)
	}
}

// 向导内联建 UART 通道但节点未上报该资源 ⇒ 400 且不落库。
func TestEdgeDevice_Create_InlineUARTChannelWithoutCapabilityRejects(t *testing.T) {
	r, db := setupEdgeDeviceTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "Test", Status: "online"})

	body, _ := json.Marshal(map[string]interface{}{
		"name":        "NoCapsUART",
		"node_id":     "NODE001",
		"type":        "sn3001_rain",
		"hardware_id": "1",
		"channel": map[string]interface{}{
			"hardware_type": "uart",
			"hardware_id":   "UART1",
		},
	})
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("节点未上报 UART 资源时 = %d，期望 400（不得落空 bus_config）：%s", w.Code, w.Body.String())
	}
	var count int64
	db.Model(&models.Channel{}).Count(&count)
	if count != 0 {
		t.Fatalf("被拒绝的内联通道仍落库了 %d 条", count)
	}
}
// 共用布局的自证：buildUARTBusConfig 造出的串必须能被 withUARTBaudrate 读出同一波特率。
// 若有人把其中一处改成不同偏移，这里会红。
func TestUARTBusConfigLayout_SharedBetweenBuildAndRewrite(t *testing.T) {
	for _, baud := range []int{2400, 4800, 9600, 115200} {
		built := buildUARTBusConfig(20, 21, baud, true)
		decoded, err := hex.DecodeString(built)
		if err != nil || len(decoded) != uartBusConfigLen {
			t.Fatalf("buildUARTBusConfig 产物非法：%q err=%v", built, err)
		}
		got := int(decoded[2])<<24 | int(decoded[3])<<16 | int(decoded[4])<<8 | int(decoded[5])
		if got != baud {
			t.Fatalf("布局自证失败：写入 %d，按同一偏移读出 %d", baud, got)
		}
		// byte 6 是**DMA flags**，不是 data_bits：固件只读 bit0
		// （bus_dma.h:59-62 + config_channel_get_dma_enabled）。写成 8(=data_bits)
		// 会让 0x08&0x01==0，即静默关闭 DMA —— 这正是修复前的缺陷。
		if decoded[6]&0x01 != 0x01 {
			t.Fatalf("byte6=0x%02x 未置 DMA 使能位（固件按 byte6&0x01 判定）", decoded[6])
		}
		// 改写函数必须认它：同值应判 unchanged（返回原串）。
		rewritten, err := withUARTBaudrate(built, baud)
		if err != nil {
			t.Fatalf("withUARTBaudrate 无法解析 buildUARTBusConfig 的产物 %q：%v", built, err)
		}
		if rewritten != built {
			t.Fatalf("同值改写应返回原串：got %q want %q", rewritten, built)
		}
	}
}
