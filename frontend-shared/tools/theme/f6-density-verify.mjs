import { chromium } from '@playwright/test'
const BASE = 'http://127.0.0.1:8082'
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox'] })
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
const req = await ctx.request.post(BASE + '/api/v1/auth/login', { data: { username: 'admin', password: 'UiuxAudit2026!' } })
const b = await req.json()
const page = await ctx.newPage()
await page.addInitScript(([t]) => { localStorage.setItem('token', t); localStorage.setItem('user', JSON.stringify({id:1,username:'admin'})); localStorage.setItem('theme','light') }, [b.data.token])

// F6 声称：用 :has() 收紧单元格 padding，使 1440 行高 40→40 逐像素不变
for (const route of ['/logical-device', '/node', '/edge-device']) {
  await page.goto(BASE + route, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector(".el-table__row, .empty-state, .el-empty", { timeout: 20000 }).catch(() => {})
  await page.waitForTimeout(2500)
  const f = await page.evaluate(() => {
    const rows = Array.from(document.querySelectorAll('.el-table__row')).slice(0, 6)
    const heights = rows.map((r) => Math.round(r.getBoundingClientRect().height))
    // 采样一个含小按钮的单元格的 padding
    const cell = document.querySelector('.el-table__row td .cell > .el-button--small, .el-table__row td .el-button--small')
    const td = cell ? cell.closest('td') : null
    return {
      route: location.pathname,
      rowHeights: heights,
      distinctHeights: [...new Set(heights)],
      sampleCellPadding: td ? getComputedStyle(td).padding : null,
      sampleBtn: cell ? (() => { const r = cell.getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height) } })() : null,
      docOverflowX: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    }
  })
  console.log(JSON.stringify(f))
}
await browser.close()
