// 逆变器功率流向「电网支路」视觉验收：读取真实浏览器渲染结果 + 截图。
// 运行：node tools/pf-grid-visual.mjs（工作目录 frontend-shared）
import { chromium } from '@playwright/test'
import assert from 'node:assert/strict'
import { mkdirSync, writeFileSync } from 'node:fs'
import path from 'node:path'

const URL = process.env.PF_URL || 'http://127.0.0.1:5188/dev/mock-power-flow'
const EXE = process.env.CHROMIUM_PATH || '/home/sun/.cache/ms-playwright/chromium-1228/chrome-linux64/chrome'
const OUT = process.env.PF_EVIDENCE || path.resolve('..', 'docs/验证/证据-2026-11-18-逆变器电网支路')
mkdirSync(OUT, { recursive: true })

const MAINTENANCE_READY = {
  code: 200,
  message: 'ok',
  data: {
    ready: true,
    failed: false,
    percent: 100,
    current_step: '',
    current_label: '',
    started_at: new Date().toISOString(),
    elapsed_ms: 1,
    steps: [],
    version: 'visual-verify',
  },
}
async function mockMaintenance(ctx) {
  await ctx.route('**/api/v1/system/maintenance', route => route.fulfill({
    status: 200,
    contentType: 'application/json',
    body: JSON.stringify(MAINTENANCE_READY),
  }))
}
const browser = await chromium.launch({ executablePath: EXE, args: ['--no-sandbox', '--disable-dev-shm-usage'] })
const errors = []
const report = { url: URL, desktop: [], mobile: [], errors }

async function readDesktop(page) {
  return page.$$eval('.pf-case', (nodes) => nodes.map((el) => {
    const svg = el.querySelector('svg.power-flow-svg')
    const grid = el.querySelector('.pf-node.grid')
    const circle = grid?.querySelector('circle')
    const paths = svg ? [...svg.querySelectorAll('path.pf-link')] : []
    const gridPath = paths[1]
    const batteryPath = paths[3]
    const gridStyle = gridPath ? getComputedStyle(gridPath) : null
    return {
      id: el.getAttribute('data-case'),
      title: el.closest('.el-card')?.querySelector('.el-card__header')?.textContent?.trim() || '',
      gridStatus: grid?.querySelector('.pf-label-status')?.getAttribute('data-status') || '',
      gridCircle: circle ? circle.getAttribute('class') || '' : '',
      gridClasses: gridPath ? gridPath.getAttribute('class') || '' : '',
      gridMarker: gridPath ? gridPath.getAttribute('marker-end') : null,
      gridStroke: gridStyle?.stroke || '',
      gridDash: gridStyle?.strokeDasharray || '',
      batteryClasses: batteryPath ? batteryPath.getAttribute('class') || '' : '',
      batteryMarker: batteryPath ? batteryPath.getAttribute('marker-end') : null,
      legend: [...el.querySelectorAll('.pf-legend .pf-legend-note')].map(n => n.textContent.trim()),
    }
  }))
}

async function readMobile(page) {
  return page.$$eval('.pf-case', (nodes) => nodes.map((el) => {
    const mobile = el.querySelector('.pf-mobile')
    if (!mobile) return { id: el.getAttribute('data-case'), mobile: false }
    const top = [...mobile.querySelectorAll('.pfm-arrow-row')][0]?.querySelectorAll('.pfm-arrow') || []
    const gridNode = mobile.querySelector('[data-branch="grid"]')
    return {
      id: el.getAttribute('data-case'),
      mobile: true,
      gridStatus: gridNode?.querySelector('.pfm-status')?.getAttribute('data-status') || '',
      gridArrow: top[1]?.textContent?.trim() || '',
      gridArrowClass: top[1]?.getAttribute('class') || '',
      gridNodeClass: gridNode?.getAttribute('class') || '',
    }
  }))
}

async function shootCases(page, cases, prefix) {
  const wanted = new Set(['discharge', 'grid-battery', 'grid-line', 'grid-off', 'grid-standby'])
  for (const c of cases) {
    if (!wanted.has(c.id)) continue
    const card = page.locator(`.pf-case[data-case="${c.id}"]`).locator('xpath=ancestor::div[contains(@class,"el-card")][1]')
    await card.screenshot({ path: path.join(OUT, `${prefix}-${c.id}.png`) })
  }
}

try {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1 })
  await mockMaintenance(ctx)
  const page = await ctx.newPage()
  page.on('console', m => { if (m.type() === 'error') errors.push(m.text()) })
  page.on('pageerror', e => errors.push(String(e)))
  await page.goto(URL, { waitUntil: 'domcontentloaded', timeout: 30000 })
  await page.waitForSelector('.mock-pf', { timeout: 30000 })
  await page.waitForSelector('.pf-case[data-case="grid-battery"] svg', { timeout: 30000 })
  await page.waitForTimeout(800)
  report.desktop = await readDesktop(page)
  const byId = Object.fromEntries(report.desktop.map(c => [c.id, c]))
  for (const id of ['discharge', 'grid-battery', 'grid-line', 'grid-off', 'grid-standby']) assert(byId[id], `缺少工况 ${id}`)
  assert.equal(byId['grid-battery'].gridStatus, '未使用')
  assert.equal(byId['grid-battery'].gridMarker, null, '电池模式电网不得有箭头')
  assert.match(byId['grid-battery'].gridClasses, /is-idle/)
  assert.doesNotMatch(byId['grid-battery'].gridClasses, /\bon\b/, '电池模式电网不得使用绿色 on 样式')
  assert.doesNotMatch(byId['grid-battery'].gridCircle, /\bon\b/, '电池模式电网节点不得点亮')
  assert.notEqual(byId['grid-battery'].gridStroke, 'rgb(103, 194, 58)', '电池模式电网不得是 success 绿色')
  assert.equal(byId['grid-line'].gridStatus, '供电')
  assert.equal(byId['grid-line'].gridMarker, 'url(#pf-arrow-flow)')
  assert.match(byId['grid-line'].gridClasses, /\bon\b/)
  assert.equal(byId['grid-line'].gridDash, 'none', '市电供电必须是绿色实线')
  assert.equal(byId['grid-off'].gridStatus, '未接入')
  assert.equal(byId['grid-off'].gridMarker, null)
  assert.equal(byId['grid-standby'].gridStatus, '待机')
  assert.equal(byId['grid-standby'].gridMarker, null)
  assert.equal(byId['discharge'].batteryMarker, 'url(#pf-arrow-discharge)')
  assert.equal(byId['charge'].batteryMarker, 'url(#pf-arrow-charge)')
  assert.equal(byId['charge'].gridMarker, 'url(#pf-arrow-flow)')
  await page.screenshot({ path: path.join(OUT, 'desktop-full.png'), fullPage: true })
  await shootCases(page, report.desktop, 'desktop')
  await ctx.close()

  const mctx = await browser.newContext({ viewport: { width: 393, height: 900 }, isMobile: true, hasTouch: true, deviceScaleFactor: 1 })
  await mockMaintenance(mctx)
  const mpage = await mctx.newPage()
  mpage.on('console', m => { if (m.type() === 'error') errors.push('mobile: ' + m.text()) })
  mpage.on('pageerror', e => errors.push('mobile: ' + String(e)))
  await mpage.goto(URL, { waitUntil: 'domcontentloaded', timeout: 30000 })
  await mpage.waitForSelector('.mock-pf', { timeout: 30000 })
  await mpage.waitForSelector('.pf-case[data-case="grid-battery"] .pf-mobile', { timeout: 30000 })
  await mpage.waitForTimeout(500)
  report.mobile = await readMobile(mpage)
  const mById = Object.fromEntries(report.mobile.map(c => [c.id, c]))
  assert.equal(mById['grid-battery'].gridStatus, '未使用')
  assert.equal(mById['grid-battery'].gridArrow, '·')
  assert.match(mById['grid-battery'].gridArrowClass, /is-idle/)
  assert.equal(mById['grid-line'].gridStatus, '供电')
  assert.equal(mById['grid-line'].gridArrow, '↓')
  assert.match(mById['grid-line'].gridArrowClass, /is-flow/)
  assert.equal(mById['grid-off'].gridStatus, '未接入')
  assert.equal(mById['grid-standby'].gridStatus, '待机')
  await mpage.screenshot({ path: path.join(OUT, 'mobile-full.png'), fullPage: true })
  await shootCases(mpage, report.mobile, 'mobile')
  await mctx.close()

  writeFileSync(path.join(OUT, 'visual-report.json'), JSON.stringify(report, null, 2))
  const lines = []
  lines.push('URL: ' + URL)
  lines.push('DESKTOP:')
  for (const c of report.desktop) lines.push(`  ${c.id.padEnd(14)} status=${c.gridStatus.padEnd(4)} marker=${String(c.gridMarker).padEnd(25)} classes=${c.gridClasses} stroke=${c.gridStroke} dash=${c.gridDash}`)
  lines.push('MOBILE:')
  for (const c of report.mobile) lines.push(`  ${c.id.padEnd(14)} status=${c.gridStatus} arrow=${c.gridArrow} class=${c.gridArrowClass}`)
  lines.push('CONSOLE_ERRORS: ' + errors.length)
  writeFileSync(path.join(OUT, 'visual-report.txt'), lines.join('\n') + '\n')
  console.log(lines.join('\n'))
} finally {
  await browser.close()
}