/**
 * F5 危险确认 —— 真实 Chromium 取证（Playwright + 审计实例 8082）。
 *
 * 目的（两条，都不依赖 8082 的产物是否为最新）：
 *   A. 【独立复现 EP 渲染事实】触发 8082 上【旧产物】的真实确认框，
 *      读取 .el-message-box 内 button 的真实 class/aria/text 顺序——
 *      这是「不能用第一个 button 定位取消键」的证据依据。
 *   B. 【验证新逻辑在真实浏览器成立】把 feedback.ts 里修好的新逻辑
 *      （语义选择器 .el-message-box__btns button + setTimeout(0) 宏任务）
 *      用 page.evaluate 注入到同一个真实确认框上，读注入前后的 activeElement。
 *      8082 的产物是旧的（不含本次修复），所以只能注入验证，不能靠点页面验证新行为。
 *
 * 只读，不提交任何写请求：所有确认框一律走取消路径（点击取消键）。
 * 复跑：cd frontend-shared && node ../.tmp-probe/f5-focus-probe.mjs
 */
import { chromium } from '@playwright/test';

const BASE = 'http://127.0.0.1:8082';

const browser = await chromium.launch({
  executablePath: '/snap/bin/chromium',
  args: ['--no-sandbox', '--disable-setuid-sandbox'],
});
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' });
const page = await ctx.newPage();
const errs = [];
page.on('pageerror', e => errs.push(String(e).slice(0, 200)));

await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', 'admin');
await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await page.waitForTimeout(2000);

// ── A. 触发真实（旧产物）确认框，记录按钮顺序 + 修复前焦点 ──
await page.goto(BASE + '/node', { waitUntil: 'domcontentloaded' });
await page.waitForTimeout(2500);
await page.locator('.collector-card .el-button--danger').first().click().catch(e => errs.push('open:' + e.message));
await page.waitForTimeout(1200);

const describeInPage = `(el) => el ? {
  tag: el.tagName.toLowerCase(),
  cls: (el.className || '').toString(),
  text: (el.textContent || '').trim().slice(0, 12),
  aria: el.getAttribute('aria-label'),
} : null`;

const before = await page.evaluate(`(() => {
  const desc = ${describeInPage};
  const box = document.querySelector('.el-message-box');
  return {
    found: !!box,
    buttons: box ? [...box.querySelectorAll('button')].map(desc) : [],
    btnsAreaOrder: box ? [...box.querySelectorAll('.el-message-box__btns button')].map(b => (b.textContent || '').trim()) : [],
    activeElement: desc(document.activeElement),
  };
})()`);
console.log('=== A. 旧产物真实确认框 ===');
console.log(JSON.stringify(before, null, 2));

// ── B. 注入新逻辑，验证真实 Chromium 上的焦点落点 ──
const injected = await page.evaluate(`(async () => {
  const sleep = (ms) => new Promise(r => setTimeout(r, ms));
  const desc = ${describeInPage};
  const out = { beforeInject: desc(document.activeElement) };
  const root = document.querySelector('.el-message-box');
  if (root) root.focus();
  out.afterRootFocus = desc(document.activeElement);
  await sleep(0);
  const area = root && root.querySelector('.el-message-box__btns');
  const cancel = area && area.querySelector('button');
  if (cancel) cancel.focus();
  out.result = cancel ? (document.activeElement === cancel ? 'focused-cancel' : 'focus-failed') : 'no-cancel';
  out.afterMacrotaskFix = desc(document.activeElement);
  await sleep(0);
  out.afterSecondMacrotask = desc(document.activeElement);
  return out;
})()`);
console.log('=== B. 注入新逻辑（真实 Chromium） ===');
console.log(JSON.stringify(injected, null, 2));


// ── C. 决定性对比：默认焦点在根节点时，第一次 Tab 是否直达「删除」──
// 这是 F5「诱导性确认」的可执行定义：不解释，直接按真实键盘看落点。
const readActive = `(() => { const a = document.activeElement; return a.tagName.toLowerCase() + '.' + (a.className || '').toString().split(' ')[0] + '|' + (a.textContent || '').trim().slice(0, 10); })()`;
await page.evaluate(`document.querySelector('.el-message-box').focus()`);
const tabTrace = [];
tabTrace.push(await page.evaluate(readActive));
for (let i = 0; i < 3; i += 1) {
  await page.keyboard.press('Tab');
  await page.waitForTimeout(150);
  tabTrace.push(await page.evaluate(readActive));
}
console.log('=== C. 焦点在对话框根节点时，连续 Tab 的落点序列 ===');
console.log(JSON.stringify(tabTrace, null, 2));
await page.locator('.el-message-box__btns button').first().click().catch(() => {});
await page.waitForTimeout(500);
console.log('=== console errors ===');
console.log(JSON.stringify(errs));
await browser.close();
