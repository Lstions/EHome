# Q6 跨容器连通 验证报告（子代理 B · probes/06-wiring.sh）

> 时间：2026-09-16 · 基线 commit `4698d230` · 规则 A 全程成立（backend/frontend-shared 未改动）

## 1. 结论

| 项 | 结果 | 证据 |
|---|---|---|
| Q6 交付模式（project=ehome-bb / 18080） | **16 PASS / 0 FAIL** | `B-20260916-102457-q6-delivery/06-wiring.log` |
| Q6 反向对照（broker 指错地址） | **如期变红：PG 绿 / EMQX 红** | `B-20260916-102522-q6-reverse/06-wiring.log` |
| Q6 dev 模式（project=bbdevb） | 16 PASS / 0 FAIL | `B-20260916-101858-q6-dev/06-wiring-DEV-normal.log` |
| 探针自证能力 | 反向对照仅 MQTT 侧变红，PG 不连带 ⇒ 两断言**互相隔离** | 同上 |

**web→postgres:5432 与 web→emqx:1883 均经【服务名】连通**（非 127.0.0.1）。

## 2. 被断言的不变量与证据形态

1. **服务名解析**（容器内只读 `getent`）
   `getent hosts postgres => 172.18.0.3` / `getent hosts emqx => 172.18.0.2`
2. **web→postgres:5432**：日志 `Database connected and migrated` 且指向本 run 独立库；
   独立库 `public` 表数 = **53**（分母守卫 >=30，防空壳）。
   **反向证据**：断言「日志未见 127.0.0.1 连库」通过 ⇒ 确走容器网络服务名。
3. **web→emqx:1883（硬证据）**：`emqx ctl clients list` 出现 `peername=<web 容器 IP>`，
   且该 client id 不在起栈前快照中（before/after **差集**）⇒ 归因到本次启动，非历史残留。
4. **分母守卫**：emqx 客户端列表非空 / web 日志 >=30 行 / 显式 `检查 2 条连线`。

## 3. 网络方案：external network 复用（已实测）

现有 `ehome-postgres` / `ehome-emqx` 挂在 compose 网络 `ehomesystem_default`，
DNS 别名同时含**服务名** `postgres`/`emqx` 与**容器名** `ehome-postgres`/`ehome-emqx`：

```
/ehome-postgres networks=ehomesystem_default(aliases=[ehome-postgres postgres])
/ehome-emqx     networks=ehomesystem_default(aliases=[ehome-emqx emqx])
```

因此 `compose.bb.yml` 用 **external network** 直接接入，**无需新起 PG/EMQX**：
省资源、无端口冲突，且服务名与生产 compose 内一致。
（`docker inspect` 原文见 `06-wiring-net-probe.txt`）

## 4. 复现命令

```bash
# 交付模式（契约值）
cd /home/sun/workspace/EHomeSystem
BB_EVIDENCE=$PWD/deploy/blackbox/evidence/q6 \
  bash deploy/blackbox/probes/06-wiring.sh

# 反向对照（broker 变异 ⇒ 期望 PG 绿 / EMQX 红）
BB_MUTATE_BROKER=tcp://nonexistent-broker-xyz:1883 BB_EXPECT_FAIL=1 \
  BB_EVIDENCE=$PWD/deploy/blackbox/evidence/q6rev \
  bash deploy/blackbox/probes/06-wiring.sh

# 开发自测（规则 B：dev project 不得与 ehome-bb 有前缀关系）
BB_PROJECT=bbdevb BB_HOME_PORT=18093 BB_DB=ehome_bbdev_wiring_1 \
  bash deploy/blackbox/probes/06-wiring.sh
```

## 5. 未覆盖 / 不确定性（如实声明）

- **未做**：MQTT 报文的端到端业务语义（属 C/D）；TLS/鉴权 broker（本栈用匿名 `tcp://emqx:1883`，与 `docker-compose.yml` 一致）。
- **未做**：`web→emqx` 的**应用层**订阅回执断言。现有硬证据是 TCP 连接 + CONNECT 成功（client 出现在 `emqx ctl`），
  未逐条核对 SUBACK；应用日志级别不足以区分。**这是本报告结论的边界。**
- **不确定**：交付模式两次取证均成功，但此前 **3 次**被并发的 `run.sh` 从外部清理打断
  （硬证据链见 `B-20260916-101858-q6-dev/COLLISION-NOTES.md`）。
  根因是**同 project `ehome-bb` 并发**：label 精确匹配对同 project 无效，只能靠串行化。
  若最终全量演练时仍有并发，交付取证可能再次被污染。
- **未做**：容器内 `nc` 直连 `postgres:5432`/`emqx:1883` 的主动探针 —— 刻意避开
  「在容器内执行命令」的边界，改用日志/`emqx ctl`/`getent` 等只读公开信号。
