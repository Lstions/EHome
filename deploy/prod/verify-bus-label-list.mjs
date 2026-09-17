// 主控复验 P1b：边缘设备列表「总线」列/卡片应显示 UART0，不得出现 "UART 1"
import { chromium } from '/home/sun/workspace/EHomeSystem/frontend-shared/node_modules/.pnpm/playwright@1.62.0/node_modules/playwright/index.mjs';
const BASE = process.env.BASE || 'http://127.0.0.1:18094';
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' }).then(c => c.newPage());
await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
await page.getByPlaceholder('请输入用户名').fill('admin');
await page.getByPlaceholder('请输入密码').fill(process.env.PASS || 'Fix12345678!');
await page.getByRole('button', { name: /登\s*录/ }).click();
await page.waitForURL(/dashboard/, { timeout: 20000 }).catch(() => {});
await page.goto(BASE + '/edge-device', { waitUntil: 'networkidle' });
await page.waitForTimeout(3000);

// 卡片视图
const cardTxt = await page.locator('body').innerText();
const hasMisleading = /UART 1\b/.test(cardTxt);
console.log('卡片视图含误导值 "UART 1":', hasMisleading);
console.log('卡片视图含 UART0:', cardTxt.includes('UART0'));
const busLine = (cardTxt.match(/总线通道[\s\S]{0,30}/) || [''])[0].replace(/\n+/g, ' | ');
console.log('卡片总线行:', JSON.stringify(busLine));
await page.screenshot({ path: '/tmp/ehome-fixes/p1b-cards.png', fullPage: true });

// 切表格视图，读「总线」列
// 视图切换是带 aria-label 的图标按钮（无文字），必须按 aria-label 定位
await page.getByRole('button', { name: '表格视图' }).click();
await page.waitForTimeout(2000);
const rows = await page.evaluate(() => Array.from(document.querySelectorAll('tbody tr')).map(tr =>
  Array.from(tr.querySelectorAll('td')).map(td => td.innerText.trim())));
console.log('表格行:', JSON.stringify(rows));
const flat = JSON.stringify(rows);
console.log('表格含误导值 "UART 1":', /UART 1"/.test(flat) || /"UART 1"/.test(flat));
console.log('表格含 UART0:', /UART0/.test(flat));
await page.screenshot({ path: '/tmp/ehome-fixes/p1b-table.png', fullPage: true });
await browser.close();
let fail = 0;
if (hasMisleading) { console.log('FAIL 卡片出现 UART 1'); fail++; }
if (!cardTxt.includes('UART0')) { console.log('FAIL 卡片未显示 UART0'); fail++; }
if (!/UART0/.test(flat)) { console.log('FAIL 表格未显示 UART0'); fail++; }
process.exit(fail ? 1 : 0);
