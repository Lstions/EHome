// 浏览器操作：删除「测试雨量计」(UART0/模拟)，逐步截图供视觉验证
import { chromium } from '/home/sun/workspace/EHomeSystem/frontend-shared/node_modules/.pnpm/playwright@1.62.0/node_modules/playwright/index.mjs';
import fs from 'node:fs';
const BASE = 'http://127.0.0.1:8080';
const OUT = '/tmp/ehome-delete';
fs.mkdirSync(OUT, { recursive: true });
const TARGET = process.env.TARGET_NAME || '测试雨量计';
const log = (...a) => console.log('[del]', ...a);
const shots = [];
const shot = async (page, name) => {
  const p = OUT + '/' + name + '.png';
  await page.screenshot({ path: p, fullPage: true });
  shots.push(p);
  log('screenshot: ' + name);
};

const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' }).then(c => c.newPage());
const errors = [];
page.on('console', m => { if (m.type() === 'error') errors.push(m.text()); });
page.on('pageerror', e => errors.push('pageerror: ' + e.message));

try {
  // 1) 登录
  await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
  await shot(page, '01-login');
  await page.getByPlaceholder('请输入用户名').fill('admin');
  await page.getByPlaceholder('请输入密码').fill('Admin@123456');
  await page.getByRole('button', { name: /登\s*录/ }).click();
  await page.waitForURL(/dashboard/, { timeout: 20000 }).catch(() => {});

  // 2) 进入边缘设备页
  await page.goto(BASE + '/edge-device', { waitUntil: 'networkidle' });
  await page.waitForTimeout(2500);
  await shot(page, '02-list-before');
  const bodyBefore = await page.locator('body').innerText();
  log('列表含目标设备:', bodyBefore.includes(TARGET));
  const countBefore = (bodyBefore.match(/测试雨量计/g) || []).length;
  log('目标出现次数:', countBefore);

  // 3) 点该卡片上的「删除」（真实点击）
  const card = page.locator('.device-card, .el-card, [class*="card"]').filter({ hasText: TARGET }).first();
  const delBtn = card.getByRole('button', { name: /删除/ }).first();
  log('卡片内删除按钮数:', await delBtn.count());
  await delBtn.click();
  await page.waitForTimeout(1800);
  await shot(page, '03-delete-dialog');

  // 4) 确认对话框内容（取证）
  const dlg = page.locator('.el-dialog').filter({ hasText: '删除边缘设备' }).first();
  const dlgText = await dlg.innerText().catch(() => '(未找到对话框)');
  log('--- 对话框文本 ---');
  log(dlgText.replace(/\n/g, ' | '));
  // 对话框内关键事实是否与目标一致？
  log('对话框含目标名:', dlgText.includes(TARGET));
  log('对话框含 UART0:', dlgText.includes('UART0'));
  log('逻辑设备信息区:', /逻辑设备信息/.test(dlgText));
  log('历史数据处理选项:', /历史数据处理/.test(dlgText));

  // 5) 保持默认「保留历史数据」，点确认删除
  const confirmBtn = dlg.getByRole('button', { name: /确认删除/ });
  log('确认按钮数:', await confirmBtn.count());
  await shot(page, '04-dialog-before-confirm');
  await confirmBtn.click();
  await page.waitForTimeout(3500);
  await shot(page, '05-after-delete');

  // 6) 结果核对
  const bodyAfter = await page.locator('body').innerText();
  log('删除后列表仍含目标:', bodyAfter.includes(TARGET));
  log('删除后仍含真实雨量计:', bodyAfter.includes('雨量计(真实)'));
  log('删除后仍含 BMS:', bodyAfter.includes('测试BMS'));
  const toast = await page.locator('.el-message').allInnerTexts().catch(() => []);
  log('提示消息:', JSON.stringify(toast));

  const fatal = errors.filter(e => !/favicon|ResizeObserver|Failed to load resource/i.test(e));
  log('console errors:', fatal.length, fatal.slice(0, 3).join(' | '));
} catch (e) {
  log('ERROR: ' + String(e));
  await shot(page, '99-error');
} finally {
  await browser.close();
  fs.writeFileSync(OUT + '/shots.json', JSON.stringify(shots, null, 2));
}
