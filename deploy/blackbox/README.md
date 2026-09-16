# 部署黑盒验证 —— 运行手册（`deploy/blackbox`）

> 方案：[`docs/分析/部署黑盒验证方案-2026-09-16.md`](../../docs/分析/部署黑盒验证方案-2026-09-16.md)
> 这一层补的是 **L5「真容器部署」**：用独立 compose project 起真实容器，只经**公开接口**
> （HTTP / MQTT / `docker inspect` / 容器日志）做黑盒断言，不读容器内私有文件、不直连其 DB 改数据。

---

## 0. 前置条件

```bash
export PATH=/snap/bin:/home/sun/.local/share/pnpm/bin:$PATH
export HOME=/home/sun
```

- Docker 29.8.0 + Compose v5.5.1（已验证）
- 共享容器必须已在运行：`ehome-postgres`、`ehome-emqx`、`nginx`、`ddns-go`
  （黑盒栈**复用**它们，不新起；也不占用它们的端口）
- **不要**把仓库根 `.env` 里的变量 export 进当前 shell（`POSTGRES_PASSWORD`、`EHOME_EXTERNAL_HOST` 等）。
  探针的「必填 env 缺失应报错」变异依赖这些变量**不存在**；若已被 export，Q2 可能假绿。
  `run.sh` 内部对 compose 调用使用独立环境，不受仓库根 `.env` 影响，但探针自身仍应遵守此约束。

---

## 1. 怎么跑

```bash
cd /home/sun/workspace/EHomeSystem

./deploy/blackbox/run.sh                          # 全量（构建 → compose 契约 → 冷启动 → 连通 → 健康 → 首次部署）
./deploy/blackbox/run.sh --skip-build             # 复用已构建镜像（置 BB_SKIP_BUILD=1 传给探针）
./deploy/blackbox/run.sh --only 04                # 只跑指定探针（逗号分隔，支持 id / name / id-name）
./deploy/blackbox/run.sh --only 04,05 --skip-build
./deploy/blackbox/run.sh --list                   # 打印依赖顺序 + 探针是否齐备
./deploy/blackbox/run.sh --probes-dir /tmp/empty  # 覆盖探针目录（自测编排用）
```

**执行顺序（依赖顺序，非 ID 顺序）**：

| 顺序 | 探针 | 命题 | 需要黑盒栈 | 说明 |
|---|---|---|---|---|
| 1 | `01-image-contract.sh` | Q1 | 否 | 镜像产物完整性（构建 + 静态资源 + 无密钥） |
| 2 | `07-reproducible.sh` | Q7 | 否 | 同 commit 两次构建关键产物一致 |
| 3 | `02-env-contract.sh` | Q2 | 否 | compose 必填 env 缺失时明确报错 |
| 4 | `03-coldstart.sh` | Q3 | 是 | 冷启动全序列（AutoMigrate / 退役 DDL / 初始化凭据） |
| 5 | `06-wiring.sh` | Q6 | 是 | 跨容器连通（服务名 `postgres` / `emqx`） |
| 6 | `04-health.sh` | Q4 | 是 | `/health` `/metrics` 未鉴权可达 |
| 7 | `05-first-boot.sh` | Q5 | 是 | 一次性凭据全流程（未初始化 → 设置 → 登录） |
| — | `S1 cache-warmup` | SUP | 是 | **附加检查**（主控 2026-09-16 指派，非 Q1–Q7）：冷启动日志须出现 `Latest value cache warmed up: N rows`；如实记录 N |

`03` 是第一个需要栈的探针，`run.sh` 会为它做**冷启动**（先 `down -v` 再 `up --wait`）；
`04/05/06` 复用同一个运行中的栈（warm），生命周期由 `run.sh` 独占。

---

## 2. 怎么读结果

每次运行产生一个证据目录 `deploy/blackbox/evidence/D-<时间戳>/`：

```
D-<时间戳>/
├── SUMMARY.md                  # 汇总：每条命题 结论 + 说明 + 证据路径 + 退出码
├── run.log                     # 完整编排日志（stdout/stderr 合流）
├── 00-baseline/                # 运行前快照：共享容器 ID、:3080/:8082 基线、git HEAD
├── 00-compose-config-*.json    # 解析后的 compose 配置（安全校验输入）
├── 00-compose-config-*.safety  # 安全校验结论（OK / UNSAFE 原因）
├── 00-compose-images.tsv       # service -> image 映射
├── 00-db-create.log            # CREATE DATABASE 输出
├── <id>-<name>/                # 每个探针
│   ├── meta.txt                # 实际调用的路径 + 注入的环境变量（可复现）
│   ├── stdout.log stderr.log rc
│   └── stack-up.log stack-ps.log stack-logs.txt   # 需要栈的探针
├── S1-cache-warmup/result.txt  # SUP-1 观察到的行与 N
├── 97-shared-health/shared.txt # 共享容器 / :3080 / :8082 运行后核对
├── 98-zero-residue/residue.txt # 零残留核对（容器/卷/网络/独立库计数）
└── 99-cleanup/cleanup.log      # 清理动作逐条记录
```

- `deploy/blackbox/evidence/latest` 软链到最近一次运行；`latest-SUMMARY.md` 是它的快照。
- **退出码**：`0`=全绿 ／ `1`=有失败、或有残留、或共享资源异常 ／ `2`=有 SKIP（探针缺失或主动跳过），**不算通过**。

常用查看方式：

```bash
cat deploy/blackbox/evidence/latest/SUMMARY.md
grep -E '[(0[1-7]|S1)/' deploy/blackbox/evidence/latest/run.log   # 逐条结论
cat deploy/blackbox/evidence/latest/98-zero-residue/residue.txt   # 零残留
cat deploy/blackbox/evidence/latest/97-shared-health/shared.txt   # 共享资源未受影响
```

---

## 3. 怎么清理

清理是**硬要求**，`run.sh` 用 `trap EXIT/INT/TERM` 保证**异常路径也执行**：

1. `docker compose -f docker-compose.yml -f compose.bb.yml -p ehome-bb down -v --remove-orphans`
2. 再按 **label `com.docker.compose.project=ehome-bb`** 与 **名字前缀 `ehome-bb-`** 精确清理容器/卷/网络
   （共享容器、共享卷 `*ehome-pgdata` 等、共享网络 `ehomesystem_default` **有白名单保护，拒绝删除**）
3. 独立库清理：**只允许** `^ehome_bb_[A-Za-z0-9_]+$`，且保留名单
   `ehome` / `ehome_test` / `ehome_uiux` / `ehome_sim_pg` / `postgres` / `template*` **永不 DROP**
4. `check_residue` 核对**容器/卷/网络/独立库计数必须全为 0**，任一非 0 → 退出码 `1`

手工兜底：

```bash
docker compose -f docker-compose.yml -f deploy/blackbox/compose.bb.yml -p ehome-bb down -v
docker ps -a --filter label=com.docker.compose.project=ehome-bb
docker volume ls  | grep ehome-bb ;  docker network ls | grep ehome-bb
docker exec -i ehome-postgres psql -U ehome -d postgres -At -c \
  "SELECT datname FROM pg_database WHERE datname LIKE 'ehome_bb_%'"
# 若上面列出了库，逐个 DROP（仅限 ehome_bb_* 前缀）：
docker exec -i ehome-postgres psql -U ehome -d postgres -At -c \
  'DROP DATABASE IF EXISTS "ehome_bb_<具体名>" WITH (FORCE)'
```

---

## 4. 探针环境契约（`run.sh` 注入给 `probes/*.sh`）

探针以**可执行脚本、无参数**方式调用，上下文经环境变量传入：

| 变量 | 含义 |
|---|---|
| `BB_REPO_ROOT` | 仓库根绝对路径（探针 cwd 也设为它） |
| `BB_DIR` | `deploy/blackbox` 绝对路径 |
| `BB_EVIDENCE_DIR` | 本探针专属证据目录（原始响应/日志写这里） |
| `BB_RUN_ID` | 本次运行 id（`D-<时间戳>`） |
| `BB_PROJECT` | compose project 名（恒为 `ehome-bb`） |
| `BB_HOME_PORT` | 黑盒栈宿主端口（默认 `18080`） |
| `BB_DB_NAME` | 独立库名，形如 `ehome_bb_<时间戳>`（**不得**改用共享库） |
| `BB_COMPOSE_BASE` / `BB_COMPOSE_FILE` | 基线 `docker-compose.yml` / `compose.bb.yml` |
| `BB_IMAGE` / `BB_APP_IMAGE` | 解析出的应用镜像名 |
| `BB_SKIP_BUILD` | `1` 表示复用已构建镜像 |
| `BB_STACK_UP` | `1` 表示栈当前已就绪 |
| `BB_COMPOSE` | 包装器路径，等价于 `docker compose -f ... -p ehome-bb` |
| `BB_PG_CONTAINER` / `BB_PG_USER` / `BB_PG_MAINT_DB` | 共享 PG 的容器名 / 用户 / 维护库（仅用于独立库运维，**不得**读写共享库） |
| `BB_BASE_3080` | 运行前 `:3080` 的基线状态码 |

**探针规则**：断言失败必须非 0 退出；`77` = 主动 SKIP；**不得**调用 `docker compose down` / `docker rm` / `DROP DATABASE`（生命周期由 `run.sh` 独占）。

---

## 5. 安全护栏（`run.sh` 自实现，实例化之前生效）

1. 解析后的 compose 若出现 **共享容器同名** 或 **受保护宿主端口**（`80/3080/8082/5432/1883/18083`），**拒绝起栈**；
   共享网络未标注 `external: true` 也拒绝。
2. project 名不是 `ehome-bb` 一律拒绝。
3. 清理按 label/名字前缀白名单执行，共享容器 ID 在启动时快照，**ID 命中即拒绝删除**。
4. `DROP DATABASE` 双重护栏：正则 `^ehome_bb_` + 保留名单。
5. 结束核对零残留，并断言 `:3080`（DSH GUI）与 `:8082`（审计后端）仍健康。

---

## 6. 已知边界（照抄方案 §6，诚实声明）

- **不做真实 ESP32 硬件验证**（需实机，见 `docs/操作/ESP32刷机指南.md`）
- **不做生产环境验证**（本方案在开发机上用独立 project；生产域名/证书/反代不在范围）
- **不做压力/容量测试**（`scripts/bench` 是另一条线）
- **不做镜像安全扫描**（CVE/漏洞扫描需额外工具，本方案只查「无密钥泄漏」）
- **不覆盖 `nginx` 反代路径**（80 端口被占用，本方案不经 nginx）

---

## 7. 已知注意事项 / 运行陷阱

- **`ehome-bb` 这个 project 名是共享资源**：同一台机器上**不要并行**跑两个黑盒栈
  （包括开发期自造的 override）。`run.sh` 清理时会 `down -v` 整个 project，会误伤另一个栈。
  并行开发请改用独立 project 名（如 `-p ehome-bb-dev`、容器名 `ehome-bb-dev-*`）。
- **探针缺失 ⇒ SKIP**，退出码 `2`，**不算通过**，报告里如实计入。
- 仓库**无 `.dockerignore`**，构建上下文约 913M（含 `node_modules` 318M）；首次构建约 2 分钟。
  国内网络加速：`--build-arg GOPROXY=https://goproxy.cn,direct`（`run.sh` 已默认注入该 GOPROXY）。
- **至少跑一次全量构建**：`--skip-build` 只用于迭代提速，不能替代一次完整 `run.sh`。
- `--only` 单跑时，被排除的探针记为「--only 未选中」的 SKIP，退出码按全局规则计算。
- 清理循环里所有 `docker exec -i` / `docker rm/volume rm/network rm` 均显式 `< /dev/null`：
  否则 `-i` 会消耗外层 `while read` 的 stdin，导致**只处理了列表第一项**——这是已修复的真实缺陷
  （先红：3 个 `ehome_bb_*` 库只 DROP 了 1 个；后绿：列出 6 = 处理 6，零残留）。

---

## 8. 复现一次全量

```bash
cd /home/sun/workspace/EHomeSystem
export PATH=/snap/bin:/home/sun/.local/share/pnpm/bin:$PATH; export HOME=/home/sun
./deploy/blackbox/run.sh ; echo "EXIT=$?"
cat deploy/blackbox/evidence/latest/SUMMARY.md
```
