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
const save = () => fs.writeFileSync(path.join(OUT, 'd4-facts2.json'), JSON.stringify(facts, null, 2));
const rec = (k, v) => { facts.measures[k] = v; save(); };
const shot = async (page, name) => { const q = path.join(OUT, name + '.png'); await page.screenshot({ path: q }); facts.shots.push(name + '.png'); save(); };

// 页内助手用数组 join 构造，避免模板字符串嵌套。
const H = [
  'window.__m = {',
  '  box: (el) => { if(!el) return null; const r = el.getBoundingClientRect(); return {w:Math.round(r.width),h:Math.round(r.height),x:Math.round(r.x),y:Math.round(r.y),right:Math.round(r.right),bottom:Math.round(r.bottom)}; },',
  '  col: (el) => { if(!el) return null; const s = getComputedStyle(el); return {bg:s.backgroundColor, color:s.color, fs:s.fontSize, caret:s.caretColor, border:s.borderColor, opacity:s.opacity, cursor:s.cursor}; },',
  '  q: (s) => document.querySelector(s),',
  '  txt: (el) => el ? (el.textContent||"").replace(/\\s+/g," ").trim().slice(0,200) : null,',
  '  lab: (el) => { if(!el) return null; const s=getComputedStyle(el); return { tag: el.tagName.toLowerCase(), cls: (typeof el.className==="string"?el.className:"").split(" ").slice(0,2).join("."), aria: el.getAttribute("aria-label"), role: el.getAttribute("role"), tabindex: el.getAttribute("tabindex"), disabled: el.disabled===true, cursor: s.cursor, text: (el.textContent||"").replace(/\\s+/g," ").trim().slice(0,28) }; }',
  '};'
].join('\n');

const browser = await chromium.launch({ executablePath: CHROME, args: ['--no-sandbox', '--disable-setuid-sandbox'] });

// ==== 1. 登录锁定流程 ====
try {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', colorScheme: 'light' });
  const page = await ctx.newPage();
  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => { localStorage.clear(); });
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.evaluate(H);
  const attempts = [];
  for (let i = 1; i <= 5; i++) {
    await page.fill('input[placeholder="请输入用户名"]', USER);
    await page.fill('input[placeholder="请输入密码"]', 'WrongPassword' + i + '!');
    await page.click('button:has-text("登")');
    await page.waitForTimeout(2300);
    await page.evaluate(H);
    const f = await page.evaluate(() => {
      const M = window.__m;
      const alerts = [...document.querySelectorAll('.el-alert')];
      const btn = M.q('.login-box button.el-button--primary');
      return {
        alertTexts: alerts.map(a => M.txt(a)),
        alertTypes: alerts.map(a => a.className.split(' ').filter(c => c.indexOf('el-alert--') === 0).join(',')),
        alertBoxes: alerts.map(a => M.box(a)),
        loginBtnText: M.txt(btn), loginBtnDisabled: btn ? btn.disabled : null,
        loginBtnClass: btn ? btn.className : null, loginBtnBox: btn ? M.box(btn) : null,
        inputsDisabled: [...document.querySelectorAll('.login-box .el-input__inner')].map(x => x.disabled),
        inputValueKept: [...document.querySelectorAll('.login-box .el-input__inner')].map(x => x.value),
        lockout: localStorage.getItem('login_lockout'), attempts: localStorage.getItem('login_attempts')
      };
    });
    f.round = i; attempts.push(f);
  }
  rec('login_lockout_sequence', attempts);
  await shot(page, 'login-locked-light-1440');
  await page.keyboard.press('Enter');
  await page.waitForTimeout(1500);
  rec('login_locked_enter', { url: page.url(), stillLogin: page.url().indexOf('/login') >= 0 });
  await ctx.close();
} catch (e) { facts.notes.push('lockout: ' + String(e).slice(0, 300)); save(); }

// ==== 2. NetworkBanner 离线横幅 ====
try {
  const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', colorScheme: 'dark' });
  const page = await ctx.newPage();
  await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
  await page.evaluate(() => { localStorage.clear(); localStorage.setItem('theme', 'dark'); });
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]', USER);
  await page.fill('input[placeholder="请输入密码"]', PASS);
  await page.click('button:has-text("登")');
  await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 });
  await page.waitForTimeout(2500);
  await page.evaluate(H);
  rec('banner_before_offline', await page.evaluate(() => ({
    bannerCount: document.querySelectorAll('.network-banner').length,
    wsText: window.__m.txt(window.__m.q('.ws-status')),
    wsClass: window.__m.q('.ws-status') ? window.__m.q('.ws-status').className : null
  })));
  await ctx.setOffline(true);
  await page.evaluate(() => window.dispatchEvent(new Event('offline')));
  await page.waitForTimeout(1500);
  await page.evaluate(H);
  rec('banner_offline', await page.evaluate(() => {
    const M = window.__m, b = M.q('.network-banner');
    return {
      bannerCount: document.querySelectorAll('.network-banner').length,
      bannerText: M.txt(b), bannerClass: b ? b.className : null, bannerCol: M.col(b), bannerBox: M.box(b),
      iconClass: b ? (b.querySelector('.el-icon') ? b.querySelector('.el-icon').className : null) : null,
      retryText: b ? M.txt(b.querySelector('button')) : null,
      retryBox: b ? M.box(b.querySelector('button')) : null, navOnline: navigator.onLine
    };
  }));
  await shot(page, 'network-banner-offline-dark-1440');
  await ctx.setOffline(false);
  await page.evaluate(() => window.dispatchEvent(new Event('online')));
  await page.waitForTimeout(1200);
  rec('banner_after_online', await page.evaluate(() => ({ bannerCount: document.querySelectorAll('.network-banner').length })));
  await ctx.close();
} catch (e) { facts.notes.push('banner: ' + String(e).slice(0, 300)); save(); }

// ==== 3. 登录页 caret / 链接 / 必填星号 双主题 ====
for (const theme of ['light', 'dark']) {
  try {
    const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', colorScheme: theme });
    const page = await ctx.newPage();
    await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
    await page.evaluate(t => { localStorage.clear(); localStorage.setItem('theme', t); }, theme);
    await page.reload({ waitUntil: 'domcontentloaded' });
    await page.waitForSelector('input[placeholder="请输入用户名"]');
    await page.evaluate(H);
    rec('login_colors_' + theme, await page.evaluate(() => {
      const M = window.__m;
      const inp = [...document.querySelectorAll('.login-box .el-input__inner')];
      const link = M.q('.el-link');
      return {
        inputCols: inp.map(i => M.col(i)),
        wrapperCols: inp.map(i => M.col(i.closest('.el-input__wrapper'))),
        placeholderCol: inp.length ? getComputedStyle(inp[0], '::placeholder').color : null,
        linkCol: link ? M.col(link) : null, linkText: M.txt(link),
        checkboxLabelCol: M.col(M.q('.el-checkbox__label')),
        brandDescCol: M.col(M.q('.brand-desc')), versionCol: M.col(M.q('.version')),
        labelCol: M.col(M.q('.el-form-item__label')), labelText: M.txt(M.q('.el-form-item__label')),
        boxBg: M.col(M.q('.login-box')),
        primaryBtnCol: M.col(M.q('.login-box .el-button--primary')),
        pageBg: M.col(M.q('.login-container'))
      };
    }));
    await shot(page, 'login-colors-' + theme + '-1440');
    await ctx.close();
  } catch (e) { facts.notes.push('colors_' + theme + ': ' + String(e).slice(0, 200)); save(); }
}

// ==== 4. 通知 popover 亮色 + Profile 亮/暗 ====
const ctx = await browser.newContext({ viewport: { width: 1440, height: 900 }, locale: 'zh-CN', colorScheme: 'light' });
const page = await ctx.newPage();
await page.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
await page.evaluate(() => { localStorage.clear(); localStorage.setItem('theme', 'light'); });
await page.reload({ waitUntil: 'domcontentloaded' });
await page.waitForSelector('input[placeholder="请输入用户名"]');
await page.fill('input[placeholder="请输入用户名"]', USER);
await page.fill('input[placeholder="请输入密码"]', PASS);
await page.click('button:has-text("登")');
await page.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 });
await page.waitForTimeout(2500);
await page.goto(BASE + '/dashboard', { waitUntil: 'domcontentloaded' });
await page.waitForTimeout(1500);
await page.evaluate(H);
await page.click('.main-header button[aria-label="打开通知中心"]');
await page.waitForTimeout(800);
await page.evaluate(H);
rec('notification_popover_light', await page.evaluate(() => {
  const M = window.__m, pop = M.q('.notification-popover');
  return { exists: !!pop, col: M.col(pop), box: M.box(pop), emptyCol: M.col(M.q('.notification-empty')), headerCol: M.col(M.q('.notification-header')), parent: pop ? pop.parentElement.tagName : null };
}));
await shot(page, 'notification-popover-light-1440');
await page.keyboard.press('Escape');
await page.waitForTimeout(500);

await page.goto(BASE + '/profile', { waitUntil: 'domcontentloaded' });
await page.waitForTimeout(1500);
await page.evaluate(H);
rec('profile_light_1440', await page.evaluate(() => {
  const M = window.__m;
  return {
    cardCol: M.col(M.q('.el-card')), pageBg: M.col(M.q('.profile-page')),
    inputCols: [...document.querySelectorAll('.profile-page .el-input__inner')].map(i => M.col(i)),
    labelCols: [...document.querySelectorAll('.profile-page .el-form-item__label')].map(l => M.col(l)),
    formTipCol: M.col(M.q('.form-tip')), formTipText: M.txt(M.q('.form-tip')),
    usernameCol: M.col(M.q('.username')), tagCol: M.col(M.q('.el-tag')), emailCol: M.col(M.q('.email')),
    submitBox: M.box(M.q('.profile-page button.el-button--primary')),
    pageHeaderBox: M.box(M.q('.page-header')), firstCardBox: M.box(M.q('.el-card')),
    pageHeaderCol: M.col(M.q('.page-header')),
    deOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
    formItemLabels: [...document.querySelectorAll('.profile-page .el-form-item__label')].map(l => M.txt(l)),
    hasUsernameField: !!M.q('.profile-page input[autocomplete="username"]')
  };
}));
await shot(page, 'profile-light-1440');
await page.evaluate(() => localStorage.setItem('theme', 'dark'));
await page.reload({ waitUntil: 'domcontentloaded' });
await page.waitForTimeout(1500);
await page.evaluate(H);
rec('profile_dark_colors_1440', await page.evaluate(() => {
  const M = window.__m;
  return {
    cardCol: M.col(M.q('.el-card')), pageBg: M.col(M.q('.profile-page')), formTipCol: M.col(M.q('.form-tip')),
    usernameCol: M.col(M.q('.username')), emailCol: M.col(M.q('.email')), tagCol: M.col(M.q('.el-tag')),
    avatarCol: M.col(M.q('.el-avatar')), labelCols: [...document.querySelectorAll('.profile-page .el-form-item__label')].map(l => M.col(l)),
    inputCols: [...document.querySelectorAll('.profile-page .el-input__inner')].map(i => M.col(i)),
    usernameText: M.txt(M.q('.username')), emailText: M.txt(M.q('.email')), tipText: M.txt(M.q('.form-tip'))
  };
}));
await shot(page, 'profile-dark-colors-1440');
console.log('part1 rec keys: ' + Object.keys(facts.measures).join(', '));

// ==== 5. 骨架键盘可达性全表 ====
try {
  await page.evaluate(() => localStorage.setItem('theme', 'light'));
  await page.goto(BASE + '/dashboard', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.sidebar', { timeout: 20000 });
  await page.waitForTimeout(1500);
  await page.evaluate(H);

  rec('skeleton_pointer_scan', await page.evaluate(() => {
    const M = window.__m;
    const out = [];
    for (const el of document.querySelectorAll('.main-header *, .sidebar *, .mobile-sidebar-drawer *')) {
      const s = getComputedStyle(el);
      if (s.cursor !== 'pointer') continue;
      out.push({ ...M.lab(el), cursor: s.cursor, tabIndex: el.tabIndex, native: ['BUTTON','A','INPUT','SELECT','TEXTAREA'].includes(el.tagName) });
    }
    return out;
  }));

  await page.evaluate(() => { if (document.activeElement) document.activeElement.blur(); });
  const order = [];
  for (let i = 0; i < 18; i++) {
    await page.keyboard.press('Tab');
    order.push(await page.evaluate(() => window.__m.lab(document.activeElement)));
  }
  rec('skeleton_tab_order', order);

  // 折叠按钮三路对照：click / Enter / Space
  const b0 = await page.evaluate(() => window.__m.box(document.querySelector('.sidebar')));
  await page.click('.collapse-btn'); await page.waitForTimeout(600);
  const bClick = await page.evaluate(() => window.__m.box(document.querySelector('.sidebar')));
  await page.click('.collapse-btn'); await page.waitForTimeout(600);
  const bRestored = await page.evaluate(() => window.__m.box(document.querySelector('.sidebar')));
  await page.focus('.collapse-btn');
  await page.keyboard.press('Enter'); await page.waitForTimeout(600);
  const bEnter = await page.evaluate(() => window.__m.box(document.querySelector('.sidebar')));
  await page.keyboard.press('Space'); await page.waitForTimeout(600);
  const bSpace = await page.evaluate(() => window.__m.box(document.querySelector('.sidebar')));
  rec('kbd_collapse_tri', { before: b0, afterClick: bClick, restored: bRestored, afterEnter: bEnter, afterSpace: bSpace });

  // logo-area DIV 键盘
  rec('kbd_logo_area', await page.evaluate(() => {
    const el = document.querySelector('.logo-area');
    el.focus();
    return { tabIndex: el.tabIndex, isActive: document.activeElement === el, tag: el.tagName, role: el.getAttribute('role'), tabindexAttr: el.getAttribute('tabindex'), cursor: getComputedStyle(el).cursor };
  }));
  await page.keyboard.press('Enter'); await page.waitForTimeout(1500);
  rec('kbd_logo_area_enter_url', { url: page.url() });

  // 通知项 DIV 键盘
  await page.goto(BASE + '/dashboard', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.sidebar'); await page.waitForTimeout(1500);
  await page.evaluate(H);
  await page.click('.main-header button[aria-label="打开通知中心"]');
  await page.waitForTimeout(800);
  await page.evaluate(H);
  rec('kbd_notification_item', await page.evaluate(() => {
    const M = window.__m;
    const items = [...document.querySelectorAll('.notification-item')];
    return { count: items.length, items: items.slice(0, 3).map(i => ({ ...M.lab(i), tabIndex: i.tabIndex, cursor: getComputedStyle(i).cursor, box: M.box(i) })) };
  }));
  await page.keyboard.press('Escape'); await page.waitForTimeout(500);

  // 用户菜单 Enter 展开
  await page.evaluate(() => document.querySelector('.user-menu').focus());
  await page.keyboard.press('Enter'); await page.waitForTimeout(900);
  rec('kbd_usermenu_enter', await page.evaluate(() => {
    const pops = [...document.querySelectorAll('.el-dropdown__popper')];
    const vis = pops.filter(x => x.getBoundingClientRect().height > 0);
    return { visibleCount: vis.length, items: vis.length ? [...vis[0].querySelectorAll('.el-dropdown-menu__item')].map(i => window.__m.txt(i)) : [] };
  }));
  await page.keyboard.press('Escape'); await page.waitForTimeout(500);

  // 搜索 Enter
  await page.click('.global-search input');
  await page.keyboard.type('固件');
  await page.keyboard.press('Enter'); await page.waitForTimeout(1600);
  rec('kbd_search_enter', { url: page.url() });

  // Ctrl+K
  await page.goto(BASE + '/dashboard', { waitUntil: 'domcontentloaded' });
  await page.waitForSelector('.sidebar'); await page.waitForTimeout(1500);
  await page.keyboard.press('Control+k'); await page.waitForTimeout(600);
  rec('kbd_ctrl_k', await page.evaluate(() => ({
    activeCls: document.activeElement ? document.activeElement.className : null,
    isSearch: document.activeElement === document.querySelector('.global-search input'),
    activeTag: document.activeElement ? document.activeElement.tagName : null
  })));

  // 焦点可见性：Tab 到折叠按钮后 outline
  rec('kbd_focus_visible', await page.evaluate(() => {
    const el = document.querySelector('.collapse-btn');
    el.focus();
    const s = getComputedStyle(el);
    return { outline: s.outline, outlineWidth: s.outlineWidth, outlineStyle: s.outlineStyle, boxShadow: s.boxShadow, cls: el.className };
  }));
} catch (e) { facts.notes.push('kbd: ' + String(e).slice(0, 400)); save(); }

// ==== 6. 360 移动端登录 + Profile ====
try {
  const p2 = await ctx.newPage();
  await p2.setViewportSize({ width: 360, height: 800 });
  await p2.goto(BASE + '/login', { waitUntil: 'domcontentloaded' });
  await p2.evaluate(() => { localStorage.clear(); localStorage.setItem('theme', 'dark'); });
  await p2.reload({ waitUntil: 'domcontentloaded' });
  await p2.waitForSelector('input[placeholder="请输入用户名"]');
  await p2.evaluate(H);
  rec('login_360_dark', await p2.evaluate(() => {
    const M = window.__m;
    return {
      boxBox: M.box(M.q('.login-box')), boxCol: M.col(M.q('.login-box')),
      inputFontSizes: [...document.querySelectorAll('.login-box .el-input__inner')].map(i => getComputedStyle(i).fontSize),
      inputBoxes: [...document.querySelectorAll('.login-box .el-input__inner')].map(i => M.box(i)),
      deOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      bodyH: document.body.scrollHeight, vh: window.innerHeight,
      submitBox: M.box(M.q('.login-box button.el-button--primary')),
      brandDescBox: M.box(M.q('.brand-desc')), brandDescText: M.txt(M.q('.brand-desc')),
      checkboxBox: M.box(M.q('.el-checkbox')), forgotBox: M.box(M.q('.el-link')),
      brandBox: M.box(M.q('.brand'))
    };
  }));
  await shot(p2, 'login-dark-mobile-360b');

  await p2.fill('input[placeholder="请输入用户名"]', USER);
  await p2.fill('input[placeholder="请输入密码"]', PASS);
  await p2.click('button:has-text("登")');
  await p2.waitForURL(u => !u.pathname.includes('/login'), { timeout: 25000 });
  await p2.waitForTimeout(2500);
  await p2.goto(BASE + '/profile', { waitUntil: 'domcontentloaded' });
  await p2.waitForTimeout(1600);
  await p2.evaluate(H);
  rec('profile_360_dark', await p2.evaluate(() => {
    const M = window.__m;
    return {
      cards: [...document.querySelectorAll('.el-card')].map(c => M.box(c)),
      inputBoxes: [...document.querySelectorAll('.profile-page .el-input__inner')].map(i => M.box(i)),
      inputFontSizes: [...document.querySelectorAll('.profile-page .el-input__inner')].map(i => getComputedStyle(i).fontSize),
      buttons: [...document.querySelectorAll('.profile-page button')].map(b => ({ txt: M.txt(b), box: M.box(b) })),
      deOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      headerBox: M.box(M.q('.page-header')),
      scrollH: document.querySelector('.main-content').scrollHeight,
      clientH: document.querySelector('.main-content').clientHeight
    };
  }));
  await shot(p2, 'profile-dark-mobile-360b');
  await p2.close();
} catch (e) { facts.notes.push('m360: ' + String(e).slice(0, 300)); save(); }

await ctx.close();
await browser.close();
save();
console.log('=== 探针2 完成 ===');
console.log('截图 ' + facts.shots.length + ' → ' + OUT);
console.log('keys: ' + Object.keys(facts.measures).join(', '));
console.log('notes: ' + JSON.stringify(facts.notes));
