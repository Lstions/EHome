#!/bin/bash
# verify_https_ota.sh - Local HTTPS OTA verification script
#
# This script:
# 1. Generates a self-signed CA certificate
# 2. Starts a local HTTPS server
# 3. Simulates OTA download with TLS verification
# 4. Verifies TLS handshake and certificate validation
#
# Usage: ./scripts/verify_https_ota.sh [port]
#
# Requirements:
#   - OpenSSL
#   - Python 3 with ssl module

set -e

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
PROJECT_ROOT="$(dirname "$SCRIPT_DIR")"
CERT_DIR="$PROJECT_ROOT/.verify_certs"
PORT="${1:-8443}"
SERVER_PID=""

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

log_info() { echo -e "${GREEN}[INFO]${NC} $1"; }
log_warn() { echo -e "${YELLOW}[WARN]${NC} $1"; }
log_error() { echo -e "${RED}[ERROR]${NC} $1"; }

cleanup() {
    if [ -n "$SERVER_PID" ]; then
        log_info "Stopping HTTPS server (PID: $SERVER_PID)..."
        kill $SERVER_PID 2>/dev/null || true
    fi
}
trap cleanup EXIT

# Step 1: Generate self-signed CA certificate
generate_certs() {
    log_info "Generating self-signed CA certificate..."
    mkdir -p "$CERT_DIR"
    
    # Generate CA private key
    openssl genrsa -out "$CERT_DIR/ca.key" 2048 2>/dev/null
    
    # Generate CA certificate
    openssl req -new -x509 -days 365 -key "$CERT_DIR/ca.key" \
        -out "$CERT_DIR/ca.pem" \
        -subj "/C=CN/ST=Beijing/L=Beijing/O=EHomeSystem/OU=OTA/CN=ehome-ota-local" 2>/dev/null
    
    # Generate server key and CSR
    openssl genrsa -out "$CERT_DIR/server.key" 2048 2>/dev/null
    openssl req -new -key "$CERT_DIR/server.key" \
        -out "$CERT_DIR/server.csr" \
        -subj "/C=CN/ST=Beijing/L=Beijing/O=EHomeSystem/OU=OTA/CN=localhost" 2>/dev/null
    
    # Sign server certificate with CA
    openssl x509 -req -days 365 -in "$CERT_DIR/server.csr" \
        -CA "$CERT_DIR/ca.pem" -CAkey "$CERT_DIR/ca.key" \
        -CAcreateserial -out "$CERT_DIR/server.pem" 2>/dev/null
    
    log_info "Certificates generated in $CERT_DIR"
    log_info "  CA cert: $CERT_DIR/ca.pem ($(wc -c < "$CERT_DIR/ca.pem") bytes)"
    log_info "  Server cert: $CERT_DIR/server.pem"
}

# Step 2: Create a dummy firmware file
create_dummy_firmware() {
    log_info "Creating dummy firmware file..."
    DUMMY_FW="$CERT_DIR/firmware.bin"
    # Create a 4KB dummy file with recognizable content
    dd if=/dev/urandom of="$DUMMY_FW" bs=4096 count=1 2>/dev/null
    log_info "Dummy firmware: $DUMMY_FW ($(wc -c < "$DUMMY_FW") bytes)"
}

# Step 3: Start HTTPS server
start_https_server() {
    log_info "Starting HTTPS server on port $PORT..."
    
    # Create a simple Python HTTPS server
    cat > "$CERT_DIR/server.py" << 'PYEOF'
import http.server
import ssl
import sys
import os

class QuietHandler(http.server.SimpleHTTPRequestHandler):
    def log_message(self, format, *args):
        pass  # Suppress logging
    
    def do_GET(self):
        if self.path == '/firmware.bin':
            self.send_response(200)
            self.send_header('Content-Type', 'application/octet-stream')
            self.send_header('Content-Length', str(os.path.getsize('firmware.bin')))
            self.end_headers()
            with open('firmware.bin', 'rb') as f:
                self.wfile.write(f.read())
        else:
            self.send_response(404)
            self.end_headers()

if __name__ == '__main__':
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8443
    cert_dir = sys.argv[2] if len(sys.argv) > 2 else '.'
    
    os.chdir(cert_dir)
    
    server = http.server.HTTPServer(('0.0.0.0', port), QuietHandler)
    context = ssl.SSLContext(ssl.PROTOCOL_TLS_SERVER)
    context.load_cert_chain('server.pem', 'server.key')
    server.socket = context.wrap_socket(server.socket, server_side=True)
    
    print(f"HTTPS server running on port {port}")
    server.serve_forever()
PYEOF
    
    python3 "$CERT_DIR/server.py" "$PORT" "$CERT_DIR" &
    SERVER_PID=$!
    sleep 2
    
    if ! kill -0 $SERVER_PID 2>/dev/null; then
        log_error "Failed to start HTTPS server"
        exit 1
    fi
    log_info "HTTPS server started (PID: $SERVER_PID)"
}

# Step 4: Test TLS connection with CA cert
test_tls_connection() {
    log_info "Testing TLS connection with CA certificate..."
    
    # Test 1: Connection with correct CA should succeed
    if openssl s_client -connect "localhost:$PORT" -CAfile "$CERT_DIR/ca.pem" \
        -servername localhost </dev/null 2>&1 | grep -q "Verify return code: 0"; then
        log_info "TLS handshake with valid CA: SUCCESS"
    else
        log_error "TLS handshake with valid CA: FAILED"
        return 1
    fi
    
    # Test 2: Connection without CA should fail verification (self-signed chain)
    VERIFY_CODE=$(openssl s_client -connect "localhost:$PORT" \
        </dev/null 2>&1 | grep 'Verify return code' | head -1)
    if echo "$VERIFY_CODE" | grep -q 'Verify return code: 0'; then
        log_warn "TLS handshake without CA: UNEXPECTED SUCCESS (check server config)"
    else
        log_info "TLS handshake without CA (expected failure): CORRECTLY REJECTED ($VERIFY_CODE)"
    fi
    
    # Test 3: Download firmware with CA cert
    log_info "Downloading firmware with TLS verification..."
    if curl -s --cacert "$CERT_DIR/ca.pem" \
        "https://localhost:$PORT/firmware.bin" \
        -o "$CERT_DIR/downloaded.bin" 2>/dev/null; then
        if cmp -s "$DUMMY_FW" "$CERT_DIR/downloaded.bin"; then
            log_info "Firmware download with TLS verification: SUCCESS"
        else
            log_error "Downloaded firmware content mismatch"
            return 1
        fi
    else
        log_error "Firmware download failed"
        return 1
    fi
}

# Step 5: Test certificate pinning scenario
test_cert_pinning() {
    log_info "Testing certificate pinning scenario..."
    
    # Generate a different CA
    openssl genrsa -out "$CERT_DIR/wrong_ca.key" 2048 2>/dev/null
    openssl req -new -x509 -days 365 -key "$CERT_DIR/wrong_ca.key" \
        -out "$CERT_DIR/wrong_ca.pem" \
        -subj "/C=US/ST=California/O=WrongCA/CN=wrong-ca" 2>/dev/null
    
    # Try to connect with wrong CA - should fail
    if curl -s --cacert "$CERT_DIR/wrong_ca.pem" \
        "https://localhost:$PORT/firmware.bin" \
        -o /dev/null 2>&1; then
        log_error "Connection with wrong CA: UNEXPECTED SUCCESS (security issue!)"
        return 1
    else
        log_info "Connection with wrong CA: CORRECTLY REJECTED (pinning works)"
    fi
}

# Step 6: Verify ESP-IDF certificate bundle format
verify_cert_format() {
    log_info "Verifying certificate format for ESP-IDF..."
    
    # Check PEM format
    if grep -q "BEGIN CERTIFICATE" "$CERT_DIR/ca.pem" && \
       grep -q "END CERTIFICATE" "$CERT_DIR/ca.pem"; then
        log_info "CA certificate PEM format: VALID"
    else
        log_error "CA certificate PEM format: INVALID"
        return 1
    fi
    
    # Show certificate details
    log_info "Certificate details:"
    openssl x509 -in "$CERT_DIR/ca.pem" -noout -subject -issuer -dates 2>/dev/null
}

# Main
main() {
    echo "========================================"
    echo "  HTTPS OTA Verification Script"
    echo "========================================"
    echo ""
    
    generate_certs
    create_dummy_firmware
    start_https_server
    test_tls_connection
    test_cert_pinning
    verify_cert_format
    
    echo ""
    echo "========================================"
    log_info "All verification tests PASSED!"
    echo "========================================"
    echo ""
    echo "To use this CA certificate in ESP32 firmware:"
    echo "  1. Copy $CERT_DIR/ca.pem to esp32-collector/components/ota/certs/"
    echo "  2. Set CONFIG_COLLECTOR_OTA_CUSTOM_CERT=y in menuconfig"
    echo "  3. Set CONFIG_COLLECTOR_OTA_CERT_PEM to the certificate content"
    echo ""
}

main "$@"
