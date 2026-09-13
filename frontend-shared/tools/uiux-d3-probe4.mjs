/**
 * 定向复跑 4：对话框 footer 是否真的可达（区分「视觉在视口外」与「用户无法滚动到」）。
 * 关键：必须测量**包含该 dialog 的**那个 .el-overlay（上一版用 querySelector('.el-overlay')
 * 可能命中隐藏的 teleport 残留，ch=0 即为证据）。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
const BASE = 'http://127.0.0.1:8082';
const OUT = '/tmp/uiux-d3d';
const sleep = ms => new Promise(r => setTimeout(r, ms));
fs.mkdirSync(OUT, { recursive: true });
const out = { results: [] };
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });

for (const theme of ['light', 'dark']) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', colorScheme: theme === 'dark' ? 'dark' : 'light' });
  const page = await ctx.newPage();
  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
  await page.evaluate(t => { localStorage.setItem('theme', t); }, theme);
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]', 'admin');
  await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
  await page.click('button:has-text("登")');
  await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
  await sleep(1500);

  for (const vp of [{ n: 'desktop-1440x900', w: 1440, h: 900 }, { n: 'mobile-390x844', w: 390, h: 844 }, { n: 'mobile-360x800', w: 360, h: 800 }]) {
    await page.setViewportSize({ width: vp.w, height: vp.h });
    await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
    await sleep(1800);
    await page.click('[data-test="create-rule"]');
    await sleep(800);

    // 1) 精确定位承载 dialog 的 overlay
    const geo = await page.evaluate(() => {
      const d = document.querySelector('.el-dialog');
      const ov = d ? d.closest('.el-overlay') : null;
      const r = el => el ? (() => { const x = el.getBoundingClientRect(); return { top: Math.round(x.top), bottom: Math.round(x.bottom), h: Math.round(x.height) }; })() : null;
      const f = document.querySelector('.el-dialog__footer');
      const b = document.querySelector('.el-dialog__body');
      const ovStyle = ov ? getComputedStyle(ov) : null;
      return {
        overlayCount: document.querySelectorAll('.el-overlay').length,
        overlayVisibleCount: [...document.querySelectorAll('.el-overlay')].filter(o => getComputedStyle(o).display !== 'none').length,
        ovBox: r(ov), ovCh: ov ? ov.clientHeight : null, ovSh: ov ? ov.scrollHeight : null,
        ovOverflowY: ovStyle ? ovStyle.overflowY : null, ovDisplay: ovStyle ? ovStyle.display : null, ovPosition: ovStyle ? ovStyle.position : null,
        ovAlignItems: ovStyle ? ovStyle.alignItems : null,
        dialog: r(d), footer: r(f), body: r(b),
        footerInViewport: f ? f.getBoundingClientRect().bottom <= window.innerHeight + 1 : null,
        saveBtnBox: (() => { const s = document.querySelector('[data-test="save-rule"]'); if (!s) return null; const x = s.getBoundingClientRect(); return { top: Math.round(x.top), bottom: Math.round(x.bottom), left: Math.round(x.left), w: Math.round(x.width), h: Math.round(x.height) }; })(),
      };
    });

    // 2) 尝试程序化滚动 overlay 到最底（模拟用户滚轮）
    const reached = await page.evaluate(() => {
      const d = document.querySelector('.el-dialog');
      const ov = d ? d.closest('.el-overlay') : null;
      if (!ov) return { scrolled: false, reason: 'no overlay' };
      const before = ov.scrollTop;
      ov.scrollTop = 100000;
      const after = ov.scrollTop;
      // 也试 overlay-dialog 与 body
      const od = d.closest('.el-overlay-dialog');
      let odAfter = null;
      if (od) { od.scrollTop = 100000; odAfter = od.scrollTop; }
      document.documentElement.scrollTop = 100000; document.body.scrollTop = 100000;
      const f = document.querySelector('.el-dialog__footer');
      return { before, after, scrolledBy: after - before, overlayDialogScrollTop: odAfter, overlayDialogOverflowY: od ? getComputedStyle(od).overflowY : null,
        footerBottomAfterScroll: f ? Math.round(f.getBoundingClientRect().bottom) : null, viewportH: window.innerHeight,
        footerReachableAfterScroll: f ? f.getBoundingClientRect().bottom <= window.innerHeight + 1 : null,
        docScrollTop: document.documentElement.scrollTop };
    });

    // 3) 真实滚轮事件（鼠标在 overlay 上滚）
    await page.mouse.move(vp.w / 2, vp.h / 2);
    for (let i = 0; i < 12; i++) await page.mouse.wheel(0, 400).catch(() => {});
    await sleep(600);
    const afterWheel = await page.evaluate(() => {
      const f = document.querySelector('.el-dialog__footer');
      const d = document.querySelector('.el-dialog');
      const ov = d ? d.closest('.el-overlay') : null;
      return { overlayScrollTop: ov ? ov.scrollTop : null, footerBottom: f ? Math.round(f.getBoundingClientRect().bottom) : null,
        viewportH: window.innerHeight, footerReachable: f ? f.getBoundingClientRect().bottom <= window.innerHeight + 1 : null,
        saveVisible: (() => { const s = document.querySelector('[data-test="save-rule"]'); if (!s) return null; const x = s.getBoundingClientRect(); return x.top >= 0 && x.bottom <= window.innerHeight; })() };
    });
    const shot = path.join(OUT, 'dlg-scroll-' + theme + '-' + vp.n + '.png');
    await page.screenshot({ path: shot });
    out.results.push({ theme, vp: vp.n, screenshot: shot, geo, reached, afterWheel });
    await page.keyboard.press('Escape'); await sleep(400);
  }
  await ctx.close();
}
await browser.close();
fs.writeFileSync(path.join(OUT, 'd3d-facts.json'), JSON.stringify(out, null, 2));
for (const r of out.results) { console.log('===== ' + r.theme + ' | ' + r.vp + ' ====='); console.log(JSON.stringify({ geo: r.geo, reached: r.reached, afterWheel: r.afterWheel }, null, 1)); }
