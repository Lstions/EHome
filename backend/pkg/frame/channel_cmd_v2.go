package frame

import (
	"errors"
	"fmt"
	"math"
)

const (
	ChannelCmdV2ProtocolVersion uint32 = 1
	ChannelCmdV2MaxTX                  = 128
	ChannelCmdV2MaxRX                  = 256
	ChannelCmdV2MaxTimeoutMS           = 30000
	ChannelCmdV2MaxBatchSteps          = 8
	ChannelCmdV2MaxBatchBytes          = 512
)

// ChannelCmdV2Step is one fixed physical transaction in a bounded batch.
// The step is server-compiled; it is never accepted from browser-provided
// wire bytes without passing the trusted driver and capability checks.
type ChannelCmdV2Step struct {
	Kind          uint32
	TXData        []byte
	ReadSize      uint32
	RXTimeoutMS   uint32
	PostTXDelayMS uint32
}

// ChannelCmdV2 is the vendor-neutral, bounded physical transaction envelope.
// The IDs are byte arrays on purpose: command identity and physical digest are
// not user-controlled strings on the wire.
type ChannelCmdV2 struct {
	CommandID      [16]byte
	PayloadDigest  [16]byte
	Attempt        uint32
	BootID         string
	EdgeDeviceID   uint32
	ChannelID      uint32
	DeadlineUnixMS uint64
	TXData         []byte
	ReadSize       uint32
	RXTimeoutMS    uint32
	PostTXDelayMS  uint32
	RiskClass      uint32 // Phase 2 accepts low risk (0) only.
	Flags          uint32 // Phase 2 requires zero.
	Plan           []ChannelCmdV2Step
}

// ChannelCmdV2Response is the identity-bearing Ack or Final envelope. The
// device never sends business semantics here; RawResponse is handed to the
// trusted driver on the server only after this identity is verified.
type ChannelCmdV2Response struct {
	CommandID     [16]byte
	PayloadDigest [16]byte
	Attempt       uint32
	BootID        string
	EventSequence uint64
	Success       bool
	ErrorCode     uint32
	RawResponse   []byte
	Replayed      bool
	Final         bool
}

func EncodeChannelCmdV2(cmd ChannelCmdV2) ([]byte, error) {
	if err := validateChannelCmdV2(cmd); err != nil {
		return nil, err
	}
	enc := NewEncoder(MsgChannelCmdV2)
	enc.EncodeBytes(1, cmd.CommandID[:])
	enc.EncodeBytes(2, cmd.PayloadDigest[:])
	enc.EncodeVarint(3, uint64(cmd.Attempt))
	enc.EncodeString(4, cmd.BootID)
	enc.EncodeVarint(5, uint64(cmd.EdgeDeviceID))
	enc.EncodeVarint(6, uint64(cmd.ChannelID))
	enc.EncodeVarint(7, cmd.DeadlineUnixMS)
	enc.EncodeBytes(8, cmd.TXData)
	enc.EncodeVarint(9, uint64(cmd.ReadSize))
	enc.EncodeVarint(10, uint64(cmd.RXTimeoutMS))
	enc.EncodeVarint(11, uint64(cmd.PostTXDelayMS))
	enc.EncodeVarint(12, uint64(cmd.RiskClass))
	enc.EncodeVarint(13, uint64(cmd.Flags))
	enc.EncodeVarint(14, uint64(ChannelCmdV2ProtocolVersion))
	for _, step := range cmd.Plan {
		sub := SubEncoder()
		sub.EncodeVarint(1, uint64(step.Kind))
		sub.EncodeBytes(2, step.TXData)
		sub.EncodeVarint(3, uint64(step.ReadSize))
		sub.EncodeVarint(4, uint64(step.RXTimeoutMS))
		sub.EncodeVarint(5, uint64(step.PostTXDelayMS))
		enc.EncodeSubFrame(15, sub.Bytes())
	}
	return enc.Bytes(), nil
}

func DecodeChannelCmdV2(payload []byte) (ChannelCmdV2, error) {
	var cmd ChannelCmdV2
	dec, err := NewDecoder(payload)
	if err != nil {
		return cmd, err
	}
	if dec.MsgType() != MsgChannelCmdV2 {
		return cmd, fmt.Errorf("unexpected message type 0x%02x", dec.MsgType())
	}
	seen := [16]bool{}
	for {
		field, err := dec.NextField()
		if errors.Is(err, ErrEndOfFrame) {
			break
		}
		if err != nil {
			return cmd, err
		}
		if field.FieldNum == 0 || field.FieldNum > 15 || (field.FieldNum != 15 && seen[field.FieldNum]) {
			return cmd, fmt.Errorf("unknown or duplicate field %d", field.FieldNum)
		}
		if field.FieldNum == 15 {
			if field.WireType != WireLengthDelimited || len(cmd.Plan) >= ChannelCmdV2MaxBatchSteps {
				return cmd, fmt.Errorf("invalid batch step")
			}
			step, err := decodeChannelCmdV2Step(GetBytes(field))
			if err != nil {
				return cmd, err
			}
			cmd.Plan = append(cmd.Plan, step)
			continue
		}
		seen[field.FieldNum] = true
		switch field.FieldNum {
		case 1, 2, 4, 8:
			if field.WireType != WireLengthDelimited {
				return cmd, fmt.Errorf("field %d wire type", field.FieldNum)
			}
			data := GetBytes(field)
			switch field.FieldNum {
			case 1:
				if len(data) != 16 {
					return cmd, fmt.Errorf("command_id length")
				}
				copy(cmd.CommandID[:], data)
			case 2:
				if len(data) != 16 {
					return cmd, fmt.Errorf("payload_digest length")
				}
				copy(cmd.PayloadDigest[:], data)
			case 4:
				cmd.BootID = string(data)
			case 8:
				cmd.TXData = append([]byte(nil), data...)
			}
		default:
			if field.WireType != WireVarint {
				return cmd, fmt.Errorf("field %d wire type", field.FieldNum)
			}
			value := GetUint64(field)
			if field.FieldNum != 7 && value > math.MaxUint32 {
				return cmd, fmt.Errorf("field %d overflow", field.FieldNum)
			}
			switch field.FieldNum {
			case 3:
				cmd.Attempt = uint32(value)
			case 5:
				cmd.EdgeDeviceID = uint32(value)
			case 6:
				cmd.ChannelID = uint32(value)
			case 7:
				cmd.DeadlineUnixMS = value
			case 9:
				cmd.ReadSize = uint32(value)
			case 10:
				cmd.RXTimeoutMS = uint32(value)
			case 11:
				cmd.PostTXDelayMS = uint32(value)
			case 12:
				cmd.RiskClass = uint32(value)
			case 13:
				cmd.Flags = uint32(value)
			case 14:
				if value != uint64(ChannelCmdV2ProtocolVersion) {
					return cmd, fmt.Errorf("protocol version %d", value)
				}
			}
		}
	}
	for i := uint8(1); i <= 14; i++ {
		if !seen[i] {
			return cmd, fmt.Errorf("missing field %d", i)
		}
	}
	if err := validateChannelCmdV2(cmd); err != nil {
		return cmd, err
	}
	return cmd, nil
}

func decodeChannelCmdV2Step(data []byte) (ChannelCmdV2Step, error) {
	var step ChannelCmdV2Step
	dec, err := NewSubDecoder(data)
	if err != nil {
		return step, err
	}
	seen := [6]bool{}
	for {
		field, err := dec.NextField()
		if errors.Is(err, ErrEndOfFrame) {
			break
		}
		if err != nil {
			return step, err
		}
		if field.FieldNum == 0 || field.FieldNum > 5 || seen[field.FieldNum] {
			return step, fmt.Errorf("invalid batch step field %d", field.FieldNum)
		}
		seen[field.FieldNum] = true
		if field.FieldNum == 2 {
			if field.WireType != WireLengthDelimited {
				return step, fmt.Errorf("batch tx wire type")
			}
			step.TXData = append([]byte(nil), GetBytes(field)...)
			continue
		}
		if field.WireType != WireVarint {
			return step, fmt.Errorf("batch step field %d wire type", field.FieldNum)
		}
		value := GetUint64(field)
		if value > uint64(^uint32(0)) {
			return step, fmt.Errorf("batch step field %d overflow", field.FieldNum)
		}
		switch field.FieldNum {
		case 1:
			step.Kind = uint32(value)
		case 3:
			step.ReadSize = uint32(value)
		case 4:
			step.RXTimeoutMS = uint32(value)
		case 5:
			step.PostTXDelayMS = uint32(value)
		}
	}
	for i := uint8(1); i <= 5; i++ {
		if !seen[i] {
			return step, fmt.Errorf("missing batch step field %d", i)
		}
	}
	if err := validateChannelCmdV2Step(step); err != nil {
		return step, err
	}
	return step, nil
}

func DecodeChannelCmdV2Response(payload []byte) (ChannelCmdV2Response, error) {
	var response ChannelCmdV2Response
	dec, err := NewDecoder(payload)
	if err != nil {
		return response, err
	}
	response.Final = dec.MsgType() == MsgChannelCmdV2Final
	if dec.MsgType() != MsgChannelCmdV2Ack && !response.Final {
		return response, fmt.Errorf("unexpected message type 0x%02x", dec.MsgType())
	}
	seen := [10]bool{}
	for {
		field, err := dec.NextField()
		if errors.Is(err, ErrEndOfFrame) {
			break
		}
		if err != nil {
			return response, err
		}
		if field.FieldNum == 0 || field.FieldNum > 9 || seen[field.FieldNum] || (!response.Final && field.FieldNum > 7) {
			return response, fmt.Errorf("unknown or duplicate field %d", field.FieldNum)
		}
		seen[field.FieldNum] = true
		switch field.FieldNum {
		case 1, 2, 4, 8:
			if field.WireType != WireLengthDelimited {
				return response, fmt.Errorf("field %d wire type", field.FieldNum)
			}
			data := GetBytes(field)
			switch field.FieldNum {
			case 1:
				if len(data) != 16 {
					return response, fmt.Errorf("command_id length")
				}
				copy(response.CommandID[:], data)
			case 2:
				if len(data) != 16 {
					return response, fmt.Errorf("payload_digest length")
				}
				copy(response.PayloadDigest[:], data)
			case 4:
				response.BootID = string(data)
			case 8:
				if len(data) > ChannelCmdV2MaxRX {
					return response, fmt.Errorf("raw_response length")
				}
				response.RawResponse = append([]byte(nil), data...)
			}
		default:
			if field.WireType != WireVarint {
				return response, fmt.Errorf("field %d wire type", field.FieldNum)
			}
			value := GetUint64(field)
			switch field.FieldNum {
			case 3:
				if value == 0 || value > math.MaxUint32 {
					return response, fmt.Errorf("attempt")
				}
				response.Attempt = uint32(value)
			case 5:
				if value == 0 {
					return response, fmt.Errorf("event_sequence")
				}
				response.EventSequence = value
			case 6:
				if value > 1 {
					return response, fmt.Errorf("non-canonical success")
				}
				response.Success = value == 1
			case 7:
				if value > math.MaxUint32 {
					return response, fmt.Errorf("error_code")
				}
				response.ErrorCode = uint32(value)
			case 9:
				if value > 1 {
					return response, fmt.Errorf("non-canonical replayed")
				}
				response.Replayed = value == 1
			}
		}
	}
	for i := uint8(1); i <= 7; i++ {
		if !seen[i] {
			return response, fmt.Errorf("missing field %d", i)
		}
	}
	if len(response.BootID) == 0 || len(response.BootID) > 32 {
		return response, fmt.Errorf("boot_id length")
	}
	if response.Success && response.ErrorCode != 0 {
		return response, fmt.Errorf("successful response has error")
	}
	return response, nil
}

func validateChannelCmdV2(cmd ChannelCmdV2) error {
	if cmd.Attempt == 0 || cmd.EdgeDeviceID == 0 || cmd.ChannelID == 0 || cmd.DeadlineUnixMS == 0 {
		return fmt.Errorf("non-zero identity fields required")
	}
	if len(cmd.BootID) == 0 || len(cmd.BootID) > 32 {
		return fmt.Errorf("boot_id length")
	}
	// ESP32 firmware requires all top-level identity fields (1-14) to be
	// present and non-zero.  The transport populates them from the first
	// plan step (bounded batch) or from the compiled single step so the
	// envelope passes firmware validation.
	if len(cmd.TXData) == 0 || len(cmd.TXData) > ChannelCmdV2MaxTX || cmd.ReadSize > ChannelCmdV2MaxRX || cmd.RXTimeoutMS == 0 || cmd.RXTimeoutMS > ChannelCmdV2MaxTimeoutMS || cmd.PostTXDelayMS > ChannelCmdV2MaxTimeoutMS {
		return fmt.Errorf("transaction fields invalid")
	}
	if cmd.RiskClass != 0 || cmd.Flags != 0 {
		return fmt.Errorf("unsupported risk class or flags")
	}
	if len(cmd.Plan) > ChannelCmdV2MaxBatchSteps {
		return fmt.Errorf("too many batch steps")
	}
	planBytes := 0
	for _, step := range cmd.Plan {
		if err := validateChannelCmdV2Step(step); err != nil {
			return err
		}
		// A nested step is bounded by its fixed fields plus the TX payload.
		planBytes += len(step.TXData) + 32
	}
	if planBytes > ChannelCmdV2MaxBatchBytes {
		return fmt.Errorf("batch plan exceeds %d bytes", ChannelCmdV2MaxBatchBytes)
	}
	return nil
}

func validateChannelCmdV2Step(step ChannelCmdV2Step) error {
	if step.Kind > 3 || len(step.TXData) == 0 || len(step.TXData) > ChannelCmdV2MaxTX ||
		step.ReadSize > ChannelCmdV2MaxRX || step.RXTimeoutMS == 0 ||
		step.RXTimeoutMS > ChannelCmdV2MaxTimeoutMS || step.PostTXDelayMS > ChannelCmdV2MaxTimeoutMS {
		return fmt.Errorf("bounded batch step fields invalid")
	}
	return nil
}
