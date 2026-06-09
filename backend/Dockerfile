# Build stage
FROM golang:1.25-alpine AS builder

ENV GOPROXY=https://goproxy.cn,direct

WORKDIR /app
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o ehome-server ./cmd/server/

# Runtime stage (golang:alpine as base, avoids pulling alpine separately)
FROM golang:1.25-alpine

RUN apk --no-cache add ca-certificates tzdata
WORKDIR /app

COPY --from=builder /app/ehome-server .
EXPOSE 8080

CMD ["./ehome-server"]
