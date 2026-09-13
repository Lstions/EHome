/** 定向复跑 11：el-drawer（数据源详情）在窄屏的宽度——只认「可见」的 drawer。 */
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
await page.evaluate(async (t) => { await fetch('/api/v1/data-sources', { method: 'POST', headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + t }, body: JSON.stringify({ device_id: 1, category: 'audit_probe_dw2', edge_device_id: 7053, name: '抽屉来源2', max_fail_count: 3 }) }); }, tok);
for (const w of [360, 390, 768]) {
  await page.setViewportSize({ width: w, height: 800 });
  await page.goto(BASE + '/data-sources', { waitUntil: 'domcontentloaded' });
  await sleep(2200);
  await page.click('[data-test="ds-detail"]', { force: true });
  await sleep(2000);
  const st = await page.evaluate(() => {
    const vis = [...document.querySelectorAll('.el-drawer')].filter(d => { const r = d.getBoundingClientRect(); return r.width > 0 && getComputedStyle(d).visibility !== 'hidden'; });
    const d = vis[0] || null;
    const rr = el => el ? (() => { const x = el.getBoundingClientRect(); return { left: Math.round(x.left), right: Math.round(x.right), w: Math.round(x.width), top: Math.round(x.top), bottom: Math.round(x.bottom) }; })() : null;
    const b = d ? d.querySelector('.el-drawer__body') : null;
    return { visibleDrawers: vis.length, box: rr(d), cssWidth: d ? getComputedStyle(d).width : null, maxWidth: d ? getComputedStyle(d).maxWidth : null,
      open: d ? d.className : null, bodyCW: b ? b.clientWidth : null, bodySW: b ? b.scrollWidth : null,
      viewportW: window.innerWidth, docSW: document.documentElement.scrollWidth, docCW: document.documentElement.clientWidth,
      leftOffscreen: d ? d.getBoundingClientRect().left < -1 : null };
  });
  await page.screenshot({ path: '/tmp/uiux-d3f/drawer2-' + w + '.png' });
  out.push({ w, screenshot: '/tmp/uiux-d3f/drawer2-' + w + '.png', ...st });
  await page.keyboard.press('Escape'); await sleep(700);
}
const cl = await page.evaluate(async (t) => { const j = await (await fetch('/api/v1/data-sources?category=audit_probe_dw2', { headers: { Authorization: 'Bearer ' + t } })).json(); const s = []; for (const it of (j?.data?.items || [])) s.push((await fetch('/api/v1/data-sources/' + it.id, { method: 'DELETE', headers: { Authorization: 'Bearer ' + t } })).status); return s; }, tok);
out.push({ cleanup: cl });
await browser.close();
fs.writeFileSync('/tmp/uiux-d3f/d3k-facts.json', JSON.stringify(out, null, 2));
for (const r of out) console.log(JSON.stringify(r));
