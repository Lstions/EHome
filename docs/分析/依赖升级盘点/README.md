# 仓库全量依赖升级盘点（总览）

> **盘点日期**：2026-10-23（所有版本号均为当日实测）
> **范围**：Go 后端 + 前端 + 固件 + Docker/CI/工具链，覆盖全仓 11 个依赖清单
> **性质**：**纯分析，零改动**。所有依赖清单经 `git diff --quiet` 逐项核验，与 HEAD 完全一致（见 §7）
> **分报告**：[go.md](go.md) ｜ [frontend.md](frontend.md) ｜ [infra-firmware.md](infra-firmware.md)

---

## 0. 一页结论

| 生态 | 清单 | 依赖数 | 可升级 | 跨大版本 | 最高优先级动作 |
|---|---|---|---|---|---|
| Go 后端 | `backend/go.mod` | 92 模块 | **42** | **0** | 🔴 工具链 1.26.5→1.26.10（11 条 stdlib CVE） |
| Go 基准工具 | `scripts/bench/ehload/go.mod` | 8 模块 | 2 | 0 | 同批带上 |
| 前端 | `frontend-shared/package.json` | 21 直接 | **18** | 6 个包有 major 可升，但 **TS 7 是死路** | 🟠 先升 11 项安全批 |
| 固件 | `dependencies.lock` / `idf_component.yml` | 2 | **0** | 0 | 🟡 删死依赖 espressif/mqtt |
| Docker | `Dockerfile` + 2 个 compose | 8 镜像 | **7** | 5 | 🔴 alpine:3.21 **9 天后 EOL（2026-11-01）** + golang 旧 tag 或被下架 |
| CI | `.github/workflows/*.yml` | 9 actions | **9** | 9/9 全跨 | 🟠 全部需从 v4→v7（node24 runtime） |

**三句话总结：**

1. **真正的风险不在"依赖版本旧"，而在工具链和基础镜像。** Go 侧 12 个"可能有新主版本"的模块探测**全部 404**——不存在跨大版本升级；但 `go 1.26.5` 命中 **11 条标准库 CVE**（7 条有真实调用链），升依赖治不了，只能抬工具链。同样，`alpine:3.21` **2026-11-01 就 EOL**（距今 9 天），`golang:1.26.5-alpine` 已不在官方镜像清单里（有 manifest unknown 风险）。
2. **依赖侧本身可以安全批量升。** Go 侧实测 `go get -u ./...` 后 build/vet/**41 包测试全绿**，模块级可调用 CVE 归零。前端 11 项 patch/minor 实测 typecheck+build 通过。真正的坑集中在三处：**TS 7（会让 vue-tsc 崩溃）**、**vitest 5 与 coverage-v8 必须精确同版本**、**@vue/test-utils minor 却破坏类型**。
3. **发现两处"该删不该升"的死依赖。** 固件 `espressif/mqtt`（全仓 0 处真实调用，但仍被声明并编译进镜像）、后端 paho.mqtt（无 import，`go mod tidy` 唯一删除项）。删掉比升级更有价值，且顺带缩小供应链暴露面。

---

## 1. 按风险排序的行动清单

### P0 — 时间敏感 / 安全（建议本周）

| # | 项目 | 当前 → 目标 | 为什么急 | 验证方式 |
|---|---|---|---|---|
| 1 | **Go 工具链** | `1.26.5` → `1.26.10` | 11 条 stdlib CVE，7 条可达（HTTP/2 HPACK 竞态崩溃 GO-2026-6617、x509 中间证书信任 GO-2026-6809、TLS ECH）。修复点在 1.26.9/1.26.10 | 三处同步改：`go.mod` / `Dockerfile` / CI `GO_VERSION`；跑 `go test ./...` + `govulncheck` 复验 CVE 归零 |
| 2 | **alpine 运行时基础镜像** | `3.21` → `3.24` | **EOL 2026-11-01，仅剩 9 天**，之后无安全更新。本阶段只装 `ca-certificates tzdata`，升级面很小 | 容器启动 + TLS 出站验证（3.22+ 有 musl 变更） |
| 3 | **golang 构建镜像** | `1.26.5-alpine` → `1.26.10-alpine` | 官方镜像清单只保留每条 minor 线最新 patch，`1.26.5` 已不在清单 ⇒ 构建可能 `manifest unknown` | 有网机器 `docker manifest inspect golang:1.26.5-alpine` 确认；改后重建镜像 |
| 4 | **x/net** | `v0.57.0` → `v0.61.0` | GO-2026-6617 **CALLED**：`cmd/server/main.go:695` → `http2.Server.ServeConn` | 随 `go get -u` 一并升 + 单测 |
| 5 | **x/text** | `v0.40.0` → `v0.43.0` | GO-2026-6629 **CALLED**：`internal/database/gorm.go:33` → `gorm.Open` → `precis.Profile.String` | 同上 |
| 6 | **mimetype** | `v1.4.15` → `v1.4.16` | **当前版本已被上游 retract**（特制 CDF 二次复杂度 DoS） | 同上 |

### P1 — 可安全批量（低风险，收益明确）

| 生态 | 动作 | 实测结果 |
|---|---|---|
| Go backend | `go get -u ./...` → `go mod tidy` | build ✅ vet ✅ **test 41 包全绿 0 FAIL**（与基线逐项一致）；模块级可调用 CVE **2 → 0** |
| Go ehload | 同上（x/net v0.44.0 更旧） | — |
| 前端「安全批」11 项 | `pnpm up`（仅 patch/minor） | typecheck ✅ build ✅（另见 §2.2 的两个例外） |

> ⚠️ **pgx v5.10→v5.11 单独走 PR**：`Rows` 接口新增 `TypeMap`、日期解析器重写、容器内 user/$HOME 解析行为变化 → 需 `make test-integration`（需要真实 PG）。

### P2 — 需单独验证（跨版本，有实质变更）

| 项目 | 当前 → 目标 | 关键风险 |
|---|---|---|
| **Prometheus** | `v2.53.0` → `v3.15.1` | **2.x 已于 2025-07-31 EOL**。本仓配置只用基础项，v3 migration guide 的 5 条影响面已逐条核对（4 条不受影响，1 条 `auto-gomemlimit` 需观察内存曲线） |
| **Alertmanager** | `v0.27.0` → `v0.34.1` | 0.x 无 LTS；`log/slog` 切换、模板错误行为变更。本仓只用 `route/matchers/receivers/email_configs`，语法自 0.22 稳定 |
| **EMQX** | `5.8` → 先 `e5.10.5`，再评估 `6.3.2` | 5→6 官方 breaking-changes 文档 404（**未能查询**）；现用 `EMQX_LOADED_MODULES` 是 4.x 时代变量，6.x 是否仍认需实测 |
| **Node** | `22-alpine` → **`24-alpine`** | ⚠️ **不要跳 26**：Node 25/26 把 corepack 改为 `--with-corepack` opt-in，Dockerfile 的 `corepack enable` 会断 |
| **CI actions** | 9 个全升（v4→v7 类） | 全部要求 node24 runtime + runner ≥2.327.1。`pnpm/action-setup` 必须 ≥v6 才支持 pnpm 12 |

### P3 — 暂不建议 / 应删除

| 项目 | 结论 |
|---|---|
| **typescript → 7.0.2** | 🔴 **不可行**（已独立复现）：vue-tsc 用 `require.resolve('typescript/lib/tsc')`，TS 7 的 `exports` 不含该子路径 ⇒ `pnpm typecheck` **崩溃**（`ERR_PACKAGE_PATH_NOT_EXPORTED`），非类型报错。**天花板是 TS 6.0.3**。危险点：vue-tsc peer 是 `>=5.0.0`，`pnpm up --latest` 会**静默**选中 7.0.2 |
| **esp32 `espressif/mqtt`** | **删除**，不是升级。全仓 0 处非注释调用（已独立复核：`grep | grep -v 注释 = 0`），但 `idf_component.yml:17` 仍声明 `'*'`，每次干净构建仍会编译进镜像 |
| **backend `paho.mqtt.golang`** | **删除**。无 import，`go mod tidy -diff` 唯一删除项 |
| **redis:7-alpine 服务** | **删除服务**。后端已无 Redis 客户端（`go.mod` 无 redis 依赖，`login_limiter.go` 注明退役） |
| **Go 1.27.3** | 暂缓。1.26.10 已覆盖全部 CVE，跨 minor 需完整回归 |
| **ESP-IDF** | **无需动作**，6.1.0 已是最新稳定（无 6.2 正式版、无 7.x） |

---

## 2. 各生态详表要点

### 2.1 Go（[详见 go.md](go.md)）

- **0 个跨大版本**。实测探测 `gin/v2`、`gorm/v2`、`pgx/v6`、`mongo-driver/v3`、`quic-go/v2`、`jwt/v6`、`yaml.v4`、`paho/v2`、`client_golang/v2`、`websocket/v2`、`uuid/v2`、`zap/v2` **全部 404**（Lead 已独立复核）。
- 四个重点对象 `gin`/`gorm`/`pgx/v5`/`client_golang` **全部已是最新**。
- 唯一有实质 API 破坏的 0.x 跃迁：`quic-go 0.61.0 → 0.64.1`（本仓不调 http3 API）。
- `go.mod` 的 `go 1.26.5` 是**最低工具链硬要求**（非提示），且升完依赖后最高要求只到 1.26.0 ⇒ **不必动 go 行**。

### 2.2 前端（[详见 frontend.md](frontend.md)）

21 个直接依赖（8 deps + 13 devDeps），可升 18，已最新 3（echarts、@element-plus/icons-vue、unplugin-vue-components）。

**4 个分批：**

| 批次 | 内容 | 实测 |
|---|---|---|
| ① 安全批（11 项） | vue/router/pinia/element-plus/axios/vite/plugin-vue/unplugin-auto-import/@types/node/vue-virtual-scroller/@playwright… | typecheck ✅ build ✅ |
| ② 行为变更批（3 项，各自独立提交） | `@vue/test-utils` 2.4.11→2.5.1（**minor 但 typecheck 红 2 处**）；`vue-tsc` 2.2.10→3.3.13（红 1 处）；`happy-dom` 20→21（安全默认值变更，影响面小） | 已实测，见下 |
| ③ 测试框架批（2 项，**必须原子**） | `vitest` + `@vitest/coverage-v8` → 5.0.5 | peer 是**精确版本**，错配只 WARN 不拦截 |
| ④ 暂缓 | `typescript` → 7.0.2 | 🔴 崩溃 |

**实测矩阵（fe-analyst 在隔离副本做的 4 组对照，Lead 已独立验证 TS 7 的 exports 事实）：**

| 实验 | TS | vue-tsc | test-utils | typecheck |
|---|---|---|---|---|
| A 基线 | 5.9.3 | 2.2.10 | 2.4.11 | ✅ 0 error |
| B 只升 vue-tsc | 5.9.3 | 3.3.13 | 2.4.11 | ⚠️ 1 error |
| C 只升 test-utils | 5.9.3 | 2.2.10 | 2.5.1 | ⚠️ 2 error |
| D 升 TS 7 | 7.0.2 | 3.3.13 | 2.4.11 | 🔴 **崩溃** |

⚠️ **CI 硬约束**：`package.json` 必须与 `pnpm-lock.yaml` 同一次提交，否则 `pnpm install --frozen-lockfile` 报 `ERR_PNPM_OUTDATED_LOCKFILE`，CI 与 Dockerfile 同时挂。

### 2.3 固件 / Docker / CI（[详见 infra-firmware.md](infra-firmware.md)）

- **固件 0 项真升级**：ESP-IDF 6.1.0 与 espressif/mqtt 1.1.0 都已是最新。
- **Docker 7 项可升**：3 可批量（golang patch、alpine、postgres 18.6）+ 4 需验证（node 24、Prometheus v3、Alertmanager、EMQX）。
- **CI 9 个 action 全部跨大版本**：v4→v7 类，共性要求 node24 runtime。
- **工具链不一致 3 项**：Go 声明 1.26.5 / 本机 go1.27.1 / 最新 1.27.3；Node CI 22 vs 本机 24（**仓库无任何文件钉 Node 版本**）；pnpm 三处三值（CI 10 / Dockerfile `pnpm@latest` 漂移 / 本机 12.14.0）⇒ **Dockerfile 不可复现**。

---

## 3. 建议执行顺序（每次一个 PR，可回滚）

```bash
# ── PR-1：Go 工具链 + 依赖批量升级（P0+P1，收益最大）
#   1) 三处同步抬工具链
sed -i 's/^go 1.26.5/go 1.26.10/' backend/go.mod
sed -i 's|golang:1.26.5-alpine|golang:1.26.10-alpine|' Dockerfile
sed -i 's/GO_VERSION: "1.26.5"/GO_VERSION: "1.26.10"/' .github/workflows/test.yml
#   2) 依赖升级
cd backend && go get -u ./... && go mod tidy && go build ./... && go vet ./... && go test ./...
#   3) 删除死依赖 paho（tidy 会自动做）
#   4) 复验 CVE
govulncheck ./...
#   5) ehload 同步
cd ../scripts/bench/ehload && go get -u ./... && go mod tidy

# ── PR-2：pgx 单独升级（需真实 PG）
cd backend && go get github.com/jackc/pgx/v5@v5.11.0 && make test-integration

# ── PR-3：前端安全批（仅 patch/minor）
cd frontend-shared && pnpm up && pnpm typecheck && pnpm test:run && pnpm build
git add package.json pnpm-lock.yaml   # 必须同一次提交

# ── PR-4/5/6：前端行为变更批，各自独立
pnpm up @vue/test-utils@latest       # 修 2 处类型
pnpm up vue-tsc@latest               # 修 1 处类型
pnpm up happy-dom@latest

# ── PR-7：前端测试框架批（必须原子操作）
pnpm add -D vitest@5.0.5 @vitest/coverage-v8@5.0.5

# ── PR-8：Docker 基础镜像（时间敏感）
#   alpine:3.21 → 3.24；node:22-alpine → 24-alpine（不要 26）；postgres → 18.6-alpine
#   ⚠️ 与 PR-1 的 golang tag 一起，或分开均可

# ── PR-9：CI actions 批量（9 个）
#   注意 pnpm/action-setup 必须 ≥v6，且与 PNPM_VERSION 决定绑定

# ── PR-10：删死配置（mqtt / redis / emqx 若确认无消费者）
```

---

## 4. 每个生态的验证门禁

| 生态 | 命令 | 通过标准 |
|---|---|---|
| Go | `go build ./... && go vet ./... && go vet -tags=simulation ./... && go test -race ./...` + `govulncheck ./...` | 41 包全绿；模块级 CALLED CVE = 0 |
| 前端 | `pnpm typecheck && pnpm test:run && pnpm build` + `pnpm e2e`（需后端） | typecheck 0 error；测试无 worker 超时 |
| 固件 | 四 profile 实编（C6/S3/S3P）+ 真机启动 | 0 panic；heap 指标不退化 |
| Docker | 构建 + 容器启动 + TLS 出站 | 镜像可拉、服务健康 |
| CI | push 到分支观察全绿 | 9 个 job 全绿 |

---

## 5. 未能查询的项（如实标注，未编造）

| 项 | 原因 |
|---|---|
| 镜像 digest / 历史 tag 是否已被回收（如 `golang:1.26.5-alpine`） | **Docker Hub 与所有 registry 镜像站均不可达**；改用 `docker-library/official-images` 官方清单作权威替代源 |
| EMQX 5→6 breaking-changes 逐条 | 官方文档 404 |
| `gorm.io/driver/postgres v1.6.3` / `mongo-driver v2.9.x` release notes | GitHub 仓库路径 404 + API 未认证限流 |
| Go 漏洞库部分条目 CVSS 分数 | 上游 severity 为空；改用 govulncheck 的 CALLED/imported 判定 + 真实调用链 |
| 前端 e2e 全量 | 需真后端与浏览器，本次未启动服务 |

---

## 6. 本次未覆盖的范围

- `docs/`、`designs/`、`.analysis/`、`.ht_head/`、`.worktrees/`、`.fallback/` 下的**历史快照清单**（含 `go.mod`/`package.json`）——它们是归档副本，不参与构建，未纳入盘点。
- `scripts/*.py` 的 Python 第三方 import（无 requirements.txt，非项目依赖管理范畴）。

---

## 7. 零改动自证

```
backend/go.mod                              UNCHANGED vs HEAD
backend/go.sum                              UNCHANGED vs HEAD
scripts/bench/ehload/go.mod                 UNCHANGED vs HEAD
scripts/bench/ehload/go.sum                 UNCHANGED vs HEAD
frontend-shared/package.json                UNCHANGED vs HEAD
frontend-shared/pnpm-lock.yaml              UNCHANGED vs HEAD
Dockerfile                                  UNCHANGED vs HEAD
docker-compose.yml                          UNCHANGED vs HEAD
docker-compose.example.yml                  UNCHANGED vs HEAD
esp32-collector/main/idf_component.yml      UNCHANGED vs HEAD
esp32-collector/dependencies.lock           UNCHANGED vs HEAD
```

（`git diff --quiet` 逐项核验，2026-10-23 于 Lead 侧执行。）

> 注：`git status` 中 `backend/internal/**` 的数个 `.go` 文件显示为 modified，那是**本次会话开始前**就已存在的业务代码未提交改动，与依赖无关（已确认 diff 中无任何 import/版本变更），非本次盘点产生。
