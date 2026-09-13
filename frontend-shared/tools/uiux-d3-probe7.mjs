/**
 * 定向复跑 7：data-sources 在 360px 下固定操作列的可达性精测
 * （§4.3.2「重要操作列保持可达」），并测 automation 的「命令 ID」死链。
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

// A. data-sources 固定操作列（用真实数据：先建一条）
try {
  const tok = await page.evaluate(() => localStorage.getItem('token'));
  const created = await page.evaluate(async (t) => {
    const r = await fetch('/api/v1/data-sources', { method: 'POST', headers: { 'Content-Type': 'application/json', Authorization: 'Bearer ' + t }, body: JSON.stringify({ device_id: 1, category: 'audit_probe_tmp', edge_device_id: 7053, name: '审计临时来源', max_fail_count: 3 }) });
    return { status: r.status, body: (await r.text()).slice(0, 200) };
  }, tok);
  await page.reload({ waitUntil: 'domcontentloaded' });
  await sleep(2000);
  const geo = await page.evaluate(() => {
    const wrap = document.querySelector('.el-table .el-scrollbar__wrap');
    const fixed = document.querySelector('.el-table-fixed-column--right');
    const main = document.querySelector('.el-main');
    const r = el => el ? (() => { const x = el.getBoundingClientRect(); return { left: Math.round(x.left), right: Math.round(x.right), w: Math.round(x.width) }; })() : null;
    const btns = [...document.querySelectorAll('.el-table-fixed-column--right .el-button')].map(b => ({ text: (b.textContent || '').trim(), ...r(b) }));
    return {
      viewportW: window.innerWidth,
      main: main ? { ...r(main), cw: main.clientWidth, sw: main.scrollWidth, ox: getComputedStyle(main).overflowX } : null,
      fixedCol: fixed ? { ...r(fixed), cls: fixed.className } : null,
      actionButtons: btns,
      tableRowCount: document.querySelectorAll('.el-table__row').length,
      mobileHint: !!document.querySelector('.mobile-table-hint'),
    };
  });
  // 滚到最右再测一次
  const geoMax = await page.evaluate(() => {
    const wrap = document.querySelector('.el-table .el-scrollbar__wrap');
    if (wrap) wrap.scrollLeft = 100000;
    const fixed = document.querySelector('.el-table-fixed-column--right');
    const main = document.querySelector('.el-main');
    const r = el => el ? (() => { const x = el.getBoundingClientRect(); return { left: Math.round(x.left), right: Math.round(x.right), w: Math.round(x.width) }; })() : null;
    const btns = [...document.querySelectorAll('.el-table-fixed-column--right .el-button')].map(b => ({ text: (b.textContent || '').trim(), ...r(b) }));
    return { scrollLeft: wrap ? wrap.scrollLeft : null, main: r(main), fixedCol: r(fixed), actionButtons: btns };
  });
  await page.screenshot({ path: '/tmp/uiux-d3f/data-sources-360-fixedcol.png' });
  out.push({ case: 'data-sources-fixed-opcol-360', created, geo, geoMax });

  // 清理临时来源
  const del = await page.evaluate(async (t) => {
    const list = await (await fetch('/api/v1/data-sources?category=audit_probe_tmp', { headers: { Authorization: 'Bearer ' + t } })).json();
    const items = list.data.items || [];
    const res = [];
    for (const it of items) { const r = await fetch('/api/v1/data-sources/' + it.id, { method: 'DELETE', headers: { Authorization: 'Bearer ' + t } }); res.push(r.status); }
    return { deleted: items.map(i => i.id), statuses: res };
  }, tok);
  out.push({ case: 'data-sources-cleanup', del });
} catch (e) { out.push({ case: 'data-sources-fixed-opcol-360', error: String(e).slice(0, 200) }); }

// B. automation 命令 ID 链接点击行为
try {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
  await sleep(2200);
  const before = await page.evaluate(() => ({ url: location.href, rows: document.querySelectorAll('.el-table__row').length }));
  const link = await page.$('.el-table__row .el-button--primary.el-button--small, .el-table__row td:nth-child(6) .el-button');
  let clicked = false, msgs = [], urlAfter = null;
  if (link) {
    const consoleMsgs = [];
    page.on('console', m => consoleMsgs.push(m.type() + ':' + m.text().slice(0, 80)));
    await link.click();
    await sleep(1200);
    clicked = true;
    urlAfter = page.url();
    msgs = await page.evaluate(() => [...document.querySelectorAll('.el-message, .el-notification, .el-dialog')].map(m => (m.textContent || '').trim().slice(0, 60)));
    out.push({ case: 'automation-command-id-link', before, clicked, urlAfter, urlChanged: urlAfter !== before.url, uiFeedback: msgs, consoleAfterClick: consoleMsgs.slice(-4) });
  } else { out.push({ case: 'automation-command-id-link', clicked: false, reason: 'link not found' }); }
} catch (e) { out.push({ case: 'automation-command-id-link', error: String(e).slice(0, 200) }); }

await browser.close();
fs.writeFileSync('/tmp/uiux-d3f/d3g-facts.json', JSON.stringify(out, null, 2));
for (const r of out) { console.log('===== ' + r.case + ' ====='); console.log(JSON.stringify(r, null, 1)); }
