// 浏览器视觉验证 OTA 升级流程，重点看进度条
import { chromium } from '/home/sun/workspace/EHomeSystem/frontend-shared/node_modules/.pnpm/playwright@1.62.0/node_modules/playwright/index.mjs';
import fs from 'node:fs';
const BASE = 'http://127.0.0.1:8080';
const NODE = 'F0F5BDFFFE02';
const OUT = '/tmp/ehome-ota';
fs.mkdirSync(OUT, { recursive: true });
const log = (...a) => console.log('[ota]', ...a);
const events = [];

const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' }).then(c => c.newPage());
const errs = [];
page.on('console', m => { if (m.type() === 'error') errs.push(m.text()); });
page.on('pageerror', e => errs.push('pageerror: ' + e.message));

try {
  await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
  await page.getByPlaceholder('请输入用户名').fill('admin');
  await page.getByPlaceholder('请输入密码').fill('Admin@123456');
  await page.getByRole('button', { name: /登\s*录/ }).click();
  await page.waitForURL(/dashboard/, { timeout: 20000 }).catch(() => {});

  // 节点详情
  await page.goto(BASE + '/node/' + NODE, { waitUntil: 'networkidle' });
  await page.waitForTimeout(2500);
  await page.screenshot({ path: OUT + '/01-node-detail.png', fullPage: true });

  // 点「OTA 升级」
  const otaBtn = page.getByRole('button', { name: /OTA\s*升级/ }).first();
  log('OTA按钮存在:', await otaBtn.count(), '禁用:', await otaBtn.isDisabled().catch(() => '?'));
  await otaBtn.click();
  await page.waitForTimeout(2000);
  await page.screenshot({ path: OUT + '/02-ota-form.png', fullPage: true });

  // 表单内容取证
  const dlg = page.locator('.el-dialog, .el-drawer').last();
  const formText = await dlg.innerText().catch(() => '(no dialog)');
  log('--- OTA 表单 ---');
  log(formText.replace(/\n/g, ' | ').slice(0, 500));

  // 选择固件并开始升级
  const selects = dlg.locator('.el-select');
  log('下拉框数:', await selects.count());
  if (await selects.count()) {
    await selects.first().click();
    await page.waitForTimeout(1200);
    await page.screenshot({ path: OUT + '/03-firmware-options.png', fullPage: true });
    const opt = page.locator('.el-select-dropdown__item').filter({ hasText: /2\.5\.21/ }).first();
    log('固件选项数:', await page.locator('.el-select-dropdown__item').count());
    if (await opt.count()) { await opt.click(); log('已选固件'); }
    else { log('未找到 2.5.21 选项'); }
    await page.waitForTimeout(800);
  }
  await page.screenshot({ path: OUT + '/04-form-filled.png', fullPage: true });

  // 提交
  const submit = dlg.getByRole('button', { name: /开始升级|确认|升级/ }).last();
  log('提交按钮:', await submit.count());
  await submit.click();
  log('已点击升级，开始高频采样进度条...');

  // 高频采样：每 700ms 抓一次进度条文本 + 宽度
  const seen = [];
  const t0 = Date.now();
  while (Date.now() - t0 < 150000) {
    const snap = await page.evaluate(() => {
      const bar = document.querySelector('.el-progress-bar__inner');
      const pct = document.querySelector('.el-progress__text');
      const boxes = Array.from(document.querySelectorAll('.el-message, .el-alert')).map(e => e.innerText);
      return {
        hasBar: !!bar,
        width: bar ? bar.style.width : null,
        pct: pct ? pct.innerText : null,
        msgs: boxes,
        body: (document.body.innerText || '').slice(0, 0),
      };
    }).catch(() => null);
    if (snap) {
      const key = JSON.stringify({ w: snap.width, p: snap.pct });
      if (!seen.length || seen[seen.length - 1].key !== key) {
        seen.push({ t: ((Date.now() - t0) / 1000).toFixed(1), key, width: snap.width, pct: snap.pct, msgs: snap.msgs });
        log('t=' + ((Date.now() - t0) / 1000).toFixed(1) + 's  bar=' + snap.hasBar + ' width=' + snap.width + ' text=' + snap.pct + (snap.msgs.length ? ' msgs=' + JSON.stringify(snap.msgs) : ''));
        await page.screenshot({ path: OUT + '/progress-' + seen.length.toString().padStart(2, '0') + '.png' });
      }
    }
    // 是否出现「升级成功/失败」
    const txt = await page.locator('body').innerText().catch(() => '');
    if (/升级成功|升级完成|升级失败/.test(txt)) { log('检测到终态文案'); break; }
    await page.waitForTimeout(700);
  }
  await page.screenshot({ path: OUT + '/99-final.png', fullPage: true });
  fs.writeFileSync(OUT + '/progress.json', JSON.stringify(seen, null, 2));
  log('采样点:', seen.length);
  log('console errors:', errs.length, errs.slice(0, 3).join(' | '));
} catch (e) {
  log('ERROR: ' + String(e));
  await page.screenshot({ path: OUT + '/99-error.png', fullPage: true }).catch(() => {});
} finally {
  await browser.close();
}
