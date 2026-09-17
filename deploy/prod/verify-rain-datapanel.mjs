// 在数据面板中真实选择雨量计并查询，验证趋势数据可见
import { chromium } from '/home/sun/workspace/EHomeSystem/frontend-shared/node_modules/.pnpm/playwright@1.62.0/node_modules/playwright/index.mjs';
const BASE = 'http://127.0.0.1:8080';
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' }).then(c => c.newPage());
const consoleErrors = [];
page.on('pageerror', e => consoleErrors.push(e.message));
await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
await page.getByPlaceholder('请输入用户名').fill('admin');
await page.getByPlaceholder('请输入密码').fill('Admin@123456');
await page.getByRole('button', { name: /登\s*录/ }).click();
await page.waitForURL(/dashboard/, { timeout: 20000 }).catch(() => {});

await page.goto(BASE + '/data', { waitUntil: 'networkidle' });
await page.waitForTimeout(2000);
// 真实交互：打开设备下拉，选择雨量计
await page.locator('.el-select').first().click();
await page.waitForTimeout(1200);
const opt = page.getByText(/雨量计/).first();
console.log('雨量计选项可见:', await opt.count() > 0);
await opt.click();
await page.waitForTimeout(800);
// 点击查询
await page.getByRole('button', { name: /查\s*询/ }).click();
await page.waitForTimeout(4000);
const body = await page.locator('body').innerText();
console.log('页面含 rainfall:', body.includes('rainfall'));
console.log('页面片段:', JSON.stringify(body.replace(/\n+/g, ' | ').slice(0, 400)));
await page.screenshot({ path: '/tmp/ehome-browser/12-rain-trend.png', fullPage: true });
// 读接口确认点数
const n = await page.evaluate(async () => {
  const tok = localStorage.getItem('token') || sessionStorage.getItem('token') || '';
  const r = await fetch('/api/v1/data/query?device_id=2&range=24h', { headers: { Authorization: 'Bearer ' + tok } });
  const j = await r.json().catch(() => null);
  return { status: r.status, keys: j ? Object.keys(j.data || j) .slice(0,6) : null };
});
console.log('数据接口:', JSON.stringify(n));
console.log('console errors:', consoleErrors.length);
await browser.close();
