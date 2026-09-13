/**
 * D2/D6/D7 补充取证探针 #3：侧栏渐变 token、404/403 小屏、移动抽屉焦点与配色。
 * 只读脚本，不改 src/。产出 <OUT>/d4-facts3.json
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

const BASE = 'http://127.0.0.1:8082';
const OUT = process.env.UIUX_OUT || '/tmp/uiux-d4';
const USER = 'admin', PASS = 'UiuxAudit2026!';
const facts = { shots: [], measures: {}, notes: [] };
const save = () => fs.writeFileSync(path.join(OUT, 'd4-facts3.json'), JSON.stringify(facts, null, 2));
const rec = (k, v) => { facts.measures[k] = v; save(); };
const shot = async (pg, n) => { await pg.screenshot({ path: path.join(OUT, n + '.png') }); facts.shots.push(n + '.png'); save(); };

const H = [
  'window.__m = {',
  '  box:(el)=>{if(!el)return null;const r=el.getBoundingClientRect();return{w:Math.round(r.width),h:Math.round(r.height),x:Math.round(r.x),y:Math.round(r.y),right:Math.round(r.right),bottom:Math.round(r.bottom)};},',
  '  q:(s)=>document.querySelector(s),',
  '  txt:(el)=>el?(el.textContent||"").replace(/\\s+/g," ").trim().slice(0,150):null,',
  '  bg:(el)=>{if(!el)return null;const s=getComputedStyle(el);return{bgImage:s.backgroundImage,bgColor:s.backgroundColor,color:s.color};}',
  '};'
].join('\n');

const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox','--disable-setuid-sandbox'] });

for (const theme of ['light','dark']) {
  try {
    const ctx = await browser.newContext({ viewport:{width:1440,height:900}, locale:'zh-CN', colorScheme:theme });
    const page = await ctx.newPage();
    await page.goto(BASE + '/login', { waitUntil:'domcontentloaded' });
    await page.evaluate(t => { localStorage.clear(); localStorage.setItem('theme', t); }, theme);
    await page.reload({ waitUntil:'domcontentloaded' });
    await page.waitForSelector('input[placeholder="请输入用户名"]');
    await page.fill('input[placeholder="请输入用户名"]', USER);
    await page.fill('input[placeholder="请输入密码"]', PASS);
    await page.click('button:has-text("登")');
    await page.waitForURL(u => !u.pathname.includes('/login'), { timeout:25000 });
    await page.waitForTimeout(2500);
    await page.goto(BASE + '/dashboard', { waitUntil:'domcontentloaded' });
    await page.waitForSelector('.sidebar'); await page.waitForTimeout(1500);
    await page.evaluate(H);
    rec('sidebar_bg_' + theme, await page.evaluate(() => ({
      sidebar: window.__m.bg(window.__m.q('.sidebar')),
      menuItem: window.__m.bg(window.__m.q('.sidebar .el-menu-item')),
      activeItem: window.__m.bg(window.__m.q('.sidebar .el-menu-item.is-active')),
      logoText: window.__m.bg(window.__m.q('.logo-text')),
      versionInfo: window.__m.bg(window.__m.q('.version-info')),
      mainContent: window.__m.bg(window.__m.q('.main-content')),
      header: window.__m.bg(window.__m.q('.main-header')),
      tokenSidebarBg: getComputedStyle(document.documentElement).getPropertyValue('--sidebar-bg').trim(),
      tokenSidebarGradient: getComputedStyle(document.documentElement).getPropertyValue('--sidebar-bg-gradient').trim().slice(0,80),
      tokenSidebarText: getComputedStyle(document.documentElement).getPropertyValue('--sidebar-text').trim()
    })));
    await shot(page, 'sidebar-' + theme + '-1440');
    await ctx.close();
  } catch(e) { facts.notes.push('sidebar_' + theme + ': ' + String(e).slice(0,250)); save(); }
}

try {
  const ctx = await browser.newContext({ viewport:{width:360,height:800}, locale:'zh-CN', colorScheme:'dark' });
  const page = await ctx.newPage();
  await page.goto(BASE + '/login', { waitUntil:'domcontentloaded' });
  await page.evaluate(() => localStorage.setItem('theme','dark'));
  for (const pair of [['nf360','/nonexistent-xyz'],['fb360','/403']]) {
    await page.goto(BASE + pair[1], { waitUntil:'domcontentloaded' });
    await page.waitForTimeout(1300);
    await page.evaluate(H);
    rec(pair[0], await page.evaluate(() => ({
      code: window.__m.txt(window.__m.q('.error-code')), title: window.__m.txt(window.__m.q('.error-title')),
      desc: window.__m.txt(window.__m.q('.error-desc')),
      codeBox: window.__m.box(window.__m.q('.error-code')), codeFs: getComputedStyle(window.__m.q('.error-code')).fontSize,
      brandBox: window.__m.box(window.__m.q('.error-brand')),
      contentBox: window.__m.box(window.__m.q('.error-content')),
      btns: [...document.querySelectorAll('.error-actions button')].map(b=>({t:window.__m.txt(b),box:window.__m.box(b)})),
      deOverflow: document.documentElement.scrollWidth - document.documentElement.clientWidth,
      docH: document.documentElement.scrollHeight, vh: window.innerHeight
    })));
    await shot(page, pair[0] + '-dark');
  }
  await ctx.close();
} catch(e) { facts.notes.push('err360: ' + String(e).slice(0,300)); save(); }

try {
  const ctx = await browser.newContext({ viewport:{width:390,height:844}, locale:'zh-CN', colorScheme:'dark' });
  const page = await ctx.newPage();
  await page.goto(BASE + '/login', { waitUntil:'domcontentloaded' });
  await page.evaluate(() => { localStorage.clear(); localStorage.setItem('theme','dark'); });
  await page.reload({ waitUntil:'domcontentloaded' });
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]', USER);
  await page.fill('input[placeholder="请输入密码"]', PASS);
  await page.click('button:has-text("登")');
  await page.waitForURL(u => !u.pathname.includes('/login'), { timeout:25000 });
  await page.waitForTimeout(2500);
  await page.goto(BASE + '/dashboard', { waitUntil:'domcontentloaded' });
  await page.waitForSelector('.main-header'); await page.waitForTimeout(1500);
  await page.evaluate(H);
  rec('drawer_before', await page.evaluate(() => ({ drawerCount: document.querySelectorAll('.el-drawer').length, overlayCount: document.querySelectorAll('.el-overlay').length })));
  await page.click('.main-header button[aria-label="打开导航菜单"]');
  await page.waitForTimeout(900);
  await page.evaluate(H);
  rec('drawer_open', await page.evaluate(() => {
    const M = window.__m; const d = M.q('.el-drawer');
    const its = [...document.querySelectorAll('.mobile-sidebar-menu .el-menu-item')];
    return {
      drawerBox: M.box(d), drawerBg: M.bg(d), drawerBodyBg: M.bg(M.q('.el-drawer__body')),
      itemBg: M.bg(M.q('.mobile-sidebar-menu .el-menu-item')),
      activeItemBg: M.bg(M.q('.mobile-sidebar-menu .el-menu-item.is-active')),
      overlayBg: M.bg(M.q('.el-overlay')),
      logoTextBg: M.bg(M.q('.mobile-logo-text')), versionBg: M.bg(M.q('.mobile-version-info')),
      versionText: M.txt(M.q('.mobile-version-info')),
      lastItemBox: its.length ? M.box(its[its.length-1]) : null,
      footerBox: M.box(M.q('.mobile-sidebar-footer')),
      itemCount: its.length,
      menuScrollH: M.q('.mobile-sidebar-menu').scrollHeight, menuClientH: M.q('.mobile-sidebar-menu').clientHeight
    };
  }));
  await shot(page, 'drawer-390-dark-verify');
  await page.keyboard.press('Escape');
  await page.waitForTimeout(900);
  rec('drawer_after_escape', await page.evaluate(() => {
    const d = document.querySelector('.el-drawer');
    return { drawerStillInDom: !!d, drawerVisible: d ? d.getBoundingClientRect().width > 0 : false,
             activeEl: document.activeElement ? document.activeElement.tagName + '.' + String(document.activeElement.className).slice(0,40) : null };
  }));
  await page.click('.main-header button[aria-label="打开导航菜单"]');
  await page.waitForTimeout(900);
  const tabs = [];
  for (let i=0;i<6;i++){
    await page.keyboard.press('Tab');
    tabs.push(await page.evaluate(() => {
      const a = document.activeElement; const d = document.querySelector('.el-drawer');
      return { tag:a.tagName, cls:String(a.className).slice(0,40), txt:(a.textContent||'').replace(/\s+/g,' ').trim().slice(0,16), insideDrawer: d ? d.contains(a) : null };
    }));
  }
  rec('drawer_focus_trap', tabs);
  await ctx.close();
} catch(e) { facts.notes.push('drawer: ' + String(e).slice(0,300)); save(); }

await browser.close(); save();
console.log('=== 探针3 完成 ===');
console.log('keys: ' + Object.keys(facts.measures).join(', '));
console.log('notes: ' + JSON.stringify(facts.notes));
