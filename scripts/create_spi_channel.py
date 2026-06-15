import json, subprocess

token = open('/tmp/eh_token.txt').read().strip()

# Check channels
r = subprocess.run(['curl', '-s', 'http://localhost:8080/api/v1/channels',
    '-H', 'Authorization: Bearer ' + token],
    capture_output=True, text=True)
items = json.loads(r.stdout).get('data', [])
print(f'Channels: {len(items)}')
has_spi = False
for ch in items:
    bt = ch.get('bus_type', '')
    print(f'  id={ch["id"]} name={ch.get("name","")} bus_type={bt} bus_id={ch.get("bus_id","")}')
    if bt == 'SPI':
        has_spi = True

if not has_spi:
    print('\nCreating SPI channel...')
    payload = json.dumps({
        "collector_id": 1,
        "name": "BMP280-SPI",
        "bus_type": "SPI",
        "bus_id": "spi2",
        "hardware_id": "spi2",
        "address": "0x76",
        "config": '{"bus_config":"0a030007a120"}',
        "interval_ms": 5000,
        "enabled": True
    })
    r = subprocess.run(['curl', '-s', '-X', 'POST',
        'http://localhost:8080/api/v1/channels',
        '-H', 'Authorization: Bearer ' + token,
        '-H', 'Content-Type: application/json',
        '-d', payload],
        capture_output=True, text=True)
    print(f'Create result: {r.stdout[:300]}')
else:
    print('\nSPI channel already exists')

# Trigger sync
print('\nTriggering config sync...')
r = subprocess.run(['curl', '-s', '-X', 'POST',
    'http://localhost:8080/api/v1/collectors/1/config/sync',
    '-H', 'Authorization: Bearer ' + token],
    capture_output=True, text=True)
print(f'Sync result: {r.stdout[:200]}')
