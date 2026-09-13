/** 定向复跑 12：用 JS 直接触发 click 打开详情抽屉，测 .el-drawer 实际宽度。 */
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
await page.evaluate(async (t) => { await fetch('/api/v1/data-sources', { method: 'POST', headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + t }, body: JSON.stringify({ device_id: 1, category: 'audit_probe_dw3', edge_device_id: 7053, name: '抽屉来源3', max_fail_count: 3 }) }); }, tok);
for (const w of [360, 390, 768]) {
  await page.setViewportSize({ width: w, height: 800 });
  await page.goto(BASE + '/data-sources', { waitUntil: 'domcontentloaded' });
  await sleep(2200);
  const clicked = await page.evaluate(() => { const b = document.querySelector('[data-test="ds-detail"]'); if (!b) return false; b.click(); return true; });
  await sleep(2000);
  const st = await page.evaluate(() => {
    const all = [...document.querySelectorAll('.el-drawer')].map(d => { const r = d.getBoundingClientRect(); const s = getComputedStyle(d); return { cls: d.className.slice(0, 40), w: Math.round(r.width), left: Math.round(r.left), right: Math.round(r.right), disp: s.display, vis: s.visibility, cssW: s.width, maxW: s.maxWidth, size: d.getAttribute('size') }; });
    return { clicked: true, drawerCount: all.length, drawers: all, viewportW: window.innerWidth, docSW: document.documentElement.scrollWidth, docCW: document.documentElement.clientWidth };
  });
  await page.screenshot({ path: '/tmp/uiux-d3f/drawer3-' + w + '.png' });
  out.push({ w, screenshot: '/tmp/uiux-d3f/drawer3-' + w + '.png', clickedJs: clicked, ...st });
  await page.keyboard.press('Escape'); await sleep(600);
}
const cl = await page.evaluate(async (t) => { const j = await (await fetch('/api/v1/data-sources?category=audit_probe_dw3', { headers: { Authorization: 'Bearer ' + t } })).json(); const s = []; for (const it of (j?.data?.items || [])) s.push((await fetch('/api/v1/data-sources/' + it.id, { method: 'DELETE', headers: { Authorization: 'Bearer ' + t } })).status); return s; }, tok);
out.push({ cleanup: cl });
await browser.close();
fs.writeFileSync('/tmp/uiux-d3f/d3l-facts.json', JSON.stringify(out, null, 2));
for (const r of out) console.log(JSON.stringify(r));
