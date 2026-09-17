// 验证 OTA 历史表能正确渲染多行（修复前：只显示一行脏数据）
import { chromium } from '/home/sun/workspace/EHomeSystem/frontend-shared/node_modules/.pnpm/playwright@1.62.0/node_modules/playwright/index.mjs';
import fs from 'node:fs';
const BASE = process.env.BASE || 'http://127.0.0.1:18093';
const NODE = process.env.NODE || 'HIST01';
const OUT = '/tmp/ehome-otahist';
fs.mkdirSync(OUT, { recursive: true });
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' }).then(c => c.newPage());
await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
await page.getByPlaceholder('请输入用户名').fill('admin');
await page.getByPlaceholder('请输入密码').fill(process.env.PASS || 'OtaHist12345!');
await page.getByRole('button', { name: /登\s*录/ }).click();
await page.waitForURL(/dashboard/, { timeout: 20000 }).catch(() => {});
await page.goto(BASE + '/node/' + NODE, { waitUntil: 'networkidle' });
await page.waitForTimeout(2000);
// tab 元素是自定义 .tab-item（不是 el-tabs），点击触发 activateTab → fetchOTAHistory
const tab = page.locator('.tab-item').filter({ hasText: 'OTA 历史' }).first();
console.log('tab 元素数:', await tab.count());
await tab.click();
// 等真实行渲染（响应到达后 Vue 还需一帧），避免在渲染前取 DOM 得到 0 行的假阴性
// 注意：Element Plus 2.x 新版不再用 .el-table__body-wrapper 包 tbody，
// 用 'tbody tr' 才是稳定选择器（此前用旧选择器导致"0 行"的假阴性）。
await page.waitForFunction(() => document.querySelectorAll('tbody tr').length > 0, { timeout: 20000 }).catch(() => {});
await page.waitForTimeout(1200);
await page.screenshot({ path: OUT + '/history.png', fullPage: true });
const rows = await page.locator('tbody tr').count();
// 表格内容取所在容器文本（.el-table 类名同样已变）
const txt = await page.locator('table').first().innerText().catch(() => '');
console.log('表格行数 =', rows, '(期望 2)');
console.log('表格内容 =', JSON.stringify(txt.replace(/\n+/g, ' | ').slice(0, 300)));
console.log('含 2.5.21 :', txt.includes('2.5.21'));
console.log('含 2.5.22 :', txt.includes('2.5.22'));
console.log('含 成功  :', /成功/.test(txt));
console.log('含 失败  :', /失败/.test(txt));
console.log('含 100%  :', txt.includes('100'));
await browser.close();
