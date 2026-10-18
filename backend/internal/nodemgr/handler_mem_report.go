package nodemgr

import (
	"encoding/json"
	"errors"
	"fmt"

	"ehome/backend/internal/models"
	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
)

// handler_mem_report.go -- MSG_MEM_RPT (0x21, ESP->SVR).
//
// # Why this file exists
//
// The firmware has sent 0x21 since 2026-10-08 (main.c send_mem_report: once at
// startup, every 60 s, and on low-memory events). The backend reserved the
// constant (pkg/frame/frame.go:88 MsgMemRpt) and the display name (:445
// "mem_report"), but the nodemgr dispatch switch never grew a case for it.
// Every report therefore fell through to the default branch and produced:
//
//	WARN [<node>] Unknown msg type: 0x21
//
// Measured on real hardware 2026-10-18: the warning fires within 1 s of the
// device connecting and then once a minute forever. The cost is not a broken
// connection (the frame is dropped, the link survives) but **the memory
// telemetry never reaches the database**, which matters because the firmware
// memory gate (main/mem_guard.c) judges on "largest" -- the same unit that
// nodes.largest_free_internal_bytes stores. Without this handler, "did the
// node pass its gate" is unanswerable from the server side.
//
// # Wire layout (frozen, mem_guard.c:mem_guard_encode_report)
//
//	1 free_bytes             varint
//	2 largest_free_bytes     varint
//	3 min_ever_bytes         varint
//	4 min_stack_high_water   varint
//	5 floor_bytes            varint
//
// All five are encoded unconditionally as varints; there are no sub-messages.
// Field presence is tracked so a schema-valid payload carrying only some
// fields does not silently overwrite good values with zero (same discipline as
// the StatusReport perf block: only write what the device actually sent).

// memReport is the decoded MSG_MEM_RPT payload.
type memReport struct {
	FreeBytes         uint64
	LargestFreeBytes  uint64
	MinEverBytes      uint64
	MinStackHighWater uint64
	FloorBytes        uint64

	// have* flags distinguish "device sent 0" from "device did not send it".
	// The firmware always sends all five, but a future/older revision may not,
	// and writing 0 over a known-good value would be a silent regression.
	haveFree, haveLargest, haveMinEver, haveMinStack, haveFloor bool
}

// handleMemReport processes MSG_MEM_RPT (0x21).
//
// Best-effort by design: memory telemetry is diagnostic, so a malformed frame
// is logged and dropped rather than closing the link. There is no ACK for this
// type (unlike DiagReport), so nothing is retried -- losing one sample is fine,
// the next one arrives in 60 s.
func (m *Manager) handleMemReport(deviceID string, payload []byte) {
	report, err := decodeMemReport(payload)
	if err != nil {
		logger.Warnf("[%s] Rejecting malformed MemReport: %v", deviceID, err)
		return
	}

	logger.Debugf("[%s] MemReport: free=%d largest=%d min_ever=%d min_stack=%d floor=%d",
		deviceID, report.FreeBytes, report.LargestFreeBytes, report.MinEverBytes,
		report.MinStackHighWater, report.FloorBytes)

	if err := m.persistMemReport(deviceID, report); err != nil {
		logger.Errorf("[%s] Failed to persist MemReport: %v", deviceID, err)
	}
}

// decodeMemReport parses the five-varint payload.
//
// Unknown field numbers are skipped (forward compatibility: the firmware may
// add more memory dimensions later) but a known field with the wrong wire type
// is rejected -- that is a genuine protocol violation, and guessing at its
// meaning is how a wrong number ends up in the database.
func decodeMemReport(payload []byte) (memReport, error) {
	dec, err := frame.NewDecoder(payload)
	if err != nil {
		return memReport{}, err
	}

	var report memReport
	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return memReport{}, err
		}

		switch field.FieldNum {
		case 1: // free_bytes
			v, err := memReportVarint(field, "free_bytes")
			if err != nil {
				return memReport{}, err
			}
			report.FreeBytes, report.haveFree = v, true
		case 2: // largest_free_bytes
			v, err := memReportVarint(field, "largest_free_bytes")
			if err != nil {
				return memReport{}, err
			}
			report.LargestFreeBytes, report.haveLargest = v, true
		case 3: // min_ever_bytes
			v, err := memReportVarint(field, "min_ever_bytes")
			if err != nil {
				return memReport{}, err
			}
			report.MinEverBytes, report.haveMinEver = v, true
		case 4: // min_stack_high_water
			v, err := memReportVarint(field, "min_stack_high_water")
			if err != nil {
				return memReport{}, err
			}
			report.MinStackHighWater, report.haveMinStack = v, true
		case 5: // floor_bytes
			v, err := memReportVarint(field, "floor_bytes")
			if err != nil {
				return memReport{}, err
			}
			report.FloorBytes, report.haveFloor = v, true
		default:
			// Forward compatibility: an unknown field is not an error.
			continue
		}
	}
	return report, nil
}

// memReportVarint reads one varint field, naming the field in the error so a
// rejected frame says which dimension was malformed.
func memReportVarint(field *frame.Field, name string) (uint64, error) {
	if field.WireType != frame.WireVarint {
		return 0, fmt.Errorf("%s wire type %d, want varint", name, field.WireType)
	}
	return frame.GetUint64(field), nil
}

// persistMemReport writes the memory dimensions that the device actually sent.
//
// Only free_heap_bytes and largest_free_internal_bytes have columns: they are
// the two the firmware gate and the UI read. The rest (min_ever,
// min_stack_high_water, floor) are folded into hardware_info.runtime_memory so
// they survive without a schema change, matching how the StatusReport perf
// block stores its extra dimensions.
//
// "Only write what was sent": an older firmware that sends neither column field
// must not zero them.
func (m *Manager) persistMemReport(deviceID string, report memReport) error {
	var node models.Node
	if err := m.db.Where("node_id = ?", deviceID).First(&node).Error; err != nil {
		logger.Warnf("[%s] Node not found for MemReport", deviceID)
		return nil
	}

	updates := map[string]interface{}{}
	if report.haveFree && report.FreeBytes <= 0x7FFFFFFF {
		updates["free_heap_bytes"] = int(report.FreeBytes)
	}
	// The gate unit. Guarded like the StatusReport field-29 path: never clobber
	// a known value with 0, and never write a value a signed column cannot hold.
	if report.haveLargest && report.LargestFreeBytes > 0 && report.LargestFreeBytes <= 0x7FFFFFFF {
		updates["largest_free_internal_bytes"] = int(report.LargestFreeBytes)
	}

	// Extra dimensions go into hardware_info.runtime_memory. Re-read the row
	// here so the merge does not use a stale snapshot: StatusReport rewrites
	// hardware_info.runtime_performance, and a lost update would drop fields.
	if report.haveMinEver || report.haveMinStack || report.haveFloor {
		hardwareInfo := map[string]interface{}{}
		if node.HardwareInfo != "" {
			if err := json.Unmarshal([]byte(node.HardwareInfo), &hardwareInfo); err != nil || hardwareInfo == nil {
				hardwareInfo = map[string]interface{}{}
			}
		}
		runtimeMemory := map[string]interface{}{}
		if prior, ok := hardwareInfo["runtime_memory"].(map[string]interface{}); ok && prior != nil {
			runtimeMemory = prior
		}
		if report.haveMinEver {
			runtimeMemory["min_ever_bytes"] = report.MinEverBytes
		}
		if report.haveMinStack {
			runtimeMemory["min_stack_high_water_bytes"] = report.MinStackHighWater
		}
		if report.haveFloor {
			runtimeMemory["floor_bytes"] = report.FloorBytes
		}
		hardwareInfo["runtime_memory"] = runtimeMemory
		if encoded, err := json.Marshal(hardwareInfo); err != nil {
			logger.Warnf("[%s] encode runtime_memory failed: %v", deviceID, err)
		} else {
			updates["hardware_info"] = string(encoded)
		}
	}

	if len(updates) == 0 {
		return nil
	}
	return m.db.Model(&models.Node{}).Where("id = ?", node.ID).Updates(updates).Error
}
