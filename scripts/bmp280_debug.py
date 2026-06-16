#!/usr/bin/env python3
"""BMP280 SPI 调试版 - 带详细日志"""
import socket, time, sys, struct

HOST = '10.42.0.173'
PORT = 8088

def ev(v):
    r=[]
    while v>0x7F: r.append((v&0x7F)|0x80); v>>=7
    r.append(v); return bytes(r)

def es(fn,s):
    d=s.encode() if isinstance(s,str) else s
    return bytes([(fn<<3)|2])+ev(len(d))+d

def evf(fn,v): return bytes([(fn<<3)|0])+ev(v)
def ebf(fn,d): return bytes([(fn<<3)|2])+ev(len(d))+d

def recv_all(sock, timeout=2):
    sock.settimeout(timeout)
    data = b''
    while True:
        try:
            chunk = sock.recv(4096)
            if not chunk: break
            data += chunk
            sock.settimeout(0.3)
        except socket.timeout:
            break
    return data

def parse_one_message(data, pos):
    """解析单个 protobuf 消息，返回 (msg_type, fields, next_pos)"""
    if pos >= len(data):
        return None, {}, pos
    
    mt = data[pos]; pos += 1
    fields = {}
    
    while pos < len(data):
        tag = data[pos]; fn = tag >> 3; wt = tag & 7; pos += 1
        
        # 检查是否遇到下一个消息的类型标记（0x03=DataReport, 0x07=WriteRsp, 0x05=ConfigResult）
        # 这些值作为 tag 时，fn=0 表示无效字段，说明是下一个消息的开始
        if fn == 0 and mt in (0x03, 0x07, 0x05):
            pos -= 1  # 回退，让下一个消息解析这个字节
            break
        
        if wt == 0:  # varint
            v = 0; s = 0
            while pos < len(data):
                b = data[pos]; pos += 1
                v |= (b & 0x7F) << s
                if not (b & 0x80): break
                s += 7
            fields[fn] = v
        elif wt == 2:  # length-delimited
            if pos >= len(data): break
            l = data[pos]; pos += 1
            d = data[pos:pos+l]; pos += l
            fields[fn] = d
        else:
            # Unknown wire type, stop parsing this message
            pos -= 1  # 回退
            break
    
    return mt, fields, pos


def parse_write_rsp(data):
    """解析 WriteRsp (0x07) + DataReport (0x03) 消息"""
    msgs = []
    pos = 0
    print(f"  [parser] total {len(data)} bytes", flush=True)
    
    while pos < len(data):
        mt, fields, pos = parse_one_message(data, pos)
        if mt is None:
            break
        msgs.append((mt, fields))
        print(f"  [parser] msg_type=0x{mt:02X} fields={list(fields.keys())} next_pos={pos}", flush=True)
    
    # 返回 WriteRsp (0x07) 和 DataReport (0x03)
    result = [(mt,f) for mt,f in msgs if mt in (0x07, 0x03)]
    print(f"  [parser] filtered to {len(result)} messages", flush=True)
    return result

def parse_config_result(data):
    """解析 ConfigResult (0x05) 消息"""
    msgs = []
    pos = 0
    
    while pos < len(data):
        mt, fields, pos = parse_one_message(data, pos)
        if mt is None:
            break
        if mt == 0x05:
            msgs.append((mt, fields))
    
    return msgs

def main():
    print("[1] 连接...", flush=True)
    sock = socket.socket(socket.AF_INET, socket.SOCK_STREAM)
    sock.settimeout(5)
    sock.connect((HOST, PORT))
    print("    ✓ 连接成功", flush=True)

    # ConfigManifest
    print("[2] ConfigManifest...", flush=True)
    tmpl = evf(1,1)+ebf(2,bytes([0xD0,0x00]))+evf(3,2)+evf(4,0)
    fc=(1000000).to_bytes(4,'big')
    bc=bytes([13,0])+fc+bytes([10,11,12,0x01])
    ch=evf(1,1)+evf(2,0)+evf(3,1)+evf(4,1000)+evf(5,1)+evf(6,3)+ebf(7,bc)
    mf=es(1,'bmp280-dbg')+evf(2,int(time.time()))+ebf(3,tmpl)+ebf(4,ch)
    sock.sendall(bytes([0x04])+mf)
    print("    已发送, 等待响应...", flush=True)
    time.sleep(2)
    rsp = recv_all(sock, timeout=2)
    print(f"    响应: {len(rsp)} bytes = {rsp[:40].hex()}", flush=True)
    msgs = parse_config_result(rsp)
    for mt,f in msgs:
        print(f"    ConfigResult: success={f.get(2,0)}", flush=True)

    time.sleep(3)

    # 读芯片ID
    print("[3] 读芯片ID (0xD0)...", flush=True)
    tx = bytes([0xD0, 0x00])
    cmd = bytes([0x06])+evf(1,1)+evf(2,1)+ebf(3,tx)+evf(4,2)
    print(f"    cmd: {cmd.hex()}", flush=True)
    sock.sendall(cmd)
    time.sleep(0.5)
    rsp = recv_all(sock, timeout=2)
    print(f"    响应: {len(rsp)} bytes = {rsp.hex()}", flush=True)
    msgs = parse_write_rsp(rsp)
    for mt,f in msgs:
        print(f"    msg=0x{mt:02X}", flush=True)
        for k,v in f.items():
            if isinstance(v, bytes):
                print(f"      F{k}=[{v.hex()}] ({len(v)}B)", flush=True)
            else:
                print(f"      F{k}={v}", flush=True)

    # 读校准参数
    print("[4] 读校准参数 (0x88, 26 bytes)...", flush=True)
    tx = bytes([0x88]) + bytes(26)  # 0x88 | 0x80 = 0x88 (bit7 already set)
    cmd = bytes([0x06])+evf(1,2)+evf(2,1)+ebf(3,tx)+evf(4,27)
    print(f"    cmd: {cmd[:20].hex()}... ({len(cmd)}B)", flush=True)
    sock.sendall(cmd)
    time.sleep(0.5)
    rsp = recv_all(sock, timeout=2)
    print(f"    响应: {len(rsp)} bytes", flush=True)
    print(f"    原始: {rsp.hex()}", flush=True)
    msgs = parse_write_rsp(rsp)
    calib = None
    print(f"    解析到 {len(msgs)} 条消息", flush=True)
    for mt,f in msgs:
        if mt == 0x07:
            success = f.get(2, 0)
            err = f.get(3, 0)
            print(f"    WriteRsp: success={success} err={err} fields={list(f.keys())}", flush=True)
        elif mt == 0x03:
            # DataReport: field 4 = raw_data
            data = f.get(4, b'')
            print(f"    DataReport: data_len={len(data) if data else 0} fields={list(f.keys())}", flush=True)
            if data:
                print(f"    data: {data.hex()}", flush=True)
            # 只使用第一个长度正确的 DataReport（27 bytes = 1 dummy + 26 calib）
            if calib is None and len(data) == 27:
                calib = data[1:]  # skip dummy byte
                print(f"    ✓ 提取校准数据: {len(calib)} bytes", flush=True)

    if not calib or len(calib) < 26:
        print(f"    ✗ 校准数据不足: {len(calib) if calib else 0} bytes", flush=True)
        sock.close()
        return 1

    # 解析校准参数
    dig_T1 = struct.unpack('<H', calib[0:2])[0]
    dig_T2 = struct.unpack('<h', calib[2:4])[0]
    dig_T3 = struct.unpack('<h', calib[4:6])[0]
    dig_P1 = struct.unpack('<H', calib[6:8])[0]
    dig_P2 = struct.unpack('<h', calib[8:10])[0]
    dig_P3 = struct.unpack('<h', calib[10:12])[0]
    dig_P4 = struct.unpack('<h', calib[12:14])[0]
    dig_P5 = struct.unpack('<h', calib[14:16])[0]
    dig_P6 = struct.unpack('<h', calib[16:18])[0]
    dig_P7 = struct.unpack('<h', calib[18:20])[0]
    dig_P8 = struct.unpack('<h', calib[20:22])[0]
    dig_P9 = struct.unpack('<h', calib[22:24])[0]
    print(f"    ✓ dig_T: {dig_T1},{dig_T2},{dig_T3}", flush=True)
    print(f"    ✓ dig_P: {dig_P1},{dig_P2},{dig_P3},{dig_P4},{dig_P5},{dig_P6},{dig_P7},{dig_P8},{dig_P9}", flush=True)

    # 触发测量
    print("[5] 触发测量 (ctrl_meas=0xB5)...", flush=True)
    tx_w = bytes([0x74, 0xB5])  # 0xF4 & 0x7F = 0x74
    cmd = bytes([0x06])+evf(1,3)+evf(2,1)+ebf(3,tx_w)+evf(4,0)
    sock.sendall(cmd)
    time.sleep(0.5)
    recv_all(sock, timeout=1)
    time.sleep(0.2)

    # 读温度/气压
    print("[6] 读温度/气压 (0xF7, 6 bytes)...", flush=True)
    tx = bytes([0xF7]) + bytes(6)
    cmd = bytes([0x06])+evf(1,4)+evf(2,1)+ebf(3,tx)+evf(4,7)
    sock.sendall(cmd)
    time.sleep(0.5)
    rsp = recv_all(sock, timeout=2)
    print(f"    响应: {len(rsp)} bytes = {rsp.hex()}", flush=True)
    msgs = parse_write_rsp(rsp)
    raw = None
    for mt,f in msgs:
        if mt == 0x07:
            success = f.get(2, 0)
            print(f"    WriteRsp: success={success}", flush=True)
        elif mt == 0x03:
            data = f.get(4, b'')
            print(f"    DataReport: data_len={len(data) if data else 0}", flush=True)
            if data:
                print(f"    data: {data.hex()}", flush=True)
            # 只使用第一个长度正确的 DataReport（7 bytes = 1 dummy + 6 data）
            if raw is None and len(data) == 7:
                raw = data[1:]  # skip dummy byte
                print(f"    ✓ 提取温压数据: {len(raw)} bytes", flush=True)

    if not raw or len(raw) < 6:
        print("    ✗ 数据不足", flush=True)
        sock.close()
        return 1

    adc_P = (raw[0]<<12)|(raw[1]<<4)|(raw[2]>>4)
    adc_T = (raw[3]<<12)|(raw[4]<<4)|(raw[5]>>4)
    print(f"    ADC: press={adc_P} temp={adc_T}", flush=True)

    # 温度补偿
    var1 = (adc_T/16384.0 - dig_T1/1024.0) * dig_T2
    var2 = ((adc_T/131072.0 - dig_T1/8192.0)**2) * dig_T3
    t_fine = var1 + var2
    temp = t_fine / 5120.0

    # 气压补偿
    var1 = t_fine/2.0 - 64000.0
    var2 = var1*var1*dig_P6/32768.0 + var1*dig_P5*2.0
    var2 = var2/4.0 + dig_P4*65536.0
    var1 = (dig_P3*var1*var1/524288.0 + dig_P2*var1)/524288.0
    var1 = (1.0 + var1/32768.0) * dig_P1
    p = 1048576.0 - adc_P
    p = (p - var2/4096.0) * 6250.0 / var1
    var1 = dig_P9*p*p/2147483648.0
    var2 = p*dig_P8/32768.0
    p = p + (var1+var2+dig_P7)/16.0
    press = p / 100.0

    print(f"\n    ╔══════════════════════════╗", flush=True)
    print(f"    ║  温度: {temp:8.2f} °C         ║", flush=True)
    print(f"    ║  气压: {press:8.2f} hPa      ║", flush=True)
    alt = (1-(press/1013.25)**0.1903)*44330
    print(f"    ║  海拔: {alt:8.1f} m          ║", flush=True)
    print(f"    ╚══════════════════════════╝", flush=True)

    # SPI 性能测试
    print(f"\n[7] SPI 性能测试 (50次)...", flush=True)
    tx_id = bytes([0xD0, 0x00])
    latencies = []
    errors = 0
    t_start = time.time()
    for i in range(50):
        t0 = time.time()
        cmd = bytes([0x06])+evf(1,100+i)+evf(2,1)+ebf(3,tx_id)+evf(4,2)
        sock.sendall(cmd)
        time.sleep(0.1)
        rsp = recv_all(sock, timeout=1)
        t1 = time.time()
        latencies.append((t1-t0)*1000)
        msgs = parse_write_rsp(rsp)
        for mt,f in msgs:
            if mt == 0x07 and f.get(2) != 1:
                errors += 1
    total = time.time() - t_start
    avg = sum(latencies)/len(latencies)
    mn = min(latencies)
    mx = max(latencies)
    p95 = sorted(latencies)[int(len(latencies)*0.95)]
    print(f"    成功: {50-errors}/50", flush=True)
    print(f"    吞吐量: {50/total:.1f} req/s", flush=True)
    print(f"    延迟: avg={avg:.1f}ms min={mn:.1f}ms max={mx:.1f}ms P95={p95:.1f}ms", flush=True)

    sock.close()
    print("\n✅ 完成!", flush=True)
    return 0

if __name__ == '__main__':
    sys.exit(main())
