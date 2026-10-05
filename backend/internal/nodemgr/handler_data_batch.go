package nodemgr

import (
	"errors"
	"fmt"

	"ehome/backend/pkg/frame"
	"ehome/backend/pkg/logger"
	"ehome/backend/pkg/metrics"
)

// =============================================================================
// DataBatch (0x20) — V3-2a, wire layout frozen in
// docs/设计/V3-2a-DataBatch-落地契约-2026-10-05.md §2.
//
// The device batches 1..4 【非关键】periodic telemetry samples of ONE
// channel/edge/template into a single frame. The backend's job is to
// **fan the frame back out into one DataEvent per sample**, semantically
// identical to the 0x03 path, so no downstream consumer and no frontend code
// needs to learn about batching.
//
// Fail-closed by construction: every invariant below rejects the WHOLE frame
// (never a prefix, never a truncation), because a partially applied batch would
// silently rewrite device history. Rejections are counted, not swallowed.
// Unknown field numbers are skipped so a future firmware can add fields
// without a coordinated backend release.
// =============================================================================

// DataBatch top-level field numbers (§2).
const (
	dataBatchFieldCount             uint8 = 1
	dataBatchFieldBaseTimestampUS   uint8 = 2
	dataBatchFieldFirstSequence     uint8 = 3
	dataBatchFieldChannelID         uint8 = 4
	dataBatchFieldSample            uint8 = 5 // repeated, count times
	dataBatchFieldEdgeDeviceID      uint8 = 6 // optional, mirrors 0x03 field 7
	dataBatchFieldCommandTemplateID uint8 = 7 // optional, mirrors 0x03 field 9
	dataBatchFieldCommandIndex      uint8 = 8 // optional, mirrors 0x03 field 8
)

// DataBatch sample sub-message field numbers (§2).
const (
	dataBatchSampleFieldDeltaUS uint8 = 1
	dataBatchSampleFieldRawData uint8 = 2
)

const (
	// dataBatchMaxSamples is the frozen upper bound of §2.1-3. A larger count is
	// a protocol violation, not a big batch.
	dataBatchMaxSamples = 4
	// dataBatchMaxRawBytes is the frozen per-sample payload budget (§2.1-5).
	dataBatchMaxRawBytes = 1024
)

// dataBatchSample is one decoded sample sub-message.
type dataBatchSample struct {
	deltaUS uint64
	rawData []byte
}

// parsedDataBatch is a fully validated DataBatch frame. The parser returns
// nothing unless every §2.1 invariant holds, so callers cannot observe a
// half-valid batch.
type parsedDataBatch struct {
	count             uint64
	baseTimestampUS   uint64
	firstSequence     uint64
	channelID         uint64
	samples           []dataBatchSample
	edgeDeviceID      uint64
	commandTemplateID uint64
	commandIndex      uint64
}

// parseDataBatch strictly decodes a DataBatch (0x20) frame.
//
// Rejected (whole frame): malformed varints/tags, count outside 1..4, count
// disagreeing with the number of field 5 occurrences, a missing required field,
// a duplicate known field, a sample missing delta_us/raw_data, an empty or
// >1024 B raw_data, a first sample with delta_us != 0, a later sample with
// delta_us == 0, and non-monotonic deltas.
//
// Accepted: unknown field numbers anywhere (skipped, forward compatible).
func parseDataBatch(payload []byte) (parsedDataBatch, error) {
	var batch parsedDataBatch

	dec, err := frame.NewDecoder(payload)
	if err != nil {
		return batch, fmt.Errorf("invalid DataBatch frame: %w", err)
	}
	if dec.MsgType() != frame.MsgDataBatch {
		return batch, fmt.Errorf("invalid DataBatch message type 0x%02X", dec.MsgType())
	}

	seen := [dataBatchFieldCommandIndex + 1]bool{}
	var haveCount, haveBase, haveFirstSequence, haveChannel bool

	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return batch, fmt.Errorf("malformed DataBatch fields: %w", err)
		}

		// §2.1-7: an unknown field number is skipped. This check MUST run before
		// the duplicate test — a future field repeating is that future field's
		// business, not a violation of this frozen contract.
		if field.FieldNum < dataBatchFieldCount || field.FieldNum > dataBatchFieldCommandIndex {
			continue
		}
		// Field 5 is repeated (sample x count): it is the ONE known field that is
		// allowed — required, even — to appear more than once. Rejecting the second
		// occurrence here would make every multi-sample batch unparseable, which is
		// precisely the case this feature exists for. The duplicate rule applies to
		// the batch-level scalar fields only.
		if field.FieldNum == dataBatchFieldSample {
			if field.WireType != frame.WireLengthDelimited {
				return batch, fmt.Errorf("invalid DataBatch sample wire type %d", field.WireType)
			}
			sample, err := decodeDataBatchSample(frame.GetBytes(field))
			if err != nil {
				return batch, fmt.Errorf("invalid DataBatch sample %d: %w", len(batch.samples), err)
			}
			batch.samples = append(batch.samples, sample)
			continue
		}
		if seen[field.FieldNum] {
			return batch, fmt.Errorf("invalid DataBatch field %d: duplicate", field.FieldNum)
		}
		seen[field.FieldNum] = true

		switch field.FieldNum {
		case dataBatchFieldCount:
			if field.WireType != frame.WireVarint {
				return batch, fmt.Errorf("invalid DataBatch count wire type %d", field.WireType)
			}
			batch.count = frame.GetUint64(field)
			haveCount = true

		case dataBatchFieldBaseTimestampUS:
			if field.WireType != frame.WireVarint {
				return batch, fmt.Errorf("invalid DataBatch base_timestamp_us wire type %d", field.WireType)
			}
			batch.baseTimestampUS = frame.GetUint64(field)
			haveBase = true

		case dataBatchFieldFirstSequence:
			if field.WireType != frame.WireVarint {
				return batch, fmt.Errorf("invalid DataBatch first_sequence wire type %d", field.WireType)
			}
			batch.firstSequence = frame.GetUint64(field)
			haveFirstSequence = true

		case dataBatchFieldChannelID:
			if field.WireType != frame.WireVarint {
				return batch, fmt.Errorf("invalid DataBatch channel_id wire type %d", field.WireType)
			}
			batch.channelID = frame.GetUint64(field)
			haveChannel = true

		case dataBatchFieldEdgeDeviceID:
			if field.WireType != frame.WireVarint {
				return batch, fmt.Errorf("invalid DataBatch edge_device_id wire type %d", field.WireType)
			}
			batch.edgeDeviceID = frame.GetUint64(field)

		case dataBatchFieldCommandTemplateID:
			if field.WireType != frame.WireVarint {
				return batch, fmt.Errorf("invalid DataBatch command_template_id wire type %d", field.WireType)
			}
			batch.commandTemplateID = frame.GetUint64(field)

		case dataBatchFieldCommandIndex:
			if field.WireType != frame.WireVarint {
				return batch, fmt.Errorf("invalid DataBatch command_index wire type %d", field.WireType)
			}
			batch.commandIndex = frame.GetUint64(field)
		}
	}

	if !haveCount || !haveBase || !haveFirstSequence || !haveChannel {
		return batch, fmt.Errorf("invalid DataBatch: missing required field (count=%v base=%v sequence=%v channel=%v)",
			haveCount, haveBase, haveFirstSequence, haveChannel)
	}

	// §2.1-3: range 1..4. Checked before the count/sample comparison so a
	// nonsense count is reported as out-of-range rather than as a mismatch.
	if batch.count < 1 || batch.count > dataBatchMaxSamples {
		return batch, fmt.Errorf("invalid DataBatch count %d: out of range 1..%d", batch.count, dataBatchMaxSamples)
	}
	// §2.1-2: fail-closed on any disagreement between the declared count and the
	// number of field 5 occurrences (too few AND too many are both violations).
	if uint64(len(batch.samples)) != batch.count {
		return batch, fmt.Errorf("invalid DataBatch: count %d does not match %d sample(s)",
			batch.count, len(batch.samples))
	}

	// §2.1-4: the first sample is the batch origin (delta 0); every later sample
	// must move forward and the running offsets must not decrease.
	for i, sample := range batch.samples {
		if i == 0 {
			if sample.deltaUS != 0 {
				return batch, fmt.Errorf("invalid DataBatch: first sample delta_us %d, want 0", sample.deltaUS)
			}
			continue
		}
		if sample.deltaUS == 0 {
			return batch, fmt.Errorf("invalid DataBatch: sample %d delta_us is zero", i)
		}
		if sample.deltaUS < batch.samples[i-1].deltaUS {
			return batch, fmt.Errorf("invalid DataBatch: sample %d delta_us %d is not monotonic (previous %d)",
				i, sample.deltaUS, batch.samples[i-1].deltaUS)
		}
	}

	return batch, nil
}

// decodeDataBatchSample decodes one field 5 sub-message.
//
// The sub-message is a pure field sequence (no message-type byte), so it is read
// with NewSubDecoder. An empty sub-message is rejected by NewSubDecoder itself.
func decodeDataBatchSample(data []byte) (dataBatchSample, error) {
	var sample dataBatchSample

	dec, err := frame.NewSubDecoder(data)
	if err != nil {
		return sample, err
	}

	seen := [dataBatchSampleFieldRawData + 1]bool{}
	var haveDelta, haveRaw bool

	for {
		field, err := dec.NextField()
		if errors.Is(err, frame.ErrEndOfFrame) {
			break
		}
		if err != nil {
			return sample, err
		}
		if field.FieldNum < dataBatchSampleFieldDeltaUS || field.FieldNum > dataBatchSampleFieldRawData {
			continue // unknown sub-field: skip (§2.1-7)
		}
		if seen[field.FieldNum] {
			return sample, fmt.Errorf("duplicate sample field %d", field.FieldNum)
		}
		seen[field.FieldNum] = true

		switch field.FieldNum {
		case dataBatchSampleFieldDeltaUS:
			if field.WireType != frame.WireVarint {
				return sample, fmt.Errorf("invalid delta_us wire type %d", field.WireType)
			}
			sample.deltaUS = frame.GetUint64(field)
			haveDelta = true

		case dataBatchSampleFieldRawData:
			if field.WireType != frame.WireLengthDelimited {
				return sample, fmt.Errorf("invalid raw_data wire type %d", field.WireType)
			}
			sample.rawData = frame.GetBytes(field)
			haveRaw = true
		}
	}

	if !haveDelta || !haveRaw {
		return sample, fmt.Errorf("sample requires delta_us and raw_data")
	}
	// §2.1-5: an empty payload carries no reading; >1024 B breaks the frame
	// budget the firmware encoder sizes against.
	if len(sample.rawData) == 0 {
		return sample, fmt.Errorf("empty raw_data")
	}
	if len(sample.rawData) > dataBatchMaxRawBytes {
		return sample, fmt.Errorf("raw_data %d bytes exceeds %d", len(sample.rawData), dataBatchMaxRawBytes)
	}

	return sample, nil
}

// handleDataBatch processes DataBatch (0x20) and fans it out by sample.
//
// Semantic identity with 0x03 is enforced structurally, not by convention: every
// sample is converted into the same dataReportJob type that handleDataReport
// builds and is submitted through the same enqueueDataReportJob backpressure
// path. processDataReportJob then turns each job into a DataEvent, so the
// timestamp/sequence/edge addressing and the downstream
// ShouldPersist/ShouldParse decisions cannot drift between the two paths.
//
// Timestamp = base_timestamp_us + delta_us; sequence = first_sequence + i.
// error_code and request_id are 0 by contract §2.1-6 (critical samples always
// travel alone on 0x03), which is exactly the 0x03 "non-critical periodic
// sample" shape (err=0, req=0, edge_device_id != 0).
func (m *Manager) handleDataBatch(deviceID string, payload []byte) {
	batch, err := parseDataBatch(payload)
	if err != nil {
		metrics.DataBatchRejectedTotal.Inc()
		logger.Warnf("[%s] Rejecting DataBatch frame: %v", deviceID, err)
		return
	}

	metrics.DataBatchFramesTotal.Inc()
	metrics.DataBatchSamplesTotal.Add(float64(len(batch.samples)))

	for i, sample := range batch.samples {
		// Each fanned-out sample is one data report in its own right; keeping
		// DataReceivedTotal in step with the 0x03 path means node-level volume
		// dashboards do not silently drop when batching turns on.
		metrics.DataReceivedTotal.WithLabelValues(deviceID, "ok").Inc()

		m.enqueueDataReportJob(deviceID, dataReportJob{
			deviceID:          deviceID,
			channelID:         batch.channelID,
			timestamp:         batch.baseTimestampUS + sample.deltaUS,
			sequence:          batch.firstSequence + uint64(i),
			rawData:           sample.rawData,
			errorCode:         0,
			requestID:         0,
			edgeDeviceID:      batch.edgeDeviceID,
			commandIndex:      batch.commandIndex,
			commandTemplateID: batch.commandTemplateID,
		})
	}

	logger.Debugf("[%s] DataBatch: ch=%d base_ts=%d first_seq=%d n=%d edge=%d cmd=%d template=%d",
		deviceID, batch.channelID, batch.baseTimestampUS, batch.firstSequence,
		len(batch.samples), batch.edgeDeviceID, batch.commandIndex, batch.commandTemplateID)
}
