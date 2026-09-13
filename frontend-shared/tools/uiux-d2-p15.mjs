/**
 * D2 可访问性与对比度取证（响应主控广播）。
 * A) cursor:pointer 元素分母 + tabIndex<0 分子（覆盖 @click div/li）
 * B) 本域可点击非原生容器的 role/tabindex/aria-label（规范 §3.1.3）
 * C) 状态徽标 / 文字 的前景/背景对比度（WCAG AA 4.5:1 正文，3:1 大字）
 */
import { chromium } from '@playwright/test';
import fs from 'node:fs';
const BASE='http://127.0.0.1:8082';
const OUT='/tmp/uiux-d2-probe';
const ROUTES=['/node','/channel','/edge-device','/logical-device','/node/1','/edge-device/7053','/device-configs','/monitor'];

const A11Y = () => {
  const qa = s => [...document.querySelectorAll(s)];
  const desc = e => e.tagName.toLowerCase() + (typeof e.className==='string'&&e.className ? '.'+e.className.trim().split(/\s+/).slice(0,2).join('.') : '');
  const vis = e => { const r=e.getBoundingClientRect(); const c=getComputedStyle(e); return r.width>0&&r.height>0&&c.visibility!=='hidden'&&c.display!=='none'; };
  const poi = qa('body *').filter(e => vis(e) && getComputedStyle(e).cursor === 'pointer');
  const notFocusable = poi.filter(e => e.tabIndex < 0);
  const nativeFocusable = e => /^(a|button|input|select|textarea|summary|area)$/.test(e.tagName.toLowerCase());
  const containerish = notFocusable.filter(e => !nativeFocusable(e));
  return {
    pointerTotal: poi.length,
    pointerNotFocusable: notFocusable.length,
    containerishCount: containerish.length,
    containerish: containerish.slice(0, 40).map(e => {
      const r = e.getBoundingClientRect();
      return { sel: desc(e), text: (e.textContent||'').trim().replace(/\s+/g,' ').slice(0,26),
        w: Math.round(r.width), h: Math.round(r.height), tabindex: e.getAttribute('tabindex'),
        role: e.getAttribute('role'), ariaLabel: e.getAttribute('aria-label') };
    }),
  };
};

const CONTRAST = () => {
  const qa = s => [...document.querySelectorAll(s)];
  const parse = c => { const m = (c||'').match(/rgba?\(([^)]+)\)/); if(!m) return null;
    const p = m[1].split(',').map(x=>parseFloat(x.trim())); return {r:p[0],g:p[1],b:p[2],a:p.length>3?p[3]:1}; };
  const lin = v => { v/=255; return v<=0.03928 ? v/12.92 : Math.pow((v+0.055)/1.055, 2.4); };
  const lum = c => 0.2126*lin(c.r)+0.7152*lin(c.g)+0.0722*lin(c.b);
  const over = (fg,bg) => ({ r: fg.r*fg.a + bg.r*(1-fg.a), g: fg.g*fg.a + bg.g*(1-fg.a), b: fg.b*fg.a + bg.b*(1-fg.a), a:1 });
  const effBg = e => { let n=e, acc=null;
    while (n && n!==document.documentElement) { const c=parse(getComputedStyle(n).backgroundColor);
      if (c && c.a>0) { acc = acc ? over(acc,c) : c; if (acc.a>=0.999) return acc; } n=n.parentElement; }
    return acc || {r:255,g:255,b:255,a:1}; };
  const ratio = (a,b) => { const l1=lum(a), l2=lum(b); const hi=Math.max(l1,l2), lo=Math.min(l1,l2); return (hi+0.05)/(lo+0.05); };
  const sample = sel => qa(sel).filter(e=>{const r=e.getBoundingClientRect(); return r.width>0&&r.height>0;}).slice(0,5).map(e => {
    const cs=getComputedStyle(e); const fg=parse(cs.color);
    const own=parse(cs.backgroundColor);
    const bg = own && own.a>0.999 ? own : over(own&&own.a>0?own:{r:0,g:0,b:0,a:0}, effBg(e.parentElement||e));
    const fs=parseFloat(cs.fontSize); const bold=parseInt(cs.fontWeight,10)>=700;
    const large = fs>=24 || (fs>=18.66 && bold);
    const cr = fg&&bg ? ratio(fg,bg) : null;
    return { sel, text:(e.textContent||'').trim().replace(/\s+/g,' ').slice(0,20), color:cs.color,
      effBg:'rgb('+Math.round(bg.r)+','+Math.round(bg.g)+','+Math.round(bg.b)+')', fontSize:cs.fontSize, bold, large,
      ratio: cr?Math.round(cr*100)/100:null, passAA: cr? cr>=(large?3:4.5):null, needs: large?3:4.5 };
  });
  const sels = { wsStatus:'.ws-status', wsStatusText:'.ws-status .status-text', statusTag:'.status-tag',
    statusIndicator:'.status-indicator', badge:'.badge', metricsOfflineTag:'.metrics-offline-tag',
    elTag:'.el-tag', subtitle:'.page-header-subtitle, .info-row .label, .fact-label, .info-label',
    muted:'.muted, .text-muted, .table-data-empty', emptyDesc:'.empty-description, .el-empty__description',
    mobileHint:'.mobile-table-hint', tableHead:'.el-table__header th .cell', tabItem:'.tab-item' };
  const out = {}; for (const [k,s] of Object.entries(sels)) out[k]=sample(s); return out;
};

const browser = await chromium.launch({ executablePath:'/snap/bin/chromium', args:['--no-sandbox','--disable-setuid-sandbox'] });
const report = {};
for (const theme of ['light','dark']) {
  const ctx = await browser.newContext({ viewport:{width:1440,height:900}, locale:'zh-CN', colorScheme: theme==='dark'?'dark':'light' });
  const page = await ctx.newPage();
  await page.goto(BASE+'/login',{waitUntil:'domcontentloaded'});
  await page.evaluate(t=>{localStorage.setItem('theme',t);document.documentElement.setAttribute('data-theme',t);document.documentElement.classList.toggle('dark',t==='dark');},theme);
  await page.waitForSelector('input[placeholder="请输入用户名"]');
  await page.fill('input[placeholder="请输入用户名"]','admin'); await page.fill('input[placeholder="请输入密码"]','UiuxAudit2026!');
  await page.click('button:has-text("登")'); await page.waitForURL(u=>!u.pathname.includes('/login'),{timeout:25000}).catch(()=>{}); await page.waitForTimeout(2000);
  report[theme] = {};
  for (const rt of ROUTES) {
    await page.goto(BASE+rt,{waitUntil:'domcontentloaded'}); await page.waitForTimeout(rt.startsWith('/edge-device/')?4800:2400);
    report[theme][rt] = { a11y: await page.evaluate(A11Y), contrast: await page.evaluate(CONTRAST) };
    const f=OUT+'/desktop-1440-'+theme+'-p15'+rt.replace(/\//g,'_')+'.png'; await page.screenshot({path:f});
  }
  await ctx.close();
}
await browser.close();
fs.writeFileSync(OUT+'/p15-a11y-contrast.json', JSON.stringify(report,null,2));
console.log('done');
