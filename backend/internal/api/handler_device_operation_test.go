package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"github.com/gin-gonic/gin"
)

func TestDeviceOperationReadOnlyLifecycle(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.OpenTestDB(t)
	reported := time.Now().UTC()
	node := models.Node{NodeID: "node-action", Name: "node", Status: "online", BootID: "boot-test", ConfigVersion: "manifest-test", ConfigStatus: "applied", ConfigSyncState: "in_sync", ResourceReportedAt: &reported, CommandEngineRevision: 1, CommandEngineCapabilities: `{"supports_channel_cmd_v2":true,"supports_finally":true,"max_tx_bytes":128,"max_rx_bytes":256,"max_step_timeout_ms":30000}`}
	if err := db.Create(&node).Error; err != nil {
		t.Fatal(err)
	}
	channel := models.Channel{NodeID: node.NodeID, HardwareType: "uart", BusType: "UART", Enabled: true}
	if err := db.Create(&channel).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&node).Update("hardware_info", `{"channels":[{"id":1,"enabled":true}]}`).Error; err != nil {
		t.Fatal(err)
	}
	edge := models.EdgeDevice{Name: "rain", NodeID: node.NodeID, ChannelID: channel.ID, DeviceConfigID: 1, Type: "prs3001", Enabled: true, Status: "active"}
	if err := db.Create(&edge).Error; err != nil {
		t.Fatal(err)
	}

	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) { c.Set("subject_id", uint(11)); c.Next() })
	actions := deviceaction.NewBuiltInRegistry(nil)
	if err := actions.SetEnabled("prs3001", "read_rainfall", true); err != nil {
		t.Fatal(err)
	}
	service := commandexec.NewService(db, actions)
	service.SetDispatchEnabled(true)
	registerDeviceOperationRoutes(v1, service, nil)

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices/1/actions", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("catalog status=%d body=%s", w.Code, w.Body.String())
	}

	body := []byte(`{"action_id":"read_rainfall","params":{}}`)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices/1/operations", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "api-read-0001")
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var response struct {
		Data struct {
			Execution        models.CommandExecution `json:"execution"`
			IdempotentReplay bool                    `json:"idempotent_replay"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if response.Data.Execution.Status != commandexec.StatusQueued || response.Data.IdempotentReplay {
		t.Fatalf("execution=%+v", response.Data)
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices/1/operations", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "api-read-0001")
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusAccepted {
		t.Fatalf("replay status=%d body=%s", w.Code, w.Body.String())
	}
	if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if !response.Data.IdempotentReplay {
		t.Fatal("identical request did not report replay")
	}

	// A confirmation token does not bypass the current physical action gate:
	// medium-risk actions remain unavailable until a separately reviewed
	// execution engine is deployed.
	if err := actions.Register(deviceaction.Definition{ID: "medium_read", Version: 1, Name: "medium", DeviceType: edge.Type, Semantics: "read", Risk: "medium", Enabled: true, Transport: deviceaction.ChannelCmdV2Adapter, SingleStep: deviceaction.SingleStep{TXData: []byte{1}, RXTimeoutMS: 1}}); err != nil {
		t.Fatal(err)
	}
	staleLogin := time.Now().UTC().Add(-time.Hour)
	subjectKey := models.SystemAdminSubjectKey
	if err := db.Create(&models.User{ID: 11, Username: "api-medium", PasswordHash: "hash", Enabled: true, SubjectKey: &subjectKey, SessionVersion: 1, LastLoginAt: &staleLogin}).Error; err != nil {
		t.Fatal(err)
	}
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices/1/actions/medium_read/confirm", bytes.NewReader([]byte(`{"params":{},"reason":"recoverable test"}`)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusForbidden || !bytes.Contains(w.Body.Bytes(), []byte(`"error_code":"recent_auth_required"`)) {
		t.Fatalf("recent-auth status=%d body=%s", w.Code, w.Body.String())
	}
	now := time.Now().UTC()
	if err := db.Model(&models.User{}).Where("id = ?", 11).UpdateColumn("last_login_at", now).Error; err != nil {
		t.Fatal(err)
	}
	grant, err := service.IssueConfirmation(context.Background(), commandexec.ConfirmationInput{EdgeDeviceID: edge.ID, ActorUserID: 11, ActionID: "medium_read", Params: json.RawMessage(`{}`), Reason: "recoverable test", SourceIP: "127.0.0.1"})
	if err != nil {
		t.Fatal(err)
	}
	body = []byte(`{"action_id":"medium_read","params":{},"confirmation_token":"` + grant.Token + `","reason":"recoverable test"}`)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices/1/operations", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "api-medium-0001")
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict || !bytes.Contains(w.Body.Bytes(), []byte(`"message":"action unavailable"`)) {
		t.Fatalf("confirmed medium create status=%d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/device-operations/"+response.Data.Execution.CommandID+"/cancel", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("cancel status=%d body=%s", w.Code, w.Body.String())
	}

	completedAt := time.Now().UTC()
	unknown := models.CommandExecution{
		CommandID: "11111111-2222-4333-8444-555555555555", EdgeDeviceID: edge.ID,
		NodeID: edge.NodeID, DeviceType: edge.Type, DeviceConfigID: edge.DeviceConfigID, ChannelID: edge.ChannelID,
		ManifestID: node.ConfigVersion, ActionID: "read_rainfall", ActionVersion: 1, ActorUserID: 11,
		IdempotencyScope: "api-unknown-scope", IdempotencyKey: "api-unknown-key", RequestHash: "api-unknown-hash",
		ParamsJSON: "{}", Status: commandexec.StatusUnknown, DeadlineAt: completedAt, FinalReason: "final timeout",
		CreatedAt: completedAt.Add(-time.Minute), CompletedAt: &completedAt,
	}
	if err := db.Create(&unknown).Error; err != nil {
		t.Fatal(err)
	}
	resolutionBody := []byte(`{"outcome":"ACKNOWLEDGED_UNKNOWN","reason":"现场无法取得独立证据"}`)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/device-operations/"+unknown.CommandID+"/resolve", bytes.NewReader(resolutionBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resolve status=%d body=%s", w.Code, w.Body.String())
	}
	var resolvedResponse struct {
		Data models.CommandExecution `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resolvedResponse); err != nil {
		t.Fatal(err)
	}
	if resolvedResponse.Data.Status != commandexec.StatusUnknown || resolvedResponse.Data.ManualResolution == nil || resolvedResponse.Data.ManualResolution.Outcome != commandexec.ResolutionAcknowledgedUnknown {
		t.Fatalf("resolved response=%+v", resolvedResponse.Data)
	}

	// An identical retry after a lost HTTP response is safe and does not create
	// a second audit conclusion.
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/device-operations/"+unknown.CommandID+"/resolve", bytes.NewReader(resolutionBody))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("resolution replay status=%d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/device-operations/"+unknown.CommandID+"/resolve", bytes.NewReader([]byte(`{"outcome":"CONFIRMED_SUCCEEDED","reason":"conflicting evidence"}`)))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusConflict {
		t.Fatalf("conflicting resolution status=%d body=%s", w.Code, w.Body.String())
	}

	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices/1/operations", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK || !bytes.Contains(w.Body.Bytes(), []byte(`"manual_resolution":{"outcome":"ACKNOWLEDGED_UNKNOWN"`)) {
		t.Fatalf("history did not include resolution status=%d body=%s", w.Code, w.Body.String())
	}
}

// TestDeviceOperationEdgeNotFound verifies that routes querying an edge device
// return 404 when the device does not exist, covering the error-handling branch
// added in the uncommitted handler_device_operation.go patch.
func TestDeviceOperationEdgeNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.OpenTestDB(t)

	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) { c.Set("subject_id", uint(11)); c.Next() })
	actions := deviceaction.NewBuiltInRegistry(nil)
	service := commandexec.NewService(db, actions)
	service.SetDispatchEnabled(true)
	registerDeviceOperationRoutes(v1, service, nil)

	// GET /edge-devices/9999/actions → 404
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices/9999/actions", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("catalog for missing edge: status=%d body=%s", w.Code, w.Body.String())
	}

	// GET /edge-devices/9999/operations → 404
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/api/v1/edge-devices/9999/operations", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("history for missing edge: status=%d body=%s", w.Code, w.Body.String())
	}

	// POST /edge-devices/9999/operations → 404
	body := []byte(`{"action_id":"read_rainfall","params":{}}`)
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices/9999/operations", bytes.NewReader(body))
	req.Header.Set("Idempotency-Key", "missing-edge-0001")
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("create for missing edge: status=%d body=%s", w.Code, w.Body.String())
	}
}

// TestDeviceOperationNotFound verifies that single-operation routes return 404
// for a nonexistent execution ID.
func TestDeviceOperationNotFound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	db := testutil.OpenTestDB(t)

	r := gin.New()
	v1 := r.Group("/api/v1")
	v1.Use(func(c *gin.Context) { c.Set("subject_id", uint(11)); c.Next() })
	actions := deviceaction.NewBuiltInRegistry(nil)
	service := commandexec.NewService(db, actions)
	service.SetDispatchEnabled(true)
	registerDeviceOperationRoutes(v1, service, nil)

	// GET /device-operations/<bogus-uuid> → 404
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/device-operations/00000000-0000-0000-0000-000000000000", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("get missing operation: status=%d body=%s", w.Code, w.Body.String())
	}

	// POST /device-operations/<bogus-uuid>/cancel → 404
	w = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPost, "/api/v1/device-operations/00000000-0000-0000-0000-000000000000/cancel", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNotFound {
		t.Fatalf("cancel missing operation: status=%d body=%s", w.Code, w.Body.String())
	}
}
