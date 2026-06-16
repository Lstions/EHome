#!/usr/bin/env python3
"""
SPI BMP280 验证脚本
通过TCP发送ConfigManifest配置SPI通道，然后读取BMP280芯片ID
"""

import socket
import time
import sys

HOST = '192.168.1.54'
PORT = 8088
TIMEOUT = 10

# 消息类型
MSG_CONFIG_MFST = 0x04
MSG_CONFIG_RSLT = 0x05
MSG_WRITE_CMD = 0x06
MSG_WRITE_RSP = 0x07

# SPI配置 (根据bus_dma.c格式)
# bus_config: [cs_pin, spi_mode, freq_msb, freq_msb-1, freq_msb-2, freq_msb-3, mosi_pin, miso_pin, sclk_pin, flags]
SPI_CS = 13
SPI_MOSI = 10
SPI_MISO = 11
SPI_SCLK = 12
SPI_MODE = 0
SPI_FREQ = 1000000  # 1MHz

# BMP280寄存器
BMP280_CHIP_ID_REG = 0xD0
BMP280_CHIP_ID_VALUE = 0x58

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
    tag = (field_num << 3) | 0x02  # length-delimited
    return bytes([tag]) + encode_varint(len(data)) + data

def encode_varint_field(field_num, value):
    """编码varint字段"""
    tag = (field_num << 3) | 0x00  # varint
    return bytes([tag]) + encode_varint(value)

def encode_bytes_field(field_num, data):
    """编码bytes字段"""
    tag = (field_num << 3) | 0x02  # length-delimited
    return bytes([tag]) + encode_varint(len(data)) + data

def create_config_manifest():
    """创建ConfigManifest消息"""
    # 通道1: SPI BMP280
    channel_id = 1
    bus_type = 3  # BUS_TYPE_SPI
    
    # bus_config: [cs, mode, freq(4 bytes), mosi, miso, sclk, flags]
    freq_bytes = SPI_FREQ.to_bytes(4, 'big')
    flags = 0x01  # DMA enabled
    bus_config = bytes([SPI_CS, SPI_MODE]) + freq_bytes + bytes([SPI_MOSI, SPI_MISO, SPI_SCLK, flags])
    
    # 构建channel
    channel = b''
    channel += encode_varint_field(1, channel_id)      # id
    channel += encode_varint_field(2, bus_type)         # bus_type
    channel += encode_bytes_field(3, bus_config)        # bus_config
    channel += encode_varint_field(4, 1000)             # interval_ms (1秒)
    channel += encode_varint_field(5, 1)                # enabled
    
    # 构建ConfigManifest
    manifest = b''
    manifest += encode_string(1, 'spi-bmp280-test')     # manifest_id
    manifest += encode_varint_field(2, int(time.time())) # epoch
    # channels是repeated字段，field_num=3
    manifest += encode_bytes_field(3, channel)          # channels
    
    return bytes([MSG_CONFIG_MFST]) + manifest

def create_write_cmd(channel_id, reg_addr, read_size=1):
    """创建WriteCommand消息"""
    cmd = b''
    cmd += encode_varint_field(1, channel_id)           # channel_id
    cmd += encode_bytes_field(2, bytes([reg_addr]))     # data (寄存器地址)
    cmd += encode_varint_field(3, read_size)            # read_size
    
    return bytes([MSG_WRITE_CMD]) + cmd

def decode_message(data):
    """解码消息"""
    if len(data) < 1:
        return None, None
    
    msg_type = data[0]
    fields = {}
    pos = 1
    
    while pos < len(data):
        if pos >= len(data):
            break
        
        tag = data[pos]
        field_num = tag >> 3
        wire_type = tag & 0x07
        pos += 1
        
        if wire_type == 0:  # varint
            value = 0
            shift = 0
            while pos < len(data):
                byte = data[pos]
                pos += 1
                value |= (byte & 0x7F) << shift
                if (byte & 0x80) == 0:
                    break
                shift += 7
            fields[field_num] = value
        elif wire_type == 2:  # length-delimited
            if pos >= len(data):
                break
            length = data[pos]
            pos += 1
            if pos + length > len(data):
                break
            fields[field_num] = data[pos:pos+length]
            pos += length
        else:
            break
    
    return msg_type, fields

def main():
    print("=" * 60)
    print("SPI BMP280 验证测试")
    print("=" * 60)
    print(f"目标: {HOST}:{PORT}")
    print(f"SPI配置: CS={SPI_CS}, MOSI={SPI_MOSI}, MISO={SPI_MISO}, SCLK={SPI_SCLK}")
    print(f"BMP280寄存器: 0x{BMP280_CHIP_ID_REG:02X} (芯片ID)")
    print()
    
    # 连接TCP
    try:
        sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        sock.settimeout(TIMEOUT)
        sock.connect((HOST, PORT))
        print("✓ TCP连接成功")
    except Exception as e:
        print(f"✗ TCP连接失败: {e}")
        return 1
    
    try:
        # 步骤1: 发送ConfigManifest
        print("\n[1/3] 发送ConfigManifest (配置SPI通道)...")
        config_msg = create_config_manifest()
        print(f"  消息: {config_msg.hex()}")
        sock.sendall(config_msg)
        
        # 等待ConfigResult
        print("  等待ConfigResult...")
        data = sock.recv(4096)
        if not data:
            print("  ✗ 未收到响应")
            return 1
        
        msg_type, fields = decode_message(data)
        if msg_type == MSG_CONFIG_RSLT:
            success = fields.get(1, 0)
            error = ''
            if 2 in fields:
                error_val = fields[2]
                if isinstance(error_val, bytes):
                    error = error_val.decode('utf-8', errors='ignore')
                elif isinstance(error_val, int):
                    error = f'code={error_val}'
            if success:
                print(f"  ✓ 配置成功")
            else:
                print(f"  ✗ 配置失败: {error}")
                return 1
        else:
            print(f"  ✗ 未知响应: msg_type=0x{msg_type:02X}")
            print(f"  数据: {data.hex()}")
            return 1
        
        # 等待配置应用
        print("\n  等待配置应用 (2秒)...")
        time.sleep(2)
        
        # 步骤2: 发送WriteCommand读取芯片ID
        print("\n[2/3] 发送WriteCommand (读取BMP280芯片ID)...")
        write_cmd = create_write_cmd(1, BMP280_CHIP_ID_REG, 1)
        print(f"  消息: {write_cmd.hex()}")
        sock.sendall(write_cmd)
        
        # 等待WriteRsp
        print("  等待WriteRsp...")
        data = sock.recv(4096)
        if not data:
            print("  ✗ 未收到响应")
            return 1
        
        msg_type, fields = decode_message(data)
        print(f"  响应: msg_type=0x{msg_type:02X}, fields={fields}")
        
        if msg_type == MSG_WRITE_RSP:
            success = fields.get(1, 0)
            read_data = fields.get(2, b'') if 2 in fields else b''
            
            if success and len(read_data) > 0:
                chip_id = read_data[0]
                print(f"\n[3/3] 验证结果:")
                print(f"  读取数据: {read_data.hex()}")
                print(f"  芯片ID: 0x{chip_id:02X}")
                
                if chip_id == BMP280_CHIP_ID_VALUE:
                    print(f"  ✓ 验证通过! 芯片ID正确 (期望0x{BMP280_CHIP_ID_VALUE:02X})")
                    return 0
                else:
                    print(f"  ✗ 验证失败! 芯片ID错误 (期望0x{BMP280_CHIP_ID_VALUE:02X})")
                    return 1
            else:
                print(f"  ✗ 读取失败: success={success}, data={read_data.hex() if read_data else 'None'}")
                return 1
        else:
            print(f"  ✗ 未知响应: msg_type=0x{msg_type:02X}")
            return 1
    
    except Exception as e:
        print(f"\n✗ 错误: {e}")
        import traceback
        traceback.print_exc()
        return 1
    finally:
        sock.close()
        print("\n连接已关闭")

if __name__ == '__main__':
    sys.exit(main())
