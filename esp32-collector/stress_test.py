#!/usr/bin/env python3
"""
UART DMA 压力测试
测试不同波特率、不同包大小下的 DMA 性能
指标：吞吐量、成功率、延迟
"""

import serial
import time
import struct
import sys
from dataclasses import dataclass
from typing import List, Optional
import statistics

@dataclass
class TestResult:
    baud_rate: int
    packet_size: int
    test_duration: float
    packets_sent: int
    packets_received: int
    bytes_sent: int
    bytes_received: int
    errors: int
    avg_latency_ms: float
    max_latency_ms: float
    min_latency_ms: float
    throughput_kbps: float
    success_rate: float

    def __str__(self):
        return (
            f"\n{'='*60}\n"
            f"测试配置:\n"
            f"  波特率: {self.baud_rate} baud\n"
            f"  包大小: {self.packet_size} bytes\n"
            f"  测试时长: {self.test_duration:.1f}s\n"
            f"\n性能指标:\n"
            f"  发送包数: {self.packets_sent}\n"
            f"  接收包数: {self.packets_received}\n"
            f"  成功率: {self.success_rate:.2f}%\n"
            f"  发送字节: {self.bytes_sent} ({self.bytes_sent/1024:.2f} KB)\n"
            f"  接收字节: {self.bytes_received} ({self.bytes_received/1024:.2f} KB)\n"
            f"  错误数: {self.errors}\n"
            f"\n延迟统计:\n"
            f"  平均: {self.avg_latency_ms:.2f} ms\n"
            f"  最小: {self.min_latency_ms:.2f} ms\n"
            f"  最大: {self.max_latency_ms:.2f} ms\n"
            f"\n吞吐量:\n"
            f"  {self.throughput_kbps:.2f} KB/s\n"
            f"  {self.throughput_kbps * 8:.2f} kbps\n"
            f"{'='*60}"
        )


class UARTStressTest:
    def __init__(self, port: str = '/dev/ttyUSB0', timeout: float = 2.0):
        self.port = port
        self.timeout = timeout
        self.serial: Optional[serial.Serial] = None
        
    def connect(self, baud_rate: int):
        """连接串口"""
        if self.serial and self.serial.is_open:
            self.serial.close()
        self.serial = serial.Serial(
            port=self.port,
            baudrate=baud_rate,
            bytesize=serial.EIGHTBITS,
            parity=serial.PARITY_NONE,
            stopbits=serial.STOPBITS_ONE,
            timeout=self.timeout
        )
        time.sleep(0.5)  # 等待串口稳定
        print(f"✓ 已连接 {self.port} @ {baud_rate} baud")
        
    def disconnect(self):
        """断开串口"""
        if self.serial and self.serial.is_open:
            self.serial.close()
            print("✓ 已断开连接")
            
    def create_test_packet(self, seq: int, size: int) -> bytes:
        """创建测试包: [seq:4][payload:size-4][checksum:1]"""
        if size < 5:
            size = 5
        
        # 序列号 (4字节)
        header = struct.pack('>I', seq)
        
        # 填充数据 (size - 5 字节)
        payload_size = size - 5
        payload = bytes([(i + seq) & 0xFF for i in range(payload_size)])
        
        # 校验和 (1字节)
        checksum = sum(header + payload) & 0xFF
        
        return header + payload + bytes([checksum])
    
    def verify_packet(self, packet: bytes, expected_seq: int) -> bool:
        """验证接收的包"""
        if len(packet) < 5:
            return False
        
        # 验证序列号
        seq = struct.unpack('>I', packet[:4])[0]
        if seq != expected_seq:
            return False
        
        # 验证校验和
        checksum = sum(packet[:-1]) & 0xFF
        return checksum == packet[-1]
    
    def run_test(self, baud_rate: int, packet_size: int, 
                 duration: float = 30.0, interval: float = 0.001) -> TestResult:
        """
        运行压力测试
        
        Args:
            baud_rate: 波特率
            packet_size: 包大小 (bytes)
            duration: 测试时长 (秒)
            interval: 发送间隔 (秒)
        """
        print(f"\n开始测试: {baud_rate} baud, {packet_size} bytes, {duration}s")
        print("-" * 60)
        
        self.connect(baud_rate)
        
        packets_sent = 0
        packets_received = 0
        bytes_sent = 0
        bytes_received = 0
        errors = 0
        latencies = []
        
        start_time = time.time()
        last_report = start_time
        
        try:
            while (time.time() - start_time) < duration:
                # 发送测试包
                seq = packets_sent
                packet = self.create_test_packet(seq, packet_size)
                
                send_time = time.time()
                self.serial.write(packet)
                self.serial.flush()
                packets_sent += 1
                bytes_sent += len(packet)
                
                # 等待回显
                response = self.serial.read(len(packet))
                recv_time = time.time()
                
                if response:
                    if self.verify_packet(response, seq):
                        packets_received += 1
                        bytes_received += len(response)
                        latency_ms = (recv_time - send_time) * 1000
                        latencies.append(latency_ms)
                    else:
                        errors += 1
                else:
                    errors += 1
                
                # 间隔
                if interval > 0:
                    time.sleep(interval)
                
                # 定期报告进度
                current_time = time.time()
                if current_time - last_report >= 5.0:
                    elapsed = current_time - start_time
                    rate = packets_received / elapsed if elapsed > 0 else 0
                    print(f"  进度: {elapsed:.1f}s, "
                          f"发送: {packets_sent}, "
                          f"接收: {packets_received}, "
                          f"速率: {rate:.1f} pkt/s")
                    last_report = current_time
                    
        except KeyboardInterrupt:
            print("\n测试被中断")
        finally:
            self.disconnect()
        
        # 计算统计
        test_duration = time.time() - start_time
        
        avg_latency = statistics.mean(latencies) if latencies else 0
        max_latency = max(latencies) if latencies else 0
        min_latency = min(latencies) if latencies else 0
        throughput = bytes_received / test_duration / 1024 if test_duration > 0 else 0
        success_rate = (packets_received / packets_sent * 100) if packets_sent > 0 else 0
        
        result = TestResult(
            baud_rate=baud_rate,
            packet_size=packet_size,
            test_duration=test_duration,
            packets_sent=packets_sent,
            packets_received=packets_received,
            bytes_sent=bytes_sent,
            bytes_received=bytes_received,
            errors=errors,
            avg_latency_ms=avg_latency,
            max_latency_ms=max_latency,
            min_latency_ms=min_latency,
            throughput_kbps=throughput,
            success_rate=success_rate
        )
        
        print(result)
        return result


def main():
    print("="*60)
    print("UART DMA 压力测试")
    print("="*60)
    
    tester = UARTStressTest(port='/dev/ttyUSB0', timeout=1.0)
    
    # 测试配置: (波特率, 包大小, 测试时长, 发送间隔)
    test_configs = [
        (9600, 64, 30.0, 0.001),      # 低速小包
        (9600, 256, 30.0, 0.001),     # 低速大包
        (115200, 64, 30.0, 0.001),    # 中速小包
        (115200, 512, 30.0, 0.001),   # 中速大包
        (460800, 128, 30.0, 0.0005),  # 高速中包
        (921600, 256, 30.0, 0.0005),  # 超高速
    ]
    
    results = []
    
    try:
        for baud, size, duration, interval in test_configs:
            print(f"\n\n{'#'*60}")
            print(f"# 测试: {baud} baud, {size} bytes")
            print(f"{'#'*60}\n")
            
            result = tester.run_test(baud, size, duration, interval)
            results.append(result)
            
            print("\n等待 3 秒...\n")
            time.sleep(3)
            
    except KeyboardInterrupt:
        print("\n\n测试被用户中断")
    finally:
        tester.disconnect()
    
    # 打印汇总
    print("\n\n" + "="*60)
    print("测试汇总")
    print("="*60)
    print(f"\n{'波特率':<12} {'包大小':<8} {'成功率':<10} {'吞吐量':<12} {'平均延迟':<10}")
    print("-" * 60)
    
    for r in results:
        print(f"{r.baud_rate:<12} {r.packet_size:<8} {r.success_rate:>6.1f}%   "
              f"{r.throughput_kbps:>8.2f} KB/s {r.avg_latency_ms:>7.2f} ms")
    
    print("\n" + "="*60)
    print("测试完成")
    print("="*60)


if __name__ == "__main__":
    main()