package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/drivers"
	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
)

// 四集相等契约（演进方案 C4/W5）:
//
//	A = GET /drivers/:type/commands 返回的模板 ID 集
//	B = GET /edge-devices/:id/commands 返回的模板 ID 集
//	C = SchedulableCommandIDs 的合法键集
//	D = manifest 编码候选集（nodemgr.CommandIsManifestCandidate）
//
// 断言 A == B == C == D；且非 schedulable 模板不属于任何一集。
func TestCommandContract_FourSetEquality(t *testing.T) {
	r, db := setupDriverCommandsTest(t) // C2 基建：fake_multi + registerDriverCommandRoutes
	// 设备：全部 schedulable 命令都配置 interval>0；模板 WriteData 全部落库
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	intervals, _ := json.Marshal(map[string]int{"read_a": 3000, "read_b": 7000})
	db.Create(&models.EdgeDevice{Name: "D", NodeID: "NODE001", ChannelID: 1, Type: "fake_multi", CommandIntervals: intervals})
	db.Create(&models.ConfigTemplate{NodeID: "NODE001", WriteData: "AA01", ReadLength: 4, DelayMs: 10})
	db.Create(&models.ConfigTemplate{NodeID: "NODE001", WriteData: "AA02", ReadLength: 4, DelayMs: 10})

	// A
	wa := httptest.NewRecorder()
	reqA := httptest.NewRequest(http.MethodGet, "/api/v1/drivers/fake_multi/commands", nil)
	reqA.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(wa, reqA)
	if wa.Code != http.StatusOK {
		t.Fatalf("A: %d %s", wa.Code, wa.Body.String())
	}
	setA := idsFromDriverCommandsResponse(t, wa)

	// B
	wb := httptest.NewRecorder()
	reqB := httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices/1/commands", nil)
	reqB.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(wb, reqB)
	if wb.Code != http.StatusOK {
		t.Fatalf("B: %d %s", wb.Code, wb.Body.String())
	}
	setB := idsFromDeviceCommandsResponse(t, wb)

	// C
	registry := newFakeRegistry()
	setC, err := SchedulableCommandIDs(registry, "fake_multi")
	if err != nil {
		t.Fatal(err)
	}

	// D：与生产编码同源谓词
	drv, _ := registry.Get("fake_multi")
	provider := drv.(drivers.CommandTemplateProvider) // 同包测试可直接断言
	stored := map[string]int{"read_a": 3000, "read_b": 7000}
	templates := []models.ConfigTemplate{
		{ID: 1, WriteData: "AA01"},
		{ID: 2, WriteData: "AA02"},
	}
	setD := map[string]struct{}{}
	for _, tmpl := range provider.GetCommandTemplates() {
		if nodemgr.CommandIsManifestCandidate(tmpl, stored, templates) {
			setD[tmpl.ID] = struct{}{}
		}
	}

	assertSameIDSet(t, "A==B", setA, setB)
	assertSameIDSet(t, "B==C", setB, setC)
	assertSameIDSet(t, "C==D", setC, setD)
	// 非 schedulable 模板不属于任何一集
	if _, ok := setA["one_shot"]; ok {
		t.Fatal("one_shot leaked into A")
	}
	if _, ok := setD["one_shot"]; ok {
		t.Fatal("one_shot leaked into D")
	}
}

// TestCommandContract_IntervalZeroDropsFromDOnly 锁定候选集谓词边界：
// 存 {read_a:3000, read_b:0} 时 read_b 仍在 A/B/C（GET 与键集与 interval 无关），
// 但不在 D（effective interval == 0 不编码）——D ⊆ C 且相等仅在"全部配置
// interval>0"时成立。
func TestCommandContract_IntervalZeroDropsFromDOnly(t *testing.T) {
	r, db := setupDriverCommandsTest(t)
	db.Create(&models.Node{NodeID: "NODE001", Name: "n", Status: "online"})
	db.Create(&models.Channel{NodeID: "NODE001", HardwareType: "UART", BusType: "UART", Enabled: true})
	intervals, _ := json.Marshal(map[string]int{"read_a": 3000, "read_b": 0})
	db.Create(&models.EdgeDevice{Name: "D", NodeID: "NODE001", ChannelID: 1, Type: "fake_multi", CommandIntervals: intervals})
	db.Create(&models.ConfigTemplate{NodeID: "NODE001", WriteData: "AA01", ReadLength: 4, DelayMs: 10})
	db.Create(&models.ConfigTemplate{NodeID: "NODE001", WriteData: "AA02", ReadLength: 4, DelayMs: 10})

	wa := httptest.NewRecorder()
	reqA := httptest.NewRequest(http.MethodGet, "/api/v1/drivers/fake_multi/commands", nil)
	reqA.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(wa, reqA)
	setA := idsFromDriverCommandsResponse(t, wa)

	wb := httptest.NewRecorder()
	reqB := httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices/1/commands", nil)
	reqB.Header.Set("Authorization", authHeader(t))
	r.ServeHTTP(wb, reqB)
	setB := idsFromDeviceCommandsResponse(t, wb)

	registry := newFakeRegistry()
	setC, err := SchedulableCommandIDs(registry, "fake_multi")
	if err != nil {
		t.Fatal(err)
	}

	drv, _ := registry.Get("fake_multi")
	provider := drv.(drivers.CommandTemplateProvider)
	stored := map[string]int{"read_a": 3000, "read_b": 0}
	templates := []models.ConfigTemplate{
		{ID: 1, WriteData: "AA01"},
		{ID: 2, WriteData: "AA02"},
	}
	setD := map[string]struct{}{}
	for _, tmpl := range provider.GetCommandTemplates() {
		if nodemgr.CommandIsManifestCandidate(tmpl, stored, templates) {
			setD[tmpl.ID] = struct{}{}
		}
	}

	assertSameIDSet(t, "A==B", setA, setB)
	assertSameIDSet(t, "B==C", setB, setC)
	// read_b 仍在 A/B/C、不在 D；D == {read_a}
	if _, ok := setC["read_b"]; !ok {
		t.Fatal("read_b missing from C")
	}
	if _, ok := setD["read_b"]; ok {
		t.Fatal("read_b with interval 0 must not be a manifest candidate (D)")
	}
	assertSameIDSet(t, "D=={read_a}", setD, map[string]struct{}{"read_a": {}})
}

// idsFromDriverCommandsResponse decodes the GET /drivers/:type/commands
// envelope {"code":200,"data":[...]} into the set of template ids.
func idsFromDriverCommandsResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]struct{} {
	t.Helper()
	return idsFromCommandsEnvelope(t, w.Body.Bytes())
}

// idsFromDeviceCommandsResponse decodes the GET /edge-devices/:id/commands
// envelope into the set of template ids.
func idsFromDeviceCommandsResponse(t *testing.T, w *httptest.ResponseRecorder) map[string]struct{} {
	t.Helper()
	return idsFromCommandsEnvelope(t, w.Body.Bytes())
}

func idsFromCommandsEnvelope(t *testing.T, body []byte) map[string]struct{} {
	t.Helper()
	var resp struct {
		Code int `json:"code"`
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("decode commands envelope: %v (%s)", err, body)
	}
	ids := map[string]struct{}{}
	for _, c := range resp.Data {
		ids[c.ID] = struct{}{}
	}
	return ids
}

// assertSameIDSet asserts both directions of set equality and prints the
// asymmetric difference on failure.
func assertSameIDSet(t *testing.T, label string, a, b map[string]struct{}) {
	t.Helper()
	var onlyA, onlyB []string
	for id := range a {
		if _, ok := b[id]; !ok {
			onlyA = append(onlyA, id)
		}
	}
	for id := range b {
		if _, ok := a[id]; !ok {
			onlyB = append(onlyB, id)
		}
	}
	if len(onlyA) > 0 || len(onlyB) > 0 {
		t.Fatalf("%s: sets differ — only in A: %v, only in B: %v (A=%v B=%v)", label, onlyA, onlyB, a, b)
	}
}
