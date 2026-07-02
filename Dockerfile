# EHomeSystem Production Dockerfile
# Build stage: Backend (Go)
FROM golang:1.25-alpine AS backend-builder

ENV GOPROXY=https://goproxy.cn,direct
WORKDIR /app
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ .
RUN CGO_ENABLED=0 GOOS=linux go build -o ehome-server ./cmd/server/

# Frontend build — we use the prebuilt dist from the host to avoid pnpm CI issues
FROM alpine:3.21

RUN apk --no-cache add ca-certificates tzdata && rm -rf /var/cache/apk/*

WORKDIR /app
COPY --from=backend-builder /app/ehome-server .
COPY frontend-shared/dist ./static/dist
RUN mkdir -p /app/firmwares
RUN ls -la /app/static/dist/

EXPOSE 8080

CMD ["./ehome-server"]
