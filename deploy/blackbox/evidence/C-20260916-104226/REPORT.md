# 子代理 C 报告 —— Q4 健康探针 + Q5 首次部署凭据流程 + 本会话修复的容器级复验

- 日期：2026-09-16
- 基线 commit：4698d230（运行前后 git status 均为空 ⇒ backend/、frontend-shared/ 未被改动，符合 CONTRACT 规则 A）
- 交付物：deploy/blackbox/probes/04-health.sh、deploy/blackbox/probes/05-first-boot.sh
- 证据目录：deploy/blackbox/evidence/C-20260916-104226/
- 镜像：ehome-bb-img:warm（基于基线 commit 重建；已复核镜像内确实含本轮新接线，见 §4.1）

## 0. 一句话结论

Q4（7/7 PASS）与 Q5（18/18 PASS）在**真容器 + 契约默认值**下全绿；
本会话三处修复（latest_data 多物理量 / reconfigure 真写库 / 告警 enabled:false 落库）**容器级复验全部成立**；
6 组变异自证全部按预期变红或按预期保持绿（后者证明"只看状态码"的判据太松）。
**过程中我自己引入并修复了 1 个真实秘密泄漏缺陷**（见 §6.1），已复验 0 泄漏。

---

## 1. 复现命令（契约模式，默认 project=ehome-bb / 端口 18080 / 独立库 ehome_bb_*）

~~~bash
cd /home/sun/workspace/EHomeSystem
set -a; . ./.env; set +a
export EHOME_EXTERNAL_HOST=127.0.0.1:18080 HOME_PORT=18080 EHOME_DB_NAME=ehome_bb_c_<runid> EHOME_BB_IMAGE=ehome-bb-img:warm
docker exec -i ehome-postgres psql -U ehome -d postgres -c 'CREATE DATABASE "ehome_bb_c_<runid>"' </dev/null
docker compose -p ehome-bb -f docker-compose.yml -f deploy/blackbox/compose.bb.yml up -d --wait --wait-timeout 240

BB_EVIDENCE_DIR=<evid>/04-health     bash deploy/blackbox/probes/04-health.sh
BB_EVIDENCE_DIR=<evid>/05-first-boot bash deploy/blackbox/probes/05-first-boot.sh

docker compose -p ehome-bb -f docker-compose.yml -f deploy/blackbox/compose.bb.yml down -v --remove-orphans
docker exec -i ehome-postgres psql -U ehome -d postgres -c 'DROP DATABASE IF EXISTS "ehome_bb_c_<runid>" WITH (FORCE)' </dev/null
~~~

开发自测（与交付 project 无前缀关系，CONTRACT 规则 B）：

~~~bash
BB_PROJECT=bbdevc BB_HOME_PORT=18092 BB_DB_NAME=ehome_bbdevc_c \
  bash deploy/blackbox/probes/04-health.sh
~~~

> 前提：探针**只探测、不起栈**（生命周期归 D 的 run.sh）。若容器未运行 ⇒ 主动 SKIP(77)。
> 探针会等待 /health 就绪（compose 的 ehome 服务**没有 healthcheck**，up -d --wait 只保证"容器已运行"
> 不保证 HTTP 已监听），避免把启动竞态误判成"端点不可达"。

---

## 2. Q4 04-health.sh —— 7 PASS / 0 FAIL（分母守卫：实际探测 4 个端点）

| 断言 | 内容 | 结论 |
|---|---|---|
| H1 | GET /health **未鉴权**可达 200，且响应体含 "status" | PASS（{"status":"ok"}） |
| H2 | GET /metrics **未鉴权**可达 200，且是 Prometheus 文本格式（# HELP） | PASS（bytes=16487, help_lines=65） |
| H3 | **反向对照**：GET /api/v1/overview 未鉴权必须 401 | PASS（401 missing authentication token） |
| H4 | /api/v1/health 的真实形态：**不是健康端点**，而是 SPA catch-all | PASS（200 + text/html + index.html） |
| H4b | **负控**：不存在的路径虽可能 200，但**通不过内容判据** | PASS |
| H5 | **分母守卫**：真的探测了 ≥2 个端点 | PASS（4 个） |
| H6 | 冷启动日志出现 Latest value cache warmed up: N rows | PASS（全新库 N=0） |

### 2.1 关键发现：/api/v1/health 是**假绿陷阱**

GET /api/v1/health 返回 **200 + index.html**（SPA catch-all），**不是**健康端点。
实测更狠的一点：**任意不存在的路径**同样返回 200 + HTML。因此：

> **"status==200 就算健康"在本服务上是天生的假绿发生器。**

我的断言因此**不靠裸 200**：H1 要求 JSON 含 "status"，H2 要求含 "# HELP"，
并由 H4b 负控证明内容判据能把 catch-all 挡在外面（详见 §5 的 M2 变异）。

---

## 3. Q5 05-first-boot.sh —— 18 PASS / 0 FAIL

| 断言 | 内容 | 结论 |
|---|---|---|
| F1 | 冷启动日志出现一次性凭据（**只记出现过 + 长度特征**） | PASS：total_len=68, selector_len=24, secret_len=43；**原值未落盘** |
| F1b/F1c | **负控**（不存在的标记 0 命中）+ **正控**（抽取器确实抽出 68 字符） | PASS |
| F4/F4b | **错误凭据**初始化被拒 409，且系统**仍未初始化** | PASS |
| F2/F2b | 用正确凭据建管理员 201，状态转 initialized | PASS |
| F3 | 新管理员登录 200 且拿到 token | PASS（token_len=251） |
| F5a | **重放已消费的凭据**被拒 409 | PASS |
| F5b | **重复初始化**（系统已 initialized 后再初始化）被拒 409 | PASS |
| F5c | 重复初始化后**原管理员仍可登录**（未被覆盖） | PASS |
| F5d | 入侵者账号**不存在**（401，未被创建） | PASS |
| F5e | 以 token 读 /account 确认身份仍为 bbadmin | PASS |
| F6/F10 | 冷启动回填日志出现且可解析 | PASS（全新库 N=0） |

### 3.1 一次性凭据的形态（不泄漏原值）

- 形状：<selector>.<secret>，**selector 24 字符、secret 43 字符、总长 68**
- 只记录"出现过 + 长度 + 是否含分隔符"；证据文件里该值被替换为 <REDACTED:credential>
- 日志原文：Initialization credential (valid for 10 minutes): <REDACTED:credential>

---

## 4. 本会话修复的容器级复验（F7–F10）

| 断言 | 内容 | 容器内实测 |
|---|---|---|
| **F7** | /overview.latest_data 在冷启动/运行中返回**多物理量**（修复前缓存命中只 1 个） | **PASS**：一帧 2 个物理量（LK-TH01 temp+humidity），n_qty=2 |
| **F8** | POST /channels/:id/reconfigure 的**响应里 bus_config 真的变了**，且 GET 回读（落库值）也变了 | **PASS**：AABB00002580CC → AABB0001C200CC，响应值==回读值 |
| **F9** | POST /alert-rules 带 enabled:false ⇒ 响应与回读均为 false | **PASS**：201 / 响应=false / 回读=false |
| **F10** | 启动回填日志可解析 | **PASS**（N=0，全新库） |

### 4.1 关于 WarmupLatestValues：修复**确实已接线**（我独立复核了镜像）

主控本轮把"启动回填"接线进生产（此前自 2026-08-21 起**无任何调用者**）。我必须先确认镜像里真有这段代码，
否则跑出来的绿只是"旧镜像 + 恰好的 SQL 回落"。做法：直接在镜像内查符号。

- 镜像构建时间 09:17:33；cmd/server/main.go 修改时间 09:16:50 ⇒ 镜像**晚于**修复 ⇒ 已含修复
- 镜像内 /app/ehome-server 含字符串 "Latest value cache warmed up"（grep 命中 1 次）；
  而**修复前**的旧镜像命中 0 次（我实测过）—— 这是"镜像含新代码"的独立证据

### 4.2 实测走的是哪条路径（如实记录）

| 场景 | 观察 | 路径判定 |
|---|---|---|
| 全新库冷启动 | Latest value cache warmed up: 0 rows；发布数据后 /overview n_qty=2 | 首次查询走**缓存 miss ⇒ 回落 DISTINCT ON SQL**（结果正确，2 个物理量） |
| 发布后**重启容器**（内存缓存清空，库有数据） | Latest value cache warmed up: 2 rows；/overview 仍 n_qty=2 | 启动回填**真的把多槽装回内存**（2 行），重启后仍多物理量 |

> ⇒ **回填在生产路径上确实发生**（N=2 只在"库里有数据 + 重启"时出现，正是它的设计场景）。
> 空库 N=0 属正常；该行**始终出现**证明接线生效。

### 4.3 一个"未覆盖项"（主动缩小结论范围）

我自己起栈时**只做了冷启动**（全新库），因此 F7 的断言是**冷启动（缓存未命中 ⇒ 回落 SQL）**路径的绿。
**"缓存命中"路径**（发布后同容器内再次查询）我在开发环境**单独手工验证过**（同为 n_qty=2），
但**没有纳入 05 的常规断言路径**，因为在我的起栈方式下栈始终是全新的。
若要断言"缓存命中"路径，需要同容器内在 TTL(30s) 内查询两次 —— 我**未做**，如实声明。

### 4.4 F8 的判据为什么"必须看 DB 值"（M5 变异给出硬证据）

M5 用**生产代码里真实存在的"返回 200 但不写库"路径**作为"故意跳过写库"的替身：
把波特率重配成**当前已有值** ⇒ 服务端返回 200 + status=unchanged 且**不写库**。

- **M5a**（严格判据：200 且 AFTER≠BEFORE 且 响应==AFTER）⇒ **变红**（抓到"没写库"）
- **M5b**（放宽为"只要 200 就算过"）⇒ **保持绿**

⇒ 证明 F8 若只看状态码就会**假绿**；必须看 DB 回读值。这也是本任务"谎报成功类缺陷"的核心教训。

---

## 5. 变异自证矩阵（全部**从当前源现生成**，并自证"真的改了东西"）

| # | 变异 | 期望 | 实测 | 落地自证文件 |
|---|---|---|---|---|
| M1 | 04：健康断言由"未鉴权应 200"改为"应 401" | 红 | **exit=1** ✓ | mutation/M1-LANDING.txt |
| M2 | 04：断言放宽为"裸 200 即健康" + 路径改 catch-all | **绿**（证明太松） | **exit=0** ✓ | mutation/M2-LANDING.txt |
| M3 | 05：错误凭据断言改为"应成功 201" | 红 | **exit=1** ✓ | mutation/M3-LANDING.txt |
| M4 | 05：F8 判据放宽为"只看 200" | **绿**（污染对照） | **exit=0** ✓ | mutation/M4-LANDING.txt |
| M5a | 05：真实"200 但不写库"(unchanged) + 严格判据 | 红 | **exit=1** ✓ | mutation/M5-LANDING.txt |
| M5b | 05：同一 no-op + 只看 200 | **绿** | **exit=0** ✓ | mutation/M5-LANDING.txt |
| BASE | 04 未变异基线 | 绿 | **exit=0** ✓ | mutation/BASE-04-stdout.txt |

- M2/M4/M5b 的"绿"**不是缺陷**，而是**故意的污染对照**：它证明"只看状态码/裸 200"的判据抓不到问题，
  从而反证我的断言**必须**看内容与 DB 值。
- 每个变异体都随附 *-LANDING.txt：记录源文件 md5 + 改动前后的行 + grep 变异体得到的实际行
  （纪律 2："变异脚本必须自证真的改了东西"）。
- 有一处**过程修正**必须记录：M3 第一次跑时 F2 意外 409，我起初怀疑服务端，
  实际是**变异体生成在使用旧的 05 源**（我用 `${CRED%%.*}` 抽出 selector 明文"同时证明了这一点）。
  已把 M1–M5 **全部从当前源重新生成**并重跑，上表为最终结果。

---

## 6. 我在本轮**自己引入并修复**的缺陷（如实报告）

### 6.1 【真泄漏】04 曾把含一次性凭据的启动日志原样写入证据（已修）

- **现象**：交付模式取证的秘密核查抓到 04-health/H6-startup-logs.txt 含 **1 个凭据形状 token**
  （启动日志里有 Initialization credential ...: <selector>.<secret>），SECRET_LEAK_CHECK=FAIL。
- **根因**：04 会把 docker logs 落盘做 H6 证据，但**没有脱敏**；我此前只把脱敏放在 05。
  错误假设："凭据是 05 的事"—— 只要**任何**探针落盘启动日志，就有泄漏面。
- **修法**：04 也内置同一份 redact_file（python3 实现），写完 H6 日志**立刻**脱敏，并在末尾对
  整个证据目录做兜底扫描；同时**删除**了那次已泄漏的证据目录（不让"已泄漏"留在交付物里）。
- **复验**：重跑契约模式 ⇒ credential_shaped_tokens_found=0，SECRET_LEAK_CHECK=PASS；
  两个日志文件里该行均为 ...: <REDACTED:credential>。

### 6.2 【真实 bug】凭据抽取用 head -1 在"重启"场景取到**最旧**（已消费）的凭据

- **现象**：M3 第一次跑时 F2 报 409（正确凭据却建不了管理员）。
- **根因**：docker logs 保留**同一容器**所有历史启动；每次启动都会新签发一次性凭据。
  容器是 restart（而非重建）时，grep ... | head -1 取到最旧一条 —— 早已 consumed/过期。
- **修法**：改为 tail -1（取当前启动签发的那条），并在代码注释里写明原因。

### 6.3 【真实 bug】"长度特征"曾差点打印 selector 明文

- 原写法 selector_len=<CRED 的 selector 部分> 打印的是 **selector 本身**（不是长度），
  而 selector **不含 "."** ⇒ 脱敏正则（要求点分）**匹配不到** ⇒ 真泄漏。
- 已改为先取子串再量长度（并修掉一个**非法 bash 语法**带来的 bad substitution）。

### 6.4 【真实 bug】脱敏用 sed 在本机报 "Invalid preceding regular expression"

- GNU sed ERE 对 {m,n} 方言不兼容，导致 redact_file **静默失效**（有 `|| true` 兜底）。
- 已改为 python3 实现（本仓探针已有依赖），并在 §6.1 复验中确认规则真的生效。

---

## 7. 清理与零残留（含异常路径）

- 开发与交付两轮均用 trap ... EXIT 清理（交付脚本见 evidence/C-*/cleanup.log）。
- **最终核对（期望空，实测全空）**：
  - 容器：无 ehome-bb-* / bbdevc-*
  - 卷 / 网络：无 ehome-bb* / bbdevc*
  - 独立库：SELECT datname LIKE 'ehome_bb%' ⇒ **空**（只剩共享 ehome/ehome_test/ehome_uiux/ehome_sim_pg）
- **共享资源未受影响**：ehome-postgres/ehome-emqx/nginx/ddns-go 均 running；
  :3080 有响应、:8082/health={"status":"ok"}；:18080 已释放（000）。
- 证据：evidence/C-*/zero-residue.txt

### 7.1 共享 EMQX 上的副作用（主动披露，非破坏性）

黑盒栈按 B 的设计**复用共享 ehome-emqx**，而该 broker 上**同时**跑着生产 :8082 审计后端
（实测 client ehome-server-v2-...）。我向 nodes/sim-*/up 发布数据帧，两侧节点服务都会收到；
但我的假节点在 ehome_uiux 里**没有对应 edge device**，事件保持 passive（ShouldPersist/ShouldParse 均为 false）
⇒ **不写业务表**。唯一观察到的副作用是 device_data 的**跳过宽限**（skip_harmless）会为未绑定节点记 1 行：

- 测试前后（只读计数）：ehome_uiux.device_data 由 396062 → 396064（**+2**，对应我的 2 次发布）
- **未修改共享库任何对象**（无 DROP/ALTER/DELETE/UPDATE），仅由**生产服务自身的正常处理**产生这 2 行跳过记录
- 区分度：真正的写路径（unified_data）**没有**新增行；这也是"未污染业务数据"的证据

---

## 8. 未覆盖 / 不确定（诚实声明）

1. F7 的**"缓存命中"路径未纳入自动断言**（我的起栈方式总是全新库 ⇒ 冷启动未命中）；仅开发环境手工验证过，见 §4.3。
2. **未验证 {"enabled": true} 的正向对照**（只验了 enabled:false 如实落库）。
3. **未做重启后路由级复验**：主控提到的"重启后路由级仍多物理量"我**未独立复验**（超出我文件范围，且需同容器缓存热态）。
4. **不做**：镜像构建（属 A）、compose env 契约（属 B）、跨容器连通/服务名（Q6，属 B）、可重复构建（Q7，属 A）。
5. **不做**：真实 ESP32 硬件、nginx 反代路径、压力/容量、镜像 CVE 扫描（与方案 §6 一致）。
6. 我发布的是**自造的 LK-TH01 数据帧**（复用仓库 pkg/frame 的真实编码语义），
   不是真实硬件流量；足以验证"一帧多物理量"的形状，但**不代表**真实设备驱动的端到端覆盖。
7. /metrics 只断言"可达 + 是 Prometheus 文本格式"，**未**校验具体指标名/数值语义。
8. 探针**不负责起栈**：若不由 run.sh 编排，需调用者先保证栈在跑（否则 SKIP 77）。
9. 未接入 D 的 probes/_lock.sh 排他锁（我的探针不起栈、不改栈，仅读 + 建自己的测试资源）。
   若 D 认为需要，我可在下一轮补上。

---

## 9. 证据索引

~~~
evidence/C-20260916-104226/
├── run.txt                      # 契约模式完整 stdout（04+05 逐条 PASS/FAIL）
├── cleanup.log                  # down -v + DROP 独立库
├── zero-residue.txt             # 残留/共享资源核对
├── 04-health/                   # H1..H6 逐条原始响应 + result.txt
├── 05-first-boot/               # F1..F10 逐条原始响应（凭据已脱敏）+ result.txt
└── mutation/                    # M1..M5 变异体(.sh) + 落地自证(-LANDING.txt) + 退出码/输出
~~~

**源文件 md5（本轮最终）**
- 04-health.sh = 9c864b86d2a297a944594d095ab4d3f7
- 05-first-boot.sh = 47ddff3e9504686960545fe7f1d876c9
