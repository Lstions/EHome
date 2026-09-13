/**
 * 定向复跑 18：失败是否被伪装成「正常」（主控广播 2 的模式）。
 *  对每个页面：用 page.route 返回 **500**（不是 abort），再取 DOM 事实：
 *   - 是否出现「正常/无异常/暂无」这类正向文案
 *   - KPI/统计卡是否渲染成 0
 *   - 有无重试入口、有无错误态（.empty-state.error / el-alert--error / el-result）
 *   - 有无可见的错误文案（页面级常驻，而非瞬态 ElMessage）
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE = 'http://127.0.0.1:8082';
const sleep = ms => new Promise(r => setTimeout(r, ms));
const out = [];
const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' });
const page = await ctx.newPage();
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', 'admin');
await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await sleep(1500);

const SNAP = () => ({
  emptyStateCount: document.querySelectorAll('.empty-state').length,
  emptyStateKind: [...document.querySelectorAll('.empty-state')].map(e => e.className),
  emptyTitle: (document.querySelector('.empty-title') || {}).textContent || null,
  emptyDesc: (document.querySelector('.empty-description') || {}).textContent || null,
  emptyQuickActions: [...document.querySelectorAll('.empty-quick-actions button')].map(b => (b.textContent || '').trim()),
  errorAlert: document.querySelectorAll('.el-alert--error').length,
  warningAlert: document.querySelectorAll('.el-alert--warning').length,
  errorText: [...document.querySelectorAll('.el-alert__title')].map(t => (t.textContent || '').trim()),
  elResult: document.querySelectorAll('.el-result').length,
  elResultTitle: (document.querySelector('.el-result__title') || {}).textContent || null,
  statCards: [...document.querySelectorAll('.stat-card')].map(c => ({ label: (c.querySelector('.stat-label') || {}).textContent || null, value: (c.querySelector('.stat-value') || {}).textContent || null })),
  tableEmptyText: (document.querySelector('.el-table__empty-text') || {}).textContent || null,
  elMessages: [...document.querySelectorAll('.el-message')].map(m => (m.textContent || '').trim().slice(0, 50)),
  loadingMasks: document.querySelectorAll('.el-loading-mask').length,
  skeletons: document.querySelectorAll('.el-skeleton').length,
  retryButtons: [...document.querySelectorAll('button')].filter(b => /重试|刷新|重新加载/.test(b.textContent || '')).map(b => (b.textContent || '').trim().slice(0, 12)),
  pagination: document.querySelectorAll('.el-pagination').length,
  tableRows: document.querySelectorAll('.el-table__row').length,
  bodyTextSample: (document.querySelector('.main-content') || document.body).textContent.trim().replace(/\s+/g, ' ').slice(0, 260),
});

const CASES = [
  ['automation-rules-500', '/automation', '**/api/v1/automation-rules*'],
  ['automation-events-500', '/automation', '**/api/v1/automation-events*'],
  ['alerts-rules-500', '/alerts', '**/api/v1/alert-rules*'],
  ['alerts-events-500', '/alerts', '**/api/v1/alert-events*'],
  ['device-configs-500', '/device-configs', '**/api/v1/device-configs*'],
  ['data-sources-500', '/data-sources', '**/api/v1/data-sources*'],
  ['data-sources-devices-500', '/data-sources', '**/api/v1/logical-devices*'],
  ['firmware-500', '/firmware', '**/api/v1/firmwares*'],
  ['automation-devices-500', '/automation', '**/api/v1/edge-devices*'],
];
for (const [nm, route, pat] of CASES) {
  try {
    await page.goto(BASE + route, { waitUntil: 'domcontentloaded' });
    await sleep(1200);
    await page.route(pat, r => { try { r.fulfill({ status: 500, contentType: 'application/json', body: JSON.stringify({ code: 500, data: null, message: 'audit injected 500' }) }); } catch { /* */ } });
    await page.reload({ waitUntil: 'domcontentloaded' });
    await sleep(3000);
    const s = await page.evaluate(SNAP);
    const sh = '/tmp/uiux-d3f/err500-' + nm + '.png';
    await page.screenshot({ path: sh });
    out.push({ case: nm, route, pattern: pat, screenshot: sh, ...s });
    await page.unroute(pat);
  } catch (e) { out.push({ case: nm, error: String(e).slice(0, 160) }); }
}
await browser.close();
fs.writeFileSync('/tmp/uiux-d3f/d3p-facts.json', JSON.stringify(out, null, 2));
for (const r of out) { console.log('===== ' + r.case + ' ====='); console.log('  emptyState=' + r.emptyStateCount + ' ' + JSON.stringify(r.emptyStateKind) + ' title=' + JSON.stringify(r.emptyTitle) + ' desc=' + JSON.stringify(r.emptyDesc));
  console.log('  errorAlert=' + r.errorAlert + ' warnAlert=' + r.warningAlert + ' elResult=' + r.elResult + ' retry=' + JSON.stringify(r.retryButtons) + ' msgs=' + JSON.stringify(r.elMessages));
  console.log('  statCards=' + JSON.stringify(r.statCards) + ' tableEmpty=' + JSON.stringify(r.tableEmptyText) + ' rows=' + r.tableRows);
  console.log('  quickActions=' + JSON.stringify(r.emptyQuickActions));
  console.log('  body=' + JSON.stringify((r.bodyTextSample || '').slice(0, 200))); }