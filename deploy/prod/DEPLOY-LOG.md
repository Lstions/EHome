# 生产部署记录（回滚锚点）

> 生产：192.168.20.6 · compose /srv/ehomesystem/docker-compose.yml · 容器 ehome-prod-web · 入口 :18090
> 镜像：ghcr.io/lstions/ehome（公开包，生产匿名拉取，无需 docker login）
> CI：.github/workflows/docker-publish.yml 在 push 到 main 时构建并推送 :latest、:main、:sha-<短sha>（7 位）。
> 回滚判据：/health 的 commit 必须等于部署目标的 git sha。

| 部署时间 (CST) | 代码 (git sha) | 镜像 digest | 内容 | 验证 |
|---|---|---|---|---|
| 2026-11-17 21:43 | 9f77db7b | sha256:d4a098e9dadf… | 上一版（BMS 受控操作回执前） | — |
| 2026-11-18 11:36 | 52b06798 | sha256:5f9786143a11… | 四轮修复（能力快照自愈/确认令牌/V7 收口/同步卡死重推/窗口不补发）+ 债务收敛（预测同源/冷却回灌/partial 生产者/告警/时区根治）+ 前端时区选择器 + 固件看门狗 | /health commit 一致；next_run 正常；BMS 手动触发 executed，命令 SUCCEEDED/final_succeeded，回读 mos_ack=1；日志 automation: 节点能力快照已刷新 node_id=F0F5BD02DD84；ehome_automation_events_total{result="executed"} 1 |
| 2026-11-18 12:13 | 1e22c47c | sha256:6806836b3c4f… | 仅仿真夹具/场景修正（补时区、对齐不补发、确认场景改用真实登录）。build tag simulation ⇒ **运行时与 52b06798 完全相同** | /health commit=1e22c47c；两条规则 next_run 正常（日落 17:41 / 日出次日 06:42） |
| 2026-11-18 12:56 | ee903d0f | sha256:ea277d70c8d9… | 删除规则级风险授权（L4）：保存前改为一次确认弹窗、system 路径直接执行风险动作、动作目录只发 risk_level；DB 列 risk_acknowledged_at 当时**保留一版**（已于同日晚删除，见下方回滚说明） | 生产实测：风险规则**不带授权字段**创建 200（旧码 400）、改参数 200（旧码 400 死循环）、precheck 清单 1 条、catalog 无 risk 字段、列仍在且新行 NULL；/health 一致、10 分钟 0 错误 |
| 2026-11-18 13:48 | a67adf88 | sha256:3590ef660096… | 启动迁移幂等删除 automation_rules.risk_acknowledged_at（旧代码零引用；删时有 2 行非空，语义已废除） | /health commit 一致；日志 dropped dead column（had 2 non-null rows）；schema 列数 0；2 条规则与名称完整保留；API 无该字段、next_run 正常 |
| 2026-11-19 16:22 | 9628b39 | （失败，已回滚） | DMA bind_to 修复（前端改为发设备规范大小写）—— ⚠ 新增启动迁移对 jsonb 列做文本比较，服务 CrashLoop | **失败**：`migrate_dma_bind_to.go:68 invalid input syntax for type json (SQLSTATE 22P02)` → `FATAL Failed to migrate database` → 重启循环；约 40 秒后回滚到 `b262f744`（`docker tag rollback-20261119-082245`），/health 一致、healthy |
| 2026-11-19 16:30 | 2913ac19 | sha256:a2a824f4fcdc… | 同上 + 迁移改为「WHERE 只用 id/IS NOT NULL，dma_configs 过滤挪 Go 侧」，且启动失败只 WARN 不阻断；DMA 写入校验/迁移共用 `internal/hwid` | /health commit=2913ac19；容器 healthy；启动日志无 FATAL/panic；前端 `NodeOverview` chunk 内 `resourceBindingKey` 已为 `` return`${e}/${t.id}` ``（旧包为 `` …`.toLowerCase()` ``）；`PUT bind_to=uart/UART1` → 200（sent），`bind_to=uart/uart1` → 400 且提示正确写法；节点 config 仍为 `uart/UART1` |

### 2026-11-19 说明（DMA 打不开）

用户报障：总线配置页任何 DMA 开关/选择器都 400 ——
`configs[0].bind_to "uart/uart1" does not match any hardware id … did you mean "uart/UART1"?`。
根因是前端 `resourceBindingKey()` 做了 `.toLowerCase()`（设备端 `strcmp` 大小写敏感）。
详见 `docs/设计/3.0-实施设计-骨架与增量-2026-10-06.md` §261。

⚠ 首次发布 `9628b39` 因启动迁移对 **jsonb** 列做文本比较而 CrashLoop，已回滚；
`2913ac19` 修复后重新部署成功。回滚锚点 `ghcr.io/lstions/ehome:rollback-20261119-082245`
（= b262f744）仍保留在服务器上。**教训已固化**：数据修复类启动迁移失败只告警，
且迁移 SQL 必须在真实 PG 上跑一次（`EHOME_TEST_DB=postgres`）。

## 回滚

镜像保留 :sha-<短sha>；生产 compose 固定 :latest，因此回滚需要先拉旧 sha、再打回 latest：

```bash
cd /srv/ehomesystem
docker pull ghcr.io/lstions/ehome:sha-9f77db7        # 9f77db7b 对应的 CI 产物（已确认存在）
docker tag  ghcr.io/lstions/ehome:sha-9f77db7 ghcr.io/lstions/ehome:latest
docker compose up -d ehome                            # 用本地 latest，不要再 pull
curl -s http://127.0.0.1:18090/health                 # commit 必须回到 9f77db7b
```

本次变更没有数据库结构迁移（next_run 是 gorm:"-"），回滚不涉及库。
唯一的启动期数据迁移是 MigrateTimeWindowTimezone：只回填 time_window 且时区为空的存量行，
生产当前没有 time_window 规则，实际影响 0 行。

> **2026-11-18 13:40 起（ee903d0f 之后）**：automation_rules.risk_acknowledged_at 已由启动迁移
> MigrateAutomationRuleDropRiskAcknowledged 删除（当时生产上该列有 **2 行非空** —— 两条 BMS 规则
> 的旧授权时间戳；该语义已废除、执行路径不再读取，删除不丢业务数据）。
> **回滚到 ee903d0f 及更早的镜像不再可行** —— 旧代码 SELECT 该列会报 unknown column，
> 本发布之后只能前进修复。

## 注意

- 固件修复（sync_manager 整轮看门狗）已进代码库，但现场节点仍运行 fw 2.8.0，需 OTA 才生效；
  在后端 reconcileStuckSync 兜底到位的前提下，可等下一次常规固件发布。
- 生产 ehome-web 必须单副本：自动化求值器状态全在进程内存（见 deploy/prod/TOPOLOGY.md §7）。
