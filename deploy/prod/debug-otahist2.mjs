import { chromium } from '/home/sun/workspace/EHomeSystem/frontend-shared/node_modules/.pnpm/playwright@1.62.0/node_modules/playwright/index.mjs';
const BASE = 'http://127.0.0.1:18093';
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' }).then(c => c.newPage());
const errs = [];
page.on('console', m => { if (m.type() === 'error') errs.push(m.text()); });
page.on('pageerror', e => errs.push('pageerror: ' + e.message));
// 抓 OTA history 请求与响应
page.on('response', async r => {
  if (r.url().includes('/ota/history')) {
    const t = await r.text().catch(() => '(no body)');
    console.log('RESP', r.status(), r.url(), t.slice(0, 220));
  }
});
await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
await page.getByPlaceholder('请输入用户名').fill('admin');
await page.getByPlaceholder('请输入密码').fill('OtaHist12345!');
await page.getByRole('button', { name: /登\s*录/ }).click();
await page.waitForURL(/dashboard/, { timeout: 20000 }).catch(() => {});
await page.goto(BASE + '/node/HIST01', { waitUntil: 'networkidle' });
await page.waitForTimeout(2500);
await page.locator('.tab-item').filter({ hasText: 'OTA 历史' }).first().click();
await page.waitForTimeout(3500);
// 直接看 Vue 组件里的 otaHistory 长度不可得，改为看 DOM
const info = await page.evaluate(() => {
  const empty = document.querySelector('.el-empty__description');
  const tables = document.querySelectorAll('.el-table');
  const tablesWithRows = Array.from(tables).map(t => t.querySelectorAll('tbody tr').length);
  const otaSection = Array.from(document.querySelectorAll('.el-card')).map(c => c.innerText.slice(0, 60));
  return { emptyText: empty ? empty.textContent : null, tableCount: tables.length, tablesWithRows, otaSection };
});
console.log('DOM:', JSON.stringify(info, null, 1));
await page.screenshot({ path: '/tmp/ehome-otahist/debug2.png', fullPage: true });
console.log('console errors:', errs.slice(0, 3));
await browser.close();
