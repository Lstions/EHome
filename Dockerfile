# EHomeSystem Production Dockerfile
# Multi-stage build: backend (Go) + frontend (Vite) → single Alpine image.

# ── Stage 1: Backend build ──────────────────────────────────────────────
FROM golang:1.26.5-alpine AS backend-builder

# Go 模块代理：默认官方代理，保证 GitHub Actions 等海外 CI 直连可用；
# 国内网络本地构建时覆盖：docker build --build-arg GOPROXY=https://goproxy.cn,direct .
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
WORKDIR /app
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o ehome-server ./cmd/server/
# 主机运维 CLI (BMS 页慢修复 §1.4): 部署维护窗口需要
# `ehomectl datalifecycle ensure-indexes` 在启动新容器前建复合索引, 因此
# 二进制必须随镜像发布 (此前只 COPY ehome-server, 生产无法执行该子命令)。
RUN CGO_ENABLED=0 GOOS=linux go build -o ehomectl ./cmd/ehomectl/

# ── Stage 2: Frontend build ─────────────────────────────────────────────
FROM node:22-alpine AS frontend-builder

# 子路径前缀部署（反代 /ehome-dev 场景）：docker build --build-arg VITE_BASE_PATH=/ehome-dev/
# 与 VITE_API_BASE_URL / VITE_WS_URL 一起注入前端构建环境
ARG VITE_BASE_PATH
ARG VITE_API_BASE_URL
ARG VITE_WS_URL
ENV VITE_BASE_PATH=${VITE_BASE_PATH} \
    VITE_API_BASE_URL=${VITE_API_BASE_URL} \
    VITE_WS_URL=${VITE_WS_URL}

RUN corepack enable && corepack prepare pnpm@latest --activate
WORKDIR /app
COPY frontend-shared/package.json frontend-shared/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY frontend-shared/ .
ENV CI=true
RUN pnpm build

# ── Stage 3: Runtime ────────────────────────────────────────────────────
FROM alpine:3.21

RUN apk --no-cache add ca-certificates tzdata && rm -rf /var/cache/apk/*

WORKDIR /app
COPY --from=backend-builder /app/ehome-server .
# 运维 CLI: /app/ehomectl (供 docker run/exec 执行 datalifecycle ensure-indexes)
COPY --from=backend-builder /app/ehomectl .
COPY --from=frontend-builder /app/dist ./static/dist
RUN mkdir -p /app/firmwares

EXPOSE 8080

CMD ["./ehome-server"]
