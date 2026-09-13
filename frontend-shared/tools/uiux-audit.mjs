/**
 * UI/UX 三合一取证探针（截图 + DOM 事实 + 代码锚点定位）。
 *
 * 权威依据：docs/规范/前端开发与UIUX设计规范.md §5.2
 *   「视觉结论用 DOM 事实复核：location.pathname、getComputedStyle、
 *     getBoundingClientRect、scrollWidth/clientWidth。截图/视觉模型只作为发现问题的辅助证据。」
 *
 * 用法（须先起后端与前端产物，见 docs/分析/UIUX审计契约-2026-09-13.md §3）：
 *   UIUX_ROUTES=dashboard,node-list UIUX_THEMES=light node uiux-audit.mjs
 *
 * 产出：<OUT>/dom-facts.json（全部度量）+ <OUT>/*.png（每个 页面×视口×主题 一张）
 *
 * 为什么不用 Playwright test runner：审计要一次遍历几十个 页面×视口×主题 组合并产出
 * 结构化 JSON，用 reporter 反而要绕开断言失败的终止语义；这里直接驱动 browser API。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

const BASE = process.env.UIUX_BASE || 'http://127.0.0.1:8082';
const OUT = process.env.UIUX_OUT || '/tmp/uiux-evidence';
const USER = process.env.UIUX_USER || 'admin';
const PASS = process.env.UIUX_PASS || 'UiuxAudit2026!';
const CHROME = process.env.UIUX_CHROME || '/snap/bin/chromium';

// 路由清单：名称必须与 docs/分析/UIUX审计契约-2026-09-13.md §4 的页面清单一致。
const ALL_ROUTES = {
  'dashboard': '/dashboard',
  'node-list': '/node',
  'channel-list': '/channel',
  'edge-device-list': '/edge-device',
  'logical-device-list': '/logical-device',
  'data-panel': '/data',
  'data-sources': '/data-sources',
  'firmware': '/firmware',
  'device-configs': '/device-configs',
  'monitor': '/monitor',
  'alerts': '/alerts',
  'automation': '/automation',
  'profile': '/profile',
  'login': '/login',
};
const ONLY = (process.env.UIUX_ROUTES || '').split(',').map(s => s.trim()).filter(Boolean);
const ROUTES = ONLY.length
  ? ONLY.map(n => [n, ALL_ROUTES[n]]).filter(([, r]) => r)
  : Object.entries(ALL_ROUTES);

const ALL_VIEWPORTS = [
  { name: 'desktop-1440', width: 1440, height: 900 },
  { name: 'laptop-1024', width: 1024, height: 768 },
  { name: 'tablet-768', width: 768, height: 1024 },
  { name: 'mobile-390', width: 390, height: 844 },
  { name: 'mobile-360', width: 360, height: 800 },
];
const VP_ONLY = (process.env.UIUX_VIEWPORTS || '').split(',').map(s => s.trim()).filter(Boolean);
const VIEWPORTS = VP_ONLY.length ? ALL_VIEWPORTS.filter(v => VP_ONLY.includes(v.name)) : ALL_VIEWPORTS;

const THEMES = (process.env.UIUX_THEMES || 'light,dark').split(',').map(s => s.trim()).filter(Boolean);

fs.mkdirSync(OUT, { recursive: true });

/**
 * 页内度量。全部返回可复核的 DOM 事实，不含主观判断。
 *
 * clipped 是本探针相对规范 §5.2 的**补充**，但有一条实测得来的排除规则：
 * 只把「祖先裁切且该祖先**自身不可横向滚动**」计为缺陷。
 *
 * 为什么必须排除可滚动祖先：Element Plus 的 el-table__header-wrapper/body-wrapper
 * 是 overflow:hidden + scrollWidth>clientWidth 的**横滚容器**，宽表格在其内部滚动
 * 是设计允许的。首版探针不分青红皂白地把它们报成"裁切"，在 automation 页产生
 * 10 处误报（实测 scrollable=true）；排掉之后 automation 归零。
 *
 * 另一条实测教训：node-list 在 mobile-390 的截图看起来"设备ID被切掉"，
 * 实测该 SPAN 右边界 341 < viewport 390 —— 它是被**视口高度**截在屏幕外，
 * 不是横向裁切。单看截图会得出错误结论，故本探针只认 DOM 度量。
 */
const MEASURE = () => {
  const de = document.documentElement;
  const cs = getComputedStyle(de);
  const q = s => document.querySelector(s);
  const txt = el => el ? (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 60) : null;
  const box = el => { if (!el) return null; const r = el.getBoundingClientRect(); return { w: Math.round(r.width), h: Math.round(r.height), x: Math.round(r.x), y: Math.round(r.y), right: Math.round(r.right) }; };

  // 小点击目标（规范 §4.4.5：移动端 ≥44x44，高密度工具栏最低 36）
  const small = [];
  document.querySelectorAll('button, .el-button, .el-switch, [role="button"], a.el-link, .el-checkbox').forEach(el => {
    const r = el.getBoundingClientRect();
    if (r.width > 0 && r.height > 0 && (r.width < 36 || r.height < 36)) {
      small.push({
        label: (el.getAttribute('aria-label') || el.textContent || (typeof el.className === 'string' ? el.className : '') || '').trim().replace(/\s+/g, ' ').slice(0, 28),
        w: Math.round(r.width), h: Math.round(r.height),
        sel: el.tagName.toLowerCase() + (typeof el.className === 'string' && el.className ? '.' + el.className.split(' ')[0] : ''),
      });
    }
  });

  // 真实裁切：元素右边界超出某个 overflow 非 visible 的祖先
  const clipped = [];
  document.querySelectorAll('body *').forEach(el => {
    const r = el.getBoundingClientRect();
    if (r.width <= 0) return;
    let anc = el.parentElement;
    while (anc && anc !== document.body) {
      const acs = getComputedStyle(anc);
      if (acs.overflowX !== 'visible' || acs.overflowY !== 'visible') {
        const ar = anc.getBoundingClientRect();
        // 该祖先自身可横向滚动 ⇒ 内容在其内部滚动是设计允许的，不计裁切。
        const scrollableX = anc.scrollWidth > anc.clientWidth + 2 &&
          (acs.overflowX === 'auto' || acs.overflowX === 'scroll' || acs.overflowX === 'hidden');
        if (!scrollableX && (r.right > ar.right + 2 || r.left < ar.left - 2)) {
          if (clipped.length < 10) {
            clipped.push({
              tag: el.tagName.toLowerCase(),
              cls: (typeof el.className === 'string' ? el.className : '').split(' ').slice(0, 2).join('.'),
              text: (el.textContent || '').trim().replace(/\s+/g, ' ').slice(0, 30),
              by: (typeof anc.className === 'string' ? anc.className : '').split(' ').slice(0, 2).join('.'),
              overBy: Math.round(Math.max(r.right - ar.right, ar.left - r.left)),
            });
          }
        }
        break;
      }
      anc = anc.parentElement;
    }
  });

  // 直接超出视口的元素（规范 §5.2 判据）
  const overflowing = [];
  document.querySelectorAll('body *').forEach(el => {
    const r = el.getBoundingClientRect();
    if (r.width > 0 && r.right > de.clientWidth + 2 && overflowing.length < 8) {
      overflowing.push({ tag: el.tagName.toLowerCase(), cls: (typeof el.className === 'string' ? el.className : '').split(' ').slice(0, 2).join('.'), right: Math.round(r.right) });
    }
  });

  // 主题事实（供暗色对比）
  const card = q('.el-card');
  const table = q('.el-table');
  const btn = q('.el-button--primary');
  return {
    path: location.pathname,
    docTitle: document.title,
    heading: txt(q('h1') || q('.page-title') || q('.el-page-header__content') || q('h2')),
    pageHeaderPresent: !!q('[class*="page-header"]'),
    pageHeaderText: txt(q('[class*="page-header"]')),
    overflow: de.scrollWidth - de.clientWidth,
    scrollW: de.scrollWidth, clientW: de.clientWidth,
    bodyScrollH: document.body.scrollHeight,
    totalEls: document.querySelectorAll('body *').length,
    rowCount: document.querySelectorAll('.el-table__row').length,
    tableCount: document.querySelectorAll('.el-table').length,
    mobileTableWrapper: document.querySelectorAll('.mobile-table-wrapper').length,
    pagination: document.querySelectorAll('.el-pagination').length,
    cardCount: document.querySelectorAll('.el-card').length,
    emptyState: document.querySelectorAll('.el-empty').length,
    emptyText: txt(q('.el-empty__description')),
    skeleton: document.querySelectorAll('[class*="skeleton"], .el-skeleton').length,
    inlineStyled: document.querySelectorAll('[style]:not([style=""])').length,
    bodyBg: cs.backgroundColor,
    textColor: cs.color,
    fontFamily: cs.fontFamily.slice(0, 50),
    cardBg: card ? getComputedStyle(card).backgroundColor : null,
    tableBg: table ? getComputedStyle(table).backgroundColor : null,
    primaryBtnBg: btn ? getComputedStyle(btn).backgroundColor : null,
    smallCount: small.length,
    smallTargets: small.slice(0, 10),
    clippedCount: clipped.length,
    clipped: clipped.slice(0, 8),
    overflowCount: overflowing.length,
    overflowing: overflowing.slice(0, 6),
    // 键盘可达性：可点击但不可聚焦
    clickableNotFocusable: [...document.querySelectorAll('[role="button"], .el-button, button')]
      .filter(el => el.tabIndex < 0).length,
    // 图片/图标缺失
    imgsBroken: [...document.querySelectorAll('img')].filter(i => i.complete && i.naturalWidth === 0).length,
    imgsTotal: document.querySelectorAll('img').length,
  };
};

const browser = await chromium.launch({
  executablePath: CHROME,
  args: ['--no-sandbox', '--disable-setuid-sandbox', '--font-render-hinting=none'],
});

const results = [];
const consoleErrors = [];

for (const theme of THEMES) {
  const ctx = await browser.newContext({
    viewport: { width: 1440, height: 900 }, locale: 'zh-CN', deviceScaleFactor: 1,
    colorScheme: theme === 'dark' ? 'dark' : 'light',
  });
  const page = await ctx.newPage();
  page.on('console', m => { if (m.type() === 'error') consoleErrors.push({ theme, text: m.text().slice(0, 300) }); });
  page.on('pageerror', e => consoleErrors.push({ theme, text: 'PAGEERROR ' + String(e).slice(0, 300) }));

  // 主题在登录前设定：app 从 localStorage 读，reload 后生效
  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded', timeout: 30000 });
  await page.evaluate(t => {
    localStorage.setItem('theme', t);
    document.documentElement.setAttribute('data-theme', t);
    document.documentElement.classList.toggle('dark', t === 'dark');
  }, theme);

  if (ROUTES.some(([n]) => n === 'login')) {
    await page.waitForSelector('input[placeholder="请输入用户名"]', { timeout: 20000 });
    results.push({ theme, vp: 'desktop-1440', name: 'login', route: '/login', ...(await page.evaluate(MEASURE)) });
    await page.screenshot({ path: path.join(OUT, 'desktop-1440-' + theme + '-login.png') });
  }

  await page.waitForSelector('input[placeholder="请输入用户名"]', { timeout: 20000 });
  await page.fill('input[placeholder="请输入用户名"]', USER);
  await page.fill('input[placeholder="请输入密码"]', PASS);
  await page.click('button:has-text("登")');
  await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 }).catch(() => {});
  await page.waitForTimeout(2500);
  const loggedIn = !page.url().includes('/login');
  if (!loggedIn) { console.error('登录失败，theme=' + theme); }

  for (const vp of VIEWPORTS) {
    await page.setViewportSize({ width: vp.width, height: vp.height });
    for (const [name, route] of ROUTES) {
      if (name === 'login') continue;
      const rec = { theme, vp: vp.name, name, route };
      try {
        await page.goto(BASE + route, { waitUntil: 'domcontentloaded', timeout: 25000 });
        await page.waitForTimeout(1600);
        Object.assign(rec, await page.evaluate(MEASURE));
        const shot = path.join(OUT, vp.name + '-' + theme + '-' + name + '.png');
        await page.screenshot({ path: shot });
        rec.screenshot = shot;
        if (vp.name === 'desktop-1440') {
          await page.screenshot({ path: path.join(OUT, 'full-' + theme + '-' + name + '.png'), fullPage: true }).catch(() => {});
        }
      } catch (e) {
        rec.error = String(e).split('\n')[0].slice(0, 180);
      }
      results.push(rec);
    }
  }
  await ctx.close();
}

await browser.close();
fs.writeFileSync(path.join(OUT, 'dom-facts.json'), JSON.stringify({ base: BASE, routes: ROUTES.map(r => r[0]), viewports: VIEWPORTS.map(v => v.name), themes: THEMES, results, consoleErrors }, null, 2));

const total = results.length;
const shots = fs.readdirSync(OUT).filter(f => f.endsWith('.png')).length;
console.log('页面记录 ' + total + ' | 截图 ' + shots + ' | 控制台错误 ' + consoleErrors.length);
for (const e of consoleErrors.slice(0, 10)) console.log('  ERR [' + e.theme + '] ' + e.text.slice(0, 150));
console.log('名字                 视口          主题   ov clip over small  rows  pag  els');
for (const r of results) {
  const flag = (r.overflow > 2 || r.clippedCount > 0 || r.error) ? ' <<<' : '';
  console.log(
    String(r.name).padEnd(20) + String(r.vp).padEnd(14) + String(r.theme).padEnd(6) +
    String(r.overflow ?? '-').padStart(3) + String(r.clippedCount ?? '-').padStart(5) +
    String(r.overflowCount ?? '-').padStart(5) + String(r.smallCount ?? '-').padStart(6) +
    String(r.rowCount ?? '-').padStart(6) + String(r.pagination ?? '-').padStart(4) +
    String(r.totalEls ?? '-').padStart(6) + (r.error ? ' ERR:' + r.error : '') + flag);
}
