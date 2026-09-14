import { chromium } from '@playwright/test'
const BASE = 'http://127.0.0.1:8082'
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox'] })
const ctx = await browser.newContext({ viewport: { width: 390, height: 844 } })
const req = await ctx.request.post(BASE + '/api/v1/auth/login', { data: { username: 'admin', password: 'UiuxAudit2026!' } })
const b = await req.json()
const page = await ctx.newPage()
await page.addInitScript(([t]) => { localStorage.setItem('token', t); localStorage.setItem('user', JSON.stringify({id:1,username:'admin'})); localStorage.setItem('theme','light') }, [b.data.token])

// 全量扫描：每个含表的页面，非固定列内的可交互控件被固定列遮挡的比例
const ROUTES = ['/dashboard','/node','/channel','/edge-device','/logical-device','/data','/data-sources','/firmware','/device-configs','/monitor','/alerts','/automation','/profile']
for (const route of ROUTES) {
  await page.goto(BASE + route, { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.main-layout, .main-content', { timeout: 20000 })
  await page.waitForTimeout(2200)
  const f = await page.evaluate(() => {
    const tables = Array.from(document.querySelectorAll('.el-table'))
    let totalControls = 0, occluded = 0
    const samples = []
    tables.forEach((t) => {
      const fixedCells = t.querySelectorAll('td.el-table-fixed-column--right, td.el-table-fixed-column--left')
      if (!fixedCells.length) return
      // 非固定列里的可交互控件
      const controls = Array.from(t.querySelectorAll('button, .el-switch, a, input, .el-checkbox, .el-select'))
        .filter((el) => !el.closest('.el-table-fixed-column--right, .el-table-fixed-column--left'))
        .filter((el) => el.getBoundingClientRect().width > 0)
      controls.forEach((el) => {
        el.scrollIntoView({ block: 'center', inline: 'center' })
        const r = el.getBoundingClientRect()
        if (r.left < 0 || r.right > innerWidth) return
        totalControls++
        const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
        if (!hit || !(hit === el || el.contains(hit))) {
          occluded++
          if (samples.length < 2) {
            const cell = hit && hit.closest('td,th')
            samples.push({ cls: String(el.className).slice(0, 34), blockedBy: hit ? String(hit.className).slice(0, 34) : null, inFixed: !!(cell && /fixed-column/.test(String(cell.className))) })
          }
        }
      })
    })
    return { route: location.pathname, totalControls, occluded, samples }
  })
  if (f.totalControls > 0) console.log(JSON.stringify(f))
}
await browser.close()
