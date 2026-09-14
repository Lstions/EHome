/**
 * UI/UX 回归门禁的确定性夹具。
 *
 * 硬性要求（任务书）：
 *  - **不依赖固定测试数据**：`ehome_uiux` 只在本地有，CI/他人机器上数据不同。
 *    需要数据的断言一律用 `page.route` 注入自己造的数据。
 *  - **不用固定 `waitForTimeout` 等数据**：一律 `waitForSelector` / `expect.poll`。
 *  - **后端不可用时给清晰错误而不是超时挂死**：先探测健康端点，失败直接 fail 并带出
 *    baseURL 与排查提示（见 `assertBackendReachable`）。
 */
import { expect, request, type Page } from '@playwright/test'

/** 受测前端地址（默认审计实例 8082，可用 UIUX_BASE 覆盖） */
export const UIUX_BASE = process.env.UIUX_BASE || 'http://127.0.0.1:8082'
export const UIUX_USER = process.env.UIUX_USER || 'admin'
export const UIUX_PASS = process.env.UIUX_PASS || 'UiuxAudit2026!'

/** 13 个受审路由（docs/分析/UIUX审计契约-2026-09-13.md §4 的冻结清单） */
export const ROUTES: Array<{ name: string; path: string }> = [
  { name: 'dashboard', path: '/dashboard' },
  { name: 'node-list', path: '/node' },
  { name: 'channel-list', path: '/channel' },
  { name: 'edge-device-list', path: '/edge-device' },
  { name: 'logical-device-list', path: '/logical-device' },
  { name: 'data-panel', path: '/data' },
  { name: 'data-sources', path: '/data-sources' },
  { name: 'firmware', path: '/firmware' },
  { name: 'device-configs', path: '/device-configs' },
  { name: 'monitor', path: '/monitor' },
  { name: 'alerts', path: '/alerts' },
  { name: 'automation', path: '/automation' },
  { name: 'profile', path: '/profile' },
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

/** 打开路由并等到"页面骨架已渲染"（不依赖数据是否返回） */
export async function gotoRoute(page: Page, path: string): Promise<void> {
  await page.goto(UIUX_BASE + path, { waitUntil: 'domcontentloaded' })
  // 任何受保护页都会先挂出主布局；以此作为"应用已启动"的信号，而非等具体数据
  await page.waitForSelector('.main-layout, .main-content, .el-card, .el-table, .empty-state', {
    timeout: 20000,
  })
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
