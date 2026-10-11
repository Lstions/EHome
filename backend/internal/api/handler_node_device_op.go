package api

import (
	"errors"
	"net/http"

	"ehome/backend/internal/nodemgr"
	"ehome/backend/pkg/frame"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// handler_node_device_op.go -- reboot / factory-reset a collector node.
//
// # What the operator asked for
//
// "Be able to reboot a node device and factory-reset it from the UI, WITHOUT
// resetting the WiFi credentials." The device side already implements both
// policies (device_op component: the factory-reset erase list is `config` and
// deliberately NOT `wifi_cfg`), so the server half is what was missing.
//
// # This endpoint is SYNCHRONOUS, deliberately
//
// It waits for the device's ACK. That is the point: the operator is about to
// decide whether to drive to site, and "request accepted" does not answer that
// question. The wait is bounded (nodemgr.DefaultDeviceOpTimeout = 15s) and the
// device ACKs BEFORE restarting (design §44.3), so in practice the answer
// arrives in well under a second.
//
// # HTTP codes answer the question the caller actually has
//
//	200 OK              the device performed the operation
//	409 Conflict        the device REFUSED (and said why), or one is already in flight
//	502 Bad Gateway     we could not hand it to any transport
//	202 Accepted        delivered, but no ACK: the device MAY have done it
//	501 Not Implemented no device transport is configured on this server
//
// The 202 case is the one worth thinking about. It is NOT a failure: a reboot
// whose ACK was lost still rebooted. Reporting it as an error would send an
// operator to site for nothing; reporting it as success would be a lie.
// 202 says exactly what we know: "we sent it; we have no answer".

func registerNodeDeviceOpRoutes(v1 *gin.RouterGroup, db *gorm.DB, nodeMgr *nodemgr.Manager) {
	// GET the supported operations so the UI does not hard-code the list and
	// cannot offer an operation the server does not understand.
	v1.GET("/nodes/device-ops", func(c *gin.Context) {
		Success(c, gin.H{
			"supported": nodeMgr.DeviceOpSupported(),
			"operations": []gin.H{
				{
					"id":   "reboot",
					"name": "重启",
					"description": "Restart the collector. Configuration and WiFi " +
						"credentials are kept.",
				},
				{
					"id":   "factory_reset",
					"name": "恢复出厂",
					"description": "Erase the device configuration. WiFi credentials " +
						"and device identity are KEPT, so the device returns to " +
						"service instead of needing physical re-provisioning.",
				},
			},
		})
	})

	v1.POST("/nodes/:id/device-ops", func(c *gin.Context) {
		node, err := findNodeByID(db, c.Param("id"))
		if err != nil {
			Error(c, http.StatusNotFound, "node not found")
			return
		}
		if !nodeMgr.DeviceOpSupported() {
			// Refuse explicitly instead of accepting a request that can never be
			// delivered: "accepted" followed by nothing is the worst answer.
			Error(c, http.StatusNotImplemented,
				"this server has no device transport configured for device operations")
			return
		}

		var req struct {
			Op     string `json:"op"`
			Reason string `json:"reason"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			Error(c, http.StatusBadRequest, "invalid request body")
			return
		}
		op, ok := parseDeviceOp(req.Op)
		if !ok {
			Error(c, http.StatusBadRequest,
				"unknown op; expected one of: reboot, factory_reset")
			return
		}

		outcome, err := nodeMgr.SendDeviceOp(node.NodeID, op, 0)
		if err != nil {
			// Local refusal ("already in flight") is the only error that reaches
			// here from SendDeviceOp itself. A transport problem arrives as an
			// unacked OUTCOME, so it is handled below rather than as an error.
			if errors.Is(err, nodemgr.ErrDeviceOpInFlight) {
				Error(c, http.StatusConflict, err.Error())
				return
			}
			Error(c, http.StatusBadGateway, err.Error())
			return
		}

		switch {
		case outcome.Acked && outcome.Result == frame.DeviceOpOK:
			SuccessMsg(c, opResultBody(outcome, req.Op), "the device accepted the operation")
		case outcome.Acked:
			// The device answered and refused. Its own reason is the useful part;
			// collapsing it into a generic "failed" would throw away the only
			// information the operator has. It goes in the error CODE so the
			// frontend can branch on it without parsing prose.
			ErrorWithCode(c, http.StatusConflict,
				"device_rejected:"+frame.DeviceOpResultName(outcome.Result),
				"the device refused the operation: "+frame.DeviceOpResultName(outcome.Result)+
					" (request_id="+outcome.RequestID+")")
		default:
			// Delivered, never acknowledged. The outcome is genuinely UNKNOWN.
			SuccessWithCodeMsg(c, http.StatusAccepted, opResultBody(outcome, req.Op),
				"the request was delivered but the device did not acknowledge it; "+
					"the operation may still have taken effect")
		}
	})
}

func opResultBody(outcome nodemgr.DeviceOpOutcome, opName string) gin.H {
	body := gin.H{
		"request_id": outcome.RequestID,
		"op":         opName,
		"acked":      outcome.Acked,
		"result":     frame.DeviceOpResultName(outcome.Result),
	}
	if outcome.Detail != "" {
		body["detail"] = outcome.Detail
	}
	return body
}

// parseDeviceOp maps the API names onto the wire enum.
//
// A CLOSED mapping: an unrecognised string is rejected rather than defaulted.
// A default here would mean a typo in the UI silently REBOOTS a device when the
// operator asked for a factory reset. The two operations are not
// interchangeable and must never be reachable by each other's spelling.
func parseDeviceOp(name string) (frame.DeviceOp, bool) {
	switch name {
	case "reboot":
		return frame.DeviceOpReboot, true
	case "factory_reset":
		return frame.DeviceOpFactoryResetKeepConn, true
	default:
		return 0, false
	}
}
