# UI/UX 审计取证脚本（frontend-shared/tools/）

> **状态**: 历史取证留档（2026-09-13 UI/UX 全站审计的产物）
> **关联**: [UIUX审计契约](../../../docs/分析/UIUX审计契约-2026-09-13.md) |
> [合并结论与优化方案](../../../docs/分析/UIUX审计合并结论与优化方案-2026-09-13.md)

## 为什么这些脚本要入库

审计报告把「复跑某脚本」写成**验收方法**（例如 D3 报告 §"验收方法"直接引用
`uiux-d3-probe15.mjs` 的对比度实现）。若脚本不入库，报告的验收方法就成了**悬空引用**——
这正是本轮已经踩过并修掉的坑（`.logs/` 下的取证清单不在版本控制内）。

因此：**脚本与它支撑的报告必须一起入库**，否则结论无法复核。

## 两类脚本

| 类别 | 文件 | 用途 |
|---|---|---|
| **可复用工具** | `uiux-audit.mjs` | 三合一取证主探针：一次遍历 页面×视口×主题，产出 `dom-facts.json` + 逐张 PNG。**新审计优先用它。** |
| | `uiux-d1-contrast.mjs` | WCAG 对比度计算（多元素批量），验收对比度修复时复用 |
| | `uiux-d4-menucheck.mjs` | 键盘 Tab 遍历与焦点落点检查 |
| **定向复现** | `uiux-d{1,2,3}-probe*.mjs`、`uiux-d2-p*.mjs`、`uiux-d4-probe*.mjs` | 每个脚本针对**一个具体问题**的最小复现（如"某接口 500 后页面渲染什么"）。**它们不是重复品**——差异可达数十行，各自注入不同的 `page.route` 或度量不同的选择器 |

## 使用前提

这些脚本都需要一个**托管前端构建产物 + 真实数据**的后端实例：

```bash
export PATH=/home/sun/.local/share/pnpm/bin:$PATH HOME=/home/sun
cd /home/sun/workspace/EHomeSystem/frontend-shared

# 主探针（推荐）
UIUX_BASE=http://127.0.0.1:8082 UIUX_OUT=/tmp/uiux-out \
UIUX_ROUTES=dashboard,node-list UIUX_VIEWPORTS=desktop-1440,mobile-390 UIUX_THEMES=light,dark \
  node tools/uiux-audit.mjs
```

环境变量：`UIUX_BASE`（后端地址）、`UIUX_OUT`（证据输出目录）、
`UIUX_USER`/`UIUX_PASS`（登录凭据，默认 admin/UiuxAudit2026!）、
`UIUX_ROUTES`/`UIUX_VIEWPORTS`/`UIUX_THEMES`（采样矩阵）。

## 已沉淀为 CI 门禁的部分

审计断言中**可机械判定**的已沉淀为 `../e2e/uiux-regression.spec.ts`（规范 §7.6：
只有可自动判断、影响面明确且能防真实回归的规则才入门禁）。本目录脚本用于**一次性深度取证**，
不参与 CI。

## 四个必须记住的探针盲区

审计期间探针先后犯过**五个**错误（详见契约 §2），复用时务必避免：

1. **截图会骗人**：node-list mobile-390「设备ID被切」实为**视口高度**截断
   （DOM 实测 `right=341 < viewport 390`）
2. **可滚动祖先不是裁切**：EP 表格 wrapper `overflow:hidden` + `scrollWidth>clientWidth` 是**正常横滚**
3. **假阴性**：`clickableNotFocusable` 只扫按钮类选择器 → 恒为 0，实为 208 个不可聚焦
4. **假阳性回归**：修完假阴性后又把原生按钮的**后代**（`button` 内的 `svg/span`）计入，
   虚高 392 → 必须用 `el.closest('button, a[href], input, ...')` 过滤
5. **空态/坏图选择器**：本项目主用自研 `.empty-state`（33 处）而非 `.el-empty`（2 处）；
   `imgsBroken=0` 需配 `imgsTotal` 分母才能解读

> **统一纪律**：**每一个计数型字段都必须能回答"分母是多少"**。
> 无法回答分母的 0 一律不得作为"合规"证据。
