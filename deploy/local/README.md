# 本机 Docker 部署（EHomeSystem）

隔离部署当前工作树，不占用既有 nginx / ehome-postgres / 生产 80/443 端口。

## 启动

```bash
cd deploy/local
./gen-env.sh                       # 首次生成 .env（DB/JWT/管理员口令）
docker compose --env-file .env -f docker-compose.yml up -d --build
./bootstrap-admin.sh               # 等待就绪、创建管理员、验证登录与节点
```

如果 Docker CLI 的 HOME 不可写（本机沙箱），构建时加：
```bash
DOCKER_CONFIG=$PWD/.docker-config docker compose --env-file .env -f docker-compose.yml build
```

## 访问

- 本机：<http://127.0.0.1:18080>
- 局域网：<http://192.168.20.3:18080>
- 管理员账号见 `bootstrap-admin.sh` 末尾输出。

## 真机链路

- 设备 mTLS 监听：`0.0.0.0:18443` → 容器 `18443`
- 服务端证书：`.secrets/pki/server-203.crt`（SAN 含 192.168.20.3 / 192.168.20.6 / 127.0.0.1）
- 客户端 CA：`.secrets/pki/ca.pem`
- 真机 C6 `F0F5BD02F35C` 已烧录指向 `192.168.20.3:18443` 的固件与 NVS 证书；启动后应自动上线。

## 运维

```bash
docker logs -f ehome-local-web
docker compose --env-file .env -f docker-compose.yml down
# 清空本机部署数据（不可恢复）：
# docker compose --env-file .env -f docker-compose.yml down -v
```
