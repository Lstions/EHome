#!/usr/bin/env python3
"""E2E Final: TCP配置 + API写入 + 后端验证"""
import socket, time, requests, json, subprocess

HOST,PORT='192.168.1.54',8088

def ev(v):
    r=[]
    while v>0x7F:
        r.append((v&0x7F)|0x80)
        v>>=7
    r.append(v)
    return bytes(r)

def evf(fn,v):
    return bytes([(fn<<3)|0])+ev(v)

def ebf(fn,d):
    return bytes([(fn<<3)|2])+ev(len(d))+d

def es(fn,s):
    d=s.encode()
    return bytes([(fn<<3)|2])+ev(len(d))+d

# Step 1: TCP Config ch=9 SPI BMP280
print("Step 1: TCP配置 SPI BMP280 (ch=9)...")
s=socket.socket();s.settimeout(5);s.connect((HOST,PORT))
tmpl=evf(1,1)+ebf(2,bytes([0xD0,0x00]))+evf(3,2)+evf(4,0)
bc=bytes([13,0])+(1000000).to_bytes(4,'big')+bytes([10,11,12,0x01])
ch=evf(1,9)+evf(2,0)+evf(3,1)+evf(4,60000)+evf(5,1)+evf(6,3)+ebf(7,bc)
mf=es(1,'e2e-final')+evf(2,int(time.time()))+ebf(3,tmpl)+ebf(4,ch)
s.sendall(bytes([0x04])+mf)
time.sleep(3)
s.settimeout(1)
rsp=b''
try:
    while True:
        c=s.recv(4096)
        if not c:break
        rsp+=c;s.settimeout(0.2)
except socket.timeout:pass
s.close()
cr=rsp.count(b'\x05')
dr=rsp.count(b'\x03')
has_chip=b'\x00\x58' in rsp
print(f"  ConfigResult: {cr}  DataReport: {dr}  ChipID=0x58: {has_chip}")

# Step 2: Wait for MQTT DataReport to reach backend
print("\nStep 2: 等待后端接收DataReport...")
time.sleep(10)

# Step 3: Verify backend logs
print("Step 3: 后端DataReport日志:")
result = subprocess.run(
    ['docker','logs','ehome-backend-dev','--tail','20'],
    capture_output=True,text=True
)
found=0
for line in result.stdout.split('\n'):
    if 'DataReport' in line:
        print(f"  {line.strip()[:150]}")
        found+=1
    if '0058' in line:
        print(f"  {line.strip()[:150]}")
        found+=1
if not found:
    print("  (未找到DataReport)")

# Step 4: API写入测试
print("\nStep 4: API通道终端写入测试...")
BASE='http://localhost:8081/api/v1'
r=requests.post(f'{BASE}/auth/login',json={'username':'admin','password':'admin123'})
token=r.json()['data']['token']
h={'Authorization':f'Bearer {token}'}
payload={'data':'d000','hex_mode':True}
r=requests.post(f'{BASE}/channels/9/write',headers=h,json=payload)
resp=r.json()
print(f"  Status={r.status_code}")
print(f"  success={resp.get('data',{}).get('success')} error={resp.get('data',{}).get('error_msg','')}")

# Summary
print("\n" + "="*50)
print("  E2E BMP280验证完成")
print(f"  TCP通道: 芯片ID 0x58 {'✅' if has_chip else '❌'}")
print(f"  后端DataReport: {'✅' if found else '❌'}")
print("="*50)
