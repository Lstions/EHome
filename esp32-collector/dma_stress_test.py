#!/usr/bin/env python3
"""
UART DMA 性能压力测试 - 连续数据流测试
测试 ESP32 UART DMA 接收和处理能力
"""

import serial
import time
import sys
import os
from datetime import datetime

class DMAStressTest:
    def __init__(self, port='/dev/ttyUSB0', baud=115200):
        self.port = port
        self.baud = baud
        self.serial = None
        
    def connect(self):
        """连接到串口"""
        try:
            self.serial = serial.Serial(
                port=self.port,
                baudrate=self.baud,
                bytesize=serial.EIGHTBITS,
                parity=serial.PARITY_NONE,
                stopbits=serial.STOPBITS_ONE,
                timeout=0.1,
                write_timeout=1.0
            )
            time.sleep(0.5)  # 等待连接稳定
            return True
        except Exception as e:
            print(f"连接失败: {e}")
            return False
    
    def disconnect(self):
        """断开串口"""
        if self.serial and self.serial.is_open:
            self.serial.close()
    
    def send_continuous_stream(self, duration_sec=10, chunk_size=128):
        """
        发送连续数据流测试 DMA 接收能力
        
        Args:
            duration_sec: 测试持续时间（秒）
            chunk_size: 每次发送的数据块大小
        """
        if not self.serial or not self.serial.is_open:
            print("串口未连接")
            return False
        
        print(f"\n{'='*60}")
        print(f"DMA 压力测试: {self.baud} baud, {duration_sec}s, chunk={chunk_size}B")
        print(f"{'='*60}")
        
        # 生成测试数据（使用可识别的模式）
        test_data = bytes([i % 256 for i in range(chunk_size)])
        
        bytes_sent = 0
        start_time = time.time()
        last_report = start_time
        
        try:
            while (time.time() - start_time) < duration_sec:
                # 发送数据块
                try:
                    written = self.serial.write(test_data)
                    bytes_sent += written
                    
                    # 短暂延迟避免溢出
                    time.sleep(0.001)  # 1ms 间隔
                    
                except serial.SerialTimeoutException:
                    print("\n写入超时")
                    break
                except Exception as e:
                    print(f"\n写入错误: {e}")
                    break
                
                # 每秒报告一次
                current_time = time.time()
                if current_time - last_report >= 1.0:
                    elapsed = current_time - start_time
                    rate = bytes_sent / elapsed / 1024  # KB/s
                    print(f"  [{elapsed:.1f}s] 已发送: {bytes_sent:,} bytes "
                          f"({bytes_sent/1024:.1f} KB) - {rate:.1f} KB/s")
                    last_report = current_time
            
            # 最终统计
            total_time = time.time() - start_time
            total_kb = bytes_sent / 1024
            avg_rate = total_kb / total_time
            
            print(f"\n{'='*60}")
            print(f"测试完成:")
            print(f"  总发送: {bytes_sent:,} bytes ({total_kb:.2f} KB)")
            print(f"  耗时: {total_time:.2f} 秒")
            print(f"  平均速率: {avg_rate:.2f} KB/s ({avg_rate * 8:.1f} kbps)")
            print(f"  理论最大: {self.baud / 10 / 1024:.2f} KB/s")
            print(f"  利用率: {(avg_rate / (self.baud / 10 / 1024)) * 100:.1f}%")
            print(f"{'='*60}")
            
            return True
            
        except KeyboardInterrupt:
            print("\n\n用户中断测试")
            return False

def main():
    print("="*60)
    print("ESP32 UART DMA 性能压力测试")
    print("="*60)
    
    # 测试配置
    test_configs = [
        # (波特率, 持续时间, 块大小)
        (9600, 10, 64),
        (9600, 10, 256),
        (115200, 10, 64),
        (115200, 10, 256),
        (115200, 10, 512),
        (460800, 10, 128),
        (460800, 10, 256),
        (921600, 10, 256),
    ]
    
    tester = DMAStressTest()
    results = []
    
    try:
        for baud, duration, chunk in test_configs:
            tester.baud = baud
            
            if not tester.connect():
                print(f"无法连接到 {tester.port}")
                continue
            
            print(f"\n\n{'#'*60}")
            print(f"# 测试: {baud} baud, {chunk}B chunks, {duration}s")
            print(f"{'#'*60}\n")
            
            success = tester.send_continuous_stream(duration, chunk)
            
            tester.disconnect()
            
            if success:
                results.append((baud, chunk, duration))
            
            print("\n等待 2 秒...")
            time.sleep(2)
            
    except KeyboardInterrupt:
        print("\n\n测试被用户中断")
    finally:
        tester.disconnect()
    
    # 汇总
    print(f"\n\n{'='*60}")
    print("测试汇总")
    print(f"{'='*60}")
    print(f"\n完成的测试:")
    for baud, chunk, duration in results:
        print(f"  ✓ {baud:>7} baud, {chunk:>3}B chunks, {duration}s")
    
    print(f"\n总计: {len(results)}/{len(test_configs)} 个测试完成")
    print(f"{'='*60}")
    
    print("\n请检查 ESP32 串口日志以查看:")
    print("  - UART DMA 接收是否有错误")
    print("  - 是否有缓冲区溢出")
    print("  - 数据完整性")
    print("\n使用以下命令监控 ESP32:")
    print("  idf.py -p /dev/ttyACM0 monitor")

if __name__ == "__main__":
    main()
