#!/usr/bin/env python3
"""
ESP32 SPI BMP280 测试脚本

通过 TCP 连接 ESP32，发送 WriteCmd 命令读取 BMP280 芯片 ID 寄存器 (0xD0)。
BMP280 SPI 协议：读取时 bit 7 = 1，所以读 0x50 (chip ID) 需要发送 0xD0。

测试步骤：
1. 连接到 ESP32 TCP 调试端口 (8088)
2. 发送 WriteCmd (0x06) 读取 BMP280 chip ID
3. 验证返回的数据是否包含 0x58 (BMP280 chip ID)
"""

import socket
import struct
import time
import sys

# ESP32 TCP 调试端口
ESP32_HOST = '10.42.0.173'
ESP32_PORT = 8088

# 消息类型
MSG_WRITE_CMD = 0x06
MSG_WRITE_RSP = 0x07

# BMP280 配置
BMP280_CHANNEL_ID = 1  # SPI 通道 ID
BMP280_CHIP_ID_REG = 0xD0  # 读取时需要设置 bit 7
BMP280_EXPECTED_ID = 0x58

def connect_to_esp32():
    """连接到 ESP32 TCP 调试端口"""
    print(f"连接到 ESP32 TCP 调试端口 {ESP32_HOST}:{ESP32_PORT}...")
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(5.0)
    sock.connect((ESP32_HOST, ESP32_PORT))
    print("✓ 连接成功")
    return sock

def encode_write_cmd(request_id, channel_id, write_data, read_size):
    """编码 WriteCmd 消息"""
    # 简化编码：直接使用帧格式
    # [msg_type] [field1: request_id (varint)] [field2: channel_id (varint)] 
    # [field3: write_data (bytes)] [field4: read_size (varint)]
    
    msg = bytearray([MSG_WRITE_CMD])
    
    # Field 1: request_id (varint)
    msg.append(0x08)  # field 1, varint
    msg.append(request_id & 0x7F)
    
    # Field 2: channel_id (varint)
    msg.append(0x10)  # field 2, varint
    msg.append(channel_id & 0x7F)
    
    # Field 3: write_data (length-delimited)
    if write_data:
        msg.append(0x1A)  # field 3, length-delimited
        msg.append(len(write_data))
        msg.extend(write_data)
    
    # Field 4: read_size (varint)
    msg.append(0x20)  # field 4, varint
    msg.append(read_size & 0x7F)
    
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
        if i >= len(data):
            break
        
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
            if i < len(data):
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

def test_bmp280_chip_id():
    """测试读取 BMP280 芯片 ID"""
    print("\n" + "="*60)
    print("BMP280 SPI 通信测试")
    print("="*60)
    
    try:
        sock = connect_to_esp32()
    except Exception as e:
        print(f"✗ 连接失败: {e}")
        print("请确保:")
        print(f"  1. ESP32 已启动并连接到 WiFi")
        print(f"  2. TCP 调试端口已启用 (CONFIG_DEBUG_TCP_ENABLED=y)")
        print(f"  3. ESP32 IP 地址正确: {ESP32_HOST}")
        return False
    
    try:
        # 发送 WriteCmd 读取 BMP280 chip ID
        request_id = 1
        write_data = bytes([BMP280_CHIP_ID_REG])  # 0xD0 = 0x80 | 0x50
        read_size = 1  # 读取 1 字节
        
        cmd = encode_write_cmd(request_id, BMP280_CHANNEL_ID, write_data, read_size)
        print(f"\n发送 WriteCmd:")
        print(f"  通道 ID: {BMP280_CHANNEL_ID}")
        print(f"  写入数据: {write_data.hex()} (寄存器 0x{BMP280_CHIP_ID_REG:02X})")
        print(f"  读取大小: {read_size} 字节")
        print(f"  消息: {cmd.hex()}")
        
        sock.sendall(cmd)
        print("✓ 消息已发送")
        
        # 接收响应
        print("\n等待响应...")
        sock.settimeout(3.0)
        
        response = sock.recv(1024)
        if not response:
            print("✗ 未收到响应")
            return False
        
        print(f"收到响应: {response.hex()}")
        
        # 解码响应
        result = decode_write_rsp(response)
        if not result:
            print("✗ 响应解码失败")
            return False
        
        print(f"\n响应内容:")
        print(f"  请求 ID: {result['request_id']}")
        print(f"  成功: {result['success']}")
        print(f"  错误代码: {result['error_code']}")
        if result['error_msg']:
            print(f"  错误消息: {result['error_msg']}")
        if result['response_data']:
            print(f"  响应数据: {result['response_data'].hex()}")
        
        # 验证芯片 ID
        if result['success'] and result['response_data']:
            chip_id = result['response_data'][0]
            print(f"\nBMP280 芯片 ID: 0x{chip_id:02X}")
            
            if chip_id == BMP280_EXPECTED_ID:
                print(f"✓ 验证通过！芯片 ID 正确 (期望 0x{BMP280_EXPECTED_ID:02X})")
                print("\n" + "="*60)
                print("✓ BMP280 SPI 通信测试成功！")
                print("="*60)
                return True
            else:
                print(f"✗ 芯片 ID 错误 (期望 0x{BMP280_EXPECTED_ID:02X})")
                return False
        else:
            print("✗ 命令执行失败")
            return False
    
    except socket.timeout:
        print("✗ 响应超时")
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
    success = test_bmp280_chip_id()
    sys.exit(0 if success else 1)
