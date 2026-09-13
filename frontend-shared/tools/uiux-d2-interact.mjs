/**
 * D2 交互取证：删除二次确认 / 编辑表单字段 / 行操作 loading / 表格视图操作列可达性。
 * 只读页面、不提交任何保存请求（删除确认一律取消）。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

const BASE = 'http://127.0.0.1:8082';
const OUT = process.env.UIUX_OUT || '/tmp/uiux-d2-probe';
const THEME = process.env.UIUX_THEME || 'light';
const W = Number(process.env.UIUX_W || 390), H = Number(process.env.UIUX_H || 844);
const VP = process.env.UIUX_VPNAME || 'mobile-390';
fs.mkdirSync(OUT, { recursive: true });

const snap = () => {
  const q = s => document.querySelector(s);
  const qa = s => [...document.querySelectorAll(s)];
  const R = el => { if (!el) return null; const r = el.getBoundingClientRect(); return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height), right: Math.round(r.right) }; };
  const T = el => el ? (el.textContent || '').trim().replace(/\s+/g, ' ') : null;
  const dialog = q('.el-dialog');
  const btns = dialog ? [...dialog.querySelectorAll('.el-dialog__footer button')].map(b => ({ t: T(b), cls: b.className, bg: getComputedStyle(b).backgroundColor, color: getComputedStyle(b).color, ...R(b) })) : [];
  const labels = dialog ? [...dialog.querySelectorAll('.el-form-item__label, label')].map(l => T(l)).filter(Boolean) : [];
  const autofocus = document.activeElement ? (document.activeElement.tagName.toLowerCase() + '.' + (document.activeElement.className || '').toString().slice(0, 40)) : null;
  return {
    dialogOpen: !!dialog,
    dialogTitle: dialog ? T(dialog.querySelector('.el-dialog__title')) : null,
    dialogAria: dialog ? dialog.getAttribute('aria-label') : null,
    dialogRole: dialog ? dialog.getAttribute('role') : null,
    dialogAriaModal: dialog ? dialog.getAttribute('aria-modal') : null,
    dialogText: dialog ? T(dialog.querySelector('.el-dialog__body')) : null,
    dialogSize: R(dialog),
    dialogButtons: btns,
    dialogFieldLabels: labels,
    autofocus,
    ariaBusy: qa('[aria-busy="true"]').length,
    ariaBusyEls: qa('[aria-busy]').map(e => e.tagName.toLowerCase() + '.' + (e.className || '').toString().slice(0, 40) + '=' + e.getAttribute('aria-busy')).slice(0, 8),
    overlays: qa('.el-overlay').length,
    bodyOverflow: getComputedStyle(document.body).overflow,
  };
};

// 表格视图下的操作列可达性
const tableFacts = () => {
  const qa = s => [...document.querySelectorAll(s)];
  const R = el => { if (!el) return null; const r = el.getBoundingClientRect(); return { x: Math.round(r.x), w: Math.round(r.width), right: Math.round(r.right) }; };
  const T = el => el ? (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 24) : null;
  return qa('.el-table').map(t => {
    const bar = t.closest('.el-scrollbar') ? t.closest('.el-scrollbar').querySelector('.el-scrollbar__bar.is-horizontal') : null;
    const fixed = t.querySelector('.el-table-fixed-column--right, .el-table__fixed-right');
    const rows = qa('.el-table__row');
    return {
      scrollableX: t.classList.contains('el-table--scrollable-x'),
      tableW: R(t),
      hasFixedRight: !!fixed,
      fixedW: R(fixed),
      scrollbarVisible: bar ? getComputedStyle(bar).display !== 'none' : null,
      firstRowActionBtns: rows.length ? [...rows[0].querySelectorAll('td:last-child button, td.el-table-fixed-column--right button')].map(b => ({ t: T(b), ...R(b) })) : [],
      viewportRight: innerWidth,
    };
  });
};

const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const ctx = await browser.newContext({ viewport: { width: W, height: H }, locale: 'zh-CN', hasTouch: W <= 480, colorScheme: THEME === 'dark' ? 'dark' : 'light' });
const page = await ctx.newPage();
const errs = [];
page.on('console', m => { if (m.type() === 'error') errs.push(m.text().slice(0, 200)); });
page.on('pageerror', e => errs.push('PAGEERROR ' + String(e).slice(0, 200)));

await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.evaluate(t => { localStorage.setItem('theme', t); document.documentElement.setAttribute('data-theme', t); document.documentElement.classList.toggle('dark', t === 'dark'); }, THEME);
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', 'admin');
await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await page.waitForTimeout(2000);

const report = { vp: VP, theme: THEME, w: W, h: H, steps: [], consoleErrors: errs };

const shot = async (n) => { const f = path.join(OUT, VP + '-' + THEME + '-i-' + n + '.png'); await page.screenshot({ path: f }); return f; };

// 1) 节点列表 删除二次确认
await page.goto(BASE + '/node', { waitUntil: 'domcontentloaded' }); await page.waitForTimeout(2200);
const delBtn = page.locator('.collector-card .el-button--danger').first();
const delBox = await delBtn.boundingBox();
await delBtn.click().catch(e => errs.push('click del: ' + e.message));
await page.waitForTimeout(900);
report.steps.push({ step: 'node-list 删除确认弹窗', deleteBtnBox: delBox && { w: Math.round(delBox.width), h: Math.round(delBox.height) }, shot: await shot('node-delete-dialog'), ...(await page.evaluate(snap)) });
await page.keyboard.press('Escape'); await page.waitForTimeout(500);
report.steps.push({ step: 'node-list 取消后', ...(await page.evaluate(snap)) });

// 2) 节点列表 表格视图：操作列可达性
await page.locator('button[aria-label="表格视图"]').first().click().catch(() => {});
await page.waitForTimeout(1200);
report.steps.push({ step: 'node-list 表格视图 390', shot: await shot('node-tableview'), tables: await page.evaluate(tableFacts) });

// 3) 边缘设备列表：切表格视图 + 删除确认
await page.goto(BASE + '/edge-device', { waitUntil: 'domcontentloaded' }); await page.waitForTimeout(2200);
await page.locator('button[aria-label="表格视图"]').first().click().catch(() => {});
await page.waitForTimeout(1200);
report.steps.push({ step: 'edge-device 表格视图 390', shot: await shot('edge-tableview'), tables: await page.evaluate(tableFacts) });
await page.locator('.el-table__row .el-button--danger').first().click().catch(e => errs.push('edge del: ' + e.message));
await page.waitForTimeout(1500);
report.steps.push({ step: 'edge-device 删除确认弹窗', shot: await shot('edge-delete-dialog'), ...(await page.evaluate(snap)) });
await page.keyboard.press('Escape'); await page.waitForTimeout(600);

// 4) 边缘设备编辑表单字段（编辑 vs 创建）
await page.goto(BASE + '/edge-device', { waitUntil: 'domcontentloaded' }); await page.waitForTimeout(2200);
await page.locator('.device-card .el-button:has-text("编辑")').first().click().catch(e => errs.push('edit: ' + e.message));
await page.waitForTimeout(1500);
report.steps.push({ step: 'edge-device 编辑弹窗', shot: await shot('edge-edit-dialog'), ...(await page.evaluate(() => {
  const d = document.querySelector('.el-dialog');
  const T = e => e ? (e.textContent || '').trim().replace(/\s+/g, ' ') : null;
  return {
    dialogOpen: !!d, title: d ? T(d.querySelector('.el-dialog__title')) : null,
    bodyText: d ? T(d.querySelector('.el-dialog__body')).slice(0, 500) : null,
    labels: d ? [...d.querySelectorAll('.el-form-item__label')].map(T) : [],
    stepsVisible: d ? d.querySelectorAll('.el-steps').length : 0,
    inputs: d ? [...d.querySelectorAll('input')].map(i => i.getAttribute('placeholder') || i.value).slice(0, 12) : [],
    footerBtns: d ? [...d.querySelectorAll('.el-dialog__footer button')].map(T) : [],
  };
})) });
await page.keyboard.press('Escape'); await page.waitForTimeout(600);

// 5) 逻辑设备编辑表单字段
await page.goto(BASE + '/logical-device', { waitUntil: 'domcontentloaded' }); await page.waitForTimeout(2500);
const editLd = page.locator('.el-table__row .el-button:has-text("编辑")').first();
if (await editLd.count()) {
  await editLd.click().catch(e => errs.push('ld edit: ' + e.message));
  await page.waitForTimeout(1500);
  report.steps.push({ step: 'logical-device 编辑弹窗', shot: await shot('ld-edit-dialog'), ...(await page.evaluate(() => {
    const d = document.querySelector('.el-dialog');
    const T = e => e ? (e.textContent || '').trim().replace(/\s+/g, ' ') : null;
    return { dialogOpen: !!d, title: d ? T(d.querySelector('.el-dialog__title')) : null,
      labels: d ? [...d.querySelectorAll('.el-form-item__label')].map(T) : [],
      bodyText: d ? T(d.querySelector('.el-dialog__body')).slice(0, 420) : null };
  })) });
} else { report.steps.push({ step: 'logical-device 编辑弹窗', note: '未找到编辑按钮（表格空态）' }); }

// 6) device-configs 工具栏窄屏情形
await page.goto(BASE + '/device-configs', { waitUntil: 'domcontentloaded' }); await page.waitForTimeout(2000);
report.steps.push({ step: 'device-configs 窄屏', shot: await shot('device-configs'), toolbar: await page.evaluate(() => {
  const q = s => document.querySelector(s); const R = el => { if (!el) return null; const r = el.getBoundingClientRect(); return { x: Math.round(r.x), w: Math.round(r.width), right: Math.round(r.right) }; };
  const card = q('.toolbar-card'); const body = q('.toolbar-card .el-card__body'); const right = q('.filter-right'); const importBtn = [...document.querySelectorAll('.filter-right button')].map(b => ({ t: (b.textContent || '').trim(), ...R(b) }));
  return { card: R(card), cardBody: R(body), filterRight: R(right), buttons: importBtn, cardBodyOverflow: body ? getComputedStyle(body).overflow : null, viewportW: innerWidth, cardScrollW: card ? card.scrollWidth : null, cardClientW: card ? card.clientWidth : null };
}) });

await browser.close();
fs.writeFileSync(path.join(OUT, 'interact-' + VP + '-' + THEME + '.json'), JSON.stringify(report, null, 2));
console.log('steps=' + report.steps.length + ' consoleErrors=' + errs.length);
for (const s of report.steps) console.log(' -', s.step, s.dialogOpen === undefined ? '' : ('dialog=' + s.dialogOpen));
