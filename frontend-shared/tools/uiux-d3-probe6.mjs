/**
 * 定向复跑 6：§4.3.1 判据的准确测量——宽表格在 <=768px 是否被 .mobile-table-wrapper 包裹，
 * 以及「横向滚动能力」到底落在哪个元素上（el-table__body-wrapper 是 overflow:hidden 的壳，
 * 真正滚动的是内部 .el-scrollbar__wrap）。避免重犯契约 §2.2 的误报。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
const BASE = 'http://127.0.0.1:8082';
const OUT = '/tmp/uiux-d3f';
const sleep = ms => new Promise(r => setTimeout(r, ms));
fs.mkdirSync(OUT, { recursive: true });
const out = { results: [] };
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const ctx = await browser.newContext({ viewport: { width: 360, height: 800 }, locale: 'zh-CN' });
const page = await ctx.newPage();
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', 'admin');
await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await sleep(1500);

for (const [nm, route] of [['automation', '/automation'], ['alerts', '/alerts'], ['data-sources', '/data-sources'], ['firmware', '/firmware'], ['device-configs', '/device-configs']]) {
  for (const w of [360, 390, 768]) {
    await page.setViewportSize({ width: w, height: 800 });
    await page.goto(BASE + route, { waitUntil: 'domcontentloaded' });
    await sleep(1800);
    const st = await page.evaluate(() => {
      const tables = [...document.querySelectorAll('.el-table')];
      return {
        tableCount: tables.length,
        hasMobileWrapper: !!document.querySelector('.mobile-table-wrapper'),
        hasMobileHint: !!document.querySelector('.mobile-table-hint'),
        hintText: (document.querySelector('.mobile-table-hint') || {}).textContent || null,
        tables: tables.slice(0, 2).map(t => {
          const wrap = t.querySelector('.el-scrollbar__wrap');
          const bw = t.querySelector('.el-table__body-wrapper');
          const hw = t.querySelector('.el-table__header-wrapper');
          const cols = [...t.querySelectorAll('.el-table__body col')].map(c => parseInt(getComputedStyle(c).width) || 0);
          return {
            cls: t.className.split(' ').slice(0, 3).join('.'),
            tableW: Math.round(t.getBoundingClientRect().width),
            innerWrap: wrap ? { cw: wrap.clientWidth, sw: wrap.scrollWidth, ox: getComputedStyle(wrap).overflowX } : null,
            bodyWrapper: bw ? { cw: bw.clientWidth, sw: bw.scrollWidth, ox: getComputedStyle(bw).overflowX } : null,
            headerWrapper: hw ? { cw: hw.clientWidth, sw: hw.scrollWidth, ox: getComputedStyle(hw).overflowX } : null,
            colWidthSum: cols.reduce((a, b) => a + b, 0),
            fixedRightCols: t.querySelectorAll('.el-table-fixed-column--right').length,
          };
        }),
      };
    });
    // 实际尝试横向滚动最后一个表格
    const rollTest = await page.evaluate(() => {
      const t = [...document.querySelectorAll('.el-table')].pop();
      const wrap = t ? t.querySelector('.el-scrollbar__wrap') : null;
      if (!wrap) return null;
      const before = wrap.scrollLeft; wrap.scrollLeft = 100000;
      const after = wrap.scrollLeft;
      const opCol = t.querySelector('.el-table-fixed-column--right');
      const r = opCol ? opCol.getBoundingClientRect() : null;
      const res = { before, after, maxScroll: wrap.scrollWidth - wrap.clientWidth, opColRight: r ? Math.round(r.right) : null, viewportW: window.innerWidth, opColVisible: r ? r.right <= window.innerWidth + 1 && r.left >= -1 : null };
      wrap.scrollLeft = before;
      return res;
    });
    out.results.push({ page: nm, vp: 'w' + w, ...st, rollTest });
  }
}
await browser.close();
fs.writeFileSync(path.join(OUT, 'd3f-facts.json'), JSON.stringify(out, null, 2));
for (const r of out.results) { console.log('===== ' + r.page + ' @' + r.vp + ' ====='); console.log(JSON.stringify({ hasMobileWrapper: r.hasMobileWrapper, hasMobileHint: r.hasMobileHint, tableCount: r.tableCount, tables: r.tables, rollTest: r.rollTest }, null, 0)); }
