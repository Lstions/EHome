#!/usr/bin/env python3
"""
Simple HTTP server for OTA testing
Serves firmware binary at /firmware.bin
"""
import http.server
import socketserver
import os
import hashlib

PORT = 8899
FIRMWARE_PATH = "build/ehome_collector.bin"

class OTAHandler(http.server.SimpleHTTPRequestHandler):
    def do_GET(self):
        if self.path == '/firmware.bin' or self.path == '/':
            if os.path.exists(FIRMWARE_PATH):
                with open(FIRMWARE_PATH, 'rb') as f:
                    data = f.read()
                    sha256 = hashlib.sha256(data).hexdigest()
                    size = len(data)
                    
                    self.send_response(200)
                    self.send_header('Content-Type', 'application/octet-stream')
                    self.send_header('Content-Length', str(size))
                    self.send_header('X-SHA256', sha256)
                    self.end_headers()
                    self.wfile.write(data)
                    
                    print(f"✓ Served firmware: {size} bytes, SHA256: {sha256}")
            else:
                self.send_error(404, "Firmware not found")
        else:
            self.send_error(404, "Not found")
    
    def log_message(self, format, *args):
        print(f"[HTTP] {args[0]}")

# Get local IP
import socket
s = socket.socket(socket.AF_INET, socket.SOCK_DGRAM)
try:
    s.connect(("10.42.0.1", 1))
    LOCAL_IP = s.getsockname()[0]
except:
    LOCAL_IP = "127.0.0.1"
finally:
    s.close()

print(f"OTA HTTP Server")
print(f"URL: http://{LOCAL_IP}:{PORT}/firmware.bin")
print(f"Firmware: {FIRMWARE_PATH}")

if os.path.exists(FIRMWARE_PATH):
    with open(FIRMWARE_PATH, 'rb') as f:
        data = f.read()
        print(f"Size: {len(data)} bytes")
        print(f"SHA256: {hashlib.sha256(data).hexdigest()}")
else:
    print(f"ERROR: {FIRMWARE_PATH} not found")

with socketserver.TCPServer(("", PORT), OTAHandler) as httpd:
    print(f"\nListening on port {PORT}...")
    print("Press Ctrl+C to stop\n")
    try:
        httpd.serve_forever()
    except KeyboardInterrupt:
        print("\nServer stopped")
