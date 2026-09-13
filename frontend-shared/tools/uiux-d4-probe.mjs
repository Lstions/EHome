/**
 * D2/D4/D6/D7 定向取证探针（全局骨架与可访问性域）
 *
 * 依据：docs/分析/UIUX审计契约-2026-09-13.md §5/§6、docs/规范/前端开发与UIUX设计规范.md §3.1.3/§3.6.3/§4.4/§4.5
 * 只读脚本：不改 src/，只做度量与截图。产出 <UIUX_OUT>/d4-facts.json
 *
 * 用法：
 *   cd frontend-shared && UIUX_OUT=/tmp/uiux-d4 node tools/uiux-d4-probe.mjs
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

const BASE = process.env.UIUX_BASE || 'http://127.0.0.1:8082';
const OUT = process.env.UIUX_OUT || '/tmp/uiux-d4';
const USER = process.env.UIUX_USER || 'admin';
const PASS = process.env.UIUX_PASS || 'UiuxAudit2026!';
const CHROME = process.env.UIUX_CHROME || '/snap/bin/chromium';
fs.mkdirSync(OUT, { recursive: true });

const facts = { base: BASE, shots: [], measures: {}, notes: [] };

// ── 页内度量助手（字符串注入，避免闭包序列化问题） ─────────────────────
const HELPERS = `
  window.__m = {
    box: (el) => { if(!el) return null; const r = el.getBoundingClientRect(); return {w:Math.round(r.width),h:Math.round(r.height),x:Math.round(r.x),y:Math.round(r.y),right:Math.round(r.right),bottom:Math.round(r.bottom)}; },
    col: (el) => { if(!el) return null; const s = getComputedStyle(el); return {bg:s.backgroundColor, color:s.color, fs:s.fontSize, border:s.borderColor, shadow:s.boxShadow}; },
    q: (s) => document.querySelector(s),
    txt: (el) => el ? (el.textContent||'').replace(/\\s+/g,' ').trim().slice(0,120) : null,
    label: (el) => { if(!el) return null; return { tag: el.tagName.toLowerCase(), cls: (typeof el.className==='string'?el.className:'').split(' ').slice(0,2).join('.'), aria: el.getAttribute('aria-label'), role: el.getAttribute('role'), tabindex: el.getAttribute('tabindex'), text: (el.textContent||'').replace(/\\s+/g,' ').trim().slice(0,24) }; },
    focusable: (el) => { if(!el) return false; return el.tabIndex >= 0 || ['A','BUTTON','INPUT','SELECT','TEXTAREA'].includes(el.tagName); }
  };
`;

// WCAG 相对亮度/对比度
function parseRGB(s) {
  const m = String(s).match(/rgba?\(([^)]+)\)/);
  if (!m) return null;
  const p = m[1].split(',').map(v => parseFloat(v.trim()));
  if (p.length > 3 && p[3] === 0) return null;
  return [p[0], p[1], p[2]];
}
function lum([r, g, b]) {
  const f = c => { c /= 255; return c <= 0.03928 ? c / 12.92 : Math.pow((c + 0.055) / 1.055, 2.4); };
  return 0.2126 * f(r) + 0.7152 * f(g) + 0.0722 * f(b);
}
function contrast(a, b) {
  const la = lum(a), lb = lum(b);
  return Math.round(((Math.max(la, lb) + 0.05) / (Math.min(la, lb) + 0.05)) * 100) / 100;
}

const browser = await chromium.launch({ executablePath: CHROME, args: ['--no-sandbox', '--disable-setuid-sandbox'] });

async function shot(page, name) {
  const p = path.join(OUT, name + '.png');
  await page.screenshot({ path: p });
  facts.shots.push({ file: name + '.png', path: p });
  save();
}
// 增量落盘：任何后续步骤崩溃都不丢已取到的 DOM 事实。
const save = () => { try { fs.writeFileSync(path.join(OUT, 'd4-facts.json'), JSON.stringify(facts, null, 2)); } catch { /* ignore */ } };
const rec = (k, v) => { facts.measures[k] = v; save(); };

// ═══════════ 1. 登录页（未登录）双主题 ═══════════
for (const theme of ['light', 'dark']) {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', deviceScaleFactor: 1, colorScheme: theme });
  const page = await ctx.newPage();
  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
  await page.evaluate(t => { localStorage.setItem('theme', t); document.documentElement.setAttribute('data-theme', t); }, theme);
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.waitForTimeout(600);
  await page.evaluate(HELPERS);

  const m = await page.evaluate(() => {
    const M = window.__m, de = document.documentElement;
    const box = M.q('.login-box'), desc = M.q('.brand-desc'), tag = M.q('.brand-name'), ver = M.q('.version');
    const bgLayer = M.q('.login-bg'), c1 = M.q('.bg-circle.c1'), c2 = M.q('.bg-circle.c2'), c3 = M.q('.bg-circle.c3');
    const inputs = [...document.querySelectorAll('.login-box .el-input__inner')].map(i => M.box(i));
    const btns = [...document.querySelectorAll('.login-box button')].map(b => ({ ...M.label(b), ...M.box(b) }));
    const tabs = [...document.querySelectorAll('a, button, input, [tabindex]')].filter(M.focusable).map(M.label);
    return {
      themeAttr: de.getAttribute('data-theme'), htmlDark: de.classList.contains('dark'), bodyClass: document.body.className,
      localStorageTheme: localStorage.getItem('theme'), prefersDark: matchMedia('(prefers-color-scheme: dark)').matches,
      pageBg: getComputedStyle(document.body).backgroundColor,
      containerBg: M.col(M.q('.login-container')),
      boxBg: M.col(box), boxBox: M.box(box),
      titleColor: M.col(tag), descColor: M.col(desc), descText: M.txt(desc), versionColor: M.col(ver),
      alertCount: document.querySelectorAll('.el-alert').length,
      bgLayerBox: M.box(bgLayer), circles: [M.box(c1), M.box(c2), M.box(c3)].map((b, i) => ({ i: i + 1, ...b })),
      inputs, buttons: btns, focusableCount: tabs.length,
      linkInfo: [...document.querySelectorAll('.el-link')].map(l => ({ txt: M.txt(l), href: l.getAttribute('href'), tag: l.tagName })),
      h1Count: document.querySelectorAll('h1').length, h1Text: M.txt(M.q('h1')),
      deOverflow: de.scrollWidth - de.clientWidth,
      htmlOverflowX: getComputedStyle(de).overflowX,
      bodyText: document.body.innerText.slice(0, 300)
    };
  });
  rec('login_' + theme, m);
  await shot(page, 'login-' + theme + '-1440');

  // 忘记密码对话框
  await page.click('a.el-link');
  await page.waitForTimeout(500);
  await page.evaluate(HELPERS);
  const dlg = await page.evaluate(() => {
    const M = window.__m;
    const d = M.q('.el-dialog'), wrap = M.q('.el-overlay'), title = M.q('.el-dialog__title');
    return { dialogBox: M.box(d), dialogCol: M.col(d), titleColor: M.col(title), titleText: M.txt(title), overlayCol: M.col(wrap), bodyText: M.txt(M.q('.el-dialog__body')) };
  });
  rec('login_dialog_' + theme, dlg);
  await shot(page, 'login-dialog-' + theme + '-1440');
  await ctx.close();
}

// ═══════════ 2. 键盘走查：Tab 顺序 + Enter/Space 行为（登录页） ═══════════
{
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', colorScheme: 'light' });
  const page = await ctx.newPage();
  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.waitForTimeout(500);
  await page.evaluate(HELPERS);
  const seq = [];
  for (let i = 0; i < 8; i++) {
    await page.keyboard.press('Tab');
    seq.push(await page.evaluate(() => window.__m.label(document.activeElement)));
  }
  rec('login_tab_order', seq);

  // Enter 提交空表单 → 应出现校验错误而不是提交
  await page.evaluate(() => document.querySelector('input[placeholder="请输入用户名"]').focus());
  await page.keyboard.type(USER);
  await page.keyboard.press('Tab');
  await page.keyboard.type(PASS);
  await page.keyboard.press('Enter');
  await page.waitForTimeout(2500);
  rec('login_enter_submit', { url: page.url(), success: !page.url().includes('/login') });
  await ctx.close();
}

// ═══════════ 3. 已登录：骨架 / 导航 / 主题 / 移动抽屉 ═══════════
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', colorScheme: 'light' });
const page = await ctx.newPage();
const consoleErrors = [];
page.on('console', m => { if (m.type() === 'error') consoleErrors.push(m.text().slice(0, 200)); });
page.on('pageerror', e => consoleErrors.push('PAGEERROR ' + String(e).slice(0, 200)));

await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.evaluate(() => { localStorage.setItem('theme', 'light'); localStorage.removeItem('login_attempts'); localStorage.removeItem('login_lockout'); });
await page.reload({ waitUntil: 'domcontentloaded' });
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', USER);
await page.fill('input[placeholder="请输入密码"]', PASS);
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 });
await page.waitForTimeout(2500);

// 3a. MainLayout 亮色全度量（dashboard 为外壳代表）
await page.goto(BASE + '/dashboard', { waitUntil: 'domcontentloaded' });
await page.waitForTimeout(1800);
await page.evaluate(HELPERS);
const SHELL = () => {
  const M = window.__m, de = document.documentElement;
  const aside = M.q('.sidebar'), header = M.q('.main-header'), search = M.q('.global-search'), kbd = M.q('.search-kbd');
  const wsStatus = M.q('.ws-status'), userMenu = M.q('.user-menu'), crumb = M.q('.breadcrumb');
  const collapseBtn = M.q('.collapse-btn'), themeBtn = [...document.querySelectorAll('.main-header button')].find(b => b.getAttribute('aria-label') === '切换主题');
  const bellBtn = [...document.querySelectorAll('.main-header button')].find(b => b.getAttribute('aria-label') === '打开通知中心');
  const hamburger = [...document.querySelectorAll('.main-header button')].find(b => b.getAttribute('aria-label') === '打开导航菜单');
  const menuItems = [...document.querySelectorAll('.sidebar .el-menu-item')].map(el => ({ txt: M.txt(el), ...M.box(el), ...M.col(el), active: el.classList.contains('is-active') }));
  const headerBtns = [...document.querySelectorAll('.main-header button, .main-header [role="button"], .main-header .user-menu, .main-header .ws-status')]
    .map(el => ({ ...M.label(el), ...M.box(el), focusable: M.focusable(el), tabIndex: el.tabIndex }));
  return {
    themeAttr: de.getAttribute('data-theme'), htmlDark: de.classList.contains('dark'), bodyClass: document.body.className,
    localStorageTheme: localStorage.getItem('theme'),
    asideBox: M.box(aside), asideCol: M.col(aside),
    headerBox: M.box(header), headerCol: M.col(header),
    mainContentBg: M.col(M.q('.main-content')),
    searchBox: M.box(search), kbdText: M.txt(kbd),
    wsStatusBox: M.box(wsStatus), wsStatusCol: M.col(wsStatus), wsStatusText: M.txt(wsStatus),
    userMenuBox: M.box(userMenu), userMenuCol: M.col(userMenu),
    crumbText: M.txt(crumb), crumbBox: M.box(crumb),
    menuItemCount: menuItems.length, menuItems,
    headerTargets: headerBtns,
    deOverflow: de.scrollWidth - de.clientWidth,
    asideVisible: !!aside
  };
};
rec('shell_light_1440', await page.evaluate(SHELL));
await shot(page, 'shell-light-1440');

// 3b. 点击 logo（非原生容器可点击）
const logoInfo = await page.evaluate(() => {
  const M = window.__m, el = M.q('.logo-area');
  return { ...M.label(el), box: M.box(el), focusable: M.focusable(el), tabIndex: el.tabIndex, role: el.getAttribute('role') };
});
rec('shell_logo_area', logoInfo);

// 3c. WS 状态 / NetworkBanner
const wsFacts = await page.evaluate(() => {
  const M = window.__m;
  return {
    bannerCount: document.querySelectorAll('.network-banner').length,
    bannerText: M.txt(M.q('.network-banner')),
    wsText: M.txt(M.q('.ws-status')),
    wsConnectedClass: M.q('.ws-status') ? M.q('.ws-status').classList.contains('connected') : null
  };
});
rec('shell_network_state', wsFacts);

// 3d. 侧栏折叠
await page.click('.collapse-btn');
await page.waitForTimeout(700);
await page.evaluate(HELPERS);
rec('shell_collapsed_1440', await page.evaluate(() => {
  const M = window.__m, de = document.documentElement;
  return {
    asideBox: M.box(M.q('.sidebar')), logoTextVisible: !!M.q('.logo-text'), versionVisible: !!M.q('.version-info'),
    menuItemBoxes: [...document.querySelectorAll('.sidebar .el-menu-item')].slice(0, 3).map(el => ({ txt: M.txt(el), ...M.box(el) })),
    deOverflow: de.scrollWidth - de.clientWidth
  };
}));
await shot(page, 'shell-collapsed-light-1440');
// 展开回来
await page.click('.collapse-btn');
await page.waitForTimeout(600);

// 3e. 主题切换到暗色 → 重测
await page.evaluate(HELPERS);
await page.click('.main-header button[aria-label="切换主题"]');
await page.waitForTimeout(500);
await page.evaluate(HELPERS);
rec('theme_dropdown_light', await page.evaluate(() => {
  const M = window.__m;
  const pop = M.q('.el-dropdown__popper'), menu = M.q('.el-dropdown-menu'), items = [...document.querySelectorAll('.el-dropdown-menu__item')];
  return {
    popoverBox: M.box(pop), popoverCol: M.col(pop), menuCol: M.col(menu),
    items: items.map(el => ({ txt: M.txt(el), disabled: el.classList.contains('is-disabled'), ariaDisabled: el.getAttribute('aria-disabled'), ...M.box(el), ...M.col(el) })),
    popoverClass: pop ? pop.className : null, popoverParent: pop ? pop.parentElement.tagName : null
  };
}));
await shot(page, 'theme-dropdown-light-1440');
await page.click('.el-dropdown-menu__item:has-text("暗色模式")');
await page.waitForTimeout(900);
await page.evaluate(HELPERS);
rec('shell_dark_1440', await page.evaluate(SHELL));
rec('theme_state_after_switch', await page.evaluate(() => ({
  localStorageTheme: localStorage.getItem('theme'),
  themeAttr: document.documentElement.getAttribute('data-theme'),
  htmlDark: document.documentElement.classList.contains('dark'),
  bodyClass: document.body.className,
  darkThemeEls: document.querySelectorAll('.dark-theme').length,
  dataThemeEls: document.querySelectorAll('[data-theme]').length,
  dataThemeValues: [...document.querySelectorAll('[data-theme]')].map(e => e.tagName + '=' + e.getAttribute('data-theme'))
})));
await shot(page, 'shell-dark-1440');

// 3f. popover / dropdown 暗色实测（通知 + 用户菜单）
await page.click('.main-header button[aria-label="打开通知中心"]');
await page.waitForTimeout(700);
await page.evaluate(HELPERS);
rec('notification_popover_dark', await page.evaluate(() => {
  const M = window.__m;
  const pop = M.q('.notification-popover'), empty = M.q('.notification-empty');
  return { box: M.box(pop), col: M.col(pop), emptyCol: empty ? M.col(empty) : null, emptyText: M.txt(empty), parent: pop ? pop.parentElement.tagName : null, bgCalc: pop ? getComputedStyle(pop).backgroundColor : null };
}));
await shot(page, 'notification-popover-dark-1440');
await page.keyboard.press('Escape');
await page.waitForTimeout(400);

// 3g. 用户菜单元件：div 是否可聚焦
await page.evaluate(HELPERS);
const userMenuFacts = await page.evaluate(() => {
  const M = window.__m, el = M.q('.user-menu');
  return { ...M.label(el), tagName: el.tagName, role: el.getAttribute('role'), tabIndex: el.tabIndex, focusable: M.focusable(el), box: M.box(el) };
});
rec('user_menu_a11y', userMenuFacts);
// 用键盘能否到达并展开？
await page.evaluate(() => document.querySelector('.global-search input')?.blur());
const reached = [];
for (let i = 0; i < 14; i++) {
  await page.keyboard.press('Tab');
  const l = await page.evaluate(() => window.__m.label(document.activeElement));
  reached.push(l);
}
rec('shell_tab_order_dark', reached);

// 3h. Ctrl+K 快速跳转
await page.keyboard.press('Control+k');
await page.waitForTimeout(400);
rec('ctrl_k_focus', await page.evaluate(() => ({ active: window.__m.label(document.activeElement), isSearchInput: document.activeElement === document.querySelector('.global-search input') })));
await page.keyboard.type('节点');
await page.keyboard.press('Enter');
await page.waitForTimeout(1500);
rec('ctrl_k_navigate', { url: page.url() });

// 3i. Profile 页（暗色）
await page.goto(BASE + '/profile', { waitUntil: 'domcontentloaded' });
await page.waitForTimeout(1400);
await page.evaluate(HELPERS);
rec('profile_dark_1440', await page.evaluate(() => {
  const M = window.__m;
  const card = M.q('.el-card'), inputs = [...document.querySelectorAll('.profile-page .el-input__inner')];
  return {
    cardCol: M.col(card), cardBox: M.box(card),
    inputFontSizes: inputs.map(i => getComputedStyle(i).fontSize),
    inputBoxes: inputs.map(i => M.box(i)),
    pageHeaderText: M.txt(M.q('[class*="page-header"]')),
    deOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    submitBtn: M.box(M.q('.profile-page button.el-button--primary'))
  };
}));
await shot(page, 'profile-dark-1440');

// 3j. 404 / 403
for (const [name, route] of [['notfound', '/this-route-does-not-exist-xyz'], ['forbidden', '/403']]) {
  await page.goto(BASE + route, { waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1200);
  await page.evaluate(HELPERS);
  rec(name + '_dark_1440', await page.evaluate(() => {
    const M = window.__m, de = document.documentElement;
    const btns = [...document.querySelectorAll('.error-actions button')];
    return {
      path: location.pathname, code: M.txt(M.q('.error-code')), title: M.txt(M.q('.error-title')), desc: M.txt(M.q('.error-desc')),
      hasSidebar: !!M.q('.sidebar'), hasHeader: !!M.q('.main-header'),
      buttons: btns.map(b => ({ txt: M.txt(b), box: M.box(b) })),
      bgColor: M.col(M.q('.error-page')), titleCol: M.col(M.q('.error-title')),
      focusableCount: [...document.querySelectorAll('a,button,input,[tabindex]')].filter(e => e.tabIndex >= 0).length,
      text: document.body.innerText.slice(0, 400),
      deOverflow: de.scrollWidth - de.clientWidth
    };
  }));
  await shot(page, name + '-dark-1440');
  // 亮色
  await page.evaluate(() => { localStorage.setItem('theme', 'light'); });
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForTimeout(1200);
  await page.evaluate(HELPERS);
  rec(name + '_light_1440', await page.evaluate(() => {
    const M = window.__m;
    return { bgColor: M.col(M.q('.error-page')), titleCol: M.col(M.q('.error-title')), descCol: M.col(M.q('.error-desc')), codeCol: M.col(M.q('.error-code')) };
  }));
  await shot(page, name + '-light-1440');
  await page.evaluate(() => { localStorage.setItem('theme', 'dark'); });
}

// ═══════════ 4. 移动端 390 / 360：抽屉 + 触控 + 输入字号 ═══════════
for (const vp of [{ n: '390', w: 390, h: 844 }, { n: '360', w: 360, h: 800 }]) {
  const p = await ctx.newPage();
  await p.setViewportSize({ width: vp.w, height: vp.h });
  await p.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
  await p.evaluate(() => { localStorage.setItem('theme', 'dark'); });
  await p.reload({ waitUntil: 'domcontentloaded' });
  await p.waitForSelector('input[placeholder="请输入用户名"]');
  await p.fill('input[placeholder="请输入用户名"]', USER);
  await p.fill('input[placeholder="请输入密码"]', PASS);
  await p.click('button:has-text("登")');
  await p.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 });
  await p.waitForTimeout(2200);
  await p.goto(BASE + '/dashboard', { waitUntil: 'domcontentloaded' });
  await p.waitForTimeout(1500);
  await p.evaluate(HELPERS);
  rec('mobile_' + vp.n + '_shell', await p.evaluate(() => {
    const M = window.__m, de = document.documentElement;
    const ham = [...document.querySelectorAll('.main-header button')].find(b => b.getAttribute('aria-label') === '打开导航菜单');
    const targets = [...document.querySelectorAll('.main-header button, .main-header [role="button"], .main-header .user-menu')].map(el => ({ ...M.label(el), ...M.box(el) }));
    return {
      hasSidebarAside: !!M.q('.sidebar'), hasHamburger: !!ham, hamburgerBox: ham ? M.box(ham) : null,
      headerTargets: targets,
      headerBox: M.box(M.q('.main-header')), crumbDisplay: M.q('.breadcrumb') ? getComputedStyle(M.q('.breadcrumb')).display : null,
      searchDisplay: M.q('.header-center') ? getComputedStyle(M.q('.header-center')).display : null,
      userNameDisplay: M.q('.user-name') ? getComputedStyle(M.q('.user-name')).display : null,
      wsTextDisplay: M.q('.ws-status .status-text') ? getComputedStyle(M.q('.ws-status .status-text')).display : null,
      deOverflow: de.scrollWidth - de.clientWidth,
      inputFontSize: M.q('.el-input__inner') ? getComputedStyle(M.q('.el-input__inner')).fontSize : null,
      htmlOverflowX: getComputedStyle(de).overflowX, mainOverflowY: M.q('.main-content') ? getComputedStyle(M.q('.main-content')).overflowY : null,
      bodyH: document.body.scrollHeight, docH: de.scrollHeight
    };
  }));
  await shot(p, 'shell-dark-mobile-' + vp.n);

  // 打开抽屉
  const hamSel = '.main-header button[aria-label="打开导航菜单"]';
  if (await p.$(hamSel)) {
    await p.click(hamSel);
    await p.waitForTimeout(800);
    await p.evaluate(HELPERS);
    rec('mobile_' + vp.n + '_drawer', await p.evaluate(() => {
      const M = window.__m, d = M.q('.el-drawer'), body = M.q('.el-drawer__body') || M.q('.mobile-drawer-body');
      const items = [...document.querySelectorAll('.mobile-sidebar-menu .el-menu-item')].map(el => ({ txt: M.txt(el), ...M.box(el), ...M.col(el) }));
      return {
        drawerBox: M.box(d), drawerCol: M.col(d), bodyCol: body ? M.col(body) : null,
        drawerClass: d ? d.className : null, parentTag: d ? d.parentElement.tagName : null,
        items, itemCount: items.length,
        logoTextCol: M.col(M.q('.mobile-logo-text')), versionCol: M.col(M.q('.mobile-version-info')),
        overlayCol: M.col(M.q('.el-overlay'))
      };
    }));
    await shot(p, 'drawer-dark-mobile-' + vp.n);
    await p.keyboard.press('Escape');
    await p.waitForTimeout(500);
  }

  // profile 移动端
  await p.goto(BASE + '/profile', { waitUntil: 'domcontentloaded' });
  await p.waitForTimeout(1300);
  await p.evaluate(HELPERS);
  rec('mobile_' + vp.n + '_profile', await p.evaluate(() => {
    const M = window.__m, de = document.documentElement;
    const inputs = [...document.querySelectorAll('.profile-page .el-input__inner')];
    const labels = [...document.querySelectorAll('.profile-page .el-form-item__label')].map(el => ({ txt: M.txt(el), ...M.box(el), ...M.col(el), textAlign: getComputedStyle(el).textAlign }));
    const btns = [...document.querySelectorAll('.profile-page button')].map(el => ({ ...M.label(el), ...M.box(el) }));
    return {
      inputFontSizes: inputs.map(i => getComputedStyle(i).fontSize),
      inputBoxes: inputs.map(i => M.box(i)),
      labels, buttons: btns,
      deOverflow: de.scrollWidth - de.clientWidth,
      cardBoxes: [...document.querySelectorAll('.el-card')].map(c => M.box(c)),
      labelPosition: labels.length ? labels[0].textAlign : null, labelTexts: labels.map(l => l.txt)
    };
  }));
  await shot(p, 'profile-dark-mobile-' + vp.n);
  await p.close();
}

// ═══════════ 5. 键盘可操作性（真实 Enter/Space） ═══════════
try {
const kp = await ctx.newPage();
await kp.setViewportSize({ width: 1440, height: 900 });
await kp.goto(BASE + '/dashboard', { waitUntil: 'domcontentloaded' });
await kp.waitForSelector('.sidebar', { timeout: 20000 });
await kp.waitForTimeout(1200);
await kp.evaluate(HELPERS);
// 5a. 可点击非原生容器枚举（全站骨架）
rec('clickable_non_native', await kp.evaluate(() => {
  const M = window.__m;
  const cands = [...document.querySelectorAll('.main-header *, .sidebar *')].filter(el => {
    if (['BUTTON', 'A', 'INPUT', 'SELECT', 'TEXTAREA'].includes(el.tagName)) return false;
    const s = getComputedStyle(el);
    return s.cursor === 'pointer';
  });
  return cands.map(el => ({ ...M.label(el), tabIndex: el.tabIndex, role: el.getAttribute('role'), aria: el.getAttribute('aria-label'), focusable: M.focusable(el), box: M.box(el) }));
}));
// 5b. 侧栏折叠按钮：Enter 与 Space 是否同 click 行为
const asideBefore = await kp.evaluate(() => window.__m.box(document.querySelector('.sidebar')));
await kp.focus('.collapse-btn');
await kp.keyboard.press('Enter');
await kp.waitForTimeout(700);
const asideAfterEnter = await kp.evaluate(() => window.__m.box(document.querySelector('.sidebar')));
await kp.keyboard.press('Space');
await kp.waitForTimeout(700);
const asideAfterSpace = await kp.evaluate(() => window.__m.box(document.querySelector('.sidebar')));
rec('keyboard_collapse_toggle', { asideBefore, asideAfterEnter, asideAfterSpace });
// 5c. logo-area（DIV）键盘可达性
const logoKeys = await kp.evaluate(() => {
  const el = document.querySelector('.logo-area');
  el.focus();
  return { tabIndex: el.tabIndex, isActive: document.activeElement === el, role: el.getAttribute('role') };
});
rec('keyboard_logo_focus', logoKeys);
await kp.keyboard.press('Enter');
await kp.waitForTimeout(1200);
rec('keyboard_logo_enter_result', { url: kp.url() });
// 5d. 用户菜单 DIV
const umKeys = await kp.evaluate(() => {
  const el = document.querySelector('.user-menu');
  el.focus();
  return { tabIndex: el.tabIndex, isActive: document.activeElement === el, role: el.getAttribute('role') };
});
rec('keyboard_usermenu_focus', umKeys);
await kp.keyboard.press('Enter');
await kp.waitForTimeout(800);
rec('keyboard_usermenu_enter', await kp.evaluate(() => ({ dropdownVisible: !!document.querySelector('.el-dropdown-menu:not([style*="display: none"])'), activeEl: window.__m.label(document.activeElement) })));
await kp.close();
} catch (e) { facts.notes.push('section5 error: ' + String(e).slice(0, 300)); save(); }

await ctx.close();
await browser.close();

facts.consoleErrors = consoleErrors;
facts.contrast = (() => {
  const out = [];
  const L = facts.measures;
  const pairs = [
    ['login_light', 'login 描述文字', L.login_light?.descColor?.color, L.login_light?.boxBg?.bg],
    ['login_dark', 'login 描述文字', L.login_dark?.descColor?.color, L.login_dark?.boxBg?.bg],
    ['login_light', 'login 版本号', L.login_light?.versionColor?.color, L.login_light?.boxBg?.bg],
    ['login_dark', 'login 版本号', L.login_dark?.versionColor?.color, L.login_dark?.boxBg?.bg],
    ['login_light', 'login 标题', L.login_light?.titleColor?.color, L.login_light?.boxBg?.bg],
    ['login_dark', 'login 标题', L.login_dark?.titleColor?.color, L.login_dark?.boxBg?.bg],
    ['theme_dropdown_light', '主题菜单项', L.theme_dropdown_light?.items?.[0]?.color, L.theme_dropdown_light?.menuCol?.bg],
  ];
  for (const [k, name, fg, bg] of pairs) {
    const a = parseRGB(fg), b = parseRGB(bg);
    if (a && b) out.push({ key: k, name, fg, bg, ratio: contrast(a, b) });
  }
  const shellL = L.shell_light_1440, shellD = L.shell_dark_1440;
  if (shellL?.wsStatusCol && shellL?.headerCol) {
    const a = parseRGB(shellL.wsStatusCol.color), b = parseRGB(shellL.headerCol.bg);
    if (a && b) out.push({ key: 'shell_light_1440', name: 'WS 状态文字/页头', fg: shellL.wsStatusCol.color, bg: shellL.headerCol.bg, ratio: contrast(a, b) });
  }
  if (shellD?.wsStatusCol && shellD?.headerCol) {
    const a = parseRGB(shellD.wsStatusCol.color), b = parseRGB(shellD.headerCol.bg);
    if (a && b) out.push({ key: 'shell_dark_1440', name: 'WS 状态文字/页头(暗)', fg: shellD.wsStatusCol.color, bg: shellD.headerCol.bg, ratio: contrast(a, b) });
  }
  for (const th of ['light', 'dark']) {
    const m = L['notfound_' + th + '_1440'];
    if (m?.descCol && m?.bgColor) {
      const a = parseRGB(m.descCol.color), b = parseRGB(m.bgColor.bg);
      if (a && b) out.push({ key: 'notfound_' + th, name: '404 描述文字/页面背景', fg: m.descCol.color, bg: m.bgColor.bg, ratio: contrast(a, b) });
    }
  }
  return out;
})();

fs.writeFileSync(path.join(OUT, 'd4-facts.json'), JSON.stringify(facts, null, 2));
console.log('=== D4 探针完成 ===');
console.log('截图 ' + facts.shots.length + ' 张 → ' + OUT);
console.log('度量键: ' + Object.keys(facts.measures).join(', '));
console.log('控制台错误 ' + consoleErrors.length);
consoleErrors.slice(0, 5).forEach(e => console.log('  ERR ' + e));
console.log('--- 对比度 ---');
facts.contrast.forEach(c => console.log('  ' + c.key.padEnd(22) + c.name.padEnd(24) + 'fg=' + c.fg + ' bg=' + c.bg + ' ratio=' + c.ratio));
