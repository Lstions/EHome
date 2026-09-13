/** 定向复跑 13：抽屉 560px 在 360 视口的可见内容量（label 是否被推出屏幕）。 */
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
await page.evaluate(async (t) => { await fetch('/api/v1/data-sources', { method: 'POST', headers: { 'Content-Type': 'application/json', 'Authorization': 'Bearer ' + t }, body: JSON.stringify({ device_id: 1, category: 'audit_probe_dw4', edge_device_id: 7053, name: '抽屉来源4', max_fail_count: 3 }) }); }, tok);
for (const w of [360, 390]) {
  await page.setViewportSize({ width: w, height: 800 });
  await page.goto(BASE + '/data-sources', { waitUntil: 'domcontentloaded' });
  await sleep(2200);
  await page.evaluate(() => { const b = document.querySelector('[data-test="ds-detail"]'); if (b) b.click(); });
  await sleep(2000);
  const st = await page.evaluate(() => {
    const d = [...document.querySelectorAll('.el-drawer.open')][0];
    if (!d) return { found: false };
    const dr = d.getBoundingClientRect();
    const rr = el => el ? (() => { const x = el.getBoundingClientRect(); return { left: Math.round(x.left), right: Math.round(x.right), w: Math.round(x.width) }; })() : null;
    const labels = [...d.querySelectorAll('.el-descriptions__label')].slice(0, 4).map(l => ({ text: (l.textContent || '').trim().slice(0, 10), ...rr(l) }));
    const cells = [...d.querySelectorAll('.el-descriptions__content')].slice(0, 4).map(l => ({ text: (l.textContent || '').trim().slice(0, 14), ...rr(l) }));
    const header = d.querySelector('.el-drawer__header');
    const closeBtn = d.querySelector('.el-drawer__close-btn');
    return {
      drawer: rr(d), viewportW: window.innerWidth,
      hiddenLeftPx: Math.max(0, -dr.left),
      visibleFractionPct: Math.round(Math.min(dr.right, window.innerWidth) - Math.max(dr.left, 0)) / Math.round(dr.width) * 100,
      header: rr(header), headerText: header ? (header.textContent || '').trim().slice(0, 20) : null,
      headerOffscreen: header ? header.getBoundingClientRect().left < 0 : null,
      closeBtn: rr(closeBtn), closeBtnOffscreen: closeBtn ? closeBtn.getBoundingClientRect().right < 0 : null,
      labels, labelsVisible: labels.filter(l => l.left >= 0).length, labelsTotal: labels.length,
      cells,
      descTableSW: (() => { const t = d.querySelector('.el-descriptions__table'); return t ? { cw: t.clientWidth, sw: t.scrollWidth } : null; })(),
    };
  });
  await page.screenshot({ path: '/tmp/uiux-d3f/drawer4-' + w + '.png' });
  out.push({ w, screenshot: '/tmp/uiux-d3f/drawer4-' + w + '.png', ...st });
  await page.keyboard.press('Escape'); await sleep(600);
}
const cl = await page.evaluate(async (t) => { const j = await (await fetch('/api/v1/data-sources?category=audit_probe_dw4', { headers: { Authorization: 'Bearer ' + t } })).json(); const s = []; for (const it of (j?.data?.items || [])) s.push((await fetch('/api/v1/data-sources/' + it.id, { method: 'DELETE', headers: { Authorization: 'Bearer ' + t } })).status); return s; }, tok);
out.push({ cleanup: cl });
await browser.close();
fs.writeFileSync('/tmp/uiux-d3f/d3m-facts.json', JSON.stringify(out, null, 2));
for (const r of out) console.log(JSON.stringify(r, null, 1));
