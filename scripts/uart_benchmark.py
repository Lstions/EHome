#!/usr/bin/env python3
"""UART0 ↔ ttyUSB0 performance benchmark for EHomeSystem ESP32-C6

Tests:
  1. ESP32→ttyUSB0 TX throughput (ESP32 sends, host receives)
  2. ttyUSB0→ESP32 RX throughput (host sends, ESP32 receives)
  3. Round-trip latency (host sends, ESP32 echoes back)
  4. Packet loss under burst

ESP32 UART0: HP_UART0, TX=GPIO16, RX=GPIO17, default 115200 baud
ttyUSB0: CP210x USB-UART bridge

The ESP32 is running the ehome_collector firmware which periodically sends
Modbus requests on UART0. We measure the raw byte throughput on the host side.
For echo test, we rely on the ESP32's RX_TASK echoing data back.
"""

import serial
import time
import struct
import threading
import statistics
import sys
from collections import defaultdict

ACM_PORT = '/dev/ttyACM0'   # ESP32 console (USB JTAG)
USB_PORT = '/dev/ttyUSB0'   # CP210x → UART0 (GPIO16/17)
BAUD = 115200
TEST_DURATION = 5           # seconds per test

def crc16_modbus(buf: bytes) -> int:
    crc = 0xFFFF
    for b in buf:
        crc ^= b
        for _ in range(8):
            crc = (crc >> 1) ^ 0xA001 if crc & 1 else crc >> 1
    return crc

def make_modbus_request(slave=1, fc=3, reg=0, count=1) -> bytes:
    """Build a valid Modbus RTU request frame"""
    frame = struct.pack('>BBHH', slave, fc, reg, count)
    crc = crc16_modbus(frame)
    return frame + struct.pack('<H', crc)

def make_modbus_response(slave=1, fc=3, data=b'\x00\x2B') -> bytes:
    """Build a valid Modbus RTU response frame"""
    frame = struct.pack('>BBB', slave, fc, len(data)) + data
    crc = crc16_modbus(frame)
    return frame + struct.pack('<H', crc)

def test_esp32_tx_throughput(baud=BAUD):
    """Test 1: Measure bytes received from ESP32 on ttyUSB0
    
    ESP32's scheduler sends Modbus requests every ~5s on UART0.
    We count all bytes received over the test window.
    """
    print(f"\n{'='*60}")
    print(f"TEST 1: ESP32→ttyUSB0 TX Throughput (baud={baud})")
    print(f"{'='*60}")
    
    ser = serial.Serial(USB_PORT, baud, timeout=0.1)
    time.sleep(0.5)
    ser.reset_input_buffer()
    
    total_bytes = 0
    frame_count = 0
    frame_sizes = []
    start_time = time.time()
    
    while time.time() - start_time < TEST_DURATION:
        data = ser.read(256)
        if data:
            total_bytes += len(data)
            # Count Modbus frames (look for 01 03 header)
            frame_count += data.count(b'\x01\x03')
            frame_sizes.append(len(data))
    
    elapsed = time.time() - start_time
    ser.close()
    
    throughput_bps = total_bytes * 8 / elapsed
    throughput_Bs = total_bytes / elapsed
    
    print(f"  Duration:       {elapsed:.2f}s")
    print(f"  Total bytes:    {total_bytes}")
    print(f"  Throughput:     {throughput_Bs:.1f} B/s ({throughput_bps:.0f} bps)")
    print(f"  Modbus frames:  ~{frame_count}")
    if frame_sizes:
        print(f"  Frame size:     min={min(frame_sizes)} max={max(frame_sizes)} avg={statistics.mean(frame_sizes):.1f}")
    print(f"  Theoretical max: {baud//10} B/s (8N1)")
    print(f"  Utilization:    {throughput_Bs/(baud//10)*100:.2f}%")
    
    return {
        'baud': baud,
        'throughput_Bs': throughput_Bs,
        'throughput_bps': throughput_bps,
        'total_bytes': total_bytes,
        'frames': frame_count,
        'utilization_pct': throughput_Bs/(baud//10)*100
    }

def test_host_tx_throughput(baud=BAUD):
    """Test 2: Measure host→ESP32 TX throughput
    
    Send as much data as possible to ESP32, measure write rate.
    ESP32 RX_TASK will process and the data appears in channel_data events.
    """
    print(f"\n{'='*60}")
    print(f"TEST 2: ttyUSB0→ESP32 TX Throughput (baud={baud})")
    print(f"{'='*60}")
    
    ser = serial.Serial(USB_PORT, baud, timeout=0.1)
    time.sleep(0.5)
    ser.reset_output_buffer()
    
    # Use a valid Modbus response frame as payload
    payload = make_modbus_response()
    payload_size = len(payload)  # 7 bytes
    
    total_bytes = 0
    write_count = 0
    errors = 0
    start_time = time.time()
    
    while time.time() - start_time < TEST_DURATION:
        try:
            written = ser.write(payload)
            total_bytes += written
            write_count += 1
            # Small delay to avoid overwhelming ESP32's 128B UART buffer
            # At 115200 baud, one byte takes ~87µs
            time.sleep(0.001)  # 1ms between frames ≈ 1000 frames/s max
        except Exception as e:
            errors += 1
            if errors > 10:
                break
    
    elapsed = time.time() - start_time
    ser.close()
    
    throughput_bps = total_bytes * 8 / elapsed
    throughput_Bs = total_bytes / elapsed
    
    print(f"  Duration:       {elapsed:.2f}s")
    print(f"  Total bytes:    {total_bytes}")
    print(f"  Writes:         {write_count}")
    print(f"  Errors:         {errors}")
    print(f"  Throughput:     {throughput_Bs:.1f} B/s ({throughput_bps:.0f} bps)")
    print(f"  Frame rate:     {write_count/elapsed:.1f} frames/s")
    print(f"  Frame size:     {payload_size} bytes")
    print(f"  Theoretical max: {baud//10} B/s (8N1)")
    print(f"  Utilization:    {throughput_Bs/(baud//10)*100:.2f}%")
    
    return {
        'baud': baud,
        'throughput_Bs': throughput_Bs,
        'throughput_bps': throughput_bps,
        'total_bytes': total_bytes,
        'writes': write_count,
        'errors': errors,
        'utilization_pct': throughput_Bs/(baud//10)*100
    }

def test_rtt_latency(baud=BAUD, count=50):
    """Test 3: Round-trip time measurement
    
    Send a Modbus request on ttyUSB0, wait for ESP32's next scheduled
    response or echo. Since ESP32 doesn't have a direct echo mode,
    we measure the time from send to receiving ANY data back.
    
    Note: True RTT requires ESP32 echo firmware. Here we measure
    the minimum observable response time using the scheduler's
    periodic Modbus exchange as a proxy.
    """
    print(f"\n{'='*60}")
    print(f"TEST 3: Round-Trip Latency (baud={baud}, count={count})")
    print(f"{'='*60}")
    
    ser = serial.Serial(USB_PORT, baud, timeout=2.0)
    acm = serial.Serial(ACM_PORT, baud, timeout=2.0)
    time.sleep(0.5)
    ser.reset_input_buffer()
    acm.reset_input_buffer()
    
    # Since ESP32 doesn't echo, we measure:
    # A) Send timestamp → first byte received on ttyUSB0 (RX_TASK → DataReport → next TX)
    # B) Time for our sent data to appear in ESP32 console logs
    
    latencies_ms = []
    acm_latencies_ms = []
    
    for i in range(count):
        ser.reset_input_buffer()
        acm.reset_input_buffer()
        
        t_send = time.time()
        ser.write(make_modbus_response())
        
        # Wait for ANY response on ttyUSB0 (next ESP32 TX)
        data = ser.read(64)
        t_recv = time.time()
        
        if data:
            rtt_ms = (t_recv - t_send) * 1000
            latencies_ms.append(rtt_ms)
        
        # Check ACM for RX_TASK hit
        acm_data = acm.read(512)
        if acm_data:
            acm_rtt = (time.time() - t_send) * 1000
            acm_latencies_ms.append(acm_rtt)
        
        # Wait between probes
        time.sleep(0.5)
    
    ser.close()
    acm.close()
    
    if latencies_ms:
        print(f"  ttyUSB0 RTT samples: {len(latencies_ms)}/{count}")
        print(f"  Min:    {min(latencies_ms):.1f} ms")
        print(f"  Max:    {max(latencies_ms):.1f} ms")
        print(f"  Mean:   {statistics.mean(latencies_ms):.1f} ms")
        print(f"  Median: {statistics.median(latencies_ms):.1f} ms")
        if len(latencies_ms) > 2:
            print(f"  Stdev:  {statistics.stdev(latencies_ms):.1f} ms")
        print(f"  P95:    {sorted(latencies_ms)[int(len(latencies_ms)*0.95)]:.1f} ms")
        print(f"  P99:    {sorted(latencies_ms)[int(len(latencies_ms)*0.99)]:.1f} ms")
    else:
        print(f"  No responses received!")
    
    # Frame transmission time at this baud
    frame_bits = 8 + 1 + 1  # 8 data + start + stop
    byte_time_us = frame_bits / (baud) * 1e6
    print(f"\n  Theoretical byte time: {byte_time_us:.1f} µs")
    print(f"  7-byte frame TX time:  {7*byte_time_us/1000:.2f} ms")
    
    return {
        'baud': baud,
        'samples': len(latencies_ms),
        'min_ms': min(latencies_ms) if latencies_ms else None,
        'max_ms': max(latencies_ms) if latencies_ms else None,
        'mean_ms': statistics.mean(latencies_ms) if latencies_ms else None,
        'median_ms': statistics.median(latencies_ms) if latencies_ms else None,
        'p95_ms': sorted(latencies_ms)[int(len(latencies_ms)*0.95)] if len(latencies_ms) > 1 else None,
    }

def test_burst_reliability(baud=BAUD, burst_size=100):
    """Test 4: Burst reliability — send many frames rapidly, check for data loss
    
    Send burst_size Modbus frames as fast as possible, then read back
    and count bytes received. Compare expected vs actual.
    """
    print(f"\n{'='*60}")
    print(f"TEST 4: Burst Reliability (baud={baud}, burst={burst_size})")
    print(f"{'='*60}")
    
    ser = serial.Serial(USB_PORT, baud, timeout=0.5)
    time.sleep(0.5)
    ser.reset_input_buffer()
    ser.reset_output_buffer()
    
    frame = make_modbus_response()
    expected_bytes = burst_size * len(frame)
    
    # Send burst
    start_time = time.time()
    for i in range(burst_size):
        ser.write(frame)
    send_elapsed = time.time() - start_time
    
    # Drain ESP32 console (ACM) to see if it processed everything
    time.sleep(1.0)
    
    # Read back any data ESP32 sent (next scheduled TX)
    total_received = 0
    read_start = time.time()
    while time.time() - read_start < 3.0:
        data = ser.read(512)
        if not data:
            break
        total_received += len(data)
    
    ser.close()
    
    send_rate = burst_size / send_elapsed
    theoretical_max = baud / (8 + 2)  # 8N1
    
    print(f"  Frame size:       {len(frame)} bytes")
    print(f"  Frames sent:      {burst_size}")
    print(f"  Expected TX:      {expected_bytes} bytes")
    print(f"  Send time:        {send_elapsed*1000:.1f} ms")
    print(f"  Send rate:        {send_rate:.0f} frames/s")
    print(f"  Send throughput:  {expected_bytes/send_elapsed:.1f} B/s")
    print(f"  RX after burst:   {total_received} bytes")
    print(f"  Theoretical max:  {theoretical_max:.0f} B/s")
    print(f"  Line utilization: {expected_bytes/send_elapsed/theoretical_max*100:.1f}%")
    
    # Check ACM for errors
    acm = serial.Serial(ACM_PORT, baud, timeout=1)
    acm_data = b''
    read_start = time.time()
    while time.time() - read_start < 2:
        chunk = acm.read(1024)
        if not chunk:
            break
        acm_data += chunk
    acm.close()
    
    acm_text = acm_data.decode('utf-8', errors='replace')
    rx_hits = acm_text.count('RX_TASK')
    errors = acm_text.count('error') + acm_text.count('Error')
    print(f"  ACM RX_TASK hits:  {rx_hits}")
    print(f"  ACM errors:        {errors}")
    
    return {
        'baud': baud,
        'burst_size': burst_size,
        'send_time_ms': send_elapsed * 1000,
        'send_rate_fps': send_rate,
        'rx_bytes': total_received,
        'acm_errors': errors,
    }

def test_byte_level_timing(baud=BAUD):
    """Test 5: Byte-level timing analysis
    
    Measure inter-byte and inter-frame gaps from ESP32's Modbus output.
    """
    print(f"\n{'='*60}")
    print(f"TEST 5: Byte-Level Timing Analysis (baud={baud})")
    print(f"{'='*60}")
    
    ser = serial.Serial(USB_PORT, baud, timeout=2.0)
    time.sleep(0.5)
    ser.reset_input_buffer()
    
    # Collect frames with timestamps
    frames = []
    byte_times = []
    last_byte_time = None
    
    start_time = time.time()
    while time.time() - start_time < 10:
        # Read one byte at a time for precise timing
        data = ser.read(1)
        now = time.time()
        if data:
            if last_byte_time:
                gap_us = (now - last_byte_time) * 1e6
                byte_times.append(gap_us)
            last_byte_time = now
            frames.append((now, data))
    
    ser.close()
    
    if not byte_times:
        print("  No data received for timing analysis")
        return None
    
    # Theoretical byte time
    theo_us = (8 + 2) / baud * 1e6  # start + 8 data + stop
    
    # Separate inter-frame (large gaps) from inter-byte (small gaps)
    inter_byte = [g for g in byte_times if g < theo_us * 3]
    inter_frame = [g for g in byte_times if g >= theo_us * 3]
    
    print(f"  Bytes received:     {len(frames)}")
    print(f"  Theoretical byte:   {theo_us:.1f} µs")
    print(f"  Inter-byte gaps:    {len(inter_byte)} samples")
    if inter_byte:
        print(f"    Min:  {min(inter_byte):.1f} µs")
        print(f"    Max:  {max(inter_byte):.1f} µs")
        print(f"    Mean: {statistics.mean(inter_byte):.1f} µs")
        print(f"    Stdev: {statistics.stdev(inter_byte):.1f} µs" if len(inter_byte) > 2 else "")
    print(f"  Inter-frame gaps:   {len(inter_frame)} samples")
    if inter_frame:
        print(f"    Min:  {min(inter_frame):.0f} µs ({min(inter_frame)/1000:.1f} ms)")
        print(f"    Max:  {max(inter_frame):.0f} µs ({max(inter_frame)/1000:.1f} ms)")
        print(f"    Mean: {statistics.mean(inter_frame):.0f} µs ({statistics.mean(inter_frame)/1000:.1f} ms)")
    
    return {
        'baud': baud,
        'theo_byte_us': theo_us,
        'bytes': len(frames),
        'inter_byte_mean_us': statistics.mean(inter_byte) if inter_byte else None,
        'inter_frame_mean_us': statistics.mean(inter_frame) if inter_frame else None,
    }

if __name__ == '__main__':
    baud = int(sys.argv[1]) if len(sys.argv) > 1 else BAUD
    
    print("="*60)
    print(f"UART0 ↔ ttyUSB0 Performance Benchmark")
    print(f"ESP32-C6 HP_UART0 (TX=GPIO16/RX=GPIO17)")
    print(f"CP210x USB-UART Bridge → /dev/ttyUSB0")
    print(f"Baud rate: {baud}")
    print(f"Test duration: {TEST_DURATION}s each")
    print("="*60)
    
    results = {}
    results['tx'] = test_esp32_tx_throughput(baud)
    results['host_tx'] = test_host_tx_throughput(baud)
    results['rtt'] = test_rtt_latency(baud, count=30)
    results['burst'] = test_burst_reliability(baud, burst_size=50)
    results['timing'] = test_byte_level_timing(baud)
    
    # Summary
    print(f"\n{'='*60}")
    print("SUMMARY")
    print(f"{'='*60}")
    print(f"  Baud rate:         {baud}")
    print(f"  ESP32→Host:        {results['tx']['throughput_Bs']:.1f} B/s ({results['tx']['utilization_pct']:.1f}% util)")
    print(f"  Host→ESP32:        {results['host_tx']['throughput_Bs']:.1f} B/s ({results['host_tx']['utilization_pct']:.1f}% util)")
    if results['rtt']['mean_ms']:
        print(f"  RTT mean:          {results['rtt']['mean_ms']:.1f} ms")
        print(f"  RTT min:           {results['rtt']['min_ms']:.1f} ms")
    print(f"  Burst 50 frames:   {results['burst']['send_time_ms']:.0f} ms")
    if results['timing'] and results['timing']['inter_byte_mean_us']:
        print(f"  Inter-byte gap:    {results['timing']['inter_byte_mean_us']:.1f} µs (theo: {results['timing']['theo_byte_us']:.1f} µs)")
