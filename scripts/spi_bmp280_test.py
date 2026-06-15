#!/usr/bin/env python3
"""SPI BMP280 chip ID verification via TCP — write-then-read mode."""
import socket, struct, time

def e_varint(v):
    r=bytearray()
    while v>0x7F: r.append((v&0x7F)|0x80); v>>=7
    r.append(v&0x7F); return bytes(r)
def efv(f,v): return bytes([((f<<3)|0)])+e_varint(v)
def efb(f,d): return bytes([((f<<3)|2)])+e_varint(len(d))+d

def tcp_send(frame, timeout=5):
    s=socket.socket(); s.settimeout(timeout); s.connect(('192.168.1.50',8088))
    s.sendall(frame); time.sleep(0.3); r=b''
    try:
        while True:
            c=s.recv(4096)
            if not c: break
            r+=c
    except: pass
    s.close()
    return r

def parse_write_rsp(resp):
    """Return (success, error_code, error_msg, raw_data)."""
    pos=0; success=None; err=None; msg=None; raw=None
    while pos<len(resp):
        mt=resp[pos]; pos+=1
        if mt==0x07:
            while pos<len(resp):
                t=resp[pos]; fn=t>>3; wt=t&7; pos+=1
                if wt==0:
                    v=0;sh=0
                    while pos<len(resp): b=resp[pos];pos+=1;v|=(b&0x7F)<<sh
                    if b&0x80==0:break;sh+=7
                    if fn==2: success=v
                    elif fn==3: err=v
                elif wt==2:
                    ln=0;sh=0
                    while pos<len(resp): b=resp[pos];pos+=1;ln|=(b&0x7F)<<sh
                    if b&0x80==0:break;sh+=7
                    raw=resp[pos:pos+ln];pos+=ln
                    if fn==4: msg=raw
                else:break
            break
        elif mt==0x03:
            while pos<len(resp):
                t=resp[pos]; fn=t>>3; wt=t&7; pos+=1
                if wt==0:
                    v=0;sh=0
                    while pos<len(resp): b=resp[pos];pos+=1;v|=(b&0x7F)<<sh
                    if b&0x80==0:break;sh+=7
                elif wt==2:
                    ln=0;sh=0
                    while pos<len(resp): b=resp[pos];pos+=1;ln|=(b&0x7F)<<sh
                    if b&0x80==0:break;sh+=7
                    raw=resp[pos:pos+ln];pos+=ln
                else:break
            break
        else:break
    return success, err, msg, raw

# Clear old config
tcp_send(bytes([0x04])+efb(1,b'cls-spi'))
time.sleep(1)

# SPI bus_config: CS=10, mode=3 (BMP280 supports mode 0 and 3), freq=500kHz
# 6-byte format: [cs(1), mode(1), freq(4BE)]
bus_cfg=struct.pack('>BBI', 10, 3, 500000)
print(f"SPI bus_config (6B): {bus_cfg.hex()} (CS=10, mode=3, 500kHz)")

ch=efv(1,10)+efb(2,b'\x02')+efv(4,5000)+efv(5,1)+efv(6,3)+efb(7,bus_cfg)
mf=bytes([0x04])+efb(1,b'spi-final')+efb(4,ch)
r=tcp_send(mf)
success, err, msg, raw = parse_write_rsp(r)
print(f"ConfigResult: success={success}")

time.sleep(3)

# BMP280 chip ID read: register 0xD0, read bit = 0x80
# SPI full-duplex: send 2 bytes, receive 2 bytes
# Byte 0: 0xD0 | 0x80 = 0xD0 (register address with read bit)
# Byte 1: 0x00 (dummy, generates clock for MISO data)
read_cmd=bytes([0xD0|0x80, 0x00])
print(f"\nBMP280 read cmd: {read_cmd.hex()}")

wcmd=bytes([0x06])+efv(1,999)+efv(2,10)+efb(3,read_cmd)+efv(4,2)
r=tcp_send(wcmd)
success, err, msg, raw = parse_write_rsp(r)
print(f"WriteRsp: success={success}, err={err}, msg={msg}")
if raw:
    print(f"Raw data ({len(raw)}B): {raw.hex()}")
    if len(raw)>=2:
        chip_id=raw[1]
        print(f"Chip ID byte[1] = 0x{chip_id:02X}")
        if chip_id==0x58:
            print("\n✅ SPI BMP280 VERIFIED: Chip ID = 0x58")
        else:
            print(f"\n⚠️ Expected 0x58, got 0x{chip_id:02X}")
    else:
        print(f"\n⚠️ Raw data too short: {len(raw)} bytes")
else:
    print("\n❌ No raw data in response")
    print(f"Full response hex: {r.hex()}")
