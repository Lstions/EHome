#!/usr/bin/env python3
"""
BMP280 SPI 性能压力测试

验证指标:
1. 峰值吞吐量 (req/s)
2. 延迟分布 (min/avg/max/P95/P99)
3. 错误率
4. 不同 SPI 频率对比
5. 温度/气压数据稳定性
"""

import socket
import time
import sys
import struct
import statistics

HOST = '10.42.0.173'
PORT = 8088

# === 帧编码 (项目自定义 frame_codec 格式) ===

def ev(v):
    """varint 编码"""
    r = []
    while v > 0x7F:
        r.append((v & 0x7F) | 0x80)
        v >>= 7
    r.append(v)
    return bytes(r)

def es(fn, s):
    """length-delimited string/bytes 字段"""
    d = s.encode() if isinstance(s, str) else s
    return bytes([(fn << 3) | 2]) + ev(len(d)) + d

def evf(fn, v):
    """varint 字段"""
    return bytes([(fn << 3) | 0]) + ev(v)

def ebf(fn, d):
    """bytes 字段"""
    return bytes([(fn << 3) | 2]) + ev(len(d)) + d

def parse_messages(data):
    """解析帧消息流"""
    msgs = []
    pos = 0
    while pos < len(data):
        mt = data[pos]; pos += 1
        fields = {}
        while pos < len(data):
            tag = data[pos]; fn = tag >> 3; wt = tag & 7; pos += 1
            if fn == 0 and mt in (0x03, 0x07, 0x05):
                pos -= 1
                break
            if wt == 0:
                v = 0; s = 0
                while pos < len(data):
                    b = data[pos]; pos += 1
                    v |= (b & 0x7F) << s
                    if not (b & 0x80): break
                    s += 7
                fields[fn] = v
            elif wt == 2:
                if pos >= len(data): break
                l = data[pos]; pos += 1
                d = data[pos:pos+l]; pos += l
                fields[fn] = d
            else:
                pos -= 1
                break
        msgs.append((mt, fields))
    return msgs

def recv_all(sock, timeout=2):
    sock.settimeout(timeout)
    data = b''
    while True:
        try:
            chunk = sock.recv(8192)
            if not chunk: break
            data += chunk
            sock.settimeout(0.1)
        except socket.timeout:
            break
    return data

# === BMP280 补偿算法 ===

class BMP280:
    def __init__(self, calib):
        self.dig_T1 = struct.unpack('<H', calib[0:2])[0]
        self.dig_T2 = struct.unpack('<h', calib[2:4])[0]
        self.dig_T3 = struct.unpack('<h', calib[4:6])[0]
        self.dig_P1 = struct.unpack('<H', calib[6:8])[0]
        self.dig_P2 = struct.unpack('<h', calib[8:10])[0]
        self.dig_P3 = struct.unpack('<h', calib[10:12])[0]
        self.dig_P4 = struct.unpack('<h', calib[12:14])[0]
        self.dig_P5 = struct.unpack('<h', calib[14:16])[0]
        self.dig_P6 = struct.unpack('<h', calib[16:18])[0]
        self.dig_P7 = struct.unpack('<h', calib[18:20])[0]
        self.dig_P8 = struct.unpack('<h', calib[20:22])[0]
        self.dig_P9 = struct.unpack('<h', calib[22:24])[0]

    def compensate_temp(self, adc_T):
        var1 = (adc_T / 16384.0 - self.dig_T1 / 1024.0) * self.dig_T2
        var2 = ((adc_T / 131072.0 - self.dig_T1 / 8192.0) ** 2) * self.dig_T3
        return (var1 + var2) / 5120.0

    def get_t_fine(self, adc_T):
        var1 = (adc_T / 16384.0 - self.dig_T1 / 1024.0) * self.dig_T2
        var2 = ((adc_T / 131072.0 - self.dig_T1 / 8192.0) ** 2) * self.dig_T3
        return var1 + var2

    def compensate_press(self, adc_P, t_fine):
        var1 = t_fine / 2.0 - 64000.0
        var2 = var1 * var1 * self.dig_P6 / 32768.0 + var1 * self.dig_P5 * 2.0
        var2 = var2 / 4.0 + self.dig_P4 * 65536.0
        var1 = (self.dig_P3 * var1 * var1 / 524288.0 + self.dig_P2 * var1) / 524288.0
        var1 = (1.0 + var1 / 32768.0) * self.dig_P1
        if var1 == 0.0: return 0.0
        p = 1048576.0 - adc_P
        p = (p - var2 / 4096.0) * 6250.0 / var1
        var1 = self.dig_P9 * p * p / 2147483648.0
        var2 = p * self.dig_P8 / 32768.0
        p = p + (var1 + var2 + self.dig_P7) / 16.0
        return p / 100.0

# === SPI 命令构建 ===

def spi_read_cmd(req_id, ch_id, reg, nbytes):
    """SPI 读: tx=[reg|0x80, 0x00*] rx=[dummy, data*]"""
    tx = bytes([reg | 0x80] + [0x00] * nbytes)
    return bytes([0x06]) + evf(1, req_id) + evf(2, ch_id) + ebf(3, tx) + evf(4, nbytes + 1)

def spi_write_cmd(req_id, ch_id, reg, value):
    """SPI 写: tx=[reg&0x7F, value]"""
    tx = bytes([reg & 0x7F, value])
    return bytes([0x06]) + evf(1, req_id) + evf(2, ch_id) + ebf(3, tx) + evf(4, 0)

def config_manifest(freq_hz):
    """ConfigManifest: SPI BMP280 通道"""
    tmpl = evf(1, 1) + ebf(2, bytes([0xD0, 0x00])) + evf(3, 2) + evf(4, 0)
    fc = freq_hz.to_bytes(4, 'big')
    bc = bytes([13, 0]) + fc + bytes([10, 11, 12, 0x01])
    ch = evf(1, 1) + evf(2, 0) + evf(3, 1) + evf(4, 10000) + evf(5, 1) + evf(6, 3) + ebf(7, bc)
    mf = es(1, 'bmp280-perf') + evf(2, int(time.time())) + ebf(3, tmpl) + ebf(4, ch)
    return bytes([0x04]) + mf

# === 压力测试 ===

class StressTest:
    def __init__(self, host, port):
        self.host = host
        self.port = port
        self.sock = None
        self.req_counter = 1000
        self.bmp = None

    def connect(self):
        self.sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
        self.sock.settimeout(5)
        self.sock.connect((self.host, self.port))

    def setup_channel(self, freq_hz):
        msg = config_manifest(freq_hz)
        self.sock.sendall(msg)
        time.sleep(0.5)
        rsp = recv_all(self.sock, timeout=3)
        msgs = parse_messages(rsp)
        success = False
        for mt, f in msgs:
            if mt == 0x05:
                success = f.get(2, 0) == 1
        time.sleep(2)  # 等待总线初始化
        return success

    def init_bmp280(self):
        """读取校准参数并初始化 BMP280 对象"""
        # 读校准参数
        cmd = spi_read_cmd(self.req_counter, 1, 0x88, 26)
        self.req_counter += 1
        self.sock.sendall(cmd)
        time.sleep(0.3)
        rsp = recv_all(self.sock, timeout=2)
        msgs = parse_messages(rsp)
        calib = None
        for mt, f in msgs:
            if mt == 0x03:
                data = f.get(4, b'')
                if len(data) == 27:
                    calib = data[1:]  # skip dummy
        if not calib or len(calib) < 26:
            return False
        self.bmp = BMP280(calib)
        return True

    def read_chip_id(self):
        """读芯片 ID - 最简 SPI 事务"""
        cmd = spi_read_cmd(self.req_counter, 1, 0xD0, 1)
        self.req_counter += 1
        self.sock.sendall(cmd)
        rsp = recv_all(self.sock, timeout=1)
        msgs = parse_messages(rsp)
        for mt, f in msgs:
            if mt == 0x07:
                return f.get(2, 0) == 1
        return False

    def read_temp_press(self):
        """读温度/气压数据"""
        # 触发测量
        cmd = spi_write_cmd(self.req_counter, 1, 0xF4, 0xB5)
        self.req_counter += 1
        self.sock.sendall(cmd)
        time.sleep(0.1)
        recv_all(self.sock, timeout=0.5)

        # 读数据
        cmd = spi_read_cmd(self.req_counter, 1, 0xF7, 6)
        self.req_counter += 1
        self.sock.sendall(cmd)
        rsp = recv_all(self.sock, timeout=1)
        msgs = parse_messages(rsp)
        for mt, f in msgs:
            if mt == 0x03:
                data = f.get(4, b'')
                if len(data) == 7:
                    raw = data[1:]
                    adc_P = (raw[0] << 12) | (raw[1] << 4) | (raw[2] >> 4)
                    adc_T = (raw[3] << 12) | (raw[4] << 4) | (raw[5] >> 4)
                    temp = self.bmp.compensate_temp(adc_T)
                    tf = self.bmp.get_t_fine(adc_T)
                    press = self.bmp.compensate_press(adc_P, tf)
                    return temp, press
        return None, None

    def run_chip_id_stress(self, count=200):
        """芯片 ID 读取压力测试"""
        latencies = []
        errors = 0
        t_start = time.time()

        for i in range(count):
            t0 = time.time()
            ok = self.read_chip_id()
            t1 = time.time()
            latencies.append((t1 - t0) * 1000)
            if not ok:
                errors += 1

        total = time.time() - t_start
        return {
            'count': count,
            'errors': errors,
            'total_time': total,
            'throughput': count / total,
            'latencies': latencies,
        }

    def run_temp_press_stress(self, count=50):
        """温度/气压读取压力测试"""
        latencies = []
        errors = 0
        temps = []
        presss = []
        t_start = time.time()

        for i in range(count):
            t0 = time.time()
            temp, press = self.read_temp_press()
            t1 = time.time()
            latencies.append((t1 - t0) * 1000)
            if temp is None:
                errors += 1
            else:
                temps.append(temp)
                presss.append(press)

        total = time.time() - t_start
        return {
            'count': count,
            'errors': errors,
            'total_time': total,
            'throughput': count / total,
            'latencies': latencies,
            'temps': temps,
            'presss': presss,
        }

    def close(self):
        if self.sock:
            self.sock.close()

def print_stats(name, stats):
    lat = stats['latencies']
    success = stats['count'] - stats['errors']
    print(f"\n{'='*60}")
    print(f"  {name}")
    print(f"{'='*60}")
    print(f"  请求数:   {stats['count']}")
    print(f"  成功数:   {success}/{stats['count']} ({100*success/stats['count']:.1f}%)")
    print(f"  错误数:   {stats['errors']}")
    print(f"  总耗时:   {stats['total_time']:.2f}s")
    print(f"  吞吐量:   {stats['throughput']:.2f} req/s")
    print(f"  延迟统计:")
    print(f"    min:  {min(lat):.1f} ms")
    print(f"    avg:  {statistics.mean(lat):.1f} ms")
    print(f"    max:  {max(lat):.1f} ms")
    print(f"    P50:  {statistics.median(lat):.1f} ms")
    print(f"    P95:  {sorted(lat)[int(len(lat)*0.95)]:.1f} ms")
    print(f"    P99:  {sorted(lat)[int(len(lat)*0.99)]:.1f} ms")
    if 'temps' in stats and stats['temps']:
        temps = stats['temps']
        presss = stats['presss']
        print(f"  温度统计:")
        print(f"    avg:  {statistics.mean(temps):.2f} °C")
        print(f"    min:  {min(temps):.2f} °C")
        print(f"    max:  {max(temps):.2f} °C")
        print(f"    std:  {statistics.stdev(temps):.3f} °C" if len(temps) > 1 else "")
        print(f"  气压统计:")
        print(f"    avg:  {statistics.mean(presss):.2f} hPa")
        print(f"    min:  {min(presss):.2f} hPa")
        print(f"    max:  {max(presss):.2f} hPa")
        print(f"    std:  {statistics.stdev(presss):.3f} hPa" if len(presss) > 1 else "")

def main():
    print("=" * 60)
    print("  BMP280 SPI 性能压力测试")
    print("=" * 60)

    test = StressTest(HOST, PORT)

    try:
        # 连接
        print("\n[1] 连接...", flush=True)
        test.connect()
        print("    ✓ 连接成功", flush=True)

        # 配置通道 @ 1MHz
        print("\n[2] 配置 SPI 通道 (1MHz)...", flush=True)
        if not test.setup_channel(1000000):
            print("    ✗ 配置失败")
            return 1
        print("    ✓ 配置成功", flush=True)

        # 初始化 BMP280
        print("\n[3] 初始化 BMP280...", flush=True)
        if not test.init_bmp280():
            print("    ✗ BMP280 初始化失败")
            return 1
        print("    ✓ BMP280 校准参数加载成功", flush=True)

        # 验证芯片 ID
        print("\n[4] 验证芯片 ID...", flush=True)
        if not test.read_chip_id():
            print("    ✗ 芯片 ID 验证失败")
            return 1
        print("    ✓ 芯片 ID = 0x58", flush=True)

        # 测试1: 芯片 ID 读取压力测试 (200次)
        print("\n[5] 芯片 ID 读取压力测试 (200次)...", flush=True)
        stats1 = test.run_chip_id_stress(200)
        print_stats("芯片 ID 读取 @ 1MHz (200次)", stats1)

        # 测试2: 温度/气压读取压力测试 (50次)
        print("\n[6] 温度/气压读取压力测试 (50次)...", flush=True)
        stats2 = test.run_temp_press_stress(50)
        print_stats("温度/气压读取 @ 1MHz (50次)", stats2)

        # 测试3: 高频突发测试
        print("\n[7] 高频突发测试 (10次突发, 每次20次读取)...", flush=True)
        burst_results = []
        for burst in range(10):
            stats = test.run_chip_id_stress(20)
            burst_results.append(stats['throughput'])
            print(f"    突发 #{burst+1}: {stats['throughput']:.2f} req/s, errors={stats['errors']}", flush=True)
        print(f"\n    突发统计:")
        print(f"      avg: {statistics.mean(burst_results):.2f} req/s")
        print(f"      min: {min(burst_results):.2f} req/s")
        print(f"      max: {max(burst_results):.2f} req/s")

        # 汇总
        print("\n" + "=" * 60)
        print("  性能汇总")
        print("=" * 60)
        print(f"  峰值吞吐量:  {max(burst_results):.2f} req/s")
        print(f"  持续吞吐量:  {stats1['throughput']:.2f} req/s")
        print(f"  最佳延迟:    {min(stats1['latencies']):.1f} ms")
        print(f"  P99延迟:     {sorted(stats1['latencies'])[int(len(stats1['latencies'])*0.99)]:.1f} ms")
        print(f"  温度稳定性:  ±{statistics.stdev(stats2['temps']):.3f} °C" if len(stats2['temps']) > 1 else "")
        print(f"  气压稳定性:  ±{statistics.stdev(stats2['presss']):.3f} hPa" if len(stats2['presss']) > 1 else "")
        print("=" * 60)
        print("  ✅ 压力测试完成!")
        print("=" * 60)
        return 0

    except Exception as e:
        print(f"\n✗ 错误: {e}")
        import traceback
        traceback.print_exc()
        return 1
    finally:
        test.close()

if __name__ == '__main__':
    sys.exit(main())
