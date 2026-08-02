# edge-control-worktree 分支 13 commit 分析报告

> 分析日期: 2026-07-30
> 分支: codex/edge-control-worktree (HEAD: f333110)
> merge-base: 9a20c63
> 实际链上共 31 个 commit（13 个指定里程碑 + 18 个中间 commit），本报告聚焦 13 个里程碑 commit。

## 1. 逐 commit 分析表

### 1. 85c872c — feat(control): unify edge actions and driver registry

| 维度 | 详情 |
|------|------|
| **文件/行数** | 116 文件, +7489 / -449 |
| **变更内容** | **后端Go**: 新建 `commandexec/` 全套（service/dispatcher/inbox/confirmation/channel/runtime_channel/state/channel_cmd_v2_transport），`deviceaction/`（definition/schema），`drivers/action.go`+`builtin.go`，`models/command_execution.go`+`command_confirmation.go`，`nodemgr/handler_channel_cmd_v2.go`，`pkg/frame/channel_cmd_v2.go`，`api/handler_device_operation.go`+`control_policy.go`，routes 重构。**前端Vue**: 新建 `deviceOperation.ts` API+store，`ActionForm.vue`/`ActionConfirmationDialog.vue`/`DeviceControlPanel.vue`。**ESP32固件**: 新建 `msg_handler/handler_channel_cmd_v2.c`，`host_tests/` 多个 C 测试，`legacy_write_guard.h`，`app_state.c` 修改。**文档**: 4 个协议/验证文档。**测试**: 后端 Go 测试 10+ 文件，前端 spec 1 个，ESP32 host_tests 5 个 |
| **性质** | 新功能 + 重构（核心架构提交） |
| **依赖** | 基于 merge-base 9a20c63，无前置依赖 |
| **风险** | ⚠️ **高风险** — 大规模架构变更，新建 116 文件，引入全新 commandexec 子系统。与 main 的现有设备操作路径完全不同。DB schema 变更（command_executions 表），需 migration。此 commit 是后续所有 commit 的基础 |

### 2. b61db70 — fix(control): close execution and firmware safety gaps

| 维度 | 详情 |
|------|------|
| **文件/行数** | 30 文件, +3439 / -147 |
| **变更内容** | **后端Go**: `commandexec/` 安全修复（service/confirmation/dispatcher/runtime_channel/channel_cmd_v2_transport），`models/command_execution.go` 状态机修复，`nodemgr/handler_channel_cmd_v2.go`+`handler_hello.go`，`pkg/frame/frame.go`。**ESP32固件**: `bus_worker.c` 大改（+87/-68），`frame_codec.c`，`hw_tables.c` 删除硬编码表，`handler_channel_cmd_v2.c` 安全加固。**前端Vue**: `deviceOperation.ts`+`DeviceControlPanel.vue` 调整。**配置**: `.env.prod.example`，`docker-compose.yml`。**文档**: 2 个大型设计文档（2081+1010 行） |
| **性质** | bug修复 + 安全加固 |
| **依赖** | 直接依赖 85c872c |
| **风险** | ⚠️ **中高风险** — ESP32 `hw_tables.c` 删除硬编码表（符合"硬件资源必须来自 ResourceReport"约定），`bus_worker.c` 大规模改写可能影响现有采集行为。文档中大量新内容 |

### 3. c12f981 — refactor(control): retire legacy frontend controls

| 维度 | 详情 |
|------|------|
| **文件/行数** | 8 文件, +19 / -359 |
| **变更内容** | **前端Vue**: 删除 `OperationButtons.vue`（246 行），精简 `DeviceHeader.vue`（-59 行），删除 `edgeDevice.ts` 中 27 行旧 API。**测试**: 更新相关 spec。**文档**: 禁门状态文档微调 |
| **性质** | 重构（删除遗留代码） |
| **依赖** | 直接父 c547b30（含 5 个中间 commit: 基线记录/通道刷新/能力目录刷新/交叉构建记录/C6 负载记录） |
| **风险** | 🟢 **低风险** — 纯删除遗留前端组件，不影响后端。但需确认没有其他页面引用 `OperationButtons.vue` |

### 4. 68163a3 — feat(control): add operational observability

| 维度 | 详情 |
|------|------|
| **文件/行数** | 19 文件, +410 / -28 |
| **变更内容** | **后端Go**: `handler_metrics.go` 扩展（+33），`audit/audit.go`+`audit_test.go`，`commandexec/` 各模块添加指标和审计日志（dispatcher/inbox/service/confirmation），`pkg/metrics/metrics.go` 新增指标定义。**前端Vue**: `monitor.ts` API 新增，`Monitor.vue` 大幅扩展（+90 行），`Monitor.spec.ts` 新增 |
| **性质** | 新功能（可观测性） |
| **依赖** | 直接父 35bfba5（含 5 个中间 commit: 命令重放/registry 对齐/设备默认值/UNKNOWN 解决）。间接依赖 85c872c+b61db70 |
| **风险** | 🟢 **低风险** — 增量添加监控指标，不改现有行为。审计日志写入可能增加 DB 负载 |

### 5. 875a1d8 — fix(control): gate high-risk actions and add reauthentication

| 维度 | 详情 |
|------|------|
| **文件/行数** | 19 文件, +340 / -44 |
| **变更内容** | **后端Go**: `envelope.go` 新增 `ErrorWithCode()` + `ErrorCode` 字段，`handler_account.go` 新增 reauthentication 端点（POST /account/reauthenticate）+ 登录限流器（5 次/15 分钟），`routes.go` 路由注册，`deviceaction/definition.go` 高风险动作标记。**前端Vue**: `client.ts` 添加 `error_code` 处理，`auth.ts` 新增 reauth API，`DeviceControlPanel.vue` 高风险动作确认流程 |
| **性质** | 安全加固 |
| **依赖** | 直接父 47ce7ef（序列化 outbox 租约）。间接依赖 85c872c~68163a3 全链 |
| **风险** | ⚠️ **中风险** — 引入登录限流器需要 Redis 依赖。reauthentication 端点改变前端交互流程。`envelope.go` 结构变更（新增 ErrorCode 字段）可能影响前端解析 |

### 6. 515d7bb — fix(auth): keep login errors on the login page

| 维度 | 详情 |
|------|------|
| **文件/行数** | 4 文件, +46 / -10 |
| **变更内容** | **前端Vue**: `client.ts` 修复 401 拦截器逻辑 — 区分登录请求 401（保留内联错误）和会话过期 401（跳转登录页），避免登录页的密码错误导致页面空白。**测试**: e2e 和 unit spec 更新 |
| **性质** | bug修复 |
| **依赖** | 直接依赖 875a1d8（复用其 `error_code` 字段） |
| **风险** | 🟢 **低风险** — 纯前端修复，修正已有 bug（401 处理过于激进） |

### 7. fea1c71 — feat(auth): add host-local password reset

| 维度 | 详情 |
|------|------|
| **文件/行数** | 4 文件, +98 / -2 |
| **变更内容** | **后端Go**: `auth/account.go` 新增 `ResetPasswordHostLocal()` — 通过环境变量接收密码（非 HTTP/CLI 参数），原子撤销所有会话，写入审计证据。`ehomectl/main.go` 添加 CLI 子命令。**前端Vue**: `LoginForm.vue` 微调。**测试**: `account_test.go` 新增 36 行测试 |
| **性质** | 新功能（运维恢复路径） |
| **依赖** | 直接依赖 515d7bb |
| **风险** | 🟢 **低风险** — 密码通过环境变量传递（安全），有审计日志，不暴露 HTTP 端点。设计符合 fail-closed 原则 |

### 8. b2b3be0 — test(control): verify SN-3001 through the frontend

| 维度 | 详情 |
|------|------|
| **文件/行数** | 6 文件, +186 / -58 |
| **变更内容** | **前端Vue**: 新建 `e2e/sn3001-real.spec.ts`（62 行 Playwright E2E 测试，需实机环境），`CommandList.vue` 重构精简（-77/+19）。**测试**: 新建 `CommandListControlBoundary.spec.ts`（72 行）。**文档**: 禁门状态文档更新 |
| **性质** | 测试补全 + 小重构 |
| **依赖** | 直接依赖 fea1c71 |
| **风险** | 🟢 **低风险** — E2E 测试有 skip 守卫（无密码时跳过）。`CommandList.vue` 重构需确认无回归 |

### 9. 564bec6 — docs: 全面更新文档

| 维度 | 详情 |
|------|------|
| **文件/行数** | 34 文件, +4062 / -1258 |
| **变更内容** | **文档**: 归档 20 个 v2.2 已实现/被取代文档到 `archive/`，更新 README/术语表/概念模型/实现文档至 v2.6。新增设计文档（多总线事件驱动方案/分配方案/分析总结）。**代码**: 无功能代码变更 |
| **性质** | 文档 |
| **依赖** | 直接父 124e82e（含 7 个 SN-3001 中间 commit: 浏览器证据/雨量复位/BMS 工作流/波特率同步/地址持久化等） |
| **风险** | 🟢 **无风险** — 纯文档变更，不影响代码行为 |

### 10. 228110b — docs: 多总线事件驱动方案实现进展

| 维度 | 详情 |
|------|------|
| **文件/行数** | 1 文件, +407 / -0 |
| **变更内容** | **文档**: 新建实现进展文档，逐项分析 10 项需求（R1-R10）的源码完成度（整体 47%），制定 8 阶段推进计划（A-H） |
| **性质** | 文档 |
| **依赖** | 直接依赖 564bec6 |
| **风险** | 🟢 **无风险** — 纯文档 |

### 11. dd97e25 — fix: 补全错误处理与边界检查

| 维度 | 详情 |
|------|------|
| **文件/行数** | 2 文件, +7 / -2 |
| **变更内容** | **后端Go**: `handler_device_operation.go` — DB 查询区分 404/500，防止 DB 异常时零值设备继续执行（fail-closed）。**ESP32固件**: `bus_worker.c` — `execute_uart_batch` 入口增加 `ch_idx` 边界检查，防止 `s_plan_active[]` 越界写入 |
| **性质** | bug修复 + 安全加固 |
| **依赖** | 直接依赖 228110b |
| **风险** | 🟢 **低风险** — 2 处小改动，均为防御性检查。符合 fail-closed 原则 |

### 12. d58b5b7 — test: 补全高风险未覆盖模块的单元测试与组件测试

| 维度 | 详情 |
|------|------|
| **文件/行数** | 9 文件, +1801 / -0 |
| **变更内容** | **后端Go测试**: `state_test.go`（36 组状态转换），`confirmation_test.go`（20 例令牌/速率限制/重放），`inbox_test.go`（17 例状态转换/幂等/过期恢复），`dispatcher_test.go`（9 例租约/通道竞争/取消），`handler_device_operation_test.go`（2 例 404 分支）。**ESP32 C测试**: `frame_codec_boundary_tests.c`（21 例边界拒绝）。**前端Vue测试**: `ActionConfirmationDialog.spec.ts`（9 例，覆盖 94.7%），`ActionForm.spec.ts`（12 例，覆盖 93.0%） |
| **性质** | 测试补全 |
| **依赖** | 直接依赖 dd97e25 |
| **风险** | 🟢 **无风险** — 纯新增测试文件，不改功能代码 |

### 13. f333110 — fix(esp32): 解析 manifest field 8 (dma_enabled) 修复 DMA 偏好被忽略

| 维度 | 详情 |
|------|------|
| **文件/行数** | 3 文件, +16 / -7 |
| **变更内容** | **ESP32固件**: `config_mgr.h` 新增 `bool dma_enabled` 字段，`config_mgr.c` `parse_channel_fields` 新增 `case 8` 解析 varint bool（默认 true 向后兼容），`bus_manager.c` `reg_bus_channel` 接受显式 `dma_enabled` 参数替代从 flags 字节推断。**根因**: 后端编码 field 8 但 ESP32 未解析，导致 `dma_enabled=false` 被忽略，DMA 初始化失败导致整个 ConfigManifest 事务被拒绝 |
| **性质** | bug修复（编解码不对称，已知 bug 类） |
| **依赖** | 直接依赖 d58b5b7 |
| **风险** | 🟢 **低风险** — 3 处小改动，向后兼容（默认 true）。修复 fail-open→fail-closed。实机验证通过 |

---

## 2. 按类别分组

### 新功能 (4 个)
| Commit | 描述 | 规模 |
|--------|------|------|
| 85c872c | 统一边缘设备控制+驱动注册 | 116 文件, +7489/-449 |
| 68163a3 | 操作可观测性（指标+审计） | 19 文件, +410/-28 |
| fea1c71 | 主机本地密码重置 | 4 文件, +98/-2 |
| b2b3be0 | SN-3001 前端验证测试 | 6 文件, +186/-58 |

### Bug修复 (4 个)
| Commit | 描述 | 规模 |
|--------|------|------|
| b61db70 | 执行和固件安全缺口修复 | 30 文件, +3439/-147 |
| 515d7bb | 登录页 401 错误保留 | 4 文件, +46/-10 |
| dd97e25 | 错误处理与边界检查 | 2 文件, +7/-2 |
| f333110 | ESP32 DMA 偏好被忽略 | 3 文件, +16/-7 |

### 安全加固 (3 个，与 bug 修复有交叉)
| Commit | 描述 | 关键安全点 |
|--------|------|------------|
| b61db70 | 固件安全缺口 | hw_tables 删除硬编码, bus_worker 加固 |
| 875a1d8 | 高风险动作门控+重新认证 | 登录限流, reauth, ErrorCode |
| dd97e25 | 边界检查 | fail-closed, 越界防护 |

### 测试补全 (2 个)
| Commit | 描述 | 规模 |
|--------|------|------|
| b2b3be0 | SN-3001 前端验证 | 6 文件, +186/-58 |
| d58b5b7 | 高风险模块单元/组件测试 | 9 文件, +1801/-0 |

### 文档 (2 个)
| Commit | 描述 | 规模 |
|--------|------|------|
| 564bec6 | 全面更新文档至 v2.6 | 34 文件, +4062/-1258 |
| 228110b | 多总线事件驱动进展 | 1 文件, +407/-0 |

### 重构 (1 个)
| Commit | 描述 | 规模 |
|--------|------|------|
| c12f981 | 退役遗留前端控件 | 8 文件, +19/-359 |

---

## 3. 依赖链分析

### 线性依赖链（所有 31 个 commit 为单一线性链）

```
9a20c63 (merge-base)
  │
  ├─ 85c872c ────────────── 核心架构 [必须]
  │    │
  ├─ b61db70 ─────────────── 安全修复 [必须, 依赖 85c872c]
  │    │
  │    └─ [5 中间 commit: 基线/通道刷新/能力目录/交叉构建/C6 负载]
  │       │
  ├─ c12f981 ─────────────── 遗留前端清理 [可选, 依赖 b61db70 链]
  │    │
  │    └─ [5 中间 commit: 命令重放/registry 对齐/设备默认值/UNKNOWN 解决]
  │       │
  ├─ 68163a3 ─────────────── 可观测性 [推荐, 依赖 c12f981 链]
  │    │
  │    └─ [1 中间 commit: outbox 租约序列化]
  │       │
  ├─ 875a1d8 ─────────────── 安全加固 [推荐, 依赖 68163a3 链]
  │    │
  ├─ 515d7bb ─────────────── 登录 bug 修复 [推荐, 依赖 875a1d8]
  │    │
  ├─ fea1c71 ─────────────── 密码重置 [可选, 依赖 515d7bb]
  │    │
  ├─ b2b3be0 ─────────────── SN-3001 测试 [可选, 依赖 fea1c71]
  │    │
  │    └─ [7 中间 commit: SN-3001 浏览器证据/雨量复位/BMS/波特率/地址持久化]
  │       │
  ├─ 564bec6 ─────────────── 文档更新 [可独立, 依赖 b2b3be0 链]
  │    │
  ├─ 228110b ─────────────── 文档进展 [可独立, 依赖 564bec6]
  │    │
  ├─ dd97e25 ─────────────── 边界修复 [推荐, 依赖 228110b]
  │    │
  ├─ d58b5b7 ─────────────── 测试补全 [推荐, 依赖 dd97e25]
  │    │
  └─ f333110 ─────────────── ESP32 DMA 修复 [必须, 依赖 d58b5b7]
```

### 必须一起合入的分组

由于这是一条**线性无分叉的 commit 链**（31 个 commit 单线串联），技术上无法 cherry-pick 单个 commit 而跳过中间 commit。实际合入选项只有两个：

1. **整体合入**（推荐）：`git merge f333110` 或 `git rebase 9a20c63..f333110` — 合入全部 31 个 commit
2. **分段 squash 合入**：按功能边界分组 squash 后合入，但需手动处理

### 逻辑分组（若分段合入）

| 组 | Commit 范围 | 包含中间 commit | 建议 |
|----|------------|----------------|------|
| **A: 核心控制架构** | 85c872c + b61db70 + 5中间 | 7 commit | 必须一起合入 |
| **B: 前端清理+可观测性** | c12f981~68163a3 + 6中间 | 8 commit | 推荐一起合入 |
| **C: 安全加固链** | 875a1d8~515d7bb | 2 commit | 推荐一起合入 |
| **D: 认证增强** | fea1c71 + b2b3be0 + 7中间 | 9 commit | 可独立合入（含 SN-3001 硬件验证） |
| **E: 文档** | 564bec6 + 228110b | 2 commit | 可独立合入 |
| **F: 健壮性收尾** | dd97e25 + d58b5b7 + f333110 | 3 commit | 推荐一起合入 |

---

## 4. 合入建议

### ✅ 推荐合入 main

| Commit | 理由 |
|--------|------|
| **85c872c** | 核心架构，后续所有功能依赖此基础。commandexec 子系统设计合理 |
| **b61db70** | 安全修复关键，删除 ESP32 硬编码表（符合项目约定），修复 fail-open 问题 |
| **875a1d8** | 安全加固，登录限流+reauthentication 是必要的安全控制 |
| **515d7bb** | 简单 bug 修复，修正 401 拦截器过度跳转 |
| **dd97e25** | fail-closed 防御性修复，2 行改动，零副作用 |
| **d58b5b7** | 纯测试文件，提升覆盖率，1801 行测试代码 |
| **f333110** | 修复已知 bug 类（编解码不对称），3 文件 16 行，实机验证通过 |

### 🟡 推荐合入但需注意

| Commit | 注意事项 |
|--------|----------|
| **68163a3** | 监控指标增加 DB 写入负载，需评估 audit 表增长 |
| **fea1c71** | 密码重置通过环境变量传递（安全），但需确认 ehomectl 部署流程 |
| **c12f981** | 删除 OperationButtons.vue 前需确认 main 分支无其他引用 |

### 🟢 可独立合入（低风险）

| Commit | 理由 |
|--------|------|
| **564bec6** | 纯文档，4062 行文档更新，不影响代码 |
| **228110b** | 纯文档，407 行进展报告 |
| **b2b3be0** | E2E 测试有 skip 守卫，不影响 CI |

### ⚠️ 有风险的合入点

1. **85c872c 是最大风险点**：116 文件 +7489 行，引入全新子系统（commandexec/deviceaction），改变 DB schema（command_executions 表），需要：
   - DB migration 脚本
   - 确认 main 分支现有设备操作 API 不冲突
   - 全量回归测试

2. **b61db70 的 ESP32 变更**：`bus_worker.c` 大幅改写（+87/-68），`hw_tables.c` 删除硬编码表，可能影响现有固件行为，需实机回归

3. **875a1d8 的 envelope 结构变更**：新增 `ErrorCode` 字段到 API 响应，前端需同步更新解析逻辑（已在同 commit 中处理）

4. **整体 31 commit 链的中间 commit**：18 个中间 commit 包含 SN-3001 硬件验证系列（7 个 commit），这些依赖特定硬件环境（C6/SN-3001），无法在 CI 中完全覆盖

### 📋 总结建议

| 方案 | 风险 | 工作量 | 推荐度 |
|------|------|--------|--------|
| 整体 merge 31 commit | 中（大范围变更，但测试覆盖好） | 低（一次操作） | ⭐⭐⭐ |
| 分段 squash 后合入 | 低（可逐步验证） | 高（需手动分组处理冲突） | ⭐⭐ |
| 只合入核心+安全+修复（跳过文档/测试） | 高（线性链无法跳过中间 commit） | 极高（需 rebase 手术） | ❌ |

**最终建议**：整体 `git merge f333110` 合入。理由：
- 31 个 commit 是严格线性链，cherry-pick 子集需手动解决依赖
- 测试覆盖充分（d58b5b7 补全了高风险模块，整体 204 文件 +21015/-2237）
- 文档同步更新（564bec6+228110b）
- 末端 dd97e25/f333110 是 fail-closed 修复，价值高
- 合入后需运行全量测试 + 实机验证 ESP32 固件
