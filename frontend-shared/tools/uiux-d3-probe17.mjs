/**
 * 定向复跑 17：自动化页在移动端的「每行固定操作列 + 500 行」放大效应
 *  ——el-table 的 fixed 列会为**每一行**额外渲染一份 DOM（探针实测 501 个 fixed td）。
 *  量化同一份数据在「保留 fixed 操作列」与「去掉 fixed」两种情形下的节点数差异。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE = 'http://127.0.0.1:8082';
const sleep = ms => new Promise(r => setTimeout(r, ms));
const out = {};
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
await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
await sleep(2500);
out.before = await page.evaluate(() => ({ els: document.querySelectorAll('body *').length, rows: document.querySelectorAll('.el-table__row').length, fixedCells: document.querySelectorAll('td.el-table-fixed-column--right').length, fixedRows: document.querySelectorAll('.el-table__row td.el-table-fixed-column--right').length }));
// 移除第二张表所有 fixed 列的 style/class（只改运行时 DOM，不碰源码）
out.after = await page.evaluate(() => {
  const t = [...document.querySelectorAll('.el-table')][1];
  t.querySelectorAll('td.el-table-fixed-column--right, th.el-table-fixed-column--right').forEach(el => { el.classList.remove('el-table-fixed-column--right'); el.classList.remove('is-last-column'); el.style.position = ''; el.style.right = ''; el.style.zIndex = ''; });
  return { els: document.querySelectorAll('body *').length, fixedCells: document.querySelectorAll('td.el-table-fixed-column--right').length };
});
// fixed 列的宽度检查：固定列是否遮住了正文
out.fixedGeom = await page.evaluate(() => {
  const t = [...document.querySelectorAll('.el-table')][1];
  const wrap = t.querySelector('.el-scrollbar__wrap');
  const fixed = document.querySelector('td.el-table-fixed-column--right');
  const cells = [...document.querySelectorAll('.el-table__row td')].slice(0, 9).map(td => { const r = td.getBoundingClientRect(); return { text: (td.textContent || '').trim().slice(0, 16), left: Math.round(r.left), right: Math.round(r.right), fixed: td.classList.contains('el-table-fixed-column--right') }; });
  return { viewportW: window.innerWidth, wrapCW: wrap ? wrap.clientWidth : null, wrapSW: wrap ? wrap.scrollWidth : null, fixedW: fixed ? Math.round(fixed.getBoundingClientRect().width) : null, cells };
});
// 移动端 360
await page.setViewportSize({ width: 360, height: 800 });
await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
await sleep(2500);
out.mobile = await page.evaluate(() => {
  const t = [...document.querySelectorAll('.el-table')][1];
  const wrap = t.querySelector('.el-scrollbar__wrap');
  const fixedTd = document.querySelector('td.el-table-fixed-column--right');
  const allRowTds = [...(t.querySelector('.el-table__row') ? t.querySelector('.el-table__row').querySelectorAll('td') : [])].map(td => { const r = td.getBoundingClientRect(); return { text: (td.textContent || '').trim().slice(0, 14), left: Math.round(r.left), right: Math.round(r.right), fixed: td.classList.contains('el-table-fixed-column--right') }; });
  const vis = allRowTds.filter(c => c.right > 36 && c.left < 324);
  return { viewportW: window.innerWidth, els: document.querySelectorAll('body *').length, rows: document.querySelectorAll('.el-table__row').length,
    fixedCells: document.querySelectorAll('td.el-table-fixed-column--right').length, fixedW: fixedTd ? Math.round(fixedTd.getBoundingClientRect().width) : null,
    wrapCW: wrap.clientWidth, wrapSW: wrap.scrollWidth, cells: allRowTds, visibleCellsAtScroll0: vis.map(c => c.text) };
});
await page.screenshot({ path: '/tmp/uiux-d3f/automation-360-row.png', clip: { x: 0, y: 560, width: 360, height: 240 } }).catch(() => {});
await browser.close();
fs.writeFileSync('/tmp/uiux-d3f/d3o-facts.json', JSON.stringify(out, null, 2));
console.log(JSON.stringify(out, null, 1));