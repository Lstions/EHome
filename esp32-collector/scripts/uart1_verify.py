#!/usr/bin/env python3
"""发送 ConfigManifest 配置 C6 UART1 (TX=21, RX=20) 并验证回环"""
import socket, struct, time, sys

HOST = '192.168.1.53'
PORT = 8088

def encode_varint(value):
    result = bytearray()
    while value > 0x7F:
        result.append((value & 0x7F) | 0x80)
        value >>= 7
    result.append(value & 0x7F)
    return bytes(result)

def encode_field(field_num, wire_type, data):
    tag = (field_num << 3) | wire_type
    result = bytearray([tag])
    if wire_type == 0:  # varint
        result.extend(encode_varint(data))
    elif wire_type == 2:  # length-delimited
        result.extend(encode_varint(len(data)))
        result.extend(data)
    return bytes(result)

# Build ConfigManifest for UART1 verification
payload = bytearray()
payload.extend(encode_field(1, 2, b'uart1-verify-001'))  # manifest_id
payload.extend(encode_field(2, 0, int(time.time())))      # epoch

# Template: id=1, write_data="HELLO\r\n", read_length=7 (read back "HELLO\r\n"), delay_ms=100
template = bytearray()
template.extend(encode_field(1, 0, 1))                    # id
template.extend(encode_field(2, 2, b'HELLO\r\n'))         # write_data
template.extend(encode_field(3, 0, 7))                    # read_length (read back 7 bytes)
template.extend(encode_field(4, 0, 100))                  # delay_ms
payload.extend(encode_field(3, 2, bytes(template)))       # field 3: templates

# Channel: id=1, hardware_id=1, template_ids=[1], interval=5000, enabled=1, bus_type=1(UART)
# bus_config: TX=21(0x15), RX=20(0x14), baud=9600(0x00002580 BE)
channel = bytearray()
channel.extend(encode_field(1, 0, 1))                     # id
channel.extend(encode_field(2, 0, 1))                     # hardware_id (varint)
tids = encode_varint(1)
channel.extend(encode_field(3, 2, tids))                  # template_ids
channel.extend(encode_field(4, 0, 5000))                  # interval_ms
channel.extend(encode_field(5, 0, 1))                     # enabled
channel.extend(encode_field(6, 0, 1))                     # bus_type=UART
# C6 UART1: TX=21(0x15), RX=20(0x14), baud=9600(0x00002580)
bus_cfg = bytes([0x15, 0x14, 0x00, 0x00, 0x25, 0x80])
channel.extend(encode_field(7, 2, bus_cfg))               # bus_config
payload.extend(encode_field(4, 2, bytes(channel)))        # field 4: channels

# Frame: [msg_type=0x04] [payload]
frame = bytearray([0x04])
frame.extend(payload)

print(f"=== UART1 (TX=21,RX=20) ConfigManifest ===")
print(f"Frame size: {len(frame)} bytes")
print(f"Frame hex: {frame.hex()}")
print()

# Send
sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.settimeout(5)
sock.connect((HOST, PORT))
print(f"Connected to {HOST}:{PORT}")

sock.sendall(frame)
print(f"Sent {len(frame)} bytes (ConfigManifest)")
print()

# Read response
time.sleep(2)
response = bytearray()
sock.settimeout(3)
try:
    while True:
        chunk = sock.recv(4096)
        if not chunk:
            break
        response.extend(chunk)
except socket.timeout:
    pass

sock.close()

if response:
    print(f"Response ({len(response)} bytes):")
    # Parse frames
    pos = 0
    while pos < len(response):
        msg_type = response[pos]
        pos += 1
        if msg_type == 0x03:  # DataRpt
            print(f"  DataRpt (0x03):")
            # Simple parse: find field 3 (data)
            while pos < len(response):
                tag = response[pos]
                field_num = tag >> 3
                wire_type = tag & 0x07
                pos += 1
                if wire_type == 0:  # varint
                    value = 0; shift = 0
                    while pos < len(response):
                        b = response[pos]; pos += 1
                        value |= (b & 0x7F) << shift
                        if not (b & 0x80): break
                        shift += 7
                    print(f"    field {field_num}: {value}")
                elif wire_type == 2:  # length-delimited
                    length = 0; shift = 0
                    while pos < len(response):
                        b = response[pos]; pos += 1
                        length |= (b & 0x7F) << shift
                        if not (b & 0x80): break
                        shift += 7
                    data = response[pos:pos+length]; pos += length
                    if field_num == 3:
                        print(f"    field 3 (data): {data.hex()} -> {data}")
                    else:
                        print(f"    field {field_num}: {data.hex()} ({len(data)}B)")
                else:
                    break
            break
        elif msg_type == 0x02:  # StatusRpt
            print(f"  StatusRpt (0x02) - device acknowledged")
            break
        else:
            print(f"  Unknown msg_type: 0x{msg_type:02x}")
            break
else:
    print("No response received - check device logs")

print()
print("Check device serial monitor for UART1 init logs")
