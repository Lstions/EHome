package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"ehome/backend/internal/models"
	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/frame"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// handler_node_device_op_test.go -- the HTTP surface of reboot / factory reset.
//
// These call registerNodeDeviceOpRoutes directly, so they also prove the routes
// REGISTER: gin panics on a conflicting wildcard ("/nodes/device-ops" next to
// "/nodes/:id"), and a panic here fails the test rather than shipping.
//
// The manager is built with the REAL nodemgr constructor and a fake publisher,
// so the tests exercise the same code path production uses -- no test-only
// back door that could diverge from it.
//
// Request bodies come from opBody() rather than string literals, so this file
// contains no backquotes: shell quoting has mangled my source edits repeatedly
// this session, and a test that cannot be edited reliably is not worth much.

// opBody marshals a request body so no JSON literal appears in this file.
func opBody(op string) string {
	b, err := json.Marshal(map[string]string{"op": op})
	if err != nil {
		panic(err)
	}
	return string(b)
}

// swallowPublisher accepts everything and answers nothing, which is exactly the
// behaviour of a device that never ACKs.
type swallowPublisher struct {
	mu       sync.Mutex
	payloads [][]byte
}

func (s *swallowPublisher) Publish(nodeID string, payload []byte) error {
	s.mu.Lock()
	s.payloads = append(s.payloads, append([]byte(nil), payload...))
	s.mu.Unlock()
	return nil
}

func (s *swallowPublisher) lastPayload() []byte {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.payloads) == 0 {
		return nil
	}
	return s.payloads[len(s.payloads)-1]
}

type opTestEnv struct {
	db  *gorm.DB
	mgr *nodemgr.Manager
	pub *swallowPublisher
}

func newDeviceOpAPI(t *testing.T) (*gin.Engine, *opTestEnv) {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Node{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	node := models.Node{NodeID: "api-node-1", Name: "n1", Status: "online"}
	if err := db.Create(&node).Error; err != nil {
		t.Fatalf("seed node: %v", err)
	}

	pub := &swallowPublisher{}
	mgr := nodemgr.NewManager(db, pub, nil, nil, nil)
	r := setupRouter()
	registerNodeDeviceOpRoutes(r.Group("/api/v1"), db, mgr)
	return r, &opTestEnv{db: db, mgr: mgr, pub: pub}
}

func doPost(t *testing.T, r *gin.Engine, path, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// TestDeviceOpRoutesRegisterAndList -- GET must work, and reaching it proves the
// wildcard registration did not conflict with "/nodes/:id".
func TestDeviceOpRoutesRegisterAndList(t *testing.T) {
	r, _ := newDeviceOpAPI(t)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/nodes/device-ops", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("GET device-ops = %d, body %s", w.Code, w.Body.String())
	}
	if !strings.Contains(w.Body.String(), "factory_reset") {
		t.Fatalf("the operation catalog does not list factory_reset: %s", w.Body.String())
	}
}

// TestUnknownOpIsRejectedNotDefaulted -- a typo must not reboot a device the
// operator asked to wipe.
func TestUnknownOpIsRejectedNotDefaulted(t *testing.T) {
	r, _ := newDeviceOpAPI(t)
	w := doPost(t, r, "/api/v1/nodes/api-node-1/device-ops", opBody("facotry_reset"))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("a misspelled op returned %d, want 400 -- it must NOT be defaulted "+
			"to some operation, because reboot and factory reset are not "+
			"interchangeable: %s", w.Code, w.Body.String())
	}
}

// TestEmptyOpIsRejected.
func TestEmptyOpIsRejected(t *testing.T) {
	r, _ := newDeviceOpAPI(t)
	w := doPost(t, r, "/api/v1/nodes/api-node-1/device-ops", opBody(""))
	if w.Code != http.StatusBadRequest {
		t.Fatalf("an empty op returned %d, want 400", w.Code)
	}
}

// TestUnknownNodeIs404.
func TestUnknownNodeIs404(t *testing.T) {
	r, _ := newDeviceOpAPI(t)
	w := doPost(t, r, "/api/v1/nodes/does-not-exist/device-ops", opBody("reboot"))
	if w.Code != http.StatusNotFound {
		t.Fatalf("unknown node returned %d, want 404: %s", w.Code, w.Body.String())
	}
}

// TestNoTransportIs501 -- refusing is better than accepting what can never run.
//
// "Accepted" followed by nothing is the worst answer, so a server that cannot
// reach any device must say so up front. A bare Manager (no tracker) models a
// build without the 3.0 transport; production always has one, but the branch
// must not be dead code that silently reports success.
func TestNoTransportIs501(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Silent),
	})
	if err != nil {
		t.Fatalf("sqlite: %v", err)
	}
	if err := db.AutoMigrate(&models.Node{}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if err := db.Create(&models.Node{NodeID: "api-node-1", Name: "n1", Status: "online"}).Error; err != nil {
		t.Fatalf("seed: %v", err)
	}
	bare := &nodemgr.Manager{} // no device transport
	if bare.DeviceOpSupported() {
		t.Fatal("a manager with no tracker claims to support device operations")
	}
	r := setupRouter()
	registerNodeDeviceOpRoutes(r.Group("/api/v1"), db, bare)

	w := doPost(t, r, "/api/v1/nodes/api-node-1/device-ops", opBody("reboot"))
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("a server with no device transport returned %d, want 501: %s",
			w.Code, w.Body.String())
	}
}

// TestOpNamesMapToDistinctWireValues -- the two operations must not be
// reachable by each other's spelling.
func TestOpNamesMapToDistinctWireValues(t *testing.T) {
	reboot, ok := parseDeviceOp("reboot")
	if !ok {
		t.Fatal("reboot did not parse")
	}
	factory, ok := parseDeviceOp("factory_reset")
	if !ok {
		t.Fatal("factory_reset did not parse")
	}
	if reboot == factory {
		t.Fatal("reboot and factory_reset map to the SAME wire value; asking to " +
			"erase a device would silently reboot it instead")
	}
	if reboot != frame.DeviceOpReboot || factory != frame.DeviceOpFactoryResetKeepConn {
		t.Fatalf("wire mapping is wrong: reboot=%d factory=%d", reboot, factory)
	}
	if _, ok := parseDeviceOp("factory-reset"); ok {
		t.Fatal("a near-miss spelling was accepted; the mapping must be exact")
	}
}

// TestRebootReachesTheWireAsAnEncodableRequest -- the payload the device would
// receive must decode, and must carry the operation the operator chose.
func TestRebootReachesTheWireAsAnEncodableRequest(t *testing.T) {
	r, env := newDeviceOpAPI(t)
	env.mgr.SetDeviceOpTimeout(30 * time.Millisecond)

	w := doPost(t, r, "/api/v1/nodes/api-node-1/device-ops", opBody("reboot"))
	if w.Code != http.StatusAccepted {
		t.Fatalf("no ACK should have arrived, so 202 was expected; got %d: %s",
			w.Code, w.Body.String())
	}
	payload := env.pub.lastPayload()
	if payload == nil {
		t.Fatal("nothing was published for the operation")
	}
	req, err := frame.DecodeDeviceOp(payload)
	if err != nil {
		t.Fatalf("the published payload does not decode: %v", err)
	}
	if req.Op != frame.DeviceOpReboot {
		t.Fatalf("wire op = %d, want reboot (%d)", req.Op, frame.DeviceOpReboot)
	}
	if req.RequestID == "" {
		t.Fatal("no request_id on the wire, so an ACK could not be correlated")
	}
}

// TestUnackedReturns202NotAnError -- the outcome is unknown, not failed.
//
// A reboot whose ACK was lost may still have happened; reporting it as a
// failure would send an operator to site for nothing, and reporting it as
// success would be a lie.
func TestUnackedReturns202NotAnError(t *testing.T) {
	r, env := newDeviceOpAPI(t)
	env.mgr.SetDeviceOpTimeout(30 * time.Millisecond)

	w := doPost(t, r, "/api/v1/nodes/api-node-1/device-ops", opBody("reboot"))
	if w.Code != http.StatusAccepted {
		t.Fatalf("an unacknowledged operation returned %d, want 202: %s",
			w.Code, w.Body.String())
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	data, ok := body["data"].(map[string]any)
	if !ok {
		t.Fatalf("no data object in the 202 response: %s", w.Body.String())
	}
	if acked, _ := data["acked"].(bool); acked {
		t.Fatal("the 202 response claims the operation was acknowledged")
	}
}

// TestConcurrentRequestReturns409 -- already in flight is a conflict; the
// operator should wait rather than retry blindly.
//
// Deterministic: the first request is held open by a long timeout and the test
// waits until the manager reports it in flight before sending the second, so
// there is no sleep-and-hope race.
func TestConcurrentRequestReturns409(t *testing.T) {
	r, env := newDeviceOpAPI(t)
	env.mgr.SetDeviceOpTimeout(2 * time.Second)

	first := make(chan int, 1)
	go func() {
		first <- doPost(t, r, "/api/v1/nodes/api-node-1/device-ops", opBody("reboot")).Code
	}()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) && !env.mgr.PendingDeviceOp("api-node-1") {
		time.Sleep(2 * time.Millisecond)
	}
	if !env.mgr.PendingDeviceOp("api-node-1") {
		t.Fatal("the first request never became pending; the test cannot prove anything")
	}

	w := doPost(t, r, "/api/v1/nodes/api-node-1/device-ops", opBody("reboot"))
	if w.Code != http.StatusConflict {
		t.Fatalf("a second concurrent request returned %d, want 409: %s",
			w.Code, w.Body.String())
	}
	<-first // let it finish (202 after its timeout)
}
