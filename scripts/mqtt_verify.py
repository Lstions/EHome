#!/usr/bin/env python3
"""MQTT full-stack verification: REST API → MQTT → ESP32 → UART → CP210x"""
import json, subprocess, time, serial

# Get token
r = subprocess.run(['curl', '-s', 'http://localhost:8080/api/v1/auth/login',
    '-H', 'Content-Type: application/json',
    '-d', '{"username":"admin","password":"admin123"}'],
    capture_output=True, text=True)
token = json.loads(r.stdout)['data']['token']
print(f"Token obtained, len={len(token)}")

# Check channels
r = subprocess.run(['curl', '-s', 'http://localhost:8080/api/v1/channels',
    '-H', f'Authorization: Bearer {token}'],
    capture_output=True, text=True)
channels = json.loads(r.stdout)
data = channels.get('data', [])
if isinstance(data, list):
    print(f"\nChannels ({len(data)}):")
    for ch in data:
        print(f"  id={ch['id']} name={ch.get('name','')} bus_type={ch.get('bus_type','')} bus_id={ch.get('bus_id','')}")
else:
    print(f"\nChannels: {data}")

# Open CP210x for monitoring
cp = serial.Serial('/dev/ttyUSB0', 9600, timeout=3)
cp.reset_input_buffer()

# Send WriteCommand to channel 1 via API
hex_data = "48656c6c6f2066726f6d204d51545421"  # "Hello from MQTT!"
payload = json.dumps({"data": hex_data, "hex_mode": True})
r = subprocess.run(['curl', '-s', f'http://localhost:8080/api/v1/channels/1/write',
    '-H', f'Authorization: Bearer {token}',
    '-H', 'Content-Type: application/json',
    '-d', payload],
    capture_output=True, text=True)
print(f"\nWrite API response: {r.stdout[:200]}")

# Read CP210x
time.sleep(1)
data = cp.read(256)
cp.close()

print(f"\nCP210x received ({len(data)}B): {data.hex()}")
if data:
    try: 
        decoded = data.decode('utf-8', errors='replace')
        print(f"  Decoded: {decoded}")
    except: pass
    if b'Hello from MQTT' in data:
        print("\n✅ MQTT FULL-STACK VERIFIED: REST API → MQTT → ESP32 → UART → CP210x!")
    else:
        print(f"\n⚠️ Data mismatch")
else:
    print("\n❌ No data on CP210x")
