/**
 * 定向复跑 8：data-sources 固定操作列在 360px 的真实可达性（上一版 token 取错，
 * 本次直接从 localStorage 的 token 建一条临时来源，测完删除）。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE = 'http://127.0.0.1:8082';
const sleep = ms => new Promise(r => setTimeout(r, ms));
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const ctx = await browser.newContext({ viewport: { width: 360, height: 800 }, locale: 'zh-CN' });
const page = await ctx.newPage();
const out = [];
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', 'admin');
await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await sleep(1500);

const tok = await page.evaluate(() => localStorage.getItem('token') || sessionStorage.getItem('token'));
const created = await page.evaluate(async (t) => {
  const r = await fetch('/api/v1/data-sources', { method: 'POST', headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + t }, body: JSON.stringify({ device_id: 1, category: 'audit_probe_tmp', edge_device_id: 7053, name: '审计临时来源', max_fail_count: 3 }) });
  return { status: r.status, body: (await r.text()).slice(0, 220) };
}, tok);
out.push({ step: 'create', created });

for (const w of [360, 390]) {
  await page.setViewportSize({ width: w, height: 800 });
  await page.goto(BASE + '/data-sources', { waitUntil: 'domcontentloaded' });
  await sleep(2200);
  const geo = await page.evaluate(() => {
    const wrap = document.querySelector('.el-table .el-scrollbar__wrap');
    const fixed = document.querySelector('.el-table-fixed-column--right');
    const r = el => el ? (() => { const x = el.getBoundingClientRect(); return { left: Math.round(x.left), right: Math.round(x.right), w: Math.round(x.width) }; })() : null;
    const btns = [...document.querySelectorAll('.el-table__fixed-right .el-button, .el-table-fixed-column--right .el-button, td.el-table-fixed-column--right .el-button')].map(b => ({ text: (b.textContent || '').trim(), ...r(b) }));
    // 也找所有行内按钮
    const allBtns = [...document.querySelectorAll('.el-table__row .el-button')].map(b => ({ text: (b.textContent || '').trim(), ...r(b) })).slice(0, 12);
    return { viewportW: window.innerWidth, rows: document.querySelectorAll('.el-table__row').length,
      fixedColFound: !!fixed, fixedCol: r(fixed), fixedBtns: btns, rowBtnsSample: allBtns,
      wrapCW: wrap ? wrap.clientWidth : null, wrapSW: wrap ? wrap.scrollWidth : null, wrapScrollLeft: wrap ? wrap.scrollLeft : null,
      mobileHint: !!document.querySelector('.mobile-table-hint') };
  });
  const shot = '/tmp/uiux-d3f/ds-fixed-' + w + '-left.png';
  await page.screenshot({ path: shot });
  // 滚到最右
  const geoMax = await page.evaluate(() => {
    const wrap = document.querySelector('.el-table .el-scrollbar__wrap');
    if (wrap) wrap.scrollLeft = 100000;
    const r = el => el ? (() => { const x = el.getBoundingClientRect(); return { left: Math.round(x.left), right: Math.round(x.right), w: Math.round(x.width) }; })() : null;
    const btns = [...document.querySelectorAll('.el-table__row .el-button')].map(b => ({ text: (b.textContent || '').trim(), ...r(b) })).slice(0, 12);
    return { scrollLeft: wrap ? wrap.scrollLeft : null, rowBtns: btns };
  });
  await sleep(400);
  const shot2 = '/tmp/uiux-d3f/ds-fixed-' + w + '-right.png';
  await page.screenshot({ path: shot2 });
  out.push({ step: 'geo', w, screenshotLeft: shot, screenshotRight: shot2, geo, geoMax });
}

// 点击「删除」验证危险确认
try {
  const delBtn = await page.$('.el-table__row .el-button--danger');
  if (delBtn) {
    await delBtn.click(); await sleep(900);
    const mb = await page.evaluate(() => ({ text: (document.querySelector('.el-message-box__message') || {}).textContent || null, btns: [...document.querySelectorAll('.el-message-box__btns button')].map(b => ({ t: (b.textContent || '').trim(), cls: b.className, focused: document.activeElement === b })) }));
    await page.screenshot({ path: '/tmp/uiux-d3f/ds-delete-confirm-360.png' });
    out.push({ step: 'delete-confirm', mb, screenshot: '/tmp/uiux-d3f/ds-delete-confirm-360.png' });
    // 确认删除以清理
    await page.click('.el-message-box__btns button.el-button--primary'); await sleep(1200);
    out.push({ step: 'cleanup-delete', done: true });
  } else { out.push({ step: 'delete-confirm', error: 'no danger button found' }); }
} catch (e) { out.push({ step: 'delete-confirm', error: String(e).slice(0, 200) }); }

// 兜底清理
const cleanup = await page.evaluate(async (t) => {
  const r = await fetch('/api/v1/data-sources?category=audit_probe_tmp', { headers: { Authorization: 'Bearer ' + t } });
  const j = await r.json();
  const items = (j && j.data && j.data.items) || [];
  const st = [];
  for (const it of items) { const d = await fetch('/api/v1/data-sources/' + it.id, { method: 'DELETE', headers: { Authorization: 'Bearer ' + t } }); st.push(d.status); }
  return { found: items.map(i => i.id), statuses: st };
}, tok);
out.push({ step: 'cleanup', cleanup });
await browser.close();
fs.writeFileSync('/tmp/uiux-d3f/d3h-facts.json', JSON.stringify(out, null, 2));
for (const r of out) { console.log(JSON.stringify(r).slice(0, 900)); }
