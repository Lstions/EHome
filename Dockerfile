# EHomeSystem Production Dockerfile
# Multi-stage build: backend (Go) + frontend (Vite) → single Alpine image.

# ── Stage 1: Backend build ──────────────────────────────────────────────
FROM golang:1.27.3-alpine AS backend-builder

# Go 模块代理：默认官方代理，保证 GitHub Actions 等海外 CI 直连可用；
# 国内网络本地构建时覆盖：docker build --build-arg GOPROXY=https://goproxy.cn,direct .
ARG GOPROXY=https://proxy.golang.org,direct
# 构建来源（2026-10-21 事故教训：镜像必须能自证"由哪个 commit 构建"）
ARG GIT_COMMIT=unknown
# 构建时间（RFC3339 UTC）。CI 应显式传 --build-arg BUILD_TIME=$(date -u +%Y-%m-%dT%H:%M:%SZ)；
# 不传则留空（运行期端点返回空串，前端对不可解析时间**不判回滚**，不会误报）。
# 刻意不在这里 `date` 兜底：那会让同一 commit 的两次构建产出不同二进制。
ARG BUILD_TIME=
ENV GOPROXY=${GOPROXY} GIT_COMMIT=${GIT_COMMIT} BUILD_TIME=${BUILD_TIME}
WORKDIR /app
COPY backend/go.mod backend/go.sum ./
RUN go mod download
COPY backend/ .
# 发布清单进 embed（方案 §6.5 / T2.9）：仓库里的 backend/internal/buildinfo/release.json
# 只是 **dev 默认值**（干净 clone 也能 go build）；镜像必须用 deploy/release.json
# 覆盖后再 go build —— 否则线上报的 version/notes 与发布清单不是同一份文件。
# 人只维护 deploy/release.json 的 version 与 notes；commit/build_time 由下面 -ldflags 注入。
COPY deploy/release.json ./internal/buildinfo/release.json
# 同时把构建期已知的 commit/build_time 写回清单（§6.5 的"构建时填入并写入"），
# 让嵌进二进制的文件本身也是自描述的（strings/取证时能直接看到）。运行期仍以
# -ldflags 为准；即便这次替换因清单重排没命中，也不会改变 /health 与版本端点的取值。
RUN sed -i \
      -e "s/\"commit\": \"\"/\"commit\": \"${GIT_COMMIT}\"/" \
      -e "s/\"build_time\": \"\"/\"build_time\": \"${BUILD_TIME}\"/" \
      ./internal/buildinfo/release.json
RUN CGO_ENABLED=0 GOOS=linux go build -buildvcs=false \
    -ldflags="-X main.buildCommit=${GIT_COMMIT} -X main.buildTime=${BUILD_TIME}" \
    -o ehome-server ./cmd/server/
# 主机运维 CLI (BMS 页慢修复 §1.4): 部署维护窗口需要
# `ehomectl datalifecycle ensure-indexes` 在启动新容器前建复合索引, 因此
# 二进制必须随镜像发布 (此前只 COPY ehome-server, 生产无法执行该子命令)。
RUN CGO_ENABLED=0 GOOS=linux go build -o ehomectl ./cmd/ehomectl/

# ── Stage 2: Frontend build ─────────────────────────────────────────────
FROM node:24-alpine AS frontend-builder

# 子路径前缀部署（反代 /ehome-dev 场景）：docker build --build-arg VITE_BASE_PATH=/ehome-dev/
# 与 VITE_API_BASE_URL / VITE_WS_URL 一起注入前端构建环境
ARG VITE_BASE_PATH
ARG VITE_API_BASE_URL
ARG VITE_WS_URL
# 构建身份（T2.4）：VITE_BUILD_ID 必须与后端 buildinfo.BuildID **同值**，即
# GIT_COMMIT 的**前 7 位**（不是 12 位：前端按字符串全等比对，12 位会让旧页面
# 永远提示"有新版本"）。一般**不要传** VITE_BUILD_ID，下面会按 GIT_COMMIT 前 7 位
# 自动推导（与 internal/buildinfo.BuildID 同规则）；GIT_COMMIT 缺失/unknown ⇒ 两端都叫 dev。
ARG GIT_COMMIT=unknown
ARG VITE_BUILD_ID=
ARG VITE_BUILD_TIME=
ARG BUILD_TIME
ENV VITE_BASE_PATH=${VITE_BASE_PATH} \
    VITE_API_BASE_URL=${VITE_API_BASE_URL} \
    VITE_WS_URL=${VITE_WS_URL}

RUN corepack enable && corepack prepare pnpm@latest --activate
WORKDIR /app
COPY frontend-shared/package.json frontend-shared/pnpm-lock.yaml ./
RUN pnpm install --frozen-lockfile --ignore-scripts
COPY frontend-shared/ .
ENV CI=true
# 构建身份注入 + 确定性守卫（一次 RUN，避免多出一层可缓存中间状态）：
#   · build_id：未显式传 ⇒ GIT_COMMIT 前 7 位（= 后端 buildinfo.BuildID）；
#   · build_time：未显式传 ⇒ 固定哨兵 1970-01-01。**不能留空**：vite.config 在
#     空值时会 fallback 到 new Date()，同一 commit 的两次构建会得到不同 asset hash，
#     直接打破 deploy/blackbox/probes/07-reproducible.sh 的可重复构建断言（Q7）。
#     生产 CI 传 --build-arg BUILD_TIME=... 时两端同时拿到真实时间。
#
# 构建期断言（本段第一件事）：显式传入的 VITE_BUILD_ID 必须恰好 7 位。
# 为什么必须在这里拦：build_id 是「旧页面是否过期」的唯一判据，前端按字符串**全等**
# 比对自己与后端响应头 X-EHome-Build。若有人照旧注释传 12 位（git rev-parse --short=12），
# 两端永远不等 ⇒ 每个 /api 响应都判「服务端更新了」⇒ 旧页面常驻更新横幅，点刷新也不消失。
# 前端已改成不截断（静默修正会掩盖 CI 传错），所以这个错误只能、也只该在构建期暴露。
RUN set -eu; \
    requested_build_id="$(printf '%s' "${VITE_BUILD_ID}" | tr -d '[:space:]')"; \
    if [ -n "$requested_build_id" ] && [ "${#requested_build_id}" -ne 7 ]; then \
      echo "ERROR: VITE_BUILD_ID='$requested_build_id' 长度 ${#requested_build_id}，必须与后端 buildinfo.BuildID 一致（7 位短 SHA）。" >&2; \
      echo "       传 12 位（git rev-parse --short=12）会让前端 build id 永远 != 后端响应头 X-EHome-Build：" >&2; \
      echo "       每个 /api 响应都判「服务端更新了」⇒ 旧页面常驻更新横幅，点刷新也不消失。" >&2; \
      echo "       修法：不要传 VITE_BUILD_ID（会按 GIT_COMMIT 前 7 位自动推导），或显式传 7 位。" >&2; \
      exit 1; \
    fi; \
    case "${GIT_COMMIT}" in \
      ""|unknown|UNKNOWN) default_build_id="dev" ;; \
      *) default_build_id="$(printf '%s' "${GIT_COMMIT}" | cut -c1-7)" ;; \
    esac; \
    build_id="${requested_build_id:-$default_build_id}"; \
    build_time="${VITE_BUILD_TIME:-${BUILD_TIME:-1970-01-01T00:00:00Z}}"; \
    echo "frontend build identity: build_id=$build_id build_time=$build_time"; \
    VITE_BUILD_ID="$build_id" VITE_BUILD_TIME="$build_time" pnpm build

# ── Stage 3: Runtime ────────────────────────────────────────────────────
FROM alpine:3.24
ARG GIT_COMMIT=unknown
LABEL org.opencontainers.image.revision=${GIT_COMMIT} \
      org.opencontainers.image.title="EHomeSystem" \
      org.opencontainers.image.description="EHome backend + web (built from git archive of main)"

RUN apk --no-cache add ca-certificates tzdata && rm -rf /var/cache/apk/*

WORKDIR /app
COPY --from=backend-builder /app/ehome-server .
# 运维 CLI: /app/ehomectl (供 docker run/exec 执行 datalifecycle ensure-indexes)
COPY --from=backend-builder /app/ehomectl .
COPY --from=frontend-builder /app/dist ./static/dist
RUN mkdir -p /app/firmwares

EXPOSE 8080

CMD ["./ehome-server"]
