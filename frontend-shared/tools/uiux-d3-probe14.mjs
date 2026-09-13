/** 定向复跑 14：补齐 alerts / data-sources 对话框的 92vw 与 footer 几何；并取字体事实。 */
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
const GEO = () => {
  const d = document.querySelector('.el-dialog');
  const f = document.querySelector('.el-dialog__footer');
  const b = document.querySelector('.el-dialog__body');
  const rr = el => el ? (() => { const x = el.getBoundingClientRect(); return { w: Math.round(x.width), h: Math.round(x.height), top: Math.round(x.top), bottom: Math.round(x.bottom), left: Math.round(x.left), right: Math.round(x.right) }; })() : null;
  const ovd = d ? d.closest('.el-overlay-dialog') : null;
  return { title: (document.querySelector('.el-dialog__title') || {}).textContent || null, viewportW: window.innerWidth, spec92vw: Math.round(window.innerWidth * 0.92),
    dialog: rr(d), cssWidth: d ? getComputedStyle(d).width : null, maxWidth: d ? getComputedStyle(d).maxWidth : null,
    footer: rr(f), footerInViewport: f ? f.getBoundingClientRect().bottom <= window.innerHeight + 1 : null,
    body: rr(b), bodyScrollable: b ? b.scrollHeight > b.clientHeight : null,
    overlayDialogScrollMax: ovd ? ovd.scrollHeight - ovd.clientHeight : null };
};
const reachTest = () => { const d = document.querySelector('.el-dialog'); const ovd = d ? d.closest('.el-overlay-dialog') : null; if (ovd) ovd.scrollTop = 100000; const f = document.querySelector('.el-dialog__footer'); return { scrollTop: ovd ? ovd.scrollTop : null, footerBottom: f ? Math.round(f.getBoundingClientRect().bottom) : null, vh: window.innerHeight, reachable: f ? f.getBoundingClientRect().bottom <= window.innerHeight + 1 : null }; };

for (const vp of [{ n: 'desktop-1440', w: 1440, h: 900 }, { n: 'mobile-360', w: 360, h: 800 }]) {
  await page.setViewportSize({ width: vp.w, height: vp.h });
  // alerts 创建规则
  await page.goto(BASE + '/alerts', { waitUntil: 'domcontentloaded' }); await sleep(1800);
  await page.click('[data-test="create-rule"]'); await sleep(800);
  out.push({ case: 'alerts-create-dialog', vp: vp.n, geo: await page.evaluate(GEO), reach: await page.evaluate(reachTest) });
  await page.screenshot({ path: '/tmp/uiux-d3c/dlg-alerts-' + vp.n + '.png' });
  await page.keyboard.press('Escape'); await sleep(500);
  // data-sources 新建
  await page.goto(BASE + '/data-sources', { waitUntil: 'domcontentloaded' }); await sleep(1800);
  await page.click('[data-test="create-source"]'); await sleep(800);
  out.push({ case: 'data-sources-create-dialog', vp: vp.n, geo: await page.evaluate(GEO), reach: await page.evaluate(reachTest) });
  await page.screenshot({ path: '/tmp/uiux-d3c/dlg-datasources-' + vp.n + '.png' });
  await page.keyboard.press('Escape'); await sleep(500);
  // 字体事实
  const fonts = await page.evaluate(() => {
    const g = el => el ? getComputedStyle(el).fontFamily : null;
    return { htmlFont: g(document.documentElement), bodyFont: g(document.body), tableCell: g(document.querySelector('.el-table__row td')), btnFont: g(document.querySelector('.el-button')), inputFont: g(document.querySelector('.el-input__inner')), tagFont: g(document.querySelector('.el-tag')), pageHeaderFont: g(document.querySelector('.page-header h2')) };
  });
  out.push({ case: 'fonts', vp: vp.n, fonts });
}
await browser.close();
fs.writeFileSync('/tmp/uiux-d3c/d3n-facts.json', JSON.stringify(out, null, 2));
for (const r of out) console.log(JSON.stringify(r));
