
/**
 * 移动端图表审计 harness（临时工具，不参与构建/门禁）
 *
 * 用途：离线渲染真实 BMS 详情页（无需后端），在指定视口下测量图表卡片几何，
 * 输出 JSON + 截图，作为「移动端图表显示效果差」的量化取证。
 *
 * 用法：node e2e/chart-audit.mjs [--w 393] [--h 851] [--dpr 3] [--out ../../.analysis/chart-mobile/before]
 */
import { chromium } from '@playwright/test'
import fs from 'node:fs'
import path from 'node:path'

const args = Object.fromEntries(
  process.argv.slice(2).reduce((acc, cur, i, arr) => {
    if (cur.startsWith('--')) acc.push([cur.slice(2), arr[i + 1] && !arr[i + 1].startsWith('--') ? arr[i + 1] : 'true'])
    return acc
  }, []),
)
const W = Number(args.w || 393)
const H = Number(args.h || 851)
const DPR = Number(args.dpr || 3)
const OUT = path.resolve(args.out || '../../.analysis/chart-mobile/before')
const BASE = args.base || 'http://127.0.0.1:5173'
const DEVICE_ID = args.device || '14'
const EXE = '/home/sun/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome'

fs.mkdirSync(OUT, { recursive: true })

// ── 假数据：与真机形态一致（温度 3 路同单位；SOC % / 电流 A 双量纲）──
const seriesFor = (cat, startMs, endMs) => {
  const raw = {
    temperature_1: { base: 26.4, amp: 1.6, rise: 4.6, riseAfter: 0.58, noise: 0.25 },
    temperature_2: { base: 24.8, amp: 1.2, rise: 1.2, riseAfter: 0.6, noise: 0.2 },
    temperature_3: { base: 25.4, amp: 1.4, rise: 0.8, riseAfter: 0.62, noise: 0.2 },
    rsoc: { base: 86, amp: 6, rise: -12, riseAfter: 0.55, noise: 0.3 },
    current: { base: -5.4, amp: 3, rise: 26, riseAfter: 0.42, noise: 0.8 },
  }[cat]
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
    out.push({ created_at: new Date(t).toISOString(), value: Number(v.toFixed(3)) })
  }
  return out
}

const envelope = (data) => JSON.stringify({ code: 0, message: 'ok', data })

const run = async () => {
  const browser = await chromium.launch({ executablePath: EXE, args: ['--no-sandbox', '--disable-dev-shm-usage'] })
  const ctx = await browser.newContext({
    viewport: { width: W, height: H },
    deviceScaleFactor: DPR,
    isMobile: W < 900,
    hasTouch: W < 900,
    locale: 'zh-CN',
    colorScheme: args.theme === 'light' ? 'light' : 'dark',
  })
  const page = await ctx.newPage()
  const errors = []
  page.on('pageerror', (e) => errors.push(String(e).slice(0, 200)))

  await page.addInitScript(() => {
    localStorage.setItem('token', 'audit-token')
    localStorage.setItem('user', JSON.stringify({ id: 1, username: 'audit', role: 'admin', nickname: '审计' }))
  })

  // catch-all 必须先注册：Playwright 路由按**注册逆序**匹配，后注册的优先。
  await page.route('**/api/v1/**', (r) => r.fulfill({ status: 404, contentType: 'application/json', body: '{"code":404,"message":"mock 未提供"}' }))
  // 启动门禁：失败即 fail-open（MaintenanceUnsupportedError）
  await page.route('**/api/v1/system/maintenance**', (r) => r.fulfill({ status: 404, contentType: 'application/json', body: '{"code":404,"message":"not found"}' }))
  /*
   * 历史数据（两条路由，顺序决定正确性）：
   * Playwright 的路由按**注册逆序**匹配（后注册先检查），而 glob '.../historical**'
   * 同时匹配 '.../historical-batch'。所以「逐测点降级」路由必须先注册，
   * 「batch」路由后注册 —— 否则 batch 请求会被单测点处理器接走（本轮实测踩到）。
   */
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
  // 各传感器最新值（首屏兜底快照）
  await page.route('**/api/v1/edge-devices/' + DEVICE_ID + '/latest-values', (r) => {
    const nowIso = new Date().toISOString()
    const vals = [
      ['total_voltage', 53.15, 'V'], ['current', -5.39, 'A'], ['rsoc', 81, '%'],
      ['remaining_capacity', 81.0, 'Ah'], ['nominal_capacity', 101, 'Ah'],
      ['temperature_1', 29.9, '℃'], ['temperature_2', 30.4, '℃'], ['temperature_3', 30.8, '℃'],
      ['cell_voltage_max', 3.325, 'V'], ['cell_voltage_min', 3.32, 'V'], ['cell_count', 16, ''],
      ['protection_status', 0, ''], ['fet_status', 3, ''], ['cycle_count', 128, ''],
    ].map(([sensor_name, value, unit]) => ({ sensor_name, value, unit, timestamp: nowIso }))
    r.fulfill({ status: 200, contentType: 'application/json', body: envelope(vals) })
  })
  // 设备详情（决定 deviceOnline 与设备类型；离线会让诊断卡渲染「设备离线」空态）
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

  // 展开两张诊断卡（默认收起）
  for (const title of ['温度趋势', 'SOC 与电流']) {
    const btn = page.locator('.collapse-head', { hasText: title }).first()
    if (await btn.count()) {
      const expanded = await btn.getAttribute('aria-expanded')
      if (expanded !== 'true') await btn.click({ force: true }).catch(() => {})
      await page.waitForTimeout(700)
    }
  }
  await page.waitForTimeout(1200)

  // ── 量测：容器/canvas/绘图区 rect + 卡片分块高度 ──
  const metrics = await page.evaluate(async () => {
    const round = (n) => Math.round(n * 100) / 100
    let echarts = null
    try {
      const hit = performance.getEntriesByType('resource').map((x) => x.name).find((n) => /echarts/i.test(n) && n.endsWith('.js'))
      if (hit) echarts = await import(hit)
    } catch { /* 忽略 */ }

    const charts = []
    document.querySelectorAll('.line-chart').forEach((el, idx) => {
      const r = el.getBoundingClientRect()
      const canvas = el.querySelector('canvas')
      let plot = null
      let ticks = null
      if (echarts?.getInstanceByDom) {
        const inst = echarts.getInstanceByDom(el)
        if (inst) {
          try {
            const grid = inst.getModel().getComponent('grid')
            const rect = grid.coordinateSystem.getRect()
            plot = { x: round(rect.x), y: round(rect.y), w: round(rect.width), h: round(rect.height) }
          } catch { /* 忽略 */ }
          try {
            const m = inst.getModel()
            const xa = m.getComponent('xAxis', 0)
            const ya = m.getComponent('yAxis', 0)
            ticks = {
              x: xa?.axis?.scale?.getTicks?.()?.length ?? null,
              y: ya?.axis?.scale?.getTicks?.()?.length ?? null,
              yAxes: inst.getOption().yAxis?.length ?? 1,
            }
          } catch { /* 忽略 */ }
        }
      }
      charts.push({
        idx,
        container: { w: round(r.width), h: round(r.height) },
        canvas: canvas ? { cssW: canvas.clientWidth, cssH: canvas.clientHeight, pxW: canvas.width, pxH: canvas.height } : null,
        plot,
        ticks,
        legendText: (el.parentElement?.querySelectorAll('.line-chart') ? '' : ''),
      })
    })

    // 卡片分块（诊断卡）
    const cards = []
    document.querySelectorAll('.diag-card').forEach((card) => {
      if (card.offsetParent === null) return
      const cr = card.getBoundingClientRect()
      const pick = (sel) => {
        const n = card.querySelector(sel)
        if (!n) return null
        const r = n.getBoundingClientRect()
        return { h: round(r.height), w: round(r.width), top: round(r.top - cr.top) }
      }
      cards.push({
        h: round(cr.height),
        w: round(cr.width),
        toolbar: pick('.diag-toolbar'),
        plotBox: pick('.diag-plot'),
        foot: pick('.diag-foot'),
        title: card.querySelector('.card-title')?.textContent?.trim() || '',
      })
    })

    // 折叠头/页头等占位
    const pageW = document.documentElement.clientWidth
    return { pageW, pageH: document.body.scrollHeight, charts, cards }
  })

  const shots = []
  const shot = async (name, locator) => {
    const file = path.join(OUT, name + '.png')
    if (locator) await locator.screenshot({ path: file }).catch(() => {})
    else await page.screenshot({ path: file, fullPage: false })
    shots.push(file)
  }
  await shot('page-' + W + 'x' + H + '-full')
  const tempCard = page.locator('.diag-card').nth(0)
  const socCard = page.locator('.diag-card').nth(1)
  if (await tempCard.count()) await shot('card-temp-' + W, tempCard)
  if (await socCard.count()) await shot('card-soc-' + W, socCard)

  const bodyText = await page.evaluate(() => document.body.innerText.slice(0, 1500))
  const report = { viewport: { w: W, h: H, dpr: DPR }, url: page.url(), bodyText, metrics, errors, shots }
  fs.writeFileSync(path.join(OUT, 'metrics-' + W + 'x' + H + '.json'), JSON.stringify(report, null, 2))
  console.log(JSON.stringify(report, null, 2))
  await browser.close()
}

run().catch((e) => { console.error('AUDIT FAILED', e); process.exit(1) })
