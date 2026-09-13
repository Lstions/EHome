/**
 * 定向复跑 19：收口剩余证据。
 *  A. device-configs 造一条模板 → 首次加载态（延迟 3s）/ 卡片渲染 / 编辑对话框是否展示创建专属字段（传感器驱动级联）/ 删除确认按钮类别 → 删除还原。
 *  B. data-sources 造一条来源 → 禁用按钮（重置）能否给出原因（原生 title / popper tooltip）→ 删除还原。
 *  C. automation 精确点击「命令 ID」链接：URL/控制台/UI 反馈。
 *  D. automation 表格 Switch 的可访问名称（outerHTML 原文）+ 页面滚动高度。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE = 'http://127.0.0.1:8082';
const sleep = ms => new Promise(r => setTimeout(r, ms));
const out = [];
const logs = [];
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' });
const page = await ctx.newPage();
page.on('console', m => logs.push(m.type() + ': ' + m.text().slice(0, 120)));
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', 'admin');
await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await sleep(1500);
const tok = await page.evaluate(() => localStorage.getItem('token') || sessionStorage.getItem('token'));

// ── A. device-configs ──
const mkCfg = await page.evaluate(async (t) => {
  const body = { name: '审计临时模板', description: 'audit probe', device_type: 'audit_probe_type', hardware_type: 'uart', protocol: 'modbus', config: { baudrate: 9600 }, is_default: false };
  const r = await fetch('/api/v1/device-configs', { method: 'POST', headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + t }, body: JSON.stringify(body) });
  return { status: r.status, body: (await r.text()).slice(0, 200) };
}, tok);
out.push({ step: 'device-configs-create', mkCfg });

// A1 首次加载态：延迟接口 3s
try {
  let released = false;
  const slow = async r => { await sleep(3000); if (released) return; try { await r.continue(); } catch { /* */ } };
  await page.route('**/api/v1/device-configs*', slow);
  await page.goto(BASE + '/device-configs', { waitUntil: 'domcontentloaded' });
  await sleep(1200);
  const fl = await page.evaluate(() => ({ skeleton: document.querySelectorAll('.el-skeleton, [class*="skeleton"]').length, mask: document.querySelectorAll('.el-loading-mask').length, cards: document.querySelectorAll('.config-card').length, emptyState: document.querySelectorAll('.empty-state').length, text: (document.querySelector('.config-grid') || {}).textContent || null }));
  await page.screenshot({ path: '/tmp/uiux-d3f/dc-firstload.png' });
  released = true; await page.unroute('**/api/v1/device-configs*');
  out.push({ step: 'device-configs-first-load', ...fl });
} catch (e) { out.push({ step: 'device-configs-first-load', error: String(e).slice(0, 150) }); }

// A2 正常加载 + 编辑对话框
try {
  await page.goto(BASE + '/device-configs', { waitUntil: 'domcontentloaded' });
  await sleep(2500);
  const cards = await page.evaluate(() => ({ n: document.querySelectorAll('.config-card').length, stats: [...document.querySelectorAll('.stat-card')].map(c => ({ l: (c.querySelector('.stat-label') || {}).textContent, v: (c.querySelector('.stat-value') || {}).textContent })) }));
  await page.click('.config-card .card-footer button:nth-child(3)');
  await sleep(1200);
  const edit = await page.evaluate(() => {
    const d = document.querySelector('.el-dialog');
    if (!d) return { dialogFound: false };
    const rr = el => el ? (() => { const x = el.getBoundingClientRect(); return { w: Math.round(x.width), h: Math.round(x.height), top: Math.round(x.top), bottom: Math.round(x.bottom) }; })() : null;
    const ovd = d.closest('.el-overlay-dialog');
    const cascader = d.querySelector('.el-cascader');
    return { dialogFound: true, title: (d.querySelector('.el-dialog__title') || {}).textContent || null,
      labels: [...d.querySelectorAll('.el-form-item__label')].map(l => l.textContent.trim()),
      cascaderPresent: !!cascader, cascaderValue: cascader ? (cascader.querySelector('input') || {}).value : null,
      cascaderPlaceholder: cascader ? (cascader.querySelector('input') || {}).placeholder : null,
      apiNote: '传感器驱动 = OEM→种类→型号 三层级联（创建专属概念）',
      dialog: rr(d), overlayDialogScrollMax: ovd ? ovd.scrollHeight - ovd.clientHeight : null,
      footer: rr(d.querySelector('.el-dialog__footer')), footerInViewport: d.querySelector('.el-dialog__footer') ? d.querySelector('.el-dialog__footer').getBoundingClientRect().bottom <= window.innerHeight + 1 : null,
      bodyOverflowY: getComputedStyle(d.querySelector('.el-dialog__body')).overflowY, bodyMaxH: getComputedStyle(d.querySelector('.el-dialog__body')).maxHeight,
      deviceTypeInput: (d.querySelectorAll('input')[0] || {}).value };
  });
  await page.screenshot({ path: '/tmp/uiux-d3f/dc-edit-dialog.png' });
  out.push({ step: 'device-configs-edit-dialog', cards, ...edit });
  await page.keyboard.press('Escape'); await sleep(600);
} catch (e) { out.push({ step: 'device-configs-edit-dialog', error: String(e).slice(0, 150) }); }

// A3 删除确认按钮类别
try {
  await page.goto(BASE + '/device-configs', { waitUntil: 'domcontentloaded' }); await sleep(2200);
  await page.click('.config-card .card-footer .el-dropdown button'); await sleep(800);
  const items = await page.$$('.el-dropdown-menu__item');
  await items[items.length - 1].click(); await sleep(1000);
  const mb = await page.evaluate(() => ({ text: (document.querySelector('.el-message-box__message') || {}).textContent || null, btns: [...document.querySelectorAll('.el-message-box__btns button')].map(b => ({ t: (b.textContent || '').trim(), cls: b.className, focused: document.activeElement === b })) }));
  await page.screenshot({ path: '/tmp/uiux-d3f/dc-delete-confirm.png' });
  out.push({ step: 'device-configs-delete-confirm', mb });
  await page.keyboard.press('Escape'); await sleep(500);
} catch (e) { out.push({ step: 'device-configs-delete-confirm', error: String(e).slice(0, 150) }); }

// ── B. data-sources 禁用按钮原因 ──
const mkDs = await page.evaluate(async (t) => (await fetch('/api/v1/data-sources', { method: 'POST', headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + t }, body: JSON.stringify({ device_id: 1, category: 'audit_probe_p19', edge_device_id: 7053, name: 'P19来源', max_fail_count: 3 }) })).status, tok);
out.push({ step: 'data-sources-create', status: mkDs });
try {
  await page.goto(BASE + '/data-sources', { waitUntil: 'domcontentloaded' }); await sleep(2500);
  const before = await page.evaluate(() => {
    const btns = [...document.querySelectorAll('.el-table__row .el-button')].map(b => ({ t: (b.textContent || '').trim(), disabled: b.disabled, cls: b.className, title: b.getAttribute('title'), ariaDisabled: b.getAttribute('aria-disabled'), pe: getComputedStyle(b).pointerEvents }));
    return { btns };
  });
  // hover 禁用按钮（重置）
  const hm = await page.evaluate(() => {
    const b = [...document.querySelectorAll('.el-table__row .el-button')].find(x => (x.textContent || '').trim() === '重置');
    if (!b) return { found: false };
    const r = b.getBoundingClientRect();
    return { found: true, box: { left: Math.round(r.left), top: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height) }, pe: getComputedStyle(b).pointerEvents };
  });
  let tip = null;
  if (hm.found) {
    await page.mouse.move(hm.box.left + hm.box.w / 2, hm.box.top + hm.box.h / 2);
    await sleep(1500);
    tip = await page.evaluate(() => ({ poppers: [...document.querySelectorAll('.el-popper')].map(p => ({ cls: p.className.slice(0, 50), text: (p.textContent || '').trim().slice(0, 60), display: getComputedStyle(p).display, vis: getComputedStyle(p).visibility })), nativeTitle: (() => { const b = [...document.querySelectorAll('.el-table__row .el-button')].find(x => (x.textContent || '').trim() === '重置'); return b ? b.getAttribute('title') : null; })() }));
    await page.screenshot({ path: '/tmp/uiux-d3f/ds-disabled-hover.png' });
  }
  out.push({ step: 'data-sources-disabled-reason', before, hoverTarget: hm, afterHover: tip });
} catch (e) { out.push({ step: 'data-sources-disabled-reason', error: String(e).slice(0, 150) }); }

// ── C. automation 命令 ID 链接 ──
try {
  await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' }); await sleep(2500);
  logs.length = 0;
  const sel = '.el-table__row td:nth-child(6) .el-button';
  const info = await page.evaluate((s) => { const b = document.querySelector(s); if (!b) return null; const r = b.getBoundingClientRect(); return { text: (b.textContent || '').trim(), box: { w: Math.round(r.width), h: Math.round(r.height), left: Math.round(r.left), top: Math.round(r.top) }, tabIndex: b.tabIndex, ariaLabel: b.getAttribute('aria-label') }; }, sel);
  await page.click(sel); await sleep(1200);
  const after = await page.evaluate(() => ({ url: location.href, dialogs: document.querySelectorAll('.el-dialog').length, messages: [...document.querySelectorAll('.el-message')].map(m => (m.textContent || '').trim()), notifications: document.querySelectorAll('.el-notification').length, drawers: document.querySelectorAll('.el-drawer').length }));
  out.push({ step: 'automation-command-id-click', link: info, after, consoleAfterClick: logs.slice(-5) });
} catch (e) { out.push({ step: 'automation-command-id-click', error: String(e).slice(0, 150) }); }

// ── D. Switch 可访问名称 + 滚动高度 ──
try {
  const sw = await page.evaluate(() => {
    const s = document.querySelector('td .el-switch');
    const inp = s ? s.querySelector('input') : null;
    const main = document.querySelector('.el-main');
    return { switchOuterHTML: s ? s.outerHTML.slice(0, 300) : null,
      switchTabIndex: s ? s.tabIndex : null, switchRole: s ? s.getAttribute('role') : null, switchAria: s ? s.getAttribute('aria-label') : null,
      inputAttrs: inp ? { type: inp.type, role: inp.getAttribute('role'), ariaLabel: inp.getAttribute('aria-label'), ariaChecked: inp.getAttribute('aria-checked'), tabIndex: inp.tabIndex, name: inp.getAttribute('name'), id: inp.id } : null,
      labelElement: s && s.id ? (document.querySelector('label[for="' + s.id + '"]') ? 'yes' : 'no') : 'no-id',
      mainScrollH: main ? main.scrollHeight : null, mainClientH: main ? main.clientHeight : null, mainRatio: main ? Math.round(main.scrollHeight / main.clientHeight * 100) / 100 : null,
      tableHeights: [...document.querySelectorAll('.el-table')].map(t => Math.round(t.getBoundingClientRect().height)),
      bodyEls: document.querySelectorAll('body *').length };
  });
  out.push({ step: 'automation-switch-a11y-and-scroll', ...sw });
} catch (e) { out.push({ step: 'automation-switch-a11y-and-scroll', error: String(e).slice(0, 150) }); }

// ── 清理 ──
const cl = await page.evaluate(async (t) => {
  const res = {};
  const cfg = await (await fetch('/api/v1/device-configs?page=1&page_size=100', { headers: { Authorization: 'Bearer ' + t } })).json();
  res.cfgIds = (cfg?.data?.list || []).filter(c => c.name === '审计临时模板').map(c => c.id);
  for (const id of res.cfgIds) { await fetch('/api/v1/device-configs/' + id, { method: 'DELETE', headers: { Authorization: 'Bearer ' + t } }); }
  const ds = await (await fetch('/api/v1/data-sources?category=audit_probe_p19', { headers: { Authorization: 'Bearer ' + t } })).json();
  res.dsIds = (ds?.data?.items || []).map(i => i.id);
  for (const id of res.dsIds) { await fetch('/api/v1/data-sources/' + id, { method: 'DELETE', headers: { Authorization: 'Bearer ' + t } }); }
  return res;
}, tok);
out.push({ step: 'cleanup', cl });
await browser.close();
fs.writeFileSync('/tmp/uiux-d3f/d3q-facts.json', JSON.stringify(out, null, 2));
for (const r of out) { console.log('===== ' + r.step + ' ====='); console.log(JSON.stringify(r, null, 1).slice(0, 2000)); }