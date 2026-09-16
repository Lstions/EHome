# 部署黑盒验证 — 最终报告（子代理 D：编排 / 清理 / 汇总）

> 方案：`docs/分析/部署黑盒验证方案-2026-09-16.md`
> 主控指派：2026-09-16（第 2 轮）。基线 commit `4698d230`（另见下方「规则 A 核查」）。
> 演练证据：`deploy/blackbox/evidence/D-20260916-103616/`（一次全量、退出码 **0**）。

## 0. 结论一句话

**一次完整演练（全部 7 个探针在位、无 SKIP）退出码 = 0**：Q1–Q7 与附加检查 S1 全绿，
合计 **97 条断言 PASS / 0 FAIL**；结束核对 **零残留**（容器/卷/网络/独立库全为 0），
共享资源 `:3080` / `:8082` / 4 个共享容器未受影响。

| 命题 | 结论 | 断言 | 证据路径 | 复现命令 |
|---|---|---|---|---|
| Q1 镜像产物完整性 | PASS | 16/0 | `evidence/D-20260916-103616/01-image-contract/` | `run.sh --only 01` |
| Q7 可重复构建 | PASS | 12/0 | `evidence/D-20260916-103616/07-reproducible/` | `run.sh --only 07` |
| Q2 compose env 契约 | PASS | 13/0 | `evidence/D-20260916-103616/02-env-contract/` | `run.sh --only 02` |
| Q3 容器冷启动 | PASS | 15/0 | `evidence/D-20260916-103616/03-coldstart/` | `run.sh --only 03` |
| Q6 跨容器连通 | PASS | 16/0 | `evidence/D-20260916-103616/06-wiring/` | `run.sh --only 06` |
| Q4 健康探针可达 | PASS | 7/0 | `evidence/D-20260916-103616/04-health/` | `run.sh --only 04` |
| Q5 首次部署凭据 | PASS | 18/0 | `evidence/D-20260916-103616/05-first-boot/` | `run.sh --only 05` |
| S1 冷启动回填日志（附加） | PASS | 1/0 | `evidence/D-20260916-103616/S1-cache-warmup/` | 随全量/`--only 03` 产出 |

（断言计数取各探针自身 `PASS=/FAIL=` 汇总行；本报告不复算探针内部逻辑，只核对总数与退出码。）

---

## 1. 逐条报告（结论 / 证据 / 复现 / 未覆盖或不确定）

### Q1 镜像能构建且产物完整

- **结论**：PASS（16/0）。镜像 `ehome-bb-ehome:local` 构建成功；`/app/ehome-server` 为可执行 ELF；
  `/app/static/dist/index.html` 存在且其引用的 **10 个** asset 在镜像内**逐个存在**；`/app/firmwares` 存在；
  镜像内**无 `.env` / 无 PEM 类密钥**；二进制含 `Latest value cache warmed up` 格式串。
- **证据**：`evidence/D-20260916-103616/01-image-contract/01-assertions.tsv`、`01-image-contract.log`。
- **复现**：`./deploy/blackbox/run.sh --only 01`（或用 `--skip-build` 复用镜像）。
- **未覆盖 / 不确定**：
  - **不做 CVE/漏洞扫描**（方案 §6 明示）；只证明「无密钥泄漏」。
  - 体积区间（参考 90.4MB）是**软区间**，用于发现数量级异常，不是精确门禁。
  - 「无密钥」的判据是**内容型**（PEM 头等）+ Env 项数分母守卫；
    主控已修掉一次假阳性（把二进制里 `os.Getenv("EHOME_JWT_SECRET")` 的**变量名字面量**当密钥值）。
    因此该结论的边界是：**未发现密钥类内容**，不等于「穷尽了所有密钥形态」。

### Q7 镜像可重复构建

- **结论**：PASS（12/0）。同 commit 连续两次多阶段构建：index.html 引用的 asset 文件名集合一致（各 10 个）、
  `/app/static/dist` 整棵目录文件名集合一致（各 100 个）、index.html 字节级 sha256 一致；
  **深比对**：两次 `/app` 内**每个文件**的 sha256 全部一致（共 101 个）⇒ 产物字节级确定。
- **证据**：`evidence/A-20260916T030500Z-a5deep/07-assertions.tsv`（交付探针的完整深比对运行）、
  `evidence/D-20260916-103616/07-reproducible/`（本次演练运行，12/0）。
- **复现**：`./deploy/blackbox/run.sh --only 07`（会构建两次；临时镜像结束即删）。
- **未覆盖 / 不确定**：
  - 只证明**同一 commit、同一机器、同一架构**下确定；**不覆盖**跨 commit、跨机器/架构（如 arm64）、
    以及构建期嵌入时间戳/随机数的情形。
  - 本次演练（`--only 07` 于全量中）断言数为 12，深比对那次的 14 条含额外的正控/幂等自证条目；
    两者结论方向一致，深度不同，已如实区分。

### Q2 compose 的 env 契约正确

- **结论**：PASS（13/0）。缺 `EHOME_EXTERNAL_HOST` / 缺 `EHOME_JWT_SECRET` 时 `compose config` **非 0 退出**
  且 stderr **点明变量名**、含 required-variable 语义；必填给全则 `config --quiet` 通过；
  并断言渲染产物确实是本 override（含 `ehome-bb-ehome`、`EHOME_DB_NAME`、`published: "18080"`、`ehomesystem_default`）。
- **证据**：`evidence/D-20260916-103616/02-env-contract/02-env-contract.log`。
- **复现**：`./deploy/blackbox/run.sh --only 02`。
- **亮点（假绿自证）**：同一脚本证明「**不抑制 `.env`** 时缺变量断言会**假绿**（rc=0）」——
  故「必须 `--env-file` 空文件抑制仓库根 `.env`」不是多余步骤。
- **未覆盖 / 不确定**：
  - 只覆盖 **compose config 阶段**的契约；变量进入容器**运行时**后的行为属 Q3/Q5，不在本条。
  - 依赖前提守卫「仓库根 `.env` 存在且含这两个变量」；若 `.env` 缺失，则那条**漏检自证**失去意义
    （探针会把前提守卫判红，而不是静默通过）。

### Q3 容器冷启动全序列成功

- **结论**：PASS（15/0）。容器进入 `running`（`Restarting=false`，非 Exited/Restarting）；
  日志出现 `Database connected successfully` / `Database connected and migrated` / `API server listening`；
  无 panic/FATAL；**独立库 `ehome_bb_20260916_103616` 被创建且 public 表数 53（>=30）**；
  启动日志含 `/ehome_bb_20260916_103616` ⇒ 应用连的**就是本次 run 的独立库**（未误连共享库）。
- **证据**：`evidence/D-20260916-103616/03-coldstart/`（`03-coldstart.log`、`03-container.log`、`03-compose-up.log`）。
- **复现**：`./deploy/blackbox/run.sh --only 03`。变异自证：`BB_MUTATE_DB_HOST=nonexistent-db-host-xyz`
  （配 `BB_EXPECT_FAIL=1` 反转为 exit 0）。
- **未覆盖 / 不确定**：
  - 方案 §1.3 提到「退役 DDL」；本探针**未按名称逐条断言退役 DDL**，只断言「migration 迹象 >=2 条 + 表数 >=30」。
    ⇒ **主动缩小结论**：Q3 证明「冷启动序列成功、schema 建成、无崩溃」，
    **不**单独证明「退役 DDL 的语义/幂等正确」。
  - 只覆盖「全新空库」冷启动；不在本条覆盖「已有旧 schema 的升级路径」。

### Q6 跨容器连通（服务名）

- **结论**：PASS（16/0，交付模式）。web 容器内 `getent hosts postgres|emqx` 均可解析；
  **PG 侧**：日志未见 `127.0.0.1` 连库 + 独立库 schema 表数 >=30；
  **EMQX 侧**：`emqx ctl clients list` 出现 `peername=<web 容器 IP>` 的客户端，且该 client id 为起栈后**新增**（before/after 差集）。
  反向对照（`MQTT_BROKER=tcp://nonexistent-broker-xyz`）使 **EMQX 专红而 PG 仍绿**，隔离成立。
- **证据**：`evidence/B-20260916-102457-q6-delivery/06-wiring.log`（交付模式 16/0，双绿）；
  本次演练：`evidence/D-20260916-103616/06-wiring/`。
- **复现**：`./deploy/blackbox/run.sh --only 06`。
- **更正说明（重要，影响阅读旧结论）**：我曾在并发环境下跑出 Q6「PG=0」并把 06 记为 FAIL。
  复核发现：探针抓到的容器启动日志为 `DB=...ehome_bb_20260916-102121`，而该次 run 的库是 `ehome_bb_20260916_101950`
  ⇒ **抓到的是另一次并发 run 的容器**。故那次「PG=0」是**环境干扰**，**不是产品缺陷**；已撤回该 FAIL 结论。
  同理，B 早期一次 Q6「EMQX=1」失败也是同类并发干扰，交付模式重跑为 16/0。
- **未覆盖 / 不确定**：
  - 只覆盖 web→PG、web→EMQX 的**服务名解析与建连**；**不覆盖**真实设备 MQTT 上行/下行业务流。
  - 不覆盖跨主机/跨编排网络（仅 `ehomesystem_default` 单网络）。

### Q4 容器内健康探针可达

- **结论**：PASS（7/0）。`/health` 与 `/metrics` **未鉴权**可达；`/health` 返回 `{"status":"ok"}`；
  `/metrics` 为 Prometheus 格式（16535 bytes、65 条 HELP）；反向对照 `/api/v1/overview` 未鉴权被拒 401；
  本轮实际探测 **4 个端点**（>=2 分母守卫）。
- **证据**：`evidence/D-20260916-103616/04-health/`。
- **复现**：`./deploy/blackbox/run.sh --only 04`。
- **重要边界（探针自己发现的陷阱）**：`/api/v1/health` 实测是 **SPA catch-all 的 HTML 回退**（200 但非健康端点）。
  ⇒ 判据**不得**写成「该路径 200 即健康」。探针用**内容判据**（含 status 字段/`# HELP`）而非裸 200，
  并有负控证明「不存在的路径虽 200 但通不过内容判据」。
- **未覆盖 / 不确定**：
  - 不覆盖鉴权之后的 `/api/v1/*` 业务端点（属其它验证层）。
  - **不覆盖 nginx 反代路径**（80 被占用，本方案不经 nginx，方案 §6）。

### Q5 首次部署的一次性凭据流程

- **结论**：PASS（18/0）。冷启动日志出现一次性凭据（**只记录长度特征** total 68 = selector 24 + secret 43，**值不落盘**）；
  错误凭据初始化被拒 409 且系统仍 `uninitialized`；用正确凭据建管理员 201；状态转 `initialized`；
  新管理员登录 200；**重放已消费凭据被拒 409**；**重复初始化被拒 409**；重复初始化后原管理员仍可登录、
  入侵者账号不存在（401）、登录主体仍为 `bbadmin`（未被替换）。
- **证据**：`evidence/D-20260916-103616/05-first-boot/`。
- **复现**：`./deploy/blackbox/run.sh --only 05`。
- **附加段（本会话修复的容器级复验，F7–F10，均 PASS）**：
  - `latest_data` 冷启动下返回**多物理量** `n_qty=2`（>=2；修复前缓存命中只 1 个）。
  - `reconfigure` **真的改了 `bus_config`**：`AABB00002580CC -> AABB0001C200CC`，且**回读 DB 值**一致（不是只看裸 200）。
  - 告警规则 `enabled:false` **落库为 false**（响应与回读一致）。
  - 启动回填日志可解析：`Latest value cache warmed up: 0 rows`。
- **未覆盖 / 不确定**：
  - **不覆盖真实 ESP32 首次接入/绑定**（需实机，方案 §6）。
  - 凭据为秘密：证据中只有**长度与出现性**，无明文；因此**无法**从证据复算凭据本身，这是刻意取舍。

### S1（附加检查，非 Q1–Q7）容器冷启动出现回填日志

- **结论**：PASS。冷启动日志出现 `Latest value cache warmed up: 0 rows`（来源 `live:ehome-bb-ehome`）。
  **实测 N=0**，符合预期：本 run 用的是**全新独立库**，无存量数据可回填。该行**必须出现**（证明接线生效），
  而 N 的大小取决于库内数据量。
- **证据**：`evidence/D-20260916-103616/S1-cache-warmup/result.txt`。
- **来源代码**：`backend/cmd/server/main.go:263`（主控本会话修复的接线点）。
- **互补关系（主控认可）**：A 的 **A7** 证明「**镜像里**含该日志格式串」；
  **S1** 证明「**运行时真的打出来**」。两者都必要，缺一不可。
- **未覆盖 / 不确定**：
  - **N>0 路径未被验证**：本方案所有环境都是空独立库 ⇒ 只观测到 N=0。
    「有数据时回填 N 行且 N 正确」**未覆盖**（需要预先造数的库，本方案未做）。
  - 归类说明：S1 属主控临时指派、**不属原 Q1–Q7**，故在汇总中单列，不混入命题结论。

---

## 2. 哪些结论依赖「分母守卫」才可信（A 的发现 + 全量盘点）

A 实测到一类**假绿**：**逐个存在型断言会空转成绿**——产物整体缺失时
`FOUND=0 MISSING=0` 仍报 PASS（因为没有任何东西可判缺失）。
若没有**分母守卫**（断言「真的扫到了 N 个东西」），这类检查在产物整体消失时**不会变红**。
因此下列结论**必须**连同其分母守卫一起看，才可信：

| 结论 | 依赖的分母守卫 | 没有它会怎样 |
|---|---|---|
| Q1 静态资源完整 | 解析到的 asset 引用数 >=2、`/app` 文件数 >=5、`/assets/*.js` >=1 | 若整个 `dist` 缺失，引用数为 0 ⇒ 空转成绿 |
| Q1 无密钥 | 密钥扫描器**正控**（植入 canary 必被抓到）+ **负控**（不存在串 0 命中） | 「扫不到」既可能是真干净，也可能是扫描器坏了 |
| Q1 Env 无敏感键 | 镜像 `Config.Env` 项数 >=1 | Env 为空时断言空转 |
| Q2 env 契约 | 前提守卫（`.env` 含被测量；`.env.empty` 为空）+ 渲染守卫 4 个 needle | 抑制失效或渲染的不是本 override 时，断言没有意义 |
| Q3 冷启动 | 容器日志 >=30 行、migration 迹象 >=2、独立库 public 表数 >=30 | 日志为空/表数为 0 时「未出现 panic」等断言空转 |
| Q6 连通 | emqx 客户端列表非空、web 日志 >=30 行、**至少检查 2 条连线** | 连线数为 0 时「全部连通」空转成绿 |
| Q4 健康 | 实际探测端点数 >=2 + 内容判据 + 不存在路径的**负控** | 只探 0 个端点、或靠裸 200 判别 ⇒ 假绿 |
| Q5 凭据流程 | 凭据抽取器**正控**（已知存在的行能抽出）+ **负控**（不存在标记 0 命中） | 「没找到凭据」不可信 |
| Q7 可重复 | 两次都解析到 >=2 asset、dist 文件数 >=5、深比对各取 >=5 摘要 | 产物为空时「两次一致」是平凡真 |
| Q7 非空转自证 | 清空清单后**必须**判出差异（diff 行数 10）；sha 变化能被检出；同一清单自比较判一致；注入已知不同清单被判出差异 | 比较器恒报一致时，R1 结论无效 |

**给读者的判读建议**：看到「PASS」时，先看该探针的 `G*`/分母守卫条目是否也在同一份证据里 PASS；
只有守卫与结论**同时**成立，这个「绿」才是真绿。

---

## 3. 本方案**未覆盖**什么（照抄方案 §6，诚实声明）

- **不做真实 ESP32 硬件验证**（需实机，见 `docs/操作/ESP32刷机指南.md`）
- **不做生产环境验证**（本方案在开发机上用独立 project；生产域名/证书/反代不在范围）
- **不做压力/容量测试**（`scripts/bench` 是另一条线）
- **不做镜像安全扫描**（CVE/漏洞扫描需额外工具，本方案只查「无密钥泄漏」）
- **不覆盖 `nginx` 反代路径**（80 端口被占用，本方案不经 nginx）

### 另外补几条本方案范围内的明确未覆盖（主动缩小结论）

1. **S1 的 N>0 路径**：只观测到 N=0（空库），「有数据时回填行数正确」未验证。
2. **Q3 的「退役 DDL」语义**：未按名称逐条断言，只证明 migration 迹象与表数。
3. **升级路径**：只验证「全新空库冷启动」，不覆盖「旧 schema 升级」。
4. **多架构/多机器可重复性**：Q7 只覆盖本机 amd64、同 commit。
5. **并行并发**：本方案按设计**串行**执行（project 排他锁）；并发场景只验证了「被正确拒绝」，
   未验证「两个栈真能并存」——因为设计上就不允许。

---

## 4. 本轮由我（D）发现并修复的真缺陷（全部先红后绿）

| # | 缺陷 | 先红证据 | 修法 | 后绿证据 |
|---|---|---|---|---|
| D1 | 清理循环 `docker exec -i` **消耗外层 while 的 stdin** ⇒ 独立库**只 DROP 了第一个** | 造 3 个 `ehome_bb_*` ⇒ 只删 1 个、残留 2 | 全部加 `</dev/null`；加**分母守卫**（列出 N vs 处理 M） | 6 个库全删，列出=处理=6，零残留 |
| D2 | 残留检查用 SQL `LIKE 'ehome_bb_%'` —— SQL 的 `_` 是**单字符通配符** ⇒ 把 `ehome_bbdev_*` 误算为残留（**假红**） | 空 project 却报 `bb_databases>=1` | 改 `starts_with(datname,'ehome_bb_')` **字面量**比较 | 残留计数 0/0/0/0 |
| D3 | 容器/卷/网络清理与残留判据用**名字前缀** ⇒ 误删/误报并行 project（实测 `run.sh --only 03` 的清理删掉了子代理 B 的 `ehome-bb-dev-b-ehome`） | 并行 project 容器被删（证据 `D-20260916-101453/99-cleanup/cleanup.log`） | 统一为 **label 精确相等**；仅**无 label 孤儿**才用前缀兜底 | 后续 `--only 03` 运行中 B/C 的容器存活；零残留仍正确 |
| D4 | 清理**只按名字前缀**过滤 `ehome-bb-` ⇒ 在 `PROJECT=ehome-bb` 时命中一切同前驱 project | — | 只按 `label=com.docker.compose.project=$PROJECT` | 同上 |
| D5 | 编排器**全程持有 project 锁** ⇒ 自带同名锁的探针 03 **被自己人饿死**（exit 3） | `D-20260916-103213/03-coldstart/stderr.log`：持有者 pid 即 run.sh | 只在起栈/清理等短窗口持锁；**自管探针运行前释放**、跑完重取 | 最终演练 03 = 15/0 PASS |
| D6 | 探针 03/06 **自管生命周期**（自己 down + dropdb），而 run.sh 预起栈/预建库 ⇒ 03 `createdb` 因库已存在而失败 | 首次 03 集成测试 FAIL | 识别 self-managed 探针：不预起栈/不预建库；探针结束后**重新检测栈与库**并按需重建 | 03 PASS；后续 04/05 仍连到本 run 的库 |
| D7 | （加固，非缺陷）并发下探针可能**抓到别人的容器** ⇒ 误判产品缺陷 | Q6 那次「PG=0」 | 新增**栈所有权校验**（比对容器日志 `DB=` 与本 run 库名）+ 并发保护（锁 + 已有容器检测） | 所有权不符判 ENV/ERROR，不再误判产品 |

其中 **D1** 已由主控**独立复现根因**并确认；**D3/D4** 的判据统一也由主控复核并同步到卷/网络。

---

## 5. 环境护栏核查（最终演练，独立复核）

| 检查 | 结果 | 证据 |
|---|---|---|
| ehome-bb 残留容器 | 0 | 独立复核命令输出；`98-zero-residue/residue.txt` |
| ehome-bb 残留卷 | 0 | 同上 |
| ehome-bb 残留网络 | 0 | 同上 |
| 残留独立库 `ehome_bb_*` | 0 | `starts_with` 只读查询 |
| `:3080`（DSH GUI） | HTTP 401（与基线一致，仍在服务） | `97-shared-health/shared.txt` |
| `:8082`（审计后端） | `/health` = `{"status":"ok"}` | 同上 |
| 共享容器 | `ehome-postgres` healthy / `ehome-emqx` healthy / `nginx` running / `ddns-go` running | 同上 |
| 保留库 | `ehome` / `ehome_test` / `ehome_uiux` / `ehome_sim_pg` **均存在**，且**全程只读** | 同上 |

**规则 A 核查**：最终演练期间 `backend/`、`frontend-shared/` **无改动**；
`md5(backend/cmd/server/main.go)` = `45e038168fc8d748fe81965e140c17e9`，与主控声明一致。
（`git diff 4698d230..09c3d4e4 -- backend frontend-shared` 为空；期间变更均在 `deploy/blackbox/` 与其它代理的产物内。）

---

## 6. 已知限制 / 阅读本报告的注意事项

1. **`ehome-bb` 这个 project 名是共享资源**。本方案用**两层**并发保护：
   per-project `flock` 排他锁（`probes/_lock.sh`）+ 「同 project 已有运行容器」检测。
   抢不到锁 ⇒ 退出码 **3**（并发冲突，不是失败也不是通过），且**不清理他人资源**。
2. **退出码语义**：`0` 全绿 ／ `1` 失败或有残留或共享资源异常 ／ `2` 有 SKIP（探针缺失或主动跳过）
   —— **2 不算通过** ／ `3` 并发冲突未跑成。
3. **探针缺失 ⇒ 明确 SKIP 并计入，不算通过**（保持该行为；本次已无 SKIP）。
4. **主控曾直接修改 `run.sh`**（统一容器/卷/网络的清理判据）。我已复核，与我的设计意图一致，未回退。
5. 报告中的「PASS」只在**本机、本 commit（`4698d230` 基线）、本次演练**范围内成立；
   跨环境复现请以 `run.sh` 重跑为准。
6. 本报告**不复算**探针内部断言逻辑（那属 A/B/C 的职责），只核对：探针是否在位、退出码、
   各探针自报的 PASS/FAIL 总数、以及编排/清理/共享资源三项环境结论。

---

## 7. 一键复现

    cd /home/sun/workspace/EHomeSystem
    export PATH=/snap/bin:/home/sun/.local/share/pnpm/bin:$PATH; export HOME=/home/sun
    ./deploy/blackbox/run.sh            # 全量：期望退出码 0
    echo "EXIT=$?"
    cat deploy/blackbox/evidence/latest/SUMMARY.md
    cat deploy/blackbox/evidence/latest/98-zero-residue/residue.txt

单条复跑：`./deploy/blackbox/run.sh --only 04`（可逗号分隔，如 `--only 03,06`）。

完整编排日志：`deploy/blackbox/evidence/D-20260916-103616/run.log`。

