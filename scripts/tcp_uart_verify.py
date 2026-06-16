#!/usr/bin/env python3
"""TCP UART验证脚本 - 通过TCP配置UART通道并发送WriteCommand"""
import socket
import time
import sys

HOST = '192.168.1.54'
PORT = 8088

def encode_varint(value):
    result = []
    while value > 0x7F:
        result.append((value & 0x7F) | 0x80)
        value >>= 7
    result.append(value)
    return bytes(result)

def encode_string(field_num, s):
    data = s.encode('utf-8') if isinstance(s, str) else s
    tag = (field_num << 3) | 0x02
    return bytes([tag]) + encode_varint(len(data)) + data

def encode_varint_field(field_num, value):
    tag = (field_num << 3) | 0x00
    return bytes([tag]) + encode_varint(value)

def encode_bytes_field(field_num, data):
    tag = (field_num << 3) | 0x02
    return bytes([tag]) + encode_varint(len(data)) + data

def create_uart_config_manifest():
    """UART: TX=20, RX=21, baud=9600"""
    template_id = 2
    write_data = bytes([0x01, 0x03, 0x00, 0x00, 0x00, 0x02, 0xC4, 0x0B])  # Modbus FC03
    read_length = 8  # Modbus response
    delay_ms = 0
    
    template = b''
    template += encode_varint_field(1, template_id)
    template += encode_bytes_field(2, write_data)
    template += encode_varint_field(3, read_length)
    template += encode_varint_field(4, delay_ms)
    
    channel_id = 2
    bus_type = 1  # BUS_TYPE_UART
    
    # bus_config: [tx, rx, baud(4 bytes big-endian)]
    TX = 20
    RX = 21
    BAUD = 9600
    bus_config = bytes([TX, RX]) + BAUD.to_bytes(4, 'big')
    
    channel = b''
    channel += encode_varint_field(1, channel_id)
    channel += encode_varint_field(2, 1)  # hardware_id = 1 (UART1)
    channel += encode_varint_field(3, template_id)
    channel += encode_varint_field(4, 1000)  # interval_ms
    channel += encode_varint_field(5, 1)  # enabled
    channel += encode_varint_field(6, bus_type)
    channel += encode_bytes_field(7, bus_config)
    
    manifest = b''
    manifest += encode_string(1, 'uart1-test')
    manifest += encode_varint_field(2, int(time.time()))
    manifest += encode_bytes_field(3, template)
    manifest += encode_bytes_field(4, channel)
    
    return bytes([0x04]) + manifest

def create_write_cmd(request_id, channel_id, write_data, read_size=1):
    cmd = b''
    cmd += encode_varint_field(1, request_id)
    cmd += encode_varint_field(2, channel_id)
    cmd += encode_bytes_field(3, write_data)
    cmd += encode_varint_field(4, read_size)
    return bytes([0x06]) + cmd

def recv_all(sock, timeout=5):
    sock.settimeout(timeout)
    data = b''
    while True:
        try:
            chunk = sock.recv(4096)
            if not chunk:
                break
            data += chunk
            sock.settimeout(0.1)
        except socket.timeout:
            break
        except Exception as e:
            print(f"  接收错误: {e}")
            break
    return data

def main():
    print("=" * 60)
    print("TCP UART 验证")
    print("=" * 60)
    
    # Step 1: Connect
    print("\n[1] 连接到 TCP 服务器...")
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(10)
    try:
        sock.connect((HOST, PORT))
        print(f"  ✓ 连接成功: {HOST}:{PORT}")
    except Exception as e:
        print(f"  ✗ 连接失败: {e}")
        return 1
    
    try:
        # Step 2: Send UART ConfigManifest
        print("\n[2] 发送 UART ConfigManifest...")
        config_msg = create_uart_config_manifest()
        print(f"  消息长度: {len(config_msg)} bytes")
        print(f"  bus_config: TX=20 RX=21 BAUD=9600")
        
        sock.sendall(config_msg)
        print(f"  ✓ 发送完成")
        
        # Step 3: Receive ConfigResult
        print("\n[3] 接收 ConfigResult...")
        time.sleep(0.5)
        response = recv_all(sock, timeout=2)
        if response:
            print(f"  响应长度: {len(response)} bytes")
            if response[0] == 0x05:
                print(f"  ✓ 收到 ConfigResult")
            else:
                print(f"  响应类型: 0x{response[0]:02X}")
        else:
            print(f"  ⚠ 未收到响应")
        
        # Step 4: Wait for config application
        print("\n[4] 等待配置应用 (2秒)...")
        time.sleep(2)
        
        # Step 5: Send WriteCommand (Hello to UART)
        print("\n[5] 发送 WriteCommand (\"HELLO\\r\\n\" 到 UART)...")
        test_data = b"HELLO\r\n"
        write_cmd = create_write_cmd(2, 2, test_data, 0)  # req=2, ch=2, no read
        print(f"  数据: {test_data}")
        
        sock.sendall(write_cmd)
        print(f"  ✓ 发送完成")
        
        # Step 6: Receive WriteRsp
        print("\n[6] 接收 WriteRsp...")
        time.sleep(0.5)
        response = recv_all(sock, timeout=2)
        if response:
            print(f"  响应长度: {len(response)} bytes")
            print(f"  原始数据: {response.hex()}")
            if response[0] == 0x07:
                print(f"  ✓ 收到 WriteRsp!")
            elif response[0] == 0x08:
                print(f"  StatusReport received (WriteRsp may arrive later)")
            else:
                print(f"  消息类型: 0x{response[0]:02X}")
        else:
            print(f"  ⚠ 未收到响应")
        
    except Exception as e:
        print(f"\n✗ 错误: {e}")
        import traceback
        traceback.print_exc()
        return 1
    finally:
        sock.close()
        print("\n连接已关闭")
    
    return 0

if __name__ == '__main__':
    sys.exit(main())
