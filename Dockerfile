# EHomeSystem Production Dockerfile
# Multi-stage build: backend (Go) + frontend (Vite) → single Alpine image.

# ── Stage 1: Backend build ──────────────────────────────────────────────
FROM golang:1.26.5-alpine AS backend-builder

ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /app
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o ehome-server ./cmd/server/

# ── Stage 2: Frontend build ─────────────────────────────────────────────
FROM node:22-alpine AS frontend-builder

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
COPY --from=frontend-builder /app/dist ./static/dist
RUN mkdir -p /app/firmwares

EXPOSE 8080

CMD ["./ehome-server"]
