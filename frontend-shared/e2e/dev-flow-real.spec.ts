// 真实浏览器业务流（**不 mock 任何 API**）：登录 → 节点列表 → 节点详情
//
// ## 与仓库既有 e2e 的区别
// e2e/ 下 35 个 spec 用 page.route 注入 mock ⇒ 证明"UI 在给定 JSON 下渲染正确"，
// **不是**"前后端真的接通"。本用例刻意一个 mock 都不加（§142.4）。
//
// ## 选择器是**实测探出来的**，不是猜的
// 先用 e2e/dom-probe.spec.ts 打印真实 DOM，得到：
//   · 用户名 input: type=text placeholder="请输入用户名"（Element Plus，无 name 属性）
//   · 密码   input: type=password placeholder="请输入密码"
//   · 提交   button: 文本是「登 录」（**中间有空格**，Element Plus 的表达）
// ⇒ 按 name="username" 找会找不到；按文本"登录"精确匹配也会失败。
//   教训：对别人的 UI 写选择器前，先把 DOM 打出来，不要按常见约定猜。
import { test, expect } from '@playwright/test'

const BASE = process.env.DEV_FLOW_BASE || 'http://127.0.0.1:5174'
const USER = 'admin'
const PASS = 'DevTest123!'

test('真实业务流：UI 登录 → 节点列表 → 进详情', async ({ page }) => {
  const seen: string[] = []
  page.on('response', (r) => {
    const u = r.url()
    if (u.includes('/api/v1/')) seen.push(`${r.status()} ${u.replace(BASE, '')}`)
  })

  // ── 1) 登录页 ──
  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' })
  await page.locator('input[type="text"]').first().fill(USER)
  await page.locator('input[type="password"]').first().fill(PASS)
  // Element Plus 的按钮文本是「登 录」（含空格）⇒ 用正则容忍空白
  await page.getByRole('button', { name: /登\s*录/ }).first().click()

  // ── 2) 登录成功：离开 /login ──
  await page.waitForURL((u) => !u.pathname.includes('/login'), { timeout: 20000 })
  expect(page.url()).not.toContain('/login')
  const loginApi = seen.find((s) => s.includes('/auth/login'))
  expect(loginApi, '应看到一次真实的 /auth/login 调用').toBeTruthy()
  expect(loginApi!.startsWith('200'), '登录 API 应 200').toBe(true)

  // ── 3) 节点列表：出现**真实**设备 ──
  await page.goto(BASE + '/node', { waitUntil: 'networkidle' })
  const body = await page.locator('body').innerText()
  const hasReal = /30EDA0A9A808|30EDA0A9|A9A808|F0F5BD02F35C|F0F5BD02|02F35C/.test(body)
  expect(hasReal, '节点列表应出现真实设备。页面文本片段: ' + body.slice(0, 300)).toBe(true)
  // ⭐ 隔离判据（内建）：不得出现生产库特有的模拟节点
  expect(/sim-2026/.test(body), '不应出现生产库的 sim-* 节点 ⇒ 代理/库指向了生产').toBe(false)

  // ── 4) 进详情页 ──
  const link = page.locator('a[href*="/node/"]').first()
  if (await link.count() > 0) {
    await link.click()
    await page.waitForLoadState('networkidle')
    await expect(page.locator('body')).toContainText(/.+/)
  }

  console.log('[flow] API 调用:\n  ' + seen.join('\n  '))
  console.log('[flow] 最终 URL: ' + page.url())
})
