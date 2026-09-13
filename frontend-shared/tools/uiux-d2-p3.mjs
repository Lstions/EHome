/**
 * D2 精确取证 3：删除确认全文/默认焦点、表格视图操作列矩阵、暗色主题。
 * 只读；删除确认一律取消，不提交写请求。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
const BASE = 'http://127.0.0.1:8082';
const OUT = '/tmp/uiux-d2-probe';
const THEME = process.env.UIUX_THEME || 'light';
const W = Number(process.env.UIUX_W || 390), H = Number(process.env.UIUX_H || 844);
const VP = process.env.UIUX_VPNAME || 'mobile-390';
fs.mkdirSync(OUT, { recursive: true });

const probeDialog = () => {
  const q = s => document.querySelector(s), qa = s => [...document.querySelectorAll(s)];
  const T = e => e ? (e.textContent || '').trim().replace(/\s+/g, ' ') : null;
  const R = e => { if (!e) return null; const r = e.getBoundingClientRect(); return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height), right: Math.round(r.right), bottom: Math.round(r.bottom) }; };
  const vis = qa('.el-overlay').filter(o => getComputedStyle(o).display !== 'none');
  const dlg = vis.length ? vis[vis.length - 1].querySelector('.el-message-box, .el-dialog') : null;
  const boxes = qa('.el-message-box').filter(o => getComputedStyle(o).display !== 'none');
  const act = document.activeElement;
  return {
    overlayVisible: vis.length,
    dlgKind: dlg ? (dlg.classList.contains('el-message-box') ? 'el-message-box' : 'el-dialog') : null,
    title: dlg ? T(dlg.querySelector('.el-message-box__title, .el-dialog__title')) : null,
    // 所有可见 overlay 的正文（拼接，确保抓到 MessageBox 文本）
    allText: vis.map(o => T(o.querySelector('.el-message-box__message, .el-dialog__body'))).filter(Boolean),
    msgBoxText: boxes.map(b => T(b.querySelector('.el-message-box__message'))),
    msgBoxTitle: boxes.map(b => T(b.querySelector('.el-message-box__title'))),
    buttons: vis.flatMap(o => [...o.querySelectorAll('button')]).map(b => ({ t: T(b), cls: b.className.slice(0, 70), bg: getComputedStyle(b).backgroundColor, color: getComputedStyle(b).color, outline: getComputedStyle(b).outlineColor, ...R(b) })),
    activeElement: act ? act.tagName.toLowerCase() + '.' + (act.className || '').toString().slice(0, 60) : null,
    activeText: T(act),
    dlgSize: R(dlg),
    viewport: { w: innerWidth, h: innerHeight },
  };
};

const tableMatrix = () => {
  const qa = s => [...document.querySelectorAll(s)];
  const R = e => { if (!e) return null; const r = e.getBoundingClientRect(); return { x: Math.round(r.x), w: Math.round(r.width), right: Math.round(r.right) }; };
  const T = e => e ? (e.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 20) : null;
  const t = document.querySelector('.el-table');
  if (!t) return { table: false };
  const rows = qa('.el-table__row');
  const btns = rows.length ? [...rows[0].querySelectorAll('button')].map(b => ({ t: T(b), ...R(b) })) : [];
  const scrollWrap = t.querySelector('.el-scrollbar__wrap');
  return {
    table: R(t),
    cls: t.className.slice(0, 120),
    scrollW: scrollWrap ? scrollWrap.scrollWidth : null,
    clientW: scrollWrap ? scrollWrap.clientWidth : null,
    scrollLeft: scrollWrap ? Math.round(scrollWrap.scrollLeft) : null,
    fixedRight: R(t.querySelector('.el-table-fixed-column--right, .el-table__fixed-right')),
    wrappedByMobileTableWrapper: !!t.closest('.mobile-table-wrapper'),
    hasHint: !!t.closest('.mobile-table-wrapper')?.querySelector('.mobile-table-hint'),
    rowCount: rows.length,
    row0Buttons: btns,
    row0ButtonsInViewport: btns.filter(b => b.right <= innerWidth + 1 && b.x >= -1).length,
    viewportW: innerWidth,
    docOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
  };
};

const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const ctx = await browser.newContext({ viewport: { width: W, height: H }, locale: 'zh-CN', hasTouch: W <= 480, colorScheme: THEME === 'dark' ? 'dark' : 'light' });
const page = await ctx.newPage();
const errs = []; page.on('pageerror', e => errs.push(String(e).slice(0, 160)));
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.evaluate(t => { localStorage.setItem('theme', t); document.documentElement.setAttribute('data-theme', t); document.documentElement.classList.toggle('dark', t === 'dark'); }, THEME);
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', 'admin'); await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await page.waitForTimeout(2000);
const rep = { vp: VP, theme: THEME, w: W, h: H, steps: [], consoleErrors: errs };
const shot = async n => { const f = path.join(OUT, VP + '-' + THEME + '-p3-' + n + '.png'); await page.screenshot({ path: f }); return path.basename(f); };

// A. node-list 删除确认
await page.goto(BASE + '/node', { waitUntil: 'domcontentloaded' }); await page.waitForTimeout(2300);
await page.locator('.collector-card .el-button--danger').first().click().catch(e => errs.push('nd:' + e.message));
await page.waitForTimeout(1200);
rep.steps.push({ step: 'node-list 删除确认', shot: await shot('node-delete'), ...(await page.evaluate(probeDialog)) });
await page.keyboard.press('Escape'); await page.waitForTimeout(800);
// A2. 表格视图
await page.locator('button[aria-label="表格视图"]').first().click().catch(() => {}); await page.waitForTimeout(1300);
rep.steps.push({ step: 'node-list 表格视图', shot: await shot('node-table'), ...(await page.evaluate(tableMatrix)) });
await page.locator('.el-table__row .el-button--danger').first().click().catch(e => errs.push('nd2:' + e.message)); await page.waitForTimeout(1200);
rep.steps.push({ step: 'node-list 表格视图 删除确认', shot: await shot('node-table-delete'), ...(await page.evaluate(probeDialog)) });
await page.keyboard.press('Escape'); await page.waitForTimeout(800);

// B. edge-device 表格视图
await page.goto(BASE + '/edge-device', { waitUntil: 'domcontentloaded' }); await page.waitForTimeout(2400);
await page.locator('button[aria-label="表格视图"]').first().click().catch(() => {}); await page.waitForTimeout(1300);
rep.steps.push({ step: 'edge-device 表格视图', shot: await shot('edge-table'), ...(await page.evaluate(tableMatrix)) });

// C. channel-list 表格视图
await page.goto(BASE + '/channel', { waitUntil: 'domcontentloaded' }); await page.waitForTimeout(2300);
rep.steps.push({ step: 'channel-list 表格', shot: await shot('channel-table'), ...(await page.evaluate(tableMatrix)) });

// D. logical-device 表格视图
await page.goto(BASE + '/logical-device', { waitUntil: 'domcontentloaded' }); await page.waitForTimeout(2600);
rep.steps.push({ step: 'logical-device 表格', shot: await shot('ld-table'), ...(await page.evaluate(tableMatrix)) });

await browser.close();
fs.writeFileSync(path.join(OUT, 'p3-' + VP + '-' + THEME + '.json'), JSON.stringify(rep, null, 2));
console.log('steps=' + rep.steps.length + ' errs=' + errs.length);
