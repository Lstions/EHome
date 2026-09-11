package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/deviceaction"
	"ehome/backend/internal/models"
	"ehome/backend/testutil"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// P0-2: POST /api/v1/nodes with a duplicate node_id must return 409,
// not 500 (previously any create error returned 500 with the raw DB error).
func TestP0_Node_CreateDuplicateNodeID_Returns409(t *testing.T) {
	r, _ := setupTestRouter(t)

	body, _ := json.Marshal(map[string]string{"node_id": "DUP-NODE-1", "name": "dup"})

	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/nodes", bytes.NewReader(body)))
	if rec.Code != http.StatusCreated && rec.Code != http.StatusOK {
		t.Fatalf("first create expected 2xx, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/api/v1/nodes", bytes.NewReader(body)))
	if rec.Code != http.StatusConflict {
		t.Fatalf("duplicate node_id expected 409, got %d: %s", rec.Code, rec.Body.String())
	}
}

// P0-3: POST /api/v1/edge-devices/:id/operations without Idempotency-Key
// must map commandexec.ErrInvalidRequest to 400, not fall through to default 500.
func TestP0_DeviceOperation_MissingIdempotencyKey_Returns400(t *testing.T) {
	db := testutil.OpenTestDB(t)
	router := newP0TestOperationRouter(t, db)

	body := []byte(`{"action_id":"read_rainfall","params":{}}`)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/edge-devices/1/operations", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	// Deliberately no Idempotency-Key header.
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("missing Idempotency-Key expected 400, got %d: %s", rec.Code, rec.Body.String())
	}
}

// P0-1: DELETE /api/v1/edge-devices/:id for a nonexistent id must return 404,
// not 500 (previously ErrRecordNotFound was answered with 500).
func TestP0_EdgeDevice_DeleteNonExistent_Returns404(t *testing.T) {
	r, _ := setupEdgeDeviceTest(t)

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/edge-devices/999999", nil)
	req.Header.Set("Authorization", authHeader(t))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("delete nonexistent expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// P0-4 regression guard: POST /api/v1/vendors happy path still returns 201
// after the error-handling fix (failure injection is not feasible on
// :memory: sqlite with all tables migrated, so only the success path is asserted).
func TestP0_Vendor_Create_Success_Still201(t *testing.T) {
	r, db := setupVendorTest(t)

	body, _ := json.Marshal(map[string]string{"name": "P0Vendor"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/vendors", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authHeader(t))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusCreated {
		t.Fatalf("create vendor expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var count int64
	db.Model(&models.Vendor{}).Count(&count)
	if count != 1 {
		t.Fatalf("expected 1 vendor row, got %d", count)
	}
}

// newP0TestOperationRouter wires commandexec + device operation routes with an
// authenticated (subject_id=11) stub, mirroring the existing lifecycle test.
func newP0TestOperationRouter(t *testing.T, db *gorm.DB) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	v1 := router.Group("/api/v1")
	v1.Use(func(c *gin.Context) { c.Set("subject_id", uint(11)); c.Next() })
	actions := deviceaction.NewBuiltInRegistry(nil)
	service := commandexec.NewService(db, actions)
	registerDeviceOperationRoutes(v1, service, nil)
	return router
}
