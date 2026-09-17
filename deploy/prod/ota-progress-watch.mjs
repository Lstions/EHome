// 观察真实 OTA 任务的进度条渲染（浏览器轮询同款接口 + 截图）
import { chromium } from '/home/sun/workspace/EHomeSystem/frontend-shared/node_modules/.pnpm/playwright@1.62.0/node_modules/playwright/index.mjs';
import fs from 'node:fs';
const BASE = 'http://127.0.0.1:8080';
const NODE = 'F0F5BDFFFE02';
const OUT = '/tmp/ehome-ota2';
fs.mkdirSync(OUT, { recursive: true });
const log = (...a) => console.log('[ota]', ...a);
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-dev-shm-usage'] });
const page = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' }).then(c => c.newPage());
try {
  await page.goto(BASE + '/login', { waitUntil: 'networkidle' });
  await page.getByPlaceholder('请输入用户名').fill('admin');
  await page.getByPlaceholder('请输入密码').fill('Admin@123456');
  await page.getByRole('button', { name: /登\s*录/ }).click();
  await page.waitForURL(/dashboard/, { timeout: 20000 }).catch(() => {});
  // 打开 OTA 历史，看该任务的进度显示
  await page.goto(BASE + '/node/' + NODE, { waitUntil: 'networkidle' });
  await page.waitForTimeout(2000);
  const hist = page.getByText(/OTA 历史/).first();
  if (await hist.count()) { await hist.click(); await page.waitForTimeout(2000); }
  await page.screenshot({ path: OUT + '/01-ota-history.png', fullPage: true });
  // 高频采样 OTA 状态（与前端轮询同源）
  const seen = [];
  for (let i = 0; i < 40; i++) {
    const s = await page.evaluate(async () => {
      const tok = localStorage.getItem('token') || sessionStorage.getItem('token') || '';
      const r = await fetch('/api/v1/ota/tasks/3', { headers: { Authorization: 'Bearer ' + tok } });
      const j = await r.json().catch(() => null);
      return j && j.data ? { status: j.data.status, progress: j.data.progress, to: j.data.to_version, err: j.data.error_msg } : null;
    });
    if (s) {
      const key = s.status + ':' + s.progress;
      if (!seen.length || seen[seen.length - 1].key !== key) {
        seen.push({ t: i * 2, key, ...s });
        log('t=' + (i * 2) + 's status=' + s.status + ' progress=' + s.progress + '% to=' + s.to + (s.err ? ' err=' + s.err : ''));
        await page.screenshot({ path: OUT + '/p-' + String(seen.length).padStart(2, '0') + '.png', fullPage: true });
      }
    }
    if (s && ['success', 'failed'].includes(s.status)) break;
    await page.waitForTimeout(2000);
  }
  fs.writeFileSync(OUT + '/samples.json', JSON.stringify(seen, null, 2));
  log('采样点:', seen.length);
  const body = await page.locator('body').innerText();
  log('页面含进度条文案:', /升级中|升级成功|升级失败|下载中|校验/.test(body));
  const m = body.match(/(升级中|下载中|校验中|升级成功|升级失败)[^\n]{0,20}/g);
  log('页面进度文案:', JSON.stringify(m));
} catch (e) { log('ERROR ' + String(e)); }
finally { await browser.close(); }
