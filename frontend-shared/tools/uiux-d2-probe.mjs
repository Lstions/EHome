/**
 * D2 域定向取证探针（资源列表与详情页）。
 * 只读：不改任何 src 文件，不动数据库。
 * 覆盖契约 §5 的 D1/D5/D6 侧重问题 + 本域必答 8 问。
 *
 *   UIUX_OUT=/tmp/uiux-d2-probe node tools/uiux-d2-probe.mjs
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

const BASE = process.env.UIUX_BASE || 'http://127.0.0.1:8082';
const OUT = process.env.UIUX_OUT || '/tmp/uiux-d2-probe';
const USER = 'admin';
const PASS = 'UiuxAudit2026!';
const CHROME = process.env.UIUX_CHROME || '/snap/bin/chromium';
fs.mkdirSync(OUT, { recursive: true });

const THEME = process.env.UIUX_THEME || 'light';
const VP = { width: Number(process.env.UIUX_W || 390), height: Number(process.env.UIUX_H || 844), name: process.env.UIUX_VPNAME || 'mobile-390' };
const PATHS = (process.env.UIUX_PATHS || '/node,/channel,/edge-device,/logical-device').split(',').map(s => s.trim());

// ---------- 页内度量（全部返回 DOM 事实） ----------
const MEASURE = () => {
  const q = s => document.querySelector(s);
  const qa = s => [...document.querySelectorAll(s)];
  const R = el => { if (!el) return null; const r = el.getBoundingClientRect(); return { x: Math.round(r.x), y: Math.round(r.y), w: Math.round(r.width), h: Math.round(r.height), right: Math.round(r.right), bottom: Math.round(r.bottom) }; };
  const CS = (el, props) => { if (!el) return null; const c = getComputedStyle(el); const o = {}; props.forEach(p => o[p] = c[p]); return o; };
  const txt = el => el ? (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 50) : null;

  // --- 容器套娃：沿祖先链统计「有背景/边框的容器」层数 ---
  const chainOf = el => {
    const out = []; let n = el;
    while (n && n !== document.documentElement) {
      const c = getComputedStyle(n);
      const isCardLike = (c.backgroundColor !== 'rgba(0, 0, 0, 0)' && c.backgroundColor !== 'transparent')
        || (c.borderTopWidth !== '0px' && c.borderTopStyle !== 'none')
        || (c.boxShadow !== 'none');
      if (isCardLike) out.push({
        tag: n.tagName.toLowerCase(),
        cls: (typeof n.className === 'string' ? n.className : '').trim().split(/\s+/).slice(0, 3).join('.'),
        bg: c.backgroundColor, radius: c.borderRadius, border: c.borderTopWidth, shadow: c.boxShadow.slice(0, 40),
      });
      n = n.parentElement;
    }
    return out;
  };
  // 最深的一条「容器链」（只取页面主内容区内的首张行卡片/行元素）
  const rowCard = q('.collector-card') || q('.device-card') || q('.config-card') || q('.el-table__row');
  const nesting = rowCard ? { sample: txt(rowCard), depth: chainOf(rowCard).length, chain: chainOf(rowCard).slice(0, 8) } : null;

  // --- 页面标题是否存在 ---
  const titleEl = q('h1') || q('.page-title') || q('.el-page-header__content') || q('[class*="page-header"]') || q('h2');

  // --- 表格 / 横滚 ---
  const tables = qa('.el-table').map(t => {
    const inner = t.querySelector('.el-table__inner-wrapper');
    const body = t.querySelector('.el-table__body-wrapper') || t.querySelector('.el-scrollbar__wrap');
    const r = t.getBoundingClientRect();
    return {
      w: Math.round(r.width),
      right: Math.round(r.right),
      innerScrollW: inner ? inner.scrollWidth : null,
      innerClientW: inner ? inner.clientWidth : null,
      bodyOverflowX: body ? getComputedStyle(body).overflowX : null,
      bodyScrollW: body ? body.scrollWidth : null,
      bodyClientW: body ? body.clientWidth : null,
      wrapped: !!t.closest('.mobile-table-wrapper'),
      hint: !!t.closest('.mobile-table-wrapper')?.querySelector('.mobile-table-hint'),
      hasFixedRight: !!t.querySelector('.el-table__fixed-right, .el-table-fixed-column--right'),
    };
  });

  // --- 操作列可达性（fixed-right 列 / 最后一个 td）---
  const actionCells = qa('.el-table__row').map(tr => {
    const tds = [...tr.querySelectorAll('td')];
    if (!tds.length) return null;
    const fixed = tr.querySelector('td.el-table-fixed-column--right') || tds[tds.length - 1];
    const btns = [...fixed.querySelectorAll('button, .el-button')].map(b => ({ t: txt(b), ...R(b) }));
    return { cellClass: (fixed.className || '').toString().slice(0, 90), cell: R(fixed), btns };
  }).filter(Boolean);

  // --- 触控目标：全部可点元素 < 44，并区分 36 阈值 ---
  const SEL = 'button, .el-button, .el-switch, [role="button"], a.el-link, .el-checkbox, .el-radio, .el-tag.closable, .el-pagination button, .el-select';
  const targets = qa(SEL).map(el => {
    const r = R(el);
    if (!r || r.w <= 0 || r.h <= 0) return null;
    const inToolbar = !!el.closest('.toolbar, .filter-bar, .filter-right, .main-header, .header-right, .table-toolbar, .batch-actions');
    const inRow = !!el.closest('.el-table__row, .collector-card, .device-card, .config-card, .card-footer, .card-actions');
    return {
      label: (el.getAttribute('aria-label') || el.getAttribute('title') || el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 26),
      sel: el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\s+/)[0] : ''),
      style: (typeof el.className === 'string' ? el.className : '').slice(0, 60),
      ...r, inToolbar, inRow,
      danger: el.classList.contains('el-button--danger') || el.classList.contains('is-danger'),
    };
  }).filter(Boolean);
  const under36 = targets.filter(t => t.w < 36 || t.h < 36);
  const under44 = targets.filter(t => t.w < 44 || t.h < 44);

  // --- 危险操作（删除类）---
  const deletes = qa('.el-button--danger, [class*="delete"], [aria-label*="删除"]').map(el => ({
    label: (el.getAttribute('aria-label') || el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 26),
    ...R(el), cls: (typeof el.className === 'string' ? el.className : '').slice(0, 70),
  }));

  // --- 状态表达：是否文字 + 颜色 ---
  const statuses = qa('.status-tag, .status-indicator, .status-cell, .status-badge, .el-tag').slice(0, 14).map(el => {
    const c = getComputedStyle(el);
    return { text: txt(el), color: c.color, bg: c.backgroundColor, kind: 'tag/status' };
  });

  // --- 离线说明 / 数据时效 ---
  const bodyText = (document.body.innerText || '').replace(/\s+/g, ' ');
  const offlineMentions = ['数据时效', '最后更新', '离线', '不可操作', '心跳'].filter(k => bodyText.includes(k));

  // --- 字体大小（<=768 输入框必须 >=16px）---
  const inputs = qa('input, textarea, .el-input__inner').map(el => {
    const c = getComputedStyle(el);
    return { ph: el.getAttribute('placeholder') || '', fontSize: c.fontSize, ...R(el) };
  }).filter(i => i.w > 0);

  const de = document.documentElement;
  return {
    path: location.pathname, vp: { w: innerWidth, h: innerHeight },
    docScrollW: de.scrollWidth, docClientW: de.clientWidth,
    pageOverflow: de.scrollWidth - de.clientWidth,
    title: { text: txt(titleEl), tag: titleEl ? titleEl.tagName.toLowerCase() : null, ...R(titleEl) },
    pageHeaderPresent: !!q('[class*="page-header"]'),
    headingEls: qa('h1,h2,h3').slice(0, 10).map(e => ({ tag: e.tagName.toLowerCase(), t: txt(e), fs: getComputedStyle(e).fontSize, ...R(e) })),
    nesting, tables, actionCells,
    targetCount: targets.length, under36Count: under36.length, under44Count: under44.length,
    under36: under36.slice(0, 30), under44: under44.filter(t => !under36.includes(t)).slice(0, 30),
    deletes, statuses, offlineMentions,
    inputsUnder16: inputs.filter(i => parseFloat(i.fontSize) < 16),
    inputCount: inputs.length,
    bodyScrollH: document.body.scrollHeight,
    elCount: document.querySelectorAll('body *').length,
  };
};

const browser = await chromium.launch({ executablePath: CHROME, args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'] });
const ctx = await browser.newContext({ viewport: { width: VP.width, height: VP.height }, locale: 'zh-CN', deviceScaleFactor: 1, hasTouch: true, isMobile: VP.width <= 480, colorScheme: THEME === 'dark' ? 'dark' : 'light' });
const page = await ctx.newPage();
const consoleErrors = [];
page.on('console', m => { if (m.type() === 'error') consoleErrors.push(m.text().slice(0, 200)); });
page.on('pageerror', e => consoleErrors.push('PAGEERROR ' + String(e).slice(0, 200)));

await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.evaluate(t => { localStorage.setItem('theme', t); document.documentElement.setAttribute('data-theme', t); document.documentElement.classList.toggle('dark', t === 'dark'); }, THEME);
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', USER);
await page.fill('input[placeholder="请输入密码"]', PASS);
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
await page.waitForTimeout(2000);

const out = [];
for (const p of PATHS) {
  const rec = { theme: THEME, vp: VP.name, route: p };
  try {
    await page.goto(BASE + p, { waitUntil: 'domcontentloaded', timeout: 25000 });
    await page.waitForTimeout(2200);
    Object.assign(rec, await page.evaluate(MEASURE));
    const f = path.join(OUT, VP.name + '-' + THEME + '-' + p.replace(/\//g, '_') + '.png');
    await page.screenshot({ path: f }); rec.screenshot = f;
  } catch (e) { rec.error = String(e).split('\n')[0].slice(0, 200); }
  out.push(rec);
}
await browser.close();
fs.writeFileSync(path.join(OUT, 'probe-' + VP.name + '-' + THEME + '.json'), JSON.stringify({ out, consoleErrors }, null, 2));
console.log('records=' + out.length + ' errors=' + consoleErrors.length);
for (const r of out) console.log(r.route + ' under36=' + r.under36Count + ' under44=' + r.under44Count + ' tables=' + (r.tables || []).length + ' title=' + JSON.stringify(r.title?.text) + (r.error ? ' ERR ' + r.error : ''));
