import { chromium } from '@playwright/test'
const BASE = 'http://127.0.0.1:8082'
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox'] })
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
const req = await ctx.request.post(BASE + '/api/v1/auth/login', { data: { username: 'admin', password: 'UiuxAudit2026!' } })
const b = await req.json()

const lum = (r, g, bl) => { const f = (c) => { c /= 255; return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4) }; return 0.2126*f(r)+0.7152*f(g)+0.0722*f(bl) }
const ratio = (a, c) => { const L1 = lum(...a), L2 = lum(...c); const hi = Math.max(L1,L2), lo = Math.min(L1,L2); return (hi+0.05)/(lo+0.05) }
const parse = (s) => { const m = s.match(/rgba?\(([^)]+)\)/); const p = m[1].split(',').map(x=>parseFloat(x)); return [p[0],p[1],p[2]] }

// 现在 8082 托管的是**含全部修复的新产物**，可直接端到端验证（不再是注入式等价）
for (const theme of ['light', 'dark']) {
  const page = await ctx.newPage()
  await page.addInitScript(([t, th]) => { localStorage.setItem('token', t); localStorage.setItem('user', JSON.stringify({id:1,username:'admin'})); localStorage.setItem('theme', th) }, [b.data.token, theme])
  await page.goto(BASE + '/dashboard', { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.sidebar .el-menu-item.is-active', { timeout: 20000 })
  await page.waitForTimeout(1500)
  const info = await page.evaluate(() => {
    const sb = document.querySelector('.sidebar')
    const el = document.querySelector('.sidebar .el-menu-item.is-active')
    const logo = document.querySelector('.sidebar .logo-area')
    const foot = document.querySelector('.sidebar .sidebar-footer')
    const item = document.querySelector('.sidebar .el-menu-item:not(.is-active)')
    const r = el.getBoundingClientRect()
    return {
      rect: { x: r.x, y: r.y, w: r.width, h: r.height },
      sidebarBg: getComputedStyle(sb).backgroundImage,
      activeColor: getComputedStyle(el).color,
      activeBg: getComputedStyle(el).backgroundColor + ' | ' + getComputedStyle(el).backgroundImage,
      logoBorder: getComputedStyle(logo).borderBottomColor,
      footBorder: getComputedStyle(foot).borderTopColor,
      menuColor: getComputedStyle(item).color,
    }
  })
  const shot = await page.screenshot({ clip: { x: Math.round(info.rect.x), y: Math.round(info.rect.y), width: Math.round(info.rect.w), height: Math.round(info.rect.h) } })
  const px = await page.evaluate(async ({ url, w, h }) => {
    const img = new Image()
    await new Promise((res, rej) => { img.onload = res; img.onerror = rej; img.src = url })
    const c = document.createElement('canvas'); c.width = w; c.height = h
    const g = c.getContext('2d'); g.drawImage(img, 0, 0)
    const s = (x, y) => { const d = g.getImageData(x, y, 1, 1).data; return [d[0], d[1], d[2]] }
    return [s(w - 6, Math.floor(h/2)), s(w - 20, Math.floor(h/2)), s(Math.floor(w/2), 3)]
  }, { url: 'data:image/png;base64,' + shot.toString('base64'), w: Math.round(info.rect.w), h: Math.round(info.rect.h) })
  const fg = parse(info.activeColor)
  const worst = Math.min(...px.map((p) => ratio(fg, p)))
  console.log('##### THEME=' + theme)
  console.log('  sidebar.backgroundImage = ' + info.sidebarBg)
  console.log('  logo borderBottom = ' + info.logoBorder + ' | footer borderTop = ' + info.footBorder)
  console.log('  non-active menu color = ' + info.menuColor)
  console.log('  is-active color = ' + info.activeColor + '  bg=' + info.activeBg)
  console.log('  WCAG (real pixels, worst of 3) = ' + worst.toFixed(2) + ':1  ' + (worst >= 4.5 ? 'PASS' : 'FAIL'))
  await page.close()
}
await browser.close()
