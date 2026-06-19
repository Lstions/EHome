#!/usr/bin/env python3
"""快速 TCP 通信测试 - 发送 ConfigManifest 到 ESP32-C6"""
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

# Build ConfigManifest
payload = bytearray()
payload.extend(encode_field(1, 2, b'uart1-verify-001'))  # manifest_id
payload.extend(encode_field(2, 0, int(time.time())))      # epoch

# Template: id=1, write_data="HELLO\r\n", read_length=0, delay_ms=100
template = bytearray()
template.extend(encode_field(1, 0, 1))                    # id
template.extend(encode_field(2, 2, b'HELLO\r\n'))         # write_data
template.extend(encode_field(3, 0, 0))                    # read_length
template.extend(encode_field(4, 0, 100))                  # delay_ms
payload.extend(encode_field(3, 2, bytes(template)))       # field 3: templates

# Channel: id=1, hardware_id=1, template_ids=[1], interval=5000, enabled=1, bus_type=1(UART)
# bus_config: TX=21(0x15), RX=20(0x14), baud=115200(0x0001C200 BE)
channel = bytearray()
channel.extend(encode_field(1, 0, 1))                     # id
channel.extend(encode_field(2, 0, 1))                     # hardware_id (varint!)
# template_ids as repeated varint in length-delimited
tids = encode_varint(1)
channel.extend(encode_field(3, 2, tids))                  # template_ids
channel.extend(encode_field(4, 0, 5000))                  # interval_ms
channel.extend(encode_field(5, 0, 1))                     # enabled
channel.extend(encode_field(6, 0, 1))                     # bus_type=UART
bus_cfg = bytes([0x15, 0x14, 0x00, 0x01, 0xC2, 0x00])    # TX=21, RX=20, 115200
channel.extend(encode_field(7, 2, bus_cfg))               # bus_config
payload.extend(encode_field(4, 2, bytes(channel)))        # field 4: channels

# Frame: [msg_type=0x04] [payload]
frame = bytearray([0x04])
frame.extend(payload)

print(f"Frame size: {len(frame)} bytes")
print(f"Frame hex: {frame.hex()}")

# Send
sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
sock.settimeout(5)
sock.connect((HOST, PORT))
print(f"Connected to {HOST}:{PORT}")

sock.sendall(frame)
print(f"Sent {len(frame)} bytes")

# Read response
time.sleep(1)
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
    for i in range(0, len(response), 16):
        hex_part = ' '.join(f'{b:02x}' for b in response[i:i+16])
        ascii_part = ''.join(chr(b) if 32 <= b < 127 else '.' for b in response[i:i+16])
        print(f"  {i:04x}: {hex_part:<48s} {ascii_part}")
else:
    print("No response received")
