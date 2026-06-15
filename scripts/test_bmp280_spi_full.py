#!/usr/bin/env python3
"""
ESP32 SPI BMP280 完整测试脚本

测试步骤：
1. 通过 TCP 连接 ESP32
2. 发送 ConfigManifest 配置 SPI 通道 (BMP280)
3. 发送 WriteCmd 读取 BMP280 chip ID (0xD0)
4. 验证返回的 chip ID 是否为 0x58

SPI2 引脚配置 (ESP32-C6):
  - MOSI: GPIO 10
  - MISO: GPIO 11
  - SCLK: GPIO 12
  - CS:   GPIO 13

BMP280 SPI 协议：
  - 读取时 bit 7 = 1
  - Chip ID 寄存器: 0x50 (读取时发送 0xD0)
  - 期望返回值: 0x58
"""

import socket
import struct
import time
import sys

ESP32_HOST = '10.42.0.173'
ESP32_PORT = 8088

# 消息类型
MSG_CONFIG_MFST = 0x04
MSG_CONFIG_RSLT = 0x05
MSG_WRITE_CMD = 0x06
MSG_WRITE_RSP = 0x07
MSG_DATA_RPT = 0x03
MSG_RESOURCE_REPORT = 0x19
MSG_QUERY_RESOURCES = 0x1A

# BMP280 配置
BMP280_CHANNEL_ID = 10  # 新的 SPI 通道 ID
BMP280_CHIP_ID_REG = 0xD0  # 读取时设置 bit 7 (0x50 | 0x80)
BMP280_EXPECTED_ID = 0x58

# SPI2 引脚 (ESP32-C6)
SPI_MOSI = 10
SPI_MISO = 11
SPI_SCLK = 12
SPI_CS = 13

def connect_to_esp32():
    """连接到 ESP32 TCP 调试端口"""
    print(f"连接到 ESP32 TCP 调试端口 {ESP32_HOST}:{ESP32_PORT}...")
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(5.0)
    sock.connect((ESP32_HOST, ESP32_PORT))
    print("✓ 连接成功")
    return sock

def encode_varint(value):
    """编码 varint"""
    result = bytearray()
    while value >= 0x80:
        result.append((value & 0x7F) | 0x80)
        value >>= 7
    result.append(value & 0x7F)
    return bytes(result)

def encode_field(field_num, wire_type, data):
    """编码 protobuf 字段"""
    tag = (field_num << 3) | wire_type
    result = bytearray([tag])
    
    if wire_type == 0:  # varint
        result.extend(encode_varint(data))
    elif wire_type == 2:  # length-delimited
        result.extend(encode_varint(len(data)))
        result.extend(data)
    
    return bytes(result)

def encode_spi_config_manifest():
    """编码 ConfigManifest，配置 SPI 通道"""
    msg = bytearray([MSG_CONFIG_MFST])
    
    # Field 1: manifest_id (string)
    manifest_id = b"spi-bmp280-test"
    msg.extend(encode_field(1, 2, manifest_id))
    
    # Field 2: config_epoch (varint)
    epoch = int(time.time())
    msg.extend(encode_field(2, 0, epoch))
    
    # Field 3: templates (repeated, length-delimited)
    # 创建一个简单的模板：读取寄存器
    template = bytearray()
    template.extend(encode_field(1, 0, 1))  # template_id = 1
    template.extend(encode_field(2, 2, bytes([BMP280_CHIP_ID_REG])))  # write_data = [0xD0]
    template.extend(encode_field(3, 0, 1))  # read_length = 1
    msg.extend(encode_field(3, 2, bytes(template)))
    
    # Field 4: channels (repeated, length-delimited)
    channel = bytearray()
    channel.extend(encode_field(1, 0, BMP280_CHANNEL_ID))  # channel_id
    channel.extend(encode_field(2, 0, 3))  # bus_type = 3 (SPI)
    channel.extend(encode_field(3, 0, 1))  # template_id = 1
    
    # bus_config: [CS, mode, freq(4 bytes big-endian), MOSI, MISO, SCLK]
    freq = 1000000  # 1 MHz
    freq_bytes = struct.pack('>I', freq)  # big-endian
    bus_config = bytes([SPI_CS, 0]) + freq_bytes + bytes([SPI_MOSI, SPI_MISO, SPI_SCLK])
    channel.extend(encode_field(4, 2, bus_config))
    
    channel.extend(encode_field(5, 0, 1000))  # interval_ms = 1000ms
    channel.extend(encode_field(6, 0, 1))  # enabled = true
    
    msg.extend(encode_field(4, 2, bytes(channel)))
    
    return bytes(msg)

def decode_write_rsp(data):
    """解码 WriteRsp 消息"""
    if len(data) < 1 or data[0] != MSG_WRITE_RSP:
        return None
    
    result = {
        'request_id': None,
        'success': False,
        'error_code': 0,
        'error_msg': None,
        'response_data': None
    }
    
    i = 1
    while i < len(data):
        field_tag = data[i]
        field_num = field_tag >> 3
        wire_type = field_tag & 0x07
        i += 1
        
        if wire_type == 0:  # varint
            value = 0
            shift = 0
            while i < len(data) and data[i] & 0x80:
                value |= (data[i] & 0x7F) << shift
                i += 1
                shift += 7
            if i < len(data):
                value |= (data[i] & 0x7F) << shift
                i += 1
            
            if field_num == 1:
                result['request_id'] = value
            elif field_num == 2:
                result['success'] = (value != 0)
            elif field_num == 3:
                result['error_code'] = value
        
        elif wire_type == 2:  # length-delimited
            length = data[i]
            i += 1
            if i + length <= len(data):
                value = data[i:i+length]
                if field_num == 4:
                    result['response_data'] = value
                elif field_num == 5:
                    result['error_msg'] = value.decode('utf-8', errors='ignore')
                i += length
    
    return result

def test_bmp280_spi():
    """完整测试 BMP280 SPI 通信"""
    print("=" * 60)
    print("BMP280 SPI 完整测试")
    print("=" * 60)
    
    try:
        sock = connect_to_esp32()
    except Exception as e:
        print(f"✗ 连接失败: {e}")
        return False
    
    try:
        # Step 1: 发送 SPI 配置
        print("\n[Step 1] 发送 SPI 配置...")
        config_msg = encode_spi_config_manifest()
        print(f"  消息长度: {len(config_msg)}")
        print(f"  消息: {config_msg.hex()}")
        sock.sendall(config_msg)
        print("✓ 配置已发送")
        
        # 等待 ConfigResult
        print("  等待 ConfigResult...")
        sock.settimeout(3.0)
        try:
            response = sock.recv(1024)
            if response and response[0] == MSG_CONFIG_RSLT:
                print("✓ 收到 ConfigResult")
                # 检查是否成功
                if len(response) > 2 and response[2] == 0x01:
                    print("✓ 配置应用成功")
                else:
                    print("⚠ 配置应用可能失败")
            else:
                print("⚠ 未收到 ConfigResult")
        except socket.timeout:
            print("⚠ ConfigResult 超时")
        
        # 等待 ESP32 应用配置
        print("  等待 ESP32 应用配置 (2秒)...")
        time.sleep(2)
        
        # Step 2: 发送 WriteCmd 读取 BMP280 chip ID
        print("\n[Step 2] 发送 WriteCmd 读取 BMP280 chip ID...")
        request_id = 1
        write_data = bytes([BMP280_CHIP_ID_REG])  # 0xD0
        read_size = 1
        
        cmd = bytearray([MSG_WRITE_CMD])
        cmd.extend(encode_field(1, 0, request_id))  # request_id
        cmd.extend(encode_field(2, 0, BMP280_CHANNEL_ID))  # channel_id
        cmd.extend(encode_field(3, 2, write_data))  # write_data
        cmd.extend(encode_field(4, 0, read_size))  # read_size
        
        print(f"  通道 ID: {BMP280_CHANNEL_ID}")
        print(f"  写入数据: {write_data.hex()} (寄存器 0x{BMP280_CHIP_ID_REG:02X})")
        print(f"  读取大小: {read_size} 字节")
        print(f"  消息: {cmd.hex()}")
        
        sock.sendall(cmd)
        print("✓ 消息已发送")
        
        # 接收响应
        print("\n[Step 3] 等待响应...")
        sock.settimeout(3.0)
        
        try:
            response = sock.recv(1024)
            if not response:
                print("✗ 未收到响应")
                return False
            
            print(f"  收到响应: {response.hex()}")
            
            # 解码响应
            result = decode_write_rsp(response)
            if not result:
                print("✗ 响应解码失败")
                return False
            
            print(f"\n  响应内容:")
            print(f"    请求 ID: {result['request_id']}")
            print(f"    成功: {result['success']}")
            print(f"    错误代码: {result['error_code']} (0x{result['error_code']:04X})")
            if result['error_msg']:
                print(f"    错误消息: {result['error_msg']}")
            if result['response_data']:
                print(f"    响应数据: {result['response_data'].hex()}")
            
            # 验证芯片 ID
            if result['success'] and result['response_data']:
                chip_id = result['response_data'][0]
                print(f"\n  BMP280 芯片 ID: 0x{chip_id:02X}")
                
                if chip_id == BMP280_EXPECTED_ID:
                    print(f"  ✓ 验证通过！芯片 ID 正确 (期望 0x{BMP280_EXPECTED_ID:02X})")
                    print("\n" + "=" * 60)
                    print("✓ BMP280 SPI 通信测试成功！")
                    print("=" * 60)
                    return True
                else:
                    print(f"  ✗ 芯片 ID 错误 (期望 0x{BMP280_EXPECTED_ID:02X})")
                    return False
            else:
                print("  ✗ 命令执行失败")
                return False
        
        except socket.timeout:
            print("  ✗ 响应超时")
            return False
    
    except Exception as e:
        print(f"✗ 测试失败: {e}")
        import traceback
        traceback.print_exc()
        return False
    finally:
        sock.close()
        print("\n连接已关闭")

if __name__ == '__main__':
    success = test_bmp280_spi()
    sys.exit(0 if success else 1)
