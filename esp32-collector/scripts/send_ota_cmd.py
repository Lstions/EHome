#!/usr/bin/env python3
"""Send OTA command to ESP32 via TCP and monitor progress"""
import socket
import time
import hashlib
import struct

# Get local IP
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
try:
    s.connect(("10.42.0.1", 1))
    LOCAL_IP = s.getsockname()[0]
except:
    LOCAL_IP = "127.0.0.1"
finally:
    s.close()

# ESP32 IP (from previous tests)
ESP32_IP = "10.42.0.155"
ESP32_PORT = 8088

# Firmware info
FIRMWARE_PATH = "/home/bcat/workspace/ehome-system/esp32-collector/build/ehome_collector.bin"
with open(FIRMWARE_PATH, 'rb') as f:
    firmware_data = f.read()
    firmware_size = len(firmware_data)
    firmware_sha256 = hashlib.sha256(firmware_data).hexdigest()

# OTA URL
OTA_URL = f"http://{LOCAL_IP}:8899/firmware.bin"

print(f"OTA Test Setup:")
print(f"  HTTP Server: {LOCAL_IP}:8899")
print(f"  ESP32: {ESP32_IP}:{ESP32_PORT}")
print(f"  Firmware: {firmware_size} bytes")
print(f"  SHA256: {firmware_sha256}")
print(f"  URL: {OTA_URL}")
print()

# Connect to ESP32
print("Connecting to ESP32...")
sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.settimeout(10)
sock.connect((ESP32_IP, ESP32_PORT))
print("✓ Connected")

# Send OTA command (0x0A)
def encode_varint(value):
    result = bytearray()
    while value > 0x7F:
        result.append((value & 0x7F) | 0x80)
        value >>= 7
    result.append(value & 0x7F)
    return bytes(result)

def encode_field_varint(field_num, value):
    tag = (field_num << 3) | 0
    return encode_varint(tag) + encode_varint(value)

def encode_field_bytes(field_num, data):
    tag = (field_num << 3) | 2
    return encode_varint(tag) + encode_varint(len(data)) + data

def encode_field_string(field_num, s):
    return encode_field_bytes(field_num, s.encode('utf-8'))

# Build OTA command
cmd = bytearray([0x0A])  # MSG_OTA_CMD
cmd.extend(encode_field_string(1, "ota-test-001"))  # ota_id
cmd.extend(encode_field_string(2, OTA_URL))          # url
cmd.extend(encode_field_string(3, firmware_sha256))  # checksum
cmd.extend(encode_field_varint(4, firmware_size))    # size

print(f"Sending OTA command ({len(cmd)} bytes)...")
sock.send(cmd)
print("✓ Command sent")

# Wait for response
print("\nWaiting for OTA progress...")
time.sleep(2)

# Read responses
sock.settimeout(1.0)
responses = []
for _ in range(10):
    try:
        data = sock.recv(4096)
        if data:
            msg_type = data[0]
            responses.append(msg_type)
            if msg_type == 0x0B:  # MSG_OTA_PROG
                # Parse progress
                pos = 1
                while pos < len(data):
                    tag = data[pos]
                    field_num = tag >> 3
                    wire_type = tag & 0x07
                    pos += 1
                    
                    if wire_type == 0:  # varint
                        value = 0
                        shift = 0
                        while pos < len(data):
                            b = data[pos]
                            value |= (b & 0x7F) << shift
                            pos += 1
                            if not (b & 0x80):
                                break
                            shift += 7
                        if field_num == 2:  # progress
                            print(f"  OTA Progress: {value}%")
                    elif wire_type == 2:  # length-delimited
                        length = 0
                        shift = 0
                        while pos < len(data):
                            b = data[pos]
                            length |= (b & 0x7F) << shift
                            pos += 1
                            if not (b & 0x80):
                                break
                            shift += 7
                        pos += length
                    else:
                        break
            elif msg_type == 0x0C:
                # 2026-10-08 FIX: was labelled MSG_OTA_RESULT, which DOES NOT EXIST.
                # Authoritative table frame_codec.h:50 => 0x0C is MSG_SCAN_RPT.
                # So this branch used to print "OTA Result received" on a SCAN report,
                # which actively MISLEADS debugging.
                # OTA progress/result actually travels as MSG_OTA_PROG (0x0B), see the
                # branch above; the backend also waits for OtaProg(status=0)
                # (backend/internal/api/handler_ota.go:75).
                print("  [0x0C = MSG_SCAN_RPT] scan report (NOT an OTA result)")
                continue
                # ⚠ 2026-10-08 修正（原注释写 MSG_OTA_RESULT，**该符号不存在**）：
                # 权威表 components/frame/frame_codec.h:50 里 0x0C 是 **MSG_SCAN_RPT**，
                # 不是 OTA 结果。设备侧根本没有 MSG_OTA_RESULT 这个符号。
                # ⇒ 本分支原先会在收到**扫描上报**时打印 "OTA Result received"，**误导排障**。
                # OTA 进度/结果走的是 **MSG_OTA_PROG (0x0B)**（见上面那个分支；
                # 后端 internal/api/handler_ota.go:75 也是等 OtaProg(status=0) 才确认完成）。
                print("  [0x0C = MSG_SCAN_RPT] 收到扫描上报（**不是** OTA 结果）")
                continue
    except socket.timeout:
        break

sock.close()
print("\nOTA command sent successfully!")
print("Check device logs for download progress.")
