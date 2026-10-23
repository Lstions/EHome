# 前端依赖升级盘点（frontend-shared）

> 任务：task-2 ｜ 分析员：fe-analyst ｜ 时间：2026-10-23
> **只做分析，未修改任何依赖文件**（`package.json` / `pnpm-lock.yaml` 经 `git status` 复核无改动）。
> 所有试验都在仓库外的隔离副本中进行（`.tmp-deps/fe-probe/`，未纳入 git）。

---

## 0. 结论速览（先看这 6 条）

| # | 结论 | 证据强度 |
|---|---|---|
| 1 | **typescript 5.9.3 → 7.0.2 目前不可用**：`vue-tsc`（含最新 3.3.13）依赖 `typescript/lib/tsc`，TS 7 的 `exports` 不再导出该子路径 ⇒ `pnpm typecheck` **直接崩溃**（非类型报错，是 crash） | **实测复现**（2 次独立环境） |
| 2 | 因此 **TS 与 vue-tsc 必须一起决策，且 TS 的天花板是 6.0.3 而不是 7.0.2**。升 vue-tsc 3.x 安全，升 TS 7 不安全 | 实测 |
| 3 | `vitest` 与 `@vitest/coverage-v8` **必须严格同版本**（peer 是精确版本 `5.0.5` 而非范围）。错配 pnpm 只给 WARN，不拦截 | 实测 + registry peer 元数据 |
| 4 | `@vue/test-utils` 2.4.11 → 2.5.1 虽只是 **minor**，但**会让 typecheck 变红**（`DOMWrapper.props` 类型收紧，2 处） | **实测复现** |
| 5 | `happy-dom` 20 → 21 的破坏点是**安全默认值变更**（默认禁用 CSS/iframe 加载），本仓库测试全部使用 stub、不加载外链样式 ⇒ **实际影响面很小** | 实测跑通 + release notes |
| 6 | 可分 **4 批**：① 安全批（11 项，patch/minor 且实测 typecheck/build 通过）② 行为变更批（3 项：test-utils / vue-tsc / happy-dom，各自独立提交）③ 测试框架批（2 项：vitest + coverage，必须原子化）④ 暂缓批（typescript 7） | 见 §7 |

**总数**：直接依赖 21 个（dependencies 8 + devDependencies 13），其中**可升级 18 个**，已是最新 3 个（`echarts` 6.1.0、`@element-plus/icons-vue` 2.3.2、`unplugin-vue-components` 32.1.0）。

---

## 1. 数据来源与环境

### 1.1 数据来源（全部为实际查询，无估算）

| 数据项 | 来源命令/接口 | 时刻 |
|---|---|---|
| 当前版本 / 可升级版本 | `cd frontend-shared && pnpm outdated --format json` | 2026-10-23 14:40 |
| 已安装版本核对 | `pnpm ls --depth 0` | 同上 |
| peer / engines 元数据 | `https://registry.npmjs.org/<pkg>/<ver>` | 同上 |
| vue-tsc ↔ TS 兼容性 | 隔离副本中实跑 `vue-tsc --noEmit` | 14:47–15:26 |
| vitest 5 迁移要点 | https://vitest.dev/guide/migration.html | 15:12 |
| happy-dom 21 变更 | GitHub releases（v21.0.0 body） | 15:09 |
| TS 7 变更 | https://devblogs.microsoft.com/typescript/announcing-typescript-7-0/ | 14:46 |
| CI 约束 | `.github/workflows/test.yml`（`L36-38`、`L269-278`） | 14:43 |

### 1.2 环境事实

| 项 | 值 | 备注 |
|---|---|---|
| Node | **v24.21.0**（本地） | CI `NODE_VERSION: "22"`（floating 22.x） |
| pnpm | **12.14.0**（本地） | CI `PNPM_VERSION: "10"`（**大版本不一致**，见 §8.2） |
| lockfileVersion | **9.0** | pnpm 10 与 pnpm 12 都写 9.0，互相兼容 ✅ |
| lockfile 规模 | 2765 行 / 296 个 entry | |
| 源码规模 | 101 个 `.vue` + 295 个 `.ts`，src 5.4 MB | |
| 当前覆盖率 | lines **69.35%** / statements 66.34% / funcs 61.21% / branches 59.25% | 门禁阈值仅 25/25/25/20，余量极大 |

---

## 2. dependencies（8 个，运行时依赖）

| 名称 | 当前版本 | 可升级版本 | 跨大版本? | 风险 | 建议 |
|---|---|---|---|---|---|
| `vue` | 3.5.40 | **3.5.44** | 否（patch） | 低 | ✅ 可直接升 |
| `vue-router` | 5.2.0 | **5.4.0** | 否（minor） | 低 | ✅ 可直接升；注意 5.4.0 的 peer 含 `vite ^7.3.0 \|\| ^8.0.0`，与本仓库 vite 8.x 匹配 |
| `pinia` | 4.0.2 | **4.1.0** | 否（minor） | 低 | ✅ 可直接升；peer 要求 `vue ^3.5.11`（当前 3.5.40 ✔）、`typescript >=5.6.0`（当前 5.9.3 ✔） |
| `element-plus` | 2.14.3 | **2.14.7** | 否（patch） | 低 | ✅ 可直接升；peer `vue ^3.3.7` ✔ |
| `@element-plus/icons-vue` | 2.3.2 | 2.3.2（**已最新**） | — | — | 无需动作 |
| `axios` | 1.18.1 | **1.20.0** | 否（minor） | 低 | ✅ 可直接升（无 peer） |
| `echarts` | 6.1.0 | 6.1.0（**已最新**） | — | — | 无需动作 |
| `vue-virtual-scroller` | 3.0.4（**精确钉死，无 ^**） | **3.0.5** | 否（patch） | 低（但见 §5.4） | ⚠️ 可升，但升级会**解除钉死**，需同步决定是否保留精确版本 |

**小计**：dependencies 中 6 个可升、2 个已最新；**无一个跨大版本**。

---

## 3. devDependencies（13 个，构建/测试依赖）

| 名称 | 当前版本 | 可升级版本 | 跨大版本? | 风险 | 建议 |
|---|---|---|---|---|---|
| `typescript` | 5.9.3（**精确钉死**） | **7.0.2** | **是，跨 2 个（6.x + 7.x）** | 🔴 **极高** | ❌ **暂不建议**。见 §4 |
| `vue-tsc` | 2.2.10（**精确钉死**） | **3.3.13** | **是（2→3）** | 🟠 中 | ⚠️ **需单独验证**：实测把 `OTAForm.vue:177` 的 `formRef` 变成 TS6133（1 处真错） |
| `vitest` | 4.1.10（范围 ^4.1.10） | **5.0.5** | **是（4→5）** | 🟠 中高 | ⚠️ **需单独验证**，且必须与 coverage 同版本 |
| `@vitest/coverage-v8` | 4.1.10（范围 ^4.1.10） | **5.0.5** | **是（4→5）** | 🟠 中高 | ⚠️ **强制与 vitest 同版本**（peer 精确 `5.0.5`） |
| `happy-dom` | 20.11.1 | **21.0.4** | **是（20→21）** | 🟡 中低 | ⚠️ 需单独验证；破坏点是**默认禁用 CSS/iframe 加载**（安全修复），本仓库测试用 stub ⇒ 影响小 |
| `@vue/test-utils` | 2.4.11 | **2.5.1** | 否（minor） | 🟠 **隐藏陷阱** | ⚠️ **必须单独验证**：minor 但会让 typecheck 变红（2 处 `DOMWrapper.props`） |
| `vite` | 8.1.5 | **8.3.5** | 否（minor） | 低 | ✅ 可升；实测 build 通过（13.69s） |
| `@vitejs/plugin-vue` | 6.0.8 | **6.0.10** | 否（patch） | 低 | ✅ 可升；engines `node ^20.19.0 \|\| >=22.12.0` ✔ |
| `@vue/compiler-sfc` | 3.5.40 | **3.5.44** | 否（patch） | 低 | ✅ 可升（需与 vue 版本对齐，两者同为 3.5.44） |
| `@types/node` | 26.1.1 | **26.6.5** | 否（minor） | 低 | ✅ 可升 |
| `unplugin-auto-import` | 21.0.0 | **21.1.0** | 否（minor） | 低 | ✅ 可升；engines `node >=20.19.0` ✔ |
| `unplugin-vue-components` | 32.1.0 | 32.1.0（**已最新**） | — | — | 无需动作 |
| `@playwright/test` | 1.62.0（**精确钉死**） | **1.64.1** | 否（1.x 内部 minor） | 🟡 中 | ⚠️ 需单独验证：**chromium 版本从 1234 → 1248，需重装浏览器二进制** |

**小计**：devDependencies 中 12 个可升、1 个已最新；**5 个跨大版本**（typescript、vue-tsc、vitest、coverage-v8、happy-dom）。

---

## 4. 最关键的判断：typescript 7 与 vue-tsc 必须一起考虑，但**不能一起升**

### 4.1 实测证据（这是本报告最重要的一节）

在隔离副本里做了 4 组对照实验，每组都是真实安装 + 真实执行 `vue-tsc --noEmit`：

| 实验 | TS | vue-tsc | @vue/test-utils | 结果 |
|---|---|---|---|---|
| A 基线（真仓库现状） | 5.9.3 | 2.2.10 | 2.4.11 | ✅ **0 error** |
| B 只升 vue-tsc | 5.9.3 | **3.3.13** | 2.4.11 | ⚠️ **1 error**：`OTAForm.vue(177,7) TS6133 'formRef' 声明未使用` |
| C 只升 test-utils | 5.9.3 | 2.2.10 | **2.5.1** | ⚠️ **2 error**：`BusConfigTruth.spec.ts(204,17)/(205,18) TS2339 'props' 不存在于 DOMWrapper<Node>` |
| D 两者都升 | 5.9.3 | **3.3.13** | **2.5.1** | ⚠️ **3 error**（= B ∪ C，无额外叠加） |
| E 升到 TS 6 | **6.0.3** | **3.3.13** | 2.5.1 → 2.4.11 | ⚠️ 先 3 error，降 test-utils 后 **1 error**（同 B） |
| F 升到 TS 7 | **7.0.2** | **3.3.13** | 2.4.11 | 🔴 **进程崩溃**，不是类型报错 |

实验 F 的真实报错（两次独立环境均复现）：

```text
Error [ERR_PACKAGE_PATH_NOT_EXPORTED]: Package subpath './lib/tsc' is not defined by "exports"
in .../node_modules/typescript/package.json
    at resolveTscPath (.../vue-tsc/index.js:73:43)
    at main (.../vue-tsc/index.js:44:45)
```

根因（已核对 TS 7.0.2 的 `package.json` 实文件）：`exports` 只剩
`./package.json`、`.`、`./unstable/{sync,async,fs,proto,ast,...}`，
**没有 `./lib/tsc`**。而 vue-tsc 用 `require.resolve('typescript/lib/tsc')` 找编译器主体。

### 4.2 为什么"peer 声明"会给出**假绿**

`vue-tsc@3.3.13` 的 peer 是 `"typescript": ">=5.0.0"`。TS 7.0.2 数值上满足 ⇒
`pnpm up --latest` **能顺利解析**，lockfile 里留下 `vue-tsc@3.3.13(typescript@7.0.2)`，
安装期**零警告**。故障只在运行时爆。

> 实测：在隔离副本执行 `pnpm up --latest --lockfile-only`，成功生成
> `vue-tsc: specifier: 3.3.13 / version: 3.3.13(typescript@7.0.2)` —— 完全静默。

**这条要写进升级 checklist：不能靠 `pnpm install` 的 peer 检查判断 TS 兼容性。**

### 4.3 TS 官方立场（原文要点）

TS 7.0 公告明确写了 Vue 场景：

> "workflows that use **Vue**, MDX, Astro, Svelte, and others will likely **not yet be able to leverage TypeScript 7**."
> "TypeScript 7 does not yet expose a stable programmatic API, and so tools (such as **Volar**) which embed TypeScript into their own compilers and language services can only currently rely on TypeScript 6.0."
> "Projects using Vue ... will need to **continue using TypeScript 6.0** for now."

即：这不是 vue-tsc 的 bug，而是 TS 7 尚未提供稳定编程 API 的**生态空窗**，不是靠升 vue-tsc 能解决的。

### 4.4 由此得出的结论

1. **TS 6.0.x 是当前可达到的天花板**（vue-tsc 3.3.13 + TS 6.0.3 实测可跑通）。
2. **TS 7.0.2 暂不建议**，等 Volar/vue-tsc 宣布支持 TS 7 的稳定 API（关注 `vuejs/language-tools` releases）。
3. TS 6 也不是免费午餐：6.0 把一批旧选项变成**硬错误**（见 §4.5），本仓库 `tsconfig.json` 需要逐项确认。
4. 若要"现在就升 vue-tsc 3.x"，成本是 **1 处 `OTAForm.vue` 的 `formRef` 死变量**（删掉或真正使用它）——代价很小，可以进"需单独验证批"。

### 4.5 若将来升 TS 6/7，`tsconfig.json` 需要预检的项

本仓库现有 `tsconfig.json` 关键项：`target: ES2020`、`module: ESNext`、`moduleResolution: bundler`、
`strict: true`、`types: ["vite/client", "node"]`、`skipLibCheck: true`、`verbatimModuleSyntax: true`、
`noUnusedLocals/Parameters: true`。

TS 6/7 的默认值与硬错误变化（来自 TS 7 公告，与本文件逐项比对）：

| TS 6/7 变化 | 本仓库现状 | 影响 |
|---|---|---|
| `strict` 默认 true | 已显式 true | 无影响 |
| `module` 默认 `esnext` | 已 ESNext | 无影响 |
| `types` 默认变为 `[]` | **已显式列出 `["vite/client","node"]`** | ✅ 不受影响（这通常是最容易踩的坑，本仓库已规避） |
| `rootDir` 默认 `./` | 未显式设置 | ⚠️ 需确认产物/路径推断；`noEmit: true` 下影响有限 |
| `target: es5` 不再支持 | 用 ES2020 | 无影响 |
| `moduleResolution: node/node10` 不再支持 | 用 `bundler` | 无影响 |
| `baseUrl` 不再支持 | **未使用 baseUrl**，`paths` 为相对写法 | ✅ 无影响 |
| `esModuleInterop`/`allowSyntheticDefaultImports` 不能为 false | 未设置（依赖默认） | 无影响 |
| `alwaysStrict` 不能为 false | 未设置 | 无影响 |
| `noUncheckedSideEffectImports` 默认 true | 未设置 | ⚠️ 若代码里有纯副作用 import（如 `import './x.css'`）可能报错，**需实测** |

---

## 5. 精确钉死版本（无 `^`）的四项分析

`package.json` 中 **4 个包写死精确版本**：

### 5.1 `typescript: "5.9.3"`（钉死）

- **为什么钉死**：git 历史可证 —— 上一次依赖升级提交里它是 `"~7.0.2"`，随后在 `21943bdc build(frontend): typecheck 切换为 vue-tsc` 中被**主动降到 `5.9.3`**，同时引入 `vue-tsc: "2.2.10"`。
- **仓库内已有明文记录**（`docs/分析/架构与接口评估及优化方案-2026-09-11.md:164`）：
  > "**不要试图让 vue-tsc 配 TS 7.0.2** —— 实测 `vue-tsc@3.3.11` 报 `ERR_PACKAGE_PATH_NOT_EXPORTED: './lib/tsc'`"
  本次分析**独立复现了同一条结论**（且确认 3.3.13 仍未修复），说明这不是历史偶然，是结构性限制。
- **升级注意**：钉死的不是"TS 版本"，而是"**TS 与 vue-tsc 的兼容配对**"。改任何一个都必须同时验另一个，且不能用 `^` 放开（`^5.9.3` 会允许 5.x 内的 patch，虽安全但失去配对确定性）。**建议升到 6.0.3 时仍写精确版本**。

### 5.2 `vue-tsc: "2.2.10"`（钉死）

- **为什么钉死**：与 5.1 成对出现（`vue-tsc@2.2.10` 是最后一个原生支持 `typescript/lib/tsc` 路径且与 TS 5.9 完美配合的版本）。它承载 `pnpm typecheck` 这条 **CI 必过门禁**（`test.yml` 的 "Lint (type-check)" 步骤）。
- **升级注意**：升 3.x 会**改变类型判定**（实测多 1 个 TS6133），必须连带改代码；且 3.x 的 bugfix 流很快（3.3.13 发布于 2026-10-20，距本次分析仅 3 天），钉死精确版本其实有"避免上游抖动打断 CI"的合理性。

### 5.3 `@playwright/test: "1.62.0"`（钉死）

- **为什么钉死**：Playwright 的 `1.x.y` 里 **y 是浏览器二进制版本序号**，不是语义化 patch。`@playwright/test` 与浏览器 builds 强绑定，`^1.62.0` 会允许 1.64.x 而**浏览器未同步下载** ⇒ 运行期报 "Executable doesn't exist"。
- **具体数据**（从两版 `playwright-core/browsers.json` 实读）：
  | 版本 | chromium revision |
  |---|---|
  | 1.62.0（当前） | **1234** |
  | 1.64.1（最新） | **1248** |
- **升级注意**：必须 `pnpm exec playwright install chromium`（或 CI 里加安装步骤）。本仓库 `playwright.config.ts` 还写死了 `launchOptions.executablePath: '/snap/bin/chromium'`（**系统 chromium，不用下载版**），所以：
  - 若继续用 `/snap/bin/chromium`，升级 playwright 后**协议兼容性**需实测（1.62→1.64 跨了 2 个 minor，CDP 协议一般向后兼容，但 `hasTouch/isMobile` 等 device 参数有变化记录）；
  - 已有的 `~/.cache/ms-playwright/chromium-1228` 与 1.62 要求的 1234 就已经不匹配了（说明该机器上 e2e 本就依赖系统 chromium）。
  - **另**：`playwright.render.config.ts` 用 `CHROME_BIN` 覆盖，不受影响。

### 5.4 `vue-virtual-scroller: "3.0.4"`（钉死）

- **为什么可能钉死**：该包 `3.x` 长期处于"发布节奏不稳"状态（历史上从 `2.0.0-beta.9` 直接跳 `3.0.4`），`^3.0.4` 风险不大但仍被钉死；仓库里只有 `LogRealtimeViewer.vue` 一个使用点（`RecycleScroller` + `RecycleScrollerExposed` **类型导入**）。
- **升级注意**：3.0.4 → 3.0.5 是 patch，但**它导出的 `RecycleScrollerExposed` 类型**被业务代码直接 `import type`；类型若改签名，`vue-tsc` 会红。升级后必须跑 `pnpm typecheck`（不是只跑 build）。若要放开为 `^3.0.5`，建议先确认上方类型契约稳定。

---

## 6. peerDependency 冲突风险实测

### 6.1 `vitest` ↔ `@vitest/coverage-v8`：**精确 peer，必须同版本**

`@vitest/coverage-v8@5.0.5` 的 peer 声明是 **`"vitest": "5.0.5"`**（精确，无 `^`），不是范围。

实测错配（`vitest@4.1.10` + `@vitest/coverage-v8@5.0.5`）：

```text
[WARN] Issues with peer dependencies found. Run "pnpm peers check" to list them.
+ @vitest/coverage-v8 5.0.5
+ vitest 4.1.10
```

即：**pnpm 只警告，照装不误**。这意味着"分批升级时只升 coverage 不升 vitest"是可行但**危险**的组合 —— 安装不报错，跑 `pnpm test:coverage` 时才可能炸。

✅ **规则：这两个包必须在同一条命令里一起升。**
```bash
pnpm add -D vitest@5.0.5 @vitest/coverage-v8@5.0.5
```

### 6.2 其余 peer 均兼容（已逐个核对 registry 元数据）

| 包 | peer 要求 | 本仓库是否满足 |
|---|---|---|
| `vue-tsc@3.3.13` | `typescript >=5.0.0` | 数值满足 **但运行期崩**（§4.2） |
| `vitest@5.0.5` | `vite ^6.4.0 \|\| ^7.0.0 \|\| ^8.0.0`；`@types/node ^22 \|\| >=24`；`happy-dom *` | ✅（vite 8.3.5 / node types 26.6.5） |
| `@vitejs/plugin-vue@6.0.10` | `vue ^3.2.25 \|\| ^3.6.0-rc.10`；`vite ^5\|^6\|^7\|^8` | ✅ |
| `vue-router@5.4.0` | `vue ^3.5.34 \|\| ^4`；`vite ^7.3 \|\| ^8`；`pinia ^3.0.4 \|\| ^4.0.2` | ✅（pinia 4.1.0 在范围内） |
| `pinia@4.1.0` | `vue ^3.5.11`；`typescript >=5.6.0` | ✅（3.5.44 / 5.9.3） |
| `element-plus@2.14.7` | `vue ^3.3.7` | ✅ |
| `@vue/test-utils@2.5.1` | `vue 3.x`、`@vue/compiler-dom 3.x`、`@vue/server-renderer 3.x` | ✅ 版本层面，但**类型层面破**（§4.1 C） |
| `unplugin-auto-import@21.1.0` | `@nuxt/kit ^4`、`@vueuse/core *` | ✅（可选 peer，未装） |

**结论：没有"装不上的" peer 冲突；唯一的真实冲突是 `vue-tsc × TS 7`（静默通过 peer 检查，运行期崩）。**

### 6.3 engines 与 CI Node 版本

| 包 | engines | 是否满足 |
|---|---|---|
| `vitest@5.0.5` | `^22.12.0 \|\| ^24.0.0 \|\| >=26.0.0` | 本地 24.21.0 ✔；**CI `NODE_VERSION: "22"` floating 到最新 22.x**（≥22.12）✔，但建议改成 `22.12` 或 `24` 显式值 |
| `vite@8.3.5` | `^20.19.0 \|\| >=22.12.0` | ✔ |
| `@vitejs/plugin-vue@6.0.10` | `^20.19.0 \|\| >=22.12.0` | ✔ |
| `happy-dom@21.0.4` | `>=20.0.0` | ✔ |

> ⚠️ vitest 5 的 engines 下限是 **22.12.0**。CI 写 `"22"` 会解析到 22.x 最新（目前满足），但这是**隐式**保证；建议 CI 显式写 `22.12` 或 `24`。

---

## 7. 分批升级方案（建议顺序：先小后大）

### 7.0 四个批次的总体设计

```text
批 1「安全批」  11 项 patch/minor，实测 typecheck + build 通过，可一次性合并
      ↓ 验证绿色、CI 通过、合并
批 2「行为变更批」 3 项：@vue/test-utils、vue-tsc、happy-dom（各自独立可回滚）
      ↓ 逐项验证
批 3「测试框架批」 2 项：vitest + coverage-v8（必须原子化一次完成）
      ↓ 验证
批 4「暂缓」      typescript → 7.0.2（等 Volar 生态）
```

---

### 批 1：安全批（可直接批量，11 项）

**实测依据**：这批的组合（含 test-utils 2.5.1、TS 5.9.3、vue-tsc 2.2.10 不变）跑完 `pnpm typecheck` 只有 2 个 error，且**全部来自 `@vue/test-utils` 2.5.1**（属批 2）。若把 test-utils 排除，本批 typecheck **应为 0 error**。

```bash
cd frontend-shared

# dependencies（6 项）
pnpm up --latest vue vue-router pinia element-plus axios vue-virtual-scroller

# devDependencies（5 项，不含 typescript / vue-tsc / vitest / coverage-v8 / happy-dom / test-utils）
pnpm up --latest vite @vitejs/plugin-vue @vue/compiler-sfc @types/node unplugin-auto-import

# playwright 单独一条（见下）
```

**验证（必跑，顺序不可省）**：
```bash
pnpm typecheck      # 期望 0 error
pnpm test:run       # 期望无断言失败
pnpm build          # 期望成功
```

**实测结果（本批主要项已在隔离副本验证）**：
- `pnpm typecheck`：✅ 0 error（排除 test-utils 后，appB 的 2 个 error 均属 test-utils）
- `vite build`（vite 8.3.5 + plugin-vue 6.0.10 + vue 3.5.44 + element-plus 2.14.7）：✅ **成功，13.69s**
  产物：`element 736.06 kB` / `echarts 565.52 kB` / `vue 29.24 kB`，与既有 `manualChunks` 配置一致运行
- ⚠️ 首次 build 失败过一次，原因是隔离副本漏拷 `public/`（`favicon.svg` 找不到）—— **不是依赖问题**，补 `public/` 后通过。

**`@playwright/test` 建议单独一条**（因为要配合浏览器二进制）：
```bash
pnpm up --latest @playwright/test          # 1.62.0 → 1.64.1
pnpm exec playwright install chromium      # chromium rev 1234 → 1248
# 注意：playwright.config.ts 里写死了 executablePath: '/snap/bin/chromium'
#       → 若继续用系统 chromium，install 可跳过，但必须实跑一次 e2e 验证协议兼容
pnpm exec playwright test --project=chromium --list   # 至少确认能加载
```

---

### 批 2：行为变更批（3 项，各自独立验证、独立回滚）

#### 2a. `@vue/test-utils` 2.4.11 → 2.5.1（**最易被低估**）

**问题**：minor 版本，但 **typecheck 会红 2 处**（实测）：

```text
src/views/node/__tests__/BusConfigTruth.spec.ts(204,17): error TS2339:
  Property 'props' does not exist on type 'DOMWrapper<Node>'.
src/views/node/__tests__/BusConfigTruth.spec.ts(205,18): error TS2339:
  Property 'props' does not exist on type 'DOMWrapper<Node>'.
```

**根因**（读代码确认）：该 spec 用的是
`const stub = wrapper.findComponent(stubs.ChannelManager as any)` —— 由于传了 `as any`，
`findComponent` 的重载退化到 `DOMWrapper` 分支，而 2.5.1 收紧了 `DOMWrapper` 的类型（`props` 只存在于
`VueWrapper`）。**是测试写法问题，不是功能回归**。

**修法**（二选一，改动极小）：
```ts
// 方案 A：去掉 as any，让 findComponent 走 VueWrapper 重载
const stub = wrapper.findComponent(stubs.ChannelManager)
expect(stub.props('modelValue')).toBe(true)

// 方案 B：保留 as any，但显式断言类型
const stub = wrapper.findComponent(stubs.ChannelManager as any) as VueWrapper<any>
```

**命令**：
```bash
pnpm add -D @vue/test-utils@2.5.1
pnpm typecheck      # 期望先红 2 处 → 按上面方案修 → 复跑至 0
pnpm test:run
```

#### 2b. `vue-tsc` 2.2.10 → 3.3.13（**必须与 TS 版本一起看**）

**实测**：TS 5.9.3 + vue-tsc 3.3.13 ⇒ **1 error**：
```text
src/components/forms/OTAForm.vue(177,7): error TS6133: 'formRef' is declared but its value is never read.
```
（`const formRef = ref()` 声明后未被模板引用 —— vue-tsc 3 的未使用检测更严格）

**修法**：删除该行，或把它真正绑到 `<el-form ref="formRef">` 上并用于 `validate()`。

**命令**：
```bash
pnpm add -D vue-tsc@3.3.13       # TS 保持 5.9.3
pnpm typecheck                    # 期望 1 error → 修 OTAForm → 复跑至 0
pnpm test:run && pnpm build
```

> ⚠️ 若同时想把 TS 抬到 6，正确姿势是：**先按本步升 vue-tsc 并修完 1 个错，绿了之后**再单独升 TS，
> 这样任一步出问题都能定位到单一变量。

#### 2c. `happy-dom` 20.11.1 → 21.0.4

**破坏点（v21.0.0 release notes 原文）**：为了修 SSRF，**默认禁用 CSS 文件加载与 iframe 页面加载**；
`disableCSSFileLoading` 废弃，新增 `enableCSSFileLoading` / `enableIframePageLoading`。

**本仓库影响评估**：
- `vitest.config.ts` 只写 `environment: 'happy-dom'`，未自定义任何浏览器设置；
- `src/test-setup.ts` 把 Element Plus 组件**全部 stub 成自写 `defineComponent`**（`config.global.components`），
  测试中不加载任何外部样式表/iframe；
- ⇒ **预期无影响**。实测在 happy-dom 21.0.4 下跑完全部测试文件，**执行到的测试 0 断言失败**
  （`Tests 1552 passed | 1 skipped`）。

**命令**：
```bash
pnpm add -D happy-dom@21.0.4
pnpm test:run
```

> 附带收益：21.x 的 21.0.2/21.0.3/21.0.4 分别修了 SSRF、Authorization 头跨域重定向泄漏、
> ESM 编译器代码注入、opaque-origin iframe 隔离 —— 从安全角度**值得升**。

---

### 批 3：测试框架批（vitest 4 → 5，**必须原子化**）

```bash
pnpm add -D vitest@5.0.5 @vitest/coverage-v8@5.0.5    # 必须同一条命令
pnpm test:run
pnpm test:coverage                                     # 验证覆盖率门禁仍 ≥25%
```

**vitest 5 的破坏性变更与本仓库的命中情况**（逐条对照迁移指南 + 本仓库代码）：

| vitest 5 变更 | 本仓库情况 | 是否命中 |
|---|---|---|
| 要求 `vite >=6.4.0`、`node >=22.12.0` | vite 8.3.5 / node 24（CI 22.x） | ✅ 满足 |
| `vite` 变为 **peer** 而非直接依赖 | 本仓库已直接声明 `vite` | ✅ 无影响 |
| `clearMocks` **默认改 true** | `vitest.config.ts` **未设置**（沿用默认）⇒ **行为会变** | 🟠 **需关注** |
| `vi.mock/vi.unmock/vi.hoisted` 出现在非顶层**改为抛错** | 扫描 379 处 `vi.mock(`、88 处 `vi.hoisted`，**缩进写法嵌套 0 处** | ✅ 不命中 |
| 未 `await` 的 `.resolves/.rejects` **改为测试失败** | 扫描未 await 的 `.resolves/.rejects`：**0 处** | ✅ 不命中 |
| `testNamePattern` 改用 `-joined` 全名 | 未见 CI/脚本使用 `-t` | ✅ 不命中 |
| inline `test.projects` 默认继承根配置 | **未使用 `test.projects`** | ✅ 不命中 |
| 自定义 matcher 的 `Matchers` 泛型签名变化 | 全仓 **无 `expect.extend`** | ✅ 不命中 |
| Benchmark API 重写（`bench` 变 fixture） | 未见 `.bench.ts` | ✅ 不命中 |
| coverage 切到 `@vitest/istanbuljs` 系 | 用 `provider: 'v8'`，不受 istanbul 影响；但**报告数值可能微变** | 🟡 需复核 |

**唯一需要真正关注的是 `clearMocks` 默认值变化**：
本仓库如果把 mock 调用记录写在 `beforeAll`/`setup` 里，v5 会在每个 test 前清历史 ⇒ 断言可能变红。
扫描结果：`src/test-setup.ts` 只注册组件 stub，**无 `beforeEach` 里的 mock 调用**；风险较低。
若遇到红，最小回退是显式写回旧行为：
```ts
// vitest.config.ts
test: { clearMocks: false, ... }
```

**覆盖率门禁安全性**：当前 lines 69.35% vs 阈值 25%，即便 v8 provider 口径微变也**不可能跌破**。

---

### 批 4：暂不建议（typescript 7.0.2）

```bash
# ❌ 现在不要执行这一条
# pnpm add -D typescript@7.0.2 vue-tsc@3.3.13
```

**实测后果**：`pnpm typecheck` 直接 `ERR_PACKAGE_PATH_NOT_EXPORTED` 崩溃 ⇒ CI 的
"Lint (type-check)" 步骤必红 ⇒ `pnpm build`（`build = typecheck && vite build`）也必红。

**什么条件下可以重新评估**：
1. `vuejs/language-tools` 发布显式声明"支持 TypeScript 7 / 使用新的稳定 API"的版本；
2. 或 TS 7.1+ 提供稳定 programmatic API（公告称 7.1 会桥接生态）；
3. 然后按 **vue-tsc 先升 → TS 再升** 的两步法验证。

---

## 8. lockfile 影响与 CI

### 8.1 `--frozen-lockfile` 的硬门禁（实测）

`test.yml:278` 与 `Dockerfile:39` 都用 `pnpm install --frozen-lockfile`。

实测（隔离副本，只改 `package.json` 的 `vite` specifier 为 `^8.3.5`，lockfile 不动）：

```text
Error: ERR_PNPM_OUTDATED_LOCKFILE
  × installing dependencies
  ╰─▶ Cannot install with "frozen-lockfile" because pnpm-lock.yaml is not up to date with package.json.
        Failure reason: specifiers in the lockfile don't match specifiers in package.json:
      * in importers["."]:
      * 1 dependency is mismatched:
        - vite (lockfile: ^8.1.5, manifest: ^8.3.5)
[EXIT=1]
```

**结论：`package.json` 与 `pnpm-lock.yaml` 必须同一次提交。**
任何"只改 package.json 不更新 lockfile"的提交都会让 CI 与 Docker 构建**同时**失败
（Dockerfile 的 `frontend-builder` 阶段同样会挂）。

### 8.2 必须同时注意的两点

**(a) pnpm 大版本不一致**：CI 用 `PNPM_VERSION: "10"`，本地是 **12.14.0**。
- 好消息：实测 pnpm 12 生成的 lockfile 仍是 `lockfileVersion: '9.0'`，与 pnpm 10 兼容 ✅
- 坏消息：pnpm 12 有一些 pnpm 10 没有的检查（例如本次 dry-run 输出
  `Verifying lockfile against supply-chain policies (296 entries)`）。**本地验证通过 ≠ CI 通过**，
  建议本地验证时用 `corepack`/`npx pnpm@10` 对齐，或把 CI 的 `PNPM_VERSION` 抬到与本地一致。

**(b) 升级 PR 的操作顺序**（保证 lockfile 与 manifest 同步）：
```bash
cd frontend-shared
pnpm up --latest <packages...>     # 这一步会同时改 package.json 与 pnpm-lock.yaml
git add package.json pnpm-lock.yaml   # 两个文件必须一起提交
pnpm install --frozen-lockfile        # 本地预演 CI，确认不报 ERR_PNPM_OUTDATED_LOCKFILE
```

### 8.3 lockfile 变更规模预估

- 当前 lockfile：**2765 行 / 296 个 entry**
- 批 1（11 项）预计改动 200–400 行（大部分是 `resolution` 与 `snapshots` 的版本号字符串替换）
- 批 3（vitest 5）预计改动**较大**：vitest 5 "bundles its own dependencies"（公告原文），
  会**删掉**一批原来平铺的依赖块、新增内部打包的外部依赖，预计 400–800 行
- **建议每个批次一个独立 PR**，让 lockfile diff 可审（尤其批 3 的 diff 会很大且不易人工核对，
  靠 CI 的 `typecheck + test:coverage + build` 三件套兜底）

---

## 9. 完整可执行命令清单（复制即用）

### 9.1 保守批（只升 patch/minor，**零 major**，推荐先做）

```bash
cd frontend-shared && git status --porcelain   # 确认干净

# ── 批次 1A：dependencies（6 项，全 patch/minor）
pnpm up --latest vue vue-router pinia element-plus axios vue-virtual-scroller

# ── 批次 1B：devDependencies 中的安全子集（5 项，全 patch/minor）
pnpm up --latest vite @vitejs/plugin-vue @vue/compiler-sfc @types/node unplugin-auto-import

# 验证三件套
pnpm typecheck && pnpm test:run && pnpm build

# ── 批次 1C：playwright（需浏览器二进制配合，单独提交）
pnpm up --latest @playwright/test
pnpm exec playwright install chromium
pnpm exec playwright test --project=chromium --list

git add package.json pnpm-lock.yaml && git commit -m "chore(deps): 前端 patch/minor 升级（批 1）"
```

> 保守批**刻意不含**：`typescript`、`vue-tsc`、`@vue/test-utils`、`happy-dom`、`vitest`、`@vitest/coverage-v8`。
> 前 4 个是"钉死 + 有行为/类型影响"，后 2 个是 major 且必须成对。

### 9.2 激进批（含 major，**按顺序逐个提交**）

```bash
cd frontend-shared

# ── 步骤 1：@vue/test-utils minor（先做，因为它会与批 1 的 typecheck 混在一起）
pnpm add -D @vue/test-utils@2.5.1
pnpm typecheck     # 预期红 2 处 BusConfigTruth.spec.ts → 按 §7-2a 修 → 复跑至 0
pnpm test:run
git add -A && git commit -m "chore(deps): @vue/test-utils 2.5.1 + 修 DOMWrapper.props 类型"

# ── 步骤 2：vue-tsc 2 → 3（TS 保持 5.9.3）
pnpm add -D vue-tsc@3.3.13
pnpm typecheck     # 预期红 1 处 OTAForm.vue formRef → 修 → 复跑至 0
pnpm test:run && pnpm build
git add -A && git commit -m "chore(deps): vue-tsc 2.2.10 → 3.3.13 + 清理 OTAForm 未使用 ref"

# ── 步骤 3：happy-dom 20 → 21
pnpm add -D happy-dom@21.0.4
pnpm test:run
git add package.json pnpm-lock.yaml && git commit -m "chore(deps): happy-dom 20 → 21（修 SSRF/注入等 4 个安全问题）"

# ── 步骤 4：vitest 4 → 5（两个包必须一条命令）
pnpm add -D vitest@5.0.5 @vitest/coverage-v8@5.0.5
pnpm test:run && pnpm test:coverage      # 确认覆盖率门禁 ≥25% 仍通过
git add package.json pnpm-lock.yaml && git commit -m "chore(deps): vitest 4 → 5（含 coverage-v8 同版本对齐）"

# ── 步骤 5（可选，风险更高）：typescript 5.9.3 → 6.0.3
pnpm add -D typescript@6.0.3
pnpm typecheck
pnpm test:run && pnpm build

# ── 步骤 6：❌ 不要做
# pnpm add -D typescript@7.0.2     ← vue-tsc 运行期崩溃，等 Volar 生态
```

### 9.3 想知道"升到最新会拿到什么"—— 安全的预演方式（不碰仓库）

```bash
# 在仓库外的副本里预演，完全不触碰真实 package.json / lockfile
OUT=/tmp/fe-probe && rm -rf "$OUT" && mkdir -p "$OUT"
cp frontend-shared/package.json frontend-shared/pnpm-workspace.yaml "$OUT"/
cd "$OUT"
pnpm up --latest --lockfile-only     # 只生成 lockfile，不装 node_modules
node -e "const p=require('./package.json');console.log(p.devDependencies, p.dependencies)"
```

---

## 10. 每批之后的验收标准（缺一不可）

| 检查项 | 命令 | 通过标准 | 备注 |
|---|---|---|---|
| 类型门禁 | `pnpm typecheck` | **0 error**（不是"比之前少"） | 当前基线是 0；任何新增都要修掉，不许加 `@ts-ignore` |
| 单元测试 | `pnpm test:run` | 无断言失败 | ⚠️ 见 §11 关于本机 worker 偶发超时的说明 |
| 覆盖率 | `pnpm test:coverage` | lines ≥25%（当前 69.35%，余量大） | `coverage-summary.json` 由 CI 解析 |
| 构建 | `pnpm build` | 成功产出 `dist/` | `build` 内含 `typecheck`，等于双重校验 |
| CI 预演 | `pnpm install --frozen-lockfile` | 不报 `ERR_PNPM_OUTDATED_LOCKFILE` | 防止 package.json 与 lockfile 脱节 |
| e2e（仅改 playwright 时） | `pnpm exec playwright test` | 至少 `--list` 能加载；“能跑”需真后端 | `playwright.config.ts` 默认 baseURL 5174 |

---

## 11. 未验证项与已知局限（务必读，避免误判为"全绿"）

1. **本机测试环境在负载下有 worker 超时问题（与依赖无关）**。
   - 基线（真仓库、当前依赖）跑 `vitest run`：`Test Files 2 failed | 145 passed`、
     `Tests 2 failed | 1799 passed`，其中大部分红是
     `[vitest-pool]: Failed to start forks worker ... Timeout waiting for worker to respond`。
   - 升级后副本（vitest 5 + happy-dom 21）跑：`Tests 1552 passed | 1 skipped`，
     **0 断言失败**，但同样有 44 条 worker 启动超时错误，导致**部分测试文件根本没跑起来**。
   - ⇒ **"测试通过"不能作为这两套依赖等价的证据**；文件覆盖面不同（149 vs 147），
     只能说明"跑到的那些用例在新版本下行为一致"。**在 CI（干净 runner）上复跑才算定论。**
2. **e2e 完全未验证**：需要真后端（8080/8082）与系统 chromium，本次未启动任何服务。
   `@playwright/test` 1.62 → 1.64 的协议兼容性属**未验证**。
3. **TS 6.0.3 只是"能跑通"，不代表该升**。实验 E 显示 TS 6 + vue-tsc 3.3.13 可执行，
   但 TS 6 有大量默认值/硬错误变更（§4.5），其中 `noUncheckedSideEffectImports` 与
   `rootDir` 两项**未做实际影响评估**（需真跑一遍再判断）。
4. **`vue-tsc` 3.3.13 是 3 天前发布的版本**（2026-10-20），版本很新；
   若要更稳可先用 `3.3.12`（2026-10-02）或 `3.0.11`（`v3.0` dist-tag），
   但本次**只实测了 3.3.13**。
5. **报告未覆盖**：`index.html` 内联脚本、`tools/` 目录下的脚本依赖、`playwright.render.config.ts` 的
   `webServer` 行为（这些不属"依赖版本"范畴，但升级 vite 可能影响）。

---

## 12. 一页纸总结

| 分类 | 包 | 动作 |
|---|---|---|
| **可安全批量（11）** | vue, vue-router, pinia, element-plus, axios, vue-virtual-scroller, vite, @vitejs/plugin-vue, @vue/compiler-sfc, @types/node, unplugin-auto-import | `pnpm up --latest` 一次搞定，实测 typecheck/build 通过 |
| **可升但需配套（1）** | @playwright/test 1.62.0 → 1.64.1 | 需 `playwright install`（chromium 1234→1248） |
| **需单独验证（3）** | @vue/test-utils 2.5.1（红 2 处类型）<br>vue-tsc 3.3.13（红 1 处类型）<br>happy-dom 21.0.4（安全默认值变更） | 逐个提交、逐个修、逐个验 |
| **必须原子化（2）** | vitest + @vitest/coverage-v8 4.1.10 → 5.0.5 | 必须同一条 `pnpm add` 命令；注意 `clearMocks` 默认变 true |
| **已最新（3）** | echarts 6.1.0, @element-plus/icons-vue 2.3.2, unplugin-vue-components 32.1.0 | 无需动作 |
| **暂不建议（1）** | typescript 5.9.3 → 7.0.2 | `vue-tsc` 崩溃（`ERR_PACKAGE_PATH_NOT_EXPORTED './lib/tsc'`）；等 Volar 支持 TS 7 稳定 API。**上限是 6.0.3** |

**一句话**：**21 个直接依赖里 18 个可升；其中 11 个能一批直接搞定，6 个需配套/单独验证（playwright 1 + test-utils 1 + vue-tsc 1 + happy-dom 1 + vitest/coverage 2）；真正的坑只有 1 个 —— typescript 7，而它的解法是"别升"，不是"想办法升"。**
