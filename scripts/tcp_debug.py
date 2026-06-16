#!/usr/bin/env python3
"""
TCP 调试脚本 - 详细显示每个步骤
"""

import socket
import time
import sys

HOST = '10.42.0.173'
PORT = 8088

def encode_varint(value):
    """编码varint"""
    result = []
    while value > 0x7F:
        result.append((value & 0x7F) | 0x80)
        value >>= 7
    result.append(value)
    return bytes(result)

def encode_string(field_num, s):
    """编码string字段"""
    data = s.encode('utf-8') if isinstance(s, str) else s
    tag = (field_num << 3) | 0x02
    return bytes([tag]) + encode_varint(len(data)) + data

def encode_varint_field(field_num, value):
    """编码varint字段"""
    tag = (field_num << 3) | 0x00
    return bytes([tag]) + encode_varint(value)

def encode_bytes_field(field_num, data):
    """编码bytes字段"""
    tag = (field_num << 3) | 0x02
    return bytes([tag]) + encode_varint(len(data)) + data

def create_config_manifest():
    """创建 ConfigManifest 消息"""
    # 模板 1: 读取 BMP280 芯片 ID (SPI: 发送[D0,00] 接收[dummy, chip_id])
    template_id = 1
    write_data = bytes([0xD0, 0x00])  # BMP280 chip ID register + dummy byte
    read_length = 2  # receive 2 bytes: [dummy, chip_id=0x58]
    delay_ms = 0
    
    # 构建 template
    template = b''
    template += encode_varint_field(1, template_id)
    template += encode_bytes_field(2, write_data)
    template += encode_varint_field(3, read_length)
    template += encode_varint_field(4, delay_ms)
    
    # 通道 1: SPI BMP280
    channel_id = 1
    bus_type = 3  # BUS_TYPE_SPI
    
    # bus_config: [cs, mode, freq(4 bytes big-endian), mosi, miso, sclk, flags]
    SPI_CS = 13
    SPI_MOSI = 10
    SPI_MISO = 11
    SPI_SCLK = 12
    SPI_MODE = 0
    SPI_FREQ = 1000000
    flags = 0x01  # DMA enabled
    
    freq_bytes = SPI_FREQ.to_bytes(4, 'big')
    bus_config = bytes([SPI_CS, SPI_MODE]) + freq_bytes + bytes([SPI_MOSI, SPI_MISO, SPI_SCLK, flags])
    
    channel = b''
    channel += encode_varint_field(1, channel_id)      # field 1: channel_id
    channel += encode_varint_field(2, 0)                # field 2: hardware_id
    channel += encode_varint_field(3, template_id)      # field 3: template_id
    channel += encode_varint_field(4, 1000)             # field 4: interval_ms
    channel += encode_varint_field(5, 1)                # field 5: enabled
    channel += encode_varint_field(6, bus_type)         # field 6: bus_type (SPI = 3)
    channel += encode_bytes_field(7, bus_config)        # field 7: bus_config
    
    # 构建 ConfigManifest
    manifest = b''
    manifest += encode_string(1, 'spi-bmp280-test')  # field 1: manifest_id
    manifest += encode_varint_field(2, int(time.time()))  # field 2: epoch
    manifest += encode_bytes_field(3, template)  # field 3: templates
    manifest += encode_bytes_field(4, channel)  # field 4: channels
    
    return bytes([0x04]) + manifest

def create_write_cmd(request_id, channel_id, write_data, read_size=1):
    """创建WriteCommand消息 - 匹配 ESP32 msg_handler 解析格式
    field 1: request_id (varint)
    field 2: channel_id (varint)
    field 3: data (bytes)
    field 4: read_size (varint)
    """
    cmd = b''
    cmd += encode_varint_field(1, request_id)
    cmd += encode_varint_field(2, channel_id)
    cmd += encode_bytes_field(3, write_data)
    cmd += encode_varint_field(4, read_size)
    
    return bytes([0x06]) + cmd

def recv_all(sock, timeout=5):
    """接收所有数据"""
    sock.settimeout(timeout)
    data = b''
    while True:
        try:
            chunk = sock.recv(4096)
            if not chunk:
                break
            data += chunk
            # 如果没有更多数据，等待一小段时间
            sock.settimeout(0.1)
        except socket.timeout:
            break
        except Exception as e:
            print(f"  接收错误: {e}")
            break
    return data

def main():
    print("=" * 60)
    print("TCP 详细调试")
    print("=" * 60)
    
    # 步骤1: 连接
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
        # 步骤2: 发送ConfigManifest
        print("\n[2] 发送 ConfigManifest...")
        config_msg = create_config_manifest()
        print(f"  消息长度: {len(config_msg)} bytes")
        print(f"  消息: {config_msg.hex()}")
        
        sock.sendall(config_msg)
        print(f"  ✓ 发送完成")
        
        # 步骤3: 接收ConfigResult
        print("\n[3] 接收 ConfigResult...")
        time.sleep(0.5)
        response = recv_all(sock, timeout=2)
        if response:
            print(f"  响应长度: {len(response)} bytes")
            print(f"  响应: {response.hex()}")
            if response[0] == 0x05:
                print(f"  ✓ 收到 ConfigResult (msg_type=0x05)")
            else:
                print(f"  ⚠ 未知消息类型: 0x{response[0]:02X}")
        else:
            print(f"  ✗ 未收到响应")
            return 1
        
        # 步骤4: 等待
        print("\n[4] 等待配置应用 (2秒)...")
        time.sleep(2)
        
        # 步骤5: 发送WriteCommand
        print("\n[5] 发送 WriteCommand (读取BMP280芯片ID)...")
        write_cmd = create_write_cmd(1, 1, bytes([0xD0, 0x00]), 2)  # req=1, ch=1, data=[0xD0,0x00], read=2 (chip_id in byte[1])
        print(f"  消息长度: {len(write_cmd)} bytes")
        print(f"  消息: {write_cmd.hex()}")
        
        sock.sendall(write_cmd)
        print(f"  ✓ 发送完成")
        
        # 步骤6: 接收WriteRsp
        print("\n[6] 接收 WriteRsp...")
        time.sleep(0.5)
        response = recv_all(sock, timeout=2)
        if response:
            print(f"  响应长度: {len(response)} bytes")
            print(f"  响应: {response.hex()}")
            if response[0] == 0x07:
                print(f"  ✓ 收到 WriteRsp (msg_type=0x07)")
                # 解码响应
                # field 1: success (varint)
                # field 2: data (bytes)
                print(f"  解码响应...")
                pos = 1
                while pos < len(response):
                    tag = response[pos]
                    field_num = tag >> 3
                    wire_type = tag & 0x07
                    pos += 1
                    
                    if wire_type == 0:  # varint
                        value = 0
                        shift = 0
                        while pos < len(response):
                            byte = response[pos]
                            pos += 1
                            value |= (byte & 0x7F) << shift
                            if (byte & 0x80) == 0:
                                break
                            shift += 7
                        print(f"    Field {field_num} (varint): {value}")
                    elif wire_type == 2:  # length-delimited
                        length = response[pos]
                        pos += 1
                        data = response[pos:pos+length]
                        pos += length
                        print(f"    Field {field_num} (bytes, len={length}): {data.hex()}")
                        if field_num == 2 and len(data) > 0:
                            chip_id = data[0]
                            print(f"  ✓ 芯片ID: 0x{chip_id:02X}")
                            if chip_id == 0x58:
                                print(f"  ✓✓ 验证通过! BMP280芯片ID正确")
                                return 0
                            else:
                                print(f"  ✗✗ 验证失败! 期望0x58")
                                return 1
            else:
                print(f"  ⚠ 未知消息类型: 0x{response[0]:02X}")
        else:
            print(f"  ✗ 未收到响应 (连接可能已关闭)")
            return 1
        
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
