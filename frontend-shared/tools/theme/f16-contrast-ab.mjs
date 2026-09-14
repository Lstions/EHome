import { chromium } from '@playwright/test'
const BASE = 'http://127.0.0.1:8082'
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox'] })
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 } })
const req = await ctx.request.post(BASE + '/api/v1/auth/login', { data: { username: 'admin', password: 'UiuxAudit2026!' } })
const b = await req.json()

const lum = (r, g, bl) => { const f = (c) => { c /= 255; return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4) }; return 0.2126*f(r)+0.7152*f(g)+0.0722*f(bl) }
const ratio = (a, c) => { const L1 = lum(...a), L2 = lum(...c); const hi = Math.max(L1,L2), lo = Math.min(L1,L2); return (hi+0.05)/(lo+0.05) }
const parse = (s) => { const m = s.match(/rgba?\(([^)]+)\)/); const p = m[1].split(',').map(x=>parseFloat(x)); return [p[0],p[1],p[2]] }

// 完整模拟 F16 的修复：既接线 token，又用新 token 值
const FIX = {
  light: [
    ':root { --sidebar-active-color: #66b1ff !important; }',
    '.sidebar .el-menu-item.is-active { color: #66b1ff !important; }',
  ].join('\n'),
  dark: [
    'html.dark { --sidebar-active-color: #79bbff !important; }',
    'html.dark .sidebar .el-menu-item.is-active { color: #79bbff !important; }',
  ].join('\n'),
}
// 对照：修复前的真实值
const BEFORE = {
  light: '.sidebar .el-menu-item.is-active { color: #1f5ad8 !important; }',
  dark: 'html.dark .sidebar .el-menu-item.is-active { color: #1f5ad8 !important; }',
}

async function measure(page) {
  const info = await page.evaluate(() => {
    const el = document.querySelector('.sidebar .el-menu-item.is-active')
    const r = el.getBoundingClientRect()
    return { rect: { x: r.x, y: r.y, w: r.width, h: r.height }, color: getComputedStyle(el).color }
  })
  const shot = await page.screenshot({ clip: { x: Math.round(info.rect.x), y: Math.round(info.rect.y), width: Math.round(info.rect.w), height: Math.round(info.rect.h) } })
  const px = await page.evaluate(async ({ url, w, h }) => {
    const img = new Image()
    await new Promise((res, rej) => { img.onload = res; img.onerror = rej; img.src = url })
    const c = document.createElement('canvas'); c.width = w; c.height = h
    const g = c.getContext('2d'); g.drawImage(img, 0, 0)
    const s = (x, y) => { const d = g.getImageData(x, y, 1, 1).data; return [d[0], d[1], d[2]] }
    return { right: s(w - 6, Math.floor(h/2)), right2: s(w - 20, Math.floor(h/2)), topMid: s(Math.floor(w/2), 3) }
  }, { url: 'data:image/png;base64,' + shot.toString('base64'), w: Math.round(info.rect.w), h: Math.round(info.rect.h) })
  const fg = parse(info.color)
  const out = []
  for (const [k, v] of Object.entries(px)) out.push({ bg: k, pixel: v, ratio: +ratio(fg, v).toFixed(2) })
  return { color: info.color, out, worst: Math.min(...out.map(o => o.ratio)) }
}

for (const theme of ['light', 'dark']) {
  const page = await ctx.newPage()
  await page.addInitScript(([t, th]) => {
    localStorage.setItem('token', t); localStorage.setItem('user', JSON.stringify({id:1,username:'admin'})); localStorage.setItem('theme', th)
  }, [b.data.token, theme])
  await page.goto(BASE + '/dashboard', { waitUntil: 'domcontentloaded' })
  await page.waitForSelector('.sidebar .el-menu-item.is-active', { timeout: 20000 })
  await page.waitForTimeout(1000)

  const handleBefore = await page.addStyleTag({ content: BEFORE[theme] })
  await page.waitForTimeout(400)
  const rBefore = await measure(page)
  await page.evaluate((el) => el.remove(), handleBefore)
  await page.waitForTimeout(300)

  const handleFix = await page.addStyleTag({ content: FIX[theme] })
  await page.waitForTimeout(400)
  const rFix = await measure(page)

  console.log('##### THEME=' + theme)
  console.log('  修复前 color=' + rBefore.color + '  最差对比度=' + rBefore.worst + ':1  ' + (rBefore.worst >= 4.5 ? 'PASS' : 'FAIL'))
  console.log('  修复后 color=' + rFix.color + '  最差对比度=' + rFix.worst + ':1  ' + (rFix.worst >= 4.5 ? 'PASS' : 'FAIL'))
  console.log('    明细: ' + rFix.out.map(o => o.bg + '=rgb(' + o.pixel.join(',') + ') ' + o.ratio).join('  |  '))
  await page.close()
}
await browser.close()
