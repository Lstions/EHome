# BMS 页慢修复 — 上线 Runbook（2026-10-20）

> 配套：根因报告 `docs/分析/BMS页外部访问慢-根因分析-2026-10-20.md`、实施契约 `docs/分析/BMS页慢修复-实施契约-2026-10-20.md`、闭环验证 `docs/验证/BMS页慢修复-闭环验证-2026-10-20.md`。
> 生产实际拓扑：`/srv/ehomesystem`（docker compose，容器 `ehome-prod-web` + `ehome-prod-postgres` + `ehome-prod-redis`），Web 端口 18090，设备 mTLS 8443。当前运行镜像 `ehome-new:ui-fix3`（**本地镜像，非 GHCR**）。

## 0. 本次上线内容

| 层 | 改动 | 作用 |
|---|---|---|
| 后端 | `historical` / `historical-batch` 支持 `agg=auto/avg/change/raw`：PG 下 SQL 分桶聚合（avg）或变更点序列（protection_status/fet_status），只返回 `{timestamp,value}` | 单请求扫描行数 2.5M → 86k；响应 1.7MB → ~0.5MB |
| 后端 | scope 条件改 `UNION ALL` 分支（logical 分支 + NULL-logical 的 device 分支） | 让复合索引真正生效 |
| 后端 | 新增 covering 索引 `idx_unified_logical_sensor_ts (logical_device_id, sensor_name, timestamp) INCLUDE (value)`（仅 PG；SQLite 用普通复合索引）、`idx_devdata_logical_created (logical_device_id, created_at DESC)`、以及 legacy 部分索引 `idx_unified_legacy_null_ts (device_id, sensor_name, timestamp) INCLUDE (value) WHERE logical_device_id IS NULL` | 24h 单测点 Index Only Scan 扫 86k 行、零 heap fetch；latest-data 2M 行 seq scan → Index Scan |
| 后端 | `ehomectl datalifecycle ensure-indexes` 子命令；Dockerfile 增加 `ehomectl` 二进制 | 维护窗口可显式建索引 |
| 前端 | 首屏请求编排：历史请求页面级并发 ≤3、batch 失败先重试 1 次（≤4 类才逐类降级）、折叠卡 lazy 挂载、commands 只取 1 次、history 超时 30s、失败显示「加载失败+重试」 | 消除 22 路降级风暴与「加载 20s 变暂无数据」 |

## 1. 前置检查（上线前 10 分钟）

```bash
# 1.1 记录回滚点
ssh root@192.168.20.6 'docker inspect ehome-prod-web --format "{{.Config.Image}}"'   # 预期 ehome-new:ui-fix3
ssh root@192.168.20.6 'cd /srv/ehomesystem && cp .env .env.bak-$(date +%Y%m%d-%H%M%S) && grep -E "^EHOME_IMAGE=" .env'  # 备份 .env 并显示当前镜像
# 1.2 磁盘/内存余量（新索引约 +1.1 GB；镜像 +~100 MB）
ssh root@192.168.20.6 'df -h / && free -m | head -2'
# 1.3 当前索引清单（回滚对照）
ssh root@192.168.20.6 'docker exec ehome-prod-postgres psql -U ehome -d ehome -c "select indexname from pg_indexes where tablename like 'unified_data_%' or tablename like 'device_data_%' order by 1;"' > /tmp/index-before.txt
```

## 2. 构建产物（本机）

```bash
cd /mnt/storage/WorkSpace/EHome/.agents/worktrees/bms-perf-fix
# 2.1 后端静态二进制（与 Dockerfile 同环境：CGO_ENABLED=0 / linux）
docker run --rm -v "$PWD/backend:/w" -w /w \
  -v /home/sun/go/pkg/mod:/go/pkg/mod:ro -v "$PWD/.gocache:/gocache" \
  -e GOCACHE=/gocache -e GOMODCACHE=/go/pkg/mod -e CGO_ENABLED=0 -e GOOS=linux \
  golang:1.26.5 sh -c 'go build -trimpath -o /w/.bench/bin/ehome-server ./cmd/server/ && go build -trimpath -o /w/.bench/bin/ehomectl ./cmd/ehomectl/'
# 2.2 前端 dist（含 vue-tsc 类型检查）
cd frontend-shared && pnpm build   # 产物 frontend-shared/dist
```

## 3. 构建派生镜像（在生产服务器上，基于当前运行镜像，快且不依赖外网）

```bash
# 3.1 准备派生镜像 Dockerfile（本地文件，避免 ssh 内多层引号）
cd /mnt/storage/WorkSpace/EHome/.agents/worktrees/bms-perf-fix
mkdir -p .bench/deploy/dist
cat > .bench/deploy/Dockerfile <<'DOCKERFILE'
FROM ehome-new:ui-fix3
COPY ehome-server /app/ehome-server
COPY ehomectl /app/ehomectl
RUN chmod +x /app/ehome-server /app/ehomectl && rm -rf /app/static/dist
COPY dist/ /app/static/dist/
DOCKERFILE

# 3.2 传产物（约 60 MB）
tar czf .bench/deploy/dist.tar.gz -C frontend-shared/dist .
ssh root@192.168.20.6 'rm -rf /tmp/bmsfix && mkdir -p /tmp/bmsfix/dist'
scp .bench/deploy/Dockerfile .bench/deploy/dist.tar.gz .bench/bin/ehome-server .bench/bin/ehomectl \
  root@192.168.20.6:/tmp/bmsfix/
ssh root@192.168.20.6 'tar xzf /tmp/bmsfix/dist.tar.gz -C /tmp/bmsfix/dist'

# 3.3 服务器侧构建派生镜像（基于本地已有 ui-fix3，不需要外网）
ssh root@192.168.20.6 'cd /tmp/bmsfix && docker build -t ehome-new:perf-fix1 .'
```

## 4. 维护窗口切换（预计停写 1–3 分钟；实测 bench 22.4M 行建索引 18 s @12vCPU，生产 3 vCPU 估算 60–120 s）

```bash
ssh root@192.168.20.6 'cd /srv/ehomesystem \
  && sed -i "s/^EHOME_IMAGE=.*/EHOME_IMAGE=ehome-new:perf-fix1/" .env \
  && docker compose stop ehome \
  && /usr/bin/time -f "INDEX_BUILD wall=%es" docker compose run --rm --no-deps ehome /app/ehomectl datalifecycle ensure-indexes \
  && docker compose up -d ehome'
```

## 4.1 本机 bench 基线（v4 修复后，2026-10-21 00:02，ehome_bench 22.4M 行/固定窗口）

| 请求 | before（旧代码） | after（v4） | 倍数 |
|---|---:|---:|---:|
| cells 16 类 × 24h | 1.917 s | **0.19 s** | 10× |
| metrics 9 类 × 24h | 1.050 s | **0.11 s** | 9.5× |
| protection 1 类 × 30d | 2.610 s | **0.26 s** | 10× |
| temperature 3 类 × 24h | 0.581 s | **0.067 s** | 8.7× |
| rsoc+current 2 类 × 24h | 0.483 s | **0.067 s** | 7.2× |
| latest-data | 0.198 s | **0.008 s** | 25× |
| **5 历史请求合计** | **6.64 s** | **0.72 s** | **9.2×** |

- EXPLAIN：avg 单测点 = `Parallel Index Only Scan`（Heap Fetches 0，扫描 86,400 行，32ms）；change 30d = `Merge Append + Index Only Scan`（无 external sort，约 350ms）；latest-data = `Index Scan`（0.12ms）。
- 响应体：cells 1.70 MB → 548 KB（-68%）。
- 注意：本机 12 vCPU/30GB 比生产（3 vCPU/4GB、冷缓存）快，生产预期 1.5–3× 本机数值，最终以 §5 生产实测为准。

---

## 5. 上线验收（必须全过）

```bash
ssh root@192.168.20.6 'curl -s http://127.0.0.1:18090/health'   # {"status":"ok"}
# 5.1 索引 valid + 实际被使用
ssh root@192.168.20.6 'docker exec ehome-prod-postgres psql -U ehome -d ehome -c \
  "select c.relname, i.indisvalid from pg_class c join pg_index i on i.indexrelid=c.oid where c.relname in ('idx_unified_logical_sensor_ts','idx_devdata_logical_created','idx_unified_logical_ts','idx_devdata_logical_ts');"'
# 5.2 单测点 24h EXPLAIN 应出现 idx_unified_logical_sensor_ts、扫描行数 ≈86k
ssh root@192.168.20.6 'docker exec ehome-prod-postgres psql -U ehome -d ehome -c \
  "EXPLAIN (ANALYZE, BUFFERS, COSTS OFF) SELECT ... ;"'
# 5.3 API 实测（JWT 见 .secrets/prod.jwt 的铸造方式；本仓库 .tmp-verify/prod_api.sh）
./.tmp-verify/prod_api.sh "/api/v1/unified-data/historical-batch?device_pk=14&categories=cell_voltage_1,...,cell_voltage_16&start_time=<24h-ago>&end_time=<now>&max_points=500" \
  -o /dev/null -w 'cells: code=%{http_code} ttfb=%{time_starttransfer}s total=%{time_total}s dl=%{size_download}B\n'
# 目标：cells < 2 s（生产 LAN；本机 bench 0.19 s），响应 ≈0.5 MB；原来的 5.6 s / 1.57 MB
#       5 个历史请求总耗时 < 3 s（原 20.6–21.1 s）
# 5.4 前端页面（CDP 或人工）
#    - 窄屏 393px：首屏不应自动发 historical-batch（折叠卡 lazy）；展开「历史趋势」后 loading < 3 s；
#    - 桌面 1440px：5 个图表正常、无 10s 超时、无 per-category 降级；
#    - 断开后端网络模拟失败：卡片显示「历史数据加载失败 + 重试」而不是「暂无数据」。
```

## 5.1 索引语义注意

- `idx_unified_logical_sensor_ts` 是 **covering 索引（INCLUDE (value)）**：只有索引仅扫（Index Only Scan）才会被 PG 代价模型选中；生产当前**没有**部署过该索引名，Ensure 会直接建成新定义。若曾手工建过同名普通索引，`IF NOT EXISTS` 不会替换定义，必须先 `DROP INDEX idx_unified_logical_sensor_ts`（会级联子分区索引）再 ensure。
- 新建分区由 PG 父索引自动继承同一 INCLUDE 定义，无需重复操作。

## 6. 回滚（任一验收失败）

```bash
ssh root@192.168.20.6 'cd /srv/ehomesystem && sed -i "s/^EHOME_IMAGE=.*/EHOME_IMAGE=ehome-new:ui-fix3/" .env && docker compose up -d ehome'
# 新索引可保留（只占空间、加速查询、无写入副作用）；如确需删除：
#   DROP INDEX CONCURRENTLY idx_unified_logical_sensor_ts;  -- 分区母表不能 CONCURRENTLY，需维护窗口
```

## 7. 风险与回滚矩阵

| 风险 | 概率 | 影响 | 处置 |
|---|---|---|---|
| 建索引超时/失败 | 低 | 维护窗口延长 | 索引幂等；失败后重跑 ensure-indexes；容器未启动前可先回滚 .env 起旧镜像 |
| 新 SQL 语义回归（change 模式） | 低 | 保护触发统计偏差 | 有 PG integration 测试与前端 bitTransitions 契约；回滚镜像即恢复 |
| 前端 lazy 导致折叠摘要为空 | 中 | 体验小退化 | 摘要加载后由卡片 emit；接受，后续可在展开时补摘要 |
| 设备断连期间数据丢失 | 中 | 1–3 分钟数据缺口 | 固件会重连；如需零丢失可改为在线并发建索引方案（后续优化） |

## 8. 上线后观察（24h）

- 后端日志：无 `index ensured` 重扫（应为 O(1) 幂等）；无聚合 SQL 错误。
- p95 端点耗时（curl 脚本或前端 RUM）应 < 2 s；无 10 s 超时。
- 前端控制台/网络：无 per-category 降级风暴、无 `ERR_CANCELED` 泄漏；折叠卡展开后加载 < 3 s。