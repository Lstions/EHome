# 主题 / 触控 / 布局回归探针（frontend-shared/tools/theme/）

> **状态**: 取证留档（2026-09-14 第九/十轮 UI/UX 修复的验证脚本）
> **关联**: [未发布产品负债盘点-2026-09-13.md](../../../docs/分析/未发布产品负债盘点-2026-09-13.md) |
> [UIUX审计契约-2026-09-13.md](../../../docs/分析/UIUX审计契约-2026-09-13.md)

## 为什么这些脚本要入库

盘点文档把「复跑某脚本」写成**验收方法**。若脚本不入库，验收方法就成了**悬空引用** ——
这正是本仓已踩过并修掉的坑（`.logs/` 下的取证清单不在版本控制内，见
`docs/分析/P2修复交付验证记录-2026-09-13.md` 文件头）。

因此：**脚本与它支撑的报告必须一起入库**，否则结论无法复核。这与
`tools/README.md` 的既有约定一致。

## 脚本

| 文件 | 支撑的结论 | 复跑 |
|---|---|---|
| `f16-contrast-ab.mjs` | F20 侧栏「当前页」对比度 2.14:1 → 5.65/6.30:1（注入式 A/B + 真实像素采样） | `node tools/theme/f16-contrast-ab.mjs` |
| `f16-e2e-final.mjs` | F16 四条判据在**真实产物**上的端到端实测（侧栏渐变/边框/文字/活动项亮暗不同） | `node tools/theme/f16-e2e-final.mjs` |
| `f26-occlusion-scan.mjs` | F26 单页遮挡：48 点 `elementFromPoint` 网格，命中率 0/48 → 48/48 | `node tools/theme/f26-occlusion-scan.mjs` |
| `f26-occlusion-fullscan.mjs` | F26 全站扫描：逐路由统计「非固定列控件被遮挡」数（用于界定影响面） | `node tools/theme/f26-occlusion-fullscan.mjs` |
| `f6-density-verify.mjs` | F6 桌面密度守恒：@1440 行高 40px、单元格 padding 1px、按钮 36x36 | `node tools/theme/f6-density-verify.mjs` |
| `f10-breadcrumb-verify.mjs` | F10 移动端面包屑可见性与横向溢出（390px） | `node tools/theme/f10-breadcrumb-verify.mjs` |

## 前置条件

- 审计实例在 `http://127.0.0.1:8082` 运行（凭据见 `docs/分析/UIUX审计契约-2026-09-13.md` §登录）。
- 脚本以 `@playwright/test` 的 chromium 驱动（`executablePath: /snap/bin/chromium`），
  须在 `frontend-shared` 包内运行（该依赖只在此包内可解析）：

      cd frontend-shared
      export PATH=/snap/bin:/home/sun/.local/share/pnpm/bin:$PATH HOME=/home/sun
      node tools/theme/<script>.mjs

## 注意

- 这些脚本**只读**：全部走登录 + 读取 DOM/计算样式，不发出任何写请求。
- 受测产物是 `frontend-shared/dist`，由 8082 同源托管。
  **测到的行为取决于当下 dist 的内容** —— 若源码已改但未 `pnpm build`，脚本测的是旧产物。
  部分脚本（`f16-contrast-ab`）用 `addStyleTag` 注入等价改动，因此**不依赖构建**；
  `f16-e2e-final` 则要求 dist 已含修复。
