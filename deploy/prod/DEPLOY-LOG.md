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

## 注意

- 固件修复（sync_manager 整轮看门狗）已进代码库，但现场节点仍运行 fw 2.8.0，需 OTA 才生效；
  在后端 reconcileStuckSync 兜底到位的前提下，可等下一次常规固件发布。
- 生产 ehome-web 必须单副本：自动化求值器状态全在进程内存（见 deploy/prod/TOPOLOGY.md §7）。
