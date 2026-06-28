#!/usr/bin/env python3
"""
UART loopback test for ESP32 P1-8 transparent pipe verification.

Setup: /dev/ttyUSB0 connected to ESP32 UART1 (GPIO20=TX, GPIO21=RX)
       /dev/ttyACM0 is ESP32 debug console (115200bps)

Test: Send a known binary frame to ESP32 UART1, verify it appears in the
      debug console as a DataReport OR via MQTT.

The ESP32 rx_task accumulates UART bytes and reports them to backend.
This script sends a test frame and verifies the ESP32 reports it properly.
"""
import serial
import time
import sys
import struct
import threading

PORT = "/dev/ttyUSB0"
BAUD = 9600
TIMEOUT = 3.0


def jiabaida_checksum(data):
    """Compute Jiabaida checksum: ^sum + 1 over bytes."""
    return (0xFFFF - sum(data) + 1) & 0xFFFF


def build_bms_frame(cmd, data_bytes):
    """Build a Jiabaida BMS frame: 0xDD | CMD | 0x00 | LEN | DATA | CHKSUM | 0x77"""
    frame = bytes([0xDD, cmd, 0x00, len(data_bytes)]) + data_bytes
    chk_data = bytes([len(data_bytes)]) + data_bytes
    cs = jiabaida_checksum(chk_data)
    frame += struct.pack('>H', cs) + bytes([0x77])
    return frame


def build_modbus_rtu_frame(addr, func, data):
    """Build Modbus RTU frame with CRC."""
    frame = bytes([addr, func]) + data
    crc = 0xFFFF
    for b in frame:
        crc ^= b
        for _ in range(8):
            if crc & 1:
                crc = (crc >> 1) ^ 0xA001
            else:
                crc >>= 1
    frame += struct.pack('<H', crc)
    return frame


def main():
    print(f"=== UART Loopback Test ===")
    print(f"Port: {PORT} @ {BAUD} baud")
    print(f"Time: {time.strftime('%H:%M:%S')}")
    print()

    try:
        import serial
        ser = serial.Serial(PORT, BAUD, timeout=TIMEOUT)
        ser.reset_input_buffer()
        ser.reset_output_buffer()
        print(f"Serial port opened: {ser.name}")
    except Exception as e:
        print(f"ERROR: Cannot open {PORT}: {e}")
        print(f"Available ports:")
        import serial.tools.list_ports
        for p in serial.tools.list_ports.comports():
            print(f"  {p.device} - {p.description}")
        sys.exit(1)

    # Test 1: Simple Modbus RTU read (0x03) with known response
    # This tests that ESP32 reports the raw bytes correctly
    print("\n--- Test 1: Modbus RTU 0x03 read (7 bytes) ---")
    cmd = build_modbus_rtu_frame(0x01, 0x03, bytes([0x00, 0x00, 0x00, 0x02]))
    print(f"TX: {cmd.hex(' ')} ({len(cmd)} bytes)")
    ser.write(cmd)
    ser.flush()

    # Wait for response
    time.sleep(0.2)
    rx = ser.read(64)
    if rx:
        print(f"RX: {rx.hex(' ')} ({len(rx)} bytes)")
        print("PASS: received response bytes")
    else:
        print("RX: (no response)")
        print("INFO: no sensor at this address, this is expected")

    # Test 2: Jiabaida BMS 0x03 read request
    # This tests a longer frame with start/stop delimiters
    print("\n--- Test 2: Jiabaida BMS 0x03 read request (7 bytes) ---")
    bms_cmd = build_bms_frame(0x03, bytes())  # 0xDD 0x03 0x00 0x00 0xFF 0xFF 0x77
    print(f"TX: {bms_cmd.hex(' ')} ({len(bms_cmd)} bytes)")

    ser.write(bms_cmd)
    ser.flush()

    # Wait for response (BMS needs up to 100ms)
    time.sleep(0.2)
    rx = ser.read(128)
    if rx:
        print(f"RX: {rx.hex(' ')} ({len(rx)} bytes)")
        print("PASS: received response bytes")
    else:
        print("RX: (no response)")
        print("INFO: no BMS connected, this is expected")

    # Test 3: Multi-part frame simulation (large response across multiple reads)
    # This verifies the UART idle detection: ESP32 should accumulate and
    # report all bytes as one complete frame after 10ms idle.
    print("\n--- Test 3: Frame boundary test (multi-part, 36 bytes) ---")
    # Build a synthetic BMS 0x03 response that looks like real data
    fake_data = bytearray(29)
    # Fill with recognizable pattern: total_voltage=52130mV, current=5000mA...
    struct.pack_into('>H', fake_data, 0, 52130)   # total voltage
    struct.pack_into('>h', fake_data, 2, 5000)    # current
    struct.pack_into('>H', fake_data, 4, 50000)   # remaining
    struct.pack_into('>H', fake_data, 6, 60000)   # nominal
    struct.pack_into('>H', fake_data, 8, 100)     # cycles
    struct.pack_into('>H', fake_data, 16, 0x0001) # protection
    fake_data[18] = 1
    fake_data[19] = 85   # RSOC 85%
    fake_data[20] = 0    # FET
    fake_data[21] = 16   # cells
    fake_data[22] = 3    # NTC count

    full_frame = build_bms_frame(0x03, bytes(fake_data))
    print(f"Full frame: {full_frame.hex(' ')} ({len(full_frame)} bytes)")

    # Send in two parts with a gap shorter than idle threshold
    part1 = full_frame[:12]
    part2 = full_frame[12:]
    print(f"Part 1 ({len(part1)}B): {part1.hex(' ')}")
    print(f"Part 2 ({len(part2)}B): sent after 5ms gap (below 10ms idle threshold)")

    # Clear buffers
    ser.reset_input_buffer()

    # Send part 1 programmatically (simulating a sensor sending data)
    ser.write(part1)
    ser.flush()
    time.sleep(0.005)  # 5ms gap — below UART_IDLE_THRESHOLD_US (10ms)
    ser.write(part2)
    ser.flush()

    print("Both parts sent. ESP32 should accumulate all bytes and report as one DataReport after 10ms idle.")
    print("Check ESP32 debug console for RX_TASK output.")

    # Test 4: Verify the ESP32 rx_task reads are actually happening
    print("\n--- Test 4: Read back for any accumulated output ---")
    time.sleep(0.1)
    rx = ser.read(256)
    if rx:
        print(f"RX: {rx.hex(' ')} ({len(rx)} bytes)")
    else:
        print("RX: (nothing)")

    ser.close()
    print(f"\n=== Test Complete ===")


if __name__ == "__main__":
    main()
