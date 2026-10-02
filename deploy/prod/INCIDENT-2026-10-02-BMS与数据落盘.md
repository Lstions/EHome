# 生产事故与恢复报告：真实 BMS 无数据 + 数据落盘改造

> 日期：2026-10-02 ｜ 环境：`/srv/ehomesystem`（`192.168.20.6:18090`，Debian 13 / Docker 29.8.1）
> 关联：`deploy/prod/TOPOLOGY.md`（台架拓扑）、`deploy/prod/KNOWN-ISSUES.md`

---

## 一、事故：真实 BMS 接入后服务端无数据

### 1.1 现象

- ESP32-S3 接真实嘉佰达 BMS：TX 灯亮后 RX 灯随即亮 —— 物理层双向都有动作；
- 服务端 100% 显示无数据，`edge_device.error_code=3`，`device_data` 的 `raw` 为空串；
- 通道终端不显示内容；设备日志里 `events=10 completions=10`（**RX 事件与完成数都非 0**）。

### 1.2 根因：`read_length` 被固件当成「最小长度」，变长帧被当短读丢弃

嘉佰达响应是**变长**的：`DD CMD STATUS LEN DATA... CHK_H CHK_L 77`，
其中 `0x03` 的 `LEN = 23 + 2×NTC`、`0x0F` 再叠加单体电压 ——
同一命令在不同电池包上字节数不同。

而 `config_templates.read_length` 在设备侧**不是建议值而是下限**：

| 环节 | 代码 | 行为 |
|---|---|---|
| 早发 | `bus_worker.c:1192` `bus_rx_boundary.h:18` | `buffered < read_size` ⇒ 不产出 |
| 空闲定帧 | `bus_worker.c:1345` | `s->len < pcmd.read_size` ⇒ **丢弃整帧**，发 `error_code=3`、空 raw |
| 上报 | `bus_worker.c:1356` | `report_enqueue(..., NULL, 0, 0x03, ...)` |

配置当时为 `read_comprehensive(0x0F) read_length=100`，而真实 0x0F 响应实测 **78 字节** ——
每帧都落进短读分支。这就是「设备在应答、服务端却什么都收不到」的原因。

**决定性佐证**：本轮之前唯一成功入库的 17 条数据，正是上一轮我按 `read_length=60`
**精确构造的 60 字节**注入帧。真实帧长度不等于配置值时，一条都进不来。

### 1.3 修复

**改为 `read_length=0`**，走固件的协议中立空闲定帧：

- `sender_snapshot.go:250`：`ReadLength == 0` 时**不编码** field 3；
- `bus_rx_boundary.h:18`：`read_size=0` ⇒ target 回退 512，不早发；
- 于是完全依赖 `UART_IDLE_THRESHOLD_US=10000`（10ms）空闲间隔断帧。
  9600 8N1 下帧内字节间隔约 1ms（远小于 10ms），帧间空闲远大于 10ms ⇒ 断帧正确。
- `bus_worker.c:1345` 条件为假 ⇒ 直接走**成功分支**，按真实长度上报。

代码层同步根修（防设备重建后回退）：
`backend/internal/drivers/jiabaida_control.go` —— 5 个模板 `ReadLength` 全部置 0，
并加长注释说明「不得改回具体值」；`jiabaida_test.go` 新增
`TestJiabaidaTemplatesUseIdleFraming` 作为防回退门禁。

### 1.4 修复效果（实测）

| 指标 | 修复前 | 修复后 |
|---|---|---|
| `error_code` | 3 | **0** |
| `status` | offline | **active** |
| `device_data` 记录 | 17 条（且均为注入帧） | **持续增长**（11372 → 12174+） |
| 通道终端 | 410 / 无内容 | **实时 78 字节帧，约 1 帧/秒** |

真实读数（修复后即时解码）：总压 **50.32→50.51 V**、电流 **1.19→1.32 A**、
RSOC 4–5%、循环 **300 次**、单体 **3.102–3.178 V**（压差 ~60 mV）、
温度 **30.25 / 28.15 / 32.25 °C** —— 数值随电池实时变化，确认为真实硬件。

---

## 二、部署改造：容器内数据 → 宿主机 bind mount

### 2.1 原问题

原部署用 Docker **named volume**，数据实际位于
`/var/lib/docker/volumes/ehomesystem_ehome-prod-*`：

- 拷贝 `/srv/ehomesystem` **得不到数据**；
- 迁移必须额外记得 `docker volume`，`down -v` 或重建卷即丢库；
- 备份/恢复语义不直观，依赖 Docker 内部状态。

> 项目历史上已经因此付过一次代价：`KNOWN-ISSUES.md` 记载生产卷曾被清空，
> 数据只能从 09-12 的旧备份恢复，**清空之后新增的数据无法找回**。

### 2.2 改造后

全部改为宿主机 bind mount，数据固定在 `/srv/ehomesystem/data/`：

| 目录 | 容器内路径 | 属主 |
|---|---|---|
| `data/postgres` | `/var/lib/postgresql` | 容器内 uid=70 |
| `data/emqx` | `/opt/emqx/data` | uid=1000 |
| `data/emqx-log` | `/opt/emqx/log` | uid=1000 |
| `data/firmwares` | `/app/firmwares` | root |

- **备份 = tar 打包 `/srv/ehomesystem/data`**；
- **迁移 = 拷贝该目录到目标机同路径**；
- `compose` 内不再声明 `volumes:` 段（保留注释说明原因）。

### 2.3 迁移步骤（已执行）

1. `docker compose stop`（named volume 原样保留，可回滚）；
2. `pg_dump -Fc` + 整个 volumes 打包 `tar.gz` 双保险；
3. `cp -a` 三个卷内容 → `/srv/ehomesystem/data/*`（保留属主/权限/时间戳）；
4. 替换 compose（旧文件存 `backups/docker-compose.pre-20261002.yml`）；
5. `docker compose up -d`，三容器 healthy。

**数据零丢失**：`device_data` 12174 条、`unified_data` 24031 条、节点/通道/设备/用户配置全部保留。

---

## 三、镜像与部署链路

本次修复需要**新后端二进制**，但 CI 镜像（rev `64fbe90`，8 小时前）不含该修复，
且生产机无源码、无 GHCR 凭据。

处理：在**生产机本地**用 `golang:1.26.5-alpine` 编译修正后的后端，
以 overlay 方式叠在 CI 已验证镜像之上（**仅替换 `/app/ehome-server`**，
前端/静态资源/运行时与 CI 产物逐字节一致）：

```dockerfile
FROM ghcr.io/lstions/ehome:latest
COPY ehome-server /app/ehome-server
```

产出 `ghcr.io/lstions/ehome:hotfix-20261002`，`.env` 用 `EHOME_IMAGE_TAG` 钉住。

> ⚠️ **这是临时措施**。正解是把 `jiabaida_control.go` 的修复推入仓库，
> 由 CI 产出正式镜像后把 `EHOME_IMAGE_TAG` 切回 `latest`/具体 sha。
> 在此之前，任何 `docker compose pull && up` 都会把镜像换回不含修复的 CI 版本。

---

## 四、验证清单（全部通过）

| # | 项 | 结果 |
|---|---|---|
| 1 | 三容器 healthy | postgres / emqx / web 全 healthy |
| 2 | 节点在线且 config applied | `30EDA0A9A808` online，`v2-e4f4ec49` applied |
| 3 | 数据完整性 | nodes=2 / edge_devices=3 / device_data=12174 / unified_data=24031 |
| 4 | 真实 BMS 数据流 | `error_code=0`，持续入库，数值实时变化 |
| 5 | 通道终端 | REST 200 + WS 实时推流（78 字节帧） |
| 6 | bind mount 生效 | `docker inspect` 显示 4 个 bind，无 named volume |
| 7 | `go build ./...` | 通过；`TestJiabaidaTemplatesUseIdleFraming` 通过 |

---

## 五、遗留与后续

1. **把代码修复推入仓库并走 CI** —— 让镜像内容与仓库一致（当前是本地 overlay）。
2. **通道终端当前为开启状态**（`EHOME_RAW_DIAGNOSTICS_ENABLED=true`）。
   这是**未审计 raw 读写**的降级开关，排障完应改回 `false`。
3. **加定时备份** —— 数据虽已落盘，但仍是单点。建议 cron 打包 `data/` 并保留 N 份。
4. **其余 JBD 命令**（0x04/0x05/0x0F/0xAA）已一并置 `read_length=0`，
   但只实测了 0x03 与 0x0F；启用其他轮询项前应确认响应确实被正确断帧。
5. **固件侧可选加固** —— 当前「短读即丢帧」对变长协议过于刚性；
   若后续接入更多变长协议设备，建议在固件层支持「空闲间隔优先」的混合定帧。
