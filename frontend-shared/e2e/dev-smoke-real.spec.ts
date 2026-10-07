// 本地联调冒烟：前端(5174) → dev 后端(8081) → dev 库(5434)
// ⚠ 本项目原有 e2e 大量使用 page.route 注入 mock（35 个 spec），
//   那证明的是"UI 在给定 JSON 下渲染正确"，**不是**"前后端真的接通"。
//   本用例刻意**不 mock**，走真实 HTTP。
import { test, expect } from '@playwright/test'

const BASE = process.env.DEV_SMOKE_BASE || 'http://127.0.0.1:5174'

test('1) 前端页面能加载', async ({ page }) => {
  const resp = await page.goto(BASE + '/', { waitUntil: 'domcontentloaded' })
  expect(resp?.status(), '首页应 200').toBe(200)
  // 页面上应能看到标题或登录入口（不依赖具体文案，只要求有可见文本）
  await expect(page.locator('body')).toContainText(/.+/)
})

test('2) ⭐ 代理真的接到 dev 后端（不是生产）', async ({ page, request }) => {
  // 经**前端的代理**请求 API：若代理指向生产，响应形态会不同（设备集合不同）
  const r = await request.post(BASE + '/api/v1/auth/login', {
    data: { username: 'admin', password: 'DevTest123!' },
  })
  expect(r.status(), '登录应 200（证明代理通到 dev 后端）').toBe(200)
  const j = await r.json()
  const token = j?.data?.token
  expect(token, '应拿到 JWT').toBeTruthy()

  const nodes = await request.get(BASE + '/api/v1/nodes', {
    headers: { Authorization: 'Bearer ' + token },
  })
  expect(nodes.status()).toBe(200)
  const nj = await nodes.json()
  const ids = (nj?.data?.items ?? []).map((n: any) => n.node_id)
  // ⭐ 决定性隔离判据：dev 库只有这 2 台；生产库有 726 台。
  //    若代理指向生产，这里会看到大量 sim-* 节点 ⇒ 断言失败。
  expect(ids.length, 'dev 库应恰好 2 台设备（生产有 726，可由此判定代理指向）').toBeLessThanOrEqual(5)
  expect(ids).toContain('30EDA0A9A808')
  // 且**不应**出现生产库特有的模拟节点前缀
  expect(ids.some((x: string) => x.startsWith('sim-')), '不应看到生产库的 sim-* 节点').toBe(false)
  console.log('[smoke] 经前端代理看到的设备:', ids.join(', '))
})
