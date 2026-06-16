#!/usr/bin/env python3
"""Check E2E status"""
import requests, json

BASE = "http://localhost:8081/api/v1"
r = requests.post(f"{BASE}/auth/login", json={"username":"admin","password":"admin123"})
token = r.json()["data"]["token"]
h = {"Authorization": f"Bearer {token}"}

# Nodes
r = requests.get(f"{BASE}/nodes", headers=h)
nodes = r.json().get("data", [])
print(f"Nodes: {len(nodes)}")
for n in nodes:
    print(f"  id={n.get('id')} node_id={n.get('node_id')} status={n.get('status')} fw={n.get('firmware_version')}")

# Channels
r = requests.get(f"{BASE}/channels", headers=h)
channels = r.json().get("data", [])
print(f"\nChannels: {len(channels)}")
for c in channels:
    print(f"  id={c.get('id')} node_id={c.get('node_id')} type={c.get('hardware_type')} hw={c.get('hardware_id')} bus={c.get('bus_type')} cfg={c.get('bus_config','')[:20]}")

# Edge devices
r = requests.get(f"{BASE}/edge-devices", headers=h)
devices = r.json().get("data", [])
print(f"\nEdge Devices: {len(devices)}")
for d in devices:
    print(f"  id={d.get('id')} name={d.get('name')} node={d.get('node_id')} ch={d.get('channel_id')} type={d.get('type')} status={d.get('status')}")
