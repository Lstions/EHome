#!/usr/bin/env python3
"""
UART监控脚本 - 支持串口重连
"""

import serial
import time
import sys
import os

def monitor_uart(log_file, duration=120):
    """监控UART输出，支持串口重连"""
    port = '/dev/ttyACM0'
    baudrate = 115200
    start_time = time.time()
    ser = None
    
    print(f'=== UART 监控开始 (持续 {duration} 秒) ===')
    sys.stdout.flush()
    
    with open(log_file, 'w') as f:
        f.write('=== UART 完整日志 ===\n')
        f.flush()
        
        while time.time() - start_time < duration:
            # 尝试连接串口
            if ser is None or not ser.is_open:
                try:
                    if ser is not None:
                        ser.close()
                    ser = serial.Serial(port, baudrate, timeout=0.3)
                    elapsed = time.time() - start_time
                    msg = f'[{elapsed:6.2f}s] === 串口已连接 ===\n'
                    print(msg.rstrip())
                    f.write(msg)
                    f.flush()
                except (serial.SerialException, OSError) as e:
                    time.sleep(0.1)
                    continue
            
            # 读取数据
            try:
                line = ser.readline().decode('utf-8', errors='ignore').rstrip()
                if line and not all(c == '\x00' for c in line):
                    elapsed = time.time() - start_time
                    log_line = f'[{elapsed:6.2f}s] {line}\n'
                    print(log_line.rstrip())
                    f.write(log_line)
                    f.flush()
                    sys.stdout.flush()
            except (serial.SerialException, OSError) as e:
                # 串口断开，准备重连
                elapsed = time.time() - start_time
                msg = f'[{elapsed:6.2f}s] === 串口断开，等待重连 ===\n'
                print(msg.rstrip())
                f.write(msg)
                f.flush()
                if ser is not None:
                    ser.close()
                ser = None
                time.sleep(0.5)
    
    if ser is not None and ser.is_open:
        ser.close()
    
    print('=== UART 监控结束 ===')

if __name__ == '__main__':
    log_file = sys.argv[1] if len(sys.argv) > 1 else '/tmp/uart_robust.log'
    duration = int(sys.argv[2]) if len(sys.argv) > 2 else 120
    monitor_uart(log_file, duration)
