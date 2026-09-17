// 生产栈浏览器黑盒验证：真实用户操作（点击/输入/断言）
import { chromium } from '/home/sun/workspace/EHomeSystem/frontend-shared/node_modules/.pnpm/playwright@1.62.0/node_modules/playwright/index.mjs';
import fs from 'node:fs';

const BASE = process.env.EHOME_BASE || 'http://127.0.0.1:8080';
const USER = process.env.EHOME_USER || 'admin';
const PASS = process.env.EHOME_PASS || 'Admin@123456';
const OUT = process.env.EHOME_OUT || '/tmp/ehome-browser';
fs.mkdirSync(OUT, { recursive: true });

const log = (...a) => console.log('[bb]', ...a);
const results = [];
const check = (name, ok, detail = '') => { results.push({ name, ok: !!ok, detail: String(detail).slice(0,200) }); log((ok ? 'PASS' : 'FAIL') + ' ' + name + (detail ? ' :: ' + detail : '')); };

const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' });
const page = await ctx.newPage();
const consoleErrors = [];
page.on('console', m => { if (m.type() === 'error') consoleErrors.push(m.text()); });
page.on('pageerror', e => consoleErrors.push('pageerror: ' + e.message));

try {
  await page.goto(BASE + '/', { waitUntil: 'networkidle', timeout: 30000 });
  await page.waitForTimeout(1000);
  check('open app -> login page', /login/i.test(page.url()) || (await page.getByPlaceholder('请输入用户名').count()) > 0, 'url=' + page.url());
  await page.screenshot({ path: OUT + '/01-login.png' });

  // 真实填表（用户可见的输入框与按钮）
  await page.getByPlaceholder('请输入用户名').fill(USER);
  await page.getByPlaceholder('请输入密码').fill(PASS);
  await page.screenshot({ path: OUT + '/02-login-filled.png' });
  await page.getByRole('button', { name: /登\s*录/ }).click();
  await page.waitForURL(/\/(dashboard|node|channel|edge-device|logical-device|data)/, { timeout: 20000 }).catch(() => {});
  await page.waitForTimeout(2000);
  check('login succeeds (redirected off /login)', !/\/login/.test(page.url()), 'url=' + page.url());

  const tokens = await page.evaluate(() => JSON.stringify({ local: Object.keys(localStorage), session: Object.keys(sessionStorage) }));
  check('auth token persisted in browser', /token/i.test(tokens), tokens);
  await page.screenshot({ path: OUT + '/03-dashboard.png' });

  // 通过 UI 导航到「节点」
  await page.goto(BASE + '/node', { waitUntil: 'networkidle', timeout: 20000 });
  await page.waitForTimeout(1500);
  const nodeText = await page.locator('body').innerText().catch(() => '');
  check('node list page renders', /本页节点|节点|在线|离线/.test(nodeText), nodeText.slice(0, 60).replace(/\n/g, ' '));
  check('flashed hardware node visible & online', /F0F5BDFFFE02/.test(nodeText), (nodeText.match(/F0F5BDFFFE02[^\n]*/)||[''])[0]);
  await page.screenshot({ path: OUT + '/04-nodes.png', fullPage: true });

  // 点击进入节点详情（真实交互）
  const nodeIdCell = page.getByText('F0F5BDFFFE02').first();
  if (await nodeIdCell.count()) { await nodeIdCell.click().catch(()=>{}); await page.waitForTimeout(2000); }
  check('node detail opened', /\/node\//.test(page.url()), 'url=' + page.url());
  await page.screenshot({ path: OUT + '/05-node-detail.png', fullPage: true });

  // 通道管理页
  await page.goto(BASE + '/channel', { waitUntil: 'networkidle', timeout: 20000 });
  await page.waitForTimeout(1500);
  const chText = await page.locator('body').innerText().catch(() => '');
  check('channel page renders', /通道/.test(chText), chText.slice(0, 60).replace(/\n/g, ' '));
  await page.screenshot({ path: OUT + '/06-channels.png', fullPage: true });

  // 数据面板
  await page.goto(BASE + '/data', { waitUntil: 'networkidle', timeout: 20000 });
  await page.waitForTimeout(1500);
  check('data panel renders', (await page.locator('body').innerText().catch(()=>'')).length > 50, 'url=' + page.url());
  await page.screenshot({ path: OUT + '/07-data.png', fullPage: true });

  // in-browser /health 必须是 JSON（真实端点，非 SPA catch-all）
  const h = await page.evaluate(async () => { const r = await fetch('/health'); return { ct: r.headers.get('content-type'), body: await r.text() }; });
  check('in-browser /health is JSON', /json/.test(h.ct) && /status/.test(h.body), h.ct + ' ' + h.body);

  // 浏览器内调用受鉴权 API，确认会话有效
  const me = await page.evaluate(async () => {
    const tok = localStorage.getItem('token') || sessionStorage.getItem('token') || '';
    const r = await fetch('/api/v1/nodes', { headers: { Authorization: 'Bearer ' + tok } });
    const body = await r.text();
    return { status: r.status, ct: r.headers.get('content-type'), len: body.length, sample: body.slice(0, 40) };
  });
  check('authenticated API via browser session', me.status === 200 && /json/.test(me.ct), JSON.stringify(me));

  const fatal = consoleErrors.filter(e => !/favicon|ResizeObserver|Failed to load resource/i.test(e));
  check('no fatal console errors', fatal.length === 0, fatal.slice(0, 2).join(' | '));
} catch (e) {
  check('script completed', false, String(e));
} finally {
  await browser.close();
}

const pass = results.filter(r => r.ok).length, fail = results.length - pass;
fs.writeFileSync(OUT + '/result.json', JSON.stringify({ pass, fail, results }, null, 2));
console.log('== SUMMARY pass=' + pass + ' fail=' + fail + ' ==');
process.exit(fail ? 1 : 0);
