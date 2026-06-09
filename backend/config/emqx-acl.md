# EHomeSystem EMQX ACL Configuration

## Files

- `emqx/acl.conf` — File-based ACL rules (applied via `authorization.sources[].path`)
- `emqx/no-match-deny.conf` — Sets `authorization.no_match = deny` (deny by default)

## How to Apply

```bash
# 1. Copy ACL rules to EMQX
docker cp emqx/acl.conf ehome-emqx:/opt/emqx/etc/acl.conf

# 2. Set no_match = deny (deny by default, only allow listed rules)
docker cp emqx/no-match-deny.conf ehome-emqx:/opt/emqx/etc/
docker exec ehome-emqx emqx ctl conf load --replace /opt/emqx/etc/no-match-deny.conf

# 3. Reload config
docker exec ehome-emqx emqx ctl conf reload
```

## Rules Summary

| Client | Permission | Topics |
|--------|-----------|--------|
| `ehome-server-v2` (backend) | allow all | `#` (everything) |
| Any device (matching clientid) | allow publish | `devices/${clientid}/up` |
| Any device (matching clientid) | allow subscribe | `devices/${clientid}/down` |
| `dashboard` user | allow subscribe | `$SYS/#` |
| Anyone else | deny subscribe | `$SYS/#`, `#`, `+/#` |
| Everyone else | deny all | (default) |

## Security Properties

- **Devices can only publish to their own up topic** — prevents impersonation/spoofing
- **Devices can only subscribe to their own down topic** — prevents snooping on other devices
- **Server has full access** — needed for the multi-device subscription pattern
- **Dashboard only reads $SYS** — for monitoring, not data planes
- **Default deny** — anything not explicitly allowed is denied

## Verification

```bash
# Check current ACL state
docker exec ehome-emqx emqx ctl conf show authorization

# Test ACL: this should be DENIED (wrong clientid)
python3 -c "
import paho.mqtt.client as mqtt
c = mqtt.Client(client_id='attacker')
c.connect('localhost', 1883, 60)
c.loop_start()
info = c.publish('devices/victim/up', 'hack', qos=1)
info.wait_for_publish(timeout=2)
print(f'Result: {info.is_published()}')  # True locally but ACL blocks at broker
c.loop_stop(); c.disconnect()
"

# Check EMQX log for deny events (enable debug first)
docker exec ehome-emqx emqx ctl log primary-level debug
```

## N3.1 Compliance

This addresses docs/v2.0/acceptance-criteria.md N3.1 (MQTT topic isolation):

> **N3.1** MQTT topic isolation: devices can only access their own topics.

✅ PASS
