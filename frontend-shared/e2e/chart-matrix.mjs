
/**
 * 移动端图表量化矩阵 harness（临时取证工具，不参与构建/门禁）
 *
 * 相对 e2e/chart-audit.mjs 的扩展（2026-11-11，图表取证员）：
 *   1. 把 evaluate 逻辑从「只量 .diag-card」扩成「量页面上**每一个** .line-chart 实例」；
 *   2. 每个实例给出：区块归属、容器/canvas 尺寸、legend/grid 配置、绘图区 rect、
 *      x/y 刻度数、y 轴数、hideOverlap，以及从 zrender displayList 实测出来的
 *      **轴标签/图例文字 bbox** —— 于是「标签互相压」「图例压轴名」不再是肉眼判断，而是可计算的矩形相交；
 *   3. mock 数据补全：运行趋势/历史趋势要的 total_voltage / remaining_capacity /
 *      cell_voltage_1..16 / cell_voltage_max / cell_voltage_min 全部给到，
 *      否则 BmsChartsPanel 会停在「暂无该项历史数据」空态、压根没有图表实例可量。
 *
 * 用法：
 *   node e2e/chart-matrix.mjs --w 393 --h 851 --dpr 3 --theme dark --out ../../.analysis/chart-mobile/matrix
 */
import { chromium } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

const argv = process.argv.slice(2)
const args = {}
for (let i = 0; i < argv.length; i++) {
  const cur = argv[i]
  if (!cur.startsWith('--')) continue
  const next = argv[i + 1]
  args[cur.slice(2)] = next && !next.startsWith('--') ? next : 'true'
}
const W = Number(args.w || 393)
const H = Number(args.h || 851)
const DPR = Number(args.dpr || 3)
const OUT = path.resolve(args.out || '../../.analysis/chart-mobile/matrix')
const BASE = args.base || 'http://127.0.0.1:5173'
const DEVICE_ID = args.device || '14'
const THEME = args.theme === 'light' ? 'light' : 'dark'
const EXE = '/home/sun/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome'

fs.mkdirSync(OUT, { recursive: true })

// ── 假数据：与真机形态一致 ──
// 温度 3 路同单位 ℃（单 Y 轴）；SOC % / 电流 A 量纲不同（LineChart 会分左右双轴）
// 总电压 ~53V 与单体 ~3.3V 量级差 16 倍（BmsChartsPanel 因此拆图，不共轴）
const SPECS = {
  temperature_1: { base: 26.4, amp: 1.6, rise: 4.6, riseAfter: 0.58, noise: 0.25 },
  temperature_2: { base: 24.8, amp: 1.2, rise: 1.2, riseAfter: 0.6, noise: 0.2 },
  temperature_3: { base: 25.4, amp: 1.4, rise: 0.8, riseAfter: 0.62, noise: 0.2 },
  rsoc: { base: 86, amp: 6, rise: -12, riseAfter: 0.55, noise: 0.3 },
  current: { base: -5.4, amp: 3, rise: 26, riseAfter: 0.42, noise: 0.8 },
  total_voltage: { base: 53.1, amp: 0.5, rise: 0.35, riseAfter: 0.5, noise: 0.05 },
  remaining_capacity: { base: 81, amp: 4, rise: -9, riseAfter: 0.55, noise: 0.2 },
  cell_voltage_max: { base: 3.331, amp: 0.004, rise: 0.006, riseAfter: 0.5, noise: 0.0008 },
  cell_voltage_min: { base: 3.318, amp: 0.004, rise: -0.006, riseAfter: 0.5, noise: 0.0008 },
}
const cellSpec = (n) => ({ base: 3.312 + 0.0015 * (((n % 7) + 7) % 7 - 3), amp: 0.004, rise: 0.006, riseAfter: 0.5, noise: 0.0008 })

function specFor(cat) {
  if (SPECS[cat]) return SPECS[cat]
  const m = /^cell_voltage_(\d+)$/.exec(cat)
  if (m) return cellSpec(Number(m[1]))
  return null
}

const seriesFor = (cat, startMs, endMs) => {
  const raw = specFor(cat)
  if (!raw) return []
  const N = 240
  const out = []
  for (let i = 0; i < N; i++) {
    const t = startMs + ((endMs - startMs) * i) / (N - 1)
    const p = i / (N - 1)
    let v = raw.base + raw.amp * Math.sin(p * Math.PI * 2 - 1.2) + (p > raw.riseAfter ? raw.rise * (p - raw.riseAfter) / (1 - raw.riseAfter) : 0)
    if (cat === 'current') v = Math.max(-9, Math.min(31, v + (p > 0.45 && p < 0.55 ? 20 : 0)))
    if (cat === 'rsoc') v = Math.max(2, Math.min(100, v))
    v += (Math.random() - 0.5) * raw.noise
    const digits = /^cell_voltage/.test(cat) || cat === 'total_voltage' ? 3 : 3
    out.push({ created_at: new Date(t).toISOString(), value: Number(v.toFixed(digits)) })
  }
  return out
}

const envelope = (data) => JSON.stringify({ code: 0, message: 'ok', data })

const sleep = (ms) => new Promise((r) => setTimeout(r, ms))

const run = async () => {
  const browser = await chromium.launch({ executablePath: EXE, args: ['--no-sandbox', '--disable-dev-shm-usage'] })
  const ctx = await browser.newContext({
    viewport: { width: W, height: H },
    deviceScaleFactor: DPR,
    isMobile: W < 900,
    hasTouch: W < 900,
    locale: 'zh-CN',
    colorScheme: THEME,
  })
  const page = await ctx.newPage()
  const errors = []
  const consoleErrors = []
  page.on('pageerror', (e) => errors.push(String(e).slice(0, 300)))
  page.on('console', (m) => { if (m.type() === 'error') consoleErrors.push(m.text().slice(0, 300)) })

  await page.addInitScript(() => {
    // 资源时间线默认只保留 250 条，dev 下模块请求极易把 echarts 那条挤掉 ⇒
    // 柱状图（没有 defineExpose 探针，只能靠 getInstanceByDom）会「未取到实例」。放开上限。
    try { performance.setResourceTimingBufferSize(30000) } catch { /* 忽略 */ }
    localStorage.setItem('token', 'audit-token')
    localStorage.setItem('user', JSON.stringify({ id: 1, username: 'audit', role: 'admin', nickname: '审计' }))
  })

  // ⚠ Playwright 路由按**注册逆序**匹配（后注册先检查）：
  //   catch-all 必须最先注册；'historical**' 会匹配 'historical-batch'，故 batch 必须后注册。
  //   glob 也不得写 '**/api/**'：会拦掉 Vite 的 /src/api/*.ts 模块请求 ⇒ 白屏。
  await page.route('**/api/v1/**', (r) => r.fulfill({ status: 404, contentType: 'application/json', body: '{"code":404,"message":"mock 未提供"}' }))
  await page.route('**/api/v1/system/maintenance**', (r) => r.fulfill({ status: 404, contentType: 'application/json', body: '{"code":404,"message":"not found"}' }))
  await page.route('**/api/v1/unified-data/historical**', (r) => {
    const u = new URL(r.request().url())
    if (u.pathname.includes('batch')) return r.fallback()
    const cat = u.searchParams.get('category') || ''
    const s = Date.parse(u.searchParams.get('start_time')) || Date.now() - 86400000
    const e = Date.parse(u.searchParams.get('end_time')) || Date.now()
    r.fulfill({ status: 200, contentType: 'application/json', body: envelope(seriesFor(cat, s, e)) })
  })
  await page.route('**/api/v1/unified-data/historical-batch**', (r) => {
    const u = new URL(r.request().url())
    const cats = (u.searchParams.get('categories') || '').split(',').filter(Boolean)
    const s = Date.parse(u.searchParams.get('start_time')) || Date.now() - 86400000
    const e = Date.parse(u.searchParams.get('end_time')) || Date.now()
    r.fulfill({ status: 200, contentType: 'application/json', body: envelope(cats.map((c) => ({ category: c, data: seriesFor(c, s, e) }))) })
  })
  await page.route('**/api/v1/edge-devices/' + DEVICE_ID + '/latest-values', (r) => {
    const nowIso = new Date().toISOString()
    const vals = [
      ['total_voltage', 53.15, 'V'], ['current', -5.39, 'A'], ['rsoc', 81, '%'],
      ['remaining_capacity', 81.0, 'Ah'], ['nominal_capacity', 101, 'Ah'],
      ['temperature_1', 29.9, '℃'], ['temperature_2', 30.4, '℃'], ['temperature_3', 30.8, '℃'],
      ['cell_voltage_max', 3.325, 'V'], ['cell_voltage_min', 3.32, 'V'], ['cell_count', 16, ''],
      ['protection_status', 0, ''], ['fet_status', 3, ''], ['cycle_count', 128, ''],
      // 电芯电压分布（柱状图）用 latestData 字典取 cell_voltage_N：不给就渲染空态
      ...Array.from({ length: 16 }, (_, i) => ['cell_voltage_' + (i + 1), Number((3.31 + 0.012 * Math.sin(i / 2.1)).toFixed(3)), 'V']),
    ].map(([sensor_name, value, unit]) => ({ sensor_name, value, unit, timestamp: nowIso }))
    r.fulfill({ status: 200, contentType: 'application/json', body: envelope(vals) })
  })
  const devicePayload = {
    id: Number(DEVICE_ID), node_id: 15, channel_id: 3, name: 'BMS-1', type: 'jiabaida_bms',
    protocol: 'uart', status: 'active', hardware_type: 'RS485', hardware_id: '1',
    max_polling_commands: 4,
    last_data: { total_voltage: 53.15, current: -5.39, rsoc: 81, remaining_capacity: 81.0, temperature_1: 29.9, temperature_2: 30.4, temperature_3: 30.8 },
    last_data_at: new Date().toISOString(), last_data_time: new Date().toISOString(),
    node: { id: 15, name: '节点-15', firmware_version: '6.1.0' },
    channel: { id: 3, node_id: 15, hardware_type: 'RS485', hardware_id: 'UART1', bus_type: 'uart' },
  }
  await page.route('**/api/v1/edge-devices/' + DEVICE_ID, (r) =>
    r.fulfill({ status: 200, contentType: 'application/json', body: envelope(devicePayload) }),
  )
  await page.route('**/api/v1/account', (r) =>
    r.fulfill({ status: 200, contentType: 'application/json', body: envelope({ id: 1, username: 'audit', role: 'admin', nickname: '审计' }) }),
  )

  await page.goto(BASE + '/edge-device/' + DEVICE_ID, { waitUntil: 'domcontentloaded', timeout: 60000 })
  await page.waitForTimeout(2500)

  // 展开**所有**折叠块（温度趋势 / SOC 与电流 / 历史趋势 / 其余诊断块）
  const collapsedTitles = []
  for (let pass = 0; pass < 3; pass++) {
    const heads = page.locator('.collapse-head')
    const n = await heads.count().catch(() => 0)
    let clicked = 0
    for (let i = 0; i < n; i++) {
      const h = heads.nth(i)
      const vis = await h.isVisible().catch(() => false)
      if (!vis) continue
      const exp = await h.getAttribute('aria-expanded').catch(() => null)
      if (exp === 'true') continue
      const title = ((await h.textContent().catch(() => '')) || '').trim().slice(0, 24)
      await h.click({ force: true }).catch(() => {})
      clicked++
      collapsedTitles.push(title)
      await page.waitForTimeout(320)
    }
    if (!clicked) break
  }
  // 等 4 个 LineChart 实例都出 canvas（2 张诊断卡 + 历史趋势里的 2 张）
  await page.waitForFunction(() => document.querySelectorAll('.line-chart canvas').length >= 4, { timeout: 20000 }).catch(() => {})
  await page.waitForTimeout(2500)

  // ── 量测 ──
  const metrics = await page.evaluate(async () => {
    const round = (n, p = 2) => (n === null || n === undefined || !Number.isFinite(n) ? null : Math.round(n * Math.pow(10, p)) / Math.pow(10, p))
    const rect2 = (x, y, w, h) => ({ x: round(x), y: round(y), w: round(w), h: round(h) })
    const area = (a, b) => {
      if (!a || !b) return 0
      const w = Math.min(a.x + a.w, b.x + b.w) - Math.max(a.x, b.x)
      const h = Math.min(a.y + a.h, b.y + b.h) - Math.max(a.y, b.y)
      return w > 0 && h > 0 ? w * h : 0
    }

    // ① 拿 echarts 实例：优先用 LineChart 自己 defineExpose 的探针 API（工程内已有），
    //    失败再退回动态 import echarts 模块的 getInstanceByDom（柱状图没有 exposed，只能走这条）。
    // 动态 import 必须用页面里**同一个** echarts 模块实例的 URL（ESM 按 URL 缓存，
    // 换个 URL 就是第二份模块 + 第二张实例表，getInstanceByDom 会返回 undefined）。
    let ecMods = null
    const loadEcMods = async () => {
      if (ecMods) return ecMods
      const urls = [...new Set(performance.getEntriesByType('resource').map((e) => e.name)
        .filter((n) => /echarts/i.test(n) && /\.js(\?|$)/.test(n)))]
      const mods = []
      for (const u of urls) {
        try {
          const m = await import(u)
          if (m && typeof m.getInstanceByDom === 'function') mods.push(m)
        } catch { /* 下一个 */ }
      }
      ecMods = mods
      return mods
    }
    const instanceByDom = async (el) => {
      const mods = await loadEcMods()
      for (const m of mods) {
        try { const c = m.getInstanceByDom(el); if (c) return c } catch { /* 下一个 */ }
      }
      return null
    }

    const findExposed = (el) => {
      let inst = el.__vueParentComponent
      for (let i = 0; inst && i < 8; i++) {
        const ex = inst.exposed
        if (ex && (typeof ex.getPlotRect === 'function' || typeof ex.getChartInstance === 'function')) return ex
        inst = inst.parent
      }
      return null
    }

    const sectionOf = (el) => {
      const card = el.closest('.card')
      const block = el.closest('.collapse-block')
      const mobileWrap = el.closest('.trend-mobile')
      return {
        cardTitle: (card?.querySelector('.card-head .card-title')?.textContent || '').trim() || null,
        cardClass: card ? String(card.className || '') : null,
        collapseTitle: (block?.querySelector('.collapse-head .collapse-title')?.textContent || '').trim() || null,
        // 窄屏运行趋势的当前量纲（data-dim 由 BmsChartsPanel 写在 .trend-mobile 上）
        dim: mobileWrap ? mobileWrap.getAttribute('data-dim') : null,
      }
    }

    // ECharts 6 把轴标签/图例文字放在 **tspan** displayable 上（type='tspan'），
    // 容器 ZRText 本身不在 displayList 里 —— 直接按 style.text 收，再按 (text,rect) 去重。
    const textRects = (chart) => {
      const out = []
      const seen = new Set()
      try {
        const zr = chart.getZr()
        const list = zr.storage.getDisplayList(true) || []
        for (const e of list) {
          if (!e || e.ignore || e.invisible) continue
          const s = e.style
          if (!s || typeof s.text !== 'string') continue
          const txt = String(s.text).trim()
          if (!txt) continue
          if (s.opacity === 0) continue
          const b = e.getBoundingRect().clone()
          if (e.transform) b.applyTransform(e.transform)
          const rec = { t: txt, x: round(b.x), y: round(b.y), w: round(b.width), h: round(b.height), fs: round(s.fontSize), zrType: e.type || null }
          const key = rec.t + '|' + rec.x + '|' + rec.y + '|' + rec.w + '|' + rec.h
          if (seen.has(key)) continue
          seen.add(key)
          out.push(rec)
        }
      } catch { /* 取不到就空 */ }
      return out
    }

    const probe = async (el) => {
      const ex = findExposed(el)
      let chart = null
      let source = null
      if (ex?.getChartInstance) {
        try { chart = ex.getChartInstance() } catch { chart = null }
        if (chart) source = 'defineExpose'
      }
      if (!chart) {
        chart = await instanceByDom(el)
        if (chart) source = 'echarts.getInstanceByDom'
      }
      const cv = el.querySelector('canvas')
      const cr = el.getBoundingClientRect()
      const rec = {
        instanceSource: source,
        container: rect2(cr.x, cr.y, cr.width, cr.height),
        canvas: cv ? { cssW: cv.clientWidth, cssH: cv.clientHeight, pxW: cv.width, pxH: cv.height } : null,
      }
      if (!chart) { rec.error = '未取到 echarts 实例'; return rec }

      try { rec.topLayout = ex?.getTopLayout ? ex.getTopLayout() : null } catch { rec.topLayout = null }
      let plot = null
      try { plot = ex?.getPlotRect ? ex.getPlotRect() : null } catch { plot = null }
      if (!plot) {
        try {
          const g = chart.getModel().getComponent('grid')
          const r = g.coordinateSystem.getRect()
          plot = { x: r.x, y: r.y, width: r.width, height: r.height }
        } catch { plot = null }
      }
      rec.plot = plot ? rect2(plot.x, plot.y, plot.width, plot.height) : null

      let opt = null
      try { opt = chart.getOption() } catch { opt = null }
      if (opt) {
        const g0 = opt.grid?.[0] || null
        rec.gridOpt = g0 ? { left: g0.left ?? null, right: g0.right ?? null, top: g0.top ?? null, bottom: g0.bottom ?? null, containLabel: g0.containLabel ?? null } : null
        const lg = opt.legend?.[0] || null
        rec.legendOpt = lg ? {
          show: lg.show ?? true, top: lg.top ?? null, bottom: lg.bottom ?? null, left: lg.left ?? null,
          type: lg.type ?? null, itemGap: lg.itemGap ?? null, itemWidth: lg.itemWidth ?? null,
          fontSize: lg.textStyle?.fontSize ?? null,
        } : null
        const xa = opt.xAxis?.[0] || {}
        rec.xAxisOpt = {
          type: xa.type ?? null, name: xa.name ?? null,
          hideOverlap: xa.axisLabel?.hideOverlap ?? null,
          fontSize: xa.axisLabel?.fontSize ?? null,
          rotate: xa.axisLabel?.rotate ?? null,
          interval: xa.axisLabel?.interval ?? null,
        }
        rec.yAxisOpt = (opt.yAxis || []).map((a) => ({
          name: a.name ?? null, position: a.position ?? 'left',
          nameLocation: a.nameLocation ?? null,
          fontSize: a.axisLabel?.fontSize ?? null,
        }))
        rec.seriesCount = (opt.series || []).length
        rec.seriesNames = (opt.series || []).map((s) => s.name ?? '')
        // compact 档：工程内 getTopLayout() 已返回真实排版档；这里再用 grid.top 交叉核对
        rec.compactByLayout = rec.topLayout ? rec.topLayout.compact : null
      }

      // 刻度数（scale 内部真实刻度，不是 axisLabel 估算）
      try { rec.xTickCount = chart.getModel().getComponent('xAxis', 0).axis.scale.getTicks().length } catch { rec.xTickCount = null }
      try {
        rec.yTickCounts = (opt?.yAxis || []).map((_, i) => {
          try { return chart.getModel().getComponent('yAxis', i).axis.scale.getTicks().length } catch { return null }
        })
      } catch { rec.yTickCounts = null }
      rec.yAxisCount = rec.yAxisOpt ? rec.yAxisOpt.length : null

      // ② zrender displayList 实测文字 bbox
      const texts = textRects(chart)
      const P = rec.plot
      const C = rec.canvas
      const legendNames = new Set(
        (opt?.legend?.[0]?.data || rec.seriesNames || []).map((s) => String(s)),
      )
      const yNames = new Set((rec.yAxisOpt || []).map((a) => a.name).filter(Boolean).map(String))
      const xName = rec.xAxisOpt?.name ? String(rec.xAxisOpt.name) : null
      /*
       * 角色判定顺序 = 由「内容身份」到「几何位置」：
       *   先按文字内容认图例项 / 轴名（图例项溢出到绘图区下方时，纯几何会把它们误判成 x 轴标签，
       *   而「图例压到 x 轴标签带上」正是要量的缺陷本身，不能被归类吃掉）；
       *   剩下的再按矩形相对绘图区的位置分左右轴标签 / x 轴标签。
       */
      const tags = texts.map((t) => {
        const cx = t.x + t.w / 2
        const cy = t.y + t.h / 2
        let role = 'other'
        if (legendNames.has(t.t)) role = 'legend'
        else if (yNames.has(t.t)) role = 'yAxisName'
        else if (xName && t.t === xName) role = 'xAxisName'
        else if (P) {
          if (t.x + t.w <= P.x + 2) role = 'yLabelLeft'
          else if (t.x >= P.x + P.w - 2) role = 'yLabelRight'
          else if (t.y >= P.y + P.h - 0.5) role = 'xLabel'
          else if (t.y + t.h <= P.y + 0.5) role = 'aboveOther'
          else role = 'inside'
        }
        return { ...t, cx: round(cx), cy: round(cy), role }
      })
      rec.texts = tags

      const of = (r) => tags.filter((t) => t.role === r)
      const xLab = of('xLabel').sort((a, b) => a.x - b.x)
      const legend = of('legend').sort((a, b) => a.y - b.y || a.x - b.x)
      const yNamesR = of('yAxisName')
      const yLeft = of('yLabelLeft').sort((a, b) => a.y - b.y)
      const yRight = of('yLabelRight').sort((a, b) => a.y - b.y)

      const pairsOverlapH = (arr) => {
        let n = 0
        let minGap = null
        for (let i = 0; i + 1 < arr.length; i++) {
          const a = arr[i]
          const b = arr[i + 1]
          const gap = b.x - (a.x + a.w)
          if (minGap === null || gap < minGap) minGap = round(gap)
          if (gap < -0.5) n++
        }
        return { pairs: n, minGap }
      }
      const pairsOverlapV = (arr) => {
        let n = 0
        let minGap = null
        for (let i = 0; i + 1 < arr.length; i++) {
          const a = arr[i]
          const b = arr[i + 1]
          const gap = b.y - (a.y + a.h)
          if (minGap === null || gap < minGap) minGap = round(gap)
          if (gap < -0.5) n++
        }
        return { pairs: n, minGap }
      }

      const legendRect = legend.length
        ? rect2(Math.min(...legend.map((t) => t.x)), Math.min(...legend.map((t) => t.y)),
          Math.max(...legend.map((t) => t.x + t.w)) - Math.min(...legend.map((t) => t.x)),
          Math.max(...legend.map((t) => t.y + t.h)) - Math.min(...legend.map((t) => t.y)))
        : null
      const legendRowsMeasured = legend.length ? new Set(legend.map((t) => Math.round(t.y / 2) * 2)).size : 0

      let legendAxisNameOverlap = 0
      for (const l of legend) for (const a of yNamesR) legendAxisNameOverlap += area(l, a)
      // 图例是否压绘图区 / 压 x 轴标签
      let legendPlotOverlap = 0
      let legendXLabelOverlap = 0
      if (legendRect && P) legendPlotOverlap = area(legendRect, P)
      for (const l of legend) for (const xl of xLab) legendXLabelOverlap += area(l, xl)

      // 图例项的实际渲染位置 vs 绘图区（"图例压绘图区/压 x 轴标签带"直接计数，不靠估）
      const legendInPlot = legend.filter((l) => P && area(l, P) > 0).length
      const legendBelowPlot = legend.filter((l) => P && l.y >= P.y + P.h - 0.5).length
      const legendOutsideCanvas = legend.filter((l) => C && (l.x < -0.5 || l.y < -0.5 || l.x + l.w > C.cssW + 0.5 || l.y + l.h > C.cssH + 0.5)).length

      rec.measured = {
        legendItemsRendered: legend.length,
        legendItemsExpected: rec.seriesCount ?? null,
        legendInPlot,
        legendBelowPlot,
        legendOutsideCanvas,
        xLabelCount: xLab.length,
        xLabels: xLab.map((t) => t.t),
        xLabelTexts: xLab.slice(0, 3).map((t) => t.t).concat(xLab.length > 4 ? ['…'] : []).concat(xLab.length > 3 ? [xLab[xLab.length - 1].t] : []),
        xLabelMaxW: xLab.length ? round(Math.max(...xLab.map((t) => t.w))) : null,
        xLabelBand: xLab.length ? rect2(Math.min(...xLab.map((t) => t.x)), Math.min(...xLab.map((t) => t.y)),
          Math.max(...xLab.map((t) => t.x + t.w)) - Math.min(...xLab.map((t) => t.x)),
          Math.max(...xLab.map((t) => t.y + t.h)) - Math.min(...xLab.map((t) => t.y))) : null,
        xLabelOverlap: pairsOverlapH(xLab),
        xLabelClipped: xLab.filter((t) => t.x < -0.5 || t.x + t.w > (C?.cssW ?? 0) + 0.5).length,
        legendCount: legend.length,
        legendRowsMeasured,
        legendRect,
        legendBottom: legendRect ? round(legendRect.y + legendRect.h) : null,
        legendPlotOverlap: round(legendPlotOverlap),
        legendAxisNameOverlap: round(legendAxisNameOverlap),
        legendXLabelOverlap: round(legendXLabelOverlap),
        yAxisNameRects: yNamesR.map((t) => ({ t: t.t, ...rect2(t.x, t.y, t.w, t.h) })),
        yLeftCount: yLeft.length,
        yRightCount: yRight.length,
        yLeftBandW: P ? round(P.x) : null,
        yRightBandW: C && P ? round(C.cssW - (P.x + P.w)) : null,
        yRightMaxW: yRight.length ? round(Math.max(...yRight.map((t) => t.w))) : null,
        yRightOverlap: pairsOverlapV(yRight),
        yLeftOverlap: pairsOverlapV(yLeft),
        xLabelSlotW: P && rec.xTickCount ? round(P.w / Math.max(1, xLab.length)) : null,
      }

      // ③ 占比（面积口径 = 绘图区 w×h / canvas CSS w×h）
      if (P && C && C.cssW && C.cssH) {
        rec.plotArea = { w: P.w, h: P.h, area: round(P.w * P.h), canvasArea: round(C.cssW * C.cssH) }
        rec.plotAreaPct = round((P.w * P.h) / (C.cssW * C.cssH) * 100)
        rec.topReservePct = round(P.y / C.cssH * 100)
        rec.bottomReservePct = round((C.cssH - (P.y + P.h)) / C.cssH * 100)
        rec.leftReservePct = round(P.x / C.cssW * 100)
        rec.rightReservePct = round((C.cssW - (P.x + P.w)) / C.cssW * 100)
        // 顶部空档：图例底边 → 绘图区顶边之间没被任何东西使用的像素
        rec.topBlankPx = rec.measured?.legendBottom != null ? round(P.y - rec.measured.legendBottom) : round(P.y)
        // 底部：绘图区底边 → canvas 底边（应刚好放 x 轴标签）
        rec.bottomBlankPx = round(C.cssH - (P.y + P.h))
        if (rec.measured?.xLabelBand) rec.bottomBlankAfterLabel = round(C.cssH - (rec.measured.xLabelBand.y + rec.measured.xLabelBand.h))
        // 绘图区是否越出 canvas / 被压到标签上（ECharts 在边距 > 高度时会钳位 grid）
        rec.plotFitsCanvas = P.y + P.h <= C.cssH + 0.5
      }
      return rec
    }

    const charts = []
    const lineEls = [...document.querySelectorAll('.line-chart')]
    for (let i = 0; i < lineEls.length; i++) {
      const el = lineEls[i]
      const rec = await probe(el)
      charts.push({ kind: 'line', idx: i, section: sectionOf(el), ...rec })
    }
    // 柱状图（电芯电压分布）：不是 .line-chart，但同属「页面图表实例」，单独量避免清单漏项
    const bars = []
    const barEls = [...document.querySelectorAll('.cell-voltage-chart')]
    for (let i = 0; i < barEls.length; i++) {
      const el = barEls[i]
      const rec = await probe(el)
      bars.push({ kind: 'bar', idx: i, section: sectionOf(el), ...rec })
    }

    return {
      pageW: document.documentElement.clientWidth,
      scrollH: document.body.scrollHeight,
      dpr: window.devicePixelRatio,
      lineChartCount: lineEls.length,
      barChartCount: barEls.length,
      charts,
      bars,
      // 卡片分块（诊断卡）保留，便于与历史基线对齐
      diagCards: [...document.querySelectorAll('.diag-card')].filter((c) => c.offsetParent !== null).map((card) => {
        const cr = card.getBoundingClientRect()
        const pick = (sel) => {
          const n = card.querySelector(sel)
          if (!n) return null
          const r = n.getBoundingClientRect()
          return { h: round(r.height), w: round(r.width), top: round(r.top - cr.top) }
        }
        return { h: round(cr.height), w: round(cr.width), toolbar: pick('.diag-toolbar'), plotBox: pick('.diag-plot'), foot: pick('.diag-foot'), title: (card.querySelector('.card-title')?.textContent || '').trim() }
      }),
      collapseHeads: [...document.querySelectorAll('.collapse-head')].map((h) => ({
        title: (h.querySelector('.collapse-title')?.textContent || '').trim(),
        expanded: h.getAttribute('aria-expanded'),
      })),
    }
  })

  // ── 截图 ──
  const SLUG = { '温度趋势': 'temp', 'SOC 与电流': 'soc', '电芯电压历史趋势': 'cell-history', '运行趋势': 'trend', '电芯电压分布': 'cell-dist', '历史趋势': 'charts-block' }
  const shots = []
  const shotPage = async (name) => {
    const file = path.join(OUT, name + '.png')
    await page.screenshot({ path: file, fullPage: true }).catch(() => {})
    shots.push(file)
  }
  await shotPage('page-' + W + 'x' + H + '-full')
  // 每张图表所在卡片 / 折叠块整块截图
  const seen = new Set()
  for (const c of [...metrics.charts, ...metrics.bars]) {
    const title = c.section?.cardTitle
    if (!title || seen.has(title)) continue
    seen.add(title)
    const slug = SLUG[title] || 'sec' + c.idx
    const loc = page.locator('.card', { has: page.locator('.card-title', { hasText: title }) }).first()
    if (await loc.count().catch(() => 0)) {
      const file = path.join(OUT, 'card-' + slug + '-' + W + '.png')
      await loc.screenshot({ path: file }).catch(() => {})
      shots.push(file)
    }
  }
  // 折叠块整块（历史趋势 = 两张卡 + 折叠头，作为一个区块的完整视觉）
  for (const h of metrics.collapseHeads || []) {
    if (h.expanded !== 'true' && h.expanded !== true) continue
    const slug = SLUG[h.title]
    if (!slug) continue
    const block = page.locator('.collapse-block', { has: page.locator('.collapse-title', { hasText: h.title }) }).first()
    if (await block.count().catch(() => 0)) {
      const file = path.join(OUT, 'block-' + slug + '-' + W + '.png')
      await block.screenshot({ path: file }).catch(() => {})
      shots.push(file)
    }
  }

  const bodyText = await page.evaluate(() => document.body.innerText.slice(0, 2000))
  const report = {
    viewport: { w: W, h: H, dpr: DPR, theme: THEME },
    url: page.url(),
    generatedAt: new Date().toISOString(),
    bodyText,
    metrics,
    errors,
    consoleErrors: consoleErrors.slice(0, 20),
    shots,
  }
  fs.writeFileSync(path.join(OUT, 'metrics-' + W + 'x' + H + '.json'), JSON.stringify(report, null, 2))
  // 终端只打摘要，完整数据在 JSON
  console.log(JSON.stringify({
    viewport: report.viewport,
    lineChartCount: metrics.lineChartCount,
    barChartCount: metrics.barChartCount,
    errors,
    charts: metrics.charts.map((c) => ({
      idx: c.idx, card: c.section.cardTitle, collapse: c.section.collapseTitle,
      container: c.container, canvas: c.canvas, plot: c.plot, pct: c.plotAreaPct,
      xTicks: c.xTickCount, yAxes: c.yAxisCount, hideOverlap: c.xAxisOpt?.hideOverlap,
      legend: c.legendOpt, measured: c.measured ? { xOverlap: c.measured.xLabelOverlap, xClipped: c.measured.xLabelClipped, legendBottom: c.measured.legendBottom, legendAxisNameOverlap: c.measured.legendAxisNameOverlap, legendPlotOverlap: c.measured.legendPlotOverlap } : null,
    })),
    bars: metrics.bars.map((b) => ({ idx: b.idx, card: b.section.cardTitle, container: b.container, canvas: b.canvas, plot: b.plot, pct: b.plotAreaPct })),
  }, null, 2))
  await browser.close()
}

run().catch((e) => { console.error('MATRIX FAILED', e); process.exit(1) })
