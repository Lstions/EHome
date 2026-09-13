/**
 * 负债 I-11「假分页」DOM 实测探针 (修复执行员自建, 2026-09-13)。
 *
 * 目的: 对 nodes / edge-devices 两个列表页取**真实浏览器 + 真实数据**的 DOM 事实,
 * 并抓取真实网络请求参数。改前/改后各跑一次, 对比:
 *   rowCount / totalEls / pagination 存在性 / 分页器文案里的 total
 *   + 翻页是否发 ?page=2
 *   + 换筛选是否发 ?page=1&...
 *
 * 用法: node tools/i11-pagination-probe.mjs <BASE_URL> <LABEL>
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';

const BASE = process.argv[2] || 'http://127.0.0.1:8099';
const LABEL = process.argv[3] || 'after';
const OUT = '/tmp/i11-probe';
fs.mkdirSync(OUT, { recursive: true });

const FACTS = () => {
  const q = s => document.querySelector(s), qa = s => [...document.querySelectorAll(s)];
  const T = e => e ? (e.textContent || '').trim().replace(/\s+/g, ' ') : null;
  const vis = e => { if (!e) return false; const r = e.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
  // 行数: el-table 数据行 + 卡片数 (两种视图各自的口径)
  const rows = qa('.el-table__row').filter(vis).length;
  const cards = qa('.collector-card, .device-card').filter(vis).length;
  // 分页器: 存在性 + 文案里的 total + 每页条数
  const pagers = qa('.el-pagination').filter(vis);
  const pager = pagers[0];
  const pagerText = T(pager);
  // "共 N 条" 是 el-pagination layout=total 渲染的
  const totalMatch = pagerText && pagerText.match(/共\s*(\d+)\s*条/);
  return {
    rowCount: rows,
    cardCount: cards,
    totalEls: document.querySelectorAll('*').length,
    paginationPresent: pagers.length,
    paginationText: pagerText,
    pagerTotal: totalMatch ? Number(totalMatch[1]) : null,
    pagerPageCount: qa('.el-pager li').filter(vis).length,
    activePage: T(q('.el-pager li.is-active')),
    bodyHead: (document.body.innerText || '').replace(/\s+/g, ' ').slice(0, 200),
  };
};

const browser = await chromium.launch({
  executablePath: '/snap/bin/chromium',
  args: ['--no-sandbox', '--disable-setuid-sandbox'],
});
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN' });
const page = await ctx.newPage();

// 抓取列表请求的真实 query 参数
const listReqs = [];
page.on('request', req => {
  const u = new URL(req.url());
  if (/\/api\/v1\/(nodes|edge-devices)$/.test(u.pathname)) {
    listReqs.push({ path: u.pathname, query: u.search, page: u.searchParams.get('page'), page_size: u.searchParams.get('page_size'), search: u.searchParams.get('search'), status: u.searchParams.get('status'), model: u.searchParams.get('model'), device_type: u.searchParams.get('device_type'), hardware_type: u.searchParams.get('hardware_type') });
  }
});

const report = { base: BASE, label: LABEL, cases: {} };

// 登录
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', 'admin');
await page.fill('input[placeholder="请输入密码"]', 'UiuxAudit2026!');
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});

// ── 用例 1: 节点列表 (表格视图看 .el-table__row) ──
{
  const before = listReqs.length;
  await page.goto(BASE + '/node', { waitUntil: 'domcontentloaded' });
  // 等真实数据: 等到表格行或卡片出现 (不用固定 sleep)
  await page.waitForSelector('.collector-card, .el-table__row, [data-testid="empty-state"]', { timeout: 15000 }).catch(() => {});
  await page.waitForFunction(() => {
    const r = document.querySelectorAll('.el-table__row').length + document.querySelectorAll('.collector-card').length;
    return r > 0;
  }, { timeout: 15000 }).catch(() => {});
  await page.waitForFunction(() => document.querySelector('.el-pagination') !== null, { timeout: 10000 }).catch(() => {});
  const initial = await page.evaluate(FACTS);
  const reqsInitial = listReqs.slice(before);
  report.cases['node-list'] = { view: 'grid(默认)', ...initial, requests: reqsInitial };

  // 切表格视图, 再取一次 (表格行数才是 rowCount 的准确口径)
  const tableBtn = page.locator('button[aria-label="表格视图"]');
  if (await tableBtn.count()) {
    await tableBtn.first().click();
    await page.waitForSelector('.el-table__row', { timeout: 10000 }).catch(() => {});
    report.cases['node-list-table'] = { view: 'table', ...(await page.evaluate(FACTS)) };
  }

  // 翻页: 点第 2 页
  const p2 = page.locator('.el-pager li', { hasText: /^2$/ });
  if (await p2.count()) {
    const mark = listReqs.length;
    await p2.first().click();
    await page.waitForFunction(() => {
      const a = document.querySelector('.el-pager li.is-active');
      return a && a.textContent.trim() === '2';
    }, { timeout: 10000 }).catch(() => {});
    await page.waitForTimeout(400);
    report.cases['node-list-page2'] = {
      ...(await page.evaluate(FACTS)),
      requests: listReqs.slice(mark),
    };
  } else {
    report.cases['node-list-page2'] = { skipped: '只有一页, 无第 2 页按钮' };
  }

  // 换筛选: 搜索框输入 → 必须发 page=1&search=...
  const mark2 = listReqs.length;
  const input = page.locator('.search-input input').first();
  if (await input.count()) {
    await input.fill('esp32');
    await page.waitForTimeout(900);  // 仅用于等防抖+请求落地; 断言靠请求记录
    report.cases['node-list-search'] = {
      ...(await page.evaluate(FACTS)),
      requests: listReqs.slice(mark2),
    };
  }
}

// ── 用例 2: 边缘设备列表 ──
{
  const before = listReqs.length;
  await page.goto(BASE + '/edge-device', { waitUntil: 'domcontentloaded' });
  await page.waitForFunction(() => {
    const r = document.querySelectorAll('.el-table__row').length + document.querySelectorAll('.device-card').length;
    return r > 0;
  }, { timeout: 15000 }).catch(() => {});
  await page.waitForFunction(() => document.querySelector('.el-pagination') !== null, { timeout: 10000 }).catch(() => {});
  report.cases['edge-list'] = { view: 'card(默认)', ...(await page.evaluate(FACTS)), requests: listReqs.slice(before) };

  const tableBtn = page.locator('button[aria-label="表格视图"]');
  if (await tableBtn.count()) {
    await tableBtn.first().click();
    await page.waitForSelector('.el-table__row', { timeout: 10000 }).catch(() => {});
    report.cases['edge-list-table'] = { view: 'table', ...(await page.evaluate(FACTS)) };
  }

  // 翻页 (若有多页)
  const p2 = page.locator('.el-pager li', { hasText: /^2$/ });
  if (await p2.count()) {
    const mark = listReqs.length;
    await p2.first().click();
    await page.waitForFunction(() => {
      const a = document.querySelector('.el-pager li.is-active');
      return a && a.textContent.trim() === '2';
    }, { timeout: 10000 }).catch(() => {});
    await page.waitForTimeout(400);
    report.cases['edge-list-page2'] = { ...(await page.evaluate(FACTS)), requests: listReqs.slice(mark) };
  } else {
    report.cases['edge-list-page2'] = { skipped: '只有一页, 无第 2 页按钮' };
  }
}

await page.screenshot({ path: OUT + '/i11-' + LABEL + '.png', fullPage: false });
fs.writeFileSync(OUT + '/i11-' + LABEL + '.json', JSON.stringify(report, null, 2));
await browser.close();
console.log(JSON.stringify(report, null, 2));

