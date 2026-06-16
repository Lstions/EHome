#!/usr/bin/env python3
"""Setup BMP280 channel + trigger config sync"""
import requests, json, struct

BASE = "http://localhost:8081/api/v1"

# Login
r = requests.post(f"{BASE}/auth/login", json={"username":"admin","password":"admin123"})
token = r.json()["data"]["token"]
headers = {"Authorization": f"Bearer {token}"}
print(f"Token: {token[:20]}...")

# List channels
r = requests.get(f"{BASE}/channels", headers=headers)
items = r.json().get("data", [])
print(f"Existing: {len(items)} channels")
for c in items:
    print(f"  id={c.get('id')} node={c.get('node_id')} type={c.get('hardware_type')} hw={c.get('hardware_id')} bus={c.get('bus_type')}")

# Delete all
for c in items:
    r = requests.delete(f"{BASE}/channels/{c['id']}", headers=headers)
    print(f"  Deleted {c['id']}: {r.status_code}")

# Create BMP280 SPI channel
bc = bytes([13, 0]) + struct.pack('>I', 1000000) + bytes([10, 11, 12, 0x01])
bc_hex = bc.hex()
print(f"\nCreating BMP280 SPI channel (bus_config={bc_hex})...")
payload = {
    "node_id": 1001,
    "hardware_type": "spi",
    "hardware_id": "spi2",
    "bus_type": "SPI",
    "bus_config": bc_hex,
    "interval_ms": 5000,
    "enabled": True
}
r = requests.post(f"{BASE}/channels", headers=headers, json=payload)
print(f"  Status: {r.status_code}")
print(f"  Response: {r.text[:200]}")

# Verify
r = requests.get(f"{BASE}/channels", headers=headers)
items = r.json().get("data", [])
print(f"\nChannels after: {len(items)}")
for c in items:
    print(f"  id={c.get('id')} node={c.get('node_id')} type={c.get('hardware_type')} hw={c.get('hardware_id')} bus={c.get('bus_type')} cfg={c.get('bus_config','')[:30]}")

# Trigger config sync
r = requests.post(f"{BASE}/collectors/1001/config/sync", headers=headers)
print(f"\nConfig sync: {r.status_code} {r.text[:200]}")
