import { chromium } from '@playwright/test'
const BASE = 'http://127.0.0.1:8082'
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox'] })

for (const vp of [{ w: 390, h: 844 }, { w: 1440, h: 900 }]) {
  const ctx = await browser.newContext({ viewport: { width: vp.w, height: vp.h } })
  const req = await ctx.request.post(BASE + '/api/v1/auth/login', { data: { username: 'admin', password: 'UiuxAudit2026!' } })
  const b = await req.json()
  const page = await ctx.newPage()
  await page.addInitScript(([t]) => { localStorage.setItem('token', t); localStorage.setItem('user', JSON.stringify({id:1,username:'admin'})); localStorage.setItem('theme','light') }, [b.data.token])
  await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.el-table__row', { timeout: 20000 })
  await page.waitForTimeout(2500)

  const res = await page.evaluate(() => {
    const switches = Array.from(document.querySelectorAll('.el-table__row .el-switch'))
    const out = switches.slice(0, 3).map((sw) => {
      sw.scrollIntoView({ inline: 'center', block: 'center' })
      const r = sw.getBoundingClientRect()
      // 48 点网格命中测试
      let self = 0, other = 0, total = 0
      for (let i = 1; i <= 6; i++) for (let j = 1; j <= 8; j++) {
        const x = r.left + (r.width * i) / 7
        const y = r.top + (r.height * j) / 9
        if (x < 0 || x > innerWidth || y < 0 || y > innerHeight) continue
        total++
        const hit = document.elementFromPoint(x, y)
        if (hit && (hit === sw || sw.contains(hit))) self++
        else { other++; if (total === 1) {} }
      }
      const blocker = (() => {
        const hit = document.elementFromPoint(r.left + r.width / 2, r.top + r.height / 2)
        if (!hit || hit === sw || sw.contains(hit)) return null
        const cell = hit.closest('td,th,.el-table__cell')
        return { tag: hit.tagName, cls: String(hit.className).slice(0, 50), inFixedCell: !!(cell && /fixed-column/.test(String(cell.className))), cellCls: cell ? String(cell.className).slice(0, 60) : null }
      })()
      return { w: Math.round(r.width), h: Math.round(r.height), selfHits: self, otherHits: other, total, blocker }
    })
    return { switchCount: switches.length, out }
  })
  console.log('##### viewport ' + vp.w + '  switches=' + res.switchCount)
  res.out.forEach((o, i) => console.log('  [' + i + '] ' + JSON.stringify(o)))
  await ctx.close()
}
await browser.close()
