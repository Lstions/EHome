/**
 * D3/D5 域定向取证探针（审计用，不改动 src/）。
 *
 * 权威依据：docs/规范/前端开发与UIUX设计规范.md §5.2 + docs/分析/UIUX审计契约-2026-09-13.md §6
 * 只对主控全量基线之外的疑点做定向复跑：不重复跑全量矩阵。
 *
 * 用法：
 *   UIUX_OUT=/tmp/uiux-d3 UIUX_ROUTES=automation,... UIUX_VIEWPORTS=... UIUX_THEMES=... \
 *     node tools/uiux-d3-probe.mjs
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

const BASE = process.env.UIUX_BASE || 'http://127.0.0.1:8082';
const OUT = process.env.UIUX_OUT || '/tmp/uiux-d3';
const USER = process.env.UIUX_USER || 'admin';
const PASS = process.env.UIUX_PASS || 'UiuxAudit2026!';
const CHROME = process.env.UIUX_CHROME || '/snap/bin/chromium';
const ALL_ROUTES = {
  'automation': '/automation', 'alerts': '/alerts', 'device-configs': '/device-configs',
  'data-sources': '/data-sources', 'firmware': '/firmware',
};
const ONLY = (process.env.UIUX_ROUTES || '').split(',').map(s => s.trim()).filter(Boolean);
const ROUTES = ONLY.length ? ONLY.map(n => [n, ALL_ROUTES[n]]).filter(([, r]) => r) : Object.entries(ALL_ROUTES);
const ALL_VP = [
  { name: 'desktop-1440', width: 1440, height: 900 },
  { name: 'laptop-1024', width: 1024, height: 768 },
  { name: 'tablet-768', width: 768, height: 1024 },
  { name: 'mobile-390', width: 390, height: 844 },
  { name: 'mobile-360', width: 360, height: 800 },
];
const VP_ONLY = (process.env.UIUX_VIEWPORTS || '').split(',').map(s => s.trim()).filter(Boolean);
const VIEWPORTS = VP_ONLY.length ? ALL_VP.filter(v => VP_ONLY.includes(v.name)) : ALL_VP;
const THEMES = (process.env.UIUX_THEMES || 'light,dark').split(',').map(s => s.trim()).filter(Boolean);
fs.mkdirSync(OUT, { recursive: true });
const sleep = ms => new Promise(r => setTimeout(r, ms));

/** 页面基础度量：全部为可复核的 DOM 原始值。 */
const MEASURE = () => {
  const de = document.documentElement;
  const q = s => document.querySelector(s);
  const qa = s => [...document.querySelectorAll(s)];
  const cs = el => el ? getComputedStyle(el) : null;
  const box = el => { if (!el) return null; const r = el.getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height), x: Math.round(r.x), y: Math.round(r.y), right: Math.round(r.right), bottom: Math.round(r.bottom) }; };
  const txt = el => el ? (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 80) : null;

  // 小点击目标：按 选择器+尺寸 聚合，给出每个具体元素的标签
  const small = [];
  qa('button, .el-button, .el-switch, [role="button"], a.el-link, .el-checkbox, .el-dropdown').forEach(el => {
    const r = el.getBoundingClientRect();
    if (r.width > 0 && r.height > 0 && (r.width < 36 || r.height < 36)) {
      small.push({
        label: (el.getAttribute('aria-label') || el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 20),
        w: Math.round(r.width), h: Math.round(r.height),
        sel: el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className ? '.' + el.className.trim().split(/\s+/).slice(0, 2).join('.') : ''),
        pe: getComputedStyle(el).pointerEvents,
        disabled: el.disabled === true || el.getAttribute('aria-disabled') === 'true' || (el.classList && el.classList.contains('is-disabled')),
        title: el.getAttribute('title'),
      });
    }
  });
  const groups = {};
  for (const s of small) { const k = s.sel + '|' + s.w + 'x' + s.h; groups[k] = (groups[k] || 0) + 1; }

  // 禁用控件：是否有可接收 hover 的外层元素（title/aria-label 能弹出来）
  const disabledCtl = qa('button[disabled], .el-button.is-disabled, [disabled]').slice(0, 12).map(el => {
    let n = el, hover = 'SELF', chain = [];
    for (let i = 0; i < 4 && n; i++) {
      const st = getComputedStyle(n);
      chain.push(n.tagName.toLowerCase() + '.' + String(n.className || '').trim().split(/\s+/).slice(0, 2).join('.') + ':pe=' + st.pointerEvents);
      n = n.parentElement;
    }
    return { text: txt(el), title: el.getAttribute('title'), ariaLabel: el.getAttribute('aria-label'), pe: getComputedStyle(el).pointerEvents, chain };
  });

  // 真实裁切：报第一个 overflow 非 visible 的祖先的**完整链条**
  const clipped = [];
  qa('body *').forEach(el => {
    const r = el.getBoundingClientRect();
    if (r.width <= 0) return;
    let anc = el.parentElement, chain = [];
    while (anc && anc !== document.body) {
      const acs = getComputedStyle(anc);
      const info = { tag: anc.tagName.toLowerCase(), cls: String(anc.className || '').trim().split(/\s+/).slice(0, 3).join('.'),
        ox: acs.overflowX, oy: acs.overflowY, cw: anc.clientWidth, sw: anc.scrollWidth, box: box(anc) };
      chain.push(info);
      if (acs.overflowX !== 'visible' || acs.overflowY !== 'visible') {
        const ar = anc.getBoundingClientRect();
        const scrollableX = anc.scrollWidth > anc.clientWidth + 2 && (acs.overflowX === 'auto' || acs.overflowX === 'scroll' || acs.overflowX === 'hidden');
        if (!scrollableX && (r.right > ar.right + 2 || r.left < ar.left - 2)) {
          if (clipped.length < 6) clipped.push({ tag: el.tagName.toLowerCase(), cls: String(el.className || '').trim().split(/\s+/).slice(0, 2).join('.'),
            text: txt(el), self: box(el), overBy: Math.round(Math.max(r.right - ar.right, ar.left - r.left)), scrollableX: false, chain });
        }
        break;
      }
      anc = anc.parentElement;
    }
  });

  // 表格横滚容器链（用于区分「正常横滚」与「真实裁切」）
  const tableRoll = qa('.el-table__body-wrapper, .el-table__header-wrapper, .el-scrollbar__wrap').slice(0, 4).map(el => ({
    cls: String(el.className || '').trim().split(/\s+/).slice(0, 2).join('.'),
    ox: getComputedStyle(el).overflowX, cw: el.clientWidth, sw: el.scrollWidth, box: box(el), rollable: el.scrollWidth > el.clientWidth,
  }));

  return {
    path: location.pathname, heading: txt(q('h1') || q('.page-title') || q('h2')),
    pageHeaderPresent: !!q('[class*="page-header"]'), pageHeaderText: txt(q('[class*="page-header"]')),
    docOverflow: de.scrollWidth - de.clientWidth, docScrollW: de.scrollWidth, docClientW: de.clientWidth,
    docScrollH: de.scrollHeight, bodyScrollH: document.body.scrollHeight, viewportH: window.innerHeight,
    totalEls: document.querySelectorAll('body *').length,
    rowCount: qa('.el-table__row').length, tableCount: qa('.el-table').length,
    pagination: qa('.el-pagination').length, emptyState: qa('.el-empty, .empty-state').length,
    emptyText: txt(q('.el-empty__description') || q('.empty-description')),
    skeleton: qa('.el-skeleton, [class*="skeleton"]').length,
    loadingMask: qa('.el-loading-mask').length,
    cardCount: qa('.el-card').length, mobileTableWrapper: qa('.mobile-table-wrapper').length,
    mobileTableHint: qa('.mobile-table-hint').length,
    smallCount: small.length, smallGroups: groups, smallSamples: small.slice(0, 14),
    clippedCount: clipped.length, clipped,
    disabledCtl, tableRoll,
    rootFont: getComputedStyle(de).fontFamily.slice(0, 60),
    bodyFont: getComputedStyle(document.body).fontFamily.slice(0, 60),
    firstCellFont: (() => { const c = q('.el-table__row td'); return c ? getComputedStyle(c).fontFamily.slice(0, 60) : null; })(),
    inputFont: (() => { const i = q('.el-input__inner'); return i ? getComputedStyle(i).fontSize : null; })(),
    imgBroken: qa('img').filter(i => i.complete && i.naturalWidth === 0).length,
  };
};

const browser = await chromium.launch({ executablePath: CHROME, args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none', '--enable-precise-memory-info'] });
const facts = { base: BASE, results: [], scenarios: [], consoleErrors: [] };

async function login(page) {
  await page.waitForSelector('input[placeholder="请输入用户名"]', { timeout: 20000 });
  await page.fill('input[placeholder="请输入用户名"]', USER);
  await page.fill('input[placeholder="请输入密码"]', PASS);
  await page.click('button:has-text("登")');
  await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
  await sleep(1500);
}

for (const theme of THEMES) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', deviceScaleFactor: 1, colorScheme: theme === 'dark' ? 'dark' : 'light' });
  const page = await ctx.newPage();
  page.on('console', m => { if (m.type() === 'error') facts.consoleErrors.push({ theme, text: m.text().slice(0, 200) }); });
  page.on('pageerror', e => facts.consoleErrors.push({ theme, text: 'PAGEERROR ' + String(e).slice(0, 200) }));
  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded', timeout: 30000 });
  await page.evaluate(t => { localStorage.setItem('theme', t); document.documentElement.setAttribute('data-theme', t); document.documentElement.classList.toggle('dark', t === 'dark'); }, theme);
  await login(page);

  for (const vp of VIEWPORTS) {
    await page.setViewportSize({ width: vp.width, height: vp.height });
    for (const [name, route] of ROUTES) {
      const rec = { theme, vp: vp.name, name, route };
      try {
        await page.goto(BASE + route, { waitUntil: 'domcontentloaded', timeout: 25000 });
        await sleep(1800);
        Object.assign(rec, await page.evaluate(MEASURE));
        const shot = path.join(OUT, vp.name + '-' + theme + '-' + name + '.png');
        await page.screenshot({ path: shot });
        rec.screenshot = shot;
      } catch (e) { rec.error = String(e).split('\n')[0].slice(0, 160); }
      facts.results.push(rec);
    }
  }

  // ── 定向场景：只在 1440 与 360 两档做 ──
  for (const vpname of ['desktop-1440', 'mobile-360']) {
    const vp = ALL_VP.find(v => v.name === vpname);
    await page.setViewportSize({ width: vp.width, height: vp.height });

    // S1 automation：事件表「规则」列 + 内存/高度 + 分页缺失
    try {
      await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
      await sleep(2200);
      const s1 = await page.evaluate(() => {
        const tables = [...document.querySelectorAll('.el-table')];
        const evTable = tables[1];
        const firstRow = evTable ? evTable.querySelector('.el-table__row') : null;
        const cells = firstRow ? [...firstRow.querySelectorAll('td')].map(td => ({ text: (td.textContent || '').trim().slice(0, 24), w: Math.round(td.getBoundingClientRect().width) })) : [];
        const ruleCol = evTable ? evTable.querySelector('.el-table__header th:nth-child(2)') : null;
        const evRows = document.querySelectorAll('.el-table__row').length;
        const heap = performance.memory ? performance.memory.usedJSHeapSize : null;
        return {
          tables: tables.length,
          eventsTableRows: evTable ? evTable.querySelectorAll('.el-table__row').length : 0,
          rulesTableRows: tables[0] ? tables[0].querySelectorAll('.el-table__row').length : 0,
          firstRowCells: cells,
          ruleHeader: ruleCol ? (ruleCol.textContent || '').trim() : null,
          distinctRuleCellTexts: [...new Set([...(evTable ? evTable.querySelectorAll('.el-table__row td:nth-child(2)') : [])].map(td => (td.textContent || '').trim()))].slice(0, 10),
          docScrollH: document.documentElement.scrollHeight,
          bodyScrollH: document.body.scrollHeight,
          elCount: document.querySelectorAll('body *').length,
          usedHeapMB: heap ? Math.round(heap / 1048576 * 10) / 10 : null,
          pagination: document.querySelectorAll('.el-pagination').length,
          inlineStyled: document.querySelectorAll('[style]:not([style=""])').length,
          ruleNames: [...document.querySelectorAll('.el-table')][0] ? [...document.querySelectorAll('.el-table')][0].querySelectorAll('.el-table__row td:nth-child(1)').length : 0,
        };
      });
      facts.scenarios.push({ theme, vp: vpname, name: 'automation-events-rule-col', vpSize: { w: vp.width, h: vp.height }, ...s1 });

      // 内存/高度随停留时间的增长（0s 已测，再等 20s）
      const t0 = await page.evaluate(() => ({ h: document.documentElement.scrollHeight, mem: performance.memory ? performance.memory.usedJSHeapSize : null, els: document.querySelectorAll('body *').length }));
      await sleep(20000);
      const t1 = await page.evaluate(() => ({ h: document.documentElement.scrollHeight, mem: performance.memory ? performance.memory.usedJSHeapSize : null, els: document.querySelectorAll('body *').length }));
      facts.scenarios.push({ theme, vp: vpname, name: 'automation-soak-20s', t0: { h: t0.h, els: t0.els, memMB: t0.mem ? Math.round(t0.mem / 1048576 * 10) / 10 : null }, t1: { h: t1.h, els: t1.els, memMB: t1.mem ? Math.round(t1.mem / 1048576 * 10) / 10 : null } });
    } catch (e) { facts.scenarios.push({ theme, vp: vpname, name: 'automation-events-rule-col', error: String(e).slice(0, 160) }); }

    // S2 automation：创建规则对话框 92vw / footer 可见 / 默认焦点
    try {
      await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
      await sleep(1600);
      await page.click('[data-test="create-rule"]');
      await sleep(700);
      const s2 = await page.evaluate(() => {
        const d = document.querySelector('.el-dialog');
        const f = document.querySelector('.el-dialog__footer');
        const b = document.querySelector('.el-dialog__body');
        const r = el => { if (!el) return null; const x = el.getBoundingClientRect(); return { w: Math.round(x.width), h: Math.round(x.height), top: Math.round(x.top), bottom: Math.round(x.bottom), left: Math.round(x.left), right: Math.round(x.right) }; };
        return {
          dialog: r(d), footer: r(f), body: r(b),
          dialogCssMaxW: d ? getComputedStyle(d).maxWidth : null,
          footerInViewport: f ? f.getBoundingClientRect().bottom <= window.innerHeight + 1 : null,
          footerVisible: f ? getComputedStyle(f).display !== 'none' && f.getBoundingClientRect().height > 0 : null,
          bodyScrollable: b ? b.scrollHeight > b.clientHeight : null,
          bodyScrollH: b ? b.scrollHeight : null, bodyClientH: b ? b.clientHeight : null,
          activeEl: document.activeElement ? document.activeElement.tagName + '.' + String(document.activeElement.className || '').split(/\s+/)[0] : null,
          saveBtnType: (() => { const s = document.querySelector('[data-test="save-rule"]'); return s ? s.className : null; })(),
          cancelBtnType: (() => { const s = [...document.querySelectorAll('.el-dialog__footer button')][0]; return s ? s.className : null; })(),
        };
      });
      const shot = path.join(OUT, 'scenario-' + vpname + '-' + theme + '-automation-create-dialog.png');
      await page.screenshot({ path: shot });
      facts.scenarios.push({ theme, vp: vpname, name: 'automation-create-dialog', screenshot: shot, ...s2 });
      await page.keyboard.press('Escape'); await sleep(400);
    } catch (e) { facts.scenarios.push({ theme, vp: vpname, name: 'automation-create-dialog', error: String(e).slice(0, 160) }); }

    // S3 automation：删除确认（危险确认是否 danger 按钮 + 默认焦点）
    try {
      await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
      await sleep(1600);
      await page.click('.el-table__row .el-button--danger');
      await sleep(700);
      const s3 = await page.evaluate(() => {
        const mb = document.querySelector('.el-message-box');
        const btns = [...document.querySelectorAll('.el-message-box__btns button')].map(b => ({ text: (b.textContent || '').trim(), cls: b.className, focused: document.activeElement === b, pe: getComputedStyle(b).pointerEvents }));
        return { message: (document.querySelector('.el-message-box__message') || {}).textContent || null, buttons: btns, type: mb ? mb.className : null, activeEl: document.activeElement ? document.activeElement.tagName + ':' + (document.activeElement.textContent || '').trim().slice(0, 10) : null };
      });
      const shot = path.join(OUT, 'scenario-' + vpname + '-' + theme + '-automation-delete-confirm.png');
      await page.screenshot({ path: shot });
      facts.scenarios.push({ theme, vp: vpname, name: 'automation-delete-confirm', screenshot: shot, ...s3 });
      await page.keyboard.press('Escape'); await sleep(400);
    } catch (e) { facts.scenarios.push({ theme, vp: vpname, name: 'automation-delete-confirm', error: String(e).slice(0, 160) }); }

    // S4 device-configs 360：导入按钮裁切链
    try {
      await page.goto(BASE + '/device-configs', { waitUntil: 'domcontentloaded' });
      await sleep(1800);
      const s4 = await page.evaluate(() => {
        const chainOf = el => { const chain = []; let n = el; while (n && n !== document.documentElement) { const st = getComputedStyle(n); const r = n.getBoundingClientRect(); chain.push({ tag: n.tagName.toLowerCase(), cls: String(n.className || '').trim().split(/\s+/).slice(0, 3).join('.'), ox: st.overflowX, oy: st.overflowY, w: Math.round(r.width), left: Math.round(r.left), right: Math.round(r.right), cw: n.clientWidth, sw: n.scrollWidth, scrollLeft: n.scrollLeft, flexWrap: st.flexWrap, minWidth: st.minWidth, display: st.display }); n = n.parentElement; } return chain; };
        const btns = [...document.querySelectorAll('button')];
        const imp = btns.find(b => (b.textContent || '').trim() === '导入');
        const exp = btns.find(b => (b.textContent || '').trim() === '导出');
        const neu = btns.find(b => (b.textContent || '').trim() === '新建模板');
        const rect = el => { if (!el) return null; const r = el.getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height), left: Math.round(r.left), right: Math.round(r.right), top: Math.round(r.top) }; };
        const fr = document.querySelector('.filter-right');
        const fb = document.querySelector('.filter-bar');
        return {
          importBtn: rect(imp), exportBtn: rect(exp), newBtn: rect(neu),
          filterRight: fr ? { ...rect(fr), sw: fr.scrollWidth, cw: fr.clientWidth, cls: fr.className } : null,
          filterBar: fb ? { ...rect(fb), sw: fb.scrollWidth, cw: fb.clientWidth, cls: fb.className } : null,
          importChain: imp ? chainOf(imp) : null,
          docScrollW: document.documentElement.scrollWidth, docClientW: document.documentElement.clientWidth,
        };
      });
      const shot = path.join(OUT, 'scenario-' + vpname + '-' + theme + '-device-configs-import-clip.png');
      await page.screenshot({ path: shot });
      facts.scenarios.push({ theme, vp: vpname, name: 'device-configs-import-clip', screenshot: shot, ...s4 });
    } catch (e) { facts.scenarios.push({ theme, vp: vpname, name: 'device-configs-import-clip', error: String(e).slice(0, 160) }); }

    // S5 data-sources：表格横滚链 + 空态 + 筛选无结果
    try {
      await page.goto(BASE + '/data-sources', { waitUntil: 'domcontentloaded' });
      await sleep(1800);
      const s5 = await page.evaluate(() => {
        const t = document.querySelector('.el-table');
        const inner = document.querySelector('.el-table__body-wrapper');
        const r = el => { if (!el) return null; const x = el.getBoundingClientRect(); return { w: Math.round(x.width), left: Math.round(x.left), right: Math.round(x.right) }; };
        const chainOf = el => { const chain = []; let n = el; while (n && n !== document.documentElement) { const st = getComputedStyle(n); chain.push({ tag: n.tagName.toLowerCase(), cls: String(n.className || '').trim().split(/\s+/).slice(0, 2).join('.'), ox: st.overflowX, cw: n.clientWidth, sw: n.scrollWidth }); n = n.parentElement; } return chain; };
        return {
          table: r(t), bodyWrapper: inner ? { ...r(inner), cw: inner.clientWidth, sw: inner.scrollWidth, ox: getComputedStyle(inner).overflowX } : null,
          tableChain: t ? chainOf(t) : null,
          emptyPresent: !!document.querySelector('.empty-state, .el-empty'),
          emptyText: (document.querySelector('.empty-description') || document.querySelector('.el-empty__description') || {}).textContent || null,
          pagination: document.querySelectorAll('.el-pagination').length,
          headerText: (document.querySelector('.page-header') || {}).textContent || null,
          tableScrollLeftMax: inner ? inner.scrollWidth - inner.clientWidth : null,
        };
      });
      facts.scenarios.push({ theme, vp: vpname, name: 'data-sources-table-empty', ...s5 });
      // 筛选无结果
      await page.fill('[data-test="filter-category"]', '___nonexistent_category___');
      await page.click('[data-test="search-btn"]');
      await sleep(1500);
      const s5b = await page.evaluate(() => ({
        emptyPresent: !!document.querySelector('.empty-state, .el-empty'),
        emptyText: (document.querySelector('.empty-description') || document.querySelector('.el-empty__description') || {}).textContent || null,
        tableRows: document.querySelectorAll('.el-table__row').length,
        errorAlert: !!document.querySelector('[data-test="ds-error"]'),
        filterCategoryValue: (document.querySelector('[data-test="filter-category"]') || {}).value,
      }));
      const shot = path.join(OUT, 'scenario-' + vpname + '-' + theme + '-data-sources-filtered-nomatch.png');
      await page.screenshot({ path: shot });
      facts.scenarios.push({ theme, vp: vpname, name: 'data-sources-filter-nomatch', screenshot: shot, ...s5b });
    } catch (e) { facts.scenarios.push({ theme, vp: vpname, name: 'data-sources-table-empty', error: String(e).slice(0, 160) }); }

    // S6 接口失败态：abort 本页主列表请求
    for (const [nm, pat] of [['automation', '**/api/v1/automation-events*'], ['data-sources', '**/api/v1/data-sources*'], ['firmware', '**/api/v1/firmwares*'], ['alerts', '**/api/v1/alert-rules*']]) {
      try {
        const p2 = await ctx.newPage();
        await p2.route(pat, r => { try { r.abort(); } catch { /* 已被 unroute */ } });
        await p2.goto(BASE + ALL_ROUTES[nm], { waitUntil: 'domcontentloaded' });
        await sleep(2500);
        const s6 = await p2.evaluate(() => ({
          emptyText: (document.querySelector('.empty-description') || document.querySelector('.el-empty__description') || {}).textContent || null,
          emptyTitle: (document.querySelector('.empty-title') || document.querySelector('.el-empty__description') || {}).textContent || null,
          errorAlert: !!document.querySelector('[data-test="ds-error"]'),
          errorText: (document.querySelector('.el-alert__title') || {}).textContent || null,
          tableRows: document.querySelectorAll('.el-table__row').length,
          elMessage: [...document.querySelectorAll('.el-message')].map(m => (m.textContent || '').trim()),
          bodyText: (document.querySelector('.page-header') || document.body).textContent.trim().replace(/\s+/g, ' ').slice(0, 200),
        }));
        const shot = path.join(OUT, 'scenario-' + vpname + '-' + theme + '-' + nm + '-api-fail.png');
        await p2.screenshot({ path: shot });
        facts.scenarios.push({ theme, vp: vpname, name: nm + '-api-fail', screenshot: shot, ...s6 });
        await p2.close();
      } catch (e) { facts.scenarios.push({ theme, vp: vpname, name: nm + '-api-fail', error: String(e).slice(0, 160) }); }
    }

    // S7 首次加载态：延迟接口 3s，观察首屏
    try {
      const p3 = await ctx.newPage();
      const slow = async r => { await sleep(3000); try { await r.continue(); } catch { /* 页面已关闭 */ } };
      await p3.route('**/api/v1/automation-events*', slow);
      await p3.route('**/api/v1/automation-rules*', slow);
      await p3.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
      await sleep(1200);
      const s7 = await p3.evaluate(() => ({
        skeleton: document.querySelectorAll('.el-skeleton, [class*="skeleton"]').length,
        loadingMask: document.querySelectorAll('.el-loading-mask').length,
        rows: document.querySelectorAll('.el-table__row').length,
        bodyText: document.body.textContent.trim().replace(/\s+/g, ' ').slice(0, 160),
      }));
      facts.scenarios.push({ theme, vp: vpname, name: 'automation-first-load', ...s7, screenshot: (await (async () => { const s = path.join(OUT, 'scenario-' + vpname + '-' + theme + '-automation-first-load.png'); await p3.screenshot({ path: s }); return s; })()) });
      await p3.close();
    } catch (e) { facts.scenarios.push({ theme, vp: vpname, name: 'automation-first-load', error: String(e).slice(0, 160) }); }

    // S8 后台刷新：点击「刷新」后旧数据是否保留
    try {
      await page.goto(BASE + '/automation', { waitUntil: 'domcontentloaded' });
      await sleep(2200);
      let unroated = false;
      const slowEv = async r => { await sleep(2500); if (unroated) return; try { await r.continue(); } catch { /* ignore */ } };
      await page.route('**/api/v1/automation-events*', slowEv);
      const before = await page.evaluate(() => document.querySelectorAll('.el-table__row').length);
      await page.click('[data-test="refresh-events"]');
      await sleep(700);
      const during = await page.evaluate(() => ({ rows: document.querySelectorAll('.el-table__row').length, masks: document.querySelectorAll('.el-loading-mask').length, emptyText: (document.querySelector('.el-table__empty-text') || {}).textContent || null }));
      unroated = true;
      await page.unroute('**/api/v1/automation-events*');
      facts.scenarios.push({ theme, vp: vpname, name: 'automation-background-refresh', beforeRows: before, during });
    } catch (e) { facts.scenarios.push({ theme, vp: vpname, name: 'automation-background-refresh', error: String(e).slice(0, 160) }); }
  }
  await ctx.close();
  // 每个主题跑完即落盘：后续主题即使崩溃也不丢已取得的证据。
  fs.writeFileSync(path.join(OUT, 'd3-facts.json'), JSON.stringify(facts, null, 2));
}

await browser.close();
fs.writeFileSync(path.join(OUT, 'd3-facts.json'), JSON.stringify(facts, null, 2));
console.log('records=' + facts.results.length + ' scenarios=' + facts.scenarios.length + ' consoleErrors=' + facts.consoleErrors.length);
for (const e of facts.consoleErrors.slice(0, 8)) console.log('  ERR ' + e.text.slice(0, 140));
