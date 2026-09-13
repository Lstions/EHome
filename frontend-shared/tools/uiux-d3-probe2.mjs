/**
 * 定向复跑 2：修正上一版探针的两个环境错误后，补齐「接口失败/首次加载/编辑模型/表格横滚」证据。
 *
 * 上一版错误（已更正）：
 *   1) API-fail / first-load 用了 ctx.newPage()，新页面没有登录会话 → 被路由守卫弹回 /login，
 *      测到的是登录页而不是目标页（bodyText 里是「用户名/密码/登 录」）。
 *   2) 用 page.route 拦截了已经建立的页面并以 r.continue() 恢复，跨导航时 Route 已被处理 → 崩溃。
 * 本版：在同一已登录 page 上先 goto 目标页，再 route.abort() 后 reload；场景之间 unroute。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

const BASE = process.env.UIUX_BASE || 'http://127.0.0.1:8082';
const OUT = process.env.UIUX_OUT || '/tmp/uiux-d3b';
const CHROME = process.env.UIUX_CHROME || '/snap/bin/chromium';
const sleep = ms => new Promise(r => setTimeout(r, ms));
fs.mkdirSync(OUT, { recursive: true });
const facts = { results: [], consoleErrors: [] };

const browser = await chromium.launch({ executablePath: CHROME, args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', deviceScaleFactor: 1 });
const page = await ctx.newPage();
page.on('console', m => { if (m.type() === 'error') facts.consoleErrors.push(m.text().slice(0, 160)); });
page.on('pageerror', e => facts.consoleErrors.push('PAGEERROR ' + String(e).slice(0, 160)));

await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded', timeout: 30000 });
await page.evaluate(() => { localStorage.setItem('theme', 'light'); });
await page.waitForSelector('input[placeholder="请输入用户名"]', { timeout: 20000 });
await page.fill('input[placeholder="请输入用户名"]', 'admin');
await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await sleep(1500);

const STATE = () => ({
  path: location.pathname,
  heading: (() => { const h = document.querySelector('h1, h2, .page-header h2'); return h ? h.textContent.trim().slice(0, 40) : null; })(),
  tableRows: document.querySelectorAll('.el-table__row').length,
  emptyText: (document.querySelector('.empty-description') || document.querySelector('.el-empty__description') || {}).textContent || null,
  emptyTitle: (document.querySelector('.empty-title') || {}).textContent || null,
  errorAlert: !!document.querySelector('[data-test="ds-error"]'),
  errorAlertText: (document.querySelector('.el-alert__title') || {}).textContent || null,
  elMessages: [...document.querySelectorAll('.el-message')].map(m => (m.textContent || '').trim().slice(0, 60)),
  tableEmptyText: (document.querySelector('.el-table__empty-text') || {}).textContent || null,
  loadingMask: document.querySelectorAll('.el-loading-mask').length,
  skeleton: document.querySelectorAll('.el-skeleton').length,
  disabledActionBtns: [...document.querySelectorAll('button.is-disabled, button[disabled]')].map(b => (b.textContent || '').trim().slice(0, 16)),
  disabledBtnTitles: [...document.querySelectorAll('button.is-disabled, button[disabled], [title]')].map(b => ({ t: (b.textContent || '').trim().slice(0, 12), title: b.getAttribute('title') })).filter(x => x.title),
  statCards: [...document.querySelectorAll('.stat-card')].map(c => ({ label: (c.querySelector('.stat-label') || {}).textContent || null, value: (c.querySelector('.stat-value') || {}).textContent || null })),
});

// ── A. 接口失败（同会话 abort + reload，会话保留） ──
for (const [nm, route, pat] of [
  ['automation', '/automation', '**/api/v1/automation-rules*'],
  ['automation-events', '/automation', '**/api/v1/automation-events*'],
  ['alerts', '/alerts', '**/api/v1/alert-rules*'],
  ['data-sources', '/data-sources', '**/api/v1/data-sources*'],
  ['firmware', '/firmware', '**/api/v1/firmwares*'],
  ['device-configs', '/device-configs', '**/api/v1/device-configs*'],
]) {
  try {
    await page.goto(BASE + route, { waitUntil: 'domcontentloaded' });
    await sleep(1500);
    await page.route(pat, r => { try { r.abort('failed'); } catch { /* ignore */ } });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await sleep(2600);
    const st = await page.evaluate(STATE);
    const shot = path.join(OUT, 'apifail-' + nm + '.png');
    await page.screenshot({ path: shot });
    facts.results.push({ scenario: 'api-fail', target: nm, route, pattern: pat, screenshot: shot, ...st });
    await page.unroute(pat);
  } catch (e) { facts.results.push({ scenario: 'api-fail', target: nm, error: String(e).slice(0, 160) }); }
}

// ── B. 首次加载态（延迟 3s，同会话 reload） ──
for (const [nm, route, pat] of [
  ['automation', '/automation', '**/api/v1/automation-events*'],
  ['data-sources', '/data-sources', '**/api/v1/data-sources*'],
  ['firmware', '/firmware', '**/api/v1/firmwares*'],
]) {
  try {
    let released = false;
    const slow = async r => { await sleep(3000); if (released) return; try { await r.continue(); } catch { /* ignore */ } };
    await page.route(pat, slow);
    await page.goto(BASE + route, { waitUntil: 'domcontentloaded' });
    await sleep(1000);
    const mid = await page.evaluate(STATE);
    const shot = path.join(OUT, 'firstload-' + nm + '.png');
    await page.screenshot({ path: shot });
    released = true;
    await page.unroute(pat);
    facts.results.push({ scenario: 'first-load', target: nm, route, pattern: pat, screenshot: shot, ...mid });
  } catch (e) { facts.results.push({ scenario: 'first-load', target: nm, error: String(e).slice(0, 160) }); }
}

// ── C. 权限不足：清 token 后直达路由 ──
try {
  await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
  await sleep(1200);
  await page.evaluate(() => { localStorage.removeItem('token'); sessionStorage.removeItem('token'); });
  await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
  await sleep(2000);
  const st = await page.evaluate(STATE);
  const shot = path.join(OUT, 'noauth-automation.png');
  await page.screenshot({ path: shot });
  facts.results.push({ scenario: 'permission', target: 'automation', screenshot: shot, url: page.url(), ...st });
} catch (e) { facts.results.push({ scenario: 'permission', error: String(e).slice(0, 160) }); }

// ── D. 重新登录后：编辑模型 / 禁用原因 / 表格横滚 ──
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input[placeholder="请输入用户名"]', { timeout: 20000 });
await page.fill('input[placeholder="请输入用户名"]', 'admin');
await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await sleep(1500);

// D1 automation 编辑规则：是否存在创建专属字段
try {
  await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
  await sleep(2200);
  await page.click('.el-table__row .el-button--primary');
  await sleep(800);
  const d1 = await page.evaluate(() => {
    const labels = [...document.querySelectorAll('.el-dialog .el-form-item__label')].map(l => l.textContent.trim());
    const d = document.querySelector('.el-dialog');
    const r = d ? d.getBoundingClientRect() : null;
    return { title: (document.querySelector('.el-dialog__title') || {}).textContent || null, formLabels: labels,
      dialogBox: r ? { w: Math.round(r.width), h: Math.round(r.height), bottom: Math.round(r.bottom) } : null,
      disabledFields: [...document.querySelectorAll('.el-dialog .is-disabled')].map(x => (x.textContent || '').trim().slice(0, 20)) };
  });
  const shot = path.join(OUT, 'edit-automation.png');
  await page.screenshot({ path: shot });
  facts.results.push({ scenario: 'edit-model', target: 'automation', screenshot: shot, ...d1 });
  await page.keyboard.press('Escape'); await sleep(400);
} catch (e) { facts.results.push({ scenario: 'edit-model', target: 'automation', error: String(e).slice(0, 160) }); }

// D2 alerts 编辑（无数据，跳过并记录）
try {
  await page.goto(BASE + '/alerts', { waitUntil: 'domcontentloaded' });
  await sleep(2000);
  const d2 = await page.evaluate(() => ({ rows: document.querySelectorAll('.el-table__row').length, empty: (document.querySelector('.el-table__empty-text') || {}).textContent || null }));
  await page.click('[data-test="create-rule"]'); await sleep(700);
  const d2b = await page.evaluate(() => ({ title: (document.querySelector('.el-dialog__title') || {}).textContent || null, labels: [...document.querySelectorAll('.el-dialog .el-form-item__label')].map(l => l.textContent.trim()), disabled: [...document.querySelectorAll('.el-dialog .is-disabled')].map(x => (x.textContent || '').trim().slice(0, 24)) }));
  const shot = path.join(OUT, 'create-alerts.png');
  await page.screenshot({ path: shot });
  facts.results.push({ scenario: 'create-dialog', target: 'alerts', emptyTableText: d2.empty, rows: d2.rows, screenshot: shot, ...d2b });
  await page.keyboard.press('Escape'); await sleep(400);
} catch (e) { facts.results.push({ scenario: 'create-dialog', target: 'alerts', error: String(e).slice(0, 160) }); }

// D3 firmware 编辑（无数据 → 只有上传对话框可用）
try {
  await page.goto(BASE + '/firmware', { waitUntil: 'domcontentloaded' });
  await sleep(2000);
  await page.click('.page-header button'); await sleep(700);
  const d3 = await page.evaluate(() => {
    const d = document.querySelector('.el-dialog');
    const f = document.querySelector('.el-dialog__footer');
    const b = document.querySelector('.el-dialog__body');
    const rr = el => el ? (() => { const r = el.getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height), top: Math.round(r.top), bottom: Math.round(r.bottom) }; })() : null;
    return { title: (document.querySelector('.el-dialog__title') || {}).textContent || null, dialog: rr(d), footer: rr(f), body: rr(b),
      footerInViewport: f ? f.getBoundingClientRect().bottom <= window.innerHeight + 1 : null, maxW: d ? getComputedStyle(d).maxWidth : null,
      bodyScrollable: b ? b.scrollHeight > b.clientHeight : null, labels: [...document.querySelectorAll('.el-dialog .el-form-item__label')].map(l => l.textContent.trim()) };
  });
  const shot = path.join(OUT, 'upload-firmware.png');
  await page.screenshot({ path: shot });
  facts.results.push({ scenario: 'upload-dialog', target: 'firmware', screenshot: shot, ...d3 });
  await page.keyboard.press('Escape'); await sleep(400);
} catch (e) { facts.results.push({ scenario: 'upload-dialog', target: 'firmware', error: String(e).slice(0, 160) }); }

// D4 data-sources：禁用/危险确认（无数据 → 只核对新建对话框的禁用字段）
try {
  await page.goto(BASE + '/data-sources', { waitUntil: 'domcontentloaded' });
  await sleep(1800);
  await page.click('[data-test="create-source"]'); await sleep(700);
  const d4 = await page.evaluate(() => ({
    title: (document.querySelector('.el-dialog__title') || {}).textContent || null,
    labels: [...document.querySelectorAll('.el-dialog .el-form-item__label')].map(l => l.textContent.trim()),
    disabledInputs: [...document.querySelectorAll('.el-dialog .is-disabled input')].map(i => i.getAttribute('placeholder') || i.value),
    hints: [...document.querySelectorAll('.el-dialog .hint')].map(h => h.textContent.trim()),
  }));
  const shot = path.join(OUT, 'create-data-source.png');
  await page.screenshot({ path: shot });
  facts.results.push({ scenario: 'create-dialog', target: 'data-sources', screenshot: shot, ...d4 });
  await page.keyboard.press('Escape'); await sleep(400);
} catch (e) { facts.results.push({ scenario: 'create-dialog', target: 'data-sources', error: String(e).slice(0, 160) }); }

// D5 device-configs：编辑已有模板的标签（库为空 → 记录事实）
try {
  await page.goto(BASE + '/device-configs', { waitUntil: 'domcontentloaded' });
  await sleep(1800);
  const d5 = await page.evaluate(() => ({
    cardCount: document.querySelectorAll('.config-card').length,
    emptyText: (document.querySelector('.empty-description') || {}).textContent || null,
    statCards: [...document.querySelectorAll('.stat-card')].map(c => ({ label: (c.querySelector('.stat-label') || {}).textContent || null, value: (c.querySelector('.stat-value') || {}).textContent || null })),
    pageTitlePresent: !!document.querySelector('[class*="page-header"]'),
    h2: document.querySelector('h2') ? document.querySelector('h2').textContent.trim() : null,
  }));
  facts.results.push({ scenario: 'device-configs-facts', target: 'device-configs', ...d5 });
} catch (e) { facts.results.push({ scenario: 'device-configs-facts', error: String(e).slice(0, 160) }); }

// D6 表格横滚提示（.mobile-table-hint / .mobile-table-wrapper）在 360 的实际存在性
try {
  await page.setViewportSize({ width: 360, height: 800 });
  for (const [nm, route] of [['automation', '/automation'], ['data-sources', '/data-sources'], ['alerts', '/alerts'], ['firmware', '/firmware'], ['device-configs', '/device-configs']]) {
    await page.goto(BASE + route, { waitUntil: 'domcontentloaded' });
    await sleep(1800);
    const d6 = await page.evaluate(() => {
      const w = document.querySelector('.el-table .el-table__body-wrapper') || document.querySelector('.el-table__body-wrapper');
      const t = document.querySelector('.el-table');
      return {
        wrapper: !!document.querySelector('.mobile-table-wrapper'), hint: !!document.querySelector('.mobile-table-hint'),
        hintText: (document.querySelector('.mobile-table-hint') || {}).textContent || null,
        tableW: t ? Math.round(t.getBoundingClientRect().width) : null,
        innerClientW: w ? w.clientWidth : null, innerScrollW: w ? w.scrollWidth : null,
        overflowX: w ? getComputedStyle(w).overflowX : null,
        minWidthSum: t ? [...t.querySelectorAll('col')].reduce((a, c) => a + (parseInt(getComputedStyle(c).width) || 0), 0) : null,
      };
    });
    facts.results.push({ scenario: 'table-roll', target: nm, vp: 'mobile-360', ...d6 });
  }
} catch (e) { facts.results.push({ scenario: 'table-roll', error: String(e).slice(0, 160) }); }

fs.writeFileSync(path.join(OUT, 'd3b-facts.json'), JSON.stringify(facts, null, 2));
await browser.close();
console.log('records=' + facts.results.length + ' consoleErrors=' + facts.consoleErrors.length);
for (const r of facts.results) console.log('  ' + JSON.stringify(r).slice(0, 210));
