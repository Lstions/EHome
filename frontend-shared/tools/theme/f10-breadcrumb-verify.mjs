import { chromium } from '@playwright/test'
const BASE = 'http://127.0.0.1:8082'
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox'] })
const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } })
const req = await ctx.request.post(BASE + '/api/v1/auth/login', { data: { username: 'admin', password: 'UiuxAudit2026!' } })
const b = await req.json()
const page = await ctx.newPage()
await page.addInitScript(([t]) => { localStorage.setItem('token', t); localStorage.setItem('user', JSON.stringify({id:1,username:'admin'})); localStorage.setItem('theme','light') }, [b.data.token])

// 找一个真实节点详情页
const list = await ctx.request.get(BASE + '/api/v1/nodes?page=1&page_size=1', { headers: { Authorization: 'Bearer ' + b.data.token } })
const lj = await list.json()
const nodeId = lj.data.items[0]?.id
console.log('nodeId=' + nodeId)

await page.goto(BASE + '/node/' + nodeId, { waitUntil: 'domcontentloaded' })
await page.waitForSelector('.main-layout, .main-content', { timeout: 20000 })
await page.waitForTimeout(2500)

const facts = await page.evaluate(() => {
  const bc = document.querySelector('.no-breadcrumb')
  const link = document.querySelector('.no-breadcrumb .el-breadcrumb__inner.is-link')
  const items = Array.from(document.querySelectorAll('.no-breadcrumb .el-breadcrumb__item'))
  const de = document.documentElement
  const r = (e) => { if (!e) return null; const x = e.getBoundingClientRect(); return { w: Math.round(x.width), h: Math.round(x.height), x: Math.round(x.left) } }
  return {
    breadcrumb: bc ? { display: getComputedStyle(bc).display, ...r(bc) } : null,
    items: items.map((i) => ({ text: (i.textContent || '').trim().slice(0, 14), display: getComputedStyle(i).display, ...r(i) })),
    backLink: link ? { text: (link.textContent || '').trim(), ...r(link) } : null,
    docOverflowX: de.scrollWidth - de.clientWidth,
    mainLayoutBreadcrumb: (() => { const m = document.querySelector('.main-content .el-breadcrumb, .topbar .el-breadcrumb'); return m ? getComputedStyle(m).display : null })(),
    bodyTitle: (document.querySelector('.ph-title')?.textContent || '').trim(),
  }
})
console.log(JSON.stringify(facts, null, 1))
await browser.close()
