#!/usr/bin/env python3
"""
BMP280 SPI 性能验证 + 温度/气压读取

功能:
1. 读取芯片 ID (0xD0) → 期望 0x58
2. 读取校准参数 (0x88-0x9F, 26 bytes)
3. 触发测量 + 读取原始数据 (0xF7-0xFC, 6 bytes)
4. 使用 Bosch 补偿算法计算温度(°C)和气压(hPa)
5. SPI 吞吐量性能测试 (多次读取统计)

BMP280 SPI 协议:
- 读: tx=[reg_addr | 0x80, 0x00, ...] rx=[dummy, data0, data1, ...]
- 写: tx=[reg_addr & 0x7F, value]
"""

import socket
import time
import sys
import struct

HOST = '192.168.1.54'
PORT = 8088

# === Protobuf 编码 ===

def ev(value):
    r = []
    while value > 0x7F:
        r.append((value & 0x7F) | 0x80)
        value >>= 7
    r.append(value)
    return bytes(r)

def es(fn, s):
    d = s.encode('utf-8') if isinstance(s, str) else s
    return bytes([(fn << 3) | 2]) + ev(len(d)) + d

def evf(fn, v):
    return bytes([(fn << 3) | 0]) + ev(v)

def ebf(fn, d):
    return bytes([(fn << 3) | 2]) + ev(len(d)) + d

# === BMP280 补偿算法 (Bosch 官方) ===

class BMP280:
    def __init__(self, calib_data):
        """解析 26 字节校准数据"""
        if len(calib_data) < 26:
            raise ValueError(f"校准数据不足: {len(calib_data)} bytes")
        c = calib_data
        self.dig_T1 = struct.unpack('<H', c[0:2])[0]
        self.dig_T2 = struct.unpack('<h', c[2:4])[0]
        self.dig_T3 = struct.unpack('<h', c[4:6])[0]
        self.dig_P1 = struct.unpack('<H', c[6:8])[0]
        self.dig_P2 = struct.unpack('<h', c[8:10])[0]
        self.dig_P3 = struct.unpack('<h', c[10:12])[0]
        self.dig_P4 = struct.unpack('<h', c[12:14])[0]
        self.dig_P5 = struct.unpack('<h', c[14:16])[0]
        self.dig_P6 = struct.unpack('<h', c[16:18])[0]
        self.dig_P7 = struct.unpack('<h', c[18:20])[0]
        self.dig_P8 = struct.unpack('<h', c[20:22])[0]
        self.dig_P9 = struct.unpack('<h', c[22:24])[0]
        # c[24] = reserved, c[25] = reserved

    def compensate_temperature(self, adc_T):
        """Bosch 官方温度补偿, 返回 °C"""
        var1 = (adc_T / 16384.0 - self.dig_T1 / 1024.0) * self.dig_T2
        var2 = ((adc_T / 131072.0 - self.dig_T1 / 8192.0) *
                (adc_T / 131072.0 - self.dig_T1 / 8192.0)) * self.dig_T3
        return (var1 + var2) / 5120.0

    def compensate_pressure(self, adc_P, t_fine):
        """Bosch 官方气压补偿, 返回 hPa"""
        var1 = (t_fine / 2.0) - 64000.0
        var2 = var1 * var1 * self.dig_P6 / 32768.0
        var2 = var2 + var1 * self.dig_P5 * 2.0
        var2 = (var2 / 4.0) + (self.dig_P4 * 65536.0)
        var1 = (self.dig_P3 * var1 * var1 / 524288.0 + self.dig_P2 * var1) / 524288.0
        var1 = (1.0 + var1 / 32768.0) * self.dig_P1
        if var1 == 0.0:
            return 0.0
        p = 1048576.0 - adc_P
        p = (p - (var2 / 4096.0)) * 6250.0 / var1
        var1 = self.dig_P9 * p * p / 2147483648.0
        var2 = p * self.dig_P8 / 32768.0
        p = p + (var1 + var2 + self.dig_P7) / 16.0
        return p / 100.0  # Pa → hPa

    def get_t_fine(self, adc_T):
        """计算 t_fine 供气压补偿使用"""
        var1 = (adc_T / 16384.0 - self.dig_T1 / 1024.0) * self.dig_T2
        var2 = ((adc_T / 131072.0 - self.dig_T1 / 8192.0) *
                (adc_T / 131072.0 - self.dig_T1 / 8192.0)) * self.dig_T3
        return var1 + var2

# === TCP 通信 ===

class TCPClient:
    def __init__(self, host, port):
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.sock.settimeout(5)
        self.sock.connect((host, port))

    def send_config_manifest(self, freq_hz=1000000):
        """配置 SPI BMP280 通道"""
        tmpl = evf(1, 1) + ebf(2, bytes([0xD0, 0x00])) + evf(3, 2) + evf(4, 0)
        fc = freq_hz.to_bytes(4, 'big')
        bc = bytes([13, 0]) + fc + bytes([10, 11, 12, 0x01])
        ch = evf(1, 1) + evf(2, 0) + evf(3, 1) + evf(4, 1000) + evf(5, 1) + evf(6, 3) + ebf(7, bc)
        mf = es(1, 'spi-bmp280-perf') + evf(2, int(time.time())) + ebf(3, tmpl) + ebf(4, ch)
        msg = bytes([0x04]) + mf
        self.sock.sendall(msg)
        time.sleep(2)
        return self._recv()

    def write_cmd(self, req_id, channel_id, write_data, read_size):
        """发送 WriteCommand 并返回响应"""
        cmd = bytes([0x06])
        cmd += evf(1, req_id)
        cmd += evf(2, channel_id)
        cmd += ebf(3, write_data)
        cmd += evf(4, read_size)
        self.sock.sendall(cmd)
        time.sleep(0.3)
        return self._recv()

    def _recv(self):
        """接收所有响应数据"""
        self.sock.settimeout(3)
        all_data = b''
        while True:
            try:
                chunk = self.sock.recv(4096)
                if not chunk:
                    break
                all_data += chunk
            except socket.timeout:
                break
        return all_data

    def close(self):
        self.sock.close()

# === 解析 protobuf 响应 ===

def parse_response(data):
    """解析多个 protobuf 消息, 返回 [(msg_type, {field_num: value}), ...]"""
    messages = []
    pos = 0
    while pos < len(data):
        msg_type = data[pos]
        pos += 1
        fields = {}
        while pos < len(data):
            tag = data[pos]
            fn = tag >> 3
            wt = tag & 7
            pos += 1
            if fn == 0 and wt == 0:
                break
            if wt == 0:  # varint
                v = 0
                s = 0
                while pos < len(data):
                    b = data[pos]
                    pos += 1
                    v |= (b & 0x7F) << s
                    if not (b & 0x80):
                        break
                    s += 7
                fields[fn] = v
            elif wt == 2:  # bytes
                if pos >= len(data):
                    break
                l = data[pos]
                pos += 1
                d = data[pos:pos + l]
                pos += l
                fields[fn] = d
            else:
                break
        messages.append((msg_type, fields))
    return messages

# === SPI 读写辅助 ===

def spi_read(register, num_bytes):
    """生成 BMP280 SPI 读命令: [reg|0x80, dummy*] → [dummy, data*]"""
    return bytes([register | 0x80] + [0x00] * num_bytes), num_bytes + 1

def spi_write(register, value):
    """生成 BMP280 SPI 写命令: [reg&0x7F, value]"""
    return bytes([register & 0x7F, value]), 0

# === 主测试 ===

def main():
    print("=" * 70)
    print("  BMP280 SPI 性能验证 + 温度/气压读取")
    print("=" * 70)

    # 1. 连接
    print("\n[1] 连接 ESP32 TCP 服务器...")
    try:
        client = TCPClient(HOST, PORT)
        print(f"    ✓ 连接成功 {HOST}:{PORT}")
    except Exception as e:
        print(f"    ✗ 连接失败: {e}")
        return 1

    try:
        # 2. 配置 SPI 通道
        print("\n[2] 发送 ConfigManifest (SPI BMP280 @ 1MHz)...")
        rsp = client.send_config_manifest(freq_hz=1000000)
        msgs = parse_response(rsp)
        for mt, fields in msgs:
            if mt == 0x05:
                print(f"    ✓ ConfigResult: success={fields.get(2, '?')}")
            elif mt == 0x03:
                print(f"    ✓ DataReport: {fields.get(4, b'').hex()}")
            elif mt == 0x07:
                print(f"    ✓ WriteRsp: success={fields.get(2, '?')}")

        time.sleep(3)  # 等待总线初始化

        # 3. 读取芯片 ID
        print("\n[3] 读取芯片 ID (reg 0xD0)...")
        tx, read_size = spi_read(0xD0, 1)
        rsp = client.write_cmd(1, 1, tx, read_size)
        msgs = parse_response(rsp)
        chip_id = None
        for mt, fields in msgs:
            if mt == 0x07 and 4 in fields:
                data = fields[4]
                chip_id = data[1] if len(data) > 1 else None
                print(f"    SPI 原始数据: {data.hex()}")
                print(f"    芯片 ID: 0x{chip_id:02X}", end="")
                if chip_id == 0x58:
                    print(" ✓ (BMP280)")
                elif chip_id in (0x56, 0x57):
                    print(f" ⚠ (BMP280 变种)")
                else:
                    print(f" ✗ (期望 0x58)")
                    return 1
            elif mt == 0x07:
                print(f"    WriteRsp: success={fields.get(2, '?')}, err={fields.get(3, 0)}")
                if fields.get(4):
                    print(f"    error_msg: {fields[4]}")

        if chip_id != 0x58:
            print("    ✗ 芯片 ID 验证失败")
            return 1

        # 4. 读取校准参数
        print("\n[4] 读取校准参数 (reg 0x88, 26 bytes)...")
        tx, read_size = spi_read(0x88, 26)
        rsp = client.write_cmd(2, 1, tx, read_size)
        msgs = parse_response(rsp)
        calib_raw = None
        for mt, fields in msgs:
            if mt == 0x07 and 4 in fields:
                data = fields[4]
                # 跳过第一个 dummy 字节
                calib_raw = data[1:] if len(data) > 1 else data
                print(f"    SPI 原始数据 ({len(data)} bytes): {data[:16].hex()}...")
            elif mt == 0x07:
                print(f"    WriteRsp: success={fields.get(2, '?')}, err={fields.get(3, 0)}")
                if fields.get(4):
                    print(f"    error_msg: {fields[4]}")
                    return 1

        if not calib_raw or len(calib_raw) < 26:
            print(f"    ✗ 校准数据不足: {len(calib_raw) if calib_raw else 0} bytes")
            return 1

        bmp = BMP280(calib_raw)
        print(f"    ✓ 校准参数解析成功 (26 bytes)")
        print(f"    dig_T1={bmp.dig_T1}, dig_T2={bmp.dig_T2}, dig_T3={bmp.dig_T3}")
        print(f"    dig_P1={bmp.dig_P1}, dig_P2={bmp.dig_P2}, dig_P3={bmp.dig_P3}")
        print(f"    dig_P4={bmp.dig_P4}, dig_P5={bmp.dig_P5}, dig_P6={bmp.dig_P6}")
        print(f"    dig_P7={bmp.dig_P7}, dig_P8={bmp.dig_P8}, dig_P9={bmp.dig_P9}")

        # 5. 触发测量 + 读取温度/气压
        print("\n[5] 触发测量 (ctrl_meas=0xB5, forced mode, osrs_p=x16, osrs_t=x2)...")
        # 先写 config 寄存器 (0xF5): standby=1000ms, filter=off, spi3w=off
        tx_w, _ = spi_write(0xF5, 0b10000000)
        rsp = client.write_cmd(3, 1, tx_w, 0)
        time.sleep(0.1)

        # 写 ctrl_meas (0xF4): osrs_t=x2(010), osrs_p=x16(101), mode=forced(01)
        tx_w, _ = spi_write(0xF4, 0b01010101)
        rsp = client.write_cmd(4, 1, tx_w, 0)
        time.sleep(0.2)  # 等待测量完成 (~75ms)

        # 读取数据 (0xF7, 6 bytes: press[3] + temp[3])
        print("    读取测量数据 (reg 0xF7, 6 bytes)...")
        tx, read_size = spi_read(0xF7, 6)
        rsp = client.write_cmd(5, 1, tx, read_size)
        msgs = parse_response(rsp)
        raw_data = None
        for mt, fields in msgs:
            if mt == 0x07 and 4 in fields:
                data = fields[4]
                raw_data = data[1:] if len(data) > 1 else data  # skip dummy
                print(f"    SPI 原始数据: {data.hex()}")

        if not raw_data or len(raw_data) < 6:
            print(f"    ✗ 测量数据不足")
            return 1

        # 解析原始 ADC 值
        adc_P = (raw_data[0] << 12) | (raw_data[1] << 4) | (raw_data[2] >> 4)
        adc_T = (raw_data[3] << 12) | (raw_data[4] << 4) | (raw_data[5] >> 4)
        print(f"    ADC 原始值: press={adc_P}, temp={adc_T}")

        # 计算温度和气压
        temp = bmp.compensate_temperature(adc_T)
        t_fine = bmp.get_t_fine(adc_T)
        press = bmp.compensate_pressure(adc_P, t_fine)

        print(f"\n    ╔══════════════════════════════════╗")
        print(f"    ║  温度: {temp:8.2f} °C               ║")
        print(f"    ║  气压: {press:8.2f} hPa            ║")
        print(f"    ║  海拔: {(1 - (press/1013.25)**0.1903) * 44330:8.1f} m               ║")
        print(f"    ╚══════════════════════════════════╝")

        # 合理性检查
        if 10 < temp < 40:
            print(f"    ✓ 温度合理 ({temp:.1f}°C)")
        else:
            print(f"    ⚠ 温度异常 ({temp:.1f}°C, 期望 10-40°C)")

        if 900 < press < 1100:
            print(f"    ✓ 气压合理 ({press:.1f} hPa)")
        else:
            print(f"    ⚠ 气压异常 ({press:.1f} hPa, 期望 900-1100 hPa)")

        # 6. SPI 性能测试
        print(f"\n[6] SPI 吞吐量测试 (连续读取芯片 ID)...")
        tx_id, rs_id = spi_read(0xD0, 1)
        num_reads = 50
        latencies = []
        errors = 0
        start = time.time()
        for i in range(num_reads):
            t0 = time.time()
            rsp = client.write_cmd(100 + i, 1, tx_id, rs_id)
            t1 = time.time()
            latencies.append((t1 - t0) * 1000)  # ms
            msgs = parse_response(rsp)
            for mt, fields in msgs:
                if mt == 0x07 and fields.get(2) != 1:
                    errors += 1
        total_time = time.time() - start

        avg_latency = sum(latencies) / len(latencies)
        min_latency = min(latencies)
        max_latency = max(latencies)
        p95_latency = sorted(latencies)[int(len(latencies) * 0.95)]
        throughput = num_reads / total_time

        print(f"    测试次数: {num_reads}")
        print(f"    总时间:   {total_time:.2f}s")
        print(f"    成功次数: {num_reads - errors}/{num_reads}")
        print(f"    吞吐量:   {throughput:.1f} req/s (TCP往返)")
        print(f"    平均延迟: {avg_latency:.1f} ms")
        print(f"    最小延迟: {min_latency:.1f} ms")
        print(f"    最大延迟: {max_latency:.1f} ms")
        print(f"    P95延迟:  {p95_latency:.1f} ms")

        # 7. 连续温度/气压采样
        print(f"\n[7] 连续温度/气压采样 (5次, forced mode)...")
        temps = []
        presss = []
        for i in range(5):
            # 触发测量
            tx_w, _ = spi_write(0xF4, 0b01010101)
            client.write_cmd(200 + i * 2, 1, tx_w, 0)
            time.sleep(0.15)

            # 读取
            tx, rs = spi_read(0xF7, 6)
            rsp = client.write_cmd(201 + i * 2, 1, tx, rs)
            msgs = parse_response(rsp)
            for mt, fields in msgs:
                if mt == 0x07 and 4 in fields:
                    data = fields[4]
                    raw = data[1:] if len(data) > 1 else data
                    if len(raw) >= 6:
                        aP = (raw[0] << 12) | (raw[1] << 4) | (raw[2] >> 4)
                        aT = (raw[3] << 12) | (raw[4] << 4) | (raw[5] >> 4)
                        t = bmp.compensate_temperature(aT)
                        tf = bmp.get_t_fine(aT)
                        p = bmp.compensate_pressure(aP, tf)
                        temps.append(t)
                        presss.append(p)
                        print(f"    #{i+1}: 温度={t:.2f}°C  气压={p:.2f} hPa")

        if temps:
            print(f"\n    温度统计: avg={sum(temps)/len(temps):.2f}°C  "
                  f"min={min(temps):.2f}°C  max={max(temps):.2f}°C  "
                  f"std={((sum((t-sum(temps)/len(temps))**2 for t in temps)/len(temps))**0.5):.3f}°C")
            print(f"    气压统计: avg={sum(presss)/len(presss):.2f} hPa  "
                  f"min={min(presss):.2f} hPa  max={max(presss):.2f} hPa  "
                  f"std={((sum((p-sum(presss)/len(presss))**2 for p in presss)/len(presss))**0.5):.3f} hPa")

        print("\n" + "=" * 70)
        print("  ✅ BMP280 SPI 验证完成!")
        print("=" * 70)
        return 0

    except Exception as e:
        print(f"\n✗ 错误: {e}")
        import traceback
        traceback.print_exc()
        return 1
    finally:
        client.close()
        print("\n连接已关闭")

if __name__ == '__main__':
    sys.exit(main())
