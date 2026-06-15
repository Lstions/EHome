"""Decode EMQX auth for MQTT publish."""
import json, subprocess

# Check if paho-mqtt is available
try:
    import paho.mqtt.client as mqtt
    print("paho-mqtt available")
except ImportError:
    print("paho-mqtt NOT available, installing...")
    subprocess.run(['pip3', 'install', 'paho-mqtt'], capture_output=True)
    import paho.mqtt.client as mqtt

# Publish ConfigManifest directly to MQTT
# ConfigManifest fields: msg_type=0x04, field1=manifest_id, field4=channels
# Channel: id=8, hardware_id=2, bus_type=3(SPI), bus_config=0a030007a120 (6 bytes hex)

def e_varint(v):
    r=bytearray()
    while v>0x7F: r.append((v&0x7F)|0x80); v>>=7
    r.append(v&0x7F); return bytes(r)
def efv(f,v): return bytes([((f<<3)|0)])+e_varint(v)
def efb(f,d): return bytes([((f<<3)|2)])+e_varint(len(d))+d

# SPI bus_config: hex "0a030007a120" -> bytes
bus_config = bytes.fromhex('0a030007a120')  # CS=10, mode=3, freq=500000
print(f"SPI bus_config: {bus_config.hex()}")

# Channel sub-message: id=8, hw_id=2 (bytes), bus_type=3, bus_config
ch8 = efv(1, 8) + efb(2, b'\x02') + efv(4, 5000) + efv(5, 1) + efv(6, 3) + efb(7, bus_config)

# Keep existing UART channels
# Channel 1: UART TX=20, RX=21, 9600baud
bus1 = bytes([20, 21]) + (50000).to_bytes(4, 'big')  # wait, 9600 = 0x00002580
bus1 = bytes([20, 21, 0x00, 0x00, 0x25, 0x80])
ch1 = efv(1, 1) + efb(2, b'\x01') + efv(4, 5000) + efv(5, 1) + efv(6, 1) + efb(7, bus1)

# ConfigManifest: field1=manifest_id, field4=channels
manifest = bytes([0x04]) + efb(1, b'mqtt-spi-1') + efb(4, ch1) + efb(4, ch8)
print(f"ConfigManifest ({len(manifest)}B): {manifest.hex()[:100]}...")

# Publish to MQTT topic nodes/1001/down
client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2)
client.connect('localhost', 1884, 30)
client.publish('nodes/1001/down', manifest)
client.disconnect()
print("ConfigManifest published to nodes/1001/down")
