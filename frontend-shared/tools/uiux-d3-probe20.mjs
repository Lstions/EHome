/**
 * 定向复跑 20：分页/筛选事实与「命令 ID 死链」的移动端复核。
 *  A. automation/alerts 两个表是否存在分页控件；渲染行数与后端上限 500 的关系。
 *  B. 移动端 360 下命令 ID 链接是否可点（固定列遮挡）。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE = 'http://127.0.0.1:8082';
const sleep = ms => new Promise(r => setTimeout(r, ms));
const out = [];
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' });
const page = await ctx.newPage();
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', 'admin');
await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await sleep(1500);

// A. 分页控件与行数
for (const [nm, route] of [['automation', '/automation'], ['alerts', '/alerts'], ['firmware', '/firmware'], ['device-configs', '/device-configs'], ['data-sources', '/data-sources']]) {
  await page.goto(BASE + route, { waitUntil: 'domcontentloaded' });
  await sleep(2200);
  const s = await page.evaluate(() => {
    const pag = [...document.querySelectorAll('.el-pagination')].map(p => ({ cls: p.className, text: (p.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 60), box: (() => { const r = p.getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height) }; })() }));
    const nets = performance.getEntriesByType('resource').filter(r => /\/api\/v1\//.test(r.name)).map(r => r.name.replace(location.origin, ''));
    return { paginationCount: pag.length, paginations: pag, tableCount: document.querySelectorAll('.el-table').length,
      rowsPerTable: [...document.querySelectorAll('.el-table')].map(t => t.querySelectorAll('.el-table__row').length),
      apiCalls: [...new Set(nets)],
      hasPageParam: [...new Set(nets)].some(u => /page=/.test(u)) };
  });
  out.push({ step: 'pagination', page: nm, ...s });
}

// B. 移动端 360：命令 ID 链接可点性
await page.setViewportSize({ width: 360, height: 800 });
await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
await sleep(2500);
const m = await page.evaluate(() => {
  const cell = document.querySelector('.el-table__row td:nth-child(6)');
  const btn = cell ? cell.querySelector('.el-button') : null;
  const wrap = document.querySelectorAll('.el-table')[1].querySelector('.el-scrollbar__wrap');
  const rr = el => el ? (() => { const r = el.getBoundingClientRect(); return { left: Math.round(r.left), right: Math.round(r.right), top: Math.round(r.top), bottom: Math.round(r.bottom), w: Math.round(r.width), h: Math.round(r.height) }; })() : null;
  let hit = null;
  if (btn) { const b = btn.getBoundingClientRect(); const cx = (Math.max(b.left, 36) + Math.min(b.right, 324)) / 2, cy = (b.top + b.bottom) / 2; const el = document.elementFromPoint(cx, cy); hit = { cx: Math.round(cx), cy: Math.round(cy), tag: el ? el.tagName.toLowerCase() : null, cls: el ? String(el.className || '').slice(0, 50) : null, isLink: el ? (el === btn || btn.contains(el)) : null }; }
  return { viewportW: window.innerWidth, cellBox: rr(cell), linkBox: rr(btn), hitTest: hit,
    wrapCW: wrap.clientWidth, wrapSW: wrap.scrollWidth, scrollLeft: wrap.scrollLeft,
    cmdColHeaderBox: rr([...document.querySelectorAll('.el-table')][1].querySelectorAll('th')[5]) };
});
await page.screenshot({ path: '/tmp/uiux-d3f/automation-360-cmdlink.png' });
out.push({ step: 'cmdlink-mobile', ...m });

await browser.close();
fs.writeFileSync('/tmp/uiux-d3f/d3r-facts.json', JSON.stringify(out, null, 2));
for (const r of out) { console.log('===== ' + r.step + ' ' + (r.page || '') + ' ====='); console.log(JSON.stringify(r, null, 1).slice(0, 1600)); }