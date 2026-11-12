/**
 * 移动端图表门禁（G1 / G2 / G3 / G8）—— Playwright 脚本，退出码即结论。
 *
 *   G8 出血：诊断行宽度 == 视口宽度（innerWidth）；documentElement.scrollWidth <= innerWidth；
 *            .el-main 强制 scrollLeft 后仍为 0（8px 落在 scrollbar-gutter 沟槽里，滚不过去）
 *   G2 面积：绘图区面积 / 画布面积 >= 0.55（诊断卡 >= 0.65）
 *   G3 刻度：Y <= 5，X <= 6
 *   G1 档位：诊断图必须 bleed 档（compact + 画布内无图例）
 *
 * 用法：node e2e/chart-gate.mjs [--w 393] [--h 851]
 *
 * ⚠ 必须跑在 **dev server**（默认 http://127.0.0.1:5173）上：门禁通过
 * `el.__vueParentComponent.exposed` 读组件暴露的 getPlotRect/getLayoutMode，
 * 而 Vue **生产构建不往 DOM 上挂 __vueParentComponent**（devtools 专用），
 * 所以对着 dist/preview 跑会报「G2 取不到绘图区」。渲染是否正确请用截图核对。
 */
import { chromium } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

const EXE = '/home/sun/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome'
const args = Object.fromEntries(process.argv.slice(2).reduce((acc, cur, i, arr) => {
  if (cur.startsWith('--')) acc.push([cur.slice(2), arr[i + 1] && !arr[i + 1].startsWith('--') ? arr[i + 1] : 'true'])
  return acc
}, []))
const W = Number(args.w || 393)
const H = Number(args.h || 851)
const BASE = args.base || 'http://127.0.0.1:5173'
const OUT = args.out || '/mnt/storage/WorkSpace/EHome/.analysis/chart-mobile/gate'
const env = (d) => JSON.stringify({ code: 0, message: 'ok', data: d })

const seriesFor = (cat, s, e) => {
  const N = 240
  const out = []
  for (let i = 0; i < N; i++) {
    const t = s + (e - s) * i / (N - 1)
    const p = i / (N - 1)
    const v = cat === 'current'
      ? (p > 0.45 && p < 0.55 ? 17.6 : -5.4) + Math.sin(i / 9) * 0.4
      : cat === 'rsoc'
        ? 68 + 20 * Math.sin(p * Math.PI)
        : 27 + 4 * Math.sin(p * Math.PI) + (cat.endsWith('1') ? 1.2 : 0)
    out.push({ created_at: new Date(t).toISOString(), value: +v.toFixed(2) })
  }
  return out
}

const run = async () => {
  const browser = await chromium.launch({ executablePath: EXE, args: ['--no-sandbox', '--disable-dev-shm-usage'] })
  const ctx = await browser.newContext({ viewport: { width: W, height: H }, deviceScaleFactor: 3, isMobile: true, hasTouch: true, locale: 'zh-CN', colorScheme: 'dark' })
  const page = await ctx.newPage()
  await page.addInitScript(() => {
    localStorage.setItem('token', 'gate')
    localStorage.setItem('user', JSON.stringify({ id: 1, username: 'gate', role: 'admin' }))
  })
  // catch-all 必须先注册（Playwright 路由按注册逆序匹配）
  await page.route('**/api/v1/**', (r) => r.fulfill({ status: 404, contentType: 'application/json', body: '{"code":404}' }))
  await page.route('**/api/v1/system/maintenance**', (r) => r.fulfill({ status: 401, body: '{}' }))
  await page.route('**/api/v1/unified-data/historical**', (r) => {
    if (r.request().url().includes('batch')) return r.fallback()
    const u = new URL(r.request().url())
    const c = u.searchParams.get('category') || 'x'
    r.fulfill({ status: 200, contentType: 'application/json', body: env(seriesFor(c, Date.now() - 864e5, Date.now())) })
  })
  await page.route('**/api/v1/unified-data/historical-batch**', (r) => {
    const u = new URL(r.request().url())
    const cats = (u.searchParams.get('categories') || '').split(',').filter(Boolean)
    r.fulfill({ status: 200, contentType: 'application/json', body: env(cats.map((c) => ({ category: c, data: seriesFor(c, Date.now() - 864e5, Date.now()) }))) })
  })
  const latest = [
    ['temperature_1', 29.4, 'C'], ['temperature_2', 24.9, 'C'], ['temperature_3', 24.9, 'C'],
    ['rsoc', 68.5, '%'], ['current', 17.6, 'A'],
  ].map((row) => ({ sensor_name: row[0], value: row[1], unit: row[2], timestamp: new Date().toISOString() }))
  await page.route('**/api/v1/edge-devices/14/latest-values', (r) => r.fulfill({ status: 200, contentType: 'application/json', body: env(latest) }))
  await page.route('**/api/v1/edge-devices/14', (r) => r.fulfill({
    status: 200, contentType: 'application/json',
    body: env({ id: 14, node_id: 15, channel_id: 3, name: 'BMS-1', type: 'jiabaida_bms', status: 'active', node: { id: 15, name: 'n15' }, channel: { id: 3 }, last_data: { rsoc: 68.5, temperature_1: 29.4 } }),
  }))

  await page.goto(BASE + '/edge-device/14', { waitUntil: 'domcontentloaded' })
  await page.waitForTimeout(2200)
  const heads = page.locator('.collapse-head')
  const n = await heads.count()
  for (let i = 0; i < n; i++) {
    const h = heads.nth(i)
    if (await h.getAttribute('aria-expanded') !== 'true') await h.click({ force: true }).catch(() => {})
    await page.waitForTimeout(120)
  }
  await page.waitForTimeout(1500)

  const data = await page.evaluate(() => {
    const exposedOf = (el) => {
      let node = el.__vueParentComponent
      for (let i = 0; i < 8 && node; i++) {
        const ex = node.exposed
        if (ex && (typeof ex.getPlotRect === 'function' || typeof ex.getLayoutMode === 'function')) return ex
        node = node.parent
      }
      return null
    }
    const rows = [...document.querySelectorAll('.diag-row')].map((r) => {
      const b = r.getBoundingClientRect()
      return { left: +b.left.toFixed(1), right: +b.right.toFixed(1), width: +b.width.toFixed(1) }
    })
    const main = document.querySelector('.el-main')
    const mainClientW = main ? main.clientWidth : window.innerWidth
    let forcedScrollLeft = 0
    if (main) { main.scrollLeft = 200; forcedScrollLeft = main.scrollLeft }
    const charts = []
    const chartEls = [...document.querySelectorAll('.line-chart, .soc-current-chart')]
    chartEls.forEach((el, idx) => {
      const r = el.getBoundingClientRect()
      const ex = exposedOf(el)
      const plot = ex && ex.getPlotRect ? ex.getPlotRect() : null
      const mode = ex && ex.getLayoutMode ? ex.getLayoutMode() : null
      const inst = ex && ex.getChartInstance ? ex.getChartInstance() : null
      let yTicks = null
      let xTicks = null
      let legendInside = null
      let hideOverlap = null
      if (inst) {
        const opt = inst.getOption()
        try { yTicks = inst.getModel().getComponent('yAxis', 0).axis.scale.getTicks().length } catch (e) {}
        try { xTicks = inst.getModel().getComponent('xAxis', 0).axis.scale.getTicks().length } catch (e) {}
        legendInside = !!(opt.legend && opt.legend.length)
        hideOverlap = !!(opt.xAxis && opt.xAxis[0] && opt.xAxis[0].axisLabel && opt.xAxis[0].axisLabel.hideOverlap)
      }
      charts.push({
        idx,
        title: (el.closest('.diag-card') || el.closest('.card'))?.querySelector('.card-title')?.textContent?.trim() || '',
        canvas: { w: +r.width.toFixed(1), h: +r.height.toFixed(1) },
        plot,
        mode,
        yTicks,
        xTicks,
        legendInside,
        hideOverlap,
        ratio: plot ? +(plot.width * plot.height / (r.width * r.height)).toFixed(4) : null,
      })
    })
    return {
      viewport: window.innerWidth,
      scrollW: document.documentElement.scrollWidth,
      clientW: document.documentElement.clientWidth,
      mainScrollLeft: forcedScrollLeft,
      mainClientW,
      rows,
      charts,
    }
  })

  const fails = []
  const ok = (cond, msg) => { if (!cond) fails.push(msg) }
  ok(data.scrollW <= data.viewport, 'G8 横向溢出：scrollWidth ' + data.scrollW + ' > viewport ' + data.viewport)
  ok(data.scrollW <= data.clientW, 'G8 横向溢出：scrollWidth ' + data.scrollW + ' > clientWidth ' + data.clientW)
  ok(data.mainScrollLeft === 0, 'G8 主区可横向滚动：main.scrollLeft=' + data.mainScrollLeft)
  for (const r of data.rows) {
    // 对齐 documentElement.clientWidth（可见宽度）：393 视口下它是 385（右侧 8px 是滚动条沟槽），
    // 对齐 innerWidth 会把图表最右 8px 藏进沟槽里。
    ok(Math.abs(r.width - data.mainClientW) < 1.5, 'G8 诊断行未出血：width ' + r.width + ' != 滚动容器可见宽 ' + data.mainClientW)
    ok(Math.abs(r.left) < 1.5, 'G8 出血行未贴左边缘：left ' + r.left)
  }
  const IN_SCOPE = ['温度趋势', 'SOC 与电流']
  const warnings = []
  for (const c of data.charts) {
    const strict = IN_SCOPE.includes(c.title)
    const bad = (msg) => { if (strict) fails.push(msg); else warnings.push(msg) }
    if (!c.plot) { bad('G2 取不到绘图区：' + c.title); continue }
    // 容量判据：10~11px 字号下，一个 Y 标签约占 14px 高、一个时刻标签约占 34px 宽。
    // X 轴开了 hideOverlap 时 ECharts 会丢弃重叠标签，只需断言开关为 true。
    if (c.yTicks !== null && c.yTicks * 14 > c.plot.height + 1) {
      bad('G3 Y 标签会互压：' + c.title + ' ' + c.yTicks + ' 个 / ' + c.plot.height.toFixed(0) + 'px')
    }
    if (c.hideOverlap === false && c.xTicks !== null) bad('G3 X 标签会互压：' + c.title + ' ' + c.xTicks + ' 个 / ' + c.plot.width.toFixed(0) + 'px')
    if (!strict) continue
    ok(c.mode && c.mode.bleed === true, 'G1 未走出血档：' + c.title + ' mode=' + JSON.stringify(c.mode))
    ok(c.legendInside === false, 'G1 画布内仍有图例：' + c.title)
    ok(c.hideOverlap === true, 'G3 X 轴未开 hideOverlap：' + c.title)
    ok(c.ratio >= 0.65, 'G2 诊断卡未达标：' + c.title + ' ' + (c.ratio * 100).toFixed(1) + '% < 65%')
  }

  fs.mkdirSync(OUT, { recursive: true })
  fs.writeFileSync(path.join(OUT, 'gate-' + W + 'x' + H + '.json'), JSON.stringify({ viewport: { w: W, h: H }, data, fails }, null, 2))
  console.log(JSON.stringify({
    ok: fails.length === 0,
    fails,
    warnings,
    rows: data.rows,
    charts: data.charts.map((c) => ({ t: c.title, ratio: c.ratio, y: c.yTicks, x: c.xTicks, bleed: c.mode && c.mode.bleed, legendInside: c.legendInside, canvas: c.canvas })),
  }, null, 1))
  await browser.close()
  process.exit(fails.length ? 1 : 0)
}
run().catch((e) => { console.error('GATE FAILED', e); process.exit(2) })
