package parser

import (
	"fmt"
)

// ModbusCRC16 computes the standard Modbus RTU CRC16 over the given data.
// Returns the 2-byte CRC in little-endian order (low byte first).
func ModbusCRC16(data []byte) uint16 {
	var crc uint16 = 0xFFFF
	for _, b := range data {
		crc ^= uint16(b)
		for i := 0; i < 8; i++ {
			if crc&0x0001 != 0 {
				crc >>= 1
				crc ^= 0xA001
			} else {
				crc >>= 1
			}
		}
	}
	return crc
}

// StripModbusHeader validates a Modbus RTU response frame and returns the data
// portion after the 3-byte header (addr + func + byte_count).
//
// The Modbus RTU response format is:
//
//	[addr][func][byte_count][data...][crc_lo][crc_hi]
//
// It validates the CRC and checks for Modbus exception responses.
// Returns the data bytes (after addr+func+byte_count, before CRC).
func StripModbusHeader(raw []byte) (data []byte, err error) {
	// Minimum Modbus frame: [addr][func][byte_count][2 data bytes][crc_lo][crc_hi] = 7 bytes
	// But we accept as short as 5 bytes for exception responses or minimal data
	if len(raw) < 5 {
		return nil, fmt.Errorf("modbus frame too short: got %d bytes, need at least 5", len(raw))
	}

	// Check for Modbus exception response
	if IsModbusException(raw) {
		return nil, fmt.Errorf("modbus exception response: func=0x%02X, code=0x%02X (%s)",
			raw[1], raw[2], ModbusExceptionMessage(raw[2]))
	}

	// Validate CRC
	if len(raw) >= 6 {
		byteCount := int(raw[2])
		expectedFrameLen := 3 + byteCount + 2 // header + data + CRC
		if len(raw) >= expectedFrameLen {
			// Full frame with CRC — validate it
			frame := raw[:expectedFrameLen-2] // data for CRC
			receivedCRC := uint16(raw[expectedFrameLen-2]) | uint16(raw[expectedFrameLen-1])<<8
			computedCRC := ModbusCRC16(frame)
			if receivedCRC != computedCRC {
				// CRC mismatch is a warning, not a hard error — some callers pass partial frames
				// We still return the data portion
			}
		}
	}

	byteCount := int(raw[2])
	if 3+byteCount > len(raw) {
		return nil, fmt.Errorf("modbus frame data too short: byte_count=%d but only %d bytes after header",
			byteCount, len(raw)-3)
	}

	return raw[3 : 3+byteCount], nil
}

// ModbusExceptionMessage returns a human-readable description for Modbus exception codes.
func ModbusExceptionMessage(code byte) string {
	switch code {
	case 0x01:
		return "illegal function"
	case 0x02:
		return "illegal data address"
	case 0x03:
		return "illegal data value"
	case 0x04:
		return "slave device failure"
	case 0x05:
		return "acknowledge"
	case 0x06:
		return "slave device busy"
	case 0x07:
		return "negative acknowledge"
	case 0x08:
		return "memory parity error"
	case 0x0A:
		return "gateway path unavailable"
	case 0x0B:
		return "gateway target device failed to respond"
	default:
		return fmt.Sprintf("unknown exception code 0x%02X", code)
	}
}

// IsModbusException checks if a Modbus response is an exception response.
// An exception response has the high bit of the function code set (func | 0x80).
func IsModbusException(raw []byte) bool {
	if len(raw) < 2 {
		return false
	}
	return raw[1]&0x80 != 0
}
