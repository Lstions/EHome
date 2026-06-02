#!/bin/bash
# EHomeSystem v2.0 - Build & Deploy Script

set -e

PROJECT_ROOT="/home/sun/workspace/EHomeSystem"
BACKEND_DIR="$PROJECT_ROOT/backend"
FRONTEND_DIR="$PROJECT_ROOT/frontend-shared"
ESP32_DIR="$PROJECT_ROOT/esp32-collector"

echo "========================================"
echo "EHomeSystem v2.0 - Build & Deploy"
echo "========================================"

# === Build Backend ===
echo ""
echo "[1/3] Building Go Backend..."
cd "$BACKEND_DIR"
go build -o bin/server ./cmd/server/
echo "Backend built: $BACKEND_DIR/bin/server"

# === Build Frontend ===
echo ""
echo "[2/3] Building Frontend..."
cd "$FRONTEND_DIR"
if [ -f "pnpm-lock.yaml" ]; then
    pnpm install
    pnpm build
elif [ -f "package-lock.json" ]; then
    npm install
    npm run build
else
    echo "Warning: No lock file found, skipping frontend build"
fi
echo "Frontend built"

# === ESP32 Build (optional) ===
echo ""
echo "[3/3] ESP32 Firmware (manual step)"
echo "cd $ESP32_DIR && idf.py build"

echo ""
echo "========================================"
echo "Build complete!"
echo ""
echo "Run backend:"
echo "  cd $BACKEND_DIR && ./bin/server"
echo ""
echo "Run frontend (dev):"
echo "  cd $FRONTEND_DIR && pnpm dev"
echo ""
echo "Flash ESP32:"
echo "  cd $ESP32_DIR && idf.py flash"
echo "========================================"
