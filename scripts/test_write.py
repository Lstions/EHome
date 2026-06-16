#!/usr/bin/env python3
"""单个通道终端API测试"""
import requests, json

BASE = 'http://localhost:8081/api/v1'
r = requests.post(f'{BASE}/auth/login', json={'username':'admin','password':'admin123'})
token = r.json()['data']['token']
h = {'Authorization': f'Bearer {token}'}

payload = {'data': 'd000', 'hex_mode': True}
r = requests.post(f'{BASE}/channels/9/write', headers=h, json=payload)
print(f'Status: {r.status_code}')
print(json.dumps(r.json(), indent=2, ensure_ascii=False))
