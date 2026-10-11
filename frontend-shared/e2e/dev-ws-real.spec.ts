// 真实浏览器 WebSocket 验证（**只有浏览器能测准**）
//
// ## ⚠ 为什么 must 用浏览器（本仓源码里已写明这条）
// backend/internal/websocket/websocket.go:71-75：
//     origin := r.Header.Get("Origin")
//     if origin == "" { return true }      ← 非浏览器客户端直接放行
// ⇒ **用 curl / Node ws（默认不发 Origin）测 WS 会假绿**：
//   它证明的只是"没被拦"，不是"浏览器能连上"。
//   vite.config.ts 里也写着同样的提醒（"用 curl 测 WS 会假绿 —— 必须用真实浏览器"）。
//
// ## 它证明什么
//   1. 浏览器真的建立了 /ws 连接（readyState=OPEN）；
//   2. 且**不是**被 Origin 校验拦下（拦截会表现为握手失败/反复重连）。
import { test, expect } from '@playwright/test'

const BASE = process.env.DEV_FLOW_BASE || 'http://127.0.0.1:5174'
const USER = 'admin'
const PASS = 'DevTest123!'

test('浏览器 WebSocket：/ws 能真正建立（非 curl 假绿）', async ({ page }) => {
  // 收集页面里所有 WebSocket 的建立/关闭事件
  await page.addInitScript(() => {
    ;(window as any).__wsLog = []
    const Orig = window.WebSocket
    // @ts-expect-error 故意包装以记录事件
    window.WebSocket = function (url: string, protocols?: any) {
      const ws = protocols ? new Orig(url, protocols) : new Orig(url)
      ;(window as any).__wsLog.push({ ev: 'construct', url: String(url) })
      ws.addEventListener('open', () => (window as any).__wsLog.push({ ev: 'open', url: String(url) }))
      ws.addEventListener('close', (e) => (window as any).__wsLog.push({ ev: 'close', url: String(url), code: (e as CloseEvent).code }))
      ws.addEventListener('error', () => (window as any).__wsLog.push({ ev: 'error', url: String(url) }))
      return ws
    } as any
    ;(window as any).WebSocket.prototype = Orig.prototype
    Object.assign((window as any).WebSocket, Orig)
  })

  // 登录（WS 多数页面在登录后才连）
  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' })
  await page.locator('input[type="text"]').first().fill(USER)
  await page.locator('input[type="password"]').first().fill(PASS)
  await page.getByRole('button', { name: /登\s*录/ }).first().click()
  await page.waitForURL((u) => !u.pathname.includes('/login'), { timeout: 20000 })

  // 进 dashboard 并等一会儿，让 WS 有机会建立/重连
  await page.goto(BASE + '/dashboard', { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(6000)

  const log = await page.evaluate(() => (window as any).__wsLog)
  console.log('[ws] 事件: ' + JSON.stringify(log, null, 1))

  const constructed = log.filter((e: any) => e.ev === 'construct')
  const opened = log.filter((e: any) => e.ev === 'open')
  expect(constructed.length, '页面应至少构造过一个 WebSocket').toBeGreaterThan(0)
  expect(opened.length, '至少应有一个 WebSocket 真正 OPEN（若为 0 则说明握手被拒或连不上）').toBeGreaterThan(0)
  // 隔离判据：URL 应指向本站（经 vite 代理），不是别的地方
  expect(opened[0].url, 'WS 应指向本前端站（经代理）').toContain('127.0.0.1:5174')
})
