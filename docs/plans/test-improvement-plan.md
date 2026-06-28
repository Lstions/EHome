# EHomeSystem 测试完善计划

## 目标：前后端整体测试完善度 ≥ 8.5/10

## P0 — 阻断修复 (让现有测试全部可运行)

### P0-1 Go 后端编译/断言修复
- [x] `internal/models/compat_test.go` — int→string 类型修复
- [x] `internal/ota/ota_test.go` — NodeID int→string 修复
- [x] `internal/offlinedetector/offlinedetector_test.go` — NodeID int→string 修复
- [x] `internal/nodemgr/datapipe_test.go` — C6IndexFallback 断言修复
- [x] `scripts/migrate_v21_to_v22/main.go` — fmt.Println 冗余换行

### P0-2 前端 Vitest 修复
- [x] `stores/theme.spec.ts` — setTheme 副作用修复
- [x] `directives/permission.spec.ts` — updated 钩子修复
- [x] `api/client.spec.ts` — unhandled rejection 修复
- [x] 安装 `@vitest/coverage-v8`

### P0-3 Playwright E2E 接入
- [x] `package.json` 添加 e2e scripts
- [x] `Makefile` 添加 `make e2e` target
- [x] `playwright.config.ts` 验证

## P1 — 单元测试补齐 (零覆盖包 → 有覆盖)

### P1-1 后端 Go 单元测试 (12个零覆盖包)
优先级排序：
1. `internal/events` — 纯常量，验证一致性
2. `internal/config` — 配置加载、env覆盖、默认值
3. `internal/worker` — Worker pool 生命周期、Job 提交
4. `internal/websocket` — Hub 注册/广播/消息处理
5. `internal/mqtt` — Client 消息路由、Subscribe/Publish
6. `internal/database` — 连接配置（用 SQLite mock）
7. `internal/redis` — 心跳/在线检测（用 miniredis 或 mock）
8. `pkg/logger` — Init/级别过滤
9. `pkg/metrics` — Prometheus 注册器验证
10. `internal/deviceinit` — 初始化序列、sendAndWait
11. `internal/seed` — 种子数据幂等性
12. `cmd/server` — 不直接测试（通过 integration 覆盖）

### P1-2 前端 Vue 组件测试 (5个高优先组件)
1. `components/common/ThemeSwitch.vue` — 主题切换按钮
2. `components/forms/LoginForm.vue` — 登录表单验证
3. `components/common/StatusBadge.vue` — 状态徽章渲染
4. `components/common/EmptyState.vue` — 空状态
5. `components/common/ConfirmDialog.vue` — 确认对话框

### P1-3 前端 Store/API 测试增强
1. `stores/channel.spec.ts` — Channel store CRUD
2. `stores/node.spec.ts` — Node store CRUD
3. `stores/dma.spec.ts` — DMA 通道 toggle/乐观更新
4. `stores/websocket.spec.ts` — WS 连接/重连/心跳

## P2 — 集成测试 + CI/CD

### P2-1 后端 API 集成测试
- `internal/api` 覆盖率从 1.5% → 40%+
- 使用 SQLite 内存 DB + httptest
- 覆盖 auth, nodes, edge-devices, channels, data 端点

### P2-2 SQLite 兼容性修复
- 修复 `manager.go` 中 `SET TRANSACTION ISOLATION LEVEL` 在 SQLite 下报错
- 改为条件执行或移除（SQLite 默认 SERIALIZABLE）

### P2-3 CI/CD 配置
- `.github/workflows/test.yml`
- 后端: `go test ./... -race -cover`
- 前端: `pnpm test:run` + `pnpm test:coverage`
- 覆盖率阈值门禁

## 验证标准
- `go test ./...` 全部 PASS，0 FAIL
- `pnpm test:run` 全部 PASS，0 FAIL
- 后端覆盖率 ≥ 45%（整体）
- 前端覆盖率 ≥ 35%（整体）
- `npx playwright test --list` 能列出所有 E2E 测试
- CI 配置文件存在且语法正确
