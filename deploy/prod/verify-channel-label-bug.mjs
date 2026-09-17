// 复现：删除对话框的「通道」标签把 Modbus 从站地址当成总线名
import { chromium } from '/home/sun/workspace/EHomeSystem/frontend-shared/node_modules/.pnpm/playwright@1.62.0/node_modules/playwright/index.mjs';
import fs from 'node:fs';
const BASE = 'http://127.0.0.1:8080';
fs.mkdirSync('/tmp/ehome-delete', { recursive: true });
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' }).then(c => c.newPage());
await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
await page.getByPlaceholder('请输入用户名').fill('admin');
await page.getByPlaceholder('请输入密码').fill('Admin@123456');
await page.getByRole('button', { name: /登\s*录/ }).click();
await page.waitForURL(/dashboard/, { timeout: 20000 }).catch(() => {});
await page.goto(BASE + '/edge-device', { waitUntil: 'networkidle' });
await page.waitForTimeout(2500);

// 对「雨量计(真实)」打开删除对话框（只读观察，不确认）
const card = page.locator('[class*="card"]').filter({ hasText: '雨量计(真实)' }).first();
await card.getByRole('button', { name: /删除/ }).click();
await page.waitForTimeout(1800);
const dlg = page.locator('.el-dialog').filter({ hasText: '删除边缘设备' }).first();
const txt = await dlg.innerText();
const chanLine = (txt.match(/通道\s*([^\n]+)/) || [])[1];
console.log('对话框显示通道 =', JSON.stringify(chanLine));
console.log('实际通道硬件ID   = "UART1"  ← 应显示这个');
console.log('设备在 UART1 上  =', chanLine && chanLine.includes('UART1'));
console.log('是否误显示为 slave 地址 "UART 1" =', chanLine === 'UART 1');
await page.screenshot({ path: '/tmp/ehome-delete/06-real-gauge-dialog.png', fullPage: true });
// 取消，不做任何修改
await dlg.getByRole('button', { name: /取\s*消/ }).click().catch(() => {});
await page.waitForTimeout(600);
await browser.close();
