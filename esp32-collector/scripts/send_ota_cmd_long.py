#!/usr/bin/env python3
"""Send OTA command and monitor progress for longer"""
import socket
import time
import struct
import hashlib

# Get local IP
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
try:
    s.connect(("10.42.0.1", 1))
    LOCAL_IP = s.getsockname()[0]
except:
    LOCAL_IP = "127.0.0.1"
finally:
    s.close()

ESP32_IP = "10.42.0.155"
ESP32_PORT = 8088
FIRMWARE_PATH = "build/ehome_collector.bin"

# Read firmware
with open(FIRMWARE_PATH, 'rb') as f:
    firmware_data = f.read()
    firmware_size = len(firmware_data)
    firmware_sha256 = hashlib.sha256(firmware_data).hexdigest()

OTA_URL = f"http://{LOCAL_IP}:8899/firmware.bin"

print(f"OTA Test:")
print(f"  Server: {LOCAL_IP}:8899")
print(f"  ESP32: {ESP32_IP}:{ESP32_PORT}")
print(f"  Firmware: {firmware_size} bytes")
print(f"  SHA256: {firmware_sha256}")
print(f"  URL: {OTA_URL}")
print()

# Connect
print("Connecting to ESP32...")
sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.settimeout(10)
sock.connect((ESP32_IP, ESP32_PORT))
print("✓ Connected")

# Build OTA command
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

cmd = bytearray([0x0A])  # MSG_OTA_CMD
cmd.extend(encode_field_string(1, "ota-test-002"))
cmd.extend(encode_field_string(2, OTA_URL))
cmd.extend(encode_field_string(3, firmware_sha256))
cmd.extend(encode_field_varint(4, firmware_size))

print(f"Sending OTA command ({len(cmd)} bytes)...")
sock.send(cmd)
print("✓ Sent")

# Monitor progress for 30 seconds
print("\nMonitoring progress (30s)...")
sock.settimeout(1.0)
last_progress = -1
start_time = time.time()

while time.time() - start_time < 30:
    try:
        data = sock.recv(4096)
        if data:
            msg_type = data[0]
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
                            if value != last_progress:
                                print(f"  [{int(time.time() - start_time):2d}s] Progress: {value}%")
                                last_progress = value
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
            elif msg_type == 0x0C:  # MSG_OTA_RESULT
                print(f"  OTA Result received!")
                # Parse result
                pos = 1
                success = False
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
                        if field_num == 1:  # success
                            success = (value != 0)
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
                
                if success:
                    print("  ✓ OTA successful!")
                else:
                    print("  ✗ OTA failed")
                break
    except socket.timeout:
        pass
    except Exception as e:
        print(f"Error: {e}")
        break

sock.close()
print(f"\nTotal time: {time.time() - start_time:.1f}s")
