// 验证雨量计真实数据在 UI 可见（浏览器黑盒，真实操作）
import { chromium } from '/home/sun/workspace/EHomeSystem/frontend-shared/node_modules/.pnpm/playwright@1.62.0/node_modules/playwright/index.mjs';
import fs from 'node:fs';
const BASE = process.env.EHOME_BASE || 'http://127.0.0.1:8080';
const USER = process.env.EHOME_USER || 'admin';
const PASS = process.env.EHOME_PASS || 'Admin@123456';
const OUT = '/tmp/ehome-browser';
fs.mkdirSync(OUT, { recursive: true });
const res = [];
const check = (n, ok, d = '') => { res.push({ n, ok: !!ok, d: String(d).slice(0, 220) }); console.log((ok ? 'PASS ' : 'FAIL ') + n + (d ? ' :: ' + d : '')); };

const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' }).then(c => c.newPage());
try {
  await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
  await page.getByPlaceholder('请输入用户名').fill(USER);
  await page.getByPlaceholder('请输入密码').fill(PASS);
  await page.getByRole('button', { name: /登\s*录/ }).click();
  await page.waitForURL(/dashboard/, { timeout: 20000 }).catch(() => {});

  // 边缘设备列表
  await page.goto(BASE + '/edge-device', { waitUntil: 'networkidle' });
  await page.waitForTimeout(2500);
  let body = await page.locator('body').innerText();
  check('边缘设备列表显示雨量计', body.includes('测试雨量计'), (body.match(/测试雨量计[^\n]*/) || [''])[0]);
  check('雨量计不再显示错误', !/测试雨量计[\s\S]{0,60}错误/.test(body));
  await page.screenshot({ path: OUT + '/10-edge-devices.png', fullPage: true });

  // 数据面板：查雨量计数据
  await page.goto(BASE + '/data', { waitUntil: 'networkidle' });
  await page.waitForTimeout(2500);
  body = await page.locator('body').innerText();
  check('数据面板可访问', body.length > 50);
  await page.screenshot({ path: OUT + '/11-data-panel.png', fullPage: true });

  // 通过浏览器会话读取该设备的真实数据（与 UI 同源）
  const api = await page.evaluate(async () => {
    const tok = localStorage.getItem('token') || sessionStorage.getItem('token') || '';
    const h = { Authorization: 'Bearer ' + tok };
    const dev = await (await fetch('/api/v1/edge-devices?page=1&page_size=50', { headers: h })).json();
    const rain = (dev.data?.items || dev.data || []).find?.(d => d.type === 'sn301_rain' || d.type === 'sn3001_rain') || null;
    return { rainId: rain?.id, rainName: rain?.name, rainErr: rain?.error_code, rainLast: rain?.last_data_at };
  });
  check('API 返回雨量计且无错误码', api.rainId && api.rainErr === 0, JSON.stringify(api));
} catch (e) {
  check('脚本执行完成', false, String(e));
} finally { await browser.close(); }
const p = res.filter(r => r.ok).length, f = res.length - p;
fs.writeFileSync(OUT + '/rain-result.json', JSON.stringify({ pass: p, fail: f, res }, null, 2));
console.log('== SUMMARY pass=' + p + ' fail=' + f + ' ==');
process.exit(f ? 1 : 0);
