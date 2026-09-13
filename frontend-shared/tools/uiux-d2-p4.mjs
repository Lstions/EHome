/**
 * D2 精确取证 4：编辑字段可见性 / 禁用原因 / 行操作 loading 范围 / 离线说明。
 * 只读，不提交写请求。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';
const BASE = 'http://127.0.0.1:8082';
const OUT = '/tmp/uiux-d2-probe';
const THEME = process.env.UIUX_THEME || 'light';
const W = Number(process.env.UIUX_W || 390), H = Number(process.env.UIUX_H || 844);
const VP = process.env.UIUX_VPNAME || 'mobile-390';

const visibleOnly = () => {
  const q = s => document.querySelector(s), qa = s => [...document.querySelectorAll(s)];
  const T = e => e ? (e.textContent || '').trim().replace(/\s+/g, ' ') : null;
  const R = e => { if (!e) return null; const r = e.getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height) }; };
  const vis = e => !!e && e.offsetParent !== null && e.getBoundingClientRect().width > 0;
  return {
    // 对话框内「可见」的表单标签与控件（offsetParent 非空）
    dialogLabelsVisible: qa('.el-dialog .el-form-item__label').filter(vis).map(T),
    dialogLabelsHidden: qa('.el-dialog .el-form-item__label').filter(e => !vis(e)).map(T),
    dialogInputsVisible: qa('.el-dialog input, .el-dialog textarea').filter(vis).map(e => e.getAttribute('placeholder') || e.value || '(无ph)'),
    dialogStepsVisible: qa('.el-dialog .el-steps').filter(vis).length,
    dialogConfirmCardVisible: qa('.el-dialog .confirm-card').filter(vis).length,
    dialogTitle: T(q('.el-dialog__title')),
    dialogSubmit: qa('.el-dialog__footer button').filter(vis).map(T),
  };
};
const disabledReport = () => {
  const qa = s => [...document.querySelectorAll(s)];
  const T = e => e ? (e.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 30) : null;
  return qa('.el-switch.is-disabled, .el-switch[disabled], button:disabled, .el-button.is-disabled').map(e => {
    let hasTip = false, tipSrc = null, n = e;
    for (let i = 0; i < 5 && n; i++) {
      if (n.getAttribute && (n.getAttribute('title') || n.getAttribute('aria-describedby') || n.getAttribute('aria-label'))) { hasTip = true; tipSrc = n.tagName.toLowerCase() + ':' + (n.getAttribute('title') || n.getAttribute('aria-describedby') || n.getAttribute('aria-label')); break; }
      n = n.parentElement;
    }
    return { tag: e.tagName.toLowerCase(), cls: (e.className || '').toString().slice(0, 60), text: T(e), label: e.getAttribute('aria-label'), hasReasonAttr: hasTip, tipSrc };
  });
};
const loadingScope = async () => null;

const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox'] });
const ctx = await browser.newContext({ viewport: { width: W, height: H }, locale: 'zh-CN', hasTouch: W <= 480, colorScheme: THEME === 'dark' ? 'dark' : 'light' });
const page = await ctx.newPage();
const errs = []; page.on('pageerror', e => errs.push(String(e).slice(0, 160)));
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.evaluate(t => { localStorage.setItem('theme', t); document.documentElement.setAttribute('data-theme', t); document.documentElement.classList.toggle('dark', t === 'dark'); }, THEME);
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', 'admin'); await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await page.waitForTimeout(2000);
const rep = { vp: VP, theme: THEME, steps: [], consoleErrors: errs };
const shot = async n => { const f = path.join(OUT, VP + '-' + THEME + '-p4-' + n + '.png'); await page.screenshot({ path: f }); return path.basename(f); };

// 1) edge-device 编辑弹窗（可见字段）
await page.goto(BASE + '/edge-device', { waitUntil: 'domcontentloaded' }); await page.waitForTimeout(2400);
await page.locator('.device-card .el-button:has-text("编辑")').first().click().catch(e => errs.push('e1:' + e.message));
await page.waitForTimeout(1600);
rep.steps.push({ step: 'edge-device 编辑弹窗 可见字段', shot: await shot('edge-edit'), ...(await page.evaluate(visibleOnly)) });
await page.keyboard.press('Escape'); await page.waitForTimeout(700);

// 2) edge-device 创建弹窗（可见字段，对照）
await page.locator('button:has-text("创建边缘设备")').first().click().catch(e => errs.push('e2:' + e.message));
await page.waitForTimeout(1600);
rep.steps.push({ step: 'edge-device 创建弹窗 step0 可见字段', shot: await shot('edge-create'), ...(await page.evaluate(visibleOnly)) });
await page.keyboard.press('Escape'); await page.waitForTimeout(700);

// 3) channel-list 禁用原因
await page.goto(BASE + '/channel', { waitUntil: 'domcontentloaded' }); await page.waitForTimeout(2300);
rep.steps.push({ step: 'channel-list 禁用控件与原因', shot: await shot('channel-disabled'), disabled: await page.evaluate(disabledReport) });

// 4) 行操作 loading 范围：点刷新，检查 aria-busy / 行是否被锁
await page.goto(BASE + '/node', { waitUntil: 'domcontentloaded' }); await page.waitForTimeout(2400);
await page.locator('button:has-text("刷新")').first().click().catch(() => {});
await page.waitForTimeout(120);
rep.steps.push({ step: 'node-list 刷新中（120ms 快照）', shot: await shot('node-refreshing'), loading: await page.evaluate(() => {
  const qa = s => [...document.querySelectorAll(s)];
  return {
    ariaBusyCount: qa('[aria-busy]').length,
    ariaBusyTrue: qa('[aria-busy="true"]').length,
    loadingButtons: qa('button.is-loading, .el-button.is-loading').map(b => (b.textContent || '').trim().slice(0, 12)),
    cardButtonsDisabled: qa('.collector-card button:disabled').length,
    allCardButtons: qa('.collector-card button').length,
    tableRowsDisabled: qa('.el-table__row button:disabled').length,
  };
}) });
await page.waitForTimeout(2500);

await browser.close();
fs.writeFileSync(path.join(OUT, 'p4-' + VP + '-' + THEME + '.json'), JSON.stringify(rep, null, 2));
console.log('steps=' + rep.steps.length + ' errs=' + errs.length);
