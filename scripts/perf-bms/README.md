# BMS 页慢修复 — 本机性能基线（ehome_bench）

复现生产 BMS 详情页（`edge_devices.id=14`，jiabaida_bms，16 串，1 Hz，29 测点）
的历史查询成本，供 before（main 旧代码）/ after（worktree 修复代码）对比。
**本目录脚本只操作本机 docker 的 `ehome-postgres`，不接触生产。**

## 前置

- docker 可用；容器 `ehome-postgres`（127.0.0.1:5432，用户 `ehome/ehome123`）在跑。
- 后端二进制已构建到 `.bench/bin/ehome-server-main`（main 工作树改动前代码）：

```bash
# 在 worktree 根目录执行；源码只读挂载主工作树 backend/（不改主工作树）
docker run --rm --network host \
  -v /mnt/storage/WorkSpace/EHome/backend:/w:ro -w /w \
  -v /home/sun/go/pkg/mod:/go/pkg/mod:ro \
  -v "$PWD/.bench/gocache:/gocache" -v "$PWD/.bench/bin:/out" \
  -e GOCACHE=/gocache -e GOMODCACHE=/go/pkg/mod -e GOFLAGS=-mod=mod \
  golang:1.26.5 go build -trimpath -o /out/ehome-server-main ./cmd/server
```

## 一键流程

```bash
cd <worktree>
scripts/perf-bms/seed.sh                      # 1) 建 ehome_bench + 迁移 schema + 造数（约 3-5 分钟）
# 2) 起后端（前台阻塞，建议放后台任务）
scripts/perf-bms/start_backend.sh &
scripts/perf-bms/explain.sh                   # 3) 旧 SQL 的 EXPLAIN (ANALYZE, BUFFERS)
scripts/perf-bms/measure.sh main              # 4) 5 请求 × (预热 1 轮 + 3 轮)，中位数
scripts/perf-bms/index_bench.sh before        # 5) 两个新索引的建索引耗时/体积（测完自动 DROP）
pkill -f ehome-server-main                    # 6) 收尾杀进程
```

## 固定窗口（保证可重复）

`BENCH_END_TS=2026-10-20 14:00:00+00`（UTC，对应取证当天）：

- 24h 窗口 = `2026-10-19T14:00:00Z .. 2026-10-20T14:00:00Z`
- 30d 窗口 = `2026-09-20T14:00:00Z .. 2026-10-20T14:00:00Z`

## 数据规模

| 表 | 行数 | 说明 |
|---|---|---|
| unified_data 最近 24h | 2,501,279 | 29 category × 86,251（1 Hz，与生产同）；按时间升序物理写入 |
| unified_data 历史 8 天 | 19,964,868 | 28 类 × 691,200 + protection 611,268；按时间升序物理写入 |
| unified_data 合计 | 22,466,147 | = 24h + 历史（不重复计）；全部 logical_device_id=14，当前月分区 ≈ 4.0 GB（生产 4.42 GB） |
| 其中 protection_status 30d | 697,519 | 24h 内 86,251 + 历史 611,268；0/1 变更序列 |
| device_data | 2,000,000 | created_at 无索引，复现 latest-data top-N 排序 |

## 产物（.bench/）

- `baseline-<label>-<run>.json/.md/.tsv`：5 请求计时原始记录与中位数
- `baseline-env-<label>-<run>.txt`：同批的 pg_settings / 行数 / 表体积
- `explain-before.txt` / `explain-after.txt`：旧/新 SQL 的 EXPLAIN (ANALYZE, BUFFERS)
- `index-create-before.txt`：新索引 CREATE INDEX 耗时（psql `\timing on`）
- `seed-migrate.log` / 后端日志

## 口径与注意

- **鉴权**：/api/v1 每请求校验 auth_states.initialized + users.system_admin + session_version。
  `seed_meta.sql` 在 bench 库写入 `users.id=1, session_version=2`，JWT 用 `sv=2` 铸造
  （算法同 `.tmp-verify/prod_api.sh`，secret 用本地 `BENCH_JWT_SECRET`）。
- **冷/热缓存**：先跑 1 轮预热，再报 3 轮中位数（本机 PG `shared_buffers=128MB`，
  小于表体积，故热缓存只覆盖部分页；每轮明细都记录在 .tsv/.md 中）。
- **建索引外推**：bench 当前月分区 3.11M 行；生产 `unified_data_202610` 约 24M 行 / 4.4GB。
  按行数线性外推（×约 7.7）仅供维护窗口估算，需标注为估算。
- 生产统一升级注意：分区母表上不能 `CREATE INDEX CONCURRENTLY`，首次启动建索引会短暂阻塞写入，
  应低峰执行（这是修复代码的既定行为，见契约 §1.4）。
- **物理写入顺序**：所有行必须按 timestamp 升序写入（`FROM generate_series(...) t CROSS JOIN (VALUES ...) c`），
  使 `pg_stats.correlation=1`（与生产采集顺序一致）。若按 category 分组写入（correlation≈0.37），planner 会选
  并行 BitmapAnd，16 路并发 batch 会耗尽容器 `/dev/shm=64MB` 且旧代码吞错返回 `data:null`。
- 本轮 before 基线完整摘要见 `.bench/baseline-summary-main.md`。
