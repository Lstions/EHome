/**
 * F5 危险确认 —— 真实 Chromium 取证 2：EP 默认 autofocus 的「回车即删除」。
 *
 * 取证实例用 8082 上的【旧产物】里一个**未经迁移**的 ElMessageBox.confirm
 * （/alerts 的「删除规则」，源码 AlertRules.vue:271，未带 confirmButtonType/autofocus）。
 * 只读：点删除按钮打开确认框，立刻 Escape 关闭，不提交任何写请求。
 *
 * 复跑：cd frontend-shared && node tools/f5-default-autofocus-probe.mjs
 */
import { chromium } from '@playwright/test';

const BASE = 'http://127.0.0.1:8082';
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox'] });
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

await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
await page.waitForTimeout(2500);
await page.locator('button:has-text("删除")').first().click().catch(e => errs.push('open:' + e.message));
await page.waitForTimeout(1200);

const readActive = `(() => { const a = document.activeElement; return { tag: a.tagName.toLowerCase(), cls: (a.className || '').toString(), text: (a.textContent || '').trim().slice(0, 12) }; })()`;
const info = await page.evaluate(`(() => {
  const box = document.querySelector('.el-message-box');
  const desc = (el) => el ? { cls: (el.className || '').toString(), text: (el.textContent || '').trim().slice(0, 12), aria: el.getAttribute('aria-label') } : null;
  return {
    boxFound: !!box,
    buttons: box ? [...box.querySelectorAll('button')].map(desc) : [],
    active: desc(document.activeElement),
    message: box ? (box.querySelector('.el-message-box__message') || {}).textContent : null,
  };
})()`);
console.log('=== D. 未迁移的裸 ElMessageBox.confirm（/automation 删除规则）===');
console.log(JSON.stringify(info, null, 2));

// 不按回车（会真的删除），只记录焦点即关闭
const active = await page.evaluate(readActive);
console.log('activeElement:', JSON.stringify(active));
console.log('=> 焦点落在危险按钮上:', active.text === '删除' || active.text === '确定');

await page.keyboard.press('Escape');
await page.waitForTimeout(600);
console.log('=== console errors ===');
console.log(JSON.stringify(errs));
await browser.close();
