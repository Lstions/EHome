#!/usr/bin/env python3
"""Trigger config sync for node 1001"""
import requests

BASE = "http://localhost:8081/api/v1"
r = requests.post(f"{BASE}/auth/login", json={"username":"admin","password":"admin123"})
token = r.json()["data"]["token"]
h = {"Authorization": f"Bearer {token}"}

# Trigger config sync
r = requests.post(f"{BASE}/nodes/1001/config/sync", headers=h)
print(f"Config sync: {r.status_code} {r.text[:200]}")

# Check node status
r = requests.get(f"{BASE}/nodes/1001", headers=h)
if r.status_code == 200:
    n = r.json().get("data", {})
    print(f"Node: status={n.get('status')} config_version={n.get('config_version')} channels={n.get('channel_count','?')}")

# Check channels
r = requests.get(f"{BASE}/channels", headers=h)
items = r.json().get("data", [])
for c in items:
    print(f"  Ch {c['id']}: type={c.get('hardware_type')} hw={c.get('hardware_id')} bus={c.get('bus_type')} node={c.get('node_id')}")
