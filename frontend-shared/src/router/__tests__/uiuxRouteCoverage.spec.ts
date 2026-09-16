/**
 * UI/UX 门禁「分母漂移」守卫（本仓第 5 条全站源码扫描门禁）。
 *
 * ─ 已确诊缺陷（2026-09-15 本守卫的立项原因）────────────────────────────────
 * e2e/helpers/uiux-fixtures.ts 的 ROUTES 是**手工维护**的“受审路由”冻结清单，
 * 而 src/router/index.ts 新增了 notification-channels / notification-deliveries
 * 两个路由级页面后**没有任何守卫能发现漏登记**。后果：这两个页面**从未被 UI/UX 门禁审过**
 * （横向溢出 / 真实裁切 / 移动端横滚都没验），而门禁本身仍然全绿 —— 门禁绿 ≠ 覆盖完整，
 * 因为分母是手写的。这正是本仓反复吃亏的“分母假绿”。
 *
 * ── 放 router/__tests__ 还是 e2e/helpers/__tests__？为什么放这里 ─────────────
 * **必须放 src/router/__tests__/**，理由是决定性的：
 *   vitest.config.ts 的 test.include 只吃 src 目录下的 .ts/.js 测试（见该文件）——
 *   把本守卫放到 e2e/helpers/__tests__/ 会让它在 npx vitest run 里**永远不执行**，
 *   等于一条恒绿的假门禁（比没有更糟）。同时本守卫读的是 src/router/index.ts（分母来源）
 *   与 e2e/helpers/uiux-fixtures.ts（受审清单），属 router 域，与 guards.spec.ts 同域。
 *   注意：这里**只把 fixtures 当文本读**（readFileSync），不 import —— 否则会把
 *   @playwright/test 拖进 vitest 进程。
 *
 * ── 口径（denominator）：什么算一个“路由级页面”──────────────────────────────
 * 契约 §4 审的是**布局（MainLayout）下、无参数的相对子路由**。因此口径是：
 *   **src/router/index.ts 里 path 为相对路径（不以 / 开头、不含 :）且有 component
 *   的路由** = 路由级页面 = 必须进 ROUTES 的分母。
 *   · **必须区分相对子路由与顶层绝对路径**：/login /403 /dev/* 是顶层绝对路由，
 *     它们在布局之外，不是“布局下的业务页”，若混进分母会制造假缺失；
 *   · **参数化详情路由**（node/:id、node/:id/overview、edge-device/:id）需要实体 ID，
 *     契约 §4 约定“由列表页进入审计”，不进冻结清单；
 *   · path: '' 是 redirect 占位，不是页面。
 *   上述三类**不是自动豁免，而是显式登记**（见 REGISTERED_ROUTE_EXCLUSIONS）——
 *   **fail-closed**：任何既不是业务页、又不在例外表里的路径形态都会被报出（unregistered），
 *   而不是被静默忽略。新增顶层路由 / 新增参数化详情路由都必须**先登记再通过**。
 *
 * ── component 两种写法（漂移成因之一，必须都覆盖）──────────────────────────
 *   ① 内联：component: () => import('@/views/x/X.vue')
 *   ② routeLoaders.ts 具名 loader：component: loadNodeList
 *   只解析一种会漏掉一半。解析不出视图文件时**硬失败**（不是跳过）——
 *   跳过 = 静默漏检 = 假绿。
 *
 * ── 本门禁**查不了什么**（能力边界，必须与规则一起读）───────────────────────
 * 1. **查不了 ROUTES 条目的 visible/settle 选择器是否属于该页**：本守卫只对齐
 *    “路径集合”，不能发现“把 /dashboard 的选择器照抄到通知页”。选择器语义由
 *    waitForRouteReady 在真实实例上验证（空态/数据态/错误态三选一）。
 * 2. **查不了页面在审计库里有没有数据**：空态也算就绪，由 ROUTES 的选择器设计保证。
 * 3. **查不了路由是否可达 / 菜单是否可达**：只静态解析路由表。
 * 4. **查不了动态注册的路由**（router.addRoute）—— 当前全站无此形态；引入需显式登记。
 *
 * ── 变红条件 ──────────────────────────────────────────────────────────────
 * · router 新增一个路由级页面而没同步 ROUTES（打印缺失清单 + 补登记指引）；
 * · ROUTES 里出现 router 之外、又没登记理由的路径（陈旧条目）；
 * · 出现第三种 component 写法 / 路由解析不出视图文件；
 * · 出现未登记的路径形态（fail-closed）。
 */
import { describe, it, expect } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'

const ROOT = process.cwd()
const ROUTER_FILE = join(ROOT, 'src', 'router', 'index.ts')
const LOADERS_FILE = join(ROOT, 'src', 'router', 'routeLoaders.ts')
const FIXTURES_FILE = join(ROOT, 'e2e', 'helpers', 'uiux-fixtures.ts')

/**
 * 显式登记的“不进受审分母”路由（**fail-closed 白名单**：未在此登记的非业务页一律报错）。
 * 每个条目都必须写清理由；新增顶层/详情路由时必须先在此登记再通过。
 */
export const REGISTERED_ROUTE_EXCLUSIONS: Record<string, string> = {
  '/': '布局壳（MainLayout.vue）本身，不是页面；契约 §4 审的是它 children 里的页面',
  '': "子路由空路径 redirect 占位（redirect: '/dashboard'），没有 component",
  '/login': '认证页：在布局之外，契约 §4 单独列为「登录页」，不进 13 页冻结清单',
  '/403': '无权限错误页：无数据面，不进受审分母',
  '/dev/mock-bms': 'DEV-only 页面（import.meta.env.DEV 门控），生产构建里不存在',
  '/dev/bms-demo': 'DEV-only 设计稿预览页（同上），不进受审分母',
  '/:pathMatch(.*)*': '404 兜底路由，非真实页面',
  'node/:id': '参数化详情路由：需要实体 ID，契约 §4 约定由列表页进入审计，不进冻结清单',
  'node/:id/overview': '参数化详情路由（同上）',
  'edge-device/:id': '参数化详情路由（同上）',
}

/**
 * 允许出现在 ROUTES 里、但不在 router 业务页里的路径（**目前为空**）。
 * 若将来确有“故意只审数据承载页”的需求，在此登记 path + 理由 + 日期，fail-closed。
 */
export const ROUTES_EXTRA_ALLOWED: Record<string, string> = {}

// ── 解析器（node:fs 读文本；绝不静默吞异常）────────────────────────────────

/** 逐字符扫出所有平衡的 {...} 块（跳过字符串与注释），供按“路由对象”取 path/component。 */
export function objectBlocks(src: string): string[] {
  const out: string[] = []
  const stack: number[] = []
  let quote = ''
  for (let i = 0; i < src.length; i += 1) {
    const c = src[i]
    if (quote) {
      if (c === quote && src[i - 1] !== '\\') quote = ''
      continue
    }
    // 注释：//... 与 /*...*/（本仓路由文件里有中文注释，先剔掉再数括号）
    if (c === '/' && src[i + 1] === '/') {
      const nl = src.indexOf('\n', i)
      i = nl === -1 ? src.length : nl
      continue
    }
    if (c === '/' && src[i + 1] === '*') {
      const end = src.indexOf('*/', i + 2)
      i = end === -1 ? src.length : end + 1
      continue
    }
    if (c === '"' || c === "'" || c === '`') { quote = c; continue }
    if (c === '{') stack.push(i)
    else if (c === '}') {
      const s = stack.pop()
      if (s !== undefined) out.push(src.slice(s, i + 1))
    }
  }
  return out
}

export interface RouteEntry {
  /** 源码里的原始 path 字符串（相对子路由或绝对路径） */
  rawPath: string
  /** component 的原始引用（import 路径或 loader 标识符）；无 component 为 null */
  componentRef: string | null
  /** 解析后的视图文件（相对前端根），解析不出为 null */
  viewFile: string | null
}

export interface RouterScan {
  /** 路由级页面（相对、无参数、有 component） */
  pages: RouteEntry[]
  /** 被显式登记排除的路径 → 理由 */
  excluded: { rawPath: string; reason: string }[]
  /** 既非业务页、又未登记的路径形态（fail-closed 信号） */
  unregistered: RouteEntry[]
  /** 业务页里 component 写法解析不出视图文件（硬失败信号） */
  unresolved: RouteEntry[]
}

export type PathKind = 'business' | 'excluded' | 'unregistered'

/**
 * 路径分类器 —— **唯一判定函数**（自检与真实扫描共用同一个，防止“测试一套、跑的是另一套”）。
 *   business    : 相对、无参数 → 布局下的路由级页面，必须进 ROUTES
 *   excluded    : 在 REGISTERED_ROUTE_EXCLUSIONS 里显式登记
 *   unregistered: 其余（新顶层路由 / 新参数化详情路由）→ fail-closed 报出
 */
export function classifyRoutePath(rawPath: string): { kind: PathKind; reason?: string } {
  if (Object.prototype.hasOwnProperty.call(REGISTERED_ROUTE_EXCLUSIONS, rawPath)) {
    return { kind: 'excluded', reason: REGISTERED_ROUTE_EXCLUSIONS[rawPath] }
  }
  const isRelativeParamless =
    rawPath !== '' && !rawPath.startsWith('/') && !rawPath.includes(':')
  if (isRelativeParamless) return { kind: 'business' }
  return { kind: 'unregistered' }
}

/** 解析 routeLoaders.ts 的具名 loader 映射（export const x = () => import('...')）。 */
export function parseLoaders(src: string): Record<string, string> {
  const map: Record<string, string> = {}
  for (const m of src.matchAll(/export const (\w+)\s*=\s*\(\)\s*=>\s*import\('([^']+)'\)/g)) {
    map[m[1]] = m[2]
  }
  return map
}

/** 扫描 router 源码，得到分母 / 例外 / 未登记 / 解析失败四组事实。 */
export function scanRouter(src: string, loaders: Record<string, string>): RouterScan {
  const byPath = new Map<string, RouteEntry>()
  for (const body of objectBlocks(src)) {
    const pm = body.match(/path:\s*'([^']*)'/)
    if (!pm) continue
    const rawPath = pm[1]
    // component 写法①：内联 () => import('...')；写法②：具名 loader 标识符
    const inline = body.match(/component:\s*\(\)\s*=>\s*import\('([^']+)'\)/)
    const named = body.match(/component:\s*([A-Za-z_$][\w$]*)/)
    let componentRef: string | null = null
    let viewFile: string | null = null
    if (inline) {
      componentRef = inline[1]
      viewFile = inline[1].replace(/^@\//, 'src/')
    } else if (named) {
      componentRef = named[1]
      viewFile = loaders[named[1]] ? loaders[named[1]].replace(/^@\//, 'src/') : null
    }
    if (!byPath.has(rawPath)) byPath.set(rawPath, { rawPath, componentRef, viewFile })
  }

  const pages: RouteEntry[] = []
  const excluded: { rawPath: string; reason: string }[] = []
  const unregistered: RouteEntry[] = []
  const unresolved: RouteEntry[] = []
  for (const entry of byPath.values()) {
    const { kind, reason } = classifyRoutePath(entry.rawPath)
    if (kind === 'excluded') { excluded.push({ rawPath: entry.rawPath, reason: reason! }); continue }
    if (kind === 'unregistered') { unregistered.push(entry); continue }
    pages.push(entry)
    if (!entry.viewFile) unresolved.push(entry)
  }
  pages.sort((a, b) => a.rawPath.localeCompare(b.rawPath))
  return { pages, excluded, unregistered, unresolved }
}

/** 从 fixtures 源码里取出 export const ROUTES 数组块（只读文本，不 import）。 */
export function routesBlock(src: string): string {
  const start = src.indexOf('export const ROUTES')
  if (start < 0) throw new Error('uiux-fixtures.ts 里找不到 export const ROUTES —— 解析器路径错了')
  // 必须从 `=` 之后找数组字面量：类型标注 `RouteProbe[]` 里也有一个 `[`，
  // 直接 indexOf('[', start) 会命中它并立刻在 `]` 处收口（本文件初版就是这样解析出 0 条的）。
  const eq = src.indexOf('=', start)
  if (eq < 0) throw new Error('ROUTES 声明没有 = —— 解析器坏了')
  const open = src.indexOf('[', eq)
  if (open < 0) throw new Error('ROUTES 后面没有数组字面量 —— 解析器坏了')
  let depth = 0
  let quote = ''
  for (let i = open; i < src.length; i += 1) {
    const c = src[i]
    if (quote) { if (c === quote && src[i - 1] !== '\\') quote = ''; continue }
    if (c === '"' || c === "'" || c === '`') { quote = c; continue }
    if (c === '[') depth += 1
    else if (c === ']') { depth -= 1; if (depth === 0) return src.slice(open, i + 1) }
  }
  throw new Error('ROUTES 数组没有闭合的 ] —— 解析器坏了')
}

/** 从 ROUTES 块里取受审路径集合（path: '/xxx'）。 */
export function routesPaths(src: string): string[] {
  return [...routesBlock(src).matchAll(/path:\s*'\/([^']+)'/g)].map((m) => '/' + m[1])
}

// ── 真实扫描（模块加载时读一次）─────────────────────────────────────────────
const routerSrc = readFileSync(ROUTER_FILE, 'utf8')
const loaders = parseLoaders(readFileSync(LOADERS_FILE, 'utf8'))
const fixturesSrc = readFileSync(FIXTURES_FILE, 'utf8')
const scan = scanRouter(routerSrc, loaders)
const declared = routesPaths(fixturesSrc)

// ── 用例 ──────────────────────────────────────────────────────────────────

describe('UI/UX 门禁分母守卫（router 路由级页面 ⊆ ROUTES）', () => {
  it('口径可复现：分母非空、且两种 component 写法都解析出了视图文件', () => {
    // 跑空守卫（本仓范式）：解析器坏掉 → 0 个页面 → 下面的 toEqual([]) 会全部假绿。
    expect(scan.pages.length, '路由级页面分母为 0 —— 路由解析器失效（这正是假绿的成因）')
      .toBeGreaterThan(0)
    expect(scan.pages.length, '路由级页面数少于已知的 15 个 —— 解析器漏页').toBeGreaterThanOrEqual(15)
    // 分母里必须同时含“具名 loader”与“内联 import”两种写法的代表，证明解析器覆盖两种。
    expect(scan.pages.find((p) => p.rawPath === 'node')?.componentRef, '具名 loader 写法未解析')
      .toBe('loadNodeList')
    expect(scan.pages.find((p) => p.rawPath === 'node')?.viewFile, '具名 loader 未解析出文件')
      .toBe('src/views/node/NodeList.vue')
    expect(scan.pages.find((p) => p.rawPath === 'notification-channels')?.componentRef, '内联 import 写法未解析')
      .toBe('@/views/notification/NotificationChannels.vue')
    expect(scan.unresolved, '有路由级页面的 component 写法解析不出视图文件（新增写法必须同步解析器）')
      .toEqual([])
  })

  it('fail-closed：非业务路径必须显式登记，未登记的路径形态一律报出', () => {
    expect(scan.unregistered, '存在既不是业务页、又未登记的路由形态 —— 请在 REGISTERED_ROUTE_EXCLUSIONS 登记并写明理由')
      .toEqual([])
    // 每条被排除的路由都必须有非空理由（防止“登记了但没写为什么”）
    for (const e of scan.excluded) {
      expect(e.reason, '排除路由 ' + e.rawPath + ' 没有理由').toBeTruthy()
    }
  })

  it('核心断言：router 里每一个路由级页面都必须在 ROUTES 里出现（缺一即红）', () => {
    const have = new Set(declared)
    const missing = scan.pages.filter((p) => !have.has('/' + p.rawPath))
    expect(
      missing.map((p) => p.rawPath),
      'UI/UX 门禁分母漂移：router 里有 ' + missing.length + ' 个路由级页面未登记进 ROUTES ——\n' +
      missing.map((p) => '  - /' + p.rawPath + '  (' + (p.viewFile || p.componentRef || '?') + ')').join('\n') +
      '\n补登记指引：在 frontend-shared/e2e/helpers/uiux-fixtures.ts 的 ROUTES 里为每个上列路径补一条\n' +
      "  { name, path: '/xxx', root, visible, settle, dataShaped }，其中\n" +
      '  · root     = 该页根容器（源码模板第一层 div 的 class）；\n' +
      '  · visible  = “有数据 / 空态 / 错误态”三者之一的信号（**空态必须也算就绪**）；\n' +
      '  · settle   = 加载已结束的信号（模板脱离 loading/v-loading 分支后才出现）。\n' +
      '禁止照抄别的页面的选择器；登记后请在真实实例上验证空态与数据态都能就绪。',
    ).toEqual([])
  })

  it('反向：ROUTES 里不得有 router 之外、又未登记理由的陈旧路径', () => {
    const pagePaths = new Set(scan.pages.map((p) => '/' + p.rawPath))
    const stale = declared.filter(
      (p) => !pagePaths.has(p) && !Object.prototype.hasOwnProperty.call(ROUTES_EXTRA_ALLOWED, p),
    )
    expect(
      stale,
      'ROUTES 里有 router 中不存在的路径（陈旧条目 / 路由改名漏同步）：\n' +
      stale.map((p) => '  - ' + p).join('\n') +
      '\n处理：删除该条目，或在 ROUTES_EXTRA_ALLOWED 里登记 path + 理由 + 日期（fail-closed）。',
    ).toEqual([])
  })

  it('分类器自检：正例/反例喂给同一个判定函数，必须能区分', () => {
    // 反例（真实 router 里出现的每一种非业务形态，必须被判为 excluded —— 已登记）
    expect(classifyRoutePath('/').kind).toBe('excluded')
    expect(classifyRoutePath('').kind).toBe('excluded')
    expect(classifyRoutePath('/login').kind).toBe('excluded')
    expect(classifyRoutePath('/403').kind).toBe('excluded')
    expect(classifyRoutePath('/dev/mock-bms').kind).toBe('excluded')
    expect(classifyRoutePath('/:pathMatch(.*)*').kind).toBe('excluded')
    expect(classifyRoutePath('node/:id').kind).toBe('excluded')
    expect(classifyRoutePath('node/:id/overview').kind).toBe('excluded')
    expect(classifyRoutePath('edge-device/:id').kind).toBe('excluded')
    // 正例（必须被判为 business）
    expect(classifyRoutePath('dashboard').kind).toBe('business')
    expect(classifyRoutePath('notification-channels').kind).toBe('business')
    expect(classifyRoutePath('notification-deliveries').kind).toBe('business')
    expect(classifyRoutePath('profile').kind).toBe('business')
    // 未登记形态（必须 fail-closed）
    expect(classifyRoutePath('/new-top-level').kind).toBe('unregistered')
    expect(classifyRoutePath('alert/:id').kind).toBe('unregistered')
    // 端到端：解析器在合成源码上也必须给出与分类器一致的结论
    const synth = scanRouter(
      'const routes = [' +
        "{ path: '/new-top-level', component: () => import('@/views/a.vue') }," +
        "{ path: 'brand-new-page', component: () => import('@/views/b.vue') }," +
        "{ path: 'detail/:id', component: () => import('@/views/c.vue') }" +
      ']',
      {},
    )
    expect(synth.unregistered.map((e) => e.rawPath).sort()).toEqual(['/new-top-level', 'detail/:id'])
    expect(synth.pages.map((e) => e.rawPath)).toEqual(['brand-new-page'])
    expect(synth.excluded).toEqual([])
  })

  it('两种 component 写法的正例/反例自检（防止只解析一种）', () => {
    const inline = scanRouter("const r = [{ path: 'a', component: () => import('@/views/a/A.vue') }]", {})
    expect(inline.pages[0].viewFile).toBe('src/views/a/A.vue')
    const named = scanRouter("const r = [{ path: 'b', component: loadB }]", { loadB: '@/views/b/B.vue' })
    expect(named.pages[0].viewFile).toBe('src/views/b/B.vue')
    // 第三种写法（裸标识符但 loader 表里没有）必须进入 unresolved，而不是被静默跳过
    const unknown = scanRouter("const r = [{ path: 'c', component: someOtherThing }]", {})
    expect(unknown.pages.map((p) => p.rawPath)).toEqual(['c'])
    expect(unknown.unresolved.map((p) => p.rawPath)).toEqual(['c'])
    // 路由级页面缺 component 同样必须被报出
    const noComp = scanRouter("const r = [{ path: 'd', meta: { title: 'x' } }]", {})
    expect(noComp.pages.map((p) => p.rawPath)).toEqual(['d'])
    expect(noComp.unresolved.map((p) => p.rawPath)).toEqual(['d'])
  })

  it('ROUTES 解析器自检：能从 fixtures 文本里取出受审路径（不依赖本轮修复）', () => {
    expect(declared.length, 'ROUTES 解析出 0 条 —— 解析器坏了').toBeGreaterThanOrEqual(13)
    expect(declared).toContain('/dashboard')
    // 路径必须带前导斜杠且不含引号（防止解析把引号漏进来）
    for (const p of declared) expect(p).toMatch(/^\/[a-z0-9-]+$/)
    // 合成 fixtures 文本的正例/反例
    expect(routesPaths("export const ROUTES = [{ path: '/a' }, { path: '/b' }]")).toEqual(['/a', '/b'])
  })
})
