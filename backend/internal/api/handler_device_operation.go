package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"ehome/backend/internal/commandexec"
	"ehome/backend/internal/events"
	"ehome/backend/internal/models"
	"ehome/backend/internal/websocket"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// registerDeviceOperationRoutes is the only public device-action entry point.
// It has no MQTT transport in Phase 1: requests persist durably as QUEUED and
// cannot become a physical command until the reviewed Phase 2 dispatcher exists.
func registerDeviceOperationRoutes(v1 *gin.RouterGroup, service *commandexec.Service, wsHub *websocket.Hub) {
	v1.GET("/edge-devices/:id/actions", func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid edge device id")
			return
		}
		items, err := service.Catalog(c.Request.Context(), uint(id))
		if errors.Is(err, gorm.ErrRecordNotFound) {
			Error(c, http.StatusNotFound, "edge device not found")
			return
		}
		if err != nil {
			Error(c, http.StatusInternalServerError, "load action catalog failed")
			return
		}
		Success(c, items)
	})

	v1.POST("/edge-devices/:id/operations", func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid edge device id")
			return
		}
		var req struct {
			ActionID          string          `json:"action_id"`
			Params            json.RawMessage `json:"params"`
			ConfirmationToken string          `json:"confirmation_token"`
			Reason            string          `json:"reason"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "invalid operation request")
			return
		}
		actor, _ := c.Get("subject_id")
		actorID, _ := actor.(uint)
		execution, replayed, err := service.Create(c.Request.Context(), commandexec.CreateInput{
			EdgeDeviceID: uint(id), ActorUserID: actorID, ActionID: req.ActionID, Params: req.Params,
			IdempotencyKey: c.GetHeader("Idempotency-Key"), SourceIP: c.ClientIP(),
			ConfirmationToken: req.ConfirmationToken, Reason: req.Reason,
		})
		switch {
		case err == nil:
			if wsHub != nil {
				wsHub.BroadcastAuthenticatedEvent(events.DeviceOperationUpdate, execution)
			}
			SuccessWithCodeMsg(c, http.StatusAccepted, gin.H{"execution": execution, "idempotent_replay": replayed}, "queued")
		case errors.Is(err, commandexec.ErrIdempotencyCollision):
			Error(c, http.StatusConflict, "idempotency key collision")
		case errors.Is(err, commandexec.ErrActionUnavailable):
			Error(c, http.StatusConflict, "action unavailable")
		case errors.Is(err, commandexec.ErrInvalidParams):
			Error(c, http.StatusBadRequest, "invalid action parameters")
		case errors.Is(err, commandexec.ErrConfirmationRequired), errors.Is(err, commandexec.ErrConfirmationInvalid):
			Error(c, http.StatusConflict, "valid confirmation is required")
		case errors.Is(err, commandexec.ErrRecentAuthRequired):
			ErrorWithCode(c, http.StatusForbidden, "recent_auth_required", "recent authentication is required")
		case errors.Is(err, commandexec.ErrConfirmationRateLimited):
			Error(c, http.StatusTooManyRequests, "confirmation rate limit exceeded")
		case errors.Is(err, gorm.ErrRecordNotFound):
			Error(c, http.StatusNotFound, "edge device not found")
		default:
			Error(c, http.StatusInternalServerError, "create operation failed")
		}
	})

	v1.GET("/edge-devices/:id/operations", func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid edge device id")
			return
		}
		var edge models.EdgeDevice
		if err := serviceDB(service).First(&edge, uint(id)).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				Error(c, http.StatusNotFound, "edge device not found")
				return
			}
			Error(c, http.StatusInternalServerError, "check edge device failed")
			return
		}
		items, err := service.List(c.Request.Context(), uint(id), 0)
		if err != nil {
			Error(c, http.StatusInternalServerError, "load operation history failed")
			return
		}
		Success(c, items)
	})

	v1.GET("/device-operations/:execution_id", func(c *gin.Context) {
		execution, err := service.Get(c.Request.Context(), c.Param("execution_id"))
		if errors.Is(err, gorm.ErrRecordNotFound) {
			Error(c, http.StatusNotFound, "operation not found")
			return
		}
		if err != nil {
			Error(c, http.StatusInternalServerError, "load operation failed")
			return
		}
		Success(c, execution)
	})

	v1.POST("/device-operations/:execution_id/cancel", func(c *gin.Context) {
		actor, _ := c.Get("subject_id")
		actorID, _ := actor.(uint)
		execution, err := service.Cancel(c.Request.Context(), c.Param("execution_id"), actorID)
		if errors.Is(err, commandexec.ErrNotCancellable) {
			Error(c, http.StatusConflict, "operation is no longer cancellable")
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			Error(c, http.StatusNotFound, "operation not found")
			return
		}
		if err != nil {
			Error(c, http.StatusInternalServerError, "cancel operation failed")
			return
		}
		if wsHub != nil {
			wsHub.BroadcastAuthenticatedEvent(events.DeviceOperationUpdate, execution)
		}
		Success(c, execution)
	})

	v1.POST("/device-operations/:execution_id/resolve", func(c *gin.Context) {
		var req struct {
			Outcome string `json:"outcome"`
			Reason  string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "invalid manual resolution")
			return
		}
		actor, _ := c.Get("subject_id")
		actorID, _ := actor.(uint)
		execution, _, err := service.ResolveUnknown(c.Request.Context(), commandexec.ResolveUnknownInput{
			CommandID: c.Param("execution_id"), ActorUserID: actorID,
			Outcome: req.Outcome, Reason: req.Reason, SourceIP: c.ClientIP(),
		})
		switch {
		case err == nil:
			if wsHub != nil {
				wsHub.BroadcastAuthenticatedEvent(events.DeviceOperationUpdate, execution)
			}
			Success(c, execution)
		case errors.Is(err, commandexec.ErrInvalidResolution):
			Error(c, http.StatusBadRequest, "invalid manual resolution")
		case errors.Is(err, commandexec.ErrNotResolvable), errors.Is(err, commandexec.ErrAlreadyResolved):
			Error(c, http.StatusConflict, "operation cannot be manually resolved")
		case errors.Is(err, gorm.ErrRecordNotFound):
			Error(c, http.StatusNotFound, "operation not found")
		default:
			Error(c, http.StatusInternalServerError, "resolve operation failed")
		}
	})

	v1.POST("/edge-devices/:id/actions/:action_id/confirm", func(c *gin.Context) {
		id, err := strconv.ParseUint(c.Param("id"), 10, 64)
		if err != nil {
			Error(c, http.StatusBadRequest, "invalid edge device id")
			return
		}
		var req struct {
			Params json.RawMessage `json:"params"`
			Reason string          `json:"reason"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "invalid confirmation request")
			return
		}
		actor, _ := c.Get("subject_id")
		actorID, _ := actor.(uint)
		grant, err := service.IssueConfirmation(c.Request.Context(), commandexec.ConfirmationInput{EdgeDeviceID: uint(id), ActorUserID: actorID, ActionID: c.Param("action_id"), Params: req.Params, Reason: req.Reason, SourceIP: c.ClientIP()})
		switch {
		case err == nil:
			Success(c, grant)
		case errors.Is(err, commandexec.ErrConfirmationNotNeeded):
			Error(c, http.StatusConflict, "confirmation is not required for this action")
		case errors.Is(err, commandexec.ErrRecentAuthRequired):
			ErrorWithCode(c, http.StatusForbidden, "recent_auth_required", "recent authentication is required")
		case errors.Is(err, commandexec.ErrActionUnavailable):
			Error(c, http.StatusConflict, "action unavailable")
		case errors.Is(err, commandexec.ErrInvalidParams), errors.Is(err, commandexec.ErrConfirmationRequired):
			Error(c, http.StatusBadRequest, "invalid confirmation request")
		case errors.Is(err, gorm.ErrRecordNotFound):
			Error(c, http.StatusNotFound, "edge device not found")
		default:
			Error(c, http.StatusInternalServerError, "issue confirmation failed")
		}
	})
}

// serviceDB exists only to verify a history path's edge-device identity before
// returning an empty list. Keep DB ownership inside commandexec otherwise.
func serviceDB(service *commandexec.Service) *gorm.DB { return service.Database() }
