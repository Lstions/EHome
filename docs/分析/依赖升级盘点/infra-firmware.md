# 固件与基础设施依赖升级盘点（A 固件 / B Docker / C CI / D 工具链）

> 盘点日期：**2026-10-23**（全部版本号均为当日实测结果，Asia/Shanghai）
> 范围：`esp32-collector/`、`Dockerfile`、`docker-compose*.yml`、`deploy/**`、`.github/workflows/**`、本机 `.tools/`
> 性质：**只做分析，未修改任何依赖文件**（Dockerfile / compose / idf_component.yml / workflows 一律未动）

## 0. 数据来源与可达性（先说清楚哪些查到了、哪些没查到）

| 数据源 | 状态 | 用途 / 备注 |
|---|---|---|
| `hub.docker.com/v2/repositories/...` | ❌ **完全不可达**（curl 超时；`registry-1.docker.io`、`auth.docker.io`、`mirror.gcr.io` 同样超时） | 无法直接列 Docker Hub tag |
| `raw.githubusercontent.com/docker-library/official-images/master/library/*` | ✅ 200 | **官方镜像 tag 的权威来源**（Docker 官方镜像库清单），用它替代 Docker Hub API；B 节所有镜像 tag 来自这里 |
| `api.github.com/repos/*/releases` | ✅ 200（后段触发 60 次/小时匿名限流） | Prometheus / Alertmanager / EMQX / esp-mqtt / GitHub Actions 版本 |
| `api.github.com/repos/espressif/esp-idf/{releases,tags,branches}` + `master` 源码 | ✅ 200 | ESP-IDF 版本线 |
| `components.espressif.com/api/components/espressif/mqtt` | ✅ 200 | espressif/mqtt 组件全部版本 |
| `endoflife.date/api/*.json` | ✅ 200 | postgresql / redis / prometheus / nodejs / alpine-linux / docker-engine 的 EOL 与 latest 交叉校验 |
| `go.dev/dl/?mode=json&include=all` | ✅ 200 | Go 全部 patch 版本 |
| `nodejs.org/dist/index.json` | ✅ 200 | Node 各 major 的 latest + LTS 状态 |
| `registry.npmjs.org` | ✅ 200 | pnpm 版本 |
| `dl-cdn.alpinelinux.org/.../latest-releases.yaml` | ✅ 200 | Alpine 最新稳定分支（3.24 / 3.24.3） |

**明确标注「未能查询」的三项**：
1. 镜像的 **digest / 某个历史 patch tag 是否已被回收**（如 `golang:1.26.5-alpine`）——需 Docker Hub，被墙。
2. **EMQX 5→6 官方 breaking-changes 文档**（`docs.emqx.com/.../breaking-changes-v6.html` 返回 404），只能从 release notes 片段推断。
3. **Node 26 官方二进制 tarball 内是否还带 corepack**——无法下载 tarball 校验，用源码 `configure.py` 构建开关语义 + 官方文档页 404 作证据链（见 D.2，标注为「需 CI 实测确认」）。

---

## A. 固件（esp32-collector）

### A.1 依赖表

| 组件 | 当前 | 最新（实测） | 跨大版本? | 风险 | 建议 |
|---|---|---|---|---|---|
| **ESP-IDF** | `6.1.0`（`dependencies.lock` / GitHub tag `v6.1`，2026-08-27 发布） | **`v6.1` 即最新稳定版**；无 `v6.1.x` patch、无 `v6.2` tag、无 `v7.x`。`master` 的 `esp_idf_version.h` 已是 `6.2.0-dev`（未发布） | 否 | **低**（已在最新大版本；IDF 每版本支持 30 个月，6.1 正处"Service 期"） | **不动**。等 `v6.2` 正式 tag 再评估；不要跟 `master` |
| **espressif/mqtt** | `1.1.0`（lock 里 `component_hash fb18bc3b…3969c`，`commit_sha 1a1e5788a5cf57a0f44a3c6c061407f6c9be1026`） | **`1.1.0` 就是最新**（registry 全量仅 2 个版本：`1.0.0` 2025-09-25、`1.1.0` 2026-07-30；GitHub `espressif/esp-mqtt` tag 也只有 `v1.0.0/v1.1.0`） | 否 | **不是版本风险，是"这个依赖本身该删"**（见 A.2） | **删除依赖**，不是升级 |
| `main/idf_component.yml` 的 idf 约束 | `idf: '>=4.1.0'` | 应 ≥ `5.3`（`espressif/mqtt` 自身 manifest 要求 `idf >=5.3`） | — | **中（声明与实际矛盾）**：manifest 声称兼容 4.1，但 mqtt 组件要求 5.3，IDF 4.1/5.0 上根本解析不过；这类"假兼容"声明会误导后来人 | 若保留 mqtt 则改为 `>=5.3`；更推荐按 A.2 整段删除 |
| `main/idf_component.yml` 的 mqtt 版本 | `espressif/mqtt: '*'`（浮动） | — | — | **中**：`'*'` 让 lock 之外的任何重解析都可能换版本/换 hash，构建不可复现 | 删除该行（顺带消除问题） |
| `sdkconfig.defaults*` 中的 MQTT 配置 | **已全部删除**（2026-10-08 §194，注释保留） | — | — | 无 | 保持 |
| `sdkconfig.defaults*` 版本类配置 | 仅 `CONFIG_LOG_VERSION_2=y`；各 profile 只配 target/flash/分区 | — | — | 低（无版本锁死项） | 保持 |
| `dependencies.lock` 其他字段 | `version: 3.0.0`（lock 格式）、`target: esp32c6`、`manifest_hash d11c86bf…` | — | — | **低-中**：`target: esp32c6` 是上次解析时的目标，S3/S3P 三个 profile 共用同一份 lock，属"单一 target 残影" | 若保留 lock，按实际 profile 重新生成；若删 mqtt 则 lock 一并简化 |

### A.2 关键发现：**MQTT 已经是死代码，这一节的正解是"删"而不是"升"**

证据链（全部实测）：

1. `sdkconfig.defaults` 第 15 行起明确写着「⭐ 2026-10-08（§194）：**MQTT 配置段已整体删除** —— MQTT 已彻底移除（用户明令）」，并说明原 `CONFIG_MQTT_*` 段（2048 B 缓冲 + 8192 B 任务栈）已随组件删除，属内部 RAM 的实质收益。
2. `main/CMakeLists.txt` 第 24/33 行注释：`uplink_mqtt_transport.c **已删除**`、`ehome_mqtt 与 transport_sel 已删除（MQTT 彻底移除）`。
3. `components/msg_handler/CMakeLists.txt`、`components/sync_manager/CMakeLists.txt` 均已去掉 `ehome_mqtt` 的 REQUIRES。
4. **`components/ehome_mqtt/` 目录已不存在**（`ls` 报 No such file or directory，`git ls-files` 中匹配 mqtt 的组件数为 0）。
5. 全仓 `esp_mqtt_client*` 符号在 `esp32-collector`（排除 `managed_components`）中**0 处引用**；`components/log_stream/log_stream.c` 里剩余的都是历史注释。
6. 但依赖声明**还在**：`main/idf_component.yml:17` 仍写 `espressif/mqtt: '*'`，且 `dependencies.lock`（**已被 git 跟踪**）仍把它列为直接依赖、`managed_components/espressif__mqtt/` 仍被下载（该目录被 `.gitignore:26` 忽略，但本地存在）。

**结论**：只要 `main/idf_component.yml` 那一行还在，任何一次干净构建仍会下载并编译 `espressif__mqtt`（历史构建产物 `build/project_description.json` 里 161 个组件中实测确有 `espressif__mqtt`）。这既是无效 flash/RAM 占用，也是**供应链面**上的多余风险暴露。

**建议动作（本次不改，仅建议）**：删除 `main/idf_component.yml` 的 `espressif/mqtt` 行 → 重新 `idf.py reconfigure` 生成 `dependencies.lock` → 删 `managed_components/espressif__mqtt`，用四 profile 实编 + 实机启动验证。

### A.3 ESP-IDF 升级成本说明（回答"6.1.0 之后有无 6.2/7.x"）

- **没有 6.2 正式版**：GitHub `tags` 前 100 个里 v6 系列只有 `v6.0/v6.0.1/.2/.3/v6.1/v6.1-rc1/v6.1-beta1/v6.1-dev`；分支只有到 `release/v6.1`；`master` 的版本宏是 `6.2.0-dev`（未发布，不享受支持期）。
- **没有 7.x**：`tags` 与 `branches` 均无 `v7`。
- 因此 **IDF 侧升级成本 = 0**（已在最新稳定线）。近期唯一要做的是跟踪 `release/v6.1` 未来可能出现的 `v6.1.1` bugfix，属 patch 级、无需改代码。
- 反向成本提示：若将来升 `6.2`，`build_firmware.sh` 第 37-50 行已有"build 目录绑定 toolchain、IDF 换版必须 `rm -rf` build"的保护逻辑（比较到 major.minor），升级时不会静默踩坑。

---

## B. Docker / 部署

> tag 来源：`docker-library/official-images` 清单（Docker 官方镜像库权威清单）+ 上游 GitHub Releases；**Docker Hub API 不可达**，故无法给 digest。

### B.1 基础镜像（Dockerfile）与 compose 镜像

| 组件 | 当前 | 最新（实测） | 跨大版本? | 风险 | 建议 |
|---|---|---|---|---|---|
| **golang（build stage）** | `golang:1.26.5-alpine` | `1.27.3-alpine`；同线最新 patch **`1.26.10-alpine`** | 是（1.26→1.27，minor） | **高（两重）**：①1.26.5 落后 5 个 patch（1.26.6~1.26.10），含安全修复；②**官方镜像清单（official-images）里已无 `1.26.5` 这个 tag**（1.26 线当前仅列 `1.26.10-alpine/-alpine3.23/-alpine3.24`）；Docker 官方镜像库只保留每条 minor 线的最新 patch，旧 patch tag 会被下架 ⇒ 该 tag 现在是否还能拉到**无法验证**（Docker Hub 被墙），构建存在 `manifest unknown` 风险 | **优先 `1.26.10-alpine`**（零代码改动）；再择期 `1.27.3-alpine`。**不要**继续钉 1.26.5 |
| **node（build stage）** | `node:22-alpine` | `22.23.4-alpine`（LTS "Jod"，EOL 2027-04-30）；`24.22.1-alpine`（LTS "Krypton"，EOL 2028-04-30）；`26.12.1-alpine`（current，2026-10-28 转 LTS） | 否（同 major 浮动）/ 是（→24/26） | **中**：`node:22-alpine` 本身是浮动 tag，已自动跟到 22.23.4，不"旧"；但 22 线 2025-10-21 已进 maintenance，仅安全修复。另有 corepack 陷阱（见 D.2） | **`24-alpine`**：仍是 LTS + corepack 默认安装，且满足 vite8 / vitest4 引擎要求。**暂不要 26** |
| **alpine（runtime stage）** | `alpine:3.21` | `3.24.3`（latest-stable = 3.24）；同线最新 `3.21.9` | 是（3.21→3.24，3 个 minor） | **高（时间敏感）**：**3.21 的 EOL 是 2026-11-01**，距今约 9 天，之后不再有安全更新；本阶段只装 `ca-certificates tzdata`，升级面很小 | **`3.24`**（或先 `3.21.9` 收尾）。注意 3.22+ 的 musl 变更，务必跑一次容器启动 + TLS 出站验证 |
| **postgres** | `postgres:18-alpine` | **`18.6-alpine`**（18 线最新；`19rc1-alpine` 为预发布，不可用） | 否（同 major 浮动） | **低**：`18-alpine` 已自动跟 18.6；18 的 EOL 2030-11-14。挂载点 `/var/lib/postgresql` 已是 18+ 要求的正确写法（10 处 compose 全部一致，`deploy/ghcr/compose.ghcr.yml:37` 还留了注释说明） | **保持浮动 tag**，不改；建议改为 `18.6-alpine` 以获得可复现性 |
| **redis** | `redis:7-alpine` | `8.10.2-alpine`（8 线）；7 线末版 `7.4.11-alpine` | 是（7→8） | **高，且方向错了**：后端**已无 Redis 客户端**（`go.mod` 无 redis 依赖；`login_limiter.go:17` 写"Redis 退役（方案 v3.4 §4 任务B）"；`docker-compose.example.yml:68` 的 `REDIS_ADDR` 无人读取） | **不要升 8，直接删这个服务** + 删 `REDIS_ADDR`。若确有外部用途再评估 8.10.2 |
| **prom/prometheus** | `v2.53.0` | `v3.15.1`（2026-10-21） | **是（2→3，大版本）** | **高**：2.53 的 EOL 已于 **2025-07-31** 到期（`endoflife.date`），已收不到安全修复；v3 有官方 migration guide 列出的不兼容项 | **升 `v3.15.1`**，但走单独验证（见 B.2） |
| **prom/alertmanager** | `v0.27.0` | `v0.34.1`（2026-09-17） | 是（0.x，7 个 minor） | **中**：0.x 无 LTS 概念，v0.28 起有 `log/slog` 切换、模板错误行为变更等；我们的配置只用 `route/matchers/receivers/email_configs`，语法自 0.22 起稳定 | **升 `v0.34.1`**，与 Prometheus 同批（`--config.file` 参数不变） |
| **emqx/emqx** | `5.8`（浮动 minor，实际解析到 `e5.8.11`） | `6.3.2`（2026-10-16）；5.x 线最新 `e5.10.5`（2026-09-17） | 是（5→6，大版本） | **高**：①5.8 是 5.x 里较老的小线，5.8 线最新才 `e5.8.11`；②**官方 breaking-changes 文档 404**（未能查询），5→6 的配置/认证/仪表盘变更无法逐条核对；③当前部署用 `EMQX_LOADED_MODULES` 这类 4.x 时代环境变量，6.x 是否仍认需实测 | **分两步**：先 `e5.10.5`（同大版本、低风险）；`6.3.2` 单独立项验证 |
| **ghcr.io/lstions/ehome** | `latest` / `main` | 自建镜像（由本仓 CI 产出） | — | 低（自控） | 保持 |

### B.2 Prometheus 2→3 升级的具体验证点（针对本仓配置）

本仓 `deploy/monitoring/prometheus.yml` 只用了 `global.scrape_interval/evaluation_interval`、`rule_files`、`alerting.alertmanagers.static_configs`、`scrape_configs[].job_name/metrics_path/static_configs` —— 全部是 v3 兼容的基础项。官方 migration guide 中**会影响我们的是这几条**：

1. `no-default-scrape-port` 特性旗标被移除并成为默认：目标不再补 `:443/:80`。我们的 targets 是 `ehome-web:8080`（显式带端口），**不受影响**。
2. 一批 `--enable-feature` 旗标（`promql-at-modifier`、`new-service-discovery-manager`、`auto-gomemlimit` 等）被移除；我们 `command:` 只传 `--config.file` 和 `--storage.tsdb.retention.time=15d`，**不受影响**。
3. `scrape_classic_histograms` → `always_scrape_classic_histograms` 改名：仅在使用 native histograms 时才相关，我们未开启，**不受影响**。
4. `auto-gomemlimit` 在 v3 变为默认行为 —— 容器内存限制下 GOMEMLIMIT 会被自动设置，可能改变内存曲线，**需观察**。
5. 12 条告警规则（`deploy/monitoring/alert_rules.yml`）用的都是 `rate(...)`、`> 0`、`> 100` 这类基础 PromQL + `for:` —— v3 无语义变更，**不受影响**。

### B.3 发现的"配置孤儿"（非版本问题，但同属基础设施债）

| 位置 | 内容 | 问题 |
|---|---|---|
| `docker-compose.example.yml:68-70` | `REDIS_ADDR: redis:6379`、`MQTT_BROKER: tcp://emqx:1883`、`EHOME_MQTT_CLIENT_ID: ehome-server` | 后端 Redis 已退役、MQTT 已于 2026-10-08 移除（`backend/internal/config/config.go:34` 注释自证），这三个环境变量**无任何代码读取** |
| `docker-compose.example.yml:24-33` | `redis` 服务 | 同上，纯空转；且带 healthcheck，会让 `depends_on: service_healthy` 多等一轮 |
| `docker-compose.example.yml:36` / `deploy/**` 共 **7 处 `emqx/emqx:5.8`** | MQTT broker | 固件侧 MQTT 已移除、后端 MQTT 已移除 —— 需确认 broker 是否还有外部消费者；若没有，这 7 处都是可删对象（否则每次 EMQX 升级要改 7 个文件） |

---

## C. CI（GitHub Actions）

### C.1 Action 版本表

> 来源：`api.github.com/repos/<owner>/<repo>/releases/latest`（2026-10-23）。`docker/setup-buildx-action`、`docker/build-push-action` 的最新 tag 与 Lead 侧独立实测一致。

| 组件 | 当前 | 最新（实测） | 跨大版本? | 风险 | 建议 |
|---|---|---|---|---|---|
| `actions/checkout` | `v4`（7 处） | `v7.0.1` | **是（+3）** | 中：v7 起 `runs.using: node24`（v4 是 node20），要求 runner ≥ 2.327.1；v7.0.0 有 "block checking out fork PR for `pull_request_target`/`workflow_run`" 的安全收紧 | 升 `v7`；`ubuntu-latest` 托管 runner 无需担心 |
| `actions/setup-go` | `v5`（4 处） | `v7.0.0` | **是（+2）** | 中：node24 运行时；入参 `go-version`/`cache-dependency-path` 保持兼容 | 升 `v7` |
| `actions/upload-artifact` | `v4`（3 处） | `v7.0.2` | **是（+3）** | **中-高**：v6 起默认 node24（要求 runner ≥ 2.327.1）；v7 新增 `archive: false` 直传、`name` 在该模式下被忽略。我们只用 `name`/`path`/`if-no-files-found` → 语义不变 | 升 `v7`；本仓无 download-artifact，无配套约束 |
| `actions/setup-node` | `v4`（1 处） | `v7.1.0` | **是（+3）** | 中：node24 运行时；`node-version`/`cache: pnpm`/`cache-dependency-path` 兼容；v7 移除 dummy `NODE_AUTH_TOKEN` 导出（我们不发布 npm 包，无影响） | 升 `v7` |
| `pnpm/action-setup` | `v4`（1 处） | `v6.1.0` | **是（+2）** | **中（有硬耦合）**：v5 起 node24；**v6.1.0 才支持 pnpm v12**。也就是说"把 `PNPM_VERSION` 提到 12"必须同时把 action 提到 v6 | 升 `v6.1.0`；与 D.1 的 pnpm 版本决定**绑定执行** |
| `docker/login-action` | `v3` | `v4.6.0` | 是（+1） | 中：v4.0.0 起默认 node24 + ESM + `@actions/core` v3；入参 `registry/username/password` 不变 | 升 `v4` |
| `docker/metadata-action` | `v5` | `v6.2.0` | 是（+1） | **中（有语义变更）**：v6.0.0 起"列表类输入里 `#` 仍保留、行首 `#` 才是注释"——我们的 `tags:` 块里有 6 行 `type=...` 和 `#` 注释，注释均在块首 → 行为不变，但需复核 | 升 `v6` + 复核 `tags:` 块注释 |
| `docker/setup-buildx-action` | `v3` | `v4.4.1` | 是（+1） | 中：v4.0.0 **移除了 deprecated inputs/outputs** + node24 + ESM；我们无入参，风险低 | 升 `v4` |
| `docker/build-push-action` | `v6` | `v7.4.0` | 是（+1） | 中：v7.0.0 移除 `DOCKER_BUILD_NO_SUMMARY`/`DOCKER_BUILD_EXPORT_RETENTION_DAYS` 环境变量与 legacy export-build；我们只用 `context/push/tags/labels/build-args/cache-from/cache-to`，**全部保留** | 升 `v7`；**`build-args: GIT_COMMIT=<sha>`（来源可追溯）必须回归验证** |

**共性风险（一次性说清）**：上述 9 个 action 升到最新后**全部 `runs.using: node24`**，最低 runner 版本 **v2.327.1**（当前最新 runner 是 `v2.338.1`）。若未来引入 self-hosted runner 且版本 < 2.327.1，会直接报 "unsupported runtime"。

### C.2 CI `env` 与 Dockerfile / go.mod / 本机的一致性

| 变量 | CI（`test.yml:36-38`） | Dockerfile | `go.mod` / lockfile | 本机 | 一致? |
|---|---|---|---|---|---|
| Go | `GO_VERSION=1.26.5` | `golang:1.26.5-alpine` | `go 1.26.5` | `.tools/gotool` = **go1.27.1** | CI↔Docker↔go.mod 三处一致 ✅；本机偏新 ⚠️（见 D.1） |
| Node | `NODE_VERSION=22` | `node:22-alpine` | `frontend-shared` 无 `engines` 字段 | **v24.21.0** | CI↔Docker 一致 ✅；**本机跨 2 个 major** ⚠️ |
| pnpm | `PNPM_VERSION=10` | `corepack prepare pnpm@latest --activate`（**无版本**） | `pnpm-lock.yaml lockfileVersion: 9.0` | **12.14.0** | ❌ **三处互不一致**（10 / latest / 12.14.0）；lockfile 9.0 对 10·11·12 均可读（已实测 pnpm 12.14.0 重写 lockfile 仍是 9.0） |

> 附注：`test.yml:34-35` 的注释写着"与 backend/go.mod 及 Dockerfile (golang:1.26.5-alpine) 保持一致，避免 runner 上触发 Go 工具链自动下载"——这条纪律目前**被遵守**，D 节的问题不在 CI，而在**本机工具链与镜像 tag 生命周期**。

---

## D. 工具链一致性（本机 / CI / 镜像 / 声明）

### D.1 不一致清单

| 维度 | 声明处 A | 声明处 B | 声明处 C | 本机实测 | 判定 | 风险 | 建议 |
|---|---|---|---|---|---|---|---|
| **Go** | `backend/go.mod: go 1.26.5` | `Dockerfile: golang:1.26.5-alpine` | `CI GO_VERSION=1.26.5` | `.tools/gotool/bin/go` → **`go1.27.1 linux/amd64`**；go.dev 最新稳定 **`go1.27.3`**（1.26 线最新 patch **`go1.26.10`**） | **本机比声明新 1 个 minor（1.26.5 → 1.27.1）**，且 1.26.5 落后同线 5 个 patch | **中**：①本机 `go test` 与 CI `go test` 不是同一编译器，可能出现"本机绿/CI 红"或反之（尤其 `-race` 与新诊断）；②1.26.5 缺 1.26.6~1.26.10 的修复；③**`go.mod` 写 `go 1.26.5` 是 patch 级声明**，意味着构建环境若只有 1.26.4 会自动下载工具链（本机需 `GOTOOLCHAIN=local` 才不下载，本身就是征兆） | ①近期把三处统一到 **`1.26.10`**（零代码风险，补安全 patch）；②再统一切 **`1.27.3`**（`go.mod` + CI + Dockerfile + 本机 gotool 四件套同批改）；③`go.mod` 建议写 `go 1.26.0` 而非 `1.26.5`，把 patch 交给镜像/CI 决定 |
| **Node** | `Dockerfile: node:22-alpine` | `CI NODE_VERSION=22` | （无 `engines`/`.nvmrc`/`packageManager` 字段） | **`v24.21.0`** | **本机跨 2 个 major（22 → 24）**；仓库里没有任何文件把 Node 版本钉住，本机与 CI 的差异**完全不可自证** | **中-高**：`vite@8` 要求 `^20.19.0 \|\| >=22.12.0`、`vitest@4` 要求 `^20.0.0 \|\| ^22.0.0 \|\| >=24.0.0`，**22 与 24 都满足**；但本机跑出的 lint/test 结果与 CI 不可直接对比 | 在 `package.json` 加 `"engines": {"node": ">=24"}` 与 `"packageManager": "pnpm@12.14.0"`；把 CI 与 Dockerfile 一起提到 `24`（LTS Krypton，EOL 2028-04-30） |
| **pnpm** | `CI PNPM_VERSION=10` | `Dockerfile: corepack prepare pnpm@latest --activate` | （`package.json` 无 `packageManager` 字段） | **`12.14.0`** | **三处三个值：10 / latest（漂移）/ 12.14.0** | **高**：①Dockerfile 每次构建拉 `pnpm@latest`，**同一 commit 两次构建可能产出不同前端**（不可复现）；②CI 用 10、本机用 12，lockfile 虽同为 `9.0`，但 11/12 的 peer 与 `allowBuilds` 解析策略已变（`pnpm-workspace.yaml` 用了 `allowBuilds`，属 10+ 新字段）；③pnpm 12 与 Node 22 的兼容性未在 CI 验证 | **统一到 `pnpm@12.14.0` 并写进 `packageManager` 字段**；Dockerfile 改 `corepack prepare pnpm@12.14.0 --activate`（或 `npm i -g pnpm@12.14.0`）；CI `PNPM_VERSION: "12.14.0"` **且必须同批把 `pnpm/action-setup` 升 v6.1.0**（见 C.1） |
| **Alpine（musl 面）** | `alpine:3.21`（runtime） | `golang:1.26.5-alpine` → 内建 alpine 3.x（清单显示 1.26 线对应 `alpine3.23/3.24`） | `node:22-alpine` → 内建 `alpine3.23/3.24` | 与构建机无关 | **不一致但无害**：多阶段各自独立，最终 runtime 只用 `ca-certificates tzdata` | 低 | 只把 runtime 的 `3.21` 提到 `3.24`，其余不动 |
| **CMake（固件 host_tests）** | `CI: cmake --version`（runner 自带） | — | — | `.tools/cmake-3.30.5-linux-x86_64` | 本机 3.30.5，CI 为 ubuntu-latest 自带（约 3.31+） | 低（host_tests 用的特性很基础） | 无需处理 |

### D.2 ⚠️ 升级 Node 基础镜像时的 corepack 断崖（本次最重要"未来坑"）

若按"node 22 → 最新"直接跳到 `node:26-alpine`，Dockerfile 里的

    RUN corepack enable && corepack prepare pnpm@latest --activate

**很可能直接失败**。证据链：

1. Node v22.23.4 / v24.22.1 的 `configure.py`：`--without-corepack`（默认安装），`node_install_corepack = b(not options.without_corepack)` → **默认带 corepack**。
2. Node v25.9.0 / v26.12.1 的 `configure.py`：改成 `--with-corepack`（`default=None`），`node_install_corepack = b(options.with_corepack)` → **默认不装**，help 文本写着 "experimental, will be removed without notice"。
3. Node 仓库 changelog 有专门提交：**"doc: note corepack package removal in distribution doc (#61207)"**；另一条 **"doc: remove Corepack documentation page (#57663)"**（后者 PR 正文确认是"Following up on #57635"的移除动作）。
4. 实测文档页：`latest-v22.x/api/corepack.html` = 200、`latest-v24.x` = 200，而 `latest-v26.x` = **404**。
5. `nodejs/docker-node` 的 `22/alpine3.23`、`26/alpine3.23` Dockerfile 里都**没有** `corepack` 字样（用官方预编译 tarball，`./configure` 无参数）→ 能否用完全取决于上游 tarball 打没打 `--with-corepack`。

**结论**：**升级目标选 `node:24-alpine`（corepack 仍在）而不是 26**；若将来必须上 26，Dockerfile 要显式安装（`npm i -g corepack` 或直接 `npm i -g pnpm@12.14.0`），并在 CI 加 `corepack --version` 断言。
（本次无法 pull 镜像实机验证，故 D.1 未把该风险计为"已确认"，仅作**升级前必测项**。）

### D.3 顺带发现：`corepack prepare` 已是 deprecated 命令

`nodejs/corepack` 源码 `sources/main.ts:17` 是 `import {PrepareCommand} from './commands/deprecated/Prepare.ts'` —— 命令仍注册（能用），但目录分类已是 `deprecated`，README 里已不再收录 `prepare` 的说明。建议新写法用 `corepack install -g pnpm@12.14.0`（或干脆 `npm i -g pnpm@12.14.0`，切掉 corepack 这一层）。

---

## 升级分组建议

### 🟢 可安全批量（同一 PR，风险低，验证成本小）

| # | 项 | 从 → 到 | 判据 |
|---|---|---|---|
| 1 | `golang` 基础镜像同线 patch | `1.26.5-alpine` → `1.26.10-alpine` | 同 major.minor，只吃 patch；且消除"1.26.5 已不在 official-images 清单、能否拉取未知"的构建脆弱性 |
| 2 | `alpine` runtime | `3.21` → `3.24` | EOL 2026-11-01 迫在眉睫；该 stage 只装 2 个包 |
| 3 | `postgres` 钉死 patch | `18-alpine` → `18.6-alpine` | 同 major，仅为可复现性 |
| 4 | Actions 4 个（入参零变更） | `checkout v4→v7.0.1`、`setup-go v5→v7.0.0`、`setup-node v4→v7.1.0`、`login-action v3→v4.6.0` | node24 托管 runner 天然支持 |
| 5 | 删死依赖/死服务（**不是升级**） | 删 `idf_component.yml` 的 mqtt 行、删 example compose 的 `redis` 服务与 3 个孤儿环境变量 | 有代码级自证（A.2、B.3） |

### 🟡 需单独验证（一项一 PR，每项带专属回归）

| # | 项 | 从 → 到 | 必须验的事 |
|---|---|---|---|
| 6 | `pnpm` 三处统一 | `10 / latest / 12.14.0` → `12.14.0` | **必须同批**：`pnpm/action-setup v4→v6.1.0` + `packageManager` 字段 + Dockerfile 去掉 `@latest`；验 `pnpm install --frozen-lockfile` 与 `allowBuilds` 仍生效 |
| 7 | `node` 基础镜像 | `22-alpine` → `24-alpine` | **corepack 存在性断言**（D.2）；`pnpm build` 产物与旧镜像对比；`vite@8` 原生依赖在 musl 下能跑 |
| 8 | `upload-artifact` | `v4` → `v7.0.2` | artifact 名/路径不变；`if: always()` 三个 job 的产物都能下载 |
| 9 | `docker/metadata-action` | `v5` → `v6.2.0` | 复核 `tags:` 块内 `#` 注释不被当成值；`latest` 只在默认分支生成 |
| 10 | `setup-buildx` + `build-push` | `v3→v4.4.1`、`v6→v7.4.0` | **`build-args: GIT_COMMIT` 必须仍进镜像**（2026-10-23 刚修好的"来源可追溯"回归点，`/health` 要能回显 commit） |
| 11 | `Prometheus` | `v2.53.0` → `v3.15.1` | 12 条规则能加载（`promtool check rules`）；抓取目标 `ehome-web:8080` 仍通；`auto-gomemlimit` 变默认后的内存曲线；`--storage.tsdb.retention.time` 仍被接受 |
| 12 | `Alertmanager` | `v0.27.0` → `v0.34.1` | `amtool check-config`；`matchers: alertname =~ "Ehome.*"` 路由仍命中；email receiver 的 `SMTP_*` 环境变量插值仍生效 |
| 13 | `EMQX` 第一步 | `5.8` → `e5.10.5` | 同大版本；`EMQX_LOADED_MODULES` 是否仍被接受；外部 MQTT 客户端握手 |
| 14 | `Go` 1.27 | `1.26.x` → `1.27.3` | 四件套同批；`go vet -tags=simulation ./...` + `go test -race ./...` + 覆盖率 ≥35% 门禁 |

### 🔴 暂不建议（现在动，收益低于风险）

| # | 项 | 原因 |
|---|---|---|
| 15 | `node:26-alpine` | corepack 可能已不在发行版内（D.2），会让 Dockerfile 的 `corepack enable` 直接断；且 26 到 2026-10-28 才转 LTS |
| 16 | `golang:1.27.3` | 1.27 是刚出的新 minor；先把 1.26.10 吃稳，避免"补 patch"和"跨 minor"两个变量混在一个 PR |
| 17 | `emqx:6.3.2` | 官方 breaking-changes 文档 404（未能查询），5→6 变更无法核对；先走 5.10.5 |
| 18 | `redis:8` | 后端已无 Redis 客户端，升级等于给死服务续命；应先删（见 #5） |
| 19 | `ESP-IDF 6.2 / 7.x` | **根本不存在正式版**（6.2 只有 master 的 `-dev`，无 7.x）；无需行动 |
| 20 | `espressif/mqtt` 升级 | **已是最新 1.1.0**，且该组件是死依赖；不存在"升级"这个动作 |

---

## 建议升级顺序 + 每步验证方式

> 原则：**先"删/钉"再"升"**；一个 PR 只带一类变量；每步可独立回滚。

| 步 | 动作 | 验证方式（须真实执行） |
|---|---|---|
| **S0** | 基线固化：记录当前所有镜像 digest 与 `go version`/`node -v`/`pnpm -v` 输出 | `docker image inspect --format '{{index .RepoDigests 0}}' <img>`；`docker build --progress=plain` 保留完整日志 |
| **S1** | 删死依赖：`main/idf_component.yml` 去掉 mqtt；重生成 `dependencies.lock`；删 `managed_components/espressif__mqtt` | 四 profile 实编（S3/S3P/C6 × n8/n16）全绿；`build/project_description.json` 的 `build_components` 里**不再出现 `espressif__mqtt`**（当前实测 161 个组件里含它）；C6 实机启动 + 上报一轮 |
| **S2** | 删死服务：example compose 删 `redis` 服务 + 3 个孤儿环境变量 | `docker compose -f docker-compose.example.yml config` 无错；`up -d` 后 `ehome` 健康检查通过；`logs` 无 "redis/mqtt connect failed" |
| **S3** | 基础镜像 patch/安全线：`golang:1.26.10-alpine`、`alpine:3.24`、`postgres:18.6-alpine` | `docker build` 成功；镜像大小变化 < ±5MB；`/health` 200；TLS 出站正常；PG 数据卷可读写 |
| **S4** | Actions 纯版本批（#4） | CI 全 job 绿；`summary` job 的 6 个 needs 全 success；artifact 可下载 |
| **S5** | pnpm 统一（#6，含 action v6.1.0） | `pnpm install --frozen-lockfile` 成功；`pnpm-lock.yaml` **不被改写**（`git diff --exit-code`）；`pnpm test:coverage` ≥25%；`pnpm build` 产物与 S0 基线对比 |
| **S6** | Node 22→24（#7） | Dockerfile 加 `RUN corepack --version` 断言；前端构建产物与 S5 对比；`vite@8` 原生依赖无 musl 报错 |
| **S7** | 监控栈 2→3（#11+#12） | `promtool check rules deploy/monitoring/alert_rules.yml` 通过；`amtool check-config deploy/monitoring/alertmanager.yml` 通过；targets 页 `ehome-server` 为 UP；人为触发一条告警走到 email receiver |
| **S8** | Go 1.26.10 → 1.27.3（#14） | `go vet ./...`、`go vet -tags=simulation ./...`、`go test -race -count=1 ./...`、覆盖率 ≥35%、`-ldflags -X main.buildCommit` 仍生效（`/health` 回显 commit） |
| **S9** | EMQX 5.8→5.10.5（#13） | broker 起来 + 外部 MQTT 客户端能连；`emqx ping` 健康检查过；**7 个 compose 文件同步改**（否则下次改版本必漏） |

**回滚策略**：S1/S2 走 `git revert`；S3~S9 全是"改 tag/改版本号"，回滚 = 改回旧值 + 重建，无数据迁移（PG 仍在 18 线内；Redis 无状态；**Prometheus 数据卷 v2→v3 是单向的**，S7 回滚需重建 `ehome-prometheus-data` 卷 —— 这是 S7 必须挑低峰期做的原因）。

---

## 附录 A：可直接执行的命令清单

（见下方代码块；注意本机 hub.docker.com 与 registry-1.docker.io **均不可达**，凡涉及 Docker Hub 的命令都会超时，已给出替代方案。）

```bash
# ── 0) 环境（本机实测：系统无 go，必须用仓内 gotool + 可写 GOPATH + goproxy.cn）
cd /mnt/storage/WorkSpace/EHome
export GOPATH=$PWD/.tmp-deps/gopath
export GOMODCACHE=$GOPATH/pkg/mod
export GOCACHE=$PWD/.tmp-deps/gocache
export GOTOOLCHAIN=local
export GOSUMDB=off
export GOPROXY=https://goproxy.cn,direct

# ── A) 固件 ────────────────────────────────────────────────────────────
# A-1 当前 IDF 与组件版本（只读）
cat esp32-collector/dependencies.lock
sed -n '1,20p' esp32-collector/main/idf_component.yml
cat esp32-collector/managed_components/espressif__mqtt/idf_component.yml   # version + repository_info.commit_sha

# A-2 espressif/mqtt 组件全部版本（registry）
curl -s https://components.espressif.com/api/components/espressif/mqtt \
  | python3 -c 'import json,sys;d=json.load(sys.stdin);[print(v["version"],v["created_at"]) for v in d["versions"]]'

# A-3 ESP-IDF 版本线（有无 6.2 / 7.x）
curl -s 'https://api.github.com/repos/espressif/esp-idf/tags?per_page=100' \
  | grep -oE '"name": "v[67][^"]*"'
curl -s 'https://api.github.com/repos/espressif/esp-idf/releases?per_page=12' \
  | grep -E '"(tag_name|prerelease|published_at)"'
# master 的版本宏（判 6.2 是否已发布）
curl -s https://raw.githubusercontent.com/espressif/esp-idf/master/components/esp_common/include/esp_idf_version.h \
  | grep ESP_IDF_VERSION_

# A-4 证明 MQTT 已是死依赖
grep -rn 'espressif/mqtt' esp32-collector --include='*.yml' | grep -v managed_components
grep -rn 'esp_mqtt_client' esp32-collector --include='*.c' --include='*.h' | grep -v managed_components
python3 - <<'PYEOF'
import json,glob
for p in glob.glob('esp32-collector/build/project_description.json'):
    j=json.load(open(p))
    print(p, [c for c in j.get('build_components',[]) if 'mqtt' in c.lower()])
PYEOF

# ── B) Docker / 部署 ───────────────────────────────────────────────────
# Docker Hub 不可达 → 用 Docker 官方镜像库清单（权威 tag 源）
for img in postgres golang node alpine redis; do
  echo "=== $img ==="
  curl -s "https://raw.githubusercontent.com/docker-library/official-images/master/library/$img" \
    | grep -E '^Tags:' | head -20
done

# 上游项目版本（Prometheus / Alertmanager / EMQX）
curl -s https://api.github.com/repos/prometheus/prometheus/releases/latest  | grep -E '"(tag_name|published_at)"'
curl -s https://api.github.com/repos/prometheus/alertmanager/releases/latest | grep -E '"(tag_name|published_at)"'
curl -s 'https://api.github.com/repos/emqx/emqx/releases?per_page=100' \
  | grep -oE '"tag_name": "(e?[56]\.[0-9.]+|v5\.[0-9.]+)"' | head -30

# EOL 交叉校验
for p in postgresql redis prometheus nodejs alpine-linux; do
  echo "=== $p ==="; curl -s "https://endoflife.date/api/$p.json" | head -c 400; echo
done

# 升级前实际校验镜像是否存在（在有网 / Docker Hub 恢复的机器上执行）
docker manifest inspect golang:1.26.10-alpine   >/dev/null && echo OK || echo MISSING
docker manifest inspect golang:1.26.5-alpine    >/dev/null && echo OK || echo "MISSING(风险点)"
docker manifest inspect alpine:3.24             >/dev/null && echo OK
docker manifest inspect postgres:18.6-alpine    >/dev/null && echo OK

# ── C) CI ──────────────────────────────────────────────────────────────
grep -rn 'uses:' .github/workflows/          # C-1 当前 action 版本
for r in actions/checkout actions/setup-go actions/upload-artifact actions/setup-node \
         pnpm/action-setup docker/login-action docker/metadata-action \
         docker/setup-buildx-action docker/build-push-action; do   # C-2 各自最新
  printf '%-32s ' "$r"
  curl -s "https://api.github.com/repos/$r/releases/latest" | grep -oE '"tag_name": "[^"]*"'
done
for t in actions/checkout/v7.0.1 actions/setup-go/v7.0.0 actions/upload-artifact/v7.0.2 \
         actions/setup-node/v7.1.0 pnpm/action-setup/v6.1.0 docker/build-push-action/v7.4.0; do
  printf '%-40s ' "$t"                                   # C-3 运行时(node20/node24)
  curl -s "https://raw.githubusercontent.com/$t/action.yml" | grep -E 'using:'
done

# ── D) 工具链一致性 ────────────────────────────────────────────────────
grep -nE '^(go|toolchain) ' backend/go.mod           # D-1 四处 Go 版本对照
grep -n 'FROM golang' Dockerfile
grep -n 'GO_VERSION' .github/workflows/test.yml
.tools/gotool/bin/go version
curl -s 'https://go.dev/dl/?mode=json&include=all' | grep -oE '"version": "go1\.2[67]\.[0-9]+"' | sort -uV

node -v; pnpm -v                                     # D-2 三处 Node/pnpm 对照
grep -n 'FROM node' Dockerfile
grep -n 'NODE_VERSION\|PNPM_VERSION' .github/workflows/test.yml
grep -n 'corepack' Dockerfile
grep -m1 'lockfileVersion' frontend-shared/pnpm-lock.yaml
grep -n 'packageManager\|engines' frontend-shared/package.json || echo '未钉 Node/pnpm 版本'
curl -s https://registry.npmjs.org/pnpm/latest | python3 -c 'import json,sys;print(json.load(sys.stdin)["version"])'

for v in v22.23.4 v24.22.1 v25.9.0 v26.12.1; do         # D-3 corepack 断崖证据
  printf '%-10s ' "$v"
  curl -s "https://raw.githubusercontent.com/nodejs/node/$v/configure.py" \
    | grep -E "with-corepack|without-corepack|node_install_corepack" | tr '\n' ' '; echo
done
for v in 22 24 26; do
  printf 'docs v%s: ' "$v"
  curl -s -o /dev/null -w '%{http_code}\n' "https://nodejs.org/docs/latest-v$v.x/api/corepack.html"
done

cd frontend-shared && pnpm outdated --format json | head -60   # D-4 只读，不改 lockfile
```

---

## 附录 B：本次盘点的"发现密度"统计

| 分类 | 数量 | 明细 |
|---|---|---|
| 可安全批量升级/收尾 | **5 组**（含 4 个 action） | golang patch、alpine runtime、postgres patch、4×action、删死依赖 |
| 需单独验证 | **9 项** | pnpm 统一、Node 24、upload-artifact v7、metadata v6、buildx+build-push、Prometheus v3、Alertmanager、EMQX 5.10.5、Go 1.27.3 |
| 暂不建议 | **6 项** | node26、golang1.27.3、EMQX6、redis8、IDF 6.2/7、mqtt"升级" |
| **死依赖/死服务**（应删不应升） | **2 处大类** | 固件 `espressif/mqtt`；compose `redis` + 3 个孤儿环境变量（另 7 处 `emqx:5.8` 待确认） |
| **工具链不一致** | **3 项** | Go（1.26.5 vs 本机 1.27.1 vs 最新 1.27.3）、Node（22 vs 本机 24）、pnpm（10 vs latest vs 12.14.0） |
| **高优先级时间炸弹** | **2 个** | `alpine:3.21` EOL **2026-11-01**（9 天）、`golang:1.26.5` 已不在 official-images 清单（旧 patch tag 会被下架，构建可能 `manifest unknown`；需在有网机器 `docker manifest inspect` 确认） |
