#!/usr/bin/env python3
"""
EHomeSystem TCP Verification Script
- Phase 2: UART WriteCommand via TCP → verify CP210x receives data
- Phase 3: SPI ConfigManifest + WriteCommand via TCP → verify BMP280 chip ID
"""

import socket
import struct
import time
import sys
import serial

ESP_IP = "192.168.1.50"
TCP_PORT = 8088
CP210X_PORT = "/dev/ttyUSB0"
ESP_SERIAL = "/dev/ttyACM0"

# === Frame Protocol (protobuf-compatible binary encoding) ===
MSG_CONFIG_MFST = 0x04
MSG_WRITE_CMD = 0x06
MSG_CONFIG_REPORT = 0x11
MSG_QUERY_RESOURCES = 0x1A
MSG_RESOURCE_REPORT = 0x19

WIRE_VARINT = 0
WIRE_LENGTH_DELIMITED = 2


def make_tag(field_num, wire_type):
    return (field_num << 3) | (wire_type & 0x07)


def encode_varint(value):
    """Encode uint64 as protobuf varint bytes."""
    result = bytearray()
    while value > 0x7F:
        result.append((value & 0x7F) | 0x80)
        value >>= 7
    result.append(value & 0x7F)
    return bytes(result)


def encode_field_varint(field_num, value):
    """Encode a varint field as tag + value."""
    return bytes([make_tag(field_num, WIRE_VARINT)]) + encode_varint(value)


def encode_field_bytes(field_num, data):
    """Encode a length-delimited (bytes/string) field as tag + length + data."""
    result = bytes([make_tag(field_num, WIRE_LENGTH_DELIMITED)])
    result += encode_varint(len(data))
    result += data
    return result


def build_frame(msg_type, fields=b""):
    """Build a complete frame: [msg_type_byte] + [fields...]"""
    return bytes([msg_type]) + fields


def build_write_cmd(channel_id, data, request_id=1, read_size=0):
    """Build a WriteCommand frame (MSG_WRITE_CMD = 0x06)."""
    fields = b""
    fields += encode_field_varint(1, request_id)
    fields += encode_field_varint(2, channel_id)
    if data:
        fields += encode_field_bytes(3, data)
    if read_size > 0:
        fields += encode_field_varint(4, read_size)
    return build_frame(MSG_WRITE_CMD, fields)


def build_config_manifest(manifest_id, channels):
    """
    Build a ConfigManifest frame (MSG_CONFIG_MFST = 0x04).
    
    ConfigManifest fields:
      Field 1: manifest_id (string)
      Field 3: templates (sub-message, one per template)  ← NOTE: field 3, not 2!
      Field 4: channels (sub-message, one per channel)
    
    Channel sub-message fields:
      Field 1: id (varint)
      Field 2: hardware_id (bytes — sent as string)
      Field 3: template_ids (repeated varint)
      Field 4: interval_ms (varint)
      Field 5: enabled (varint, 0/1)
      Field 6: bus_type (varint, 1=UART, 2=I2C, 3=SPI)
      Field 7: bus_config (bytes — binary pin/baud config)
    """
    fields = b""
    fields += encode_field_bytes(1, manifest_id.encode())

    # Encode channels
    for ch in channels:
        ch_fields = b""
        ch_fields += encode_field_varint(1, ch["id"])
        ch_fields += encode_field_bytes(2, ch.get("hardware_id", b"\x01"))  # bytes
        if ch.get("template_ids"):
            for tid in ch["template_ids"]:
                ch_fields += encode_field_varint(3, tid)
        ch_fields += encode_field_varint(4, ch.get("interval_ms", 20000))
        ch_fields += encode_field_varint(5, 1 if ch.get("enabled", True) else 0)
        ch_fields += encode_field_varint(6, ch["bus_type"])
        if "bus_config" in ch:
            ch_fields += encode_field_bytes(7, ch["bus_config"])
        
        fields += encode_field_bytes(4, ch_fields)  # Field 4 = channel sub-message

    return build_frame(MSG_CONFIG_MFST, fields)


def send_tcp_recv(host, port, frame, timeout=5):
    """Send a frame over TCP and return the response."""
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(timeout)
        sock.connect((host, port))
        sock.sendall(frame)
        time.sleep(0.3)
        response = b""
        while True:
            try:
                chunk = sock.recv(4096)
                if not chunk:
                    break
                response += chunk
            except socket.timeout:
                break
        sock.close()
        return response
    except Exception as e:
        return f"TCP_ERROR: {e}".encode()


def parse_frame_response(data):
    """Parse frame response bytes to human-readable format."""
    if not data:
        return "NO_RESPONSE"
    if isinstance(data, bytes) and data.startswith(b"TCP_ERROR:"):
        return data.decode()

    result = []
    offset = 0
    while offset < len(data):
        if offset >= len(data):
            break
        msg_type = data[offset]
        offset += 1

        msg_names = {
            0x07: "WriteResponse", 0x03: "DataReport",
            0x02: "StatusReport", 0x05: "ConfigResult",
            0x09: "Pong", 0x11: "ConfigReport",
            0x19: "ResourceReport"
        }
        name = msg_names.get(msg_type, f"UNKNOWN(0x{msg_type:02X})")

        # Parse fields
        fields = {}
        while offset < len(data):
            if offset >= len(data):
                break
            tag = data[offset]
            if tag == 0:
                offset += 1
                break
            field_num = tag >> 3
            wire_type = tag & 0x07
            offset += 1

            if wire_type == WIRE_VARINT:
                value = 0
                shift = 0
                while offset < len(data):
                    b = data[offset]
                    offset += 1
                    value |= (b & 0x7F) << shift
                    if b & 0x80 == 0:
                        break
                    shift += 7
                fields[field_num] = ("varint", value)
            elif wire_type == WIRE_LENGTH_DELIMITED:
                length = 0
                shift = 0
                while offset < len(data):
                    b = data[offset]
                    offset += 1
                    length |= (b & 0x7F) << shift
                    if b & 0x80 == 0:
                        break
                    shift += 7
                if offset + length <= len(data):
                    bytes_val = data[offset:offset + length]
                    offset += length
                    # Try to decode as string
                    try:
                        str_val = bytes_val.decode('ascii')
                    except:
                        str_val = bytes_val.hex()
                    fields[field_num] = ("bytes", str_val[:80], bytes_val)
                else:
                    break
            else:
                break

        result.append(f"{name}: {fields}")

    return "\n".join(result) if result else "EMPTY"


# ═══════════════════════════════════════════════════════════════
# Phase 2: UART Verification
# ═══════════════════════════════════════════════════════════════

def test_uart_write():
    """Send WriteCommand via TCP to channel 1 (UART), verify CP210x receives data."""
    print("=" * 60)
    print("Phase 2: UART TCP Verification")
    print("=" * 60)

    test_data = b"Hello from ESP32 UART!\r\n"

    # Open CP210x for reading BEFORE sending WriteCommand
    cp210x = serial.Serial(CP210X_PORT, 9600, timeout=5)
    cp210x.reset_input_buffer()

    # Send WriteCommand to channel 1 with data
    frame = build_write_cmd(channel_id=1, data=test_data, request_id=100)
    print(f"\n[1] Sending WriteCommand to channel 1: {test_data.hex()}")
    print(f"    Frame ({len(frame)} bytes): {frame.hex()}")

    response = send_tcp_recv(ESP_IP, TCP_PORT, frame, timeout=5)

    # Read CP210x
    time.sleep(0.5)
    cpx_data = cp210x.read(1024)
    cp210x.close()

    print(f"\n[2] TCP Response: {parse_frame_response(response)}")
    print(f"\n[3] CP210x received ({len(cpx_data)} bytes): {cpx_data.hex() if cpx_data else 'NOTHING'}")

    if cpx_data:
        try:
            print(f"    Decoded: {cpx_data.decode('utf-8', errors='replace')}")
        except:
            pass

    # Verification
    if cpx_data and b"Hello" in cpx_data:
        print("\n✅ UART TX VERIFIED: ESP32→CP210x data received correctly")
        return True
    elif cpx_data:
        print(f"\n⚠️  UART TX: Data received but content mismatch. Expected 'Hello', got {cpx_data[:30]}")
        return False
    else:
        print("\n❌ UART TX FAILED: No data received on CP210x")
        return False


# ═══════════════════════════════════════════════════════════════
# Phase 3: SPI BMP280 Verification
# ═══════════════════════════════════════════════════════════════

def test_spi_bmp280():
    """Configure SPI channel via TCP ConfigManifest, then read BMP280 chip ID."""
    print("\n" + "=" * 60)
    print("Phase 3: SPI BMP280 Verification")
    print("=" * 60)

    # SPI BMP280 bus_config format: [cs_pin, mode, freq×4(BE)] + optional flags at offset 6
    # BMP280: CS=GPIO13, mode=0, freq=1MHz (1000000 Hz = 0x000F4240)
    cs_pin = 13
    spi_mode = 0
    spi_freq = 1_000_000  # 1 MHz

    bus_config = bytes([cs_pin, spi_mode]) + struct.pack('>I', spi_freq)
    print(f"\n[1] SPI bus_config: cs={cs_pin}, mode={spi_mode}, freq={spi_freq}Hz")
    print(f"    Raw: {bus_config.hex()} (len={len(bus_config)})")

    # Build ConfigManifest with one SPI channel
    channels = [{
        "id": 10,
        "hardware_id": b"\x02",  # SPI hardware id
        "bus_type": 3,  # BUS_TYPE_SPI
        "interval_ms": 5000,
        "enabled": True,
        "bus_config": bus_config,
    }]

    manifest = build_config_manifest("tcp-spi-test", channels)
    print(f"\n[2] Sending ConfigManifest ({len(manifest)} bytes)")

    response = send_tcp_recv(ESP_IP, TCP_PORT, manifest, timeout=5)
    print(f"    Response: {parse_frame_response(response)}")

    # Give ESP32 time to initialize SPI
    time.sleep(2)

    # Read BMP280 Chip ID (register 0xD0, should return 0x58)
    # BMP280 read: write [register_addr], then read 1 byte
    # For SPI: first bit of register address = 1 for read (0x80 | addr)
    read_cmd = bytes([0xD0 | 0x80, 0x00])  # Read register 0xD0, dummy byte for clock

    print(f"\n[3] Reading BMP280 Chip ID: write={read_cmd.hex()}")
    frame = build_write_cmd(channel_id=10, data=read_cmd, request_id=200, read_size=2)
    print(f"    Frame ({len(frame)} bytes): {frame.hex()}")

    response = send_tcp_recv(ESP_IP, TCP_PORT, frame, timeout=5)
    parsed = parse_frame_response(response)
    print(f"    Response: {parsed}")

    # Parse DataReport for raw data
    chip_id = None
    if isinstance(response, bytes) and len(response) > 0:
        # Try to find DataReport (0x03) and extract raw_data
        for i in range(len(response) - 1):
            if response[i] == 0x03:  # MSG_DATA_RPT
                # Crude parse: look for field 4 (raw_data bytes)
                pos = i + 1
                while pos < len(response):
                    tag = response[pos]
                    fn = tag >> 3
                    wt = tag & 0x07
                    pos += 1
                    if wt == WIRE_LENGTH_DELIMITED:
                        # read varint length
                        length = 0
                        shift = 0
                        while pos < len(response):
                            b = response[pos]; pos += 1
                            length |= (b & 0x7F) << shift
                            if b & 0x80 == 0:
                                break
                            shift += 7
                        if fn == 4 and pos + length <= len(response):
                            raw = response[pos:pos + length]
                            print(f"    DataReport raw_data ({len(raw)} bytes): {raw.hex()}")
                            # For BMP280 SPI read: first byte is dummy, second is chip ID
                            if len(raw) >= 2:
                                chip_id = raw[1]
                            pos += length
                        else:
                            pos += length
                    elif wt == WIRE_VARINT:
                        while pos < len(response) and response[pos] & 0x80:
                            pos += 1
                        pos += 1
                    else:
                        break

    if chip_id == 0x58:
        print(f"\n✅ SPI BMP280 VERIFIED: Chip ID = 0x{chip_id:02X} (expected 0x58)")
        return True
    elif chip_id is not None:
        print(f"\n⚠️  SPI BMP280: Chip ID = 0x{chip_id:02X} (expected 0x58) — wrong device or connection?")
        return False
    else:
        print(f"\n⚠️  SPI BMP280: Could not extract chip ID from response")
        print(f"    Full raw response: {response.hex() if isinstance(response, bytes) else response}")
        return False


# ═══════════════════════════════════════════════════════════════
# Main
# ═══════════════════════════════════════════════════════════════

if __name__ == "__main__":
    print("EHomeSystem TCP Verification")
    print(f"ESP32: {ESP_IP}:{TCP_PORT}")
    print(f"CP210x: {CP210X_PORT}")

    results = {}

    # Phase 2
    results["UART"] = test_uart_write()

    if results["UART"]:
        # Phase 3
        results["SPI"] = test_spi_bmp280()
    else:
        print("\n⚠️  Skipping SPI test — UART verification failed")
        results["SPI"] = False

    # Summary
    print("\n" + "=" * 60)
    print("VERIFICATION SUMMARY")
    print("=" * 60)
    for name, passed in results.items():
        status = "✅ PASS" if passed else "❌ FAIL"
        print(f"  {name}: {status}")

    all_pass = all(results.values())
    print(f"\nOverall: {'✅ ALL PASSED' if all_pass else '❌ SOME FAILED'}")
    sys.exit(0 if all_pass else 1)
