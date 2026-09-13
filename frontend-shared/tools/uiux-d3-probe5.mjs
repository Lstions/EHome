/**
 * 定向复跑 5：核实规范 §4.3.2「Teleport 的弹窗样式必须是全局选择器，
 * 不能依赖 scoped CSS 穿透」——DeviceConfigForm.vue 用 class + :deep() 给
 * el-dialog / el-dialog__body 设样式，若 scoped 属性未随 Teleport 生效，
 * 则 360px 下 width:95%!important 与 body max-height:70vh 都不会应用。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
const BASE = 'http://127.0.0.1:8082';
const OUT = '/tmp/uiux-d3e';
const sleep = ms => new Promise(r => setTimeout(r, ms));
fs.mkdirSync(OUT, { recursive: true });
const out = { results: [] };
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

for (const vp of [{ n: 'desktop-1440x900', w: 1440, h: 900 }, { n: 'mobile-390x844', w: 390, h: 844 }, { n: 'mobile-360x800', w: 360, h: 800 }]) {
  await page.setViewportSize({ width: vp.w, height: vp.h });
  await page.goto(BASE + '/device-configs', { waitUntil: 'domcontentloaded' });
  await sleep(1800);
  await page.click('.filter-right button:last-child');
  await sleep(1200);
  const st = await page.evaluate(() => {
    const d = document.querySelector('.el-dialog');
    if (!d) return { dialogFound: false };
    const b = d.querySelector('.el-dialog__body');
    const f = d.querySelector('.el-dialog__footer');
    const ov = d.closest('.el-overlay-dialog');
    const cs = getComputedStyle(d);
    const bs = b ? getComputedStyle(b) : null;
    const r = el => el ? (() => { const x = el.getBoundingClientRect(); return { w: Math.round(x.width), h: Math.round(x.height), top: Math.round(x.top), bottom: Math.round(x.bottom), left: Math.round(x.left), right: Math.round(x.right) }; })() : null;
    return {
      dialogFound: true,
      cls: d.className,
      hasDataAttr: [...d.attributes].map(a => a.name).filter(n => n.startsWith('data-v')),
      width: cs.width, maxWidth: cs.maxWidth, marginTop: cs.marginTop,
      bodyMaxHeight: bs ? bs.maxHeight : null, bodyOverflowY: bs ? bs.overflowY : null,
      bodyBox: r(b), footerBox: r(f), dialogBox: r(d),
      bodyScrollable: b ? b.scrollHeight > b.clientHeight : null, bodyScrollH: b ? b.scrollHeight : null, bodyClientH: b ? b.clientHeight : null,
      overlayDialogOverflowY: ov ? getComputedStyle(ov).overflowY : null,
      overlayDialogScrollTopMax: ov ? ov.scrollHeight - ov.clientHeight : null,
      formItemCount: d.querySelectorAll('.el-form-item').length,
      viewport: { w: window.innerWidth, h: window.innerHeight },
      spec92vw: Math.round(window.innerWidth * 0.92),
    };
  });
  const shot = path.join(OUT, 'deviceconfig-form-' + vp.n + '.png');
  await page.screenshot({ path: shot });
  // 尝试滚动到 footer，并点击创建（不提交真实数据，只看可达性）
  const reach = await page.evaluate(() => {
    const d = document.querySelector('.el-dialog');
    const ov = d ? d.closest('.el-overlay-dialog') : null;
    if (ov) ov.scrollTop = 100000;
    const f = d ? d.querySelector('.el-dialog__footer') : null;
    return { scrolled: ov ? ov.scrollTop : null, footerBottom: f ? Math.round(f.getBoundingClientRect().bottom) : null, viewportH: window.innerHeight, footerReachable: f ? f.getBoundingClientRect().bottom <= window.innerHeight + 1 : null };
  });
  const shot2 = path.join(OUT, 'deviceconfig-form-scrolled-' + vp.n + '.png');
  await page.screenshot({ path: shot2 });
  out.results.push({ vp: vp.n, screenshot: shot, screenshotScrolled: shot2, ...st, reach });
  await page.keyboard.press('Escape'); await sleep(600);
}
await browser.close();
fs.writeFileSync(path.join(OUT, 'd3e-facts.json'), JSON.stringify(out, null, 2));
for (const r of out.results) { console.log('===== ' + r.vp + ' ====='); console.log(JSON.stringify(r, null, 1)); }
