# 并发清理互踩取证（子代理 B · Q6 交付模式阻断）

## 摘要
Q6 的**业务逻辑**已在 dev project 下完整证明（正常 16/16 + 反向对照隔离变红）。
但**交付模式**（project 必须 = 契约值 `ehome-bb`）三次尝试均被并发的 `run.sh` 从外部
kill/destroy 容器，无法取得自洽证据。三起事件均由 docker events + 对方 cleanup.log 硬证据定位。

---

## 事件 1：dev 容器被 `^ehome-bb-` 前缀清理命中
- 我的 project `ehome-bb-dev-b`，容器 `ehome-bb-dev-b-ehome`
- docker events：`1789524880 kill → stop → die → destroy`
- 对方证据：`evidence/D-20260916-101453/99-cleanup/cleanup.log` 原文
  `  docker rm -f ehome-bb-dev-b-ehome`
- 根因：run.sh 用 `docker ps -aq --filter name=^ehome-bb-`，`^ehome-bb-` 同时命中
  `ehome-bb-dev-b-ehome`。主控已复现并修正 CONTRACT 规则 B（dev project 不得与交付 project 有前缀关系）。
- 规避：改用 `bbdevb` 后正常/反向对照均完整跑通。

## 事件 2：交付模式第 1 次 —— 容器 cid 变化（我读到了别的 run 的容器）
- 我的库 `ehome_bb_20260916_101937`；但 `docker logs` 读到的容器启动日志显示
  `DB=postgres:5432/ehome_bb_20260916_101950` ⇒ 容器已被重建为 D 的 run（runid 101950）。
- docker events（同一容器名 ehome-bb-ehome）：`create→start→kill→stop→die→destroy→rename→start`
- 现象：分母守卫只有 3 行日志、PG 断言假红。**探针如实报红，未假绿。**

## 事件 3：交付模式第 2 次 —— 断言 4 前容器被删（误报为「MQTT 不通」）
- docker events：`1789525282 create → 1789525283 start → 1789525296 exec(getent postgres/emqx) → 1789525297 kill→stop→die→destroy`
- web 容器 IP 曾为 `172.18.0.5`（断言 2 的 getent 成功，证明容器当时存在且在网）
- 断言 4 时 `docker inspect` 已 `No such object` ⇒ 被外部删除，而非 MQTT 连不上。
- 已加「断言 4 前置存活复核」把这种情况标注为 COLLISION，不再误判为 EMQX 红。

## 机制（为什么 label 修复后仍会互踩）
事件 2/3 的容器都属 **project `ehome-bb`**。D 已把清理改为按 compose label 精确，
但我的交付取证**也用** `-p ehome-bb` ⇒ `com.docker.compose.project=ehome-bb` 相同，
label 精确匹配**依然命中我的容器**。⇒ 根因是**同 project 并发**，不是命名，
无法靠单侧改动消除，只能由主控**串行化**（等 D 空闲再跑，或 D 暂停）。

## 结论
- Q6 断言本身有效（dev 下双绿 + 反向对照隔离变红）。
- 交付模式取证**未完成**，属**环境并发**导致的阻断，非探针/应用缺陷。
- 证据：本目录 `06-wiring-DEV-*.log`（有效）、`06-wiring-DELIVERY-attempt*.log`（被污染，仅作取证）、`DELIVERY2-docker-events.txt`。
