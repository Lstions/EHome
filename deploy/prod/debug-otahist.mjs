import { chromium } from '/home/sun/workspace/EHomeSystem/frontend-shared/node_modules/.pnpm/playwright@1.62.0/node_modules/playwright/index.mjs';
const BASE = 'http://127.0.0.1:18093';
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' }).then(c => c.newPage());
page.on('pageerror', e => console.log('PAGEERROR:', e.message));
await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
await page.getByPlaceholder('请输入用户名').fill('admin');
await page.getByPlaceholder('请输入密码').fill('OtaHist12345!');
await page.getByRole('button', { name: /登\s*录/ }).click();
await page.waitForURL(/dashboard/, { timeout: 20000 }).catch(() => {});
await page.goto(BASE + '/node/HIST01', { waitUntil: 'networkidle' });
await page.waitForTimeout(3000);
console.log('URL:', page.url());
const body = await page.locator('body').innerText();
console.log('页面片段:', JSON.stringify(body.replace(/\n+/g, ' | ').slice(0, 400)));
// 找 OTA 历史 tab 的所有可能形态
const tabs = await page.locator('.el-tabs__item').allInnerTexts().catch(() => []);
console.log('tabs:', JSON.stringify(tabs));
const histTab = page.locator('.el-tabs__item').filter({ hasText: 'OTA 历史' }).first();
console.log('tab 数:', await histTab.count());
if (await histTab.count()) {
  await histTab.click();
  await page.waitForTimeout(3000);
  const tables = await page.locator('.el-table').count();
  const rows = await page.locator('.el-table__body-wrapper tbody tr').count();
  console.log('点击后 table 数:', tables, ' 行数:', rows);
  const t = await page.locator('.el-table').first().innerText().catch(() => '(none)');
  console.log('表格:', JSON.stringify(t.replace(/\n+/g, ' | ').slice(0, 300)));
  await page.screenshot({ path: '/tmp/ehome-otahist/debug.png', fullPage: true });
}
await browser.close();
