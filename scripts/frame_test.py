#!/usr/bin/env python3
"""
Binary Frame Protocol - Python Reference Implementation + Test Vectors
Phase 1: Verify encoding/decoding correctness
"""

from enum import IntEnum
from typing import List, Dict, Any, Optional, Callable
import struct
import json

class WireType(IntEnum):
    VARINT = 0
    FIXED64 = 1
    LENGTH_DELIMITED = 2
    START_GROUP = 3
    END_GROUP = 4
    FIXED32 = 5

def make_tag(field_num: int, wire_type: WireType) -> int:
    return (field_num << 3) | wire_type

def encode_varint(value: int) -> bytes:
    """Encode unsigned integer as varint."""
    result = bytearray()
    while value > 0x7F:
        result.append((value & 0x7F) | 0x80)
        value >>= 7
    result.append(value & 0x7F)
    return bytes(result)

def decode_varint(data: bytes, offset: int = 0) -> tuple:
    """Decode varint from data at offset. Returns (value, next_offset)."""
    result = 0
    shift = 0
    pos = offset
    while True:
        if pos >= len(data):
            raise ValueError("Incomplete varint")
        byte = data[pos]
        result |= (byte & 0x7F) << shift
        pos += 1
        if not (byte & 0x80):
            break
        shift += 7
        if shift >= 64:
            raise ValueError("Varint too long")
    return result, pos

def encode_field(field_num: int, wire_type: WireType, value) -> bytes:
    """Encode a single field."""
    tag = make_tag(field_num, wire_type)
    result = encode_varint(tag)
    
    if wire_type == WireType.VARINT:
        if isinstance(value, bool):
            result += encode_varint(1 if value else 0)
        else:
            result += encode_varint(value)
    elif wire_type == WireType.LENGTH_DELIMITED:
        if isinstance(value, str):
            value = value.encode('utf-8')
        result += encode_varint(len(value))
        result += value
    else:
        raise ValueError(f"Unsupported wire type: {wire_type}")
    
    return result

def parse_frame(data: bytes) -> tuple:
    """Parse a frame, returns (msg_type, fields_dict)."""
    if len(data) < 1:
        raise ValueError("Empty frame")
    
    msg_type = data[0]
    offset = 1
    fields = {}
    
    while offset < len(data):
        tag, offset = decode_varint(data, offset)
        field_num = tag >> 3
        wire_type = tag & 0x07
        
        if wire_type == WireType.VARINT:
            value, offset = decode_varint(data, offset)
            fields[field_num] = value
        elif wire_type == WireType.LENGTH_DELIMITED:
            length, offset = decode_varint(data, offset)
            if offset + length > len(data):
                raise ValueError("Length-delimited field exceeds frame")
            value = data[offset:offset + length]
            offset += length
            fields[field_num] = value
        else:
            raise ValueError(f"Unsupported wire type in frame: {wire_type}")
    
    return msg_type, fields

class FrameEncoder:
    """Helper to build frames."""
    def __init__(self, msg_type: int):
        self.msg_type = msg_type
        self.data = bytearray()
        self.data.append(msg_type)
    
    def add_varint(self, field_num: int, value: int):
        self.data.extend(encode_field(field_num, WireType.VARINT, value))
        return self
    
    def add_string(self, field_num: int, value: str):
        self.data.extend(encode_field(field_num, WireType.LENGTH_DELIMITED, value))
        return self
    
    def add_bytes(self, field_num: int, value: bytes):
        self.data.extend(encode_field(field_num, WireType.LENGTH_DELIMITED, value))
        return self
    
    def build(self) -> bytes:
        return bytes(self.data)

# ===== 消息类型常量 =====
MSG_HELLO       = 0x01
MSG_STATUS_RPT  = 0x02
MSG_DATA_RPT    = 0x03
MSG_CONFIG_MFST = 0x04
MSG_CONFIG_RSLT = 0x05
MSG_WRITE_CMD   = 0x06
MSG_WRITE_RSP   = 0x07
MSG_PING        = 0x08
MSG_PONG        = 0x09
MSG_OTA_CMD     = 0x0A
MSG_OTA_PROG    = 0x0B
MSG_SCAN_RPT    = 0x0C
MSG_SCAN_REQ    = 0x0D
MSG_QUERY_REQ   = 0x0E
MSG_QUERY_RSP   = 0x0F
MSG_CONFIG_MFST_PY = 0x04  # alias to avoid name clash
MSG_CONFIG_QUERY = 0x10
MSG_CONFIG_REPORT = 0x11
MSG_HELLO_ACK   = 0x12

# ===== 测试向量 =====
def test_hello():
    print("=== Test: Hello ===")
    encoder = FrameEncoder(MSG_HELLO)
    encoder.add_string(1, "AABBCCDDEEFF")
    encoder.add_string(2, "2.0.0")
    encoder.add_string(3, "ESP32S3")
    encoder.add_varint(4, 4)
    frame = encoder.build()
    
    print(f"Encoded ({len(frame)} bytes): {frame.hex()}")
    
    msg_type, fields = parse_frame(frame)
    print(f"Type: 0x{msg_type:02X}")
    print(f"Field 1 (device_id): {fields[1].decode('utf-8')}")
    print(f"Field 2 (firmware): {fields[2].decode('utf-8')}")
    print(f"Field 3 (model): {fields[3].decode('utf-8')}")
    print(f"Field 4 (channel_count): {fields[4]}")
    
    assert msg_type == MSG_HELLO
    assert fields[1] == b"AABBCCDDEEFF"
    assert fields[2] == b"2.0.0"
    assert fields[3] == b"ESP32S3"
    assert fields[4] == 4
    print("PASS")
    return frame

def test_data_report():
    print("\n=== Test: DataReport ===")
    raw = bytes([0x01, 0x02, 0x03, 0x04, 0x05])
    encoder = FrameEncoder(MSG_DATA_RPT)
    encoder.add_varint(1, 1)
    encoder.add_varint(2, 12345678)
    encoder.add_varint(3, 42)
    encoder.add_bytes(4, raw)
    frame = encoder.build()
    
    print(f"Encoded ({len(frame)} bytes): {frame.hex()}")
    
    msg_type, fields = parse_frame(frame)
    print(f"Type: 0x{msg_type:02X}")
    print(f"Field 1 (channel_id): {fields[1]}")
    print(f"Field 2 (timestamp_us): {fields[2]}")
    print(f"Field 3 (sequence): {fields[3]}")
    print(f"Field 4 (raw_data): {fields[4].hex()}")
    
    assert msg_type == MSG_DATA_RPT
    assert fields[1] == 1
    assert fields[2] == 12345678
    assert fields[3] == 42
    assert fields[4] == raw
    print("PASS")
    return frame

def test_varint_edge_cases():
    print("\n=== Test: Varint Edge Cases ===")
    test_cases = [
        (0, "00"),
        (1, "01"),
        (127, "7f"),
        (128, "80 01"),
        (255, "ff 01"),
    ]
    
    for value, expected_hex in test_cases:
        encoded = encode_varint(value)
        expected = bytes.fromhex(expected_hex.replace(" ", ""))
        assert encoded == expected, f"Failed for {value}: got {encoded.hex()}, expected {expected.hex()}"
        decoded, _ = decode_varint(encoded)
        assert decoded == value, f"Decode failed for {value}: got {decoded}"
        print(f"  varint({value}) = {encoded.hex()} OK")
    
    print("PASS")

def test_hello_ack():
    print("\n=== Test: HelloAck ===")
    server_time = 1717200000000  # Unix ms timestamp
    features = 0
    encoder = FrameEncoder(MSG_HELLO_ACK)
    encoder.add_varint(1, server_time)
    encoder.add_varint(2, features)
    frame = encoder.build()
    
    print(f"Encoded ({len(frame)} bytes): {frame.hex()}")
    
    msg_type, fields = parse_frame(frame)
    print(f"Type: 0x{msg_type:02X}")
    print(f"Field 1 (server_time): {fields[1]}")
    print(f"Field 2 (features): {fields[2]}")
    
    assert msg_type == MSG_HELLO_ACK
    assert fields[1] == server_time
    assert fields[2] == features
    print("PASS")
    return frame

if __name__ == "__main__":
    print("=" * 50)
    print("Binary Frame Protocol - Python Reference Implementation")
    print("=" * 50)
    
    test_varint_edge_cases()
    test_hello()
    test_data_report()
    test_hello_ack()
    
    print("\n" + "=" * 50)
    print("All tests PASSED")
    print("=" * 50)
