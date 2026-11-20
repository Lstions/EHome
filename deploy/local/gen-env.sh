set -euo pipefail
cd "$(dirname "$0")"
if [ ! -f .env ]; then
  umask 077
  cat > .env <<EOF
POSTGRES_USER=ehome
POSTGRES_PASSWORD=$(openssl rand -hex 24)
POSTGRES_DB=ehome
EHOME_JWT_SECRET=$(openssl rand -hex 48)
EHOME_EXTERNAL_HOST=192.168.20.3:18080
LOCAL_WEB_PORT=18080
LOCAL_DEVICE_PORT=18443
LOCAL_ADMIN_USERNAME=admin
LOCAL_ADMIN_PASSWORD=EHome-Local-$(openssl rand -hex 4)!
LOCAL_ADMIN_EMAIL=admin@ehome.local
EOF
fi
echo "env ready"
