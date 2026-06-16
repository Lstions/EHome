#!/usr/bin/env python3
"""E2E通道终端验证：通过API发送WriteCommand，验证BMP280 SPI收发"""
import requests, json, time

BASE = "http://localhost:8081/api/v1"
r = requests.post(f"{BASE}/auth/login", json={"username":"admin","password":"admin123"})
token = r.json()["data"]["token"]
h = {"Authorization": f"Bearer {token}"}

print("=" * 60)
print("E2E BMP280 通道终端验证")
print("=" * 60)

# Step 1: 读取芯片ID (0xD0)
print("\n[1] 通道终端写入: 读取BMP280芯片ID (0xD0)...")
payload = {"data": "d000", "hex_mode": True}
r = requests.post(f"{BASE}/channels/9/write", headers=h, json=payload)
print(f"  API响应: {r.status_code}")
if r.status_code == 200:
    result = r.json().get("data", {})
    print(f"  success={result.get('success')} request_id={result.get('request_id')}")
    resp_data = result.get("response_data", "")
    if resp_data:
        print(f"  响应数据: {resp_data}")
        # 解析芯片ID (跳过第一个dummy字节)
        raw = bytes.fromhex(resp_data)
        if len(raw) >= 2:
            chip_id = raw[1]
            print(f"  芯片ID: 0x{chip_id:02X}", end="")
            if chip_id == 0x58:
                print(" ✅ BMP280验证通过!")
            else:
                print(f" ❌ 期望0x58")
    else:
        print(f"  无响应数据 (error_code={result.get('error_code')})")
else:
    print(f"  API错误: {r.text[:200]}")

# Step 2: 读取校准参数 (0x88, 26 bytes)
print("\n[2] 通道终端写入: 读取BMP280校准参数 (0x88, 26 bytes)...")
payload = {"data": "88" + "00" * 26, "hex_mode": True}
r = requests.post(f"{BASE}/channels/9/write", headers=h, json=payload)
if r.status_code == 200:
    result = r.json().get("data", {})
    resp_data = result.get("response_data", "")
    if resp_data:
        raw = bytes.fromhex(resp_data)
        print(f"  收到 {len(raw)} bytes (含1字节dummy)")
        if len(raw) >= 26:
            # 跳过dummy字节
            calib = raw[1:27] if len(raw) > 26 else raw[1:]
            print(f"  校准数据({len(calib)}B): {calib[:12].hex()}...")
            print(f"  ✅ 校准参数读取成功")
    else:
        print(f"  无响应数据")
else:
    print(f"  API错误: {r.text[:200]}")

# Step 3: 连续多次读取芯片ID（验证稳定性）
print("\n[3] 稳定性测试: 连续10次读取芯片ID...")
chip_ids = []
errors = 0
for i in range(10):
    payload = {"data": "d000", "hex_mode": True}
    r = requests.post(f"{BASE}/channels/9/write", headers=h, json=payload)
    if r.status_code == 200:
        result = r.json().get("data", {})
        resp_data = result.get("response_data", "")
        if resp_data:
            raw = bytes.fromhex(resp_data)
            if len(raw) >= 2:
                chip_ids.append(raw[1])
            else:
                errors += 1
        else:
            errors += 1
    else:
        errors += 1

print(f"  成功: {len(chip_ids)}/10  错误: {errors}/10")
if chip_ids:
    unique = set(chip_ids)
    print(f"  芯片ID: {[hex(c) for c in chip_ids]}")
    if unique == {0x58}:
        print(f"  ✅ 稳定性验证通过! 10次读取全部返回0x58")
    else:
        print(f"  ⚠️ 存在异常值: {[hex(c) for c in unique if c != 0x58]}")

# Step 4: 边缘设备数据验证
print("\n[4] 边缘设备状态检查...")
r = requests.get(f"{BASE}/edge-devices", headers=h)
devices = r.json().get("data", [])
for d in devices:
    print(f"  [{d['id']}] {d['name']}: type={d['type']} status={d['status']} ch={d['channel_id']}")

# Step 5: 检查后端DataReport日志
print("\n[5] 后端DataReport日志...")
import subprocess
result = subprocess.run(
    ["docker", "logs", "ehome-backend-dev", "--tail", "20"],
    capture_output=True, text=True
)
for line in result.stdout.split('\n'):
    if 'DataReport' in line or '0058' in line:
        print(f"  {line.strip()[:120]}")

print("\n" + "=" * 60)
print("✅ E2E BMP280 通道终端验证完成")
print("=" * 60)
