
/**
 * D1 域定向取证探针：图表主题 / KPI 卡 / tooltip / 降采样。
 * 仅在 frontend-shared/tools/ 下新增，不改 src（契约 §7.2）。
 *
 * 关键手段：page.route 拦截 unified-data 接口注入合成数据，
 * 让审计库中原本无历史数据的图表真实渲染，从而能取到 canvas 容器与 tooltip 的实际色值。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

const BASE = process.env.UIUX_BASE || 'http://127.0.0.1:8082';
const OUT = process.env.UIUX_OUT || '/tmp/uiux-d1';
const CHROME = process.env.UIUX_CHROME || '/snap/bin/chromium';
const USER = 'admin', PASS = 'UiuxAudit2026!';
fs.mkdirSync(OUT, { recursive: true });

const NOW = Date.now();
const pt = (minAgo, value) => ({
  id: minAgo, device_id: 7053, sensor_name: 'x', value, unit: '',
  timestamp: new Date(NOW - minAgo * 60000).toISOString(),
  created_at: new Date(NOW - minAgo * 60000).toISOString(),
});
const mk = (values) => values.map((v, i) => pt((values.length - i) * 30, v));
const wobble = (n, base, amp) => Array.from({ length: n }, (_, i) => +(base + amp * Math.sin(i / 5)).toFixed(3));

const batchPayload = (cats) => ({
  code: 200, message: 'ok',
  data: cats.map(([cat, values, unit]) => ({
    category: cat, data: mk(values).map(p => ({ ...p, sensor_name: cat, unit: unit || '' })),
  })),
});

const BMS_CATS = [
  ['temperature', wobble(120, 22.5, 3.4), 'C'],
  ['total_voltage', wobble(120, 48, 1.2), 'V'],
  ['cell_voltage_max', wobble(120, 3.35, 0.03), 'V'],
  ['cell_voltage_min', wobble(120, 3.28, 0.03), 'V'],
  ['current', wobble(120, 2, 0.4), 'A'],
  ['rsoc', wobble(120, 78, 6), '%'],
  ['remaining_capacity', wobble(120, 100, 3), 'Ah'],
  ['temperature_1', wobble(120, 24.9, 1.1), 'C'],
  ['temperature_2', wobble(120, 25.9, 1.1), 'C'],
  ['temperature_3', wobble(120, 26.9, 1.1), 'C'],
];
for (let i = 1; i <= 16; i++) BMS_CATS.push(['cell_voltage_' + i, wobble(120, 3.30 + i * 0.002, 0.02), 'V']);

const MEASURE = () => {
  const g = (el, p) => el ? getComputedStyle(el).getPropertyValue(p) : null;
  const rect = el => { if (!el) return null; const r = el.getBoundingClientRect(); return { w: +r.width.toFixed(1), h: +r.height.toFixed(1), x: +r.x.toFixed(1), y: +r.y.toFixed(1), right: +r.right.toFixed(1) }; };
  const out = { path: location.pathname, html: document.documentElement.className, dataTheme: document.documentElement.getAttribute('data-theme') };

  // 1) 图表容器（canvas 宿主）实际色值
  out.charts = [...document.querySelectorAll('.line-chart')].map((el, i) => {
    const cs = getComputedStyle(el);
    const cv = el.querySelector('canvas');
    return {
      idx: i, rect: rect(el),
      bg: cs.backgroundColor, color: cs.color, font: cs.fontFamily.slice(0, 24),
      canvas: cv ? { w: cv.width, h: cv.height, rect: rect(cv) } : null,
    };
  });

  // 2) 图表卡片背景（对比用）
  out.chartCardBg = (() => {
    const el = document.querySelector('.line-chart');
    if (!el) return null;
    let a = el.parentElement;
    while (a && !a.classList.contains('el-card')) a = a.parentElement;
    return a ? getComputedStyle(a).backgroundColor : null;
  })();

  // 3) root 主题 token 实际解析值（亮/暗两组对比用）
  const rs = getComputedStyle(document.documentElement);
  out.tokens = {};
  ['--color-primary','--color-danger','--color-warning','--color-success','--color-info','--color-adc','--terminal-accent','--terminal-warning','--text-color-primary','--text-color-regular','--border-color','--border-color-light','--bg-color-overlay'].forEach(t => out.tokens[t] = rs.getPropertyValue(t).trim());

  // 4) KPI / 统计卡：移动端四列契约（§4.3 统计卡）
  const grab = (sel) => [...document.querySelectorAll(sel)].map(el => {
    const cs = getComputedStyle(el);
    return { cls: (el.className||'').toString().slice(0,40), rect: rect(el), display: cs.display,
      cols: cs.gridTemplateColumns, gap: cs.gap || cs.columnGap,
      font: cs.fontSize, color: cs.color, bg: cs.backgroundColor, radius: cs.borderRadius,
      overflow: cs.overflow, textOverflow: cs.textOverflow, whiteSpace: cs.whiteSpace };
  });
  out.dashboardStats = grab('.dashboard-stats');
  out.dashboardStatValue = grab('.dashboard-stats .stat-value');
  out.dashboardStatLabel = grab('.dashboard-stats .stat-label');
  out.dashboardStatIcon = grab('.dashboard-stats .stat-icon');
  out.dashboardStatCard = grab('.dashboard-stats .el-card');
  out.dataStats = grab('.data-stats');
  out.dataStatValue = grab('.data-stats .stat-value');
  out.dataStatLabel = grab('.data-stats .stat-label');
  out.monitorStatValue = grab('.stat-cards .stat-value');
  out.monitorStatLabel = grab('.stat-cards .stat-label');
  out.monitorStatCard = grab('.stat-cards .el-card');
  out.controlGrid = grab('.control-grid');
  out.controlMetric = grab('.control-metric');
  out.controlStrong = grab('.control-metric strong');
  out.controlAttention = grab('.control-metric.attention strong');
  out.toolbarH2 = grab('.toolbar h2');
  out.statCardTitles = [...document.querySelectorAll('.stat-card .el-card__header')].map(e => e.textContent.trim().slice(0,30));

  // 5) 数值截断检测（§4.2.5 关键数值不得被 ellipsis 截断）
  out.ellipsisRisk = [...document.querySelectorAll('.stat-value, .stat-label, .control-metric strong, .control-metric span, .stat-card .stat-label')]
    .map(el => ({ t: el.textContent.trim().slice(0, 24), sw: el.scrollWidth, cw: el.clientWidth, trunc: el.scrollWidth > el.clientWidth + 1, cs: getComputedStyle(el).textOverflow }))
    .filter(x => x.trunc);

  // 6) 空态
  out.emptyStates = [...document.querySelectorAll('.empty-state, .el-empty')].map(el => ({
    cls: (el.className||'').toString().slice(0,50), text: el.textContent.trim().replace(/\s+/g,' ').slice(0, 70)
  }));

  // 7) 页面标题（页头）
  out.pageHeader = (() => { const el = document.querySelector('[class*="page-header"]'); return el ? el.textContent.trim().slice(0,20) : null; })();
  out.h1h2 = [...document.querySelectorAll('h1,h2')].map(e => e.textContent.trim().slice(0, 20));
  out.totalEls = document.querySelectorAll('body *').length;
  return out;
};

const TOOLTIP = () => {
  const el = document.querySelector('.line-chart');
  if (!el) return { err: 'no .line-chart' };
  const cands = [...el.querySelectorAll('div')].filter(d => {
    const s = getComputedStyle(d);
    return s.position === 'absolute' && d.textContent.trim().length > 0;
  });
  return cands.map(d => {
    const cs = getComputedStyle(d);
    return { rect: (() => { const r = d.getBoundingClientRect(); return { w: +r.width.toFixed(1), h: +r.height.toFixed(1) }; })(),
      bg: cs.backgroundColor, color: cs.color, border: cs.borderColor, fontSize: cs.fontSize,
      text: d.textContent.trim().replace(/\s+/g, ' ').slice(0, 60),
      innerColor: d.querySelector('b') ? getComputedStyle(d.querySelector('b')).color : null };
  });
};

const browser = await chromium.launch({ executablePath: CHROME, args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const report = { probe: 'uiux-d1-charts', base: BASE, themes: {}, notes: [] };

for (const theme of ['light', 'dark']) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', deviceScaleFactor: 1, colorScheme: theme });
  const page = await ctx.newPage();
  const consoleErrors = [];
  page.on('console', m => { if (m.type() === 'error') consoleErrors.push(m.text().slice(0, 200)); });
  page.on('pageerror', e => consoleErrors.push('PAGEERROR ' + String(e).slice(0, 200)));

  // 注入合成历史数据：让图表真实渲染
  await page.route('**/api/v1/unified-data/historical-batch*', r => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify(batchPayload(BMS_CATS)) }));
  await page.route(u => u.pathname.endsWith('/api/v1/unified-data/historical'), r => {
    const u = new URL(r.request().url());
    const cat = u.searchParams.get('category');
    const found = BMS_CATS.find(c => c[0] === cat);
    r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 200, message: 'ok', data: found ? mk(found[1]).map(p => ({ ...p, sensor_name: cat, unit: found[2] || '' })) : [] }) });
  });
  await page.route('**/api/v1/edge-devices/*/data*', r => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 200, message: 'ok', data: { items: Array.from({ length: 20 }, (_, i) => ({ collected_at: new Date(NOW - i * 60000).toISOString(), data: { total_voltage: 48, current: 2, cell_voltage_1: 3.3, temperature_1: 24.9 }, error_code: 0 })), total: 20, page: 1, page_size: 20 } }) }));
  await page.route('**/api/v1/unified-data/categories*', r => r.fulfill({ status: 200, contentType: 'application/json', body: JSON.stringify({ code: 200, message: 'ok', data: [{ code: 'total_voltage', unit: 'V' }, { code: 'current', unit: 'A' }, { code: 'temperature_1', unit: 'C' }] }) }));

  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
  await page.evaluate(t => { localStorage.setItem('theme', t); document.documentElement.setAttribute('data-theme', t); document.documentElement.classList.toggle('dark', t === 'dark'); }, theme);
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]', USER);
  await page.fill('input[placeholder="请输入密码"]', PASS);
  await page.click('button:has-text("登")');
  await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
  await page.waitForTimeout(2000);

  report.themes[theme] = {};

  // ---- A. /dashboard（趋势图） ----
  await page.goto(BASE + '/dashboard', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(3000);
  report.themes[theme].dashboard_desktop = await page.evaluate(MEASURE);
  // tooltip：鼠标移到图表中心
  { const el = await page.$('.line-chart'); if (el) { await el.scrollIntoViewIfNeeded(); await page.waitForTimeout(500);
    const bb = await el.boundingBox(); if (bb) {
      await page.mouse.move(bb.x + bb.width * 0.5, bb.y + bb.height * 0.5);
      await page.mouse.move(bb.x + bb.width * 0.52, bb.y + bb.height * 0.5, { steps: 4 });
      await page.waitForTimeout(900);
      report.themes[theme].dashboard_tooltip = await page.evaluate(TOOLTIP);
      await page.screenshot({ path: path.join(OUT, 'chart-tooltip-dashboard-' + theme + '.png') });
      await page.mouse.move(5, 5); await page.waitForTimeout(300); } } }

  // 移动端 390/360 KPI 度量
  for (const vp of [[390, 844], [360, 800]]) {
    await page.setViewportSize({ width: vp[0], height: vp[1] });
    await page.waitForTimeout(800);
    report.themes[theme]['dashboard_m' + vp[0]] = await page.evaluate(MEASURE);
  }
  await page.setViewportSize({ width: 1440, height: 900 });

  // ---- B. /monitor ----
  await page.goto(BASE + '/monitor', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(2500);
  report.themes[theme].monitor_desktop = await page.evaluate(MEASURE);
  for (const vp of [[390, 844], [360, 800]]) {
    await page.setViewportSize({ width: vp[0], height: vp[1] });
    await page.waitForTimeout(800);
    report.themes[theme]['monitor_m' + vp[0]] = await page.evaluate(MEASURE);
  }
  await page.setViewportSize({ width: 1440, height: 900 });

  // ---- C. /edge-device/7053（真实 BMS 详情页的两张图表） ----
  await page.goto(BASE + '/edge-device/7053', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(3500);
  report.themes[theme].detail = await page.evaluate(MEASURE);
  report.themes[theme].detail_chartCount = (await page.$$('.line-chart')).length;
  { const el = await page.$('.line-chart'); if (el) { await el.scrollIntoViewIfNeeded(); await page.waitForTimeout(500);
    const bb = await el.boundingBox(); if (bb) {
      await page.mouse.move(bb.x + bb.width * 0.5, bb.y + bb.height * 0.5);
      await page.mouse.move(bb.x + bb.width * 0.52, bb.y + bb.height * 0.5, { steps: 4 });
      await page.waitForTimeout(900);
      report.themes[theme].detail_tooltip = await page.evaluate(TOOLTIP);
      await page.screenshot({ path: path.join(OUT, 'chart-tooltip-detail-' + theme + '.png') });
      await page.mouse.move(5, 5); await page.waitForTimeout(300); } } }

  await page.evaluate(() => window.scrollTo(0, 900));
  await page.waitForTimeout(400);
  const full = await page.screenshot({ path: path.join(OUT, 'detail-' + theme + '-1440.png') });
  for (const vp of [[390, 844]]) {
    await page.setViewportSize({ width: vp[0], height: vp[1] });
    await page.waitForTimeout(1200);
    report.themes[theme]['detail_m' + vp[0]] = await page.evaluate(MEASURE);
    await page.screenshot({ path: path.join(OUT, 'detail-' + theme + '-390.png') });
  }
  await page.setViewportSize({ width: 1440, height: 900 });

  // ---- D. /data 选设备后（KPI + 图表 + 分页） ----
  await page.goto(BASE + '/data', { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(2000);
  report.themes[theme].data_before = await page.evaluate(MEASURE);
  try {
    await page.click('.el-form-item:has-text("设备") .el-select');
    await page.waitForTimeout(700);
    await page.click('.el-select-dropdown__item');
    await page.waitForTimeout(500);
    await page.keyboard.press('Escape');
    await page.click('button:has-text("查询")');
    await page.waitForTimeout(3500);
    report.themes[theme].data_after = await page.evaluate(MEASURE);
  { const el = await page.$('.line-chart'); if (el) { await el.scrollIntoViewIfNeeded(); await page.waitForTimeout(500);
    const bb = await el.boundingBox(); if (bb) {
      await page.mouse.move(bb.x + bb.width * 0.5, bb.y + bb.height * 0.5);
      await page.mouse.move(bb.x + bb.width * 0.52, bb.y + bb.height * 0.5, { steps: 4 });
      await page.waitForTimeout(900);
      report.themes[theme].data_tooltip = await page.evaluate(TOOLTIP);
      await page.screenshot({ path: path.join(OUT, 'chart-tooltip-data-' + theme + '.png') });
      await page.mouse.move(5, 5); await page.waitForTimeout(300); } } }

    await page.screenshot({ path: path.join(OUT, 'data-after-' + theme + '-1440.png'), fullPage: true });
    for (const vp of [[390, 844]]) {
      await page.setViewportSize({ width: vp[0], height: vp[1] });
      await page.waitForTimeout(1200);
      report.themes[theme]['data_after_m' + vp[0]] = await page.evaluate(MEASURE);
      await page.screenshot({ path: path.join(OUT, 'data-after-' + theme + '-390.png') });
    }
  } catch (e) { report.notes.push('data panel interaction failed [' + theme + ']: ' + String(e).slice(0, 150)); }

  report.themes[theme].consoleErrors = consoleErrors;
  await ctx.close();
}

await browser.close();
fs.writeFileSync(path.join(OUT, 'chart-facts.json'), JSON.stringify(report, null, 2));
console.log('written ' + path.join(OUT, 'chart-facts.json'));
console.log(JSON.stringify(report.notes));
