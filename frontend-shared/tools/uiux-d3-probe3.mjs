/**
 * 定向复跑 3：对话框 footer 可见性（多视口高度）、设备配置 360 工具栏裁切补充证据、
 * 禁用原因 tooltip 可用性、告警页 ErrorBoundary 崩溃原文。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
const BASE = 'http://127.0.0.1:8082';
const OUT = process.env.UIUX_OUT || '/tmp/uiux-d3c';
const sleep = ms => new Promise(r => setTimeout(r, ms));
fs.mkdirSync(OUT, { recursive: true });
const out = { results: [], consoleErrors: [] };

const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' });
const page = await ctx.newPage();
page.on('console', m => { if (m.type() === 'error') out.consoleErrors.push(m.text().slice(0, 400)); });
page.on('pageerror', e => out.consoleErrors.push('PAGEERROR ' + String(e).slice(0, 400)));
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', 'admin');
await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await sleep(1500);

const DIALOG = () => {
  const d = document.querySelector('.el-dialog');
  const f = document.querySelector('.el-dialog__footer');
  const b = document.querySelector('.el-dialog__body');
  const r = el => el ? (() => { const x = el.getBoundingClientRect(); return { w: Math.round(x.width), h: Math.round(x.height), top: Math.round(x.top), bottom: Math.round(x.bottom) }; })() : null;
  const ov = document.querySelector('.el-overlay');
  const ovScroll = ov ? { ch: ov.clientHeight, sh: ov.scrollHeight, oy: getComputedStyle(ov).overflowY, canScroll: ov.scrollHeight > ov.clientHeight } : null;
  return {
    title: (document.querySelector('.el-dialog__title') || {}).textContent || null,
    vp: { w: window.innerWidth, h: window.innerHeight },
    dialog: r(d), footer: r(f), body: r(b),
    dialogMaxW: d ? getComputedStyle(d).maxWidth : null,
    dialogMarginTop: d ? getComputedStyle(d).marginTop : null,
    overlay: ovScroll,
    footerInViewport: f ? f.getBoundingClientRect().bottom <= window.innerHeight + 1 : null,
    bodyScrollable: b ? b.scrollHeight > b.clientHeight : null,
    bodyOverflowY: b ? getComputedStyle(b).overflowY : null,
    bodyScrollH: b ? b.scrollHeight : null, bodyClientH: b ? b.clientHeight : null,
    formItemCount: document.querySelectorAll('.el-dialog .el-form-item').length,
  };
};

// A. automation 创建规则对话框：1440x900 / 1440x600 / 390x844 / 360x800
for (const vp of [{ n: 'desktop-1440x900', w: 1440, h: 900 }, { n: 'laptop-1440x600', w: 1440, h: 600 }, { n: 'mobile-390x844', w: 390, h: 844 }, { n: 'mobile-360x800', w: 360, h: 800 }]) {
  try {
    await page.setViewportSize({ width: vp.w, height: vp.h });
    await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
    await sleep(1800);
    await page.click('[data-test="create-rule"]');
    await sleep(800);
    const st = await page.evaluate(DIALOG);
    const shot = path.join(OUT, 'dlg-automation-create-' + vp.n + '.png');
    await page.screenshot({ path: shot });
    // 实际尝试点击保存按钮
    let saveClickable = false;
    try { await page.click('[data-test="save-rule"]', { timeout: 1500, trial: false }); saveClickable = true; } catch { saveClickable = false; }
    out.results.push({ case: 'automation-create-dialog', vp: vp.n, screenshot: shot, saveClickableByPlaywright: saveClickable, ...st });
    await page.keyboard.press('Escape'); await sleep(400);
  } catch (e) { out.results.push({ case: 'automation-create-dialog', vp: vp.n, error: String(e).slice(0, 200) }); }
}

// B. firmware 上传对话框 360
for (const vp of [{ n: 'desktop-1440x900', w: 1440, h: 900 }, { n: 'mobile-360x800', w: 360, h: 800 }]) {
  try {
    await page.setViewportSize({ width: vp.w, height: vp.h });
    await page.goto(BASE + '/firmware', { waitUntil: 'domcontentloaded' });
    await sleep(1800);
    await page.click('.page-header button');
    await sleep(800);
    const st = await page.evaluate(DIALOG);
    const shot = path.join(OUT, 'dlg-firmware-upload-' + vp.n + '.png');
    await page.screenshot({ path: shot });
    out.results.push({ case: 'firmware-upload-dialog', vp: vp.n, screenshot: shot, ...st });
    await page.keyboard.press('Escape'); await sleep(400);
  } catch (e) { out.results.push({ case: 'firmware-upload-dialog', vp: vp.n, error: String(e).slice(0, 200) }); }
}

// C. device-configs 360：工具栏按钮 title / 裁切链 / 是否为 el-card__body 的 overflow
try {
  await page.setViewportSize({ width: 360, height: 800 });
  await page.goto(BASE + '/device-configs', { waitUntil: 'domcontentloaded' });
  await sleep(2000);
  const st = await page.evaluate(() => {
    const btns = [...document.querySelectorAll('.filter-right button')].map(b => { const r = b.getBoundingClientRect(); return { text: (b.textContent || '').trim(), title: b.getAttribute('title'), ariaLabel: b.getAttribute('aria-label'), w: Math.round(r.width), h: Math.round(r.height), left: Math.round(r.left), right: Math.round(r.right) }; });
    const cardBody = document.querySelector('.toolbar-card .el-card__body');
    const fr = document.querySelector('.filter-right');
    const styleOf = el => { const s = getComputedStyle(el); return { ox: s.overflowX, oy: s.overflowY, display: s.display, flexWrap: s.flexWrap, justifyContent: s.justifyContent, width: s.width, minWidth: s.minWidth, padding: s.padding, boxSizing: s.boxSizing }; };
    return {
      buttons: btns,
      cardBodyStyle: cardBody ? styleOf(cardBody) : null,
      cardBodyBox: cardBody ? (() => { const r = cardBody.getBoundingClientRect(); return { w: Math.round(r.width), left: Math.round(r.left), right: Math.round(r.right), cw: cardBody.clientWidth, sw: cardBody.scrollWidth, paddingLeft: getComputedStyle(cardBody).paddingLeft }; })() : null,
      filterRightStyle: fr ? styleOf(fr) : null,
      filterRightBox: fr ? (() => { const r = fr.getBoundingClientRect(); return { w: Math.round(r.width), left: Math.round(r.left), right: Math.round(r.right) }; })() : null,
      filterBarWidth: (() => { const r = document.querySelector('.filter-bar').getBoundingClientRect(); return { w: Math.round(r.width), left: Math.round(r.left), right: Math.round(r.right) }; })(),
      docScrollW: document.documentElement.scrollWidth, docClientW: document.documentElement.clientWidth,
    };
  });
  out.results.push({ case: 'device-configs-360-toolbar', ...st });
} catch (e) { out.results.push({ case: 'device-configs-360-toolbar', error: String(e).slice(0, 200) }); }

// D. 禁用原因与 tooltip：device-configs 导出；data-sources 行内禁用（无数据，记录事实）
try {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(BASE + '/device-configs', { waitUntil: 'domcontentloaded' });
  await sleep(1800);
  const st = await page.evaluate(() => {
    const b = [...document.querySelectorAll('button')].find(x => (x.textContent || '').trim() === '导出');
    if (!b) return { found: false };
    return { found: true, disabled: b.disabled, cls: b.className, title: b.getAttribute('title'), ariaLabel: b.getAttribute('aria-label'), ariaDisabled: b.getAttribute('aria-disabled'), pe: getComputedStyle(b).pointerEvents, parentPE: getComputedStyle(b.parentElement).pointerEvents, parentCls: b.parentElement.className };
  });
  // 尝试 hover 看有没有 tooltip
  let tooltipText = null;
  try {
    await page.hover('.filter-right button:nth-child(2)');
    await sleep(1200);
    tooltipText = await page.evaluate(() => { const t = document.querySelector('.el-popper[role="tooltip"], .el-tooltip__popper'); return t ? (t.textContent || '').trim().slice(0, 80) : null; });
  } catch { /* ignore */ }
  const shot = path.join(OUT, 'disabled-export-hover.png');
  await page.screenshot({ path: shot });
  out.results.push({ case: 'disabled-reason', page: 'device-configs', tooltipTextAfterHover: tooltipText, screenshot: shot, ...st });
} catch (e) { out.results.push({ case: 'disabled-reason', error: String(e).slice(0, 200) }); }

// E. alerts 崩溃原文
try {
  await page.goto(BASE + '/alerts', { waitUntil: 'domcontentloaded' });
  await sleep(1500);
  await page.route('**/api/v1/alert-rules*', r => { try { r.abort('failed'); } catch { /* ignore */ } });
  await page.reload({ waitUntil: 'domcontentloaded' });
  await sleep(3000);
  const st = await page.evaluate(() => ({
    path: location.pathname,
    resultTitle: (document.querySelector('.el-result__title') || {}).textContent || null,
    resultSub: (document.querySelector('.el-result__subtitle') || {}).textContent || null,
    stack: (document.querySelector('.error-details pre') || {}).textContent ? document.querySelector('.error-details pre').textContent.slice(0, 500) : null,
    pageHeader: !!document.querySelector('.page-header'),
    sidebarPresent: !!document.querySelector('.el-menu'),
  }));
  const shot = path.join(OUT, 'alerts-boundary.png');
  await page.screenshot({ path: shot });
  out.results.push({ case: 'alerts-error-boundary', screenshot: shot, ...st });
  await page.unroute('**/api/v1/alert-rules*');
} catch (e) { out.results.push({ case: 'alerts-error-boundary', error: String(e).slice(0, 200) }); }

fs.writeFileSync(path.join(OUT, 'd3c-facts.json'), JSON.stringify(out, null, 2));
await browser.close();
console.log('records=' + out.results.length);
for (const r of out.results) console.log(JSON.stringify(r).slice(0, 420));
