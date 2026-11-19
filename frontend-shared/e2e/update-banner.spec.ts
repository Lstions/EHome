import { test, expect, type Page, type Route } from '@playwright/test'
import { loginAsAdmin } from './helpers/login'

/**
 * E2E-04 … E2E-10：旧页面更新提醒（契约 §6.4 / 附录 D）
 *
 * ── 运行前提 ───────────────────────────────────────────────────────────────
 * 1. 后端在本机 8080（或 E2E_BASE 指定），同源托管**构建产物**。
 *    `vite dev` 没有注入 `VITE_BUILD_ID`，按设计「无法比对 ⇒ 不提示」，
 *    此时用例显式 skip（不是假绿）：`window.__EHOME_BUILD__.id` 为空即跳过。
 * 2. 账号默认 admin/admin123（E2E_USER / E2E_PASSWORD 可覆盖）。
 * 3. 全部接口用 page.route 注入，**不写任何业务数据**。
 *
 * ── 为什么 mock 必须同时改「响应头」（2026-11-18 D4 实测）──────────────────
 * 更新检测的通道 1 是**每一个** /api 响应上的 `X-EHome-Build` 头比对。
 * 只改 `/system/version` 的 body、却让其它响应头保持真实值，等于
 * 「版本端点刚说部署了新版本 → 下一个接口又说 build 没变」，
 * 状态会被立刻复位成 same、横幅被卸载，后续点击报 detached from DOM。
 * 这是**测试替身不完整**，不是产品缺陷 —— 因此这里统一用 {@link mockServerBuild}
 * 把「服务端当前 build」整体改口（版本端点 body + 所有 /api 响应头同值）。
 */

const BASE = process.env.E2E_BASE || 'http://127.0.0.1:8080'
const VERSION_PATH_MARK = '/system/version'
const BUILD_HEADER = 'x-ehome-build'

/** 与被测页面必然不同（只要求「不同」，不要求格式）。 */
const NEW_BUILD = 'e2e-server-build-new'
const NEW_BUILD_TIME = '2099-01-01T00:00:00Z'

interface ServerBuildMock {
  version: string
  build_id: string
  commit?: string
  build_time?: string
  notes?: string[]
}

interface MockContext {
  /** 第几次请求 /system/version（1 = 首次加载，2 = 刷新后……）。 */
  versionCalls: number
}

/**
 * 只识别「拦截被导航 / 页面关闭打断」这一类错误 —— 不做无差别 `catch {}`。
 *
 * 2026-11-18 D6 实测（同一份 spec 连跑 7 次，3 次随机红）：
 * 页面刷新/导航会打断在途请求，Playwright 抛两类错误：
 *   · `route.fulfill: Fetch response has been disposed`（响应体已随导航作废）
 *   · `route.fulfill: Route is already handled!`（同一 route 被并发/上一次处理）
 * 这两类**都发生在请求已经离开产品代码之后**，与产品行为无关；
 * 而「真实回归」的形态完全不同（header 值非法、fulfill 参数非法、断言写错等）。
 * 所以用**窄匹配**只吞前两类：其余错误原样抛出，保证用例不会因为一个兜底 catch
 * 变成"永远绿"（这正是本仓反复吃过的亏）。
 */
const ROUTE_INTERRUPTED_PATTERNS = [
  'has been disposed',
  'route is already handled',
  'request is already handled',
  'target page, context or browser has been closed',
  'page has been closed',
]

function isRouteInterruptedError(error: unknown): boolean {
  const message = error instanceof Error ? error.message : String(error)
  const lower = message.toLowerCase()
  return ROUTE_INTERRUPTED_PATTERNS.some((pattern) => lower.includes(pattern))
}

/**
 * 把「服务端当前 build」整体改口：
 * · `GET /api/v1/system/version` → 给定快照（body 与 `X-EHome-Build` **同值**）；
 * · 其它 `/api/*` → 透传真实响应，但把 `X-EHome-Build` 改写成同一个 build id。
 *
 * 传函数可以表达「刷新之后已经是新版本」这类时序（按 versionCalls 分支）。
 */
async function mockServerBuild(
  page: Page,
  resolve: ServerBuildMock | ((ctx: MockContext) => ServerBuildMock),
): Promise<void> {
  await page.unroute('**/api/**').catch(() => undefined)
  let versionCalls = 0

  await page.route('**/api/**', async (route: Route) => {
    const isVersion = route.request().url().includes(VERSION_PATH_MARK)
    if (isVersion) versionCalls += 1
    const payload = typeof resolve === 'function' ? resolve({ versionCalls }) : resolve

    if (isVersion) {
      await route.fulfill({
        status: 200,
        headers: {
          'content-type': 'application/json',
          'cache-control': 'no-store',
          [BUILD_HEADER]: payload.build_id,
        },
        body: JSON.stringify({ code: 200, message: 'ok', data: payload }),
      })
      return
    }

    // 其它接口：保留真实响应，但把「服务端 build」改写成本次 mock 的值。
    //
    // ⚠️ 两次 try 不是"为了绿而吞错"：页面刷新/导航会打断在途请求（D6 实测的两类错误，
    // 见 {@link isRouteInterruptedError}），此时 route 已不可用；只吞这两类，
    // 其它错误（网络不通、参数非法、产品侧真实故障）一律冒泡，用例才不会被兜底掩盖。
    let response
    try {
      response = await route.fetch()
    } catch (error) {
      if (!isRouteInterruptedError(error)) throw error
      // 导航打断：尽量把请求交还给浏览器；此时 route 往往也已失效
      await route.continue().catch(() => undefined)
      return
    }
    try {
      await route.fulfill({
        response,
        headers: { ...response.headers(), [BUILD_HEADER]: payload.build_id },
      })
    } catch (error) {
      // 只吞「fulfill 时响应/路由已随导航失效」这一类
      if (!isRouteInterruptedError(error)) throw error
    }
  })
}

function newBuildPayload(patch: Partial<ServerBuildMock> = {}): ServerBuildMock {
  return {
    version: '9.9.9',
    build_id: NEW_BUILD,
    commit: 'e2e-commit',
    build_time: NEW_BUILD_TIME,
    notes: ['E2E 变更条目'],
    ...patch,
  }
}

/** 读取构建产物注入的身份（main.ts 的 window.__EHOME_BUILD__）。 */
async function pageBuildId(page: Page): Promise<string> {
  return page.evaluate(() => {
    const build = (window as unknown as { __EHOME_BUILD__?: { id?: string } }).__EHOME_BUILD__
    return build?.id || ''
  })
}

/**
 * 前置：被测页面必须是**构建产物**（`window.__EHOME_BUILD__.id` 非空）。
 *
 * `vite dev` 按设计不注入 build id（否则前后端 id 天然不同源会常驻横幅），
 * 此时更新检测整体关闭 —— 用 `test.skip` 会把它变成「静默变绿」，
 * 所以这里用**断言**：在不该跑的环境里跑就大声失败，并直接告诉人怎么跑。
 */
async function requireBuiltPage(page: Page): Promise<string> {
  const buildId = await pageBuildId(page)
  expect(
    buildId,
    '被测站点必须是构建产物（window.__EHOME_BUILD__.id 为空）。vite dev 下更新检测按设计关闭，' +
      '请用 dist（pnpm build）+ 后端同源托管，或 E2E_BASE 指向已构建站点。',
  ).toBeTruthy()
  return buildId
}

/** 横幅可见（唯一入口：业务提示条本身）。 */
function updateBanner(page: Page) {
  return page.getByTestId('update-banner')
}

/** SPA 内换页（不整页重载）——这样触发的 API 响应才会走通道 1。 */
async function navigateInApp(page: Page, menuText: string): Promise<void> {
  await page.locator('.el-menu-item', { hasText: menuText }).first().click()
  await page.waitForTimeout(800)
}

test.describe('旧页面更新提醒（E2E-04..10）', () => {
  test.beforeEach(async ({ page }) => {
    // 登录按钮是 el-button + 字面量「登 录」（无 native-type）⇒ 必须按可访问名匹配。
    await loginAsAdmin(page, { baseURL: BASE })
  })

  test('E2E-04 旧页面收到更新提示：横幅出现后刷新进入新 build', async ({ page }) => {
    const localBuild = await requireBuiltPage(page)

    // 「刷新后已经是新版本」用可变开关表达，而不是「先改 mock 再点刷新」：
    // 后者会让**当前页**后续的 /api 响应头立刻变回本页 build ⇒ stale 被复位、横幅被卸载，
    // 点刷新时就 detached from DOM。这里在点击之后才翻转开关，新文档的首次
    // /system/version 必然发生在 click() resolve 之后（要经过整页加载 + JS 启动）。
    let refreshed = false
    await mockServerBuild(page, () =>
      refreshed
        ? { version: '1.0.0', build_id: localBuild, build_time: '2020-01-01T00:00:00Z', notes: [] }
        : newBuildPayload({ notes: ['E2E-04 变更条目'] }),
    )
    await page.reload()

    await expect(updateBanner(page)).toBeVisible({ timeout: 20_000 })
    await expect(updateBanner(page)).toContainText('系统已发布新版本')

    const reloaded = page.waitForEvent('load')
    await page.getByTestId('update-refresh').click()
    refreshed = true
    await reloaded

    // 新页面拿到的就是它自己的 build ⇒ 横幅不再出现（AC-13「刷新即恢复」）
    await expect(page.locator('.el-menu-item').first()).toBeVisible({ timeout: 20_000 })
    await expect(updateBanner(page)).toHaveCount(0)
  })

  test('E2E-05 部署回滚（服务端 build_time 更旧）→ 不出现横幅', async ({ page }) => {
    const localBuild = await requireBuiltPage(page)

    await mockServerBuild(page, {
      version: '0.0.1',
      build_id: 'e2e-rollback-build',
      build_time: '2000-01-01T00:00:00Z',
      notes: ['回滚说明'],
    })
    await page.reload()
    // 先确认页面确实跑起来了（否则 waitForTimeout 之后「无横幅」是假绿）
    await expect(page.locator('.el-menu-item').first()).toBeVisible({ timeout: 20_000 })
    await page.waitForTimeout(3000)
    await expect(updateBanner(page)).toHaveCount(0)
  })

  test('E2E-06 活跃页面（持续发 API）部署后，下一次响应即出现横幅', async ({ page }) => {
    const localBuild = await requireBuiltPage(page)

    // ① 未部署：服务端 build == 本页 build
    await mockServerBuild(page, {
      version: '1.0.0',
      build_id: localBuild,
      build_time: '2020-01-01T00:00:00Z',
      notes: [],
    })
    await page.reload()
    await expect(page.locator('.el-menu-item').first()).toBeVisible({ timeout: 20_000 })
    await expect(updateBanner(page)).toHaveCount(0)

    // ② 部署：版本端点改口 + **所有** /api 响应头同步改口（通道 1 的真实触发条件）
    await mockServerBuild(page, newBuildPayload({ notes: [] }))

    // ③ 一次 SPA 换页即产生 API 响应 ⇒ 通道 1 触发
    //    注意：若这次触发落在 5s 最小间隔内，store 会在边界**尾沿补跑**（不丢），
    //    所以最迟 5s 后横幅必然出现 —— 这正是 E2E-06 要守的「不漏提示」。
    await navigateInApp(page, '节点')
    await expect(updateBanner(page)).toBeVisible({ timeout: 20_000 })
  })

  test('E2E-07 篡改懒加载 chunk 名触发 404 → 不可推迟的刷新模态', async ({ page }) => {
    await requireBuiltPage(page)

    // ⚠️ 不能在 arm 404 之后 page.goto：整页重载时入口 chunk 也 404，应用起不来。
    // 正确做法：应用已就绪 → 让后续懒加载 chunk 404 → SPA 内点路由。
    await page.waitForSelector('.el-menu-item', { timeout: 20_000 })
    await page.route(/\/assets\/.*\.js$/, (route: Route) =>
      route.fulfill({ status: 404, contentType: 'text/plain', body: 'not found' }),
    )
    await navigateInApp(page, '固件管理')

    await expect(page.getByTestId('update-blocked-body')).toBeVisible({ timeout: 20_000 })
    await expect(page.getByTestId('update-blocked-refresh')).toBeVisible()
    // 不可推迟：没有「稍后」
    await expect(page.getByTestId('update-later')).toHaveCount(0)
  })

  test('E2E-08 点「稍后」后继续操作不再主动提示；chunk 失败仍强提示', async ({ page }) => {
    await requireBuiltPage(page)

    await mockServerBuild(page, newBuildPayload({ notes: ['E2E-08 变更条目'] }))
    await page.reload()
    await expect(updateBanner(page)).toBeVisible({ timeout: 20_000 })
    await page.getByTestId('update-later').click()
    await expect(updateBanner(page)).toHaveCount(0)

    // 同一 build 下继续操作（响应头也一直是同一 build）：不得再主动提示
    await navigateInApp(page, '仪表盘')
    await navigateInApp(page, '系统监控')
    await expect(updateBanner(page)).toHaveCount(0)

    // 但 chunk 加载失败仍然强提示（blocked 不可被「稍后」压制）
    await page.route(/\/assets\/.*\.js$/, (route: Route) =>
      route.fulfill({ status: 404, contentType: 'text/plain', body: 'not found' }),
    )
    await navigateInApp(page, '固件管理')
    await expect(page.getByTestId('update-blocked-body')).toBeVisible({ timeout: 20_000 })
  })

  test('E2E-09 部署后点「查看更新内容」→ 弹窗列出全部 notes，并可立即刷新', async ({ page }) => {
    await requireBuiltPage(page)

    const notes = ['固件上传改为必填 sidecar', '固件闸门新增节点硬件档案校验', '旧页面提醒刷新']
    await mockServerBuild(page, newBuildPayload({ notes }))
    await page.reload()
    await expect(updateBanner(page)).toBeVisible({ timeout: 20_000 })

    await page.getByTestId('update-notes-link').click()
    const items = page.getByTestId('release-note-item')
    await expect(items).toHaveCount(notes.length)
    for (let i = 0; i < notes.length; i++) {
      await expect(items.nth(i)).toHaveText(notes[i])
    }
    await expect(page.getByTestId('release-notes-refresh')).toBeVisible()
  })

  test('E2E-10 notes 为空 → 两处都不出现「查看更新内容」，无空弹窗', async ({ page }) => {
    await requireBuiltPage(page)

    await mockServerBuild(page, newBuildPayload({ notes: [] }))
    await page.reload()
    await expect(updateBanner(page)).toBeVisible({ timeout: 20_000 })

    // ① 横幅上不得有「查看更新内容」
    await expect(page.getByTestId('update-notes-link')).toHaveCount(0)
    // ② 侧栏「关于」入口也不得出现（版本号退回纯文本）
    await expect(page.getByTestId('about-entry-sidebar')).toHaveCount(0)
    // ③ 任何情况下都不该出现空弹窗
    await expect(page.getByTestId('release-notes-body')).toHaveCount(0)
  })
})
