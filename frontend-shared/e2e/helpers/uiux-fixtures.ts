/**
 * UI/UX 回归门禁的确定性夹具。
 *
 * 硬性要求（任务书）：
 *  - **不依赖固定测试数据**：\`ehome_uiux\` 只在本地有，CI/他人机器上数据不同。
 *    需要数据的断言一律用 \`page.route\` 注入自己造的数据。
 *  - **不用固定 \`waitForTimeout\` 等数据**：一律 \`waitForSelector\` / \`expect.poll\`。
 *  - **后端不可用时给清晰错误而不是超时挂死**：先探测健康端点，失败直接 fail 并带出
 *    baseURL 与排查提示（见 \`assertBackendReachable\`）。
 *
 * 2026-09-14 追加（门禁假绿修复）：\`gotoRoute\` 只等"应用外壳挂载"，**不再承担就绪语义**；
 * 数据就绪统一走 \`waitForRouteReady\`（判据见该函数与 \`ROUTES\` 的注释）。
 */
import { expect, request, type Page } from '@playwright/test'
import { measureOverflow, type DocumentOverflow } from './uiux-metrics'

/** 受测前端地址（默认审计实例 8082，可用 UIUX_BASE 覆盖） */
export const UIUX_BASE = process.env.UIUX_BASE || 'http://127.0.0.1:8082'
export const UIUX_USER = process.env.UIUX_USER || 'admin'
export const UIUX_PASS = process.env.UIUX_PASS || 'UiuxAudit2026!'

/**
 * 13 个受审路由（docs/分析/UIUX审计契约-2026-09-13.md §4 的冻结清单）+ **就绪判据**。
 *
 * ## 为什么每条路由要带一个"数据承载信号"（2026-09-14 门禁假绿修复）
 *
 * 旧判据是 `scanned > 50`（结构分母）。主控在 360×800 实测 `/logical-device`：
 *   t+  0ms  {scanned:149, clips:0, rows:0, loadingMask:1, bodyLen:154}   ← 旧判据在这里就认为"就绪"
 *   t+300ms  {scanned:705, clips:12, maxClip:365, rows:20, loadingMask:1}
 *   t+500ms  {scanned:701, clips:12, maxClip:365, rows:20, loadingMask:0}
 * 即"元素够多"完全不等于"布局已终态"：t+0 时表格 0 行、bodyLen=154（只有工具栏和表头）、
 * loadmask 还挂着，而真实裁切 300ms 后才出现。门禁于是在数据未渲染的瞬间取数并判为合规。
 *
 * 更严重的是它**已经造成过真实假绿**：删掉 `.ld-pagination :deep(.el-pagination){flex-wrap:wrap}`
 * 并重新构建后（产物 CSS 中该规则确实消失），旧门禁仍 1 passed，而独立探针在同一份产物上
 * 测得 clips=12 / maxClip=365px / btn-prev x=-118（视口外不可达）。
 *
 * ## 本表使用的信号（每一条都在**真实实例**上用"6s 延迟注入法"验证过：
 *    "已加载态必须命中、数据被 withhold 时不得命中"，2026-09-14 实测）
 *
 *   1) `visible`  **数据承载元素**（必须真实可见：有盒、未 display:none、未 visibility:hidden）——
 *      出现即证明该路由的数据/空态/错误态之一已经进了 DOM；
 *   2) `settle`  **加载结束信号**（模板 v-if/v-else 之后才出现的元素）。它同时排除两类
 *      结构性假信号：陈旧 DOM（rows 从缓存页带回来但内容还是旧的）与加载骨架
 *      （此信号在骨架态必然缺席）。
 *
 * 本表**不改度量原语**（uiux-metrics.ts 已验证在稳定态能测出 12 处裁切），只改"何时测量"。
 */
export interface RouteProbe {
  name: string
  path: string
  /** 路由容器：用于判定"当前渲染的是哪一页"（相对上次快照） */
  root: string
  /** 数据承载信号：出现即可下"有数据/空态/错误态"三种确定结论之一 */
  visible: string
  /** 加载结束信号：模板分支已经脱离 loading */
  settle: string
  /** 该路由期望存在的"数据面"（表格行或数据行容器） */
  dataShaped: boolean
}

export const ROUTES: RouteProbe[] = [
  {
    name: 'dashboard',
    path: '/dashboard',
    root: '.dashboard',
    visible: '.el-table__row, .empty-state, [data-test="dashboard-error"]',
    settle: '.stat-value, .empty-state, [data-test="dashboard-error"]',
    dataShaped: true,
  },
  {
    name: 'node-list',
    path: '/node',
    root: '.collector-page',
    visible: '.collector-card, .collector-table-card, .el-table__row, .empty-state',
    settle: '.stats-row, .empty-state',
    dataShaped: true,
  },
  {
    name: 'channel-list',
    path: '/channel',
    root: '.channel-page',
    visible: '.el-table__row, .empty-state, [data-test="channel-error"]',
    settle: '.el-table, .empty-state, [data-test="channel-error"]',
    dataShaped: true,
  },
  {
    name: 'edge-device-list',
    path: '/edge-device',
    root: '.device-page',
    visible: '.device-card, .empty-state',
    settle: '.stats-row, .empty-state',
    dataShaped: true,
  },
  {
    name: 'logical-device-list',
    path: '/logical-device',
    root: '.logical-device-page',
    visible: '.el-table__row, .empty-state, .el-table__empty-text',
    settle: '.ld-pagination, .el-table__row, .empty-state, .el-table__empty-text',
    dataShaped: true,
  },
  {
    name: 'data-panel',
    path: '/data',
    root: '.data-panel',
    visible: '.empty-state, .el-table__row',
    settle: '.empty-state, .el-table, .mobile-table-wrapper',
    dataShaped: true,
  },
  {
    name: 'data-sources',
    path: '/data-sources',
    root: '.data-source-page',
    visible: '.el-table__row, .empty-state, [data-test="ds-error"]',
    settle: '.ds-table, .empty-state, [data-test="ds-error"]',
    dataShaped: true,
  },
  {
    name: 'firmware',
    path: '/firmware',
    root: '.firmware-manage',
    visible: '.el-table__row, .empty-state, .el-table__empty-text',
    settle: '.el-table, .empty-state, .el-table__empty-text',
    dataShaped: true,
  },
  {
    name: 'device-configs',
    path: '/device-configs',
    root: '.config-page',
    visible: '.config-card, .el-table__row, .empty-state',
    settle: '.stats-row, .empty-state',
    dataShaped: true,
  },
  {
    name: 'monitor',
    path: '/monitor',
    root: '.monitor-container',
    // F31：必须把**错误态**也算作「确定结论」。契约规定确定态有三种：有数据 / 空态 / 错误态。
    // 修复前这里只有 '.stat-value' —— 而 F28 让未知态也渲染 .stat-value（显示 '—'），
    // 于是「加载中」与「已加载」在这一条信号上不可区分（实测：接口挂起 10s 仍判就绪）。
    // 照 /dashboard 的既有范式（'.el-table__row, .empty-state, [data-test="dashboard-error"]'）补齐。
    visible: '.stat-value, [data-test="monitor-error"], [data-test="monitor-detail-error"]',
    settle: '.stat-value, [data-test="monitor-error"], [data-test="monitor-detail-error"]',
    dataShaped: true,
  },
  {
    name: 'alerts',
    path: '/alerts',
    root: '.alert-rules-page',
    visible: '.el-table__row, .el-table__empty-text',
    settle: '.el-table__empty-text, .el-table__row',
    dataShaped: true,
  },
  {
    name: 'automation',
    path: '/automation',
    root: '.automation-rules-page',
    visible: '.el-table__row, .el-table__empty-text',
    settle: '.el-table__empty-text, .el-table__row',
    dataShaped: true,
  },
  {
    name: 'profile',
    path: '/profile',
    root: '.profile-page',
    // 唯一不依赖后端取数的受审路由：它没有列表接口。以"用户名已从 store 渲染"作为
    // 数据承载信号（旧判据下它的风险最低，但不能因此把它排除出稳定态判据）。
    visible: '.profile-page .username',
    settle: '.profile-page .username',
    dataShaped: false,
  },
]

/** 契约 §4 的视口矩阵（1440/1024/768/390/360；门禁取任务书要求的四档） */
export const VIEWPORTS = [
  { name: '1440', width: 1440, height: 900 },
  { name: '768', width: 768, height: 1024 },
  { name: '390', width: 390, height: 844 },
  { name: '360', width: 360, height: 800 },
] as const

/** 规范 §4.4.5：移动端主要任务 ≥44px；极高密度工具栏可降到 36px */
export const TOUCH_MIN_MOBILE = 44
export const TOUCH_MIN_DENSE_TOOLBAR = 36

/** WCAG AA：正文 4.5:1；大字（≥18.66px bold 或 ≥24px）与次要 3.0:1 */
export const CONTRAST_BODY = 4.5
export const CONTRAST_LARGE = 3.0

/**
 * 后端可达性前置检查。
 *
 * 为什么需要：Playwright 默认的 `page.goto` 在服务不可用时会一直等到 actionTimeout，
 * 报出的是 `page.goto: Timeout` 这种与真实原因无关的信息。这里先打健康端点，
 * 用一条能直接指向根因的错误信息提前失败。
 */
export async function assertBackendReachable(): Promise<void> {
  const ctx = await request.newContext({ baseURL: UIUX_BASE, timeout: 8000 })
  try {
    const res = await ctx.get('/api/v1/health')
    if (res.status() !== 200) {
      throw new Error(`健康检查返回 ${res.status()}`)
    }
  } catch (err) {
    throw new Error(
      `后端不可达：${UIUX_BASE}/api/v1/health —— ${String(err).split('\n')[0]}\n` +
        '这是 UI/UX 回归门禁的前置依赖（前端静态产物由该后端同源托管）。\n' +
        '排查：1) 后端是否在运行；2) UIUX_BASE 是否指向正确地址；' +
        '3) 若你自己的实例在别的端口，用 UIUX_BASE=http://127.0.0.1:<port> 运行。'
    )
  } finally {
    await ctx.dispose()
  }
}

/**
 * 用真实登录 API 换 token，再注入 localStorage 打开受保护页面。
 *
 * 为什么不用 UI 填表登录：每个 project × 每条用例都走一遍登录表单会让门禁慢一个量级，
 * 且登录页自身的变化会污染无关断言。这里只把"已登录"当作前置条件。
 * token 仍来自**真实** `/api/v1/auth/login`，不是伪造字符串。
 */
export async function loginViaApi(page: Page, theme: 'light' | 'dark' = 'light'): Promise<void> {
  const ctx = await request.newContext({ baseURL: UIUX_BASE, timeout: 10000 })
  try {
    const res = await ctx.post('/api/v1/auth/login', {
      data: { username: UIUX_USER, password: UIUX_PASS },
    })
    if (!res.ok()) {
      throw new Error(
        `登录失败：HTTP ${res.status()}（用户 ${UIUX_USER}）—— 凭据或库 ehome_uiux 可能未就绪`
      )
    }
    const body = (await res.json()) as { data?: { token?: string; user?: unknown } }
    const token = body.data?.token
    if (!token) throw new Error('登录响应里没有 token，无法注入会话')
    const user = JSON.stringify(body.data?.user ?? { id: 1, username: UIUX_USER })
    await page.addInitScript(
      ([t, u, th]) => {
        localStorage.setItem('token', t)
        localStorage.setItem('user', u)
        localStorage.setItem('theme', th)
      },
      [token, user, theme] as const
    )
  } finally {
    await ctx.dispose()
  }
}

/** 打开路由并等到"应用外壳已挂载"（不依赖数据是否返回；数据就绪见 waitForRouteReady） */
export async function gotoRoute(page: Page, path: string): Promise<void> {
  await page.goto(UIUX_BASE + path, { waitUntil: 'domcontentloaded' })
  // 任何受保护页都会先挂出主布局；以此作为"应用已启动"的信号，而非等具体数据
  await page.waitForSelector('.main-layout, .main-content, .el-card, .el-table, .empty-state, .el-loading-mask, .el-skeleton', {
    timeout: 20000,
  })
}

/**
 * 就绪判据所需的**结构事实**（都是廉价 DOM 读数，不含任何裁切度量逻辑）。
 *
 * 度量本身**不做第二次实现**：真正取数时直接调用 uiux-metrics.ts 的 \`measureOverflow\`
 * （见下面的 \`page.evaluate(measureOverflow)\`）—— 与修复前的门禁是**同一个代码路径**，
 * 所以"探针自校准"用例证明的就是门禁实际使用的那个度量。
 */
export interface StructuralFacts {
  /** 当前路由容器（.dashboard/.collector-page…），用于发现"还没换页" */
  root: string
  /** 可见的加载遮罩数（>0 表示还在取数） */
  masks: number
  /** 可见的骨架屏项数（>0 表示"结构性渲染中"，不是数据） */
  skeletons: number
  /** 表中已渲染的行数（分母守卫 + 陈旧态判据） */
  rows: number
  /** 已确认的空态元素数（.empty-state / EP 表格空态文案） */
  empties: number
  /** 可见的数据承载元素数（三种确定结论之一的证据） */
  signal: number
  /** 加载结束信号是否可见（模板已脱离 loading 分支） */
  settle: boolean
  /** 数据表格数（用于"该有意义的分母是 0"的上报） */
  tables: number
  /**
   * 「请求在飞」标记数（F31 残余）。
   *
   * 为什么必须有：仅靠「骨架/错误态/数据」三态**无法**覆盖「已有陈旧数据的刷新中」——
   * 那种页面既无骨架也无错误态，`.stat-value` 显示上一次成功的旧值，看起来可信。
   * 产品侧因此暴露了 `aria-busy` + `[data-test=monitor-refreshing]`（见 Monitor.vue 根容器）。
   * 精确匹配 `"true"`：Vue 布尔 attr 在 false 时渲染字面量 `"false"`，存在性判断会恒真。
   */
  inFlight: number
}

/** 在页内采一次结构事实（不度量裁切） */
async function structuralFacts(page: Page, probe: RouteProbe): Promise<StructuralFacts> {
  return page.evaluate(
    (p: { root: string; visible: string; settle: string }) => {
      const visible = (el: Element): boolean => {
        const r = el.getBoundingClientRect()
        if (r.width <= 0 || r.height <= 0) return false
        const cs = getComputedStyle(el)
        return cs.visibility !== 'hidden' && cs.display !== 'none'
      }
      const countVisible = (sel: string): number =>
        Array.from(document.querySelectorAll(sel)).filter(visible).length
      const cur = document.querySelector(p.root)
      const cls = cur && typeof cur.className === 'string' ? cur.className : ''
      return {
        root: cls.split(' ')[0] || '',
        masks: countVisible('.el-loading-mask'),
        // F31：骨架判定必须同时覆盖**本仓自研**骨架，而不只是 EP 的 .el-skeleton。
        // 为什么：/monitor 的首屏骨架用的是 src/components/common/SkeletonCard.vue，
        // 它渲染 .skeleton-card（variant 全挂在同一根节点，故单选择器覆盖全部变体）；
        // 详情区顶层另用 .detail-skeleton（即 [data-test="monitor-loading"] 所在节点）。
        // 修复前只认 .el-skeleton ⇒ 接口挂起时实测 skeletons=0、blockers=[]，门禁在
        // **只有骨架的 DOM**（scanned=181 vs 真实数据 295）上做了裁切判定 —— 真实假绿。
        // 其余自研骨架（.logical-info-skeleton/.candidate-skeleton/.skeleton-grid）一并纳入，
        // 避免同类盲区在别的路由复现。
        skeletons: countVisible(
          '.el-skeleton, .skeleton-card, .detail-skeleton, .logical-info-skeleton, .candidate-skeleton, .skeleton-grid'
        ),
        rows: document.querySelectorAll('.el-table__row').length,
        empties: countVisible('.empty-state, .el-table__empty-text, .el-table__empty-block'),
        signal: countVisible(p.visible),
        settle: Array.from(document.querySelectorAll(p.settle)).filter(visible).length > 0,
        tables: document.querySelectorAll('.el-table').length,
        // F31 残余：**请求在飞**的通用信号。
        //
        // 为什么需要它（此前是盲区）：「首屏成功后刷新时挂起」场景下，页面既无骨架也无错误态，
        // .stat-value 显示的是**上一次成功的陈旧值**（实测 153.68K），看起来完全可信 ——
        // 门禁会判为「已就绪」并在这份**不代表终态**的 DOM 上做裁切判定。
        //
        // 判据来源：产品侧已在 /monitor 根容器上暴露 aria-busy（标准属性）+ data-test
        // （本仓稳定约定）。**精确匹配 "true"** —— Vue 对布尔 attr 在 false 时会渲染字面量
        // "false"，用存在性判断会恒真。
        inFlight: countVisible('[aria-busy="true"], [data-test="monitor-refreshing"]'),
      }
    },
    { root: probe.root, visible: probe.visible, settle: probe.settle }
  )
}

/**
 * 未进入确定态的原因清单（同时把**分母**带出来，契约 §2.3：
 * scanned/tables/rows 三个计数必须能回答"分母是多少"）。
 */
function readyBlockers(f: StructuralFacts, probe: RouteProbe): string[] {
  const why: string[] = []
  if (f.root !== probe.root.slice(1)) {
    why.push('路由容器未切换（期望 ' + probe.root + '，实测 .' + (f.root || '(无)') + '）')
  }
  if (f.signal <= 0) why.push('数据承载信号不可见（' + probe.visible + '）—— 还没有"有数据/空态/错误态"的结论')
  if (f.masks > 0) why.push('仍有 ' + f.masks + ' 个可见加载遮罩 .el-loading-mask')
  if (f.skeletons > 0) why.push('仍有 ' + f.skeletons + ' 个可见骨架屏 .el-skeleton')
  if (!f.settle) why.push('加载结束信号未出现（' + probe.settle + '）')
  if (f.inFlight > 0) {
    why.push('仍有 ' + f.inFlight + ' 个「请求在飞」标记（[aria-busy="true"]/[data-test=monitor-refreshing]）')
  }
  if (f.rows > 0 && (f.masks > 0 || f.skeletons > 0)) {
    why.push('表格行已出现但仍在加载（陈旧 DOM / 中间态）')
  }
  return why
}

/** 把一次结构事实写成一行可复核的实测值（失败信息里必须能自证分母非 0） */
function describeFacts(f: StructuralFacts, probe: RouteProbe): string {
  return (
    'tables=' + f.tables + ' rows=' + f.rows + ' empties=' + f.empties +
    ' loadingMask=' + f.masks + ' skeleton=' + f.skeletons +
    ' dataSignal=' + f.signal + ' settleSignal=' + f.settle +
    ' root=.' + (f.root || '(无)') + ' path=' + probe.path
  )
}

/** 把一次裁切度量写成一行（含契约 §2.3 要求的分母 scanned） */
function describeMeasure(m: DocumentOverflow | null): string {
  if (!m) return '(未取到度量)'
  return (
    'scanned=' + m.scanned + ' clips=' + m.clips.length + ' overflow=' + m.overflow +
    ' path=' + m.path
  )
}

/**
 * 等到**这一条**路由进入确定态（三种确定结论之一：有数据 / 空态 / 错误态）。
 *
 * 为什么不能用 scanned > 50（旧判据，已造成真实假绿）：见 ROUTES 上方注释。
 *
 * 判据分两阶段，缺一不可：
 *   **阶段 1 · 有结论**：数据承载信号可见（probe.visible）——证明数据/空态/错误态之一
 *     已经进了 DOM，而不是"页面骨架搭好了但一个字都没渲染"；且取数已结束
 *     （可见 .el-loading-mask 与 .el-skeleton 都归零）；且 probe.settle 可见
 *     （模板已脱离 v-if="loading" 分支）；且路由容器已切到本路由。
 *   **阶段 2 · 布局已终态**：连续两次 \`measureOverflow\` 的 \`scanned\` 与 \`clips.length\`
 *     一致（本轮实测证明"元素数够多"不代表"布局已终态"，所以必须显式要求稳定）。
 *
 * 超时仍不满足 ⇒ **抛出**（用例失败），绝不允许退回"测得 0 处裁切所以通过"。
 */
export async function waitForRouteReady(
  page: Page,
  probe: RouteProbe,
  timeout = 20000
): Promise<DocumentOverflow> {
  const started = Date.now()
  let lastFacts: StructuralFacts | null = null

  // ── 阶段 1：数据承载信号 + 取数结束 + 加载相越过 ─────────────────────────
  try {
    await expect
      .poll(
        async () => {
          lastFacts = await structuralFacts(page, probe)
          return readyBlockers(lastFacts, probe).length === 0
        },
        {
          message:
            '页面未进入确定态：' + probe.name + ' ' + probe.path +
            ' 未拿到"有数据 / 空态 / 错误态"三者之一的结论（等待 ' + timeout + 'ms）',
          timeout,
          intervals: [100, 200, 500],
        }
      )
      .toBe(true)
  } catch {
    // 失败信息里必须给出**分母**（契约 §2.3）：scanned 与 tables/rows 一起自证"确实扫到了东西"
    const lastMeasure = await readOverflow(page).catch(() => null)
    throw new Error(
      '页面未进入确定态（阶段 1 超时 ' + timeout + 'ms）：' + probe.name + ' ' + probe.path + '\n' +
        '  未满足的判据：' + (lastFacts ? readyBlockers(lastFacts, probe).join('；') : '(一次都没采到)') + '\n' +
        '  最后度量（分母）：' + describeMeasure(lastMeasure) + '\n' +
        '  最后结构事实：' + (lastFacts ? describeFacts(lastFacts, probe) : '(无)') + '\n' +
        '  ⚠️ 门禁纪律（契约 §2.3 / 2026-09-14 假绿修复）：拿不到数据承载信号时**必须失败**，' +
        '不得退回"测得 0 处裁切所以通过"——"没有结论"不是"合规"。'
    )
  }

  // ── 阶段 2：布局稳定（连续两次 measured scanned 与 clips.length 一致）────
  const remaining = Math.max(1500, timeout - (Date.now() - started))
  let prev: DocumentOverflow | null = null
  let stable: DocumentOverflow | null = null
  try {
    await expect
      .poll(
        async () => {
          const facts = await structuralFacts(page, probe)
          const terminal = readyBlockers(facts, probe).length === 0
          const cur = terminal ? await readOverflow(page) : null
          const same =
            !!cur && !!prev && cur.scanned === prev.scanned && cur.clips.length === prev.clips.length
          stable = same ? cur : null
          prev = cur
          return same
        },
        {
          message:
            '布局未稳定：' + probe.name + ' ' + probe.path +
            ' 连续两次度量（scanned / clips.length）不一致 —— 不能在过渡帧上判定合规',
          timeout: remaining,
          intervals: [100, 150],
        }
      )
      .toBe(true)
  } catch {
    throw new Error(
      '布局未稳定（阶段 2 超时 ' + remaining + 'ms）：' + probe.name + ' ' + probe.path + '\n' +
        '  最后一次度量：' + describeMeasure(prev) + '\n' +
        '  ⚠️ "元素数够多"不等于"布局已终态"：本轮实测中真实裁切在元素数达标之后才出现。'
    )
  }

  return stable!
}

/** 在页内取一次裁切度量（**复用** uiux-metrics.ts 的原语）。
 *  cast 只为满足 Playwright 的 PageFunction 签名（\`(arg: void) => R\`）；运行的就是原函数本身。 */
export function readOverflow(page: Page): Promise<DocumentOverflow> {
  return page.evaluate(measureOverflow as unknown as () => DocumentOverflow)
}

/** 路由级就绪 + 度量（gotoRoute + waitForRouteReady 的组合，供表格类用例复用） */
export async function measureRouteWhenReady(
  page: Page,
  probe: RouteProbe,
  timeout = 20000
): Promise<DocumentOverflow> {
  await gotoRoute(page, probe.path)
  return waitForRouteReady(page, probe, timeout)
}

/** 让 /dashboard 的概览接口返回 500（U-1 的核心注入点） */
export async function mockOverviewFailure(page: Page, status = 500): Promise<void> {
  await page.route('**/api/v1/overview**', (route) =>
    route.fulfill({
      status,
      contentType: 'application/json',
      body: JSON.stringify({ code: status, message: 'injected-failure' }),
    })
  )
}

/** 让某列表接口返回 500 */
export async function mockListFailure(page: Page, urlPattern: string, status = 500): Promise<void> {
  await page.route(urlPattern, (route) =>
    route.fulfill({
      status,
      contentType: 'application/json',
      body: JSON.stringify({ code: status, message: 'injected-failure' }),
    })
  )
}

/** 造 N 行逻辑设备（D2-02：1003 行曾无分页） */
export function logicalDevicePage(total: number, size = 20, page = 1) {
  const start = (page - 1) * size
  const items = Array.from({ length: Math.min(size, Math.max(total - start, 0)) }, (_, i) => ({
    id: start + i + 1,
    identity_key: `jiabaida_bms:mock-${start + i + 1}`,
    name: `MOCK-LD-${String(start + i + 1).padStart(4, '0')}`,
    device_type: 'jiabaida_bms',
    retention_days: 365,
    merged_into: null,
    merge_status: null,
    purge_requested: false,
    created_at: '2026-09-01T00:00:00+08:00',
    updated_at: '2026-09-01T00:00:00+08:00',
    instance_count: 1,
    row_estimate: 1234,
    last_data_at: '2026-09-13T00:00:00Z',
  }))
  return { code: 200, message: 'ok', data: { items, page, page_size: size, total } }
}

/** 造 N 行自动化事件（D3 #2：500 行 / 11902 节点曾无分页） */
export function automationEventPage(total: number, size = 20, page = 1) {
  const start = (page - 1) * size
  const items = Array.from({ length: Math.min(size, Math.max(total - start, 0)) }, (_, i) => ({
    id: total - (start + i),
    rule_id: 1,
    triggered_at: '2026-09-13T10:00:00+08:00',
    trigger_value: 12,
    trigger_source: 'auto',
    result: 'executed',
    command_id: '4aa9a48b-7a13-4499-925a-2fdf7b2a2446',
    created_at: '2026-09-13T10:00:00+08:00',
  }))
  return { code: 200, message: 'ok', data: { items, page, page_size: size, total } }
}

/**
 * 断言：等一个条件成立，失败时把实测样本带进错误信息。
 * 用 `expect.poll` 而不是 `waitForTimeout`（任务书硬性要求）。
 */
export async function pollUntil<T>(
  fn: () => Promise<T>,
  predicate: (value: T) => boolean,
  message: string,
  timeout = 10000
): Promise<T> {
  let last: T
  await expect
    .poll(
      async () => {
        last = await fn()
        return predicate(last)
      },
      { message, timeout, intervals: [100, 200, 500] }
    )
    .toBe(true)
  return last!
}
