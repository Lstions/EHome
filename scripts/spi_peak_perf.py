#!/usr/bin/env python3
"""BMP280 SPI 峰值性能压测 — 连续WriteCommand测试吞吐量"""
import socket, time, sys

HOST, PORT = '192.168.1.54', 8088

def ev(v):
    r=[]; 
    while v>0x7F: r.append((v&0x7F)|0x80); v>>=7
    r.append(v); return bytes(r)
def evf(fn,v): return bytes([(fn<<3)|0])+ev(v)
def ebf(fn,d): return bytes([(fn<<3)|2])+ev(len(d))+d
def es(fn,s): d=s.encode(); return bytes([(fn<<3)|2])+ev(len(d))+d

def send_cfg(sock):
    """SPI BMP280 config: MOSI=10 MISO=11 SCLK=12 CS=13, CH=1"""
    tmpl = evf(1,1)+ebf(2,bytes([0xD0,0x00]))+evf(3,2)+evf(4,0)
    bc = bytes([13,0])+(1000000).to_bytes(4,'big')+bytes([10,11,12,0x01])
    ch = evf(1,1)+evf(2,0)+evf(3,1)+evf(4,100)+evf(5,1)+evf(6,3)+ebf(7,bc)
    mf = es(1,'spi-perf')+evf(2,int(time.time()))+ebf(3,tmpl)+ebf(4,ch)
    sock.sendall(bytes([0x04])+mf)
    time.sleep(2)
    sock.settimeout(1)
    try:
        while True:
            c=sock.recv(4096)
            if not c: break
            sock.settimeout(0.2)
    except socket.timeout: pass

def send_write(sock, rid, ch, data, rs):
    cmd = evf(1,rid)+evf(2,ch)+ebf(3,data)+evf(4,rs)
    sock.sendall(bytes([0x06])+cmd)

def recv_all(sock, timeout=0.5):
    sock.settimeout(timeout)
    data=b''
    try:
        while True:
            c=sock.recv(4096)
            if not c: break
            data+=c; sock.settimeout(0.1)
    except socket.timeout: pass
    return data

def main():
    print("="*60)
    print("  BMP280 SPI 峰值性能压测")
    print("="*60)

    # Connect
    sock=socket.socket(); sock.settimeout(10)
    try:
        sock.connect((HOST,PORT))
        print(f"✓ 连接 {HOST}:{PORT}")
    except Exception as e:
        print(f"✗ 连接失败: {e}"); return 1

    try:
        # Send config
        print("\n📡 发送 SPI ConfigManifest...")
        send_cfg(sock)
        print("✓ 配置已发送")
        time.sleep(2)

        # Warmup
        print("\n🔥 预热 (10次)...")
        for i in range(10):
            send_write(sock, i, 1, bytes([0xD0,0x00]), 2)
            recv_all(sock, 0.5)
        print("✓ 预热完成")

        # Batch size sweep
        print("\n" + "="*60)
        print("  不同批次大小的吞吐量测试")
        print("="*60)

        for batch in [1, 5, 10, 20, 50]:
            rid_start = 1000
            total_tx = 0
            errors = 0
            t0 = time.time()

            for i in range(batch):
                rid = rid_start + i
                t_tx = time.time()
                send_write(sock, rid, 1, bytes([0xD0,0x00]), 2)
                data = recv_all(sock, 0.5)
                t_rx = time.time()
                total_tx += len(data) + 11  # approx msg size

                # Check for success
                if b'\x07' not in data:
                    errors += 1

            elapsed = time.time() - t0
            req_per_sec = batch / elapsed if elapsed > 0 else 0
            err_rate = errors / batch * 100 if batch > 0 else 0

            print(f"  batch={batch:3d}:  {req_per_sec:6.1f} req/s  "
                  f"err={errors}/{batch} ({err_rate:.0f}%)  "
                  f"{elapsed*1000/batch:5.1f} ms/req")

        # Sustained throughput (burst of 100)
        print(f"\n⚡ 持续吞吐量 (100次burst)...")
        n = 100
        times = []
        err = 0
        t_start = time.time()
        for i in range(n):
            t0 = time.time()
            send_write(sock, 2000+i, 1, bytes([0xD0,0x00]), 2)
            resp = recv_all(sock, 0.5)
            dt = time.time() - t0
            times.append(dt * 1000)
            if b'\x07' not in resp:
                err += 1
        t_total = time.time() - t_start
        avg = sum(times)/len(times)
        p95 = sorted(times)[int(len(times)*0.95)]
        p99 = sorted(times)[int(len(times)*0.99)]
        rps = n / t_total

        print(f"  请求数:   {n}")
        print(f"  总时间:   {t_total:.2f}s")
        print(f"  吞吐量:   {rps:.1f} req/s (TCP往返)")
        print(f"  错误数:   {err}/{n}")
        print(f"  平均延迟: {avg:.1f} ms")
        print(f"  P50延迟:  {sorted(times)[len(times)//2]:.1f} ms")
        print(f"  P95延迟:  {p95:.1f} ms")
        print(f"  P99延迟:  {p99:.1f} ms")
        print(f"  最小延迟: {min(times):.1f} ms")
        print(f"  最大延迟: {max(times):.1f} ms")

        # Theoretical max: burst without waiting for response
        print(f"\n🚀 极限吞吐量测试 (fire-and-forget, 100次)...")
        n2 = 100
        t0 = time.time()
        for i in range(n2):
            send_write(sock, 3000+i, 1, bytes([0xD0,0x00]), 2)
        t_tx_only = time.time() - t0
        print(f"  纯发送:   {n2/t_tx_only:.1f} req/s (无等待响应)")

        # Read back all pending responses
        time.sleep(0.5)
        all_rsp = recv_all(sock, 2)
        responses = all_rsp.count(b'\x07')
        print(f"  收到响应: {responses}/{n2}")

        print("\n" + "="*60)
        print("  ✅ SPI 峰值性能压测完成")
        print("="*60)

    except Exception as e:
        print(f"✗ 错误: {e}")
        import traceback; traceback.print_exc()
        return 1
    finally:
        sock.close()

if __name__ == '__main__':
    sys.exit(main())
