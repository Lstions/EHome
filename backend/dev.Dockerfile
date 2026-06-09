# Build stage: Go tools
FROM golang:1.25-alpine AS builder

ENV GOPROXY=https://goproxy.cn,direct

# Install air for hot-reload
RUN go install github.com/air-verse/air@latest

# Runtime: minimal Go dev image
FROM golang:1.25-alpine

ENV GOPROXY=https://goproxy.cn,direct

RUN apk --no-cache add ca-certificates tzdata git

# Copy air binary from builder
COPY --from=builder /go/bin/air /usr/local/bin/air

# air will run in working_dir, source comes from bind mount
WORKDIR /app

EXPOSE 8080

CMD ["air", "-c", ".air.toml"]
