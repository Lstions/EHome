// 主控独立复验三处修复（不采信子代理自报）—— 对隔离栈跑，真实浏览器交互
// 用法: BASE=http://127.0.0.1:18094 PASS=... node verify-fixes-browser.mjs
import { chromium } from '/home/sun/workspace/EHomeSystem/frontend-shared/node_modules/.pnpm/playwright@1.62.0/node_modules/playwright/index.mjs';
import fs from 'node:fs';
const BASE = process.env.BASE || 'http://127.0.0.1:18094';
const PASS = process.env.PASS || 'Fix12345678!';
const OUT = process.env.OUT || '/tmp/ehome-fixes';
fs.mkdirSync(OUT, { recursive: true });
const res = [];
const check = (n, ok, d = '') => { res.push({ n, ok: !!ok, d: String(d).slice(0, 240) }); console.log((ok ? 'PASS ' : 'FAIL ') + n + (d ? ' :: ' + d : '')); };

const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' }).then(c => c.newPage());
const errs = [];
page.on('console', m => { if (m.type() === 'error') errs.push(m.text()); });
page.on('pageerror', e => errs.push('pageerror: ' + e.message));
try {
  await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
  await page.getByPlaceholder('请输入用户名').fill('admin');
  await page.getByPlaceholder('请输入密码').fill(PASS);
  await page.getByRole('button', { name: /登\s*录/ }).click();
  await page.waitForURL(/dashboard/, { timeout: 20000 }).catch(() => {});

  // ─ P1：删除对话框「通道」应显示真实总线（UART0），不得是 "UART 1" ──
  await page.goto(BASE + '/edge-device', { waitUntil: 'networkidle' });
  await page.waitForTimeout(2500);
  const card = page.locator('[class*="card"]').filter({ hasText: '测试雨量计' }).first();
  if (await card.count()) {
    await card.getByRole('button', { name: /删除/ }).first().click();
    await page.waitForTimeout(1800);
    const dlg = page.locator('.el-dialog').filter({ hasText: '删除边缘设备' }).first();
    const txt = await dlg.innerText().catch(() => '');
    const chan = (txt.match(/通道\s*([^\n]+)/) || [])[1];
    check('P1 通道显示真实总线 UART0', chan && chan.trim() === 'UART0', 'chan=' + JSON.stringify(chan));
    check('P1 不再出现误导值 "UART 1"', !/UART 1$/.test((chan || '').trim()), 'chan=' + JSON.stringify(chan));
    await page.screenshot({ path: OUT + '/p1-dialog.png', fullPage: true });
    await dlg.getByRole('button', { name: /取\s*消/ }).click().catch(() => {});
    await page.waitForTimeout(500);
  } else {
    check('P1 找到目标设备卡片', false, 'card not found');
  }

  // ── P2：数据面板「数据」列应显示 rainfall: 0.50（不再恒 —） ──
  await page.goto(BASE + '/data', { waitUntil: 'networkidle' });
  await page.waitForTimeout(2000);
  await page.locator('.el-select').first().click();
  await page.waitForTimeout(1000);
  const opt = page.getByText(/测试雨量计/).first();
  if (await opt.count()) await opt.click();
  await page.waitForTimeout(800);
  await page.getByRole('button', { name: /查\s*询/ }).click();
  await page.waitForTimeout(3500);
  // Element Plus 把表头与表体拆成两个 <table>，只读第一个 table 会只拿到表头
  // （曾因此误判"没有数据行"）。这里直接读 tbody 行的每个单元格。
  const rowsTxt = await page.evaluate(() => Array.from(document.querySelectorAll('tbody tr'))
    .map(tr => Array.from(tr.querySelectorAll('td')).map(td => td.innerText.trim())));
  const flat = JSON.stringify(rowsTxt);
  check('P2 数据列不再是恒 —', /rainfall:\s*0\.50/.test(flat), flat.slice(0, 200));
  check('P2 原始数据列有 hex', /\d+B hex/.test(flat), flat.slice(0, 200));
  // 「有值」与「取不到」必须区分开：所有行都必须显示具体读数，不得退化为 —
  // （注意不要断言"必须是 0.00"—— 那是**环境特定**的：真实雨量计读数 0mm 时为 0.00，
  //  而 UART0 台架模拟器恒为 0.50。断言具体数值会让脚本只在某种接线下通过。）
  const rowVals = rowsTxt.map(r => r[1]);
  const allHaveValues = rowVals.length > 0 && rowVals.every(v => /^[a-z_]+:\s*[-\d.]+/i.test(v));
  check('P2 每行都有具体读数（未退化为 —）', allHaveValues, JSON.stringify(rowVals.slice(0, 3)));
  await page.screenshot({ path: OUT + '/p2-datapanel.png', fullPage: true });

  // ── P4：OTA 弹层固件信息不得竖排 ──
  await page.goto(BASE + '/node/' + (process.env.NODE || 'F0F5BDFFFE02'), { waitUntil: 'networkidle' });
  await page.waitForTimeout(2000);
  const otaBtn = page.getByRole('button', { name: /OTA\s*升级/ }).first();
  if (await otaBtn.count() && !(await otaBtn.isDisabled())) {
    await otaBtn.click();
    await page.waitForTimeout(1500);
    const dlg2 = page.locator('.el-dialog').filter({ hasText: 'OTA 固件升级' }).first();
    const sel = dlg2.locator('.el-select').first();
    if (await sel.count()) { await sel.click(); await page.waitForTimeout(1200); const o = page.locator('.el-select-dropdown__item').first(); if (await o.count()) await o.click(); }
    await page.waitForTimeout(1200);
    // 逐项测量：每个描述项的文本宽度与高度，检测逐字竖排
    const metrics = await dlg2.evaluate((el) => {
      const items = Array.from(el.querySelectorAll('.el-descriptions__cell, .el-descriptions__label, .el-descriptions__content'));
      return items.map(n => ({ t: (n.textContent || '').trim().slice(0, 24), w: n.clientWidth, h: n.clientHeight }));
    }).catch(() => []);
    const squeezed = metrics.filter(m => m.w > 0 && m.h > m.w * 1.5 && m.t.length > 3);
    check('P4 固件信息无逐字竖排', squeezed.length === 0, JSON.stringify(squeezed.slice(0, 3)));
    await page.screenshot({ path: OUT + '/p4-otaform.png', fullPage: true });
  } else {
    check('P4 OTA 按钮可点', false, 'button missing/disabled');
  }

  const fatal = errs.filter(e => !/favicon|ResizeObserver|Failed to load resource/i.test(e));
  check('无致命 console 错误', fatal.length === 0, fatal.slice(0, 2).join(' | '));
} catch (e) {
  check('脚本执行完成', false, String(e));
} finally { await browser.close(); }
const p = res.filter(r => r.ok).length, f = res.length - p;
fs.writeFileSync(OUT + '/result.json', JSON.stringify({ pass: p, fail: f, res }, null, 2));
console.log('== SUMMARY pass=' + p + ' fail=' + f + ' ==');
process.exit(f ? 1 : 0);
