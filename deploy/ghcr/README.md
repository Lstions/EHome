# GHCR 已发布镜像验证（`deploy/ghcr`）

> 目的：把「**CI 绿**」这个弱信号，升级为「**发布的镜像真能跑且功能正常**」这个强信号。
> CI 成功只说明构建+推送没报错，**不等于**镜像可运行、不含 bug、是你这次提交的产物。

## 1. 推送后会发生什么

`.github/workflows/docker-publish.yml` 在 push 到 `main` 时触发（paths 过滤：`backend/**`、
`frontend-shared/**`、`Dockerfile`、`.github/workflows/**`），构建并推送：

| tag | 来源 |
|---|---|
| `:main` | 分支名（默认分支） |
| `:latest` | `is_default_branch` |
| `:sha-<短sha>` | 每次提交 |
| `:1.2.3` / `:1.2` / `:1` | 打 `v*` tag 时 |

镜像地址：`ghcr.io/lstions/ehome`

## 2. 验证（一条命令）

```bash
# 一键跑法（仓库根目录；等价于下面第一条命令）
make verify-image

# 验证 main 上最新镜像
./deploy/ghcr/verify-published-image.sh

# 验证「就是我这次推的 commit」（推荐：这样才是「最新」的严格判据）
EXPECT_COMMIT=$(git rev-parse github/main) ./deploy/ghcr/verify-published-image.sh

# 验证指定 tag
GHCR_TAG=sha-abe3a7b0 ./deploy/ghcr/verify-published-image.sh
```

## 3. 它断言什么（10 条）

| # | 断言 | 为什么重要 |
|---|---|---|
| 1 | 镜像可拉取 | 存在性 |
| 2 | **`org.opencontainers.image.revision` == 期望 commit** | **这是「是否为最新」的判据**；只看 tag 名无法区分新旧镜像 |
| 3 | compose up 成功 | 镜像可运行 |
| 4 | `/health` 就绪 | 服务真的起来了 |
| 5 | `/health` 返回 **JSON** | 区分真实端点与 SPA catch-all（见下） |
| 6 | SPA 可访问（`text/html`） | 前端资源已打进镜像 |
| 7 | 启动日志含 `Latest value cache warmed up` | 镜像含启动回填接线 |
| 8 | 启动日志无 panic/FATAL | 启动序列健康 |
| 9 | 一次性凭据出现（**值不落证据文件**） | 首次部署可用 |
| 10 | `initialize` 201 → `login` 200 → **凭据重放 409** | 首次部署全流程 + 一次性语义 |

## 4. 两个必须知道的坑（都是实测踩出来的）

### 4.1 `/api/v1/health` **不是**健康端点
SPA catch-all 对**任意未知路径**返回 `200 + index.html`。实测：
```bash
curl -s -o /dev/null -w '%{http_code} %{content_type}' http://127.0.0.1:18090/api/v1/health
#  200 text/html          ← 和 /totally/bogus/path 完全相同
curl -s http://127.0.0.1:18090/health
#  {"status":"ok"}        ← 这才是真实端点
```
⇒ **「status==200 就算健康」在本服务上是天生的假绿发生器**。故断言 5 要求内容为 JSON。

### 4.2 postgres 18+ 的挂载点变了
`postgres:18-alpine` 要求挂载 `/var/lib/postgresql`（**不是**旧的 `/var/lib/postgresql/data`）。
用旧路径 ⇒ 容器 `Restarting`，日志提示 "upgrading the Docker image without upgrading the
underlying database"。本 compose 已按新路径。

## 5. 隔离与清理

- 独立 project `ehome-ghcr`、独立端口 `18090`、独立卷 `ghcr_pgdata`
- **自带 PG + EMQX** ⇒ 自包含，不依赖宿主机已有容器
- `trap` 保证**异常也清理**（`down -v`）；脚本结束打印残留容器数

## 6. 本目录文件

| 文件 | 用途 |
|---|---|
| `compose.ghcr.yml` | 用 GHCR 镜像运行（**无 `build:` 段** ⇒ 强制远端产物，杜绝本地构建掩盖问题） |
| `verify-published-image.sh` | 一键验证（10 断言 + 自动清理） |
| `evidence/` | 每次验证的证据 |

## 7. 未覆盖（诚实声明）

- 不验证镜像的 CVE/漏洞（需额外扫描器）
- 不验证多架构（CI 仅 linux/amd64）
- 不覆盖 nginx 反代路径（本栈直连 `:18090`）
- 不验证真实 ESP32 接入
- **不校验镜像 digest 是否为「逐位可复现」**（见 `docs/分析/后续工作计划与方案-2026-09-15.md`
  §1.7 (21)：同 commit 的镜像 id/Size 会因层 tar 的 mtime 而变，需 `SOURCE_DATE_EPOCH`；
  但**镜像内文件内容**已实测逐文件 sha256 一致）