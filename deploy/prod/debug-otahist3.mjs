import { chromium } from '/home/sun/workspace/EHomeSystem/frontend-shared/node_modules/.pnpm/playwright@1.62.0/node_modules/playwright/index.mjs';
const BASE = 'http://127.0.0.1:18093';
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' }).then(c => c.newPage());
await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
await page.getByPlaceholder('请输入用户名').fill('admin');
await page.getByPlaceholder('请输入密码').fill('OtaHist12345!');
await page.getByRole('button', { name: /登\s*录/ }).click();
await page.waitForURL(/dashboard/, { timeout: 20000 }).catch(() => {});
await page.goto(BASE + '/node/HIST01', { waitUntil: 'networkidle' });
await page.waitForTimeout(2500);
await page.locator('.tab-item').filter({ hasText: 'OTA 历史' }).first().click();
await page.waitForTimeout(4000);
const info = await page.evaluate(() => {
  const tables = Array.from(document.querySelectorAll('.el-table'));
  return {
    tableCount: tables.length,
    perTable: tables.map(t => ({
      rows_tbody_tr: t.querySelectorAll('tbody tr').length,
      rows_any_tr: t.querySelectorAll('tr').length,
      text: t.innerText.replace(/\n+/g, ' | ').slice(0, 120),
    })),
    // el-table 可能用 div 结构（虚拟表格）
    rowClassCounts: ['.el-table__row', '.el-table__body tr', 'tbody tr', '.el-table__body-wrapper tr'].map(s => ({ sel: s, n: document.querySelectorAll(s).length })),
  };
});
console.log(JSON.stringify(info, null, 1));
await browser.close();
