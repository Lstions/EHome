/**
 * 补充取证 #4：WS-only 断线横幅（CDP 阻断 WS 后重载）、aria 语义、恢复路径。
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
import path from 'node:path';

const BASE = 'http://127.0.0.1:8082';
const OUT = process.env.UIUX_OUT || '/tmp/uiux-d4';
const USER = 'admin', PASS = 'UiuxAudit2026!';
const facts = { shots: [], measures: {}, notes: [] };
const save = () => fs.writeFileSync(path.join(OUT, 'd4-facts4.json'), JSON.stringify(facts, null, 2));
const rec = (k, v) => { facts.measures[k] = v; save(); };
const shot = async (pg, n) => { await pg.screenshot({ path: path.join(OUT, n + '.png') }); facts.shots.push(n + '.png'); save(); };

const browser = await chromium.launch({ executablePath: '/snap/bin/chromium', args: ['--no-sandbox','--disable-setuid-sandbox'] });

// ==== 1. WS 断线（浏览器仍在线）→ NetworkBanner 是否出现 ====
try {
  const ctx = await browser.newContext({ viewport:{width:1440,height:900}, locale:'zh-CN', colorScheme:'dark' });
  const page = await ctx.newPage();
  const cdp = await ctx.newCDPSession(page);
  await page.goto(BASE + '/login', { waitUntil:'domcontentloaded' });
  await page.evaluate(() => { localStorage.clear(); localStorage.setItem('theme','dark'); });
  await page.reload({ waitUntil:'domcontentloaded' });
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]', USER);
  await page.fill('input[placeholder="请输入密码"]', PASS);
  await page.click('button:has-text("登")');
  await page.waitForURL(u => !u.pathname.includes('/login'), { timeout:25000 });
  await page.waitForTimeout(2500);
  rec('ws_connected_state', await page.evaluate(() => {
    const ws = document.querySelector('.ws-status');
    return { text: ws ? ws.textContent.replace(/\s+/g,' ').trim() : null, cls: ws ? ws.className : null, banner: document.querySelectorAll('.network-banner').length };
  }));

  // 阻断 WS 后重载：不触发 offline 事件，只有 wsStore.connected=false
  await cdp.send('Network.enable');
  await cdp.send('Network.setBlockedURLs', { urls: ['*/api/v1/ws*', 'wss://*', 'ws://*'] });
  await page.goto(BASE + '/dashboard', { waitUntil:'domcontentloaded' });
  await page.waitForTimeout(1500);
  rec('ws_blocked_immediate', await page.evaluate(() => {
    const ws = document.querySelector('.ws-status');
    return { navOnline: navigator.onLine, wsText: ws ? ws.textContent.replace(/\s+/g,' ').trim() : null, wsCls: ws ? ws.className : null, banner: document.querySelectorAll('.network-banner').length };
  }));
  // 定时器 10s 检查
  await page.waitForTimeout(13000);
  rec('ws_blocked_after_13s', await page.evaluate(() => {
    const ws = document.querySelector('.ws-status');
    const b = document.querySelector('.network-banner');
    return {
      navOnline: navigator.onLine,
      wsText: ws ? ws.textContent.replace(/\s+/g,' ').trim() : null,
      bannerCount: document.querySelectorAll('.network-banner').length,
      bannerText: b ? b.textContent.replace(/\s+/g,' ').trim() : null,
      bannerClass: b ? b.className : null,
      bannerBg: b ? getComputedStyle(b).backgroundImage : null,
      iconCls: b ? (b.querySelector('.el-icon') ? b.querySelector('.el-icon').className : null) : null,
      retryText: b ? (b.querySelector('button') ? b.querySelector('button').textContent.trim() : null) : null
    };
  }));
  await shot(page, 'network-banner-ws-down-dark-1440');
  await ctx.close();
} catch(e) { facts.notes.push('wsbanner: ' + String(e).slice(0,400)); save(); }

// ==== 2. aria 语义检查（登录 + 错误页 + 骨架） ====
try {
  const ctx = await browser.newContext({ viewport:{width:1440,height:900}, locale:'zh-CN', colorScheme:'light' });
  const page = await ctx.newPage();
  await page.goto(BASE + '/login', { waitUntil:'domcontentloaded' });
  await page.evaluate(() => localStorage.clear());
  await page.reload({ waitUntil:'domcontentloaded' });
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  rec('login_aria', await page.evaluate(() => {
    const out = { liveRegions: [], alerts: [], landmarks: [], headings: [] };
    document.querySelectorAll('[aria-live]').forEach(e => out.liveRegions.push({ cls: e.className, live: e.getAttribute('aria-live') }));
    document.querySelectorAll('[role="alert"]').forEach(e => out.alerts.push(e.className));
    document.querySelectorAll('main, nav, header, aside, [role="main"], [role="navigation"], [role="banner"]').forEach(e => out.landmarks.push(e.tagName + '.' + String(e.className).slice(0,30) + '[' + (e.getAttribute('role')||'') + ']'));
    document.querySelectorAll('h1,h2,h3').forEach(e => out.headings.push(e.tagName + ':' + (e.textContent||'').trim().slice(0,20)));
    return out;
  }));
  // 故意触发登录失败，看 alert 是否 role=alert
  await page.fill('input[placeholder="请输入用户名"]', 'admin');
  await page.fill('input[placeholder="请输入密码"]', 'DefinitelyWrong!99');
  await page.click('button:has-text("登")');
  await page.waitForTimeout(2500);
  rec('login_error_aria', await page.evaluate(() => {
    const a = document.querySelector('.el-alert');
    return {
      alertCount: document.querySelectorAll('.el-alert').length,
      alertText: a ? a.textContent.replace(/\s+/g,' ').trim() : null,
      role: a ? a.getAttribute('role') : null,
      ariaLive: a ? a.getAttribute('aria-live') : null,
      ariaAtomic: a ? a.getAttribute('aria-atomic') : null,
      cls: a ? a.className : null
    };
  }));
  await page.evaluate(() => { localStorage.clear(); });
  await page.reload({ waitUntil:'domcontentloaded' });
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]', USER);
  await page.fill('input[placeholder="请输入密码"]', PASS);
  await page.click('button:has-text("登")');
  await page.waitForURL(u => !u.pathname.includes('/login'), { timeout:25000 });
  await page.waitForTimeout(2500);
  await page.goto(BASE + '/403', { waitUntil:'domcontentloaded' });
  await page.waitForTimeout(1300);
  rec('forbidden_aria', await page.evaluate(() => ({
    landmarks: [...document.querySelectorAll('main, nav, header, aside, [role]')].map(e => e.tagName + '[' + (e.getAttribute('role')||'') + ']'),
    headings: [...document.querySelectorAll('h1,h2,h3')].map(e => e.tagName + ':' + (e.textContent||'').trim().slice(0,24)),
    title: document.title,
    tagText: (document.querySelector('.el-tag')||{}).textContent,
    bodyText: document.body.innerText.replace(/\s+/g,' ').trim().slice(0,220)
  })));
  // 404 无历史时返回上页
  const p2 = await ctx.newPage();
  await p2.goto(BASE + '/definitely-not-a-route-abc', { waitUntil:'domcontentloaded' });
  await p2.waitForTimeout(1300);
  await p2.click('.error-actions button:has-text("返回上页")');
  await p2.waitForTimeout(1500);
  rec('notfound_back_no_history', { url: p2.url() });
  await p2.close();
  await ctx.close();
} catch(e) { facts.notes.push('aria: ' + String(e).slice(0,400)); save(); }

await browser.close(); save();
console.log('=== 探针4 完成 ===');
console.log('keys: ' + Object.keys(facts.measures).join(', '));
console.log('notes: ' + JSON.stringify(facts.notes));
